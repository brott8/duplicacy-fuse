package dpfs

import (
	"os"
	"reflect"
	"testing"

	duplicacy "github.com/gilbertchen/duplicacy/src"
	"github.com/winfsp/cgofuse/fuse"
)

func TestNormalizeSelectedPaths(t *testing.T) {
	got, err := normalizeSelectedPaths([]string{"docs/./report.txt", "docs/report.txt", "photos"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"docs/report.txt", "photos"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized paths = %#v, want %#v", got, want)
	}
	if got, err := normalizeSelectedPaths([]string{"../outside"}); err == nil || got != nil {
		t.Fatalf("escaping path = %#v, %v; want an error", got, err)
	}
	if got, err := normalizeSelectedPaths([]string{"docs/report.txt", ""}); err != nil || !reflect.DeepEqual(got, []string{""}) {
		t.Fatalf("root selection = %#v, %v", got, err)
	}
}

func TestMatchingSelectedEntriesIncludesPartialMatches(t *testing.T) {
	entries := []*duplicacy.Entry{
		duplicacy.CreateEntry("documents/", 0, 0, uint32(os.ModeDir)|0555),
		duplicacy.CreateEntry("documents/report.txt", 12, 0, 0444),
		duplicacy.CreateEntry("photo.jpg", 5, 0, 0444),
	}
	got := matchingSelectedEntries(entries, []string{"documents", "missing", "photo.jpg"})
	want := []selectedEntry{{path: "documents", dir: true}, {path: "photo.jpg", dir: false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matched selections = %#v, want %#v", got, want)
	}
	if got := matchingSelectedEntries(entries, []string{"missing"}); len(got) != 0 {
		t.Fatalf("missing selection matches = %#v", got)
	}
}

func TestComparisonViewOnlyExposesSelectedPaths(t *testing.T) {
	revisionName := comparisonRevisionName(7, 1780401000)
	directory := duplicacy.CreateEntry("documents/", 0, 0, uint32(os.ModeDir)|0555)
	selectedFile := duplicacy.CreateEntry("documents/report.txt", 12, 0, 0444)
	unselectedFile := duplicacy.CreateEntry("documents/secret.txt", 8, 0, 0444)
	view := &ComparisonView{
		byName: map[string]*comparisonRevision{},
		revisions: []*comparisonRevision{{
			name:      revisionName,
			startTime: 1780401000,
			selected:  []selectedEntry{{path: "documents/report.txt"}},
			view: &Dpfs{
				entries: map[string]*duplicacy.Entry{
					"documents":            directory,
					"documents/report.txt": selectedFile,
					"documents/secret.txt": unselectedFile,
				},
				children: map[string][]string{
					".":         {"documents"},
					"documents": {"report.txt", "secret.txt"},
				},
				loadedDirs: map[string]bool{".": true, "documents": true},
			},
		}},
	}
	view.byName[revisionName] = view.revisions[0]

	stat := &fuse.Stat_t{}
	if code := view.Getattr("/"+revisionName, stat, 0); code != 0 || stat.Mode&fuse.S_IFDIR == 0 {
		t.Fatalf("revision folder getattr = %d, stat %#v", code, stat)
	}
	if code := view.Getattr("/"+revisionName+"/documents/report.txt", stat, 0); code != 0 || stat.Mode&fuse.S_IFREG == 0 {
		t.Fatalf("selected file getattr = %d, stat %#v", code, stat)
	}
	if code := view.Getattr("/"+revisionName+"/documents/secret.txt", stat, 0); code != -fuse.ENOENT {
		t.Fatalf("unselected file getattr = %d, want %d", code, -fuse.ENOENT)
	}
	var names []string
	if code := view.Readdir("/"+revisionName+"/documents", func(name string, _ *fuse.Stat_t, _ int64) bool {
		names = append(names, name)
		return true
	}, 0, 0); code != 0 {
		t.Fatalf("selected directory readdir = %d", code)
	}
	if want := []string{".", "..", "report.txt"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("selected directory entries = %#v, want %#v", names, want)
	}
	if code, _ := view.Open("/"+revisionName+"/documents/report.txt", 0); code != 0 {
		t.Fatalf("selected file open = %d", code)
	}
	if code, _ := view.Open("/"+revisionName+"/documents/secret.txt", 0); code != -fuse.ENOENT {
		t.Fatalf("unselected file open = %d, want %d", code, -fuse.ENOENT)
	}
	if code := view.Write("/"+revisionName+"/documents/report.txt", []byte("x"), 0, 0); code != -fuse.EROFS {
		t.Fatalf("write = %d, want %d", code, -fuse.EROFS)
	}
}

func TestComparisonViewPreservesSelectedDirectoryRecursively(t *testing.T) {
	view := &comparisonRevision{
		selected: []selectedEntry{{path: "photos", dir: true}},
	}
	for _, path := range []string{"photos", "photos/2024", "photos/2024/summer.jpg"} {
		if !view.allows(path) {
			t.Errorf("selected directory does not expose %q", path)
		}
	}
	if view.allows("documents/secret.txt") {
		t.Fatal("selected directory exposed an unrelated path")
	}
}

func TestBrowseSnapshotDirectoryRejectsEscapingPaths(t *testing.T) {
	if entries, err := BrowseSnapshotDirectory(SelectionOptions{}, 2, "../outside", nil); err == nil || entries != nil {
		t.Fatalf("escaping browse path = %#v, %v; want an error", entries, err)
	}
}
