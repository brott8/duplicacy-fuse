package dpfs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const cacheMarker = ".duplicacy-fuse-cache"

type chunkFetch struct {
	done chan struct{}
	data []byte
	err  error
}

type chunkCache struct {
	dir      string
	maxBytes int64
	mu       sync.Mutex
	pending  map[string]*chunkFetch
}

func newChunkCache(dir string, maxBytes int64) (*chunkCache, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("cache size must not be negative")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create chunk cache: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("chunk cache must be a real directory")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, fmt.Errorf("secure chunk cache directory: %w", err)
	}
	marker := filepath.Join(dir, cacheMarker)
	markerInfo, err := os.Lstat(marker)
	if os.IsNotExist(err) {
		if err := ioutil.WriteFile(marker, []byte("duplicacy-fuse chunk cache\n"), 0600); err != nil {
			return nil, fmt.Errorf("mark chunk cache directory: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("inspect chunk cache: %w", err)
	} else {
		if !markerInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("refusing to use non-regular cache marker")
		}
		markerData, err := ioutil.ReadFile(marker)
		if err != nil || string(markerData) != "duplicacy-fuse chunk cache\n" {
			return nil, fmt.Errorf("refusing to use unmarked cache directory %s", dir)
		}
	}
	if err := os.Chmod(marker, 0600); err != nil {
		return nil, fmt.Errorf("secure chunk cache marker: %w", err)
	}
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("inspect chunk cache files: %w", err)
	}
	for _, file := range files {
		if file.Mode().IsRegular() && isCacheFile(file.Name()) {
			if err := os.Chmod(filepath.Join(dir, file.Name()), 0600); err != nil {
				return nil, fmt.Errorf("secure cached chunk: %w", err)
			}
		}
	}
	cache := &chunkCache{dir: dir, maxBytes: maxBytes, pending: make(map[string]*chunkFetch)}
	if maxBytes > 0 {
		cache.mu.Lock()
		err := cache.evictLocked()
		cache.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("enforce chunk cache size: %w", err)
		}
	}
	return cache, nil
}

func cacheFileName(dir, key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, hex.EncodeToString(sum[:]))
}

func (cache *chunkCache) Get(key string, fetch func() ([]byte, error)) ([]byte, error) {
	name := cacheFileName(cache.dir, key)
	cache.mu.Lock()
	if cache.maxBytes > 0 {
		if encoded, err := ioutil.ReadFile(name); err == nil {
			if len(encoded) >= sha256.Size {
				data := encoded[sha256.Size:]
				checksum := sha256.Sum256(data)
				if string(checksum[:]) == string(encoded[:sha256.Size]) {
					now := time.Now()
					_ = os.Chtimes(name, now, now)
					cache.mu.Unlock()
					return data, nil
				}
			}
			_ = os.Remove(name)
		}
	}
	if pending := cache.pending[key]; pending != nil {
		cache.mu.Unlock()
		<-pending.done
		return pending.data, pending.err
	}
	pending := &chunkFetch{done: make(chan struct{})}
	cache.pending[key] = pending
	cache.mu.Unlock()

	data, err := safeFetchChunk(fetch)
	cache.mu.Lock()
	if err == nil && cache.maxBytes > 0 && int64(len(data)) <= cache.maxBytes {
		_ = cache.storeLocked(name, data)
	}
	pending.data = data
	pending.err = err
	delete(cache.pending, key)
	close(pending.done)
	cache.mu.Unlock()
	return data, err
}

func safeFetchChunk(fetch func() ([]byte, error)) (data []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("chunk fetch panicked: %v", recovered)
		}
	}()
	return fetch()
}

func (cache *chunkCache) storeLocked(name string, data []byte) error {
	temporary, err := ioutil.TempFile(cache.dir, ".chunk-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	checksum := sha256.Sum256(data)
	if _, err = temporary.Write(checksum[:]); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Close()
	} else {
		_ = temporary.Close()
	}
	if err != nil {
		_ = os.Remove(temporaryName)
		return err
	}
	if err = os.Rename(temporaryName, name); err != nil {
		_ = os.Remove(temporaryName)
		return err
	}
	if err := cache.evictLocked(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

func (cache *chunkCache) evictLocked() error {
	files, err := ioutil.ReadDir(cache.dir)
	if err != nil {
		return err
	}
	type cacheFile struct {
		name string
		size int64
		time time.Time
	}
	var chunks []cacheFile
	var total int64
	for _, file := range files {
		if !file.Mode().IsRegular() || file.Name() == cacheMarker || !isCacheFile(file.Name()) {
			continue
		}
		size := file.Size() - sha256.Size
		if size < 0 {
			size = 0
		}
		total += size
		chunks = append(chunks, cacheFile{name: filepath.Join(cache.dir, file.Name()), size: size, time: file.ModTime()})
	}
	if total <= cache.maxBytes {
		return nil
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].time.Before(chunks[j].time) })
	for _, file := range chunks {
		if total <= cache.maxBytes {
			break
		}
		if err := os.Remove(file.name); err != nil && !os.IsNotExist(err) {
			return err
		}
		total -= file.size
	}
	return nil
}

func isCacheFile(name string) bool {
	if len(name) != 64 {
		return false
	}
	_, err := hex.DecodeString(name)
	return err == nil
}

func ClearChunkCache(dir string) error {
	marker := filepath.Join(dir, cacheMarker)
	directory, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !directory.IsDir() {
		return fmt.Errorf("refusing to clear an invalid cache directory %s", dir)
	}
	markerData, err := ioutil.ReadFile(marker)
	if err != nil || string(markerData) != "duplicacy-fuse chunk cache\n" {
		return fmt.Errorf("refusing to clear an unmarked cache directory %s", dir)
	}
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.Name() == cacheMarker || file.Mode().IsDir() || (!isCacheFile(file.Name()) && !strings.HasPrefix(file.Name(), ".chunk-")) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, file.Name())); err != nil {
			return err
		}
	}
	return nil
}
