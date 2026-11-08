package dpfs

import (
	"path"
	"sort"
	"strings"

	duplicacy "github.com/gilbertchen/duplicacy/src"
)

func (view *Dpfs) ensureDirectoryLoaded(directory string) error {
	if !view.lazyMetadata {
		return nil
	}
	if directory == "" {
		directory = "."
	}
	view.metadataMu.Lock()
	defer view.metadataMu.Unlock()
	return view.ensureDirectoryLoadedLocked(directory)
}

func (view *Dpfs) ensureDirectoryLoadedLocked(directory string) error {
	if view.metadataComplete || view.loadedDirs[directory] {
		return nil
	}
	if directory != "." {
		parent := path.Dir(directory)
		if err := view.ensureDirectoryLoadedLocked(parent); err != nil {
			return err
		}
		entry, found := view.entries[directory]
		if !found || !entry.IsDir() {
			return nil
		}
	}

	if view.legacyMetadata {
		// Legacy JSON snapshots are sorted by Duplicacy only after the full list is decoded.
		view.listRemoteEntries(func(entry *duplicacy.Entry) bool {
			view.addEntry(entry)
			return true
		})
		view.metadataComplete = true
		view.loadedDirs["."] = true
		for name, entry := range view.entries {
			if entry.IsDir() {
				view.loadedDirs[name] = true
			}
		}
		for parent := range view.children {
			view.loadedDirs[parent] = true
		}
		for parent := range view.children {
			sort.Strings(view.children[parent])
		}
		return nil
	}

	// Current snapshots are emitted directory-by-directory, so stop as soon as
	// the first descendant shows that this directory's direct children are complete.
	seenDirectory := directory == "."
	view.listRemoteEntries(func(entry *duplicacy.Entry) bool {
		name := strings.TrimSuffix(entry.Path, "/")
		if name == "" {
			return true
		}

		if directory == "." {
			if strings.Contains(name, "/") {
				return false
			}
			view.addEntry(entry)
			return true
		}

		insideDirectory := name == directory || strings.HasPrefix(name, directory+"/")
		if !insideDirectory {
			return !seenDirectory
		}
		if name == directory {
			view.addEntry(entry)
			return true
		}
		seenDirectory = true
		relative := strings.TrimPrefix(name, directory+"/")
		if strings.Contains(relative, "/") {
			return false
		}
		view.addEntry(entry)
		return true
	})

	view.loadedDirs[directory] = true
	sort.Strings(view.children[directory])
	return nil
}

func (view *Dpfs) listRemoteEntries(entryOut func(*duplicacy.Entry) bool) {
	if view.listSnapshotEntries != nil {
		view.listSnapshotEntries(entryOut)
		return
	}
	operator := duplicacy.CreateChunkOperator(view.config, view.storage, nil, false, false, 1, false)
	defer operator.Stop()
	view.snapshot.ListRemoteFiles(view.config, operator, entryOut)
}

func (view *Dpfs) addEntry(entry *duplicacy.Entry) {
	name := strings.TrimSuffix(entry.Path, "/")
	if name == "" {
		return
	}
	if _, exists := view.entries[name]; exists {
		return
	}
	view.entries[name] = entry
	parent := path.Dir(name)
	view.children[parent] = append(view.children[parent], path.Base(name))
}
