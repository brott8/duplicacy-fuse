package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
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

func TestCommandHelpSucceeds(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"revisions", "-h"}, &output, &output); err != nil {
		t.Fatalf("help returned error: %v", err)
	}
	if !strings.Contains(output.String(), "repository directory") {
		t.Fatalf("help output lacks command options: %s", output.String())
	}
}
