package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func selectionEntityIDs(t *testing.T, service *AppService, roots ...string) []string {
	t.Helper()
	selection, err := service.ResolvePlaySelection(roots)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(selection.Mods))
	for _, mod := range selection.Mods {
		ids = append(ids, mod.EntityID)
	}
	slices.Sort(ids)
	return ids
}

func TestDisabledMembershipStopsShippingButKeepsMembership(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	kept, err := service.CreateNewMod(NewModRequest{Name: "Enable Kept", ModID: "enable_kept", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	parked, err := service.CreateNewMod(NewModRequest{Name: "Enable Parked", ModID: "enable_parked", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	collection, err := service.CreateCollection("Garage", "", "")
	if err != nil {
		t.Fatal(err)
	}
	detail, err := service.SetCollectionMods(collection.Collection.ID, []string{kept.Entity.EntityID, parked.Entity.EntityID}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Members) != 2 || !detail.Members[0].Enabled || !detail.Members[1].Enabled {
		t.Fatalf("new members are not enabled by default: %#v", detail.Members)
	}
	if detail.Collection.DirectEnabledCount != 2 || detail.Collection.ModCount != 2 {
		t.Fatalf("counts before disabling = %#v", detail.Collection)
	}
	before, err := service.ResolvePlaySelection([]string{collection.Collection.ID})
	if err != nil {
		t.Fatal(err)
	}

	disabled, err := service.SetCollectionModsEnabled(collection.Collection.ID, []string{parked.Entity.EntityID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled.Members) != 2 {
		t.Fatalf("disabling dropped the membership: %#v", disabled.Members)
	}
	if disabled.Collection.DirectModCount != 2 || disabled.Collection.DirectEnabledCount != 1 || disabled.Collection.ModCount != 1 {
		t.Fatalf("counts after disabling = %#v", disabled.Collection)
	}
	if got := selectionEntityIDs(t, service, collection.Collection.ID); len(got) != 1 || got[0] != kept.Entity.EntityID {
		t.Fatalf("disabled mod still ships: %#v", got)
	}
	after, err := service.ResolvePlaySelection([]string{collection.Collection.ID})
	if err != nil {
		t.Fatal(err)
	}
	if after.Fingerprint == before.Fingerprint {
		t.Fatal("the selection fingerprint survived an enabled-flag change")
	}
	if _, err := service.LaunchPlaySelection(PlayRequest{CollectionIDs: []string{collection.Collection.ID}, Fingerprint: before.Fingerprint}); err == nil {
		t.Fatal("a Play request carrying the pre-toggle fingerprint was accepted")
	}

	if _, err := service.SetCollectionModsEnabled(collection.Collection.ID, []string{parked.Entity.EntityID}, true); err != nil {
		t.Fatal(err)
	}
	restored, err := service.ResolvePlaySelection([]string{collection.Collection.ID})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Fingerprint != before.Fingerprint {
		t.Fatal("re-enabling did not restore the original contribution")
	}
}

func TestEnabledMembershipWinsAcrossCollections(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	shared, err := service.CreateNewMod(NewModRequest{Name: "Shared Mod", ModID: "shared_mod", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	enabledIn, err := service.CreateCollection("Enabled Here", "", "")
	if err != nil {
		t.Fatal(err)
	}
	disabledIn, err := service.CreateCollection("Disabled Here", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, collectionID := range []string{enabledIn.Collection.ID, disabledIn.Collection.ID} {
		if _, err := service.SetCollectionMods(collectionID, []string{shared.Entity.EntityID}, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.SetCollectionModsEnabled(disabledIn.Collection.ID, []string{shared.Entity.EntityID}, false); err != nil {
		t.Fatal(err)
	}
	selection, err := service.ResolvePlaySelection([]string{enabledIn.Collection.ID, disabledIn.Collection.ID})
	if err != nil {
		t.Fatal(err)
	}
	if selection.ModCount != 1 {
		t.Fatalf("a disabled membership vetoed an enabled one: %#v", selection.Mods)
	}
	provenance := selection.Mods[0].CollectionIDs
	if len(provenance) != 1 || provenance[0] != enabledIn.Collection.ID {
		t.Fatalf("provenance did not name the contributing collection: %#v", provenance)
	}
	if got := selectionEntityIDs(t, service, disabledIn.Collection.ID); len(got) != 0 {
		t.Fatalf("the disabling collection still ships the mod on its own: %#v", got)
	}
}

func TestDisabledChildEdgeIsNotTraversed(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	parentMod, err := service.CreateNewMod(NewModRequest{Name: "Parent Mod", ModID: "parent_mod", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	childMod, err := service.CreateNewMod(NewModRequest{Name: "Child Mod", ModID: "child_mod", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	grandchildMod, err := service.CreateNewMod(NewModRequest{Name: "Grandchild Mod", ModID: "grandchild_mod", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := service.CreateCollection("Parent", "", "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateCollection("Child", "", "")
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := service.CreateCollection("Grandchild", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for collectionID, entityID := range map[string]string{
		parent.Collection.ID:     parentMod.Entity.EntityID,
		child.Collection.ID:      childMod.Entity.EntityID,
		grandchild.Collection.ID: grandchildMod.Entity.EntityID,
	} {
		if _, err := service.SetCollectionMods(collectionID, []string{entityID}, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.SetCollectionChildren(parent.Collection.ID, []string{child.Collection.ID}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionChildren(child.Collection.ID, []string{grandchild.Collection.ID}, true); err != nil {
		t.Fatal(err)
	}
	if got := selectionEntityIDs(t, service, parent.Collection.ID); len(got) != 3 {
		t.Fatalf("nested selection before disabling = %#v", got)
	}

	detail, err := service.SetCollectionChildrenEnabled(parent.Collection.ID, []string{child.Collection.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Children) != 1 || detail.Children[0].Enabled {
		t.Fatalf("child edge state = %#v", detail.Children)
	}
	got := selectionEntityIDs(t, service, parent.Collection.ID)
	if len(got) != 1 || got[0] != parentMod.Entity.EntityID {
		t.Fatalf("a disabled child edge still contributed: %#v", got)
	}
	// Enabled wins: the same child named directly still ships, descendants
	// included, because the disabled flag belongs to that one edge.
	direct := selectionEntityIDs(t, service, parent.Collection.ID, child.Collection.ID)
	if len(direct) != 3 {
		t.Fatalf("directly selected child did not contribute: %#v", direct)
	}
}

func TestFirstScanSeedsDefaultProfileFromEnabledMods(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	ctx := context.Background()
	enabled, err := service.CreateNewMod(NewModRequest{Name: "Seed Enabled", ModID: "seed_enabled", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateNewMod(NewModRequest{Name: "Seed Disabled", ModID: "seed_disabled", Kind: "script", Version: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	item, err := service.store.GetLibraryItem(ctx, enabled.Entity.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	database, err := json.Marshal(map[string]any{"header": map[string]any{"version": 1.1}, "mods": map[string]any{
		"seed-enabled": map[string]any{"active": true, "filename": filepath.Base(item.ArchivePath), "fullpath": "/mods/"},
		"seed-missing": map[string]any{"active": true, "filename": "not-in-library.zip", "fullpath": "/mods/"},
		"seed-off":     map[string]any{"active": false, "filename": "irrelevant.zip", "fullpath": "/mods/"},
		// BeamWorlds' own throwaway install: never the user's mod, and its
		// archive is gone, so it must not be seeded or reported.
		"modstudio-test-01a05af2": map[string]any{"active": true, "filename": "modstudio-test-01a05af2.zip", "fullpath": "/mods/"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.config.ActiveModsDir, "db.json"), database, 0o644); err != nil {
		t.Fatal(err)
	}

	service.seedDefaultPlayProfile(ctx)

	organization, err := service.Organization()
	if err != nil {
		t.Fatal(err)
	}
	if len(organization.Collections) != 1 || len(organization.Profiles) != 1 {
		t.Fatalf("seeding produced %d collections and %d profiles", len(organization.Collections), len(organization.Profiles))
	}
	seededCollection := organization.Collections[0]
	if seededCollection.ModCount != 1 || seededCollection.DirectEnabledCount != 1 {
		t.Fatalf("seeded collection = %#v", seededCollection)
	}
	profile := organization.Profiles[0]
	if len(profile.CollectionIDs) != 1 || profile.CollectionIDs[0] != seededCollection.ID {
		t.Fatalf("seeded profile does not hold the seeded collection: %#v", profile)
	}
	state, err := service.GetPlayState()
	if err != nil {
		t.Fatal(err)
	}
	if state.ProfileID != profile.ID || len(state.CollectionIDs) != 1 || state.CollectionIDs[0] != seededCollection.ID {
		t.Fatalf("seeded profile was not selected: %#v", state)
	}
	// The notice has to name the archive and say nothing about BeamWorlds' own
	// test install, which was filtered out before matching.
	if len(state.Notices) != 1 || !strings.Contains(state.Notices[0], "not-in-library.zip") {
		t.Fatalf("notices = %#v", state.Notices)
	}
	if strings.Contains(state.Notices[0], "modstudio-test") {
		t.Fatalf("a BeamWorlds test install was reported to the user: %q", state.Notices[0])
	}
	selection, err := service.ResolvePlaySelection(state.CollectionIDs)
	if err != nil {
		t.Fatal(err)
	}
	if selection.ModCount != 1 || selection.Mods[0].EntityID != enabled.Entity.EntityID {
		t.Fatalf("seeded selection = %#v", selection.Mods)
	}

	// Seeding is one-shot: deleting the seeded work must not resurrect it.
	if _, err := service.DeletePlayProfile(profile.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteCollections([]string{seededCollection.ID}); err != nil {
		t.Fatal(err)
	}
	service.seedDefaultPlayProfile(ctx)
	organization, err = service.Organization()
	if err != nil {
		t.Fatal(err)
	}
	if len(organization.Collections) != 0 || len(organization.Profiles) != 0 {
		t.Fatalf("seeding ran a second time: %d collections, %d profiles", len(organization.Collections), len(organization.Profiles))
	}
}

func TestSeedingReadsSnapshotWhenLiveDatabaseHasNothingActive(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	ctx := context.Background()
	mod, err := service.CreateNewMod(NewModRequest{Name: "Snapshot Mod", ModID: "snapshot_mod", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.store.GetLibraryItem(ctx, mod.Entity.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Base(item.ArchivePath)
	database := func(active bool) []byte {
		payload, marshalErr := json.Marshal(map[string]any{"header": map[string]any{"version": 1.1}, "mods": map[string]any{
			"snapshot-mod": map[string]any{"active": active, "filename": filename, "fullpath": "/mods/"},
		}})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return payload
	}
	// An earlier apply left the live database with everything switched off,
	// which is exactly the state that must not be mistaken for "the user
	// enables nothing".
	if err := os.WriteFile(filepath.Join(service.config.ActiveModsDir, "db.json"), database(false), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.config.ActiveModsDir, "db.json.beamworlds-original"), database(true), 0o644); err != nil {
		t.Fatal(err)
	}

	service.seedDefaultPlayProfile(ctx)

	organization, err := service.Organization()
	if err != nil {
		t.Fatal(err)
	}
	if len(organization.Collections) != 1 || organization.Collections[0].ModCount != 1 {
		t.Fatalf("the snapshot was not used to seed: %#v", organization.Collections)
	}
}
