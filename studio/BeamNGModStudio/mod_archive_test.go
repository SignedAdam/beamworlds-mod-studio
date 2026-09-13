package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestArchiveModsHidesActiveAndShowsArchivedListing(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archives := libraryFixtureArchives(root, 1, 9000)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archives[0].ArchivePath, []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	items := applyLibraryArchives(t, service.store, root, archives)
	if len(items) != 1 {
		t.Fatalf("scan items = %#v, want one item", items)
	}
	entityID := items[0].EntityID

	active, err := service.ListLibrary("all", "all", "", "all", "active")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].EntityID != entityID || active[0].ArchivedAt != "" {
		t.Fatalf("active listing before archive = %#v", active)
	}
	if result, err := service.ArchiveMods([]string{entityID}); err != nil {
		t.Fatal(err)
	} else if result.Archived != 1 || result.Restored != 0 {
		t.Fatalf("archive result = %#v", result)
	}
	if _, err := os.Stat(archives[0].ArchivePath); err != nil {
		t.Fatalf("archive operation touched the file: %v", err)
	}

	active, err = service.ListLibrary("all", "all", "", "all", "active")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("active listing after archive = %#v, want empty", active)
	}
	archived, err := service.ListLibrary("all", "all", "", "all", "archived")
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 1 || archived[0].EntityID != entityID || archived[0].ArchivedAt == "" {
		t.Fatalf("archived listing = %#v", archived)
	}
	if _, err := service.ListLibrary("all", "all", "", "all", "unexpected"); err == nil {
		t.Fatal("invalid archive scope was silently accepted")
	}
}

func TestArchiveModsRescanLeavesArchiveState(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	archive := libraryFixtureArchives(root, 1, 9001)[0]
	items := applyLibraryArchives(t, service.store, root, []ScanArchive{archive})
	if len(items) != 1 {
		t.Fatalf("scan items = %#v, want one item", items)
	}
	entityID := items[0].EntityID
	if _, err := service.ArchiveMods([]string{entityID}); err != nil {
		t.Fatal(err)
	}
	before, err := service.store.GetLibraryItem(context.Background(), entityID)
	if err != nil {
		t.Fatal(err)
	}
	if before.ArchivedAt == "" {
		t.Fatal("archive timestamp was not set")
	}

	applyLibraryArchives(t, service.store, root, []ScanArchive{archive})
	after, err := service.store.GetLibraryItem(context.Background(), entityID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ArchivedAt != before.ArchivedAt {
		t.Fatalf("rescan changed archivedAt from %q to %q", before.ArchivedAt, after.ArchivedAt)
	}
	archived, err := service.ListLibrary("all", "all", "", "all", "archived")
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 1 || archived[0].EntityID != entityID {
		t.Fatalf("archived listing after rescan = %#v", archived)
	}
}

func TestRestoreModsPreservesCollectionsTagsAndScanStatus(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	items := applyLibraryArchives(t, service.store, root, libraryFixtureArchives(root, 1, 9002))
	if len(items) != 1 {
		t.Fatalf("scan items = %#v, want one item", items)
	}
	entityID := items[0].EntityID
	collection, err := service.CreateCollection("Archived Restore Collection", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(collection.Collection.ID, []string{entityID}, true); err != nil {
		t.Fatal(err)
	}
	organization, err := service.CreateModTag("Archived Restore Tag", "#123456", "tag")
	if err != nil {
		t.Fatal(err)
	}
	var tagID string
	for _, tag := range organization.Tags {
		if tag.Name == "Archived Restore Tag" {
			tagID = tag.ID
			break
		}
	}
	if tagID == "" {
		t.Fatal("created tag was not returned")
	}
	if _, err := service.SetLibraryItemTags(entityID, []string{tagID}); err != nil {
		t.Fatal(err)
	}
	before, err := service.store.GetLibraryItem(context.Background(), entityID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ArchiveMods([]string{entityID}); err != nil {
		t.Fatal(err)
	}
	if result, err := service.RestoreMods([]string{entityID}); err != nil {
		t.Fatal(err)
	} else if result.Restored != 1 || result.Archived != 0 {
		t.Fatalf("restore result = %#v", result)
	}
	after, err := service.store.GetLibraryItem(context.Background(), entityID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ArchivedAt != "" {
		t.Fatalf("restored item remains archived: %#v", after)
	}
	if !slices.Equal(before.CollectionIDs, after.CollectionIDs) || len(after.CollectionIDs) != 1 || after.CollectionIDs[0] != collection.Collection.ID {
		t.Fatalf("collections changed across archive/restore: before=%v after=%v", before.CollectionIDs, after.CollectionIDs)
	}
	if len(before.Tags) != len(after.Tags) || len(after.Tags) != 1 || after.Tags[0].ID != tagID {
		t.Fatalf("tags changed across archive/restore: before=%#v after=%#v", before.Tags, after.Tags)
	}
	if before.HealthStatus != after.HealthStatus || before.LastSecurityScanAt != after.LastSecurityScanAt || before.LastSecurityScanVerdict != after.LastSecurityScanVerdict || before.LastSeenAt != after.LastSeenAt {
		t.Fatalf("scan status changed across archive/restore: before=%#v after=%#v", before, after)
	}
	active, err := service.ListLibrary("all", "all", "", "all", "active")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].EntityID != entityID {
		t.Fatalf("restored active listing = %#v", active)
	}
}

func TestResolvePlaySelectionOmitsArchivedMembers(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	items := applyLibraryArchives(t, service.store, root, libraryFixtureArchives(root, 1, 9003))
	collection, err := service.CreateCollection("Play Archived Collection", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(collection.Collection.ID, []string{items[0].EntityID}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ArchiveMods([]string{items[0].EntityID}); err != nil {
		t.Fatal(err)
	}
	detail, err := service.store.CollectionDetail(context.Background(), collection.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Mods) != 1 || detail.Mods[0].EntityID != items[0].EntityID || detail.Mods[0].ArchivedAt == "" {
		t.Fatalf("collection detail hid archived member: %#v", detail.Mods)
	}
	if detail.Collection.ModCount != 1 || detail.Collection.ArchivedModCount != 1 {
		t.Fatalf("collection counts = %#v, want total 1 and archived 1", detail.Collection)
	}
	selection, err := service.ResolvePlaySelection([]string{collection.Collection.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Mods) != 0 || selection.ModCount != 0 {
		t.Fatalf("archived member still resolved: %#v", selection)
	}
	if selection.MissingCount != 0 || selection.ArchivedCount != 1 {
		t.Fatalf("selection omission counts = %#v", selection)
	}
	wantWarning := "1 archived mod was left out of this selection"
	if !slices.Contains(selection.Warnings, wantWarning) {
		t.Fatalf("selection warnings = %#v, want %q", selection.Warnings, wantWarning)
	}
	if err := validatePlaySelectionRequest(PlayRequest{Fingerprint: selection.Fingerprint}, selection); err != nil {
		t.Fatalf("intentional archived omission blocked Play: %v", err)
	}
}

func TestPlaySelectionFingerprintChangesWhenMemberArchived(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	items := applyLibraryArchives(t, service.store, root, libraryFixtureArchives(root, 1, 9004))
	collection, err := service.CreateCollection("Play Fingerprint Collection", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(collection.Collection.ID, []string{items[0].EntityID}, true); err != nil {
		t.Fatal(err)
	}
	before, err := service.ResolvePlaySelection([]string{collection.Collection.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Mods) != 1 {
		t.Fatalf("selection before archive = %#v", before)
	}
	if _, err := service.ArchiveMods([]string{items[0].EntityID}); err != nil {
		t.Fatal(err)
	}
	after, err := service.ResolvePlaySelection([]string{collection.Collection.ID})
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint == after.Fingerprint {
		t.Fatalf("selection fingerprint did not change: %q", before.Fingerprint)
	}
	if after.ArchivedCount != 1 || !strings.Contains(after.Warnings[0], "archived mod") {
		t.Fatalf("selection after archive = %#v", after)
	}
}

func TestArchiveModsIsIdempotentAndUnknownIDErrors(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	items := applyLibraryArchives(t, service.store, root, libraryFixtureArchives(root, 1, 9005))
	entityID := items[0].EntityID
	if result, err := service.ArchiveMods([]string{entityID}); err != nil {
		t.Fatal(err)
	} else if result.Archived != 1 {
		t.Fatalf("first archive result = %#v", result)
	}
	var eventCount int
	if err := service.store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events WHERE entity_id=? AND type='mod_archived'`, entityID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if result, err := service.ArchiveMods([]string{entityID}); err != nil {
		t.Fatal(err)
	} else if result.Archived != 0 || result.Restored != 0 {
		t.Fatalf("idempotent archive result = %#v", result)
	}
	var repeatedEventCount int
	if err := service.store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events WHERE entity_id=? AND type='mod_archived'`, entityID).Scan(&repeatedEventCount); err != nil {
		t.Fatal(err)
	}
	if repeatedEventCount != eventCount {
		t.Fatalf("idempotent archive appended an event: before=%d after=%d", eventCount, repeatedEventCount)
	}
	if _, err := service.ArchiveMods([]string{"missing-entity"}); err == nil || !strings.Contains(err.Error(), "not in the library") {
		t.Fatalf("unknown archive ID error = %v", err)
	}
}
