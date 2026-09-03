package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type VirgilSessionRecord struct {
	ID           string                `json:"id"`
	WorkspaceID  string                `json:"workspaceId"`
	Profile      string                `json:"profile"`
	OMPSessionID string                `json:"runtimeSessionId"`
	Title        string                `json:"title"`
	OMPTitle     string                `json:"runtimeTitle"`
	UserTitle    string                `json:"userTitle"`
	Status       string                `json:"status"`
	LastError    string                `json:"lastError"`
	TabOrder     int                   `json:"tabOrder"`
	CreatedAt    string                `json:"createdAt"`
	UpdatedAt    string                `json:"updatedAt"`
	Runs         []AgentRunRecord      `json:"runs"`
	Summary      *VirgilSessionSummary `json:"summary,omitempty"`
}

type AgentRunRecord struct {
	ID          string `json:"id"`
	SessionID   string `json:"sessionId"`
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

const legacyVirgilSessionPrefix = "legacy-"

func legacyVirgilSessionID(workspaceID string) string {
	return legacyVirgilSessionPrefix + strings.TrimSpace(workspaceID)
}
func validateVirgilSessionTitle(value string, required bool) (string, error) {
	title := strings.Join(strings.Fields(value), " ")
	if required && title == "" {
		return "", errors.New("Virgil session title is required")
	}
	if len(title) > 120 {
		return "", errors.New("Virgil session title exceeds 120 bytes")
	}
	return title, nil
}

func effectiveVirgilSessionTitle(userTitle, ompTitle string) string {
	if title := strings.TrimSpace(userTitle); title != "" {
		return title
	}
	if title := strings.TrimSpace(ompTitle); title != "" {
		return title
	}
	return "Virgil session"
}
func normalizeVirgilAIProfile(value string) string {
	switch profile := strings.ToLower(strings.TrimSpace(value)); profile {
	case "omp", "codex":
		return "chatgpt"
	default:
		return profile
	}
}

func normalizeVirgilSessionRecord(record VirgilSessionRecord) VirgilSessionRecord {
	record.ID = strings.TrimSpace(record.ID)
	record.WorkspaceID = strings.TrimSpace(record.WorkspaceID)
	record.Profile = normalizeVirgilAIProfile(record.Profile)
	record.OMPSessionID = strings.TrimSpace(record.OMPSessionID)
	record.OMPTitle = strings.TrimSpace(record.OMPTitle)
	record.UserTitle = strings.TrimSpace(record.UserTitle)
	record.Title = effectiveVirgilSessionTitle(record.UserTitle, record.OMPTitle)
	if record.Status == "" {
		record.Status = "idle"
	}
	if record.CreatedAt == "" {
		record.CreatedAt = nowUTC()
	}
	if record.UpdatedAt == "" {
		record.UpdatedAt = record.CreatedAt
	}
	if record.Runs == nil {
		record.Runs = []AgentRunRecord{}
	}
	return record
}

func scanAgentRun(scanner workspaceScanner) (AgentRunRecord, error) {
	var record AgentRunRecord
	err := scanner.Scan(
		&record.ID, &record.SessionID, &record.WorkspaceID, &record.Prompt,
		&record.Status, &record.StartedAt, &record.FinishedAt, &record.FinalText, &record.Error,
	)
	return record, err
}

func scanVirgilSession(scanner workspaceScanner) (VirgilSessionRecord, error) {
	var record VirgilSessionRecord
	err := scanner.Scan(
		&record.ID, &record.WorkspaceID, &record.Profile, &record.OMPSessionID, &record.Title,
		&record.OMPTitle, &record.UserTitle, &record.Status, &record.LastError,
		&record.TabOrder, &record.CreatedAt, &record.UpdatedAt,
	)
	record.Profile = normalizeVirgilAIProfile(record.Profile)
	record.Title = effectiveVirgilSessionTitle(record.UserTitle, record.OMPTitle)
	record.Runs = []AgentRunRecord{}
	return record, err
}

func (s *Store) CreateVirgilSession(ctx context.Context, record VirgilSessionRecord) error {
	userTitle, err := validateVirgilSessionTitle(record.UserTitle, false)
	if err != nil {
		return err
	}
	record.UserTitle = userTitle
	record = normalizeVirgilSessionRecord(record)
	if record.ID == "" || record.WorkspaceID == "" {
		return errors.New("Virgil session and workspace IDs are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertVirgilSessionTx(ctx, tx, record); err != nil {
		return err
	}
	return tx.Commit()
}

func insertVirgilSessionTx(ctx context.Context, tx *sql.Tx, record VirgilSessionRecord) error {
	record = normalizeVirgilSessionRecord(record)
	_, err := tx.ExecContext(ctx, `INSERT INTO virgil_sessions(
		id,workspace_id,profile,omp_session_id,title,omp_title,user_title,status,last_error,tab_order,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		record.ID, record.WorkspaceID, record.Profile, record.OMPSessionID, record.Title, record.OMPTitle,
		record.UserTitle, record.Status, record.LastError, record.TabOrder, record.CreatedAt, record.UpdatedAt)
	return err
}

func (s *Store) CreateVirgilSessionAndRun(ctx context.Context, session VirgilSessionRecord, run AgentRunRecord) error {
	userTitle, err := validateVirgilSessionTitle(session.UserTitle, false)
	if err != nil {
		return err
	}
	session.UserTitle = userTitle
	session = normalizeVirgilSessionRecord(session)
	if session.ID == "" || session.WorkspaceID == "" {
		return errors.New("Virgil session and workspace IDs are required")
	}
	if strings.TrimSpace(run.ID) == "" {
		return errors.New("agent run ID is required")
	}
	run.SessionID = session.ID
	run.WorkspaceID = session.WorkspaceID
	if run.Status == "" {
		run.Status = "running"
	}
	if run.StartedAt == "" {
		run.StartedAt = nowUTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(tab_order)+1,0) FROM virgil_sessions WHERE workspace_id=?`, session.WorkspaceID).Scan(&session.TabOrder); err != nil {
		return err
	}
	if err := insertVirgilSessionTx(ctx, tx, session); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_runs(
		id,session_id,workspace_id,prompt,status,started_at,finished_at,final_text,error
	) VALUES(?,?,?,?,?,?,?,?,?)`,
		run.ID, run.SessionID, run.WorkspaceID, run.Prompt, run.Status, run.StartedAt,
		run.FinishedAt, run.FinalText, run.Error); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetVirgilSession(ctx context.Context, sessionID string) (VirgilSessionRecord, error) {
	record, err := scanVirgilSession(s.db.QueryRowContext(ctx, `SELECT
		id,workspace_id,profile,omp_session_id,title,omp_title,user_title,status,last_error,tab_order,created_at,updated_at
		FROM virgil_sessions WHERE id=?`, strings.TrimSpace(sessionID)))
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	record.Runs, err = s.ListAgentRunsBySession(ctx, record.ID)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	record.Summary, err = s.virgilSessionSummary(ctx, record)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	return record, nil
}

func (s *Store) ListVirgilSessions(ctx context.Context, workspaceID string) ([]VirgilSessionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		id,workspace_id,profile,omp_session_id,title,omp_title,user_title,status,last_error,tab_order,created_at,updated_at
		FROM virgil_sessions WHERE workspace_id=? ORDER BY tab_order ASC,created_at ASC,id ASC`, strings.TrimSpace(workspaceID))
	if err != nil {
		return nil, err
	}
	records := []VirgilSessionRecord{}
	for rows.Next() {
		record, scanErr := scanVirgilSession(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range records {
		records[index].Runs, err = s.ListAgentRunsBySession(ctx, records[index].ID)
		if err != nil {
			return nil, err
		}
		records[index].Summary, err = s.virgilSessionSummary(ctx, records[index])
		if err != nil {
			return nil, err
		}
	}
	return records, nil
}

func (s *Store) ListAgentRunsBySession(ctx context.Context, sessionID string) ([]AgentRunRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		id,session_id,workspace_id,prompt,status,started_at,finished_at,final_text,error
		FROM agent_runs WHERE session_id=? ORDER BY started_at ASC,id ASC`, strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AgentRunRecord{}
	for rows.Next() {
		record, scanErr := scanAgentRun(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) RenameVirgilSession(ctx context.Context, sessionID, title string) (VirgilSessionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	title, err := validateVirgilSessionTitle(title, true)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE virgil_sessions SET
		user_title=?,title=?,
		updated_at=? WHERE id=?`, title, title, nowUTC(), sessionID)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return VirgilSessionRecord{}, err
	} else if affected == 0 {
		return VirgilSessionRecord{}, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return VirgilSessionRecord{}, err
	}
	return s.GetVirgilSession(ctx, sessionID)
}

func (s *Store) SetVirgilSessionOMPState(ctx context.Context, sessionID, ompSessionID, ompTitle string) error {
	sessionID = strings.TrimSpace(sessionID)
	ompSessionID = strings.TrimSpace(ompSessionID)
	var err error
	ompTitle, err = validateVirgilSessionTitle(ompTitle, false)
	if err != nil {
		return err
	}
	if ompSessionID == "" {
		return errors.New("Virgil runtime session ID is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE virgil_sessions SET
		omp_session_id=?,omp_title=CASE WHEN TRIM(?)<>'' THEN TRIM(?) ELSE omp_title END,
		title=CASE WHEN TRIM(user_title)<>'' THEN TRIM(user_title)
			WHEN TRIM(?)<>'' THEN TRIM(?) ELSE 'Virgil session' END,
		updated_at=? WHERE id=?`,
		ompSessionID, ompTitle, ompTitle, ompTitle, ompTitle, nowUTC(), sessionID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) SetVirgilSessionProfile(ctx context.Context, sessionID, profile string) error {
	sessionID = strings.TrimSpace(sessionID)
	profile = normalizeVirgilAIProfile(profile)
	if profile == "" {
		return errors.New("Virgil AI profile is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE virgil_sessions SET profile=?,updated_at=? WHERE id=?`, profile, nowUTC(), sessionID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) SetVirgilSessionOMPTitle(ctx context.Context, sessionID, ompTitle string) error {
	sessionID = strings.TrimSpace(sessionID)
	var err error
	ompTitle, err = validateVirgilSessionTitle(ompTitle, true)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE virgil_sessions SET
		omp_title=?,title=CASE WHEN TRIM(user_title)<>'' THEN TRIM(user_title) ELSE TRIM(?) END,
		updated_at=? WHERE id=?`, ompTitle, ompTitle, nowUTC(), sessionID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SetVirgilSessionStatus(ctx context.Context, sessionID, status, lastError string) error {
	sessionID = strings.TrimSpace(sessionID)
	status = strings.TrimSpace(status)
	if status == "" {
		return errors.New("Virgil session status is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE virgil_sessions SET status=?,last_error=?,updated_at=? WHERE id=?`, status, lastError, nowUTC(), sessionID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ReserveVirgilSessionRun(ctx context.Context, sessionID string, record AgentRunRecord) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("Virgil session ID is required")
	}
	if record.ID == "" {
		return errors.New("agent run ID is required")
	}
	if record.Status == "" {
		record.Status = "running"
	}
	if record.StartedAt == "" {
		record.StartedAt = nowUTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var workspaceID, ompSessionID, status string
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id,omp_session_id,status FROM virgil_sessions WHERE id=?`, sessionID).Scan(&workspaceID, &ompSessionID, &status); err != nil {
		return err
	}
	if status != "idle" {
		return fmt.Errorf("Virgil session is %s, not idle", status)
	}
	if strings.TrimSpace(ompSessionID) == "" {
		return errors.New("Virgil session has no resumable runtime session ID")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_runs(
		id,session_id,workspace_id,prompt,status,started_at,finished_at,final_text,error
	) VALUES(?,?,?,?,?,?,?,?,?)`,
		record.ID, sessionID, workspaceID, record.Prompt, record.Status, record.StartedAt,
		record.FinishedAt, record.FinalText, record.Error); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE virgil_sessions SET status='starting',last_error='',updated_at=? WHERE id=?`, nowUTC(), sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) BeginVirgilSessionResume(ctx context.Context, sessionID string) (VirgilSessionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	defer tx.Rollback()
	var status, ompSessionID string
	if err := tx.QueryRowContext(ctx, `SELECT status,omp_session_id FROM virgil_sessions WHERE id=?`, sessionID).Scan(&status, &ompSessionID); err != nil {
		return VirgilSessionRecord{}, err
	}
	if status != "paused" && status != "error" && status != "idle" {
		return VirgilSessionRecord{}, fmt.Errorf("Virgil session is %s and cannot be resumed", status)
	}
	if strings.TrimSpace(ompSessionID) == "" {
		return VirgilSessionRecord{}, errors.New("Virgil session has no resumable runtime session ID")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE virgil_sessions SET status='starting',last_error='',updated_at=? WHERE id=?`, nowUTC(), sessionID); err != nil {
		return VirgilSessionRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return VirgilSessionRecord{}, err
	}
	return s.GetVirgilSession(ctx, sessionID)
}

func (s *Store) DeleteVirgilSession(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM virgil_sessions WHERE id=?`, sessionID).Scan(&exists); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_events WHERE run_id IN (SELECT id FROM agent_runs WHERE session_id=?)`, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_runs WHERE session_id=?`, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM virgil_sessions WHERE id=?`, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateAgentRun(ctx context.Context, record AgentRunRecord) error {
	if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.WorkspaceID) == "" {
		return errors.New("agent run and workspace IDs are required")
	}
	if record.Status == "" {
		record.Status = "running"
	}
	if record.StartedAt == "" {
		record.StartedAt = nowUTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	record.SessionID = strings.TrimSpace(record.SessionID)
	if record.SessionID == "" {
		record.SessionID = legacyVirgilSessionID(record.WorkspaceID)
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO virgil_sessions(
			id,workspace_id,profile,omp_session_id,title,omp_title,user_title,status,last_error,tab_order,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			record.SessionID, record.WorkspaceID, "", "", "Virgil session", "", "", "paused", "", 0,
			record.StartedAt, record.StartedAt); err != nil {
			return err
		}
	}
	var sessionWorkspaceID string
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM virgil_sessions WHERE id=?`, record.SessionID).Scan(&sessionWorkspaceID); err != nil {
		return err
	}
	if sessionWorkspaceID != record.WorkspaceID {
		return fmt.Errorf("agent run workspace %q does not match Virgil session workspace %q", record.WorkspaceID, sessionWorkspaceID)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_runs(
		id,session_id,workspace_id,prompt,status,started_at,finished_at,final_text,error
	) VALUES(?,?,?,?,?,?,?,?,?)`,
		record.ID, record.SessionID, record.WorkspaceID, record.Prompt, record.Status,
		record.StartedAt, record.FinishedAt, record.FinalText, record.Error); err != nil {
		return err
	}
	if record.Status == "running" {
		if _, err := tx.ExecContext(ctx, `UPDATE virgil_sessions SET status='running',updated_at=? WHERE id=?`, nowUTC(), record.SessionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) FinishAgentRun(ctx context.Context, id, status, finalText, runError string) error {
	now := nowUTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status=?,finished_at=?,final_text=?,error=? WHERE id=?`, status, now, finalText, runError, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	var sessionID string
	if err := tx.QueryRowContext(ctx, `SELECT session_id FROM agent_runs WHERE id=?`, id).Scan(&sessionID); err != nil {
		return err
	}
	sessionStatus := "error"
	switch status {
	case "complete":
		sessionStatus = "idle"
	case "cancelled":
		sessionStatus = "paused"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE virgil_sessions SET status=?,last_error=?,updated_at=?
		WHERE id=? AND NOT EXISTS(SELECT 1 FROM agent_runs WHERE session_id=? AND status='running')`,
		sessionStatus, runError, now, sessionID, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RecoverInterruptedAgentRuns(ctx context.Context) error {
	const message = "Application exited before the Virgil run finished"
	now := nowUTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='failed',finished_at=?,error=? WHERE status='running'`, now, message); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE virgil_sessions SET
		status=CASE WHEN TRIM(omp_session_id)<>'' THEN 'paused' ELSE 'error' END,
		last_error=?,updated_at=?
		WHERE status IN ('running','starting')`, message, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE virgil_sessions SET status='paused',last_error='',updated_at=?
		WHERE status='idle' AND TRIM(omp_session_id)<>''`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AppendAgentEvent(ctx context.Context, runID, eventType, message string, data map[string]any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_events(run_id,at,type,message,data_json) VALUES(?,?,?,?,?)`,
		strings.TrimSpace(runID), nowUTC(), eventType, message, string(encoded))
	return err
}

func (s *Store) ListAgentEvents(ctx context.Context, runID string, limit int) ([]AgentEventRecord, error) {
	if limit <= 0 || limit > 2000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,at,type,message,data_json FROM (
		SELECT id,run_id,at,type,message,data_json FROM agent_events
		WHERE run_id=? ORDER BY id DESC LIMIT ?
	) ORDER BY id ASC`, strings.TrimSpace(runID), limit)
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
