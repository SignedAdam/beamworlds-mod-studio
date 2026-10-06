package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

const (
	collectionDescriptionLimit = 500
	automaticCollectionCover   = `{"mode":"automatic","images":[]}`
)

// A membership edge carries its own enabled flag. Disabled means "still a
// member, contributes nothing", so ordering and identity survive a toggle.
type collectionNode struct {
	collection      ModCollection
	directEntityIDs []string
	members         []CollectionMember
	childIDs        []string
	children        []CollectionChild
	parentIDs       []string
	coverAssetSHA   string
}

type collectionGraph map[string]*collectionNode

type collectionModProvenance struct {
	collectionIDs []string
	rootIDs       []string
	collections   map[string]struct{}
	roots         map[string]struct{}
}

type collectionModMetadata struct {
	entityID     string
	artifactID   string
	displayName  string
	kind         modkit.Kind
	archivePath  string
	sha256       string
	sizeBytes    int64
	modifiedAt   string
	active       int
	archivedAt   string
	thumbnailSHA string
}

func normalizeCollectionDescription(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > collectionDescriptionLimit {
		return "", fmt.Errorf("collection description exceeds %d characters", collectionDescriptionLimit)
	}
	return value, nil
}

func normalizeOrganizationIDs(values []string, label string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%s contains an empty ID", label)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func normalizeProfileCollectionIDs(values []string) ([]string, error) {
	return normalizeOrganizationIDs(values, "collection IDs")
}

func organizationUniqueNameError(err error, label, name string) error {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return fmt.Errorf("%s %q already exists", label, name)
	}
	return err
}

func (s *Store) Organization(ctx context.Context) (OrganizationState, error) {
	collections, err := s.listCollections(ctx)
	if err != nil {
		return OrganizationState{}, err
	}
	tags, err := s.listModTags(ctx)
	if err != nil {
		return OrganizationState{}, err
	}
	profiles, err := s.listPlayProfiles(ctx)
	if err != nil {
		return OrganizationState{}, err
	}
	return OrganizationState{Collections: collections, Tags: tags, Profiles: profiles}, nil
}

func (s *Store) listCollections(ctx context.Context) ([]ModCollection, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	graph, err := loadCollectionGraphTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	nodes := make([]*collectionNode, 0, len(graph))
	for _, node := range graph {
		nodes = append(nodes, node)
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].collection.Position != nodes[j].collection.Position {
			return nodes[i].collection.Position < nodes[j].collection.Position
		}
		left, right := strings.ToLower(nodes[i].collection.Name), strings.ToLower(nodes[j].collection.Name)
		if left != right {
			return left < right
		}
		return nodes[i].collection.ID < nodes[j].collection.ID
	})
	result := make([]ModCollection, 0, len(nodes))
	for _, node := range nodes {
		selection, err := resolveCollectionSelectionGraphTx(ctx, tx, graph, []string{node.collection.ID}, nil, false)
		if err != nil {
			return nil, err
		}
		node.collection.ModCount = len(selection.Mods)
		node.collection.ArchivedModCount = selection.ArchivedCount
		node.collection.CoverURL = collectionCoverURL(node, selection.Mods)
		result = append(result, node.collection)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) listPlayProfiles(ctx context.Context) ([]ModProfile, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	graph, err := loadCollectionGraphTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,name,updated_at FROM play_profiles ORDER BY name COLLATE NOCASE,id`)
	if err != nil {
		return nil, err
	}
	profiles := make([]ModProfile, 0)
	for rows.Next() {
		var profile ModProfile
		if err := rows.Scan(&profile.ID, &profile.Name, &profile.UpdatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids, excludedIDs, err := profileCollectionIDsTx(ctx, tx, profile.ID)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		profile.CollectionIDs = ids
		profile.ExcludedCollectionIDs = excludedIDs
		profile.CollectionCount = len(ids)
		selection, err := resolveCollectionSelectionGraphTx(ctx, tx, graph, ids, excludedIDs, true)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("resolve profile %q: %w", profile.Name, err)
		}
		profile.ModCount = len(selection.Mods)
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return profiles, nil
}

func loadCollectionGraphTx(ctx context.Context, tx *sql.Tx) (collectionGraph, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,description,updated_at,position,cover_json,cover_asset_sha FROM collections ORDER BY position,name COLLATE NOCASE,id`)
	if err != nil {
		return nil, err
	}
	graph := collectionGraph{}
	for rows.Next() {
		var node collectionNode
		var rawCover string
		if err := rows.Scan(&node.collection.ID, &node.collection.Name, &node.collection.Description, &node.collection.UpdatedAt, &node.collection.Position, &rawCover, &node.coverAssetSHA); err != nil {
			_ = rows.Close()
			return nil, err
		}
		cover, err := decodeCollectionCover(rawCover)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("collection %q cover: %w", node.collection.ID, err)
		}
		node.collection.Cover = cover
		node.collection.ChildIDs = []string{}
		node.collection.ParentIDs = []string{}
		node.directEntityIDs = []string{}
		node.members = []CollectionMember{}
		node.children = []CollectionChild{}
		graph[node.collection.ID] = &node
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	childRows, err := tx.QueryContext(ctx, `SELECT parent_id,child_id,enabled FROM collection_children ORDER BY parent_id,position,child_id`)
	if err != nil {
		return nil, err
	}
	for childRows.Next() {
		var parentID, childID string
		var enabled int
		if err := childRows.Scan(&parentID, &childID, &enabled); err != nil {
			_ = childRows.Close()
			return nil, err
		}
		parent, parentOK := graph[parentID]
		child, childOK := graph[childID]
		if !parentOK || !childOK {
			_ = childRows.Close()
			return nil, fmt.Errorf("collection graph contains missing edge %q -> %q", parentID, childID)
		}
		parent.childIDs = append(parent.childIDs, childID)
		parent.children = append(parent.children, CollectionChild{CollectionID: childID, Enabled: enabled != 0})
		parent.collection.ChildIDs = append(parent.collection.ChildIDs, childID)
		child.parentIDs = append(child.parentIDs, parentID)
		child.collection.ParentIDs = append(child.collection.ParentIDs, parentID)
	}
	if err := childRows.Err(); err != nil {
		_ = childRows.Close()
		return nil, err
	}
	if err := childRows.Close(); err != nil {
		return nil, err
	}

	modRows, err := tx.QueryContext(ctx, `SELECT collection_id,entity_id,enabled,disabled_by_archive FROM collection_mods ORDER BY collection_id,position,entity_id`)
	if err != nil {
		return nil, err
	}
	for modRows.Next() {
		var collectionID, entityID string
		var enabled, disabledByArchive int
		if err := modRows.Scan(&collectionID, &entityID, &enabled, &disabledByArchive); err != nil {
			_ = modRows.Close()
			return nil, err
		}
		node, ok := graph[collectionID]
		if !ok {
			_ = modRows.Close()
			return nil, fmt.Errorf("collection membership references missing collection %q", collectionID)
		}
		node.directEntityIDs = append(node.directEntityIDs, entityID)
		node.members = append(node.members, CollectionMember{EntityID: entityID, Enabled: enabled != 0, DisabledByArchive: disabledByArchive != 0})
		node.collection.DirectModCount++
		if enabled != 0 {
			node.collection.DirectEnabledCount++
		}
	}
	if err := modRows.Err(); err != nil {
		_ = modRows.Close()
		return nil, err
	}
	if err := modRows.Close(); err != nil {
		return nil, err
	}
	for _, node := range graph {
		node.collection.ChildCount = len(node.childIDs)
		node.collection.ChildIDs = append([]string(nil), node.childIDs...)
		node.collection.ParentIDs = append([]string(nil), node.parentIDs...)
		if node.collection.ChildIDs == nil {
			node.collection.ChildIDs = []string{}
		}
		if node.collection.ParentIDs == nil {
			node.collection.ParentIDs = []string{}
		}
	}
	return graph, nil
}

func decodeCollectionCover(raw string) (CollectionCover, error) {
	if strings.TrimSpace(raw) == "" {
		raw = automaticCollectionCover
	}
	var cover CollectionCover
	if err := json.Unmarshal([]byte(raw), &cover); err != nil {
		return CollectionCover{}, err
	}
	cover.Mode = strings.ToLower(strings.TrimSpace(cover.Mode))
	if cover.Mode == "" {
		cover.Mode = "automatic"
	}
	if cover.Mode != "automatic" && cover.Mode != "single" && cover.Mode != "collage" {
		return CollectionCover{}, fmt.Errorf("unsupported cover mode %q", cover.Mode)
	}
	if cover.Images == nil {
		cover.Images = []CollectionCoverImage{}
	}
	if cover.Mode == "automatic" && len(cover.Images) != 0 {
		return CollectionCover{}, errors.New("automatic cover cannot contain images")
	}
	if cover.Mode == "single" && len(cover.Images) != 1 {
		return CollectionCover{}, errors.New("single cover requires exactly one image")
	}
	if cover.Mode == "collage" && (len(cover.Images) < 2 || len(cover.Images) > 9) {
		return CollectionCover{}, errors.New("collage cover requires 2 to 9 images")
	}
	seen := make(map[string]struct{}, len(cover.Images))
	for _, image := range cover.Images {
		assetID := strings.TrimSpace(image.AssetID)
		if assetID == "" {
			return CollectionCover{}, errors.New("cover image asset ID is required")
		}
		if _, ok := seen[assetID]; ok {
			return CollectionCover{}, fmt.Errorf("cover image %q is duplicated", assetID)
		}
		seen[assetID] = struct{}{}
		if image.FocalX < 0 || image.FocalX > 1 || image.FocalY < 0 || image.FocalY > 1 {
			return CollectionCover{}, fmt.Errorf("cover focal position for %q must be between 0 and 1", assetID)
		}
	}
	return cover, nil
}

func collectionCoverURL(node *collectionNode, mods []CollectionMod) string {
	if node.collection.Cover.Mode != "automatic" {
		if node.coverAssetSHA != "" {
			return "/cache/" + node.coverAssetSHA
		}
		if len(node.collection.Cover.Images) > 0 && node.collection.Cover.Images[0].AssetID != "" {
			return "/cache/" + node.collection.Cover.Images[0].AssetID
		}
		return ""
	}
	for _, mod := range mods {
		if mod.ArchivedAt != "" {
			continue
		}
		if mod.Available && mod.ThumbnailURL != "" {
			return mod.ThumbnailURL
		}
	}
	return ""
}

func (s *Store) CollectionDetail(ctx context.Context, collectionID string) (CollectionDetail, error) {
	collectionID = strings.TrimSpace(collectionID)
	if collectionID == "" {
		return CollectionDetail{}, errors.New("collection ID is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CollectionDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	graph, err := loadCollectionGraphTx(ctx, tx)
	if err != nil {
		return CollectionDetail{}, err
	}
	detail, err := collectionDetailGraphTx(ctx, tx, graph, collectionID)
	if err != nil {
		return CollectionDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return CollectionDetail{}, err
	}
	return detail, nil
}

func collectionDetailGraphTx(ctx context.Context, tx *sql.Tx, graph collectionGraph, collectionID string) (CollectionDetail, error) {
	node, ok := graph[collectionID]
	if !ok {
		return CollectionDetail{}, sql.ErrNoRows
	}
	selection, err := resolveCollectionSelectionGraphTx(ctx, tx, graph, []string{collectionID}, nil, false)
	if err != nil {
		return CollectionDetail{}, err
	}
	node.collection.ModCount = len(selection.Mods)
	node.collection.ArchivedModCount = selection.ArchivedCount
	node.collection.CoverURL = collectionCoverURL(node, selection.Mods)
	usage, err := collectionUsageGraphTx(ctx, tx, graph, []string{collectionID})
	if err != nil {
		return CollectionDetail{}, err
	}
	return CollectionDetail{
		Collection: node.collection,
		Members:    append([]CollectionMember(nil), node.members...),
		Children:   append([]CollectionChild(nil), node.children...),
		Mods:       selection.Mods,
		Usage:      usage,
	}, nil
}

func (s *Store) CreateCollection(ctx context.Context, name, description, parentID string) (CollectionDetail, error) {
	name, err := cleanOrganizationName(name, "collection name")
	if err != nil {
		return CollectionDetail{}, err
	}
	description, err = normalizeCollectionDescription(description)
	if err != nil {
		return CollectionDetail{}, err
	}
	parentID = strings.TrimSpace(parentID)
	id, err := modkit.NewID()
	if err != nil {
		return CollectionDetail{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CollectionDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if parentID != "" {
		if err := requireCollectionExistsTx(ctx, tx, parentID); err != nil {
			return CollectionDetail{}, err
		}
	}
	now := nowUTC()
	var position int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),-1)+1 FROM collections`).Scan(&position); err != nil {
		return CollectionDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO collections(id,name,description,position,cover_json,cover_asset_sha,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, id, name, description, position, automaticCollectionCover, "", now, now); err != nil {
		return CollectionDetail{}, organizationUniqueNameError(err, "collection", name)
	}
	if parentID != "" {
		var childPosition int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),-1)+1 FROM collection_children WHERE parent_id=?`, parentID).Scan(&childPosition); err != nil {
			return CollectionDetail{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO collection_children(parent_id,child_id,position) VALUES(?,?,?)`, parentID, id, childPosition); err != nil {
			return CollectionDetail{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE collections SET updated_at=? WHERE id=?`, now, parentID); err != nil {
			return CollectionDetail{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CollectionDetail{}, err
	}
	return s.CollectionDetail(ctx, id)
}

func (s *Store) UpdateCollection(ctx context.Context, collectionID, name, description string) (CollectionDetail, error) {
	collectionID = strings.TrimSpace(collectionID)
	if collectionID == "" {
		return CollectionDetail{}, errors.New("collection ID is required")
	}
	name, err := cleanOrganizationName(name, "collection name")
	if err != nil {
		return CollectionDetail{}, err
	}
	description, err = normalizeCollectionDescription(description)
	if err != nil {
		return CollectionDetail{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CollectionDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var oldName string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM collections WHERE id=?`, collectionID).Scan(&oldName); err != nil {
		return CollectionDetail{}, err
	}
	entityIDs, err := collectionDirectEntityIDsTx(ctx, tx, collectionID)
	if err != nil {
		return CollectionDetail{}, err
	}
	now := nowUTC()
	result, err := tx.ExecContext(ctx, `UPDATE collections SET name=?,description=?,updated_at=? WHERE id=?`, name, description, now, collectionID)
	if err != nil {
		return CollectionDetail{}, organizationUniqueNameError(err, "collection", name)
	}
	if err := requireChanged(result, nil, "collection"); err != nil {
		return CollectionDetail{}, err
	}
	if oldName != name {
		for _, entityID := range entityIDs {
			if err := s.refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
				return CollectionDetail{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return CollectionDetail{}, err
	}
	return s.CollectionDetail(ctx, collectionID)
}

func (s *Store) DuplicateCollection(ctx context.Context, collectionID string) (CollectionDetail, error) {
	collectionID = strings.TrimSpace(collectionID)
	if collectionID == "" {
		return CollectionDetail{}, errors.New("collection ID is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CollectionDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	graph, err := loadCollectionGraphTx(ctx, tx)
	if err != nil {
		return CollectionDetail{}, err
	}
	source, ok := graph[collectionID]
	if !ok {
		return CollectionDetail{}, sql.ErrNoRows
	}
	name, err := nextCollectionNameTx(ctx, tx, source.collection.Name+" Copy")
	if err != nil {
		return CollectionDetail{}, err
	}
	newID, err := modkit.NewID()
	if err != nil {
		return CollectionDetail{}, err
	}
	now := nowUTC()
	coverJSON, err := json.Marshal(source.collection.Cover)
	if err != nil {
		return CollectionDetail{}, err
	}
	var position int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),-1)+1 FROM collections`).Scan(&position); err != nil {
		return CollectionDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO collections(id,name,description,position,cover_json,cover_asset_sha,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, newID, name, source.collection.Description, position, string(coverJSON), source.coverAssetSHA, now, now); err != nil {
		return CollectionDetail{}, organizationUniqueNameError(err, "collection", name)
	}
	for index, member := range source.members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO collection_mods(collection_id,entity_id,position,enabled) VALUES(?,?,?,?)`, newID, member.EntityID, index, boolInt(member.Enabled)); err != nil {
			return CollectionDetail{}, err
		}
	}
	for index, child := range source.children {
		if _, err := tx.ExecContext(ctx, `INSERT INTO collection_children(parent_id,child_id,position,enabled) VALUES(?,?,?,?)`, newID, child.CollectionID, index, boolInt(child.Enabled)); err != nil {
			return CollectionDetail{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CollectionDetail{}, err
	}
	return s.CollectionDetail(ctx, newID)
}

func nextCollectionNameTx(ctx context.Context, tx *sql.Tx, base string) (string, error) {
	base, err := cleanOrganizationName(base, "collection name")
	if err != nil {
		base = "Collection Copy"
	}
	candidate := base
	for index := 2; ; index++ {
		var present int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM collections WHERE name=? COLLATE NOCASE)`, candidate).Scan(&present); err != nil {
			return "", err
		}
		if present == 0 {
			return candidate, nil
		}
		suffix := fmt.Sprintf(" (%d)", index)
		prefix := base
		if len(prefix)+len(suffix) > 80 {
			prefix = strings.TrimSpace(prefix[:80-len(suffix)])
		}
		candidate = prefix + suffix
	}
}

func nextProfileNameTx(ctx context.Context, tx *sql.Tx) (string, error) {
	for index := 1; ; index++ {
		candidate := fmt.Sprintf("Profile %d", index)
		var present int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM play_profiles WHERE name=? COLLATE NOCASE)`, candidate).Scan(&present); err != nil {
			return "", err
		}
		if present == 0 {
			return candidate, nil
		}
	}
}

func (s *Store) GetCollectionDeleteImpact(ctx context.Context, collectionIDs []string) (CollectionUsage, error) {
	ids, err := normalizeOrganizationIDs(collectionIDs, "collection IDs")
	if err != nil {
		return CollectionUsage{}, err
	}
	if len(ids) == 0 {
		return CollectionUsage{}, errors.New("at least one collection ID is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CollectionUsage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	graph, err := loadCollectionGraphTx(ctx, tx)
	if err != nil {
		return CollectionUsage{}, err
	}
	for _, id := range ids {
		if _, ok := graph[id]; !ok {
			return CollectionUsage{}, sql.ErrNoRows
		}
	}
	usage, err := collectionUsageGraphTx(ctx, tx, graph, ids)
	if err != nil {
		return CollectionUsage{}, err
	}
	if err := tx.Commit(); err != nil {
		return CollectionUsage{}, err
	}
	return usage, nil
}

func (s *Store) DeleteCollections(ctx context.Context, collectionIDs []string) (OrganizationState, error) {
	ids, err := normalizeOrganizationIDs(collectionIDs, "collection IDs")
	if err != nil {
		return OrganizationState{}, err
	}
	if len(ids) == 0 {
		return OrganizationState{}, errors.New("at least one collection ID is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OrganizationState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	graph, err := loadCollectionGraphTx(ctx, tx)
	if err != nil {
		return OrganizationState{}, err
	}
	for _, id := range ids {
		if _, ok := graph[id]; !ok {
			return OrganizationState{}, sql.ErrNoRows
		}
	}
	usage, err := collectionUsageGraphTx(ctx, tx, graph, ids)
	if err != nil {
		return OrganizationState{}, err
	}
	entityIDs, err := collectionEntitiesTx(ctx, tx, ids)
	if err != nil {
		return OrganizationState{}, err
	}
	deleted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		deleted[id] = struct{}{}
	}
	ancestors := make([]string, 0, len(usage.Collections))
	for _, reference := range usage.Collections {
		if _, deleting := deleted[reference.ID]; !deleting {
			ancestors = append(ancestors, reference.ID)
		}
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = id
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM collections WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return OrganizationState{}, err
	}
	if changed, err := result.RowsAffected(); err != nil || int(changed) != len(ids) {
		if err != nil {
			return OrganizationState{}, err
		}
		return OrganizationState{}, errors.New("collection deletion changed an unexpected number of rows")
	}
	now := nowUTC()
	for _, parentID := range ancestors {
		if _, err := tx.ExecContext(ctx, `UPDATE collections SET updated_at=? WHERE id=?`, now, parentID); err != nil {
			return OrganizationState{}, err
		}
	}
	for _, profile := range usage.Profiles {
		if _, err := tx.ExecContext(ctx, `UPDATE play_profiles SET updated_at=? WHERE id=?`, now, profile.ID); err != nil {
			return OrganizationState{}, err
		}
	}
	for _, entityID := range entityIDs {
		if err := touchEntityUpdatedAtTx(ctx, tx, entityID, now); err != nil {
			return OrganizationState{}, err
		}
		if err := s.refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
			return OrganizationState{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return OrganizationState{}, err
	}
	return s.Organization(ctx)
}

func (s *Store) SetCollectionMods(ctx context.Context, collectionID string, entityIDs []string, included bool) (CollectionDetail, error) {
	collectionID = strings.TrimSpace(collectionID)
	if collectionID == "" {
		return CollectionDetail{}, errors.New("collection ID is required")
	}
	ids, err := normalizeOrganizationIDs(entityIDs, "entity IDs")
	if err != nil {
		return CollectionDetail{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CollectionDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireCollectionExistsTx(ctx, tx, collectionID); err != nil {
		return CollectionDetail{}, err
	}
	if err := requireEntitiesExistTx(ctx, tx, ids); err != nil {
		return CollectionDetail{}, err
	}
	current := map[string]struct{}{}
	rows, err := tx.QueryContext(ctx, `SELECT entity_id FROM collection_mods WHERE collection_id=?`, collectionID)
	if err != nil {
		return CollectionDetail{}, err
	}
	for rows.Next() {
		var entityID string
		if err := rows.Scan(&entityID); err != nil {
			_ = rows.Close()
			return CollectionDetail{}, err
		}
		current[entityID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return CollectionDetail{}, err
	}
	if err := rows.Close(); err != nil {
		return CollectionDetail{}, err
	}
	changedIDs := make([]string, 0, len(ids))
	changed := false
	if included {
		var nextPosition int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),-1)+1 FROM collection_mods WHERE collection_id=?`, collectionID).Scan(&nextPosition); err != nil {
			return CollectionDetail{}, err
		}
		for _, entityID := range ids {
			if _, exists := current[entityID]; exists {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO collection_mods(collection_id,entity_id,position) VALUES(?,?,?)`, collectionID, entityID, nextPosition); err != nil {
				return CollectionDetail{}, err
			}
			nextPosition++
			changedIDs = append(changedIDs, entityID)
			changed = true
		}
	} else {
		for _, entityID := range ids {
			if _, exists := current[entityID]; !exists {
				continue
			}
			result, err := tx.ExecContext(ctx, `DELETE FROM collection_mods WHERE collection_id=? AND entity_id=?`, collectionID, entityID)
			if err != nil {
				return CollectionDetail{}, err
			}
			if affected, err := result.RowsAffected(); err != nil {
				return CollectionDetail{}, err
			} else if affected > 0 {
				changedIDs = append(changedIDs, entityID)
				changed = true
			}
		}
	}
	if changed {
		now := nowUTC()
		if _, err := tx.ExecContext(ctx, `UPDATE collections SET updated_at=? WHERE id=?`, now, collectionID); err != nil {
			return CollectionDetail{}, err
		}
		for _, entityID := range changedIDs {
			if err := touchEntityUpdatedAtTx(ctx, tx, entityID, now); err != nil {
				return CollectionDetail{}, err
			}
			if err := s.refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
				return CollectionDetail{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return CollectionDetail{}, err
	}
	return s.CollectionDetail(ctx, collectionID)
}

// SetCollectionModsEnabled flips the enabled flag on existing memberships.
// Membership, position, and the collection's contents are untouched, so a
// disabled mod keeps its place and returns unchanged when re-enabled.
func (s *Store) SetCollectionModsEnabled(ctx context.Context, collectionID string, entityIDs []string, enabled bool) (CollectionDetail, error) {
	collectionID = strings.TrimSpace(collectionID)
	if collectionID == "" {
		return CollectionDetail{}, errors.New("collection ID is required")
	}
	ids, err := normalizeOrganizationIDs(entityIDs, "entity IDs")
	if err != nil {
		return CollectionDetail{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CollectionDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireCollectionExistsTx(ctx, tx, collectionID); err != nil {
		return CollectionDetail{}, err
	}
	changedIDs := make([]string, 0, len(ids))
	for _, entityID := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE collection_mods SET enabled=? WHERE collection_id=? AND entity_id=? AND enabled<>?`, boolInt(enabled), collectionID, entityID, boolInt(enabled))
		if err != nil {
			return CollectionDetail{}, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return CollectionDetail{}, err
		}
		if affected > 0 {
			changedIDs = append(changedIDs, entityID)
			continue
		}
		var present int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM collection_mods WHERE collection_id=? AND entity_id=?)`, collectionID, entityID).Scan(&present); err != nil {
			return CollectionDetail{}, err
		}
		if present == 0 {
			return CollectionDetail{}, fmt.Errorf("mod %q is not a member of this collection", entityID)
		}
	}
	if len(changedIDs) > 0 {
		now := nowUTC()
		if _, err := tx.ExecContext(ctx, `UPDATE collections SET updated_at=? WHERE id=?`, now, collectionID); err != nil {
			return CollectionDetail{}, err
		}
		for _, entityID := range changedIDs {
			if err := touchEntityUpdatedAtTx(ctx, tx, entityID, now); err != nil {
				return CollectionDetail{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return CollectionDetail{}, err
	}
	return s.CollectionDetail(ctx, collectionID)
}

// SetCollectionChildrenEnabled flips the enabled flag on parent -> child edges.
// A disabled edge is not traversed, so the child and its descendants stop
// contributing through this parent only.
func (s *Store) SetCollectionChildrenEnabled(ctx context.Context, collectionID string, childIDs []string, enabled bool) (CollectionDetail, error) {
	collectionID = strings.TrimSpace(collectionID)
	if collectionID == "" {
		return CollectionDetail{}, errors.New("collection ID is required")
	}
	ids, err := normalizeOrganizationIDs(childIDs, "child collection IDs")
	if err != nil {
		return CollectionDetail{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CollectionDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireCollectionExistsTx(ctx, tx, collectionID); err != nil {
		return CollectionDetail{}, err
	}
	changed := false
	for _, childID := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE collection_children SET enabled=? WHERE parent_id=? AND child_id=? AND enabled<>?`, boolInt(enabled), collectionID, childID, boolInt(enabled))
		if err != nil {
			return CollectionDetail{}, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return CollectionDetail{}, err
		}
		if affected > 0 {
			changed = true
			continue
		}
		var present int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM collection_children WHERE parent_id=? AND child_id=?)`, collectionID, childID).Scan(&present); err != nil {
			return CollectionDetail{}, err
		}
		if present == 0 {
			return CollectionDetail{}, fmt.Errorf("collection %q is not a child of this collection", childID)
		}
	}
	if changed {
		if _, err := tx.ExecContext(ctx, `UPDATE collections SET updated_at=? WHERE id=?`, nowUTC(), collectionID); err != nil {
			return CollectionDetail{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CollectionDetail{}, err
	}
	return s.CollectionDetail(ctx, collectionID)
}

func (s *Store) SetCollectionChildren(ctx context.Context, collectionID string, childIDs []string, included bool) (CollectionDetail, error) {
	collectionID = strings.TrimSpace(collectionID)
	if collectionID == "" {
		return CollectionDetail{}, errors.New("collection ID is required")
	}
	ids, err := normalizeOrganizationIDs(childIDs, "child collection IDs")
	if err != nil {
		return CollectionDetail{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CollectionDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	graph, err := loadCollectionGraphTx(ctx, tx)
	if err != nil {
		return CollectionDetail{}, err
	}
	if _, ok := graph[collectionID]; !ok {
		return CollectionDetail{}, sql.ErrNoRows
	}
	for _, childID := range ids {
		if _, ok := graph[childID]; !ok {
			return CollectionDetail{}, fmt.Errorf("child collection %q was not found", childID)
		}
	}
	adjacency := make(map[string][]string, len(graph))
	for id, node := range graph {
		adjacency[id] = append([]string(nil), node.childIDs...)
	}
	current := make(map[string]struct{}, len(adjacency[collectionID]))
	for _, childID := range adjacency[collectionID] {
		current[childID] = struct{}{}
	}
	changed := false
	if included {
		for _, childID := range ids {
			if _, exists := current[childID]; exists {
				continue
			}
			if wouldCollectionCycle(adjacency, collectionID, childID) {
				return CollectionDetail{}, fmt.Errorf("cannot add collection %q to %q: membership would create a cycle", childID, collectionID)
			}
			adjacency[collectionID] = append(adjacency[collectionID], childID)
			current[childID] = struct{}{}
			changed = true
		}
	} else {
		for _, childID := range ids {
			if _, exists := current[childID]; !exists {
				continue
			}
			filtered := adjacency[collectionID][:0]
			for _, existing := range adjacency[collectionID] {
				if existing != childID {
					filtered = append(filtered, existing)
				}
			}
			adjacency[collectionID] = filtered
			delete(current, childID)
			changed = true
		}
	}
	if err := validateCollectionAdjacency(adjacency); err != nil {
		return CollectionDetail{}, err
	}
	if changed {
		now := nowUTC()
		for _, childID := range ids {
			if included {
				if _, exists := current[childID]; !exists {
					continue
				}
				// INSERT below is idempotent; the relation may have existed before
				// this request and therefore should not be assigned a new position.
				var present int
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM collection_children WHERE parent_id=? AND child_id=?)`, collectionID, childID).Scan(&present); err != nil {
					return CollectionDetail{}, err
				}
				if present != 0 {
					continue
				}
				var position int
				if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),-1)+1 FROM collection_children WHERE parent_id=?`, collectionID).Scan(&position); err != nil {
					return CollectionDetail{}, err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO collection_children(parent_id,child_id,position) VALUES(?,?,?)`, collectionID, childID, position); err != nil {
					return CollectionDetail{}, err
				}
			} else {
				if _, err := tx.ExecContext(ctx, `DELETE FROM collection_children WHERE parent_id=? AND child_id=?`, collectionID, childID); err != nil {
					return CollectionDetail{}, err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE collections SET updated_at=? WHERE id=?`, now, collectionID); err != nil {
			return CollectionDetail{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CollectionDetail{}, err
	}
	return s.CollectionDetail(ctx, collectionID)
}

func (s *Store) ReorderCollectionMembers(ctx context.Context, collectionID string, entityIDs, childIDs []string) (CollectionDetail, error) {
	collectionID = strings.TrimSpace(collectionID)
	if collectionID == "" {
		return CollectionDetail{}, errors.New("collection ID is required")
	}
	entities, err := normalizeOrganizationIDs(entityIDs, "entity IDs")
	if err != nil {
		return CollectionDetail{}, err
	}
	children, err := normalizeOrganizationIDs(childIDs, "child collection IDs")
	if err != nil {
		return CollectionDetail{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CollectionDetail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireCollectionExistsTx(ctx, tx, collectionID); err != nil {
		return CollectionDetail{}, err
	}
	if err := requireEntitiesExistTx(ctx, tx, entities); err != nil {
		return CollectionDetail{}, err
	}
	graph, err := loadCollectionGraphTx(ctx, tx)
	if err != nil {
		return CollectionDetail{}, err
	}
	node := graph[collectionID]
	for _, childID := range children {
		if _, ok := graph[childID]; !ok {
			return CollectionDetail{}, fmt.Errorf("child collection %q was not found", childID)
		}
	}
	if !sameStringSet(entities, node.directEntityIDs) {
		return CollectionDetail{}, errors.New("entity order must contain every direct member exactly once")
	}
	if !sameStringSet(children, node.childIDs) {
		return CollectionDetail{}, errors.New("child order must contain every included collection exactly once")
	}
	changed := !sameStringOrder(entities, node.directEntityIDs) || !sameStringOrder(children, node.childIDs)
	if changed {
		for position, entityID := range entities {
			if _, err := tx.ExecContext(ctx, `UPDATE collection_mods SET position=? WHERE collection_id=? AND entity_id=?`, position, collectionID, entityID); err != nil {
				return CollectionDetail{}, err
			}
		}
		for position, childID := range children {
			if _, err := tx.ExecContext(ctx, `UPDATE collection_children SET position=? WHERE parent_id=? AND child_id=?`, position, collectionID, childID); err != nil {
				return CollectionDetail{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE collections SET updated_at=? WHERE id=?`, nowUTC(), collectionID); err != nil {
			return CollectionDetail{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CollectionDetail{}, err
	}
	return s.CollectionDetail(ctx, collectionID)
}

func (s *Store) CreatePlayProfile(ctx context.Context, collectionIDs, excludedCollectionIDs []string) (ModProfile, error) {
	ids, err := normalizeProfileCollectionIDs(collectionIDs)
	if err != nil {
		return ModProfile{}, err
	}
	excludedIDs, err := normalizeProfileCollectionIDs(excludedCollectionIDs)
	if err != nil {
		return ModProfile{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ModProfile{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireCollectionsOrSentinelExistTx(ctx, tx, ids); err != nil {
		return ModProfile{}, err
	}
	if err := requireCollectionsOrSentinelExistTx(ctx, tx, excludedIDs); err != nil {
		return ModProfile{}, err
	}
	name, err := nextProfileNameTx(ctx, tx)
	if err != nil {
		return ModProfile{}, err
	}
	profileID, err := modkit.NewID()
	if err != nil {
		return ModProfile{}, err
	}
	now := nowUTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO play_profiles(id,name,created_at,updated_at) VALUES(?,?,?,?)`, profileID, name, now, now); err != nil {
		return ModProfile{}, organizationUniqueNameError(err, "profile", name)
	}
	if err := insertProfileCollectionsTx(ctx, tx, profileID, ids, excludedIDs); err != nil {
		return ModProfile{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModProfile{}, err
	}
	return s.playProfile(ctx, profileID)
}

// SeedDefaultPlayProfile creates the first collection and the profile that
// holds it, in one transaction, and only while the library is unorganized. It
// returns the profile and the collection it contains.
func (s *Store) SeedDefaultPlayProfile(ctx context.Context, collectionName, description, profileName string, entityIDs []string) (ModProfile, string, error) {
	name, err := cleanOrganizationName(collectionName, "collection name")
	if err != nil {
		return ModProfile{}, "", err
	}
	profile, err := cleanOrganizationName(profileName, "profile name")
	if err != nil {
		return ModProfile{}, "", err
	}
	ids, err := normalizeOrganizationIDs(entityIDs, "entity IDs")
	if err != nil {
		return ModProfile{}, "", err
	}
	if len(ids) == 0 {
		return ModProfile{}, "", errors.New("no mods to seed")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ModProfile{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	var organized int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM collections)+(SELECT COUNT(*) FROM play_profiles)`).Scan(&organized); err != nil {
		return ModProfile{}, "", err
	}
	if organized > 0 {
		return ModProfile{}, "", errors.New("the library already has collections or profiles")
	}
	if err := requireEntitiesExistTx(ctx, tx, ids); err != nil {
		return ModProfile{}, "", err
	}
	collectionID, err := modkit.NewID()
	if err != nil {
		return ModProfile{}, "", err
	}
	profileID, err := modkit.NewID()
	if err != nil {
		return ModProfile{}, "", err
	}
	now := nowUTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO collections(id,name,description,position,cover_json,cover_asset_sha,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`,
		collectionID, name, description, 0, automaticCollectionCover, "", now, now); err != nil {
		return ModProfile{}, "", organizationUniqueNameError(err, "collection", name)
	}
	for position, entityID := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO collection_mods(collection_id,entity_id,position,enabled) VALUES(?,?,?,1)`, collectionID, entityID, position); err != nil {
			return ModProfile{}, "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO play_profiles(id,name,created_at,updated_at) VALUES(?,?,?,?)`, profileID, profile, now, now); err != nil {
		return ModProfile{}, "", organizationUniqueNameError(err, "profile", profile)
	}
	if err := insertProfileCollectionsTx(ctx, tx, profileID, []string{collectionID}, nil); err != nil {
		return ModProfile{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return ModProfile{}, "", err
	}
	seeded, err := s.playProfile(ctx, profileID)
	if err != nil {
		return ModProfile{}, "", err
	}
	return seeded, collectionID, nil
}

func (s *Store) UpdatePlayProfile(ctx context.Context, profileID string, collectionIDs, excludedCollectionIDs []string) (ModProfile, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return ModProfile{}, errors.New("profile ID is required")
	}
	ids, err := normalizeProfileCollectionIDs(collectionIDs)
	if err != nil {
		return ModProfile{}, err
	}
	excludedIDs, err := normalizeProfileCollectionIDs(excludedCollectionIDs)
	if err != nil {
		return ModProfile{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ModProfile{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireProfileExistsTx(ctx, tx, profileID); err != nil {
		return ModProfile{}, err
	}
	if err := requireCollectionsOrSentinelExistTx(ctx, tx, ids); err != nil {
		return ModProfile{}, err
	}
	if err := requireCollectionsOrSentinelExistTx(ctx, tx, excludedIDs); err != nil {
		return ModProfile{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM play_profile_collections WHERE profile_id=?`, profileID); err != nil {
		return ModProfile{}, err
	}
	if err := insertProfileCollectionsTx(ctx, tx, profileID, ids, excludedIDs); err != nil {
		return ModProfile{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE play_profiles SET updated_at=? WHERE id=?`, nowUTC(), profileID); err != nil {
		return ModProfile{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModProfile{}, err
	}
	return s.playProfile(ctx, profileID)
}

func (s *Store) RenamePlayProfile(ctx context.Context, profileID, name string) (ModProfile, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return ModProfile{}, errors.New("profile ID is required")
	}
	name, err := cleanOrganizationName(name, "profile name")
	if err != nil {
		return ModProfile{}, err
	}
	if strings.EqualFold(name, "Default") {
		return ModProfile{}, errors.New("Default is reserved for the unnamed Play selection")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ModProfile{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireProfileExistsTx(ctx, tx, profileID); err != nil {
		return ModProfile{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE play_profiles SET name=?,updated_at=? WHERE id=?`, name, nowUTC(), profileID)
	if err != nil {
		return ModProfile{}, organizationUniqueNameError(err, "profile", name)
	}
	if err := requireChanged(result, nil, "profile"); err != nil {
		return ModProfile{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModProfile{}, err
	}
	return s.playProfile(ctx, profileID)
}

func (s *Store) DeletePlayProfile(ctx context.Context, profileID string) (OrganizationState, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return OrganizationState{}, errors.New("profile ID is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OrganizationState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireProfileExistsTx(ctx, tx, profileID); err != nil {
		return OrganizationState{}, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM play_profiles WHERE id=?`, profileID)
	if err := requireChanged(result, err, "profile"); err != nil {
		return OrganizationState{}, err
	}
	if err := tx.Commit(); err != nil {
		return OrganizationState{}, err
	}
	return s.Organization(ctx)
}

func (s *Store) playProfile(ctx context.Context, profileID string) (ModProfile, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ModProfile{}, err
	}
	defer func() { _ = tx.Rollback() }()
	graph, err := loadCollectionGraphTx(ctx, tx)
	if err != nil {
		return ModProfile{}, err
	}
	var profile ModProfile
	if err := tx.QueryRowContext(ctx, `SELECT id,name,updated_at FROM play_profiles WHERE id=?`, profileID).Scan(&profile.ID, &profile.Name, &profile.UpdatedAt); err != nil {
		return ModProfile{}, err
	}
	ids, excludedIDs, err := profileCollectionIDsTx(ctx, tx, profileID)
	if err != nil {
		return ModProfile{}, err
	}
	profile.CollectionIDs = ids
	profile.ExcludedCollectionIDs = excludedIDs
	profile.CollectionCount = len(ids)
	selection, err := resolveCollectionSelectionGraphTx(ctx, tx, graph, ids, excludedIDs, true)
	if err != nil {
		return ModProfile{}, err
	}
	profile.ModCount = len(selection.Mods)
	if err := tx.Commit(); err != nil {
		return ModProfile{}, err
	}
	return profile, nil
}

func (s *Store) ResolvePlaySelection(ctx context.Context, collectionIDs, excludedCollectionIDs []string) (PlaySelection, error) {
	ids, err := normalizeProfileCollectionIDs(collectionIDs)
	if err != nil {
		return PlaySelection{}, err
	}
	excludedIDs, err := normalizeProfileCollectionIDs(excludedCollectionIDs)
	if err != nil {
		return PlaySelection{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PlaySelection{}, err
	}
	defer func() { _ = tx.Rollback() }()
	graph, err := loadCollectionGraphTx(ctx, tx)
	if err != nil {
		return PlaySelection{}, err
	}
	selection, err := resolveCollectionSelectionGraphTx(ctx, tx, graph, ids, excludedIDs, true)
	if err != nil {
		return PlaySelection{}, err
	}
	if err := tx.Commit(); err != nil {
		return PlaySelection{}, err
	}
	return selection, nil
}

func resolveCollectionSelectionGraphTx(ctx context.Context, tx *sql.Tx, graph collectionGraph, roots, excludedRoots []string, excludeArchived bool) (PlaySelection, error) {
	rootIDs, err := normalizeProfileCollectionIDs(roots)
	if err != nil {
		return PlaySelection{}, err
	}
	excludedRootIDs, err := normalizeProfileCollectionIDs(excludedRoots)
	if err != nil {
		return PlaySelection{}, err
	}
	// "all-mods" is a mod-level source, not shorthand for every collection.
	// Expanding it to collections would drop any mod that belongs to none -
	// which is exactly how Adam lost the 79 mods an in-game downloader added:
	// they were indexed and in no collection he had selected.
	rootIDs, includeAllMods := takeAllModsSentinel(rootIDs)
	excludedRootIDs, excludeAllMods := takeAllModsSentinel(excludedRootIDs)
	for _, rootID := range rootIDs {
		if _, ok := graph[rootID]; !ok {
			return PlaySelection{}, fmt.Errorf("collection %q was not found", rootID)
		}
	}
	for _, rootID := range excludedRootIDs {
		if _, ok := graph[rootID]; !ok {
			return PlaySelection{}, fmt.Errorf("excluded collection %q was not found", rootID)
		}
	}
	included := make([]string, 0)
	includedSeen := map[string]struct{}{}
	includedOrder := map[string]int{}
	provenance := map[string]*collectionModProvenance{}
	seenByRoot := map[string]map[string]struct{}{}
	visiting := map[string]struct{}{}
	var walk func(rootID, collectionID string, path []string) error
	walk = func(rootID, collectionID string, path []string) error {
		if _, active := visiting[collectionID]; active {
			cyclePath := append(append([]string(nil), path...), collectionID)
			return fmt.Errorf("collection membership cycle detected: %s", strings.Join(cyclePath, " -> "))
		}
		seen := seenByRoot[rootID]
		if seen == nil {
			seen = map[string]struct{}{}
			seenByRoot[rootID] = seen
		}
		if _, done := seen[collectionID]; done {
			return nil
		}
		node, ok := graph[collectionID]
		if !ok {
			return fmt.Errorf("collection %q was not found", collectionID)
		}
		visiting[collectionID] = struct{}{}
		if _, exists := includedSeen[collectionID]; !exists {
			includedSeen[collectionID] = struct{}{}
			includedOrder[collectionID] = len(included)
			included = append(included, collectionID)
		}
		// A user-disabled membership contributes nothing. An archive-disabled
		// membership is always collected so the metadata phase can count it
		// and, when the caller wants the full view, include it in the result
		// with ArchivedAt set. The existing excludeArchived logic in the
		// metadata phase handles whether the mod appears in the final list
		// or is only counted.
		for _, member := range node.members {
			if !member.Enabled && !member.DisabledByArchive {
				continue
			}
			entry := provenance[member.EntityID]
			if entry == nil {
				entry = &collectionModProvenance{collections: map[string]struct{}{}, roots: map[string]struct{}{}}
				provenance[member.EntityID] = entry
			}
			if _, exists := entry.collections[collectionID]; !exists {
				entry.collections[collectionID] = struct{}{}
				entry.collectionIDs = append(entry.collectionIDs, collectionID)
			}
			if _, exists := entry.roots[rootID]; !exists {
				entry.roots[rootID] = struct{}{}
				entry.rootIDs = append(entry.rootIDs, rootID)
			}
		}
		// A disabled edge is not traversed, so the child and everything below
		// it are unreachable through this parent. Reaching the same child by
		// another enabled path, or naming it in the profile, still counts.
		for _, child := range node.children {
			if !child.Enabled {
				continue
			}
			if err := walk(rootID, child.CollectionID, append(path, collectionID)); err != nil {
				return err
			}
		}
		delete(visiting, collectionID)
		seen[collectionID] = struct{}{}
		return nil
	}
	for _, rootID := range rootIDs {
		if err := walk(rootID, rootID, nil); err != nil {
			return PlaySelection{}, err
		}
	}
	if includeAllMods {
		everyMod, err := indexedModEntityIDsTx(ctx, tx)
		if err != nil {
			return PlaySelection{}, err
		}
		for _, entityID := range everyMod {
			entry := provenance[entityID]
			if entry == nil {
				entry = &collectionModProvenance{collections: map[string]struct{}{}, roots: map[string]struct{}{}}
				provenance[entityID] = entry
			}
			if _, exists := entry.roots[AllModsCollectionID]; !exists {
				entry.roots[AllModsCollectionID] = struct{}{}
				entry.rootIDs = append(entry.rootIDs, AllModsCollectionID)
			}
		}
	}

	// Build the excluded entity set by walking excluded roots with the same
	// traversal logic. Exclusion beats inclusion: any mod reachable from both
	// an included and excluded collection is out.
	excludedEntities := map[string]struct{}{}
	if excludeAllMods {
		everyMod, err := indexedModEntityIDsTx(ctx, tx)
		if err != nil {
			return PlaySelection{}, err
		}
		for _, entityID := range everyMod {
			excludedEntities[entityID] = struct{}{}
		}
	}
	if len(excludedRootIDs) > 0 {
		excludedSeenByRoot := map[string]map[string]struct{}{}
		excludedVisiting := map[string]struct{}{}
		var excludeWalk func(rootID, collectionID string, path []string) error
		excludeWalk = func(rootID, collectionID string, path []string) error {
			if _, active := excludedVisiting[collectionID]; active {
				return nil // cycles silently stop in exclusion
			}
			seen := excludedSeenByRoot[rootID]
			if seen == nil {
				seen = map[string]struct{}{}
				excludedSeenByRoot[rootID] = seen
			}
			if _, done := seen[collectionID]; done {
				return nil
			}
			node, ok := graph[collectionID]
			if !ok {
				return fmt.Errorf("excluded collection %q was not found", collectionID)
			}
			excludedVisiting[collectionID] = struct{}{}
			for _, member := range node.members {
				if !member.Enabled && !member.DisabledByArchive {
					continue
				}
				excludedEntities[member.EntityID] = struct{}{}
			}
			for _, child := range node.children {
				if !child.Enabled {
					continue
				}
				if err := excludeWalk(rootID, child.CollectionID, append(path, collectionID)); err != nil {
					return err
				}
			}
			delete(excludedVisiting, collectionID)
			seen[collectionID] = struct{}{}
			return nil
		}
		for _, rootID := range excludedRootIDs {
			if err := excludeWalk(rootID, rootID, nil); err != nil {
				return PlaySelection{}, err
			}
		}
	}

	entityIDs := make([]string, 0, len(provenance))
	for entityID := range provenance {
		entityIDs = append(entityIDs, entityID)
	}
	metadata, err := collectionMetadataTx(ctx, tx, entityIDs)
	if err != nil {
		return PlaySelection{}, err
	}
	mods := make([]CollectionMod, 0, len(provenance))
	archivedCount := 0
	excludedModCount := 0
	for entityID, entry := range provenance {
		meta, ok := metadata[entityID]
		if !ok {
			return PlaySelection{}, fmt.Errorf("collection membership references missing entity %q", entityID)
		}
		if strings.TrimSpace(meta.archivedAt) != "" {
			archivedCount++
			if excludeArchived {
				continue
			}
		}
		// Exclusion beats inclusion: a mod reachable from both is out.
		if _, excluded := excludedEntities[entityID]; excluded {
			excludedModCount++
			continue
		}
		sort.SliceStable(entry.collectionIDs, func(i, j int) bool {
			return includedOrder[entry.collectionIDs[i]] < includedOrder[entry.collectionIDs[j]]
		})
		rootOrder := map[string]int{}
		for index, rootID := range rootIDs {
			rootOrder[rootID] = index
		}
		sort.SliceStable(entry.rootIDs, func(i, j int) bool { return rootOrder[entry.rootIDs[i]] < rootOrder[entry.rootIDs[j]] })
		mod := CollectionMod{
			EntityID:      meta.entityID,
			ArtifactID:    meta.artifactID,
			DisplayName:   meta.displayName,
			Kind:          meta.kind,
			ArchivePath:   meta.archivePath,
			ArchivedAt:    meta.archivedAt,
			SHA256:        meta.sha256,
			SizeBytes:     meta.sizeBytes,
			ModifiedAt:    meta.modifiedAt,
			Available:     meta.active != 0 && strings.TrimSpace(meta.archivePath) != "",
			CollectionIDs: append([]string(nil), entry.collectionIDs...),
			RootIDs:       append([]string(nil), entry.rootIDs...),
		}
		if meta.thumbnailSHA != "" {
			mod.ThumbnailURL = "/cache/" + meta.thumbnailSHA
		}
		mods = append(mods, mod)
	}
	sort.SliceStable(mods, func(i, j int) bool {
		left, right := strings.ToLower(mods[i].DisplayName), strings.ToLower(mods[j].DisplayName)
		if left != right {
			return left < right
		}
		return mods[i].EntityID < mods[j].EntityID
	})
	warnings := make([]string, 0)
	if archivedCount > 0 {
		label := "mods were"
		if archivedCount == 1 {
			label = "mod was"
		}
		warnings = append(warnings, fmt.Sprintf("%d archived %s left out of this selection", archivedCount, label))
	}
	missingCount := 0
	for _, mod := range mods {
		if !mod.Available {
			missingCount++
			name := mod.DisplayName
			if strings.TrimSpace(name) == "" {
				name = mod.EntityID
			}
			warnings = append(warnings, fmt.Sprintf("Archive unavailable for %s", name))
		}
	}
	// The sentinel was taken out before graph validation, so it goes back into
	// what this selection reports: the caller asked for "all mods", the
	// fingerprint has to cover that, and a launch prepared from it must be
	// rejected if the request no longer says the same thing.
	selection := PlaySelection{
		CollectionIDs:         restoreAllModsSentinel(rootIDs, includeAllMods),
		ExcludedCollectionIDs: restoreAllModsSentinel(excludedRootIDs, excludeAllMods),
		IncludedCollectionIDs: included,
		Mods:                  mods,
		ModCount:              len(mods),
		ExcludedModCount:      excludedModCount,
		MissingCount:          missingCount,
		ArchivedCount:         archivedCount,
		Warnings:              warnings,
	}
	selection.Fingerprint = collectionSelectionFingerprint(selection)
	if selection.CollectionIDs == nil {
		selection.CollectionIDs = []string{}
	}
	if selection.ExcludedCollectionIDs == nil {
		selection.ExcludedCollectionIDs = []string{}
	}
	if selection.IncludedCollectionIDs == nil {
		selection.IncludedCollectionIDs = []string{}
	}
	if selection.Mods == nil {
		selection.Mods = []CollectionMod{}
	}
	if selection.Warnings == nil {
		selection.Warnings = []string{}
	}
	return selection, nil
}

// takeAllModsSentinel splits the sentinel out of a collection id list so the
// remaining ids can be validated against the graph.
func takeAllModsSentinel(ids []string) ([]string, bool) {
	found := false
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == AllModsCollectionID {
			found = true
			continue
		}
		kept = append(kept, id)
	}
	return kept, found
}

// restoreAllModsSentinel puts it back in front, where the caller wrote it.
func restoreAllModsSentinel(ids []string, present bool) []string {
	if !present {
		return append([]string(nil), ids...)
	}
	return append([]string{AllModsCollectionID}, ids...)
}

// indexedModEntityIDsTx lists every mod in the library that has an archive to
// launch. Archived mods are included here on purpose: the resolver counts them
// and drops them, which is what keeps the promise that archived mods never
// reach the game while the selection still says how many it left out. Read at
// resolve time, so a mod indexed after a selection was saved is picked up
// without the user editing anything - the case an in-game downloader creates.
func indexedModEntityIDsTx(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.id FROM entities e
		JOIN archive_links al ON al.entity_id = e.id AND al.active = 1
		GROUP BY e.id
		ORDER BY e.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entityIDs := make([]string, 0, 512)
	for rows.Next() {
		var entityID string
		if err := rows.Scan(&entityID); err != nil {
			return nil, err
		}
		entityIDs = append(entityIDs, entityID)
	}
	return entityIDs, rows.Err()
}

func collectionSelectionFingerprint(selection PlaySelection) string {
	type fingerprintMod struct {
		EntityID      string   `json:"entityId"`
		ArchivePath   string   `json:"archivePath"`
		ArtifactID    string   `json:"artifactId"`
		Available     bool     `json:"available"`
		CollectionIDs []string `json:"collectionIds"`
		RootIDs       []string `json:"rootIds"`
	}
	mods := make([]fingerprintMod, 0, len(selection.Mods))
	for _, mod := range selection.Mods {
		mods = append(mods, fingerprintMod{EntityID: mod.EntityID, ArchivePath: mod.ArchivePath, ArtifactID: mod.ArtifactID, Available: mod.Available, CollectionIDs: mod.CollectionIDs, RootIDs: mod.RootIDs})
	}
	payload, _ := json.Marshal(struct {
		CollectionIDs         []string         `json:"collectionIds"`
		ExcludedCollectionIDs []string         `json:"excludedCollectionIds"`
		ArchivedCount         int              `json:"archivedCount"`
		Mods                  []fingerprintMod `json:"mods"`
	}{selection.CollectionIDs, selection.ExcludedCollectionIDs, selection.ArchivedCount, mods})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func collectionMetadataTx(ctx context.Context, tx *sql.Tx, entityIDs []string) (map[string]collectionModMetadata, error) {
	metadata := make(map[string]collectionModMetadata, len(entityIDs))
	if len(entityIDs) == 0 {
		return metadata, nil
	}
	const chunkSize = 800
	for start := 0; start < len(entityIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(entityIDs) {
			end = len(entityIDs)
		}
		chunk := entityIDs[start:end]
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for index, id := range chunk {
			args[index] = id
		}
		rows, err := tx.QueryContext(ctx, `SELECT e.id,e.display_name,e.kind,COALESCE(l.path,''),COALESCE(a.sha256,''),COALESCE(l.size_bytes,0),COALESCE(l.modified_at,''),COALESCE(l.active,0),COALESCE(e.archived_at,''),COALESCE(ast.sha256,''),COALESCE(a.id,'')
			FROM entities e
			LEFT JOIN archive_links l ON l.id=(SELECT l2.id FROM archive_links l2 WHERE l2.entity_id=e.id ORDER BY l2.active DESC,l2.last_seen_at DESC,l2.id DESC LIMIT 1)
			LEFT JOIN artifacts a ON a.id=l.artifact_id
			LEFT JOIN entity_assets ea ON ea.entity_id=e.id AND ea.role='thumbnail' AND ea.ordinal=0
			LEFT JOIN assets ast ON ast.sha256=ea.asset_sha256
			WHERE e.id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var meta collectionModMetadata
			var kind string
			if err := rows.Scan(&meta.entityID, &meta.displayName, &kind, &meta.archivePath, &meta.sha256, &meta.sizeBytes, &meta.modifiedAt, &meta.active, &meta.archivedAt, &meta.thumbnailSHA, &meta.artifactID); err != nil {
				_ = rows.Close()
				return nil, err
			}
			meta.kind = modkit.Kind(kind)
			metadata[meta.entityID] = meta
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return metadata, nil
}

func collectionDirectEntityIDsTx(ctx context.Context, tx *sql.Tx, collectionID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT entity_id FROM collection_mods WHERE collection_id=? ORDER BY position,entity_id`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func collectionEntitiesTx(ctx context.Context, tx *sql.Tx, collectionIDs []string) ([]string, error) {
	if len(collectionIDs) == 0 {
		return []string{}, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(collectionIDs)), ",")
	args := make([]any, len(collectionIDs))
	for index, id := range collectionIDs {
		args[index] = id
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT entity_id FROM collection_mods WHERE collection_id IN (`+placeholders+`) ORDER BY entity_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func collectionUsageGraphTx(ctx context.Context, tx *sql.Tx, graph collectionGraph, targetIDs []string) (CollectionUsage, error) {
	targets := map[string]struct{}{}
	for _, id := range targetIDs {
		if _, ok := graph[id]; !ok {
			return CollectionUsage{}, sql.ErrNoRows
		}
		targets[id] = struct{}{}
	}
	ancestors := map[string]struct{}{}
	queue := append([]string(nil), targetIDs...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, parentID := range graph[id].parentIDs {
			if _, exists := ancestors[parentID]; exists {
				continue
			}
			ancestors[parentID] = struct{}{}
			queue = append(queue, parentID)
		}
	}
	collections := make([]CollectionReference, 0, len(ancestors))
	for id := range ancestors {
		collections = append(collections, CollectionReference{ID: id, Name: graph[id].collection.Name})
	}
	sort.SliceStable(collections, func(i, j int) bool {
		left, right := strings.ToLower(collections[i].Name), strings.ToLower(collections[j].Name)
		if left != right {
			return left < right
		}
		return collections[i].ID < collections[j].ID
	})

	rows, err := tx.QueryContext(ctx, `SELECT id,name FROM play_profiles ORDER BY name COLLATE NOCASE,id`)
	if err != nil {
		return CollectionUsage{}, err
	}
	profiles := make([]CollectionReference, 0)
	for rows.Next() {
		var profileID, profileName string
		if err := rows.Scan(&profileID, &profileName); err != nil {
			_ = rows.Close()
			return CollectionUsage{}, err
		}
		profileIDs, profileExcluded, err := profileCollectionIDsTx(ctx, tx, profileID)
		if err != nil {
			_ = rows.Close()
			return CollectionUsage{}, err
		}
		used := false
		for _, rootID := range append(profileIDs, profileExcluded...) {
			if _, direct := targets[rootID]; direct {
				used = true
				break
			}
			if _, ancestor := ancestors[rootID]; ancestor {
				used = true
				break
			}
			if collectionReachesAny(graph, rootID, targets, map[string]struct{}{}) {
				used = true
				break
			}
		}
		if used {
			profiles = append(profiles, CollectionReference{ID: profileID, Name: profileName})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return CollectionUsage{}, err
	}
	if err := rows.Close(); err != nil {
		return CollectionUsage{}, err
	}
	return CollectionUsage{Collections: collections, Profiles: profiles}, nil
}

func collectionReachesAny(graph collectionGraph, rootID string, targets, seen map[string]struct{}) bool {
	if _, ok := targets[rootID]; ok {
		return true
	}
	if _, ok := seen[rootID]; ok {
		return false
	}
	seen[rootID] = struct{}{}
	for _, childID := range graph[rootID].childIDs {
		if collectionReachesAny(graph, childID, targets, seen) {
			return true
		}
	}
	return false
}

func profileCollectionIDsTx(ctx context.Context, tx *sql.Tx, profileID string) ([]string, []string, error) {
	// Read sentinel flags from the profile row.
	var includesAllMods, excludesAllMods int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(includes_all_mods,0), COALESCE(excludes_all_mods,0) FROM play_profiles WHERE id=?`, profileID).Scan(&includesAllMods, &excludesAllMods); err != nil {
		return nil, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT collection_id, excluded FROM play_profile_collections WHERE profile_id=? ORDER BY excluded, position, collection_id`, profileID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	ids := []string{}
	excludedIDs := []string{}
	if includesAllMods != 0 {
		ids = append(ids, AllModsCollectionID)
	}
	if excludesAllMods != 0 {
		excludedIDs = append(excludedIDs, AllModsCollectionID)
	}
	for rows.Next() {
		var id string
		var excluded int
		if err := rows.Scan(&id, &excluded); err != nil {
			return nil, nil, err
		}
		if excluded != 0 {
			excludedIDs = append(excludedIDs, id)
		} else {
			ids = append(ids, id)
		}
	}
	return ids, excludedIDs, rows.Err()
}

func insertProfileCollectionsTx(ctx context.Context, tx *sql.Tx, profileID string, collectionIDs, excludedCollectionIDs []string) error {
	includesAllMods := 0
	excludesAllMods := 0
	for position, collectionID := range collectionIDs {
		if collectionID == AllModsCollectionID {
			includesAllMods = 1
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO play_profile_collections(profile_id,collection_id,position,excluded) VALUES(?,?,?,0)`, profileID, collectionID, position); err != nil {
			return err
		}
	}
	for position, collectionID := range excludedCollectionIDs {
		if collectionID == AllModsCollectionID {
			excludesAllMods = 1
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO play_profile_collections(profile_id,collection_id,position,excluded) VALUES(?,?,?,1)`, profileID, collectionID, position); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE play_profiles SET includes_all_mods=?, excludes_all_mods=? WHERE id=?`, includesAllMods, excludesAllMods, profileID); err != nil {
		return err
	}
	return nil
}

func requireCollectionExistsTx(ctx context.Context, tx *sql.Tx, collectionID string) error {
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM collections WHERE id=?)`, collectionID).Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		return fmt.Errorf("collection %q was not found", collectionID)
	}
	return nil
}

func requireCollectionsExistTx(ctx context.Context, tx *sql.Tx, collectionIDs []string) error {
	for _, id := range collectionIDs {
		if err := requireCollectionExistsTx(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// requireCollectionsOrSentinelExistTx validates that every ID is either the
// all-mods sentinel or an existing collection.
func requireCollectionsOrSentinelExistTx(ctx context.Context, tx *sql.Tx, collectionIDs []string) error {
	for _, id := range collectionIDs {
		if id == AllModsCollectionID {
			continue
		}
		if err := requireCollectionExistsTx(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

func requireProfileExistsTx(ctx context.Context, tx *sql.Tx, profileID string) error {
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM play_profiles WHERE id=?)`, profileID).Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		return fmt.Errorf("profile %q was not found", profileID)
	}
	return nil
}

func requireEntitiesExistTx(ctx context.Context, tx *sql.Tx, entityIDs []string) error {
	for _, id := range entityIDs {
		var present int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM entities WHERE id=?)`, id).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			return fmt.Errorf("entity %q was not found", id)
		}
	}
	return nil
}

func wouldCollectionCycle(adjacency map[string][]string, parentID, childID string) bool {
	if parentID == childID {
		return true
	}
	return reachesCollection(adjacency, childID, parentID, map[string]struct{}{})
}

func reachesCollection(adjacency map[string][]string, fromID, targetID string, seen map[string]struct{}) bool {
	if fromID == targetID {
		return true
	}
	if _, ok := seen[fromID]; ok {
		return false
	}
	seen[fromID] = struct{}{}
	for _, childID := range adjacency[fromID] {
		if reachesCollection(adjacency, childID, targetID, seen) {
			return true
		}
	}
	return false
}

func validateCollectionAdjacency(adjacency map[string][]string) error {
	state := map[string]uint8{}
	var visit func(string, []string) error
	visit = func(id string, path []string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("collection membership cycle detected: %s -> %s", strings.Join(path, " -> "), id)
		case 2:
			return nil
		}
		state[id] = 1
		for _, childID := range adjacency[id] {
			if err := visit(childID, append(path, id)); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	ids := make([]string, 0, len(adjacency))
	for id := range adjacency {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id, nil); err != nil {
			return err
		}
	}
	return nil
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]struct{}, len(left))
	for _, value := range left {
		if _, ok := seen[value]; ok {
			return false
		}
		seen[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	return true
}

func sameStringOrder(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
