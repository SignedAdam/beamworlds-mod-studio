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

type ProfileProgress struct {
	ProfileID   string `json:"profileId"`
	Phase       string `json:"phase"`
	Current     string `json:"current"`
	Completed   int    `json:"completed"`
	Total       int    `json:"total"`
	BytesCopied int64  `json:"bytesCopied"`
	TotalBytes  int64  `json:"totalBytes"`
	Done        bool   `json:"done"`
	Error       string `json:"error,omitempty"`
}

type ProfileActivation struct {
	ProfileID   string `json:"profileId"`
	ProfileName string `json:"profileName"`
	UserPath    string `json:"userPath"`
	ModsPath    string `json:"modsPath"`
	ModCount    int    `json:"modCount"`
	ActivatedAt string `json:"activatedAt"`
}

type ProfileLaunch struct {
	Activation ProfileActivation `json:"activation"`
	Process    ProcessLaunch     `json:"process"`
}

func (service *AppService) ActivateProfile(profileID string) (ProfileActivation, error) {
	service.profileMu.Lock()
	defer service.profileMu.Unlock()
	return service.activateProfile(context.Background(), strings.TrimSpace(profileID))
}

func (service *AppService) LaunchProfile(profileID string) (ProfileLaunch, error) {
	service.profileMu.Lock()
	defer service.profileMu.Unlock()
	if service.config.GameExecutable == "" {
		return ProfileLaunch{}, errors.New("BeamNG executable was not found in the configured game install directory")
	}
	ctx := context.Background()
	activation, err := service.activateProfile(ctx, strings.TrimSpace(profileID))
	if err != nil {
		return ProfileLaunch{}, err
	}
	starter := service.startProcess
	if starter == nil {
		starter = startDetachedProcess
	}
	launch, err := starter(service.config.GameExecutable, []string{"-userpath", activation.UserPath}, service.config.GameInstallDir)
	if err != nil {
		return ProfileLaunch{}, err
	}
	_ = service.store.AppendEvent(ctx, "", "profile_launched", map[string]any{"profileId": profileID, "profileName": activation.ProfileName, "pid": launch.PID, "userPath": activation.UserPath})
	return ProfileLaunch{Activation: activation, Process: launch}, nil
}
func startDetachedProcess(executable string, arguments []string, directory string) (ProcessLaunch, error) {
	command := exec.Command(executable, arguments...)
	command.Dir = directory
	if err := command.Start(); err != nil {
		return ProcessLaunch{}, err
	}
	launch := ProcessLaunch{PID: command.Process.Pid, Executable: executable, StartedAt: nowUTC()}
	if err := command.Process.Release(); err != nil {
		return ProcessLaunch{}, err
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

func (service *AppService) HasAppliedModProfile() bool {
	return hasOriginalBeamNGModDatabase(service.config.ActiveModsDir)
}

func (service *AppService) RestoreNormalModSelection() error {
	service.profileMu.Lock()
	defer service.profileMu.Unlock()
	if service.gameRunning != nil {
		running, err := service.gameRunning()
		if err != nil {
			return fmt.Errorf("check BeamNG process: %w", err)
		}
		if running {
			return errors.New("close BeamNG before restoring the normal mod selection")
		}
	}
	managedRoot := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
	temporary := filepath.Join(service.config.ActiveModsDir, ".beamworlds-managed-restore")
	if err := os.RemoveAll(temporary); err != nil {
		return fmt.Errorf("clean previous managed-mod restore: %w", err)
	}
	if _, err := os.Stat(managedRoot); err == nil {
		if err := os.Rename(managedRoot, temporary); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := applyOriginalBeamNGModDatabase(service.config.ActiveModsDir); err != nil {
		if _, previousErr := os.Stat(temporary); previousErr == nil {
			_ = os.Rename(temporary, managedRoot)
		}
		return err
	}
	if err := os.RemoveAll(temporary); err != nil {
		return fmt.Errorf("remove managed profile mods: %w", err)
	}
	if err := discardOriginalBeamNGModDatabase(service.config.ActiveModsDir); err != nil {
		return fmt.Errorf("finish normal mod restoration: %w", err)
	}
	_ = service.store.AppendEvent(context.Background(), "", "profile_restored", map[string]any{"activeModsDir": service.config.ActiveModsDir})
	return nil
}

func (service *AppService) activateProfile(ctx context.Context, profileID string) (activation ProfileActivation, resultErr error) {
	if service.config.BeamNGRoot == "" || service.config.ActiveModsDir == "" {
		return ProfileActivation{}, errors.New("BeamNG paths are not configured")
	}
	if service.gameRunning != nil {
		running, err := service.gameRunning()
		if err != nil {
			return ProfileActivation{}, fmt.Errorf("check BeamNG process: %w", err)
		}
		if running {
			return ProfileActivation{}, errors.New("close BeamNG before preparing a mod profile")
		}
	}
	detail, err := service.store.ProfileDetail(ctx, profileID)
	if err != nil {
		return ProfileActivation{}, err
	}
	profileRoot := filepath.Join(service.config.ProfileDir, profileID)
	managedRoot := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
	cacheRoot := filepath.Join(service.config.ProfileDir, ".archive-cache")
	activationID, err := modkit.NewID()
	if err != nil {
		return ProfileActivation{}, err
	}
	nextManaged := filepath.Join(service.config.ActiveModsDir, ".beamworlds-managed-next-"+activationID[:8])
	if err := os.MkdirAll(nextManaged, 0o755); err != nil {
		return ProfileActivation{}, err
	}
	defer func() {
		if resultErr != nil {
			_ = os.RemoveAll(nextManaged)
		}
	}()
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return ProfileActivation{}, err
	}
	progress := ProfileProgress{ProfileID: profileID, Phase: "preparing", Total: len(detail.Mods)}
	for _, mod := range detail.Mods {
		progress.TotalBytes += mod.SizeBytes
	}
	service.emitProfileProgress(progress)
	defer func() {
		if resultErr != nil {
			progress.Phase = "failed"
			progress.Error = resultErr.Error()
			progress.Done = true
			service.emitProfileProgress(progress)
		}
	}()
	selectedKeys := make([]string, 0, len(detail.Mods))
	for index, mod := range detail.Mods {
		if err := ctx.Err(); err != nil {
			return ProfileActivation{}, err
		}
		progress.Phase = "materializing"
		progress.Current = mod.DisplayName
		service.emitProfileProgress(progress)
		sourcePath := cleanOptionalPath(mod.ArchivePath)
		if sourcePath != "" && strings.EqualFold(filepath.Ext(sourcePath), ".zip") && pathWithin(sourcePath, service.config.ActiveModsDir) && !pathWithin(sourcePath, managedRoot) {
			if info, statErr := os.Stat(sourcePath); statErr == nil && !info.IsDir() {
				key, keyErr := beamNGModKey(sourcePath, service.config.ActiveModsDir)
				if keyErr != nil {
					return ProfileActivation{}, keyErr
				}
				selectedKeys = append(selectedKeys, key)
				progress.Completed = index + 1
				service.emitProfileProgress(progress)
				continue
			}
		}
		expectedHash := strings.ToLower(strings.TrimSpace(mod.SHA256))
		if expectedHash == "" {
			if sourcePath == "" {
				return ProfileActivation{}, fmt.Errorf("%s has no linked archive or cached fingerprint", mod.DisplayName)
			}
			expectedHash, err = modkit.FullSHA256(ctx, sourcePath)
			if err != nil {
				return ProfileActivation{}, fmt.Errorf("fingerprint %s: %w", mod.DisplayName, err)
			}
			if err := service.store.SetEntityArtifactSHA(ctx, mod.EntityID, expectedHash); err != nil {
				return ProfileActivation{}, fmt.Errorf("record fingerprint %s: %w", mod.DisplayName, err)
			}
		}
		cachePath := filepath.Join(cacheRoot, expectedHash+".zip")
		if _, statErr := os.Stat(cachePath); errors.Is(statErr, os.ErrNotExist) {
			if sourcePath == "" {
				return ProfileActivation{}, fmt.Errorf("%s is unavailable; rescan or relink its archive", mod.DisplayName)
			}
			if err := copyArchiveToCache(ctx, sourcePath, cachePath, expectedHash, func(written int64) {
				progress.BytesCopied += written
				service.emitProfileProgress(progress)
			}); err != nil {
				return ProfileActivation{}, fmt.Errorf("cache %s: %w", mod.DisplayName, err)
			}
		} else if statErr != nil {
			return ProfileActivation{}, statErr
		}
		base := sanitizeArchiveLabel(strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath)))
		if base == "" {
			base = sanitizeArchiveLabel(mod.DisplayName)
		}
		if base == "" {
			base = "mod"
		}
		destination := filepath.Join(nextManaged, fmt.Sprintf("%s-%s.zip", base, mod.EntityID[:8]))
		if err := os.Link(cachePath, destination); err != nil {
			if err := copyFileAtomic(cachePath, destination); err != nil {
				return ProfileActivation{}, err
			}
		}
		key, err := beamNGModKey(filepath.Join(managedRoot, filepath.Base(destination)), service.config.ActiveModsDir)
		if err != nil {
			return ProfileActivation{}, err
		}
		selectedKeys = append(selectedKeys, key)
		progress.Completed = index + 1
		service.emitProfileProgress(progress)
	}
	progress.Phase = "activating"
	progress.Current = ""
	service.emitProfileProgress(progress)
	previous := filepath.Join(service.config.ActiveModsDir, ".beamworlds-managed-previous")
	_ = os.RemoveAll(previous)
	if _, err := os.Stat(managedRoot); err == nil {
		if err := os.Rename(managedRoot, previous); err != nil {
			return ProfileActivation{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ProfileActivation{}, err
	}
	if err := os.Rename(nextManaged, managedRoot); err != nil {
		if _, previousErr := os.Stat(previous); previousErr == nil {
			_ = os.Rename(previous, managedRoot)
		}
		return ProfileActivation{}, err
	}
	rollback := func() {
		_ = os.RemoveAll(managedRoot)
		if _, previousErr := os.Stat(previous); previousErr == nil {
			_ = os.Rename(previous, managedRoot)
		}
	}
	hadOriginalModState := hasOriginalBeamNGModDatabase(service.config.ActiveModsDir)
	if err := applyBeamNGModSelection(service.config.ActiveModsDir, selectedKeys); err != nil {
		rollback()
		return ProfileActivation{}, err
	}
	activatedAt := nowUTC()
	marker, _ := json.MarshalIndent(map[string]any{"profileId": profileID, "profileName": detail.Profile.Name, "activatedAt": activatedAt, "modCount": len(detail.Mods)}, "", "  ")
	if err := writeFileAtomic(filepath.Join(profileRoot, ".beamworlds-profile.json"), append(marker, '\n'), 0o644); err != nil {
		rollback()
		if restoreErr := restoreBeamNGModDatabase(service.config.ActiveModsDir); restoreErr == nil && !hadOriginalModState {
			_ = discardOriginalBeamNGModDatabase(service.config.ActiveModsDir)
		}
		return ProfileActivation{}, err
	}
	_ = os.RemoveAll(previous)
	activation = ProfileActivation{ProfileID: profileID, ProfileName: detail.Profile.Name, UserPath: service.config.BeamNGRoot, ModsPath: managedRoot, ModCount: len(detail.Mods), ActivatedAt: activatedAt}
	progress.Phase = "ready"
	progress.Completed = progress.Total
	progress.Done = true
	service.emitProfileProgress(progress)
	_ = service.store.AppendEvent(ctx, "", "profile_activated", map[string]any{"profileId": profileID, "profileName": detail.Profile.Name, "modCount": len(detail.Mods), "userPath": service.config.BeamNGRoot})
	return activation, nil
}

func (service *AppService) emitProfileProgress(progress ProfileProgress) {
	if service.emit != nil {
		service.emit("profile:progress", progress)
	}
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
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".profile-cache-*.tmp")
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
			onWrite(int64(read))
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
