package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"gitlab.com/andrewheberle/duplicacy-fuse/dpfs"
)

func TestHistoryArgs(t *testing.T) {
	want := []string{"history", "-storage", "nas", "-id", "repo-id", "folder/file name.txt"}
	if got := historyArgs("nas", "repo-id", "folder/file name.txt"); !reflect.DeepEqual(got, want) {
		t.Fatalf("history args = %v, want %v", got, want)
	}
}

func TestListArgs(t *testing.T) {
	want := []string{"list", "-storage", "nas", "-id", "repo-id"}
	if got := listArgs("nas", "repo-id"); !reflect.DeepEqual(got, want) {
		t.Fatalf("list args = %v, want %v", got, want)
	}
}

func TestParseSnapshotListOutput(t *testing.T) {
	fixture := "Storage set to /nas\n" +
		"Snapshot repo-id revision 7 created at 2024-06-01 12:30 -hash\n" +
		"Snapshot repo-id revision 8 created at 2024-06-02 14:45\n" +
		"Snapshot other-id revision 1 created at 2024-06-02 15:00\n"
	revisions, err := parseSnapshotListOutput(fixture)
	if err != nil {
		t.Fatal(err)
	}
	want := []snapshotRevision{
		{SnapshotID: "repo-id", Revision: 7, CreatedAt: "2024-06-01 12:30"},
		{SnapshotID: "repo-id", Revision: 8, CreatedAt: "2024-06-02 14:45"},
		{SnapshotID: "other-id", Revision: 1, CreatedAt: "2024-06-02 15:00"},
	}
	if !reflect.DeepEqual(revisions, want) {
		t.Fatalf("parsed revisions = %#v, want %#v", revisions, want)
	}
}

func TestParseHistoryOutput(t *testing.T) {
	hash := strings.Repeat("a", 64)
	fixture := "Storage set to /nas\n" +
		"      1:     120 2024-06-01 12:30:45 " + hash + " folder/my file.txt*\n" +
		"      2:\n" +
		"current: 150 2024-06-02 12:30:45 " + hash + " folder/my file.txt\n"
	versions, err := parseHistoryOutput(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("got %d revision records, want 2: %#v", len(versions), versions)
	}
	if versions[0].Revision != 1 || versions[0].Size != 120 || versions[0].Time != "2024-06-01 12:30:45" ||
		versions[0].Hash != hash || versions[0].Path != "folder/my file.txt*" {
		t.Fatalf("parsed version = %#v", versions[0])
	}
	if versions[1].Revision != 2 || versions[1].Path != "" {
		t.Fatalf("missing revision record = %#v", versions[1])
	}
}

func TestNormalizeHistoryPath(t *testing.T) {
	if got, err := normalizeHistoryPath("docs/../file.txt"); err != nil || got != "file.txt" {
		t.Fatalf("normalized path = %q, %v", got, err)
	}
	for _, input := range []string{"../outside", "/absolute"} {
		if _, err := normalizeHistoryPath(input); err == nil {
			t.Errorf("normalizeHistoryPath(%q) accepted invalid path", input)
		}
	}
	if got, err := normalizeHistoryPath("."); err != nil || got != "" {
		t.Fatalf("root directory path = %q, %v", got, err)
	}
}

func TestParseByteSize(t *testing.T) {
	for input, want := range map[string]int64{"10GiB": 10 * 1024 * 1024 * 1024, "2MB": 2000000, "0": 0} {
		got, err := parseByteSize(input)
		if err != nil || got != want {
			t.Errorf("parseByteSize(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	if _, err := parseByteSize("-1"); err == nil {
		t.Fatal("negative cache size was accepted")
	}
}

func TestRepeatableMountFlags(t *testing.T) {
	var revisions intListFlag
	var paths stringListFlag
	flags := newFlagSet("mount", &bytes.Buffer{})
	flags.Var(&revisions, "revision", "revision")
	flags.Var(&paths, "path", "path")
	if err := flags.Parse([]string{"--revision", "7", "--path", "docs/a.txt", "--revision", "9", "--path", "photos"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual([]int(revisions), []int{7, 9}) {
		t.Fatalf("revisions = %v", revisions)
	}
	if !reflect.DeepEqual([]string(paths), []string{"docs/a.txt", "photos"}) {
		t.Fatalf("paths = %v", paths)
	}
}

func TestComparisonMountSelectionKeepsLegacySingleLayout(t *testing.T) {
	if useComparisonMount(false, 1, 1) {
		t.Fatal("a normal one-revision, one-path mount should retain its existing layout")
	}
	for _, test := range []struct {
		force     bool
		revisions int
		paths     int
	}{
		{force: true, revisions: 1, paths: 1},
		{revisions: 2, paths: 1},
		{revisions: 1, paths: 2},
	} {
		if !useComparisonMount(test.force, test.revisions, test.paths) {
			t.Errorf("useComparisonMount(%t, %d, %d) = false", test.force, test.revisions, test.paths)
		}
	}
}

func TestCommandHelpSucceeds(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"revisions", "-h"}, &output, &output); err != nil {
		t.Fatalf("help returned error: %v", err)
	}
	if !strings.Contains(output.String(), "repository directory") {
		t.Fatalf("help output lacks command options: %s", output.String())
	}
}

func TestRunDuplicacyUsesRepositoryPasswordAndPreservesOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fake executable uses a POSIX shell script")
	}

	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if err := os.Mkdir(repository, 0700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "duplicacy")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$PWD\" \"$DUPLICACY_NAS_PASSWORD\" \"$@\" > \"$DUPLICACY_FUSE_CAPTURE\"\n" +
		"printf 'stdout marker\\n'\n" +
		"printf 'stderr marker\\n' >&2\n" +
		"exit \"${DUPLICACY_FUSE_EXIT:-0}\"\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(root, "invocation.txt")
	t.Setenv("DUPLICACY_FUSE_CAPTURE", capture)
	t.Setenv("DUPLICACY_NAS_PASSWORD", "inherited-password")
	t.Setenv("DUPLICACY_FUSE_EXIT", "0")
	selection := &dpfs.Selection{Repository: repository, Storage: "nas", Password: "selected-password"}

	stdout, stderr, err := runDuplicacy(context.Background(), executable, selection,
		[]string{"history", "-storage", "nas", "file with spaces.txt"})
	if err != nil {
		t.Fatalf("runDuplicacy returned error: %v", err)
	}
	if stdout != "stdout marker\n" || stderr != "stderr marker\n" {
		t.Fatalf("command output = %q / %q", stdout, stderr)
	}
	invocation, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	wantInvocation := strings.Join([]string{
		repository,
		"selected-password",
		"history",
		"-storage",
		"nas",
		"file with spaces.txt",
		"",
	}, "\n")
	if string(invocation) != wantInvocation {
		t.Fatalf("captured invocation = %q, want %q", invocation, wantInvocation)
	}

	t.Setenv("DUPLICACY_FUSE_EXIT", "7")
	stdout, stderr, err = runDuplicacy(context.Background(), executable, selection, []string{"list"})
	if err == nil || stdout != "stdout marker\n" || stderr != "stderr marker\n" {
		t.Fatalf("failed command result = %q / %q / %v", stdout, stderr, err)
	}
}
