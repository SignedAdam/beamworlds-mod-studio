package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveSetupPersistsValidatedPaths(t *testing.T) {
	service := newTestAppService(t)
	setupHome := t.TempDir()
	t.Setenv("BEAMWORLDS_HOME", setupHome)
	root := t.TempDir()
	beamNGRoot := filepath.Join(root, "BeamNG User")
	activeModsDir := filepath.Join(beamNGRoot, "current", "mods")
	libraryDir := filepath.Join(root, "Mod Library")
	gameInstallDir := filepath.Join(root, "BeamNG.drive")
	dataDir := filepath.Join(root, "BeamWorlds Data")
	additionalRoot := filepath.Join(root, "Archive Shelf")
	for _, directory := range []string{beamNGRoot, activeModsDir, libraryDir, filepath.Join(gameInstallDir, "Bin64"), additionalRoot} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(gameInstallDir, "Bin64", "BeamNG.drive.x64.exe"), []byte("test executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := service.SaveSetup(SetupInput{
		BeamNGRoot: beamNGRoot, ActiveModsDir: activeModsDir, LibraryDir: libraryDir,
		GameInstallDir: gameInstallDir, DataDir: dataDir, AdditionalScanRoots: []string{additionalRoot},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.RestartRequired || !result.Config.SetupComplete || !samePath(result.Config.BeamNGRoot, beamNGRoot) || !samePath(result.Config.DataDir, dataDir) {
		t.Fatalf("saved setup result = %#v", result)
	}
	payload, err := os.ReadFile(filepath.Join(setupHome, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]json.RawMessage
	if err := json.Unmarshal(payload, &persisted); err != nil {
		t.Fatal(err)
	}
	if _, exists := persisted["databasePath"]; exists {
		t.Fatalf("derived machine state leaked into config: %s", payload)
	}
	loaded, err := LoadAppConfig()
	if err != nil {
		t.Fatal(err)
	}
	if setupRequired(loaded) || !samePath(loaded.ActiveModsDir, activeModsDir) || !samePath(loaded.GameExecutable, filepath.Join(gameInstallDir, "Bin64", "BeamNG.drive.x64.exe")) {
		t.Fatalf("reloaded setup = %#v", loaded)
	}
	if _, err := service.SaveSetup(SetupInput{
		BeamNGRoot: beamNGRoot, ActiveModsDir: activeModsDir, LibraryDir: libraryDir,
		GameInstallDir: gameInstallDir, DataDir: libraryDir,
	}); err == nil {
		t.Fatal("setup accepted overlapping Studio storage and mod library")
	}
}

func TestBeamNGPathHintsAndNativeCounts(t *testing.T) {
	hints := parseBeamNGPathHints("\ufeffversion = 0.39.4.0\nuserFolder = X:\\Games\\BeamNG\ninstallPath = X:\\Steam\\BeamNG.drive\\\n")
	if hints.Version != "0.39.4.0" || hints.UserFolder == "" || hints.InstallPath == "" {
		t.Fatalf("parsed hints = %#v", hints)
	}
	activeModsDir := t.TempDir()
	payload := []byte(`{"header":{"version":1.1},"mods":{"one":{"active":true},"two":{"active":false},"three":{}}}`)
	if err := os.WriteFile(filepath.Join(activeModsDir, "db.json"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	mods, enabled := beamNGNativeModCounts(activeModsDir)
	if mods != 3 || enabled != 2 {
		t.Fatalf("native counts = %d/%d, want 3/2", mods, enabled)
	}
}

func TestBeamNGModStateAppliesExactSelectionAndRestores(t *testing.T) {
	activeModsDir := t.TempDir()
	selectedPath := filepath.Join(activeModsDir, "selected.zip")
	otherPath := filepath.Join(activeModsDir, "repo", "other.zip")
	if err := os.MkdirAll(filepath.Dir(otherPath), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{selectedPath, otherPath} {
		if err := os.WriteFile(filename, []byte("zip placeholder"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	selectedKey, err := beamNGModKey(selectedPath, activeModsDir)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := beamNGModKey(otherPath, activeModsDir)
	if err != nil {
		t.Fatal(err)
	}
	database := map[string]any{"header": map[string]any{"version": 1.1}, "mods": map[string]any{
		selectedKey: map[string]any{"active": false, "fullpath": "/mods/selected.zip"},
		otherKey:    map[string]any{"active": true, "fullpath": "/mods/repo/other.zip"},
	}}
	payload, err := json.Marshal(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activeModsDir, "db.json"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyBeamNGModSelection(activeModsDir, []string{selectedKey}); err != nil {
		t.Fatal(err)
	}
	assertNativeModState(t, activeModsDir, selectedKey, true)
	assertNativeModState(t, activeModsDir, otherKey, false)
	if !hasOriginalBeamNGModDatabase(activeModsDir) {
		t.Fatal("original BeamNG mod state was not preserved")
	}
	if err := restoreOriginalBeamNGModDatabase(activeModsDir); err != nil {
		t.Fatal(err)
	}
	if hasOriginalBeamNGModDatabase(activeModsDir) {
		t.Fatal("original state marker remains after restoration")
	}
	assertNativeModState(t, activeModsDir, selectedKey, false)
	assertNativeModState(t, activeModsDir, otherKey, true)
	if err := os.WriteFile(filepath.Join(activeModsDir, "unregistered.zip"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyBeamNGModSelection(activeModsDir, []string{selectedKey}); err == nil {
		t.Fatal("unregistered inactive archive was silently enabled by BeamNG defaults")
	}
}

func TestEmptyBeamNGModStateRestoresWithoutDatabase(t *testing.T) {
	activeModsDir := t.TempDir()
	if err := applyBeamNGModSelection(activeModsDir, []string{"beamworlds-managedexample"}); err != nil {
		t.Fatal(err)
	}
	if !hasOriginalBeamNGModDatabase(activeModsDir) {
		t.Fatal("missing-database state was not preserved")
	}
	if err := os.WriteFile(filepath.Join(activeModsDir, "db.json"), []byte(`{"header":{"version":1.1},"mods":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := restoreOriginalBeamNGModDatabase(activeModsDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(activeModsDir, "db.json")); !os.IsNotExist(err) {
		t.Fatalf("BeamNG database created during profile use was not removed: %v", err)
	}
}

func TestMissingBeamNGDatabaseKeepsEarlierOriginalState(t *testing.T) {
	activeModsDir := t.TempDir()
	databasePath := filepath.Join(activeModsDir, "db.json")
	original := []byte(`{"header":{"version":1.1},"mods":{"normal":{"active":true}}}`)
	if err := os.WriteFile(databasePath+originalModDBSuffix, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyBeamNGModSelection(activeModsDir, nil); err != nil {
		t.Fatal(err)
	}
	preserved, err := os.ReadFile(databasePath + originalModDBSuffix)
	if err != nil || string(preserved) != string(original) {
		t.Fatalf("earlier original state was overwritten: %s, err %v", preserved, err)
	}
	if err := restoreOriginalBeamNGModDatabase(activeModsDir); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(databasePath)
	if err != nil || string(restored) != string(original) {
		t.Fatalf("earlier original state was not restored: %s, err %v", restored, err)
	}
}

func assertNativeModState(t *testing.T, activeModsDir, key string, want bool) {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(activeModsDir, "db.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Mods map[string]struct {
			Active bool `json:"active"`
		} `json:"mods"`
	}
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	if got := document.Mods[key].Active; got != want {
		t.Fatalf("mod %s active = %t, want %t; db=%s", key, got, want, payload)
	}
}
