package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type LibraryItem struct {
	EntityID                string          `json:"entityId"`
	ArtifactID              string          `json:"artifactId"`
	LinkID                  string          `json:"linkId"`
	FolderID                string          `json:"folderId"`
	DisplayName             string          `json:"displayName"`
	Kind                    modkit.Kind     `json:"kind"`
	ArchivePath             string          `json:"archivePath"`
	RootPath                string          `json:"rootPath"`
	Linked                  bool            `json:"linked"`
	SizeBytes               int64           `json:"sizeBytes"`
	ModifiedAt              string          `json:"modifiedAt"`
	LastSeenAt              string          `json:"lastSeenAt"`
	Fingerprint             string          `json:"fingerprint"`
	SHA256                  string          `json:"sha256"`
	ThumbnailURL            string          `json:"thumbnailUrl"`
	MemberCount             int             `json:"memberCount"`
	NamespaceCount          int             `json:"namespaceCount"`
	VariantCount            int             `json:"variantCount"`
	IssueCount              int             `json:"issueCount"`
	HealthStatus            string          `json:"healthStatus"`
	HealthLabel             string          `json:"healthLabel"`
	LastSecurityScanAt      string          `json:"lastSecurityScanAt"`
	LastSecurityScanVerdict string          `json:"lastSecurityScanVerdict"`
	LastSecurityScanSHA256  string          `json:"lastSecurityScanSha256"`
	SecurityScanChanged     bool            `json:"securityScanChanged"`
	Manifest                modkit.Manifest `json:"manifest"`
	Tags                    []ModTag        `json:"tags"`
}

type EventRecord struct {
	ID       int64          `json:"id"`
	At       string         `json:"at"`
	EntityID string         `json:"entityId"`
	Type     string         `json:"type"`
	Data     map[string]any `json:"data"`
}

type EntityDetail struct {
	Item    LibraryItem   `json:"item"`
	Links   []ArchiveLink `json:"links"`
	History []EventRecord `json:"history"`
}

type ArchiveLink struct {
	ID           string `json:"id"`
	ArtifactID   string `json:"artifactId"`
	Path         string `json:"path"`
	RootPath     string `json:"rootPath"`
	Linked       bool   `json:"linked"`
	SizeBytes    int64  `json:"sizeBytes"`
	ModifiedAt   string `json:"modifiedAt"`
	DiscoveredAt string `json:"discoveredAt"`
	LastSeenAt   string `json:"lastSeenAt"`
}

type WorkspaceRecord struct {
	ID               string      `json:"id"`
	EntityID         string      `json:"entityId"`
	ArtifactID       string      `json:"artifactId"`
	Root             string      `json:"root"`
	FilesRoot        string      `json:"filesRoot"`
	SourcePath       string      `json:"sourcePath"`
	SourceSHA256     string      `json:"sourceSha256"`
	CreatedAt        string      `json:"createdAt"`
	UpdatedAt        string      `json:"updatedAt"`
	Status           string      `json:"status"`
	LastValidation   string      `json:"lastValidation"`
	DisplayName      string      `json:"displayName"`
	Kind             modkit.Kind `json:"kind"`
	VirgilConfigured bool        `json:"virgilConfigured"`
	VirgilEnabled    bool        `json:"virgilEnabled"`
	AgentRunID       string      `json:"agentRunId"`
	AgentGoal        string      `json:"agentGoal"`
	AgentStatus      string      `json:"agentStatus"`
	AgentProcess     string      `json:"agentProcess"`
	AgentUpdatedAt   string      `json:"agentUpdatedAt"`
}

type ExportRecord struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
	ArtifactID  string `json:"artifactId"`
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	Kind        string `json:"kind"`
	CreatedAt   string `json:"createdAt"`
}

type Dashboard struct {
	Linked           int           `json:"linked"`
	Unlinked         int           `json:"unlinked"`
	Vehicles         int           `json:"vehicles"`
	Maps             int           `json:"maps"`
	UIAndScripts     int           `json:"uiAndScripts"`
	Workspaces       int           `json:"workspaces"`
	Entities         int           `json:"entities"`
	Artifacts        int           `json:"artifacts"`
	CachedAssets     int           `json:"cachedAssets"`
	CachedAssetBytes int64         `json:"cachedAssetBytes"`
	LastScanAt       string        `json:"lastScanAt"`
	LastScanStatus   string        `json:"lastScanStatus"`
	LastScanFound    int           `json:"lastScanFound"`
	LastScanAnalyzed int           `json:"lastScanAnalyzed"`
	LastScanFailed   int           `json:"lastScanFailed"`
	LatestEvents     []EventRecord `json:"latestEvents"`
	DatabaseBytes    int64         `json:"databaseBytes"`
}

type AssetRecord struct {
	SHA256    string `json:"sha256"`
	Path      string `json:"path"`
	MIME      string `json:"mime"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	SizeBytes int64  `json:"sizeBytes"`
}

func OpenStore(filename string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.RecoverInterruptedAgentRuns(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS schema_meta (version INTEGER NOT NULL)`,
		`INSERT INTO schema_meta(version) SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM schema_meta)`,
		`CREATE TABLE IF NOT EXISTS entities (
			id TEXT PRIMARY KEY, display_name TEXT NOT NULL, kind TEXT NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS artifacts (
			id TEXT PRIMARY KEY, central_fingerprint TEXT NOT NULL UNIQUE, sha256 TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL, manifest_json TEXT NOT NULL, analyzer_version TEXT NOT NULL,
			analyzed_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS scans (
			id TEXT PRIMARY KEY, started_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL, roots_json TEXT NOT NULL, discovered INTEGER NOT NULL DEFAULT 0,
			analyzed INTEGER NOT NULL DEFAULT 0, failed INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS archive_links (
			id TEXT PRIMARY KEY, entity_id TEXT NOT NULL REFERENCES entities(id), artifact_id TEXT NOT NULL REFERENCES artifacts(id),
			path TEXT NOT NULL COLLATE NOCASE UNIQUE, root_path TEXT NOT NULL COLLATE NOCASE,
			active INTEGER NOT NULL, size_bytes INTEGER NOT NULL, modified_at TEXT NOT NULL,
			discovered_at TEXT NOT NULL, last_seen_at TEXT NOT NULL, last_scan_id TEXT NOT NULL,
			basename_key TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS archive_links_entity_idx ON archive_links(entity_id, active, last_seen_at)`,
		`CREATE INDEX IF NOT EXISTS archive_links_scan_idx ON archive_links(root_path, last_scan_id)`,
		`CREATE TABLE IF NOT EXISTS assets (
			sha256 TEXT PRIMARY KEY, path TEXT NOT NULL, mime TEXT NOT NULL, width INTEGER NOT NULL,
			height INTEGER NOT NULL, size_bytes INTEGER NOT NULL, created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS entity_assets (
			entity_id TEXT NOT NULL REFERENCES entities(id), asset_sha256 TEXT NOT NULL REFERENCES assets(sha256),
			role TEXT NOT NULL, ordinal INTEGER NOT NULL, PRIMARY KEY(entity_id, role, ordinal)
		)`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, entity_id TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL, data_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS events_entity_idx ON events(entity_id, id DESC)`,
		`CREATE TABLE IF NOT EXISTS workspaces (
			id TEXT PRIMARY KEY, entity_id TEXT NOT NULL REFERENCES entities(id), artifact_id TEXT NOT NULL REFERENCES artifacts(id),
			root TEXT NOT NULL, files_root TEXT NOT NULL, source_path TEXT NOT NULL, source_sha256 TEXT NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL, status TEXT NOT NULL,
			last_validation_json TEXT NOT NULL DEFAULT '',
			virgil_configured INTEGER NOT NULL DEFAULT 0,
			virgil_enabled INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS workspaces_entity_idx ON workspaces(entity_id, updated_at DESC)`,
		`CREATE TABLE IF NOT EXISTS virgil_sessions (
			id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			profile TEXT NOT NULL DEFAULT '', omp_session_id TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT 'Virgil session',
			omp_title TEXT NOT NULL DEFAULT '', user_title TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'idle', last_error TEXT NOT NULL DEFAULT '',
			tab_order INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS virgil_sessions_workspace_idx ON virgil_sessions(workspace_id, tab_order, created_at, id)`,
		`CREATE TABLE IF NOT EXISTS exports (
			id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), artifact_id TEXT NOT NULL,
			path TEXT NOT NULL, sha256 TEXT NOT NULL, kind TEXT NOT NULL, created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS agent_runs (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL DEFAULT '', workspace_id TEXT NOT NULL REFERENCES workspaces(id), prompt TEXT NOT NULL,
			status TEXT NOT NULL, started_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT '',
			final_text TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS agent_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL REFERENCES agent_runs(id),
			at TEXT NOT NULL, type TEXT NOT NULL, message TEXT NOT NULL, data_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS agent_events_run_idx ON agent_events(run_id, id)`,
		`CREATE TABLE IF NOT EXISTS mod_audits (
			id TEXT PRIMARY KEY, entity_id TEXT NOT NULL REFERENCES entities(id), artifact_id TEXT NOT NULL REFERENCES artifacts(id),
			status TEXT NOT NULL, stage TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			deterministic_json TEXT NOT NULL DEFAULT '{}', attack_surface_json TEXT NOT NULL DEFAULT '{}',
			pre_scan_json TEXT NOT NULL DEFAULT '{}', final_json TEXT NOT NULL DEFAULT '{}',
			follow_up_json TEXT NOT NULL DEFAULT '[]', error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS mod_audits_entity_idx ON mod_audits(entity_id, artifact_id, updated_at DESC)`,
		`CREATE TABLE IF NOT EXISTS mod_audit_files (
			audit_id TEXT NOT NULL REFERENCES mod_audits(id) ON DELETE CASCADE, path TEXT NOT NULL,
			fingerprint TEXT NOT NULL, size_bytes INTEGER NOT NULL, entrypoint_type TEXT NOT NULL,
			signals_json TEXT NOT NULL, excerpt TEXT NOT NULL, pre_scan_json TEXT NOT NULL DEFAULT '{}',
			PRIMARY KEY(audit_id, path)
		)`,
		`CREATE TABLE IF NOT EXISTS virus_scans (
			id TEXT PRIMARY KEY, entity_id TEXT NOT NULL REFERENCES entities(id), artifact_id TEXT NOT NULL REFERENCES artifacts(id),
			file_sha256 TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL, status TEXT NOT NULL, current_stage TEXT NOT NULL, verdict TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL, error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS virus_scans_entity_idx ON virus_scans(entity_id, artifact_id, updated_at DESC)`,
		`CREATE TABLE IF NOT EXISTS virus_scan_stages (
			id TEXT PRIMARY KEY, scan_id TEXT NOT NULL REFERENCES virus_scans(id) ON DELETE CASCADE,
			entity_id TEXT NOT NULL REFERENCES entities(id), artifact_id TEXT NOT NULL REFERENCES artifacts(id),
			file_sha256 TEXT NOT NULL DEFAULT '',
			stage TEXT NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '',
			parameters_json TEXT NOT NULL DEFAULT '{}', inputs_json TEXT NOT NULL DEFAULT '[]',
			metadata_file TEXT NOT NULL, audit_id TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS virus_scan_stages_lineage_idx ON virus_scan_stages(entity_id, artifact_id, stage, completed_at DESC)`,
		`CREATE TABLE IF NOT EXISTS test_installs (
			id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), export_id TEXT NOT NULL REFERENCES exports(id),
			path TEXT NOT NULL, sha256 TEXT NOT NULL, installed_at TEXT NOT NULL, log_baseline_at TEXT NOT NULL,
			log_path TEXT NOT NULL, log_offset INTEGER NOT NULL, active INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS library_folders (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, parent_id TEXT REFERENCES library_folders(id) ON DELETE CASCADE,
			position INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS library_folder_entities (
			entity_id TEXT PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
			folder_id TEXT NOT NULL REFERENCES library_folders(id) ON DELETE CASCADE,
			position INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS library_folder_entities_folder_idx ON library_folder_entities(folder_id, position)`,
		`CREATE TABLE IF NOT EXISTS mod_tags (
			id TEXT PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE,
			color TEXT NOT NULL DEFAULT '#7a8791', icon TEXT NOT NULL DEFAULT 'tag',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS mod_tag_entities (
			tag_id TEXT NOT NULL REFERENCES mod_tags(id) ON DELETE CASCADE,
			entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			created_at TEXT NOT NULL, PRIMARY KEY(tag_id, entity_id)
		)`,
		`CREATE INDEX IF NOT EXISTS mod_tag_entities_entity_idx ON mod_tag_entities(entity_id, tag_id)`,
		`CREATE TABLE IF NOT EXISTS mod_presets (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS mod_preset_entities (
			preset_id TEXT NOT NULL REFERENCES mod_presets(id) ON DELETE CASCADE,
			entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			position INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(preset_id, entity_id)
		)`,
		`CREATE TABLE IF NOT EXISTS mod_profiles (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, default_preset_id TEXT NOT NULL REFERENCES mod_presets(id),
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS mod_profile_presets (
			profile_id TEXT NOT NULL REFERENCES mod_profiles(id) ON DELETE CASCADE,
			preset_id TEXT NOT NULL REFERENCES mod_presets(id) ON DELETE CASCADE,
			position INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(profile_id, preset_id)
		)`,
		`CREATE TABLE IF NOT EXISTS workspace_drafts (
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			path TEXT NOT NULL, content TEXT NOT NULL, base_sha256 TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL,
			PRIMARY KEY(workspace_id, path)
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("database migration: %w", err)
		}
	}
	if err := s.ensureModTagVisualColumns(ctx); err != nil {
		return fmt.Errorf("migrate mod tag visuals: %w", err)
	}
	if err := s.ensureExampleModTags(ctx); err != nil {
		return fmt.Errorf("seed example tags: %w", err)
	}
	if err := s.ensureVirusScanHashColumns(ctx); err != nil {
		return fmt.Errorf("migrate virus scan hashes: %w", err)
	}
	if err := s.ensureWorkspaceVirgilColumns(ctx); err != nil {
		return fmt.Errorf("migrate workspace virgil preferences: %w", err)
	}
	if err := s.ensureVirgilSessionSchema(ctx); err != nil {
		return fmt.Errorf("migrate Virgil sessions: %w", err)
	}
	if err := s.ensureWorkspaceDraftBaseSHA(ctx); err != nil {
		return fmt.Errorf("migrate workspace draft base hashes: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE schema_meta SET version=3 WHERE version<3`); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	return nil
}
func (s *Store) ensureWorkspaceVirgilColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(workspaces)`)
	if err != nil {
		return err
	}
	hasConfigured, hasEnabled := false, false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "virgil_configured":
			hasConfigured = true
		case "virgil_enabled":
			hasEnabled = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !hasConfigured {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE workspaces ADD COLUMN virgil_configured INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if !hasEnabled {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE workspaces ADD COLUMN virgil_enabled INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) ensureWorkspaceDraftBaseSHA(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(workspace_drafts)`)
	if err != nil {
		return err
	}
	hasBaseSHA := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if strings.EqualFold(strings.TrimSpace(name), "base_sha256") {
			hasBaseSHA = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !hasBaseSHA {
		_, err = s.db.ExecContext(ctx, `ALTER TABLE workspace_drafts ADD COLUMN base_sha256 TEXT NOT NULL DEFAULT ''`)
		return err
	}
	return nil
}

func (s *Store) ensureVirgilSessionSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	hasRunSessionID := false
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(agent_runs)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if strings.EqualFold(strings.TrimSpace(name), "session_id") {
			hasRunSessionID = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	hasProfile := false
	rows, err = tx.QueryContext(ctx, `PRAGMA table_info(virgil_sessions)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if strings.EqualFold(strings.TrimSpace(name), "profile") {
			hasProfile = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !hasProfile {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE virgil_sessions ADD COLUMN profile TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	if !hasRunSessionID {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_runs ADD COLUMN session_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS agent_runs_session_idx ON agent_runs(session_id, started_at, id)`); err != nil {
		return err
	}

	now := nowUTC()
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO virgil_sessions(
		id,workspace_id,profile,omp_session_id,title,omp_title,user_title,status,last_error,tab_order,created_at,updated_at
	)
	SELECT 'legacy-' || ar.workspace_id,ar.workspace_id,'','','Virgil session','','','paused','',0,
		COALESCE(MIN(NULLIF(ar.started_at,'')),?),COALESCE(MAX(NULLIF(ar.finished_at,'')),?)
	FROM agent_runs ar
	WHERE TRIM(COALESCE(ar.session_id,''))=''
	GROUP BY ar.workspace_id`, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO virgil_sessions(
		id,workspace_id,profile,omp_session_id,title,omp_title,user_title,status,last_error,tab_order,created_at,updated_at
	)
	SELECT ar.session_id,ar.workspace_id,'','','Virgil session','','','paused','',0,
		COALESCE(MIN(NULLIF(ar.started_at,'')),?),COALESCE(MAX(NULLIF(ar.finished_at,'')),?)
	FROM agent_runs ar
	LEFT JOIN virgil_sessions vs ON vs.id=ar.session_id
	WHERE TRIM(COALESCE(ar.session_id,''))<>'' AND vs.id IS NULL
	GROUP BY ar.session_id,ar.workspace_id`, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET session_id='legacy-' || workspace_id WHERE TRIM(COALESCE(session_id,''))=''`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE virgil_sessions SET title=CASE
		WHEN TRIM(user_title)<>'' THEN TRIM(user_title)
		WHEN TRIM(omp_title)<>'' THEN TRIM(omp_title)
		ELSE 'Virgil session' END`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) BeginScan(ctx context.Context, roots []string) (string, error) {
	id, err := modkit.NewID()
	if err != nil {
		return "", err
	}
	encoded, _ := json.Marshal(roots)
	_, err = s.db.ExecContext(ctx, `INSERT INTO scans(id, started_at, status, roots_json) VALUES(?, ?, 'running', ?)`, id, nowUTC(), string(encoded))
	return id, err
}

func (s *Store) FinishScan(ctx context.Context, scanID string, roots []string, discovered, analyzed, failed int, scanErr error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	status := "complete"
	if scanErr != nil {
		status = "failed"
	} else {
		for _, root := range roots {
			rows, queryErr := tx.QueryContext(ctx, `SELECT id, entity_id, path FROM archive_links WHERE root_path = ? COLLATE NOCASE AND active = 1 AND last_scan_id <> ?`, root, scanID)
			if queryErr != nil {
				return queryErr
			}
			type missingLink struct{ id, entityID, path string }
			missing := []missingLink{}
			for rows.Next() {
				var link missingLink
				if err := rows.Scan(&link.id, &link.entityID, &link.path); err != nil {
					rows.Close()
					return err
				}
				missing = append(missing, link)
			}
			if err := rows.Close(); err != nil {
				return err
			}
			for _, link := range missing {
				if _, err := tx.ExecContext(ctx, `UPDATE archive_links SET active = 0 WHERE id = ?`, link.id); err != nil {
					return err
				}
				if err := appendEventTx(ctx, tx, link.entityID, "archive_unlinked", map[string]any{"path": link.path}); err != nil {
					return err
				}
			}
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE scans SET finished_at = ?, status = ?, discovered = ?, analyzed = ?, failed = ? WHERE id = ?`, nowUTC(), status, discovered, analyzed, failed, scanID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReuseArchiveAnalysis(ctx context.Context, scanID, root, archivePath string, size int64, modified time.Time) (LibraryItem, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LibraryItem{}, false, err
	}
	defer tx.Rollback()
	var linkID, entityID string
	var wasActive int
	modifiedAt := modified.UTC().Format(time.RFC3339Nano)
	err = tx.QueryRowContext(ctx, `
		SELECT l.id,l.entity_id,l.active
		FROM archive_links l JOIN artifacts a ON a.id=l.artifact_id
		WHERE l.path=? COLLATE NOCASE AND l.size_bytes=? AND l.modified_at=? AND a.analyzer_version=?`,
		archivePath, size, modifiedAt, modkit.AnalyzerVersion,
	).Scan(&linkID, &entityID, &wasActive)
	if errors.Is(err, sql.ErrNoRows) {
		return LibraryItem{}, false, nil
	}
	if err != nil {
		return LibraryItem{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE archive_links SET root_path=?,active=1,last_seen_at=?,last_scan_id=? WHERE id=?`, root, nowUTC(), scanID, linkID); err != nil {
		return LibraryItem{}, false, err
	}
	if wasActive == 0 {
		if err := appendEventTx(ctx, tx, entityID, "archive_relinked", map[string]any{"path": archivePath}); err != nil {
			return LibraryItem{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return LibraryItem{}, false, err
	}
	item, err := s.GetLibraryItem(ctx, entityID)
	return item, err == nil, err
}

func (s *Store) UpsertArchive(ctx context.Context, scanID, root, archivePath string, size int64, modified time.Time, manifest modkit.Manifest, asset *AssetRecord) (LibraryItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LibraryItem{}, err
	}
	defer tx.Rollback()
	now := nowUTC()
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return LibraryItem{}, err
	}
	var artifactID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM artifacts WHERE central_fingerprint = ?`, manifest.CentralFingerprint).Scan(&artifactID)
	if errors.Is(err, sql.ErrNoRows) {
		artifactID, err = modkit.NewID()
		if err != nil {
			return LibraryItem{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO artifacts(id, central_fingerprint, sha256, size_bytes, manifest_json, analyzer_version, analyzed_at) VALUES(?, ?, ?, ?, ?, ?, ?)`, artifactID, manifest.CentralFingerprint, manifest.FullSHA256, size, string(manifestJSON), modkit.AnalyzerVersion, now)
	} else if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE artifacts SET sha256 = CASE WHEN ? <> '' THEN ? ELSE sha256 END, size_bytes = ?, manifest_json = ?, analyzer_version = ?, analyzed_at = ? WHERE id = ?`, manifest.FullSHA256, manifest.FullSHA256, size, string(manifestJSON), modkit.AnalyzerVersion, now, artifactID)
	}
	if err != nil {
		return LibraryItem{}, err
	}
	basenameKey := strings.ToLower(filepath.Base(archivePath)) + "\x00" + manifest.CentralFingerprint
	var linkID, entityID, previousArtifact string
	var wasActive int
	err = tx.QueryRowContext(ctx, `SELECT id, entity_id, artifact_id, active FROM archive_links WHERE path = ? COLLATE NOCASE`, archivePath).Scan(&linkID, &entityID, &previousArtifact, &wasActive)
	newLink := false
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT entity_id FROM archive_links WHERE basename_key = ? ORDER BY active DESC, last_seen_at DESC LIMIT 1`, basenameKey).Scan(&entityID)
		if errors.Is(err, sql.ErrNoRows) {
			entityID, err = modkit.NewID()
			if err != nil {
				return LibraryItem{}, err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO entities(id, display_name, kind, created_at, updated_at) VALUES(?, ?, ?, ?, ?)`, entityID, displayName(manifest, archivePath), manifest.Kind, now, now)
		}
		if err != nil {
			return LibraryItem{}, err
		}
		linkID, err = modkit.NewID()
		if err != nil {
			return LibraryItem{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO archive_links(id, entity_id, artifact_id, path, root_path, active, size_bytes, modified_at, discovered_at, last_seen_at, last_scan_id, basename_key) VALUES(?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)`, linkID, entityID, artifactID, archivePath, root, size, modified.UTC().Format(time.RFC3339Nano), now, now, scanID, basenameKey)
		newLink = true
	} else if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE archive_links SET artifact_id = ?, root_path = ?, active = 1, size_bytes = ?, modified_at = ?, last_seen_at = ?, last_scan_id = ?, basename_key = ? WHERE id = ?`, artifactID, root, size, modified.UTC().Format(time.RFC3339Nano), now, scanID, basenameKey, linkID)
	}
	if err != nil {
		return LibraryItem{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE entities SET display_name = ?, kind = ?, updated_at = ? WHERE id = ?`, displayName(manifest, archivePath), manifest.Kind, now, entityID)
	if err != nil {
		return LibraryItem{}, err
	}
	if asset != nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO assets(sha256, path, mime, width, height, size_bytes, created_at) VALUES(?, ?, ?, ?, ?, ?, ?) ON CONFLICT(sha256) DO UPDATE SET path=excluded.path, mime=excluded.mime, width=excluded.width, height=excluded.height, size_bytes=excluded.size_bytes`, asset.SHA256, asset.Path, asset.MIME, asset.Width, asset.Height, asset.SizeBytes, now)
		if err != nil {
			return LibraryItem{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO entity_assets(entity_id, asset_sha256, role, ordinal) VALUES(?, ?, 'thumbnail', 0) ON CONFLICT(entity_id, role, ordinal) DO UPDATE SET asset_sha256=excluded.asset_sha256`, entityID, asset.SHA256)
		if err != nil {
			return LibraryItem{}, err
		}
	}
	if newLink {
		err = appendEventTx(ctx, tx, entityID, "archive_discovered", map[string]any{"path": archivePath, "artifactId": artifactID})
	} else {
		if previousArtifact != artifactID {
			err = appendEventTx(ctx, tx, entityID, "archive_changed", map[string]any{"path": archivePath, "fromArtifactId": previousArtifact, "artifactId": artifactID})
		}
		if err == nil && wasActive == 0 {
			err = appendEventTx(ctx, tx, entityID, "archive_relinked", map[string]any{"path": archivePath})
		}
	}
	if err != nil {
		return LibraryItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return LibraryItem{}, err
	}
	return s.GetLibraryItem(ctx, entityID)
}

func (s *Store) GetLibraryItem(ctx context.Context, entityID string) (LibraryItem, error) {
	items, err := s.listItems(ctx, entityID)
	if err != nil {
		return LibraryItem{}, err
	}
	if len(items) == 0 {
		return LibraryItem{}, sql.ErrNoRows
	}
	return items[0], nil
}

func (s *Store) ListLibrary(ctx context.Context, health, kind, query, folderID string) ([]LibraryItem, error) {
	items, err := s.listItems(ctx, "")
	if err != nil {
		return nil, err
	}
	collectionNames, err := s.libraryCollectionNames(ctx)
	if err != nil {
		return nil, err
	}
	search := parseLibrarySearchQuery(query)
	health = normalizeLibraryStatus(health)
	filtered := items[:0]
	for _, item := range items {
		if health != "" && item.HealthStatus != health {
			continue
		}
		if kind != "" && kind != "all" && string(item.Kind) != kind {
			continue
		}
		if folderID == "unfiled" && item.FolderID != "" || folderID != "" && folderID != "all" && folderID != "unfiled" && item.FolderID != folderID {
			continue
		}
		if !search.matches(item, collectionNames) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered, nil
}

func (s *Store) listItems(ctx context.Context, entityID string) ([]LibraryItem, error) {
	query := `SELECT e.id, e.display_name, e.kind, COALESCE(lfe.folder_id,''),
		COALESCE(l.id,''), COALESCE(l.artifact_id,''), COALESCE(l.path,''), COALESCE(l.root_path,''),
		COALESCE(l.active,0), COALESCE(l.size_bytes,0), COALESCE(l.modified_at,''), COALESCE(l.last_seen_at,''),
		COALESCE(a.central_fingerprint,''), COALESCE(a.sha256,''), COALESCE(a.manifest_json,'{}'),
		COALESCE(ast.sha256,'')
	FROM entities e
	LEFT JOIN library_folder_entities lfe ON lfe.entity_id=e.id
	LEFT JOIN archive_links l ON l.id = (SELECT l2.id FROM archive_links l2 WHERE l2.entity_id=e.id ORDER BY l2.active DESC, l2.last_seen_at DESC LIMIT 1)
	LEFT JOIN artifacts a ON a.id=l.artifact_id
	LEFT JOIN entity_assets ea ON ea.entity_id=e.id AND ea.role='thumbnail' AND ea.ordinal=0
	LEFT JOIN assets ast ON ast.sha256=ea.asset_sha256`
	args := []any{}
	if entityID != "" {
		query += ` WHERE e.id = ?`
		args = append(args, entityID)
	}
	query += ` ORDER BY l.active DESC, e.updated_at DESC, e.display_name COLLATE NOCASE LIMIT 5000`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []LibraryItem{}
	for rows.Next() {
		var item LibraryItem
		var kind, manifestJSON, assetSHA string
		if err := rows.Scan(&item.EntityID, &item.DisplayName, &kind, &item.FolderID, &item.LinkID, &item.ArtifactID, &item.ArchivePath, &item.RootPath, &item.Linked, &item.SizeBytes, &item.ModifiedAt, &item.LastSeenAt, &item.Fingerprint, &item.SHA256, &manifestJSON, &assetSHA); err != nil {
			return nil, err
		}
		item.Kind = modkit.Kind(kind)
		if err := json.Unmarshal([]byte(manifestJSON), &item.Manifest); err != nil {
			return nil, err
		}
		item.MemberCount = len(item.Manifest.Members)
		item.NamespaceCount = len(item.Manifest.Namespaces)
		item.VariantCount = len(item.Manifest.Variants)
		item.IssueCount = len(item.Manifest.Issues)
		if assetSHA != "" {
			item.ThumbnailURL = "/cache/" + assetSHA
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := s.attachLibraryItemTags(ctx, items); err != nil {
		return nil, err
	}
	if err := s.attachLibraryItemHealth(ctx, items); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) GetEntityDetail(ctx context.Context, entityID string) (EntityDetail, error) {
	item, err := s.GetLibraryItem(ctx, entityID)
	if err != nil {
		return EntityDetail{}, err
	}
	links := []ArchiveLink{}
	rows, err := s.db.QueryContext(ctx, `SELECT id, artifact_id, path, root_path, active, size_bytes, modified_at, discovered_at, last_seen_at FROM archive_links WHERE entity_id=? ORDER BY active DESC,last_seen_at DESC`, entityID)
	if err != nil {
		return EntityDetail{}, err
	}
	for rows.Next() {
		var link ArchiveLink
		if err := rows.Scan(&link.ID, &link.ArtifactID, &link.Path, &link.RootPath, &link.Linked, &link.SizeBytes, &link.ModifiedAt, &link.DiscoveredAt, &link.LastSeenAt); err != nil {
			rows.Close()
			return EntityDetail{}, err
		}
		links = append(links, link)
	}
	if err := rows.Close(); err != nil {
		return EntityDetail{}, err
	}
	history, err := s.ListEvents(ctx, entityID, 100)
	return EntityDetail{Item: item, Links: links, History: history}, err
}

func (s *Store) ListEvents(ctx context.Context, entityID string, limit int) ([]EventRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT id, at, entity_id, type, data_json FROM events`
	args := []any{}
	if entityID != "" {
		query += ` WHERE entity_id=?`
		args = append(args, entityID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []EventRecord{}
	for rows.Next() {
		var event EventRecord
		var data string
		if err := rows.Scan(&event.ID, &event.At, &event.EntityID, &event.Type, &data); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(data), &event.Data)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) Dashboard(ctx context.Context, databasePath string) (Dashboard, error) {
	var result Dashboard
	queries := []struct {
		query string
		dest  *int
	}{
		{`SELECT COUNT(*) FROM archive_links WHERE active=1`, &result.Linked},
		{`SELECT COUNT(*) FROM entities e WHERE NOT EXISTS(SELECT 1 FROM archive_links l WHERE l.entity_id=e.id AND l.active=1)`, &result.Unlinked},
		{`SELECT COUNT(*) FROM entities WHERE kind='vehicle'`, &result.Vehicles},
		{`SELECT COUNT(*) FROM entities WHERE kind='map'`, &result.Maps},
		{`SELECT COUNT(*) FROM entities WHERE kind IN ('ui','script','mixed')`, &result.UIAndScripts},
		{`SELECT COUNT(*) FROM workspaces WHERE status='active'`, &result.Workspaces},
		{`SELECT COUNT(*) FROM entities`, &result.Entities},
		{`SELECT COUNT(*) FROM artifacts`, &result.Artifacts},
		{`SELECT COUNT(*) FROM assets`, &result.CachedAssets},
	}
	for _, query := range queries {
		if err := s.db.QueryRowContext(ctx, query.query).Scan(query.dest); err != nil {
			return result, err
		}
	}
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size_bytes),0) FROM assets`).Scan(&result.CachedAssetBytes)
	_ = s.db.QueryRowContext(ctx, `SELECT finished_at,status,discovered,analyzed,failed FROM scans ORDER BY started_at DESC LIMIT 1`).Scan(&result.LastScanAt, &result.LastScanStatus, &result.LastScanFound, &result.LastScanAnalyzed, &result.LastScanFailed)
	result.LatestEvents, _ = s.ListEvents(ctx, "", 8)
	if info, err := os.Stat(databasePath); err == nil {
		result.DatabaseBytes = info.Size()
	}
	return result, nil
}

func (s *Store) GetAsset(ctx context.Context, sha string) (AssetRecord, error) {
	var asset AssetRecord
	err := s.db.QueryRowContext(ctx, `SELECT sha256,path,mime,width,height,size_bytes FROM assets WHERE sha256=?`, sha).Scan(&asset.SHA256, &asset.Path, &asset.MIME, &asset.Width, &asset.Height, &asset.SizeBytes)
	return asset, err
}

const workspaceQuery = `
	WITH latest_runs AS (
		SELECT ar.id,ar.workspace_id,ar.prompt,ar.status,ar.started_at,ar.finished_at,
			ROW_NUMBER() OVER (
				PARTITION BY ar.workspace_id
				ORDER BY CASE WHEN ar.status='running' THEN 0 ELSE 1 END,
					ar.started_at DESC, ar.id DESC
			) AS run_rank
		FROM agent_runs ar
	),
	latest_events AS (
		SELECT ae.run_id,ae.at,ae.type,ae.message,
			ROW_NUMBER() OVER (PARTITION BY ae.run_id ORDER BY ae.id DESC) AS event_rank
		FROM agent_events ae
	)
	SELECT w.id,w.entity_id,w.artifact_id,w.root,w.files_root,w.source_path,w.source_sha256,
		w.created_at,w.updated_at,w.status,w.last_validation_json,e.display_name,e.kind,
		COALESCE(w.virgil_configured,0),COALESCE(w.virgil_enabled,0),
		COALESCE(lr.id,''),COALESCE(lr.prompt,''),COALESCE(lr.status,'idle'),
		COALESCE(NULLIF(TRIM(le.message),''),NULLIF(le.type,''),''),
		COALESCE(NULLIF(le.at,''),NULLIF(lr.finished_at,''),NULLIF(lr.started_at,''),'')
	FROM workspaces w
	JOIN entities e ON e.id=w.entity_id
	LEFT JOIN latest_runs lr ON lr.workspace_id=w.id AND lr.run_rank=1
	LEFT JOIN latest_events le ON le.run_id=lr.id AND le.event_rank=1`

type workspaceScanner interface {
	Scan(dest ...any) error
}

func scanWorkspaceRecord(scanner workspaceScanner) (WorkspaceRecord, error) {
	var record WorkspaceRecord
	var virgilConfigured, virgilEnabled int
	err := scanner.Scan(
		&record.ID, &record.EntityID, &record.ArtifactID, &record.Root, &record.FilesRoot,
		&record.SourcePath, &record.SourceSHA256, &record.CreatedAt, &record.UpdatedAt,
		&record.Status, &record.LastValidation, &record.DisplayName, &record.Kind,
		&virgilConfigured, &virgilEnabled, &record.AgentRunID, &record.AgentGoal,
		&record.AgentStatus, &record.AgentProcess, &record.AgentUpdatedAt,
	)
	record.VirgilConfigured = virgilConfigured != 0
	record.VirgilEnabled = virgilEnabled != 0
	return record, err
}

func (s *Store) SaveWorkspace(ctx context.Context, manifest modkit.WorkspaceManifest, root, sourcePath string) (WorkspaceRecord, error) {
	now := nowUTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspaces(id,entity_id,artifact_id,root,files_root,source_path,source_sha256,created_at,updated_at,status) VALUES(?,?,?,?,?,?,?,?,?,'active')`, manifest.ID, manifest.EntityID, manifest.ArtifactID, root, filepath.Join(root, "files"), sourcePath, manifest.SourceFingerprint, manifest.CreatedAt.UTC().Format(time.RFC3339Nano), now)
	if err != nil {
		return WorkspaceRecord{}, err
	}
	_ = s.AppendEvent(ctx, manifest.EntityID, "workspace_created", map[string]any{"workspaceId": manifest.ID})
	return s.GetWorkspace(ctx, manifest.ID)
}

func (s *Store) GetWorkspace(ctx context.Context, id string) (WorkspaceRecord, error) {
	return scanWorkspaceRecord(s.db.QueryRowContext(ctx, workspaceQuery+` WHERE w.id=?`, id))
}

func (s *Store) GetLatestWorkspaceByEntity(ctx context.Context, entityID string) (WorkspaceRecord, error) {
	return scanWorkspaceRecord(s.db.QueryRowContext(ctx, workspaceQuery+` WHERE w.entity_id=? ORDER BY w.updated_at DESC,w.created_at DESC,w.id DESC LIMIT 1`, entityID))
}

func (s *Store) ListWorkspaces(ctx context.Context) ([]WorkspaceRecord, error) {
	rows, err := s.db.QueryContext(ctx, workspaceQuery+`
		WHERE w.id=(
			SELECT candidate.id
			FROM workspaces candidate
			WHERE candidate.entity_id=w.entity_id
			ORDER BY candidate.updated_at DESC,candidate.created_at DESC,candidate.id DESC
			LIMIT 1
		)
		ORDER BY w.updated_at DESC,w.created_at DESC,w.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []WorkspaceRecord{}
	for rows.Next() {
		record, err := scanWorkspaceRecord(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) TouchWorkspace(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workspaces SET updated_at=? WHERE id=?`, nowUTC(), id)
	return err
}

func (s *Store) SetWorkspaceVirgil(ctx context.Context, id string, enabled bool) error {
	enabledValue := 0
	if enabled {
		enabledValue = 1
	}
	result, err := s.db.ExecContext(ctx, `UPDATE workspaces SET virgil_configured=1,virgil_enabled=? WHERE id=?`, enabledValue, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SetWorkspaceValidation(ctx context.Context, id string, result modkit.ValidationResult) error {
	encoded, _ := json.Marshal(result)
	_, err := s.db.ExecContext(ctx, `UPDATE workspaces SET updated_at=?, last_validation_json=? WHERE id=?`, nowUTC(), string(encoded), id)
	return err
}

func (s *Store) AddExport(ctx context.Context, record ExportRecord, entityID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO exports(id,workspace_id,artifact_id,path,sha256,kind,created_at) VALUES(?,?,?,?,?,?,?)`, record.ID, record.WorkspaceID, record.ArtifactID, record.Path, record.SHA256, record.Kind, record.CreatedAt)
	if err == nil {
		err = s.AppendEvent(ctx, entityID, "workspace_exported", map[string]any{"workspaceId": record.WorkspaceID, "path": record.Path, "sha256": record.SHA256, "kind": record.Kind})
	}
	return err
}

func (s *Store) ListExports(ctx context.Context, workspaceID string) ([]ExportRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,workspace_id,artifact_id,path,sha256,kind,created_at FROM exports WHERE workspace_id=? ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ExportRecord{}
	for rows.Next() {
		var record ExportRecord
		if err := rows.Scan(&record.ID, &record.WorkspaceID, &record.ArtifactID, &record.Path, &record.SHA256, &record.Kind, &record.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) AppendEvent(ctx context.Context, entityID, eventType string, data map[string]any) error {
	encoded, _ := json.Marshal(data)
	_, err := s.db.ExecContext(ctx, `INSERT INTO events(at,entity_id,type,data_json) VALUES(?,?,?,?)`, nowUTC(), entityID, eventType, string(encoded))
	return err
}

func appendEventTx(ctx context.Context, tx *sql.Tx, entityID, eventType string, data map[string]any) error {
	encoded, _ := json.Marshal(data)
	_, err := tx.ExecContext(ctx, `INSERT INTO events(at,entity_id,type,data_json) VALUES(?,?,?,?)`, nowUTC(), entityID, eventType, string(encoded))
	return err
}

func displayName(manifest modkit.Manifest, archivePath string) string {
	if strings.TrimSpace(manifest.Title) != "" {
		return strings.TrimSpace(manifest.Title)
	}
	for _, key := range []string{"name", "title", "Name", "Title"} {
		for _, document := range manifest.MetadataDocuments {
			if value, ok := document.Data[key]; ok {
				if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}
	name := filepath.Base(archivePath)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	return strings.ReplaceAll(name, "_", " ")
}

func namespaceValues(namespaces map[string][]string) []string {
	values := []string{}
	for root, entries := range namespaces {
		values = append(values, root)
		values = append(values, entries...)
	}
	return values
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }
