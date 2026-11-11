package dpfs

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestChunkCacheBoundedLRUAndClear(t *testing.T) {
	if err := ClearChunkCache(t.TempDir() + "/not-created"); err != nil {
		t.Fatalf("clearing a missing cache should be a no-op: %v", err)
	}
	dir := t.TempDir()
	cache, err := newChunkCache(dir, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"first", "second"} {
		if _, err := cache.Get(key, func() ([]byte, error) { return []byte("abc"), nil }); err != nil {
			t.Fatal(err)
		}
		if key == "first" {
			old := time.Now().Add(-time.Minute)
			if err := os.Chtimes(cacheFileName(dir, key), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := os.Stat(cacheFileName(dir, "first")); !os.IsNotExist(err) {
		t.Fatalf("oldest cache entry was not evicted: %v", err)
	}
	if _, err := os.Stat(cacheFileName(dir, "second")); err != nil {
		t.Fatalf("newest cache entry was evicted: %v", err)
	}
	if err := ClearChunkCache(dir); err != nil {
		t.Fatal(err)
	}
	files, err := ioutil.ReadDir(dir)
	if err != nil || len(files) != 1 || files[0].Name() != cacheMarker {
		t.Fatalf("cache after clearing = %v, err=%v", files, err)
	}
	if err := ClearChunkCache(t.TempDir()); err == nil {
		t.Fatal("clearing an unmarked directory should fail")
	}
}

func TestChunkCacheEnforcesPrivatePermissions(t *testing.T) {
	dir := t.TempDir() + "/cache"
	if err := os.Mkdir(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	legacyCacheFile := cacheFileName(dir, "old")
	if err := os.WriteFile(legacyCacheFile, []byte("old cache"), 0644); err != nil {
		t.Fatal(err)
	}
	cache, err := newChunkCache(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.storeLocked(cacheFileName(dir, "private"), []byte("plaintext")); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0700 {
		t.Fatalf("cache directory mode = %o, want 700", dirInfo.Mode().Perm())
	}
	for _, name := range []string{filepath.Join(dir, cacheMarker), legacyCacheFile, cacheFileName(dir, "private")} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("cache file %s mode = %o, want 600", name, info.Mode().Perm())
		}
	}
}

func TestChunkCacheRejectsSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	target := root + "/target"
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := root + "/link"
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := newChunkCache(link, 1024); err == nil {
		t.Fatal("cache symlink was accepted")
	}
}

func TestChunkCacheDeduplicatesConcurrentFetches(t *testing.T) {
	cache, err := newChunkCache(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})
	const readers = 12
	var wait sync.WaitGroup
	wait.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wait.Done()
			data, err := cache.Get("same-chunk", func() ([]byte, error) {
				if atomic.AddInt32(&calls, 1) == 1 {
					close(started)
				}
				<-release
				return []byte("chunk"), nil
			})
			if err != nil || string(data) != "chunk" {
				t.Errorf("cache.Get() = %q, %v", data, err)
			}
		}()
	}
	<-started
	close(release)
	wait.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("fetch count = %d, want 1", got)
	}
}

func TestChunkCacheReleasesWaitersAfterFetchPanic(t *testing.T) {
	cache, err := newChunkCache(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get("panic", func() ([]byte, error) { panic("storage failure") }); err == nil {
		t.Fatal("fetch panic did not become an error")
	}
	data, err := cache.Get("panic", func() ([]byte, error) { return []byte("ok"), nil })
	if err != nil || string(data) != "ok" {
		t.Fatalf("retry after panic = %q, %v", data, err)
	}
}
