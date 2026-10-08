package main

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newMonitorTestService creates an AppService suitable for game monitor tests.
// Uses separate temp dirs so ActiveModsDir and LibraryDir are not under DataDir
// (the library scanner skips directories within DataDir).
func newMonitorTestService(t *testing.T) (*AppService, AppConfig) {
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

// writeTestModZip creates a minimal valid mod archive at the given path.
func writeTestModZip(t *testing.T, path, title string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	m, err := w.Create("mod_info/info.json")
	if err == nil {
		_, err = m.Write([]byte(fmt.Sprintf(`{"title":%q,"author":"Test","version":"1.0"}`, title)))
	}
	if err == nil {
		v, verr := w.Create("vehicles/test/main.jbeam")
		if verr != nil {
			err = verr
		} else {
			_, err = v.Write([]byte(`{}`))
		}
	}
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
}

// writeInvalidZip creates a file that is not a valid zip archive.
func writeInvalidZip(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a zip archive at all"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestArrivalsQueuedWithCorrectOrigin(t *testing.T) {
	t.Parallel()
	service, config := newMonitorTestService(t)

	// First index with one mod; ScanLibrary marks all seen on first index.
	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "seed.zip"), "Seed Mod")
	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	// Add new mods.
	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "repo", "beamng_mod.zip"), "BeamNG Mod")
	writeTestModZip(t, filepath.Join(config.LibraryDir, "library_mod.zip"), "Library Mod")

	// Scan again; arrival detection fires inside ScanLibrary.
	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	reviews, err := service.store.pendingNewModReviews(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 2 {
		t.Fatalf("expected 2 reviews, got %d", len(reviews))
	}
	originMap := map[string]string{}
	for _, r := range reviews {
		items, _ := service.store.listItemsByIDs(context.Background(), []string{r.entityID})
		if len(items) > 0 {
			originMap[items[0].DisplayName] = r.origin
		}
	}
	if originMap["BeamNG Mod"] != "beamng" {
		t.Errorf("BeamNG Mod origin = %q, want beamng", originMap["BeamNG Mod"])
	}
	if originMap["Library Mod"] != "library-folder" {
		t.Errorf("Library Mod origin = %q, want library-folder", originMap["Library Mod"])
	}
}

func TestFirstIndexQueuesNothingAndNothingIsNew(t *testing.T) {
	t.Parallel()
	service, config := newMonitorTestService(t)

	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "first.zip"), "First Mod")
	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "second.zip"), "Second Mod")

	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	reviews, _ := service.store.pendingNewModReviews(context.Background())
	if len(reviews) != 0 {
		t.Fatalf("expected 0 reviews on first index, got %d", len(reviews))
	}

	items, err := service.store.listItems(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.New {
			t.Errorf("mod %q should not be New on first index", item.DisplayName)
		}
	}
}

func TestStudioPlacedPathsNotQueued(t *testing.T) {
	t.Parallel()
	service, config := newMonitorTestService(t)

	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "seed.zip"), "Seed")
	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	studioPath := filepath.Join(config.LibraryDir, "From BeamNG", "studio_placed.zip")
	writeTestModZip(t, studioPath, "Studio Placed")
	if err := service.store.recordStudioPlacedArchive(context.Background(), studioPath); err != nil {
		t.Fatal(err)
	}

	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	reviews, _ := service.store.pendingNewModReviews(context.Background())
	if len(reviews) != 0 {
		t.Fatalf("studio-placed mod should not be queued, got %d reviews", len(reviews))
	}
}

func TestResolveAddsToCollectionsAndClearsOnlyReviewedIDs(t *testing.T) {
	t.Parallel()
	service, config := newMonitorTestService(t)

	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "seed.zip"), "Seed")
	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "mod_a.zip"), "Mod A")
	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "mod_b.zip"), "Mod B")

	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	reviews, _ := service.store.pendingNewModReviews(context.Background())
	if len(reviews) != 2 {
		t.Fatalf("expected 2 reviews, got %d", len(reviews))
	}

	// Create a collection.
	col, err := service.store.CreateCollection(context.Background(), "Test Collection", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Resolve only the first review; add it to the collection.
	reviewedIDs := []string{reviews[0].entityID}
	addIDs := []string{reviews[0].entityID}
	collectionIDs := []string{col.Collection.ID}
	result, err := service.ResolveNewMods(reviewedIDs, addIDs, collectionIDs)
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 1 {
		t.Errorf("expected 1 added, got %d", result.Added)
	}
	if len(result.Collections) != 1 || result.Collections[0] != "Test Collection" {
		t.Errorf("unexpected collections: %v", result.Collections)
	}

	// The second review should still be pending.
	remaining, _ := service.store.pendingNewModReviews(context.Background())
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining review, got %d", len(remaining))
	}
	if remaining[0].entityID != reviews[1].entityID {
		t.Errorf("wrong remaining review: got %s, want %s", remaining[0].entityID, reviews[1].entityID)
	}

	// Check that the mod was added to the collection.
	colDetail, err := service.store.CollectionDetail(context.Background(), col.Collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, member := range colDetail.Members {
		if member.EntityID == reviews[0].entityID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("resolved mod not found in collection")
	}
}

func TestNewFlagWindowSeenArchived(t *testing.T) {
	t.Parallel()
	service, config := newMonitorTestService(t)

	// Seed the library so it is non-empty (first index marks all seen).
	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "seed.zip"), "Seed")
	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	// Add a new mod (not first index, so it will be New).
	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "new_mod.zip"), "New Mod")
	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	items, err := service.store.listItems(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var item LibraryItem
	found := false
	for _, it := range items {
		if it.DisplayName == "New Mod" {
			item = it
			found = true
			break
		}
	}
	if !found {
		t.Fatal("New Mod not found")
	}

	// New mod created just now should have New=true.
	if !item.New {
		t.Error("freshly created mod should be New")
	}
	if item.AddedAt == "" {
		t.Error("AddedAt should be set")
	}

	// Mark as seen.
	if err := service.MarkModsSeen([]string{item.EntityID}); err != nil {
		t.Fatal(err)
	}

	items, _ = service.store.listItems(context.Background(), "")
	for _, it := range items {
		if it.EntityID == item.EntityID && it.New {
			t.Error("mod should not be New after MarkModsSeen")
		}
	}

	// Test that archived mods are never New: un-see it, archive it.
	service.store.writeMu.Lock()
	_, _ = service.store.db.Exec(`DELETE FROM mod_seen WHERE entity_id=?`, item.EntityID)
	service.store.writeMu.Unlock()

	_, err = service.store.ArchiveMods(context.Background(), []string{item.EntityID})
	if err != nil {
		t.Fatal(err)
	}

	items, _ = service.store.listItems(context.Background(), "")
	for _, it := range items {
		if it.EntityID == item.EntityID && it.New {
			t.Error("archived mod should not be New")
		}
	}
}

func TestPartiallyWrittenZipNotLinkedOrScanned(t *testing.T) {
	t.Parallel()
	service, config := newMonitorTestService(t)

	monitor := newGameMonitor(service)
	service.monitorInstance = monitor
	monitor.processFunc = func() ([]gameProcess, error) { return nil, nil }
	monitor.refreshArchiveLinks()

	// Write an invalid zip into ActiveModsDir.
	invalidPath := filepath.Join(config.ActiveModsDir, "partial.zip")
	writeInvalidZip(t, invalidPath)

	// Tick 1: first seen.
	monitor.poll()
	if got := service.computeGameStatus().ArrivingMods; got != 1 {
		t.Fatalf("a file still settling should count as arriving, got %d", got)
	}
	// Tick 2: still first seen (needs two consecutive unchanged after first).
	monitor.poll()
	// Tick 3: should be stable now; zip validation should reject it.
	monitor.poll()
	if got := service.computeGameStatus().ArrivingMods; got != 0 {
		t.Fatalf("a stable unreadable zip must not stay \"arriving\" forever, got %d", got)
	}

	// No scan should have happened (no new entities).
	items, _ := service.store.listItems(context.Background(), "")
	if len(items) != 0 {
		t.Errorf("invalid zip should not have been scanned, got %d items", len(items))
	}

	// Now replace with a valid zip; the candidate's sig changes, resetting it.
	writeTestModZip(t, invalidPath, "Now Valid Mod")
	// Need 3 more ticks for settle.
	monitor.poll()
	monitor.poll()
	monitor.poll()

	items, _ = service.store.listItems(context.Background(), "")
	if len(items) != 1 {
		t.Fatalf("expected 1 item after valid zip, got %d", len(items))
	}
	if items[0].DisplayName != "Now Valid Mod" {
		t.Errorf("unexpected display name: %q", items[0].DisplayName)
	}
}

func TestProfileDownloadLinkedIntoRealModsThenReviewed(t *testing.T) {
	t.Parallel()
	service, config := newMonitorTestService(t)

	// Seed library first.
	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "seed.zip"), "Seed")
	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	// Set up a play user path.
	playRoot, err := playUserPath(config)
	if err != nil {
		t.Skipf("cannot create play user path: %v", err)
	}
	profileMods := playProfileModsDir(playRoot)
	if err := os.MkdirAll(profileMods, 0o755); err != nil {
		t.Fatal(err)
	}

	monitor := newGameMonitor(service)
	service.monitorInstance = monitor
	monitor.processFunc = func() ([]gameProcess, error) { return nil, nil }
	monitor.refreshArchiveLinks()

	// Write a mod into the profile mods directory.
	profilePath := filepath.Join(profileMods, "repo", "profile_mod.zip")
	writeTestModZip(t, profilePath, "Profile Download")

	// Settle: 3 polls.
	monitor.poll()
	monitor.poll()
	monitor.poll()

	// The mod should have been linked into ActiveModsDir.
	realPath := filepath.Join(config.ActiveModsDir, "repo", "profile_mod.zip")
	if _, err := os.Stat(realPath); err != nil {
		t.Fatalf("expected real path %s to exist: %v", realPath, err)
	}

	// And should now be scanned into the library.
	items, _ := service.store.listItems(context.Background(), "")
	found := false
	for _, it := range items {
		if it.DisplayName == "Profile Download" {
			found = true
			break
		}
	}
	if !found {
		t.Error("profile download should have been scanned into the library")
	}
}

func TestOwnedProfileEntriesIgnored(t *testing.T) {
	t.Parallel()
	service, config := newMonitorTestService(t)

	// Write a mod into ActiveModsDir and register it as owned.
	ownedPath := filepath.Join(config.ActiveModsDir, "owned.zip")
	writeTestModZip(t, ownedPath, "Owned Mod")

	// Save an owned archive entry for this path.
	ctx := context.Background()
	entries := []OwnedArchiveEntry{{
		ID:           "test-owned-1",
		Purpose:      archivePurposePlay,
		OwnerID:      playDeploymentOwnerID,
		TargetRoot:   config.ActiveModsDir,
		RelativePath: "owned.zip",
		State:        archiveStateActive,
	}}
	if err := service.store.saveOwnedArchiveEntries(ctx, entries); err != nil {
		t.Fatal(err)
	}

	monitor := newGameMonitor(service)
	service.monitorInstance = monitor
	monitor.processFunc = func() ([]gameProcess, error) { return nil, nil }
	monitor.refreshArchiveLinks()

	// 3 polls; owned entry should be skipped.
	monitor.poll()
	monitor.poll()
	monitor.poll()

	items, _ := service.store.listItems(ctx, "")
	if len(items) != 0 {
		t.Errorf("owned entries should be ignored, got %d items", len(items))
	}
}

func TestExitTransitionHarvests(t *testing.T) {
	t.Parallel()
	service, config := newMonitorTestService(t)

	// Seed library.
	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "seed.zip"), "Seed")
	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	playRoot, err := playUserPath(config)
	if err != nil {
		t.Skipf("cannot create play user path: %v", err)
	}
	profileMods := playProfileModsDir(playRoot)
	if err := os.MkdirAll(profileMods, 0o755); err != nil {
		t.Fatal(err)
	}

	// Place a download in profile mods.
	downloadPath := filepath.Join(profileMods, "repo", "downloaded.zip")
	writeTestModZip(t, downloadPath, "Downloaded During Play")

	running := true
	monitor := newGameMonitor(service)
	service.monitorInstance = monitor
	monitor.processFunc = func() ([]gameProcess, error) {
		if running {
			return []gameProcess{{PID: 12345, Started: time.Now()}}, nil
		}
		return nil, nil
	}
	service.gameRunning = func() (bool, error) { return running, nil }
	monitor.refreshArchiveLinks()

	// Poll while running.
	monitor.poll()
	monitor.poll()

	// Transition to stopped.
	running = false
	monitor.poll()

	// The file should have been harvested into ActiveModsDir.
	realPath := filepath.Join(config.ActiveModsDir, "repo", "downloaded.zip")
	if _, err := os.Stat(realPath); err != nil {
		t.Fatalf("expected harvested file at %s: %v", realPath, err)
	}
}

func TestFailedScanDoesNotLoop(t *testing.T) {
	t.Parallel()
	service, config := newMonitorTestService(t)

	monitor := newGameMonitor(service)
	service.monitorInstance = monitor
	monitor.processFunc = func() ([]gameProcess, error) { return nil, nil }
	monitor.refreshArchiveLinks()

	// Write a valid zip.
	zipPath := filepath.Join(config.ActiveModsDir, "valid.zip")
	writeTestModZip(t, zipPath, "Valid Mod")

	// Settle: 3 polls.
	monitor.poll()
	monitor.poll()
	monitor.poll()

	// The mod should be scanned.
	items, _ := service.store.listItems(context.Background(), "")
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	// Poll again; the already-attempted candidate should not cause another scan.
	// (It's now in archive_links, so it will be skipped. But if somehow it wasn't,
	// the 'attempted' flag prevents re-scanning.)
	monitor.refreshArchiveLinks()
	monitor.poll()

	// Still 1 item.
	items, _ = service.store.listItems(context.Background(), "")
	if len(items) != 1 {
		t.Fatalf("expected 1 item after re-poll, got %d", len(items))
	}
}

func TestMarkModsSeenIdempotent(t *testing.T) {
	t.Parallel()
	service, config := newMonitorTestService(t)
	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "seen_test.zip"), "Seen Test")
	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	items, _ := service.store.listItems(context.Background(), "")
	if len(items) == 0 {
		t.Fatal("expected at least 1 item")
	}
	entityID := items[0].EntityID

	// Mark once.
	if err := service.MarkModsSeen([]string{entityID}); err != nil {
		t.Fatal(err)
	}
	// Mark again (idempotent).
	if err := service.MarkModsSeen([]string{entityID}); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileSameFileShortcut(t *testing.T) {
	t.Parallel()
	service, config := newMonitorTestService(t)

	playRoot, err := playUserPath(config)
	if err != nil {
		t.Skipf("cannot create play user path: %v", err)
	}
	profileMods := playProfileModsDir(playRoot)
	if err := os.MkdirAll(filepath.Join(profileMods, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a mod in ActiveModsDir.
	realPath := filepath.Join(config.ActiveModsDir, "repo", "linked.zip")
	writeTestModZip(t, realPath, "Linked Mod")

	// Hardlink from profile to real (simulating in-session linking).
	profilePath := filepath.Join(profileMods, "repo", "linked.zip")
	if err := os.Link(realPath, profilePath); err != nil {
		t.Skipf("hardlink not supported: %v", err)
	}

	// Reconcile should detect same-file and just remove the profile link.
	service.modImportMu.Lock()
	harvested, err := service.reconcileProfileSessionDownloads(context.Background(), playRoot)
	service.modImportMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if harvested != 0 {
		t.Errorf("expected 0 harvested (same-file shortcut), got %d", harvested)
	}

	// Profile file should be gone.
	if _, err := os.Stat(profilePath); !os.IsNotExist(err) {
		t.Errorf("profile link should have been removed")
	}
	// Real file should still exist.
	if _, err := os.Stat(realPath); err != nil {
		t.Errorf("real file should still exist: %v", err)
	}
}

func TestGameStatusComputedCorrectly(t *testing.T) {
	t.Parallel()
	service, _ := newMonitorTestService(t)

	monitor := newGameMonitor(service)
	service.monitorInstance = monitor
	// No processes running.
	monitor.processFunc = func() ([]gameProcess, error) { return nil, nil }

	status := service.GetGameStatus()
	if status.Running {
		t.Error("expected Running=false with no processes")
	}
	if status.CollectionNames == nil {
		t.Error("CollectionNames should be empty slice, not nil")
	}
}

func TestPendingNewModsReview(t *testing.T) {
	t.Parallel()
	service, config := newMonitorTestService(t)

	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "seed.zip"), "Seed")
	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "arrival.zip"), "Arrival")
	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	// PendingNewMods should return it.
	review, err := service.PendingNewMods()
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Arrivals) != 1 {
		t.Fatalf("expected 1 arrival, got %d", len(review.Arrivals))
	}
	if review.Arrivals[0].Item.DisplayName != "Arrival" {
		t.Errorf("unexpected arrival name: %q", review.Arrivals[0].Item.DisplayName)
	}
	if review.Arrivals[0].Origin != "beamng" {
		t.Errorf("expected origin beamng, got %q", review.Arrivals[0].Origin)
	}
}

func TestResolveNewModsCancelClearsReviews(t *testing.T) {
	t.Parallel()
	service, config := newMonitorTestService(t)

	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "seed.zip"), "Seed")
	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	writeTestModZip(t, filepath.Join(config.ActiveModsDir, "cancel_test.zip"), "Cancel Test")
	if _, err := service.ScanLibrary(); err != nil {
		t.Fatal(err)
	}

	reviews, _ := service.store.pendingNewModReviews(context.Background())
	if len(reviews) != 1 {
		t.Fatalf("expected 1 review, got %d", len(reviews))
	}

	// Cancel = ResolveNewMods(reviewedIDs, nil, nil).
	_, err := service.ResolveNewMods([]string{reviews[0].entityID}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	remaining, _ := service.store.pendingNewModReviews(context.Background())
	if len(remaining) != 0 {
		t.Fatalf("expected 0 remaining reviews after cancel, got %d", len(remaining))
	}
}

func TestResolveNewModsInvalidCollectionFails(t *testing.T) {
	t.Parallel()
	service, _ := newMonitorTestService(t)

	_, err := service.ResolveNewMods([]string{"x"}, []string{"x"}, []string{"nonexistent-collection"})
	if err == nil {
		t.Error("expected error for nonexistent collection")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClassifyModOrigin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path      string
		activeDir string
		want      string
	}{
		{`C:\Games\BeamNG\current\mods\repo\mod.zip`, `C:\Games\BeamNG\current\mods`, "beamng"},
		{`C:\Library\mod.zip`, `C:\Games\BeamNG\current\mods`, "library-folder"},
		{"", "", "library-folder"},
	}
	for _, tt := range tests {
		got := classifyModOrigin(tt.path, tt.activeDir)
		if got != tt.want {
			t.Errorf("classifyModOrigin(%q, %q) = %q, want %q", tt.path, tt.activeDir, got, tt.want)
		}
	}
}
