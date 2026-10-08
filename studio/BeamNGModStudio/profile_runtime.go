package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const playRuntimeMarkerName = ".beamworlds-play.json"

func (service *AppService) LaunchPlaySelection(ctx context.Context, request PlayRequest) (PlayResult, error) {
	if !service.profileMu.TryLock() {
		return PlayResult{}, errors.New("another Play operation is already in progress")
	}
	defer service.profileMu.Unlock()

	if strings.TrimSpace(service.config.GameExecutable) == "" {
		return PlayResult{}, errors.New("BeamNG executable was not found in the configured game install directory")
	}
	if err := service.requireGameStopped(); err != nil {
		return PlayResult{}, err
	}
	// ModMaker edits waiting to reach the library go into this launch.
	service.flushLibrarySyncs(ctx)
	activation, err := service.activatePlaySelectionDirect(ctx, request)
	if err != nil {
		return PlayResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return PlayResult{Applied: true, Activation: activation, Error: "Selection applied; launch cancelled."}, nil
	}
	if err := service.requireGameStopped(); err != nil {
		return PlayResult{Applied: true, Activation: activation, Error: err.Error(), ProcessUncertain: true}, nil
	}

	progress := PlayProgress{
		OperationID: activation.OperationID,
		Phase:       "launching",
		Completed:   0,
		Total:       1,
		Done:        false,
	}
	service.emitPlayProgress(progress)
	starter := service.startProcess
	if starter == nil {
		starter = startDetachedProcess
	}
	launch, startErr := starter(service.config.GameExecutable, []string{"-userpath", activation.UserPath}, service.config.GameInstallDir)
	if startErr != nil {
		started, uncertain, message := service.classifyProcessLaunchFailure(launch, startErr)
		progress.Phase = "launch-failed"
		progress.Completed = 1
		progress.Done = true
		progress.Error = message
		service.emitPlayProgress(progress)
		_ = service.store.AppendEvent(ctx, "", "play_launch_failed", map[string]any{
			"operationId": activation.OperationID, "modCount": activation.ModCount,
			"collectionIds": activation.CollectionIDs, "fingerprint": activation.Fingerprint,
			"started": started, "processUncertain": uncertain, "error": message,
		})
		return PlayResult{
			Applied:          true,
			Started:          started,
			Activation:       activation,
			Process:          launch,
			Error:            message,
			ProcessUncertain: uncertain,
		}, nil
	}

	progress.Phase = "started"
	progress.Completed = 1
	progress.Done = true
	service.emitPlayProgress(progress)
	_ = service.store.AppendEvent(ctx, "", "play_launched", map[string]any{
		"operationId": activation.OperationID, "modCount": activation.ModCount,
		"collectionIds": activation.CollectionIDs, "fingerprint": activation.Fingerprint,
		"pid": launch.PID, "userPath": activation.UserPath,
	})
	// The game monitor detects exit transitions and harvests session downloads.
	return PlayResult{Applied: true, Started: true, Activation: activation, Process: launch}, nil
}

func startDetachedProcess(executable string, arguments []string, directory string) (ProcessLaunch, error) {
	command := exec.Command(executable, arguments...)
	command.Dir = directory
	if err := command.Start(); err != nil {
		return ProcessLaunch{}, err
	}
	launch := ProcessLaunch{PID: command.Process.Pid, Executable: executable, StartedAt: nowUTC()}
	if err := command.Process.Release(); err != nil {
		return launch, fmt.Errorf("detach BeamNG process: %w", err)
	}
	return launch, nil
}

func (service *AppService) OpenGameDirectory() error {
	path := strings.TrimSpace(service.config.GameInstallDir)
	if path == "" {
		return errors.New("BeamNG installation directory is not configured")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return errors.New("BeamNG installation directory does not exist")
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("explorer.exe", path)
	case "darwin":
		command = exec.Command("open", path)
	default:
		command = exec.Command("xdg-open", path)
	}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func (service *AppService) GetPlayRuntimeState() (PlayRuntimeState, error) {
	if !service.profileMu.TryLock() {
		return PlayRuntimeState{}, errors.New("another Play operation is already in progress")
	}
	defer service.profileMu.Unlock()

	running, err := service.currentGameRunning()
	if err != nil {
		return PlayRuntimeState{}, err
	}
	state := PlayRuntimeState{GameRunning: running}
	if service.playProfileWarning != "" {
		state.Warning = appendPlayWarning(state.Warning, service.playProfileWarning)
	}
	activation, markerExists, markerErr := service.readPlayRuntimeMarker()
	if markerErr != nil {
		state.Warning = appendPlayWarning(state.Warning, markerErr.Error())
	}
	// The marker records the applied selection; the ownership ledger and journal
	// establish whether its generated files are still coherent.
	if markerExists {
		state.Applied = true
		state.Activation = activation
		if activation.ModsPath != "" {
			if info, statErr := os.Stat(activation.ModsPath); statErr != nil || !info.IsDir() {
				state.Applied = false
				state.Warning = appendPlayWarning(state.Warning, "the applied selection marker points to missing managed mod files")
			}
		}
	}
	journals, err := service.store.listPendingDeploymentJournals(context.Background())
	if err != nil { return state, err }
	// Unfinished cleanup of a previous selection is retried automatically at
	// startup and before every Play; it needs nothing from the player.
	for _, journal := range journals {
		if journal.Purpose == archivePurposePlay && journal.State == journalStateActivating {
			state.Applied = false
			state.Warning = appendPlayWarning(state.Warning, "An interrupted deployment requires recovery while BeamNG is stopped.")
		}
	}
	if state.Applied {
		if blockers := checkManagedRootOwnership(context.Background(), service.store, activation.ModsPath); len(blockers) > 0 {
			state.Applied = false
			state.Warning = appendPlayWarning(state.Warning, strings.Join(blockers, "; "))
		}
	}
	return state, nil
}

func (service *AppService) requireGameStopped() error {
	running, err := service.currentGameRunning()
	if err != nil {
		return fmt.Errorf("check BeamNG process: %w", err)
	}
	if running {
		return errors.New("close BeamNG before changing the active mod selection")
	}
	return nil
}

func (service *AppService) currentGameRunning() (bool, error) {
	if service.gameRunning == nil {
		return false, nil
	}
	return service.gameRunning()
}


func validatePlaySelectionRequest(request PlayRequest, selection PlaySelection) error {
	supplied := strings.TrimSpace(request.Fingerprint)
	expected := strings.TrimSpace(selection.Fingerprint)
	if supplied == "" {
		return errors.New("review the current Play selection before applying it")
	}
	if expected == "" {
		return errors.New("the Play selection has no review fingerprint; refresh it before applying")
	}
	if !strings.EqualFold(supplied, expected) {
		return errors.New("the Play selection changed since it was reviewed; refresh the preview and try again")
	}
	if selection.MissingCount > 0 {
		detail := strings.TrimSpace(strings.Join(selection.Warnings, "; "))
		if detail != "" {
			return fmt.Errorf("the Play selection has %d unavailable archive(s): %s", selection.MissingCount, detail)
		}
		return fmt.Errorf("the Play selection has %d unavailable archive(s); relink or rescan them before launching", selection.MissingCount)
	}
	missing := make([]string, 0)
	for _, mod := range selection.Mods {
		if !mod.Available {
			label := strings.TrimSpace(mod.DisplayName)
			if label == "" {
				label = strings.TrimSpace(mod.EntityID)
			}
			if len(mod.CollectionIDs) > 0 {
				label += fmt.Sprintf(" (collections: %s)", strings.Join(mod.CollectionIDs, ", "))
			}
			if label != "" {
				missing = append(missing, label)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("unavailable archives in the Play selection: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (service *AppService) classifyProcessLaunchFailure(launch ProcessLaunch, startErr error) (started, uncertain bool, message string) {
	if launch.PID > 0 {
		if service.gameRunning != nil {
			running, checkErr := service.gameRunning()
			if checkErr == nil && running {
				return true, true, fmt.Sprintf("BeamNG started (PID %d) but could not be detached; it is still running, so retry is disabled until it closes", launch.PID)
			}
			if checkErr != nil {
				return true, true, fmt.Sprintf("BeamNG started (PID %d), but detaching and process confirmation failed: %v (process check: %v)", launch.PID, startErr, checkErr)
			}
		}
		return true, true, fmt.Sprintf("BeamNG started (PID %d), but detaching or confirming the process failed: %v", launch.PID, startErr)
	}
	if service.gameRunning != nil {
		running, checkErr := service.gameRunning()
		if checkErr == nil && running {
			return true, true, "BeamNG may already be running; the launch result could not be confirmed, so retry is disabled until its state is checked"
		}
		if checkErr != nil {
			return false, true, fmt.Sprintf("BeamNG launch failed and its process state is uncertain: %v (process check: %v)", startErr, checkErr)
		}
		return false, false, fmt.Sprintf("BeamNG did not start: %v", startErr)
	}
	return false, true, fmt.Sprintf("BeamNG launch failed and its process state could not be checked: %v", startErr)
}

func (service *AppService) emitPlayProgress(progress PlayProgress) {
	if service.emit != nil {
		service.emit("play:progress", progress)
	}
}

func normalizePlayCollectionIDs(ids []string) []string {
	normalized := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	return normalized
}

func shortPlayID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 8 {
		return value[:8]
	}
	if value == "" {
		return "selection"
	}
	return value
}

func playRuntimeMarkerPath(config AppConfig) string {
	return filepath.Join(config.ProfileDir, playRuntimeMarkerName)
}

func (service *AppService) readPlayRuntimeMarker() (PlayActivation, bool, error) {
	payload, err := os.ReadFile(playRuntimeMarkerPath(service.config))
	if errors.Is(err, os.ErrNotExist) {
		return PlayActivation{}, false, nil
	}
	if err != nil {
		return PlayActivation{}, false, fmt.Errorf("read applied Play state: %w", err)
	}
	var activation PlayActivation
	if err := json.Unmarshal(payload, &activation); err != nil {
		return PlayActivation{}, true, fmt.Errorf("parse applied Play state: %w", err)
	}
	return activation, true, nil
}

func (service *AppService) writePlayRuntimeMarker(activation PlayActivation) error {
	if err := os.MkdirAll(service.config.ProfileDir, 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(activation, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(playRuntimeMarkerPath(service.config), append(payload, '\n'), 0o644)
}

func (service *AppService) removePlayRuntimeMarker() error {
	if err := os.Remove(playRuntimeMarkerPath(service.config)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func appendPlayWarning(existing, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return next
	}
	return existing + "; " + next
}

func (s *Store) SetEntityArtifactSHA(ctx context.Context, entityID, hash string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE artifacts SET sha256=? WHERE id=(SELECT artifact_id FROM archive_links WHERE entity_id=? ORDER BY active DESC,last_seen_at DESC LIMIT 1)`, strings.ToLower(strings.TrimSpace(hash)), entityID)
	return requireChanged(result, err, "artifact")
}

