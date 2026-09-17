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
	selection, err := service.ResolvePlaySelection([]string{collection.Collection.ID}, nil)
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
	before, err := service.ResolvePlaySelection([]string{collection.Collection.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Mods) != 1 {
		t.Fatalf("selection before archive = %#v", before)
	}
	if _, err := service.ArchiveMods([]string{items[0].EntityID}); err != nil {
		t.Fatal(err)
	}
	after, err := service.ResolvePlaySelection([]string{collection.Collection.ID}, nil)
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

func TestArchiveDisablesBothCollectionMembershipsAndReportsCount(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	items := applyLibraryArchives(t, service.store, root, libraryFixtureArchives(root, 1, 9010))
	entityID := items[0].EntityID

	colA, err := service.CreateCollection("Archive Col A", "", "")
	if err != nil {
		t.Fatal(err)
	}
	colB, err := service.CreateCollection("Archive Col B", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(colA.Collection.ID, []string{entityID}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(colB.Collection.ID, []string{entityID}, true); err != nil {
		t.Fatal(err)
	}

	result, err := service.ArchiveMods([]string{entityID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Archived != 1 {
		t.Fatalf("archived count = %d, want 1", result.Archived)
	}
	if result.DisabledMemberships != 2 {
		t.Fatalf("disabled memberships = %d, want 2", result.DisabledMemberships)
	}

	// Both memberships should be disabled and flagged.
	ctx := context.Background()
	for _, colID := range []string{colA.Collection.ID, colB.Collection.ID} {
		var enabled, flag int
		if err := service.store.db.QueryRowContext(ctx,
			`SELECT enabled, disabled_by_archive FROM collection_mods WHERE collection_id=? AND entity_id=?`,
			colID, entityID).Scan(&enabled, &flag); err != nil {
			t.Fatalf("query membership %s: %v", colID, err)
		}
		if enabled != 0 || flag != 1 {
			t.Fatalf("collection %s: enabled=%d disabled_by_archive=%d, want 0/1", colID, enabled, flag)
		}
	}
}

func TestRestoreReenablesBothMembershipsAndReportsCount(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	items := applyLibraryArchives(t, service.store, root, libraryFixtureArchives(root, 1, 9011))
	entityID := items[0].EntityID

	colA, err := service.CreateCollection("Restore Col A", "", "")
	if err != nil {
		t.Fatal(err)
	}
	colB, err := service.CreateCollection("Restore Col B", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(colA.Collection.ID, []string{entityID}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(colB.Collection.ID, []string{entityID}, true); err != nil {
		t.Fatal(err)
	}

	if _, err := service.ArchiveMods([]string{entityID}); err != nil {
		t.Fatal(err)
	}

	result, err := service.RestoreMods([]string{entityID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Restored != 1 {
		t.Fatalf("restored count = %d, want 1", result.Restored)
	}
	if result.ReenabledMemberships != 2 {
		t.Fatalf("re-enabled memberships = %d, want 2", result.ReenabledMemberships)
	}

	// Both memberships should be re-enabled and flag cleared.
	ctx := context.Background()
	for _, colID := range []string{colA.Collection.ID, colB.Collection.ID} {
		var enabled, flag int
		if err := service.store.db.QueryRowContext(ctx,
			`SELECT enabled, disabled_by_archive FROM collection_mods WHERE collection_id=? AND entity_id=?`,
			colID, entityID).Scan(&enabled, &flag); err != nil {
			t.Fatalf("query membership %s: %v", colID, err)
		}
		if enabled != 1 || flag != 0 {
			t.Fatalf("collection %s: enabled=%d disabled_by_archive=%d, want 1/0", colID, enabled, flag)
		}
	}
}

func TestRestorePreservesUserDisabledMembership(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	items := applyLibraryArchives(t, service.store, root, libraryFixtureArchives(root, 1, 9012))
	entityID := items[0].EntityID

	// Two collections: one will be user-disabled before archiving.
	colEnabled, err := service.CreateCollection("User Enabled Col", "", "")
	if err != nil {
		t.Fatal(err)
	}
	colUserDisabled, err := service.CreateCollection("User Disabled Col", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(colEnabled.Collection.ID, []string{entityID}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(colUserDisabled.Collection.ID, []string{entityID}, true); err != nil {
		t.Fatal(err)
	}

	// User manually disables the membership in one collection.
	if _, err := service.SetCollectionModsEnabled(colUserDisabled.Collection.ID, []string{entityID}, false); err != nil {
		t.Fatal(err)
	}

	// Archive: only the enabled membership should be disabled.
	archiveResult, err := service.ArchiveMods([]string{entityID})
	if err != nil {
		t.Fatal(err)
	}
	if archiveResult.DisabledMemberships != 1 {
		t.Fatalf("disabled memberships = %d, want 1 (only the enabled one)", archiveResult.DisabledMemberships)
	}

	// Verify: colEnabled is now disabled_by_archive=1; colUserDisabled is disabled_by_archive=0.
	ctx := context.Background()
	var enabledFlag, enabledByArchive int
	if err := service.store.db.QueryRowContext(ctx,
		`SELECT enabled, disabled_by_archive FROM collection_mods WHERE collection_id=? AND entity_id=?`,
		colEnabled.Collection.ID, entityID).Scan(&enabledFlag, &enabledByArchive); err != nil {
		t.Fatal(err)
	}
	if enabledFlag != 0 || enabledByArchive != 1 {
		t.Fatalf("enabled col: enabled=%d disabled_by_archive=%d, want 0/1", enabledFlag, enabledByArchive)
	}
	if err := service.store.db.QueryRowContext(ctx,
		`SELECT enabled, disabled_by_archive FROM collection_mods WHERE collection_id=? AND entity_id=?`,
		colUserDisabled.Collection.ID, entityID).Scan(&enabledFlag, &enabledByArchive); err != nil {
		t.Fatal(err)
	}
	if enabledFlag != 0 || enabledByArchive != 0 {
		t.Fatalf("user-disabled col: enabled=%d disabled_by_archive=%d, want 0/0", enabledFlag, enabledByArchive)
	}

	// Restore: only the archive-disabled membership should be re-enabled.
	restoreResult, err := service.RestoreMods([]string{entityID})
	if err != nil {
		t.Fatal(err)
	}
	if restoreResult.ReenabledMemberships != 1 {
		t.Fatalf("re-enabled memberships = %d, want 1", restoreResult.ReenabledMemberships)
	}

	// colEnabled should be re-enabled; colUserDisabled should STILL be disabled.
	if err := service.store.db.QueryRowContext(ctx,
		`SELECT enabled, disabled_by_archive FROM collection_mods WHERE collection_id=? AND entity_id=?`,
		colEnabled.Collection.ID, entityID).Scan(&enabledFlag, &enabledByArchive); err != nil {
		t.Fatal(err)
	}
	if enabledFlag != 1 || enabledByArchive != 0 {
		t.Fatalf("restored enabled col: enabled=%d disabled_by_archive=%d, want 1/0", enabledFlag, enabledByArchive)
	}
	if err := service.store.db.QueryRowContext(ctx,
		`SELECT enabled, disabled_by_archive FROM collection_mods WHERE collection_id=? AND entity_id=?`,
		colUserDisabled.Collection.ID, entityID).Scan(&enabledFlag, &enabledByArchive); err != nil {
		t.Fatal(err)
	}
	if enabledFlag != 0 || enabledByArchive != 0 {
		t.Fatalf("user-disabled col after restore: enabled=%d disabled_by_archive=%d, want 0/0", enabledFlag, enabledByArchive)
	}
}

func TestArchivePreservesMembershipRowAndPosition(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	items := applyLibraryArchives(t, service.store, root, libraryFixtureArchives(root, 3, 9020))

	col, err := service.CreateCollection("Position Col", "", "")
	if err != nil {
		t.Fatal(err)
	}
	entityIDs := []string{items[0].EntityID, items[1].EntityID, items[2].EntityID}
	if _, err := service.SetCollectionMods(col.Collection.ID, entityIDs, true); err != nil {
		t.Fatal(err)
	}

	// Capture positions before archiving.
	ctx := context.Background()
	type memberRow struct {
		entityID string
		position int
	}
	queryMembers := func() []memberRow {
		rows, err := service.store.db.QueryContext(ctx,
			`SELECT entity_id, position FROM collection_mods WHERE collection_id=? ORDER BY position`, col.Collection.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var members []memberRow
		for rows.Next() {
			var m memberRow
			if err := rows.Scan(&m.entityID, &m.position); err != nil {
				t.Fatal(err)
			}
			members = append(members, m)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return members
	}

	before := queryMembers()
	if len(before) != 3 {
		t.Fatalf("members before = %d, want 3", len(before))
	}

	// Archive the middle mod.
	if _, err := service.ArchiveMods([]string{items[1].EntityID}); err != nil {
		t.Fatal(err)
	}

	after := queryMembers()
	if len(after) != 3 {
		t.Fatalf("members after archive = %d, want 3 (row must be preserved)", len(after))
	}
	// Positions and entity IDs must be identical.
	for i := range before {
		if before[i].entityID != after[i].entityID || before[i].position != after[i].position {
			t.Fatalf("membership row changed: before[%d]=%+v after[%d]=%+v", i, before[i], i, after[i])
		}
	}
}

func TestCollectionPickerOmitsArchivedModsWhileDetailRetainsThem(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	archives := libraryFixtureArchives(root, 2, 9030)
	for _, archive := range archives {
		if err := os.WriteFile(archive.ArchivePath, []byte("archive"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	items := applyLibraryArchives(t, service.store, root, archives)

	col, err := service.CreateCollection("Picker Col", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(col.Collection.ID, []string{items[0].EntityID, items[1].EntityID}, true); err != nil {
		t.Fatal(err)
	}

	// Archive the first mod.
	if _, err := service.ArchiveMods([]string{items[0].EntityID}); err != nil {
		t.Fatal(err)
	}

	// The library listing with scope "active" (the picker's data source)
	// should NOT include the archived mod.
	active, err := service.ListLibrary("all", "all", "", "all", "active")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range active {
		if item.EntityID == items[0].EntityID {
			t.Fatalf("active library listing includes archived mod %s", items[0].EntityID)
		}
	}
	if len(active) != 1 || active[0].EntityID != items[1].EntityID {
		t.Fatalf("active listing = %d items, want 1 (the non-archived mod)", len(active))
	}

	// CollectionDetail.Mods MUST still include the archived member.
	detail, err := service.store.CollectionDetail(context.Background(), col.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundArchived := false
	for _, mod := range detail.Mods {
		if mod.EntityID == items[0].EntityID {
			foundArchived = true
			if mod.ArchivedAt == "" {
				t.Fatal("archived member in detail has empty ArchivedAt")
			}
		}
	}
	if !foundArchived {
		t.Fatalf("CollectionDetail.Mods does not contain the archived member; mods = %v", detail.Mods)
	}
	if detail.Collection.ArchivedModCount != 1 {
		t.Fatalf("ArchivedModCount = %d, want 1", detail.Collection.ArchivedModCount)
	}
}

func TestPlaySelectionExcludesArchivedMemberAndCounts(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	items := applyLibraryArchives(t, service.store, root, libraryFixtureArchives(root, 2, 9040))

	col, err := service.CreateCollection("Play Exclude Col", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(col.Collection.ID, []string{items[0].EntityID, items[1].EntityID}, true); err != nil {
		t.Fatal(err)
	}

	if _, err := service.ArchiveMods([]string{items[0].EntityID}); err != nil {
		t.Fatal(err)
	}

	selection, err := service.ResolvePlaySelection([]string{col.Collection.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The archived mod must be excluded from the resolved list.
	for _, mod := range selection.Mods {
		if mod.EntityID == items[0].EntityID {
			t.Fatal("archived member appeared in play selection Mods")
		}
	}
	if selection.ModCount != 1 {
		t.Fatalf("ModCount = %d, want 1", selection.ModCount)
	}
	if selection.ArchivedCount != 1 {
		t.Fatalf("ArchivedCount = %d, want 1", selection.ArchivedCount)
	}
	if len(selection.Warnings) == 0 || !strings.Contains(selection.Warnings[0], "archived") {
		t.Fatalf("warnings = %v, want archived warning", selection.Warnings)
	}
}

func TestDisabledByArchiveMigrationIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "idempotent.sqlite")

	// First open: creates schema and runs migration.
	store1, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	// Verify column exists.
	var hasColumn bool
	if err := store1.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*)>0 FROM pragma_table_info('collection_mods') WHERE name='disabled_by_archive'`).Scan(&hasColumn); err != nil {
		t.Fatal(err)
	}
	if !hasColumn {
		t.Fatal("disabled_by_archive column missing after first open")
	}
	if err := store1.Close(); err != nil {
		t.Fatal(err)
	}

	// Second open: migration must be idempotent.
	store2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if err := store2.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*)>0 FROM pragma_table_info('collection_mods') WHERE name='disabled_by_archive'`).Scan(&hasColumn); err != nil {
		t.Fatal(err)
	}
	if !hasColumn {
		t.Fatal("disabled_by_archive column missing after second open")
	}
	if err := store2.Close(); err != nil {
		t.Fatal(err)
	}
}
