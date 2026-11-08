package dpfs

import (
	"os"
	"reflect"
	"testing"

	duplicacy "github.com/gilbertchen/duplicacy/src"
	"github.com/winfsp/cgofuse/fuse"
)

func newLazyMetadataTestView(entries []*duplicacy.Entry, legacy bool) (*Dpfs, *int) {
	listCalls := 0
	view := &Dpfs{
		entries:        make(map[string]*duplicacy.Entry),
		children:       make(map[string][]string),
		loadedDirs:     make(map[string]bool),
		lazyMetadata:   true,
		legacyMetadata: legacy,
		listSnapshotEntries: func(entryOut func(*duplicacy.Entry) bool) {
			listCalls++
			for _, entry := range entries {
				if !entryOut(entry) {
					return
				}
			}
		},
	}
	return view, &listCalls
}

func TestLazyMetadataLoadsOnlyRequestedDirectories(t *testing.T) {
	entries := []*duplicacy.Entry{
		duplicacy.CreateEntry("", 0, 0, 0),
		duplicacy.CreateEntry("top.txt", 3, 0, 0644),
		duplicacy.CreateEntry("a/", 0, 0, uint32(os.ModeDir)|0555),
		duplicacy.CreateEntry("b/", 0, 0, uint32(os.ModeDir)|0555),
		duplicacy.CreateEntry("a/first.txt", 5, 0, 0644),
		duplicacy.CreateEntry("a/nested/", 0, 0, uint32(os.ModeDir)|0555),
		duplicacy.CreateEntry("a/nested/deep.txt", 4, 0, 0644),
		duplicacy.CreateEntry("b/second.txt", 6, 0, 0644),
	}
	view, listCalls := newLazyMetadataTestView(entries, false)

	if err := view.ensureDirectoryLoaded("."); err != nil {
		t.Fatal(err)
	}
	if got, want := view.children["."], []string{"a", "b", "top.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root children = %v, want %v", got, want)
	}
	if _, found := view.entries["a/first.txt"]; found {
		t.Fatal("root listing eagerly loaded a descendant file")
	}
	if *listCalls != 1 {
		t.Fatalf("snapshot scans = %d, want 1", *listCalls)
	}

	if err := view.ensureDirectoryLoaded("a"); err != nil {
		t.Fatal(err)
	}
	if got, want := view.children["a"], []string{"first.txt", "nested"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("a children = %v, want %v", got, want)
	}
	if _, found := view.entries["a/nested/deep.txt"]; found {
		t.Fatal("listing a eagerly loaded a nested directory's contents")
	}

	if err := view.ensureDirectoryLoaded("a/nested"); err != nil {
		t.Fatal(err)
	}
	if got, want := view.children["a/nested"], []string{"deep.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("a/nested children = %v, want %v", got, want)
	}
	if *listCalls != 3 {
		t.Fatalf("snapshot scans = %d, want 3", *listCalls)
	}
}

func TestLegacyLazyMetadataLoadsOnFirstBrowse(t *testing.T) {
	entries := []*duplicacy.Entry{
		duplicacy.CreateEntry("", 0, 0, 0),
		duplicacy.CreateEntry("dir/", 0, 0, uint32(os.ModeDir)|0555),
		duplicacy.CreateEntry("dir/file.txt", 4, 0, 0644),
	}
	view, listCalls := newLazyMetadataTestView(entries, true)

	if err := view.ensureDirectoryLoaded("."); err != nil {
		t.Fatal(err)
	}
	if !view.metadataComplete || !view.loadedDirs["dir"] {
		t.Fatal("legacy metadata was not fully cached on first browse")
	}
	if *listCalls != 1 {
		t.Fatalf("snapshot scans = %d, want 1", *listCalls)
	}
}

func TestReaddirLoadsRootLazily(t *testing.T) {
	entries := []*duplicacy.Entry{
		duplicacy.CreateEntry("top.txt", 3, 0, 0644),
		duplicacy.CreateEntry("dir/", 0, 0, uint32(os.ModeDir)|0555),
		duplicacy.CreateEntry("dir/file.txt", 4, 0, 0644),
	}
	view, listCalls := newLazyMetadataTestView(entries, false)
	var names []string
	if code := view.Readdir("/", func(name string, _ *fuse.Stat_t, _ int64) bool {
		names = append(names, name)
		return true
	}, 0, 0); code != 0 {
		t.Fatalf("Readdir returned %d", code)
	}
	if got, want := names, []string{".", "..", "dir", "top.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root entries = %v, want %v", got, want)
	}
	if _, found := view.entries["dir/file.txt"]; found {
		t.Fatal("root Readdir eagerly loaded a descendant file")
	}
	if *listCalls != 1 {
		t.Fatalf("snapshot scans = %d, want 1", *listCalls)
	}
}
