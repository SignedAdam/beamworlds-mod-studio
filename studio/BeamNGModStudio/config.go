package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type AppConfig struct {
	SetupComplete   bool     `json:"setupComplete,omitempty"`
	BeamNGRoot      string   `json:"beamngRoot"`
	ActiveModsDir   string   `json:"activeModsDir"`
	LibraryDir      string   `json:"libraryDir"`
	GameInstallDir  string   `json:"gameInstallDir"`
	ScanRoots       []string `json:"scanRoots"`
	ScanConcurrency int      `json:"scanConcurrency"`
	DataDir         string   `json:"dataDir,omitempty"`
	AIRuntimePath   string   `json:"-"`

	ConfigPath     string `json:"configPath"`
	ProjectRoot    string `json:"projectRoot"`
	DatabasePath   string `json:"databasePath"`
	ImageCacheDir  string `json:"imageCacheDir"`
	WorkspaceDir   string `json:"workspaceDir"`
	ExportDir      string `json:"exportDir"`
	ProfileDir     string `json:"profileDir"`
	TestInstallDir string `json:"testInstallDir"`
	GameExecutable string `json:"gameExecutable"`
}

func LoadAppConfig() (AppConfig, error) {
	projectRoot, configPath := findProjectConfig()
	config := AppConfig{ScanConcurrency: 4, ProjectRoot: projectRoot, ConfigPath: configPath}
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return config, err
		}
		if err := json.Unmarshal(data, &config); err != nil {
			return config, fmt.Errorf("parse %s: %w", configPath, err)
		}
		config.ProjectRoot = projectRoot
		config.ConfigPath = configPath
	}
	if config.ProjectRoot == "" && configPath != "" {
		config.ProjectRoot = filepath.Dir(configPath)
	}
	if config.DataDir == "" {
		if config.ProjectRoot != "" {
			config.DataDir = filepath.Join(config.ProjectRoot, "studio-data")
		} else if local, err := os.UserConfigDir(); err == nil {
			config.DataDir = filepath.Join(local, "BeamWorlds", "ModStudio")
		} else {
			return config, errors.New("cannot determine application data directory")
		}
	}
	return finalizeAppConfig(config)
}

func finalizeAppConfig(config AppConfig) (AppConfig, error) {
	config.BeamNGRoot = cleanOptionalPath(config.BeamNGRoot)
	config.ActiveModsDir = cleanOptionalPath(config.ActiveModsDir)
	config.LibraryDir = cleanOptionalPath(config.LibraryDir)
	config.GameInstallDir = cleanOptionalPath(config.GameInstallDir)
	config.DataDir = cleanOptionalPath(config.DataDir)
	if config.ScanConcurrency < 1 {
		config.ScanConcurrency = 4
	}
	if config.ScanConcurrency > 12 {
		config.ScanConcurrency = 12
	}
	config.ScanRoots = effectiveScanRoots(config)
	config.DatabasePath = filepath.Join(config.DataDir, "modstudio.sqlite")
	config.ImageCacheDir = filepath.Join(config.DataDir, "cache", "images")
	config.WorkspaceDir = filepath.Join(config.DataDir, "workspaces")
	config.ProfileDir = filepath.Join(config.DataDir, "profiles")
	config.ExportDir = filepath.Join(config.DataDir, "exports")
	config.TestInstallDir = config.ActiveModsDir
	config.GameExecutable = resolveGameExecutable(config.GameInstallDir)
	for _, directory := range []string{config.DataDir, config.ImageCacheDir, config.WorkspaceDir, config.ExportDir, config.ProfileDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return config, err
		}
	}
	return config, nil
}

func findProjectConfig() (string, string) {
	if value := strings.TrimSpace(os.Getenv("BEAMWORLDS_HOME")); value != "" {
		root := cleanOptionalPath(value)
		if configRoot, configPath := findConfigFrom(root); configPath != "" {
			return configRoot, configPath
		}
		return root, ""
	}
	if local, err := os.UserConfigDir(); err == nil {
		root := filepath.Join(local, "BeamWorlds", "ModStudio")
		filename := filepath.Join(root, "config.json")
		if info, statErr := os.Stat(filename); statErr == nil && !info.IsDir() {
			return root, filename
		}
	}
	candidates := []string{}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Dir(executable))
	}
	for _, candidate := range candidates {
		if root, filename := findConfigFrom(candidate); filename != "" {
			return root, filename
		}
	}
	return "", ""
}

func findConfigFrom(candidate string) (string, string) {
	current := filepath.Clean(candidate)
	for range 8 {
		filename := filepath.Join(current, "config.json")
		if info, err := os.Stat(filename); err == nil && !info.IsDir() {
			return current, filename
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", ""
}

func effectiveScanRoots(config AppConfig) []string {
	candidates := config.ScanRoots
	if len(candidates) == 0 {
		candidates = []string{config.LibraryDir, config.ActiveModsDir}
	}
	roots := []string{}
	seen := map[string]bool{}
	add := func(candidate string) {
		candidate = cleanOptionalPath(candidate)
		if candidate == "" {
			return
		}
		key := strings.ToLower(candidate)
		if seen[key] {
			return
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			seen[key] = true
			roots = append(roots, candidate)
		}
	}
	for _, candidate := range candidates {
		if samePath(candidate, config.BeamNGRoot) {
			add(config.LibraryDir)
			add(config.ActiveModsDir)
			continue
		}
		add(candidate)
	}
	if len(roots) == 0 {
		add(config.LibraryDir)
		add(config.ActiveModsDir)
	}
	return roots
}

func resolveGameExecutable(installDir string) string {
	for _, relative := range []string{filepath.Join("Bin64", "BeamNG.drive.x64.exe"), "BeamNG.drive.exe"} {
		candidate := filepath.Join(installDir, relative)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func cleanOptionalPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.Clean(filepath.FromSlash(value))
}

func samePath(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(filepath.FromSlash(left)), filepath.Clean(filepath.FromSlash(right)))
}
