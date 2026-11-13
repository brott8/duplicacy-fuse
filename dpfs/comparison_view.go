package dpfs

import (
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	duplicacy "github.com/gilbertchen/duplicacy/src"
	"github.com/winfsp/cgofuse/fuse"
)

type ComparisonMountOptions struct {
	SelectionOptions
	Revisions      []int
	SelectedPaths  []string
	CacheDir       string
	CacheBytes     int64
	Debug          bool
	DebugWriter    io.Writer
	OnInit         func()
	OnReadError    func()
	PasswordPrompt PasswordPrompt
}

type comparisonRevision struct {
	name      string
	view      *Dpfs
	selected  []selectedEntry
	startTime int64
}

type selectedEntry struct {
	path string
	dir  bool
}

// ComparisonView exposes selected paths from several snapshot revisions below
// revision-named directories. It is intentionally read-only.
type ComparisonView struct {
	fuse.FileSystemBase
	revisions []*comparisonRevision
	byName    map[string]*comparisonRevision
	onInit    func()
}

var _ fuse.FileSystemInterface = (*ComparisonView)(nil)

func NewComparisonView(options ComparisonMountOptions) (*ComparisonView, error) {
	if len(options.Revisions) == 0 {
		return nil, fmt.Errorf("at least one revision is required")
	}

	revisions := append([]int(nil), options.Revisions...)
	seenRevisions := make(map[int]bool, len(revisions))
	for _, revision := range revisions {
		if revision < 1 {
			return nil, fmt.Errorf("revision must be a positive integer")
		}
		if seenRevisions[revision] {
			return nil, fmt.Errorf("revision %d was selected more than once", revision)
		}
		seenRevisions[revision] = true
	}
	sort.Ints(revisions)

	selectedPaths, err := normalizeSelectedPaths(options.SelectedPaths)
	if err != nil {
		return nil, err
	}
	selection, err := ResolveSelection(options.SelectionOptions)
	if err != nil {
		return nil, err
	}
	cacheDir, err := cachePathOutsideRepository(selection.Repository, options.CacheDir)
	if err != nil {
		return nil, err
	}
	manager, config, storage, metadataCacheDir, err := openBackupManager(selection, options.PasswordPrompt)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(metadataCacheDir)
	cache, err := newChunkCache(cacheDir, options.CacheBytes)
	if err != nil {
		return nil, err
	}
	storageMu := &sync.Mutex{}
	view := &ComparisonView{
		byName: make(map[string]*comparisonRevision, len(revisions)),
		onInit: options.OnInit,
	}
	selectedCount := 0
	for _, revision := range revisions {
		started := time.Now()
		snapshot := manager.SnapshotManager.DownloadSnapshot(selection.SnapshotID, revision)
		if snapshot == nil {
			return nil, fmt.Errorf("revision %d was not found", revision)
		}
		if !manager.SnapshotManager.DownloadSnapshotSequence(snapshot, "chunks") ||
			!manager.SnapshotManager.DownloadSnapshotSequence(snapshot, "lengths") {
			return nil, fmt.Errorf("failed to load metadata for revision %d", revision)
		}
		entries, err := loadSnapshotEntries(config, storage, snapshot)
		if err != nil {
			return nil, fmt.Errorf("failed to load file metadata for revision %d: %w", revision, err)
		}

		matched := matchingSelectedEntries(entries, selectedPaths)
		if len(matched) == 0 {
			debugLog(options.DebugWriter, options.Debug, "DPFS_COMPARISON", "revision=%d has no selected paths", revision)
			continue
		}
		selectedCount += len(matched)

		child := &Dpfs{
			storage:     storage,
			storageMu:   storageMu,
			config:      config,
			preference:  selection.Preference,
			repository:  selection.Repository,
			snapshot:    snapshot,
			entries:     make(map[string]*duplicacy.Entry, len(entries)),
			children:    make(map[string][]string),
			loadedDirs:  make(map[string]bool),
			debug:       options.Debug,
			debugWriter: options.DebugWriter,
			onReadError: options.OnReadError,
			cache:       cache,
		}
		for _, entry := range entries {
			name := strings.TrimSuffix(entry.Path, "/")
			if name == "" {
				continue
			}
			child.entries[name] = entry
			parent := path.Dir(name)
			child.children[parent] = append(child.children[parent], path.Base(name))
		}
		for parent := range child.children {
			sort.Strings(child.children[parent])
			child.loadedDirs[parent] = true
		}
		child.loadedDirs["."] = true

		name := comparisonRevisionName(revision, snapshot.StartTime)
		revisionView := &comparisonRevision{name: name, view: child, selected: matched, startTime: snapshot.StartTime}
		view.revisions = append(view.revisions, revisionView)
		view.byName[name] = revisionView
		debugLog(options.DebugWriter, options.Debug, "DPFS_COMPARISON", "loaded revision=%d entries=%d selected=%d elapsed=%s",
			revision, len(entries), len(matched), time.Since(started))
	}
	if selectedCount == 0 {
		return nil, fmt.Errorf("none of the selected paths exist in the chosen revisions")
	}
	if options.Debug {
		debugLog(options.DebugWriter, true, "DPFS_COMPARISON", "view ready revisions=%d selected_paths=%d", len(view.revisions), selectedCount)
	}
	return view, nil
}

func comparisonRevisionName(revision int, timestamp int64) string {
	return fmt.Sprintf("revision-%d-%s", revision, time.Unix(timestamp, 0).UTC().Format("2006-01-02-150405Z"))
}

func normalizeSelectedPaths(inputs []string) ([]string, error) {
	if len(inputs) == 0 {
		return []string{""}, nil
	}
	seen := make(map[string]bool, len(inputs))
	paths := make([]string, 0, len(inputs))
	for _, input := range inputs {
		selected, err := normalizeSelectedPath(input)
		if err != nil {
			return nil, err
		}
		if !seen[selected] {
			seen[selected] = true
			paths = append(paths, selected)
		}
	}
	if seen[""] {
		return []string{""}, nil
	}
	sort.Strings(paths)
	return paths, nil
}

func matchingSelectedEntries(entries []*duplicacy.Entry, selectedPaths []string) []selectedEntry {
	byName := make(map[string]*duplicacy.Entry, len(entries))
	for _, entry := range entries {
		byName[strings.TrimSuffix(entry.Path, "/")] = entry
	}
	matched := make([]selectedEntry, 0, len(selectedPaths))
	for _, selectedPath := range selectedPaths {
		if selectedPath == "" {
			return []selectedEntry{{path: "", dir: true}}
		}
		entry := byName[selectedPath]
		if entry != nil {
			matched = append(matched, selectedEntry{path: selectedPath, dir: entry.IsDir()})
		}
	}
	return matched
}

func (view *ComparisonView) Init() {
	if view.onInit != nil {
		view.onInit()
	}
}

func (view *ComparisonView) Destroy() {
	for _, revision := range view.revisions {
		revision.view.Destroy()
	}
}

func (view *ComparisonView) Statfs(name string, stat *fuse.Statfs_t) int {
	if stat == nil {
		return -fuse.EINVAL
	}
	stat.Bsize = 4096
	stat.Frsize = 4096
	stat.Namemax = 255
	for _, revision := range view.revisions {
		stat.Blocks += uint64((revision.view.snapshot.FileSize + 4095) / 4096)
		stat.Files += uint64(revision.view.snapshot.NumberOfFiles)
	}
	return 0
}

func (view *ComparisonView) Getattr(name string, stat *fuse.Stat_t, fh uint64) int {
	if stat == nil {
		return -fuse.EINVAL
	}
	relative, valid := comparisonPath(name)
	if !valid {
		return -fuse.ENOENT
	}
	if relative == "." {
		setComparisonDirectory(stat, 0)
		stat.Nlink = uint32(2 + len(view.revisions))
		return 0
	}
	_, childPath, revision := view.resolve(relative)
	if revision == nil {
		return -fuse.ENOENT
	}
	if childPath == "." {
		setComparisonDirectory(stat, revision.startTime)
		return 0
	}
	if !revision.allows(childPath) {
		return -fuse.ENOENT
	}
	if synthetic := revision.syntheticDirectory(childPath); synthetic {
		setComparisonDirectory(stat, 0)
		return 0
	}
	return revision.view.Getattr("/"+childPath, stat, fh)
}

func (view *ComparisonView) Readdir(name string, fill func(name string, stat *fuse.Stat_t, ofst int64) bool, ofst int64, fh uint64) int {
	relative, valid := comparisonPath(name)
	if !valid {
		return -fuse.ENOENT
	}
	if ofst < 0 {
		return -fuse.EINVAL
	}
	if relative == "." {
		entries := make([]string, 0, len(view.revisions)+2)
		entries = append(entries, ".", "..")
		for _, revision := range view.revisions {
			entries = append(entries, revision.name)
		}
		for i := int(ofst); i < len(entries); i++ {
			if !fill(entries[i], nil, int64(i+1)) {
				break
			}
		}
		return 0
	}
	_, childPath, revision := view.resolve(relative)
	if revision == nil {
		return -fuse.ENOENT
	}
	if childPath != "." && !revision.allows(childPath) {
		return -fuse.ENOENT
	}
	if childPath != "." && !revision.isDirectory(childPath) {
		return -fuse.ENOTDIR
	}
	innerPath := "/"
	if childPath != "." {
		innerPath = "/" + childPath
	}
	return revision.view.Readdir(innerPath, func(entry string, stat *fuse.Stat_t, offset int64) bool {
		if entry == "." || entry == ".." {
			return fill(entry, stat, offset)
		}
		candidate := entry
		if childPath != "." {
			candidate = path.Join(childPath, entry)
		}
		if !revision.allows(candidate) {
			return true
		}
		return fill(entry, stat, offset)
	}, ofst, fh)
}

func (view *ComparisonView) Open(name string, flags int) (int, uint64) {
	if flags&3 != 0 || flags&0x200 != 0 {
		return -fuse.EROFS, 0
	}
	if relative, valid := comparisonPath(name); valid && relative == "." {
		return -fuse.EISDIR, 0
	}
	revision, childPath, code := view.resolveAllowed(name)
	if code != 0 {
		return code, 0
	}
	if childPath == "." {
		return -fuse.EISDIR, 0
	}
	return revision.view.Open("/"+childPath, flags)
}

func (view *ComparisonView) Read(name string, buff []byte, offset int64, fh uint64) int {
	if relative, valid := comparisonPath(name); valid && relative == "." {
		return -fuse.EISDIR
	}
	revision, childPath, code := view.resolveAllowed(name)
	if code != 0 {
		return code
	}
	if childPath == "." {
		return -fuse.EISDIR
	}
	return revision.view.Read("/"+childPath, buff, offset, fh)
}

func (view *ComparisonView) Readlink(name string) (int, string) {
	if relative, valid := comparisonPath(name); valid && relative == "." {
		return -fuse.EINVAL, ""
	}
	revision, childPath, code := view.resolveAllowed(name)
	if code != 0 {
		return code, ""
	}
	if childPath == "." {
		return -fuse.EINVAL, ""
	}
	return revision.view.Readlink("/" + childPath)
}

func (view *ComparisonView) Access(name string, mask uint32) int {
	if mask&2 != 0 {
		return -fuse.EROFS
	}
	if relative, valid := comparisonPath(name); valid && relative == "." {
		return 0
	}
	revision, childPath, code := view.resolveAllowed(name)
	if code != 0 {
		return code
	}
	if childPath == "." || revision.syntheticDirectory(childPath) {
		return 0
	}
	return revision.view.Access("/"+childPath, mask)
}

func (view *ComparisonView) Opendir(name string) (int, uint64) {
	relative, valid := comparisonPath(name)
	if !valid {
		return -fuse.ENOENT, 0
	}
	if relative == "." {
		return 0, 0
	}
	_, childPath, revision := view.resolve(relative)
	if revision == nil || !revision.allows(childPath) {
		return -fuse.ENOENT, 0
	}
	if childPath != "." && !revision.isDirectory(childPath) {
		return -fuse.ENOTDIR, 0
	}
	if revision.syntheticDirectory(childPath) {
		return 0, 0
	}
	return revision.view.Opendir("/" + childPath)
}

func (view *ComparisonView) Releasedir(name string, fh uint64) int { return 0 }
func (view *ComparisonView) Release(name string, fh uint64) int    { return 0 }
func (view *ComparisonView) Flush(name string, fh uint64) int      { return 0 }
func (view *ComparisonView) Fsync(name string, datasync bool, fh uint64) int {
	return 0
}
func (view *ComparisonView) Fsyncdir(name string, datasync bool, fh uint64) int { return 0 }
func (view *ComparisonView) Write(name string, buff []byte, offset int64, fh uint64) int {
	return -fuse.EROFS
}
func (view *ComparisonView) Create(name string, flags int, mode uint32) (int, uint64) {
	return -fuse.EROFS, 0
}
func (view *ComparisonView) Mkdir(name string, mode uint32) int              { return -fuse.EROFS }
func (view *ComparisonView) Mknod(name string, mode uint32, dev uint64) int  { return -fuse.EROFS }
func (view *ComparisonView) Unlink(name string) int                          { return -fuse.EROFS }
func (view *ComparisonView) Rmdir(name string) int                           { return -fuse.EROFS }
func (view *ComparisonView) Symlink(target string, name string) int          { return -fuse.EROFS }
func (view *ComparisonView) Rename(oldname string, newname string) int       { return -fuse.EROFS }
func (view *ComparisonView) Link(oldname string, newname string) int         { return -fuse.EROFS }
func (view *ComparisonView) Chmod(name string, mode uint32) int              { return -fuse.EROFS }
func (view *ComparisonView) Chown(name string, uid uint32, gid uint32) int   { return -fuse.EROFS }
func (view *ComparisonView) Truncate(name string, size int64, fh uint64) int { return -fuse.EROFS }
func (view *ComparisonView) Utimens(name string, times []fuse.Timespec) int  { return -fuse.EROFS }
func (view *ComparisonView) Setxattr(name string, attr string, data []byte, flags int) int {
	return -fuse.EROFS
}
func (view *ComparisonView) Removexattr(name string, attr string) int { return -fuse.EROFS }

func (view *ComparisonView) resolveAllowed(name string) (*comparisonRevision, string, int) {
	relative, valid := comparisonPath(name)
	if !valid {
		return nil, "", -fuse.ENOENT
	}
	_, childPath, revision := view.resolve(relative)
	if revision == nil || !revision.allows(childPath) {
		return nil, "", -fuse.ENOENT
	}
	return revision, childPath, 0
}

func (view *ComparisonView) resolve(relative string) (string, string, *comparisonRevision) {
	if relative == "." {
		return "", ".", nil
	}
	name, childPath, found := strings.Cut(relative, "/")
	revision := view.byName[name]
	if revision == nil {
		return name, "", nil
	}
	if !found {
		return name, ".", revision
	}
	return name, childPath, revision
}

func comparisonPath(name string) (string, bool) {
	if name == "" || name == "/" || name == "." {
		return ".", true
	}
	for _, component := range strings.Split(name, "/") {
		if component == ".." {
			return "", false
		}
	}
	clean := path.Clean("/" + strings.TrimPrefix(name, "/"))
	return strings.TrimPrefix(clean, "/"), true
}

func (revision *comparisonRevision) allows(name string) bool {
	if name == "." || name == "" {
		return true
	}
	for _, selected := range revision.selected {
		if selected.path == "" || selected.path == name || strings.HasPrefix(selected.path, name+"/") ||
			(selected.dir && strings.HasPrefix(name, selected.path+"/")) {
			return true
		}
	}
	return false
}

func (revision *comparisonRevision) syntheticDirectory(name string) bool {
	if name == "." || name == "" {
		return true
	}
	for _, selected := range revision.selected {
		if selected.path != "" && strings.HasPrefix(selected.path, name+"/") {
			return true
		}
	}
	return false
}

func (revision *comparisonRevision) isDirectory(name string) bool {
	if name == "." || revision.syntheticDirectory(name) {
		return true
	}
	entry, ok := revision.view.lookup(name)
	return ok && entry != nil && entry.IsDir()
}

func setComparisonDirectory(stat *fuse.Stat_t, timestamp int64) {
	stat.Mode = fuse.S_IFDIR | 0555
	stat.Nlink = 2
	if timestamp > 0 {
		stat.Mtim = fuse.NewTimespec(time.Unix(timestamp, 0))
	}
}
