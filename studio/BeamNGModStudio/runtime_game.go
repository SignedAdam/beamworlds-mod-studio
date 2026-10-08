package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

//go:embed gametest/harness.lua
var gameTestHarness []byte

type WorkspaceGameTestOptions struct {
	Level           string `json:"level"`
	Vehicle         string `json:"vehicle"`
	Config          string `json:"config"`
	DurationSeconds int    `json:"durationSeconds"`
}

type WorkspaceGameTestResult struct {
	ExportPath   string `json:"exportPath"`
	ExportSHA256 string `json:"exportSha256"`

	Process    ProcessLaunch `json:"process"`
	UserPath   string        `json:"userPath"`
	GameExited bool          `json:"gameExited"`
	ExitCode   int           `json:"exitCode"`
	ExitError  string        `json:"exitError,omitempty"`

	Observations *gameTestObservations `json:"observations,omitempty"`
	Log          RuntimeLogReadResult  `json:"log"`

	LevelLoaded      bool    `json:"levelLoaded"`
	VehicleSpawned   bool    `json:"vehicleSpawned"`
	VehicleName      string  `json:"vehicleName"`
	SimSeconds       float64 `json:"simSeconds"`
	ScenarioComplete bool    `json:"scenarioComplete"`
	LogClean         bool    `json:"logClean"`
	Passed           bool    `json:"passed"`
	Error            string  `json:"error,omitempty"`
}

type gameTestObservations struct {
	HarnessVersion int                 `json:"harnessVersion"`
	LevelLoaded    bool                `json:"levelLoaded"`
	VehicleSpawned bool                `json:"vehicleSpawned"`
	VehicleName    string              `json:"vehicleName"`
	TargetVehicle  string              `json:"targetVehicle"`
	LevelPath      string              `json:"levelPath,omitempty"`
	SimSeconds     float64             `json:"simSeconds"`
	Errors         []string            `json:"errors"`
	Stages         []gameTestStageInfo `json:"stages"`
}

type gameTestStageInfo struct {
	Stage      string  `json:"stage"`
	At         float64 `json:"at"`
	Vehicle    string  `json:"vehicle,omitempty"`
	SimSeconds float64 `json:"simSeconds,omitempty"`
}

const (
	gameTestDefaultLevel        = "smallgrid"
	gameTestDefaultDuration     = 5
	gameTestMaxDuration         = 120
	gameTestProcessTimeout      = 300 * time.Second
	gameTestMaxObservationBytes = 64 << 10
	// BeamNG resolves single underscores to path separators in extension names:
	// modstudio_test_harness → modstudio/test/harness.lua
	gameTestExtensionName = "modstudio_test_harness"
	gameTestObsFilename   = "modstudio_test_observations.json"
)

func (service *AppService) runWorkspaceGameTest(ctx context.Context, workspaceID string, options WorkspaceGameTestOptions) (WorkspaceGameTestResult, error) {
	var result WorkspaceGameTestResult
	if err := ctx.Err(); err != nil {
		return result, err
	}

	if strings.TrimSpace(service.config.GameExecutable) == "" {
		return result, errors.New("BeamNG executable was not found in the configured game install directory")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return result, errors.New("workspace ID is required")
	}

	level := strings.TrimSpace(options.Level)
	if level == "" {
		level = gameTestDefaultLevel
	}
	vehicle := strings.TrimSpace(options.Vehicle)
	vehicleConfig := strings.TrimSpace(options.Config)
	duration := options.DurationSeconds
	if duration <= 0 {
		duration = gameTestDefaultDuration
	}
	if duration > gameTestMaxDuration {
		duration = gameTestMaxDuration
	}

	// Acquire profileMu for game-launch exclusion; reuse existing Play/launch guard.
	if !service.profileMu.TryLock() {
		return result, errors.New("another game launch or Play operation is already in progress")
	}
	defer service.profileMu.Unlock()

	if err := service.requireGameStopped(); err != nil {
		return result, err
	}

	workspaceLock := service.agents.workspaceToolMutex(workspaceID)
	if err := lockMutexContext(ctx, workspaceLock); err != nil {
		return result, fmt.Errorf("acquire workspace lock: %w", err)
	}
	exportResp, err := service.exportWorkspace(ctx, workspaceID, "game-test", "game_test")
	workspaceLock.Unlock()
	if err != nil {
		return result, fmt.Errorf("export workspace: %w", err)
	}
	result.ExportPath = exportResp.Record.Path
	result.ExportSHA256 = exportResp.Record.SHA256

	if err := ctx.Err(); err != nil {
		return result, err
	}

	if vehicle == "" {
		vehicle = deriveVehicleFromManifest(ctx, service, workspaceID)
	}

	// BeamNG rejects -userpath values containing spaces, even when quoted.
	// Keep temporary tests separate from both Studio data and the player's mods.
	userPath, err := createGameTestUserPath(service.config.BeamNGRoot)
	if err != nil {
		return result, err
	}
	defer func() { _ = os.RemoveAll(userPath) }()
	versionDir := filepath.Join(userPath, "current")
	modsDir := filepath.Join(versionDir, "mods")
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		return result, fmt.Errorf("create isolated user folder: %w", err)
	}
	result.UserPath = userPath

	// Install the exported mod ZIP.
	modDest := filepath.Join(modsDir, filepath.Base(exportResp.Record.Path))
	if err := copyFileAtomic(exportResp.Record.Path, modDest); err != nil {
		return result, fmt.Errorf("install mod in isolated folder: %w", err)
	}
	copiedHash, err := modkit.FullSHA256(ctx, modDest)
	if err != nil || !strings.EqualFold(copiedHash, exportResp.Record.SHA256) {
		return result, errors.New("mod copy checksum mismatch in isolated user folder")
	}

	// Install the Lua harness. The extension name modstudio_test_harness resolves
	// to modstudio/test/harness.lua via BeamNG's underscore-to-slash mapping.
	harnessDir := filepath.Join(versionDir, "lua", "ge", "extensions", "modstudio", "test")
	if err := os.MkdirAll(harnessDir, 0o755); err != nil {
		return result, fmt.Errorf("create harness directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(harnessDir, "harness.lua"), gameTestHarness, 0o644); err != nil {
		return result, fmt.Errorf("write harness: %w", err)
	}

	obsPath := filepath.Join(versionDir, gameTestObsFilename)
	logPath := filepath.Join(versionDir, "beamng.log")
	settingsData, err := json.Marshal(struct {
		ObservationPath string `json:"observationPath"`
		DurationSeconds int    `json:"durationSeconds"`
		Vehicle         string `json:"vehicle"`
	}{"/" + gameTestObsFilename, duration, vehicle})
	if err != nil {
		return result, err
	}
	settingsDir := filepath.Join(versionDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "modstudioGameTest.json"), settingsData, 0o600); err != nil {
		return result, err
	}
	args := buildGameTestArgs(userPath, level, vehicle, vehicleConfig)

	// Re-check that the game is still stopped (export can take minutes).
	if err := service.requireGameStopped(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	cmd := exec.Command(service.config.GameExecutable, args...)
	cmd.Dir = service.config.GameInstallDir
	if err := cmd.Start(); err != nil {
		return result, fmt.Errorf("start BeamNG: %w", err)
	}
	result.Process = ProcessLaunch{PID: cmd.Process.Pid, Executable: service.config.GameExecutable, StartedAt: nowUTC()}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case waitErr := <-waitDone:
		result.GameExited = true
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				result.ExitCode = exitErr.ExitCode()
			}
			result.ExitError = waitErr.Error()
		}
	case <-ctx.Done():
		killOwnedProcess(cmd)
		<-waitDone
		result.GameExited = true
		result.ExitError = "test cancelled"
		result.Error = "game test was cancelled"
		result = fillGameTestLog(ctx, result, logPath)
		return result, ctx.Err()
	case <-time.After(gameTestProcessTimeout):
		killOwnedProcess(cmd)
		<-waitDone
		result.GameExited = true
		result.ExitError = "process timeout exceeded"
		result.Error = fmt.Sprintf("BeamNG did not exit within %s", gameTestProcessTimeout)
	}

	result = readGameTestObservations(result, obsPath)
	result = fillGameTestLog(ctx, result, logPath)
	result = evaluateGameTestResult(result, vehicle, duration)
	// Persist test outcome on the export row.
	testSummary := ExportTestSummary{
		TestedAt:         nowUTC(),
		Level:            level,
		Vehicle:          vehicle,
		SimSeconds:       result.SimSeconds,
		ScenarioComplete: result.ScenarioComplete,
		Passed:           result.Passed,
	}
	if result.Observations != nil {
		testSummary.LogErrors = len(result.Observations.Errors)
	}
	if encoded, err := json.Marshal(testSummary); err == nil {
		_, _ = service.store.db.ExecContext(ctx, `UPDATE exports SET test_json=? WHERE id=?`, string(encoded), exportResp.Record.ID)
	}
	return result, nil
}

func buildGameTestArgs(userPath, level, vehicle, vehicleConfig string) []string {
	args := []string{
		"-userpath", filepath.ToSlash(userPath),
		"-level", level,
	}
	args = append(args, "-onLevelLoad_ext", gameTestExtensionName)
	if vehicle != "" {
		args = append(args, "-vehicle", vehicle)
	}
	if vehicleConfig != "" {
		vehicleConfig = strings.TrimPrefix(strings.TrimPrefix(filepath.ToSlash(vehicleConfig), "/"), "vehicles/")
		if !strings.HasSuffix(vehicleConfig, ".pc") {
			vehicleConfig += ".pc"
		}
		if !strings.Contains(vehicleConfig, "/") && vehicle != "" {
			vehicleConfig = vehicle + "/" + vehicleConfig
		}
		args = append(args, "-vehicleConfig", vehicleConfig)
	}
	return args
}

func deriveVehicleFromManifest(ctx context.Context, service *AppService, workspaceID string) string {
	workspace, err := service.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return ""
	}
	item, err := service.store.GetLibraryItem(ctx, workspace.EntityID)
	if err != nil {
		return ""
	}
	for _, variant := range item.Manifest.Variants {
		if variant.Namespace != "" {
			return variant.Namespace
		}
	}
	if vehicles, ok := item.Manifest.Namespaces["vehicles"]; ok && len(vehicles) > 0 {
		return vehicles[0]
	}
	return ""
}

func readGameTestObservations(result WorkspaceGameTestResult, obsPath string) WorkspaceGameTestResult {
	file, err := os.Open(obsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			result.Error = appendGameTestError(result.Error, "harness did not produce observations; game may have crashed or failed to load the extension")
		} else {
			result.Error = appendGameTestError(result.Error, fmt.Sprintf("read observations: %v", err))
		}
		return result
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, gameTestMaxObservationBytes+1))
	if err != nil {
		result.Error = appendGameTestError(result.Error, fmt.Sprintf("read observations: %v", err))
		return result
	}
	if len(data) > gameTestMaxObservationBytes {
		result.Error = appendGameTestError(result.Error, "harness observations exceeded the size limit")
		return result
	}
	var obs gameTestObservations
	if err := json.Unmarshal(data, &obs); err != nil {
		result.Error = appendGameTestError(result.Error, fmt.Sprintf("parse observations: %v", err))
		return result
	}
	result.Observations = &obs
	result.LevelLoaded = obs.LevelLoaded
	result.VehicleSpawned = obs.VehicleSpawned
	result.VehicleName = obs.VehicleName
	result.SimSeconds = obs.SimSeconds
	return result
}

func fillGameTestLog(ctx context.Context, result WorkspaceGameTestResult, logPath string) WorkspaceGameTestResult {
	baseContext := ctx
	if ctx.Err() != nil {
		baseContext = context.Background()
	}
	logCtx, cancel := context.WithTimeout(baseContext, 10*time.Second)
	defer cancel()
	logResult, logErr := readBeamNGRuntimeLog(logCtx, logPath, RuntimeLogReadOptions{FromStart: true, MaxLines: maxLogReadLines})
	if logErr != nil {
		logResult.LogPath = logPath
		result.Error = appendGameTestError(result.Error, fmt.Sprintf("read runtime log: %v", logErr))
	}
	result.Log = logResult
	return result
}

func evaluateGameTestResult(result WorkspaceGameTestResult, expectedVehicle string, requestedDuration int) WorkspaceGameTestResult {
	if result.Observations == nil {
		result.Passed = false
		if result.Error == "" {
			result.Error = "no harness observations; cannot confirm test execution"
		}
		return result
	}
	obs := result.Observations

	// Scenario completion: level loaded, target vehicle positively identified, full simulation duration.
	result.ScenarioComplete = obs.LevelLoaded && obs.VehicleSpawned &&
		obs.VehicleName != "" && obs.SimSeconds >= float64(requestedDuration)

	if !obs.LevelLoaded {
		result.Error = appendGameTestError(result.Error, "level did not load")
	}
	if !obs.VehicleSpawned {
		result.Error = appendGameTestError(result.Error, "vehicle did not spawn")
	}
	if obs.VehicleSpawned && obs.VehicleName == "" {
		result.Error = appendGameTestError(result.Error, "vehicle spawned but could not be identified")
	}
	if expectedVehicle != "" && obs.VehicleName != "" && obs.VehicleName != expectedVehicle {
		result.Error = appendGameTestError(result.Error, fmt.Sprintf("expected vehicle %q but observed %q", expectedVehicle, obs.VehicleName))
		result.ScenarioComplete = false
	}
	if obs.SimSeconds < float64(requestedDuration) {
		result.Error = appendGameTestError(result.Error, fmt.Sprintf("simulation ran %.2fs of %ds requested", obs.SimSeconds, requestedDuration))
		result.ScenarioComplete = false
	}
	if len(obs.Errors) > 0 {
		result.Error = appendGameTestError(result.Error, fmt.Sprintf("harness errors: %s", strings.Join(obs.Errors, "; ")))
		result.ScenarioComplete = false
	}
	if result.ExitCode != 0 {
		result.Error = appendGameTestError(result.Error, fmt.Sprintf("game exited with code %d", result.ExitCode))
	}

	// Log verdict: separate from scenario completion.
	// An incomplete or failed log read is not clean.
	logReadOK := result.Log.LogPath != "" && result.Log.BytesRead > 0 &&
		result.Log.FromOffset == 0 && result.Log.ToOffset == result.Log.FileSize && !result.Log.PartialLine
	result.LogClean = logReadOK && result.Log.TotalErrors == 0
	if !logReadOK {
		result.Error = appendGameTestError(result.Error, "runtime log is missing, empty, or incomplete")
	} else if !result.LogClean {
		result.Error = appendGameTestError(result.Error, fmt.Sprintf("runtime log contains %d error(s)", result.Log.TotalErrors))
	}

	result.Passed = result.ScenarioComplete && result.LogClean && result.GameExited &&
		result.ExitCode == 0 && result.ExitError == "" && result.Error == ""
	return result
}

func killOwnedProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

func appendGameTestError(existing, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return next
	}
	return existing + "; " + next
}

func createGameTestUserPath(beamNGRoot string) (string, error) {
	candidates := []string{os.TempDir()}
	if beamNGRoot != "" {
		candidates = append(candidates, filepath.Join(beamNGRoot, "temp"))
	}
	var lastErr error
	for _, parent := range candidates {
		if strings.ContainsAny(parent, " \t\r\n") {
			continue
		}
		if err := os.MkdirAll(parent, 0o700); err != nil {
			lastErr = err
			continue
		}
		path, err := os.MkdirTemp(parent, "beamworlds-game-test-")
		if err == nil {
			return path, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", fmt.Errorf("create isolated BeamNG user folder: %w", lastErr)
	}
	return "", errors.New("BeamNG requires a writable test user-folder path without spaces; neither the temporary folder nor the configured BeamNG folder provides one")
}
