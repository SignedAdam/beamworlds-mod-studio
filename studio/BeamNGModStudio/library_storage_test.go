package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

// Library query benchmarks use an untimed cold database setup and then issue
// warm queries against one open SQLite connection. Fixture sizes are 32 and
// 5,005 entities; the incremental benchmark reports archives/second for one
// transactional refresh of the large fixture. Benchmark results are recorded
// by the parent verification phase, since this file intentionally does not
// prescribe a machine-specific latency target.

func openLibraryStorage(tb testing.TB) (*Store, string) {
	tb.Helper()
	root := tb.TempDir()
	filename := filepath.Join(root, "library.sqlite")
	store, err := OpenStore(filename)
	if err != nil {
		tb.Fatalf("open library store: %v", err)
	}
	tb.Cleanup(func() { _ = store.Close() })
	return store, filepath.Join(root, "mods")
}

func libraryFixtureArchives(root string, count, offset int) []ScanArchive {
	archives := make([]ScanArchive, count)
	for index := range archives {
		serial := offset + index
		path := filepath.Join(root, fmt.Sprintf("library-%05d.zip", serial))
		kind := modkit.KindVehicle
		switch serial % 4 {
		case 1:
			kind = modkit.KindMap
		case 2:
			kind = modkit.KindUI
		case 3:
			kind = modkit.KindScript
		}
		title := fmt.Sprintf("Library Mod %05d", serial)
		if serial == 0 {
			title = "Road Runner"
		}
		author := fmt.Sprintf("Author %05d", serial)
		if serial == 0 {
			author = "Ava Builder"
		}
		namespace := fmt.Sprintf("namespace_%05d", serial)
		if serial == 0 {
			namespace = "road_namespace"
		}
		modified := time.Unix(1_700_000_000+int64(serial), 0).UTC()
		archives[index] = ScanArchive{
			Root:        root,
			ArchivePath: path,
			SizeBytes:   int64(1000 + serial),
			Modified:    modified,
			Manifest: modkit.Manifest{
				SchemaVersion:      modkit.SchemaVersion,
				AnalyzerVersion:    modkit.AnalyzerVersion,
				AnalyzedAt:         modified,
				ArchivePath:        path,
				Filename:           filepath.Base(path),
				SizeBytes:          int64(1000 + serial),
				ModifiedAt:         modified,
				CentralFingerprint: fmt.Sprintf("fixture-fingerprint-%05d", serial),
				FullSHA256:         fmt.Sprintf("fixture-sha-%05d", serial),
				ValidArchive:       true,
				Kind:               kind,
				Title:              title,
				Description:        fmt.Sprintf("Representative fixture %05d", serial),
				Author:             author,
				Version:            "1.0.0",
				Namespaces:         map[string][]string{"vehicles": {namespace}},
				Members:            []modkit.ArchiveMember{{Path: "vehicles/main.jbeam"}},
			},
		}
	}
	return archives
}

func applyLibraryArchives(tb testing.TB, store *Store, root string, archives []ScanArchive) []LibraryItem {
	tb.Helper()
	ctx := context.Background()
	scanID, err := store.BeginScan(ctx, []string{root})
	if err != nil {
		tb.Fatalf("begin library scan: %v", err)
	}
	items, err := store.ApplyScanBatch(ctx, scanID, []string{root}, archives, len(archives), len(archives), 0)
	if err != nil {
		tb.Fatalf("apply library scan: %v", err)
	}
	return items
}

func libraryItemByPath(items []LibraryItem, path string) (LibraryItem, bool) {
	for _, item := range items {
		if strings.EqualFold(item.ArchivePath, path) {
			return item, true
		}
	}
	return LibraryItem{}, false
}

func libraryEntityIDs(items []LibraryItem) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.EntityID)
	}
	return result
}

func equalLibraryEntitySet(left, right []LibraryItem) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]struct{}, len(left))
	for _, item := range left {
		seen[item.EntityID] = struct{}{}
	}
	for _, item := range right {
		if _, ok := seen[item.EntityID]; !ok {
			return false
		}
	}
	return true
}

func referenceListLibrary(ctx context.Context, store *Store, health, kind, query, folderID string) ([]LibraryItem, error) {
	items, err := store.listItems(ctx, "")
	if err != nil {
		return nil, err
	}
	collectionNames, err := store.libraryCollectionNames(ctx)
	if err != nil {
		return nil, err
	}
	search := parseLibrarySearchQuery(query)
	health = normalizeLibraryStatus(health)
	filtered := make([]LibraryItem, 0, len(items))
	for _, item := range items {
		if health != "" && item.HealthStatus != health {
			continue
		}
		if kind != "" && kind != "all" && string(item.Kind) != kind {
			continue
		}
		if folderID == "unfiled" && item.FolderID != "" || folderID != "" && folderID != "all" && folderID != "unfiled" && item.FolderID != folderID {
			continue
		}
		if !search.matches(item, collectionNames) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered, nil
}

func TestLibraryInlineScopedQuerySemantics(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()
	archives := libraryFixtureArchives(root, 4, 100)
	archives[0].Manifest.Title = "BMW Car"
	archives[1].Manifest.Title = "BMW Racecar"
	archives[2].Manifest.Title = "BMW Cargo"
	archives[3].Manifest.Title = "BMW Drift"
	archives[0].SourceClass = "beamng-repository"
	archives[1].SourceClass = "user-added"
	archives[2].SourceClass = "user-added"
	archives[3].SourceClass = "user-added"
	items := applyLibraryArchives(t, store, root, archives)
	if len(items) != len(archives) {
		t.Fatalf("inline query fixture items = %d, want %d", len(items), len(archives))
	}

	itemByTitle := make(map[string]LibraryItem, len(items))
	for _, item := range items {
		itemByTitle[item.DisplayName] = item
	}
	car := itemByTitle["BMW Car"]
	racecar := itemByTitle["BMW Racecar"]
	cargo := itemByTitle["BMW Cargo"]
	drift := itemByTitle["BMW Drift"]
	if car.EntityID == "" || racecar.EntityID == "" || cargo.EntityID == "" || drift.EntityID == "" {
		t.Fatalf("inline query fixture titles were not hydrated: %#v", itemByTitle)
	}

	tagIDs := make(map[string]string)
	for _, name := range []string{"Car", "Racecar", "Cargo", "Road Test Favorite"} {
		if err := store.CreateModTag(ctx, name, "#7a8791", "tag"); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "already exists") {
			t.Fatal(err)
		}
		var tagID string
		if err := store.db.QueryRowContext(ctx, `SELECT id FROM mod_tags WHERE name=?`, name).Scan(&tagID); err != nil {
			t.Fatal(err)
		}
		tagIDs[name] = tagID
	}
	for item, tagName := range map[string]string{
		car.EntityID:     "Car",
		racecar.EntityID: "Racecar",
		cargo.EntityID:   "Cargo",
		drift.EntityID:   "Road Test Favorite",
	} {
		if err := store.SetLibraryItemTags(ctx, item, []string{tagIDs[tagName]}); err != nil {
			t.Fatal(err)
		}
	}

	assertQueryIDs := func(query string, want ...string) {
		t.Helper()
		got, err := store.ListLibrary(ctx, "all", "all", query, "all")
		if err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		gotSet := make(map[string]struct{}, len(got))
		for _, item := range got {
			gotSet[item.EntityID] = struct{}{}
		}
		wantSet := make(map[string]struct{}, len(want))
		for _, entityID := range want {
			wantSet[entityID] = struct{}{}
		}
		if len(gotSet) != len(wantSet) {
			t.Fatalf("query %q returned %v, want %v", query, libraryEntityIDs(got), want)
		}
		for entityID := range wantSet {
			if _, ok := gotSet[entityID]; !ok {
				t.Fatalf("query %q returned %v, want %v", query, libraryEntityIDs(got), want)
			}
		}
	}

	// Complete inline tag operators are one-shot and combine with ordinary
	// text on either side, while matching the configured tag name exactly.
	assertQueryIDs("tags:car bmw", car.EntityID)
	assertQueryIDs("bmw tag:CAR", car.EntityID)
	assertQueryIDs("tags:Car", car.EntityID)
	assertQueryIDs(`tags:"Road Test Favorite" bmw`, drift.EntityID)
	// The legacy in:tag form remains a sticky substring search.
	assertQueryIDs("in:tag car", car.EntityID, racecar.EntityID, cargo.EntityID)
	// A complete source token must also leave the following ordinary term in
	// the default all-fields scope.
	assertQueryIDs("source:repository bmw", car.EntityID)
}

func TestLibrarySQLFTSSemanticsMatchReference(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()
	archives := libraryFixtureArchives(root, 4, 0)
	items := applyLibraryArchives(t, store, root, archives)
	first, ok := libraryItemByPath(items, archives[0].ArchivePath)
	if !ok {
		t.Fatal("fixture first item was not returned")
	}
	second, ok := libraryItemByPath(items, archives[1].ArchivePath)
	if !ok {
		t.Fatal("fixture second item was not returned")
	}
	if err := store.CreateModTag(ctx, "Road Test Favorite", "#7a8791", "tag"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateModTag(ctx, "Must Play", "#7a8791", "shield"); err != nil {
		t.Fatal(err)
	}
	var gameplayID, mustPlayID string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM mod_tags WHERE name=?`, "Road Test Favorite").Scan(&gameplayID); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM mod_tags WHERE name=?`, "Must Play").Scan(&mustPlayID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLibraryItemTags(ctx, first.EntityID, []string{gameplayID, mustPlayID}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateLibraryFolder(ctx, "Road Tests", ""); err != nil {
		t.Fatal(err)
	}
	var folderID string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM library_folders WHERE name=?`, "Road Tests").Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	if err := store.MoveLibraryItem(ctx, first.EntityID, folderID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE archive_links SET active=0 WHERE path=?`, second.ArchivePath); err != nil {
		t.Fatal(err)
	}

	queries := []struct {
		name                string
		health, kind, query string
		folderID            string
	}{
		{name: "all", health: "all", kind: "all", query: "", folderID: "all"},
		{name: "quoted name", health: "all", kind: "all", query: `in:name "Road Runner"`, folderID: "all"},
		{name: "quoted author", health: "all", kind: "all", query: `in:author "Ava Builder"`, folderID: "all"},
		{name: "quoted tag", health: "all", kind: "all", query: `in:tag "Road Test Favorite"`, folderID: "all"},
		{name: "namespace", health: "all", kind: "all", query: `in:namespace road_namespace`, folderID: "all"},
		{name: "collection", health: "all", kind: "all", query: `in:collection "Road Tests"`, folderID: "all"},
		{name: "source available", health: "all", kind: "all", query: "in:source available", folderID: "all"},
		{name: "source missing", health: "all", kind: "all", query: "in:source missing", folderID: "all"},
		{name: "folder", health: "all", kind: "all", query: "", folderID: folderID},
		{name: "unfiled", health: "all", kind: "all", query: "", folderID: "unfiled"},
		{name: "status", health: "unscanned", kind: "all", query: "is:unscanned", folderID: "all"},
		{name: "no result", health: "all", kind: "all", query: `in:name "does not exist"`, folderID: "all"},
	}
	for _, tc := range queries {
		t.Run(tc.name, func(t *testing.T) {
			expected, err := referenceListLibrary(ctx, store, tc.health, tc.kind, tc.query, tc.folderID)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := store.ListLibrary(ctx, tc.health, tc.kind, tc.query, tc.folderID)
			if err != nil {
				t.Fatal(err)
			}
			got, want := libraryEntityIDs(actual), libraryEntityIDs(expected)
			if len(got) != len(want) {
				t.Fatalf("query %q returned %d items, reference returned %d: got=%v want=%v", tc.query, len(got), len(want), got, want)
			}
			for index := range got {
				if got[index] != want[index] {
					t.Fatalf("query %q ordering differs: got=%v want=%v", tc.query, got, want)
				}
			}
		})
	}
}

func TestListLibraryExceedsLegacyCandidateCap(t *testing.T) {
	store, root := openLibraryStorage(t)
	archives := libraryFixtureArchives(root, 5_005, 0)
	applyLibraryArchives(t, store, root, archives)
	items, err := store.ListLibrary(context.Background(), "all", "all", "", "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != len(archives) {
		t.Fatalf("large library returned %d items, want %d", len(items), len(archives))
	}
	query := `in:name "Library Mod 05004"`
	matches, err := store.ListLibrary(context.Background(), "all", "all", query, "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].DisplayName != "Library Mod 05004" {
		t.Fatalf("large indexed query = %#v, want one exact item", matches)
	}
}

func TestLibraryScanConcurrentReadSeesPreOrPostSnapshot(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()
	beforeArchives := libraryFixtureArchives(root, 64, 0)
	before := applyLibraryArchives(t, store, root, beforeArchives)
	beforeIDs := libraryEntityIDs(before)

	afterArchives := libraryFixtureArchives(root, 64, 10_000)
	readDone := make(chan struct{})
	var during []LibraryItem
	var readErr error
	go func() {
		during, readErr = store.ListLibrary(ctx, "all", "all", "", "all")
		close(readDone)
	}()
	applyLibraryArchives(t, store, root, afterArchives)
	<-readDone
	if readErr != nil {
		t.Fatal(readErr)
	}
	after, err := store.ListLibrary(ctx, "all", "all", "", "all")
	if err != nil {
		t.Fatal(err)
	}
	if equalLibraryEntitySet(during, before) || equalLibraryEntitySet(during, after) {
		return
	}
	t.Fatalf("concurrent read observed a partial snapshot: got=%v before=%v after=%v", libraryEntityIDs(during), beforeIDs, libraryEntityIDs(after))
}

func TestLibraryScanBatchRollbackPreservesPriorSnapshot(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()
	archives := libraryFixtureArchives(root, 2, 0)
	before := applyLibraryArchives(t, store, root, archives)
	trigger := "library_storage_test_abort"
	_, err := store.db.ExecContext(ctx, `CREATE TRIGGER `+trigger+` BEFORE INSERT ON artifacts
		WHEN NEW.central_fingerprint='library-storage-test-rollback'
		BEGIN SELECT RAISE(ABORT, 'library storage test rollback'); END`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = store.db.Exec(`DROP TRIGGER IF EXISTS ` + trigger) })
	bad := libraryFixtureArchives(root, 1, 9000)
	bad[0].Manifest.CentralFingerprint = "library-storage-test-rollback"
	bad[0].Manifest.ArchivePath = filepath.Join(root, "rollback.zip")
	bad[0].ArchivePath = bad[0].Manifest.ArchivePath
	scanID, err := store.BeginScan(ctx, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyScanBatch(ctx, scanID, []string{root}, bad, len(bad), 0, 1); err == nil {
		t.Fatal("rollback fixture unexpectedly committed")
	} else if finishErr := store.FinishScan(ctx, scanID, []string{root}, 1, 0, 1, err); finishErr != nil {
		t.Fatalf("record failed scan: %v", finishErr)
	}
	after, err := store.ListLibrary(ctx, "all", "all", "", "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) || !equalLibraryEntitySet(after, before) {
		t.Fatalf("failed batch changed prior snapshot: before=%v after=%v", libraryEntityIDs(before), libraryEntityIDs(after))
	}
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM scans WHERE id=?`, scanID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("failed batch status = %q, want failed", status)
	}
}

func TestLibraryScanLeavesUnchangedRecordsUntouched(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()
	archives := libraryFixtureArchives(root, 2, 0)
	initial := applyLibraryArchives(t, store, root, archives)
	unchanged, ok := libraryItemByPath(initial, archives[0].ArchivePath)
	if !ok {
		t.Fatal("unchanged fixture item missing")
	}
	var beforeAnalyzed, beforeEntityUpdated, beforeManifest string
	if err := store.db.QueryRowContext(ctx, `SELECT a.analyzed_at,e.updated_at,a.manifest_json
		FROM artifacts a JOIN archive_links l ON l.artifact_id=a.id JOIN entities e ON e.id=l.entity_id
		WHERE l.path=?`, archives[0].ArchivePath).Scan(&beforeAnalyzed, &beforeEntityUpdated, &beforeManifest); err != nil {
		t.Fatal(err)
	}
	changed := archives[1]
	changed.SizeBytes++
	changed.Modified = changed.Modified.Add(time.Hour)
	changed.Manifest.SizeBytes = changed.SizeBytes
	changed.Manifest.ModifiedAt = changed.Modified
	changed.Manifest.CentralFingerprint = "fixture-fingerprint-changed"
	changed.Manifest.FullSHA256 = "fixture-sha-changed"
	batch := []ScanArchive{
		{Root: root, ArchivePath: archives[0].ArchivePath, SizeBytes: archives[0].SizeBytes, Modified: archives[0].Modified, Reused: true},
		changed,
	}
	applyLibraryArchives(t, store, root, batch)
	var afterAnalyzed, afterEntityUpdated, afterManifest string
	if err := store.db.QueryRowContext(ctx, `SELECT a.analyzed_at,e.updated_at,a.manifest_json
		FROM artifacts a JOIN archive_links l ON l.artifact_id=a.id JOIN entities e ON e.id=l.entity_id
		WHERE l.path=?`, archives[0].ArchivePath).Scan(&afterAnalyzed, &afterEntityUpdated, &afterManifest); err != nil {
		t.Fatal(err)
	}
	if afterAnalyzed != beforeAnalyzed || afterEntityUpdated != beforeEntityUpdated || afterManifest != beforeManifest {
		t.Fatalf("unchanged record was rewritten: analyzed %q->%q entity %q->%q manifest changed=%v", beforeAnalyzed, afterAnalyzed, beforeEntityUpdated, afterEntityUpdated, beforeManifest != afterManifest)
	}
	if _, ok := libraryItemByPath(initial, archives[0].ArchivePath); !ok || unchanged.EntityID == "" {
		t.Fatal("unchanged entity identity was not retained")
	}
}

func equalLibrarySnapshot(left, right []LibraryItem) bool {
	if len(left) != len(right) {
		return false
	}
	type snapshotItem struct {
		displayName string
		path        string
		linkID      string
		fingerprint string
		linked      bool
		sourceID    string
	}
	snapshot := func(items []LibraryItem) map[string]snapshotItem {
		result := make(map[string]snapshotItem, len(items))
		for _, item := range items {
			result[item.EntityID] = snapshotItem{
				displayName: item.DisplayName, path: item.ArchivePath, linkID: item.LinkID,
				fingerprint: item.Fingerprint, linked: item.Linked, sourceID: item.SourceID,
			}
		}
		return result
	}
	leftSnapshot, rightSnapshot := snapshot(left), snapshot(right)
	if len(leftSnapshot) != len(rightSnapshot) {
		return false
	}
	for entityID, leftItem := range leftSnapshot {
		if rightItem, ok := rightSnapshot[entityID]; !ok || rightItem != leftItem {
			return false
		}
	}
	return true
}

func waitForLibraryReaderConnection(t *testing.T, store *Store) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if store.db.Stats().InUse > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("ListLibrary did not acquire a SQLite connection")
}

func TestLibrarySourceHydrationSearchAndReusedRetention(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()
	archives := libraryFixtureArchives(root, 2, 30)
	archives[0].SourceClass = "beamng-repository"
	archives[1].SourceClass = "user-added"
	items := applyLibraryArchives(t, store, root, archives)
	if len(items) != 2 {
		t.Fatalf("source fixture items = %d, want 2", len(items))
	}
	repository, ok := libraryItemByPath(items, archives[0].ArchivePath)
	if !ok {
		t.Fatal("repository source fixture was not returned")
	}
	userAdded, ok := libraryItemByPath(items, archives[1].ArchivePath)
	if !ok {
		t.Fatal("user-added source fixture was not returned")
	}
	if repository.SourceID != "beamng-repository" || repository.Source != "BeamNG Repository" {
		t.Fatalf("repository source hydration = id %q label %q", repository.SourceID, repository.Source)
	}
	if userAdded.SourceID != "user-added" || userAdded.Source != "User added" {
		t.Fatalf("user-added source hydration = id %q label %q", userAdded.SourceID, userAdded.Source)
	}
	for _, tc := range []struct {
		name  string
		query string
		want  string
	}{
		{name: "repository id", query: "in:source beamng-repository", want: repository.EntityID},
		{name: "repository alias", query: "in:source repository", want: repository.EntityID},
		{name: "repository label", query: `in:source "BeamNG Repository"`, want: repository.EntityID},
		{name: "user id", query: "in:source user-added", want: userAdded.EntityID},
		{name: "user alias", query: "in:source third-party", want: userAdded.EntityID},
		{name: "user label", query: `in:source "User added"`, want: userAdded.EntityID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.ListLibrary(ctx, "all", "all", tc.query, "all")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].EntityID != tc.want {
				t.Fatalf("source query %q returned %#v, want %q", tc.query, libraryEntityIDs(got), tc.want)
			}
		})
	}

	stale, reused, err := store.LookupArchiveAnalysis(ctx, repository.RootPath, repository.ArchivePath, repository.SizeBytes+1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if reused {
		t.Fatal("stale archive analysis was reported as reused")
	}
	if stale.EntityID != repository.EntityID || stale.SourceID != "beamng-repository" || stale.Source != "BeamNG Repository" {
		t.Fatalf("stale archive lookup lost source/canonical identity: %#v", stale)
	}
	changed := archives[0]
	changed.SourceClass = stale.SourceID
	changed.SizeBytes++
	changed.Modified = changed.Modified.Add(time.Hour)
	changed.Manifest.SizeBytes = changed.SizeBytes
	changed.Manifest.ModifiedAt = changed.Modified
	changed.Manifest.CentralFingerprint = "source-retention-changed-fingerprint"
	changed.Manifest.FullSHA256 = "source-retention-changed-sha"
	changed.Manifest.Title = "Repository Changed Archive"
	changedItems := applyLibraryArchives(t, store, root, []ScanArchive{changed})
	if len(changedItems) != 1 || changedItems[0].EntityID != repository.EntityID {
		t.Fatalf("changed source archive changed entity identity: %#v", changedItems)
	}
	if changedItems[0].SourceID != "beamng-repository" || changedItems[0].Source != "BeamNG Repository" {
		t.Fatalf("changed source archive lost source retention: id %q label %q", changedItems[0].SourceID, changedItems[0].Source)
	}

	reusedArchive := changed
	reusedArchive.Reused = true
	reusedArchive.Manifest = modkit.Manifest{}
	reusedArchive.SourceClass = "user-added"
	reusedItems := applyLibraryArchives(t, store, root, []ScanArchive{reusedArchive})
	if len(reusedItems) != 1 || reusedItems[0].EntityID != repository.EntityID {
		t.Fatalf("reused source archive changed entity identity: %#v", reusedItems)
	}
	if reusedItems[0].SourceID != "beamng-repository" || reusedItems[0].Source != "BeamNG Repository" {
		t.Fatalf("reused source archive lost stored source: id %q label %q", reusedItems[0].SourceID, reusedItems[0].Source)
	}
}

func writeLibraryScanArchive(tb testing.TB, path, title string) {
	tb.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		tb.Fatalf("create scan archive directory: %v", err)
	}
	file, err := os.Create(path)
	if err != nil {
		tb.Fatalf("create scan archive: %v", err)
	}
	writer := zip.NewWriter(file)
	metadata, err := writer.Create("mod_info/info.json")
	if err == nil {
		_, err = metadata.Write([]byte(fmt.Sprintf(`{"title":%q,"author":"Scan Fixture","version":"1.0"}`, title)))
	}
	if err == nil {
		member, memberErr := writer.Create("vehicles/scan_fixture/main.jbeam")
		if memberErr != nil {
			err = memberErr
		} else {
			_, err = member.Write([]byte(`{}`))
		}
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		tb.Fatalf("write scan archive: %v", err)
	}
}

func TestLibraryEngineClassifiesRepositoryAndUserSources(t *testing.T) {
	store, _ := openLibraryStorage(t)
	root := t.TempDir()
	activeModsDir := filepath.Join(root, "active", "mods")
	repositoryRoot := filepath.Join(activeModsDir, "repo")
	userRoot := filepath.Join(root, "external-mods")
	repositoryPath := filepath.Join(repositoryRoot, "repository.zip")
	userPath := filepath.Join(userRoot, "user.zip")
	writeLibraryScanArchive(t, repositoryPath, "Scanned Repository Vehicle")
	writeLibraryScanArchive(t, userPath, "Scanned User Vehicle")
	config := AppConfig{
		ActiveModsDir:   activeModsDir,
		ScanRoots:       []string{repositoryRoot, userRoot},
		ScanConcurrency: 1,
	}
	engine := NewLibraryEngine(store, config, func(string, any) {})
	summary, err := engine.Scan(context.Background())
	if err != nil {
		t.Fatalf("library engine scan: %v", err)
	}
	if summary.Failed != 0 || summary.Analyzed != 2 {
		t.Fatalf("library engine scan summary = %#v", summary)
	}
	items, err := store.ListLibrary(context.Background(), "all", "all", "", "all")
	if err != nil {
		t.Fatal(err)
	}
	repository, ok := libraryItemByPath(items, repositoryPath)
	if !ok {
		t.Fatalf("repository archive missing from scanned library: %#v", libraryEntityIDs(items))
	}
	userAdded, ok := libraryItemByPath(items, userPath)
	if !ok {
		t.Fatalf("user archive missing from scanned library: %#v", libraryEntityIDs(items))
	}
	if repository.SourceID != "beamng-repository" || repository.Source != "BeamNG Repository" {
		t.Fatalf("scanned repository source = %q/%q", repository.SourceID, repository.Source)
	}
	if userAdded.SourceID != "user-added" || userAdded.Source != "User added" {
		t.Fatalf("scanned user source = %q/%q", userAdded.SourceID, userAdded.Source)
	}
}

func TestLibraryListUsesOneSnapshotAcrossCoordinatedScanCommit(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()
	beforeArchives := libraryFixtureArchives(root, 5_005, 30_000)
	before := applyLibraryArchives(t, store, root, beforeArchives)
	if len(before) != len(beforeArchives) {
		t.Fatalf("before snapshot items = %d, want %d", len(before), len(beforeArchives))
	}
	changed := beforeArchives[0]
	changed.SizeBytes++
	changed.Modified = changed.Modified.Add(time.Hour)
	changed.Manifest.SizeBytes = changed.SizeBytes
	changed.Manifest.ModifiedAt = changed.Modified
	changed.Manifest.CentralFingerprint = "coordinated-snapshot-changed-fingerprint"
	changed.Manifest.FullSHA256 = "coordinated-snapshot-changed-sha"
	changed.Manifest.Title = "Coordinated Snapshot Changed"
	newArchive := libraryFixtureArchives(root, 1, 40_000)[0]
	afterArchives := []ScanArchive{changed, newArchive}
	readerStarted := make(chan struct{})
	readDone := make(chan struct{})
	var during []LibraryItem
	var readErr error
	go func() {
		close(readerStarted)
		during, readErr = store.ListLibrary(ctx, "all", "all", "", "all")
		close(readDone)
	}()
	<-readerStarted
	waitForLibraryReaderConnection(t, store)
	// The writer commits a changed existing row and a new row without root
	// reconciliation. If ListLibrary releases its candidate snapshot before
	// hydration, it can return the changed old ID while omitting the new one.
	scanID, err := store.BeginScan(ctx, []string{})
	if err != nil {
		t.Fatal(err)
	}
	commitDone := make(chan error, 1)
	go func() {
		_, err := store.ApplyScanBatch(ctx, scanID, []string{}, afterArchives, 2, 2, 0)
		commitDone <- err
	}()
	if err := <-commitDone; err != nil {
		t.Fatal("coordinated scan commit: ", err)
	}
	<-readDone
	if readErr != nil {
		t.Fatal(readErr)
	}
	after, err := store.ListLibrary(ctx, "all", "all", "", "all")
	if err != nil {
		t.Fatal(err)
	}
	if !equalLibrarySnapshot(during, before) && !equalLibrarySnapshot(during, after) {
		t.Fatalf("ListLibrary returned a mixed scan snapshot: during=%d before=%d after=%d", len(during), len(before), len(after))
	}
}

// Legacy JSON query baselines create one representative catalog before the
// timed children. Every iteration still performs the loadCatalog-equivalent
// file read and JSON parse before applying the old in-memory operation. The
// repeated read is normally OS-cache warm; catalog creation and serialization
// are intentionally excluded from these query measurements.
func legacyBenchmarkOptionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func legacyBenchmarkDocument(items []LibraryItem) (legacyCatalogDocument, error) {
	const createdAt = "2026-01-01T00:00:00Z"
	document := legacyCatalogDocument{
		SchemaVersion: legacyCatalogSchemaVersion,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
		LastScan:      json.RawMessage(fmt.Sprintf(`{"startedAt":%q,"finishedAt":%q,"discovered":%d,"missing":0}`, createdAt, createdAt, len(items))),
		Mods:          make([]legacyCatalogMod, len(items)),
	}
	for index, item := range items {
		manifestJSON, err := json.Marshal(item.Manifest)
		if err != nil {
			return legacyCatalogDocument{}, fmt.Errorf("marshal legacy fixture manifest %q: %w", item.EntityID, err)
		}
		namespaces := make(map[string][]string, len(item.Manifest.Namespaces))
		for kind, values := range item.Manifest.Namespaces {
			namespaces[kind] = append([]string{}, values...)
		}
		location := "library"
		if !item.Linked {
			location = "missing"
		}
		health := "healthy"
		if len(item.Manifest.Issues) > 0 {
			health = "invalid"
		}
		document.Mods[index] = legacyCatalogMod{
			ID:                 item.EntityID,
			Path:               item.ArchivePath,
			Filename:           filepath.Base(item.ArchivePath),
			OriginalPath:       item.ArchivePath,
			Location:           location,
			Enabled:            false,
			Missing:            !item.Linked,
			Source:             "third-party",
			ActiveRelativePath: filepath.Join("_managed", filepath.Base(item.ArchivePath)),
			Size:               item.SizeBytes,
			ModifiedAt:         item.ModifiedAt,
			Fingerprint:        item.Fingerprint,
			SHA256:             item.SHA256,
			FullSHA256:         item.Manifest.FullSHA256,
			ValidArchive:       item.Manifest.ValidArchive,
			EntryCount:         item.Manifest.EntryCount,
			ContentTags:        append([]string{}, item.Manifest.ContentTags...),
			Namespaces:         namespaces,
			Title:              item.DisplayName,
			ArchiveDescription: legacyBenchmarkOptionalString(item.Manifest.Description),
			Description:        "",
			Author:             legacyBenchmarkOptionalString(item.Manifest.Author),
			Version:            legacyBenchmarkOptionalString(item.Manifest.Version),
			MetadataDocuments:  append([]modkit.MetadataDocument{}, item.Manifest.MetadataDocuments...),
			Database:           json.RawMessage("null"),
			Issues:             append([]modkit.Issue{}, item.Manifest.Issues...),
			Tags:               []json.RawMessage{},
			Notes:              "",
			Problematic:        false,
			RuntimeIssues:      []modkit.Issue{},
			Health:             health,
			Manifest:           json.RawMessage(manifestJSON),
			AutoCategory:       string(item.Kind),
			Kind:               string(item.Kind),
		}
	}
	return document, nil
}

func legacyBenchmarkPayload(document legacyCatalogDocument) ([]byte, error) {
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(payload, '\n'), nil
}

func legacyBenchmarkLoad(path string) (legacyCatalogDocument, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return legacyCatalogDocument{}, err
	}
	var document legacyCatalogDocument
	if err := json.Unmarshal(payload, &document); err != nil {
		return legacyCatalogDocument{}, err
	}
	if document.SchemaVersion != legacyCatalogSchemaVersion || document.Mods == nil {
		return legacyCatalogDocument{}, fmt.Errorf("unsupported legacy catalog fixture schema")
	}
	return document, nil
}

func legacyBenchmarkHealthMatches(mod legacyCatalogMod, expected string) bool {
	health := strings.ToLower(strings.TrimSpace(mod.Health))
	switch expected {
	case "safe":
		return health == "healthy" || health == "safe" || health == "clean"
	case "review":
		return health == "review" || health == "needs-review" || health == "needs_review"
	case "threat":
		return health == "threat" || health == "unsafe" || health == "infected"
	case "broken":
		return health == "broken" || health == "invalid" || health == "problematic"
	case "unscanned":
		return health == "" || health == "unscanned"
	default:
		return health == expected
	}
}

func legacyBenchmarkTagNames(tags []json.RawMessage) []string {
	names := make([]string, 0, len(tags))
	for _, raw := range tags {
		var name string
		if err := json.Unmarshal(raw, &name); err == nil {
			names = append(names, name)
			continue
		}
		var tag struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &tag); err == nil {
			names = append(names, tag.Name)
		}
	}
	return names
}

func legacyBenchmarkMatches(mod legacyCatalogMod, search librarySearchQuery, folderID string) bool {
	if folderID != "" && folderID != "all" && folderID != "unfiled" {
		return false
	}
	if search.status != "" && !legacyBenchmarkHealthMatches(mod, search.status) {
		return false
	}
	kind := mod.Kind
	if kind == "" {
		kind = mod.AutoCategory
	}
	author := ""
	if mod.Author != nil {
		author = *mod.Author
	}
	for _, term := range search.terms {
		contains := func(value string) bool {
			return strings.Contains(strings.ToLower(value), term.value)
		}
		switch term.scope {
		case "tag":
			found := false
			for _, name := range legacyBenchmarkTagNames(mod.Tags) {
				if contains(name) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		case "kind":
			if !contains(kind) && !contains(libraryKindSearchName(kind)) {
				return false
			}
		case "author":
			if !contains(author) {
				return false
			}
		case "name":
			if !contains(mod.Title) {
				return false
			}
		case "path":
			if !contains(mod.Path) {
				return false
			}
		case "namespace":
			found := false
			for _, values := range mod.Namespaces {
				for _, value := range values {
					if contains(value) {
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			if !found {
				return false
			}
		case "collection":
			if term.value != "unfiled" {
				return false
			}
		case "source":
			switch term.value {
			case "available", "linked", "present":
				if mod.Missing {
					return false
				}
			case "missing", "unlinked", "unavailable":
				if !mod.Missing {
					return false
				}
			default:
				if !contains(mod.Path) {
					return false
				}
			}
		default:
			if !contains(mod.Title) && !contains(mod.Path) && !contains(author) &&
				!contains(kind) && !contains(libraryKindSearchName(kind)) {
				found := false
				for _, values := range mod.Namespaces {
					for _, value := range values {
						if contains(value) {
							found = true
							break
						}
					}
					if found {
						break
					}
				}
				if !found {
					for _, name := range legacyBenchmarkTagNames(mod.Tags) {
						if contains(name) {
							found = true
							break
						}
					}
				}
				if !found {
					return false
				}
			}
		}
	}
	return true
}

func legacyBenchmarkFilter(mods []legacyCatalogMod, health, kind, query, folderID string) []legacyCatalogMod {
	search := parseLibrarySearchQuery(query)
	health = normalizeLibraryStatus(health)
	filtered := make([]legacyCatalogMod, 0, len(mods))
	for _, mod := range mods {
		if health != "" && !legacyBenchmarkHealthMatches(mod, health) {
			continue
		}
		if kind != "" && kind != "all" && kind != mod.Kind && kind != mod.AutoCategory {
			continue
		}
		if !legacyBenchmarkMatches(mod, search, folderID) {
			continue
		}
		filtered = append(filtered, mod)
	}
	return filtered
}

func legacyBenchmarkLess(left, right legacyCatalogMod) bool {
	leftLinked, rightLinked := !left.Missing, !right.Missing
	if leftLinked != rightLinked {
		return leftLinked
	}
	if left.ModifiedAt != right.ModifiedAt {
		return left.ModifiedAt > right.ModifiedAt
	}
	leftName, rightName := strings.ToLower(left.Title), strings.ToLower(right.Title)
	if leftName != rightName {
		return leftName < rightName
	}
	return left.ID < right.ID
}

// BenchmarkLegacyJSONLibraryQueries measures repeated reads/parses of the
// active legacy catalog plus the corresponding in-memory query operation.
func BenchmarkLegacyJSONLibraryQueries(b *testing.B) {
	for _, fixture := range []struct {
		name               string
		size               int
		query              string
		expectedSearchName string
		expectedFilter     int
	}{
		{name: "fixture-32", size: 32, query: `in:name "Library Mod 00031"`, expectedSearchName: "Library Mod 00031", expectedFilter: 8},
		{name: "fixture-5005", size: 5_005, query: `in:name "Library Mod 05004"`, expectedSearchName: "Library Mod 05004", expectedFilter: 1_252},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			store, root := openLibraryStorage(b)
			hydrated := applyLibraryArchives(b, store, root, libraryFixtureArchives(root, fixture.size, 0))
			if len(hydrated) != fixture.size {
				b.Fatalf("hydrated fixture contains %d items, want %d", len(hydrated), fixture.size)
			}
			document, err := legacyBenchmarkDocument(hydrated)
			if err != nil {
				b.Fatal(err)
			}
			// Keep the input unsorted so the timed sort does real work while
			// preserving the same records and ordering comparator as SQLite.
			for left, right := 0, len(document.Mods)-1; left < right; left, right = left+1, right-1 {
				document.Mods[left], document.Mods[right] = document.Mods[right], document.Mods[left]
			}
			payload, err := legacyBenchmarkPayload(document)
			if err != nil {
				b.Fatal(err)
			}
			catalogPath := filepath.Join(root, "catalog.json")
			if err := os.MkdirAll(filepath.Dir(catalogPath), 0o755); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(catalogPath, payload, 0o600); err != nil {
				b.Fatal(err)
			}

			b.Run("warm-legacy-json/read+unmarshal+sort", func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(fixture.size), "fixture-items")
				b.SetBytes(int64(fixture.size))
				var ordered []legacyCatalogMod
				b.ResetTimer()
				for range b.N {
					document, err := legacyBenchmarkLoad(catalogPath)
					if err != nil {
						b.StopTimer()
						b.Fatal(err)
					}
					sort.SliceStable(document.Mods, func(left, right int) bool {
						return legacyBenchmarkLess(document.Mods[left], document.Mods[right])
					})
					ordered = document.Mods
					if len(ordered) != fixture.size {
						b.StopTimer()
						b.Fatalf("legacy JSON sort lost fixture records: got %d want %d", len(ordered), fixture.size)
					}
				}
				b.StopTimer()
				if !sort.SliceIsSorted(ordered, func(left, right int) bool {
					return legacyBenchmarkLess(ordered[left], ordered[right])
				}) {
					b.Fatal("legacy JSON sort did not produce ordered records")
				}
			})
			b.Run("warm-legacy-json/read+unmarshal+filter", func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(fixture.size), "fixture-items")
				b.SetBytes(int64(fixture.size))
				observed := 0
				b.ResetTimer()
				for range b.N {
					document, err := legacyBenchmarkLoad(catalogPath)
					if err != nil {
						b.StopTimer()
						b.Fatal(err)
					}
					items := legacyBenchmarkFilter(document.Mods, "all", "vehicle", "", "all")
					observed += len(items)
				}
				b.StopTimer()
				if observed != b.N*fixture.expectedFilter {
					b.Fatalf("legacy JSON filter matched %d items over %d iterations, want %d", observed, b.N, b.N*fixture.expectedFilter)
				}
			})
			b.Run("warm-legacy-json/read+unmarshal+text-search", func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(fixture.size), "fixture-items")
				b.SetBytes(int64(fixture.size))
				observed := 0
				b.ResetTimer()
				for range b.N {
					document, err := legacyBenchmarkLoad(catalogPath)
					if err != nil {
						b.StopTimer()
						b.Fatal(err)
					}
					items := legacyBenchmarkFilter(document.Mods, "all", "all", fixture.query, "all")
					observed += len(items)
					if len(items) != 1 || items[0].Title != fixture.expectedSearchName {
						b.StopTimer()
						b.Fatalf("legacy JSON text search returned %#v, want %q", items, fixture.expectedSearchName)
					}
				}
				b.StopTimer()
				if observed != b.N {
					b.Fatalf("legacy JSON text search matched %d items over %d iterations, want %d", observed, b.N, b.N)
				}
			})
		})
	}
}

func legacyBenchmarkCopyFile(sourcePath, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	destination, err := os.Create(destinationPath)
	if err != nil {
		_ = source.Close()
		return err
	}
	_, copyErr := io.Copy(destination, source)
	destinationErr := destination.Close()
	sourceErr := source.Close()
	if copyErr != nil {
		return copyErr
	}
	if destinationErr != nil {
		return destinationErr
	}
	return sourceErr
}

func legacyBenchmarkSave(path string, document legacyCatalogDocument, iteration int) error {
	document.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	payload, err := legacyBenchmarkPayload(document)
	if err != nil {
		return err
	}
	temporary := fmt.Sprintf("%s.%d.tmp", path, iteration)
	handle, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	written, err := handle.Write(payload)
	if err == nil && written != len(payload) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = handle.Sync()
	}
	closeErr := handle.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := legacyBenchmarkCopyFile(path, path+".bak"); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

// BenchmarkLegacyJSONFullRewrite measures the JSON.stringify-equivalent
// serialization, backup copy, durable temporary-file write, and rename from
// catalog.saveCatalog. Archive discovery/inspection and initial catalog
// construction are setup and intentionally excluded; this is a rewrite
// baseline, not a total legacy scan.
func BenchmarkLegacyJSONFullRewrite(b *testing.B) {
	for _, fixtureSize := range []int{32, 5_005} {
		b.Run(fmt.Sprintf("warm-legacy-json/full-rewrite/fixture-%d", fixtureSize), func(b *testing.B) {
			store, root := openLibraryStorage(b)
			hydrated := applyLibraryArchives(b, store, root, libraryFixtureArchives(root, fixtureSize, 0))
			if len(hydrated) != fixtureSize {
				b.Fatalf("hydrated fixture contains %d items, want %d", len(hydrated), fixtureSize)
			}
			document, err := legacyBenchmarkDocument(hydrated)
			if err != nil {
				b.Fatal(err)
			}
			payload, err := legacyBenchmarkPayload(document)
			if err != nil {
				b.Fatal(err)
			}
			catalogPath := filepath.Join(root, "catalog.json")
			if err := os.MkdirAll(filepath.Dir(catalogPath), 0o755); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(catalogPath, payload, 0o600); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ReportMetric(float64(fixtureSize), "fixture-items")
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for iteration := range b.N {
				if err := legacyBenchmarkSave(catalogPath, document, iteration); err != nil {
					b.StopTimer()
					b.Fatal(err)
				}
			}
			b.StopTimer()
			final, err := legacyBenchmarkLoad(catalogPath)
			if err != nil {
				b.Fatal(err)
			}
			if len(final.Mods) != fixtureSize {
				b.Fatalf("legacy JSON rewrite produced %d mods, want %d", len(final.Mods), fixtureSize)
			}
		})
	}
}

func BenchmarkSQLiteLibraryQueries(b *testing.B) {
	for _, fixture := range []struct {
		name               string
		size               int
		query              string
		expectedSearchName string
		expectedFilter     int
	}{
		{name: "fixture-32", size: 32, query: `in:name "Library Mod 00031"`, expectedSearchName: "Library Mod 00031", expectedFilter: 8},
		{name: "fixture-5005", size: 5_005, query: `in:name "Library Mod 05004"`, expectedSearchName: "Library Mod 05004", expectedFilter: 1_252},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			// Opening, migrating, populating, and first hydration are cold
			// setup and intentionally happen before each warm query child.
			store, root := openLibraryStorage(b)
			hydrated := applyLibraryArchives(b, store, root, libraryFixtureArchives(root, fixture.size, 0))
			if len(hydrated) != fixture.size {
				b.Fatalf("hydrated fixture contains %d items, want %d", len(hydrated), fixture.size)
			}
			ctx := context.Background()

			b.Run("warm-sqlite/sort", func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(fixture.size), "fixture-items")
				b.SetBytes(int64(fixture.size))
				observed := 0
				b.ResetTimer()
				for range b.N {
					items, err := store.ListLibrary(ctx, "all", "all", "", "all")
					if err != nil {
						b.Fatal(err)
					}
					observed += len(items)
					if len(items) > 0 && items[0].EntityID == "" {
						b.Fatal("SQLite sort returned an item without an entity ID")
					}
				}
				b.StopTimer()
				if observed != b.N*fixture.size {
					b.Fatalf("SQLite sort returned %d items over %d iterations, want %d", observed, b.N, b.N*fixture.size)
				}
			})
			b.Run("warm-sqlite/filter", func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(fixture.size), "fixture-items")
				b.SetBytes(int64(fixture.size))
				observed := 0
				b.ResetTimer()
				for range b.N {
					items, err := store.ListLibrary(ctx, "all", "vehicle", "", "all")
					if err != nil {
						b.Fatal(err)
					}
					observed += len(items)
				}
				b.StopTimer()
				if observed != b.N*fixture.expectedFilter {
					b.Fatalf("SQLite filter matched %d items over %d iterations, want %d", observed, b.N, b.N*fixture.expectedFilter)
				}
			})
			b.Run("warm-sqlite/text-search", func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(fixture.size), "fixture-items")
				b.SetBytes(int64(fixture.size))
				observed := 0
				b.ResetTimer()
				for range b.N {
					items, err := store.ListLibrary(ctx, "all", "all", fixture.query, "all")
					if err != nil {
						b.Fatal(err)
					}
					observed += len(items)
					if len(items) == 1 && items[0].DisplayName != fixture.expectedSearchName {
						b.Fatalf("SQLite text search returned %q, want %q", items[0].DisplayName, fixture.expectedSearchName)
					}
				}
				b.StopTimer()
				if observed != b.N {
					b.Fatalf("SQLite text search matched %d items over %d iterations, want %d", observed, b.N, b.N)
				}
			})
		})
	}
}

func BenchmarkSQLiteIncrementalLibraryScan(b *testing.B) {
	const fixtureSize = 5_005
	store, root := openLibraryStorage(b)
	archives := libraryFixtureArchives(root, fixtureSize, 0)
	applyLibraryArchives(b, store, root, archives)
	for index := range archives {
		archives[index].Reused = true
		archives[index].Manifest = modkit.Manifest{}
	}
	b.Run("warm-sqlite/unchanged-reused/fixture-5005", func(b *testing.B) {
		b.ReportAllocs()
		b.ReportMetric(float64(fixtureSize), "fixture-items")
		b.SetBytes(int64(fixtureSize))
		b.ResetTimer()
		for range b.N {
			scanID, err := store.BeginScan(context.Background(), []string{root})
			if err != nil {
				b.Fatal(err)
			}
			items, err := store.ApplyScanBatch(context.Background(), scanID, []string{root}, archives, fixtureSize, fixtureSize, 0)
			if err != nil {
				b.Fatal(err)
			}
			if len(items) != fixtureSize {
				b.Fatalf("unchanged SQLite scan returned %d items, want %d", len(items), fixtureSize)
			}
		}
		b.StopTimer()
		if elapsed := b.Elapsed().Seconds(); elapsed > 0 {
			b.ReportMetric(float64(b.N*fixtureSize)/elapsed, "archives/s")
		}
	})
}

// BenchmarkSQLiteFullLibraryScan measures the initialized SQLite persistence
// boundary for one complete scan result. It intentionally excludes filesystem
// discovery and archive inspection, so it is a warm persistence benchmark
// comparable with BenchmarkLegacyJSONFullRewrite rather than a total scan.
func BenchmarkSQLiteFullLibraryScan(b *testing.B) {
	for _, fixtureSize := range []int{32, 5_005} {
		b.Run(fmt.Sprintf("warm-sqlite/full-persistence/fixture-%d", fixtureSize), func(b *testing.B) {
			store, root := openLibraryStorage(b)
			archives := libraryFixtureArchives(root, fixtureSize, 0)
			hydrated := applyLibraryArchives(b, store, root, archives)
			if len(hydrated) != fixtureSize {
				b.Fatalf("hydrated fixture contains %d items, want %d", len(hydrated), fixtureSize)
			}
			document, err := legacyBenchmarkDocument(hydrated)
			if err != nil {
				b.Fatal(err)
			}
			payload, err := legacyBenchmarkPayload(document)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ReportMetric(float64(fixtureSize), "fixture-items")
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			observed := 0
			for range b.N {
				ctx := context.Background()
				scanID, err := store.BeginScan(ctx, []string{root})
				if err != nil {
					b.StopTimer()
					b.Fatal(err)
				}
				items, err := store.ApplyScanBatch(ctx, scanID, []string{root}, archives, fixtureSize, fixtureSize, 0)
				if err != nil {
					b.StopTimer()
					b.Fatal(err)
				}
				observed += len(items)
			}
			b.StopTimer()
			if observed != b.N*fixtureSize {
				b.Fatalf("SQLite persistence scan returned %d items over %d iterations, want %d", observed, b.N, b.N*fixtureSize)
			}
		})
	}
}
