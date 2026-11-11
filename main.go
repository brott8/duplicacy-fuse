package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	duplicacy "github.com/gilbertchen/duplicacy/src"
	"github.com/winfsp/cgofuse/fuse"
	"gitlab.com/andrewheberle/duplicacy-fuse/dpfs"
)

const defaultChunkCacheBytes = int64(10 * 1024 * 1024 * 1024)

type cliOptions struct {
	repository   string
	storage      string
	snapshot     string
	password     string
	path         string
	revision     int
	mountpoint   string
	cacheDir     string
	cacheBytes   int64
	duplicacy    string
	json         bool
	directory    bool
	debug        bool
	background   bool
	guiControlFD int
}

type fileVersion struct {
	Revision int    `json:"revision"`
	Size     int64  `json:"size"`
	Time     string `json:"time"`
	Hash     string `json:"hash,omitempty"`
	Path     string `json:"path"`
	Raw      string `json:"raw"`
}

type snapshotRevision struct {
	SnapshotID string `json:"snapshot_id"`
	Revision   int    `json:"revision"`
	CreatedAt  string `json:"created_at"`
}

var historyLine = regexp.MustCompile(`^([0-9]+):\s*(.*)$`)
var snapshotListLine = regexp.MustCompile(`^Snapshot\s+(.+)\s+revision\s+([0-9]+)\s+created at\s+(.+)$`)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: duplicacy-fuse <mount|history|revisions|clear-cache|gui-storages|gui-snapshots|gui-revisions> [options]")
	}
	var err error
	switch args[0] {
	case "mount":
		err = runMount(args[1:], stderr)
	case "history":
		err = runHistory(ctx, args[1:], stdout, stderr)
	case "revisions":
		err = runRevisions(ctx, args[1:], stdout, stderr)
	case "clear-cache":
		err = runClearCache(args[1:], stderr)
	case "gui-storages":
		err = runGUIStorages(args[1:], stdout, stderr)
	case "gui-snapshots":
		err = runGUISnapshots(args[1:], stdout, stderr)
	case "gui-revisions":
		err = runGUIRevisions(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func addRepositoryFlags(fs *flag.FlagSet, options *cliOptions) {
	fs.StringVar(&options.repository, "repo", "", "repository directory (required)")
	fs.StringVar(&options.storage, "storage", "", "configured storage name; required when multiple storages exist")
	fs.StringVar(&options.snapshot, "snapshot", "", "snapshot ID (defaults to the selected storage's snapshot ID)")
	fs.StringVar(&options.password, "password", "", "storage password; prefer Duplicacy's password environment variable")
}

func runMount(args []string, stderr io.Writer) error {
	options := cliOptions{revision: -1, cacheBytes: defaultChunkCacheBytes}
	fs := newFlagSet("mount", stderr)
	addRepositoryFlags(fs, &options)
	fs.StringVar(&options.path, "path", "", "repository-relative file or directory to expose")
	fs.IntVar(&options.revision, "revision", -1, "snapshot revision (required)")
	fs.StringVar(&options.mountpoint, "mountpoint", "", "FUSE mountpoint (required)")
	fs.StringVar(&options.cacheDir, "cache-dir", "", "persistent chunk cache directory")
	fs.BoolVar(&options.debug, "debug", false, "log mount timings, directory scans, chunk reads, and cache activity")
	fs.BoolVar(&options.background, "background", false, "return after the FUSE mount is ready")
	fs.IntVar(&options.guiControlFD, "gui-control-fd", 0, "private desktop IPC socket descriptor (internal)")
	cacheSize := fs.String("cache-size", "10GiB", "maximum persistent chunk cache size")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}
	if options.repository == "" || options.mountpoint == "" || options.revision < 1 {
		return errors.New("mount requires --repo, --mountpoint, and a positive --revision")
	}
	var guiControl *guiMountControl
	if options.guiControlFD != 0 {
		if options.guiControlFD < 3 || options.background || options.password != "" {
			return errors.New("invalid GUI mount helper options")
		}
		var err error
		guiControl, err = newGUIMountControl(options.guiControlFD)
		if err != nil {
			return err
		}
		defer guiControl.Close()
		if err := validateGUIMountpoint(options.mountpoint); err != nil {
			_ = guiControl.send(guiIPCMessage{Type: "failed", Message: err.Error()})
			return err
		}
	}
	parsedCacheSize, err := parseByteSize(*cacheSize)
	if err != nil {
		return fmt.Errorf("invalid --cache-size: %w", err)
	}
	options.cacheBytes = parsedCacheSize
	if options.debug || options.background || guiControl != nil {
		duplicacy.SetLoggingLevel(duplicacy.WARN)
		defer duplicacy.SetLoggingLevel(duplicacy.INFO)
	}
	if options.cacheDir == "" {
		options.cacheDir, err = defaultCacheDir()
		if err != nil {
			return err
		}
	}
	if options.background && os.Getenv(backgroundChildEnvironment) != "1" {
		return startBackgroundMount(args, options.mountpoint, stderr)
	}
	onInit, err := backgroundReadyNotifier()
	if err != nil {
		return err
	}
	var passwordPrompt dpfs.PasswordPrompt
	if guiControl != nil {
		passwordPrompt = func(attempt int, previousError error) (string, error) {
			return guiControl.requestPassword(attempt, previousError)
		}
	}
	view, err := dpfs.NewView(dpfs.MountOptions{
		SelectionOptions: dpfs.SelectionOptions{
			Repository:           options.repository,
			Storage:              options.storage,
			SnapshotID:           options.snapshot,
			Password:             options.password,
			IgnoreStoredPassword: guiControl != nil,
		},
		Revision:     options.revision,
		SelectedPath: options.path,
		CacheDir:     options.cacheDir,
		CacheBytes:   options.cacheBytes,
		Debug:        options.debug,
		DebugWriter:  stderr,
		OnReadError: func() {
			if guiControl != nil {
				guiControl.notifyReadError()
			}
		},
		OnInit: func() {
			if onInit != nil {
				onInit()
			}
			if guiControl != nil {
				go guiControl.watchMount(options.mountpoint)
				_ = guiControl.send(guiIPCMessage{Type: "mounted"})
			}
		},
		PasswordPrompt: passwordPrompt,
	})
	if err != nil {
		if guiControl != nil {
			_ = guiControl.send(guiIPCMessage{Type: "failed", Message: err.Error()})
		}
		return err
	}
	host := fuse.NewFileSystemHost(view)
	fuseOptions := "ro,default_permissions"
	if guiControl != nil {
		fuseOptions += ",auto_unmount"
	}
	if !host.Mount(options.mountpoint, []string{"-f", "-o", fuseOptions}) {
		if guiControl != nil {
			if guiControl.isUnmountRequested() {
				_ = guiControl.send(guiIPCMessage{Type: "unmounted"})
				return nil
			}
			_ = guiControl.send(guiIPCMessage{Type: "failed", Message: "FUSE mount failed"})
		}
		return fmt.Errorf("failed to mount %s", options.mountpoint)
	}
	if guiControl != nil {
		_ = guiControl.send(guiIPCMessage{Type: "unmounted"})
	}
	return nil
}

func runGUIStorages(args []string, stdout, stderr io.Writer) error {
	restoreLogger := discardDuplicacyLogs()
	defer restoreLogger()
	fs := newFlagSet("gui-storages", stderr)
	repository := fs.String("repo", "", "repository directory (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *repository == "" {
		return errors.New("gui-storages requires --repo and accepts no positional arguments")
	}
	storages, err := dpfs.ConfiguredStorages(*repository)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(storages)
}

func runGUIRevisions(args []string, _ io.Writer, stderr io.Writer) error {
	fs := newFlagSet("gui-revisions", stderr)
	options := cliOptions{}
	addRepositoryFlags(fs, &options)
	fs.IntVar(&options.guiControlFD, "gui-control-fd", 0, "private desktop IPC socket descriptor (internal)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || options.guiControlFD < 3 || options.repository == "" {
		return errors.New("gui-revisions requires --repo and a private --gui-control-fd")
	}
	if options.password != "" {
		return errors.New("GUI revision discovery does not accept password arguments")
	}
	control, err := newGUIMountControl(options.guiControlFD)
	if err != nil {
		return err
	}
	defer control.Close()
	restoreLogger := discardDuplicacyLogs()
	defer restoreLogger()
	duplicacy.SetLoggingLevel(duplicacy.WARN)
	defer duplicacy.SetLoggingLevel(duplicacy.INFO)
	revisions, err := dpfs.ListRevisions(dpfs.SelectionOptions{
		Repository:           options.repository,
		Storage:              options.storage,
		SnapshotID:           options.snapshot,
		IgnoreStoredPassword: true,
	}, func(attempt int, previousError error) (string, error) {
		return control.requestPassword(attempt, previousError)
	})
	if err != nil {
		_ = control.send(guiIPCMessage{Type: "failed", Message: err.Error()})
		return err
	}
	revisionData, err := json.Marshal(revisions)
	if err != nil {
		return err
	}
	if err := control.send(guiIPCMessage{Type: "revisions-ready", Revisions: revisionData}); err != nil {
		return err
	}
	return nil
}

func runGUISnapshots(args []string, _ io.Writer, stderr io.Writer) error {
	fs := newFlagSet("gui-snapshots", stderr)
	options := cliOptions{}
	addRepositoryFlags(fs, &options)
	fs.IntVar(&options.guiControlFD, "gui-control-fd", 0, "private desktop IPC socket descriptor (internal)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || options.guiControlFD < 3 || options.repository == "" {
		return errors.New("gui-snapshots requires --repo and a private --gui-control-fd")
	}
	if options.password != "" {
		return errors.New("GUI snapshot discovery does not accept password arguments")
	}
	control, err := newGUIMountControl(options.guiControlFD)
	if err != nil {
		return err
	}
	defer control.Close()
	restoreLogger := discardDuplicacyLogs()
	defer restoreLogger()
	duplicacy.SetLoggingLevel(duplicacy.WARN)
	defer duplicacy.SetLoggingLevel(duplicacy.INFO)
	snapshotIDs, err := dpfs.ListSnapshotIDs(dpfs.SelectionOptions{
		Repository:           options.repository,
		Storage:              options.storage,
		IgnoreStoredPassword: true,
	}, func(attempt int, previousError error) (string, error) {
		return control.requestPassword(attempt, previousError)
	})
	if err != nil {
		_ = control.send(guiIPCMessage{Type: "failed", Message: err.Error()})
		return err
	}
	snapshotData, err := json.Marshal(snapshotIDs)
	if err != nil {
		return err
	}
	if err := control.send(guiIPCMessage{Type: "snapshots-ready", SnapshotIDs: snapshotData}); err != nil {
		return err
	}
	return nil
}

func discardDuplicacyLogs() func() {
	previous := duplicacy.LogFunction
	duplicacy.LogFunction = func(int, string, string) {}
	return func() { duplicacy.LogFunction = previous }
}

func runHistory(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	options := cliOptions{}
	fs := newFlagSet("history", stderr)
	addRepositoryFlags(fs, &options)
	fs.StringVar(&options.path, "path", "", "repository-relative file or directory (required)")
	fs.BoolVar(&options.directory, "directory", false, "read directory history from revision manifests")
	fs.BoolVar(&options.json, "json", false, "write machine-readable JSON")
	fs.StringVar(&options.duplicacy, "duplicacy-bin", "duplicacy", "Duplicacy CLI executable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}
	if options.repository == "" || options.path == "" {
		return errors.New("history requires --repo and --path")
	}
	selection, err := dpfs.ResolveSelection(dpfs.SelectionOptions{
		Repository: options.repository,
		Storage:    options.storage,
		SnapshotID: options.snapshot,
		Password:   options.password,
	})
	if err != nil {
		return err
	}
	selected, err := normalizeHistoryPath(options.path)
	if err != nil {
		return err
	}
	if selected == "" && !options.directory {
		return errors.New("file history requires a non-root --path")
	}
	if options.directory {
		listOutput, listError, err := runDuplicacy(ctx, options.duplicacy, selection,
			listArgs(selection.Storage, selection.SnapshotID))
		if err != nil {
			if listError != "" {
				fmt.Fprint(stderr, listError)
			}
			return fmt.Errorf("duplicacy list failed: %w", err)
		}
		snapshotRevisions, err := parseSnapshotListOutput(listOutput)
		if err != nil {
			return err
		}
		var revisions []int
		for _, revision := range snapshotRevisions {
			if revision.SnapshotID == selection.SnapshotID {
				revisions = append(revisions, revision.Revision)
			}
		}
		history, err := dpfs.DirectoryHistory(dpfs.SelectionOptions{
			Repository: selection.Repository,
			Storage:    selection.Storage,
			SnapshotID: selection.SnapshotID,
			Password:   selection.Password,
		}, selected, revisions)
		if err != nil {
			return err
		}
		if options.json {
			return json.NewEncoder(stdout).Encode(struct {
				Path      string                   `json:"path"`
				Revisions []dpfs.DirectoryRevision `json:"revisions"`
			}{Path: selected, Revisions: history})
		}
		for _, revision := range history {
			state := "missing"
			if revision.Exists {
				state = "present"
			}
			fmt.Fprintf(stdout, "%d\t%s\t%s\n", revision.Revision, timeString(revision.Time), state)
		}
		return nil
	}

	commandArgs := historyArgs(selection.Storage, selection.SnapshotID, selected)
	output, commandError, err := runDuplicacy(ctx, options.duplicacy, selection, commandArgs)
	if err != nil {
		if commandError != "" {
			fmt.Fprint(stderr, commandError)
		}
		return fmt.Errorf("duplicacy history failed: %w", err)
	}
	if options.json {
		versions, err := parseHistoryOutput(output)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(struct {
			Path     string        `json:"path"`
			Versions []fileVersion `json:"versions"`
		}{Path: selected, Versions: versions})
	}
	_, err = io.WriteString(stdout, output)
	return err
}

func runRevisions(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	options := cliOptions{}
	fs := newFlagSet("revisions", stderr)
	addRepositoryFlags(fs, &options)
	fs.BoolVar(&options.json, "json", false, "write machine-readable JSON")
	fs.StringVar(&options.duplicacy, "duplicacy-bin", "duplicacy", "Duplicacy CLI executable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if options.repository == "" || fs.NArg() != 0 {
		return errors.New("revisions requires --repo and accepts no positional arguments")
	}
	selection, err := dpfs.ResolveSelection(dpfs.SelectionOptions{
		Repository: options.repository,
		Storage:    options.storage,
		SnapshotID: options.snapshot,
		Password:   options.password,
	})
	if err != nil {
		return err
	}
	output, commandError, err := runDuplicacy(ctx, options.duplicacy, selection,
		listArgs(selection.Storage, selection.SnapshotID))
	if err != nil {
		if commandError != "" {
			fmt.Fprint(stderr, commandError)
		}
		return fmt.Errorf("duplicacy list failed: %w", err)
	}
	revisions, err := parseSnapshotListOutput(output)
	if err != nil {
		return err
	}
	filtered := revisions[:0]
	for _, revision := range revisions {
		if revision.SnapshotID == selection.SnapshotID {
			filtered = append(filtered, revision)
		}
	}
	revisions = filtered
	if options.json {
		return json.NewEncoder(stdout).Encode(struct {
			SnapshotID string             `json:"snapshot_id"`
			Revisions  []snapshotRevision `json:"revisions"`
		}{SnapshotID: selection.SnapshotID, Revisions: revisions})
	}
	for _, revision := range revisions {
		fmt.Fprintf(stdout, "%d\t%s\n", revision.Revision, revision.CreatedAt)
	}
	return nil
}

func listArgs(storage, snapshot string) []string {
	return []string{"list", "-storage", storage, "-id", snapshot}
}

func runDuplicacy(ctx context.Context, executable string, selection *dpfs.Selection, args []string) (string, string, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = selection.Repository
	var output, commandError bytes.Buffer
	command.Stdout = &output
	command.Stderr = &commandError
	if selection.Password != "" {
		command.Env = append(os.Environ(), passwordEnvironment(selection.Storage)+"="+selection.Password)
	}
	err := command.Run()
	return output.String(), commandError.String(), err
}

func parseSnapshotListOutput(output string) ([]snapshotRevision, error) {
	var revisions []snapshotRevision
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if index := strings.Index(line, "Snapshot "); index >= 0 {
			line = line[index:]
		}
		line = strings.TrimSuffix(line, " -hash")
		match := snapshotListLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		revision, err := strconv.Atoi(match[2])
		if err != nil {
			return nil, fmt.Errorf("invalid revision in duplicacy list output: %s", line)
		}
		revisions = append(revisions, snapshotRevision{
			SnapshotID: match[1],
			Revision:   revision,
			CreatedAt:  strings.TrimSpace(match[3]),
		})
	}
	return revisions, nil
}

func historyArgs(storage, snapshot, selected string) []string {
	return []string{"history", "-storage", storage, "-id", snapshot, selected}
}

func normalizeHistoryPath(input string) (string, error) {
	if strings.HasPrefix(input, "/") {
		return "", errors.New("history path must be relative to the repository")
	}
	clean := filepath.ToSlash(filepath.Clean(input))
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("history path must name an item inside the repository")
	}
	return clean, nil
}

func passwordEnvironment(storage string) string {
	if storage == "default" {
		return "DUPLICACY_PASSWORD"
	}
	var name strings.Builder
	name.WriteString("DUPLICACY_")
	for _, char := range strings.ToUpper(storage) {
		if char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' {
			name.WriteRune(char)
		} else {
			name.WriteByte('_')
		}
	}
	name.WriteString("_PASSWORD")
	return name.String()
}

func parseHistoryOutput(output string) ([]fileVersion, error) {
	var versions []fileVersion
	for _, line := range strings.Split(output, "\n") {
		recordLine := strings.TrimSpace(line)
		if index := strings.Index(recordLine, "SNAPSHOT_HISTORY"); index >= 0 {
			recordLine = strings.TrimSpace(recordLine[index+len("SNAPSHOT_HISTORY"):])
			recordLine = strings.TrimPrefix(recordLine, ":")
			recordLine = strings.TrimSpace(recordLine)
		}
		if strings.HasPrefix(recordLine, "current:") {
			continue
		}
		match := historyLine.FindStringSubmatch(recordLine)
		if match == nil {
			continue
		}
		revision, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		row := strings.TrimSpace(match[2])
		version := fileVersion{Revision: revision, Raw: strings.TrimSpace(line)}
		if row != "" {
			fields := strings.Fields(row)
			if len(fields) < 3 {
				return nil, fmt.Errorf("unrecognized history row for revision %d: %s", revision, line)
			}
			version.Size, err = strconv.ParseInt(fields[0], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("unrecognized file size for revision %d: %s", revision, fields[0])
			}
			version.Time = fields[1] + " " + fields[2]
			if len(fields) >= 5 {
				version.Hash = fields[3]
				version.Path = strings.Join(fields[4:], " ")
			} else if len(fields) == 4 {
				version.Path = fields[3]
			}
		}
		versions = append(versions, version)
	}
	if len(versions) == 0 {
		return nil, errors.New("duplicacy history output contained no revision records")
	}
	return versions, nil
}

func runClearCache(args []string, stderr io.Writer) error {
	fs := newFlagSet("clear-cache", stderr)
	dir := fs.String("cache-dir", "", "chunk cache directory to clear (defaults to the standard cache path)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("clear-cache takes no positional arguments")
	}
	cacheDir := *dir
	if cacheDir == "" {
		var err error
		cacheDir, err = defaultCacheDir()
		if err != nil {
			return err
		}
	}
	return dpfs.ClearChunkCache(cacheDir)
}

func defaultCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("determine user cache directory: %w", err)
	}
	return filepath.Join(base, "duplicacy-fuse", "chunks"), nil
}

func parseByteSize(input string) (int64, error) {
	value := strings.TrimSpace(strings.ToUpper(input))
	units := []struct {
		suffix string
		factor int64
	}{
		{"GIB", 1024 * 1024 * 1024}, {"GB", 1000 * 1000 * 1000},
		{"MIB", 1024 * 1024}, {"MB", 1000 * 1000},
		{"KIB", 1024}, {"KB", 1000}, {"B", 1},
	}
	factor := int64(1)
	for _, unit := range units {
		if strings.HasSuffix(value, unit.suffix) {
			factor = unit.factor
			value = strings.TrimSpace(strings.TrimSuffix(value, unit.suffix))
			break
		}
	}
	amount, err := strconv.ParseInt(value, 10, 64)
	if err != nil || amount < 0 || amount > (1<<63-1)/factor {
		return 0, fmt.Errorf("expected a non-negative byte count such as 10GiB")
	}
	return amount * factor, nil
}

func timeString(timestamp int64) string {
	return time.Unix(timestamp, 0).Format(time.RFC3339)
}
