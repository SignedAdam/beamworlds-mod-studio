package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

const (
	playRuntimeMarkerName = ".beamworlds-play.json"
	playCacheDirectory    = ".archive-cache"
)

func (service *AppService) LaunchPlaySelection(request PlayRequest) (PlayResult, error) {
	if !service.profileMu.TryLock() {
		return PlayResult{}, errors.New("another Play operation is already in progress")
	}
	defer service.profileMu.Unlock()

	if strings.TrimSpace(service.config.GameExecutable) == "" {
		return PlayResult{}, errors.New("BeamNG executable was not found in the configured game install directory")
	}
	ctx := context.Background()
	if err := service.requireGameStopped(); err != nil {
		return PlayResult{}, err
	}
	activation, err := service.activatePlaySelection(ctx, request)
	if err != nil {
		return PlayResult{}, err
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
	activation, markerExists, markerErr := service.readPlayRuntimeMarker()
	if markerErr != nil {
		state.Warning = appendPlayWarning(state.Warning, markerErr.Error())
	}
	// The marker is now the only record that BeamWorlds owns the active mod
	// set: it is written with the managed folder and removed with it.
	if markerExists {
		state.Applied = true
		state.Activation = activation
		if activation.ModsPath != "" {
			if info, statErr := os.Stat(activation.ModsPath); statErr != nil || !info.IsDir() {
				state.Warning = appendPlayWarning(state.Warning, "the applied selection marker points to missing managed mod files")
			}
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

func (service *AppService) activatePlaySelection(ctx context.Context, request PlayRequest) (activation PlayActivation, resultErr error) {
	if strings.TrimSpace(service.config.BeamNGRoot) == "" || strings.TrimSpace(service.config.ActiveModsDir) == "" {
		return PlayActivation{}, errors.New("BeamNG paths are not configured")
	}
	if err := service.requireGameStopped(); err != nil {
		return PlayActivation{}, err
	}
	ids := normalizePlayCollectionIDs(request.CollectionIDs)
	operationID, err := modkit.NewID()
	if err != nil {
		return PlayActivation{}, err
	}
	progress := PlayProgress{OperationID: operationID, Phase: "reviewing"}
	service.emitPlayProgress(progress)
	defer func() {
		if resultErr != nil {
			progress.Phase = "failed"
			progress.Error = resultErr.Error()
			progress.Done = true
			service.emitPlayProgress(progress)
		}
	}()

	selection, err := service.store.ResolvePlaySelection(ctx, ids)
	if err != nil {
		return PlayActivation{}, err
	}
	if err := service.validateRuntimePlaySelection(PlayRequest{CollectionIDs: ids, Fingerprint: request.Fingerprint}, selection); err != nil {
		return PlayActivation{}, err
	}
	progress.Phase = "preparing"
	progress.Total = len(selection.Mods)
	for _, mod := range selection.Mods {
		if mod.SizeBytes > 0 {
			progress.TotalBytes += mod.SizeBytes
		}
	}
	service.emitPlayProgress(progress)

	activeModsInfo, err := os.Stat(service.config.ActiveModsDir)
	if err != nil {
		return PlayActivation{}, fmt.Errorf("inspect BeamNG mods directory: %w", err)
	}
	if !activeModsInfo.IsDir() {
		return PlayActivation{}, errors.New("configured BeamNG mods path is not a directory")
	}
	if err := os.MkdirAll(service.config.ProfileDir, 0o755); err != nil {
		return PlayActivation{}, fmt.Errorf("prepare Play state directory: %w", err)
	}
	cacheRoot := filepath.Join(service.config.ProfileDir, playCacheDirectory)
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return PlayActivation{}, fmt.Errorf("prepare archive cache: %w", err)
	}
	managedRoot := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
	activationID := operationID
	nextManaged := filepath.Join(service.config.ActiveModsDir, ".beamworlds-managed-next-"+shortPlayID(activationID))
	if err := os.RemoveAll(nextManaged); err != nil {
		return PlayActivation{}, err
	}
	if err := os.MkdirAll(nextManaged, 0o755); err != nil {
		return PlayActivation{}, err
	}
	defer func() {
		if nextManaged != "" && resultErr != nil {
			_ = os.RemoveAll(nextManaged)
		}
	}()

	selectedKeys := make([]string, 0, len(selection.Mods))
	selectedKeySet := make(map[string]struct{}, len(selection.Mods))
	// Newly computed archive fingerprints are recorded only after the selection
	// is activated. Writing them here would change the resolved selection that
	// the post-materialization review compares against, rejecting the very
	// first Play of any mod whose archive was never hashed.
	pendingFingerprints := make(map[string]string, len(selection.Mods))
	for index, mod := range selection.Mods {
		if err := ctx.Err(); err != nil {
			return PlayActivation{}, err
		}
		progress.Phase = "materializing"
		progress.Current = mod.DisplayName
		service.emitPlayProgress(progress)
		sourcePath := cleanOptionalPath(mod.ArchivePath)
		expectedHash := strings.ToLower(strings.TrimSpace(mod.SHA256))
		hashMissing := expectedHash == ""
		sourceExists := false
		if sourcePath != "" {
			info, statErr := os.Stat(sourcePath)
			switch {
			case statErr == nil && !info.IsDir():
				sourceExists = true
				actualHash, hashErr := modkit.FullSHA256(ctx, sourcePath)
				if hashErr != nil {
					return PlayActivation{}, fmt.Errorf("fingerprint %s: %w", mod.DisplayName, hashErr)
				}
				if expectedHash != "" && !strings.EqualFold(actualHash, expectedHash) {
					return PlayActivation{}, fmt.Errorf("%s changed since the Play preview; review the selection again", mod.DisplayName)
				}
				if expectedHash == "" {
					expectedHash = actualHash
				}
			case statErr != nil && !errors.Is(statErr, os.ErrNotExist):
				return PlayActivation{}, fmt.Errorf("inspect archive %s: %w", mod.DisplayName, statErr)
			}
		}
		if sourceExists && strings.EqualFold(filepath.Ext(sourcePath), ".zip") &&
			pathWithin(sourcePath, service.config.ActiveModsDir) && !pathWithin(sourcePath, managedRoot) {
			key, keyErr := beamNGModKey(sourcePath, service.config.ActiveModsDir)
			if keyErr != nil {
				return PlayActivation{}, keyErr
			}
			if _, exists := selectedKeySet[key]; !exists {
				selectedKeys = append(selectedKeys, key)
				selectedKeySet[key] = struct{}{}
			}
			progress.Completed = index + 1
			service.emitPlayProgress(progress)
			continue
		}
		if hashMissing && sourceExists {
			pendingFingerprints[mod.EntityID] = expectedHash
		}
		if expectedHash == "" {
			if sourcePath == "" {
				return PlayActivation{}, fmt.Errorf("%s has no linked archive or cached fingerprint", mod.DisplayName)
			}
			return PlayActivation{}, fmt.Errorf("%s is unavailable; rescan or relink its archive", mod.DisplayName)
		}
		cachePath := filepath.Join(cacheRoot, expectedHash+".zip")
		cacheInfo, statErr := os.Stat(cachePath)
		if statErr == nil {
			if cacheInfo.IsDir() {
				return PlayActivation{}, fmt.Errorf("cached archive for %s is not a file", mod.DisplayName)
			}
			cachedHash, hashErr := modkit.FullSHA256(ctx, cachePath)
			if hashErr != nil {
				return PlayActivation{}, fmt.Errorf("verify cached archive %s: %w", mod.DisplayName, hashErr)
			}
			if !strings.EqualFold(cachedHash, expectedHash) {
				if err := os.Remove(cachePath); err != nil {
					return PlayActivation{}, fmt.Errorf("discard corrupt cache for %s: %w", mod.DisplayName, err)
				}
				statErr = os.ErrNotExist
			}
		}
		if errors.Is(statErr, os.ErrNotExist) {
			if !sourceExists {
				return PlayActivation{}, fmt.Errorf("%s is unavailable; rescan or relink its archive", mod.DisplayName)
			}
			if err := copyArchiveToCache(ctx, sourcePath, cachePath, expectedHash, func(written int64) {
				progress.BytesCopied += written
				service.emitPlayProgress(progress)
			}); err != nil {
				return PlayActivation{}, fmt.Errorf("cache %s: %w", mod.DisplayName, err)
			}
		} else if statErr != nil {
			return PlayActivation{}, statErr
		}
		base := sanitizeArchiveLabel(strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath)))
		if base == "" {
			base = sanitizeArchiveLabel(mod.DisplayName)
		}
		if base == "" {
			base = "mod"
		}
		entitySuffix := shortPlayID(mod.EntityID)
		destination := filepath.Join(nextManaged, fmt.Sprintf("%s-%s.zip", base, entitySuffix))
		if err := os.Link(cachePath, destination); err != nil {
			if err := copyFileAtomic(cachePath, destination); err != nil {
				return PlayActivation{}, err
			}
		}
		key, err := beamNGModKey(filepath.Join(managedRoot, filepath.Base(destination)), service.config.ActiveModsDir)
		if err != nil {
			return PlayActivation{}, err
		}
		if _, exists := selectedKeySet[key]; !exists {
			selectedKeys = append(selectedKeys, key)
			selectedKeySet[key] = struct{}{}
		}
		progress.Completed = index + 1
		service.emitPlayProgress(progress)
	}

	progress.Phase = "reviewing"
	progress.Current = ""
	service.emitPlayProgress(progress)
	finalSelection, err := service.store.ResolvePlaySelection(ctx, ids)
	if err != nil {
		return PlayActivation{}, err
	}
	if err := service.validateRuntimePlaySelection(PlayRequest{CollectionIDs: ids, Fingerprint: request.Fingerprint}, finalSelection); err != nil {
		return PlayActivation{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(finalSelection.Fingerprint), strings.TrimSpace(selection.Fingerprint)) {
		return PlayActivation{}, errors.New("the Play selection changed while it was being prepared; refresh the preview and try again")
	}

	progress.Phase = "activating"
	progress.Current = ""
	service.emitPlayProgress(progress)
	previous := filepath.Join(service.config.ActiveModsDir, ".beamworlds-managed-previous")
	_ = os.RemoveAll(previous)
	if _, err := os.Stat(managedRoot); err == nil {
		if err := os.Rename(managedRoot, previous); err != nil {
			return PlayActivation{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return PlayActivation{}, err
	}
	if err := os.Rename(nextManaged, managedRoot); err != nil {
		if _, previousErr := os.Stat(previous); previousErr == nil {
			_ = os.Rename(previous, managedRoot)
		}
		return PlayActivation{}, err
	}
	nextManaged = ""
	rollback := func() error {
		if err := os.RemoveAll(managedRoot); err != nil {
			return err
		}
		if _, previousErr := os.Stat(previous); previousErr == nil {
			return os.Rename(previous, managedRoot)
		}
		return nil
	}
	if err := applyBeamNGModSelection(service.config.ActiveModsDir, selectedKeys); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return PlayActivation{}, fmt.Errorf("apply Play selection: %w (restoring managed mod files failed: %v)", err, rollbackErr)
		}
		return PlayActivation{}, err
	}
	activatedAt := nowUTC()
	activation = PlayActivation{
		OperationID:   operationID,
		ModCount:      len(selection.Mods),
		UserPath:      service.config.BeamNGRoot,
		ModsPath:      managedRoot,
		ActivatedAt:   activatedAt,
		CollectionIDs: append([]string(nil), ids...),
		Fingerprint:   strings.TrimSpace(selection.Fingerprint),
	}
	if err := service.writePlayRuntimeMarker(activation); err != nil {
		rollbackErr := rollback()
		restoreErr := restoreBeamNGModDatabase(service.config.ActiveModsDir)
		if rollbackErr != nil || restoreErr != nil {
			return PlayActivation{}, fmt.Errorf("save applied Play state: %w (rollback: %v, native restore: %v)", err, rollbackErr, restoreErr)
		}
		return PlayActivation{}, err
	}
	_ = os.RemoveAll(previous)
	// Now that the reviewed selection is applied, persist the fingerprints that
	// were computed while materializing so later launches can reuse the cache.
	for entityID, hash := range pendingFingerprints {
		if err := service.store.SetEntityArtifactSHA(ctx, entityID, hash); err != nil {
			progress.Error = fmt.Sprintf("record archive fingerprint: %v", err)
		}
	}
	progress.Phase = "ready"
	progress.Completed = progress.Total
	progress.Done = true
	service.emitPlayProgress(progress)
	_ = service.store.AppendEvent(ctx, "", "play_applied", map[string]any{
		"operationId": activation.OperationID, "modCount": activation.ModCount,
		"collectionIds": activation.CollectionIDs, "fingerprint": activation.Fingerprint,
		"userPath": activation.UserPath,
	})
	return activation, nil
}

func (service *AppService) validateRuntimePlaySelection(request PlayRequest, selection PlaySelection) error {
	runtimeSelection := selection
	runtimeSelection.Mods = append([]CollectionMod(nil), selection.Mods...)
	cacheRoot := filepath.Join(service.config.ProfileDir, playCacheDirectory)
	cacheBacked := 0
	for index, mod := range runtimeSelection.Mods {
		if mod.Available || strings.TrimSpace(mod.SHA256) == "" {
			continue
		}
		cachePath := filepath.Join(cacheRoot, strings.ToLower(strings.TrimSpace(mod.SHA256))+".zip")
		info, err := os.Stat(cachePath)
		if err == nil && !info.IsDir() {
			runtimeSelection.Mods[index].Available = true
			cacheBacked++
		}
	}
	if runtimeSelection.MissingCount > 0 && cacheBacked > 0 {
		runtimeSelection.MissingCount -= cacheBacked
		if runtimeSelection.MissingCount < 0 {
			runtimeSelection.MissingCount = 0
		}
	}
	return validatePlaySelectionRequest(request, runtimeSelection)
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

func copyArchiveToCache(ctx context.Context, source, destination, expectedHash string, onWrite func(int64)) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".play-cache-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	hash := sha256.New()
	buffer := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		read, readErr := input.Read(buffer)
		if read > 0 {
			if _, err := temporary.Write(buffer[:read]); err != nil {
				return err
			}
			if _, err := hash.Write(buffer[:read]); err != nil {
				return err
			}
			if onWrite != nil {
				onWrite(int64(read))
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	actualHash := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actualHash, expectedHash) {
		return fmt.Errorf("source checksum changed: got %s, expected %s", actualHash, expectedHash)
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		if _, statErr := os.Stat(destination); statErr == nil {
			return nil
		}
		return err
	}
	keep = true
	return nil
}
