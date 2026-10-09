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
	"path/filepath"
	"strings"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

// Lock contracts for archive deployment helpers:
//
//   - SetArchiveDeploymentMode: acquires service.archivePolicyMu (write)
//   - GetArchiveDeploymentState: acquires service.archivePolicyMu (read)
//   - buildArchiveDeploymentPlan: acquires service.archivePolicyMu (read) for mode
//   - applyArchiveDeployment: caller holds modImportMu; does NOT acquire archivePolicyMu
//   - retireOwnedArchiveEntries: caller holds modImportMu; does NOT acquire store.writeMu
//   - recoverArchiveDeployment: called at startup, no lock required

// stagingExtension replaces .zip on files in the staging directory so that
// BeamNG never loads a partially staged archive. The extension is replaced
// with .zip only when the file is renamed into the final managed root.
const stagingExtension = ".staging"

// deploymentWorkDir returns the .beamworlds-deployment directory as a sibling
// of the resolved destinationRoot's parent. This ensures it is on the SAME
// volume as the destination (required for atomic renames) and outside the
// mods directory itself (BeamNG won't scan .beamworlds-* prefixed siblings).
// Parent excludes .beamworlds-* ancestors in the scanner.
func deploymentWorkDir(destinationRoot string) string {
	// Resolve symlinks/junctions to find the real parent volume.
	resolved, err := filepath.EvalSymlinks(destinationRoot)
	if err != nil {
		resolved = destinationRoot
	}
	parent := filepath.Dir(filepath.Clean(resolved))
	return filepath.Join(parent, ".beamworlds-deployment")
}

// ---------------------------------------------------------------------------
// Schema
// ---------------------------------------------------------------------------

// ensureArchiveDeploymentSchemaTx creates the owned_archive_entries and
// archive_deployment_journal tables idempotently within the caller's
// transaction. Main wires this into the versioned migration gate.
func ensureArchiveDeploymentSchemaTx(ctx context.Context, tx *sql.Tx) error {
	const ownedEntriesDDL = `CREATE TABLE IF NOT EXISTS owned_archive_entries (
		id TEXT PRIMARY KEY,
		purpose TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL DEFAULT '',
		entity_id TEXT NOT NULL DEFAULT '',
		artifact_id TEXT NOT NULL DEFAULT '',
		sha256 TEXT NOT NULL DEFAULT '',
		source_path TEXT NOT NULL DEFAULT '',
		target_root TEXT NOT NULL DEFAULT '',
		relative_path TEXT NOT NULL DEFAULT '',
		method TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT 'active',
		source_identity TEXT NOT NULL DEFAULT '{}',
		target_identity TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT ''
	)`
	const journalDDL = `CREATE TABLE IF NOT EXISTS archive_deployment_journal (
		id TEXT PRIMARY KEY,
		operation_id TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT 'planned',
		purpose TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL DEFAULT '',
		destination_root TEXT NOT NULL DEFAULT '',
		staging_dir TEXT NOT NULL DEFAULT '',
		previous_dir TEXT NOT NULL DEFAULT '',
		plan_json TEXT NOT NULL DEFAULT '{}',
		native_backup BLOB,
		native_existed INTEGER NOT NULL DEFAULT 0,
		marker_backup BLOB,
		marker_existed INTEGER NOT NULL DEFAULT 0,
		prior_owned TEXT NOT NULL DEFAULT '[]',
		prepared_entries TEXT NOT NULL DEFAULT '[]',
		applied_entries TEXT NOT NULL DEFAULT '[]',
		moved_files TEXT NOT NULL DEFAULT '[]',
		started_at TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT '',
		completed_at TEXT NOT NULL DEFAULT '',
		error_message TEXT NOT NULL DEFAULT ''
	)`
	if _, err := tx.ExecContext(ctx, ownedEntriesDDL); err != nil {
		return fmt.Errorf("create owned_archive_entries: %w", err)
	}
	if _, err := tx.ExecContext(ctx, journalDDL); err != nil {
		return fmt.Errorf("create archive_deployment_journal: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS owned_archive_target ON owned_archive_entries(target_root COLLATE NOCASE, relative_path COLLATE NOCASE)`); err != nil {
		return fmt.Errorf("index owned archive targets: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS archive_deployment_prepared (
		journal_id TEXT NOT NULL REFERENCES archive_deployment_journal(id) ON DELETE CASCADE,
		entry_id TEXT NOT NULL, entry_json TEXT NOT NULL,
		PRIMARY KEY(journal_id,entry_id))`); err != nil { return err }
	return nil
}

// ---------------------------------------------------------------------------
// Store helpers for owned archive entries
// ---------------------------------------------------------------------------

func (s *Store) listOwnedArchiveEntries(ctx context.Context) ([]OwnedArchiveEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, purpose, owner_id, entity_id, artifact_id, sha256,
		source_path, target_root, relative_path, method, state,
		source_identity, target_identity
		FROM owned_archive_entries ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []OwnedArchiveEntry
	for rows.Next() {
		var e OwnedArchiveEntry
		var srcJSON, tgtJSON string
		if err := rows.Scan(
			&e.ID, &e.Purpose, &e.OwnerID, &e.EntityID, &e.ArtifactID, &e.SHA256,
			&e.SourcePath, &e.TargetRoot, &e.RelativePath, &e.Method, &e.State,
			&srcJSON, &tgtJSON,
		); err != nil {
			return nil, err
		}
		if err:=json.Unmarshal([]byte(srcJSON),&e.SourceIdentity);err!=nil{return nil,fmt.Errorf("decode owned source identity %s: %w",e.ID,err)}
		if err:=json.Unmarshal([]byte(tgtJSON),&e.TargetIdentity);err!=nil{return nil,fmt.Errorf("decode owned target identity %s: %w",e.ID,err)}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *Store) saveOwnedArchiveEntries(ctx context.Context, entries []OwnedArchiveEntry) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Upsert by ID only. A different entry claiming the same target must fail
	// the unique index rather than silently deleting the other ownership row.
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO owned_archive_entries
		(id, purpose, owner_id, entity_id, artifact_id, sha256,
		 source_path, target_root, relative_path, method, state,
		 source_identity, target_identity, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET purpose=excluded.purpose, owner_id=excluded.owner_id,
		 entity_id=excluded.entity_id, artifact_id=excluded.artifact_id, sha256=excluded.sha256,
		 source_path=excluded.source_path, target_root=excluded.target_root,
		 relative_path=excluded.relative_path, method=excluded.method, state=excluded.state,
		 source_identity=excluded.source_identity, target_identity=excluded.target_identity,
		 updated_at=excluded.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := nowUTC()
	for _, e := range entries {
		srcJSON, _ := json.Marshal(e.SourceIdentity)
		tgtJSON, _ := json.Marshal(e.TargetIdentity)
		if _, err := stmt.ExecContext(ctx,
			e.ID, e.Purpose, e.OwnerID, e.EntityID, e.ArtifactID, e.SHA256,
			e.SourcePath, e.TargetRoot, e.RelativePath, e.Method, e.State,
			string(srcJSON), string(tgtJSON), now, now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) deleteOwnedArchiveEntry(ctx context.Context, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM owned_archive_entries WHERE id=?`, id)
	return err
}

// ---------------------------------------------------------------------------
// Deployment journal store helpers
// ---------------------------------------------------------------------------
func (s *Store) writeDeploymentJournal(ctx context.Context, entry deploymentJournalEntry) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	planJSON, _ := json.Marshal(entry.Plan)
	movedJSON, _ := json.Marshal(entry.MovedFiles)
	priorJSON, _ := json.Marshal(entry.PriorOwned)
	preparedJSON, _ := json.Marshal(entry.PreparedEntries)
	appliedJSON, _ := json.Marshal(entry.AppliedEntries)
	_, err := s.db.ExecContext(ctx, `INSERT INTO archive_deployment_journal
		(id, operation_id, state, purpose, owner_id, destination_root,
		 staging_dir, previous_dir, plan_json,
		 native_backup, native_existed, marker_backup, marker_existed,
		 prior_owned, prepared_entries, applied_entries, moved_files,
		 started_at, updated_at, completed_at, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET state=excluded.state,staging_dir=excluded.staging_dir,
		previous_dir=excluded.previous_dir,plan_json=excluded.plan_json,
		native_backup=excluded.native_backup,native_existed=excluded.native_existed,
		marker_backup=excluded.marker_backup,marker_existed=excluded.marker_existed,
		prior_owned=excluded.prior_owned,prepared_entries=excluded.prepared_entries,
		applied_entries=excluded.applied_entries,moved_files=excluded.moved_files,
		updated_at=excluded.updated_at,completed_at=excluded.completed_at,error_message=excluded.error_message`,
		entry.ID, entry.OperationID, entry.State, entry.Purpose, entry.OwnerID,
		entry.DestinationRoot, entry.StagingDir, entry.PreviousDir,
		string(planJSON),
		entry.NativeBackup, entry.NativeExisted, entry.MarkerBackup, entry.MarkerExisted,
		string(priorJSON), string(preparedJSON), string(appliedJSON), string(movedJSON),
		entry.StartedAt, nowUTC(),
		entry.CompletedAt, entry.ErrorMessage)
	return err
}

func (s *Store) updateDeploymentJournalState(ctx context.Context, id, state string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE archive_deployment_journal SET state=?, updated_at=? WHERE id=?`,
		state, nowUTC(), id)
	return err
}

func (s *Store) updateDeploymentJournalPaths(ctx context.Context, id, stagingDir, previousDir string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE archive_deployment_journal
		SET staging_dir=?, previous_dir=?, updated_at=? WHERE id=?`,
		stagingDir, previousDir, nowUTC(), id)
	return err
}

// updateDeploymentJournalNativeBackup captures db.json and marker bytes and
// their existence state before filesystem mutation. SQL NULL native_backup
// with native_existed=0 means the file did not exist; non-NULL with
// native_existed=1 means it existed (even if empty).
func (s *Store) updateDeploymentJournalNativeBackup(ctx context.Context, id string, nativeBackup, markerBackup []byte, nativeExisted, markerExisted bool) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE archive_deployment_journal
		SET native_backup=?, native_existed=?, marker_backup=?, marker_existed=?, updated_at=? WHERE id=?`,
		nativeBackup, nativeExisted, markerBackup, markerExisted, nowUTC(), id)
	return err
}

// updateDeploymentJournalMoves records per-file moved reuse steps for crash undo.
func (s *Store) updateDeploymentJournalMoves(ctx context.Context, id string, moves []movedFileRecord) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	movedJSON, _ := json.Marshal(moves)
	_, err := s.db.ExecContext(ctx, `UPDATE archive_deployment_journal
		SET moved_files=?, updated_at=? WHERE id=?`,
		string(movedJSON), nowUTC(), id)
	return err
}

// commitDeploymentApplied atomically saves new ownership entries, marks the
// journal as applied, and snapshots the applied entries — all in one
// transaction. If any step fails, the entire transaction rolls back and the
// journal stays in activating state for recovery.
func (s *Store) commitDeploymentApplied(ctx context.Context, journalID string, newEntries []OwnedArchiveEntry) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var priorJSON string
	if err := tx.QueryRowContext(ctx, `SELECT prior_owned FROM archive_deployment_journal WHERE id=? AND state=?`, journalID, journalStateActivating).Scan(&priorJSON); err != nil {
		return fmt.Errorf("load activating journal: %w", err)
	}
	var prior []OwnedArchiveEntry
	if err := json.Unmarshal([]byte(priorJSON), &prior); err != nil { return err }
	for _, entry := range prior {
		if _, err := tx.ExecContext(ctx, `UPDATE owned_archive_entries SET state=?,updated_at=? WHERE id=?`, archiveStatePendingRetire, nowUTC(), entry.ID); err != nil { return err }
	}

	// Save new ownership entries.
	if len(newEntries) > 0 {
		stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO owned_archive_entries
			(id, purpose, owner_id, entity_id, artifact_id, sha256,
			 source_path, target_root, relative_path, method, state,
			 source_identity, target_identity, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		now := nowUTC()
		for _, e := range newEntries {
			srcJSON, _ := json.Marshal(e.SourceIdentity)
			tgtJSON, _ := json.Marshal(e.TargetIdentity)
			if _, err := stmt.ExecContext(ctx,
				e.ID, e.Purpose, e.OwnerID, e.EntityID, e.ArtifactID, e.SHA256,
				e.SourcePath, e.TargetRoot, e.RelativePath, e.Method, e.State,
				string(srcJSON), string(tgtJSON), now, now,
			); err != nil {
				return err
			}
		}
	}

	// Snapshot applied entries and mark journal as applied — same transaction.
	appliedJSON, _ := json.Marshal(newEntries)
	if _, err := tx.ExecContext(ctx, `UPDATE archive_deployment_journal
		SET state=?, applied_entries=?, updated_at=? WHERE id=?`,
		journalStateApplied, string(appliedJSON), nowUTC(), journalID); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) completeDeploymentJournal(ctx context.Context, id, state, errorMessage string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE archive_deployment_journal
		SET state=?, updated_at=?, completed_at=?, error_message=? WHERE id=?`,
		state, nowUTC(), nowUTC(), errorMessage, id)
	return err
}

// compactCompletedJournals strips large payloads (plan, native/marker backups)
// from completed journal entries so repeated launches don't grow the DB.
func (s *Store) compactCompletedJournals(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM archive_deployment_prepared WHERE journal_id IN (SELECT id FROM archive_deployment_journal WHERE state IN ('done','failed'))`); err != nil { return err }
	_, err := s.db.ExecContext(ctx, `UPDATE archive_deployment_journal
		SET plan_json='{}', native_backup=NULL, marker_backup=NULL,
		    prior_owned='[]', prepared_entries='[]', applied_entries='[]', moved_files='[]'
		WHERE state IN ('done', 'failed') AND length(plan_json) > 2`)
	return err
}

func (s *Store) listPendingDeploymentJournals(ctx context.Context) ([]deploymentJournalEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, operation_id, state, purpose, owner_id, destination_root,
		staging_dir, previous_dir, plan_json,
		native_backup, native_existed, marker_backup, marker_existed,
		prior_owned, prepared_entries, applied_entries, moved_files,
		started_at, updated_at, completed_at, error_message
		FROM archive_deployment_journal
		WHERE state NOT IN ('done', 'failed')
		ORDER BY started_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []deploymentJournalEntry
	for rows.Next() {
		var e deploymentJournalEntry
		var planJSON, movedJSON, priorJSON, preparedJSON, appliedJSON string
		if err := rows.Scan(
			&e.ID, &e.OperationID, &e.State, &e.Purpose, &e.OwnerID,
			&e.DestinationRoot, &e.StagingDir, &e.PreviousDir, &planJSON,
			&e.NativeBackup, &e.NativeExisted, &e.MarkerBackup, &e.MarkerExisted,
			&priorJSON, &preparedJSON, &appliedJSON, &movedJSON,
			&e.StartedAt, &e.UpdatedAt, &e.CompletedAt, &e.ErrorMessage,
		); err != nil {
			return nil, err
		}
		payloads := []struct{ data string; target any }{
			{planJSON, &e.Plan}, {priorJSON, &e.PriorOwned}, {preparedJSON, &e.PreparedEntries},
			{appliedJSON, &e.AppliedEntries}, {movedJSON, &e.MovedFiles},
		}
		for _, payload := range payloads {
			if err := json.Unmarshal([]byte(payload.data), payload.target); err != nil {
				return nil, fmt.Errorf("decode deployment journal %s: %w", e.ID, err)
			}
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil { return nil, err }
	if err := rows.Close(); err != nil { return nil, err }
	for i := range entries {
		prepared, err := s.loadPreparedArchiveEntries(ctx, entries[i].ID)
		if err != nil { return nil, err }
		if len(prepared) > 0 { entries[i].PreparedEntries = prepared }
	}
	return entries, nil
}

type deploymentJournalEntry struct {
	ID              string                `json:"id"`
	OperationID     string                `json:"operationId"`
	State           string                `json:"state"`
	Purpose         string                `json:"purpose"`
	OwnerID         string                `json:"ownerId"`
	DestinationRoot string                `json:"destinationRoot"`
	StagingDir      string                `json:"stagingDir"`
	PreviousDir     string                `json:"previousDir"`
	Plan            ArchiveDeploymentPlan `json:"plan"`
	NativeBackup    []byte                `json:"nativeBackup,omitempty"`
	NativeExisted   bool                  `json:"nativeExisted"`
	MarkerBackup    []byte                `json:"markerBackup,omitempty"`
	MarkerExisted   bool                  `json:"markerExisted"`
	// PriorOwned is an immutable snapshot of the active owned entries for this
	// purpose/owner BEFORE the deployment mutates anything.
	PriorOwned []OwnedArchiveEntry `json:"priorOwned,omitempty"`
	// PreparedEntries are the new ownership records with staged file identities,
	// persisted after staging completes but before activation. Recovery for
	// prepared/activating states uses these to identify staged payloads.
	PreparedEntries []OwnedArchiveEntry `json:"preparedEntries,omitempty"`
	// AppliedEntries are the final ownership records with actual target
	// identities after activation. Written atomically with state=applied.
	AppliedEntries []OwnedArchiveEntry `json:"appliedEntries,omitempty"`
	// MovedFiles records each reuse-file rename with source identity, persisted
	// BEFORE each rename executes so crash mid-batch can undo by identity.
	MovedFiles  []movedFileRecord `json:"movedFiles,omitempty"`
	StartedAt   string            `json:"startedAt"`
	UpdatedAt   string            `json:"updatedAt"`
	CompletedAt string            `json:"completedAt"`
	ErrorMessage string           `json:"errorMessage"`
}

// movedFileRecord tracks a file move for crash undo, including source identity
// so recovery can verify the file before reverse-renaming.
type movedFileRecord struct {
	From           string              `json:"from"`
	To             string              `json:"to"`
	SourceIdentity ArchiveFileIdentity `json:"sourceIdentity"`
}

// ---------------------------------------------------------------------------
// Deployment mode config
// ---------------------------------------------------------------------------

// archiveDeploymentMode reads the persisted deployment mode from the config.
// Returns DeploymentModeAuto when unset. archivePolicyMu must be held for read.
func (service *AppService) archiveDeploymentMode() string {
	return archiveDeploymentModeFromConfig(service.config)
}

func archiveDeploymentModeFromConfig(config AppConfig) string {
	mode := config.ArchiveDeploymentMode
	if mode == "" {
		return DeploymentModeAuto
	}
	if !ValidDeploymentMode(mode) {
		return DeploymentModeAuto
	}
	return mode
}

// SetArchiveDeploymentMode persists the new mode and returns the updated state.
// Acquires archivePolicyMu for write. Persists to disk BEFORE changing the
// in-memory value, so a crash after write but before memory update is safe
// (next startup reads the persisted value). Does not apply the mode; the next
// deployment uses it.
func (service *AppService) SetArchiveDeploymentMode(ctx context.Context, mode string) (ArchiveDeploymentState, error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	if !ValidDeploymentMode(mode) {
		return ArchiveDeploymentState{}, fmt.Errorf("invalid archive deployment mode: %q; valid values are auto, hardlink-only, copy", mode)
	}
	service.archivePolicyMu.Lock()
	defer service.archivePolicyMu.Unlock()

	// Persist first; only update in-memory after disk write succeeds.
	if err := service.persistDeploymentModeConfig(mode); err != nil {
		return ArchiveDeploymentState{}, fmt.Errorf("save deployment mode: %w", err)
	}
	service.config.ArchiveDeploymentMode = mode

	return service.getArchiveDeploymentStateLocked(ctx)
}

// persistDeploymentModeConfig writes the mode to the persisted config file.
// Caller holds archivePolicyMu.
func (service *AppService) persistDeploymentModeConfig(mode string) error {
	configPath := service.config.ConfigPath
	if configPath == "" {
		return errors.New("no configuration file path available")
	}
	data, err := os.ReadFile(configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var doc map[string]json.RawMessage
	if len(data) > 0 {
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
	}
	if doc == nil {
		doc = make(map[string]json.RawMessage)
	}
	modeJSON, _ := json.Marshal(mode)
	doc["archiveDeploymentMode"] = modeJSON
	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomic(configPath, payload, 0o600)
}

// GetArchiveDeploymentState reports the current mode, capabilities, and any
// warnings. Acquires archivePolicyMu for read.
func (service *AppService) GetArchiveDeploymentState(ctx context.Context) (ArchiveDeploymentState, error) {
	service.archivePolicyMu.RLock()
	defer service.archivePolicyMu.RUnlock()
	return service.getArchiveDeploymentStateLocked(ctx)
}

func (service *AppService) getArchiveDeploymentStateLocked(ctx context.Context) (ArchiveDeploymentState, error) {
	mode := service.archiveDeploymentMode()
	state := ArchiveDeploymentState{Mode: mode}

	// Probe each configured source root against the game destination.
	destinationRoot := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
	for _, sourceRoot := range service.config.ScanRoots {
		cap, err := service.ProbeArchiveDeployment(ctx, sourceRoot, destinationRoot)
		if err != nil {
			cap = ArchiveCapability{
				SourceRoot:      sourceRoot,
				DestinationRoot: destinationRoot,
				ReasonCode:      capReasonIOError,
				Reason:          err.Error(),
				Checked:         true,
				CheckedAt:       nowUTC(),
			}
		}
		state.Capabilities = append(state.Capabilities, cap)
	}

	// Determine mixed state and warning.
	hasHardlink := false
	hasCopyOnly := false
	for _, cap := range state.Capabilities {
		if cap.Hardlinks {
			hasHardlink = true
		} else if cap.Checked {
			hasCopyOnly = true
		}
	}
	state.Mixed = hasHardlink && hasCopyOnly

	if mode == DeploymentModeHardlinkOnly && hasCopyOnly {
		state.Warning = "Some mod folders can't use links, so Play won't start while mods from them are selected. Choose Automatic to copy those mods instead."
	}

	return state, nil
}

// ---------------------------------------------------------------------------
// Capability probing
// ---------------------------------------------------------------------------

// ProbeArchiveDeployment tests whether hardlinks are supported from sourceRoot
// to destinationRoot by creating a disposable probe file and attempting a link.
// It handles read-only sources by linking an existing file instead. Always
// cleans up probe artifacts. Does NOT create directories — probes the nearest
// existing ancestor of the destination instead.
func (service *AppService) ProbeArchiveDeployment(ctx context.Context, sourceRoot, destinationRoot string) (ArchiveCapability, error) {
	cap := ArchiveCapability{SourceRoot: sourceRoot, DestinationRoot: destinationRoot, CheckedAt: nowUTC(), FreeBytes: -1}
	if err := ctx.Err(); err != nil { return cap, err }
	if strings.TrimSpace(sourceRoot) == "" || strings.TrimSpace(destinationRoot) == "" {
		cap.ReasonCode, cap.Reason = capReasonNotChecked, "Choose both folders before checking deployment."
		return cap, nil
	}
	source, err := os.Stat(sourceRoot)
	if err != nil || !source.IsDir() {
		cap.ReasonCode, cap.Reason = capReasonDriveUnavailable, "Source folder is unavailable."
		return cap, nil
	}
	if destination, err := os.Stat(destinationRoot); err == nil && !destination.IsDir() {
		cap.ReasonCode, cap.Reason = capReasonIOError, "Destination is not a folder."
		return cap, nil
	}
	destination := nearestExistingDir(destinationRoot)
	if destination == "" {
		cap.ReasonCode, cap.Reason = capReasonDriveUnavailable, "Destination drive is unavailable."
		return cap, nil
	}
	// A real exclusive write tests destination permissions even across volumes.
	destProbe, cleanDestination, err := createProbeFile(destination)
	if err != nil {
		cap.Checked = true
		cap.ReasonCode, cap.Reason = capReasonPermission, fmt.Sprintf("Destination is not writable: %v", err)
		return cap, nil
	}
	defer cleanDestination()
	cap.CopyPossible = true
	if free, err := availableArchiveBytes(destination); err == nil { cap.FreeBytes = free }
	sourceIdentity, _ := inspectArchiveFile(sourceRoot)
	destinationIdentity, _ := inspectArchiveFile(destination)
	if sourceIdentity.IdentityKnown { cap.SourceVolumeID = sourceIdentity.VolumeID }
	if destinationIdentity.IdentityKnown { cap.DestinationVolumeID = destinationIdentity.VolumeID }
	if cap.SourceVolumeID != "" && cap.DestinationVolumeID != "" && cap.SourceVolumeID != cap.DestinationVolumeID {
		cap.Checked = true
		cap.ReasonCode, cap.Reason = capReasonDifferentVolume, "Different volumes"
		return cap, nil
	}
	probeSource, cleanSource, err := createProbeFile(sourceRoot)
	if err != nil {
		probeSource = findExistingReadableFile(sourceRoot)
		if probeSource == "" {
			cap.ReasonCode, cap.Reason = capReasonReadOnlySource, "Hardlink support is unverified: source is read-only and no readable archive is available."
			return cap, nil
		}
	} else { defer cleanSource() }
	if err := ctx.Err(); err != nil { return cap, err }
	sourceInfo, sourceErr := os.Stat(probeSource)
	if sourceErr != nil { return cap, sourceErr }
	if err := os.Remove(destProbe); err != nil { return cap, err }
	if err := os.Link(probeSource, destProbe); err != nil {
		cap.Checked = true
		cap.ReasonCode, cap.Reason = capReasonUnsupported, fmt.Sprintf("Hardlink test failed: %v", err)
		if errors.Is(err, os.ErrPermission) { cap.ReasonCode = capReasonPermission }
		return cap, nil
	}
	defer func() {
		if current, err := os.Lstat(destProbe); err == nil && os.SameFile(sourceInfo, current) { _ = os.Remove(destProbe) }
	}()
	destinationInfo, destinationErr := os.Lstat(destProbe)
	if sourceErr != nil || destinationErr != nil || !os.SameFile(sourceInfo, destinationInfo) {
		cap.ReasonCode, cap.Reason = capReasonUnsupported, "Hardlink identity could not be verified."
		return cap, nil
	}
	if err := ctx.Err(); err != nil { return cap, err }
	cap.Checked, cap.Hardlinks = true, true
	cap.ReasonCode, cap.Reason = capReasonSameVolume, "Same volume, hardlinks available"
	return cap, nil
}

// nearestExistingDir walks up from path to find the first existing directory.
func nearestExistingDir(path string) string {
	current := filepath.Clean(path)
	for range 64 {
		info, err := os.Stat(current)
		if err == nil && info.IsDir() {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return ""
}

// createProbeFile creates a small disposable file in root and returns its path
// and a cleanup function. Returns an error if the directory is not writable.
func createProbeFile(root string) (string, func(), error) {
	file, err := os.CreateTemp(root, ".beamworlds-probe-*")
	if err != nil { return "", nil, err }
	path := file.Name()
	info, statErr := file.Stat()
	_, writeErr := file.Write([]byte("probe"))
	closeErr := file.Close()
	cleanup := func() {
		if current, err := os.Lstat(path); err == nil && info != nil && os.SameFile(info, current) { _ = os.Remove(path) }
	}
	if err := errors.Join(statErr, writeErr, closeErr); err != nil { cleanup(); return "", nil, err }
	return path, cleanup, nil
}

// findExistingReadableFile finds a regular file in root that can be used as
// a probe source when the source directory is read-only.
func findExistingReadableFile(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 || !isModArchive(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			continue
		}
		path := filepath.Join(root, e.Name())
		file, err := os.Open(path)
		if err != nil { continue }
		_ = file.Close()
		return path
	}
	return ""
}

// ---------------------------------------------------------------------------
// Plan
// ---------------------------------------------------------------------------

// PlanPlayDeployment builds a reviewed deployment plan for a Play selection.
// Besides adopting managed archives verified identical to catalog archives,
// it does not mutate the filesystem or database. The plan includes a
// fingerprint that must be presented to apply it. OwnerID is the stable
// logical owner "active-play", not a per-invocation UUID; operationID is
// generated only at apply time.
func (service *AppService) PlanPlayDeployment(ctx context.Context, request PlayRequest) (ArchiveDeploymentPlan, error) {
	service.flushLibrarySyncs(ctx)
	ids := normalizePlayCollectionIDs(request.CollectionIDs)
	excludedIDs := normalizePlayCollectionIDs(request.ExcludedCollectionIDs)

	playRoot, err := playUserPath(service.config)
	if err != nil {
		return ArchiveDeploymentPlan{}, err
	}
	destinationRoot := playProfileModsDir(playRoot)

	service.modImportMu.Lock()
	if adoptErr := service.adoptExistingManagedEntries(ctx); adoptErr != nil {
		service.modImportMu.Unlock()
		return ArchiveDeploymentPlan{}, fmt.Errorf("adopt managed entries: %w", adoptErr)
	}
	if syncErr := service.syncPlayProfile(ctx); syncErr != nil {
		service.modImportMu.Unlock()
		return ArchiveDeploymentPlan{}, fmt.Errorf("sync play profile: %w", syncErr)
	}
	if _, harvestErr := service.reconcileProfileSessionDownloads(ctx, playRoot); harvestErr != nil {
		service.modImportMu.Unlock()
		return ArchiveDeploymentPlan{}, fmt.Errorf("harvest session downloads: %w", harvestErr)
	}
	service.modImportMu.Unlock()

	selection, err := service.store.ResolvePlaySelection(ctx, ids, excludedIDs)
	if err != nil {
		return ArchiveDeploymentPlan{}, err
	}

	return service.buildArchiveDeploymentPlan(ctx, selection, destinationRoot, archivePurposePlay, playDeploymentOwnerID)
}

// buildArchiveDeploymentPlan constructs a deployment plan from a resolved selection.
// Acquires archivePolicyMu for reading the current mode.
//
// OwnerID is a stable logical identifier (e.g. "active-play", full collection ID),
// not a per-invocation UUID. OperationID is generated only at apply time.
//
// Lock contract: does NOT hold modImportMu or store.writeMu.
func (service *AppService) buildArchiveDeploymentPlan(ctx context.Context, selection PlaySelection, destinationRoot, purpose, ownerID string) (ArchiveDeploymentPlan, error) {
	service.archivePolicyMu.RLock()
	mode := service.archiveDeploymentMode()
	service.archivePolicyMu.RUnlock()

	plan := ArchiveDeploymentPlan{
		SelectionFingerprint:  selection.Fingerprint,
		Mode:                  mode,
		Purpose:               purpose,
		OwnerID:               ownerID,
		DestinationRoot:       destinationRoot,
		CollectionIDs:         append([]string(nil), selection.CollectionIDs...),
		ExcludedCollectionIDs: append([]string(nil), selection.ExcludedCollectionIDs...),
	}

	// Probe capability for each unique source root.
	sourceRoots := make(map[string]ArchiveCapability)
	capForSource := func(sourcePath string) ArchiveCapability {
		root := filepath.Dir(sourcePath)
		if cap, ok := sourceRoots[strings.ToLower(root)]; ok {
			return cap
		}
		cap, _ := service.ProbeArchiveDeployment(ctx, root, destinationRoot)
		sourceRoots[strings.ToLower(root)] = cap
		plan.Capabilities = append(plan.Capabilities, cap)
		return cap
	}

	// Load existing owned entries for reuse detection. Filter to same
	// purpose AND same logical owner so one collection's entries are never
	// reused by a different collection.
	existingEntries, err := service.store.listOwnedArchiveEntries(ctx)
	if err != nil { return ArchiveDeploymentPlan{}, err }
	existingByEntity := make(map[string]OwnedArchiveEntry, len(existingEntries))
	for _, e := range existingEntries {
		if e.State == archiveStateActive && e.Purpose == purpose && e.OwnerID == ownerID {
			existingByEntity[e.EntityID] = e
		}
	}

	fileLimit, fileLimitErr := archiveDestinationFileLimit(destinationRoot)

	for _, mod := range selection.Mods {
		if err := ctx.Err(); err != nil { return ArchiveDeploymentPlan{}, err }
		sourcePath := cleanOptionalPath(mod.ArchivePath)
		if sourcePath == "" || !mod.Available {
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s: no source archive available", mod.DisplayName))
			continue
		}

		entry := ArchiveDeploymentEntry{
			EntityID:   mod.EntityID,
			ArtifactID: mod.ArtifactID,
			SourcePath: sourcePath,
			SHA256:     strings.ToLower(strings.TrimSpace(mod.SHA256)),
			SizeBytes:  mod.SizeBytes,
		}

		// Folder sources are deployed as directory junctions (zero-copy).
		if kind, kindErr := modkit.SourceKindOf(sourcePath); kindErr == nil && kind == modkit.SourceFolder {
			entry.Method = deployMethodJunction
			entry.VerifySource = false
			base := sanitizeArchiveLabel(mod.DisplayName)
			if base == "" { base = sanitizeArchiveLabel(filepath.Base(sourcePath)) }
			if base == "" { base = "mod" }
			if runes := []rune(base); len(runes) > 80 { base = string(runes[:80]) }
			entry.DestinationPath = filepath.Join(destinationRoot, "unpacked", fmt.Sprintf("%s-%s", base, mod.EntityID))

			// Reuse: an existing junction pointing at the same source folder.
			if existing, ok := existingByEntity[mod.EntityID]; ok && existing.Method == deployMethodJunction && samePath(existing.SourcePath, sourcePath) {
				existingDest := filepath.Join(existing.TargetRoot, existing.RelativePath)
				if isDirectoryJunction(existingDest) && samePath(junctionTarget(existingDest), filepath.Clean(sourcePath)) {
					entry.Reuse = true
					entry.DestinationPath = existingDest
					plan.ReusedCount++
				}
			}
			if !entry.Reuse {
				plan.LinkedCount++
			}
			plan.Entries = append(plan.Entries, entry)
			continue
		}

		srcIdent, srcErr := inspectArchiveFile(sourcePath)
		if srcErr != nil || !srcIdent.Regular || !srcIdent.IdentityKnown {
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s: source archive identity could not be verified: %v", mod.DisplayName, srcErr))
			continue
		}
		info, statErr := os.Stat(sourcePath)
		if statErr != nil { return ArchiveDeploymentPlan{}, statErr }
		entry.VerifySource = !recordedArchiveIdentityHolds(mod,info)
		if existing,ok:=existingByEntity[mod.EntityID];ok && entry.SHA256!="" && existing.SHA256==entry.SHA256 && sameArchiveObject(srcIdent,existing.SourceIdentity){entry.VerifySource=false}
		if entry.SHA256=="" && !recordedArchiveMetadataHolds(mod,info){plan.Blockers=append(plan.Blockers,fmt.Sprintf("%s changed since indexing; rescan before verifying its first checksum",mod.DisplayName));continue}
		entry.SourceIdentity = srcIdent
		entry.SizeBytes = srcIdent.SizeBytes

		// Determine method from capability and mode.
		cap := capForSource(sourcePath)
		if !cap.CopyPossible {
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s: %s", mod.DisplayName, cap.Reason))
			continue
		}
		method := deployMethodCopy
		switch mode {
		case DeploymentModeAuto:
			if cap.Hardlinks {
				method = deployMethodHardlink
			}
		case DeploymentModeHardlinkOnly:
			if !cap.Hardlinks {
				plan.Blockers = append(plan.Blockers,
					fmt.Sprintf("%s: hardlinks unavailable (%s)", mod.DisplayName, cap.Reason))
				continue
			}
			method = deployMethodHardlink
		case DeploymentModeCopy:
			method = deployMethodCopy
		}
		entry.Method = method
		if method == deployMethodCopy {
			if fileLimitErr != nil { plan.Blockers=append(plan.Blockers,fmt.Sprintf("Cannot inspect destination filesystem: %v",fileLimitErr)); continue }
			if fileLimit>0 && entry.SizeBytes>fileLimit { plan.Blockers=append(plan.Blockers,fmt.Sprintf("%s exceeds the destination filesystem's %d-byte file limit",mod.DisplayName,fileLimit)); continue }
		}


		// Build collision-safe destination path using the FULL entity ID.
		base := sanitizeArchiveLabel(strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath)))
		if base == "" {
			base = sanitizeArchiveLabel(mod.DisplayName)
		}
		if base == "" {
			base = "mod"
		}
		if runes:=[]rune(base);len(runes)>80 {base=string(runes[:80])}
		entry.DestinationPath = filepath.Join(destinationRoot, fmt.Sprintf("%s-%s.zip", base, mod.EntityID))

		// Check for reuse: existing deployment entry for this owner with matching identity.
		if existing, ok := existingByEntity[mod.EntityID]; ok {
			existingDest := filepath.Join(existing.TargetRoot, existing.RelativePath)
			if existing.Method == method && existing.SHA256 == entry.SHA256 && sameArchiveObject(srcIdent, existing.SourceIdentity) {
				tgtIdent, tgtErr := inspectArchiveFile(existingDest)
				if tgtErr == nil && sameArchiveObject(tgtIdent, existing.TargetIdentity) && tgtIdent.SizeBytes == srcIdent.SizeBytes {
					reused := false
					if method == deployMethodHardlink {
						if tgtIdent.IdentityKnown && srcIdent.IdentityKnown &&
							tgtIdent.FileID == srcIdent.FileID &&
							tgtIdent.VolumeID == srcIdent.VolumeID {
							reused = true
						}
					} else if method == deployMethodCopy {
						if tgtIdent.IdentityKnown && srcIdent.IdentityKnown &&
							(tgtIdent.FileID != srcIdent.FileID || tgtIdent.VolumeID != srcIdent.VolumeID) &&
							tgtIdent.ModifiedNs == existing.TargetIdentity.ModifiedNs {
							reused = true
						} else if !tgtIdent.IdentityKnown &&
							tgtIdent.ModifiedNs == existing.TargetIdentity.ModifiedNs {
							reused = true
						}
					}
					if reused {
						entry.Reuse = true
						entry.TargetIdentity = tgtIdent
						// If the existing file is at a different root (e.g.
						// collection rename), record the prior path for a
						// journaled move rather than a recopy.
						if !strings.EqualFold(existing.TargetRoot, destinationRoot) {
							entry.ReusePath = existingDest
						} else {
							entry.DestinationPath = existingDest
						}
					}
				}
			}
		}

		if entry.Reuse {
			plan.ReusedCount++
			entry.VerifySource=false
		} else if method == deployMethodHardlink {
			plan.LinkedCount++
		} else {
			plan.CopyBytes += entry.SizeBytes
		}
		if !entry.Reuse && (entry.VerifySource || method==deployMethodCopy) {plan.HashBytes+=entry.SizeBytes}

		plan.Entries = append(plan.Entries, entry)
	}

	// Space requirements: actual peak incremental accounts for old + new copies
	// coexisting during the staged swap. Only NEW copy payloads count; reused
	// copies stay in place and are not duplicated.
	plan.AdditionalBytes = plan.CopyBytes
	ownershipHash:=sha256.New()
	ownershipEncoder:=json.NewEncoder(ownershipHash)
	for _,existing:=range existingEntries {
		if existing.Purpose!=purpose || existing.OwnerID!=ownerID || existing.State!=archiveStateActive {continue}
		snapshot:=existing
		snapshot.SourceIdentity.Links,snapshot.TargetIdentity.Links=0,0
		snapshot.SourceIdentity.AllocatedBytes,snapshot.TargetIdentity.AllocatedBytes=0,0
		snapshot.SourceIdentity.AllocationKnown,snapshot.TargetIdentity.AllocationKnown=false,false
		_ = ownershipEncoder.Encode(snapshot)
		reused:=false
		for _,entry:=range plan.Entries {if entry.Reuse && entry.EntityID==existing.EntityID && sameArchiveObject(entry.TargetIdentity,existing.TargetIdentity){reused=true;break}}
		if reused {continue}
		plan.Retiring=append(plan.Retiring,existing)
		if existing.Method==deployMethodCopy {
			target,exists,targetErr:=archiveIdentityIfPresent(filepath.Join(existing.TargetRoot,existing.RelativePath))
			source,available,sourceErr:=archiveIdentityIfPresent(existing.SourcePath)
			if targetErr==nil && sourceErr==nil && exists && available && target.Links==1 && sameArchiveObject(target,existing.TargetIdentity) && sameArchiveObject(source,existing.SourceIdentity) {
				plan.AdditionalBytes-=target.AllocatedBytes
			}
		}
	}
	if plan.AdditionalBytes<0{plan.AdditionalBytes=0}
	plan.OwnershipFingerprint=hex.EncodeToString(ownershipHash.Sum(nil))
	if purpose==archivePurposePlay {
		plan.Blockers=append(plan.Blockers,checkManagedRootOwnership(ctx,service.store,destinationRoot)...)
	}
	// Check available space against the actual destination.
	destFree := int64(-1)
	for _, cap := range plan.Capabilities {
		if cap.FreeBytes > destFree {
			destFree = cap.FreeBytes
		}
	}
	plan.PeakBytes = plan.CopyBytes

	if plan.CopyBytes > 0 && destFree < plan.PeakBytes {
		plan.Blockers = append(plan.Blockers,
			fmt.Sprintf("insufficient space: need %d bytes peak, %d available", plan.PeakBytes, destFree))
	}

	// Copy confirmation.
	if plan.CopyBytes > 0 {
		plan.RequiresCopyConfirmation = mode == DeploymentModeAuto || mode == DeploymentModeCopy
	}

	// Collection-purpose deployments use merge mode.
	if purpose == archivePurposeCollection {
		plan.Merge = true
	}

	// Generate deterministic fingerprint.
	plan.Fingerprint = computeDeploymentFingerprint(plan)

	return plan, nil
}


// computeDeploymentFingerprint produces a deterministic fingerprint covering
// purpose, owner, mode, merge, destination, and per-entry entity/artifact/SHA/
// method/source path/destination path/source identity/target identity. Excludes
// timestamps, free-space, transient probe data. Replanning identical state
// yields the same fingerprint.
func computeDeploymentFingerprint(plan ArchiveDeploymentPlan) string {
	h := sha256.New()
	encoder := json.NewEncoder(h)
	_ = encoder.Encode(struct {
		Purpose, Owner, Mode, Destination, Selection, Ownership string
		CopyBytes, PeakBytes, AdditionalBytes, HashBytes int64
		Merge bool
	}{plan.Purpose, plan.OwnerID, plan.Mode, plan.DestinationRoot, plan.SelectionFingerprint, plan.OwnershipFingerprint, plan.CopyBytes, plan.PeakBytes, plan.AdditionalBytes, plan.HashBytes, plan.Merge})
	for _, entry := range plan.Entries {
		// Link counts/allocation change when another alias is made; they do not
		// change the source bytes or invalidate an otherwise identical plan.
		entry.SourceIdentity.Links, entry.TargetIdentity.Links = 0, 0
		entry.SourceIdentity.AllocatedBytes, entry.TargetIdentity.AllocatedBytes = 0, 0
		entry.SourceIdentity.AllocationKnown, entry.TargetIdentity.AllocationKnown = false, false
		_ = encoder.Encode(entry)
	}
	_ = encoder.Encode(plan.Blockers)
	return hex.EncodeToString(h.Sum(nil))
}

// ---------------------------------------------------------------------------
// Apply
// ---------------------------------------------------------------------------

// applyArchiveDeployment executes a deployment plan. The caller MUST hold
// modImportMu. This function does NOT acquire store.writeMu over file I/O —
// it uses reviewed snapshots, journals, and revalidation.
//
// beforeActivate is called after staging completes but before the first
// visible filesystem mutation (directory swap or merge file write). Play
// passes service.requireGameStopped; collection mirrors may pass their own
// guard. A nil beforeActivate skips the check.
//
// commit is called after filesystem activation succeeds but before ownership
// records are persisted. Play writes db.json and the runtime marker here.
//
// Lock contract:
//   - Caller holds: modImportMu
//   - This function acquires: store.writeMu (briefly, for journal/ownership writes)
//   - This function does NOT hold: store.writeMu during hashing/copying
func (service *AppService) applyArchiveDeployment(ctx context.Context, plan ArchiveDeploymentPlan, beforeActivate func() error, commit func() error) (ArchiveDeploymentResult, error) {
	return service.runArchiveDeployment(ctx, plan, beforeActivate, commit)
}


// makeOwnedEntry constructs an OwnedArchiveEntry from plan/entry data.
// OwnerID is the plan's stable logical owner, not the per-apply operationID.
func makeOwnedEntry(id string, plan ArchiveDeploymentPlan, entry ArchiveDeploymentEntry, destinationRoot string, tgtIdent ArchiveFileIdentity) OwnedArchiveEntry {
	relPath := filepath.Base(entry.DestinationPath)
	if entry.Method == deployMethodJunction {
		if rel, err := filepath.Rel(destinationRoot, entry.DestinationPath); err == nil && rel != "" {
			relPath = rel
		}
	}
	return OwnedArchiveEntry{
		ID:             id,
		Purpose:        plan.Purpose,
		OwnerID:        plan.OwnerID,
		EntityID:       entry.EntityID,
		ArtifactID:     entry.ArtifactID,
		SHA256:         entry.SHA256,
		SourcePath:     entry.SourcePath,
		TargetRoot:     destinationRoot,
		RelativePath:   relPath,
		Method:         entry.Method,
		State:          archiveStateActive,
		SourceIdentity: entry.SourceIdentity,
		TargetIdentity: tgtIdent,
	}
}

// checkManagedRootOwnership inspects the current managed directory for files
// that are not tracked in the ownership ledger. Returns a list of blocking
// descriptions for unowned ZIPs and unknown mod-bearing directories.
// Non-ZIP regular files are not blockers (harmless metadata, logs, etc.).
// The "unpacked" subdirectory is allowed and its owned junctions are verified.
func checkManagedRootOwnership(ctx context.Context, store *Store, managedRoot string) []string {
	root, err := os.Lstat(managedRoot)
	if errors.Is(err, os.ErrNotExist) { return nil }
	if err != nil { return []string{fmt.Sprintf("inspect managed directory: %v", err)} }
	if !root.IsDir() || root.Mode()&os.ModeSymlink != 0 { return []string{"managed directory is a changed reparse or non-directory entry"} }
	entries, err := os.ReadDir(managedRoot)
	if err != nil { return []string{fmt.Sprintf("read managed directory: %v", err)} }
	owned, err := store.listOwnedArchiveEntries(ctx)
	if err != nil { return []string{fmt.Sprintf("read ownership ledger: %v", err)} }
	byPath := make(map[string]OwnedArchiveEntry, len(owned))
	for _, entry := range owned {
		if entry.State == archiveStateActive && entry.Purpose == archivePurposePlay && samePath(entry.TargetRoot, managedRoot) {
			byPath[archivePathKey(filepath.Join(entry.TargetRoot, entry.RelativePath))] = entry
		}
	}
	var blockers []string
	for _, file := range entries {
		path := filepath.Join(managedRoot, file.Name())
		if entry, ok := byPath[archivePathKey(path)]; ok {
			identity, exists, err := archiveIdentityIfPresent(path)
			if err == nil && exists && sameArchiveObject(identity, entry.TargetIdentity) { continue }
			blockers = append(blockers, fmt.Sprintf("owned archive changed outside Studio: %s", path))
			continue
		}
		if strings.EqualFold(file.Name(), "unpacked") && file.IsDir() {
			blockers = append(blockers, checkUnpackedSubdirOwnership(managedRoot, path, byPath)...)
			continue
		}
		if file.IsDir() {
			// Empty subdirectories in the profile mods dir are not blockers.
			sub, _ := os.ReadDir(path)
			if len(sub) == 0 { continue }
			blockers = append(blockers, fmt.Sprintf("%q in Studio's game mod folder doesn't match any mod in your library. Add it to your library from Review storage, or move it out of that folder", file.Name()))
		} else if file.Type()&os.ModeSymlink != 0 || isModArchive(file.Name()) {
			blockers = append(blockers, fmt.Sprintf("%q in Studio's game mod folder doesn't match any mod in your library. Add it to your library from Review storage, or move it out of that folder", file.Name()))
		}
	}
	return blockers
}

// checkUnpackedSubdirOwnership verifies entries inside the unpacked subdirectory
// of the managed mods folder. Owned junctions are accepted if their target has
// not changed. Real (non-junction) folders are not blockers—they will be
// harvested after the Play session ends.
func checkUnpackedSubdirOwnership(managedRoot, unpackedDir string, byPath map[string]OwnedArchiveEntry) []string {
	children, err := os.ReadDir(unpackedDir)
	if err != nil { return []string{fmt.Sprintf("read unpacked directory: %v", err)} }
	var blockers []string
	for _, child := range children {
		childPath := filepath.Join(unpackedDir, child.Name())
		relPath := filepath.Join("unpacked", child.Name())
		fullKey := archivePathKey(filepath.Join(managedRoot, relPath))
		if entry, ok := byPath[fullKey]; ok {
			if entry.Method == deployMethodJunction {
				if !isDirectoryJunction(childPath) {
					blockers = append(blockers, fmt.Sprintf("owned junction replaced by a regular entry: %s", childPath))
					continue
				}
				target := junctionTarget(childPath)
				if !samePath(target, entry.SourcePath) {
					blockers = append(blockers, fmt.Sprintf("junction target changed: %s (expected %s, found %s)", childPath, entry.SourcePath, target))
				}
				continue
			}
			// Non-junction owned entry inside unpacked: verify like a regular entry.
			identity, exists, identErr := archiveIdentityIfPresent(childPath)
			if identErr == nil && exists && sameArchiveObject(identity, entry.TargetIdentity) { continue }
			blockers = append(blockers, fmt.Sprintf("owned entry changed outside Studio: %s", childPath))
			continue
		}
		// Unowned entry. Junctions we don't recognise are blockers.
		if isDirectoryJunction(childPath) {
			blockers = append(blockers, fmt.Sprintf("unknown junction in unpacked folder: %s", childPath))
			continue
		}
		// Real folders are potential harvest candidates—not blockers.
		// Regular files are harmless metadata.
	}
	return blockers
}


// adoptExistingManagedEntries gives Studio ownership of unowned archives in
// beamworlds-managed whose bytes are verified identical to a catalog archive:
// a hardlink of the catalog file, or an independent copy whose full SHA-256
// matches. Such files are Studio deployments whose ledger rows were lost or
// never written (for example, legacy names that collided before the v7
// ledger); their contents remain available from the catalog, so adopting them
// cannot lose data. Anything else stays unowned and blocks deployment.
//
// Lock contract:
//   - Caller MUST hold modImportMu (this mutates the ledger and inspects files)
func (service *AppService) adoptExistingManagedEntries(ctx context.Context) error {
	// Scan the current profile mods directory.
	managedRoot := service.playManagedRoot()
	if err := service.adoptManagedEntriesInRoot(ctx, managedRoot); err != nil {
		return err
	}
	// Also scan the legacy managed directory for backward compatibility.
	legacyRoot := filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)
	if !samePath(legacyRoot, managedRoot) {
		return service.adoptManagedEntriesInRoot(ctx, legacyRoot)
	}
	return nil
}

func (service *AppService) adoptManagedEntriesInRoot(ctx context.Context, managedRoot string) error {
	files, err := os.ReadDir(managedRoot)
	if errors.Is(err, os.ErrNotExist) { return nil }
	if err != nil { return err }
	existing, err := service.store.listOwnedArchiveEntries(ctx)
	if err != nil { return err }
	ownedPaths := make(map[string]bool, len(existing))
	for _, entry := range existing { ownedPaths[archivePathKey(filepath.Join(entry.TargetRoot, entry.RelativePath))] = true }
	type orphan struct { name string; identity ArchiveFileIdentity }
	var orphans []orphan
	sizes := map[int64]bool{}
	for _, file := range files {
		if !file.Type().IsRegular() || !isModArchive(file.Name()) { continue }
		path := filepath.Join(managedRoot, file.Name())
		if ownedPaths[archivePathKey(path)] { continue }
		identity, exists, err := archiveIdentityIfPresent(path)
		if err != nil || !exists { continue }
		orphans = append(orphans, orphan{file.Name(), identity})
		sizes[identity.SizeBytes] = true
	}
	if len(orphans) == 0 { return nil }
	rows, err := service.store.db.QueryContext(ctx, `SELECT l.entity_id,l.artifact_id,l.path,COALESCE(a.sha256,'')
		FROM archive_links l JOIN artifacts a ON a.id=l.artifact_id
		ORDER BY l.active DESC,l.last_seen_at DESC,l.id DESC`)
	if err != nil { return err }
	var sources []OwnedArchiveEntry
	for rows.Next() {
		var entry OwnedArchiveEntry
		if err := rows.Scan(&entry.EntityID, &entry.ArtifactID, &entry.SourcePath, &entry.SHA256); err != nil { rows.Close(); return err }
		if pathWithin(entry.SourcePath, managedRoot) || pathWithin(entry.SourcePath, service.config.ProfileDir) || pathWithin(entry.SourcePath, service.config.ExportDir) { continue }
		identity, exists, err := archiveIdentityIfPresent(entry.SourcePath)
		if err != nil || !exists || !sizes[identity.SizeBytes] { continue }
		entry.SourceIdentity = identity
		sources = append(sources, entry)
	}
	if err := rows.Err(); err != nil { rows.Close(); return err }
	if err := rows.Close(); err != nil { return err }
	var adopted []OwnedArchiveEntry
	for _, file := range orphans {
		path := filepath.Join(managedRoot, file.name)
		var match *OwnedArchiveEntry
		method := deployMethodHardlink
		for i := range sources {
			if sameArchiveObject(file.identity, sources[i].SourceIdentity) { match = &sources[i]; break }
		}
		if match == nil {
			copyHash, _, err := service.verifiedArchiveHash(ctx, path, file.identity)
			if err != nil { if ctx.Err() != nil { return ctx.Err() }; continue }
			for i := range sources {
				source := &sources[i]
				if source.SourceIdentity.SizeBytes != file.identity.SizeBytes || (source.SHA256 != "" && !strings.EqualFold(source.SHA256, copyHash)) { continue }
				sourceHash, _, err := service.verifiedArchiveHash(ctx, source.SourcePath, source.SourceIdentity)
				if err != nil || sourceHash != copyHash { if ctx.Err() != nil { return ctx.Err() }; continue }
				source.SHA256, match, method = sourceHash, source, deployMethodCopy
				break
			}
		}
		if match == nil { continue }
		id, err := modkit.NewID(); if err != nil { return err }
		entry := *match
		entry.ID, entry.Purpose, entry.OwnerID = id, archivePurposePlay, playDeploymentOwnerID
		entry.TargetRoot, entry.RelativePath = managedRoot, file.name
		entry.Method, entry.State, entry.TargetIdentity = method, archiveStateActive, file.identity
		adopted = append(adopted, entry)
	}
	if len(adopted) == 0 { return nil }
	if err := service.store.saveOwnedArchiveEntries(ctx, adopted); err != nil { return err }
	names := make([]string, len(adopted))
	for i, entry := range adopted { names[i] = entry.RelativePath }
	return service.store.AppendEvent(ctx, "", "archive_managed_ownership_adopted", map[string]any{"files": names})
}



// ---------------------------------------------------------------------------
// Ownership helpers for sibling/Main integration
// ---------------------------------------------------------------------------

// retireOwnedArchiveEntries marks owned entries for the given entity IDs as
// pending-retire and removes their target files if they are app-owned and no
// other active entry references the same file.
//
// Lock contract:
//   - Caller MUST hold modImportMu
//   - This function does NOT hold store.writeMu over filesystem I/O
//   - Preserves sole surviving data and pending ledger entries
func (service *AppService) retireOwnedArchiveEntries(ctx context.Context, entityIDs []string) error {
	wanted := make(map[string]bool,len(entityIDs))
	for _, id := range entityIDs { wanted[id]=true }
	entries, err := service.store.listOwnedArchiveEntries(ctx)
	if err != nil { return err }
	for _, entry := range entries {
		if !wanted[entry.EntityID] { continue }
		if err := service.validateOwnedArchivePath(entry); err != nil { return err }
		entry.State=archiveStatePendingRetire
		if err := service.store.saveOwnedArchiveEntries(ctx,[]OwnedArchiveEntry{entry}); err != nil { return err }
		target := filepath.Join(entry.TargetRoot,entry.RelativePath)
		if entry.Method == deployMethodJunction {
			// Junction retirement: remove the junction only, never recurse.
			if isDirectoryJunction(target) {
				actual := junctionTarget(target)
				if !samePath(actual, entry.SourcePath) {
					return fmt.Errorf("retirement pending: junction target changed: %s (expected %s, found %s)", target, entry.SourcePath, actual)
				}
				if err := service.requireGameStopped(); err != nil { return err }
				if err := removeDirectoryJunction(target); err != nil {
					return fmt.Errorf("retirement pending for junction %s: %w", target, err)
				}
			}
			if err := service.store.deleteOwnedArchiveEntry(ctx,entry.ID); err != nil { return err }
			continue
		}
		current, exists, err := archiveIdentityIfPresent(target)
		if err != nil { return err }
		if exists {
			source, sourceExists, sourceErr := archiveIdentityIfPresent(entry.SourcePath)
			if sourceErr != nil || !sourceExists || !source.Regular || samePath(entry.SourcePath,target) {
				return fmt.Errorf("retirement pending: preserve possible sole surviving archive %s; recover its canonical source through Review storage",target)
			}
			if !sameArchiveObject(current,entry.TargetIdentity) { return fmt.Errorf("retirement pending: owned file changed outside Studio: %s",target) }
			if err := service.requireGameStopped(); err != nil { return err }
			if err := os.Remove(target); err != nil { return fmt.Errorf("retirement pending for %s: %w",target,err) }
		}
		if err := service.store.deleteOwnedArchiveEntry(ctx,entry.ID); err != nil { return err }
	}
	return nil
}

// ---------------------------------------------------------------------------
// Updated activatePlaySelection integration
// ---------------------------------------------------------------------------

// activatePlaySelectionDirect is the new direct-deployment path that replaces
// the cache-based materializeMod loop. It builds a deployment plan, applies it
// with native state integration, and returns the activation record.
//
// This function is called from LaunchPlaySelection with modImportMu NOT held
// (profileMu is held instead). It acquires modImportMu internally for the
// filesystem mutation phase.
func (service *AppService) activatePlaySelectionDirect(ctx context.Context, request PlayRequest) (PlayActivation, error) {
	startedAt:=time.Now()
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	if err := service.recoverArchiveDeployment(ctx); err != nil { return PlayActivation{}, err }
	if strings.TrimSpace(service.config.BeamNGRoot) == "" || strings.TrimSpace(service.config.ActiveModsDir) == "" {
		return PlayActivation{}, errors.New("BeamNG paths are not configured")
	}
	if err := service.requireGameStopped(); err != nil {
		return PlayActivation{}, err
	}

	playRoot, err := playUserPath(service.config)
	if err != nil {
		return PlayActivation{}, err
	}
	destinationRoot := playProfileModsDir(playRoot)

	// Sync profile and harvest before planning.
	if err := service.syncPlayProfile(ctx); err != nil {
		return PlayActivation{}, fmt.Errorf("sync play profile: %w", err)
	}
	if _, err := service.reconcileProfileSessionDownloads(ctx, playRoot); err != nil {
		return PlayActivation{}, fmt.Errorf("harvest session downloads: %w", err)
	}

	ids := normalizePlayCollectionIDs(request.CollectionIDs)
	excludedIDs := normalizePlayCollectionIDs(request.ExcludedCollectionIDs)
	operationID, err := modkit.NewID()
	if err != nil {
		return PlayActivation{}, err
	}

	progress := PlayProgress{OperationID: operationID, Phase: "reviewing"}
	service.emitPlayProgress(progress)
	var resultErr error
	defer func() {
		if resultErr != nil {
			progress.Phase = "failed"
			progress.Error = resultErr.Error()
			progress.Done = true
			service.emitPlayProgress(progress)
		}
	}()

	selection, err := service.store.ResolvePlaySelection(ctx, ids, excludedIDs)
	if err != nil {
		resultErr = err
		return PlayActivation{}, err
	}
	if err := validatePlaySelectionRequest(request, selection); err != nil {
		resultErr = err
		return PlayActivation{}, err
	}

	progress.Phase = "planning"
	progress.Total = len(selection.Mods)
	for _, mod := range selection.Mods {
		if mod.SizeBytes > 0 {
			progress.TotalBytes += mod.SizeBytes
		}
	}
	service.emitPlayProgress(progress)

	plan, err := service.buildArchiveDeploymentPlan(ctx, selection, destinationRoot, archivePurposePlay, playDeploymentOwnerID)
	if err != nil {
		resultErr = err
		return PlayActivation{}, err
	}

	if request.DeploymentFingerprint == "" || plan.Fingerprint != request.DeploymentFingerprint {
		resultErr = errors.New("the deployment plan changed or was not reviewed; refresh and review it before applying")
		return PlayActivation{}, resultErr
	}
	if plan.RequiresCopyConfirmation && plan.CopyBytes > 0 && !request.AllowCopy {
		resultErr = fmt.Errorf("this deployment requires copying %d bytes; set allowCopy to proceed", plan.CopyBytes)
		return PlayActivation{}, resultErr
	}
	if len(plan.Blockers) > 0 {
		resultErr = fmt.Errorf("deployment blocked: %s", strings.Join(plan.Blockers, "; "))
		return PlayActivation{}, resultErr
	}

	progress.Phase = "activating"
	service.emitPlayProgress(progress)

	service.archivePolicyMu.RLock()
	currentMode := service.archiveDeploymentMode()
	service.archivePolicyMu.RUnlock()
	if currentMode != plan.Mode {
		resultErr = fmt.Errorf("deployment mode changed from %q to %q since the plan was reviewed; replan", plan.Mode, currentMode)
		return PlayActivation{}, resultErr
	}

	activatedAt := nowUTC()
	activation := PlayActivation{
		OperationID:           operationID,
		ModCount:              len(selection.Mods),
		UserPath:              playRoot,
		ModsPath:              destinationRoot,
		ActivatedAt:           activatedAt,
		CollectionIDs:         append([]string(nil), ids...),
		ExcludedCollectionIDs: append([]string(nil), excludedIDs...),
		Fingerprint:           strings.TrimSpace(selection.Fingerprint),
	}

	plan.operationID=operationID
	deployResult, err := service.applyArchiveDeployment(ctx, plan,
		service.requireGameStopped,
		func() error {
			// Delete profile db.json so BeamNG mounts all present zips as active.
			_ = os.Remove(filepath.Join(destinationRoot, "db.json"))
			if err := service.writePlayRuntimeMarker(activation); err != nil {
				return fmt.Errorf("write play marker: %w", err)
			}
			return nil
		})
	progress.BytesCopied=deployResult.CopiedBytes
	progress.BytesHashed=deployResult.HashedBytes

	if err != nil {
		resultErr = err
		return PlayActivation{}, err
	}

	progress.Phase = "ready"
	progress.Completed = progress.Total
	progress.BytesCopied = deployResult.CopiedBytes
	progress.Done = true
	service.emitPlayProgress(progress)

	_ = service.store.AppendEvent(ctx, "", "play_applied", map[string]any{
		"operationId": activation.OperationID, "modCount": activation.ModCount,
		"collectionIds": activation.CollectionIDs, "fingerprint": activation.Fingerprint,
		"linked": deployResult.Linked, "copied": deployResult.Copied,
		"reused": deployResult.Reused, "copiedBytes": deployResult.CopiedBytes,
		"hashedBytes":deployResult.HashedBytes,
		"preparationMillis":time.Since(startedAt).Milliseconds(),
	})

	return activation, nil
}

// emitStorageProgress sends a storage:progress event. Used by the cleanup
// sibling for audit/cleanup progress.
func (service *AppService) emitStorageProgress(progress StorageProgress) {
	if service.emit != nil {
		service.emit("storage:progress", progress)
	}
}

// StorageProgress reports progress for storage audit/cleanup operations.
type StorageProgress struct {
	OperationID    string                `json:"operationId"`
	Phase          string                `json:"phase"`
	Current        string                `json:"current"`
	Completed      int                   `json:"completed"`
	Total          int                   `json:"total"`
	BytesProcessed int64                 `json:"bytesProcessed"`
	Done           bool                  `json:"done"`
	Error          string                `json:"error"`
	Result         *StorageCleanupResult `json:"result,omitempty"`
}

// recordedArchiveIdentityHolds reports whether the archive on disk is still the
// one the scan fingerprinted: same length, same modification time, and a hash
// recorded alongside them. A rewrite that preserved both would defeat it, but
// nothing a mod manager or a download does preserves both, and this is the same
// identity the scan pipeline already trusts when it skips re-analysis.
func recordedArchiveIdentityHolds(mod CollectionMod, info os.FileInfo) bool {
	return strings.TrimSpace(mod.SHA256)!="" && recordedArchiveMetadataHolds(mod,info)
}

func recordedArchiveMetadataHolds(mod CollectionMod, info os.FileInfo) bool {
	if mod.SizeBytes <= 0 || strings.TrimSpace(mod.ModifiedAt) == "" {
		return false
	}
	if info.Size() != mod.SizeBytes {
		return false
	}
	recorded, parseErr := time.Parse(time.RFC3339Nano, mod.ModifiedAt)
	if parseErr != nil {
		return false
	}
	return info.ModTime().UTC().Equal(recorded.UTC())
}
