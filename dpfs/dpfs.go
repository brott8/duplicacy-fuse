package dpfs

import (
	"io"
	"sync"

	duplicacy "github.com/gilbertchen/duplicacy/src"
	"github.com/winfsp/cgofuse/fuse"
)

// Dpfs is the Duplicacy filesystem type. This type satisfies the fuse.FileSystemInterface interace
type Dpfs struct {
	fuse.FileSystemBase
	config              *duplicacy.Config
	storage             duplicacy.Storage
	preference          *duplicacy.Preference
	repository          string
	mu                  sync.Mutex
	metadataMu          sync.RWMutex
	entries             map[string]*duplicacy.Entry
	children            map[string][]string
	loadedDirs          map[string]bool
	lazyMetadata        bool
	legacyMetadata      bool
	metadataComplete    bool
	debug               bool
	debugWriter         io.Writer
	listSnapshotEntries func(func(*duplicacy.Entry) bool)
	folderRoot          string
	fileRoot            string
	snapshot            *duplicacy.Snapshot
	cache               *chunkCache
}

var _ fuse.FileSystemInterface = (*Dpfs)(nil)
