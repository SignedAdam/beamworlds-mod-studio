package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// playUserPath returns the deterministic isolated BeamNG user folder for Play.
// Candidates: <BeamNGRoot>/.beamworlds-play, then <volume root of LibraryDir>/.beamworlds-play.
func playUserPath(config AppConfig) (string, error) {
	var candidates []string
	if root := strings.TrimSpace(config.BeamNGRoot); root != "" {
		candidates = append(candidates, filepath.Join(root, ".beamworlds-play"))
	}
	if lib := strings.TrimSpace(config.LibraryDir); lib != "" {
		vol := filepath.VolumeName(lib)
		if vol != "" {
			candidates = append(candidates, filepath.Join(vol+`\`, ".beamworlds-play"))
		}
	}
	for _, candidate := range candidates {
		if strings.ContainsAny(candidate, " \t\r\n") {
			continue
		}
		if err := os.MkdirAll(candidate, 0o755); err != nil {
			continue
		}
		return candidate, nil
	}
	return "", errors.New("cannot create an isolated Play user folder: all candidate paths contain spaces or are not writable")
}

func playProfileModsDir(playRoot string) string {
	return filepath.Join(playRoot, "current", "mods")
}

func playProfileDeploymentDir(playRoot string) string {
	return filepath.Join(playRoot, "current", ".beamworlds-deployment")
}

// syncPlayProfile ensures junctions and migrates profile-created data.
// Caller must hold modImportMu; the game must be stopped.
func (service *AppService) syncPlayProfile(ctx context.Context) error {
	playRoot, err := playUserPath(service.config)
	if err != nil {
		return err
	}
	realCurrent := filepath.Join(service.config.BeamNGRoot, "current")
	playCurrent := filepath.Join(playRoot, "current")

	if err := os.MkdirAll(playCurrent, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(playProfileModsDir(playRoot), 0o755); err != nil {
		return err
	}

	// 1. Ensure junctions for every real directory except mods and .beamworlds-*.
	realEntries, err := os.ReadDir(realCurrent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range realEntries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if lower == "mods" || strings.HasPrefix(lower, ".beamworlds-") {
			continue
		}
		realDir := filepath.Join(realCurrent, name)
		linkPath := filepath.Join(playCurrent, name)
		if err := ensureJunction(linkPath, realDir); err != nil {
			return fmt.Errorf("ensure junction %s → %s: %w", linkPath, realDir, err)
		}
	}

	// 2. Migrate profile-created data.
	playEntries, err := os.ReadDir(playCurrent)
	if err != nil {
		return err
	}
	for _, entry := range playEntries {
		name := entry.Name()
		lower := strings.ToLower(name)
		if lower == "mods" || strings.HasPrefix(lower, ".beamworlds-") {
			continue
		}
		linkPath := filepath.Join(playCurrent, name)
		if isDirectoryJunction(linkPath) {
			continue
		}
		info, err := os.Lstat(linkPath)
		if err != nil || !info.IsDir() {
			continue
		}
		realDir := filepath.Join(realCurrent, name)
		conflicts, err := migrateProfileDir(linkPath, realDir)
		if err != nil {
			return fmt.Errorf("migrate profile dir %s: %w", name, err)
		}
		if len(conflicts) > 0 {
			log.Printf("play profile: migration conflicts in %s: %v (left in profile; junction deferred)", name, conflicts)
			continue
		}
		if err := ensureJunction(linkPath, realDir); err != nil {
			log.Printf("play profile: junction after migrate %s: %v", name, err)
		}
	}

	return nil
}

// ensureJunction makes sure linkPath is a junction/symlink pointing to target.
func ensureJunction(linkPath, target string) error {
	cleanTarget := filepath.Clean(target)
	if isDirectoryJunction(linkPath) {
		existing := junctionTarget(linkPath)
		if samePath(existing, cleanTarget) {
			return nil
		}
		if err := removeDirectoryJunction(linkPath); err != nil {
			return fmt.Errorf("replace junction: %w", err)
		}
	} else if info, err := os.Lstat(linkPath); err == nil && info.IsDir() {
		entries, _ := os.ReadDir(linkPath)
		if len(entries) > 0 {
			return fmt.Errorf("real directory blocks junction at %s", linkPath)
		}
		if err := os.Remove(linkPath); err != nil {
			return fmt.Errorf("remove empty directory for junction at %s: %w", linkPath, err)
		}
	}
	return createDirectoryJunction(linkPath, cleanTarget)
}

// migrateProfileDir recursively merges entries from profileDir into realDir.
// For each entry: if the destination does not exist, move it. If both sides
// have a directory with the same name, recurse. File-level conflicts (both
// exist) are left in place and returned. After a successful migration the
// profile directory is removed if empty.
func migrateProfileDir(profileDir, realDir string) (conflicts []string, err error) {
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(profileDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		src := filepath.Join(profileDir, entry.Name())
		dst := filepath.Join(realDir, entry.Name())
		dstInfo, dstErr := os.Lstat(dst)
		if dstErr != nil && !os.IsNotExist(dstErr) {
			conflicts = append(conflicts, entry.Name())
			continue
		}
		dstExists := dstErr == nil

		if dstExists && entry.IsDir() && dstInfo.IsDir() {
			// Both are directories: recurse.
			sub, subErr := migrateProfileDir(src, dst)
			if subErr != nil {
				return conflicts, subErr
			}
			for _, c := range sub {
				conflicts = append(conflicts, filepath.Join(entry.Name(), c))
			}
			continue
		}

		if dstExists {
			conflicts = append(conflicts, entry.Name())
			continue
		}

		// Destination does not exist: try rename (same volume).
		if renameErr := os.Rename(src, dst); renameErr == nil {
			continue
		}
		// Cross-volume: copy, verify, remove source.
		if entry.IsDir() {
			if cpErr := copyEntry(src, dst); cpErr != nil {
				conflicts = append(conflicts, entry.Name())
				continue
			}
		} else {
			if cpErr := copyAndVerifyFile(src, dst); cpErr != nil {
				conflicts = append(conflicts, entry.Name())
				continue
			}
		}
		_ = os.RemoveAll(src)
	}
	// Remove the profile directory if now empty.
	remaining, _ := os.ReadDir(profileDir)
	if len(remaining) == 0 {
		_ = os.Remove(profileDir)
	}
	return conflicts, nil
}

// copyEntry copies a directory tree from src to dst.
func copyEntry(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyEntry(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	return copyAndVerifyFile(src, dst)
}

// copyAndVerifyFile copies src to dst and verifies size matches.
func copyAndVerifyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	srcInfo, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, srcInfo.Mode())
	if err != nil {
		return err
	}
	written, err := io.Copy(out, in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(dst)
		return err
	}
	if written != srcInfo.Size() {
		_ = os.Remove(dst)
		return fmt.Errorf("copy size mismatch: wrote %d, expected %d", written, srcInfo.Size())
	}
	return nil
}

// reconcileProfileSessionDownloads moves non-owned zips from profile mods into
// the real ActiveModsDir. Repo-path downloads with different bytes are treated
// as BeamNG repository updates. Uses verified-hash comparison, no-replace
// renames, and cross-volume copy fallback.
//
// Caller must hold modImportMu.
func (service *AppService) reconcileProfileSessionDownloads(ctx context.Context, playRoot string) (int, error) {
	profileMods := playProfileModsDir(playRoot)
	entries, err := service.store.listOwnedArchiveEntries(ctx)
	if err != nil {
		return 0, err
	}
	ownedPaths := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.Purpose == archivePurposePlay && entry.OwnerID == playDeploymentOwnerID && entry.State == archiveStateActive {
			ownedPaths[archivePathKey(filepath.Join(entry.TargetRoot, entry.RelativePath))] = true
		}
	}

	harvested := 0
	var failures []string
	walkErr := filepath.WalkDir(profileMods, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		lower := strings.ToLower(d.Name())
		if lower == "db.json" {
			_ = os.Remove(path)
			return nil
		}
		if !isModArchive(d.Name()) {
			return nil
		}
		if ownedPaths[archivePathKey(path)] {
			return nil
		}

		rel, err := filepath.Rel(profileMods, path)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: relative path: %v", d.Name(), err))
			return nil
		}
		realDest := filepath.Join(service.config.ActiveModsDir, rel)
		if err := os.MkdirAll(filepath.Dir(realDest), 0o755); err != nil {
			failures = append(failures, fmt.Sprintf("%s: mkdir: %v", d.Name(), err))
			return nil
		}

		srcIdent, srcExists, srcErr := archiveIdentityIfPresent(path)
		if srcErr != nil || !srcExists {
			failures = append(failures, fmt.Sprintf("%s: inspect: %v", d.Name(), srcErr))
			return nil
		}

		dstIdent, dstExists, dstErr := archiveIdentityIfPresent(realDest)
		if dstErr != nil {
			failures = append(failures, fmt.Sprintf("%s: inspect dest: %v", d.Name(), dstErr))
			return nil
		}

		if dstExists {
			// Same file ID (hardlinked during the session)? Just remove the profile link.
			if sameArchiveFileID(srcIdent, dstIdent) {
				_ = os.Remove(path)
				return nil
			}
			// Same size? Compare hashes.
			if srcIdent.SizeBytes == dstIdent.SizeBytes {
				srcHash, _, hashErr1 := service.verifiedArchiveHash(ctx, path, srcIdent)
				dstHash, _, hashErr2 := service.verifiedArchiveHash(ctx, realDest, dstIdent)
				if hashErr1 == nil && hashErr2 == nil && srcHash == dstHash {
					_ = os.Remove(path)
					return nil
				}
			}

			// Different bytes. Repo-path downloads are updates.
			isRepo := strings.HasPrefix(strings.ToLower(filepath.ToSlash(rel)), "repo/")
			fromDir := filepath.Join(service.config.LibraryDir, "From BeamNG")
			if err := os.MkdirAll(fromDir, 0o755); err != nil {
				failures = append(failures, fmt.Sprintf("%s: mkdir From BeamNG: %v", d.Name(), err))
				return nil
			}
			if isRepo {
				// Preserve old real file, replace with the new download.
				oldDest := uniqueFilePath(fromDir, filepath.Base(realDest))
				if err := moveFileVerified(ctx, service, realDest, oldDest); err != nil {
					failures = append(failures, fmt.Sprintf("%s: preserve old: %v", d.Name(), err))
					return nil
				}
				_ = service.store.recordStudioPlacedArchive(ctx, oldDest)
				if err := moveFileVerified(ctx, service, path, realDest); err != nil {
					// Restore the old file.
					_ = moveFileVerified(ctx, service, oldDest, realDest)
					failures = append(failures, fmt.Sprintf("%s: install new: %v", d.Name(), err))
					return nil
				}
			} else {
				// A different mod that happens to share the name: it is a new
				// download, so it stays eligible for the new-mods prompt.
				uniqueDest := uniqueFilePath(fromDir, d.Name())
				if err := moveFileVerified(ctx, service, path, uniqueDest); err != nil {
					failures = append(failures, fmt.Sprintf("%s: move to From BeamNG: %v", d.Name(), err))
					return nil
				}
			}
		} else {
			if err := moveFileVerified(ctx, service, path, realDest); err != nil {
				failures = append(failures, fmt.Sprintf("%s: move: %v", d.Name(), err))
				return nil
			}
		}
		harvested++
		return nil
	})

	removeEmptySubdirs(profileMods)

	if walkErr != nil {
		failures = append(failures, walkErr.Error())
	}
	if len(failures) > 0 {
		return harvested, fmt.Errorf("session download harvest failures: %s", strings.Join(failures, "; "))
	}
	return harvested, nil
}

// moveFileVerified tries renameArchiveNoReplace first. If that fails due to
// cross-volume, copies to a temp name in the destination dir, verifies the
// hash, renames into place, and removes the source.
func moveFileVerified(ctx context.Context, service *AppService, from, to string) error {
	err := renameArchiveNoReplace(from, to)
	if err == nil {
		return nil
	}
	// Cross-volume fallback: copy, verify, rename, remove source.
	dir := filepath.Dir(to)
	tmp, tmpErr := os.CreateTemp(dir, ".beamworlds-harvest-*.tmp")
	if tmpErr != nil {
		return fmt.Errorf("create temp for cross-volume move: %w", tmpErr)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	in, openErr := os.Open(from)
	if openErr != nil {
		_ = tmp.Close()
		return openErr
	}
	_, copyErr := io.Copy(tmp, in)
	in.Close()
	if closeErr := tmp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return copyErr
	}
	// Verify by hash.
	srcIdent, _, inspErr := archiveIdentityIfPresent(from)
	if inspErr != nil {
		return inspErr
	}
	tmpIdent, _, inspErr := archiveIdentityIfPresent(tmpName)
	if inspErr != nil {
		return inspErr
	}
	if srcIdent.SizeBytes != tmpIdent.SizeBytes {
		return fmt.Errorf("cross-volume copy size mismatch: %d vs %d", srcIdent.SizeBytes, tmpIdent.SizeBytes)
	}
	srcHash, _, _ := service.verifiedArchiveHash(ctx, from, srcIdent)
	tmpHash, _, _ := service.verifiedArchiveHash(ctx, tmpName, tmpIdent)
	if srcHash == "" || srcHash != tmpHash {
		return fmt.Errorf("cross-volume copy hash mismatch")
	}
	if err := renameArchiveNoReplace(tmpName, to); err != nil {
		return fmt.Errorf("rename temp into place: %w", err)
	}
	_ = os.Remove(from)
	return nil
}

func uniqueFilePath(dir, name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	ext := filepath.Ext(name)
	candidate := filepath.Join(dir, name)
	for i := 1; ; i++ {
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, i, ext))
	}
}

func removeEmptySubdirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, _ error) error {
		if d != nil && d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		entries, _ := os.ReadDir(dirs[i])
		if len(entries) == 0 {
			_ = os.Remove(dirs[i])
		}
	}
}

// cutoverLegacyManagedDir performs the one-time migration of the old
// beamworlds-managed layout. Idempotent; safe to rerun after a crash.
// Returns a non-nil error on any step failure; the caller must surface it.
//
// Caller must hold modImportMu; the game must be stopped.
func (service *AppService) cutoverLegacyManagedDir(ctx context.Context) error {
	managedRoot := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
	managedExists := false
	if _, err := os.Stat(managedRoot); err == nil {
		managedExists = true
	} else if !os.IsNotExist(err) {
		return err
	}

	// db.json restoration is independent of whether the managed dir still exists.
	needsDBRestore := service.needsDBJsonRestore()

	// The applied-selection marker from the old layout describes files the
	// cutover retires; keeping it makes Play report missing managed files.
	if marker, exists, err := service.readPlayRuntimeMarker(); err != nil {
		return err
	} else if exists && (samePath(marker.ModsPath, managedRoot) || pathWithin(marker.ModsPath, managedRoot)) {
		if err := service.removePlayRuntimeMarker(); err != nil {
			return fmt.Errorf("remove legacy Play marker: %w", err)
		}
	}

	if !managedExists && !needsDBRestore {
		return nil
	}

	adopted, retired, movedToLib, droppedEntries, restoredEntries := 0, 0, 0, 0, 0

	if managedExists {
		// 1. Adoption.
		if err := service.adoptExistingManagedEntries(ctx); err != nil {
			return fmt.Errorf("cutover adoption: %w", err)
		}

		// 2. Retire owned Play entries in the old managed dir.
		ownedEntries, err := service.store.listOwnedArchiveEntries(ctx)
		if err != nil {
			return err
		}
		var retireEntityIDs []string
		for _, entry := range ownedEntries {
			if entry.Purpose != archivePurposePlay || entry.OwnerID != playDeploymentOwnerID || entry.State != archiveStateActive {
				continue
			}
			if samePath(entry.TargetRoot, managedRoot) || pathWithin(entry.TargetRoot, managedRoot) {
				retireEntityIDs = append(retireEntityIDs, entry.EntityID)
			}
		}
		if len(retireEntityIDs) > 0 {
			if err := service.retireOwnedArchiveEntries(ctx, retireEntityIDs); err != nil {
				return fmt.Errorf("cutover retire: %w", err)
			}
			retired = len(retireEntityIDs)
		}

		// 3. Move remaining unowned zips to LibraryDir/From BeamNG/.
		files, err := os.ReadDir(managedRoot)
		if err != nil {
			return err
		}
		fromDir := filepath.Join(service.config.LibraryDir, "From BeamNG")
		var moveFailures []string
		for _, f := range files {
			if f.IsDir() || !isModArchive(f.Name()) {
				continue
			}
			src := filepath.Join(managedRoot, f.Name())
			if err := os.MkdirAll(fromDir, 0o755); err != nil {
				moveFailures = append(moveFailures, fmt.Sprintf("%s: mkdir: %v", f.Name(), err))
				continue
			}
			dest := uniqueFilePath(fromDir, f.Name())
			if err := moveFileVerified(ctx, service, src, dest); err != nil {
				moveFailures = append(moveFailures, fmt.Sprintf("%s: %v", f.Name(), err))
				continue
			}
			_ = service.store.recordStudioPlacedArchive(ctx, dest)
			movedToLib++
		}
		if len(moveFailures) > 0 {
			return fmt.Errorf("cutover move failures: %s", strings.Join(moveFailures, "; "))
		}

		// Remove the managed dir only if empty.
		remaining, _ := os.ReadDir(managedRoot)
		if len(remaining) == 0 {
			_ = os.Remove(managedRoot)
		}
	}

	// 4. Restore real db.json.
	if needsDBRestore {
		dropped, restored, err := service.restoreRealDBJson()
		if err != nil {
			return fmt.Errorf("cutover db.json restore: %w", err)
		}
		droppedEntries, restoredEntries = dropped, restored
	}

	// 5. Record event only after full success.
	_ = service.store.AppendEvent(ctx, "", "play_profile_migrated", map[string]any{
		"adopted": adopted, "retired": retired, "movedToLibrary": movedToLib,
		"dbEntriesDropped": droppedEntries, "dbEntriesRestored": restoredEntries,
	})

	return nil
}

// needsDBJsonRestore returns true when db.json still has beamworlds-managed
// entries, or when .beamworlds-original or .beamworlds-backup files exist.
func (service *AppService) needsDBJsonRestore() bool {
	dbPath := filepath.Join(service.config.ActiveModsDir, "db.json")
	if _, err := os.Stat(dbPath + ".beamworlds-original"); err == nil {
		return true
	}
	if _, err := os.Stat(dbPath + ".beamworlds-backup"); err == nil {
		return true
	}
	data, err := os.ReadFile(dbPath)
	if err != nil {
		return false
	}
	return bytes.Contains(bytes.ToLower(data), []byte("beamworlds-managed"))
}

// restoreRealDBJson drops managed entries and restores original active states.
// Returns counts of dropped and restored entries.
func (service *AppService) restoreRealDBJson() (dropped, restored int, err error) {
	dbPath := filepath.Join(service.config.ActiveModsDir, "db.json")
	originalPath := dbPath + ".beamworlds-original"
	backupPath := dbPath + ".beamworlds-backup"

	data, err := os.ReadFile(dbPath)
	if errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(originalPath)
		_ = os.Remove(backupPath)
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}), &document); err != nil {
		return 0, 0, fmt.Errorf("parse db.json: %w", err)
	}

	mods := map[string]json.RawMessage{}
	if raw := document["mods"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &mods); err != nil {
			return 0, 0, fmt.Errorf("parse db.json mods: %w", err)
		}
	}

	var originalMods map[string]json.RawMessage
	if origData, err := os.ReadFile(originalPath); err == nil {
		var origDoc map[string]json.RawMessage
		if err := json.Unmarshal(bytes.TrimPrefix(origData, []byte{0xef, 0xbb, 0xbf}), &origDoc); err == nil {
			if raw := origDoc["mods"]; len(raw) > 0 {
				_ = json.Unmarshal(raw, &originalMods)
			}
		}
	}

	changed := false
	for key := range mods {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(mods[key], &entry); err != nil {
			continue
		}

		isManaged := false
		for _, field := range []string{"fullpath", "dirname"} {
			if raw, ok := entry[field]; ok {
				var val string
				if json.Unmarshal(raw, &val) == nil {
					if strings.Contains(strings.ToLower(val), "beamworlds-managed") {
						isManaged = true
						break
					}
				}
			}
		}
		if isManaged {
			delete(mods, key)
			dropped++
			changed = true
			continue
		}

		if originalMods != nil {
			if origRaw, ok := originalMods[key]; ok {
				var origEntry map[string]json.RawMessage
				if json.Unmarshal(origRaw, &origEntry) == nil {
					if activeRaw, ok := origEntry["active"]; ok {
						entry["active"] = activeRaw
						encoded, err := json.Marshal(entry)
						if err == nil {
							mods[key] = encoded
							restored++
							changed = true
						}
					}
				}
			}
		}
	}

	if !changed {
		_ = os.Remove(originalPath)
		_ = os.Remove(backupPath)
		return 0, 0, nil
	}

	encoded, err := json.Marshal(mods)
	if err != nil {
		return dropped, restored, err
	}
	document["mods"] = encoded
	updated, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return dropped, restored, err
	}
	if err := writeFileAtomic(dbPath, append(updated, '\n'), 0o644); err != nil {
		return dropped, restored, err
	}
	_ = os.Remove(originalPath)
	_ = os.Remove(backupPath)
	return dropped, restored, nil
}

// harvestSessionDownloads reconciles profile session downloads and triggers a
// library scan if any were harvested.
func (service *AppService) harvestSessionDownloads(playRoot string) {
	service.modImportMu.Lock()
	harvested, err := service.reconcileProfileSessionDownloads(context.Background(), playRoot)
	service.modImportMu.Unlock()
	if err != nil {
		log.Printf("play profile: harvest session downloads: %v", err)
	}
	if harvested > 0 {
		log.Printf("play profile: harvested %d session downloads", harvested)
		if _, err := service.ScanLibrary(); err != nil {
			log.Printf("play profile: post-harvest scan: %v", err)
		}
	}
}
