//go:build windows

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// openWithoutDeleteSharing holds path the way antivirus scanners, indexers,
// and Go's own os.Open do: readers and writers may share it, renames may not.
func openWithoutDeleteSharing(t *testing.T, path string) windows.Handle {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil { t.Fatal(err) }
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil { t.Fatal(err) }
	return handle
}

func TestArchiveMoveWaitsForBriefFileLock(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "managed.zip"), filepath.Join(dir, "parked.zip")
	if err := os.WriteFile(from, []byte("archive"), 0o600); err != nil { t.Fatal(err) }
	handle := openWithoutDeleteSharing(t, from)
	released := make(chan struct{})
	go func() { time.Sleep(300 * time.Millisecond); windows.CloseHandle(handle); close(released) }()
	err := renameArchiveNoReplace(from, to)
	<-released
	if err != nil { t.Fatalf("move failed although the lock was released: %v", err) }
	if _, err := os.Stat(to); err != nil { t.Fatal("archive was not moved", err) }
}

func TestArchiveMoveNamesProgramHoldingLastingLock(t *testing.T) {
	previous := archiveMoveLockTimeout
	archiveMoveLockTimeout = 200 * time.Millisecond
	t.Cleanup(func() { archiveMoveLockTimeout = previous })
	dir := t.TempDir()
	from, to := filepath.Join(dir, "managed.zip"), filepath.Join(dir, "parked.zip")
	if err := os.WriteFile(from, []byte("archive"), 0o600); err != nil { t.Fatal(err) }
	handle := openWithoutDeleteSharing(t, from)
	defer windows.CloseHandle(handle)
	err := renameArchiveNoReplace(from, to)
	if err == nil { t.Fatal("moved a file another program holds open") }
	message := err.Error()
	if !strings.Contains(message, from) || !strings.Contains(message, "process "+strconv.Itoa(os.Getpid())) {
		t.Fatalf("error does not name the locked file and its holder: %v", err)
	}
	if _, statErr := os.Stat(from); statErr != nil { t.Fatal("locked archive was disturbed", statErr) }
}

// A mod edited in ModMaker replaces its library file while Play still holds a
// link to the old bytes; the saved version is another link to those bytes, so
// the old deployment is cleaned up without keeping anything back.
func TestOldDeploymentIsCleanedWhenItsBytesAreLinkedElsewhere(t *testing.T) {
	service := newTestAppService(t)
	service.config.LibraryDir = filepath.Join(service.config.DataDir, "library")
	items, collectionID := scanAndCreateCollection(t, service, "Edited mod", 1, 9950)
	ctx := context.Background()
	if _, err := service.activatePlaySelectionDirect(ctx, reviewedStoragePlay(t, service, collectionID)); err != nil { t.Fatal(err) }
	old, err := os.ReadFile(items[0].ArchivePath)
	if err != nil { t.Fatal(err) }
	saved := filepath.Join(service.config.DataDir, "versions", "saved.zip")
	if err := os.MkdirAll(filepath.Dir(saved), 0o755); err != nil { t.Fatal(err) }
	if err := os.Link(items[0].ArchivePath, saved); err != nil { t.Fatal(err) }
	replacement := items[0].ArchivePath + ".new"
	if err := os.WriteFile(replacement, []byte("edited archive bytes"), 0o644); err != nil { t.Fatal(err) }
	if err := os.Remove(items[0].ArchivePath); err != nil { t.Fatal(err) }
	if err := os.Rename(replacement, items[0].ArchivePath); err != nil { t.Fatal(err) }
	if _, err := service.SetCollectionMods(collectionID, []string{items[0].EntityID}, false); err != nil { t.Fatal(err) }
	if _, err := service.activatePlaySelectionDirect(ctx, reviewedStoragePlay(t, service, collectionID)); err != nil { t.Fatal(err) }
	journals, err := service.store.listPendingDeploymentJournals(ctx)
	if err != nil { t.Fatal(err) }
	for _, journal := range journals {
		if journal.Purpose == archivePurposePlay { t.Fatalf("cleanup of a copy that still exists elsewhere was left pending: %s", journal.State) }
	}
	if data, err := os.ReadFile(saved); err != nil || !bytes.Equal(data, old) { t.Fatal("saved version lost its bytes", err) }
	if _, err := os.Stat(filepath.Join(service.config.LibraryDir, "Previous versions")); !os.IsNotExist(err) { t.Fatal("bytes that survive elsewhere were duplicated into the library", err) }
}
