package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

// scanFolderModAndCreateCollection creates a real folder mod in
// <DataDir>/unpacked/<name>, indexes it via the library scan pipeline, creates
// a collection containing it, and returns the library item and collection ID.
func scanFolderModAndCreateCollection(t *testing.T, service *AppService, name string, serial int) (LibraryItem, string) {
	t.Helper()
	root := filepath.Join(service.config.DataDir, "unpacked")
	folderPath := filepath.Join(root, name)
	if err := os.MkdirAll(folderPath, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a few files so the folder is clearly a mod.
	for _, f := range []string{"info.json", "vehicles/mycar.jbeam", "textures/body.png"} {
		p := filepath.Join(folderPath, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("content-"+f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Insert into library as a folder source.
	fixtures := libraryFixtureArchives(root, 1, serial)
	fixtures[0].ArchivePath = folderPath
	fixtures[0].Manifest.ArchivePath = folderPath
	fixtures[0].Manifest.Filename = name
	fixtures[0].Manifest.Title = name
	items := applyLibraryArchives(t, service.store, root, fixtures)
	if len(items) == 0 {
		t.Fatalf("scan produced no items for folder mod %s", name)
	}
	collection, err := service.CreateCollection(name, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(collection.Collection.ID, []string{items[0].EntityID}, true); err != nil {
		t.Fatal(err)
	}
	return items[0], collection.Collection.ID
}

// TestFolderModDeployCreatesJunction verifies that deploying a folder mod
// creates a junction at <play mods>/unpacked/<label>-<entityID>.
func TestFolderModDeployCreatesJunction(t *testing.T) {
	service := newTestAppService(t)
	service.config.LibraryDir = filepath.Join(service.config.DataDir, "library")
	item, collectionID := scanFolderModAndCreateCollection(t, service, "MyCoolMod", 8000)
	ctx := context.Background()
	request := reviewedStoragePlay(t, service, collectionID)
	if _, err := service.activatePlaySelectionDirect(ctx, request); err != nil {
		t.Fatal(err)
	}

	// Check that a junction was created in the unpacked subdirectory.
	playRoot, err := playUserPath(service.config)
	if err != nil {
		t.Fatal(err)
	}
	modsDir := playProfileModsDir(playRoot)
	unpackedDir := filepath.Join(modsDir, "unpacked")
	entries, err := os.ReadDir(unpackedDir)
	if err != nil {
		t.Fatalf("unpacked dir missing: %v", err)
	}
	found := false
	for _, e := range entries {
		if strings.Contains(e.Name(), item.EntityID) {
			jPath := filepath.Join(unpackedDir, e.Name())
			if !isDirectoryJunction(jPath) {
				t.Fatalf("expected junction at %s, got regular entry", jPath)
			}
			target := junctionTarget(jPath)
			if !samePath(target, item.ArchivePath) {
				t.Fatalf("junction target = %s, want %s", target, item.ArchivePath)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no junction found for entity %s in %s", item.EntityID, unpackedDir)
	}

	// Verify the junction works: files from the real mod folder are visible.
	for _, e := range entries {
		if strings.Contains(e.Name(), item.EntityID) {
			jPath := filepath.Join(unpackedDir, e.Name())
			if _, err := os.Stat(filepath.Join(jPath, "info.json")); err != nil {
				t.Fatalf("junction does not expose mod files: %v", err)
			}
		}
	}
}

// TestFolderModDeployReusesJunction verifies that a second deployment with the
// same selection reuses the existing junction without removing it.
func TestFolderModDeployReusesJunction(t *testing.T) {
	service := newTestAppService(t)
	service.config.LibraryDir = filepath.Join(service.config.DataDir, "library")
	_, collectionID := scanFolderModAndCreateCollection(t, service, "ReuseMod", 8010)
	ctx := context.Background()

	// First deployment.
	request1 := reviewedStoragePlay(t, service, collectionID)
	if _, err := service.activatePlaySelectionDirect(ctx, request1); err != nil {
		t.Fatal(err)
	}
	// Record the junction path.
	playRoot, _ := playUserPath(service.config)
	unpackedDir := filepath.Join(playProfileModsDir(playRoot), "unpacked")
	entries1, _ := os.ReadDir(unpackedDir)
	if len(entries1) != 1 {
		t.Fatalf("expected 1 junction, got %d", len(entries1))
	}
	jPath := filepath.Join(unpackedDir, entries1[0].Name())

	// Second deployment: should reuse the junction.
	request2 := reviewedStoragePlay(t, service, collectionID)
	result, err := service.activatePlaySelectionDirect(ctx, request2)
	if err != nil {
		t.Fatal(err)
	}
	_ = result
	// Junction should still exist at the same path.
	if !isDirectoryJunction(jPath) {
		t.Fatalf("junction was removed instead of reused: %s", jPath)
	}
}

// TestFolderModRetirePreservesRealFolder verifies that deselecting/retiring a
// deployed folder mod removes only the junction, leaving every file in the
// real mod folder intact.
func TestFolderModRetirePreservesRealFolder(t *testing.T) {
	service := newTestAppService(t)
	service.config.LibraryDir = filepath.Join(service.config.DataDir, "library")
	item, collectionID := scanFolderModAndCreateCollection(t, service, "RetireMod", 8020)
	ctx := context.Background()

	// Deploy.
	request := reviewedStoragePlay(t, service, collectionID)
	if _, err := service.activatePlaySelectionDirect(ctx, request); err != nil {
		t.Fatal(err)
	}
	playRoot, _ := playUserPath(service.config)
	unpackedDir := filepath.Join(playProfileModsDir(playRoot), "unpacked")

	// Record original file content hashes.
	type fileRecord struct {
		size    int64
		content string
	}
	originalFiles := map[string]fileRecord{}
	_ = filepath.WalkDir(item.ArchivePath, func(path string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			rel, _ := filepath.Rel(item.ArchivePath, path)
			data, _ := os.ReadFile(path)
			originalFiles[rel] = fileRecord{size: int64(len(data)), content: string(data)}
		}
		return nil
	})

	// Retire: remove the mod from the collection, then apply empty selection.
	if _, err := service.SetCollectionMods(collectionID, []string{item.EntityID}, false); err != nil {
		t.Fatal(err)
	}
	request2 := reviewedStoragePlay(t, service, collectionID)
	if _, err := service.activatePlaySelectionDirect(ctx, request2); err != nil {
		t.Fatal(err)
	}

	// The junction should be gone.
	entries, _ := os.ReadDir(unpackedDir)
	for _, e := range entries {
		jPath := filepath.Join(unpackedDir, e.Name())
		if isDirectoryJunction(jPath) {
			t.Fatalf("junction not removed after deselection: %s", jPath)
		}
	}

	// Every file in the real mod folder must be byte-identical.
	for rel, orig := range originalFiles {
		data, err := os.ReadFile(filepath.Join(item.ArchivePath, rel))
		if err != nil {
			t.Fatalf("real mod file deleted: %s: %v", rel, err)
		}
		if string(data) != orig.content {
			t.Fatalf("real mod file changed: %s", rel)
		}
	}
}

// TestOwnershipCheckAllowsOwnedJunctions verifies that checkManagedRootOwnership
// does not block on junctions that are tracked in the ownership ledger.
func TestOwnershipCheckAllowsOwnedJunctions(t *testing.T) {
	service := newTestAppService(t)
	service.config.LibraryDir = filepath.Join(service.config.DataDir, "library")
	_, collectionID := scanFolderModAndCreateCollection(t, service, "OwnedJunction", 8030)
	ctx := context.Background()

	// Deploy to create the junction.
	request := reviewedStoragePlay(t, service, collectionID)
	if _, err := service.activatePlaySelectionDirect(ctx, request); err != nil {
		t.Fatal(err)
	}

	// checkManagedRootOwnership should return no blockers.
	playRoot, _ := playUserPath(service.config)
	managedRoot := playProfileModsDir(playRoot)
	blockers := checkManagedRootOwnership(ctx, service.store, managedRoot)
	if len(blockers) > 0 {
		t.Fatalf("owned junction blocked Play: %v", blockers)
	}
}

// TestUnpackedFolderHarvestedToActiveModsDir verifies that real (non-junction)
// folders left in <play mods>/unpacked/ after a Play session are harvested to
// <ActiveModsDir>/unpacked/.
func TestUnpackedFolderHarvestedToActiveModsDir(t *testing.T) {
	service := newTestAppService(t)
	service.config.LibraryDir = filepath.Join(service.config.DataDir, "library")
	ctx := context.Background()
	playRoot, err := playUserPath(service.config)
	if err != nil {
		t.Fatal(err)
	}
	profileMods := playProfileModsDir(playRoot)
	unpackedDir := filepath.Join(profileMods, "unpacked")
	if err := os.MkdirAll(unpackedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a real folder mod (as if BeamNG unpacked it during a session).
	sessionFolder := filepath.Join(unpackedDir, "some_mod")
	if err := os.MkdirAll(filepath.Join(sessionFolder, "vehicles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionFolder, "info.json"), []byte(`{"name":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionFolder, "vehicles", "car.jbeam"), []byte(`{"car":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Harvest.
	service.modImportMu.Lock()
	harvested, harvestErr := service.reconcileProfileSessionDownloads(ctx, playRoot)
	service.modImportMu.Unlock()
	if harvestErr != nil {
		t.Fatal(harvestErr)
	}
	if harvested < 1 {
		t.Fatalf("harvested = %d, want >= 1", harvested)
	}

	// Verify the folder was moved to <ActiveModsDir>/unpacked/.
	dest := filepath.Join(service.config.ActiveModsDir, "unpacked", "some_mod")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("harvested folder not at expected destination: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "info.json"))
	if err != nil || string(data) != `{"name":"test"}` {
		t.Fatalf("harvested folder content mismatch: %v", err)
	}

	// Source should be gone.
	if _, err := os.Stat(sessionFolder); !os.IsNotExist(err) {
		t.Fatalf("source folder still exists after harvest: %v", err)
	}
}

// TestJunctionIsNotAFolderModSource ensures a junction (a Play link) is never
// mistaken for an unpacked mod and linked again.
func TestJunctionIsNotAFolderModSource(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "junction")
	if err := createDirectoryJunction(link, target); err != nil {
		t.Skipf("junctions not supported: %v", err)
	}
	if kind, err := modkit.SourceKindOf(link); err == nil && kind == modkit.SourceFolder {
		t.Fatal("a junction must not be treated as a folder mod")
	}
}
