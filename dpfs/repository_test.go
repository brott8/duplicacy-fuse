package dpfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	duplicacy "github.com/gilbertchen/duplicacy/src"
)

func TestCachePathMustResolveOutsideRepository(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	if err := os.Mkdir(repository, 0700); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(root, "cache", "chunks")
	if got, err := cachePathOutsideRepository(repository, outside); err != nil || got != outside {
		t.Fatalf("outside cache path = %q, %v", got, err)
	}
	if _, err := cachePathOutsideRepository(repository, filepath.Join(repository, "cache")); err == nil {
		t.Fatal("cache path inside repository was accepted")
	}

	link := filepath.Join(root, "cache-link")
	if err := os.Symlink(repository, link); err != nil {
		t.Fatal(err)
	}
	if _, err := cachePathOutsideRepository(repository, filepath.Join(link, "chunks")); err == nil {
		t.Fatal("cache path resolving inside repository was accepted")
	}
}

func TestResolveSelectionLoadsPreferencesAndLibraryPassword(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	preferencePath := filepath.Join(repository, ".duplicacy")
	if err := os.MkdirAll(preferencePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "storage"), 0700); err != nil {
		t.Fatal(err)
	}
	preferences, err := json.Marshal([]duplicacy.Preference{{
		Name:           "nas",
		SnapshotID:     "fixture-id",
		RepositoryPath: repository,
		StorageURL:     filepath.Join(root, "storage"),
		Keys:           map[string]string{"password": "preference-password"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(preferencePath, "preferences"), preferences, 0600); err != nil {
		t.Fatal(err)
	}

	previousPreferences := append([]duplicacy.Preference(nil), duplicacy.Preferences...)
	t.Cleanup(func() {
		duplicacy.Preferences = previousPreferences
		duplicacy.SetDuplicacyPreferencePath("")
	})
	t.Setenv("DUPLICACY_NAS_PASSWORD", "test-storage-password")

	selection, err := ResolveSelection(SelectionOptions{Repository: repository})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Storage != "nas" || selection.SnapshotID != "fixture-id" {
		t.Fatalf("resolved storage/snapshot = %q/%q", selection.Storage, selection.SnapshotID)
	}
	if selection.Password != "test-storage-password" {
		t.Fatalf("resolved password = %q, want library environment lookup", selection.Password)
	}
	storages, err := ConfiguredStorages(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(storages) != 1 || storages[0].Name != "nas" || storages[0].SnapshotID != "fixture-id" {
		t.Fatalf("configured storages = %#v", storages)
	}

	selection, err = ResolveSelection(SelectionOptions{Repository: repository, IgnoreStoredPassword: true})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Password != "" {
		t.Fatalf("password with IgnoreStoredPassword = %q, want empty", selection.Password)
	}
	if !selection.IgnoreStoredPassword {
		t.Fatal("selection did not retain IgnoreStoredPassword policy")
	}
}

func TestEncryptedStoragePasswordPromptIgnoresSavedValuesAndRetries(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	preferencePath := filepath.Join(repository, ".duplicacy")
	storagePath := filepath.Join(root, "storage")
	for _, directory := range []string{preferencePath, storagePath} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	preferences, err := json.Marshal([]duplicacy.Preference{{
		Name:           "nas",
		SnapshotID:     "fixture-id",
		RepositoryPath: repository,
		StorageURL:     storagePath,
		Keys:           map[string]string{"password": "saved-password"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(preferencePath, "preferences"), preferences, 0600); err != nil {
		t.Fatal(err)
	}
	previousPreferences := append([]duplicacy.Preference(nil), duplicacy.Preferences...)
	t.Cleanup(func() {
		duplicacy.Preferences = previousPreferences
		duplicacy.SetDuplicacyPreferencePath("")
	})
	if !duplicacy.LoadPreferences(repository) {
		t.Fatal("failed to load test preferences")
	}
	preference := duplicacy.Preferences[0]
	storage := duplicacy.CreateStorage(preference, false, 1)
	if storage == nil {
		t.Fatal("failed to create local test storage")
	}
	config := duplicacy.CreateConfigFromParameters(100, 64*1024, 128*1024, 32*1024, false, nil, false)
	if !duplicacy.UploadConfig(storage, config, "correct-password", 1000) {
		t.Fatal("failed to create encrypted test storage")
	}
	t.Setenv("DUPLICACY_NAS_PASSWORD", "environment-password")

	selection, err := ResolveSelection(SelectionOptions{Repository: repository, Storage: "nas", IgnoreStoredPassword: true})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Password != "" {
		t.Fatalf("selected password = %q, want empty", selection.Password)
	}
	attempts := 0
	manager, _, _, metadataCacheDir, err := openBackupManager(selection, func(attempt int, previousError error) (string, error) {
		attempts++
		if attempt != attempts || previousError == nil {
			t.Fatalf("prompt attempt/error = %d/%v", attempt, previousError)
		}
		if attempt == 1 {
			return "wrong-password", nil
		}
		return "correct-password", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if manager == nil || attempts != 2 {
		t.Fatalf("manager=%v prompt attempts=%d, want manager and two prompts", manager != nil, attempts)
	}
	if err := os.RemoveAll(metadataCacheDir); err != nil {
		t.Fatal(err)
	}
}
