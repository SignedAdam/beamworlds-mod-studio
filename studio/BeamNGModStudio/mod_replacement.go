package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

type ModReplacementImpact struct {
	Keeper       ModRemovalTarget   `json:"keeper"`
	Mods         []ModRemovalTarget `json:"mods"`
	Collections  []string           `json:"collections"`
	Groups       []string           `json:"groups"`
	Tags         []string           `json:"tags"`
	Workspaces   []string           `json:"workspaces"`
	ArchiveCount int                `json:"archiveCount"`
	ArchiveBytes int64              `json:"archiveBytes"`
	Refusals     []string           `json:"refusals"`
	Fingerprint  string             `json:"fingerprint"`
}

type ModReplacementResult struct {
	Replaced      int      `json:"replaced"`
	Forgotten     int      `json:"forgotten"`
	Recycled      int      `json:"recycled"`
	RecycledBytes int64    `json:"recycledBytes"`
	Failures      []string `json:"failures"`
}

func (service *AppService) PlanModReplacement(keeperID string, entityIDs []string) (ModReplacementImpact, error) {
	return service.store.modReplacementImpact(context.Background(), keeperID, entityIDs)
}

// ReplaceModArchives retires the old versions after handing their collection,
// group, and tag usages to the keeper.
func (service *AppService) ReplaceModArchives(keeperID string, entityIDs []string, fingerprint string) (ModReplacementResult, error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	return service.store.retireModVersions(context.Background(), keeperID, entityIDs, fingerprint, true)
}

// RemoveModVersions retires the old versions and drops their collection,
// group, and tag usages; the keeper's own usages are left exactly as they are.
// It shares the replacement review, so the same fingerprint guards both.
func (service *AppService) RemoveModVersions(keeperID string, entityIDs []string, fingerprint string) (ModReplacementResult, error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	return service.store.retireModVersions(context.Background(), keeperID, entityIDs, fingerprint, false)
}

type replacementMembership struct {
	CollectionID      string
	Name              string
	Position          int
	Enabled           int
	DisabledByArchive int
}

type replacementTag struct {
	ID      string
	Name    string
	Grouped bool
}

type replacementArchive struct {
	ID         string
	ArtifactID string
	Path       string
	Active     int
	SHA256     string
	Size       int64
	Modified   int64
	Missing    bool
}

type replacementEntity struct {
	ID          string
	Name        string
	ArchivedAt  string
	Memberships []replacementMembership
	Tags        []replacementTag
	Workspaces  []string
	Archives    []replacementArchive
}

type replacementPlan struct {
	Impact   ModReplacementImpact
	Entities []replacementEntity // keeper first, sources in ID order
}

func (s *Store) modReplacementImpact(ctx context.Context, keeperID string, entityIDs []string) (ModReplacementImpact, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ModReplacementImpact{}, err
	}
	defer func() { _ = tx.Rollback() }()
	plan, err := loadReplacementPlanTx(ctx, tx, keeperID, entityIDs)
	if err != nil {
		return ModReplacementImpact{}, err
	}
	return plan.Impact, tx.Commit()
}

// A single database snapshot binds the displayed impact to its fingerprint.
// File identity includes every indexed path, including inactive links, so a
// scan cannot resurrect an overlooked copy after the old entity is forgotten.
func loadReplacementPlanTx(ctx context.Context, tx *sql.Tx, keeperID string, entityIDs []string) (replacementPlan, error) {
	keeperID = strings.TrimSpace(keeperID)
	ids, err := normalizeOrganizationIDs(entityIDs, "replacement targets")
	if err != nil {
		return replacementPlan{}, err
	}
	if keeperID == "" || len(ids) == 0 {
		return replacementPlan{}, errors.New("choose a keeper and at least one version to replace")
	}
	if slices.Contains(ids, keeperID) {
		return replacementPlan{}, errors.New("the keeper cannot also be a version to replace")
	}
	slices.Sort(ids)
	plan := replacementPlan{Impact: ModReplacementImpact{
		Mods: []ModRemovalTarget{}, Collections: []string{}, Groups: []string{}, Tags: []string{}, Workspaces: []string{}, Refusals: []string{},
	}}
	allIDs := append([]string{keeperID}, ids...)
	collections, groups, tags := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for index, id := range allIDs {
		entity, err := loadReplacementEntityTx(ctx, tx, id)
		if err != nil {
			return replacementPlan{}, err
		}
		plan.Entities = append(plan.Entities, entity)
		target := ModRemovalTarget{EntityID: id, DisplayName: entity.Name, Missing: true}
		for _, archive := range entity.Archives {
			if target.ArchivePath == "" || target.Missing && !archive.Missing && archive.Active == 1 {
				target.ArchivePath, target.SizeBytes = archive.Path, archive.Size
				target.Missing = archive.Missing || archive.Active != 1
			}
			if index > 0 && !archive.Missing {
				plan.Impact.ArchiveCount++
				plan.Impact.ArchiveBytes += archive.Size
			}
		}
		if entity.ArchivedAt != "" {
			plan.Impact.Refusals = append(plan.Impact.Refusals, fmt.Sprintf("%s is archived; restore it first", entity.Name))
		}
		if index == 0 {
			plan.Impact.Keeper = target
			if target.Missing {
				plan.Impact.Refusals = append(plan.Impact.Refusals, fmt.Sprintf("the file of %s is missing, so it can't be the version kept; keep another version or rescan", entity.Name))
			}
			continue
		}
		plan.Impact.Mods = append(plan.Impact.Mods, target)
		for _, member := range entity.Memberships {
			collections[member.Name] = true
		}
		for _, tag := range entity.Tags {
			if tag.Grouped {
				groups[tag.Name] = true
			} else {
				tags[tag.Name] = true
			}
		}
		for _, workspace := range entity.Workspaces {
			plan.Impact.Workspaces = append(plan.Impact.Workspaces, entity.Name+" ("+workspace+")")
		}
		if len(entity.Workspaces) > 0 {
			plan.Impact.Refusals = append(plan.Impact.Refusals, fmt.Sprintf("%s has a ModMaker project. Export or preserve that work, then delete the project before removing this version", entity.Name))
		}
		for _, archive := range entity.Archives {
			for _, keptArchive := range plan.Entities[0].Archives {
				if archive.Path != "" && samePath(archive.Path, keptArchive.Path) {
					plan.Impact.Refusals = append(plan.Impact.Refusals, "an old version shares the kept version's file path; rescan first")
				}
			}
		}
	}
	for name := range collections {
		plan.Impact.Collections = append(plan.Impact.Collections, name)
	}
	for name := range groups {
		plan.Impact.Groups = append(plan.Impact.Groups, name)
	}
	for name := range tags {
		plan.Impact.Tags = append(plan.Impact.Tags, name)
	}
	for _, names := range [][]string{plan.Impact.Collections, plan.Impact.Groups, plan.Impact.Tags, plan.Impact.Workspaces, plan.Impact.Refusals} {
		slices.SortFunc(names, compareLibrarySortText)
	}
	payload, err := json.Marshal(plan.Entities)
	if err != nil {
		return replacementPlan{}, err
	}
	digest := sha256.Sum256(payload)
	plan.Impact.Fingerprint = hex.EncodeToString(digest[:])
	return plan, nil
}

func loadReplacementEntityTx(ctx context.Context, tx *sql.Tx, id string) (replacementEntity, error) {
	entity := replacementEntity{ID: id}
	if err := tx.QueryRowContext(ctx, `SELECT display_name,COALESCE(archived_at,'') FROM entities WHERE id=?`, id).Scan(&entity.Name, &entity.ArchivedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entity, fmt.Errorf("mod %q is no longer in the library; review the remaining versions", id)
		}
		return entity, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT cm.collection_id,c.name,cm.position,cm.enabled,cm.disabled_by_archive FROM collection_mods cm JOIN collections c ON c.id=cm.collection_id WHERE cm.entity_id=? ORDER BY cm.collection_id`, id)
	if err != nil {
		return entity, err
	}
	for rows.Next() {
		var member replacementMembership
		if err := rows.Scan(&member.CollectionID, &member.Name, &member.Position, &member.Enabled, &member.DisabledByArchive); err != nil {
			_ = rows.Close()
			return entity, err
		}
		entity.Memberships = append(entity.Memberships, member)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return entity, err
	}
	if err := rows.Close(); err != nil {
		return entity, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT t.id,t.name,t.grouped FROM mod_tag_entities mt JOIN mod_tags t ON t.id=mt.tag_id WHERE mt.entity_id=? ORDER BY t.id`, id)
	if err != nil {
		return entity, err
	}
	for rows.Next() {
		var tag replacementTag
		if err := rows.Scan(&tag.ID, &tag.Name, &tag.Grouped); err != nil {
			_ = rows.Close()
			return entity, err
		}
		entity.Tags = append(entity.Tags, tag)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return entity, err
	}
	if err := rows.Close(); err != nil {
		return entity, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id FROM workspaces WHERE entity_id=? ORDER BY id`, id)
	if err != nil {
		return entity, err
	}
	for rows.Next() {
		var workspace string
		if err := rows.Scan(&workspace); err != nil {
			_ = rows.Close()
			return entity, err
		}
		entity.Workspaces = append(entity.Workspaces, workspace)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return entity, err
	}
	if err := rows.Close(); err != nil {
		return entity, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT l.id,l.artifact_id,l.path,l.active,COALESCE(a.sha256,'') FROM archive_links l LEFT JOIN artifacts a ON a.id=l.artifact_id WHERE l.entity_id=? ORDER BY l.active DESC,l.last_seen_at DESC,l.id DESC`, id)
	if err != nil {
		return entity, err
	}
	for rows.Next() {
		var archive replacementArchive
		if err := rows.Scan(&archive.ID, &archive.ArtifactID, &archive.Path, &archive.Active, &archive.SHA256); err != nil {
			_ = rows.Close()
			return entity, err
		}
		if err := archive.readFileIdentity(); err != nil {
			_ = rows.Close()
			return entity, err
		}
		entity.Archives = append(entity.Archives, archive)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return entity, err
	}
	return entity, rows.Close()
}

func (archive *replacementArchive) readFileIdentity() error {
	archive.Size, archive.Modified, archive.Missing = 0, 0, false
	if strings.TrimSpace(archive.Path) == "" {
		archive.Missing = true
		return nil
	}
	info, err := os.Stat(archive.Path)
	if errors.Is(err, os.ErrNotExist) {
		archive.Missing = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect archive %s: %w", archive.Path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("archive %s is not a regular file; nothing was removed", archive.Path)
	}
	archive.Size, archive.Modified = info.Size(), info.ModTime().UnixNano()
	return nil
}

// retireModVersions moves (transferUsages) or drops the old versions' usages in
// one transaction, then recycles and forgets each old version.
func (s *Store) retireModVersions(ctx context.Context, keeperID string, entityIDs []string, fingerprint string, transferUsages bool) (ModReplacementResult, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result := ModReplacementResult{Failures: []string{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	// Reserve the SQLite writer before reading the reviewed snapshot. The
	// no-op update also prevents writers outside this Store from racing it.
	if _, err := tx.ExecContext(ctx, `UPDATE entities SET updated_at=updated_at WHERE id=?`, strings.TrimSpace(keeperID)); err != nil {
		return result, err
	}
	plan, err := loadReplacementPlanTx(ctx, tx, keeperID, entityIDs)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(fingerprint) == "" || fingerprint != plan.Impact.Fingerprint {
		return result, errors.New("the library changed since these versions were checked; review them again")
	}
	if len(plan.Impact.Refusals) > 0 {
		return result, errors.New(strings.Join(plan.Impact.Refusals, "; "))
	}
	// On Windows the read handle also prevents the retained file being deleted
	// while its replacements are recycled. Never retire files for an unreadable keeper.
	keptFile, err := os.Open(plan.Impact.Keeper.ArchivePath)
	if err != nil {
		return result, fmt.Errorf("open archive to keep: %w", err)
	}
	defer keptFile.Close()
	if transferUsages {
		err = transferReplacementReferencesTx(ctx, tx, plan)
	} else {
		err = dropRetiredReferencesTx(ctx, tx, plan)
	}
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	result.Replaced = len(plan.Entities) - 1
	// Filesystem recycling cannot share a transaction with SQLite. The durable
	// usage change above makes even a partial cleanup safe and reviewable on retry.
	for _, source := range plan.Entities[1:] {
		if err := s.retireReplacementSource(ctx, source, &result); err != nil {
			result.Failures = append(result.Failures, fmt.Sprintf("%s: %v", source.Name, err))
		}
	}
	return result, nil
}

func transferReplacementReferencesTx(ctx context.Context, tx *sql.Tx, plan replacementPlan) error {
	keeper := plan.Entities[0]
	merged := map[string]replacementMembership{}
	original := map[string]bool{}
	affected := map[string]bool{}
	for _, member := range keeper.Memberships {
		merged[member.CollectionID] = member
		original[member.CollectionID] = true
	}
	for _, source := range plan.Entities[1:] {
		for _, member := range source.Memberships {
			affected[member.CollectionID] = true
			if previous, exists := merged[member.CollectionID]; exists {
				if !original[member.CollectionID] {
					previous.Position = min(previous.Position, member.Position)
				}
				previous.Enabled = max(previous.Enabled, member.Enabled)
				previous.DisabledByArchive = max(previous.DisabledByArchive, member.DisabledByArchive)
				if previous.Enabled != 0 {
					previous.DisabledByArchive = 0
				}
				merged[member.CollectionID] = previous
			} else {
				merged[member.CollectionID] = member
			}
		}
	}
	now := nowUTC()
	for collectionID := range affected {
		member := merged[collectionID]
		if _, err := tx.ExecContext(ctx, `INSERT INTO collection_mods(collection_id,entity_id,position,enabled,disabled_by_archive) VALUES(?,?,?,?,?) ON CONFLICT(collection_id,entity_id) DO UPDATE SET position=excluded.position,enabled=excluded.enabled,disabled_by_archive=excluded.disabled_by_archive`, collectionID, keeper.ID, member.Position, member.Enabled, member.DisabledByArchive); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE collections SET updated_at=? WHERE id=?`, now, collectionID); err != nil {
			return err
		}
	}
	sourceIDs := make([]string, 0, len(plan.Entities)-1)
	for _, source := range plan.Entities[1:] {
		sourceIDs = append(sourceIDs, source.ID)
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO mod_tag_entities(tag_id,entity_id,created_at) SELECT tag_id,?,created_at FROM mod_tag_entities WHERE entity_id=?`, keeper.ID, source.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM mod_tag_entities WHERE entity_id=?`, source.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM collection_mods WHERE entity_id=?`, source.ID); err != nil {
			return err
		}
	}
	for _, entity := range plan.Entities {
		if err := touchEntityUpdatedAtTx(ctx, tx, entity.ID, now); err != nil {
			return err
		}
		if err := refreshLibrarySearchEntryTx(ctx, tx, entity.ID); err != nil {
			return err
		}
	}
	if err := appendEventTx(ctx, tx, keeper.ID, "mod_versions_replaced", map[string]any{"sourceEntityIds": sourceIDs, "collections": plan.Impact.Collections, "groups": plan.Impact.Groups, "tags": plan.Impact.Tags}); err != nil {
		return err
	}
	return markLibraryIndexFreshTx(ctx, tx)
}

// dropRetiredReferencesTx removes the old versions from every collection,
// group, and tag without giving those places to the keeper.
func dropRetiredReferencesTx(ctx context.Context, tx *sql.Tx, plan replacementPlan) error {
	now := nowUTC()
	affected := map[string]bool{}
	sourceIDs := make([]string, 0, len(plan.Entities)-1)
	for _, source := range plan.Entities[1:] {
		sourceIDs = append(sourceIDs, source.ID)
		for _, member := range source.Memberships {
			affected[member.CollectionID] = true
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM mod_tag_entities WHERE entity_id=?`, source.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM collection_mods WHERE entity_id=?`, source.ID); err != nil {
			return err
		}
	}
	for collectionID := range affected {
		if _, err := tx.ExecContext(ctx, `UPDATE collections SET updated_at=? WHERE id=?`, now, collectionID); err != nil {
			return err
		}
	}
	for _, entity := range plan.Entities {
		if err := touchEntityUpdatedAtTx(ctx, tx, entity.ID, now); err != nil {
			return err
		}
		if err := refreshLibrarySearchEntryTx(ctx, tx, entity.ID); err != nil {
			return err
		}
	}
	if err := appendEventTx(ctx, tx, plan.Entities[0].ID, "mod_versions_removed", map[string]any{"sourceEntityIds": sourceIDs, "collections": plan.Impact.Collections, "groups": plan.Impact.Groups, "tags": plan.Impact.Tags}); err != nil {
		return err
	}
	return markLibraryIndexFreshTx(ctx, tx)
}

func (s *Store) retireReplacementSource(ctx context.Context, source replacementEntity, result *ModReplacementResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Keep new usages/links from appearing between this check and forgetting.
	if _, err := tx.ExecContext(ctx, `UPDATE entities SET updated_at=updated_at WHERE id=?`, source.ID); err != nil {
		return err
	}
	current, err := loadReplacementEntityTx(ctx, tx, source.ID)
	if err != nil {
		return err
	}
	if len(current.Memberships) > 0 || len(current.Tags) > 0 || len(current.Workspaces) > 0 || current.ArchivedAt != source.ArchivedAt {
		return errors.New("it gained a new usage, ModMaker project, or archive state in the meantime; kept it for another review")
	}
	if !slices.Equal(current.Archives, source.Archives) {
		return errors.New("its files changed in the meantime; review it again before deleting")
	}
	var failures []string
	for _, archive := range current.Archives {
		if archive.Missing {
			continue
		}
		checked := archive
		if err := checked.readFileIdentity(); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if checked != archive {
			failures = append(failures, fmt.Sprintf("%s changed before recycling", archive.Path))
			continue
		}
		if err := recycleFile(archive.Path); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", archive.Path, err))
			continue
		}
		result.Recycled++
		result.RecycledBytes += archive.Size
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	forgotten, err := forgetEntitiesTx(ctx, tx, []string{source.ID})
	if err != nil {
		return err
	}
	if err := markLibraryIndexFreshTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	result.Forgotten += forgotten
	return nil
}
