package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

// ---------------------------------------------------------------------------
// Migration & backfill
// ---------------------------------------------------------------------------

func TestArtifactSummaryBackfillPopulatesExistingArtifacts(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()

	archives := libraryFixtureArchives(root, 5, 0)
	items := applyLibraryArchives(t, store, root, archives)
	if len(items) != 5 {
		t.Fatalf("expected 5 items, got %d", len(items))
	}
	// Simulate an existing catalog before the additive projection was populated.
	if _, err := store.db.ExecContext(ctx, `DELETE FROM artifact_summaries`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP INDEX archive_links_entity_idx;
		CREATE INDEX archive_links_entity_idx ON archive_links(entity_id,active,last_seen_at)`); err != nil {
		t.Fatal(err)
	}
	filename := store.dbPath
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	store = reopened

	// Verify every artifact has a summary row with correct counts.
	for _, item := range items {
		var rev, title, author string
		var memberCount, nsCount, variantCount, issueCount int
		if err := store.db.QueryRowContext(ctx,
			`SELECT revision, title, author, member_count, namespace_count, variant_count, issue_count
			 FROM artifact_summaries WHERE artifact_id=?`, item.ArtifactID).
			Scan(&rev, &title, &author, &memberCount, &nsCount, &variantCount, &issueCount); err != nil {
			t.Fatalf("missing summary for artifact %s: %v", item.ArtifactID, err)
		}
		if rev == "" {
			t.Error("summary revision is empty")
		}
		if memberCount != item.MemberCount {
			t.Errorf("member_count mismatch: summary=%d item=%d", memberCount, item.MemberCount)
		}
		if nsCount != item.NamespaceCount {
			t.Errorf("namespace_count mismatch: summary=%d item=%d", nsCount, item.NamespaceCount)
		}
		if title != item.Manifest.Title {
			t.Errorf("title mismatch: summary=%q item=%q", title, item.Manifest.Title)
		}
		if author != item.Manifest.Author {
			t.Errorf("author mismatch: summary=%q item=%q", author, item.Manifest.Author)
		}
	}
}

// ---------------------------------------------------------------------------
// Write-through freshness on manifest change
// ---------------------------------------------------------------------------

func TestArtifactSummaryWriteThroughOnManifestChange(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()

	archives := libraryFixtureArchives(root, 1, 0)
	items := applyLibraryArchives(t, store, root, archives)
	if len(items) != 1 {
		t.Fatal("expected 1 item")
	}
	artifactID := items[0].ArtifactID

	var revBefore string
	if err := store.db.QueryRowContext(ctx,
		`SELECT revision FROM artifact_summaries WHERE artifact_id=?`, artifactID).
		Scan(&revBefore); err != nil {
		t.Fatal(err)
	}

	// Re-scan with an updated manifest that adds a second member and changes the author.
	archives[0].Modified = time.Now().UTC()
	archives[0].Manifest.Author = "Changed Author"
	archives[0].Manifest.Members = append(archives[0].Manifest.Members,
		modkit.ArchiveMember{Path: "vehicles/extra.jbeam"})
	archives[0].Manifest.Issues = []modkit.Issue{
		{Code: "test-issue", Severity: modkit.SeverityWarning, Message: "example"},
	}
	applyLibraryArchives(t, store, root, archives)

	var revAfter, author string
	var memberCount, issueCount int
	if err := store.db.QueryRowContext(ctx,
		`SELECT revision, author, member_count, issue_count
		 FROM artifact_summaries WHERE artifact_id=?`, artifactID).
		Scan(&revAfter, &author, &memberCount, &issueCount); err != nil {
		t.Fatal(err)
	}
	if revAfter == revBefore {
		t.Error("revision should change when manifest changes")
	}
	if author != "Changed Author" {
		t.Errorf("author not updated: got %q", author)
	}
	if memberCount != 2 {
		t.Errorf("member_count not updated: got %d", memberCount)
	}
	if issueCount != 1 {
		t.Errorf("issue_count not updated: got %d", issueCount)
	}
}

// ---------------------------------------------------------------------------
// Rollback invalidation — summary revision must be fresh per randomblob
// ---------------------------------------------------------------------------

func TestArtifactSummaryRollbackDoesNotPoisonCachedDetails(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()

	archives := libraryFixtureArchives(root, 1, 0)
	initial := applyLibraryArchives(t, store, root, archives)

	var revA string
	if err := store.db.QueryRowContext(ctx,
		`SELECT s.revision FROM artifact_summaries s
		 JOIN archive_links l ON l.artifact_id=s.artifact_id
		 WHERE l.path=?`, archives[0].ArchivePath).Scan(&revA); err != nil {
		t.Fatal(err)
	}

	// Start a scan that we will roll back.
	scanID, err := store.BeginScan(ctx, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	archives[0].Modified = time.Now().UTC()
	archives[0].Manifest.Author = "Rolled-back Author"
	store.writeMu.Lock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		store.writeMu.Unlock()
		t.Fatal(err)
	}
	if _, err := store.applyScanArchiveTx(ctx, tx, scanID, archives[0]); err != nil {
		_ = tx.Rollback()
		store.writeMu.Unlock()
		t.Fatal(err)
	}
	uncommitted, readErr := store.listItemsByIDsTx(ctx, tx, []string{initial[0].EntityID})
	if readErr != nil || len(uncommitted) != 1 || uncommitted[0].Manifest.Author != "Rolled-back Author" {
		_ = tx.Rollback()
		store.writeMu.Unlock()
		t.Fatalf("transaction did not see its own metadata change: %#v, %v", uncommitted, readErr)
	}
	// Intentional rollback.
	_ = tx.Rollback()
	store.writeMu.Unlock()

	var revB string
	if err := store.db.QueryRowContext(ctx,
		`SELECT s.revision FROM artifact_summaries s
		 JOIN archive_links l ON l.artifact_id=s.artifact_id
		 WHERE l.path=?`, archives[0].ArchivePath).Scan(&revB); err != nil {
		t.Fatal(err)
	}
	if revA != revB {
		t.Error("rollback should not change the committed revision")
	}
	// Commit a different value without first evicting the cached uncommitted
	// parse. A rollback-reused revision token would serve the wrong author.
	archives[0].Manifest.Author = "Committed Author"
	applyLibraryArchives(t, store, root, archives)
	committed, err := store.GetEntityDetail(ctx, initial[0].EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Item.Manifest.Author != "Committed Author" {
		t.Fatalf("rolled-back cache entry poisoned current detail: %q", committed.Item.Manifest.Author)
	}
}

// ---------------------------------------------------------------------------
// Cache mutation isolation
// ---------------------------------------------------------------------------

func TestManifestCacheMutationIsolation(t *testing.T) {
	cache := newArtifactManifestCache(16)
	original := modkit.Manifest{
		Title:             "Test",
		Author:            "Author",
		Namespaces:        map[string][]string{"vehicles": {"car_a", "car_b"}},
		Issues:            []modkit.Issue{{Code: "a", Severity: modkit.SeverityInfo, Message: "m"}},
		Members:           []modkit.ArchiveMember{{Path: "a.jbeam"}},
		Variants:          []modkit.Variant{{Namespace: "vehicles", BaseName: "car", Fields: map[string]any{"nested": map[string]any{"value": "kept"}}}},
		MetadataDocuments: []modkit.MetadataDocument{{Data: map[string]any{"list": []any{map[string]any{"value": "kept"}}}}},
	}
	cache.put("art1", "rev1", original, 1024)
	original.MetadataDocuments[0].Data["list"].([]any)[0].(map[string]any)["value"] = "changed input"

	// Get a copy and mutate it.
	copy1, ok := cache.get("art1", "rev1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	copy1.Namespaces["vehicles"] = append(copy1.Namespaces["vehicles"], "car_c")
	copy1.Issues = append(copy1.Issues, modkit.Issue{Code: "b"})
	copy1.Members = append(copy1.Members, modkit.ArchiveMember{Path: "b.jbeam"})
	copy1.Variants = append(copy1.Variants, modkit.Variant{Namespace: "x"})
	copy1.Author = "Mutated"
	copy1.MetadataDocuments[0].Data["list"].([]any)[0].(map[string]any)["value"] = "changed returned document"
	copy1.Variants[0].Fields["nested"].(map[string]any)["value"] = "changed returned variant"

	// Get another copy — it must not see the mutations.
	copy2, ok := cache.get("art1", "rev1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if len(copy2.Namespaces["vehicles"]) != 2 {
		t.Errorf("namespace mutation leaked: got %d entries", len(copy2.Namespaces["vehicles"]))
	}
	if len(copy2.Issues) != 1 {
		t.Errorf("issues mutation leaked: got %d", len(copy2.Issues))
	}
	if len(copy2.Members) != 1 {
		t.Errorf("members mutation leaked: got %d", len(copy2.Members))
	}
	if len(copy2.Variants) != 1 {
		t.Errorf("variants mutation leaked: got %d", len(copy2.Variants))
	}
	if copy2.Author != "Author" {
		t.Errorf("scalar mutation leaked: got %q", copy2.Author)
	}
	if copy2.MetadataDocuments[0].Data["list"].([]any)[0].(map[string]any)["value"] != "kept" ||
		copy2.Variants[0].Fields["nested"].(map[string]any)["value"] != "kept" {
		t.Fatal("nested metadata mutation escaped into the cached manifest")
	}

	// Stale revision should miss.
	_, ok = cache.get("art1", "rev-old")
	if ok {
		t.Error("stale revision should miss")
	}
}

func TestManifestCacheEvictsOldEntries(t *testing.T) {
	cache := newArtifactManifestCache(3)
	for i := range 5 {
		cache.put(fmt.Sprintf("art%d", i), "r", modkit.Manifest{Title: fmt.Sprintf("m%d", i)}, 1024)
	}
	// Oldest 2 should be evicted.
	_, ok0 := cache.get("art0", "r")
	_, ok1 := cache.get("art1", "r")
	_, ok4 := cache.get("art4", "r")
	if ok0 || ok1 {
		t.Error("evicted entries should miss")
	}
	if !ok4 {
		t.Error("recent entry should hit")
	}
	cache = newArtifactManifestCache(100)
	cache.put("large-a", "r", modkit.Manifest{Title: "A"}, manifestCacheSourceBudget/2+1)
	cache.put("large-b", "r", modkit.Manifest{Title: "B"}, manifestCacheSourceBudget/2+1)
	if _, ok := cache.get("large-a", "r"); ok {
		t.Fatal("source-data budget did not evict the older entry")
	}
	if _, ok := cache.get("large-b", "r"); !ok {
		t.Fatal("source-data budget discarded the new entry")
	}
}

// ---------------------------------------------------------------------------
// Summary listing returns correct data and lightweight manifest
// ---------------------------------------------------------------------------

func TestSummaryListingMatchesFullDetailFields(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()

	archives := libraryFixtureArchives(root, 3, 0)
	items := applyLibraryArchives(t, store, root, archives)
	entityIDs := libraryEntityIDs(items)

	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	summaries, err := store.listLibrarySummaryItemsByIDsTx(ctx, tx, entityIDs)
	if err != nil {
		t.Fatalf("summary listing: %v", err)
	}
	_ = tx.Commit()

	if len(summaries) != 3 {
		t.Fatalf("expected 3 summaries, got %d", len(summaries))
	}
	summaryMap := make(map[string]LibraryItem, len(summaries))
	for _, s := range summaries {
		summaryMap[s.EntityID] = s
	}

	for _, full := range items {
		sum, ok := summaryMap[full.EntityID]
		if !ok {
			t.Errorf("summary missing for entity %s", full.EntityID)
			continue
		}
		// Scalar fields must match.
		if sum.DisplayName != full.DisplayName {
			t.Errorf("DisplayName mismatch: %q vs %q", sum.DisplayName, full.DisplayName)
		}
		if sum.Kind != full.Kind {
			t.Errorf("Kind mismatch: %q vs %q", sum.Kind, full.Kind)
		}
		if sum.SourceID != full.SourceID {
			t.Errorf("SourceID mismatch: %q vs %q", sum.SourceID, full.SourceID)
		}
		if sum.SizeBytes != full.SizeBytes {
			t.Errorf("SizeBytes mismatch: %d vs %d", sum.SizeBytes, full.SizeBytes)
		}
		if sum.MemberCount != full.MemberCount {
			t.Errorf("MemberCount mismatch: %d vs %d", sum.MemberCount, full.MemberCount)
		}
		if sum.NamespaceCount != full.NamespaceCount {
			t.Errorf("NamespaceCount mismatch: %d vs %d", sum.NamespaceCount, full.NamespaceCount)
		}
		if sum.VariantCount != full.VariantCount {
			t.Errorf("VariantCount mismatch: %d vs %d", sum.VariantCount, full.VariantCount)
		}
		if sum.IssueCount != full.IssueCount {
			t.Errorf("IssueCount mismatch: %d vs %d", sum.IssueCount, full.IssueCount)
		}
		// Summary manifest should carry author/title/namespaces but no Members array.
		if sum.Manifest.Author != full.Manifest.Author {
			t.Errorf("Author mismatch: %q vs %q", sum.Manifest.Author, full.Manifest.Author)
		}
		if sum.Manifest.Title != full.Manifest.Title {
			t.Errorf("Title mismatch: %q vs %q", sum.Manifest.Title, full.Manifest.Title)
		}
		if len(sum.Manifest.Members) != 0 {
			t.Errorf("summary should not carry Members array, got %d", len(sum.Manifest.Members))
		}
		if len(sum.Manifest.Variants) != 0 {
			t.Errorf("summary should not carry Variants array, got %d", len(sum.Manifest.Variants))
		}
		// Health status must be populated.
		if sum.HealthStatus == "" {
			t.Error("health status should be populated on summary items")
		}
	}
}

// ---------------------------------------------------------------------------
// Summary listing supports residual search (broken items)
// ---------------------------------------------------------------------------

func TestSummaryListingBrokenHealthFromIssues(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()

	archives := libraryFixtureArchives(root, 1, 0)
	archives[0].Manifest.Issues = []modkit.Issue{
		{Code: "fatal", Severity: modkit.SeverityError, Message: "broken"},
	}
	items := applyLibraryArchives(t, store, root, archives)

	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	summaries, err := store.listLibrarySummaryItemsByIDsTx(ctx, tx, []string{items[0].EntityID})
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()

	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].HealthStatus != "broken" {
		t.Errorf("expected broken health, got %q", summaries[0].HealthStatus)
	}
}

// ---------------------------------------------------------------------------
// Full detail still available via GetEntity path
// ---------------------------------------------------------------------------

func TestGetEntityRetainsFullManifest(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()

	archives := libraryFixtureArchives(root, 1, 0)
	archives[0].Manifest.Members = []modkit.ArchiveMember{
		{Path: "a.jbeam", UncompressedBytes: 100},
		{Path: "b.jbeam", UncompressedBytes: 200},
	}
	archives[0].Manifest.Variants = []modkit.Variant{
		{Namespace: "vehicles", BaseName: "car"},
	}
	items := applyLibraryArchives(t, store, root, archives)

	detail, err := store.GetEntityDetail(ctx, items[0].EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Item.Manifest.Members) != 2 {
		t.Errorf("full detail should carry all Members, got %d", len(detail.Item.Manifest.Members))
	}
	if len(detail.Item.Manifest.Variants) != 1 {
		t.Errorf("full detail should carry Variants, got %d", len(detail.Item.Manifest.Variants))
	}
}

// ---------------------------------------------------------------------------
// FTS refresh uses summaries (no full manifest parse)
// ---------------------------------------------------------------------------

func TestFTSRefreshUsesArtifactSummaries(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()

	archives := libraryFixtureArchives(root, 1, 0)
	archives[0].Manifest.Author = "Searchable Author FTS"
	items := applyLibraryArchives(t, store, root, archives)

	// Verify the FTS content includes the author from the summary.
	var content string
	if err := store.db.QueryRowContext(ctx,
		`SELECT content FROM library_search_fts WHERE entity_id=?`,
		items[0].EntityID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Searchable Author FTS") {
		t.Errorf("FTS content should include author from summary, got: %s", content)
	}
}

// ---------------------------------------------------------------------------
// Changed metadata updates summary counts accurately
// ---------------------------------------------------------------------------

func TestSummaryCountsAccurateAfterMetadataChange(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()

	archives := libraryFixtureArchives(root, 1, 0)
	archives[0].Manifest.Members = []modkit.ArchiveMember{
		{Path: "a.jbeam"}, {Path: "b.jbeam"}, {Path: "c.jbeam"},
	}
	archives[0].Manifest.Namespaces = map[string][]string{
		"vehicles": {"car_a"}, "props": {"prop_b"},
	}
	archives[0].Manifest.Variants = []modkit.Variant{
		{Namespace: "vehicles", BaseName: "a"},
		{Namespace: "vehicles", BaseName: "b"},
	}
	archives[0].Manifest.Issues = []modkit.Issue{
		{Code: "warn1", Severity: modkit.SeverityWarning, Message: "w1"},
		{Code: "warn2", Severity: modkit.SeverityWarning, Message: "w2"},
		{Code: "err1", Severity: modkit.SeverityError, Message: "e1"},
	}
	items := applyLibraryArchives(t, store, root, archives)

	var memberCount, nsCount, variantCount, issueCount int
	if err := store.db.QueryRowContext(ctx,
		`SELECT member_count, namespace_count, variant_count, issue_count
		 FROM artifact_summaries WHERE artifact_id=?`, items[0].ArtifactID).
		Scan(&memberCount, &nsCount, &variantCount, &issueCount); err != nil {
		t.Fatal(err)
	}
	if memberCount != 3 {
		t.Errorf("member_count: got %d, want 3", memberCount)
	}
	if nsCount != 2 {
		t.Errorf("namespace_count: got %d, want 2", nsCount)
	}
	if variantCount != 2 {
		t.Errorf("variant_count: got %d, want 2", variantCount)
	}
	if issueCount != 3 {
		t.Errorf("issue_count: got %d, want 3", issueCount)
	}

	// Verify the summary listing returns matching counts.
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	summaries, err := store.listLibrarySummaryItemsByIDsTx(ctx, tx, []string{items[0].EntityID})
	_ = tx.Commit()
	if err != nil {
		t.Fatal(err)
	}
	s := summaries[0]
	if s.MemberCount != 3 || s.NamespaceCount != 2 || s.VariantCount != 2 || s.IssueCount != 3 {
		t.Errorf("summary listing counts: members=%d ns=%d variants=%d issues=%d",
			s.MemberCount, s.NamespaceCount, s.VariantCount, s.IssueCount)
	}
}

func TestSummarySearchKeepsNamespaceAndHealthSemantics(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()
	archives := libraryFixtureArchives(root, 2, 0)
	archives[0].Manifest.Namespaces = map[string][]string{"vehicles": {"test_car_unique"}}
	archives[0].Manifest.Issues = []modkit.Issue{{Code: "broken-part", Severity: modkit.SeverityError, Message: "Detailed inspection message"}}
	items := applyLibraryArchives(t, store, root, archives)
	results, err := store.ListLibrarySummary(ctx, "broken", "all", "in:namespace test_car_unique", "all", "active")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].EntityID != items[0].EntityID || results[0].IssueCount != 1 || results[0].HealthStatus != "broken" {
		t.Fatalf("summary filtering lost namespace or issue severity: %#v", results)
	}
	detail, err := store.GetEntityDetail(ctx, items[0].EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Item.Manifest.Issues) != 1 || detail.Item.Manifest.Issues[0].Message != "Detailed inspection message" {
		t.Fatal("lightweight listing discarded canonical inspection details")
	}
}

// ---------------------------------------------------------------------------
// Scoped health query restricts to requested entities
// ---------------------------------------------------------------------------

func TestScopedHealthQueryReturnsCorrectStatus(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()

	archives := libraryFixtureArchives(root, 2, 0)
	items := applyLibraryArchives(t, store, root, archives)

	// Insert a virus scan record for the first entity.
	scanID, _ := modkit.NewID()
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO virus_scans(id,entity_id,artifact_id,status,verdict,created_at,updated_at)
		 VALUES(?,?,?,'complete','clean',?,?)`,
		scanID, items[0].EntityID, items[0].ArtifactID, nowUTC(), nowUTC()); err != nil {
		t.Fatal(err)
	}

	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	summaries, err := store.listLibrarySummaryItemsByIDsTx(ctx, tx, []string{items[0].EntityID, items[1].EntityID})
	_ = tx.Commit()
	if err != nil {
		t.Fatal(err)
	}
	m := make(map[string]LibraryItem)
	for _, s := range summaries {
		m[s.EntityID] = s
	}
	if m[items[0].EntityID].HealthStatus != "clean" {
		t.Errorf("first entity should be clean, got %q", m[items[0].EntityID].HealthStatus)
	}
	if m[items[1].EntityID].HealthStatus != "unscanned" {
		t.Errorf("second entity should be unscanned, got %q", m[items[1].EntityID].HealthStatus)
	}
}

// ---------------------------------------------------------------------------
// ListLibrarySummary route is used by AppService.ListLibrary
// ---------------------------------------------------------------------------

func TestListLibrarySummaryRouteWorksForFrontend(t *testing.T) {
	store, root := openLibraryStorage(t)
	ctx := context.Background()

	archives := libraryFixtureArchives(root, 3, 0)
	applyLibraryArchives(t, store, root, archives)

	items, err := store.ListLibrarySummary(ctx, "", "", "", "", "")
	if err != nil {
		t.Fatalf("ListLibrarySummary: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("expected 3 items, got %d", len(items))
	}
	// Verify lightweight: no Members array.
	for _, item := range items {
		if len(item.Manifest.Members) > 0 {
			t.Errorf("summary listing should not carry Members, entity=%s", item.EntityID)
		}
	}
}

// ---------------------------------------------------------------------------
// EnsureArtifact path also maintains summary via trigger
// ---------------------------------------------------------------------------

func TestEnsureArtifactMaintainsSummaryViaTrigger(t *testing.T) {
	store, _ := openLibraryStorage(t)
	ctx := context.Background()

	manifest := modkit.Manifest{
		CentralFingerprint: "ensure-test-fp",
		FullSHA256:         "ensure-test-sha",
		SizeBytes:          999,
		Title:              "Ensure Test",
		Author:             "Ensure Author",
		Version:            "2.0",
		Members:            []modkit.ArchiveMember{{Path: "a.jbeam"}, {Path: "b.jbeam"}},
		Namespaces:         map[string][]string{"vehicles": {"car_x"}},
	}

	artifactID, err := store.EnsureArtifact(ctx, manifest)
	if err != nil {
		t.Fatal(err)
	}

	var title, author string
	var memberCount, nsCount int
	if err := store.db.QueryRowContext(ctx,
		`SELECT title, author, member_count, namespace_count
		 FROM artifact_summaries WHERE artifact_id=?`, artifactID).
		Scan(&title, &author, &memberCount, &nsCount); err != nil {
		t.Fatalf("missing summary after EnsureArtifact: %v", err)
	}
	if title != "Ensure Test" || author != "Ensure Author" {
		t.Errorf("unexpected title/author: %q / %q", title, author)
	}
	if memberCount != 2 || nsCount != 1 {
		t.Errorf("unexpected counts: members=%d ns=%d", memberCount, nsCount)
	}

	// Update the artifact via EnsureArtifact.
	manifest.Author = "Updated Author"
	manifest.Members = append(manifest.Members, modkit.ArchiveMember{Path: "c.jbeam"})
	_, err = store.EnsureArtifact(ctx, manifest)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.db.QueryRowContext(ctx,
		`SELECT author, member_count FROM artifact_summaries WHERE artifact_id=?`, artifactID).
		Scan(&author, &memberCount); err != nil {
		t.Fatal(err)
	}
	if author != "Updated Author" {
		t.Errorf("author not updated: %q", author)
	}
	if memberCount != 3 {
		t.Errorf("member_count not updated: %d", memberCount)
	}
}
