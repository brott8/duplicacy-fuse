package dpfs

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/winfsp/cgofuse/fuse"
)

type SnapshotEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

func BrowseSnapshotDirectory(options SelectionOptions, revision int, directory string, passwordPrompt PasswordPrompt) ([]SnapshotEntry, error) {
	selected, err := normalizeSelectedPath(directory)
	if err != nil {
		return nil, err
	}
	cacheDir, err := os.MkdirTemp("", "duplicacy-fuse-browse-cache-")
	if err != nil {
		return nil, fmt.Errorf("create temporary cache for snapshot browser: %w", err)
	}
	defer os.RemoveAll(cacheDir)
	view, err := NewView(MountOptions{
		SelectionOptions: options,
		Revision:         revision,
		CacheDir:         cacheDir,
		CacheBytes:       0,
		PasswordPrompt:   passwordPrompt,
	})
	if err != nil {
		return nil, err
	}
	directoryPath := "/"
	if selected != "" {
		directoryPath += selected
	}
	names := make([]string, 0)
	if code := view.Readdir(directoryPath, func(name string, _ *fuse.Stat_t, _ int64) bool {
		if name != "." && name != ".." {
			names = append(names, name)
		}
		return true
	}, 0, 0); code != 0 {
		return nil, fmt.Errorf("cannot browse %q in revision %d (FUSE error %d)", selected, revision, code)
	}
	entries := make([]SnapshotEntry, 0, len(names))
	for _, name := range names {
		childPath := path.Join(selected, name)
		var stat fuse.Stat_t
		if code := view.Getattr("/"+childPath, &stat, 0); code != 0 {
			return nil, fmt.Errorf("cannot inspect %q in revision %d (FUSE error %d)", childPath, revision, code)
		}
		typeName := "file"
		switch stat.Mode & 0170000 {
		case fuse.S_IFDIR:
			typeName = "directory"
		case fuse.S_IFLNK:
			typeName = "symlink"
		}
		entries = append(entries, SnapshotEntry{Name: name, Path: childPath, Type: typeName, Size: stat.Size})
	}
	sort.Slice(entries, func(i, j int) bool {
		leftDirectory := entries[i].Type == "directory"
		rightDirectory := entries[j].Type == "directory"
		if leftDirectory != rightDirectory {
			return leftDirectory
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}
