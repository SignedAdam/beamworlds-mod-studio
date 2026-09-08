package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const managedModDirectoryName = "beamworlds-managed"

func applyBeamNGModSelection(activeModsDir string, selectedKeys []string) error {
	selected := make(map[string]bool, len(selectedKeys))
	for _, key := range selectedKeys {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" {
			selected[key] = true
		}
	}
	databasePath := filepath.Join(activeModsDir, "db.json")
	payload, err := os.ReadFile(databasePath)
	if errors.Is(err, os.ErrNotExist) {
		unknown, walkErr := unmanagedUnknownArchives(activeModsDir, selected, nil)
		if walkErr != nil {
			return walkErr
		}
		if len(unknown) > 0 {
			return fmt.Errorf("BeamNG has not registered %s; start BeamNG once before applying an exact Play selection", filepath.Base(unknown[0]))
		}
		return nil
	}
	if err != nil {
		return err
	}
	payload = bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf})
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		return fmt.Errorf("parse BeamNG mod database: %w", err)
	}
	var mods map[string]json.RawMessage
	if raw := document["mods"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &mods); err != nil {
			return fmt.Errorf("parse BeamNG mod entries: %w", err)
		}
	}
	if mods == nil {
		mods = map[string]json.RawMessage{}
	}
	known := make(map[string]bool, len(mods))
	for key, raw := range mods {
		normalized := strings.ToLower(strings.TrimSpace(key))
		known[normalized] = true
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entry); err != nil {
			return fmt.Errorf("parse BeamNG mod %s: %w", key, err)
		}
		if entry == nil {
			entry = map[string]json.RawMessage{}
		}
		if selected[normalized] {
			entry["active"] = json.RawMessage("true")
		} else {
			entry["active"] = json.RawMessage("false")
		}
		updated, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		mods[key] = updated
	}
	unknown, err := unmanagedUnknownArchives(activeModsDir, selected, known)
	if err != nil {
		return err
	}
	if len(unknown) > 0 {
		return fmt.Errorf("BeamNG has not registered %s; start BeamNG once before applying an exact Play selection", filepath.Base(unknown[0]))
	}
	updatedMods, err := json.Marshal(mods)
	if err != nil {
		return err
	}
	document["mods"] = updatedMods
	updatedDocument, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	updatedDocument = append(updatedDocument, '\n')
	if err := writeFileAtomic(databasePath+".beamworlds-backup", append([]byte(nil), payload...), 0o644); err != nil {
		return fmt.Errorf("back up BeamNG mod database: %w", err)
	}
	if err := writeFileAtomic(databasePath, updatedDocument, 0o644); err != nil {
		if restoreErr := writeFileAtomic(databasePath, payload, 0o644); restoreErr != nil {
			return fmt.Errorf("update BeamNG mod database: %w (restoring the original also failed: %v)", err, restoreErr)
		}
		return fmt.Errorf("update BeamNG mod database: %w", err)
	}
	return nil
}

// restoreBeamNGModDatabase rolls back a failed apply from the pre-write backup.
// It is the only rollback mechanism; the one-shot "original selection" snapshot
// that used to sit beside it existed solely for the removed restore action.
func restoreBeamNGModDatabase(activeModsDir string) error {
	databasePath := filepath.Join(activeModsDir, "db.json")
	payload, err := os.ReadFile(databasePath + ".beamworlds-backup")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return writeFileAtomic(databasePath, payload, 0o644)
}

func unmanagedUnknownArchives(activeModsDir string, selected, known map[string]bool) ([]string, error) {
	unknown := []string{}
	err := filepath.WalkDir(activeModsDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == activeModsDir {
				return nil
			}
			name := strings.ToLower(entry.Name())
			if name == managedModDirectoryName || strings.HasPrefix(name, ".beamworlds-managed-") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".zip") {
			return nil
		}
		key, err := beamNGModKey(path, activeModsDir)
		if err != nil {
			return err
		}
		if selected[key] || known != nil && known[key] {
			return nil
		}
		unknown = append(unknown, path)
		return nil
	})
	return unknown, err
}

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
