package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- helpers ----------------------------------------------------------------

// setupPlayProfileDirs creates a temp BeamNG root with current/mods and a few
// real directories plus a db.json, then returns the root and service.
func setupPlayProfileDirs(t *testing.T) (*AppService, string) {
	t.Helper()
	service := newTestAppService(t)
	// Ensure LibraryDir is set for From BeamNG operations.
	if service.config.LibraryDir == "" {
		service.config.LibraryDir = filepath.Join(service.config.DataDir, "library")
		if err := os.MkdirAll(service.config.LibraryDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	beamNGRoot := service.config.BeamNGRoot
	realCurrent := filepath.Join(beamNGRoot, "current")
	for _, dir := range []string{
		filepath.Join(realCurrent, "mods"),
		filepath.Join(realCurrent, "settings"),
		filepath.Join(realCurrent, "career"),
		filepath.Join(realCurrent, "vehicles"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Write a sentinel file in settings to verify junction sharing.
	if err := os.WriteFile(filepath.Join(realCurrent, "settings", "test.json"), []byte(`{"sentinel":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return service, beamNGRoot
}

// --- tests ------------------------------------------------------------------

func TestPlayProfileRealModsFolderUntouchedAfterPlay(t *testing.T) {
	service, _ := setupPlayProfileDirs(t)
	realModsDir := service.config.ActiveModsDir

	// Place extra zips and a db.json in the real mods folder.
	extraZip := filepath.Join(realModsDir, "user-mod.zip")
	if err := os.WriteFile(extraZip, []byte("user mod data"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPayload := []byte(`{"header":{"version":1.1},"mods":{"user-mod":{"active":true}}}`)
	dbPath := filepath.Join(realModsDir, "db.json")
	if err := os.WriteFile(dbPath, dbPayload, 0o644); err != nil {
		t.Fatal(err)
	}

	// Snapshot real mods folder state.
	realModsBefore, _ := os.ReadDir(realModsDir)
	dbBefore, _ := os.ReadFile(dbPath)
	extraBefore, _ := os.ReadFile(extraZip)

	// Create a collection and activate it.
	_, collectionID := scanAndCreateCollection(t, service, "ProfileTest", 2, 9000)
	request := resolveAndFingerprint(t, service, collectionID)
	activation := activateAndCheck(t, service, request)

	// Verify: profile mods contains exactly the selection.
	profileEntries, err := os.ReadDir(activation.ModsPath)
	if err != nil {
		t.Fatal(err)
	}
	zipCount := 0
	for _, e := range profileEntries {
		if strings.HasSuffix(strings.ToLower(e.Name()), ".zip") {
			zipCount++
		}
	}
	if zipCount != 2 {
		t.Fatalf("profile mods has %d zips, want 2", zipCount)
	}

	// Verify: no profile db.json.
	if _, err := os.Stat(filepath.Join(activation.ModsPath, "db.json")); !os.IsNotExist(err) {
		t.Fatalf("profile db.json exists after Play: %v", err)
	}

	// Verify: real mods folder is byte-identical.
	realModsAfter, _ := os.ReadDir(realModsDir)
	if len(realModsBefore) != len(realModsAfter) {
		t.Fatalf("real mods folder entry count changed: %d → %d", len(realModsBefore), len(realModsAfter))
	}
	dbAfter, _ := os.ReadFile(dbPath)
	if !bytes.Equal(dbBefore, dbAfter) {
		t.Fatalf("real db.json changed:\n before: %s\n after:  %s", dbBefore, dbAfter)
	}
	extraAfter, _ := os.ReadFile(extraZip)
	if !bytes.Equal(extraBefore, extraAfter) {
		t.Fatalf("extra zip in real mods changed")
	}
}

func TestPlayProfileHasJunctionsForRealDirectories(t *testing.T) {
	service, beamNGRoot := setupPlayProfileDirs(t)
	realCurrent := filepath.Join(beamNGRoot, "current")

	// Sync the profile.
	service.modImportMu.Lock()
	if err := service.syncPlayProfile(context.Background()); err != nil {
		service.modImportMu.Unlock()
		t.Fatal(err)
	}
	service.modImportMu.Unlock()

	playRoot, err := playUserPath(service.config)
	if err != nil {
		t.Fatal(err)
	}
	playCurrent := filepath.Join(playRoot, "current")

	// Check that settings, career, vehicles are junctions.
	for _, name := range []string{"settings", "career", "vehicles"} {
		linkPath := filepath.Join(playCurrent, name)
		if !isDirectoryJunction(linkPath) {
			t.Fatalf("%s is not a junction", name)
		}
		target := junctionTarget(linkPath)
		realDir := filepath.Join(realCurrent, name)
		if !samePath(target, realDir) {
			t.Fatalf("%s junction points to %s, want %s", name, target, realDir)
		}
	}

	// Verify settings written through the profile land in the real folder.
	profileSettings := filepath.Join(playCurrent, "settings", "test.json")
	data, err := os.ReadFile(profileSettings)
	if err != nil {
		t.Fatalf("cannot read settings through profile junction: %v", err)
	}
	if !bytes.Contains(data, []byte("sentinel")) {
		t.Fatalf("settings content through junction does not match: %s", data)
	}

	// Write through junction and verify in real folder.
	if err := os.WriteFile(filepath.Join(playCurrent, "settings", "new.json"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(realCurrent, "settings", "new.json")); err != nil {
		t.Fatalf("write through junction did not land in real folder: %v", err)
	}

	// mods must NOT be a junction.
	modsPath := filepath.Join(playCurrent, "mods")
	if isDirectoryJunction(modsPath) {
		t.Fatal("mods directory is a junction; it should be a real directory")
	}
}

func TestPlayProfileSessionDownloadMovedToRealMods(t *testing.T) {
	service, _ := setupPlayProfileDirs(t)
	playRoot, err := playUserPath(service.config)
	if err != nil {
		t.Fatal(err)
	}

	// Place a session download in profile mods.
	profileMods := playProfileModsDir(playRoot)
	if err := os.MkdirAll(filepath.Join(profileMods, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	download := filepath.Join(profileMods, "repo", "session-download.zip")
	if err := os.WriteFile(download, []byte("downloaded mod data"), 0o644); err != nil {
		t.Fatal(err)
	}

	service.modImportMu.Lock()
	harvested, err := service.reconcileProfileSessionDownloads(context.Background(), playRoot)
	service.modImportMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if harvested != 1 {
		t.Fatalf("harvested %d, want 1", harvested)
	}

	// Verify: download is now in real mods at same relative path.
	realDest := filepath.Join(service.config.ActiveModsDir, "repo", "session-download.zip")
	data, err := os.ReadFile(realDest)
	if err != nil {
		t.Fatalf("download not moved to real mods: %v", err)
	}
	if string(data) != "downloaded mod data" {
		t.Fatalf("moved data mismatch: %s", data)
	}

	// Profile copy should be gone.
	if _, err := os.Stat(download); !os.IsNotExist(err) {
		t.Fatalf("profile download was not removed: %v", err)
	}
}

func TestPlayProfileRepoUpdateReplaces(t *testing.T) {
	service, _ := setupPlayProfileDirs(t)
	playRoot, err := playUserPath(service.config)
	if err != nil {
		t.Fatal(err)
	}

	// Place old version in real mods/repo.
	if err := os.MkdirAll(filepath.Join(service.config.ActiveModsDir, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(service.config.ActiveModsDir, "repo", "updated.zip")
	if err := os.WriteFile(realPath, []byte("old version"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Place new version as session download in profile mods/repo.
	profileMods := playProfileModsDir(playRoot)
	if err := os.MkdirAll(filepath.Join(profileMods, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(profileMods, "repo", "updated.zip")
	if err := os.WriteFile(profilePath, []byte("new version"), 0o644); err != nil {
		t.Fatal(err)
	}

	service.modImportMu.Lock()
	_, err = service.reconcileProfileSessionDownloads(context.Background(), playRoot)
	service.modImportMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	// New version should be at real path.
	data, err := os.ReadFile(realPath)
	if err != nil || string(data) != "new version" {
		t.Fatalf("repo update not installed: %v data=%s", err, data)
	}

	// Old version preserved in From BeamNG.
	fromDir := filepath.Join(service.config.LibraryDir, "From BeamNG")
	entries, err := os.ReadDir(fromDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("old version not preserved: %v entries=%d", err, len(entries))
	}
	preserved := false
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(fromDir, e.Name()))
		if string(data) == "old version" {
			preserved = true
			break
		}
	}
	if !preserved {
		t.Fatal("old version bytes not found in From BeamNG")
	}
}

func TestPlayProfileNonRepoCollisionGoesToFromBeamNG(t *testing.T) {
	service, _ := setupPlayProfileDirs(t)
	playRoot, err := playUserPath(service.config)
	if err != nil {
		t.Fatal(err)
	}

	// Place a file in real mods root.
	realPath := filepath.Join(service.config.ActiveModsDir, "collision.zip")
	if err := os.WriteFile(realPath, []byte("real version"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Place different version in profile mods root (not repo/).
	profileMods := playProfileModsDir(playRoot)
	if err := os.MkdirAll(profileMods, 0o755); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(profileMods, "collision.zip")
	if err := os.WriteFile(profilePath, []byte("profile version"), 0o644); err != nil {
		t.Fatal(err)
	}

	service.modImportMu.Lock()
	_, err = service.reconcileProfileSessionDownloads(context.Background(), playRoot)
	service.modImportMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	// Real file stays unchanged.
	data, _ := os.ReadFile(realPath)
	if string(data) != "real version" {
		t.Fatalf("real file changed: %s", data)
	}

	// Profile copy went to From BeamNG.
	fromDir := filepath.Join(service.config.LibraryDir, "From BeamNG")
	entries, _ := os.ReadDir(fromDir)
	if len(entries) == 0 {
		t.Fatal("profile copy not moved to From BeamNG")
	}
}

func TestPlayProfileDirCreatedOnlyInProfile(t *testing.T) {
	service, beamNGRoot := setupPlayProfileDirs(t)
	playRoot, err := playUserPath(service.config)
	if err != nil {
		t.Fatal(err)
	}

	// Sync the profile first to create initial junctions.
	service.modImportMu.Lock()
	if err := service.syncPlayProfile(context.Background()); err != nil {
		service.modImportMu.Unlock()
		t.Fatal(err)
	}
	service.modImportMu.Unlock()

	// Simulate BeamNG creating a "replays" directory in the profile.
	replaysDir := filepath.Join(playRoot, "current", "replays")
	if err := os.MkdirAll(replaysDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replaysDir, "test.replay"), []byte("replay"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Sync again — it should migrate "replays" into the real folder and junction it.
	service.modImportMu.Lock()
	if err := service.syncPlayProfile(context.Background()); err != nil {
		service.modImportMu.Unlock()
		t.Fatal(err)
	}
	service.modImportMu.Unlock()

	// The real replays directory should exist.
	realReplays := filepath.Join(beamNGRoot, "current", "replays")
	if _, err := os.Stat(filepath.Join(realReplays, "test.replay")); err != nil {
		t.Fatalf("replay not migrated to real folder: %v", err)
	}

	// The profile path should be a junction now.
	if !isDirectoryJunction(replaysDir) {
		t.Fatal("replays is not junctioned after migration")
	}
}

func TestCutoverLegacyManagedDir(t *testing.T) {
	service, _ := setupPlayProfileDirs(t)
	managedDir := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
	if err := os.MkdirAll(managedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Place a sole-copy zip in managed dir.
	if err := os.WriteFile(filepath.Join(managedDir, "legacy.zip"), []byte("legacy data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a db.json with a managed entry and a normal entry.
	dbPayload, _ := json.Marshal(map[string]any{
		"header": map[string]any{"version": 1.1},
		"mods": map[string]any{
			"beamworlds-managedlegacy": map[string]any{"active": true, "fullpath": "/mods/beamworlds-managed/legacy.zip"},
			"user-mod":                map[string]any{"active": false, "fullpath": "/mods/user-mod.zip"},
		},
	})
	dbPath := filepath.Join(service.config.ActiveModsDir, "db.json")
	if err := os.WriteFile(dbPath, dbPayload, 0o644); err != nil {
		t.Fatal(err)
	}

	// Create an original snapshot that has user-mod as active.
	origPayload, _ := json.Marshal(map[string]any{
		"header": map[string]any{"version": 1.1},
		"mods": map[string]any{
			"user-mod": map[string]any{"active": true},
		},
	})
	if err := os.WriteFile(dbPath+".beamworlds-original", origPayload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := service.writePlayRuntimeMarker(PlayActivation{OperationID: "legacy", ModsPath: managedDir, UserPath: service.config.BeamNGRoot}); err != nil {
		t.Fatal(err)
	}

	service.modImportMu.Lock()
	if err := service.cutoverLegacyManagedDir(context.Background()); err != nil {
		service.modImportMu.Unlock()
		t.Fatal(err)
	}
	service.modImportMu.Unlock()

	// Managed dir should be gone.
	if _, err := os.Stat(managedDir); !os.IsNotExist(err) {
		t.Fatalf("managed dir still exists: %v", err)
	}
	if _, exists, err := service.readPlayRuntimeMarker(); err != nil || exists {
		t.Fatalf("legacy Play marker survived cutover (exists=%v, err=%v); Play would report missing managed files", exists, err)
	}

	// Legacy zip preserved in From BeamNG.
	fromDir := filepath.Join(service.config.LibraryDir, "From BeamNG")
	entries, _ := os.ReadDir(fromDir)
	found := false
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(fromDir, e.Name()))
		if string(data) == "legacy data" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("legacy zip not preserved")
	}

	// db.json: managed entry dropped, user-mod active restored.
	data, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Mods map[string]struct {
			Active bool `json:"active"`
		} `json:"mods"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if _, exists := doc.Mods["beamworlds-managedlegacy"]; exists {
		t.Fatal("managed entry not dropped from db.json")
	}
	if !doc.Mods["user-mod"].Active {
		t.Fatal("user-mod active state not restored")
	}

	// Original and backup files should be cleaned up.
	if _, err := os.Stat(dbPath + ".beamworlds-original"); !os.IsNotExist(err) {
		t.Fatal("original snapshot not cleaned up")
	}
}

func TestJunctionRemovalPreservesTargetContents(t *testing.T) {
	target := t.TempDir()
	sentinel := filepath.Join(target, "important.txt")
	if err := os.WriteFile(sentinel, []byte("do not delete"), 0o644); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(t.TempDir(), "junction")
	if err := createDirectoryJunction(link, target); err != nil {
		t.Skipf("junctions not supported: %v", err)
	}

	// Verify the junction works.
	data, err := os.ReadFile(filepath.Join(link, "important.txt"))
	if err != nil || string(data) != "do not delete" {
		t.Fatalf("junction not working: %v, data=%s", err, data)
	}

	// Remove the junction (link-only).
	if err := removeDirectoryJunction(link); err != nil {
		t.Fatal(err)
	}

	// The link should be gone.
	if _, err := os.Stat(link); !os.IsNotExist(err) {
		t.Fatalf("junction still exists: %v", err)
	}

	// The target contents must be intact.
	data, err = os.ReadFile(sentinel)
	if err != nil || string(data) != "do not delete" {
		t.Fatalf("target contents were deleted when junction was removed: %v, data=%s", err, data)
	}
}

func TestJunctionWithSpaceInTargetPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "path with spaces", "target dir")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "data.txt"), []byte("spaced"), 0o644); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(dir, "link")
	if err := createDirectoryJunction(link, target); err != nil {
		t.Skipf("junctions not supported: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(link, "data.txt"))
	if err != nil || string(data) != "spaced" {
		t.Fatalf("junction to spaced path failed: err=%v data=%s", err, data)
	}

	// Cleanup: remove junction, verify target intact.
	if err := removeDirectoryJunction(link); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(target, "data.txt"))
	if err != nil || string(data) != "spaced" {
		t.Fatalf("target contents deleted: err=%v", err)
	}
}

func TestPlayUserPathRejectsSpaces(t *testing.T) {
	// Both candidates must have spaces; don't provide a LibraryDir so the
	// volume-root fallback doesn't kick in.
	config := AppConfig{
		BeamNGRoot: filepath.Join(t.TempDir(), "path with spaces"),
	}
	_, err := playUserPath(config)
	if err == nil {
		t.Fatal("expected error for paths with spaces")
	}
}

func TestPlayUserPathSelectsFirstCandidate(t *testing.T) {
	root := t.TempDir()
	config := AppConfig{
		BeamNGRoot: root,
		LibraryDir: filepath.Join(root, "library"),
	}
	path, err := playUserPath(config)
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(root, ".beamworlds-play")
	if !samePath(path, expected) {
		t.Fatalf("play user path = %s, want %s", path, expected)
	}
}

func TestPlayProfileIdenticalDownloadRemoved(t *testing.T) {
	service, _ := setupPlayProfileDirs(t)
	playRoot, err := playUserPath(service.config)
	if err != nil {
		t.Fatal(err)
	}

	// Place identical file in both real and profile mods.
	content := []byte("identical content")
	realPath := filepath.Join(service.config.ActiveModsDir, "same.zip")
	if err := os.WriteFile(realPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	profileMods := playProfileModsDir(playRoot)
	if err := os.MkdirAll(profileMods, 0o755); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(profileMods, "same.zip")
	if err := os.WriteFile(profilePath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	service.modImportMu.Lock()
	_, err = service.reconcileProfileSessionDownloads(context.Background(), playRoot)
	service.modImportMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	// Profile copy removed; real copy unchanged.
	if _, err := os.Stat(profilePath); !os.IsNotExist(err) {
		t.Fatalf("identical profile copy not removed: %v", err)
	}
	data, _ := os.ReadFile(realPath)
	if !bytes.Equal(data, content) {
		t.Fatal("real copy changed")
	}
}

func TestDirectDeploymentUsesProfileDirectory(t *testing.T) {
	service := newTestAppService(t)
	_, collectionID := scanAndCreateCollection(t, service, "ProfileDest", 1, 8001)
	request := resolveAndFingerprint(t, service, collectionID)

	activation := activateAndCheck(t, service, request)

	playRoot, err := playUserPath(service.config)
	if err != nil {
		t.Fatal(err)
	}
	expectedModsDir := playProfileModsDir(playRoot)
	if !samePath(activation.ModsPath, expectedModsDir) {
		t.Fatalf("deployment went to %s, expected %s", activation.ModsPath, expectedModsDir)
	}
	if !samePath(activation.UserPath, playRoot) {
		t.Fatalf("UserPath = %s, expected %s", activation.UserPath, playRoot)
	}

	// The old managed directory should not exist.
	managedDir := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
	if _, err := os.Stat(managedDir); !os.IsNotExist(err) {
		t.Fatalf("old managed directory exists: %v", err)
	}

	// Verify profile mods has at least one deployed file.
	entries, err := os.ReadDir(activation.ModsPath)
	if err != nil {
		t.Fatal(err)
	}
	zips := 0
	for _, e := range entries {
		if strings.HasSuffix(strings.ToLower(e.Name()), ".zip") {
			zips++
		}
	}
	if zips != 1 {
		t.Fatalf("expected 1 zip in profile mods, got %d (entries: %v)", zips, func() []string {
			names := make([]string, len(entries))
			for i, e := range entries {
				names[i] = e.Name()
			}
			return names
		}())
	}
}

// Test that ensureJunction replaces a wrong-target junction.
func TestEnsureJunctionReplacesWrongTarget(t *testing.T) {
	dir := t.TempDir()
	target1 := filepath.Join(dir, "target1")
	target2 := filepath.Join(dir, "target2")
	link := filepath.Join(dir, "link")
	for _, d := range []string{target1, target2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(target1, "a.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target2, "b.txt"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureJunction(link, target1); err != nil {
		t.Skipf("junctions not supported: %v", err)
	}
	// Read through junction.
	if _, err := os.Stat(filepath.Join(link, "a.txt")); err != nil {
		t.Fatal(err)
	}

	// Replace with target2.
	if err := ensureJunction(link, target2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(link, "b.txt")); err != nil {
		t.Fatalf("junction not replaced: %v", err)
	}
	// target1 contents intact.
	if _, err := os.Stat(filepath.Join(target1, "a.txt")); err != nil {
		t.Fatalf("target1 contents were deleted: %v", err)
	}
}

func TestEnsureJunctionKeepsCorrectLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "settings")
	link := filepath.Join(dir, "link")
	if err := os.MkdirAll(target, 0o755); err != nil { t.Fatal(err) }
	if err := ensureJunction(link, target); err != nil { t.Skipf("junctions not supported: %v", err) }
	before, err := os.Lstat(link)
	if err != nil { t.Fatal(err) }
	if err := ensureJunction(link, target); err != nil { t.Fatal(err) }
	after, err := os.Lstat(link)
	if err != nil { t.Fatal(err) }
	if !os.SameFile(before, after) { t.Fatal("a junction already pointing at its target was torn down and recreated") }
}

// Suppress unused import warning for fmt.
var _ = fmt.Sprintf
