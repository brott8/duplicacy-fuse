package dpfs

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	duplicacy "github.com/gilbertchen/duplicacy/src"
	"github.com/winfsp/cgofuse/fuse"
)

func TestNormalizeSelectedPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
		valid bool
	}{
		{"", "", true},
		{".", "", true},
		{"docs/../file.txt", "file.txt", true},
		{" file ", " file ", true},
		{"../outside", "", false},
		{"/absolute/path", "", false},
		{"/", "", false},
		{"folder\\name", "folder\\name", true},
	}
	for _, test := range tests {
		got, err := normalizeSelectedPath(test.input)
		if (err == nil) != test.valid || got != test.want {
			t.Errorf("normalizeSelectedPath(%q) = %q, %v; want %q, valid=%t", test.input, got, err, test.want, test.valid)
		}
	}
}

func TestPlanReadRangesAcrossChunks(t *testing.T) {
	snapshot := &duplicacy.Snapshot{ChunkLengths: []int{5, 4, 6}, ChunkHashes: []string{"a", "b", "c"}}
	entry := duplicacy.CreateEntry("file", 11, 0, 0)
	entry.StartChunk, entry.StartOffset = 0, 2
	entry.EndChunk, entry.EndOffset = 2, 4

	ranges, err := planReadRanges(snapshot, entry, 2, 6)
	if err != nil {
		t.Fatal(err)
	}
	want := []readRange{
		{index: 0, chunkStart: 4, chunkEnd: 5, bufferOffset: 0},
		{index: 1, chunkStart: 0, chunkEnd: 4, bufferOffset: 1},
		{index: 2, chunkStart: 0, chunkEnd: 1, bufferOffset: 5},
	}
	if !reflect.DeepEqual(ranges, want) {
		t.Fatalf("read ranges = %#v, want %#v", ranges, want)
	}

	if got, err := planReadRanges(snapshot, entry, 11, 10); err != nil || len(got) != 0 {
		t.Fatalf("EOF range = %#v, %v; want no ranges", got, err)
	}
	if _, err := planReadRanges(snapshot, entry, -1, 2); err == nil {
		t.Fatal("negative offset was accepted")
	}
}

func TestPlannedReadsMatchFileBytesAtRandomOffsets(t *testing.T) {
	chunks := [][]byte{[]byte("ABCDE"), []byte("FGHI"), []byte("JKLMNO")}
	snapshot := &duplicacy.Snapshot{ChunkLengths: []int{5, 4, 6}, ChunkHashes: []string{"a", "b", "c"}}
	entry := duplicacy.CreateEntry("file", 11, 0, 0)
	entry.StartChunk, entry.StartOffset = 0, 2
	entry.EndChunk, entry.EndOffset = 2, 4
	fileData := []byte("CDEFGHIJKLM")

	for _, offset := range []int64{0, 1, 2, 3, 6, 8, 10, 11} {
		for _, requested := range []int{1, 3, 6, 20} {
			ranges, err := planReadRanges(snapshot, entry, offset, requested)
			if err != nil {
				t.Fatalf("offset=%d length=%d: %v", offset, requested, err)
			}
			length := requested
			if offset >= int64(len(fileData)) {
				length = 0
			} else if available := len(fileData) - int(offset); length > available {
				length = available
			}
			got := make([]byte, length)
			for _, read := range ranges {
				copy(got[read.bufferOffset:read.bufferOffset+read.chunkEnd-read.chunkStart],
					chunks[read.index][read.chunkStart:read.chunkEnd])
			}
			want := fileData
			if offset >= int64(len(fileData)) {
				want = nil
			} else {
				want = fileData[int(offset) : int(offset)+length]
			}
			if !bytes.Equal(got, want) {
				t.Errorf("read offset=%d length=%d = %q, want %q", offset, requested, got, want)
			}
		}
	}
}

func TestViewRootAndReadOnlyOperations(t *testing.T) {
	entry := duplicacy.CreateEntry("selected/file.txt", 7, 100, 0644)
	view := &Dpfs{
		folderRoot: "selected",
		entries:    map[string]*duplicacy.Entry{"file.txt": entry},
		children:   map[string][]string{".": {"file.txt"}},
	}
	stat := &fuse.Stat_t{}
	if code := view.Getattr("/", stat, 0); code != 0 || stat.Mode&fuse.S_IFDIR == 0 {
		t.Fatalf("mount root getattr = %d, mode %#o", code, stat.Mode)
	}
	if code := view.Getattr("/file.txt", stat, 0); code != 0 || stat.Size != 7 || stat.Mode&fuse.S_IFREG == 0 {
		t.Fatalf("selected file getattr = %d, stat %#v", code, stat)
	}
	if code := view.Getattr("/missing", stat, 0); code != -fuse.ENOENT {
		t.Fatalf("missing path getattr = %d, want %d", code, -fuse.ENOENT)
	}

	var names []string
	view.Readdir("/", func(name string, _ *fuse.Stat_t, _ int64) bool {
		names = append(names, name)
		return true
	}, 0, 0)
	if !reflect.DeepEqual(names, []string{".", "..", "file.txt"}) {
		t.Fatalf("root entries = %v", names)
	}
	if code, _ := view.Open("/file.txt", 1); code != -fuse.EROFS {
		t.Fatalf("write open = %d, want %d", code, -fuse.EROFS)
	}
	if code := view.Write("/file.txt", []byte("x"), 0, 0); code != -fuse.EROFS {
		t.Fatalf("write = %d, want %d", code, -fuse.EROFS)
	}
	if code := view.Unlink("/file.txt"); code != -fuse.EROFS {
		t.Fatalf("unlink = %d, want %d", code, -fuse.EROFS)
	}
}

func TestFileRootExposesOnlySelectedFile(t *testing.T) {
	entry := duplicacy.CreateEntry("dir/chosen.txt", 1, 0, 0644)
	view := &Dpfs{fileRoot: "chosen.txt", entries: map[string]*duplicacy.Entry{"chosen.txt": entry}, children: map[string][]string{".": {"chosen.txt"}}}
	stat := &fuse.Stat_t{}
	if code := view.Getattr("/other.txt", stat, 0); code != -fuse.ENOENT {
		t.Fatalf("unselected file getattr = %d", code)
	}
	if code := view.Getattr("/chosen.txt", stat, 0); code != 0 || stat.Mode&fuse.S_IFREG == 0 {
		t.Fatalf("selected file getattr = %d, mode %#o", code, stat.Mode)
	}
}

func TestFolderViewIsScopedAndRejectsTraversal(t *testing.T) {
	directory := duplicacy.CreateEntry("selected/subdir/", 0, 0, uint32(os.ModeDir)|0555)
	file := duplicacy.CreateEntry("selected/subdir/file.txt", 3, 0, 0444)
	view := &Dpfs{
		folderRoot: "selected",
		entries: map[string]*duplicacy.Entry{
			"subdir":          directory,
			"subdir/file.txt": file,
		},
		children: map[string][]string{".": {"subdir"}, "subdir": {"file.txt"}},
	}
	if got, valid := view.fusePath("/subdir/file.txt"); !valid || got != "subdir/file.txt" {
		t.Fatalf("selected-folder path = %q, valid=%t", got, valid)
	}
	if code := view.Getattr("/outside", &fuse.Stat_t{}, 0); code != -fuse.ENOENT {
		t.Fatalf("outside path getattr = %d", code)
	}
	if _, valid := view.fusePath("/../outside"); valid {
		t.Fatal("path traversal was accepted")
	}
}

func TestDirectoryHistoryMatchesExactDirectory(t *testing.T) {
	entries := []*duplicacy.Entry{
		duplicacy.CreateEntry("folder/", 0, 0, uint32(os.ModeDir)|0555),
		duplicacy.CreateEntry("folder/file.txt", 1, 0, 0644),
		duplicacy.CreateEntry("folder-old/", 0, 0, uint32(os.ModeDir)|0555),
	}
	if !directoryExists(entries, "folder") {
		t.Fatal("exact directory should be present")
	}
	if directoryExists(entries, "folder-old/subdir") {
		t.Fatal("a nested or similarly prefixed path must not count as the selected directory")
	}
	if !directoryExists(nil, "") {
		t.Fatal("repository root should exist")
	}
}
