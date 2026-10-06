package main

// Seeding the first collection and profile from the mod set BeamNG already has
// enabled. Without this, a fresh installation has no collections, an empty Play
// selection resolves to zero mods, and the first launch would disable
// everything the user had.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	defaultProfileSeedKey  = "default_profile_seeded"
	seededCollectionName   = "My mods"
	seededProfileName      = "My setup"
	seededCollectionDetail = "Created from the mods BeamNG had enabled when BeamWorlds first indexed your library."
	// BeamWorlds' own throwaway installs share this archive prefix.
	testInstallPrefix = "modstudio-test-"
)

// beamNGEnabledArchiveKeys returns lookup keys for the archives BeamNG lists
// as active. Paths are authoritative; filenames are the fallback for library
// entries that live outside the game's mods folder.
//
// A live database with nothing active is not evidence that the user enables
// nothing: an earlier apply may have left it that way. When BeamWorlds has a
// snapshot of the state it found first, that is the better description of
// "the setup the user had", so it is read instead.
func beamNGEnabledArchiveKeys(activeModsDir string) (map[string]struct{}, map[string]string, map[string]string, int, error) {
	activeModsDir = strings.TrimSpace(activeModsDir)
	if activeModsDir == "" {
		return map[string]struct{}{}, map[string]string{}, map[string]string{}, 0, nil
	}
	for _, name := range []string{"db.json", "db.json.beamworlds-original", "db.json.beamworlds-backup"} {
		paths, filenames, resolved, enabled, err := beamNGEnabledArchiveKeysFrom(activeModsDir, filepath.Join(activeModsDir, name))
		if err != nil {
			return nil, nil, nil, 0, err
		}
		if enabled > 0 {
			return paths, filenames, resolved, enabled, nil
		}
	}
	return map[string]struct{}{}, map[string]string{}, map[string]string{}, 0, nil
}

func beamNGEnabledArchiveKeysFrom(activeModsDir, databasePath string) (map[string]struct{}, map[string]string, map[string]string, int, error) {
	paths := map[string]struct{}{}
	filenames := map[string]string{}
	resolvedPaths := map[string]string{}
	payload, err := os.ReadFile(databasePath)
	if errors.Is(err, os.ErrNotExist) {
		return paths, filenames, resolvedPaths, 0, nil
	}
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("read BeamNG mod database: %w", err)
	}
	payload = bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf})
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		// A damaged snapshot must not stop the live database from seeding.
		return paths, filenames, resolvedPaths, 0, nil
	}
	var mods map[string]json.RawMessage
	if raw := document["mods"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &mods); err != nil {
			return nil, nil, nil, 0, fmt.Errorf("parse BeamNG mod entries: %w", err)
		}
	}
	enabled := 0
	for _, raw := range mods {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entry); err != nil || entry == nil {
			continue
		}
		var active bool
		if rawActive := entry["active"]; len(rawActive) > 0 {
			if err := json.Unmarshal(rawActive, &active); err != nil {
				continue
			}
		}
		if !active {
			continue
		}
		filename := beamNGDatabaseString(entry, "filename")
		if filename == "" || strings.HasPrefix(strings.ToLower(filename), testInstallPrefix) {
			// BeamWorlds' own test installs are not the user's mods, and their
			// archives are deleted after use.
			continue
		}
		enabled++
		archivePath := beamNGDatabaseArchivePath(activeModsDir, beamNGDatabaseString(entry, "fullpath"), filename)
		filenameKey := archiveSourceFilenameKey(filename)
		paths[archiveSourcePathKey(archivePath)] = struct{}{}
		filenames[filenameKey] = filename
		resolvedPaths[filenameKey] = archivePath
	}
	return paths, filenames, resolvedPaths, enabled, nil
}

// seedDefaultPlayProfile runs at most once, after a scan has indexed something.
// It is deliberately silent about failures: a seeding problem must never break
// a scan, and the next scan retries until the marker is written.
func (service *AppService) seedDefaultPlayProfile(ctx context.Context) {
	store := service.store
	if store == nil {
		return
	}
	if marker, err := store.readSetting(ctx, defaultProfileSeedKey); err == nil && strings.TrimSpace(marker) != "" {
		return
	}
	organization, err := store.Organization(ctx)
	if err != nil {
		return
	}
	if len(organization.Collections) > 0 || len(organization.Profiles) > 0 {
		// The user already organizes their library; there is nothing to seed
		// and never will be.
		_ = store.writeSetting(ctx, defaultProfileSeedKey, "1")
		return
	}
	items, err := store.ListLibrary(ctx, "", "", "", "", "active")
	if err != nil || len(items) == 0 {
		return
	}
	paths, filenames, resolvedPaths, enabledCount, err := beamNGEnabledArchiveKeys(service.config.ActiveModsDir)
	if err != nil || enabledCount == 0 {
		return
	}
	// One archive is often indexed under several scan roots. An enabled entry
	// means one mod, so each enabled filename contributes exactly one entity:
	// the one whose path BeamNG actually names, else the copy in the library,
	// else the first seen.
	chosen := map[string]LibraryItem{}
	exact := map[string]bool{}
	libraryDir := archiveSourcePathKey(strings.TrimSpace(service.config.LibraryDir))
	for _, item := range items {
		archivePath := strings.TrimSpace(item.ArchivePath)
		if archivePath == "" {
			continue
		}
		pathKey := archiveSourcePathKey(archivePath)
		filenameKey := archiveSourceFilenameKey(archivePath)
		_, byPath := paths[pathKey]
		_, byFilename := filenames[filenameKey]
		if !byPath && !byFilename {
			continue
		}
		if byPath {
			chosen[filenameKey] = item
			exact[filenameKey] = true
			continue
		}
		if exact[filenameKey] {
			continue
		}
		current, taken := chosen[filenameKey]
		if !taken {
			chosen[filenameKey] = item
			continue
		}
		inLibrary := libraryDir != "" && strings.HasPrefix(pathKey, libraryDir)
		currentInLibrary := libraryDir != "" && strings.HasPrefix(archiveSourcePathKey(current.ArchivePath), libraryDir)
		if inLibrary && !currentInLibrary {
			chosen[filenameKey] = item
		}
	}
	entityIDs := make([]string, 0, len(chosen))
	matchedFilenames := map[string]struct{}{}
	for filenameKey, item := range chosen {
		entityIDs = append(entityIDs, item.EntityID)
		matchedFilenames[filenameKey] = struct{}{}
	}
	slices.Sort(entityIDs)
	if len(entityIDs) == 0 {
		return
	}
	profile, collectionID, err := store.SeedDefaultPlayProfile(ctx, seededCollectionName, seededCollectionDetail, seededProfileName, entityIDs)
	if err != nil {
		return
	}
	notices := enabledModNotices(filenames, matchedFilenames, resolvedPaths, service.config.ActiveModsDir)
	if _, err := store.SavePlayState(ctx, PlayState{
		ProfileID:     profile.ID,
		CollectionIDs: []string{collectionID},
		Notices:       notices,
	}); err != nil {
		return
	}
	_ = store.writeSetting(ctx, defaultProfileSeedKey, "1")
}

// reconcilePlayNotices re-evaluates persisted Play notices against the current
// library and BeamNG mod state. It runs after every scan and at startup so
// that notices from the seed or older code versions are replaced with
// current-format diagnostics when the condition persists, or removed when it
// has resolved. Unknown notices (those the reconciler does not recognise) are
// preserved so that externally-generated diagnostics are never suppressed.
func (service *AppService) reconcilePlayNotices(ctx context.Context) {
	store := service.store
	if store == nil {
		return
	}
	state, err := store.GetPlayState(ctx)
	if err != nil || len(state.Notices) == 0 {
		return
	}

	// Identify which pre-existing notices are stale seed or legacy notices.
	// Current normalization notices (from GetPlayState above) are kept.
	freshNotices := make([]string, 0, len(state.Notices))
	hadSeedNotice := false
	for _, notice := range state.Notices {
		switch {
		case isSeedMissingNotice(notice):
			hadSeedNotice = true
			// Handled below: replaced with current evaluation if still valid.
		case isStaleNormalizationNotice(notice):
			// Legacy notice format from older code. The reference was already
			// removed; current normalizePlayStateTx regenerates valid notices.
		default:
			freshNotices = append(freshNotices, notice)
		}
	}

	// Re-evaluate the seed condition only if there was a seed notice to
	// reconcile. Without a prior seed notice, adding one here would be
	// surprising — the seed itself is the only entry point for that warning.
	if hadSeedNotice {
		// Find the original seed notice text for error-path preservation.
		var originalSeedNotice string
		for _, notice := range state.Notices {
			if isSeedMissingNotice(notice) {
				originalSeedNotice = notice
				break
			}
		}
		_, filenames, resolvedPaths, enabledCount, keyErr := beamNGEnabledArchiveKeys(service.config.ActiveModsDir)
		if keyErr != nil {
			// Cannot re-evaluate; preserve the original notice so it is not
			// silently dropped on a transient error.
			freshNotices = append(freshNotices, originalSeedNotice)
		} else if enabledCount > 0 {
			items, listErr := store.ListLibrary(ctx, "", "", "", "", "active")
			if listErr != nil {
				freshNotices = append(freshNotices, originalSeedNotice)
			} else {
				matched := map[string]struct{}{}
				for _, item := range items {
					filenameKey := archiveSourceFilenameKey(strings.TrimSpace(item.ArchivePath))
					if _, ok := filenames[filenameKey]; ok {
						matched[filenameKey] = struct{}{}
					}
				}
				freshNotices = append(freshNotices, enabledModNotices(filenames, matched, resolvedPaths, service.config.ActiveModsDir)...)
			}
		}
		// enabledCount == 0 and no error: condition resolved, no notice emitted.
	}

	if len(freshNotices) == len(state.Notices) && !hadSeedNotice {
		return
	}

	state.Notices = freshNotices
	_, _ = store.SavePlayState(ctx, state)
}

// isSeedMissingNotice returns true for notices generated by the seed (current
// and historical formats) about enabled mods missing from the library.
func isSeedMissingNotice(notice string) bool {
	if strings.HasPrefix(notice, "Studio could not index BeamNG mod ") ||
		strings.HasPrefix(notice, "Cannot access BeamNG mod ") ||
		strings.HasPrefix(notice, "BeamNG mod folder ") {
		return true
	}
	// Current format: 'Enabled in BeamNG but not found in your library: …'
	if strings.HasPrefix(notice, "Enabled in BeamNG but not found") {
		return true
	}
	// Previous format: 'Not added to "My mods", because …'
	if strings.HasPrefix(notice, "Not added to ") && strings.Contains(notice, "not in your library yet") {
		return true
	}
	// Legacy format: 'N mod(s) enabled in BeamNG …'
	if strings.Contains(notice, "enabled in BeamNG") && strings.Contains(notice, "not in your library") {
		return true
	}
	return false
}

// Only historical formats are retired. Newly detected dangling references use
// the current user-facing messages and remain available until dismissed.
func isStaleNormalizationNotice(notice string) bool {
	if strings.HasPrefix(notice, "collection \"") && strings.Contains(notice, "was deleted and removed from the Play draft") ||
		strings.HasPrefix(notice, "saved Play profile \"") && strings.Contains(notice, "was deleted; switched to Default") {
		return true
	}
	// Legacy count-based formats from older code versions. Current code
	// never produces these strings; they are zombies from previous runs.
	if strings.Contains(notice, "selected collection was removed from Play") {
		return true
	}
	if strings.Contains(notice, "collection was removed from Play") {
		return true
	}
	return false
}

// Diagnose only unmatched, existing files. Deleted db.json entries are not
// installed mods; generated deployments are not separate library archives.
func enabledModNotices(filenames map[string]string, matched map[string]struct{}, resolvedPaths map[string]string, activeModsDir string) []string {
	notices := []string{}
	managedRoot := filepath.Join(activeModsDir, managedModDirectoryName)
	for key, name := range filenames {
		if _, found := matched[key]; found {
			continue
		}
		resolved := resolvedPaths[key]
		if resolved == "" || pathWithin(resolved, managedRoot) {
			continue
		}
		info, err := os.Stat(resolved)
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			notices = append(notices, fmt.Sprintf("Cannot access BeamNG mod %q at %q: %v", name, resolved, err))
		case info.IsDir():
			notices = append(notices, fmt.Sprintf("BeamNG mod folder %q is unpacked. Studio supports ZIP mods, not unpacked folders. Location: %s", name, resolved))
		default:
			notices = append(notices, fmt.Sprintf("Studio could not index BeamNG mod %q. File: %s", name, resolved))
		}
	}
	slices.Sort(notices)
	return notices
}
