package dpfs

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	duplicacy "github.com/gilbertchen/duplicacy/src"
	"github.com/winfsp/cgofuse/fuse"
)

type MountOptions struct {
	SelectionOptions
	Revision     int
	SelectedPath string
	CacheDir     string
	CacheBytes   int64
}

type readRange struct {
	index        int
	chunkStart   int
	chunkEnd     int
	bufferOffset int
}

// NewView downloads only the selected revision's manifest and chunk index.
func NewView(options MountOptions) (*Dpfs, error) {
	if options.Revision < 1 {
		return nil, fmt.Errorf("revision must be a positive integer")
	}
	selected, err := normalizeSelectedPath(options.SelectedPath)
	if err != nil {
		return nil, err
	}
	selection, err := ResolveSelection(options.SelectionOptions)
	if err != nil {
		return nil, err
	}
	cacheDir, err := cachePathOutsideRepository(selection.Repository, options.CacheDir)
	if err != nil {
		return nil, err
	}
	manager, config, storage, metadataCacheDir, err := openBackupManager(selection)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(metadataCacheDir)
	snapshot := manager.SnapshotManager.DownloadSnapshot(selection.SnapshotID, options.Revision)
	if snapshot == nil {
		return nil, fmt.Errorf("revision %d was not found", options.Revision)
	}
	if !manager.SnapshotManager.DownloadSnapshotSequence(snapshot, "chunks") ||
		!manager.SnapshotManager.DownloadSnapshotSequence(snapshot, "lengths") {
		return nil, fmt.Errorf("failed to load metadata for revision %d", options.Revision)
	}
	entries, err := loadSnapshotEntries(config, storage, snapshot)
	if err != nil {
		return nil, err
	}

	cache, err := newChunkCache(cacheDir, options.CacheBytes)
	if err != nil {
		return nil, err
	}
	view := &Dpfs{
		storage:    storage,
		config:     config,
		preference: selection.Preference,
		repository: selection.Repository,
		snapshot:   snapshot,
		entries:    make(map[string]*duplicacy.Entry),
		children:   make(map[string][]string),
		cache:      cache,
	}
	entry := findSnapshotEntry(entries, selected)
	isDir := selected == "" || (entry != nil && entry.IsDir())
	if isDir {
		view.folderRoot = selected
		if selected != "" {
			if entry == nil || !entry.IsDir() {
				return nil, fmt.Errorf("selected directory %q does not exist in revision %d", selected, options.Revision)
			}
		}
	} else {
		if entry == nil || !entry.IsFile() {
			return nil, fmt.Errorf("selected file %q does not exist in revision %d", selected, options.Revision)
		}
		view.fileRoot = strings.TrimSuffix(path.Base(selected), "/")
		view.entries[view.fileRoot] = entry
		view.children["."] = []string{view.fileRoot}
	}

	if isDir {
		for _, entry := range entries {
			name := strings.TrimSuffix(entry.Path, "/")
			if name == "" || name == view.folderRoot {
				continue
			}
			if view.folderRoot != "" {
				prefix := view.folderRoot + "/"
				if !strings.HasPrefix(name, prefix) {
					continue
				}
				name = strings.TrimPrefix(name, prefix)
			}
			view.entries[name] = entry
			parent := path.Dir(name)
			view.children[parent] = append(view.children[parent], path.Base(name))
		}
		for parent := range view.children {
			sort.Strings(view.children[parent])
		}
	}
	return view, nil
}

func normalizeSelectedPath(input string) (string, error) {
	if input == "" || input == "." {
		return "", nil
	}
	if strings.HasPrefix(input, "/") {
		return "", fmt.Errorf("selected path must be relative to the repository")
	}
	clean := path.Clean(input)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("selected path escapes the repository")
	}
	if clean == "." {
		return "", nil
	}
	return clean, nil
}

func findSnapshotEntry(entries []*duplicacy.Entry, name string) *duplicacy.Entry {
	for _, entry := range entries {
		if strings.TrimSuffix(entry.Path, "/") == name {
			return entry
		}
	}
	return nil
}

func planReadRanges(snapshot *duplicacy.Snapshot, entry *duplicacy.Entry, offset int64, length int) ([]readRange, error) {
	if offset < 0 || length < 0 {
		return nil, fmt.Errorf("negative read range")
	}
	if length == 0 || offset >= entry.Size {
		return nil, nil
	}
	if entry.StartChunk < 0 || entry.EndChunk < entry.StartChunk || entry.EndChunk >= len(snapshot.ChunkLengths) ||
		entry.EndChunk >= len(snapshot.ChunkHashes) {
		return nil, fmt.Errorf("invalid chunk range for %s", entry.Path)
	}
	if int64(length) > entry.Size-offset {
		length = int(entry.Size - offset)
	}
	requestEnd := offset + int64(length)
	var ranges []readRange
	var fileOffset int64
	for index := entry.StartChunk; index <= entry.EndChunk; index++ {
		chunkStart := 0
		chunkEnd := snapshot.ChunkLengths[index]
		if index == entry.StartChunk {
			chunkStart = entry.StartOffset
		}
		if index == entry.EndChunk {
			chunkEnd = entry.EndOffset
		}
		if chunkStart < 0 || chunkEnd < chunkStart || chunkEnd > snapshot.ChunkLengths[index] {
			return nil, fmt.Errorf("invalid chunk offsets for %s", entry.Path)
		}
		segmentEnd := fileOffset + int64(chunkEnd-chunkStart)
		start := offset
		if start < fileOffset {
			start = fileOffset
		}
		end := requestEnd
		if end > segmentEnd {
			end = segmentEnd
		}
		if start < end {
			ranges = append(ranges, readRange{
				index:        index,
				chunkStart:   chunkStart + int(start-fileOffset),
				chunkEnd:     chunkStart + int(end-fileOffset),
				bufferOffset: int(start - offset),
			})
		}
		fileOffset = segmentEnd
	}
	if fileOffset != entry.Size {
		return nil, fmt.Errorf("chunk lengths for %s total %d bytes, expected %d", entry.Path, fileOffset, entry.Size)
	}
	return ranges, nil
}

func (view *Dpfs) lookup(name string) (*duplicacy.Entry, bool) {
	if name == "." || name == "" {
		return nil, true
	}
	if view.fileRoot != "" {
		if name == view.fileRoot {
			entry, ok := view.entries[name]
			return entry, ok
		}
		return nil, false
	}
	entry, ok := view.entries[name]
	return entry, ok
}

func (view *Dpfs) fusePath(name string) (string, bool) {
	if name == "/" || name == "" {
		return ".", true
	}
	for _, component := range strings.Split(name, "/") {
		if component == ".." {
			return "", false
		}
	}
	clean := path.Clean("/" + strings.TrimPrefix(name, "/"))
	if clean == "/" {
		return ".", true
	}
	relative := strings.TrimPrefix(clean, "/")
	if view.fileRoot != "" {
		if relative == view.fileRoot {
			return relative, true
		}
		return "", false
	}
	if view.folderRoot != "" {
		return relative, true
	}
	return relative, true
}

func (view *Dpfs) Init() {
}

func (view *Dpfs) Destroy() {
}

func (view *Dpfs) Statfs(name string, stat *fuse.Statfs_t) int {
	if stat == nil {
		return -fuse.EINVAL
	}
	stat.Bsize = 4096
	stat.Frsize = 4096
	stat.Namemax = 255
	if view.snapshot != nil {
		stat.Blocks = uint64((view.snapshot.FileSize + 4095) / 4096)
		stat.Files = uint64(view.snapshot.NumberOfFiles)
	}
	return 0
}

func (view *Dpfs) Getattr(name string, stat *fuse.Stat_t, fh uint64) int {
	if stat == nil {
		return -fuse.EINVAL
	}
	relative, valid := view.fusePath(name)
	if !valid {
		return -fuse.ENOENT
	}
	if relative == "." {
		stat.Mode = fuse.S_IFDIR | 0555
		stat.Nlink = 2
		stat.Mtim = fuse.NewTimespec(time.Unix(0, 0))
		return 0
	}
	entry, found := view.lookup(relative)
	if !found || entry == nil {
		return -fuse.ENOENT
	}
	mode := uint32(entry.GetPermissions()) & 0555
	switch {
	case entry.IsDir():
		stat.Mode = fuse.S_IFDIR | mode
		stat.Nlink = 2
	case entry.IsLink():
		stat.Mode = fuse.S_IFLNK | mode
		stat.Nlink = 1
		stat.Size = int64(len(entry.Link))
	default:
		stat.Mode = fuse.S_IFREG | mode
		stat.Nlink = 1
		stat.Size = entry.Size
	}
	stat.Mtim = fuse.NewTimespec(time.Unix(entry.Time, 0))
	stat.Ctim = stat.Mtim
	stat.Atim = stat.Mtim
	if entry.UID >= 0 {
		stat.Uid = uint32(entry.UID)
	}
	if entry.GID >= 0 {
		stat.Gid = uint32(entry.GID)
	}
	return 0
}

func (view *Dpfs) Readdir(name string, fill func(name string, stat *fuse.Stat_t, ofst int64) bool, ofst int64, fh uint64) int {
	relative, valid := view.fusePath(name)
	if !valid {
		return -fuse.ENOENT
	}
	if relative != "." {
		entry, found := view.lookup(relative)
		if !found || entry == nil || !entry.IsDir() {
			return -fuse.ENOTDIR
		}
	}
	entries := append([]string{".", ".."}, view.children[relative]...)
	if ofst < 0 {
		return -fuse.EINVAL
	}
	for index := int(ofst); index < len(entries); index++ {
		if !fill(entries[index], nil, int64(index+1)) {
			break
		}
	}
	return 0
}

func (view *Dpfs) Open(name string, flags int) (int, uint64) {
	if flags&3 != 0 || flags&0x200 != 0 {
		return -fuse.EROFS, 0
	}
	relative, valid := view.fusePath(name)
	if !valid {
		return -fuse.ENOENT, 0
	}
	if relative == "." {
		return -fuse.EISDIR, 0
	}
	entry, found := view.lookup(relative)
	if !found || entry == nil {
		return -fuse.ENOENT, 0
	}
	if entry.IsDir() {
		return -fuse.EISDIR, 0
	}
	if entry.IsLink() {
		return -fuse.ELOOP, 0
	}
	if entry.GetPermissions()&0444 == 0 {
		return -fuse.EACCES, 0
	}
	return 0, 0
}

func (view *Dpfs) Read(name string, buff []byte, offset int64, fh uint64) int {
	if offset < 0 {
		return -fuse.EINVAL
	}
	relative, valid := view.fusePath(name)
	if !valid {
		return -fuse.ENOENT
	}
	entry, found := view.lookup(relative)
	if !found || entry == nil {
		return -fuse.ENOENT
	}
	if entry.IsDir() {
		return -fuse.EISDIR
	}
	if entry.IsLink() {
		return -fuse.ELOOP
	}
	if !entry.IsFile() {
		return -fuse.EINVAL
	}
	if offset >= entry.Size || len(buff) == 0 {
		return 0
	}
	remaining := entry.Size - offset
	if int64(len(buff)) > remaining {
		buff = buff[:int(remaining)]
	}
	ranges, err := planReadRanges(view.snapshot, entry, offset, len(buff))
	if err != nil {
		return -fuse.EIO
	}
	for _, read := range ranges {
		chunkHash := view.snapshot.ChunkHashes[read.index]
		key := view.repository + "\x00" + view.preference.StorageURL + "\x00" + chunkHash
		data, err := view.cache.Get(key, func() ([]byte, error) {
			view.mu.Lock()
			defer view.mu.Unlock()
			return fetchChunk(view.config, view.storage, chunkHash)
		})
		if err != nil || len(data) != view.snapshot.ChunkLengths[read.index] {
			if read.bufferOffset > 0 {
				return read.bufferOffset
			}
			return -fuse.EIO
		}
		length := read.chunkEnd - read.chunkStart
		copy(buff[read.bufferOffset:read.bufferOffset+length], data[read.chunkStart:read.chunkEnd])
	}
	return len(buff)
}

func (view *Dpfs) Readlink(name string) (int, string) {
	relative, valid := view.fusePath(name)
	if !valid {
		return -fuse.ENOENT, ""
	}
	entry, found := view.lookup(relative)
	if !found || entry == nil || !entry.IsLink() {
		return -fuse.EINVAL, ""
	}
	return 0, entry.Link
}

func (view *Dpfs) Access(name string, mask uint32) int {
	if mask&2 != 0 {
		return -fuse.EROFS
	}
	relative, valid := view.fusePath(name)
	if !valid {
		return -fuse.ENOENT
	}
	if relative == "." {
		return 0
	}
	entry, found := view.lookup(relative)
	if !found || entry == nil {
		return -fuse.ENOENT
	}
	permissions := uint32(entry.GetPermissions())
	if mask&4 != 0 && permissions&0444 == 0 || mask&1 != 0 && permissions&0111 == 0 {
		return -fuse.EACCES
	}
	return 0
}

func (view *Dpfs) Write(name string, buff []byte, offset int64, fh uint64) int {
	return -fuse.EROFS
}

func (view *Dpfs) Create(name string, flags int, mode uint32) (int, uint64) {
	return -fuse.EROFS, 0
}

func (view *Dpfs) Opendir(name string) (int, uint64) {
	relative, valid := view.fusePath(name)
	if !valid {
		return -fuse.ENOENT, 0
	}
	if relative != "." {
		entry, found := view.lookup(relative)
		if !found || entry == nil {
			return -fuse.ENOENT, 0
		}
		if !entry.IsDir() {
			return -fuse.ENOTDIR, 0
		}
	}
	return 0, 0
}

func (view *Dpfs) Releasedir(name string, fh uint64) int                         { return 0 }
func (view *Dpfs) Release(name string, fh uint64) int                            { return 0 }
func (view *Dpfs) Flush(name string, fh uint64) int                              { return 0 }
func (view *Dpfs) Fsync(name string, datasync bool, fh uint64) int               { return 0 }
func (view *Dpfs) Fsyncdir(name string, datasync bool, fh uint64) int            { return 0 }
func (view *Dpfs) Mkdir(name string, mode uint32) int                            { return -fuse.EROFS }
func (view *Dpfs) Mknod(name string, mode uint32, dev uint64) int                { return -fuse.EROFS }
func (view *Dpfs) Unlink(name string) int                                        { return -fuse.EROFS }
func (view *Dpfs) Rmdir(name string) int                                         { return -fuse.EROFS }
func (view *Dpfs) Symlink(target string, name string) int                        { return -fuse.EROFS }
func (view *Dpfs) Rename(oldname string, newname string) int                     { return -fuse.EROFS }
func (view *Dpfs) Link(oldname string, newname string) int                       { return -fuse.EROFS }
func (view *Dpfs) Chmod(name string, mode uint32) int                            { return -fuse.EROFS }
func (view *Dpfs) Chown(name string, uid uint32, gid uint32) int                 { return -fuse.EROFS }
func (view *Dpfs) Truncate(name string, size int64, fh uint64) int               { return -fuse.EROFS }
func (view *Dpfs) Utimens(name string, times []fuse.Timespec) int                { return -fuse.EROFS }
func (view *Dpfs) Setxattr(name string, attr string, data []byte, flags int) int { return -fuse.EROFS }
func (view *Dpfs) Removexattr(name string, attr string) int                      { return -fuse.EROFS }
