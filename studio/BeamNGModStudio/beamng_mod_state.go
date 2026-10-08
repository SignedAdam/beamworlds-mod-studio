package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

const managedModDirectoryName = "beamworlds-managed"

func beamNGModKey(archivePath, activeModsDir string) (string, error) {
	relative, err := filepath.Rel(activeModsDir, archivePath)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive is outside the BeamNG mods folder: %s", archivePath)
	}
	key := strings.ToLower(filepath.ToSlash(relative))
	key = strings.TrimPrefix(key, "dir:/")
	key = strings.ReplaceAll(key, "repo/", "")
	key = strings.ReplaceAll(key, "unpacked/", "")
	key = strings.ReplaceAll(key, "/", "")
	key = strings.TrimSuffix(key, ".zip")
	return key, nil
}
