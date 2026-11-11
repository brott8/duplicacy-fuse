package dpfs

import (
	"fmt"
	"os"
	"sort"

	duplicacy "github.com/gilbertchen/duplicacy/src"
)

type DirectoryRevision struct {
	Revision int    `json:"revision"`
	Time     int64  `json:"time"`
	Exists   bool   `json:"exists"`
	Path     string `json:"path"`
}

func DirectoryHistory(options SelectionOptions, selectedPath string, revisions []int) ([]DirectoryRevision, error) {
	selected, err := normalizeSelectedPath(selectedPath)
	if err != nil {
		return nil, err
	}
	selection, err := ResolveSelection(options)
	if err != nil {
		return nil, err
	}
	manager, config, storage, metadataCacheDir, err := openBackupManager(selection, nil)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(metadataCacheDir)
	revisions = append([]int(nil), revisions...)
	sort.Ints(revisions)
	result := make([]DirectoryRevision, 0, len(revisions))
	for _, revision := range revisions {
		snapshot := manager.SnapshotManager.DownloadSnapshot(selection.SnapshotID, revision)
		if snapshot == nil {
			return nil, fmt.Errorf("revision %d disappeared while reading history", revision)
		}
		entries, err := loadSnapshotEntries(config, storage, snapshot)
		if err != nil {
			return nil, fmt.Errorf("failed to load directory metadata for revision %d: %w", revision, err)
		}
		exists := directoryExists(entries, selected)
		result = append(result, DirectoryRevision{Revision: revision, Time: snapshot.StartTime, Exists: exists, Path: selected})
	}
	return result, nil
}

func directoryExists(entries []*duplicacy.Entry, selected string) bool {
	if selected == "" {
		return true
	}
	entry := findSnapshotEntry(entries, selected)
	return entry != nil && entry.IsDir()
}
