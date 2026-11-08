package dpfs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	duplicacy "github.com/gilbertchen/duplicacy/src"
)

type SelectionOptions struct {
	Repository string
	Storage    string
	SnapshotID string
	Password   string
}

type Selection struct {
	Repository string
	Storage    string
	SnapshotID string
	Password   string
	Preference *duplicacy.Preference
}

func ResolveSelection(options SelectionOptions) (*Selection, error) {
	if strings.TrimSpace(options.Repository) == "" {
		return nil, fmt.Errorf("repository path is required")
	}

	repository, err := filepath.Abs(options.Repository)
	if err != nil {
		return nil, fmt.Errorf("resolve repository path: %w", err)
	}
	if info, err := os.Stat(repository); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("repository is not a directory: %s", repository)
	}

	if !duplicacy.LoadPreferences(repository) {
		return nil, fmt.Errorf("failed to load Duplicacy preferences for repository %s", repository)
	}
	if _, err := os.Stat(filepath.Join(duplicacy.GetDuplicacyPreferencePath(), "preferences")); err != nil {
		return nil, fmt.Errorf("no Duplicacy preferences found for repository %s", repository)
	}
	duplicacy.SetKeyringFile(filepath.Join(duplicacy.GetDuplicacyPreferencePath(), "keyring"))

	var preference *duplicacy.Preference
	if options.Storage != "" {
		preference = duplicacy.FindPreference(options.Storage)
		if preference == nil {
			return nil, fmt.Errorf("storage %q is not configured for repository %s", options.Storage, repository)
		}
	} else {
		if len(duplicacy.Preferences) != 1 {
			return nil, fmt.Errorf("storage must be specified: repository has %d configured storages", len(duplicacy.Preferences))
		}
		preference = &duplicacy.Preferences[0]
	}

	snapshotID := options.SnapshotID
	if snapshotID == "" {
		snapshotID = preference.SnapshotID
	}
	if snapshotID == "" {
		return nil, fmt.Errorf("snapshot ID is not configured; specify one explicitly")
	}

	password := options.Password
	if password == "" {
		password = duplicacy.GetPasswordFromPreference(*preference, "password")
	}
	return &Selection{
		Repository: repository,
		Storage:    preference.Name,
		SnapshotID: snapshotID,
		Password:   password,
		Preference: preference,
	}, nil
}

func cachePathOutsideRepository(repository, cacheDir string) (string, error) {
	if cacheDir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("determine user cache directory: %w", err)
		}
		cacheDir = filepath.Join(base, "duplicacy-fuse", "chunks")
	}
	absoluteCache, err := filepath.Abs(cacheDir)
	if err != nil {
		return "", fmt.Errorf("resolve chunk cache directory: %w", err)
	}
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		return "", fmt.Errorf("resolve repository directory: %w", err)
	}

	candidate := absoluteCache
	var suffix []string
	for {
		_, err := os.Lstat(candidate)
		if err == nil {
			candidate, err = filepath.EvalSymlinks(candidate)
			if err != nil {
				return "", fmt.Errorf("resolve chunk cache path: %w", err)
			}
			break
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect chunk cache path: %w", err)
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", fmt.Errorf("could not resolve chunk cache path %s", absoluteCache)
		}
		suffix = append(suffix, filepath.Base(candidate))
		candidate = parent
	}
	for i := len(suffix) - 1; i >= 0; i-- {
		candidate = filepath.Join(candidate, suffix[i])
	}
	relative, err := filepath.Rel(canonicalRepository, candidate)
	if err != nil {
		return "", fmt.Errorf("compare chunk cache and repository paths: %w", err)
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))) {
		return "", fmt.Errorf("chunk cache must be outside the repository")
	}
	return absoluteCache, nil
}

func openBackupManager(selection *Selection) (*duplicacy.BackupManager, *duplicacy.Config, duplicacy.Storage, string, error) {
	storage := duplicacy.CreateStorage(*selection.Preference, false, 1)
	if storage == nil {
		return nil, nil, nil, "", fmt.Errorf("failed to open storage %q", selection.Storage)
	}
	config, encrypted, err := duplicacy.DownloadConfig(storage, selection.Password)
	if err != nil && encrypted && selection.Password == "" {
		selection.Password = duplicacy.GetPassword(*selection.Preference, "password",
			fmt.Sprintf("Enter password for storage %s:", selection.Storage), false, false)
		config, _, err = duplicacy.DownloadConfig(storage, selection.Password)
	}
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("load storage configuration: %w", err)
	}
	if config == nil {
		return nil, nil, nil, "", fmt.Errorf("storage %q has not been initialized", selection.Storage)
	}
	if strings.HasPrefix(strings.ToLower(selection.Preference.StorageURL), "smb://") {
		preference := *selection.Preference
		storage = &smbReconnectStorage{
			Storage: storage,
			reopen: func() (duplicacy.Storage, error) {
				reconnected := duplicacy.CreateStorage(preference, false, 1)
				if reconnected == nil {
					return nil, fmt.Errorf("failed to reopen SMB storage %q", selection.Storage)
				}
				reconnected.SetNestingLevels(config)
				return reconnected, nil
			},
		}
	}
	manager := duplicacy.CreateBackupManager(selection.SnapshotID, storage, selection.Repository, selection.Password,
		selection.Preference.NobackupFile, selection.Preference.FiltersFile, false)
	if manager == nil {
		return nil, nil, nil, "", fmt.Errorf("failed to create Duplicacy backup manager")
	}
	metadataCacheDir, err := os.MkdirTemp("", "duplicacy-fuse-metadata-")
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("create temporary metadata cache: %w", err)
	}
	previousPreferencePath := duplicacy.GetDuplicacyPreferencePath()
	duplicacy.SetDuplicacyPreferencePath(metadataCacheDir)
	storageKey := sha256.Sum256([]byte(selection.Storage))
	cacheReady := manager.SetupSnapshotCache(hex.EncodeToString(storageKey[:]))
	duplicacy.SetDuplicacyPreferencePath(previousPreferencePath)
	if !cacheReady {
		_ = os.RemoveAll(metadataCacheDir)
		return nil, nil, nil, "", fmt.Errorf("failed to set up temporary Duplicacy metadata cache")
	}
	return manager, config, storage, metadataCacheDir, nil
}

func loadSnapshotEntries(config *duplicacy.Config, storage duplicacy.Storage, snapshot *duplicacy.Snapshot) ([]*duplicacy.Entry, error) {
	if len(snapshot.FileSequence) == 0 {
		return nil, fmt.Errorf("snapshot file sequence is empty")
	}
	operator := duplicacy.CreateChunkOperator(config, storage, nil, false, false, 1, false)
	defer operator.Stop()

	entries := make([]*duplicacy.Entry, 0, snapshot.NumberOfFiles)
	fileCount := int64(0)
	snapshot.ListRemoteFiles(config, operator, func(entry *duplicacy.Entry) bool {
		entries = append(entries, entry)
		if entry.IsFile() {
			fileCount++
		}
		return true
	})
	if len(entries) == 0 {
		return nil, fmt.Errorf("snapshot contains no file entries")
	}
	if snapshot.NumberOfFiles > 0 && fileCount != snapshot.NumberOfFiles {
		return nil, fmt.Errorf("loaded %d snapshot files, expected %d", fileCount, snapshot.NumberOfFiles)
	}
	return entries, nil
}
