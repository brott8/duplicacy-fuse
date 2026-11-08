package dpfs

import (
	"io/ioutil"
	"os"
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
