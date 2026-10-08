package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Organization exposes the unified collection graph, tags, and saved Play
// profiles. Folders, presets, and private profile defaults are intentionally
// not part of the live API after the organization migration.
func (service *AppService) Organization() (OrganizationState, error) {
	return service.store.Organization(context.Background())
}

func (service *AppService) CreateCollection(name, description, parentID string) (CollectionDetail, error) {
	return service.store.CreateCollection(context.Background(), name, description, parentID)
}

func (service *AppService) GetCollection(collectionID string) (CollectionDetail, error) {
	return service.store.CollectionDetail(context.Background(), collectionID)
}

func (service *AppService) UpdateCollection(collectionID, name, description string) (CollectionDetail, error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	detail, err := service.store.UpdateCollection(context.Background(), collectionID, name, description)
	if err == nil { service.reportCollectionMirrorRetirement(context.Background()) }
	return detail, err
}

func (service *AppService) DuplicateCollection(collectionID string) (CollectionDetail, error) {
	return service.store.DuplicateCollection(context.Background(), collectionID)
}

func (service *AppService) GetCollectionDeleteImpact(collectionIDs []string) (CollectionUsage, error) {
	return service.store.GetCollectionDeleteImpact(context.Background(), collectionIDs)
}

func (service *AppService) DeleteCollections(collectionIDs []string) (OrganizationState, error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	state, err := service.store.DeleteCollections(context.Background(), collectionIDs)
	if err == nil { service.reportCollectionMirrorRetirement(context.Background()) }
	return state, err
}

func (service *AppService) SetCollectionMods(collectionID string, entityIDs []string, included bool) (CollectionDetail, error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	detail, err := service.store.SetCollectionMods(context.Background(), collectionID, entityIDs, included)
	if err == nil { service.reportCollectionMirrorRetirement(context.Background()) }
	return detail, err
}

func (service *AppService) SetCollectionChildren(collectionID string, childIDs []string, included bool) (CollectionDetail, error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	detail, err := service.store.SetCollectionChildren(context.Background(), collectionID, childIDs, included)
	if err == nil { service.reportCollectionMirrorRetirement(context.Background()) }
	return detail, err
}

func (service *AppService) SetCollectionModsEnabled(collectionID string, entityIDs []string, enabled bool) (CollectionDetail, error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	detail, err := service.store.SetCollectionModsEnabled(context.Background(), collectionID, entityIDs, enabled)
	if err == nil { service.reportCollectionMirrorRetirement(context.Background()) }
	return detail, err
}

func (service *AppService) SetCollectionChildrenEnabled(collectionID string, childIDs []string, enabled bool) (CollectionDetail, error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	detail, err := service.store.SetCollectionChildrenEnabled(context.Background(), collectionID, childIDs, enabled)
	if err == nil { service.reportCollectionMirrorRetirement(context.Background()) }
	return detail, err
}

func (service *AppService) ReorderCollectionMembers(collectionID string, entityIDs, childIDs []string) (CollectionDetail, error) {
	return service.store.ReorderCollectionMembers(context.Background(), collectionID, entityIDs, childIDs)
}

func (service *AppService) CreatePlayProfile(collectionIDs, excludedCollectionIDs []string) (ModProfile, error) {
	return service.store.CreatePlayProfile(context.Background(), collectionIDs, excludedCollectionIDs)
}

func (service *AppService) UpdatePlayProfile(profileID string, collectionIDs, excludedCollectionIDs []string) (ModProfile, error) {
	return service.store.UpdatePlayProfile(context.Background(), profileID, collectionIDs, excludedCollectionIDs)
}

func (service *AppService) RenamePlayProfile(profileID, name string) (ModProfile, error) {
	return service.store.RenamePlayProfile(context.Background(), profileID, name)
}

func (service *AppService) DeletePlayProfile(profileID string) (OrganizationState, error) {
	return service.store.DeletePlayProfile(context.Background(), profileID)
}

func (service *AppService) ResolvePlaySelection(collectionIDs, excludedCollectionIDs []string) (PlaySelection, error) {
	// The preview describes the mods Play would use, including fresh edits.
	service.flushLibrarySyncs(context.Background())
	return service.store.ResolvePlaySelection(context.Background(), collectionIDs, excludedCollectionIDs)
}

// cleanOrganizationName is shared by tags and the collection/profile APIs.
// Keep the one-line/length contract in one place so all organization names
// have identical validation and tag callers remain compatible.
func cleanOrganizationName(value, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 80 || strings.ContainsAny(value, "\r\n\t") {
		return "", fmt.Errorf("%s must contain 1 to 80 characters on one line", label)
	}
	return value, nil
}

func requireChanged(result sql.Result, err error, label string) error {
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("%s was not found", label)
	}
	return nil
}
