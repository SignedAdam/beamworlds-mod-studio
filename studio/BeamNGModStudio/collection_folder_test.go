package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncCollectionFolderMirrorsResolvedMods(t *testing.T) {
	service := newTestAppService(t)
	items, collectionID := scanAndCreateCollection(t, service, "Export Me", 3, 9100)

	folder, err := service.syncCollectionFolder(context.Background(), collectionID)
	if err != nil {
		t.Fatal(err)
	}
	if folder.ModCount != 3 || folder.AddedCount != 3 || folder.CopiedCount != 0 {
		t.Fatalf("mirror counts = %+v", folder)
	}
	if filepath.Base(folder.Path) != "Export-Me" {
		t.Fatalf("folder path = %s", folder.Path)
	}
	for _, item := range items {
		mirrored := filepath.Join(folder.Path, filepath.Base(item.ArchivePath))
		mirroredInfo, err := os.Stat(mirrored)
		if err != nil {
			t.Fatalf("stat %s: %v", mirrored, err)
		}
		sourceInfo, err := os.Stat(item.ArchivePath)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(mirroredInfo, sourceInfo) {
			t.Fatalf("%s is not linked to the library archive", mirrored)
		}
	}
}

func TestSyncCollectionFolderDropsRemovedModsAndKeepsLinks(t *testing.T) {
	service := newTestAppService(t)
	items, collectionID := scanAndCreateCollection(t, service, "Trim", 2, 9200)

	first, err := service.syncCollectionFolder(context.Background(), collectionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(collectionID, []string{items[0].EntityID}, false); err != nil {
		t.Fatal(err)
	}

	second, err := service.syncCollectionFolder(context.Background(), collectionID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Path != first.Path {
		t.Fatalf("folder moved: %s -> %s", first.Path, second.Path)
	}
	if second.ModCount != 1 || second.RemovedCount != 1 || second.AddedCount != 0 {
		t.Fatalf("refresh counts = %+v", second)
	}
	if _, err := os.Stat(filepath.Join(second.Path, filepath.Base(items[0].ArchivePath))); !os.IsNotExist(err) {
		t.Fatalf("removed mod still mirrored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(second.Path, filepath.Base(items[1].ArchivePath))); err != nil {
		t.Fatalf("remaining mod lost its link: %v", err)
	}
}

func TestSyncCollectionFolderFollowsRenameAndKeepsForeignFiles(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "Before", 1, 9300)

	before, err := service.syncCollectionFolder(context.Background(), collectionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateCollection(collectionID, "After", ""); err != nil {
		t.Fatal(err)
	}

	after, err := service.syncCollectionFolder(context.Background(), collectionID)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(after.Path) != "After" {
		t.Fatalf("renamed folder = %s", after.Path)
	}
	if _, err := os.Stat(before.Path); !os.IsNotExist(err) {
		t.Fatalf("stale folder survived the rename: %v", err)
	}

	// A second refresh must leave non-archive files the user dropped in alone.
	if err := os.WriteFile(filepath.Join(after.Path, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	refreshed, err := service.syncCollectionFolder(context.Background(), collectionID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.RemovedCount != 0 || refreshed.AddedCount != 0 {
		t.Fatalf("idempotent refresh changed files: %+v", refreshed)
	}
	if _, err := os.Stat(filepath.Join(after.Path, "notes.txt")); err != nil {
		t.Fatalf("user file removed: %v", err)
	}
}
