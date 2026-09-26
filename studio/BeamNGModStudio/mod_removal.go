package main

// Removing mods from the library. Two separate operations, deliberately never
// merged: forgetting drops the index entry and leaves every file alone, while
// deleting also sends the archive to the Recycle Bin. A single "delete" that
// silently did both would make an irreversible act look like a list edit.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

type ModRemovalTarget struct {
	EntityID    string `json:"entityId"`
	DisplayName string `json:"displayName"`
	ArchivePath string `json:"archivePath"`
	SizeBytes   int64  `json:"sizeBytes"`
	Missing     bool   `json:"missing"`
}

// ModRemovalImpact is what the confirmation needs to state plainly before the
// user agrees to anything.
type ModRemovalImpact struct {
	Mods         []ModRemovalTarget `json:"mods"`
	Collections  []string           `json:"collections"`
	Workspaces   []string           `json:"workspaces"`
	ArchiveCount int                `json:"archiveCount"`
	ArchiveBytes int64              `json:"archiveBytes"`
}

type ModRemovalResult struct {
	Forgotten int      `json:"forgotten"`
	Recycled  int      `json:"recycled"`
	Failures  []string `json:"failures"`
}

func (service *AppService) PlanModRemoval(entityIDs []string) (ModRemovalImpact, error) {
	return service.store.ModRemovalImpact(context.Background(), entityIDs)
}

// ForgetEntities stays as the index-drop half of deletion; there is no
// user-facing forget, because it left the archive on disk and the next scan
// re-indexed the mod, undoing the action.

// DeleteModArchives recycles each archive and then forgets the mod. A mod whose
// archive could not be recycled stays in the library: the index must never
// claim a file is gone while it is still on disk.
func (service *AppService) DeleteModArchives(entityIDs []string) (ModRemovalResult, error) {
	ctx := context.Background()
	impact, err := service.store.ModRemovalImpact(ctx, entityIDs)
	if err != nil {
		return ModRemovalResult{}, err
	}
	if len(impact.Workspaces) > 0 {
		return ModRemovalResult{}, fmt.Errorf("open in ModMaker: %s. Delete the project first", strings.Join(impact.Workspaces, ", "))
	}
	result := ModRemovalResult{}
	removable := make([]string, 0, len(impact.Mods))
	for _, mod := range impact.Mods {
		if mod.Missing || strings.TrimSpace(mod.ArchivePath) == "" {
			// Nothing to recycle, so forgetting is the whole operation.
			removable = append(removable, mod.EntityID)
			continue
		}
		if err := recycleFile(mod.ArchivePath); err != nil {
			result.Failures = append(result.Failures, fmt.Sprintf("%s: %v", mod.DisplayName, err))
			continue
		}
		result.Recycled++
		removable = append(removable, mod.EntityID)
	}
	if len(removable) > 0 {
		forgotten, err := service.store.ForgetEntities(ctx, removable)
		if err != nil {
			return result, err
		}
		result.Forgotten = forgotten
	}
	return result, nil
}

// DeleteModArchivesAndWorkspaces deletes both the library archives and any
// ModMaker projects associated with the given entities. The caller must have
// shown the user which projects will be destroyed and received explicit
// confirmation; this is the acknowledged path where the user agreed to lose
// both the archive and the project.
func (service *AppService) DeleteModArchivesAndWorkspaces(entityIDs []string) (ModRemovalResult, error) {
	ctx := context.Background()
	// Delete every workspace belonging to these entities first, so the
	// archive-deletion path no longer sees them and does not refuse.
	for _, entityID := range entityIDs {
		workspaceIDs, err := service.store.scanStrings(ctx,
			`SELECT id FROM workspaces WHERE entity_id=?`, entityID)
		if err != nil {
			return ModRemovalResult{}, err
		}
		for _, wsID := range workspaceIDs {
			if err := service.DeleteWorkspace(wsID); err != nil {
				return ModRemovalResult{}, fmt.Errorf(
					"deleting ModMaker project for %s: %w", entityID, err)
			}
		}
	}
	return service.DeleteModArchives(entityIDs)
}

func (s *Store) ModRemovalImpact(ctx context.Context, entityIDs []string) (ModRemovalImpact, error) {
	ids, err := normalizeOrganizationIDs(entityIDs, "entity IDs")
	if err != nil {
		return ModRemovalImpact{}, err
	}
	impact := ModRemovalImpact{Mods: []ModRemovalTarget{}, Collections: []string{}, Workspaces: []string{}}
	collections := map[string]struct{}{}
	for _, entityID := range ids {
		target := ModRemovalTarget{EntityID: entityID}
		err := s.db.QueryRowContext(ctx, `SELECT e.display_name, COALESCE(al.path,''), COALESCE(al.size_bytes,0)
			FROM entities e
			LEFT JOIN archive_links al ON al.entity_id=e.id AND al.active=1
			WHERE e.id=?
			ORDER BY al.size_bytes DESC`, entityID).Scan(&target.DisplayName, &target.ArchivePath, &target.SizeBytes)
		if errors.Is(err, sql.ErrNoRows) {
			return ModRemovalImpact{}, fmt.Errorf("mod %q is not in the library", entityID)
		}
		if err != nil {
			return ModRemovalImpact{}, err
		}
		if target.ArchivePath != "" {
			if info, statErr := os.Stat(target.ArchivePath); statErr != nil || !info.Mode().IsRegular() {
				target.Missing = true
			} else {
				impact.ArchiveCount++
				impact.ArchiveBytes += info.Size()
			}
		} else {
			target.Missing = true
		}
		impact.Mods = append(impact.Mods, target)

		names, err := s.scanStrings(ctx, `SELECT c.name FROM collection_mods cm JOIN collections c ON c.id=cm.collection_id WHERE cm.entity_id=?`, entityID)
		if err != nil {
			return ModRemovalImpact{}, err
		}
		for _, name := range names {
			collections[name] = struct{}{}
		}
		projects, err := s.scanStrings(ctx, `SELECT COALESCE(NULLIF(e.display_name,''), w.id) FROM workspaces w JOIN entities e ON e.id=w.entity_id WHERE w.entity_id=?`, entityID)
		if err != nil {
			return ModRemovalImpact{}, err
		}
		impact.Workspaces = append(impact.Workspaces, projects...)
	}
	for name := range collections {
		impact.Collections = append(impact.Collections, name)
	}
	byName := func(left, right string) int { return strings.Compare(strings.ToLower(left), strings.ToLower(right)) }
	slices.SortFunc(impact.Collections, byName)
	slices.SortFunc(impact.Workspaces, byName)
	return impact, nil
}

// ForgetEntities drops index rows only. Cascades remove memberships, tags,
// assets, and archive links; the cached preview images stay, because the cache
// is content-addressed and may be shared with another mod.
func (s *Store) ForgetEntities(ctx context.Context, entityIDs []string) (int, error) {
	ids, err := normalizeOrganizationIDs(entityIDs, "entity IDs")
	if err != nil {
		return 0, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	forgotten, err := forgetEntitiesTx(ctx, tx, ids)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return forgotten, nil
}

// forgetEntitiesTx removes index records inside the caller's transaction.
// Callers must serialize writers and validate any replacement references first.
func forgetEntitiesTx(ctx context.Context, tx *sql.Tx, entityIDs []string) (int, error) {
	forgotten := 0
	for _, entityID := range entityIDs {
		var workspaceCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE entity_id=?`, entityID).Scan(&workspaceCount); err != nil {
			return 0, err
		}
		if workspaceCount > 0 {
			// A workspace is unsaved user work living outside the archive.
			return 0, fmt.Errorf("mod %q has a ModMaker project; delete the project first", entityID)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM library_search_fts WHERE entity_id=?`, entityID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, entityPreviewSelectionKey(entityID)); err != nil {
			return 0, err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM entities WHERE id=?`, entityID)
		if err != nil {
			return 0, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		forgotten += int(affected)
	}
	// Archives that no longer belong to any mod would otherwise be re-linked by
	// the next scan and reappear as ghosts.
	if _, err := tx.ExecContext(ctx, `DELETE FROM artifacts WHERE id NOT IN (SELECT artifact_id FROM archive_links)`); err != nil {
		return 0, err
	}
	return forgotten, nil
}

func (s *Store) scanStrings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		if strings.TrimSpace(value) != "" {
			values = append(values, value)
		}
	}
	return values, rows.Err()
}
