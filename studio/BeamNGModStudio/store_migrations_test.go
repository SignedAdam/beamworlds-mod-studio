package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

func migrationFixtureManifest(path string, serial int) modkit.Manifest {
	modified := time.Unix(1_700_500_000+int64(serial), 0).UTC()
	title := fmt.Sprintf("Migration Fixture %04d", serial)
	return modkit.Manifest{
		SchemaVersion:      modkit.SchemaVersion,
		AnalyzerVersion:    modkit.AnalyzerVersion,
		AnalyzedAt:         modified,
		ArchivePath:        path,
		Filename:           filepath.Base(path),
		SizeBytes:          int64(2000 + serial),
		ModifiedAt:         modified,
		CentralFingerprint: fmt.Sprintf("migration-fingerprint-%04d", serial),
		FullSHA256:         fmt.Sprintf("migration-sha-%04d", serial),
		ValidArchive:       true,
		Kind:               modkit.KindVehicle,
		Title:              title,
		Description:        fmt.Sprintf("Durability fixture %04d", serial),
		Author:             "Migration Test Author",
		Version:            "1.2.3",
		Namespaces:         map[string][]string{"vehicles": {fmt.Sprintf("migration_namespace_%04d", serial)}},
		Members:            []modkit.ArchiveMember{{Path: "vehicles/main.jbeam"}},
	}
}

func migrationFixtureArchive(root string, serial int) ScanArchive {
	path := filepath.Join(root, fmt.Sprintf("migration-%04d.zip", serial))
	modified := time.Unix(1_700_500_000+int64(serial), 0).UTC()
	manifest := migrationFixtureManifest(path, serial)
	return ScanArchive{
		Root:        root,
		ArchivePath: path,
		SizeBytes:   manifest.SizeBytes,
		Modified:    modified,
		Manifest:    manifest,
	}
}

func migrationApplyBatch(tb testing.TB, store *Store, root string, archives []ScanArchive, discovered, analyzed, failed int) []LibraryItem {
	tb.Helper()
	ctx := context.Background()
	scanID, err := store.BeginScan(ctx, []string{root})
	if err != nil {
		tb.Fatalf("begin scan: %v", err)
	}
	items, err := store.ApplyScanBatch(ctx, scanID, []string{root}, nil, archives, discovered, analyzed, failed)
	if err != nil {
		tb.Fatalf("apply scan batch: %v", err)
	}
	return items
}

func migrationCount(tb testing.TB, store *Store, table string) int {
	tb.Helper()
	var count int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
		tb.Fatalf("count %s: %v", table, err)
	}
	return count
}

func migrationItemForPath(tb testing.TB, store *Store, path string) LibraryItem {
	tb.Helper()
	var entityID string
	if err := store.db.QueryRowContext(context.Background(), `SELECT entity_id FROM archive_links WHERE path=?`, path).Scan(&entityID); err != nil {
		tb.Fatalf("find fixture link: %v", err)
	}
	item, err := store.GetLibraryItem(context.Background(), entityID)
	if err != nil {
		tb.Fatalf("hydrate fixture item: %v", err)
	}
	return item
}

func migrationFTSEntityMatches(tb testing.TB, store *Store, term string) []string {
	tb.Helper()
	rows, err := store.db.QueryContext(context.Background(), `SELECT entity_id FROM library_search_fts WHERE library_search_fts MATCH ? ORDER BY entity_id`, libraryFTSMatch(term))
	if err != nil {
		tb.Fatalf("query FTS term %q: %v", term, err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var entityID string
		if err := rows.Scan(&entityID); err != nil {
			tb.Fatalf("scan FTS term %q: %v", term, err)
		}
		ids = append(ids, entityID)
	}
	if err := rows.Err(); err != nil {
		tb.Fatalf("iterate FTS term %q: %v", term, err)
	}
	return ids
}

func TestSQLiteCleanInstallCreatesVersion6SchemaAndEnforcesForeignKeys(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "clean.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var version int
	if err := store.db.QueryRowContext(ctx, `SELECT version FROM schema_meta`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != storeSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, storeSchemaVersion)
	}
	for _, table := range []string{"source_classifications", "library_search_fts", "test_installs"} {
		var present int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table','view') AND name=?`, table).Scan(&present); err != nil {
			t.Fatal(err)
		}
		if present != 1 {
			t.Fatalf("required table %q is absent", table)
		}
	}
	for sourceID, expectedLabel := range map[string]string{
		"beamng-repository": "BeamNG Repository",
		"user-added":        "User added",
	} {
		var label string
		if err := store.db.QueryRowContext(ctx, `SELECT label FROM source_classifications WHERE id=?`, sourceID).Scan(&label); err != nil {
			t.Fatalf("source classification %q: %v", sourceID, err)
		}
		if label != expectedLabel {
			t.Fatalf("source classification %q label = %q, want %q", sourceID, label, expectedLabel)
		}
	}
	var sourceColumn int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('archive_links') WHERE name='source_id'`).Scan(&sourceColumn); err != nil {
		t.Fatal(err)
	}
	if sourceColumn != 1 {
		t.Fatal("archive_links.source_id is missing")
	}
	if err := store.verifyIntegrity(ctx); err != nil {
		t.Fatalf("new schema failed integrity check: %v", err)
	}

	store.db.SetMaxOpenConns(4)
	connections := make([]*sql.Conn, 0, 4)
	for range 4 {
		conn, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
	}
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	for index, conn := range connections {
		var foreignKeys int
		if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", index, err)
		}
		if foreignKeys != 1 {
			t.Fatalf("connection %d foreign_keys = %d, want 1", index, foreignKeys)
		}
	}
}

func TestGroupedTagMigrationToCollections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "grouped-tags.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	root := filepath.Join(t.TempDir(), "mods")
	archives := []ScanArchive{
		migrationFixtureArchive(root, 1),
		migrationFixtureArchive(root, 2),
		migrationFixtureArchive(root, 3),
	}
	items := migrationApplyBatch(t, store, root, archives, len(archives), len(archives), 0)
	if len(items) != len(archives) {
		t.Fatalf("fixture items = %d, want %d", len(items), len(archives))
	}

	existing, err := store.CreateCollection(ctx, "Existing Collection", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetCollectionMods(ctx, existing.Collection.ID, []string{items[0].EntityID}, true); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateModTag(ctx, "existing collection", "#123456", "tag"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateModTag(ctx, "Migrated Tag Collection", "#654321", "tag"); err != nil {
		t.Fatal(err)
	}
	var existingTagID, newTagID string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM mod_tags WHERE name=? COLLATE NOCASE`, "existing collection").Scan(&existingTagID); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM mod_tags WHERE name=? COLLATE NOCASE`, "Migrated Tag Collection").Scan(&newTagID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLibraryItemTags(ctx, items[0].EntityID, []string{existingTagID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLibraryItemTags(ctx, items[1].EntityID, []string{existingTagID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLibraryItemTags(ctx, items[2].EntityID, []string{newTagID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE mod_tags ADD COLUMN grouped INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE mod_tags SET grouped=1 WHERE id IN (?,?)`, existingTagID, newTagID); err != nil {
		t.Fatal(err)
	}
	for _, tagID := range []string{existingTagID, newTagID} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?)`, "library_group_collapsed:"+tagID, "1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE schema_meta SET version=5`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE settings SET value='5' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA user_version=5`); err != nil {
		t.Fatal(err)
	}

	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	assertGroupedTagMigration := func() {
		var groupedColumn int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('mod_tags') WHERE name='grouped'`).Scan(&groupedColumn); err != nil {
			t.Fatal(err)
		}
		if groupedColumn != 0 {
			t.Fatal("grouped column survived migration")
		}
		var foldStates int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key LIKE 'library_group_collapsed:%'`).Scan(&foldStates); err != nil {
			t.Fatal(err)
		}
		if foldStates != 0 {
			t.Fatalf("fold-state settings survived migration: %d", foldStates)
		}
		var existingCount int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collections WHERE name=? COLLATE NOCASE`, "existing collection").Scan(&existingCount); err != nil {
			t.Fatal(err)
		}
		if existingCount != 1 {
			t.Fatalf("case-insensitive existing collection count = %d, want 1", existingCount)
		}
		var newCount int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collections WHERE name=? COLLATE NOCASE`, "Migrated Tag Collection").Scan(&newCount); err != nil {
			t.Fatal(err)
		}
		if newCount != 1 {
			t.Fatalf("migrated collection count = %d, want 1", newCount)
		}
		var existingCollectionID, newCollectionID string
		if err := store.db.QueryRowContext(ctx, `SELECT id FROM collections WHERE name=? COLLATE NOCASE`, "existing collection").Scan(&existingCollectionID); err != nil {
			t.Fatal(err)
		}
		if existingCollectionID != existing.Collection.ID {
			t.Fatalf("existing collection was replaced: got %q, want %q", existingCollectionID, existing.Collection.ID)
		}
		if err := store.db.QueryRowContext(ctx, `SELECT id FROM collections WHERE name=? COLLATE NOCASE`, "Migrated Tag Collection").Scan(&newCollectionID); err != nil {
			t.Fatal(err)
		}
		var existingMembers, newMembers int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collection_mods WHERE collection_id=?`, existingCollectionID).Scan(&existingMembers); err != nil {
			t.Fatal(err)
		}
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collection_mods WHERE collection_id=?`, newCollectionID).Scan(&newMembers); err != nil {
			t.Fatal(err)
		}
		if existingMembers != 2 || newMembers != 1 {
			t.Fatalf("migrated memberships = existing %d, new %d; want 2 and 1", existingMembers, newMembers)
		}
		for _, check := range []struct {
			collectionID string
			entityID     string
		}{
			{existingCollectionID, items[0].EntityID},
			{existingCollectionID, items[1].EntityID},
			{newCollectionID, items[2].EntityID},
		} {
			var present int
			if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collection_mods WHERE collection_id=? AND entity_id=?`, check.collectionID, check.entityID).Scan(&present); err != nil {
				t.Fatal(err)
			}
			if present != 1 {
				t.Fatalf("missing migrated membership %#v", check)
			}
		}
		for _, check := range []struct {
			tagID string
			count int
		}{
			{existingTagID, 2},
			{newTagID, 1},
		} {
			var assignments int
			if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mod_tag_entities WHERE tag_id=?`, check.tagID).Scan(&assignments); err != nil {
				t.Fatal(err)
			}
			if assignments != check.count {
				t.Fatalf("tag %q assignment count = %d, want %d", check.tagID, assignments, check.count)
			}
		}
	}
	assertGroupedTagMigration()
	var beforeCollections, beforeMemberships, beforeAssignments int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collections`).Scan(&beforeCollections); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collection_mods`).Scan(&beforeMemberships); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mod_tag_entities`).Scan(&beforeAssignments); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	assertGroupedTagMigration()
	var afterCollections, afterMemberships, afterAssignments int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collections`).Scan(&afterCollections); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collection_mods`).Scan(&afterMemberships); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mod_tag_entities`).Scan(&afterAssignments); err != nil {
		t.Fatal(err)
	}
	if beforeCollections != afterCollections || beforeMemberships != afterMemberships || beforeAssignments != afterAssignments {
		t.Fatalf("second migration changed counts: before collections/memberships/assignments %d/%d/%d, after %d/%d/%d",
			beforeCollections, beforeMemberships, beforeAssignments, afterCollections, afterMemberships, afterAssignments)
	}
}

func TestSQLiteSourceClassificationForeignKeyRejectsUnknownValues(t *testing.T) {
	ctx := context.Background()
	store, root := openLibraryStorage(t)
	items := migrationApplyBatch(t, store, root, []ScanArchive{migrationFixtureArchive(root, 1)}, 1, 1, 0)
	if len(items) != 1 {
		t.Fatalf("applied fixture items = %d, want 1", len(items))
	}
	var linkID, sourceID string
	path := filepath.Join(root, "migration-0001.zip")
	if err := store.db.QueryRowContext(ctx, `SELECT id,source_id FROM archive_links WHERE path=?`, path).Scan(&linkID, &sourceID); err != nil {
		t.Fatal(err)
	}
	if sourceID != "beamng-repository" && sourceID != "user-added" {
		t.Fatalf("scan source classification = %q", sourceID)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE archive_links SET source_id=? WHERE id=?`, "not-a-source", linkID); err == nil {
		t.Fatal("unknown source classification was accepted")
	}
	var retained string
	if err := store.db.QueryRowContext(ctx, `SELECT source_id FROM archive_links WHERE id=?`, linkID).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != sourceID {
		t.Fatalf("failed source update changed source from %q to %q", sourceID, retained)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE archive_links SET source_id=NULL WHERE id=?`, linkID); err == nil {
		t.Fatal("NULL source classification was accepted")
	}
}

func TestSQLiteVersion3MigrationPreservesRecordsTagsAndAssignments(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "version3.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(filepath.Dir(path), "mods")
	archive := migrationFixtureArchive(root, 2)
	items := migrationApplyBatch(t, store, root, []ScanArchive{archive}, 1, 1, 0)
	if len(items) != 1 {
		t.Fatalf("seed items = %d, want 1", len(items))
	}
	item := migrationItemForPath(t, store, archive.ArchivePath)
	if err := store.CreateModTag(ctx, "Migration Preserved", "#7a8791", "tag"); err != nil {
		t.Fatal(err)
	}
	var tagID string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM mod_tags WHERE name=?`, "Migration Preserved").Scan(&tagID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLibraryItemTags(ctx, item.EntityID, []string{tagID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM library_search_fts`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE schema_meta SET version=3`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var version int
	if err := reopened.db.QueryRowContext(ctx, `SELECT version FROM schema_meta`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != storeSchemaVersion {
		t.Fatalf("migrated schema version = %d, want %d", version, storeSchemaVersion)
	}
	migrated := migrationItemForPath(t, reopened, archive.ArchivePath)
	if migrated.EntityID != item.EntityID || migrated.DisplayName != item.DisplayName || migrated.SizeBytes != item.SizeBytes || migrated.Manifest.Title != item.Manifest.Title || migrated.Manifest.Author != item.Manifest.Author {
		t.Fatalf("version-3 migration changed canonical record: before=%#v after=%#v", item, migrated)
	}
	if len(migrated.Tags) != 1 || migrated.Tags[0].Name != "Migration Preserved" {
		t.Fatalf("version-3 migration changed tag assignment: %#v", migrated.Tags)
	}
	ids, err := reopened.listLibraryQuery(ctx, "all", "all", `in:name "Migration Fixture 0002"`, "all", "active")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != item.EntityID {
		t.Fatalf("version-3 migration did not rebuild FTS: %#v", ids)
	}
}

func TestSQLiteCancelledMigrationLeavesPriorSchemaUsable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cancelled-migration.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, `UPDATE schema_meta SET version=3`); err != nil {
		t.Fatal(err)
	}
	beforeFTS := migrationCount(t, store, "library_search_fts")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.migrate(cancelled); err == nil {
		t.Fatal("cancelled migration unexpectedly succeeded")
	}
	var version int
	if err := store.db.QueryRowContext(ctx, `SELECT version FROM schema_meta`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("cancelled migration changed schema version to %d", version)
	}
	if got := migrationCount(t, store, "library_search_fts"); got != beforeFTS {
		t.Fatalf("cancelled migration changed FTS rows from %d to %d", beforeFTS, got)
	}
}

func TestSQLiteFTSRefreshDeleteAndTagSynchronization(t *testing.T) {
	ctx := context.Background()
	store, root := openLibraryStorage(t)
	archive := migrationFixtureArchive(root, 3)
	items := migrationApplyBatch(t, store, root, []ScanArchive{archive}, 1, 1, 0)
	if len(items) != 1 {
		t.Fatalf("seed items = %d, want 1", len(items))
	}
	item := items[0]
	if _, err := store.db.ExecContext(ctx, `UPDATE entities SET display_name=? WHERE id=?`, "Refreshable Search Name", item.EntityID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.refreshLibrarySearchEntryTx(ctx, tx, item.EntityID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	ids := migrationFTSEntityMatches(t, store, "Refreshable Search Name")
	if len(ids) != 1 || ids[0] != item.EntityID {
		t.Fatalf("refreshed FTS entity IDs = %#v", ids)
	}

	tx, err = store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.deleteLibrarySearchEntryTx(ctx, tx, item.EntityID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if ids := migrationFTSEntityMatches(t, store, "Refreshable Search Name"); len(ids) != 0 {
		t.Fatalf("deleted FTS entry still matched = %#v", ids)
	}

	if err := store.CreateModTag(ctx, "FTS Initial Tag", "#7a8791", "tag"); err != nil {
		t.Fatal(err)
	}
	var tagID string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM mod_tags WHERE name=?`, "FTS Initial Tag").Scan(&tagID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLibraryItemTags(ctx, item.EntityID, []string{tagID}); err != nil {
		t.Fatal(err)
	}
	if ids := migrationFTSEntityMatches(t, store, "FTS Initial Tag"); len(ids) != 1 || ids[0] != item.EntityID {
		t.Fatalf("tag assignment was not indexed = %#v", ids)
	}
	if err := store.RenameModTag(ctx, tagID, "FTS Renamed Tag"); err != nil {
		t.Fatal(err)
	}
	if ids := migrationFTSEntityMatches(t, store, "FTS Renamed Tag"); len(ids) != 1 || ids[0] != item.EntityID {
		t.Fatalf("tag rename was not synchronized = %#v", ids)
	}
}

func TestSQLiteArchiveAnalysisReuseKeepsCanonicalRecord(t *testing.T) {
	ctx := context.Background()
	store, root := openLibraryStorage(t)
	archive := migrationFixtureArchive(root, 4)
	first := migrationApplyBatch(t, store, root, []ScanArchive{archive}, 1, 1, 0)
	if len(first) != 1 {
		t.Fatalf("seed items = %d, want 1", len(first))
	}
	var beforeAnalyzed, beforeManifest string
	if err := store.db.QueryRowContext(ctx, `SELECT a.analyzed_at,a.manifest_json FROM artifacts a JOIN archive_links l ON l.artifact_id=a.id WHERE l.path=?`, archive.ArchivePath).Scan(&beforeAnalyzed, &beforeManifest); err != nil {
		t.Fatal(err)
	}
	if _, reused, err := store.LookupArchiveAnalysis(ctx, archive.Root, archive.ArchivePath, archive.SizeBytes, archive.Modified); err != nil || !reused {
		t.Fatalf("archive lookup reuse = %v, err %v", reused, err)
	}
	reusedArchive := ScanArchive{Root: archive.Root, ArchivePath: archive.ArchivePath, SizeBytes: archive.SizeBytes, Modified: archive.Modified, Reused: true}
	second := migrationApplyBatch(t, store, root, []ScanArchive{reusedArchive}, 1, 1, 0)
	if len(second) != 1 || second[0].EntityID != first[0].EntityID {
		t.Fatalf("reused scan changed entity identity: before=%#v after=%#v", first, second)
	}
	var afterAnalyzed, afterManifest string
	if err := store.db.QueryRowContext(ctx, `SELECT a.analyzed_at,a.manifest_json FROM artifacts a JOIN archive_links l ON l.artifact_id=a.id WHERE l.path=?`, archive.ArchivePath).Scan(&afterAnalyzed, &afterManifest); err != nil {
		t.Fatal(err)
	}
	if beforeAnalyzed != afterAnalyzed || beforeManifest != afterManifest {
		t.Fatal("reused scan rewrote analyzed artifact metadata")
	}
}

func TestSQLiteScanFailureAndInterruptedMetadataRemainActionable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "scan-metadata.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(filepath.Dir(path), "mods")
	failedID, err := store.BeginScan(ctx, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	scanFailure := errors.New("fixture archive became unreadable")
	if err := store.FinishScan(ctx, failedID, []string{root}, 9, 4, 2, scanFailure); err != nil {
		t.Fatal(err)
	}
	var status, finishedAt, storedError string
	var discovered, analyzed, failed int
	if err := store.db.QueryRowContext(ctx, `SELECT status,finished_at,discovered,analyzed,failed FROM scans WHERE id=?`, failedID).Scan(&status, &finishedAt, &discovered, &analyzed, &failed); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || strings.TrimSpace(finishedAt) == "" || discovered != 9 || analyzed != 4 || failed != 2 {
		t.Fatalf("failed scan metadata = %q,%q,%d,%d,%d", status, finishedAt, discovered, analyzed, failed)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT error FROM scans WHERE id=?`, failedID).Scan(&storedError); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(storedError, scanFailure.Error()) {
		t.Fatalf("failed scan error = %q", storedError)
	}
	interruptedID, err := store.BeginScan(ctx, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.db.QueryRowContext(ctx, `SELECT status,finished_at FROM scans WHERE id=?`, interruptedID).Scan(&status, &finishedAt); err != nil {
		t.Fatal(err)
	}
	if status != "interrupted" || strings.TrimSpace(finishedAt) == "" {
		t.Fatalf("interrupted scan metadata = %q,%q", status, finishedAt)
	}
}

func migrationLegacyCatalog() map[string]any {
	return map[string]any{
		"schemaVersion": 1,
		"createdAt":     "2026-01-01T00:00:00Z",
		"updatedAt":     "2026-01-02T00:00:00Z",
		"lastScan":      nil,
		"mods": []any{
			map[string]any{
				"id": "legacy-repository-mod", "path": `C:\BeamNG\mods\legacy-repository.zip`, "filename": "legacy-repository.zip",
				"enabled": true, "missing": false, "source": "repository", "size": int64(4096), "modifiedAt": "2026-01-01T12:00:00Z",
				"fingerprint": "legacy-repository-fingerprint", "title": "Legacy Repository Vehicle", "archiveDescription": "Imported repository archive",
				"author": "Legacy Author", "version": "3.1.0", "tags": []string{"featured", "vehicle"},
			},
			map[string]any{
				"id": "legacy-user-mod", "path": `D:\Mods\legacy-user.zip`, "filename": "legacy-user.zip",
				"enabled": false, "missing": true, "source": "third-party", "size": int64(8192), "modifiedAt": "2026-01-01T13:00:00Z",
				"fingerprint": "legacy-user-fingerprint", "title": "Legacy User Map", "archiveDescription": "Imported user archive",
				"author": "User Author", "version": "0.9.0", "tags": []string{"maps"},
			},
		},
	}
}

func migrationWriteLegacyCatalog(tb testing.TB, path string, value map[string]any) []byte {
	tb.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		tb.Fatalf("marshal legacy catalog: %v", err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		tb.Fatalf("write legacy catalog: %v", err)
	}
	return encoded
}

func TestSQLiteLegacyJSONImportCreatesBackupAndPreservesParityIdempotently(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "import.sqlite")
	legacyPath := filepath.Join(root, "catalog.json")
	original := migrationWriteLegacyCatalog(t, legacyPath, migrationLegacyCatalog())
	store, err := OpenStore(databasePath, legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := migrationCount(t, store, "entities"); got != 2 {
		t.Fatalf("imported entities = %d, want 2", got)
	}
	for _, expected := range []struct {
		path, source, title, author string
		active                      int
	}{
		{`C:\BeamNG\mods\legacy-repository.zip`, "beamng-repository", "Legacy Repository Vehicle", "Legacy Author", 1},
		{`D:\Mods\legacy-user.zip`, "user-added", "Legacy User Map", "User Author", 0},
	} {
		var source, title, manifestJSON string
		var active int
		if err := store.db.QueryRowContext(ctx, `SELECT l.source_id,e.display_name,a.manifest_json,l.active FROM archive_links l JOIN entities e ON e.id=l.entity_id JOIN artifacts a ON a.id=l.artifact_id WHERE l.path=?`, expected.path).Scan(&source, &title, &manifestJSON, &active); err != nil {
			t.Fatal(err)
		}
		var manifest modkit.Manifest
		if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
			t.Fatal(err)
		}
		author := manifest.Author
		if source != expected.source || title != expected.title || author != expected.author || active != expected.active {
			t.Fatalf("legacy parity for %q = source %q title %q author %q active %d", expected.path, source, title, author, active)
		}
	}
	var firstEntityID string
	if err := store.db.QueryRowContext(ctx, `SELECT entity_id FROM archive_links WHERE path=?`, `C:\BeamNG\mods\legacy-repository.zip`).Scan(&firstEntityID); err != nil {
		t.Fatal(err)
	}
	item, err := store.GetLibraryItem(ctx, firstEntityID)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Tags) != 2 || item.Tags[0].Name == "" || item.Tags[1].Name == "" {
		t.Fatalf("legacy tag parity = %#v", item.Tags)
	}
	backup, err := os.ReadFile(legacyPath + ".bak")
	if err != nil {
		t.Fatalf("legacy backup missing: %v", err)
	}
	if !bytes.Equal(backup, original) {
		t.Fatal("legacy backup differs from original JSON")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(databasePath, legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := migrationCount(t, reopened, "entities"); got != 2 {
		t.Fatalf("idempotent reopen entities = %d, want 2", got)
	}
	if got := migrationCount(t, reopened, "archive_links"); got != 2 {
		t.Fatalf("idempotent reopen links = %d, want 2", got)
	}
	backupAfter, err := os.ReadFile(legacyPath + ".bak")
	if err != nil || !bytes.Equal(backupAfter, original) {
		t.Fatalf("idempotent reopen changed backup: err=%v equal=%v", err, bytes.Equal(backupAfter, original))
	}
}

func TestSQLiteLegacyImportReconcilesDuplicateFingerprintRepresentatives(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "duplicate-fingerprint.sqlite")
	legacyPath := filepath.Join(root, "catalog.json")
	currentPath := `C:\BeamNG\current\mods\repo\shared.zip`
	disabledPath := `C:\BeamNG\library\disabled\shared.zip`
	catalog := migrationLegacyCatalog()
	catalog["mods"] = []any{
		map[string]any{
			"id": "legacy-current-mod", "path": currentPath, "filename": "shared.zip",
			"enabled": true, "missing": false, "source": "third-party", "size": int64(4096), "modifiedAt": "2026-01-01T12:00:00Z",
			"fingerprint": "shared-legacy-fingerprint", "title": "Current Shared Vehicle", "archiveDescription": "Current archive metadata",
		},
		map[string]any{
			"id": "legacy-disabled-mod", "path": disabledPath, "filename": "shared.zip",
			"enabled": false, "missing": false, "source": "repository", "size": int64(4096), "modifiedAt": "2026-01-01T12:00:00Z",
			"fingerprint": "shared-legacy-fingerprint", "title": "Disabled Shared Vehicle", "archiveDescription": "Disabled archive metadata",
		},
	}
	migrationWriteLegacyCatalog(t, legacyPath, catalog)

	store, err := OpenStore(databasePath, legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got := migrationCount(t, store, "entities"); got != 1 {
		t.Fatalf("canonical entities = %d, want 1", got)
	}
	if got := migrationCount(t, store, "archive_links"); got != 2 {
		t.Fatalf("archive links = %d, want 2", got)
	}

	expected := map[string]struct {
		source string
		title  string
	}{
		currentPath:  {source: "user-added", title: "Current Shared Vehicle"},
		disabledPath: {source: "beamng-repository", title: "Disabled Shared Vehicle"},
	}
	var entityID string
	for path, want := range expected {
		var gotEntityID, gotSource string
		if err := store.db.QueryRowContext(ctx, `SELECT entity_id,source_id FROM archive_links WHERE path=?`, path).Scan(&gotEntityID, &gotSource); err != nil {
			t.Fatal(err)
		}
		if gotSource != want.source {
			t.Fatalf("link source for %q = %q, want %q", path, gotSource, want.source)
		}
		if entityID == "" {
			entityID = gotEntityID
		} else if gotEntityID != entityID {
			t.Fatalf("duplicate fingerprint links use entities %q and %q", entityID, gotEntityID)
		}
	}

	var representativePath, linkSource, entitySource, displayName, manifestJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT l.path,l.source_id,e.source_id,e.display_name,a.manifest_json
		FROM entities e
		JOIN archive_links l ON l.id=(SELECT l2.id FROM archive_links l2 WHERE l2.entity_id=e.id ORDER BY l2.active DESC,l2.last_seen_at DESC,l2.id DESC LIMIT 1)
		JOIN artifacts a ON a.id=l.artifact_id
		WHERE e.id=?`, entityID).Scan(&representativePath, &linkSource, &entitySource, &displayName, &manifestJSON); err != nil {
		t.Fatal(err)
	}
	want, ok := expected[representativePath]
	if !ok {
		t.Fatalf("unexpected representative path %q", representativePath)
	}
	if linkSource != want.source || entitySource != want.source || displayName != want.title {
		t.Fatalf("representative = path %q link source %q entity source %q title %q", representativePath, linkSource, entitySource, displayName)
	}
	var manifest modkit.Manifest
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Title != want.title {
		t.Fatalf("representative manifest title = %q, want %q", manifest.Title, want.title)
	}
	var ftsContent string
	if err := store.db.QueryRowContext(ctx, `SELECT content FROM library_search_fts WHERE entity_id=?`, entityID).Scan(&ftsContent); err != nil {
		t.Fatal(err)
	}
	for path := range expected {
		if !strings.Contains(ftsContent, path) {
			t.Fatalf("search content does not contain archive path %q", path)
		}
	}
}

func TestSQLiteLegacyImportRejectsMalformedInvalidSourceAndIndexFailureAtomically(t *testing.T) {
	cases := []struct {
		name    string
		legacy  func(t *testing.T, path string) []byte
		trigger bool
	}{
		{name: "malformed", legacy: func(t *testing.T, path string) []byte {
			value := []byte(`{"schemaVersion":1,"mods":[`)
			if err := os.WriteFile(path, value, 0o600); err != nil {
				t.Fatal(err)
			}
			return value
		}},
		{name: "invalid-source", legacy: func(t *testing.T, path string) []byte {
			catalog := migrationLegacyCatalog()
			mods := catalog["mods"].([]any)
			mods[0].(map[string]any)["source"] = "unsupported-source"
			return migrationWriteLegacyCatalog(t, path, catalog)
		}},
		{name: "index-failure", legacy: func(t *testing.T, path string) []byte {
			return migrationWriteLegacyCatalog(t, path, migrationLegacyCatalog())
		}, trigger: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			databasePath := filepath.Join(root, "rollback.sqlite")
			legacyPath := filepath.Join(root, "catalog.json")
			store, err := OpenStore(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			beforeCounts := make(map[string]int)
			for _, table := range []string{"entities", "artifacts", "archive_links", "mod_tags", "mod_tag_entities", "library_search_fts"} {
				beforeCounts[table] = migrationCount(t, store, table)
			}
			original := tc.legacy(t, legacyPath)
			if tc.trigger {
				if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER migration_import_index_failure BEFORE INSERT ON entities BEGIN SELECT RAISE(ABORT,'fixture index failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.importLegacyCatalogOnce(ctx, legacyPath); err == nil {
				t.Fatal("invalid legacy import unexpectedly succeeded")
			}
			for _, table := range []string{"entities", "artifacts", "archive_links", "mod_tags", "mod_tag_entities", "library_search_fts"} {
				if got := migrationCount(t, store, table); got != beforeCounts[table] {
					t.Fatalf("failed %s import changed %s count from %d to %d", tc.name, table, beforeCounts[table], got)
				}
			}
			preserved, readErr := os.ReadFile(legacyPath)
			if readErr != nil || !bytes.Equal(preserved, original) {
				t.Fatalf("failed %s import changed legacy JSON: err=%v equal=%v", tc.name, readErr, bytes.Equal(preserved, original))
			}
			if backup, readErr := os.ReadFile(legacyPath + ".bak"); readErr == nil && !bytes.Equal(backup, original) {
				t.Fatalf("failed %s import changed backup JSON", tc.name)
			}
		})
	}
}

func TestSQLiteReaderTransactionSeesConsistentPreOrPostScanSnapshot(t *testing.T) {
	ctx := context.Background()
	store, root := openLibraryStorage(t)
	before := migrationFixtureArchive(root, 5)
	if got := migrationApplyBatch(t, store, root, []ScanArchive{before}, 1, 1, 0); len(got) != 1 {
		t.Fatalf("seed items = %d, want 1", len(got))
	}
	readTx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var preCount int
	if err := readTx.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities`).Scan(&preCount); err != nil {
		_ = readTx.Rollback()
		t.Fatal(err)
	}
	after := migrationFixtureArchive(root, 6)
	if got := migrationApplyBatch(t, store, root, []ScanArchive{after}, 1, 1, 0); len(got) != 1 {
		_ = readTx.Rollback()
		t.Fatalf("post snapshot items = %d, want 1", len(got))
	}
	var heldCount int
	if err := readTx.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities`).Scan(&heldCount); err != nil {
		_ = readTx.Rollback()
		t.Fatal(err)
	}
	if heldCount != preCount {
		_ = readTx.Rollback()
		t.Fatalf("reader transaction observed partial snapshot: before=%d held=%d", preCount, heldCount)
	}
	if err := readTx.Commit(); err != nil {
		t.Fatal(err)
	}
	var postCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities`).Scan(&postCount); err != nil {
		t.Fatal(err)
	}
	if postCount != preCount+1 {
		t.Fatalf("post-commit entity count = %d, want %d", postCount, preCount+1)
	}
}

func TestSQLiteVerifyIntegrityReportsActionableForeignKeyFailure(t *testing.T) {
	ctx := context.Background()
	store, root := openLibraryStorage(t)
	_ = root
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO mod_tag_entities(tag_id,entity_id,created_at) VALUES('orphan-tag','orphan-entity',?)`, nowUTC()); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.verifyIntegrity(ctx); err == nil {
		t.Fatal("integrity check accepted an orphaned foreign-key row")
	} else {
		message := strings.ToLower(err.Error())
		if !strings.Contains(message, "foreign") && !strings.Contains(message, "integrity") {
			t.Fatalf("integrity failure was not actionable: %v", err)
		}
	}
}
func TestSQLiteCleanInstallSupportsTestInstallWorkflow(t *testing.T) {
	ctx := context.Background()
	store, root := openLibraryStorage(t)
	archives := []ScanArchive{migrationFixtureArchive(root, 100)}
	items := migrationApplyBatch(t, store, root, archives, 1, 1, 0)
	if len(items) != 1 {
		t.Fatalf("seed items = %d, want 1", len(items))
	}
	item := items[0]
	workspaceID := "clean-install-workspace"
	exportID := "clean-install-export"
	workspaceRoot := filepath.Join(root, "workspace")
	if _, err := store.db.ExecContext(ctx, `INSERT INTO workspaces(
		id,entity_id,artifact_id,root,files_root,source_path,source_sha256,
		created_at,updated_at,status
	) VALUES(?,?,?,?,?,?,?,?,?,'active')`,
		workspaceID, item.EntityID, item.ArtifactID, workspaceRoot,
		filepath.Join(workspaceRoot, "files"), item.ArchivePath, item.SHA256,
		nowUTC(), nowUTC()); err != nil {
		t.Fatalf("seed clean-install workspace: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO exports(
		id,workspace_id,artifact_id,path,sha256,kind,created_at
	) VALUES(?,?,?,?,?,?,?)`,
		exportID, workspaceID, item.ArtifactID, filepath.Join(workspaceRoot, "export.zip"),
		item.SHA256, "vehicle", nowUTC()); err != nil {
		t.Fatalf("seed clean-install export: %v", err)
	}

	record := TestInstallRecord{
		ID: "clean-install-test-install", WorkspaceID: workspaceID, ExportID: exportID,
		Path: filepath.Join(root, "installed.zip"), SHA256: "clean-install-sha",
		InstalledAt: nowUTC(), LogBaselineAt: nowUTC(), LogPath: filepath.Join(root, "game.log"),
		LogOffset: 17, Active: true,
	}
	if err := store.SaveTestInstall(ctx, record); err != nil {
		t.Fatalf("save test install on clean schema: %v", err)
	}
	got, err := store.GetActiveTestInstall(ctx, workspaceID)
	if err != nil {
		t.Fatalf("get test install on clean schema: %v", err)
	}
	if got != record {
		t.Fatalf("saved test install = %#v, want %#v", got, record)
	}
	if err := store.UpdateTestLogBaseline(ctx, record.ID, "updated-game.log", 29, "2026-02-03T04:05:06Z"); err != nil {
		t.Fatalf("update test-install baseline on clean schema: %v", err)
	}
	got, err = store.GetActiveTestInstall(ctx, workspaceID)
	if err != nil {
		t.Fatalf("get updated test install: %v", err)
	}
	if got.LogPath != "updated-game.log" || got.LogOffset != 29 || got.LogBaselineAt != "2026-02-03T04:05:06Z" {
		t.Fatalf("updated test-install baseline = %#v", got)
	}
	if err := store.DeactivateTestInstall(ctx, record.ID); err != nil {
		t.Fatalf("deactivate test install on clean schema: %v", err)
	}
	if _, err := store.GetActiveTestInstall(ctx, workspaceID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deactivated test install lookup = %v, want sql.ErrNoRows", err)
	}

	second := record
	second.ID = "clean-install-test-install-2"
	second.InstalledAt = "2026-02-03T04:05:07Z"
	if err := store.SaveTestInstall(ctx, second); err != nil {
		t.Fatalf("save replacement test install: %v", err)
	}
	var activeCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM test_installs WHERE workspace_id=? AND active=1`, workspaceID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 {
		t.Fatalf("active test installs for one workspace = %d, want 1", activeCount)
	}

	invalid := record
	invalid.ID = "clean-install-invalid-workspace"
	invalid.WorkspaceID = "missing-workspace"
	if err := store.SaveTestInstall(ctx, invalid); err == nil {
		t.Fatal("test install with an unknown workspace was accepted")
	}
	invalid = record
	invalid.ID = "clean-install-invalid-export"
	invalid.ExportID = "missing-export"
	if err := store.SaveTestInstall(ctx, invalid); err == nil {
		t.Fatal("test install with an unknown export was accepted")
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO test_installs(
		id,workspace_id,export_id,path,sha256,installed_at,log_baseline_at,log_path,log_offset,active
	) VALUES(?,?,?,?,?,?,?,?,?,2)`,
		"clean-install-invalid-active", workspaceID, exportID, "bad", "bad", nowUTC(), "", "", 0); err == nil {
		t.Fatal("test install active-state constraint accepted value 2")
	}
}

// downgradeStoreToVersion3 deliberately recreates the tables whose declared
// v4 constraints differ from the v3 schema, including the legacy folder tables
// that the collection migration removes. A v3 database predates test_installs,
// so the fixture removes that v4-only table as well; migration must recreate
// the v4 tables, convert legacy folders into collections, and validate.
func downgradeStoreToVersion3(tb testing.TB, store *Store) {
	ctx := context.Background()
	// PRAGMA foreign_keys is connection-local; use one pooled connection so
	// the fixture's DDL and marker writes target the same SQLite connection.
	store.db.SetMaxOpenConns(1)
	store.db.SetMaxIdleConns(1)
	if _, err := store.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		tb.Fatalf("disable foreign keys for v3 fixture: %v", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		tb.Fatalf("begin v3 fixture rebuild: %v", err)
	}
	rollback := func(err error) {
		_ = tx.Rollback()
		tb.Fatalf("build v3 fixture: %v", err)
	}
	statements := []string{
		`CREATE TEMP TABLE migration_v3_entities AS
			SELECT id,display_name,kind,
				CASE WHEN source_id='beamng-repository' THEN 'repository' ELSE 'third-party' END AS source_class,
				created_at,updated_at
			FROM entities`,
		`CREATE TEMP TABLE migration_v3_archive_links AS
			SELECT id,entity_id,artifact_id,path,root_path,active,
				CASE WHEN source_id='beamng-repository' THEN 'repository' ELSE 'third-party' END AS source_class,
				size_bytes,modified_at,discovered_at,last_seen_at,last_scan_id,basename_key
			FROM archive_links`,
		`DROP TABLE test_installs`,
		`DROP TABLE archive_links`,
		`DROP TABLE entities`,
		`CREATE TABLE entities(
			id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT 'unknown',
			source_class TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT ''
		)`,
		`INSERT INTO entities(id,display_name,kind,source_class,created_at,updated_at)
			SELECT id,display_name,kind,source_class,created_at,updated_at
			FROM migration_v3_entities`,
		`CREATE TABLE archive_links(
			id TEXT PRIMARY KEY,
			entity_id TEXT NOT NULL,
			artifact_id TEXT NOT NULL,
			path TEXT NOT NULL DEFAULT '',
			root_path TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)),
			source_class TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL DEFAULT 0 CHECK(size_bytes >= 0),
			modified_at TEXT NOT NULL DEFAULT '',
			discovered_at TEXT NOT NULL DEFAULT '',
			last_seen_at TEXT NOT NULL DEFAULT '',
			last_scan_id TEXT NOT NULL DEFAULT '',
			basename_key TEXT NOT NULL DEFAULT '',
			FOREIGN KEY(entity_id) REFERENCES entities(id) ON DELETE CASCADE,
			FOREIGN KEY(artifact_id) REFERENCES artifacts(id) ON DELETE CASCADE
		)`,
		`INSERT INTO archive_links(
			id,entity_id,artifact_id,path,root_path,active,source_class,size_bytes,
			modified_at,discovered_at,last_seen_at,last_scan_id,basename_key
		) SELECT id,entity_id,artifact_id,path,root_path,active,source_class,size_bytes,
			modified_at,discovered_at,last_seen_at,last_scan_id,basename_key
			FROM migration_v3_archive_links`,
		`CREATE UNIQUE INDEX migration_v3_archive_links_path_unique
			ON archive_links(path COLLATE NOCASE)`,
		`CREATE TABLE library_folders(
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL COLLATE NOCASE UNIQUE,
			parent_id TEXT,
			position INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT '',
			FOREIGN KEY(parent_id) REFERENCES library_folders(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE library_folder_entities(
			entity_id TEXT PRIMARY KEY,
			folder_id TEXT NOT NULL,
			position INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY(entity_id) REFERENCES entities(id) ON DELETE CASCADE,
			FOREIGN KEY(folder_id) REFERENCES library_folders(id) ON DELETE CASCADE
		)`,
		`DROP TABLE migration_v3_entities`,
		`DROP TABLE migration_v3_archive_links`,
		`UPDATE schema_meta SET version=3`,
		`UPDATE settings SET value='3' WHERE key='schema_version'`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			rollback(err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit v3 fixture: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA user_version=3`); err != nil {
		tb.Fatalf("set v3 user version: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		tb.Fatalf("restore foreign keys after v3 fixture: %v", err)
	}
	var sourceIDColumns int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('entities') WHERE name='source_id'`).Scan(&sourceIDColumns); err != nil {
		tb.Fatalf("inspect v3 entity columns: %v", err)
	}
	if sourceIDColumns != 0 {
		tb.Fatalf("v3 fixture unexpectedly retained entities.source_id")
	}
}

func downgradeEventsToVersion3(tb testing.TB, store *Store) {
	tb.Helper()
	ctx := context.Background()
	store.db.SetMaxOpenConns(1)
	store.db.SetMaxIdleConns(1)
	if _, err := store.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		tb.Fatalf("disable foreign keys for v3 event fixture: %v", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		tb.Fatalf("begin v3 event fixture rebuild: %v", err)
	}
	statements := []string{
		`DROP TABLE events`,
		`CREATE TABLE events(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at TEXT NOT NULL DEFAULT '',
			entity_id TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT '',
			data_json TEXT NOT NULL DEFAULT '{}'
		)`,
		`UPDATE schema_meta SET version=3`,
		`UPDATE settings SET value='3' WHERE key='schema_version'`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			tb.Fatalf("build v3 event fixture: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit v3 event fixture: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA user_version=3`); err != nil {
		tb.Fatalf("set v3 event user version: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		tb.Fatalf("restore foreign keys after v3 event fixture: %v", err)
	}
}

func TestSQLiteVersion3MigrationPreservesGlobalEvent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "global-event-v3.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeEventsToVersion3(t, store)
	const payload = `{"scope":"global","message":"scan started"}`
	if _, err := store.db.ExecContext(ctx, `INSERT INTO events(at,entity_id,type,data_json) VALUES(?,?,?,?)`, "2026-01-03T00:00:00Z", "", "global_scan", payload); err != nil {
		t.Fatalf("insert v3 global event: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal("migrate v3 global event database: ", err)
	}
	defer reopened.Close()
	var entityID, eventType, dataJSON string
	if err := reopened.db.QueryRowContext(ctx, `SELECT entity_id,type,data_json FROM events WHERE type=?`, "global_scan").Scan(&entityID, &eventType, &dataJSON); err != nil {
		t.Fatal("read migrated global event: ", err)
	}
	if entityID != "" || eventType != "global_scan" || dataJSON != payload {
		t.Fatalf("migrated global event = entity %q type %q data %q", entityID, eventType, dataJSON)
	}
}

func TestSQLiteVersion3MigrationRebuildsConstraintsAndConvertsFolderAssignments(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "genuine-v3.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(filepath.Dir(path), "mods")
	archive := migrationFixtureArchive(root, 101)
	seed := migrationApplyBatch(t, store, root, []ScanArchive{archive}, 1, 1, 0)
	if len(seed) != 1 {
		t.Fatalf("seed items = %d, want 1", len(seed))
	}
	parentName, childName := "v3 parent", "v3 child"
	parentID, childID := "v3-parent-folder", "v3-child-folder"
	downgradeStoreToVersion3(t, store)
	// Legacy folder rows can only exist in the v3 shape, so seed them after the
	// downgrade and let the migration convert them into collections.
	if _, err := store.db.ExecContext(ctx, `INSERT INTO library_folders(id,name,parent_id,position,created_at,updated_at) VALUES(?,?,NULL,0,?,?),(?,?,?,1,?,?)`,
		parentID, parentName, nowUTC(), nowUTC(),
		childID, childName, parentID, nowUTC(), nowUTC()); err != nil {
		t.Fatalf("seed legacy folders: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO library_folder_entities(entity_id,folder_id,position) VALUES(?,?,0)`, seed[0].EntityID, childID); err != nil {
		t.Fatalf("seed legacy folder assignment: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var version int
	if err := reopened.db.QueryRowContext(ctx, `SELECT version FROM schema_meta`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != storeSchemaVersion {
		t.Fatalf("migrated genuine v3 schema version = %d, want %d", version, storeSchemaVersion)
	}
	var sourceForeignKeys int
	if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_list('entities') WHERE "table"='source_classifications' AND "from"='source_id'`).Scan(&sourceForeignKeys); err != nil {
		t.Fatal(err)
	}
	if sourceForeignKeys != 1 {
		t.Fatalf("entities source foreign key count = %d, want 1", sourceForeignKeys)
	}
	if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_list('archive_links') WHERE "table"='source_classifications' AND "from"='source_id'`).Scan(&sourceForeignKeys); err != nil {
		t.Fatal(err)
	}
	if sourceForeignKeys != 1 {
		t.Fatalf("archive_links source foreign key count = %d, want 1", sourceForeignKeys)
	}
	var testInstallTable, testInstallWorkspaceFK, testInstallExportFK, testInstallUnique int
	if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='test_installs'`).Scan(&testInstallTable); err != nil {
		t.Fatal(err)
	}
	if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_list('test_installs') WHERE "table"='workspaces' AND "from"='workspace_id' AND on_delete='CASCADE'`).Scan(&testInstallWorkspaceFK); err != nil {
		t.Fatal(err)
	}
	if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_list('test_installs') WHERE "table"='exports' AND "from"='export_id' AND on_delete='CASCADE'`).Scan(&testInstallExportFK); err != nil {
		t.Fatal(err)
	}
	if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='test_installs_workspace_active_unique_idx'`).Scan(&testInstallUnique); err != nil {
		t.Fatal(err)
	}
	if testInstallTable != 1 || testInstallWorkspaceFK != 1 || testInstallExportFK != 1 || testInstallUnique != 1 {
		t.Fatalf("migrated test-install schema = table %d, workspace FK %d, export FK %d, unique index %d", testInstallTable, testInstallWorkspaceFK, testInstallExportFK, testInstallUnique)
	}

	var entityID, artifactID string
	if err := reopened.db.QueryRowContext(ctx, `SELECT entity_id,artifact_id FROM archive_links WHERE path=?`, archive.ArchivePath).Scan(&entityID, &artifactID); err != nil {
		t.Fatal(err)
	}
	if entityID != seed[0].EntityID {
		t.Fatalf("v3 migration changed entity identity from %q to %q", seed[0].EntityID, entityID)
	}
	if _, err := reopened.db.ExecContext(ctx, `INSERT INTO entities(id,display_name,kind,source_id,created_at,updated_at) VALUES('unknown-source-entity','bad','vehicle','not-a-source',?,?)`, nowUTC(), nowUTC()); err == nil {
		t.Fatal("v3 migration accepted an unknown entity source")
	}
	if _, err := reopened.db.ExecContext(ctx, `INSERT INTO archive_links(
		id,entity_id,artifact_id,path,root_path,active,source_id,size_bytes,modified_at,
		discovered_at,last_seen_at,last_scan_id,basename_key
	) VALUES('unknown-source-link',?,?,?,?,?,'not-a-source',0,?,?,?,?,?)`,
		entityID, artifactID, filepath.Join(root, "unknown.zip"), root, 1,
		nowUTC(), nowUTC(), nowUTC(), "", "unknown"); err == nil {
		t.Fatal("v3 migration accepted an unknown archive-link source")
	}
	for _, table := range []string{"library_folders", "library_folder_entities"} {
		var present int
		if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&present); err != nil {
			t.Fatal(err)
		}
		if present != 0 {
			t.Fatalf("legacy organization table %s survived the collection migration", table)
		}
	}
	var migratedParentID, migratedChildID string
	if err := reopened.db.QueryRowContext(ctx, `SELECT id FROM collections WHERE name=?`, parentName).Scan(&migratedParentID); err != nil {
		t.Fatalf("migrated parent collection %q: %v", parentName, err)
	}
	if err := reopened.db.QueryRowContext(ctx, `SELECT id FROM collections WHERE name=?`, childName).Scan(&migratedChildID); err != nil {
		t.Fatalf("migrated child collection %q: %v", childName, err)
	}
	var edgeCount int
	if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collection_children WHERE parent_id=? AND child_id=?`, migratedParentID, migratedChildID).Scan(&edgeCount); err != nil {
		t.Fatal(err)
	}
	if edgeCount != 1 {
		t.Fatalf("migrated folder hierarchy edge count = %d, want 1", edgeCount)
	}
	var membershipCount int
	if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collection_mods WHERE entity_id=? AND collection_id=?`, seed[0].EntityID, migratedChildID).Scan(&membershipCount); err != nil {
		t.Fatal(err)
	}
	if membershipCount != 1 {
		t.Fatalf("migrated folder assignment count = %d, want 1", membershipCount)
	}
	// Deleting a parent collection must not remove a shared child collection or
	// its mods, unlike the legacy cascade semantics.
	if _, err := reopened.db.ExecContext(ctx, `DELETE FROM collections WHERE id=?`, migratedParentID); err != nil {
		t.Fatalf("delete migrated parent collection: %v", err)
	}
	if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collection_mods WHERE entity_id=? AND collection_id=?`, seed[0].EntityID, migratedChildID).Scan(&membershipCount); err != nil {
		t.Fatal(err)
	}
	if membershipCount != 1 {
		t.Fatalf("deleting the migrated parent removed child membership: count=%d", membershipCount)
	}
}

func TestSQLiteRejectsNewerSchemaVersionWithoutRewritingMarker(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "newer.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE schema_meta SET version=?`, storeSchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE settings SET value=? WHERE key='schema_version'`, fmt.Sprint(storeSchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version=%d`, storeSchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err == nil {
		_ = reopened.Close()
		t.Fatal("opening a database newer than the supported schema unexpectedly succeeded")
	}
	raw, readErr := sql.Open("sqlite", path+`?_pragma=foreign_keys(1)`)
	if readErr != nil {
		t.Fatal(readErr)
	}
	defer raw.Close()
	var version int
	if err := raw.QueryRowContext(ctx, `SELECT version FROM schema_meta`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != storeSchemaVersion+1 {
		t.Fatalf("newer schema marker changed to %d after rejected open", version)
	}
}

func legacyCatalogModForArchive(path, fingerprint, id string) map[string]any {
	return map[string]any{
		"id": id, "path": path, "filename": filepath.Base(path),
		"originalPath": path, "location": "library", "enabled": true, "missing": false,
		"source": "repository", "activeRelativePath": filepath.Join("_managed", filepath.Base(path)),
		"size": int64(4096), "modifiedAt": "2026-01-01T12:00:00Z",
		"fingerprint": fingerprint, "sha256": "legacy-sha", "fullSha256": "legacy-sha",
		"validArchive": true, "entryCount": 1, "compressedBytes": uint64(128),
		"uncompressedBytes": uint64(256), "wrapper": nil, "autoCategory": "vehicle",
		"kind": "vehicle", "contentTags": []string{"vehicle"}, "namespaces": map[string][]string{"vehicles": {"legacy_namespace"}},
		"nestedArchiveCount": 0, "title": "Legacy Reconciled Vehicle",
		"archiveDescription": "archive description", "description": "user description",
		"author": "Legacy Author", "version": "3.1.0", "metadataDocuments": []any{},
		"previewPath": nil, "database": json.RawMessage(`null`), "issues": []any{},
		"tags": []any{"legacy-tag"}, "notes": "legacy notes", "problematic": false,
		"categoryOverride": nil, "runtimeIssues": []any{}, "health": "healthy",
		"manifest": json.RawMessage(fmt.Sprintf(`{"archivePath":%q,"filename":%q,"centralFingerprint":%q,"fullSha256":"legacy-sha","kind":"vehicle","title":"Legacy Reconciled Vehicle"}`, path, filepath.Base(path), fingerprint)),
	}
}

func TestSQLiteLegacyImportReconcilesExistingV3PathAndFingerprint(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "reconcile.sqlite")
	legacyPath := filepath.Join(root, "catalog.json")
	store, err := OpenStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "existing-v3.zip")
	archive := migrationFixtureArchive(root, 102)
	archive.ArchivePath = archivePath
	archive.Manifest.ArchivePath = archivePath
	archive.Manifest.CentralFingerprint = "legacy-reconcile-fingerprint"
	archive.Manifest.FullSHA256 = "legacy-sha"
	archive.SourceClass = "beamng-repository"
	seed := migrationApplyBatch(t, store, root, []ScanArchive{archive}, 1, 1, 0)
	if len(seed) != 1 {
		t.Fatalf("seed items = %d, want 1", len(seed))
	}
	var existingLinkID, existingEntityID, existingArtifactID string
	if err := store.db.QueryRowContext(ctx, `SELECT id,entity_id,artifact_id FROM archive_links WHERE path=?`, archivePath).Scan(&existingLinkID, &existingEntityID, &existingArtifactID); err != nil {
		t.Fatal(err)
	}
	downgradeStoreToVersion3(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{
		"schemaVersion": 1, "createdAt": "2026-01-01T00:00:00Z",
		"updatedAt": "2026-01-02T00:00:00Z", "lastScan": nil,
		"mods": []any{legacyCatalogModForArchive(strings.ToUpper(archivePath), "legacy-reconcile-fingerprint", "legacy-import-id")},
	}
	migrationWriteLegacyCatalog(t, legacyPath, legacy)
	reopened, err := OpenStore(databasePath, legacyPath)
	if err != nil {
		t.Fatal("open/import existing v3 database: ", err)
	}
	defer reopened.Close()
	if got := migrationCount(t, reopened, "entities"); got != 1 {
		t.Fatalf("reconciled entity count = %d, want 1", got)
	}
	if got := migrationCount(t, reopened, "archive_links"); got != 1 {
		t.Fatalf("reconciled link count = %d, want 1", got)
	}
	var linkID, entityID, artifactID, pathValue string
	if err := reopened.db.QueryRowContext(ctx, `SELECT id,entity_id,artifact_id,path FROM archive_links WHERE path=? COLLATE NOCASE`, archivePath).Scan(&linkID, &entityID, &artifactID, &pathValue); err != nil {
		t.Fatal(err)
	}
	if linkID != existingLinkID || entityID != existingEntityID || artifactID != existingArtifactID {
		t.Fatalf("v3 reconciliation changed canonical identities: link=%q/%q entity=%q/%q artifact=%q/%q", linkID, existingLinkID, entityID, existingEntityID, artifactID, existingArtifactID)
	}
	if !strings.EqualFold(pathValue, archivePath) {
		t.Fatalf("reconciled archive path = %q, want case-insensitive %q", pathValue, archivePath)
	}
	var marker string
	if err := reopened.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, legacyCatalogImportMarker).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != "1" {
		t.Fatalf("successful reconciled import marker = %q, want 1", marker)
	}
	imported, err := reopened.GetLibraryItem(ctx, entityID)
	if err != nil {
		t.Fatal("hydrate reconciled legacy item: ", err)
	}
	if imported.Manifest.Description != "user description" {
		t.Fatalf("imported manifest description = %q, want user description over archive description", imported.Manifest.Description)
	}

}

func TestSQLiteLegacyImportMergesPathAndFingerprintIdentityConflict(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "cross-identity.sqlite")
	legacyPath := filepath.Join(root, "catalog.json")
	store, err := OpenStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	archiveA := migrationFixtureArchive(root, 201)
	archiveA.ArchivePath = filepath.Join(root, "path-owner.zip")
	archiveA.Manifest.ArchivePath = archiveA.ArchivePath
	archiveA.Manifest.Filename = filepath.Base(archiveA.ArchivePath)
	archiveA.Manifest.CentralFingerprint = "cross-identity-fingerprint-a"
	archiveA.Manifest.FullSHA256 = "cross-identity-sha-a"
	archiveA.SourceClass = "beamng-repository"
	archiveB := migrationFixtureArchive(root, 202)
	archiveB.ArchivePath = filepath.Join(root, "fingerprint-owner.zip")
	archiveB.Manifest.ArchivePath = archiveB.ArchivePath
	archiveB.Manifest.Filename = filepath.Base(archiveB.ArchivePath)
	archiveB.Manifest.CentralFingerprint = "cross-identity-fingerprint-b"
	archiveB.Manifest.FullSHA256 = "legacy-sha"
	archiveB.SourceClass = "user-added"
	seed := migrationApplyBatch(t, store, root, []ScanArchive{archiveA, archiveB}, 2, 2, 0)
	if len(seed) != 2 {
		t.Fatalf("cross-identity seed items = %d, want 2", len(seed))
	}
	var entityA, entityB string
	if err := store.db.QueryRowContext(ctx, `SELECT entity_id FROM archive_links WHERE path=?`, archiveA.ArchivePath).Scan(&entityA); err != nil {
		t.Fatal("read path-owner entity: ", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT entity_id FROM archive_links WHERE path=?`, archiveB.ArchivePath).Scan(&entityB); err != nil {
		t.Fatal("read fingerprint-owner entity: ", err)
	}
	if entityA == entityB {
		t.Fatalf("cross-identity seed unexpectedly shared entity %q", entityA)
	}
	legacy := map[string]any{
		"schemaVersion": 1,
		"createdAt":     "2026-01-01T00:00:00Z",
		"updatedAt":     "2026-01-02T00:00:00Z",
		"lastScan":      nil,
		"mods": []any{
			legacyCatalogModForArchive(strings.ToUpper(archiveA.ArchivePath), archiveB.Manifest.CentralFingerprint, "legacy-cross-identity"),
		},
	}
	migrationWriteLegacyCatalog(t, legacyPath, legacy)
	if err := store.importLegacyCatalogOnce(ctx, legacyPath); err != nil {
		t.Fatalf("import path/fingerprint conflict: %v", err)
	}
	if got := migrationCount(t, store, "entities"); got != 1 {
		t.Fatalf("cross-identity import left %d entities, want 1", got)
	}
	if got := migrationCount(t, store, "archive_links"); got != 2 {
		t.Fatalf("cross-identity import left %d links, want 2", got)
	}
	var distinctEntities int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT entity_id) FROM archive_links`).Scan(&distinctEntities); err != nil {
		t.Fatal(err)
	}
	if distinctEntities != 1 {
		t.Fatalf("cross-identity links reference %d entities, want 1", distinctEntities)
	}
	var canonicalEntity string
	if err := store.db.QueryRowContext(ctx, `SELECT entity_id FROM archive_links WHERE path=? COLLATE NOCASE`, archiveA.ArchivePath).Scan(&canonicalEntity); err != nil {
		t.Fatal("read canonical path-owner link: ", err)
	}
	if canonicalEntity != entityA {
		t.Fatalf("path-owner entity changed from %q to %q during conflict merge", entityA, canonicalEntity)
	}
	var survivingDuplicate int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities WHERE id=?`, entityB).Scan(&survivingDuplicate); err != nil {
		t.Fatal(err)
	}
	if survivingDuplicate != 0 {
		t.Fatalf("fingerprint-owner entity %q survived conflict merge", entityB)
	}
}

func richLegacyCatalog(lastScan any) map[string]any {
	return map[string]any{
		"schemaVersion": 1,
		"createdAt":     "2026-01-01T00:00:00Z",
		"updatedAt":     "2026-01-02T00:00:00Z",
		"lastScan":      lastScan,
		"mods": []any{
			map[string]any{
				"id": "lossless-null-fields", "path": `C:\BeamNG\mods\lossless-null.zip`, "filename": "lossless-null.zip",
				"originalPath": `C:\BeamNG\mods\lossless-original.zip`, "location": "archive", "enabled": true, "missing": false,
				"source": "repository", "activeRelativePath": `_managed\lossless-null.zip`, "size": int64(1111),
				"modifiedAt": "2026-01-01T12:00:00Z", "fingerprint": "lossless-null-fingerprint",
				"sha256": "lossless-null-sha", "fullSha256": "lossless-null-full-sha", "validArchive": true,
				"entryCount": 7, "compressedBytes": uint64(17), "uncompressedBytes": uint64(71), "wrapper": nil,
				"autoCategory": "vehicle", "kind": "vehicle", "contentTags": []string{"content-null"},
				"namespaces": map[string][]string{"vehicles": {"lossless_null_namespace"}}, "nestedArchiveCount": 2,
				"title": "Lossless Null Vehicle", "archiveDescription": nil, "description": "user-edited null-side description",
				"author": nil, "version": nil, "metadataDocuments": []any{map[string]any{"path": "metadata.json", "data": map[string]any{"nullSide": true}}},
				"previewPath": nil, "database": json.RawMessage(`null`), "issues": []any{}, "tags": []any{"null-tag"},
				"notes": "null-side notes", "problematic": true, "categoryOverride": nil, "runtimeIssues": []any{},
				"health": "broken", "manifest": json.RawMessage(`{"archivePath":"C:\\BeamNG\\mods\\lossless-null.zip","filename":"lossless-null.zip","centralFingerprint":"lossless-null-fingerprint","fullSha256":"lossless-null-full-sha","kind":"vehicle","title":"Lossless Null Vehicle"}`),
			},
			map[string]any{
				"id": "lossless-empty-fields", "path": `D:\Mods\lossless-empty.zip`, "filename": "lossless-empty.zip",
				"originalPath": `D:\Mods\lossless-original.zip`, "location": "library", "enabled": false, "missing": true,
				"source": "third-party", "activeRelativePath": `_managed\lossless-empty.zip`, "size": int64(2222),
				"modifiedAt": "2026-01-01T13:00:00Z", "fingerprint": "lossless-empty-fingerprint",
				"sha256": "lossless-empty-sha", "fullSha256": "lossless-empty-full-sha", "validArchive": false,
				"entryCount": 0, "compressedBytes": uint64(0), "uncompressedBytes": uint64(0), "wrapper": "",
				"autoCategory": "map", "kind": "map", "contentTags": []string{}, "namespaces": map[string][]string{},
				"nestedArchiveCount": 0, "title": "Lossless Empty Map", "archiveDescription": "",
				"description": "", "author": "", "version": "", "metadataDocuments": []any{},
				"previewPath": "", "database": json.RawMessage(`{}`), "issues": []any{}, "tags": []any{"empty-tag"},
				"notes": "", "problematic": false, "categoryOverride": "", "runtimeIssues": []any{},
				"health": "unscanned", "manifest": json.RawMessage(`{"archivePath":"D:\\Mods\\lossless-empty.zip","filename":"lossless-empty.zip","centralFingerprint":"lossless-empty-fingerprint","kind":"map","title":"Lossless Empty Map"}`),
			},
		},
	}
}

func assertLegacyPayloadRows(t *testing.T, store *Store, original []byte, catalog map[string]any, expectedLastScan any) {
	t.Helper()
	ctx := context.Background()
	var stateStatus string
	var stateVersion int
	var catalogJSON string
	var present, isNull int
	var lastScanJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT status,schema_version,catalog_json,last_scan_present,last_scan_is_null,last_scan_json FROM legacy_catalog_state WHERE id=1`).Scan(&stateStatus, &stateVersion, &catalogJSON, &present, &isNull, &lastScanJSON); err != nil {
		t.Fatalf("read lossless legacy state: %v", err)
	}
	if stateStatus != "imported" || stateVersion != legacyCatalogSchemaVersion {
		t.Fatalf("stored legacy state marker = %q schema=%d, want imported/%d", stateStatus, stateVersion, legacyCatalogSchemaVersion)
	}
	if !bytes.Equal([]byte(catalogJSON), bytes.TrimSpace(original)) {
		t.Fatalf("stored legacy catalog JSON differs from source")
	}
	expectedLastScanJSON, err := json.Marshal(expectedLastScan)
	if err != nil {
		t.Fatal(err)
	}
	if expectedLastScan == nil {
		if present != 1 || isNull != 1 || lastScanJSON != string(expectedLastScanJSON) {
			t.Fatalf("stored null lastScan state = present %d null %d json %q, want raw %q", present, isNull, lastScanJSON, expectedLastScanJSON)
		}
	} else {
		if present != 1 || isNull != 0 || lastScanJSON != string(expectedLastScanJSON) {
			t.Fatalf("stored object lastScan state = present %d null %d json %q, want %q", present, isNull, lastScanJSON, expectedLastScanJSON)
		}
	}
	mods, ok := catalog["mods"].([]any)
	if !ok {
		t.Fatal("rich legacy fixture mods lost its array type")
	}
	for ordinal, expected := range mods {
		expectedJSON, err := json.Marshal(expected)
		if err != nil {
			t.Fatal(err)
		}
		var gotJSON string
		if err := store.db.QueryRowContext(ctx, `SELECT payload_json FROM legacy_catalog_mods WHERE ordinal=?`, ordinal).Scan(&gotJSON); err != nil {
			t.Fatalf("read lossless legacy mod %d: %v", ordinal, err)
		}
		if !bytes.Equal([]byte(gotJSON), expectedJSON) {
			t.Fatalf("stored legacy mod %d payload differs: got=%s want=%s", ordinal, gotJSON, expectedJSON)
		}
	}
}

func TestSQLiteLegacyImportRetainsEveryFieldAndNullEmptyParity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lastScan any
	}{
		{name: "object-last-scan", lastScan: map[string]any{"startedAt": "2026-01-03T00:00:00Z", "finishedAt": "2026-01-03T00:01:00Z", "discovered": 2, "failed": 1}},
		{name: "null-last-scan", lastScan: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			databasePath := filepath.Join(root, "lossless.sqlite")
			legacyPath := filepath.Join(root, "catalog.json")
			catalog := richLegacyCatalog(tc.lastScan)
			original := migrationWriteLegacyCatalog(t, legacyPath, catalog)
			store, err := OpenStore(databasePath, legacyPath)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			assertLegacyPayloadRows(t, store, original, catalog, tc.lastScan)
			var marker string
			if err := store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, legacyCatalogImportMarker).Scan(&marker); err != nil {
				t.Fatal(err)
			}
			if marker != "1" {
				t.Fatalf("lossless import marker = %q, want 1", marker)
			}
			if got := migrationCount(t, store, "legacy_catalog_mods"); got != 2 {
				t.Fatalf("lossless legacy mod payload rows = %d, want 2", got)
			}
		})
	}
}

func TestSQLiteAbsentLegacyCatalogRecordsCutoverAndIgnoresLaterJSON(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "absent.sqlite")
	legacyPath := filepath.Join(root, "catalog.json")
	store, err := OpenStore(databasePath, legacyPath)
	if err != nil {
		t.Fatal("open absent legacy catalog: ", err)
	}
	var marker string
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, legacyCatalogImportMarker).Scan(&marker); err != nil {
		t.Fatal("read absent cutover marker: ", err)
	}
	if marker != "absent" {
		t.Fatalf("absent legacy catalog marker = %q, want absent", marker)
	}
	if got := migrationCount(t, store, "entities"); got != 0 {
		t.Fatalf("absent legacy catalog created %d entities", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	migrationWriteLegacyCatalog(t, legacyPath, migrationLegacyCatalog())
	reopened, err := OpenStore(databasePath, legacyPath)
	if err != nil {
		t.Fatal("reopen after stale legacy catalog appeared: ", err)
	}
	defer reopened.Close()
	if got := migrationCount(t, reopened, "entities"); got != 0 {
		t.Fatalf("stale JSON after absent cutover imported %d entities", got)
	}
	if _, err := os.Stat(legacyPath + ".bak"); err == nil {
		t.Fatal("stale JSON after absent cutover was backed up/imported")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := reopened.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, legacyCatalogImportMarker).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != "absent" {
		t.Fatalf("absent cutover marker changed after stale JSON appeared to %q", marker)
	}
}

func TestSQLiteFailedAnalysisPreservesExistingLinks(t *testing.T) {
	ctx := context.Background()
	store, root := openLibraryStorage(t)
	archive := migrationFixtureArchive(root, 103)
	initial := migrationApplyBatch(t, store, root, []ScanArchive{archive}, 1, 1, 0)
	if len(initial) != 1 {
		t.Fatalf("seed items = %d, want 1", len(initial))
	}
	scanID, err := store.BeginScan(ctx, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyScanBatch(ctx, scanID, []string{root}, nil, nil, 1, 0, 1); err == nil {
		t.Fatal("failed analysis batch unexpectedly committed")
	}
	if err := store.FinishScan(ctx, scanID, []string{root}, 1, 0, 1, errors.New("archive analysis failed")); err != nil {
		t.Fatal("finish failed analysis scan: ", err)
	}
	var active int
	if err := store.db.QueryRowContext(ctx, `SELECT active FROM archive_links WHERE path=?`, archive.ArchivePath).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("failed analysis deactivated still-present link: active=%d", active)
	}
	item, err := store.GetLibraryItem(ctx, initial[0].EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if !item.Linked {
		t.Fatal("failed analysis marked still-present item unlinked")
	}
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM scans WHERE id=?`, scanID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("failed analysis scan status = %q, want failed", status)
	}
}

func TestSQLiteCommittedScanDoesNotBecomeFailureWhenHydrationContextCancels(t *testing.T) {
	ctx := context.Background()
	store, root := openLibraryStorage(t)
	archives := libraryFixtureArchives(root, 5_005, 20_000)
	scanID, err := store.BeginScan(ctx, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	cancelled := make(chan struct{})
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		defer close(cancelled)
		for range ticker.C {
			var status string
			if err := store.db.QueryRowContext(ctx, `SELECT status FROM scans WHERE id=?`, scanID).Scan(&status); err == nil && status == "complete" {
				cancel()
				return
			}
		}
	}()
	items, applyErr := store.ApplyScanBatch(cancelCtx, scanID, []string{root}, nil, archives, len(archives), len(archives), 0)
	select {
	case <-cancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for scan commit observation")
	}
	if applyErr != nil {
		t.Fatalf("committed scan returned hydration cancellation as an error: %v", applyErr)
	}
	if len(items) != len(archives) {
		t.Fatalf("committed scan hydrated %d items, want %d", len(items), len(archives))
	}
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM scans WHERE id=?`, scanID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "complete" {
		t.Fatalf("committed scan status = %q, want complete", status)
	}
}
