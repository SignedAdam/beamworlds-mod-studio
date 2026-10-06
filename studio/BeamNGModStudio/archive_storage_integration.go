package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

// backupBeforeDeploymentMigration uses SQLite's snapshot mechanism rather than
// copying a live database file without its WAL. It runs only before the v7
// ownership cutover and never overwrites an earlier recovery backup.
func (s *Store) backupBeforeDeploymentMigration(ctx context.Context) (string, error) {
	var metadataExists int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='schema_meta')`).Scan(&metadataExists); err != nil {
		return "", err
	}
	if metadataExists == 0 { return "", nil }
	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_meta`).Scan(&version); err != nil { return "", err }
	if version >= 7 || version == 0 { return "", nil }
	id, err := modkit.NewID()
	if err != nil { return "", err }
	extension := filepath.Ext(s.dbPath)
	backup := strings.TrimSuffix(s.dbPath, extension)+".pre-v7-"+id+".sqlite"
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, backup); err != nil {
		return "", fmt.Errorf("back up database before archive deployment migration: %w", err)
	}
	return backup, nil
}

// retireArchiveReferences is called only after a reviewed removal has been
// validated. Callers hold modImportMu, but not store.writeMu: the ownership
// helpers perform their own short database transactions around filesystem work.
func (service *AppService) retireArchiveReferences(ctx context.Context, entityIDs []string) error {
	if err := service.requireGameStopped(); err != nil { return err }
	if err := service.recoverArchiveDeployment(ctx); err != nil { return err }
	if err := service.adoptExistingManagedEntries(ctx); err != nil { return err }
	// An archive operation invalidates the previously applied selection. Keep
	// the ownership ledger, but do not report the old marker as current.
	if err := os.Remove(playRuntimeMarkerPath(service.config)); err != nil && !errors.Is(err, os.ErrNotExist) { return err }
	if err := service.retireOwnedArchiveEntries(ctx, entityIDs); err != nil { return err }
	if err := service.retireLegacyArchiveReferences(ctx, entityIDs); err != nil { return err }
	return service.store.AppendEvent(ctx, "", "play_deployment_invalidated", map[string]any{"entityIds":entityIDs})
}

// retireCollectionMirrors removes only ledger-owned entries that no longer
// belong to a live collection's resolved set. It does not materialize additions:
// that remains the explicit, copy-cost-reviewed Open collection folder action.
// Unknown files and sole surviving copies stay in place for storage review.
func (service *AppService) retireCollectionMirrors(ctx context.Context) error {
	if err := service.requireGameStopped(); err != nil { return err }
	entries, err := service.store.listOwnedArchiveEntries(ctx)
	if err != nil { return err }
	byCollection := make(map[string][]OwnedArchiveEntry)
	for _, entry := range entries {
		if entry.Purpose == "collection" { byCollection[entry.OwnerID] = append(byCollection[entry.OwnerID], entry) }
	}
	var failures []string
	for collectionID, owned := range byCollection {
		selection, resolveErr := service.store.ResolvePlaySelection(ctx, []string{collectionID}, nil)
		wanted := map[string]CollectionMod{}
		if resolveErr == nil {
			for _, mod := range selection.Mods { wanted[mod.EntityID] = mod }
		} else {
			var exists int
			if err := service.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collections WHERE id=?`, collectionID).Scan(&exists); err != nil { return err }
			if exists != 0 { failures = append(failures, resolveErr.Error()); continue }
		}
		for _, entry := range owned {
			if mod, keep := wanted[entry.EntityID]; keep && samePath(mod.ArchivePath, entry.SourcePath) {
				source, sourceErr := inspectArchiveFile(mod.ArchivePath)
				target, targetErr := inspectArchiveFile(filepath.Join(entry.TargetRoot, entry.RelativePath))
				contentMatches := mod.SHA256 != "" && strings.EqualFold(mod.SHA256, entry.SHA256)
				if sourceErr == nil && targetErr == nil && sameArchiveObject(target, entry.TargetIdentity) &&
					(contentMatches || sameArchiveObject(source, entry.SourceIdentity)) {
					if entry.State == archiveStatePendingRetire {
						entry.State = archiveStateActive
						if err := service.store.saveOwnedArchiveEntries(ctx, []OwnedArchiveEntry{entry}); err != nil { failures = append(failures, err.Error()) }
					}
					continue
				}
			}
			if err := service.retireCollectionMirrorEntry(ctx, entry); err != nil { failures = append(failures, err.Error()) }
		}
	}
	if len(failures) > 0 { return errors.New(strings.Join(failures, "; ")) }
	return nil
}

func (service *AppService) reportCollectionMirrorRetirement(ctx context.Context) {
	if err := service.retireCollectionMirrors(ctx); err != nil {
		_ = service.store.AppendEvent(ctx, "", "archive_cleanup_pending", map[string]any{"error":err.Error(),"purpose":"collection"})
		service.emitStorageProgress(StorageProgress{Phase:"cleanup-pending",Done:true,Error:err.Error()})
	}
}

// sourceHasProtectedWorkspaceTx is shared by ordinary retirement to avoid
// deleting archive bytes before noticing editable projects still depend on them.
func sourceHasProtectedWorkspaceTx(ctx context.Context, tx *sql.Tx, entityID string) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE entity_id=?`, entityID).Scan(&count); err != nil { return err }
	if count > 0 { return fmt.Errorf("mod %q has a ModMaker project; preserve and delete the project before retiring the archive",entityID) }
	return nil
}

func sameArchiveObject(left, right ArchiveFileIdentity) bool {
	return left.IdentityKnown && right.IdentityKnown &&
		left.VolumeID == right.VolumeID && left.FileID == right.FileID &&
		left.SizeBytes == right.SizeBytes && left.ModifiedNs == right.ModifiedNs
}

func (service *AppService) retireCollectionMirrorEntry(ctx context.Context, entry OwnedArchiveEntry) error {
	root := filepath.Join(service.config.ExportDir, collectionFolderDirectory)
	target := filepath.Join(entry.TargetRoot, entry.RelativePath)
	if entry.Purpose != "collection" || entry.RelativePath == "" || filepath.IsAbs(entry.RelativePath) ||
		!pathWithin(entry.TargetRoot, root) || !pathWithin(target, entry.TargetRoot) {
		return fmt.Errorf("refuse cleanup outside a registered collection folder: %s", target)
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) { return service.store.deleteOwnedArchiveEntry(ctx, entry.ID) }
	if err != nil { return err }
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("preserved changed or unowned collection entry: %s", target)
	}
	resolvedBase, err := filepath.EvalSymlinks(service.config.ExportDir)
	if err != nil { return err }
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil { return err }
	relativeParent, err := filepath.Rel(service.config.ExportDir, filepath.Dir(target))
	if err != nil || !samePath(resolvedParent, filepath.Join(resolvedBase, relativeParent)) {
		return fmt.Errorf("preserved collection entry beneath a changed reparse path: %s", target)
	}
	current, err := inspectArchiveFile(target)
	if err != nil { return err }
	entry.State = archiveStatePendingRetire
	if err := service.store.saveOwnedArchiveEntries(ctx, []OwnedArchiveEntry{entry}); err != nil { return err }
	if !sameArchiveObject(current, entry.TargetIdentity) {
		return fmt.Errorf("collection entry changed; review storage before removing %s", target)
	}
	var sourcePath string
	err = service.store.db.QueryRowContext(ctx, `SELECT path FROM archive_links WHERE entity_id=? AND active=1 ORDER BY last_seen_at DESC,id DESC LIMIT 1`, entry.EntityID).Scan(&sourcePath)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) { return fmt.Errorf("preserved possible sole archive copy: %s", target) }
		return err
	}
	source, err := inspectArchiveFile(sourcePath)
	if err != nil || !source.Regular {
		return fmt.Errorf("preserved %s because its canonical archive is unavailable", target)
	}
	if samePath(sourcePath, target) || (source.VolumeID == current.VolumeID && source.FileID == current.FileID && source.Links < 2) {
		return fmt.Errorf("preserved possible sole archive copy: %s", target)
	}
	if !sameArchiveObject(source, entry.SourceIdentity) && !sameArchiveObject(source, current) {
		if entry.SHA256 == "" { return fmt.Errorf("preserved %s because its original canonical archive cannot be verified", target) }
		checksum, err := hashFileSHA256(ctx, sourcePath)
		if err != nil || !strings.EqualFold(checksum, entry.SHA256) {
			return fmt.Errorf("preserved %s because the available canonical archive has different or unreadable contents", target)
		}
		after, err := inspectArchiveFile(sourcePath)
		if err != nil || !sameArchiveObject(source, after) { return fmt.Errorf("canonical archive changed during cleanup verification: %s", sourcePath) }
	}
	after, err := inspectArchiveFile(target)
	if err != nil || !sameArchiveObject(current, after) { return fmt.Errorf("collection entry changed during cleanup verification: %s", target) }
	if err := os.Remove(target); err != nil { return err }
	return service.store.deleteOwnedArchiveEntry(ctx, entry.ID)
}
