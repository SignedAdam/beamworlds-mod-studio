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
	"sync"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
	_ "modernc.org/sqlite"
)

type Store struct {
	db            *sql.DB
	dbPath        string
	writeMu       sync.Mutex
	manifestCache *artifactManifestCache
}

// libraryQueryer is the smallest common read surface implemented by *sql.DB
// and *sql.Tx. Keeping hydration and search on this interface prevents a
// logical library read from accidentally escaping its transaction snapshot.
type libraryQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type LibraryItem struct {
	EntityID                string          `json:"entityId"`
	Revision                string          `json:"revision"`
	ArchivedAt              string          `json:"archivedAt"`
	ArtifactID              string          `json:"artifactId"`
	LinkID                  string          `json:"linkId"`
	CollectionIDs           []string        `json:"collectionIds"`
	DisplayName             string          `json:"displayName"`
	Kind                    modkit.Kind     `json:"kind"`
	SourceID                string          `json:"sourceId"`
	Source                  string          `json:"source"`
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
	Item         LibraryItem   `json:"item"`
	Links        []ArchiveLink `json:"links"`
	History      []EventRecord `json:"history"`
	HistoryTotal int           `json:"historyTotal"`
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
	Linked               int           `json:"linked"`
	Unlinked             int           `json:"unlinked"`
	Vehicles             int           `json:"vehicles"`
	Maps                 int           `json:"maps"`
	UIAndScripts         int           `json:"uiAndScripts"`
	Workspaces           int           `json:"workspaces"`
	Entities             int           `json:"entities"`
	Artifacts            int           `json:"artifacts"`
	CachedAssets         int           `json:"cachedAssets"`
	CachedAssetBytes     int64         `json:"cachedAssetBytes"`
	LastScanAt           string        `json:"lastScanAt"`
	LastSuccessfulScanAt string        `json:"lastSuccessfulScanAt"`
	LastScanStatus       string        `json:"lastScanStatus"`
	LastScanFound        int           `json:"lastScanFound"`
	LastScanAnalyzed     int           `json:"lastScanAnalyzed"`
	LastScanFailed       int           `json:"lastScanFailed"`
	LatestEvents         []EventRecord `json:"latestEvents"`
	DatabaseBytes        int64         `json:"databaseBytes"`
}

type AssetRecord struct {
	SHA256    string `json:"sha256"`
	Path      string `json:"path"`
	MIME      string `json:"mime"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	SizeBytes int64  `json:"sizeBytes"`
}

// ScanArchive is the immutable result of inspecting one archive during a
// library scan. Reused entries intentionally carry no manifest: the existing
// artifact row is the source of truth until a changed archive is inspected.
type ScanArchive struct {
	Root        string
	ArchivePath string
	SizeBytes   int64
	Modified    time.Time
	Manifest    modkit.Manifest
	Asset       *AssetRecord
	SourceClass string
	Reused      bool
}

func OpenStore(filename string, legacyCatalogPath ...string) (*Store, error) {
	if filename == "" {
		return nil, errors.New("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return nil, err
	}
	// _pragma parameters are applied by modernc.org/sqlite whenever it opens a
	// physical connection, rather than only to the first connection in the
	// pool. WAL keeps foreground snapshots available while the scan writer
	// commits, and the busy timeout turns transient lock contention into a
	// bounded, explicit error.
	dsn := filename + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Reads must be able to use more than the single writer connection. All
	// in-process writers are serialized with writeMu; SQLite still reports any
	// external writer failure to its caller.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	store := &Store{db: db, dbPath: filename, manifestCache: newArtifactManifestCache(2048)}
	fail := func(err error) (*Store, error) {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrate(context.Background()); err != nil {
		return fail(err)
	}
	if err := store.recoverInterruptedScans(context.Background()); err != nil {
		return fail(fmt.Errorf("recover interrupted scans: %w", err))
	}
	if err := store.verifyIntegrity(context.Background()); err != nil {
		return fail(err)
	}
	if len(legacyCatalogPath) > 0 {
		if path := strings.TrimSpace(legacyCatalogPath[0]); path != "" {
			if err := store.importLegacyCatalogOnce(context.Background(), path); err != nil {
				return fail(err)
			}
			if err := store.verifyIntegrity(context.Background()); err != nil {
				return fail(err)
			}
		}
	}
	// Older scans unlinked removed archives without disabling their saved
	// selections. Repair those memberships before exposing the library.
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return fail(err)
	}
	if err := store.disableMissingCollectionModsTx(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	if err := store.RecoverInterruptedAgentRuns(context.Background()); err != nil {
		return fail(err)
	}
	return store, nil
}

func (s *Store) Close() error {
	err := s.db.Close()
	s.manifestCache.clear()
	return err
}

func (s *Store) migrate(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.migrateVersioned(ctx)
}

// recoverInterruptedScans turns scans left in the running state by a process
// crash into an explicit recovery state. It deliberately does not touch
// archive links: an incomplete batch is rolled back by SQLite.
func (s *Store) recoverInterruptedScans(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE scans
		SET finished_at=CASE WHEN finished_at='' THEN ? ELSE finished_at END,
			status='interrupted'
		WHERE status='running'`, nowUTC()); err != nil {
		return err
	}
	return tx.Commit()
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
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	id, err := modkit.NewID()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(roots)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO scans(id, started_at, status, roots_json, error)
		VALUES(?, ?, 'running', ?, '')`, id, nowUTC(), string(encoded))
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) FinishScan(ctx context.Context, scanID string, roots []string, discovered, analyzed, failed int, scanErr error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	status := "complete"
	scanError := ""
	if scanErr != nil {
		status = "failed"
		scanError = scanErr.Error()
		if errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) {
			status = "interrupted"
		}
	}
	removedEntities := map[string]struct{}{}
	if scanErr == nil {
		for _, root := range roots {
			rows, queryErr := tx.QueryContext(ctx, `SELECT id, entity_id, path
				FROM archive_links
				WHERE root_path = ? COLLATE NOCASE
					AND active = 1
					AND COALESCE(last_scan_id, '') <> ?`, root, scanID)
			if queryErr != nil {
				return queryErr
			}
			type missingLink struct {
				id       string
				entityID string
				path     string
			}
			missing := make([]missingLink, 0)
			for rows.Next() {
				var link missingLink
				if err := rows.Scan(&link.id, &link.entityID, &link.path); err != nil {
					_ = rows.Close()
					return err
				}
				missing = append(missing, link)
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
			for _, link := range missing {
				result, err := tx.ExecContext(ctx, `UPDATE archive_links SET active = 0 WHERE id = ? AND active = 1`, link.id)
				if err != nil {
					return err
				}
				affected, err := result.RowsAffected()
				if err != nil {
					return err
				}
				if affected == 0 {
					continue
				}
				if err := appendEventTx(ctx, tx, link.entityID, "archive_unlinked", map[string]any{"path": link.path}); err != nil {
					return err
				}
				removedEntities[link.entityID] = struct{}{}
			}
		}
		if err := s.disableMissingCollectionModsTx(ctx, tx); err != nil {
			return err
		}
	}
	if len(removedEntities) > 0 {
		revisionAt := nowUTC()
		for entityID := range removedEntities {
			if err := touchEntityUpdatedAtTx(ctx, tx, entityID, revisionAt); err != nil {
				return err
			}
		}
	}
	for entityID := range removedEntities {
		if err := s.refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
			return err
		}
	}
	if len(removedEntities) > 0 {
		if err := markLibraryIndexFreshTx(ctx, tx); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE scans
		SET finished_at = ?, status = ?, discovered = ?, analyzed = ?, failed = ?, error = ?
		WHERE id = ?`, nowUTC(), status, discovered, analyzed, failed, scanError, scanID)
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
	return tx.Commit()
}

// LookupArchiveAnalysis is intentionally read-only. The scan worker uses it
// before opening an archive; the corresponding freshness update is deferred
// to ApplyScanBatch so a lookup cannot make an interrupted scan look complete.
// A path match is returned even when its analysis is stale, allowing callers
// to preserve the retained source while reporting reusable=false.
func (s *Store) LookupArchiveAnalysis(ctx context.Context, root, archivePath string, size int64, modified time.Time) (LibraryItem, bool, error) {
	modifiedAt := modified.UTC().Format(time.RFC3339Nano)
	var entityID string
	var reusable int
	err := s.db.QueryRowContext(ctx, `SELECT l.entity_id,
			CASE WHEN l.size_bytes = ? AND l.modified_at = ? AND a.analyzer_version = ? THEN 1 ELSE 0 END
		FROM archive_links l
		JOIN artifacts a ON a.id = l.artifact_id
		WHERE l.path = ? COLLATE NOCASE
		ORDER BY l.active DESC, l.last_seen_at DESC, l.id DESC
		LIMIT 1`, size, modifiedAt, modkit.AnalyzerVersion, archivePath).Scan(&entityID, &reusable)
	if errors.Is(err, sql.ErrNoRows) {
		return LibraryItem{}, false, nil
	}
	if err != nil {
		return LibraryItem{}, false, err
	}
	item, err := s.GetLibraryItem(ctx, entityID)
	if err != nil {
		return LibraryItem{}, false, err
	}
	return item, reusable == 1, nil
}

func (s *Store) ReuseArchiveAnalysis(ctx context.Context, scanID, root, archivePath string, size int64, modified time.Time) (LibraryItem, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LibraryItem{}, false, err
	}
	defer tx.Rollback()
	modifiedAt := modified.UTC().Format(time.RFC3339Nano)
	var found int
	err = tx.QueryRowContext(ctx, `SELECT 1
		FROM archive_links l
		JOIN artifacts a ON a.id = l.artifact_id
		WHERE l.path = ? COLLATE NOCASE
			AND l.size_bytes = ?
			AND l.modified_at = ?
			AND a.analyzer_version = ?
		LIMIT 1`, archivePath, size, modifiedAt, modkit.AnalyzerVersion).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return LibraryItem{}, false, nil
	}
	if err != nil {
		return LibraryItem{}, false, err
	}
	entityID, err := s.applyScanArchiveTx(ctx, tx, scanID, ScanArchive{
		Root: root, ArchivePath: archivePath, SizeBytes: size, Modified: modified, Reused: true,
	})
	if err != nil {
		return LibraryItem{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return LibraryItem{}, false, err
	}
	item, err := s.GetLibraryItem(ctx, entityID)
	if err != nil {
		return LibraryItem{}, false, err
	}
	return item, true, nil
}

func (s *Store) UpsertArchive(ctx context.Context, scanID, root, archivePath string, size int64, modified time.Time, manifest modkit.Manifest, asset *AssetRecord) (LibraryItem, error) {
	s.writeMu.Lock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.writeMu.Unlock()
		return LibraryItem{}, err
	}
	defer tx.Rollback()
	entityID, err := s.applyScanArchiveTx(ctx, tx, scanID, ScanArchive{
		Root: root, ArchivePath: archivePath, SizeBytes: size, Modified: modified, Manifest: manifest, Asset: asset,
	})
	if err != nil {
		s.writeMu.Unlock()
		return LibraryItem{}, err
	}
	if err := tx.Commit(); err != nil {
		s.writeMu.Unlock()
		return LibraryItem{}, err
	}
	s.writeMu.Unlock()
	return s.GetLibraryItem(ctx, entityID)
}

// ApplyScanBatch applies the complete scan snapshot and marks its scan row
// complete in the same transaction. A caller sees either the previous
// snapshot or this one; returned items are hydrated before the transaction
// commits, so a hydration error can only roll the batch back.
func (s *Store) ApplyScanBatch(ctx context.Context, scanID string, roots []string, archives []ScanArchive, discovered, analyzed, failed int) ([]LibraryItem, error) {
	if strings.TrimSpace(scanID) == "" {
		return nil, errors.New("scan ID is required")
	}
	if failed > 0 {
		return nil, fmt.Errorf("scan batch contains %d analysis failures; refusing to reconcile the scan", failed)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.applyScanBatchTx(ctx, scanID, roots, archives, discovered, analyzed, failed)
}

func (s *Store) applyScanBatchTx(ctx context.Context, scanID string, roots []string, archives []ScanArchive, discovered, analyzed, failed int) ([]LibraryItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var scanStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM scans WHERE id = ?`, scanID).Scan(&scanStatus); err != nil {
		return nil, err
	}
	if scanStatus != "running" {
		return nil, fmt.Errorf("scan %q is not running", scanID)
	}

	entityIDs := make([]string, 0, len(archives))
	seenEntities := make(map[string]struct{}, len(archives))
	allReused := true
	for _, archive := range archives {
		if !archive.Reused {
			allReused = false
			break
		}
	}
	if allReused {
		entityIDs, err = s.applyReusedScanArchivesTx(ctx, tx, scanID, archives)
		if err != nil {
			return nil, err
		}
	} else {
		for _, archive := range archives {
			entityID, err := s.applyScanArchiveTx(ctx, tx, scanID, archive)
			if err != nil {
				return nil, err
			}
			if _, seen := seenEntities[entityID]; !seen {
				seenEntities[entityID] = struct{}{}
				entityIDs = append(entityIDs, entityID)
			}
		}
	}

	removedEntities := map[string]struct{}{}
	var rootScanStmt, deactivateStmt, eventStmt *sql.Stmt
	if len(roots) > 0 {
		rootScanStmt, err = tx.PrepareContext(ctx, `SELECT id, entity_id, path
			FROM archive_links
			WHERE root_path = ? COLLATE NOCASE
				AND active = 1
				AND COALESCE(last_scan_id, '') <> ?`)
		if err != nil {
			return nil, err
		}
		defer rootScanStmt.Close()
		deactivateStmt, err = tx.PrepareContext(ctx, `UPDATE archive_links SET active = 0 WHERE id = ? AND active = 1`)
		if err != nil {
			return nil, err
		}
		defer deactivateStmt.Close()
		eventStmt, err = tx.PrepareContext(ctx, `INSERT INTO events(at,entity_id,type,data_json) VALUES(?,?,?,?)`)
		if err != nil {
			return nil, err
		}
		defer eventStmt.Close()
	}
	for _, root := range roots {
		rows, err := rootScanStmt.QueryContext(ctx, root, scanID)
		if err != nil {
			return nil, err
		}
		type missingLink struct {
			id       string
			entityID string
			path     string
		}
		missing := make([]missingLink, 0)
		for rows.Next() {
			var link missingLink
			if err := rows.Scan(&link.id, &link.entityID, &link.path); err != nil {
				_ = rows.Close()
				return nil, err
			}
			missing = append(missing, link)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		for _, link := range missing {
			result, err := deactivateStmt.ExecContext(ctx, link.id)
			if err != nil {
				return nil, err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return nil, err
			}
			if affected == 0 {
				continue
			}
			if err := appendEventStmtTx(ctx, eventStmt, link.entityID, "archive_unlinked", map[string]any{"path": link.path}); err != nil {
				return nil, err
			}
			removedEntities[link.entityID] = struct{}{}
		}
	}
	if err := s.disableMissingCollectionModsTx(ctx, tx); err != nil {
		return nil, err
	}
	if len(removedEntities) > 0 {
		revisionAt := nowUTC()
		for entityID := range removedEntities {
			if err := touchEntityUpdatedAtTx(ctx, tx, entityID, revisionAt); err != nil {
				return nil, err
			}
		}
	}
	for entityID := range removedEntities {
		if err := s.refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
			return nil, err
		}
	}
	if len(removedEntities) > 0 || len(entityIDs) > 0 {
		if err := markLibraryIndexFreshTx(ctx, tx); err != nil {
			return nil, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE scans
		SET finished_at = ?, status = 'complete', discovered = ?, analyzed = ?, failed = ?, error = ''
		WHERE id = ? AND status = 'running'`, nowUTC(), discovered, analyzed, failed, scanID)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, sql.ErrNoRows
	}
	hydrated, err := s.listItemsByIDsTx(ctx, tx, entityIDs)
	if err != nil {
		return nil, err
	}
	byEntity := make(map[string]int, len(hydrated))
	for index := range hydrated {
		byEntity[hydrated[index].EntityID] = index
	}
	items := make([]LibraryItem, 0, len(entityIDs))
	for _, entityID := range entityIDs {
		itemIndex, ok := byEntity[entityID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		items = append(items, hydrated[itemIndex])
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

// disableMissingCollectionModsTx reconciles saved selections only after the
// archive index has committed to a removal. Keep membership and ordering so
// the user can re-enable a restored mod; never disable a mod with another
// active source, or treat an entity that never had an archive as a removal.
func (s *Store) disableMissingCollectionModsTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT cm.entity_id, cm.collection_id
		FROM collection_mods cm
		WHERE cm.enabled=1
			AND EXISTS (SELECT 1 FROM archive_links al WHERE al.entity_id=cm.entity_id)
			AND NOT EXISTS (SELECT 1 FROM archive_links al WHERE al.entity_id=cm.entity_id AND al.active=1)`)
	if err != nil {
		return err
	}
	type membership struct{ entityID, collectionID string }
	var missing []membership
	for rows.Next() {
		var member membership
		if err := rows.Scan(&member.entityID, &member.collectionID); err != nil {
			_ = rows.Close()
			return err
		}
		missing = append(missing, member)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := nowUTC()
	for _, member := range missing {
		if _, err := tx.ExecContext(ctx, `UPDATE collection_mods SET enabled=0 WHERE collection_id=? AND entity_id=?`, member.collectionID, member.entityID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE collections SET updated_at=? WHERE id=?`, now, member.collectionID); err != nil {
			return err
		}
		if err := touchEntityUpdatedAtTx(ctx, tx, member.entityID, now); err != nil {
			return err
		}
		if err := s.refreshLibrarySearchEntryTx(ctx, tx, member.entityID); err != nil {
			return err
		}
		if err := appendEventTx(ctx, tx, member.entityID, "collection_mod_disabled", map[string]any{
			"collectionId": member.collectionID, "reason": "archive_removed",
		}); err != nil {
			return err
		}
	}
	if len(missing) > 0 {
		return markLibraryIndexFreshTx(ctx, tx)
	}
	return nil
}

type reusedScanArchiveKey struct {
	path        string
	sizeBytes   int64
	modifiedAt  string
	analyzerVer string
}

type reusedScanArchiveState struct {
	linkID   string
	entityID string
	rootPath string
	active   int
}

// applyReusedScanArchivesTx handles the common incremental-scan case in one
// indexed prefetch followed by executions of a single prepared update. The
// archive and artifact rows are read from the transaction snapshot before any
// link is touched, while state is updated in memory for duplicate inputs to
// preserve the sequential behavior of the general path.
func (s *Store) applyReusedScanArchivesTx(ctx context.Context, tx *sql.Tx, scanID string, archives []ScanArchive) ([]string, error) {
	if len(archives) == 0 {
		return []string{}, nil
	}
	paths := make([]string, 0, len(archives))
	seenPaths := make(map[string]struct{}, len(archives))
	for _, archive := range archives {
		archivePath := strings.TrimSpace(archive.ArchivePath)
		if archivePath == "" {
			return nil, errors.New("archive path is required")
		}
		if _, err := normalizeArchiveSourceClass(archive.SourceClass); err != nil {
			return nil, err
		}
		pathKey := sqliteNoCaseKey(archivePath)
		if _, seen := seenPaths[pathKey]; seen {
			continue
		}
		seenPaths[pathKey] = struct{}{}
		paths = append(paths, archivePath)
	}

	states := make(map[reusedScanArchiveKey]reusedScanArchiveState, len(paths))
	stateEntityIDs := make([]string, 0, len(paths))
	seenStateEntities := make(map[string]struct{}, len(paths))
	const prefetchChunkSize = 800
	for chunk := range (len(paths) + prefetchChunkSize - 1) / prefetchChunkSize {
		start := chunk * prefetchChunkSize
		end := start + prefetchChunkSize
		if end > len(paths) {
			end = len(paths)
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", end-start), ",")
		args := make([]any, end-start)
		for index := range paths[start:end] {
			args[index] = paths[start+index]
		}
		rows, err := tx.QueryContext(ctx, `SELECT l.path,l.id,l.entity_id,l.root_path,l.active,l.size_bytes,l.modified_at,COALESCE(a.analyzer_version,'')
			FROM archive_links l
			JOIN artifacts a ON a.id=l.artifact_id
			WHERE l.path COLLATE NOCASE IN (`+placeholders+`)
			ORDER BY l.path COLLATE NOCASE,l.rowid`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var path, linkID, entityID, rootPath, modifiedAt, analyzerVer string
			var active int
			var sizeBytes int64
			if err := rows.Scan(&path, &linkID, &entityID, &rootPath, &active, &sizeBytes, &modifiedAt, &analyzerVer); err != nil {
				_ = rows.Close()
				return nil, err
			}
			key := reusedScanArchiveKey{
				path:        sqliteNoCaseKey(path),
				sizeBytes:   sizeBytes,
				modifiedAt:  modifiedAt,
				analyzerVer: analyzerVer,
			}
			if _, exists := states[key]; exists {
				continue
			}
			states[key] = reusedScanArchiveState{
				linkID:   linkID,
				entityID: entityID,
				rootPath: rootPath,
				active:   active,
			}
			if _, seen := seenStateEntities[entityID]; !seen {
				seenStateEntities[entityID] = struct{}{}
				stateEntityIDs = append(stateEntityIDs, entityID)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	oldLatest, err := prefetchLatestScanLinksTx(ctx, tx, stateEntityIDs)
	if err != nil {
		return nil, err
	}

	updateStmt, err := tx.PrepareContext(ctx, `UPDATE archive_links
		SET root_path = ?, active = 1, size_bytes = ?, modified_at = ?,
			last_seen_at = ?, last_scan_id = ?
		WHERE id = ?`)
	if err != nil {
		return nil, err
	}
	defer updateStmt.Close()
	eventStmt, err := tx.PrepareContext(ctx, `INSERT INTO events(at,entity_id,type,data_json) VALUES(?,?,?,?)`)
	if err != nil {
		return nil, err
	}
	defer eventStmt.Close()

	entityIDs := make([]string, 0, len(archives))
	seenEntities := make(map[string]struct{}, len(archives))
	changedEntities := make(map[string]struct{})
	for _, archive := range archives {
		archivePath := strings.TrimSpace(archive.ArchivePath)
		modifiedAt := archive.Modified.UTC().Format(time.RFC3339Nano)
		key := reusedScanArchiveKey{
			path:        sqliteNoCaseKey(archivePath),
			sizeBytes:   archive.SizeBytes,
			modifiedAt:  modifiedAt,
			analyzerVer: modkit.AnalyzerVersion,
		}
		state, ok := states[key]
		if !ok {
			return nil, fmt.Errorf("reuse archive %q: %w", archivePath, sql.ErrNoRows)
		}
		wasActive := state.active
		if state.rootPath != archive.Root {
			changedEntities[state.entityID] = struct{}{}
		}
		if wasActive == 0 {
			changedEntities[state.entityID] = struct{}{}
		}
		if _, err := updateStmt.ExecContext(ctx, archive.Root, archive.SizeBytes, modifiedAt, nowUTC(), scanID, state.linkID); err != nil {
			return nil, err
		}
		if wasActive == 0 {
			encoded, _ := json.Marshal(map[string]any{"path": archivePath})
			if _, err := eventStmt.ExecContext(ctx, nowUTC(), state.entityID, "archive_relinked", string(encoded)); err != nil {
				return nil, err
			}
		}
		state.active = 1
		state.rootPath = archive.Root
		states[key] = state
		if _, seen := seenEntities[state.entityID]; !seen {
			seenEntities[state.entityID] = struct{}{}
			entityIDs = append(entityIDs, state.entityID)
		}
	}
	newLatest, err := prefetchLatestScanLinksTx(ctx, tx, entityIDs)
	if err != nil {
		return nil, err
	}
	revisionAt := nowUTC()
	for _, entityID := range entityIDs {
		if oldLatest[entityID] != newLatest[entityID] {
			changedEntities[entityID] = struct{}{}
		}
		if _, changed := changedEntities[entityID]; changed {
			if err := touchEntityUpdatedAtTx(ctx, tx, entityID, revisionAt); err != nil {
				return nil, err
			}
			if err := s.refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
				return nil, err
			}
		}
	}
	return entityIDs, nil
}

func prefetchLatestScanLinksTx(ctx context.Context, tx *sql.Tx, entityIDs []string) (map[string]string, error) {
	latest := make(map[string]string, len(entityIDs))
	const chunkSize = 800
	for chunk := range (len(entityIDs) + chunkSize - 1) / chunkSize {
		start := chunk * chunkSize
		end := start + chunkSize
		if end > len(entityIDs) {
			end = len(entityIDs)
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", end-start), ",")
		args := make([]any, end-start)
		for index := range entityIDs[start:end] {
			args[index] = entityIDs[start+index]
		}
		rows, err := tx.QueryContext(ctx, `SELECT entity_id,id FROM (
			SELECT entity_id,id,
				ROW_NUMBER() OVER(PARTITION BY entity_id ORDER BY active DESC,last_seen_at DESC,id DESC) AS ordinal
			FROM archive_links
			WHERE entity_id IN (`+placeholders+`)
		) WHERE ordinal=1`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var entityID, linkID string
			if err := rows.Scan(&entityID, &linkID); err != nil {
				_ = rows.Close()
				return nil, err
			}
			latest[entityID] = linkID
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return latest, nil
}

func sqliteNoCaseKey(value string) string {
	for index := range value {
		if value[index] < 'A' || value[index] > 'Z' {
			continue
		}
		lowered := []byte(value)
		for inner := range lowered[index:] {
			position := index + inner
			if lowered[position] >= 'A' && lowered[position] <= 'Z' {
				lowered[position] += 'a' - 'A'
			}
		}
		return string(lowered)
	}
	return value
}

func (s *Store) applyScanArchiveTx(ctx context.Context, tx *sql.Tx, scanID string, archive ScanArchive) (string, error) {
	archivePath := strings.TrimSpace(archive.ArchivePath)
	if archivePath == "" {
		return "", errors.New("archive path is required")
	}
	sourceClass, err := normalizeArchiveSourceClass(archive.SourceClass)
	if err != nil {
		return "", err
	}
	sourceExplicit := strings.TrimSpace(archive.SourceClass) != ""
	modifiedAt := archive.Modified.UTC().Format(time.RFC3339Nano)
	now := nowUTC()

	if archive.Reused {
		var linkID, entityID, previousRoot string
		var wasActive int
		if err := tx.QueryRowContext(ctx, `SELECT l.id, l.entity_id, l.active, l.root_path
			FROM archive_links l
			JOIN artifacts a ON a.id = l.artifact_id
			WHERE l.path = ? COLLATE NOCASE
				AND l.size_bytes = ?
				AND l.modified_at = ?
				AND a.analyzer_version = ?
			LIMIT 1`, archivePath, archive.SizeBytes, modifiedAt, modkit.AnalyzerVersion).
			Scan(&linkID, &entityID, &wasActive, &previousRoot); err != nil {
			return "", fmt.Errorf("reuse archive %q: %w", archivePath, err)
		}
		var previousRepresentative string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM archive_links
			WHERE entity_id = ?
			ORDER BY active DESC, last_seen_at DESC, id DESC LIMIT 1`, entityID).Scan(&previousRepresentative); err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE archive_links
			SET root_path = ?, active = 1, size_bytes = ?, modified_at = ?,
				last_seen_at = ?, last_scan_id = ?
			WHERE id = ?`, archive.Root, archive.SizeBytes, modifiedAt, now, scanID, linkID); err != nil {
			return "", err
		}
		if wasActive == 0 {
			if err := appendEventTx(ctx, tx, entityID, "archive_relinked", map[string]any{"path": archivePath}); err != nil {
				return "", err
			}
		}
		var currentRepresentative string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM archive_links
			WHERE entity_id = ?
			ORDER BY active DESC, last_seen_at DESC, id DESC LIMIT 1`, entityID).Scan(&currentRepresentative); err != nil {
			return "", err
		}
		if wasActive == 0 || previousRoot != archive.Root || previousRepresentative != currentRepresentative {
			if err := touchEntityUpdatedAtTx(ctx, tx, entityID, now); err != nil {
				return "", err
			}
		}
		if err := s.refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
			return "", err
		}
		return entityID, nil
	}

	centralFingerprint := strings.TrimSpace(archive.Manifest.CentralFingerprint)
	if centralFingerprint == "" {
		return "", errors.New("archive central fingerprint is required")
	}
	manifestJSON, err := json.Marshal(archive.Manifest)
	if err != nil {
		return "", err
	}
	manifestText := string(manifestJSON)
	var artifactID, previousSHA, previousManifest, previousAnalyzer string
	var previousArtifactSize int64
	err = tx.QueryRowContext(ctx, `SELECT id, sha256, size_bytes, manifest_json, analyzer_version
		FROM artifacts WHERE central_fingerprint = ?`, centralFingerprint).
		Scan(&artifactID, &previousSHA, &previousArtifactSize, &previousManifest, &previousAnalyzer)
	artifactChanged := true
	if errors.Is(err, sql.ErrNoRows) {
		artifactID, err = modkit.NewID()
		if err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(
			id, central_fingerprint, sha256, size_bytes, manifest_json, analyzer_version, analyzed_at
		) VALUES(?, ?, ?, ?, ?, ?, ?)`, artifactID, centralFingerprint, archive.Manifest.FullSHA256,
			archive.SizeBytes, manifestText, modkit.AnalyzerVersion, now); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else {
		effectiveSHA := previousSHA
		if archive.Manifest.FullSHA256 != "" {
			effectiveSHA = archive.Manifest.FullSHA256
		}
		artifactChanged = effectiveSHA != previousSHA ||
			previousArtifactSize != archive.SizeBytes ||
			previousManifest != manifestText ||
			previousAnalyzer != modkit.AnalyzerVersion
		if artifactChanged {
			if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET
				sha256 = CASE WHEN ? <> '' THEN ? ELSE sha256 END,
				size_bytes = ?, manifest_json = ?, analyzer_version = ?, analyzed_at = ?
				WHERE id = ?`, archive.Manifest.FullSHA256, archive.Manifest.FullSHA256,
				archive.SizeBytes, manifestText, modkit.AnalyzerVersion, now, artifactID); err != nil {
				return "", err
			}
		}
	}

	display := displayName(archive.Manifest, archivePath)
	kind := string(archive.Manifest.Kind)
	basenameKey := strings.ToLower(filepath.Base(archivePath)) + "\x00" + centralFingerprint
	var linkID, entityID, previousArtifact, previousLinkSource, previousRoot, previousModified string
	var previousLinkSize int64
	var wasActive int
	err = tx.QueryRowContext(ctx, `SELECT id, entity_id, artifact_id, active, root_path, size_bytes, modified_at, source_id
		FROM archive_links WHERE path = ? COLLATE NOCASE`, archivePath).
		Scan(&linkID, &entityID, &previousArtifact, &wasActive, &previousRoot, &previousLinkSize, &previousModified, &previousLinkSource)
	newLink := false
	newEntity := false
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT entity_id FROM archive_links
			WHERE basename_key = ?
			ORDER BY active DESC, last_seen_at DESC, id DESC LIMIT 1`, basenameKey).Scan(&entityID)
		if errors.Is(err, sql.ErrNoRows) {
			entityID, err = modkit.NewID()
			if err != nil {
				return "", err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO entities(
				id, display_name, kind, source_id, created_at, updated_at
			) VALUES(?, ?, ?, ?, ?, ?)`, entityID, display, kind, sourceClass, now, now); err != nil {
				return "", err
			}
			newEntity = true
		} else if err != nil {
			return "", err
		} else if !sourceExplicit {
			if err := tx.QueryRowContext(ctx, `SELECT source_id FROM entities WHERE id = ?`, entityID).Scan(&sourceClass); err != nil {
				return "", err
			}
		}
		linkID, err = modkit.NewID()
		if err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO archive_links(
			id, entity_id, artifact_id, path, root_path, active, size_bytes,
			modified_at, discovered_at, last_seen_at, last_scan_id, basename_key, source_id
		) VALUES(?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)`, linkID, entityID, artifactID,
			archivePath, archive.Root, archive.SizeBytes, modifiedAt, now, now, scanID, basenameKey, sourceClass); err != nil {
			return "", err
		}
		newLink = true
	} else {
		if !sourceExplicit {
			sourceClass = strings.TrimSpace(previousLinkSource)
			if sourceClass == "" {
				sourceClass = "user-added"
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE archive_links SET
			artifact_id = ?, root_path = ?, active = 1, source_id = ?, size_bytes = ?, modified_at = ?,
				last_seen_at = ?, last_scan_id = ?, basename_key = ?
			WHERE id = ?`, artifactID, archive.Root, sourceClass, archive.SizeBytes, modifiedAt, now, scanID, basenameKey, linkID); err != nil {
			return "", err
		}
	}

	var previousDisplay, previousKind, previousSource string
	if !newLink {
		if err := tx.QueryRowContext(ctx, `SELECT display_name, kind, source_id
			FROM entities WHERE id = ?`, entityID).Scan(&previousDisplay, &previousKind, &previousSource); err != nil {
			return "", err
		}
	}
	entityChanged := newLink || previousArtifact != artifactID || artifactChanged ||
		previousRoot != archive.Root || previousLinkSize != archive.SizeBytes ||
		previousModified != modifiedAt || wasActive == 0 || previousLinkSource != sourceClass
	if !newLink && (previousDisplay != display || previousKind != kind || previousSource != sourceClass) {
		entityChanged = true
	}
	if entityChanged {
		if newLink {
			if !newEntity {
				if err := touchEntityUpdatedAtTx(ctx, tx, entityID, now); err != nil {
					return "", err
				}
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE entities SET
				display_name = ?, kind = ?, source_id = ?, updated_at = ? WHERE id = ?`,
				display, kind, sourceClass, now, entityID); err != nil {
				return "", err
			}
		}
	}
	if err := applyAssetTx(ctx, tx, entityID, archive, now); err != nil {
		return "", err
	}
	if newLink {
		if err := appendEventTx(ctx, tx, entityID, "archive_discovered", map[string]any{"path": archivePath, "artifactId": artifactID}); err != nil {
			return "", err
		}
	} else {
		if previousArtifact != artifactID {
			if err := appendEventTx(ctx, tx, entityID, "archive_changed", map[string]any{
				"path": archivePath, "fromArtifactId": previousArtifact, "artifactId": artifactID,
			}); err != nil {
				return "", err
			}
		}
		if wasActive == 0 {
			if err := appendEventTx(ctx, tx, entityID, "archive_relinked", map[string]any{"path": archivePath}); err != nil {
				return "", err
			}
		}
	}
	if err := s.refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
		return "", err
	}
	return entityID, nil
}

func applyAssetTx(ctx context.Context, tx *sql.Tx, entityID string, archive ScanArchive, now string) error {
	var manualSelection string
	selectionErr := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, entityPreviewSelectionKey(entityID)).Scan(&manualSelection)
	if selectionErr != nil && !errors.Is(selectionErr, sql.ErrNoRows) {
		return selectionErr
	}
	if selectionErr == nil && strings.TrimSpace(manualSelection) != "" {
		// A manual preview is intentionally independent of the scanner's
		// heuristic so rescans cannot silently replace the user's choice.
		return nil
	}
	if archive.Asset != nil {
		if strings.TrimSpace(archive.Asset.SHA256) == "" {
			return errors.New("asset SHA-256 is required")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO assets(
			sha256, path, mime, width, height, size_bytes, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(sha256) DO UPDATE SET
			path = excluded.path, mime = excluded.mime, width = excluded.width,
			height = excluded.height, size_bytes = excluded.size_bytes`,
			archive.Asset.SHA256, archive.Asset.Path, archive.Asset.MIME, archive.Asset.Width,
			archive.Asset.Height, archive.Asset.SizeBytes, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO entity_assets(entity_id, asset_sha256, role, ordinal)
			VALUES(?, ?, 'thumbnail', 0)
			ON CONFLICT(entity_id, role, ordinal) DO UPDATE SET asset_sha256 = excluded.asset_sha256`,
			entityID, archive.Asset.SHA256)
		return err
	}
	if !archive.Reused && strings.TrimSpace(archive.Manifest.SelectedImagePath) == "" {
		_, err := tx.ExecContext(ctx, `DELETE FROM entity_assets
			WHERE entity_id = ? AND role = 'thumbnail' AND ordinal = 0`, entityID)
		return err
	}
	return nil
}

func markLibraryIndexFreshTx(ctx context.Context, tx *sql.Tx) error {
	return markLibraryFTSFreshTx(ctx, tx)
}

func touchEntityUpdatedAtTx(ctx context.Context, tx *sql.Tx, entityID, updatedAt string) error {
	_, err := tx.ExecContext(ctx, `UPDATE entities SET updated_at=? WHERE id=?`, updatedAt, entityID)
	return err
}

func normalizeArchiveSourceClass(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "user-added", nil
	case "beamng-repository":
		return "beamng-repository", nil
	case "user-added":
		return "user-added", nil
	default:
		return "", fmt.Errorf("unsupported archive source classification %q", value)
	}
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

func (s *Store) ListLibrary(ctx context.Context, health, kind, query, collectionID, scope string) ([]LibraryItem, error) {
	archiveScope, err := normalizeLibraryArchiveScope(scope)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	entityIDs, err := s.listLibraryQueryTx(ctx, tx, health, kind, query, collectionID, archiveScope)
	if err != nil {
		return nil, err
	}
	items, err := s.listItemsByIDsTx(ctx, tx, entityIDs)
	if err != nil {
		return nil, err
	}
	byEntity := make(map[string]int, len(items))
	for index := range items {
		byEntity[items[index].EntityID] = index
	}
	search := parseLibrarySearchQuery(query)
	collectionNames := map[string]string{}
	effectiveCollectionNames := map[string][]string{}
	needsCollectionNames := false
	for _, term := range search.terms {
		if term.scope == "all" || term.scope == "collection" {
			needsCollectionNames = true
			break
		}
	}
	if needsCollectionNames {
		collectionNames, err = s.libraryCollectionNamesTx(ctx, tx)
		if err != nil {
			return nil, err
		}
		effectiveCollectionNames, err = libraryEffectiveCollectionNamesTx(ctx, tx, entityIDs)
		if err != nil {
			return nil, err
		}
	}
	health = normalizeLibraryStatus(health)
	filtered := make([]LibraryItem, 0, len(entityIDs))
	for _, entityID := range entityIDs {
		itemIndex, ok := byEntity[entityID]
		if !ok {
			continue
		}
		item := items[itemIndex]
		if health != "" && item.HealthStatus != health {
			continue
		}
		if kind != "" && kind != "all" && string(item.Kind) != kind {
			continue
		}
		if collectionID == "unfiled" && len(item.CollectionIDs) > 0 {
			continue
		}
		// listLibraryQuery intentionally returns candidates. Keep this
		// residual check so scoped terms and status semantics remain exactly
		// those of the pre-SQL query implementation.
		if (search.status != "" || len(search.terms) > 0) &&
			!search.matches(item, collectionNames, effectiveCollectionNames[entityID]) {
			continue
		}
		filtered = append(filtered, item)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return filtered, nil
}

func (s *Store) listItems(ctx context.Context, entityID string) ([]LibraryItem, error) {
	if strings.TrimSpace(entityID) == "" {
		return s.queryLibraryItemsQuery(ctx, s.db, nil)
	}
	return s.listItemsByIDs(ctx, []string{entityID})
}

func (s *Store) listItemsByIDs(ctx context.Context, entityIDs []string) ([]LibraryItem, error) {
	return s.listItemsByIDsQuery(ctx, s.db, entityIDs)
}

func (s *Store) listItemsByIDsTx(ctx context.Context, tx *sql.Tx, entityIDs []string) ([]LibraryItem, error) {
	return s.listItemsByIDsQuery(ctx, tx, entityIDs)
}

func (s *Store) listItemsByIDsQuery(ctx context.Context, queryer libraryQueryer, entityIDs []string) ([]LibraryItem, error) {
	if len(entityIDs) == 0 {
		return []LibraryItem{}, nil
	}
	// Keep IN lists below SQLite's conservative host-parameter limit. The
	// caller restores candidate ordering after hydration.
	const chunkSize = 800
	items := make([]LibraryItem, 0, len(entityIDs))
	for start := 0; start < len(entityIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(entityIDs) {
			end = len(entityIDs)
		}
		chunk, err := s.queryLibraryItemsQuery(ctx, queryer, entityIDs[start:end])
		if err != nil {
			return nil, err
		}
		items = append(items, chunk...)
	}
	return items, nil
}
func (s *Store) queryLibraryItemsQuery(ctx context.Context, queryer libraryQueryer, entityIDs []string) ([]LibraryItem, error) {
	if db, ok := queryer.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		items, err := s.queryLibraryItemsQuery(ctx, tx, entityIDs)
		if err != nil {
			return nil, err
		}
		return items, tx.Commit()
	}
	query := `SELECT e.id, e.updated_at, COALESCE(e.archived_at,''), e.display_name, e.kind,
		COALESCE(NULLIF(l.source_id,''),NULLIF(e.source_id,''),'user-added'),
		CASE lower(COALESCE(NULLIF(l.source_id,''),NULLIF(e.source_id,''),'user-added'))
			WHEN 'beamng-repository' THEN 'BeamNG Repository'
			WHEN 'user-added' THEN 'User added'
			ELSE COALESCE(sc.label,'')
		END,
		COALESCE(l.id,''), COALESCE(l.artifact_id,''), COALESCE(l.path,''), COALESCE(l.root_path,''),
		COALESCE(l.active,0), COALESCE(l.size_bytes,0), COALESCE(l.modified_at,''), COALESCE(l.last_seen_at,''),
		COALESCE(a.central_fingerprint,''), COALESCE(a.sha256,''),
		COALESCE(ast.sha256,''),
		COALESCE(sm.revision,'')
	FROM entities e
	LEFT JOIN archive_links l ON l.id = (
		SELECT l2.id FROM archive_links l2
		WHERE l2.entity_id=e.id
		ORDER BY l2.active DESC, l2.last_seen_at DESC, l2.id DESC LIMIT 1
	)
	LEFT JOIN source_classifications sc ON sc.id = COALESCE(NULLIF(l.source_id,''),NULLIF(e.source_id,''),'user-added')
	LEFT JOIN artifacts a ON a.id=l.artifact_id
	LEFT JOIN artifact_summaries sm ON sm.artifact_id=a.id
	LEFT JOIN entity_assets ea ON ea.entity_id=e.id AND ea.role='thumbnail' AND ea.ordinal=0
	LEFT JOIN assets ast ON ast.sha256=ea.asset_sha256`
	args := make([]any, 0, len(entityIDs))
	if len(entityIDs) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(entityIDs)), ",")
		query += ` WHERE e.id IN (` + placeholders + `)`
		for _, entityID := range entityIDs {
			args = append(args, entityID)
		}
	}
	query += ` ORDER BY l.active DESC, e.updated_at DESC, e.display_name COLLATE NOCASE, e.id`
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]LibraryItem, 0, len(entityIDs))
	missing := map[string][]int{}
	revisions := map[string]string{}
	for rows.Next() {
		var item LibraryItem
		var kind, assetSHA, summaryRevision string
		if err := rows.Scan(&item.EntityID, &item.Revision, &item.ArchivedAt, &item.DisplayName, &kind, &item.SourceID, &item.Source,
			&item.LinkID, &item.ArtifactID, &item.ArchivePath, &item.RootPath,
			&item.Linked, &item.SizeBytes, &item.ModifiedAt, &item.LastSeenAt,
			&item.Fingerprint, &item.SHA256, &assetSHA, &summaryRevision); err != nil {
			return nil, err
		}
		item.Kind = modkit.Kind(kind)
		if cached, ok := s.manifestCache.get(item.ArtifactID, summaryRevision); ok && item.ArtifactID != "" {
			item.Manifest = cached
		} else if item.ArtifactID != "" {
			missing[item.ArtifactID] = append(missing[item.ArtifactID], len(items))
			revisions[item.ArtifactID] = summaryRevision
		}
		item.CollectionIDs = []string{}
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
	if err := s.hydrateMissingManifests(ctx, queryer, items, missing, revisions); err != nil {
		return nil, err
	}
	for index := range items {
		item := &items[index]
		item.MemberCount = len(item.Manifest.Members)
		item.NamespaceCount = len(item.Manifest.Namespaces)
		item.VariantCount = len(item.Manifest.Variants)
		item.IssueCount = len(item.Manifest.Issues)
	}
	if err := attachLibraryItemCollectionsQuery(ctx, queryer, items); err != nil {
		return nil, err
	}
	if err := attachLibraryItemTagsQuery(ctx, queryer, items); err != nil {
		return nil, err
	}
	if err := attachLibraryItemHealthQuery(ctx, queryer, items); err != nil {
		return nil, err
	}
	return items, nil
}

func attachLibraryItemCollectionsQuery(ctx context.Context, queryer libraryQueryer, items []LibraryItem) error {
	if len(items) == 0 {
		return nil
	}
	byEntity := make(map[string]*LibraryItem, len(items))
	entityIDs := make([]string, 0, len(items))
	for index := range items {
		items[index].CollectionIDs = []string{}
		if _, exists := byEntity[items[index].EntityID]; exists {
			continue
		}
		byEntity[items[index].EntityID] = &items[index]
		entityIDs = append(entityIDs, items[index].EntityID)
	}
	const chunkSize = 800
	for start := 0; start < len(entityIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(entityIDs) {
			end = len(entityIDs)
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", end-start), ",")
		args := make([]any, 0, end-start)
		for _, entityID := range entityIDs[start:end] {
			args = append(args, entityID)
		}
		rows, err := queryer.QueryContext(ctx, `SELECT entity_id,collection_id
			FROM collection_mods
			WHERE entity_id IN (`+placeholders+`)
			ORDER BY entity_id,position,collection_id`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var entityID, collectionID string
			if err := rows.Scan(&entityID, &collectionID); err != nil {
				_ = rows.Close()
				return err
			}
			item := byEntity[entityID]
			if item == nil {
				continue
			}
			if len(item.CollectionIDs) == 0 ||
				item.CollectionIDs[len(item.CollectionIDs)-1] != collectionID {
				item.CollectionIDs = append(item.CollectionIDs, collectionID)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

// libraryEffectiveCollectionNamesTx resolves every direct membership and its
// ancestors for the supplied entities. It uses UNION rather than UNION ALL so
// shared descendants and legacy cycles cannot expand without bounds.
func libraryEffectiveCollectionNamesTx(ctx context.Context, queryer libraryQueryer, entityIDs []string) (map[string][]string, error) {
	result := make(map[string][]string, len(entityIDs))
	if len(entityIDs) == 0 {
		return result, nil
	}
	const chunkSize = 800
	for start := 0; start < len(entityIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(entityIDs) {
			end = len(entityIDs)
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", end-start), ",")
		args := make([]any, 0, end-start)
		for _, entityID := range entityIDs[start:end] {
			args = append(args, entityID)
		}
		rows, err := queryer.QueryContext(ctx, `WITH RECURSIVE effective(entity_id,collection_id) AS (
				SELECT cm.entity_id,cm.collection_id
				FROM collection_mods cm
				WHERE cm.entity_id IN (`+placeholders+`)
				UNION
				SELECT effective.entity_id,cc.parent_id
				FROM effective
				JOIN collection_children cc ON cc.child_id=effective.collection_id
			)
			SELECT effective.entity_id,c.name
			FROM effective
			JOIN collections c ON c.id=effective.collection_id
			ORDER BY effective.entity_id,c.position,c.name COLLATE NOCASE,c.id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var entityID, name string
			if err := rows.Scan(&entityID, &name); err != nil {
				_ = rows.Close()
				return nil, err
			}
			result[entityID] = append(result[entityID], name)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func attachLibraryItemTagsQuery(ctx context.Context, queryer libraryQueryer, items []LibraryItem) error {
	if len(items) == 0 {
		return nil
	}
	byEntity := make(map[string]*LibraryItem, len(items))
	for index := range items {
		items[index].Tags = []ModTag{}
		byEntity[items[index].EntityID] = &items[index]
	}
	query := `SELECT te.entity_id,t.id,t.name,t.color,t.icon,t.origin
		FROM mod_tag_entities te JOIN mod_tags t ON t.id=te.tag_id`
	args := []any{}
	if len(items) == 1 {
		query += ` WHERE te.entity_id=?`
		args = append(args, items[0].EntityID)
	}
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var entityID string
		var tag ModTag
		if err := rows.Scan(&entityID, &tag.ID, &tag.Name, &tag.Color, &tag.Icon, &tag.Origin); err != nil {
			return err
		}
		if item := byEntity[entityID]; item != nil {
			item.Tags = append(item.Tags, tag)
		}
	}
	return rows.Err()
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
	historyTotal := 0
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE entity_id=?`, entityID).Scan(&historyTotal); err != nil {
		return EntityDetail{}, err
	}
	history, err := s.ListEvents(ctx, entityID, 100)
	return EntityDetail{Item: item, Links: links, History: history, HistoryTotal: historyTotal}, err
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
	_ = s.db.QueryRowContext(ctx, `SELECT finished_at,status,discovered,analyzed,failed FROM scans ORDER BY julianday(started_at) DESC, rowid DESC LIMIT 1`).Scan(&result.LastScanAt, &result.LastScanStatus, &result.LastScanFound, &result.LastScanAnalyzed, &result.LastScanFailed)
	_ = s.db.QueryRowContext(ctx, `SELECT finished_at FROM scans WHERE status='complete' AND finished_at<>'' ORDER BY julianday(finished_at) DESC, rowid DESC LIMIT 1`).Scan(&result.LastSuccessfulScanAt)
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

// AssetsNeedingThumbnails lists the cached images whose size makes a
// derivative worthwhile, largest first, so the backfill removes the worst
// stutters before the small ones.
func (s *Store) AssetsNeedingThumbnails(ctx context.Context) ([]AssetRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sha256,path,mime,width,height,size_bytes FROM assets
		WHERE path<>'' AND (width>? OR height>?)
		ORDER BY size_bytes DESC`, thumbnailMaxEdge, thumbnailMaxEdge)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assets []AssetRecord
	for rows.Next() {
		var asset AssetRecord
		if err := rows.Scan(&asset.SHA256, &asset.Path, &asset.MIME, &asset.Width, &asset.Height, &asset.SizeBytes); err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	return assets, rows.Err()
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

func appendEventStmtTx(ctx context.Context, stmt *sql.Stmt, entityID, eventType string, data map[string]any) error {
	encoded, _ := json.Marshal(data)
	_, err := stmt.ExecContext(ctx, nowUTC(), entityID, eventType, string(encoded))
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
