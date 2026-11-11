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
}
