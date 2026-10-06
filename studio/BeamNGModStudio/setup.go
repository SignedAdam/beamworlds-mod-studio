package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type SetupInput struct {
	BeamNGRoot            string   `json:"beamngRoot"`
	ActiveModsDir         string   `json:"activeModsDir"`
	LibraryDir            string   `json:"libraryDir"`
	GameInstallDir        string   `json:"gameInstallDir"`
	DataDir               string   `json:"dataDir"`
	AdditionalScanRoots   []string `json:"additionalScanRoots"`
	ArchiveDeploymentMode string   `json:"archiveDeploymentMode"`
}

type SetupState struct {
	Required           bool       `json:"required"`
	Suggested          SetupInput `json:"suggested"`
	ConfigPath         string     `json:"configPath"`
	NativeModCount     int        `json:"nativeModCount"`
	NativeEnabledCount int        `json:"nativeEnabledCount"`
}

type SetupResult struct {
	Config          AppConfig `json:"config"`
	RestartRequired bool      `json:"restartRequired"`
}

type persistedAppConfig struct {
	SetupComplete         bool     `json:"setupComplete"`
	BeamNGRoot            string   `json:"beamngRoot"`
	ActiveModsDir         string   `json:"activeModsDir"`
	LibraryDir            string   `json:"libraryDir"`
	GameInstallDir        string   `json:"gameInstallDir"`
	ScanRoots             []string `json:"scanRoots"`
	ScanConcurrency       int      `json:"scanConcurrency"`
	DataDir               string   `json:"dataDir"`
	ArchiveDeploymentMode string   `json:"archiveDeploymentMode,omitempty"`
}

type beamNGPathHints struct {
	UserFolder  string
	InstallPath string
	Version     string
}

func (service *AppService) GetSetupState() SetupState {
	service.archivePolicyMu.RLock()
	defer service.archivePolicyMu.RUnlock()
	hints := detectBeamNGPaths()
	suggested := SetupInput{
		BeamNGRoot:     firstPath(service.config.BeamNGRoot, hints.UserFolder),
		GameInstallDir: firstPath(service.config.GameInstallDir, hints.InstallPath),
		DataDir:        service.config.DataDir,
		ArchiveDeploymentMode: archiveDeploymentModeFromConfig(service.config),
	}
	if suggested.BeamNGRoot != "" {
		suggested.ActiveModsDir = firstPath(service.config.ActiveModsDir, filepath.Join(suggested.BeamNGRoot, "current", "mods"))
		suggested.LibraryDir = firstPath(service.config.LibraryDir, filepath.Join(suggested.BeamNGRoot, "ModLibrary"))
		if service.config.DataDir == "" || !service.config.SetupComplete && service.config.ConfigPath == "" {
			suggested.DataDir = filepath.Join(filepath.Dir(suggested.BeamNGRoot), "BeamWorlds Mod Studio")
		}
	} else {
		suggested.ActiveModsDir = service.config.ActiveModsDir
		suggested.LibraryDir = service.config.LibraryDir
	}
	for _, root := range service.config.ScanRoots {
		if !samePath(root, suggested.ActiveModsDir) && !samePath(root, suggested.LibraryDir) {
			suggested.AdditionalScanRoots = append(suggested.AdditionalScanRoots, root)
		}
	}
	modCount, enabledCount := beamNGNativeModCounts(suggested.ActiveModsDir)
	return SetupState{
		Required:           setupRequired(service.config),
		Suggested:          suggested,
		ConfigPath:         service.config.ConfigPath,
		NativeModCount:     modCount,
		NativeEnabledCount: enabledCount,
	}
}

func (service *AppService) PickDirectory(title, initialDirectory string) (string, error) {
	app := application.Get()
	if app == nil || app.Dialog == nil {
		return "", errors.New("native directory picker is unavailable")
	}
	dialog := app.Dialog.OpenFile().CanChooseFiles(false).CanChooseDirectories(true).CanCreateDirectories(true).SetTitle(strings.TrimSpace(title)).SetButtonText("Choose folder")
	if initial := cleanOptionalPath(initialDirectory); initial != "" {
		if info, err := os.Stat(initial); err == nil && info.IsDir() {
			dialog.SetDirectory(initial)
		}
	}
	return dialog.PromptForSingleSelection()
}

func (service *AppService) SaveSetup(input SetupInput) (SetupResult, error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	service.archivePolicyMu.Lock()
	defer service.archivePolicyMu.Unlock()
	config, err := validateSetup(service.config, input)
	if err != nil {
		return SetupResult{}, err
	}
	configPath, err := setupConfigPath(service.config)
	if err != nil {
		return SetupResult{}, err
	}
	config.ConfigPath = configPath
	config.ProjectRoot = filepath.Dir(configPath)
	persisted := persistedAppConfig{
		SetupComplete:         true,
		BeamNGRoot:            config.BeamNGRoot,
		ActiveModsDir:         config.ActiveModsDir,
		LibraryDir:            config.LibraryDir,
		GameInstallDir:        config.GameInstallDir,
		ScanRoots:             append([]string(nil), config.ScanRoots...),
		ScanConcurrency:       config.ScanConcurrency,
		DataDir:               config.DataDir,
		ArchiveDeploymentMode: config.ArchiveDeploymentMode,
	}
	payload, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return SetupResult{}, err
	}
	payload = append(payload, '\n')
	if err := writeFileAtomic(configPath, payload, 0o600); err != nil {
		return SetupResult{}, fmt.Errorf("save setup: %w", err)
	}
	return SetupResult{Config: config, RestartRequired: true}, nil
}

func (service *AppService) RestartApplication() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	directory := filepath.Dir(executable)
	if service.config.ProjectRoot != "" {
		directory = service.config.ProjectRoot
	}
	if _, err := startDetachedProcess(executable, os.Args[1:], directory); err != nil {
		return err
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		if app := application.Get(); app != nil {
			app.Quit()
		}
	}()
	return nil
}

func validateSetup(base AppConfig, input SetupInput) (AppConfig, error) {
	config := base
	config.SetupComplete = true
	config.BeamNGRoot = cleanOptionalPath(input.BeamNGRoot)
	config.ActiveModsDir = cleanOptionalPath(input.ActiveModsDir)
	config.LibraryDir = cleanOptionalPath(input.LibraryDir)
	config.GameInstallDir = cleanOptionalPath(input.GameInstallDir)
	config.DataDir = cleanOptionalPath(input.DataDir)
	if input.ArchiveDeploymentMode != "" {
		if !ValidDeploymentMode(input.ArchiveDeploymentMode) {
			return config, fmt.Errorf("invalid archive deployment mode: %q", input.ArchiveDeploymentMode)
		}
		config.ArchiveDeploymentMode = input.ArchiveDeploymentMode
	}
	if config.BeamNGRoot == "" || config.ActiveModsDir == "" || config.LibraryDir == "" || config.GameInstallDir == "" || config.DataDir == "" {
		return config, errors.New("all setup locations are required")
	}
	if info, err := os.Stat(config.BeamNGRoot); err != nil || !info.IsDir() {
		return config, fmt.Errorf("BeamNG user folder does not exist: %s", config.BeamNGRoot)
	}
	if executable := resolveGameExecutable(config.GameInstallDir); executable == "" {
		return config, fmt.Errorf("BeamNG.drive.x64.exe was not found under %s", config.GameInstallDir)
	}
	if pathsOverlap(config.ActiveModsDir, config.LibraryDir) {
		return config, errors.New("the active BeamNG mods folder and mod library must be separate")
	}
	if pathsOverlap(config.DataDir, config.ActiveModsDir) || pathsOverlap(config.DataDir, config.LibraryDir) {
		return config, errors.New("Studio storage must be separate from mod folders")
	}
	for _, directory := range []string{config.ActiveModsDir, config.LibraryDir, config.DataDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return config, fmt.Errorf("create %s: %w", directory, err)
		}
		if err := verifyDirectoryWritable(directory); err != nil {
			return config, err
		}
	}
	config.ScanRoots = []string{config.LibraryDir, config.ActiveModsDir}
	for _, root := range input.AdditionalScanRoots {
		root = cleanOptionalPath(root)
		if root == "" || samePath(root, config.LibraryDir) || samePath(root, config.ActiveModsDir) {
			continue
		}
		if pathsOverlap(root, config.DataDir) || pathsOverlap(root, config.LibraryDir) || pathsOverlap(root, config.ActiveModsDir) {
			return config, fmt.Errorf("additional scan folder overlaps a managed folder: %s", root)
		}
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			return config, fmt.Errorf("additional scan folder does not exist: %s", root)
		}
		config.ScanRoots = append(config.ScanRoots, root)
	}
	return finalizeAppConfig(config)
}

func pathsOverlap(left, right string) bool {
	return pathWithin(left, right) || pathWithin(right, left)
}

func setupRequired(config AppConfig) bool {
	if config.SetupComplete {
		return false
	}
	return config.BeamNGRoot == "" || config.ActiveModsDir == "" || config.LibraryDir == "" || config.GameExecutable == "" || config.DataDir == ""
}

func setupConfigPath(config AppConfig) (string, error) {
	if root := cleanOptionalPath(os.Getenv("BEAMWORLDS_HOME")); root != "" {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", err
		}
		return filepath.Join(root, "config.json"), nil
	}
	if config.ConfigPath != "" {
		return config.ConfigPath, nil
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", errors.New("cannot determine user configuration directory")
	}
	root = filepath.Join(root, "BeamWorlds", "ModStudio")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(root, "config.json"), nil
}

func detectBeamNGPaths() beamNGPathHints {
	candidates := []string{}
	if runtime.GOOS == "windows" {
		if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
			candidates = append(candidates,
				filepath.Join(local, "BeamNG", "BeamNG.drive.ini"),
				filepath.Join(local, "BeamNG.drive", "BeamNG.drive.ini"),
			)
		}
	}
	for _, candidate := range candidates {
		payload, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		hints := parseBeamNGPathHints(string(payload))
		if hints.UserFolder != "" || hints.InstallPath != "" {
			return hints
		}
	}
	return beamNGPathHints{}
}

func parseBeamNGPathHints(content string) beamNGPathHints {
	result := beamNGPathHints{}
	for _, line := range strings.Split(strings.TrimPrefix(content, "\ufeff"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = cleanOptionalPath(strings.TrimSpace(value))
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "userfolder":
			result.UserFolder = value
		case "installpath":
			result.InstallPath = value
		case "version":
			result.Version = strings.TrimSpace(value)
		}
	}
	return result
}

func beamNGNativeModCounts(activeModsDir string) (int, int) {
	payload, err := os.ReadFile(filepath.Join(activeModsDir, "db.json"))
	if err != nil {
		return 0, 0
	}
	var document struct {
		Mods map[string]struct {
			Active *bool `json:"active"`
		} `json:"mods"`
	}
	if json.Unmarshal(payload, &document) != nil {
		return 0, 0
	}
	enabled := 0
	for _, mod := range document.Mods {
		if mod.Active == nil || *mod.Active {
			enabled++
		}
	}
	return len(document.Mods), enabled
}

func verifyDirectoryWritable(directory string) error {
	file, err := os.CreateTemp(directory, ".beamworlds-write-test-*")
	if err != nil {
		return fmt.Errorf("folder is not writable: %s", directory)
	}
	name := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(name)
		return closeErr
	}
	if err := os.Remove(name); err != nil {
		return err
	}
	return nil
}

func writeFileAtomic(filename string, payload []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(filename), ".beamworlds-config-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(filename)
	}
	return os.Rename(temporaryName, filename)
}

func firstPath(values ...string) string {
	for _, value := range values {
		if cleaned := cleanOptionalPath(value); cleaned != "" {
			return cleaned
		}
	}
	return ""
}
