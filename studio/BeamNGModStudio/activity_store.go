package main

import (
	"context"
	"database/sql"
	"encoding/json"
)

type AgentRunRecord struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
	Prompt      string `json:"prompt"`
	Status      string `json:"status"`
	StartedAt   string `json:"startedAt"`
	FinishedAt  string `json:"finishedAt"`
	FinalText   string `json:"finalText"`
	Error       string `json:"error"`
}

type AgentEventRecord struct {
	ID      int64          `json:"id"`
	RunID   string         `json:"runId"`
	At      string         `json:"at"`
	Type    string         `json:"type"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

type TestInstallRecord struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspaceId"`
	ExportID      string `json:"exportId"`
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	InstalledAt   string `json:"installedAt"`
	LogBaselineAt string `json:"logBaselineAt"`
	LogPath       string `json:"logPath"`
	LogOffset     int64  `json:"logOffset"`
	Active        bool   `json:"active"`
}

func (s *Store) CreateAgentRun(ctx context.Context, record AgentRunRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_runs(id,workspace_id,prompt,status,started_at) VALUES(?,?,?,'running',?)`, record.ID, record.WorkspaceID, record.Prompt, record.StartedAt)
	return err
}

func (s *Store) FinishAgentRun(ctx context.Context, id, status, finalText, runError string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_runs SET status=?,finished_at=?,final_text=?,error=? WHERE id=?`, status, nowUTC(), finalText, runError, id)
	return err
}

func (s *Store) RecoverInterruptedAgentRuns(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_runs SET status='failed',finished_at=?,error=? WHERE status='running'`, nowUTC(), "Application exited before the OMP run finished")
	return err
}

func (s *Store) AppendAgentEvent(ctx context.Context, runID, eventType, message string, data map[string]any) error {
	encoded, _ := json.Marshal(data)
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_events(run_id,at,type,message,data_json) VALUES(?,?,?,?,?)`, runID, nowUTC(), eventType, message, string(encoded))
	return err
}

func (s *Store) ListAgentRuns(ctx context.Context, workspaceID string) ([]AgentRunRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,workspace_id,prompt,status,started_at,finished_at,final_text,error FROM agent_runs WHERE workspace_id=? ORDER BY started_at DESC LIMIT 100`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AgentRunRecord{}
	for rows.Next() {
		var record AgentRunRecord
		if err := rows.Scan(&record.ID, &record.WorkspaceID, &record.Prompt, &record.Status, &record.StartedAt, &record.FinishedAt, &record.FinalText, &record.Error); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) ListAgentEvents(ctx context.Context, runID string, limit int) ([]AgentEventRecord, error) {
	if limit <= 0 || limit > 2000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,at,type,message,data_json FROM agent_events WHERE run_id=? ORDER BY id LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AgentEventRecord{}
	for rows.Next() {
		var record AgentEventRecord
		var data string
		if err := rows.Scan(&record.ID, &record.RunID, &record.At, &record.Type, &record.Message, &data); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(data), &record.Data)
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) SaveTestInstall(ctx context.Context, record TestInstallRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE test_installs SET active=0 WHERE workspace_id=?`, record.WorkspaceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO test_installs(id,workspace_id,export_id,path,sha256,installed_at,log_baseline_at,log_path,log_offset,active) VALUES(?,?,?,?,?,?,?,?,?,1)`, record.ID, record.WorkspaceID, record.ExportID, record.Path, record.SHA256, record.InstalledAt, record.LogBaselineAt, record.LogPath, record.LogOffset); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetActiveTestInstall(ctx context.Context, workspaceID string) (TestInstallRecord, error) {
	var record TestInstallRecord
	err := s.db.QueryRowContext(ctx, `SELECT id,workspace_id,export_id,path,sha256,installed_at,log_baseline_at,log_path,log_offset,active FROM test_installs WHERE workspace_id=? AND active=1 ORDER BY installed_at DESC LIMIT 1`, workspaceID).Scan(&record.ID, &record.WorkspaceID, &record.ExportID, &record.Path, &record.SHA256, &record.InstalledAt, &record.LogBaselineAt, &record.LogPath, &record.LogOffset, &record.Active)
	return record, err
}

func (s *Store) UpdateTestLogBaseline(ctx context.Context, id, logPath string, offset int64, at string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE test_installs SET log_baseline_at=?,log_path=?,log_offset=? WHERE id=? AND active=1`, at, logPath, offset, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeactivateTestInstall(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE test_installs SET active=0 WHERE id=?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
