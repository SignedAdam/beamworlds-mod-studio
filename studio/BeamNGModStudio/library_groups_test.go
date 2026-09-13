package main

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

// openGroupStore returns a fresh Store and mod root directory.
func openGroupStore(tb testing.TB) (*Store, string) {
	tb.Helper()
	return openLibraryStorage(tb)
}

// seedGroupFixture populates a store with `count` mods at offset 0.  Each
// mod has a unique entity ID and a display name matching the fixture pattern.
func seedGroupFixture(tb testing.TB, store *Store, root string, count int) []LibraryItem {
	tb.Helper()
	archives := libraryFixtureArchives(root, count, 0)
	return applyLibraryArchives(tb, store, root, archives)
}

// promoteTag marks a tag as grouped and returns its ID.
func promoteTag(tb testing.TB, store *Store, tagName string) string {
	tb.Helper()
	ctx := context.Background()

	// Ensure the tag exists.
	var tagID string
	err := store.db.QueryRowContext(ctx,
		`SELECT id FROM mod_tags WHERE name=? COLLATE NOCASE`, tagName).Scan(&tagID)
	if err != nil {
		tb.Fatalf("find tag %q: %v", tagName, err)
	}
	if err := store.SetTagGrouped(ctx, tagID, true); err != nil {
		tb.Fatalf("promote tag %q: %v", tagName, err)
	}
	return tagID
}

// createGroupWithMembers creates a promoted group tag with the given name and
// assigns the specified entity IDs.
func createGroupWithMembers(tb testing.TB, store *Store, name string, entityIDs []string) string {
	tb.Helper()
	ctx := context.Background()
	if err := store.CreateGroupFromSelection(ctx, name, entityIDs); err != nil {
		tb.Fatalf("create group %q: %v", name, err)
	}
	var tagID string
	if err := store.db.QueryRowContext(ctx,
		`SELECT id FROM mod_tags WHERE name=? COLLATE NOCASE`, name).Scan(&tagID); err != nil {
		tb.Fatalf("find created group %q: %v", name, err)
	}
	return tagID
}

// setCollapsed sets the fold state for a group.
func setCollapsed(tb testing.TB, store *Store, groupID string, collapsed bool) {
	tb.Helper()
	if err := store.writeGroupCollapsed(context.Background(), groupID, collapsed); err != nil {
		tb.Fatalf("set collapsed %q: %v", groupID, err)
	}
}

// groupPage is a convenience wrapper that calls ListLibraryGroupPage with no
// filters and the default scope.
func groupPage(tb testing.TB, store *Store, page, pageSize int) LibraryGroupPage {
	tb.Helper()
	result, err := store.ListLibraryGroupPage(context.Background(), "", "", "", "", "", page, pageSize)
	if err != nil {
		tb.Fatalf("group page: %v", err)
	}
	return result
}

// groupPageQuery calls ListLibraryGroupPage with a search query.
func groupPageQuery(tb testing.TB, store *Store, query string, page, pageSize int) LibraryGroupPage {
	tb.Helper()
	result, err := store.ListLibraryGroupPage(context.Background(), "", "", query, "", "", page, pageSize)
	if err != nil {
		tb.Fatalf("group page query: %v", err)
	}
	return result
}

// groupPageScope calls ListLibraryGroupPage with a given scope.
func groupPageScope(tb testing.TB, store *Store, scope string, page, pageSize int) LibraryGroupPage {
	tb.Helper()
	result, err := store.ListLibraryGroupPage(context.Background(), "", "", "", "", scope, page, pageSize)
	if err != nil {
		tb.Fatalf("group page scope: %v", err)
	}
	return result
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestGroupFoldedGroupContributesOneRow verifies that a folded group
// contributes exactly one header row and suppresses all its member rows.
func TestGroupFoldedGroupContributesOneRow(t *testing.T) {
	store, root := openGroupStore(t)
	items := seedGroupFixture(t, store, root, 10)

	// Create a group with 5 members, leaving it folded (CreateGroupFromSelection
	// sets the fold state to collapsed).
	memberIDs := []string{items[0].EntityID, items[1].EntityID, items[2].EntityID, items[3].EntityID, items[4].EntityID}
	createGroupWithMembers(t, store, "FoldTest", memberIDs)

	result := groupPage(t, store, 0, 100)

	// The group header should be present.
	var groupRow *LibraryGroupRow
	for i := range result.Rows {
		if result.Rows[i].RowType == "group" && result.Rows[i].Label == "FoldTest" {
			groupRow = &result.Rows[i]
			break
		}
	}
	if groupRow == nil {
		t.Fatal("FoldTest group header not found")
	}
	if groupRow.ModCount != 5 {
		t.Errorf("FoldTest mod count = %d, want 5", groupRow.ModCount)
	}
	if !groupRow.Collapsed {
		t.Error("FoldTest should be collapsed")
	}

	// No member rows should carry the FoldTest group ID.
	for _, row := range result.Rows {
		if row.RowType == "mod" && row.GroupID == groupRow.GroupID {
			t.Errorf("found mod row under folded group: %s", row.Item.EntityID)
		}
	}
}

// TestGroupTotalRowsEqualsHeadersPlusMembersPlusUngrouped checks that
// TotalRows equals the count of group headers + visible member rows + ungrouped rows.
func TestGroupTotalRowsEqualsHeadersPlusMembersPlusUngrouped(t *testing.T) {
	store, root := openGroupStore(t)
	items := seedGroupFixture(t, store, root, 12)

	// Create two groups: one folded (3 members), one unfolded (4 members).
	foldedIDs := []string{items[0].EntityID, items[1].EntityID, items[2].EntityID}
	unfoldedIDs := []string{items[3].EntityID, items[4].EntityID, items[5].EntityID, items[6].EntityID}

	foldedTagID := createGroupWithMembers(t, store, "Folded", foldedIDs)
	unfoldedTagID := createGroupWithMembers(t, store, "Unfolded", unfoldedIDs)

	// Unfold the second group.
	setCollapsed(t, store, unfoldedTagID, false)
	_ = foldedTagID

	result := groupPage(t, store, 0, 0) // all rows

	// Count actual rows by type.
	headers := 0
	modRows := 0
	for _, row := range result.Rows {
		switch row.RowType {
		case "group":
			headers++
		case "mod":
			modRows++
		}
	}

	// Expect: 3 headers (Folded, Unfolded, Ungrouped) + 4 unfolded members + 5 ungrouped = 12
	// Folded: 1 header, 0 members (folded)
	// Unfolded: 1 header + 4 members
	// Ungrouped: 1 header + 5 members (items 7-11)
	expectedHeaders := 3
	expectedModRows := 4 + 5 // unfolded + ungrouped
	expectedTotal := expectedHeaders + expectedModRows

	if headers != expectedHeaders {
		t.Errorf("headers = %d, want %d", headers, expectedHeaders)
	}
	if modRows != expectedModRows {
		t.Errorf("mod rows = %d, want %d", modRows, expectedModRows)
	}
	if result.TotalRows != expectedTotal {
		t.Errorf("TotalRows = %d, want %d", result.TotalRows, expectedTotal)
	}
	if result.TotalRows != len(result.Rows) {
		t.Errorf("TotalRows (%d) != len(Rows) (%d)", result.TotalRows, len(result.Rows))
	}
}

// TestGroupModInTwoGroupsAppearsTwiceDistinctCountsOnce verifies that a mod
// in two unfolded groups appears once under each, while DistinctMods counts
// it once.
func TestGroupModInTwoGroupsAppearsTwiceDistinctCountsOnce(t *testing.T) {
	store, root := openGroupStore(t)
	items := seedGroupFixture(t, store, root, 5)

	shared := items[0].EntityID

	groupA := createGroupWithMembers(t, store, "Alpha", []string{shared, items[1].EntityID})
	groupB := createGroupWithMembers(t, store, "Beta", []string{shared, items[2].EntityID})

	// Unfold both.
	setCollapsed(t, store, groupA, false)
	setCollapsed(t, store, groupB, false)

	result := groupPage(t, store, 0, 0)

	// Count appearances of the shared entity.
	appearances := 0
	for _, row := range result.Rows {
		if row.RowType == "mod" && row.Item != nil && row.Item.EntityID == shared {
			appearances++
		}
	}
	if appearances != 2 {
		t.Errorf("shared mod appearances = %d, want 2", appearances)
	}

	// DistinctMods should count the shared entity only once.
	if result.DistinctMods != 5 {
		t.Errorf("DistinctMods = %d, want 5", result.DistinctMods)
	}
}

// TestGroupCountsAndSizesOverDistinctIdentities checks that group counts and
// sizes reflect distinct mod identities, not additive across overlapping
// group memberships.
func TestGroupCountsAndSizesOverDistinctIdentities(t *testing.T) {
	store, root := openGroupStore(t)
	items := seedGroupFixture(t, store, root, 5)

	shared := items[0]

	groupA := createGroupWithMembers(t, store, "AlphaSize", []string{shared.EntityID, items[1].EntityID})
	setCollapsed(t, store, groupA, false)

	groupB := createGroupWithMembers(t, store, "BetaSize", []string{shared.EntityID, items[2].EntityID})
	setCollapsed(t, store, groupB, false)

	result := groupPage(t, store, 0, 0)

	// Find group headers.
	for _, row := range result.Rows {
		if row.RowType != "group" {
			continue
		}
		switch row.Label {
		case "AlphaSize":
			if row.ModCount != 2 {
				t.Errorf("AlphaSize modCount = %d, want 2", row.ModCount)
			}
			expectedSize := shared.SizeBytes + items[1].SizeBytes
			if row.SizeBytes != expectedSize {
				t.Errorf("AlphaSize sizeBytes = %d, want %d", row.SizeBytes, expectedSize)
			}
		case "BetaSize":
			if row.ModCount != 2 {
				t.Errorf("BetaSize modCount = %d, want 2", row.ModCount)
			}
			expectedSize := shared.SizeBytes + items[2].SizeBytes
			if row.SizeBytes != expectedSize {
				t.Errorf("BetaSize sizeBytes = %d, want %d", row.SizeBytes, expectedSize)
			}
		}
	}
}

// TestGroupSearchRevealsFoldedMembers verifies R3: a search matching only
// inside a folded group reveals those members with a match count, while
// leaving the stored fold state untouched.
func TestGroupSearchRevealsFoldedMembers(t *testing.T) {
	store, root := openGroupStore(t)
	items := seedGroupFixture(t, store, root, 10)

	// The fixture items[0] has title "Road Runner", the rest are "Library Mod NNNNN".
	// Put Road Runner in a folded group.
	groupID := createGroupWithMembers(t, store, "SearchFold", []string{items[0].EntityID})

	// Verify it is collapsed.
	if !store.readGroupCollapsed(context.Background(), groupID) {
		t.Fatal("SearchFold should start collapsed")
	}

	// Search for "Road Runner" - should reveal the mod under the folded group.
	result := groupPageQuery(t, store, "Road Runner", 0, 100)

	var groupRow *LibraryGroupRow
	memberFound := false
	for i := range result.Rows {
		row := &result.Rows[i]
		if row.RowType == "group" && row.Label == "SearchFold" {
			groupRow = row
		}
		if row.RowType == "mod" && row.Item != nil && row.Item.EntityID == items[0].EntityID {
			memberFound = true
		}
	}

	if groupRow == nil {
		t.Fatal("SearchFold header not found in search results")
	}
	if groupRow.MatchCount != 1 {
		t.Errorf("SearchFold matchCount = %d, want 1", groupRow.MatchCount)
	}
	if !groupRow.Collapsed {
		t.Error("SearchFold should report collapsed = true (stored state)")
	}
	if !memberFound {
		t.Error("Road Runner should be revealed despite fold")
	}

	// Verify stored fold state is untouched.
	if !store.readGroupCollapsed(context.Background(), groupID) {
		t.Error("SearchFold stored fold state was modified by search")
	}
}

// TestGroupUngroupedAccountsForEveryMod verifies that every in-scope mod not
// in a group appears in the Ungrouped section.
func TestGroupUngroupedAccountsForEveryMod(t *testing.T) {
	store, root := openGroupStore(t)
	items := seedGroupFixture(t, store, root, 10)

	// Group 3 mods.
	createGroupWithMembers(t, store, "Grouped3", []string{items[0].EntityID, items[1].EntityID, items[2].EntityID})

	result := groupPage(t, store, 0, 0)

	// Find Ungrouped header.
	var ungroupedRow *LibraryGroupRow
	ungroupedModCount := 0
	for i := range result.Rows {
		row := &result.Rows[i]
		if row.RowType == "group" && row.GroupID == ungroupedGroupID {
			ungroupedRow = row
		}
		if row.RowType == "mod" && row.GroupID == ungroupedGroupID {
			ungroupedModCount++
		}
	}

	if ungroupedRow == nil {
		t.Fatal("Ungrouped header not found")
	}
	if ungroupedRow.ModCount != 7 {
		t.Errorf("Ungrouped modCount = %d, want 7", ungroupedRow.ModCount)
	}
	if ungroupedModCount != 7 {
		t.Errorf("ungrouped mod rows = %d, want 7", ungroupedModCount)
	}

	// Verify total accounting: grouped(3) + ungrouped(7) = all(10).
	if result.DistinctMods != 10 {
		t.Errorf("DistinctMods = %d, want 10", result.DistinctMods)
	}
}

// TestGroupMigrationPromotesNothing verifies that the additive migration does
// not automatically promote any tag.
func TestGroupMigrationPromotesNothing(t *testing.T) {
	store, _ := openGroupStore(t)
	ctx := context.Background()

	rows, err := store.db.QueryContext(ctx, `SELECT id FROM mod_tags WHERE grouped=1`)
	if err != nil {
		t.Fatalf("query grouped tags: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 0 {
		t.Errorf("migration promoted %d tag(s): %v", len(ids), ids)
	}
}

// TestGroupArchivedExcludedUnderActiveScope verifies that archived mods are
// excluded from the grouped stream under the active scope.
func TestGroupArchivedExcludedUnderActiveScope(t *testing.T) {
	store, root := openGroupStore(t)
	items := seedGroupFixture(t, store, root, 5)

	// Archive one mod.
	ctx := context.Background()
	_, err := store.db.ExecContext(ctx,
		`UPDATE entities SET archived_at=? WHERE id=?`, nowUTC(), items[0].EntityID)
	if err != nil {
		t.Fatalf("archive entity: %v", err)
	}

	// Create a group containing the archived mod and a live mod.
	groupID := createGroupWithMembers(t, store, "ArchiveTest", []string{items[0].EntityID, items[1].EntityID})
	setCollapsed(t, store, groupID, false)

	// Active scope should exclude the archived mod.
	result := groupPageScope(t, store, "active", 0, 0)

	for _, row := range result.Rows {
		if row.RowType == "mod" && row.Item != nil && row.Item.EntityID == items[0].EntityID {
			t.Error("archived mod should not appear under active scope")
		}
	}

	// The group header should show count=1 (only the live mod).
	for _, row := range result.Rows {
		if row.RowType == "group" && row.Label == "ArchiveTest" {
			if row.ModCount != 1 {
				t.Errorf("ArchiveTest modCount under active scope = %d, want 1", row.ModCount)
			}
		}
	}

	if result.DistinctMods != 4 {
		t.Errorf("DistinctMods under active scope = %d, want 4", result.DistinctMods)
	}
}

// TestGroupPageBoundaryInsideIdenticalNames creates many mods with the same
// display name and verifies that consecutive pages are disjoint and complete.
func TestGroupPageBoundaryInsideIdenticalNames(t *testing.T) {
	store, root := openGroupStore(t)

	// Create 20 mods all named "Samename" to force tie-breaking by entity ID.
	archives := libraryFixtureArchives(root, 20, 0)
	for i := range archives {
		archives[i].Manifest.Title = "Samename"
	}
	items := applyLibraryArchives(t, store, root, archives)

	// No groups; all ungrouped.  Page size 7 forces a boundary inside the
	// identical-name block.
	pageSize := 7
	allEntityIDs := map[string]bool{}
	seenEntityIDs := map[string]bool{}
	for _, item := range items {
		allEntityIDs[item.EntityID] = true
	}

	// Collect pages.
	for page := 0; ; page++ {
		result := groupPage(t, store, page, pageSize)
		if len(result.Rows) == 0 {
			break
		}
		for _, row := range result.Rows {
			if row.RowType == "mod" && row.Item != nil {
				if seenEntityIDs[row.Item.EntityID] {
					t.Errorf("page %d: duplicate entity %s", page, row.Item.EntityID)
				}
				seenEntityIDs[row.Item.EntityID] = true
			}
		}
		if page > 10 {
			t.Fatal("too many pages")
		}
	}

	// All mods should have been seen.
	for id := range allEntityIDs {
		if !seenEntityIDs[id] {
			t.Errorf("entity %s never appeared in any page", id)
		}
	}
}
