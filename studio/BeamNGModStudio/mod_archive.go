package main

import (
	"context"
	"database/sql"
	"fmt"
)

// ArchiveResult reports state transitions completed by an archive operation.
// Archiving and restoring are intentionally metadata-only: archive links,
// files, memberships (as rows), tags, workspaces, and scan rows are left
// untouched. Archiving does, however, disable enabled memberships and record
// the flag so restoring can reverse exactly those disablements.
type ArchiveResult struct {
	Archived             int      `json:"archived"`
	Restored             int      `json:"restored"`
	DisabledMemberships  int      `json:"disabledMemberships"`
	ReenabledMemberships int      `json:"reenabledMemberships"`
	Failures             []string `json:"failures"`
}

func (service *AppService) ArchiveMods(entityIDs []string) (ArchiveResult, error) {
	return service.store.ArchiveMods(context.Background(), entityIDs)
}

func (service *AppService) RestoreMods(entityIDs []string) (ArchiveResult, error) {
	return service.store.RestoreMods(context.Background(), entityIDs)
}

func (s *Store) ArchiveMods(ctx context.Context, entityIDs []string) (ArchiveResult, error) {
	return s.setArchiveState(ctx, entityIDs, true)
}

func (s *Store) RestoreMods(ctx context.Context, entityIDs []string) (ArchiveResult, error) {
	return s.setArchiveState(ctx, entityIDs, false)
}

func (s *Store) setArchiveState(ctx context.Context, entityIDs []string, archived bool) (ArchiveResult, error) {
	ids, err := normalizeOrganizationIDs(entityIDs, "entity IDs")
	if err != nil {
		return ArchiveResult{}, err
	}
	result := ArchiveResult{Failures: []string{}}
	if len(ids) == 0 {
		return result, nil
	}
	if s == nil || s.db == nil {
		return ArchiveResult{}, fmt.Errorf("SQLite store is not initialized")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArchiveResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, entityID := range ids {
		var archivedAt string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(archived_at,'') FROM entities WHERE id=?`, entityID).Scan(&archivedAt); err != nil {
			if err == sql.ErrNoRows {
				return ArchiveResult{}, fmt.Errorf("mod %q is not in the library", entityID)
			}
			return ArchiveResult{}, err
		}
		isArchived := archivedAt != ""
		if isArchived == archived {
			continue
		}
		now := nowUTC()
		newArchivedAt := ""
		eventType := "mod_restored"
		if archived {
			newArchivedAt = now
			eventType = "mod_archived"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE entities SET archived_at=?, updated_at=? WHERE id=?`, newArchivedAt, now, entityID); err != nil {
			return ArchiveResult{}, err
		}
		eventData := map[string]any{"archivedAt": newArchivedAt}
		if archivedAt != "" {
			eventData["previousArchivedAt"] = archivedAt
		}
		if err := appendEventTx(ctx, tx, entityID, eventType, eventData); err != nil {
			return ArchiveResult{}, err
		}
		if archived {
			// Disable all enabled memberships and mark them as archive-driven
			// so restoring can reverse exactly these disablements.
			res, err := tx.ExecContext(ctx, `UPDATE collection_mods SET enabled=0, disabled_by_archive=1 WHERE entity_id=? AND enabled=1`, entityID)
			if err != nil {
				return ArchiveResult{}, err
			}
			n, _ := res.RowsAffected()
			result.DisabledMemberships += int(n)
			result.Archived++
		} else {
			// Re-enable only memberships that were disabled by archiving.
			// Memberships the user disabled by hand (disabled_by_archive=0)
			// remain disabled — that is the whole reason the flag exists.
			res, err := tx.ExecContext(ctx, `UPDATE collection_mods SET enabled=1, disabled_by_archive=0 WHERE entity_id=? AND disabled_by_archive=1`, entityID)
			if err != nil {
				return ArchiveResult{}, err
			}
			n, _ := res.RowsAffected()
			result.ReenabledMemberships += int(n)
			result.Restored++
		}
	}
	if err := tx.Commit(); err != nil {
		return ArchiveResult{}, err
	}
	return result, nil
}
