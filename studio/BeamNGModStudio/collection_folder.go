package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// CollectionFolder describes the on-disk mirror of one collection: a plain
// folder holding every resolved mod archive. Entries are hard links into the
// library (symlink, then copy, as fallbacks) so the mirror costs no extra disk
// space yet can be zipped, copied, or dragged out like any other folder.
type CollectionFolder struct {
	CollectionID string   `json:"collectionId"`
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	ModCount     int      `json:"modCount"`
	AddedCount   int      `json:"addedCount"`
	CopiedCount  int      `json:"copiedCount"`
	RemovedCount int      `json:"removedCount"`
	SkippedMods  []string `json:"skippedMods"`
}

const collectionFolderDirectory = "collections"

// OpenCollectionFolder refreshes the collection's folder mirror and opens it in
// the native file manager.
func (service *AppService) OpenCollectionFolder(collectionID string) (CollectionFolder, error) {
	ctx := context.Background()
	folder, err := service.syncCollectionFolder(ctx, collectionID)
	if err != nil {
		return CollectionFolder{}, err
	}
	if err := openFolderInFileManager(folder.Path); err != nil {
		return CollectionFolder{}, fmt.Errorf("open %s in the file manager: %w", folder.Path, err)
	}
	_ = service.store.AppendEvent(ctx, "", "collection_folder_opened", map[string]any{
		"collectionId": folder.CollectionID, "path": folder.Path, "modCount": folder.ModCount,
		"added": folder.AddedCount, "copied": folder.CopiedCount, "removed": folder.RemovedCount,
		"skipped": len(folder.SkippedMods),
	})
	return folder, nil
}

// syncCollectionFolder makes the folder match the collection's resolved
// selection: it is incremental, so reopening a large collection only touches
// what changed. The folder is keyed by collection ID, so two collections that
// share a name never share a folder, and a rename renames the folder.
func (service *AppService) syncCollectionFolder(ctx context.Context, collectionID string) (CollectionFolder, error) {
	id := strings.TrimSpace(collectionID)
	if id == "" {
		return CollectionFolder{}, errors.New("collection id is required")
	}
	if strings.TrimSpace(service.config.ExportDir) == "" {
		return CollectionFolder{}, errors.New("export directory is not configured")
	}
	service.collectionFolderMu.Lock()
	defer service.collectionFolderMu.Unlock()

	detail, err := service.store.CollectionDetail(ctx, id)
	if err != nil {
		return CollectionFolder{}, err
	}
	// The Play resolver is the authority on what a collection ships: it walks
	// enabled child edges and drops archived mods.
	selection, err := service.store.ResolvePlaySelection(ctx, []string{id})
	if err != nil {
		return CollectionFolder{}, err
	}

	label := sanitizeArchiveLabel(detail.Collection.Name)
	if label == "" {
		label = "collection"
	}
	root := filepath.Join(service.config.ExportDir, collectionFolderDirectory, shortPlayID(id))
	target := filepath.Join(root, label)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return CollectionFolder{}, fmt.Errorf("prepare collection folder: %w", err)
	}
	// Renames leave the previous label behind. Everything under this root
	// belongs to this collection, so the stale folder goes.
	if entries, readErr := os.ReadDir(root); readErr == nil {
		for _, entry := range entries {
			if entry.Name() == label {
				continue
			}
			_ = os.RemoveAll(filepath.Join(root, entry.Name()))
		}
	}

	folder := CollectionFolder{CollectionID: id, Name: detail.Collection.Name, Path: target}
	wanted := make(map[string]CollectionMod, len(selection.Mods))
	for _, mod := range selection.Mods {
		if !mod.Available || cleanOptionalPath(mod.ArchivePath) == "" {
			folder.SkippedMods = append(folder.SkippedMods, mod.DisplayName)
			continue
		}
		wanted[collectionFolderEntryName(mod, wanted)] = mod
	}
	folder.ModCount = len(wanted)

	entries, err := os.ReadDir(target)
	if err != nil {
		return CollectionFolder{}, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		mod, keep := wanted[name]
		if keep && collectionFolderEntryCurrent(filepath.Join(target, name), cleanOptionalPath(mod.ArchivePath)) {
			delete(wanted, name)
			continue
		}
		if !keep && !isModArchive(name) {
			// Anything the user put here that is not a mod archive stays.
			continue
		}
		if err := os.Remove(filepath.Join(target, name)); err != nil {
			return CollectionFolder{}, fmt.Errorf("replace %s: %w", name, err)
		}
		if !keep {
			folder.RemovedCount++
		}
	}

	for name, mod := range wanted {
		copied, err := linkArchiveIntoFolder(cleanOptionalPath(mod.ArchivePath), filepath.Join(target, name))
		if err != nil {
			return CollectionFolder{}, fmt.Errorf("add %s: %w", mod.DisplayName, err)
		}
		folder.AddedCount++
		if copied {
			folder.CopiedCount++
		}
	}
	return folder, nil
}

// openFolderInFileManager hands the folder to the running app's environment
// when there is one. The direct command is only a fallback, and it must not
// wait on the process: explorer.exe exits 1 even after opening the window.
func openFolderInFileManager(path string) error {
	if app := application.Get(); app != nil && app.Env != nil {
		return app.Env.OpenFileManager(path, false)
	}
	executable, arguments := nativeFileManagerFolderCommand(runtime.GOOS, path)
	command := exec.Command(executable, arguments...)
	if err := command.Start(); err != nil {
		return fmt.Errorf("%s: %w", executable, err)
	}
	return command.Process.Release()
}

// linkArchiveIntoFolder prefers a hard link so the mirror is free and the file
// reads as a normal archive; symlinks need a privilege Windows may withhold and
// a copy needs the disk space, so both are fallbacks. Reports whether the
// archive had to be copied.
func linkArchiveIntoFolder(source, destination string) (bool, error) {
	if err := os.Link(source, destination); err == nil {
		return false, nil
	}
	if err := os.Symlink(source, destination); err == nil {
		return false, nil
	}
	if err := copyFileAtomic(source, destination); err != nil {
		return false, err
	}
	return true, nil
}

// collectionFolderEntryCurrent reports whether an existing entry already mirrors
// the source. Links share an inode; copies only match on size and are never
// older than the archive they came from.
func collectionFolderEntryCurrent(destination, source string) bool {
	destinationInfo, err := os.Stat(destination)
	if err != nil {
		return false
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return false
	}
	if os.SameFile(destinationInfo, sourceInfo) {
		return true
	}
	return destinationInfo.Size() == sourceInfo.Size() && !destinationInfo.ModTime().Before(sourceInfo.ModTime())
}

// collectionFolderEntryName keeps the library file name so the folder looks
// like a normal mods folder, and only disambiguates real collisions.
func collectionFolderEntryName(mod CollectionMod, taken map[string]CollectionMod) string {
	base := filepath.Base(cleanOptionalPath(mod.ArchivePath))
	if _, exists := taken[base]; !exists {
		return base
	}
	extension := filepath.Ext(base)
	stem := strings.TrimSuffix(base, extension)
	candidate := fmt.Sprintf("%s-%s%s", stem, shortPlayID(mod.EntityID), extension)
	for index := 2; ; index++ {
		if _, exists := taken[candidate]; !exists {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%s-%d%s", stem, shortPlayID(mod.EntityID), index, extension)
	}
}
