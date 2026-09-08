package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func terrainTagForTest(t *testing.T, store *Store) (id, color, icon string) {
	t.Helper()
	if err := store.db.QueryRowContext(context.Background(), `SELECT id,color,icon FROM mod_tags WHERE name=? COLLATE NOCASE`, "Terrain").Scan(&id, &color, &icon); err != nil {
		t.Fatalf("read Terrain tag: %v", err)
	}
	return id, color, icon
}

func assertSingleTerrainTag(t *testing.T, store *Store) {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM mod_tags WHERE name=? COLLATE NOCASE`, "Terrain").Scan(&count); err != nil {
		t.Fatalf("count Terrain tags: %v", err)
	}
	if count != 1 {
		t.Fatalf("Terrain tag count = %d, want 1", count)
	}
	var mapCount int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM mod_tags WHERE name=? COLLATE NOCASE`, "Map").Scan(&mapCount); err != nil {
		t.Fatalf("count Map tags: %v", err)
	}
	if mapCount != 0 {
		t.Fatalf("unexpected Map default tag count = %d", mapCount)
	}
}

func assertDefaultTerrainVisual(t *testing.T, store *Store) {
	t.Helper()
	_, color, icon := terrainTagForTest(t, store)
	if color != defaultModTagColor || icon != defaultModTagIcon {
		t.Fatalf("Terrain defaults = %q/%q, want %q/%q", color, icon, defaultModTagColor, defaultModTagIcon)
	}
}

func TestCleanInstallSeedsTerrainDefaultTagIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clean.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	assertSingleTerrainTag(t, store)
	assertDefaultTerrainVisual(t, store)
	if got := migrationCount(t, store, "mod_tags"); got != len(exampleModTagNames) {
		t.Fatalf("clean default tag count = %d, want %d", got, len(exampleModTagNames))
	}
	if got := migrationCount(t, store, "mod_tag_entities"); got != 0 {
		t.Fatalf("clean tag assignment count = %d, want 0", got)
	}
	if got := migrationCount(t, store, "library_search_fts"); got != 0 {
		t.Fatalf("clean FTS row count = %d, want 0", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("idempotent reopen: %v", err)
	}
	defer reopened.Close()
	assertSingleTerrainTag(t, reopened)
	assertDefaultTerrainVisual(t, reopened)
	if got := migrationCount(t, reopened, "mod_tags"); got != len(exampleModTagNames) {
		t.Fatalf("reopened default tag count = %d, want %d", got, len(exampleModTagNames))
	}
}

// The Tag editor offers every name in modTagIcons, so each must survive persistence.
func TestTagVisualAcceptsEveryOfferedIcon(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "icons.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id, _, _ := terrainTagForTest(t, store)

	for _, icon := range modTagIcons {
		if err := store.UpdateModTagVisual(context.Background(), id, "#3f93c5", icon); err != nil {
			t.Fatalf("icon %q rejected: %v", icon, err)
		}
		var stored string
		if err := store.db.QueryRowContext(context.Background(), `SELECT icon FROM mod_tags WHERE id=?`, id).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != icon {
			t.Fatalf("stored icon = %q, want %q", stored, icon)
		}
	}

	if err := store.UpdateModTagVisual(context.Background(), id, "#3f93c5", "rocket"); err == nil {
		t.Fatal("unsupported tag icon was accepted")
	}
}

func TestCurrentV4MigrationAddsTerrainAndPreservesFTSAndAssignments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current-v4.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(filepath.Dir(path), "mods")
	items := migrationApplyBatch(t, store, root, []ScanArchive{migrationFixtureArchive(root, 901)}, 1, 1, 0)
	if len(items) != 1 {
		t.Fatalf("fixture items = %d, want 1", len(items))
	}
	var carID string
	if err := store.db.QueryRowContext(context.Background(), `SELECT id FROM mod_tags WHERE name=? COLLATE NOCASE`, "Car").Scan(&carID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLibraryItemTags(context.Background(), items[0].EntityID, []string{carID}); err != nil {
		t.Fatal(err)
	}
	if got := migrationCount(t, store, "mod_tag_entities"); got != 1 {
		t.Fatalf("initial tag assignment count = %d, want 1", got)
	}
	if got := migrationCount(t, store, "library_search_fts"); got != 1 {
		t.Fatalf("initial FTS row count = %d, want 1", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`DELETE FROM mod_tags WHERE name='Terrain' COLLATE NOCASE`); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`DELETE FROM settings WHERE key=?`, terrainTagSeedKey); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("upgrade current v4 database: %v", err)
	}
	assertSingleTerrainTag(t, reopened)
	assertDefaultTerrainVisual(t, reopened)
	if got := migrationCount(t, reopened, "mod_tags"); got != len(exampleModTagNames) {
		t.Fatalf("upgraded default tag count = %d, want %d", got, len(exampleModTagNames))
	}
	if got := migrationCount(t, reopened, "mod_tag_entities"); got != 1 {
		t.Fatalf("upgraded tag assignment count = %d, want 1", got)
	}
	if got := migrationCount(t, reopened, "library_search_fts"); got != 1 {
		t.Fatalf("upgraded FTS row count = %d, want 1", got)
	}
	if ids := migrationFTSEntityMatches(t, reopened, "Car"); len(ids) != 1 || ids[0] != items[0].EntityID {
		t.Fatalf("upgraded FTS assignment match = %#v", ids)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	repeated, err := OpenStore(path)
	if err != nil {
		t.Fatalf("repeated current v4 reopen: %v", err)
	}
	defer repeated.Close()
	assertSingleTerrainTag(t, repeated)
	assertDefaultTerrainVisual(t, repeated)
	if got := migrationCount(t, repeated, "mod_tags"); got != len(exampleModTagNames) {
		t.Fatalf("repeated default tag count = %d, want %d", got, len(exampleModTagNames))
	}
}

func TestTerrainDefaultMigrationPreservesCustomizedExistingTag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom-terrain.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	terrainID, _, _ := terrainTagForTest(t, store)
	if err := store.UpdateModTagVisual(context.Background(), terrainID, "#123456", "map"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `DELETE FROM settings WHERE key=?`, terrainTagSeedKey); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen customized Terrain database: %v", err)
	}
	defer reopened.Close()
	assertSingleTerrainTag(t, reopened)
	id, color, icon := terrainTagForTest(t, reopened)
	if id != terrainID || color != "#123456" || icon != "map" {
		t.Fatalf("customized Terrain = %q/%q/%q, want %q/%q/%q", id, color, icon, terrainID, "#123456", "map")
	}
}
