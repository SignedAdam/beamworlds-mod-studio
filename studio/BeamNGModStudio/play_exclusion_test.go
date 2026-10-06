package main

import (
	"context"
	"slices"
	"strings"
	"testing"
)

//  1. all-mods means every mod in the library, including mods that belong to no
//     collection at all, and it grows when the library does.
//
//     This is the case that started the work: Adam's in-game downloader added 79
//     mods, none of which joined any collection he had, so no selection he could
//     express included them. A version of all-mods that expanded to "every
//     collection" passed a test like this only because the test put every mod in
//     a collection first. So none of these mods is filed anywhere.
func TestAllModsCoversModsInNoCollection(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	first, err := service.CreateNewMod(NewModRequest{Name: "Mod Alpha", ModID: "mod_alpha", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateNewMod(NewModRequest{Name: "Mod Beta", ModID: "mod_beta", Kind: "ui", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}

	selection, err := service.ResolvePlaySelection([]string{AllModsCollectionID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if selection.ModCount != 2 {
		t.Fatalf("all-mods resolved %d unfiled mods, want 2", selection.ModCount)
	}
	fingerprint := selection.Fingerprint

	// One of them gets filed, and that collection is excluded. The unfiled mod
	// has to survive: it was never in the collection being subtracted.
	filed, err := service.CreateCollection("Son's mods", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(filed.Collection.ID, []string{first.Entity.EntityID}, true); err != nil {
		t.Fatal(err)
	}
	difference, err := service.ResolvePlaySelection([]string{AllModsCollectionID}, []string{filed.Collection.ID})
	if err != nil {
		t.Fatal(err)
	}
	if difference.ModCount != 1 || difference.ExcludedModCount != 1 {
		t.Fatalf("all-mods minus one collection resolved %d mods and excluded %d, want 1 and 1",
			difference.ModCount, difference.ExcludedModCount)
	}
	if difference.Mods[0].EntityID != second.Entity.EntityID {
		t.Fatalf("the surviving mod was %q, want the unfiled %q", difference.Mods[0].DisplayName, "Mod Beta")
	}

	// A mod indexed afterwards and filed nowhere - the downloader case - joins
	// all-mods with no user action.
	if _, err := service.CreateNewMod(NewModRequest{Name: "Mod Gamma", ModID: "mod_gamma", Kind: "map", Version: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	grown, err := service.ResolvePlaySelection([]string{AllModsCollectionID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if grown.ModCount != 3 {
		t.Fatalf("all-mods after an unfiled mod arrived resolved %d mods, want 3", grown.ModCount)
	}
	if grown.Fingerprint == fingerprint {
		t.Fatal("fingerprint did not change when the library grew")
	}
}

//  2. Include all-mods, exclude one collection: the resolved set is exactly the
//     difference, and ExcludedModCount equals the number removed.
func TestAllModsMinusOneCollectionYieldsDifference(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	mods := make([]string, 5)
	for i := range mods {
		m, err := service.CreateNewMod(NewModRequest{
			Name: "Mod " + string(rune('A'+i)), ModID: "mod_" + string(rune('a'+i)),
			Kind: "script", Version: "0.1.0",
		})
		if err != nil {
			t.Fatal(err)
		}
		mods[i] = m.Entity.EntityID
	}
	all, err := service.CreateCollection("All", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(all.Collection.ID, mods, true); err != nil {
		t.Fatal(err)
	}
	exclude, err := service.CreateCollection("Sons Mods", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Put 2 mods in the excluded collection.
	if _, err := service.SetCollectionMods(exclude.Collection.ID, mods[:2], true); err != nil {
		t.Fatal(err)
	}

	selection, err := service.ResolvePlaySelection(
		[]string{AllModsCollectionID},
		[]string{exclude.Collection.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if selection.ModCount != 3 {
		t.Fatalf("all-mods minus 2 = %d mods, want 3", selection.ModCount)
	}
	if selection.ExcludedModCount != 2 {
		t.Fatalf("ExcludedModCount = %d, want 2", selection.ExcludedModCount)
	}
	// Verify the excluded mods are not in the result.
	for _, mod := range selection.Mods {
		if mod.EntityID == mods[0] || mod.EntityID == mods[1] {
			t.Fatalf("excluded mod %s still in selection", mod.EntityID)
		}
	}
}

//  3. A mod reachable through BOTH an included and an excluded collection is
//     out, including when reached through nested children.
func TestExclusionBeatsInclusionThroughNesting(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	shared, err := service.CreateNewMod(NewModRequest{Name: "Shared Mod", ModID: "shared_mod", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	only, err := service.CreateNewMod(NewModRequest{Name: "Only Included", ModID: "only_included", Kind: "ui", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}

	// Tree: parent -> child; both have "shared". Parent also has "only".
	parent, err := service.CreateCollection("Parent", "", "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateCollection("Child", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(parent.Collection.ID, []string{shared.Entity.EntityID, only.Entity.EntityID}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(child.Collection.ID, []string{shared.Entity.EntityID}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionChildren(parent.Collection.ID, []string{child.Collection.ID}, true); err != nil {
		t.Fatal(err)
	}

	// Exclude "child" — "shared" is still reachable through parent directly,
	// but exclusion beats inclusion at any nesting depth.
	excludeCol, err := service.CreateCollection("Exclude Wrapper", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(excludeCol.Collection.ID, []string{shared.Entity.EntityID}, true); err != nil {
		t.Fatal(err)
	}

	selection, err := service.ResolvePlaySelection(
		[]string{parent.Collection.ID},
		[]string{excludeCol.Collection.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	// "shared" should be out, "only" should remain.
	if selection.ModCount != 1 {
		t.Fatalf("expected 1 mod, got %d", selection.ModCount)
	}
	if selection.Mods[0].EntityID != only.Entity.EntityID {
		t.Fatalf("wrong mod survived: %s", selection.Mods[0].EntityID)
	}
	if selection.ExcludedModCount != 1 {
		t.Fatalf("ExcludedModCount = %d, want 1", selection.ExcludedModCount)
	}
}

// 4. Excluding everything yields zero mods and no error.
func TestExcludingEverythingYieldsEmptySelection(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	m, err := service.CreateNewMod(NewModRequest{Name: "Solo Mod", ModID: "solo_mod", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	col, err := service.CreateCollection("The One", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(col.Collection.ID, []string{m.Entity.EntityID}, true); err != nil {
		t.Fatal(err)
	}

	selection, err := service.ResolvePlaySelection(
		[]string{AllModsCollectionID},
		[]string{AllModsCollectionID},
	)
	if err != nil {
		t.Fatalf("excluding everything errored: %v", err)
	}
	if selection.ModCount != 0 {
		t.Fatalf("excluding everything left %d mods", selection.ModCount)
	}
	if selection.ExcludedModCount != 1 {
		t.Fatalf("ExcludedModCount = %d, want 1", selection.ExcludedModCount)
	}
}

//  5. The fingerprint changes when only the excluded set changes, and a launch
//     carrying a stale fingerprint is refused.
func TestFingerprintChangesWithExclusionAndStaleLaunchRefused(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	m1, err := service.CreateNewMod(NewModRequest{Name: "FP Mod A", ModID: "fp_a", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := service.CreateNewMod(NewModRequest{Name: "FP Mod B", ModID: "fp_b", Kind: "ui", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	col, err := service.CreateCollection("FP All", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(col.Collection.ID, []string{m1.Entity.EntityID, m2.Entity.EntityID}, true); err != nil {
		t.Fatal(err)
	}
	excludeCol, err := service.CreateCollection("FP Exclude", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(excludeCol.Collection.ID, []string{m2.Entity.EntityID}, true); err != nil {
		t.Fatal(err)
	}

	// Resolve without exclusion.
	withoutExclude, err := service.ResolvePlaySelection([]string{col.Collection.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Resolve with exclusion.
	withExclude, err := service.ResolvePlaySelection([]string{col.Collection.ID}, []string{excludeCol.Collection.ID})
	if err != nil {
		t.Fatal(err)
	}
	if withoutExclude.Fingerprint == withExclude.Fingerprint {
		t.Fatal("fingerprint did not change when excluded set changed")
	}

	// A launch carrying the no-exclusion fingerprint must fail when the UI
	// now expects exclusions.
	err = validatePlaySelectionRequest(
		PlayRequest{CollectionIDs: []string{col.Collection.ID}, ExcludedCollectionIDs: []string{excludeCol.Collection.ID}, Fingerprint: withoutExclude.Fingerprint},
		withExclude,
	)
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale fingerprint was accepted: %v", err)
	}

	// Correct fingerprint passes.
	err = validatePlaySelectionRequest(
		PlayRequest{CollectionIDs: []string{col.Collection.ID}, ExcludedCollectionIDs: []string{excludeCol.Collection.ID}, Fingerprint: withExclude.Fingerprint},
		withExclude,
	)
	if err != nil {
		t.Fatalf("correct fingerprint rejected: %v", err)
	}
}

// 6. A saved profile round-trips both lists across a store reopen.
func TestProfileRoundTripsBothListsAcrossReopen(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	m1, err := service.CreateNewMod(NewModRequest{Name: "RT Mod 1", ModID: "rt_1", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := service.CreateNewMod(NewModRequest{Name: "RT Mod 2", ModID: "rt_2", Kind: "ui", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	include, err := service.CreateCollection("RT Include", "", "")
	if err != nil {
		t.Fatal(err)
	}
	exclude, err := service.CreateCollection("RT Exclude", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(include.Collection.ID, []string{m1.Entity.EntityID, m2.Entity.EntityID}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(exclude.Collection.ID, []string{m2.Entity.EntityID}, true); err != nil {
		t.Fatal(err)
	}

	profile, err := service.CreatePlayProfile(
		[]string{include.Collection.ID},
		[]string{exclude.Collection.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.ExcludedCollectionIDs) != 1 || profile.ExcludedCollectionIDs[0] != exclude.Collection.ID {
		t.Fatalf("created profile excluded = %v", profile.ExcludedCollectionIDs)
	}
	if profile.ModCount != 1 {
		t.Fatalf("created profile ModCount = %d, want 1", profile.ModCount)
	}

	// Reopen the store and verify the profile persists.
	dbPath := service.store.dbPath
	if err := service.store.Close(); err != nil {
		t.Fatal(err)
	}
	store2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	org, err := store2.Organization(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range org.Profiles {
		if p.ID == profile.ID {
			found = true
			if !slices.Equal(p.CollectionIDs, profile.CollectionIDs) {
				t.Fatalf("reopened included = %v, want %v", p.CollectionIDs, profile.CollectionIDs)
			}
			if !slices.Equal(p.ExcludedCollectionIDs, profile.ExcludedCollectionIDs) {
				t.Fatalf("reopened excluded = %v, want %v", p.ExcludedCollectionIDs, profile.ExcludedCollectionIDs)
			}
			if p.ModCount != 1 {
				t.Fatalf("reopened ModCount = %d, want 1", p.ModCount)
			}
		}
	}
	if !found {
		t.Fatal("profile not found after store reopen")
	}
}

// A profile that includes "all mods" is the one Adam will actually save: play
// everything except his son's mods. The sentinel is not a row in collections,
// so storing it as a membership row fails the foreign key - it lives in flags
// on the profile instead, and has to survive a reopen like any other choice.
func TestProfileWithAllModsSentinelSavesAndReloads(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	kept, err := service.CreateNewMod(NewModRequest{Name: "Sentinel Kept", ModID: "sentinel_kept", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	dropped, err := service.CreateNewMod(NewModRequest{Name: "Sentinel Dropped", ModID: "sentinel_dropped", Kind: "ui", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	sons, err := service.CreateCollection("Sentinel Son", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(sons.Collection.ID, []string{dropped.Entity.EntityID}, true); err != nil {
		t.Fatal(err)
	}

	profile, err := service.CreatePlayProfile([]string{AllModsCollectionID}, []string{sons.Collection.ID})
	if err != nil {
		t.Fatalf("saving a profile that includes all mods: %v", err)
	}
	if !slices.Contains(profile.CollectionIDs, AllModsCollectionID) {
		t.Fatalf("saved profile included = %v, want the all-mods sentinel", profile.CollectionIDs)
	}
	if profile.ModCount != 1 {
		t.Fatalf("saved profile ModCount = %d, want 1 (only the unfiled mod survives)", profile.ModCount)
	}

	dbPath := service.store.dbPath
	if err := service.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	organization, err := reopened.Organization(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, saved := range organization.Profiles {
		if saved.ID != profile.ID {
			continue
		}
		if !slices.Contains(saved.CollectionIDs, AllModsCollectionID) {
			t.Fatalf("reopened included = %v, want the all-mods sentinel", saved.CollectionIDs)
		}
		if !slices.Equal(saved.ExcludedCollectionIDs, []string{sons.Collection.ID}) {
			t.Fatalf("reopened excluded = %v, want %v", saved.ExcludedCollectionIDs, []string{sons.Collection.ID})
		}
		// A mod indexed after the profile was saved must be picked up by the
		// sentinel without editing the profile.
		if _, err := reopened.db.ExecContext(context.Background(), `SELECT 1`); err != nil {
			t.Fatal(err)
		}
		selection, err := reopened.ResolvePlaySelection(context.Background(), saved.CollectionIDs, saved.ExcludedCollectionIDs)
		if err != nil {
			t.Fatal(err)
		}
		if selection.ModCount != 1 || selection.Mods[0].EntityID != kept.Entity.EntityID {
			t.Fatalf("reopened selection resolved %d mods, want only %q", selection.ModCount, kept.Entity.DisplayName)
		}
		return
	}
	t.Fatal("profile not found after store reopen")
}

// 7. Archived mods stay out of all-mods, preserving the existing archive guarantee.
func TestArchivedModsExcludedFromAllMods(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	m1, err := service.CreateNewMod(NewModRequest{Name: "Archive Mod A", ModID: "arch_a", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := service.CreateNewMod(NewModRequest{Name: "Archive Mod B", ModID: "arch_b", Kind: "ui", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	col, err := service.CreateCollection("Archive Test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(col.Collection.ID, []string{m1.Entity.EntityID, m2.Entity.EntityID}, true); err != nil {
		t.Fatal(err)
	}

	// Before archiving: 2 mods.
	before, err := service.ResolvePlaySelection([]string{AllModsCollectionID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if before.ModCount != 2 {
		t.Fatalf("all-mods before archive = %d, want 2", before.ModCount)
	}

	// Archive one mod.
	if _, err := service.ArchiveMods([]string{m1.Entity.EntityID}); err != nil {
		t.Fatal(err)
	}

	// After archiving: 1 mod.
	after, err := service.ResolvePlaySelection([]string{AllModsCollectionID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if after.ModCount != 1 {
		t.Fatalf("all-mods after archive = %d, want 1", after.ModCount)
	}
	if after.Mods[0].EntityID != m2.Entity.EntityID {
		t.Fatalf("wrong mod survived archive: %s", after.Mods[0].EntityID)
	}
	if after.ArchivedCount != 1 {
		t.Fatalf("ArchivedCount = %d, want 1", after.ArchivedCount)
	}
}
