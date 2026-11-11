package dpfs

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	duplicacy "github.com/gilbertchen/duplicacy/src"
)

func TestEmbeddedLibraryReadsLocalSnapshot(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repo")
	preferencePath := filepath.Join(repository, ".duplicacy")
	storagePath := filepath.Join(root, "storage")
	cachePath := filepath.Join(root, "cache")
	for _, directory := range []string{preferencePath, storagePath} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	const snapshotID = "fixture-repo"
	fileContents := make([]byte, 512*1024+173)
	for index := range fileContents {
		fileContents[index] = byte((index*31 + index/127) % 251)
	}
	if err := os.WriteFile(filepath.Join(repository, "payload.bin"), fileContents, 0600); err != nil {
		t.Fatal(err)
	}
	preferences, err := json.Marshal([]duplicacy.Preference{{
		Name:           "fixture",
		SnapshotID:     snapshotID,
		RepositoryPath: repository,
		StorageURL:     storagePath,
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
		t.Fatal("failed to load fixture preferences")
	}
	preference := duplicacy.Preferences[0]
	storage := duplicacy.CreateStorage(preference, false, 1)
	if storage == nil {
		t.Fatal("failed to open fixture storage")
	}
	config := duplicacy.CreateConfigFromParameters(100, 64*1024, 128*1024, 32*1024, false, nil, false)
	if !duplicacy.UploadConfig(storage, config, "", 0) {
		t.Fatal("failed to initialize fixture storage")
	}
	manager := duplicacy.CreateBackupManager(snapshotID, storage, repository, "", "", "", false)
	if manager == nil || !manager.SetupSnapshotCache(preference.Name) {
		t.Fatal("failed to initialize fixture snapshot cache")
	}
	if !manager.Backup(repository, false, 1, "fixture", false, false, 0, false, 1024, 1024) {
		t.Fatal("failed to create fixture snapshot")
	}

	prompted := false
	revisions, err := ListRevisions(SelectionOptions{Repository: repository, Storage: preference.Name}, func(int, error) (string, error) {
		prompted = true
		return "unexpected", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if prompted {
		t.Fatal("unencrypted storage prompted for a password")
	}
	if len(revisions) != 1 || revisions[0].Revision != 1 || revisions[0].CreatedAt == 0 {
		t.Fatalf("listed revisions = %#v, want revision 1 with a creation time", revisions)
	}
	snapshotIDs, err := ListSnapshotIDs(SelectionOptions{Repository: repository, Storage: preference.Name}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshotIDs) != 1 || snapshotIDs[0] != preference.SnapshotID {
		t.Fatalf("listed snapshot IDs = %#v, want %q", snapshotIDs, preference.SnapshotID)
	}

	view, err := NewView(MountOptions{
		SelectionOptions: SelectionOptions{Repository: repository, Storage: preference.Name},
		Revision:         1,
		SelectedPath:     "payload.bin",
		CacheDir:         cachePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(cachePath)

	if len(view.snapshot.ChunkHashes) < 2 {
		t.Fatalf("fixture snapshot has %d chunks, want a multi-chunk file", len(view.snapshot.ChunkHashes))
	}
	buffer := make([]byte, len(fileContents))
	if read := view.Read("/payload.bin", buffer, 0, 0); read != len(buffer) {
		t.Fatalf("read returned %d bytes, want %d", read, len(buffer))
	}
	if !bytes.Equal(buffer, fileContents) {
		t.Fatal("snapshot content did not match the source bytes")
	}

	const offset = 123456
	partial := make([]byte, 170000)
	if read := view.Read("/payload.bin", partial, offset, 0); read != len(partial) {
		t.Fatalf("partial read returned %d bytes, want %d", read, len(partial))
	}
	if !bytes.Equal(partial, fileContents[offset:offset+len(partial)]) {
		t.Fatal("cross-chunk random read did not match the source bytes")
	}
}
