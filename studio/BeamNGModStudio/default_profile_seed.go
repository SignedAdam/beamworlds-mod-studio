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
)

// beamNGEnabledArchiveKeys returns lookup keys for the archives BeamNG lists
// as active. Paths are authoritative; filenames are the fallback for library
// entries that live outside the game's mods folder.
//
// A live database with nothing active is not evidence that the user enables
// nothing: an earlier apply may have left it that way. When BeamWorlds has a
// snapshot of the state it found first, that is the better description of
// "the setup the user had", so it is read instead.
func beamNGEnabledArchiveKeys(activeModsDir string) (map[string]struct{}, map[string]struct{}, int, error) {
	activeModsDir = strings.TrimSpace(activeModsDir)
	if activeModsDir == "" {
		return map[string]struct{}{}, map[string]struct{}{}, 0, nil
	}
	for _, name := range []string{"db.json", "db.json.beamworlds-original", "db.json.beamworlds-backup"} {
		paths, filenames, enabled, err := beamNGEnabledArchiveKeysFrom(activeModsDir, filepath.Join(activeModsDir, name))
		if err != nil {
			return nil, nil, 0, err
		}
		if enabled > 0 {
			return paths, filenames, enabled, nil
		}
	}
	return map[string]struct{}{}, map[string]struct{}{}, 0, nil
}

func beamNGEnabledArchiveKeysFrom(activeModsDir, databasePath string) (map[string]struct{}, map[string]struct{}, int, error) {
	paths := map[string]struct{}{}
	filenames := map[string]struct{}{}
	payload, err := os.ReadFile(databasePath)
	if errors.Is(err, os.ErrNotExist) {
		return paths, filenames, 0, nil
	}
	if err != nil {
		return nil, nil, 0, fmt.Errorf("read BeamNG mod database: %w", err)
	}
	payload = bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf})
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		// A damaged snapshot must not stop the live database from seeding.
		return paths, filenames, 0, nil
	}
	var mods map[string]json.RawMessage
	if raw := document["mods"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &mods); err != nil {
			return nil, nil, 0, fmt.Errorf("parse BeamNG mod entries: %w", err)
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
		if filename == "" {
			continue
		}
		enabled++
		paths[archiveSourcePathKey(beamNGDatabaseArchivePath(activeModsDir, beamNGDatabaseString(entry, "fullpath"), filename))] = struct{}{}
		filenames[archiveSourceFilenameKey(filename)] = struct{}{}
	}
	return paths, filenames, enabled, nil
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
	items, err := store.ListLibrary(ctx, "", "", "", "")
	if err != nil || len(items) == 0 {
		return
	}
	paths, filenames, enabledCount, err := beamNGEnabledArchiveKeys(service.config.ActiveModsDir)
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
	notices := []string{}
	if unmatched := len(filenames) - len(matchedFilenames); unmatched > 0 {
		notices = append(notices, fmt.Sprintf("%d mod%s enabled in BeamNG %s not in your library yet, so %s not in %q.",
			unmatched, plural(unmatched, "", "s"), plural(unmatched, "is", "are"), plural(unmatched, "it is", "they are"), seededCollectionName))
	}
	if _, err := store.SavePlayState(ctx, PlayState{
		ProfileID:     profile.ID,
		CollectionIDs: []string{collectionID},
		Notices:       notices,
	}); err != nil {
		return
	}
	_ = store.writeSetting(ctx, defaultProfileSeedKey, "1")
}

func plural(count int, singular, pluralForm string) string {
	if count == 1 {
		return singular
	}
	return pluralForm
}
