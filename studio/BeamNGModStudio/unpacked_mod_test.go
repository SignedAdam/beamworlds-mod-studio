package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// writeTestFolderMod creates a folder mod with an info.json and a vehicle file
// so modkit.Inspect produces a valid manifest.
func writeTestFolderMod(t *testing.T, dir, title string) {
	t.Helper()
	modInfo := filepath.Join(dir, "mod_info")
	if err := os.MkdirAll(modInfo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modInfo, "info.json"),
		[]byte(`{"title":"`+title+`","author":"Test","version":"1.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	vehicles := filepath.Join(dir, "vehicles", "test")
	if err := os.MkdirAll(vehicles, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vehicles, "main.jbeam"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newUnpackedTestService(t *testing.T) (*AppService, AppConfig) {
	t.Helper()
	dataDir := t.TempDir()
	gameDir := t.TempDir()
	beamNGRoot := filepath.Join(gameDir, "beamng")
	activeModsDir := filepath.Join(beamNGRoot, "current", "mods")
	libraryDir := filepath.Join(gameDir, "library")
	config := AppConfig{
		SetupComplete:   true,
		BeamNGRoot:      beamNGRoot,
		DataDir:         dataDir,
		DatabasePath:    filepath.Join(dataDir, "modstudio.sqlite"),
		ImageCacheDir:   filepath.Join(dataDir, "cache", "images"),
		WorkspaceDir:    filepath.Join(dataDir, "workspaces"),
		ExportDir:       filepath.Join(dataDir, "exports"),
		ProfileDir:      filepath.Join(dataDir, "profiles"),
		ActiveModsDir:   activeModsDir,
		TestInstallDir:  activeModsDir,
		LibraryDir:      libraryDir,
		ScanRoots:       []string{activeModsDir, libraryDir},
		ScanConcurrency: 1,
	}
	for _, d := range []string{config.BeamNGRoot, config.ImageCacheDir, config.WorkspaceDir,
		config.ExportDir, config.ProfileDir, config.ActiveModsDir, config.LibraryDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store, err := OpenStore(config.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := NewAppService(config, store, func(string, any) {})
	service.gameRunning = func() (bool, error) { return false, nil }
	return service, config
}

// ---------------------------------------------------------------------------
// Discovery tests
// ---------------------------------------------------------------------------

func TestDiscoveryFindsFolderModsInUnpackedDirectories(t *testing.T) {
	t.Parallel()
	service, config := newUnpackedTestService(t)

	// Create folder mods under both mods/unpacked and LibraryDir/unpacked.
	modA := filepath.Join(config.ActiveModsDir, "unpacked", "modA")
	writeTestFolderMod(t, modA, "Mod A")

	modB := filepath.Join(config.LibraryDir, "unpacked", "modB")
	writeTestFolderMod(t, modB, "Mod B")

	summary, err := service.ScanLibrary()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if summary.Discovered != 2 {
		t.Fatalf("discovered = %d, want 2", summary.Discovered)
	}

	items, err := service.store.listItems(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	names := map[string]bool{}
	for _, item := range items {
		names[item.DisplayName] = true
		if item.SourceKind != "folder" {
			t.Errorf("item %q has SourceKind %q, want folder", item.DisplayName, item.SourceKind)
		}
	}
	if !names["Mod A"] || !names["Mod B"] {
		t.Errorf("expected Mod A and Mod B, got %v", names)
	}
}

func TestDiscoveryIgnoresZIPInsideFolderMod(t *testing.T) {
	t.Parallel()
	service, config := newUnpackedTestService(t)

	modA := filepath.Join(config.ActiveModsDir, "unpacked", "modA")
	writeTestFolderMod(t, modA, "Mod A")

	// Put a ZIP inside the folder mod — it should NOT be discovered as a separate mod.
	writeTestModZip(t, filepath.Join(modA, "nested.zip"), "Nested ZIP")

	summary, err := service.ScanLibrary()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if summary.Discovered != 1 {
		t.Fatalf("discovered = %d, want 1 (ZIP inside folder mod should not be separate)", summary.Discovered)
	}
}

func TestDiscoveryIgnoresFilesDirectlyInUnpacked(t *testing.T) {
	t.Parallel()
	service, config := newUnpackedTestService(t)

	unpackedDir := filepath.Join(config.ActiveModsDir, "unpacked")
	if err := os.MkdirAll(unpackedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A loose file directly in "unpacked" should be ignored.
	if err := os.WriteFile(filepath.Join(unpackedDir, "readme.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Also a ZIP directly in "unpacked" (not a child directory).
	writeTestModZip(t, filepath.Join(unpackedDir, "direct.zip"), "Direct ZIP")

	summary, err := service.ScanLibrary()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if summary.Discovered != 0 {
		t.Fatalf("discovered = %d, want 0 (files directly in unpacked should be ignored)", summary.Discovered)
	}
}

func TestDiscoveryIgnoresUnpackedInsideBeamWorldsPlay(t *testing.T) {
	t.Parallel()
	service, config := newUnpackedTestService(t)

	// Create a .beamworlds-play directory with an "unpacked" inside.
	playUnpacked := filepath.Join(config.ActiveModsDir, ".beamworlds-play", "mods", "unpacked", "someMod")
	writeTestFolderMod(t, playUnpacked, "Play Mod")

	summary, err := service.ScanLibrary()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if summary.Discovered != 0 {
		t.Fatalf("discovered = %d, want 0 (.beamworlds-play/unpacked should be skipped)", summary.Discovered)
	}
}

// ---------------------------------------------------------------------------
// Scan cache tests
// ---------------------------------------------------------------------------

func TestRescanReusesUnchangedFolderModAnalysis(t *testing.T) {
	t.Parallel()
	service, config := newUnpackedTestService(t)

	modDir := filepath.Join(config.ActiveModsDir, "unpacked", "cached_mod")
	writeTestFolderMod(t, modDir, "Cached Mod")

	summary1, err := service.ScanLibrary()
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if summary1.Cached != 0 {
		t.Fatalf("first scan cached = %d, want 0", summary1.Cached)
	}

	// Second scan with no changes: should reuse the analysis.
	summary2, err := service.ScanLibrary()
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if summary2.Cached != 1 {
		t.Fatalf("second scan cached = %d, want 1 (unchanged folder should be reused)", summary2.Cached)
	}
}

func TestRescanReInspectsChangedFolderMod(t *testing.T) {
	t.Parallel()
	service, config := newUnpackedTestService(t)

	modDir := filepath.Join(config.ActiveModsDir, "unpacked", "changing_mod")
	writeTestFolderMod(t, modDir, "Changing Mod")

	_, err := service.ScanLibrary()
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}

	// Touch a file to change the listing fingerprint.
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(modDir, "new_file.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}

	summary2, err := service.ScanLibrary()
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if summary2.Cached != 0 {
		t.Fatalf("second scan cached = %d, want 0 (changed folder should be re-inspected)", summary2.Cached)
	}
}

// ---------------------------------------------------------------------------
// Import tests
// ---------------------------------------------------------------------------

func TestImportFolderCopiesAndIndexes(t *testing.T) {
	t.Parallel()
	service, _ := newUnpackedTestService(t)

	sourceDir := filepath.Join(t.TempDir(), "my_mod")
	writeTestFolderMod(t, sourceDir, "Imported Folder Mod")

	result, err := service.ImportMods([]string{sourceDir})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.ImportedCount != 1 || len(result.Failures) != 0 {
		t.Fatalf("import result = %#v", result)
	}

	// Verify it's under <LibraryDir>/unpacked/.
	item := result.Items[0]
	expectedPrefix := filepath.Join(service.config.LibraryDir, "unpacked")
	if !pathWithin(item.ArchivePath, expectedPrefix) {
		t.Errorf("imported path %q is not under %q", item.ArchivePath, expectedPrefix)
	}
	if item.SourceKind != "folder" {
		t.Errorf("source kind = %q, want folder", item.SourceKind)
	}

	// The source should still exist unchanged.
	if _, err := os.Stat(sourceDir); err != nil {
		t.Errorf("source folder was modified or deleted: %v", err)
	}

	// The copy should exist.
	if _, err := os.Stat(item.ArchivePath); err != nil {
		t.Errorf("imported folder does not exist: %v", err)
	}
}

func TestImportFolderRefusesEmptyFolder(t *testing.T) {
	t.Parallel()
	service, _ := newUnpackedTestService(t)

	emptyDir := filepath.Join(t.TempDir(), "empty_mod")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := service.ImportMods([]string{emptyDir})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Failures) != 1 {
		t.Fatalf("expected 1 failure for empty folder, got %d", len(result.Failures))
	}
	if !strings.Contains(result.Failures[0].Message, "empty") {
		t.Errorf("failure message %q should mention empty", result.Failures[0].Message)
	}
}

func TestImportFolderRefusesLibraryOwnUnpacked(t *testing.T) {
	t.Parallel()
	service, config := newUnpackedTestService(t)

	// Create a folder inside the library's own unpacked directory.
	libMod := filepath.Join(config.LibraryDir, "unpacked", "already_here")
	writeTestFolderMod(t, libMod, "Already Here")

	result, err := service.ImportMods([]string{libMod})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Failures) != 1 {
		t.Fatalf("expected 1 failure for library's own folder, got %d", len(result.Failures))
	}
	if !strings.Contains(result.Failures[0].Message, "already in the library") {
		t.Errorf("failure message %q should mention already in the library", result.Failures[0].Message)
	}
}

// ---------------------------------------------------------------------------
// Removal tests
// ---------------------------------------------------------------------------

func TestDeleteFolderModRecyclesFolder(t *testing.T) {
	t.Parallel()
	service, config := newUnpackedTestService(t)

	modDir := filepath.Join(config.ActiveModsDir, "unpacked", "to_delete")
	writeTestFolderMod(t, modDir, "Delete Me Folder")

	_, err := service.ScanLibrary()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	items, err := service.store.listItems(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	result, err := service.DeleteModArchives([]string{items[0].EntityID})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if result.Recycled != 1 {
		t.Errorf("recycled = %d, want 1", result.Recycled)
	}
	if len(result.Failures) != 0 {
		t.Errorf("failures = %v", result.Failures)
	}

	// The folder should be gone (recycleWorkspaceRoot on non-Windows just does RemoveAll).
	if _, err := os.Stat(modDir); !os.IsNotExist(err) {
		t.Errorf("folder mod should have been removed, err = %v", err)
	}
}

func TestDeleteFolderModRefusesIfChanged(t *testing.T) {
	t.Parallel()
	service, config := newUnpackedTestService(t)

	modDir := filepath.Join(config.ActiveModsDir, "unpacked", "changing_mod")
	writeTestFolderMod(t, modDir, "Changed Before Delete")

	_, err := service.ScanLibrary()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	items, err := service.store.listItems(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	// Change the folder after scan but before delete.
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(modDir, "extra.txt"), []byte("surprise"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := service.DeleteModArchives([]string{items[0].EntityID})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(result.Failures) != 1 {
		t.Fatalf("expected 1 failure for changed mod, got %d: %v", len(result.Failures), result.Failures)
	}
	if !strings.Contains(result.Failures[0], "changed since the last scan") {
		t.Errorf("failure %q should mention changed since last scan", result.Failures[0])
	}

	// The folder should still exist.
	if _, err := os.Stat(modDir); err != nil {
		t.Errorf("folder mod should not have been removed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Game monitor tests
// ---------------------------------------------------------------------------

func TestGameMonitorDetectsNewUnpackedFolder(t *testing.T) {
	t.Parallel()
	service, config := newUnpackedTestService(t)

	// Seed the library so first index marks all seen.
	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "seed.zip"), "Seed")
	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	monitor := newGameMonitor(service)
	service.monitorInstance = monitor
	monitor.processFunc = func() ([]gameProcess, error) { return nil, nil }
	monitor.refreshArchiveLinks()

	// Create an unpacked folder mod in the watched directory.
	modDir := filepath.Join(config.ActiveModsDir, "unpacked", "new_folder_mod")
	writeTestFolderMod(t, modDir, "New Folder Mod")

	// Tick 1: first seen.
	monitor.poll()
	key := archivePathKey(modDir)
	state, exists := monitor.candidates[key]
	if !exists {
		t.Fatal("folder mod should be a candidate after first poll")
	}
	if !state.sig.isFolder {
		t.Error("candidate signature should have isFolder=true")
	}

	// Tick 2: still settling.
	monitor.poll()

	// Tick 3: stable — should trigger a scan.
	monitor.poll()

	items, err := service.store.listItems(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item.DisplayName == "New Folder Mod" {
			found = true
			if item.SourceKind != "folder" {
				t.Errorf("source kind = %q, want folder", item.SourceKind)
			}
			break
		}
	}
	if !found {
		names := make([]string, 0, len(items))
		for _, item := range items {
			names = append(names, item.DisplayName)
		}
		t.Fatalf("New Folder Mod not found in library items: %v", names)
	}
}

// ---------------------------------------------------------------------------
// Mod families tests
// ---------------------------------------------------------------------------

func TestFolderAndZIPNeverReportedAsByteIdenticalCopies(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")

	// Create a ZIP archive.
	zipArchive := modFamilyScanArchive(t, root, "mod.zip", "Same Mod", "Author", "1.0",
		"repo-99", "sha-zip", "fp-zip", 1000, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 0)

	// Create a fake "folder" archive with different fingerprint.
	folderArchive := modFamilyScanArchive(t, root, "folder_mod", "Same Mod", "Author", "1.0",
		"repo-99", "", "fp-folder", 1000, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 0)
	folderArchive.Manifest.SourceKind = modkit.SourceFolder

	applyLibraryArchives(t, service.store, root, []ScanArchive{zipArchive, folderArchive})

	families, err := service.ModFamilies()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.Kind == "copies" {
			// A folder and a ZIP should never be copies (fingerprints differ).
			hasFolder := false
			hasZIP := false
			for _, member := range family.Members {
				if strings.HasSuffix(member.ArchivePath, ".zip") {
					hasZIP = true
				} else {
					hasFolder = true
				}
			}
			if hasFolder && hasZIP {
				t.Errorf("folder and ZIP should never be byte-identical copies, but got family %q with kind %q", family.ID, family.Kind)
			}
		}
	}
}
