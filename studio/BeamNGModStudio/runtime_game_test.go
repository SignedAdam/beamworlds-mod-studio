package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWorkspaceGameTestRejectsWithoutGameExecutable(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	service.config.GameExecutable = ""
	_, err := service.runWorkspaceGameTest(context.Background(), "ws-1", WorkspaceGameTestOptions{})
	if err == nil || !strings.Contains(err.Error(), "executable") {
		t.Fatalf("expected executable error, got: %v", err)
	}
}

func TestRunWorkspaceGameTestRejectsEmptyWorkspace(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	service.config.GameExecutable = "test.exe"
	_, err := service.runWorkspaceGameTest(context.Background(), "", WorkspaceGameTestOptions{})
	if err == nil || !strings.Contains(err.Error(), "workspace ID") {
		t.Fatalf("expected workspace ID error, got: %v", err)
	}
}

func TestRunWorkspaceGameTestRejectsWhileGameRunning(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	service.config.GameExecutable = "test.exe"
	service.gameRunning = func() (bool, error) { return true, nil }
	_, err := service.runWorkspaceGameTest(context.Background(), "ws-1", WorkspaceGameTestOptions{})
	if err == nil || !strings.Contains(err.Error(), "close BeamNG") {
		t.Fatalf("expected game-running error, got: %v", err)
	}
}

func TestRunWorkspaceGameTestRejectsConcurrentLaunch(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	service.config.GameExecutable = "test.exe"
	service.profileMu.Lock()
	defer service.profileMu.Unlock()
	_, err := service.runWorkspaceGameTest(context.Background(), "ws-1", WorkspaceGameTestOptions{})
	if err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("expected concurrent-launch error, got: %v", err)
	}
}

func TestRunWorkspaceGameTestHonoursCancellationBeforeExport(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	service.config.GameExecutable = "test.exe"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := service.runWorkspaceGameTest(ctx, "ws-1", WorkspaceGameTestOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation before export, got %v", err)
	}
}

func TestEvaluateGameTestResultRequiresFullDuration(t *testing.T) {
	t.Parallel()
	obs := &gameTestObservations{
		LevelLoaded: true, VehicleSpawned: true,
		VehicleName: "pickup", SimSeconds: 4.9,
		Errors: []string{},
	}
	result := evaluateGameTestResult(WorkspaceGameTestResult{Observations: obs}, "pickup", 5)
	if result.ScenarioComplete {
		t.Fatal("scenario should not be complete with only 4.9s of 5s")
	}
	if result.Passed {
		t.Fatal("should not pass with insufficient sim time")
	}
}

func TestEvaluateGameTestResultRequiresPositiveVehicleIdentity(t *testing.T) {
	t.Parallel()
	obs := &gameTestObservations{
		LevelLoaded: true, VehicleSpawned: true,
		VehicleName: "", SimSeconds: 6.0,
		Errors: []string{},
	}
	result := evaluateGameTestResult(WorkspaceGameTestResult{Observations: obs}, "pickup", 5)
	if result.ScenarioComplete {
		t.Fatal("scenario should not complete with empty vehicle identity")
	}
	if !strings.Contains(result.Error, "could not be identified") {
		t.Fatalf("expected identity error, got: %q", result.Error)
	}
}

func TestEvaluateGameTestResultRejectsVehicleMismatch(t *testing.T) {
	t.Parallel()
	obs := &gameTestObservations{
		LevelLoaded: true, VehicleSpawned: true,
		VehicleName: "covet", SimSeconds: 6.0,
		Errors: []string{},
	}
	result := evaluateGameTestResult(WorkspaceGameTestResult{Observations: obs}, "pickup", 5)
	if result.Passed {
		t.Fatal("should fail on vehicle mismatch")
	}
	if !strings.Contains(result.Error, "expected vehicle") {
		t.Fatalf("expected mismatch error, got: %q", result.Error)
	}
}

func TestEvaluateGameTestResultRejectsLogErrors(t *testing.T) {
	t.Parallel()
	obs := &gameTestObservations{
		LevelLoaded: true, VehicleSpawned: true,
		VehicleName: "pickup", SimSeconds: 6.0,
		Errors: []string{},
	}
	result := WorkspaceGameTestResult{
		Observations: obs,
		Log:          RuntimeLogReadResult{LogPath: "/test/beamng.log", BytesRead: 100, TotalErrors: 3},
	}
	result = evaluateGameTestResult(result, "pickup", 5)
	if result.LogClean {
		t.Fatal("LogClean should be false with 3 errors")
	}
	if result.Passed {
		t.Fatal("should not pass with log errors")
	}
}

func TestEvaluateGameTestResultRejectsNonZeroExit(t *testing.T) {
	t.Parallel()
	obs := &gameTestObservations{
		LevelLoaded: true, VehicleSpawned: true,
		VehicleName: "pickup", SimSeconds: 6.0,
		Errors: []string{},
	}
	result := WorkspaceGameTestResult{
		Observations: obs,
		ExitCode:     1,
	}
	result = evaluateGameTestResult(result, "pickup", 5)
	if result.Passed {
		t.Fatal("should not pass with exit code 1")
	}
}

func TestEvaluateGameTestResultRejectsHarnessErrors(t *testing.T) {
	t.Parallel()
	obs := &gameTestObservations{
		LevelLoaded: true, VehicleSpawned: true,
		VehicleName: "pickup", SimSeconds: 6.0,
		Errors: []string{"safety timeout 180s"},
	}
	result := evaluateGameTestResult(WorkspaceGameTestResult{Observations: obs}, "pickup", 5)
	if result.ScenarioComplete {
		t.Fatal("scenario should not complete with harness errors")
	}
}

func TestEvaluateGameTestResultRejectsMissingObservations(t *testing.T) {
	t.Parallel()
	result := evaluateGameTestResult(WorkspaceGameTestResult{}, "pickup", 5)
	if result.Passed {
		t.Fatal("should fail without observations")
	}
}

func TestEvaluateGameTestResultRejectsEmptyLog(t *testing.T) {
	t.Parallel()
	obs := &gameTestObservations{
		LevelLoaded: true, VehicleSpawned: true,
		VehicleName: "pickup", SimSeconds: 6.0,
		Errors: []string{},
	}
	result := WorkspaceGameTestResult{
		Observations: obs,
		Log:          RuntimeLogReadResult{LogPath: "", BytesRead: 0},
	}
	result = evaluateGameTestResult(result, "pickup", 5)
	if result.LogClean {
		t.Fatal("LogClean should be false when log could not be read")
	}
	if result.Passed {
		t.Fatal("should not pass with unreadable log")
	}
}

func TestEvaluateGameTestResultPassesCleanRun(t *testing.T) {
	t.Parallel()
	obs := &gameTestObservations{
		HarnessVersion: 1,
		LevelLoaded:    true,
		VehicleSpawned: true,
		VehicleName:    "beamroyale_human",
		TargetVehicle:  "beamroyale_human",
		LevelPath:      "/levels/smallgrid/main.level.json",
		SimSeconds:     5.2,
		Errors:         []string{},
		Stages: []gameTestStageInfo{
			{Stage: "init", At: 0},
			{Stage: "levelLoaded", At: 2.5},
			{Stage: "vehicleSpawned", At: 3.1, Vehicle: "beamroyale_human"},
			{Stage: "simComplete", At: 8.3, SimSeconds: 5.2},
		},
	}
	result := WorkspaceGameTestResult{Observations: obs, GameExited: true, Log: RuntimeLogReadResult{LogPath: "/test/beamng.log", BytesRead: 500, TotalErrors: 0}}
	result = evaluateGameTestResult(result, "beamroyale_human", 5)
	if !result.ScenarioComplete {
		t.Fatalf("scenario should be complete, error: %q", result.Error)
	}
	if !result.LogClean {
		t.Fatal("log should be clean")
	}
	if !result.Passed {
		t.Fatalf("should pass, error: %q", result.Error)
	}
}

func TestReadGameTestObservationsMissingFile(t *testing.T) {
	t.Parallel()
	result := readGameTestObservations(WorkspaceGameTestResult{}, filepath.Join(t.TempDir(), "missing.json"))
	if result.Observations != nil {
		t.Fatal("should be nil when file missing")
	}
	if !strings.Contains(result.Error, "harness did not produce") {
		t.Fatalf("unexpected error: %q", result.Error)
	}
}

func TestDeriveVehicleFromManifestReturnsEmptyOnMissing(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	if v := deriveVehicleFromManifest(context.Background(), service, "nonexistent"); v != "" {
		t.Fatalf("expected empty, got %q", v)
	}
}

func TestEvaluateGameTestResultRejectsIncompleteEvidence(t *testing.T) {
	t.Parallel()
	base := WorkspaceGameTestResult{
		GameExited: true,
		Observations: &gameTestObservations{
			LevelLoaded: true, VehicleSpawned: true, VehicleName: "pickup", SimSeconds: 5,
		},
		Log: RuntimeLogReadResult{LogPath: "/test/beamng.log", BytesRead: 100, ToOffset: 100, FileSize: 100},
	}
	if result := evaluateGameTestResult(base, "pickup", 5); !result.Passed {
		t.Fatalf("complete evidence rejected: %#v", result)
	}
	summary := base
	summary.Log.Bounded = true
	if result := evaluateGameTestResult(summary, "pickup", 5); !result.Passed {
		t.Fatalf("complete log scan rejected because displayed context was limited: %#v", result)
	}
	cases := []struct {
		name   string
		change func(*WorkspaceGameTestResult)
	}{
		{"unfinished log record", func(result *WorkspaceGameTestResult) { result.Log.PartialLine = true }},
		{"unread log tail", func(result *WorkspaceGameTestResult) {
			result.Log.Bounded = true
			result.Log.ToOffset = 50
		}},
		{"missing log start", func(result *WorkspaceGameTestResult) { result.Log.FromOffset = 50 }},
		{"game still running", func(result *WorkspaceGameTestResult) { result.GameExited = false }},
		{"process timeout after observations", func(result *WorkspaceGameTestResult) { result.ExitError = "process timeout" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result := base
			test.change(&result)
			if result = evaluateGameTestResult(result, "pickup", 5); result.Passed {
				t.Fatalf("incomplete test reported success: %#v", result)
			}
		})
	}
}

func TestOversizedGameObservationsCannotReportSuccess(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "observations.json")
	data := `{"levelLoaded":true,"vehicleSpawned":true,"vehicleName":"pickup","simSeconds":10}` +
		strings.Repeat(" ", gameTestMaxObservationBytes)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	result := readGameTestObservations(WorkspaceGameTestResult{
		GameExited: true, Log: RuntimeLogReadResult{LogPath: "/test/beamng.log", BytesRead: 100},
	}, path)
	result = evaluateGameTestResult(result, "pickup", 5)
	if result.Observations != nil || result.ScenarioComplete || result.Passed {
		t.Fatalf("oversized evidence accepted: %#v", result)
	}
}
