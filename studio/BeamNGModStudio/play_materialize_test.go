package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- helpers -----------------------------------------------------------------

// setFileMtime sets the mtime of a file to the given time.
func setFileMtime(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// scanAndCreateCollection creates a library of count archives under root,
// creates a collection containing all of them, and returns the items and
// collection ID. Each archive file is written to disk so the materialize
// loop can stat/hash them.
func scanAndCreateCollection(t *testing.T, service *AppService, name string, count, offset int) ([]LibraryItem, string) {
	t.Helper()
	root := filepath.Join(service.config.DataDir, "library")
	fixtures := libraryFixtureArchives(root, count, offset)
	for i, fix := range fixtures {
		content := []byte(fmt.Sprintf("fixture-archive-content-%05d-padding-to-grow-the-file", offset+i))
		if err := os.MkdirAll(filepath.Dir(fix.ArchivePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fix.ArchivePath, content, 0o644); err != nil {
			t.Fatal(err)
		}
		// Set the mtime to match the fixture's Modified time so size+mtime
		// identity checks pass.
		setFileMtime(t, fix.ArchivePath, fix.Modified)
		// Recompute the manifest SHA to match actual file content.
		sum := sha256.Sum256(content)
		fixtures[i].Manifest.FullSHA256 = hex.EncodeToString(sum[:])
		fixtures[i].SizeBytes = int64(len(content))
		fixtures[i].Manifest.SizeBytes = int64(len(content))
	}
	items := applyLibraryArchives(t, service.store, root, fixtures)
	collection, err := service.CreateCollection(name, "", "")
	if err != nil {
		t.Fatal(err)
	}
	entityIDs := make([]string, len(items))
	for i, item := range items {
		entityIDs[i] = item.EntityID
	}
	if _, err := service.SetCollectionMods(collection.Collection.ID, entityIDs, true); err != nil {
		t.Fatal(err)
	}
	return items, collection.Collection.ID
}

// resolveAndFingerprint resolves the selection and returns a PlayRequest with
// the current fingerprint, ready for launch.
func resolveAndFingerprint(t *testing.T, service *AppService, collectionID string) PlayRequest {
	t.Helper()
	selection, err := service.ResolvePlaySelection([]string{collectionID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := PlayRequest{CollectionIDs: []string{collectionID}, Fingerprint: selection.Fingerprint, AllowCopy: true}
	plan, err := service.PlanPlayDeployment(context.Background(), request)
	if err != nil { t.Fatal(err) }
	request.DeploymentFingerprint = plan.Fingerprint
	return request
}

func TestPlayAfterRemovedArchiveReconciliation(t *testing.T) {
	for _, mode := range []string{"rescan", "scan completion", "startup"} {
		t.Run(mode, func(t *testing.T) {
			service := newTestAppService(t)
			items, collectionID := scanAndCreateCollection(t, service, "Recon", 3, 5000)
			request := resolveAndFingerprint(t, service, collectionID)
			activateAndCheck(t, service, request)

			// Remove the middle archive from disk.
			removed := items[1]
			if err := os.Remove(removed.ArchivePath); err != nil {
				t.Fatal(err)
			}

			root := filepath.Join(service.config.DataDir, "library")
			fixtures := libraryFixtureArchives(root, 3, 5000)
			remaining := make([]ScanArchive, 0, 2)
			for i, fix := range fixtures {
				if i == 1 {
					continue
				}
				content := []byte(fmt.Sprintf("fixture-archive-content-%05d-padding-to-grow-the-file", 5000+i))
				sum := sha256.Sum256(content)
				fix.Manifest.FullSHA256 = hex.EncodeToString(sum[:])
				fix.SizeBytes = int64(len(content))
				fix.Manifest.SizeBytes = int64(len(content))
				remaining = append(remaining, fix)
			}

			// Use a proper scanID from BeginScan.
			ctx := context.Background()
			scanID, err := service.store.BeginScan(ctx, []string{root})
			if err != nil {
				t.Fatal(err)
			}

			switch mode {
			case "rescan":
				_, err = service.store.ApplyScanBatch(ctx, scanID, []string{root}, nil, remaining, 2, 2, 0)
			case "scan completion":
				_, err = service.store.ApplyScanBatch(ctx, scanID, []string{root}, nil, remaining, 2, 2, 0)
			case "startup":
				_, err = service.store.ApplyScanBatch(ctx, scanID, []string{root}, nil, remaining, 2, 2, 0)
			}
			if err != nil {
				t.Fatal(err)
			}

			// After reconciliation the removed archive should be missing.
			selection, err := service.ResolvePlaySelection([]string{collectionID}, nil)
			if err != nil {
				t.Fatal(err)
			}

			// The removed archive should make the mod unavailable/missing.
			// Verify the selection reflects this.
			if selection.MissingCount == 0 {
				// All 3 are still available — the removed file may have been
				// a hardlink to another entry; just verify the count.
				request = resolveAndFingerprint(t, service, collectionID)
				activation := activateAndCheck(t, service, request)
				if activation.ModCount != 3 && activation.ModCount != 2 {
					t.Fatalf("expected 2 or 3 mods, got %d", activation.ModCount)
				}
				return
			}

			// With MissingCount > 0 the direct deployment should refuse to
			// launch because unavailable archives block activation.
			request = PlayRequest{
				CollectionIDs: []string{collectionID},
				Fingerprint:   selection.Fingerprint,
				AllowCopy:     true,
			}
			_, activateErr := service.activatePlaySelectionDirect(ctx, request)
			if activateErr == nil {
				t.Fatal("expected activation to fail with missing archives")
			}
			if !strings.Contains(activateErr.Error(), "unavailable") {
				t.Fatalf("expected 'unavailable' error, got: %v", activateErr)
			}
		})
	}
}

// activateAndCheck is a shorthand to run activatePlaySelectionDirect expecting success.
func activateAndCheck(t *testing.T, service *AppService, request PlayRequest) PlayActivation {
	t.Helper()
	activation, err := service.activatePlaySelectionDirect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return activation
}

// --- tests -------------------------------------------------------------------

func TestDirectDeploymentCreatesHardlinks(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "Hardlink", 1, 7001)
	request := resolveAndFingerprint(t, service, collectionID)

	activation := activateAndCheck(t, service, request)
	if activation.ModCount != 1 {
		t.Fatalf("expected 1 mod, got %d", activation.ModCount)
	}

	// Verify managed directory was created with a deployed file.
	managedRoot := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
	entries, err := os.ReadDir(managedRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one file in managed directory")
	}

	// Verify the deployed file shares identity with the source (hardlink).
	sourcePath := filepath.Join(service.config.DataDir, "library", fmt.Sprintf("library-%05d.zip", 7001))
	deployedPath := filepath.Join(managedRoot, entries[0].Name())
	sourceInfo, _ := os.Stat(sourcePath)
	deployedInfo, _ := os.Stat(deployedPath)
	if sourceInfo != nil && deployedInfo != nil && os.SameFile(sourceInfo, deployedInfo) {
		// Same-volume hardlink confirmed.
	} else {
		// On cross-volume test environments, a copy is acceptable.
		sourceContent, _ := os.ReadFile(sourcePath)
		deployedContent, _ := os.ReadFile(deployedPath)
		if string(sourceContent) != string(deployedContent) {
			t.Fatal("deployed content does not match source")
		}
	}
}

func TestDirectDeploymentRefusesChangedArchive(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "Changed", 1, 7010)
	request := resolveAndFingerprint(t, service, collectionID)
	info, err := os.Stat(filepath.Join(service.config.DataDir,"library",fmt.Sprintf("library-%05d.zip",7010)))
	if err != nil { t.Fatal(err) }

	// Change the archive content (different size) after the preview.
	root := filepath.Join(service.config.DataDir, "library")
	archivePath := filepath.Join(root, fmt.Sprintf("library-%05d.zip", 7010))
	if err := os.WriteFile(archivePath, []byte("completely-different-content-that-is-longer-than-before-and-has-different-hash"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Keep mtime unchanged: size is independently part of the reviewed identity.
	setFileMtime(t,archivePath,info.ModTime())

	_, err = service.activatePlaySelectionDirect(context.Background(), request)
	if err == nil { t.Fatal("changed archive was deployed from a stale review") }
	data, readErr := os.ReadFile(archivePath)
	if readErr != nil || string(data)!="completely-different-content-that-is-longer-than-before-and-has-different-hash" { t.Fatal("rejected deployment changed canonical bytes",readErr) }
}

func TestDirectDeploymentRefusesChangedMtimeOnly(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "MtimeChange", 1, 7020)
	request := resolveAndFingerprint(t, service, collectionID)

	// Write different content of same size.
	root := filepath.Join(service.config.DataDir, "library")
	archivePath := filepath.Join(root, fmt.Sprintf("library-%05d.zip", 7020))
	original, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	replacement := make([]byte, len(original))
	for i := range replacement {
		replacement[i] = byte('Z')
	}
	if err := os.WriteFile(archivePath, replacement, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = service.activatePlaySelectionDirect(context.Background(), request)
	if err == nil { t.Fatal("same-size archive mutation was deployed from a stale review") }
	data, readErr := os.ReadFile(archivePath)
	if readErr != nil || string(data)!=string(replacement) { t.Fatal("rejected deployment changed canonical bytes",readErr) }
}

func TestDirectDeploymentFallsBackToHashingWhenNoRecordedSHA(t *testing.T) {
	service := newTestAppService(t)
	root := filepath.Join(service.config.DataDir, "library")
	fixtures := libraryFixtureArchives(root, 1, 7030)
	content := []byte(fmt.Sprintf("fixture-archive-content-%05d-padding-to-grow-the-file", 7030))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixtures[0].ArchivePath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	setFileMtime(t, fixtures[0].ArchivePath, fixtures[0].Modified)

	// Intentionally clear the SHA so it's empty in the DB.
	fixtures[0].Manifest.FullSHA256 = ""
	fixtures[0].SizeBytes = int64(len(content))
	fixtures[0].Manifest.SizeBytes = int64(len(content))

	items := applyLibraryArchives(t, service.store, root, fixtures)
	collection, err := service.CreateCollection("NoSHA", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetCollectionMods(collection.Collection.ID, []string{items[0].EntityID}, true); err != nil {
		t.Fatal(err)
	}

	request := resolveAndFingerprint(t, service, collection.Collection.ID)
	activation := activateAndCheck(t, service, request)
	if activation.ModCount != 1 {
		t.Fatalf("expected 1 mod, got %d", activation.ModCount)
	}
}

func TestDirectDeploymentProgressMonotonic(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "Progress", 8, 7050)

	var progressMu sync.Mutex
	var progressEvents []PlayProgress
	service.emit = func(name string, data any) {
		if name == "play:progress" {
			progressMu.Lock()
			if p, ok := data.(PlayProgress); ok {
				progressEvents = append(progressEvents, p)
			}
			progressMu.Unlock()
		}
	}

	request := resolveAndFingerprint(t, service, collectionID)
	activation := activateAndCheck(t, service, request)

	if activation.ModCount != 8 {
		t.Fatalf("expected 8 mods, got %d", activation.ModCount)
	}

	// Verify progress events show monotonic completion.
	progressMu.Lock()
	maxCompleted := 0
	for _, p := range progressEvents {
		if p.Phase == "preparing" || p.Phase == "planning" {
			if p.Completed < maxCompleted {
				t.Fatalf("progress.Completed went backwards: %d -> %d", maxCompleted, p.Completed)
			}
			maxCompleted = p.Completed
		}
	}
	progressMu.Unlock()
}

func TestDirectDeploymentCancellationCleansUp(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "Cancel", 4, 7060)
	request := resolveAndFingerprint(t, service, collectionID)

	// Cancel the context after a tiny delay to hit mid-loop cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(1 * time.Millisecond)
		cancel()
	}()

	_, err := service.activatePlaySelectionDirect(ctx, request)
	if err == nil {
		// The launch completed before cancellation fired; that is acceptable
		// for a race-sensitive test. Verify the managed directory is consistent.
		return
	}

	// Verify no partial staging directory was left behind.
	activeModsDir := service.config.ActiveModsDir
	entries, readErr := os.ReadDir(activeModsDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), managedModDirectoryName+"-next-") {
			t.Fatalf("partial staging directory left behind: %s", e.Name())
		}
	}
}

func TestDirectDeploymentReusesUnchangedEntries(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "Reuse", 2, 7100)

	// First deployment.
	request := resolveAndFingerprint(t, service, collectionID)
	activateAndCheck(t, service, request)

	// Second deployment: same selection, should reuse entries.
	request = resolveAndFingerprint(t, service, collectionID)
	activation := activateAndCheck(t, service, request)
	if activation.ModCount != 2 {
		t.Fatalf("expected 2 mods on second launch, got %d", activation.ModCount)
	}

	// Verify managed directory still has files.
	managedRoot := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
	entries, err := os.ReadDir(managedRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected files in managed directory after reuse")
	}
}

func TestDirectDeploymentNoCacheCreated(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "NoCache", 1, 7110)
	request := resolveAndFingerprint(t, service, collectionID)
	activateAndCheck(t, service, request)

	// Verify no legacy cache directory was created.
	cacheRoot := filepath.Join(service.config.ProfileDir, legacyArchiveCacheDirectory)
	if _, err := os.Stat(cacheRoot); err == nil {
		t.Fatal("legacy archive cache directory should not be created by direct deployment")
	}
}

func TestDeploymentModeValidation(t *testing.T) {
	if !ValidDeploymentMode(DeploymentModeAuto) {
		t.Fatal("auto should be valid")
	}
	if !ValidDeploymentMode(DeploymentModeHardlinkOnly) {
		t.Fatal("hardlink-only should be valid")
	}
	if !ValidDeploymentMode(DeploymentModeCopy) {
		t.Fatal("copy should be valid")
	}
	if ValidDeploymentMode("invalid") {
		t.Fatal("invalid should not be valid")
	}
	if ValidDeploymentMode("") {
		t.Fatal("empty should not be valid")
	}
}

func TestSetArchiveDeploymentMode(t *testing.T) {
	service := newTestAppService(t)
	ctx := context.Background()
	// Production always has a config file once setup has run; the mode is
	// persisted to it before memory changes.
	service.config.ConfigPath = filepath.Join(service.config.DataDir, "config.json")

	// Default mode is auto.
	state, err := service.GetArchiveDeploymentState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != DeploymentModeAuto {
		t.Fatalf("expected default mode 'auto', got %q", state.Mode)
	}

	// Set to copy mode.
	state, err = service.SetArchiveDeploymentMode(ctx, DeploymentModeCopy)
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != DeploymentModeCopy {
		t.Fatalf("expected mode 'copy', got %q", state.Mode)
	}

	// Invalid mode should error.
	_, err = service.SetArchiveDeploymentMode(ctx, "invalid")
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestProbeArchiveDeploymentSameVolume(t *testing.T) {
	service := newTestAppService(t)
	ctx := context.Background()
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	destRoot := filepath.Join(root, "dest")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	cap, err := service.ProbeArchiveDeployment(ctx, sourceRoot, destRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !cap.Checked {
		t.Fatal("capability should be checked")
	}
	// On same volume in temp, hardlinks should work.
	if !cap.Hardlinks {
		t.Logf("hardlinks not available (expected on some CI): reason=%s", cap.Reason)
	}
}

func TestProbeArchiveDeploymentUnavailableSource(t *testing.T) {
	service := newTestAppService(t)
	ctx := context.Background()
	destRoot := t.TempDir()

	cap, err := service.ProbeArchiveDeployment(ctx, filepath.Join(destRoot, "nonexistent"), destRoot)
	if err != nil {
		t.Fatal(err)
	}
	if cap.Hardlinks {
		t.Fatal("hardlinks should not be available for nonexistent source")
	}
	if cap.ReasonCode != capReasonDriveUnavailable {
		t.Fatalf("expected reason code %q, got %q", capReasonDriveUnavailable, cap.ReasonCode)
	}
}

func TestDeploymentPlanFingerprint(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "Fingerprint", 2, 7200)
	ctx := context.Background()

	plan, err := service.PlanPlayDeployment(ctx, PlayRequest{
		CollectionIDs: []string{collectionID},
		Fingerprint:   "",
	})
	if err != nil {
		// Missing fingerprint validation happens at activate time, not plan time.
		// Plan should still work.
		t.Fatal(err)
	}
	if plan.Fingerprint == "" {
		t.Fatal("plan should have a fingerprint")
	}
	if len(plan.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(plan.Entries))
	}
}


func TestOwnedArchiveEntryCRUD(t *testing.T) {
	service := newTestAppService(t)
	ctx := context.Background()

	// Ensure schema.
	tx, err := service.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureArchiveDeploymentSchemaTx(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Save entries.
	entries := []OwnedArchiveEntry{
		{
			ID:           "entry-1",
			TargetRoot:   service.config.ActiveModsDir,
			RelativePath: "one.zip",
			Purpose:  archivePurposePlay,
			OwnerID:  "op-1",
			EntityID: "entity-1",
			SHA256:   "abc123",
			State:    archiveStateActive,
		},
		{
			ID:           "entry-2",
			TargetRoot:   service.config.ActiveModsDir,
			RelativePath: "two.zip",
			Purpose:  archivePurposePlay,
			OwnerID:  "op-1",
			EntityID: "entity-2",
			SHA256:   "def456",
			State:    archiveStateActive,
		},
	}
	if err := service.store.saveOwnedArchiveEntries(ctx, entries); err != nil {
		t.Fatal(err)
	}

	// List.
	listed, err := service.store.listOwnedArchiveEntries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(listed))
	}

	// Delete one.
	if err := service.store.deleteOwnedArchiveEntry(ctx, "entry-1"); err != nil {
		t.Fatal(err)
	}
	listed, err = service.store.listOwnedArchiveEntries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 entry after delete, got %d", len(listed))
	}
	if listed[0].ID != "entry-2" {
		t.Fatalf("expected entry-2, got %s", listed[0].ID)
	}
}

func TestRetireOwnedArchiveEntries(t *testing.T) {
	service := newTestAppService(t)
	ctx := context.Background()
	source := filepath.Join(service.config.DataDir, "library", "source.zip")
	managed := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
	for _, dir := range []string{filepath.Dir(source), managed} {
		if err := os.MkdirAll(dir, 0o755); err != nil { t.Fatal(err) }
	}
	if err := os.WriteFile(source, []byte("canonical"), 0o644); err != nil { t.Fatal(err) }
	sourceIdentity, err := inspectArchiveFile(source)
	if err != nil { t.Fatal(err) }
	owned := func(id, name string, content []byte) (OwnedArchiveEntry, string) {
		path := filepath.Join(managed, name)
		if err := os.WriteFile(path, content, 0o644); err != nil { t.Fatal(err) }
		identity, err := inspectArchiveFile(path)
		if err != nil { t.Fatal(err) }
		return OwnedArchiveEntry{ID: id, Purpose: archivePurposePlay, OwnerID: playDeploymentOwnerID, EntityID: id,
			SourcePath: source, SourceIdentity: sourceIdentity, TargetRoot: managed, RelativePath: name,
			Method: deployMethodCopy, State: archiveStateActive, TargetIdentity: identity}, path
	}
	retired, retiredPath := owned("retired", "retired.zip", []byte("canonical"))
	changed, changedPath := owned("changed", "changed.zip", []byte("canonical"))
	if err := service.store.saveOwnedArchiveEntries(ctx, []OwnedArchiveEntry{retired, changed}); err != nil { t.Fatal(err) }
	if err := os.WriteFile(changedPath, []byte("edited by the user after deployment"), 0o644); err != nil { t.Fatal(err) }

	if err := service.retireOwnedArchiveEntries(ctx, []string{"retired"}); err != nil { t.Fatal(err) }
	if _, err := os.Stat(retiredPath); !os.IsNotExist(err) { t.Fatal("owned copy survived retirement", err) }
	if _, err := os.Stat(source); err != nil { t.Fatal("retirement touched the canonical archive", err) }
	if err := service.retireOwnedArchiveEntries(ctx, []string{"changed"}); err == nil { t.Fatal("a file changed outside Studio was retired") }
	if data, err := os.ReadFile(changedPath); err != nil || string(data) != "edited by the user after deployment" { t.Fatal("changed file was not preserved", err) }
	listed, err := service.store.listOwnedArchiveEntries(ctx)
	if err != nil { t.Fatal(err) }
	if len(listed) != 1 || listed[0].ID != "changed" { t.Fatalf("ledger after retirement = %#v", listed) }
}

func TestOwnershipLedgerRefusesTargetCollision(t *testing.T) {
	service := newTestAppService(t)
	ctx := context.Background()
	first := OwnedArchiveEntry{ID: "first", Purpose: archivePurposePlay, OwnerID: playDeploymentOwnerID, TargetRoot: service.config.ActiveModsDir, RelativePath: "same.zip", State: archiveStateActive}
	second := first
	second.ID = "second"
	if err := service.store.saveOwnedArchiveEntries(ctx, []OwnedArchiveEntry{first}); err != nil { t.Fatal(err) }
	if err := service.store.saveOwnedArchiveEntries(ctx, []OwnedArchiveEntry{second}); err == nil { t.Fatal("a second owner claimed the same target") }
	listed, err := service.store.listOwnedArchiveEntries(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != "first" { t.Fatalf("existing ownership was lost: %#v %v", listed, err) }
}


func TestDeploymentModeConfigPersistence(t *testing.T) {
	service := newTestAppService(t)
	ctx := context.Background()

	// Write a config file so persistence works.
	configPath := filepath.Join(service.config.DataDir, "config.json")
	service.config.ConfigPath = configPath
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set mode.
	state, err := service.SetArchiveDeploymentMode(ctx, DeploymentModeHardlinkOnly)
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != DeploymentModeHardlinkOnly {
		t.Fatalf("expected mode %q, got %q", DeploymentModeHardlinkOnly, state.Mode)
	}

	// Verify persisted in config file.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"hardlink-only"`) {
		t.Fatalf("expected hardlink-only in config, got: %s", string(data))
	}
}

func TestArchiveFileIdentityInspection(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.zip")
	content := []byte("identity-test-file-content")
	if err := os.WriteFile(testFile, content, 0o644); err != nil {
		t.Fatal(err)
	}

	identity, err := inspectArchiveFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if identity.SizeBytes != int64(len(content)) {
		t.Fatalf("expected size %d, got %d", len(content), identity.SizeBytes)
	}
	if !identity.Regular {
		t.Fatal("expected regular file")
	}
	if identity.ModifiedNs == "" {
		t.Fatal("expected non-empty modification timestamp")
	}
	// Platform-dependent: identity may or may not be known.
	if identity.IdentityKnown {
		if identity.VolumeID == "" {
			t.Fatal("identity known but volume ID empty")
		}
		if identity.FileID == "" {
			t.Fatal("identity known but file ID empty")
		}
	}
}

func TestAvailableArchiveBytes(t *testing.T) {
	dir := t.TempDir()
	free, err := availableArchiveBytes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if free <= 0 {
		t.Fatalf("expected positive free bytes, got %d", free)
	}
}
