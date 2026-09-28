package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ArchiveFileTarget struct {
	LinkID      string `json:"linkId"`
	EntityID    string `json:"entityId"`
	DisplayName string `json:"displayName"`
	ArchivePath string `json:"archivePath"`
	SizeBytes   int64  `json:"sizeBytes"`
	Missing     bool   `json:"missing"`
}

type ArchiveFileRemovalImpact struct {
	Files        []ArchiveFileTarget `json:"files"`
	Refusals     []string            `json:"refusals"`
	ArchiveCount int                 `json:"archiveCount"`
	ArchiveBytes int64               `json:"archiveBytes"`
}

type archiveFileRemovalRecord struct {
	target      ArchiveFileTarget
	active      int
	activeCount int
}

func (service *AppService) PlanArchiveFileRemoval(linkIDs []string) (ArchiveFileRemovalImpact, error) {
	return service.store.archiveFileRemovalImpact(context.Background(), linkIDs)
}

func (service *AppService) DeleteArchiveFiles(linkIDs []string) (ModRemovalResult, error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	if err := service.requireGameStopped(); err != nil { return ModRemovalResult{}, err }
	return service.store.deleteArchiveFiles(context.Background(), linkIDs, service.retireArchiveReferences)
}

func (s *Store) archiveFileRemovalImpact(ctx context.Context, linkIDs []string) (ArchiveFileRemovalImpact, error) {
	records, err := s.archiveFileRemovalRecords(ctx, linkIDs)
	if err != nil {
		return ArchiveFileRemovalImpact{}, err
	}
	impact := ArchiveFileRemovalImpact{
		Files:    make([]ArchiveFileTarget, 0, len(records)),
		Refusals: []string{},
	}
	for _, record := range records {
		target := record.target
		info, statErr := os.Stat(target.ArchivePath)
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) { return impact, statErr }
		target.Missing = errors.Is(statErr, os.ErrNotExist)
		if !target.Missing && !info.Mode().IsRegular() { return impact, fmt.Errorf("%s is not a regular archive", target.ArchivePath) }
		other, err := s.otherArchiveCopyAvailable(ctx, target)
		if err != nil { return impact, err }
		refused := record.activeCount <= 1 || (!target.Missing && !other)
		if !target.Missing && !refused {
			impact.ArchiveCount++
			impact.ArchiveBytes += info.Size()
		}
		impact.Files = append(impact.Files, target)
		if refused { impact.Refusals = append(impact.Refusals, archiveFileLastLinkRefusal(target)) }
	}
	return impact, nil
}

func (s *Store) archiveFileRemovalRecords(ctx context.Context, linkIDs []string) ([]archiveFileRemovalRecord, error) {
	ids, err := normalizeOrganizationIDs(linkIDs, "archive link IDs")
	if err != nil {
		return nil, err
	}
	records := make([]archiveFileRemovalRecord, 0, len(ids))
	for _, linkID := range ids {
		record, err := s.archiveFileRemovalRecord(ctx, linkID)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Store) archiveFileRemovalRecord(ctx context.Context, linkID string) (archiveFileRemovalRecord, error) {
	var record archiveFileRemovalRecord
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT l.entity_id,e.display_name,l.path,COALESCE(l.size_bytes,0),l.active,
		(SELECT COUNT(*) FROM archive_links remaining WHERE remaining.entity_id=l.entity_id AND remaining.active=1)
		FROM archive_links l
		JOIN entities e ON e.id=l.entity_id
		WHERE l.id=?`, linkID).Scan(
		&record.target.EntityID, &record.target.DisplayName, &record.target.ArchivePath,
		&record.target.SizeBytes, &active, &record.activeCount)
	if errors.Is(err, sql.ErrNoRows) {
		return archiveFileRemovalRecord{}, fmt.Errorf("archive link %q is not in the library", linkID)
	}
	if err != nil {
		return archiveFileRemovalRecord{}, err
	}
	if active == 0 {
		return archiveFileRemovalRecord{}, fmt.Errorf("archive link %q is not active", linkID)
	}
	record.target.LinkID = linkID
	record.active = active
	return record, nil
}
func archiveFileInfo(path string) (os.FileInfo, bool) {
	if strings.TrimSpace(path) == "" {
		return nil, false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	return info, true
}

func archiveFileLastLinkRefusal(target ArchiveFileTarget) string {
	name := strings.TrimSpace(target.DisplayName)
	if name == "" {
		name = target.EntityID
	}
	return fmt.Sprintf("%s: this is the only remaining archive; use the mod's own delete instead", name)
}

func archiveFileRemovalFailure(target ArchiveFileTarget, err error) string {
	name := strings.TrimSpace(target.DisplayName)
	if name == "" {
		name = target.EntityID
	}
	if strings.TrimSpace(target.ArchivePath) == "" {
		return fmt.Sprintf("%s: %v", name, err)
	}
	return fmt.Sprintf("%s (%s): %v", name, target.ArchivePath, err)
}

func (s *Store) deleteArchiveFiles(ctx context.Context, linkIDs []string, retireReferences func(context.Context, []string) error) (ModRemovalResult, error) {
	ids, err := normalizeOrganizationIDs(linkIDs, "archive link IDs")
	if err != nil {
		return ModRemovalResult{}, err
	}
	result := ModRemovalResult{Failures: []string{}}
	if len(ids) == 0 {
		return result, nil
	}

	records, err := s.archiveFileRemovalRecords(ctx, ids)
	if err != nil {
		return result, err
	}
	for _, planned := range records {
		record, err := s.archiveFileRemovalRecord(ctx, planned.target.LinkID)
		if err != nil {
			return result, err
		}
		if record.activeCount <= 1 {
			result.Failures = append(result.Failures, archiveFileLastLinkRefusal(record.target))
			continue
		}
		info, statErr := os.Stat(record.target.ArchivePath)
		exists := statErr == nil
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			result.Failures = append(result.Failures, archiveFileRemovalFailure(record.target, statErr)); continue
		}
		if exists && !info.Mode().IsRegular() {
			result.Failures = append(result.Failures, fmt.Sprintf("%s is not a regular archive", record.target.ArchivePath)); continue
		}
		other, err := s.otherArchiveCopyAvailable(ctx, record.target)
		if err != nil { return result, err }
		if exists && !other {
			result.Failures = append(result.Failures, archiveFileLastLinkRefusal(record.target)); continue
		}
		if retireReferences != nil {
			if err := retireReferences(ctx, []string{record.target.EntityID}); err != nil {
				result.Failures = append(result.Failures, archiveFileRemovalFailure(record.target, err))
				continue
			}
		}
		if exists {
			if err := recycleFile(record.target.ArchivePath); err != nil {
				result.Failures = append(result.Failures, archiveFileRemovalFailure(record.target, err))
				continue
			}
		}
		if err := s.deleteArchiveFileLinkTx(ctx, record.target); err != nil {
			return result, err
		}
		if exists {
			result.Recycled++
		}
	}
	return result, nil
}

func (s *Store) deleteArchiveFileLinkTx(ctx context.Context, target ArchiveFileTarget) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var entityID string
	if err := tx.QueryRowContext(ctx, `SELECT entity_id FROM archive_links WHERE id=? AND active=1`, target.LinkID).Scan(&entityID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("archive link %q is no longer active", target.LinkID)
		}
		return err
	}
	if entityID != target.EntityID {
		return fmt.Errorf("archive link %q belongs to a different mod", target.LinkID)
	}
	var activeCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM archive_links WHERE entity_id=? AND active=1`, entityID).Scan(&activeCount); err != nil {
		return err
	}
	if activeCount <= 1 {
		return errors.New(archiveFileLastLinkRefusal(target))
	}
	deleted, err := tx.ExecContext(ctx, `DELETE FROM archive_links WHERE id=? AND active=1`, target.LinkID)
	if err != nil {
		return err
	}
	affected, err := deleted.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("archive link %q was not deleted", target.LinkID)
	}
	if err := refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) otherArchiveCopyAvailable(ctx context.Context, target ArchiveFileTarget) (bool,error) {
	paths, err := s.scanStrings(ctx, `SELECT path FROM archive_links WHERE entity_id=? AND active=1 AND id<>?`, target.EntityID, target.LinkID)
	if err != nil { return false,err }
	targetPath, _ := filepath.EvalSymlinks(target.ArchivePath)
	targetIdentity, _ := inspectArchiveFile(target.ArchivePath)
	for _, path := range paths {
		otherPath, err := filepath.EvalSymlinks(path)
		if err != nil || samePath(otherPath,targetPath) { continue }
		identity, err := inspectArchiveFile(path)
		if err != nil || !identity.Regular { continue }
		if identity.IdentityKnown && targetIdentity.IdentityKnown &&
			identity.VolumeID==targetIdentity.VolumeID && identity.FileID==targetIdentity.FileID && identity.Links<2 { continue }
		return true,nil
	}
	return false,nil
}
