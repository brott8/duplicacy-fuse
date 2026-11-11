package dpfs

import (
	"fmt"
	"os"
	"path/filepath"

	duplicacy "github.com/gilbertchen/duplicacy/src"
)

type ConfiguredStorage struct {
	Name       string `json:"name"`
	SnapshotID string `json:"snapshot_id"`
}

type ListedRevision struct {
	Revision  int   `json:"revision"`
	CreatedAt int64 `json:"created_at"`
}

func ListSnapshotIDs(options SelectionOptions, passwordPrompt PasswordPrompt) ([]string, error) {
	options.IgnoreStoredPassword = true
	selection, err := ResolveSelection(options)
	if err != nil {
		return nil, err
	}
	manager, _, _, metadataCacheDir, err := openBackupManager(selection, passwordPrompt)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(metadataCacheDir)
	snapshotIDs, err := manager.SnapshotManager.ListSnapshotIDs()
	if err != nil {
		return nil, fmt.Errorf("list snapshot IDs: %w", err)
	}
	return snapshotIDs, nil
}

func ConfiguredStorages(repository string) ([]ConfiguredStorage, error) {
	if repository == "" {
		return nil, fmt.Errorf("repository path is required")
	}
	absolute, err := filepath.Abs(repository)
	if err != nil {
		return nil, fmt.Errorf("resolve repository path: %w", err)
	}
	if info, err := os.Stat(absolute); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("repository is not a directory: %s", absolute)
	}
	if !duplicacy.LoadPreferences(absolute) {
		return nil, fmt.Errorf("failed to load Duplicacy preferences for repository %s", absolute)
	}
	if _, err := os.Stat(filepath.Join(duplicacy.GetDuplicacyPreferencePath(), "preferences")); err != nil {
		return nil, fmt.Errorf("no Duplicacy preferences found for repository %s", absolute)
	}
	storages := make([]ConfiguredStorage, 0, len(duplicacy.Preferences))
	for _, preference := range duplicacy.Preferences {
		storages = append(storages, ConfiguredStorage{Name: preference.Name, SnapshotID: preference.SnapshotID})
	}
	return storages, nil
}

func ListRevisions(options SelectionOptions, passwordPrompt PasswordPrompt) ([]ListedRevision, error) {
	options.IgnoreStoredPassword = true
	selection, err := ResolveSelection(options)
	if err != nil {
		return nil, err
	}
	manager, _, _, metadataCacheDir, err := openBackupManager(selection, passwordPrompt)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(metadataCacheDir)
	revisions, err := manager.SnapshotManager.ListSnapshotRevisions(selection.SnapshotID)
	if err != nil {
		return nil, fmt.Errorf("list revisions for snapshot %q: %w", selection.SnapshotID, err)
	}
	listed := make([]ListedRevision, 0, len(revisions))
	for _, revision := range revisions {
		snapshot := manager.SnapshotManager.DownloadSnapshot(selection.SnapshotID, revision)
		if snapshot == nil {
			return nil, fmt.Errorf("failed to load revision %d metadata", revision)
		}
		listed = append(listed, ListedRevision{Revision: revision, CreatedAt: snapshot.StartTime})
	}
	return listed, nil
}
