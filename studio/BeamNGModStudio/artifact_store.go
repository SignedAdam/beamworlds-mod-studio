package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

func (s *Store) EnsureArtifact(ctx context.Context, manifest modkit.Manifest) (string, error) {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	var id string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM artifacts WHERE central_fingerprint=?`, manifest.CentralFingerprint).Scan(&id)
	if err == nil {
		_, updateErr := s.db.ExecContext(ctx, `UPDATE artifacts SET sha256=CASE WHEN ?<>'' THEN ? ELSE sha256 END,size_bytes=?,manifest_json=?,analyzer_version=?,analyzed_at=? WHERE id=?`, manifest.FullSHA256, manifest.FullSHA256, manifest.SizeBytes, string(encoded), modkit.AnalyzerVersion, nowUTC(), id)
		return id, updateErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id, err = modkit.NewID()
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO artifacts(id,central_fingerprint,sha256,size_bytes,manifest_json,analyzer_version,analyzed_at) VALUES(?,?,?,?,?,?,?)`, id, manifest.CentralFingerprint, manifest.FullSHA256, manifest.SizeBytes, string(encoded), modkit.AnalyzerVersion, nowUTC())
	return id, err
}

func (s *Store) GetExport(ctx context.Context, id string) (ExportRecord, error) {
	var record ExportRecord
	err := s.db.QueryRowContext(ctx, `SELECT id,workspace_id,artifact_id,path,sha256,kind,created_at FROM exports WHERE id=?`, id).Scan(&record.ID, &record.WorkspaceID, &record.ArtifactID, &record.Path, &record.SHA256, &record.Kind, &record.CreatedAt)
	return record, err
}
