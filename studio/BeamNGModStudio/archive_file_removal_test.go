package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlanArchiveFileRemovalRefusesLastLink(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := []ScanArchive{
		modFamilyScanArchive(t, root, "only-copy.zip", "Only Copy Mod", "Archive Author", "1.0", "", "only-copy-sha", "only-copy-fingerprint", 100, testArchiveModified(1), 0),
	}
	items := applyLibraryArchives(t, service.store, root, archives)
	if len(items) != 1 {
		t.Fatalf("items = %#v, want one entity", items)
	}
	links := familyArchiveLinks(t, service, items[0].EntityID)
	if len(links) != 1 {
		t.Fatalf("links = %#v, want one active link", links)
	}

	impact, err := service.PlanArchiveFileRemoval([]string{links[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.Files) != 1 || impact.Files[0].LinkID != links[0].ID || len(impact.Refusals) != 1 {
		t.Fatalf("impact = %#v, want one refused file", impact)
	}
	if !strings.Contains(impact.Refusals[0], "Only Copy Mod") || !strings.Contains(impact.Refusals[0], "mod's own delete") {
		t.Fatalf("refusal = %q, want mod name and own-delete guidance", impact.Refusals[0])
	}
	if impact.ArchiveCount != 0 || impact.ArchiveBytes != 0 {
		t.Fatalf("impact archive totals = %#v, want refused file excluded", impact)
	}
}

func TestDeleteArchiveFilesRecyclesOneLinkKeepsModAndCollection(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	oldArchive := modFamilyScanArchive(t, root, filepath.Join("old", "same-name.zip"), "Keep One Copy", "Archive Author", "1.0", "", "two-link-sha", "two-link-fingerprint", 100, testArchiveModified(2), 0)
	newArchive := modFamilyScanArchive(t, root, filepath.Join("new", "same-name.zip"), "Keep One Copy", "Archive Author", "1.0", "", "two-link-sha", "two-link-fingerprint", 200, testArchiveModified(3), 0)
	items := applyLibraryArchives(t, service.store, root, []ScanArchive{oldArchive, newArchive})
	if len(items) != 1 {
		t.Fatalf("items = %#v, want one entity with two links", items)
	}
	item := items[0]
	links := familyArchiveLinks(t, service, item.EntityID)
	if len(links) != 2 {
		t.Fatalf("links = %#v, want two active links", links)
	}
	var oldLink ArchiveLink
	for _, link := range links {
		if link.Path == oldArchive.ArchivePath {
			oldLink = link
			break
		}
	}
	if oldLink.ID == "" {
		t.Fatalf("old archive link not found in %#v", links)
	}

	collection, err := service.CreateCollection("Keeps Membership", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(collection.Collection.ID, []string{item.EntityID}, true); err != nil {
		t.Fatal(err)
	}

	result, err := service.DeleteArchiveFiles([]string{oldLink.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) > 0 && strings.Contains(result.Failures[0], "only supported on Windows") {
		t.Skipf("archive recycling is not supported on this platform: %#v", result)
	}
	if result.Forgotten != 0 || result.Recycled != 1 || len(result.Failures) != 0 {
		t.Fatalf("delete result = %#v, want one recycled link and no failures", result)
	}
	if _, err := os.Stat(oldArchive.ArchivePath); !os.IsNotExist(err) {
		t.Fatalf("old archive survived recycle: %v", err)
	}
	if _, err := os.Stat(newArchive.ArchivePath); err != nil {
		t.Fatalf("remaining archive is not on disk: %v", err)
	}

	remaining, err := service.store.GetLibraryItem(context.Background(), item.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if remaining.ArchivePath != newArchive.ArchivePath {
		t.Fatalf("representative archive = %q, want %q", remaining.ArchivePath, newArchive.ArchivePath)
	}
	members, err := service.GetCollection(collection.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members.Members) != 1 || members.Members[0].EntityID != item.EntityID {
		t.Fatalf("collection membership = %#v, want retained entity", members.Members)
	}
	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 0 {
		t.Fatalf("families after one link removal = %#v, want none", families)
	}
}

func TestDeleteArchiveFilesKeepsLinkWhenRecycleFails(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	oldArchive := modFamilyScanArchive(t, root, filepath.Join("old", "same-name.zip"), "Locked Copy", "Archive Author", "1.0", "", "locked-copy-sha", "locked-copy-fingerprint", 100, testArchiveModified(4), 0)
	newArchive := modFamilyScanArchive(t, root, filepath.Join("new", "same-name.zip"), "Locked Copy", "Archive Author", "1.0", "", "locked-copy-sha", "locked-copy-fingerprint", 200, testArchiveModified(5), 0)
	items := applyLibraryArchives(t, service.store, root, []ScanArchive{oldArchive, newArchive})
	if len(items) != 1 {
		t.Fatalf("items = %#v, want one entity with two links", items)
	}
	links := familyArchiveLinks(t, service, items[0].EntityID)
	var oldLink ArchiveLink
	for _, link := range links {
		if link.Path == oldArchive.ArchivePath {
			oldLink = link
			break
		}
	}
	if oldLink.ID == "" {
		t.Fatalf("old archive link not found in %#v", links)
	}
	handle, err := os.Open(oldArchive.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()

	result, err := service.DeleteArchiveFiles([]string{oldLink.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) == 0 {
		if result.Recycled == 1 {
			t.Skipf("the platform allowed an open archive to be recycled: %#v", result)
		}
		t.Fatalf("delete result = %#v, want a recycle failure", result)
	}
	if result.Recycled != 0 || result.Forgotten != 0 {
		t.Fatalf("delete result = %#v, want no successful removal", result)
	}
	if _, err := os.Stat(oldArchive.ArchivePath); err != nil {
		t.Fatalf("failed-recycle archive disappeared: %v", err)
	}
	var active int
	if err := service.store.db.QueryRowContext(context.Background(), `SELECT active FROM archive_links WHERE id=?`, oldLink.ID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("failed-recycle link active = %d, want 1", active)
	}
}

func testArchiveModified(day int) time.Time {
	return time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC)
}
