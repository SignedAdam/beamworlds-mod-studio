package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Membership merge: enabled beats disabled, positions preserved
// ---------------------------------------------------------------------------

func TestReplacementMergesEnabledAndDisabledMemberships(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	ctx := context.Background()

	keeper := modFamilyScanArchive(t, root, "keeper.zip", "Keeper Mod", "Author", "2.0", "", "keeper-sha", "keeper-fp", 200, testArchiveModified(10), 0)
	old := modFamilyScanArchive(t, root, "old.zip", "Old Mod", "Author", "1.0", "", "old-sha", "old-fp", 100, testArchiveModified(1), 0)
	items := applyLibraryArchives(t, service.store, root, []ScanArchive{keeper, old})
	if len(items) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(items))
	}
	keeperItem := itemByPath(t, items, keeper.ArchivePath)
	oldItem := itemByPath(t, items, old.ArchivePath)

	// Collection A: keeper enabled, old disabled → keeper stays enabled.
	collA, err := service.CreateCollection("Collection A", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(collA.Collection.ID, []string{keeperItem.EntityID, oldItem.EntityID}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionModsEnabled(collA.Collection.ID, []string{oldItem.EntityID}, false); err != nil {
		t.Fatal(err)
	}

	// Collection B: only old has membership (enabled) → keeper inherits it.
	collB, err := service.CreateCollection("Collection B", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(collB.Collection.ID, []string{oldItem.EntityID}, true); err != nil {
		t.Fatal(err)
	}

	// Collection C: both disabled → keeper stays disabled.
	collC, err := service.CreateCollection("Collection C", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(collC.Collection.ID, []string{keeperItem.EntityID, oldItem.EntityID}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionModsEnabled(collC.Collection.ID, []string{keeperItem.EntityID, oldItem.EntityID}, false); err != nil {
		t.Fatal(err)
	}

	impact, err := service.PlanModReplacement(keeperItem.EntityID, []string{oldItem.EntityID})
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.Refusals) != 0 {
		t.Fatalf("unexpected refusals: %v", impact.Refusals)
	}

	result, err := service.ReplaceModArchives(keeperItem.EntityID, []string{oldItem.EntityID}, impact.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replaced != 1 || result.Forgotten != 1 {
		t.Fatalf("result = %+v", result)
	}

	// Old entity must be gone.
	if _, err := service.store.GetLibraryItem(ctx, oldItem.EntityID); err == nil {
		t.Fatal("old entity survived replacement")
	}

	// Collection A: keeper still enabled.
	detailA, err := service.GetCollection(collA.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detailA.Members) != 1 || detailA.Members[0].EntityID != keeperItem.EntityID || !detailA.Members[0].Enabled {
		t.Fatalf("collection A members = %+v, want keeper enabled", detailA.Members)
	}

	// Collection B: keeper inherited the membership and is enabled.
	detailB, err := service.GetCollection(collB.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detailB.Members) != 1 || detailB.Members[0].EntityID != keeperItem.EntityID || !detailB.Members[0].Enabled {
		t.Fatalf("collection B members = %+v, want keeper enabled via inheritance", detailB.Members)
	}

	// Collection C: keeper is disabled (both were disabled, disabled wins).
	detailC, err := service.GetCollection(collC.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detailC.Members) != 1 || detailC.Members[0].EntityID != keeperItem.EntityID || detailC.Members[0].Enabled {
		t.Fatalf("collection C members = %+v, want keeper disabled", detailC.Members)
	}
}

// ---------------------------------------------------------------------------
// Group + tag union: keeper inherits source tags and group memberships
// ---------------------------------------------------------------------------

func TestReplacementUnionsTagsAndGroups(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	ctx := context.Background()

	keeper := modFamilyScanArchive(t, root, "keeper-tags.zip", "Keeper Tags", "Author", "2.0", "", "kt-sha", "kt-fp", 200, testArchiveModified(10), 0)
	old := modFamilyScanArchive(t, root, "old-tags.zip", "Old Tags", "Author", "1.0", "", "ot-sha", "ot-fp", 100, testArchiveModified(1), 0)
	items := applyLibraryArchives(t, service.store, root, []ScanArchive{keeper, old})
	keeperItem := itemByPath(t, items, keeper.ArchivePath)
	oldItem := itemByPath(t, items, old.ArchivePath)

	// Create tags.
	org, err := service.CreateModTag("Shared Tag", "#ff0000", "tag")
	if err != nil {
		t.Fatal(err)
	}
	sharedTagID := findModTag(t, org, "Shared Tag").ID

	org, err = service.CreateModTag("Old Only Tag", "#00ff00", "tag")
	if err != nil {
		t.Fatal(err)
	}
	oldOnlyTagID := findModTag(t, org, "Old Only Tag").ID

	org, err = service.CreateModTag("Keeper Only Tag", "#0000ff", "tag")
	if err != nil {
		t.Fatal(err)
	}
	keeperOnlyTagID := findModTag(t, org, "Keeper Only Tag").ID

	// Assign tags.
	if _, err := service.SetLibraryItemTags(keeperItem.EntityID, []string{sharedTagID, keeperOnlyTagID}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetLibraryItemTags(oldItem.EntityID, []string{sharedTagID, oldOnlyTagID}); err != nil {
		t.Fatal(err)
	}

	// Make "Shared Tag" a group and add both mods.
	if _, err := service.SetTagGrouped(sharedTagID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddModsToGroup(sharedTagID, []string{keeperItem.EntityID, oldItem.EntityID}); err != nil {
		t.Fatal(err)
	}

	impact, err := service.PlanModReplacement(keeperItem.EntityID, []string{oldItem.EntityID})
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.Groups) == 0 {
		t.Fatal("impact should list the group")
	}

	result, err := service.ReplaceModArchives(keeperItem.EntityID, []string{oldItem.EntityID}, impact.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replaced != 1 {
		t.Fatalf("replaced = %d, want 1", result.Replaced)
	}

	// Keeper should have all three tags now.
	keeperDetail, err := service.store.GetLibraryItem(ctx, keeperItem.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	tagIDs := make(map[string]struct{})
	for _, tag := range keeperDetail.Tags {
		tagIDs[tag.ID] = struct{}{}
	}
	for _, expected := range []string{sharedTagID, oldOnlyTagID, keeperOnlyTagID} {
		if _, ok := tagIDs[expected]; !ok {
			t.Fatalf("keeper missing tag %s; has %v", expected, keeperDetail.Tags)
		}
	}

	// Old entity gone.
	if _, err := service.store.GetLibraryItem(ctx, oldItem.EntityID); err == nil {
		t.Fatal("old entity survived replacement")
	}
}

// ---------------------------------------------------------------------------
// Play resolves keeper only after replacement
// ---------------------------------------------------------------------------

func TestReplacementPlayResolvesKeeperOnly(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")

	keeper := modFamilyScanArchive(t, root, "play-keeper.zip", "Play Keeper", "Author", "2.0", "", "pk-sha", "pk-fp", 200, testArchiveModified(10), 0)
	old := modFamilyScanArchive(t, root, "play-old.zip", "Play Old", "Author", "1.0", "", "po-sha", "po-fp", 100, testArchiveModified(1), 0)
	items := applyLibraryArchives(t, service.store, root, []ScanArchive{keeper, old})
	keeperItem := itemByPath(t, items, keeper.ArchivePath)
	oldItem := itemByPath(t, items, old.ArchivePath)

	coll, err := service.CreateCollection("Play Test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(coll.Collection.ID, []string{keeperItem.EntityID, oldItem.EntityID}, true); err != nil {
		t.Fatal(err)
	}

	// Before replacement: both in selection.
	selBefore, err := service.ResolvePlaySelection([]string{coll.Collection.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selBefore.Mods) != 2 {
		t.Fatalf("before: selection has %d mods, want 2", len(selBefore.Mods))
	}

	impact, err := service.PlanModReplacement(keeperItem.EntityID, []string{oldItem.EntityID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ReplaceModArchives(keeperItem.EntityID, []string{oldItem.EntityID}, impact.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replaced != 1 {
		t.Fatalf("replaced = %d", result.Replaced)
	}

	// After replacement: only keeper.
	selAfter, err := service.ResolvePlaySelection([]string{coll.Collection.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selAfter.Mods) != 1 || selAfter.Mods[0].EntityID != keeperItem.EntityID {
		t.Fatalf("after: selection = %+v, want only keeper", selAfter.Mods)
	}
}

// ---------------------------------------------------------------------------
// Stale fingerprint has zero side effects
// ---------------------------------------------------------------------------

func TestReplacementStaleFingerprint(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	ctx := context.Background()

	keeper := modFamilyScanArchive(t, root, "stale-keeper.zip", "Stale Keeper", "Author", "2.0", "", "sk-sha", "sk-fp", 200, testArchiveModified(10), 0)
	old := modFamilyScanArchive(t, root, "stale-old.zip", "Stale Old", "Author", "1.0", "", "so-sha", "so-fp", 100, testArchiveModified(1), 0)
	items := applyLibraryArchives(t, service.store, root, []ScanArchive{keeper, old})
	keeperItem := itemByPath(t, items, keeper.ArchivePath)
	oldItem := itemByPath(t, items, old.ArchivePath)

	coll, err := service.CreateCollection("Stale Test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(coll.Collection.ID, []string{oldItem.EntityID}, true); err != nil {
		t.Fatal(err)
	}

	impact, err := service.PlanModReplacement(keeperItem.EntityID, []string{oldItem.EntityID})
	if err != nil {
		t.Fatal(err)
	}

	// Mutate state after planning.
	if _, err := service.SetCollectionMods(coll.Collection.ID, []string{keeperItem.EntityID}, true); err != nil {
		t.Fatal(err)
	}

	// Execute with stale fingerprint — must error.
	_, err = service.ReplaceModArchives(keeperItem.EntityID, []string{oldItem.EntityID}, impact.Fingerprint)
	if err == nil {
		t.Fatal("stale fingerprint should have been rejected")
	}
	if !strings.Contains(err.Error(), "changed") {
		t.Fatalf("error = %v, want stale message", err)
	}

	// Old entity still exists — no side effects.
	if _, err := service.store.GetLibraryItem(ctx, oldItem.EntityID); err != nil {
		t.Fatal("old entity was deleted despite stale fingerprint rejection")
	}
	if _, err := os.Stat(old.ArchivePath); err != nil {
		t.Fatal("old archive file was deleted despite stale fingerprint rejection")
	}
}

// ---------------------------------------------------------------------------
// Workspace protection: source with project is refused
// ---------------------------------------------------------------------------

func TestReplacementRefusesSourceWithWorkspace(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)

	// CreateNewMod produces an entity with a workspace.
	mod, err := service.CreateNewMod(NewModRequest{Name: "Has Project", ModID: "ws_project", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(service.config.DataDir, "library")
	keeper := modFamilyScanArchive(t, root, "ws-keeper.zip", "WS Keeper", "Author", "2.0", "", "wsk-sha", "wsk-fp", 200, testArchiveModified(10), 0)
	items := applyLibraryArchives(t, service.store, root, []ScanArchive{keeper})
	keeperItem := itemByPath(t, items, keeper.ArchivePath)

	impact, err := service.PlanModReplacement(keeperItem.EntityID, []string{mod.Entity.EntityID})
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.Refusals) == 0 {
		t.Fatal("expected a refusal for the workspace-protected source")
	}
	if len(impact.Workspaces) == 0 {
		t.Fatal("expected workspace to be listed")
	}

	// Execution also refuses.
	_, err = service.ReplaceModArchives(keeperItem.EntityID, []string{mod.Entity.EntityID}, impact.Fingerprint)
	if err == nil {
		t.Fatal("replacement should have refused a source with a workspace")
	}
}

// ---------------------------------------------------------------------------
// Multiple old archive paths: all are recycled
// ---------------------------------------------------------------------------

func TestReplacementRecyclesMultipleArchivePaths(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")

	// Create an entity with two archive links by scanning two paths with same fingerprint.
	old1 := modFamilyScanArchive(t, root, filepath.Join("a", "mod.zip"), "Multi Path Mod", "Author", "1.0", "", "mp-sha", "mp-fp", 100, testArchiveModified(1), 0)
	old2 := modFamilyScanArchive(t, root, filepath.Join("b", "mod.zip"), "Multi Path Mod", "Author", "1.0", "", "mp-sha", "mp-fp", 150, testArchiveModified(2), 0)
	items := applyLibraryArchives(t, service.store, root, []ScanArchive{old1, old2})
	if len(items) != 1 {
		t.Fatalf("expected 1 entity with 2 links, got %d entities", len(items))
	}
	oldItem := items[0]

	keeper := modFamilyScanArchive(t, root, "multi-keeper.zip", "Multi Keeper", "Author", "2.0", "", "mk-sha", "mk-fp", 200, testArchiveModified(10), 0)
	items = applyLibraryArchives(t, service.store, root, []ScanArchive{keeper})
	keeperItem := itemByPath(t, items, keeper.ArchivePath)

	impact, err := service.PlanModReplacement(keeperItem.EntityID, []string{oldItem.EntityID})
	if err != nil {
		t.Fatal(err)
	}
	if impact.ArchiveCount != 2 || impact.ArchiveBytes != 250 {
		t.Fatalf("review omitted an obsolete archive: %#v", impact)
	}

	result, err := service.ReplaceModArchives(keeperItem.EntityID, []string{oldItem.EntityID}, impact.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if result.Recycled < 2 {
		if len(result.Failures) > 0 && strings.Contains(result.Failures[0], "only supported on Windows") {
			t.Skipf("archive recycling is not supported on this platform: %+v", result)
		}
		t.Fatalf("recycled = %d, want at least 2 for both archive links; failures = %v", result.Recycled, result.Failures)
	}
	if result.Forgotten != 1 {
		t.Fatalf("forgotten = %d, want 1", result.Forgotten)
	}

	// Both old files should be gone.
	for _, p := range []string{old1.ArchivePath, old2.ArchivePath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("old archive %s survived recycling: %v", p, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Recycle failure preserves source entity for retry
// ---------------------------------------------------------------------------

func TestReplacementRecycleFailurePreservesSource(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	ctx := context.Background()

	keeper := modFamilyScanArchive(t, root, "rf-keeper.zip", "RF Keeper", "Author", "2.0", "", "rfk-sha", "rfk-fp", 200, testArchiveModified(10), 0)
	old := modFamilyScanArchive(t, root, "rf-old.zip", "RF Old", "Author", "1.0", "", "rfo-sha", "rfo-fp", 100, testArchiveModified(1), 0)
	items := applyLibraryArchives(t, service.store, root, []ScanArchive{keeper, old})
	keeperItem := itemByPath(t, items, keeper.ArchivePath)
	oldItem := itemByPath(t, items, old.ArchivePath)
	collection, err := service.CreateCollection("Locked version users", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(collection.Collection.ID, []string{oldItem.EntityID}, true); err != nil {
		t.Fatal(err)
	}
	groupID := createGroupWithMembers(t, service.store, "Locked version group", []string{oldItem.EntityID})

	// Lock the old archive so recycling fails on Windows.
	handle, err := os.Open(old.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()

	impact, err := service.PlanModReplacement(keeperItem.EntityID, []string{oldItem.EntityID})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.ReplaceModArchives(keeperItem.EntityID, []string{oldItem.EntityID}, impact.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	// References were transferred (Replaced=1) but recycling may have failed.
	if result.Replaced != 1 {
		t.Fatalf("replaced = %d, want 1", result.Replaced)
	}
	if result.Recycled == 1 {
		t.Skipf("the platform allowed an open archive to be recycled: %+v", result)
	}
	if len(result.Failures) == 0 {
		t.Fatal("expected a recycle failure")
	}
	// Source entity must still be indexed (file is still on disk).
	if _, err := service.store.GetLibraryItem(ctx, oldItem.EntityID); err != nil {
		t.Fatal("old entity was forgotten despite recycle failure")
	}
	selection, err := service.ResolvePlaySelection([]string{collection.Collection.ID}, nil)
	if err != nil || len(selection.Mods) != 1 || selection.Mods[0].EntityID != keeperItem.EntityID {
		t.Fatalf("recycle failure lost the replacement's Play usage: %#v, %v", selection, err)
	}
	var oldUsages int
	if err := service.store.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM collection_mods WHERE entity_id=?)+(SELECT COUNT(*) FROM mod_tag_entities WHERE entity_id=?)`, oldItem.EntityID, oldItem.EntityID).Scan(&oldUsages); err != nil {
		t.Fatal(err)
	}
	if oldUsages != 0 {
		t.Fatal("failed cleanup restored obsolete collection/group usages")
	}
	group, err := service.store.ListLibraryGroupPage(ctx, "", "", "", "", "", 0, 0, "name", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range group.Rows {
		if row.GroupID == groupID && row.RowType == "mod" && row.Item.EntityID != keeperItem.EntityID {
			t.Fatal("old version still appears in the transferred group")
		}
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	retry, err := service.PlanModReplacement(keeperItem.EntityID, []string{oldItem.EntityID})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := service.ReplaceModArchives(keeperItem.EntityID, []string{oldItem.EntityID}, retry.Fingerprint)
	if err != nil || finished.Forgotten != 1 || len(finished.Failures) != 0 {
		t.Fatalf("cleanup was not retryable after unlocking: %#v, %v", finished, err)
	}
}

func TestReplacementRefusesArchivedSource(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	keeper := modFamilyScanArchive(t, root, "keeper.zip", "Keeper", "Author", "2.0", "", "keeper-sha", "keeper-fp", 200, testArchiveModified(10), 0)
	old := modFamilyScanArchive(t, root, "old.zip", "Old", "Author", "1.0", "", "old-sha", "old-fp", 100, testArchiveModified(1), 0)
	items := applyLibraryArchives(t, service.store, root, []ScanArchive{keeper, old})
	keeperItem, oldItem := itemByPath(t, items, keeper.ArchivePath), itemByPath(t, items, old.ArchivePath)
	if _, err := service.ArchiveMods([]string{oldItem.EntityID}); err != nil {
		t.Fatal(err)
	}
	impact, err := service.PlanModReplacement(keeperItem.EntityID, []string{oldItem.EntityID})
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.Refusals) == 0 {
		t.Fatal("archived source was offered for replacement")
	}
	if _, err := service.ReplaceModArchives(keeperItem.EntityID, []string{oldItem.EntityID}, impact.Fingerprint); err == nil {
		t.Fatal("archived source was replaced")
	}
	if _, err := os.Stat(old.ArchivePath); err != nil {
		t.Fatal("archived source file was touched")
	}
}

func TestReplacementRefusesUnavailableKeeper(t *testing.T) {
	for _, state := range []string{"archived", "missing", "inactive"} {
		t.Run(state, func(t *testing.T) {
			service := newTestAppService(t)
			root := filepath.Join(service.config.DataDir, "library")
			keeper := modFamilyScanArchive(t, root, "keeper.zip", "Keeper", "Author", "2.0", "", "keeper-sha", "keeper-fp", 200, testArchiveModified(10), 0)
			old := modFamilyScanArchive(t, root, "old.zip", "Old", "Author", "1.0", "", "old-sha", "old-fp", 100, testArchiveModified(1), 0)
			items := applyLibraryArchives(t, service.store, root, []ScanArchive{keeper, old})
			keeperItem, oldItem := itemByPath(t, items, keeper.ArchivePath), itemByPath(t, items, old.ArchivePath)
			switch state {
			case "archived":
				if _, err := service.ArchiveMods([]string{keeperItem.EntityID}); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(keeper.ArchivePath); err != nil {
					t.Fatal(err)
				}
			case "inactive":
				if _, err := service.store.db.Exec(`UPDATE archive_links SET active=0 WHERE entity_id=?`, keeperItem.EntityID); err != nil {
					t.Fatal(err)
				}
			}
			impact, err := service.PlanModReplacement(keeperItem.EntityID, []string{oldItem.EntityID})
			if err != nil {
				t.Fatal(err)
			}
			if len(impact.Refusals) == 0 {
				t.Fatal("unusable keeper was offered for replacement")
			}
			if _, err := service.ReplaceModArchives(keeperItem.EntityID, []string{oldItem.EntityID}, impact.Fingerprint); err == nil {
				t.Fatal("working source was retired for an unusable keeper")
			}
			if _, err := os.Stat(old.ArchivePath); err != nil {
				t.Fatal("working source archive was touched")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Plan lists all affected collections, groups, tags
// ---------------------------------------------------------------------------

func TestReplacementPlanListsAffectedOrganization(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")

	keeper := modFamilyScanArchive(t, root, "plan-keeper.zip", "Plan Keeper", "Author", "2.0", "", "plk-sha", "plk-fp", 200, testArchiveModified(10), 0)
	old := modFamilyScanArchive(t, root, "plan-old.zip", "Plan Old", "Author", "1.0", "", "plo-sha", "plo-fp", 100, testArchiveModified(1), 0)
	items := applyLibraryArchives(t, service.store, root, []ScanArchive{keeper, old})
	keeperItem := itemByPath(t, items, keeper.ArchivePath)
	oldItem := itemByPath(t, items, old.ArchivePath)

	// Create a collection for old only.
	coll, err := service.CreateCollection("Plan Coll", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(coll.Collection.ID, []string{oldItem.EntityID}, true); err != nil {
		t.Fatal(err)
	}

	// Create a tag for old only.
	org, err := service.CreateModTag("Plan Tag", "#ff0000", "tag")
	if err != nil {
		t.Fatal(err)
	}
	tagID := findModTag(t, org, "Plan Tag").ID
	if _, err := service.SetLibraryItemTags(oldItem.EntityID, []string{tagID}); err != nil {
		t.Fatal(err)
	}

	// Create a group for old only.
	org, err = service.CreateModTag("Plan Group", "#00ff00", "tag")
	if err != nil {
		t.Fatal(err)
	}
	groupTagID := findModTag(t, org, "Plan Group").ID
	if _, err := service.SetTagGrouped(groupTagID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddModsToGroup(groupTagID, []string{oldItem.EntityID}); err != nil {
		t.Fatal(err)
	}

	impact, err := service.PlanModReplacement(keeperItem.EntityID, []string{oldItem.EntityID})
	if err != nil {
		t.Fatal(err)
	}

	if !containsString(impact.Collections, "Plan Coll") {
		t.Fatalf("collections = %v, want 'Plan Coll'", impact.Collections)
	}
	if !containsString(impact.Tags, "Plan Tag") {
		t.Fatalf("tags = %v, want 'Plan Tag'", impact.Tags)
	}
	if !containsString(impact.Groups, "Plan Group") {
		t.Fatalf("groups = %v, want 'Plan Group'", impact.Groups)
	}
}

// ---------------------------------------------------------------------------
// Remove: old versions leave their collections, groups, and tags; the keeper
// gains nothing and keeps what it had
// ---------------------------------------------------------------------------

func TestRemoveModVersionsDropsUsagesWithoutTransfer(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	ctx := context.Background()

	keeper := modFamilyScanArchive(t, root, "rm-keeper.zip", "Remove Mod", "Author", "2.0", "", "rmk-sha", "rmk-fp", 200, testArchiveModified(10), 0)
	old := modFamilyScanArchive(t, root, "rm-old.zip", "Remove Mod", "Author", "1.0", "", "rmo-sha", "rmo-fp", 100, testArchiveModified(1), 0)
	items := applyLibraryArchives(t, service.store, root, []ScanArchive{keeper, old})
	keeperItem := itemByPath(t, items, keeper.ArchivePath)
	oldItem := itemByPath(t, items, old.ArchivePath)

	shared, err := service.CreateCollection("Shared", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(shared.Collection.ID, []string{keeperItem.EntityID, oldItem.EntityID}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionModsEnabled(shared.Collection.ID, []string{keeperItem.EntityID}, false); err != nil {
		t.Fatal(err)
	}
	oldOnly, err := service.CreateCollection("Old only", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(oldOnly.Collection.ID, []string{oldItem.EntityID}, true); err != nil {
		t.Fatal(err)
	}
	org, err := service.CreateModTag("Old Tag", "#ff0000", "tag")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetLibraryItemTags(oldItem.EntityID, []string{findModTag(t, org, "Old Tag").ID}); err != nil {
		t.Fatal(err)
	}
	createGroupWithMembers(t, service.store, "Old Group", []string{oldItem.EntityID})

	// The review lists each version's own usages, split into collections,
	// groups, and tags, so the choice can be made per version.
	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	var oldMember *ModFamilyMember
	for fi := range families {
		for mi := range families[fi].Members {
			if families[fi].Members[mi].EntityID == oldItem.EntityID {
				oldMember = &families[fi].Members[mi]
			}
		}
	}
	if oldMember == nil {
		t.Fatalf("old version is not in a family: %#v", families)
	}
	if !slices.Equal(oldMember.Collections, []string{"Old only", "Shared"}) || !slices.Equal(oldMember.Groups, []string{"Old Group"}) || !slices.Equal(oldMember.Tags, []string{"Old Tag"}) {
		t.Fatalf("old member usages = collections %v, groups %v, tags %v", oldMember.Collections, oldMember.Groups, oldMember.Tags)
	}

	impact, err := service.PlanModReplacement(keeperItem.EntityID, []string{oldItem.EntityID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RemoveModVersions(keeperItem.EntityID, []string{oldItem.EntityID}, impact.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if result.Forgotten != 1 {
		if len(result.Failures) > 0 && strings.Contains(result.Failures[0], "only supported on Windows") {
			t.Skipf("archive recycling is not supported on this platform: %+v", result)
		}
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(old.ArchivePath); !os.IsNotExist(err) {
		t.Fatalf("old archive survived: %v", err)
	}

	detail, err := service.GetCollection(oldOnly.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Members) != 0 {
		t.Fatalf("keeper was given the old version's place: %+v", detail.Members)
	}
	detail, err = service.GetCollection(shared.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Members) != 1 || detail.Members[0].EntityID != keeperItem.EntityID || detail.Members[0].Enabled {
		t.Fatalf("keeper's own membership changed: %+v", detail.Members)
	}
	keeperDetail, err := service.store.GetLibraryItem(ctx, keeperItem.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keeperDetail.Tags) != 0 {
		t.Fatalf("keeper inherited tags or groups: %+v", keeperDetail.Tags)
	}
}

// ---------------------------------------------------------------------------
// Keeper in targets rejected
// ---------------------------------------------------------------------------

func TestReplacementRefusesKeeperInTargets(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")

	keeper := modFamilyScanArchive(t, root, "self-keeper.zip", "Self Keeper", "Author", "2.0", "", "slk-sha", "slk-fp", 200, testArchiveModified(10), 0)
	items := applyLibraryArchives(t, service.store, root, []ScanArchive{keeper})
	keeperItem := itemByPath(t, items, keeper.ArchivePath)

	_, err := service.PlanModReplacement(keeperItem.EntityID, []string{keeperItem.EntityID})
	if err == nil {
		t.Fatal("should refuse keeper in targets")
	}
}

func TestReplacementInheritsEarliestSourcePosition(t *testing.T) {
	service := newTestAppService(t)
	items, originalCollection := scanAndCreateCollection(t, service, "Existing keeper position", 4, 9700)
	keeper, earliest, later, untouched := items[0], items[1], items[2], items[3]
	// Force the earliest-position source to be visited last by the canonical
	// ID order; the first source processed must not determine the final slot.
	if earliest.EntityID < later.EntityID {
		earliest, later = later, earliest
	}
	collection, err := service.CreateCollection("Inherited order", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(collection.Collection.ID, []string{earliest.EntityID, untouched.EntityID, later.EntityID}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionModsEnabled(collection.Collection.ID, []string{earliest.EntityID}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.db.Exec(`UPDATE collection_mods SET position=CASE entity_id WHEN ? THEN 0 WHEN ? THEN 1 ELSE 2 END WHERE collection_id=?`, earliest.EntityID, untouched.EntityID, collection.Collection.ID); err != nil {
		t.Fatal(err)
	}
	targets := []string{earliest.EntityID, later.EntityID}
	impact, err := service.PlanModReplacement(keeper.EntityID, targets)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ReplaceModArchives(keeper.EntityID, targets, impact.Fingerprint)
	if err != nil || result.Forgotten != 2 {
		t.Fatalf("replace: %#v, %v", result, err)
	}
	for _, collectionID := range []string{originalCollection, collection.Collection.ID} {
		detail, err := service.GetCollection(collectionID)
		if err != nil {
			t.Fatal(err)
		}
		if len(detail.Members) != 2 || detail.Members[0].EntityID != keeper.EntityID || detail.Members[1].EntityID != untouched.EntityID || !detail.Members[0].Enabled {
			t.Fatalf("replacement changed surrounding order or lost enablement: %#v", detail.Members)
		}
	}
}

func TestReplacementReviewDetectsGroupingChange(t *testing.T) {
	service := newTestAppService(t)
	items, _ := scanAndCreateCollection(t, service, "Grouping review", 2, 9710)
	organization, err := service.CreateModTag("Promoted after review", "#123456", "tag")
	if err != nil {
		t.Fatal(err)
	}
	tagID := findModTag(t, organization, "Promoted after review").ID
	if _, err := service.SetLibraryItemTags(items[1].EntityID, []string{tagID}); err != nil {
		t.Fatal(err)
	}
	impact, err := service.PlanModReplacement(items[0].EntityID, []string{items[1].EntityID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetTagGrouped(tagID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReplaceModArchives(items[0].EntityID, []string{items[1].EntityID}, impact.Fingerprint); err == nil {
		t.Fatal("changed group classification did not invalidate the review")
	}
	old, err := service.store.GetLibraryItem(context.Background(), items[1].EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if len(old.Tags) != 1 || old.Tags[0].ID != tagID || !old.Tags[0].Grouped {
		t.Fatal("stale review changed group assignments")
	}
	if _, err := os.Stat(old.ArchivePath); err != nil {
		t.Fatal("stale review recycled the archive")
	}
}

// ---------------------------------------------------------------------------
// Shared helper
// ---------------------------------------------------------------------------

func itemByPath(t *testing.T, items []LibraryItem, path string) LibraryItem {
	t.Helper()
	item, found := libraryItemByPath(items, path)
	if !found {
		t.Fatalf("no item with path %s in %d items", path, len(items))
	}
	return item
}
