package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

// Library mods arrive from a scan and have no ModMaker project; mods made with
// CreateNewMod always do, which is what the removal guard protects.
func scanFixtureMod(t *testing.T, service *AppService, title string) LibraryItem {
	t.Helper()
	root := filepath.Join(service.config.DataDir, "library")
	path := filepath.Join(root, strings.ToLower(strings.ReplaceAll(title, " ", "_"))+".zip")
	writeLibraryScanArchive(t, path, title)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	modified := info.ModTime().UTC()
	items := applyLibraryArchives(t, service.store, root, []ScanArchive{{
		Root:        root,
		ArchivePath: path,
		SizeBytes:   info.Size(),
		Modified:    modified,
		Manifest: modkit.Manifest{
			SchemaVersion:      modkit.SchemaVersion,
			AnalyzerVersion:    modkit.AnalyzerVersion,
			AnalyzedAt:         modified,
			ArchivePath:        path,
			Filename:           filepath.Base(path),
			SizeBytes:          info.Size(),
			ModifiedAt:         modified,
			CentralFingerprint: "removal-fixture-" + title,
			ValidArchive:       true,
			Kind:               modkit.KindVehicle,
			Title:              title,
			Author:             "Scan Fixture",
			Version:            "1.0",
			Namespaces:         map[string][]string{"vehicles": {strings.ToLower(strings.ReplaceAll(title, " ", "_"))}},
			Members:            []modkit.ArchiveMember{{Path: "vehicles/main.jbeam"}},
		},
	}})
	item, found := libraryItemByPath(items, path)
	if !found {
		t.Fatalf("the scan did not index %s", path)
	}
	return item
}

func TestForgetModLeavesTheArchiveOnDisk(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	item := scanFixtureMod(t, service, "Forget Me")
	collection, err := service.CreateCollection("Holds The Mod", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(collection.Collection.ID, []string{item.EntityID}, true); err != nil {
		t.Fatal(err)
	}

	impact, err := service.PlanModRemoval([]string{item.EntityID})
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.Mods) != 1 || impact.Mods[0].ArchivePath != item.ArchivePath || impact.ArchiveCount != 1 {
		t.Fatalf("impact = %#v", impact)
	}
	if len(impact.Collections) != 1 || impact.Collections[0] != "Holds The Mod" {
		t.Fatalf("the confirmation would not have mentioned the collection: %#v", impact.Collections)
	}

	result, err := service.ForgetMods([]string{item.EntityID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Forgotten != 1 || result.Recycled != 0 {
		t.Fatalf("forget result = %#v", result)
	}
	if _, err := os.Stat(item.ArchivePath); err != nil {
		t.Fatalf("forgetting removed the archive from disk: %v", err)
	}
	items, err := service.ListLibrary("", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, listed := range items {
		if listed.EntityID == item.EntityID {
			t.Fatal("the forgotten mod is still indexed")
		}
	}
	detail, err := service.GetCollection(collection.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Members) != 0 {
		t.Fatalf("the membership outlived the mod: %#v", detail.Members)
	}
}

func TestDeleteModArchiveRemovesTheFileAndTheIndexEntry(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	ctx := context.Background()
	item := scanFixtureMod(t, service, "Delete Me")

	result, err := service.DeleteModArchives([]string{item.EntityID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Forgotten != 1 || result.Recycled != 1 || len(result.Failures) != 0 {
		t.Fatalf("delete result = %#v", result)
	}
	if _, err := os.Stat(item.ArchivePath); !os.IsNotExist(err) {
		t.Fatalf("the archive survived the delete: %v", err)
	}
	if _, err := service.store.GetLibraryItem(ctx, item.EntityID); err == nil {
		t.Fatal("the mod is still indexed after its archive was deleted")
	}
}

func TestRemovalRefusesWhileAModMakerProjectExists(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	mod, err := service.CreateNewMod(NewModRequest{Name: "Has Project", ModID: "has_project", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	// A mod authored in ModMaker owns a workspace: unsaved user work that does
	// not live in the archive, so removal has to refuse rather than discard it.
	impact, err := service.PlanModRemoval([]string{mod.Entity.EntityID})
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.Workspaces) == 0 {
		t.Fatal("a ModMaker project was not reported in the removal impact")
	}
	if _, err := service.ForgetMods([]string{mod.Entity.EntityID}); err == nil {
		t.Fatal("a mod with an open project was forgotten")
	}
	if _, err := service.DeleteModArchives([]string{mod.Entity.EntityID}); err == nil {
		t.Fatal("a mod with an open project had its archive deleted")
	}
}

func TestDeleteKeepsTheIndexTruthfulWhenTheArchiveCannotBeRecycled(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	ctx := context.Background()
	item := scanFixtureMod(t, service, "Locked Archive")
	// Hold the archive open: Windows refuses to recycle a file in use.
	handle, err := os.Open(item.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()

	result, err := service.DeleteModArchives([]string{item.EntityID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Recycled != 0 || result.Forgotten != 0 || len(result.Failures) != 1 {
		t.Skipf("this platform allowed the open archive to be deleted: %#v", result)
	}
	if !strings.Contains(result.Failures[0], "Locked Archive") {
		t.Fatalf("the failure does not name the mod: %#v", result.Failures)
	}
	if _, err := service.store.GetLibraryItem(ctx, item.EntityID); err != nil {
		t.Fatal("the mod was dropped from the library even though its archive is still on disk")
	}
}

func TestForgetReportsAMissingArchiveRatherThanFailing(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	item := scanFixtureMod(t, service, "Vanished")
	if err := os.Remove(item.ArchivePath); err != nil {
		t.Fatal(err)
	}
	impact, err := service.PlanModRemoval([]string{item.EntityID})
	if err != nil {
		t.Fatal(err)
	}
	if !impact.Mods[0].Missing || impact.ArchiveCount != 0 {
		t.Fatalf("a deleted archive was not reported as missing: %#v", impact)
	}
	if _, err := os.Stat(filepath.Dir(item.ArchivePath)); err != nil {
		t.Fatal(err)
	}
}
