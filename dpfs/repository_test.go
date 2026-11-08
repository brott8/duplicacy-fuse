package dpfs

import (
	"os"
	"path/filepath"
	"testing"
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
