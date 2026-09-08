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
	"strings"
)

const (
	defaultProfileSeedKey  = "default_profile_seeded"
	seededCollectionName   = "My mods"
	seededProfileName      = "My setup"
	seededCollectionDetail = "Created from the mods BeamNG had enabled when BeamWorlds first indexed your library."
)

// beamNGEnabledArchiveKeys returns lookup keys for the archives BeamNG lists as
// active. Paths are authoritative; filenames are the fallback for library
// entries that live outside the game's mods folder.
func beamNGEnabledArchiveKeys(activeModsDir string) (map[string]struct{}, map[string]struct{}, int, error) {
	paths := map[string]struct{}{}
	filenames := map[string]struct{}{}
	activeModsDir = strings.TrimSpace(activeModsDir)
	if activeModsDir == "" {
		return paths, filenames, 0, nil
	}
	payload, err := os.ReadFile(filepath.Join(activeModsDir, "db.json"))
	if errors.Is(err, os.ErrNotExist) {
		return paths, filenames, 0, nil
	}
	if err != nil {
		return nil, nil, 0, fmt.Errorf("read BeamNG mod database: %w", err)
	}
	payload = bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf})
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		return nil, nil, 0, fmt.Errorf("parse BeamNG mod database: %w", err)
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
	entityIDs := make([]string, 0, len(items))
	matchedFilenames := map[string]struct{}{}
	for _, item := range items {
		archivePath := strings.TrimSpace(item.ArchivePath)
		if archivePath == "" {
			continue
		}
		filenameKey := archiveSourceFilenameKey(archivePath)
		_, byPath := paths[archiveSourcePathKey(archivePath)]
		_, byFilename := filenames[filenameKey]
		if !byPath && !byFilename {
			continue
		}
		entityIDs = append(entityIDs, item.EntityID)
		matchedFilenames[filenameKey] = struct{}{}
	}
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
