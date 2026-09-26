package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

const (
	storeSchemaVersion          = 5
	legacyCatalogSchemaVersion  = 1
	legacyCatalogImportMarker   = "legacy_catalog_imported"
	legacyCatalogImportAbsent   = "absent"
	legacyCatalogImportImported = "imported"
	libraryFTSVersion           = "2"
	libraryFTSFreshnessKey      = "fts_fresh"
	libraryFTSVersionKey        = "fts_version"
	legacyCatalogBackupSuffix   = ".bak"
)

var (
	v4EntitiesTableDDL = `CREATE TABLE IF NOT EXISTS entities (
		id TEXT PRIMARY KEY,
		display_name TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL DEFAULT 'unknown',
		source_id TEXT NOT NULL DEFAULT 'user-added',
		created_at TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT '',
		FOREIGN KEY(source_id) REFERENCES source_classifications(id)
	)`
	v4ArchiveLinksTableDDL = `CREATE TABLE IF NOT EXISTS archive_links (
		id TEXT PRIMARY KEY,
		entity_id TEXT NOT NULL,
		artifact_id TEXT NOT NULL,
		path TEXT NOT NULL DEFAULT '',
		root_path TEXT NOT NULL DEFAULT '',
		active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)),
		source_id TEXT NOT NULL DEFAULT 'user-added',
		size_bytes INTEGER NOT NULL DEFAULT 0 CHECK(size_bytes >= 0),
		modified_at TEXT NOT NULL DEFAULT '',
		discovered_at TEXT NOT NULL DEFAULT '',
		last_seen_at TEXT NOT NULL DEFAULT '',
		last_scan_id TEXT NOT NULL DEFAULT '',
		basename_key TEXT NOT NULL DEFAULT '',
		FOREIGN KEY(entity_id) REFERENCES entities(id) ON DELETE CASCADE,
		FOREIGN KEY(artifact_id) REFERENCES artifacts(id) ON DELETE CASCADE,
		FOREIGN KEY(source_id) REFERENCES source_classifications(id)
	)`
	collectionsTableDDL = `CREATE TABLE IF NOT EXISTS collections (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL COLLATE NOCASE UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	position INTEGER NOT NULL DEFAULT 0,
	cover_json TEXT NOT NULL DEFAULT '{"mode":"automatic","images":[]}',
	cover_asset_sha TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL DEFAULT ''
)`
	collectionModsTableDDL = `CREATE TABLE IF NOT EXISTS collection_mods (
	collection_id TEXT NOT NULL,
	entity_id TEXT NOT NULL,
	position INTEGER NOT NULL DEFAULT 0,
	enabled INTEGER NOT NULL DEFAULT 1,
	disabled_by_archive INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(collection_id,entity_id),
	FOREIGN KEY(collection_id) REFERENCES collections(id) ON DELETE CASCADE,
	FOREIGN KEY(entity_id) REFERENCES entities(id) ON DELETE CASCADE
)`
	collectionChildrenTableDDL = `CREATE TABLE IF NOT EXISTS collection_children (
	parent_id TEXT NOT NULL,
	child_id TEXT NOT NULL,
	position INTEGER NOT NULL DEFAULT 0,
	enabled INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY(parent_id,child_id),
	CHECK(parent_id<>child_id),
	FOREIGN KEY(parent_id) REFERENCES collections(id) ON DELETE CASCADE,
	FOREIGN KEY(child_id) REFERENCES collections(id) ON DELETE CASCADE
)`
	playProfilesTableDDL = `CREATE TABLE IF NOT EXISTS play_profiles (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL COLLATE NOCASE UNIQUE,
	created_at TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL DEFAULT ''
)`
	playProfileCollectionsTableDDL = `CREATE TABLE IF NOT EXISTS play_profile_collections (
	profile_id TEXT NOT NULL,
	collection_id TEXT NOT NULL,
	position INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(profile_id,collection_id),
	FOREIGN KEY(profile_id) REFERENCES play_profiles(id) ON DELETE CASCADE,
	FOREIGN KEY(collection_id) REFERENCES collections(id) ON DELETE CASCADE
)`
	playStateTableDDL = `CREATE TABLE IF NOT EXISTS play_state (
	id INTEGER PRIMARY KEY CHECK(id=1),
	state_json TEXT NOT NULL
)`
	v4TestInstallsTableDDL = `CREATE TABLE IF NOT EXISTS test_installs (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		export_id TEXT NOT NULL,
		path TEXT NOT NULL DEFAULT '',
		sha256 TEXT NOT NULL DEFAULT '',
		installed_at TEXT NOT NULL DEFAULT '',
		log_baseline_at TEXT NOT NULL DEFAULT '',
		log_path TEXT NOT NULL DEFAULT '',
		log_offset INTEGER NOT NULL DEFAULT 0,
		active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)),
		FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
		FOREIGN KEY(export_id) REFERENCES exports(id) ON DELETE CASCADE
	)`
)

// migrateVersioned creates the complete normalized schema and advances the
// metadata marker only after every schema/index/data operation has succeeded.
// Legacy folder/preset/profile tables are copied into the unified collection
// graph in this transaction and dropped before the marker advances.
func (s *Store) migrateVersioned(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("SQLite store is not initialized")
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("enable SQLite foreign keys: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin SQLite migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	startingVersion, err := readExistingSchemaVersionTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("read SQLite schema version: %w", err)
	}
	if startingVersion > storeSchemaVersion {
		return fmt.Errorf("unsupported newer SQLite schema version %d (maximum supported is %d)", startingVersion, storeSchemaVersion)
	}
	if startingVersion < 0 {
		return fmt.Errorf("invalid SQLite schema version %d", startingVersion)
	}
	legacyOrganizationPresent, err := legacyOrganizationTablesPresentTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("inspect legacy organization tables: %w", err)
	}
	preV4TablesPresent := false
	if startingVersion < storeSchemaVersion {
		preV4TablesPresent, err = preV4TablesPresentTx(ctx, tx)
		if err != nil {
			return fmt.Errorf("inspect pre-v4 SQLite tables: %w", err)
		}
	}
	if err := createStoreSchemaTx(ctx, tx); err != nil {
		return fmt.Errorf("create SQLite schema: %w", err)
	}
	if err := ensureStoreColumnsTx(ctx, tx); err != nil {
		return fmt.Errorf("upgrade SQLite schema: %w", err)
	}
	if err := ensureSourceClassificationsTx(ctx, tx); err != nil {
		return fmt.Errorf("initialize source classifications: %w", err)
	}
	if err := backfillSourceIDsTx(ctx, tx); err != nil {
		return fmt.Errorf("backfill source IDs: %w", err)
	}
	if legacyOrganizationPresent {
		if err := migrateLegacyOrganizationTx(ctx, tx); err != nil {
			return fmt.Errorf("migrate legacy organization: %w", err)
		}
	}
	needsRebuild := false
	if startingVersion < storeSchemaVersion {
		needsRebuild = preV4TablesPresent
	} else if needsRebuild, err = v4SchemaNeedsRebuildTx(ctx, tx); err != nil {
		return fmt.Errorf("inspect SQLite v5 constraints: %w", err)
	}
	if needsRebuild {
		if err := rebuildV3ToV4Tx(ctx, tx); err != nil {
			return fmt.Errorf("rebuild SQLite schema v%d to v5: %w", startingVersion, err)
		}
	}
	if err := ensureV4ConstraintsTx(ctx, tx); err != nil {
		return fmt.Errorf("enforce SQLite constraints: %w", err)
	}
	if err := ensureVersionedAdditiveMigrationsTx(ctx, tx); err != nil {
		return fmt.Errorf("apply additive SQLite migrations: %w", err)
	}
	if err := rebuildLibrarySearchFTSTx(ctx, tx); err != nil {
		return fmt.Errorf("build library search index: %w", err)
	}
	if err := validateIntegrityTx(ctx, tx); err != nil {
		return fmt.Errorf("validate SQLite v5 integrity: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('schema_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.Itoa(storeSchemaVersion)); err != nil {
		return fmt.Errorf("record settings schema version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO library_index_metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, libraryFTSVersionKey, libraryFTSVersion); err != nil {
		return fmt.Errorf("record FTS version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO library_index_metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, libraryFTSFreshnessKey, "1"); err != nil {
		return fmt.Errorf("record FTS freshness: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=5`); err != nil {
		return fmt.Errorf("record SQLite user version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM schema_meta`); err != nil {
		return fmt.Errorf("clear SQLite schema version marker: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_meta(version) VALUES(?)`, storeSchemaVersion); err != nil {
		return fmt.Errorf("record SQLite schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit SQLite migration: %w", err)
	}
	return nil
}

func readExistingSchemaVersionTx(ctx context.Context, tx *sql.Tx) (int, error) {
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN EXISTS(
		SELECT 1 FROM sqlite_master WHERE type='table' AND name='schema_meta'
	) THEN 1 ELSE 0 END`).Scan(&present); err != nil {
		return 0, err
	}
	if present == 1 {
		rows, err := tx.QueryContext(ctx, `SELECT version FROM schema_meta`)
		if err != nil {
			return 0, err
		}
		version := 0
		found := false
		for rows.Next() {
			var value int
			if err := rows.Scan(&value); err != nil {
				return 0, err
			}
			if found && value != version {
				return 0, fmt.Errorf("schema_meta contains conflicting versions %d and %d", version, value)
			}
			version = value
			found = true
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if err := rows.Close(); err != nil {
			return 0, err
		}
		if found {
			var userVersion int
			if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
				return 0, err
			}
			if userVersion > storeSchemaVersion {
				return 0, fmt.Errorf("unsupported newer SQLite user version %d", userVersion)
			}
			var settingsPresent int
			if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN EXISTS(
				SELECT 1 FROM sqlite_master WHERE type='table' AND name='settings'
			) THEN 1 ELSE 0 END`).Scan(&settingsPresent); err != nil {
				return 0, err
			}
			if settingsPresent == 1 {
				var raw string
				if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='schema_version'`).Scan(&raw); err == nil {
					auxVersion, parseErr := strconv.Atoi(strings.TrimSpace(raw))
					if parseErr != nil {
						return 0, fmt.Errorf("settings schema_version is invalid: %q", raw)
					}
					if auxVersion > storeSchemaVersion {
						return 0, fmt.Errorf("unsupported newer settings schema version %d", auxVersion)
					}
				} else if !errors.Is(err, sql.ErrNoRows) {
					return 0, err
				}
			}
			return version, nil
		}
	}
	var settingsPresent int
	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN EXISTS(
		SELECT 1 FROM sqlite_master WHERE type='table' AND name='settings'
	) THEN 1 ELSE 0 END`).Scan(&settingsPresent); err != nil {
		return 0, err
	}
	if settingsPresent == 1 {
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='schema_version'`).Scan(&raw); err == nil {
			version, parseErr := strconv.Atoi(strings.TrimSpace(raw))
			if parseErr != nil {
				return 0, fmt.Errorf("settings schema_version is invalid: %q", raw)
			}
			return version, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	var userVersion int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		return 0, err
	}
	return userVersion, nil
}

func tableExistsTx(ctx context.Context, tx *sql.Tx, table string) (bool, error) {
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&present); err != nil {
		return false, err
	}
	return present != 0, nil
}
func legacyOrganizationTablesPresentTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	const query = `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN
		('library_folders','library_folder_entities','mod_presets','mod_preset_entities','mod_profiles','mod_profile_presets')`
	var count int
	if err := tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}
func preV4TablesPresentTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	placeholders := make([]string, len(v4RebuildTables))
	args := make([]interface{}, len(v4RebuildTables))
	for index, table := range v4RebuildTables {
		placeholders[index] = "?"
		args[index] = table
	}
	var count int
	query := `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN (` + strings.Join(placeholders, ",") + `)`
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func foreignKeyMatchesTx(ctx context.Context, tx *sql.Tx, table, column, parent, onDelete string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_list(`+quoteSQLiteIdentifier(table)+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, seq int
		var parentTable, from, to, onUpdate, deleteAction, match string
		if err := rows.Scan(&id, &seq, &parentTable, &from, &to, &onUpdate, &deleteAction, &match); err != nil {
			return false, err
		}
		if strings.EqualFold(from, column) && strings.EqualFold(parentTable, parent) && (onDelete == "" || strings.EqualFold(deleteAction, onDelete)) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func hasUniqueIndexTx(ctx context.Context, tx *sql.Tx, table, index string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA index_list(`+quoteSQLiteIdentifier(table)+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var seq, unique, partial int
		var origin, name string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return false, err
		}
		if name == index && unique == 1 {
			return true, nil
		}
	}
	return false, rows.Err()
}

func tableSQLTx(ctx context.Context, tx *sql.Tx, table string) (string, error) {
	var sqlText sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&sqlText)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return sqlText.String, nil
}

func eventsNeedRebuildTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	hasEntityForeignKey, err := foreignKeyMatchesTx(ctx, tx, "events", "entity_id", "entities", "")
	if err != nil {
		return false, err
	}
	return hasEntityForeignKey, nil
}

func v4SchemaNeedsRebuildTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	requiredForeignKeys := []struct {
		table, column, parent, onDelete string
	}{
		{"entities", "source_id", "source_classifications", "NO ACTION"},
		{"archive_links", "entity_id", "entities", "CASCADE"},
		{"archive_links", "artifact_id", "artifacts", "CASCADE"},
		{"archive_links", "source_id", "source_classifications", "NO ACTION"},
		{"collection_mods", "collection_id", "collections", "CASCADE"},
		{"collection_mods", "entity_id", "entities", "CASCADE"},
		{"collection_children", "parent_id", "collections", "CASCADE"},
		{"collection_children", "child_id", "collections", "CASCADE"},
		{"play_profile_collections", "profile_id", "play_profiles", "CASCADE"},
		{"play_profile_collections", "collection_id", "collections", "CASCADE"},
		{"test_installs", "workspace_id", "workspaces", "CASCADE"},
		{"test_installs", "export_id", "exports", "CASCADE"},
	}
	for _, required := range requiredForeignKeys {
		ok, err := foreignKeyMatchesTx(ctx, tx, required.table, required.column, required.parent, required.onDelete)
		if err != nil {
			return false, err
		}
		if !ok {
			return true, nil
		}
	}
	eventsNeedRebuild, err := eventsNeedRebuildTx(ctx, tx)
	if err != nil {
		return false, err
	}
	if eventsNeedRebuild {
		return true, nil
	}
	for _, required := range []struct{ table, index string }{
		{"archive_links", "archive_links_path_unique_idx"},
		{"artifacts", "artifacts_fingerprint_unique_idx"},
		{"test_installs", "test_installs_workspace_active_unique_idx"},
	} {
		ok, err := hasUniqueIndexTx(ctx, tx, required.table, required.index)
		if err != nil {
			return false, err
		}
		if !ok {
			return true, nil
		}
	}
	testSQL, err := tableSQLTx(ctx, tx, "test_installs")
	if err != nil {
		return false, err
	}
	normalized := strings.ReplaceAll(strings.ToLower(testSQL), " ", "")
	normalized = strings.ReplaceAll(normalized, "\n", "")
	normalized = strings.ReplaceAll(normalized, "\t", "")
	if !strings.Contains(normalized, "check(activein(0,1))") {
		return true, nil
	}
	collectionChildrenSQL, err := tableSQLTx(ctx, tx, "collection_children")
	if err != nil {
		return false, err
	}
	normalized = strings.ReplaceAll(strings.ToLower(collectionChildrenSQL), " ", "")
	normalized = strings.ReplaceAll(normalized, "\n", "")
	normalized = strings.ReplaceAll(normalized, "\t", "")
	if !strings.Contains(normalized, "check(parent_id<>child_id)") && !strings.Contains(normalized, "check(parent_id!=child_id)") {
		return true, nil
	}
	return false, nil
}

type migrationBackupTable struct {
	table, backup string
	columns       []string
	rows          int64
}

var v4RebuildTables = []string{
	"entities",
	"archive_links",
	"entity_assets",
	"events",
	"workspaces",
	"exports",
	"workspace_drafts",
	"virgil_sessions",
	"agent_runs",
	"agent_events",
	"collections",
	"collection_mods",
	"collection_children",
	"play_profiles",
	"play_profile_collections",
	"play_state",
	"mod_tag_entities",
	"mod_audits",
	"mod_audit_files",
	"virus_scans",
	"virus_scan_stages",
	"legacy_catalog_mods",
	"test_installs",
}

func tableColumnsTx(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+quoteSQLiteIdentifier(table)+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := []string{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	return columns, rows.Err()
}

func rebuildV3ToV4Tx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys=ON`); err != nil {
		return fmt.Errorf("defer foreign keys: %w", err)
	}
	backups := make([]migrationBackupTable, 0, len(v4RebuildTables))
	for index, table := range v4RebuildTables {
		columns, err := tableColumnsTx(ctx, tx, table)
		if err != nil {
			return fmt.Errorf("read %s columns: %w", table, err)
		}
		if len(columns) == 0 {
			continue
		}
		backup := fmt.Sprintf("__v4_migration_backup_%d", index)
		if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE `+quoteSQLiteIdentifier(backup)+` AS SELECT * FROM `+quoteSQLiteIdentifier(table)); err != nil {
			return fmt.Errorf("backup %s: %w", table, err)
		}
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteSQLiteIdentifier(backup)).Scan(&count); err != nil {
			return fmt.Errorf("count backup %s: %w", table, err)
		}
		backups = append(backups, migrationBackupTable{table: table, backup: backup, columns: columns, rows: count})
	}
	for index := len(v4RebuildTables) - 1; index >= 0; index-- {
		table := v4RebuildTables[index]
		if exists, err := tableExistsTx(ctx, tx, table); err != nil {
			return err
		} else if exists {
			if _, err := tx.ExecContext(ctx, `DROP TABLE `+quoteSQLiteIdentifier(table)); err != nil {
				return fmt.Errorf("drop old %s: %w", table, err)
			}
		}
	}
	if err := createStoreSchemaTx(ctx, tx); err != nil {
		return fmt.Errorf("recreate v4 schema after dependency closure drop: %w", err)
	}
	for _, backup := range backups {
		destinationColumns, err := tableColumnsTx(ctx, tx, backup.table)
		if err != nil {
			return fmt.Errorf("read rebuilt %s columns: %w", backup.table, err)
		}
		sourceColumns, err := tableColumnsTx(ctx, tx, backup.backup)
		if err != nil {
			return fmt.Errorf("read backup %s columns: %w", backup.table, err)
		}
		sourceSet := make(map[string]struct{}, len(sourceColumns))
		for _, column := range sourceColumns {
			sourceSet[strings.ToLower(column)] = struct{}{}
		}
		columns := make([]string, 0, len(destinationColumns))
		for _, column := range destinationColumns {
			if _, ok := sourceSet[strings.ToLower(column)]; ok {
				columns = append(columns, quoteSQLiteIdentifier(column))
			}
		}
		if len(columns) > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO `+quoteSQLiteIdentifier(backup.table)+`(`+strings.Join(columns, ",")+`) SELECT `+strings.Join(columns, ",")+` FROM `+quoteSQLiteIdentifier(backup.backup)); err != nil {
				return fmt.Errorf("restore %s: %w", backup.table, err)
			}
		}
		var restored int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteSQLiteIdentifier(backup.table)).Scan(&restored); err != nil {
			return fmt.Errorf("count restored %s: %w", backup.table, err)
		}
		if restored < backup.rows {
			return fmt.Errorf("restore %s lost rows: backed up %d, restored %d", backup.table, backup.rows, restored)
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE `+quoteSQLiteIdentifier(backup.backup)); err != nil {
			return fmt.Errorf("drop backup %s: %w", backup.table, err)
		}
	}
	if err := reconcileLibraryDuplicatesTx(ctx, tx); err != nil {
		return err
	}
	return nil
}

func ensureV4ConstraintsTx(ctx context.Context, tx *sql.Tx) error {
	if err := reconcileLibraryDuplicatesTx(ctx, tx); err != nil {
		return err
	}
	for _, statement := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS archive_links_path_unique_idx ON archive_links(path COLLATE NOCASE) WHERE TRIM(path)<>''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS artifacts_fingerprint_unique_idx ON artifacts(central_fingerprint COLLATE NOCASE) WHERE TRIM(central_fingerprint)<>''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS test_installs_workspace_active_unique_idx ON test_installs(workspace_id) WHERE active=1`,
		`CREATE INDEX IF NOT EXISTS test_installs_active_idx ON test_installs(workspace_id,active,installed_at)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func validateIntegrityTx(ctx context.Context, tx *sql.Tx) error {
	quickRows, err := tx.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return err
	}
	for quickRows.Next() {
		var result string
		if err := quickRows.Scan(&result); err != nil {
			_ = quickRows.Close()
			return err
		}
		if !strings.EqualFold(strings.TrimSpace(result), "ok") {
			_ = quickRows.Close()
			return fmt.Errorf("PRAGMA quick_check: %s", result)
		}
	}
	if err := quickRows.Err(); err != nil {
		_ = quickRows.Close()
		return err
	}
	if err := quickRows.Close(); err != nil {
		return err
	}
	foreignRows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer foreignRows.Close()
	if foreignRows.Next() {
		var table, parent string
		var rowID, foreignKeyID sql.NullInt64
		if err := foreignRows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			return err
		}
		return fmt.Errorf("PRAGMA foreign_key_check: table=%q parent=%q rowid=%s fk=%s", table, parent, nullableIntString(rowID), nullableIntString(foreignKeyID))
	}
	return foreignRows.Err()
}

type migrationArchiveLink struct {
	id, entityID, artifactID, path, lastSeenAt string
	active                                     int
}

func reconcileLibraryDuplicatesTx(ctx context.Context, tx *sql.Tx) error {
	pathRows, err := tx.QueryContext(ctx, `SELECT path FROM archive_links WHERE TRIM(path)<>'' GROUP BY path COLLATE NOCASE HAVING COUNT(*)>1`)
	if err != nil {
		return err
	}
	paths := []string{}
	for pathRows.Next() {
		var path string
		if err := pathRows.Scan(&path); err != nil {
			_ = pathRows.Close()
			return err
		}
		paths = append(paths, path)
	}
	if err := pathRows.Err(); err != nil {
		_ = pathRows.Close()
		return err
	}
	if err := pathRows.Close(); err != nil {
		return err
	}
	for _, path := range paths {
		rows, err := tx.QueryContext(ctx, `SELECT id,entity_id,artifact_id,path,active,last_seen_at
			FROM archive_links WHERE path=? COLLATE NOCASE
			ORDER BY active DESC,last_seen_at DESC,id ASC`, path)
		if err != nil {
			return err
		}
		links := []migrationArchiveLink{}
		for rows.Next() {
			var link migrationArchiveLink
			if err := rows.Scan(&link.id, &link.entityID, &link.artifactID, &link.path, &link.active, &link.lastSeenAt); err != nil {
				_ = rows.Close()
				return err
			}
			links = append(links, link)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(links) < 2 {
			continue
		}
		canonical := links[0]
		for _, duplicate := range links[1:] {
			if duplicate.entityID != canonical.entityID {
				if err := mergeEntityRecordsTx(ctx, tx, duplicate.entityID, canonical.entityID); err != nil {
					return fmt.Errorf("merge entities %q and %q: %w", duplicate.entityID, canonical.entityID, err)
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM archive_links WHERE id=?`, duplicate.id); err != nil {
				return fmt.Errorf("remove duplicate archive link %q: %w", duplicate.id, err)
			}
		}
	}
	fingerprintRows, err := tx.QueryContext(ctx, `SELECT central_fingerprint FROM artifacts
		WHERE TRIM(central_fingerprint)<>'' GROUP BY central_fingerprint COLLATE NOCASE HAVING COUNT(*)>1`)
	if err != nil {
		return err
	}
	fingerprints := []string{}
	for fingerprintRows.Next() {
		var fingerprint string
		if err := fingerprintRows.Scan(&fingerprint); err != nil {
			_ = fingerprintRows.Close()
			return err
		}
		fingerprints = append(fingerprints, fingerprint)
	}
	if err := fingerprintRows.Err(); err != nil {
		_ = fingerprintRows.Close()
		return err
	}
	if err := fingerprintRows.Close(); err != nil {
		return err
	}
	for _, fingerprint := range fingerprints {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM artifacts WHERE central_fingerprint=? COLLATE NOCASE ORDER BY id`, fingerprint)
		if err != nil {
			return err
		}
		artifactIDs := []string{}
		for rows.Next() {
			var artifactID string
			if err := rows.Scan(&artifactID); err != nil {
				_ = rows.Close()
				return err
			}
			artifactIDs = append(artifactIDs, artifactID)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(artifactIDs) < 2 {
			continue
		}
		for _, duplicateID := range artifactIDs[1:] {
			if err := mergeArtifactRecordsTx(ctx, tx, duplicateID, artifactIDs[0]); err != nil {
				return fmt.Errorf("merge artifacts %q and %q: %w", duplicateID, artifactIDs[0], err)
			}
		}
	}
	entityFingerprintRows, err := tx.QueryContext(ctx, `SELECT a.central_fingerprint FROM artifacts a
		JOIN archive_links l ON l.artifact_id=a.id
		WHERE TRIM(a.central_fingerprint)<>'' GROUP BY a.central_fingerprint COLLATE NOCASE
		HAVING COUNT(DISTINCT l.entity_id)>1`)
	if err != nil {
		return err
	}
	entityFingerprints := []string{}
	for entityFingerprintRows.Next() {
		var fingerprint string
		if err := entityFingerprintRows.Scan(&fingerprint); err != nil {
			_ = entityFingerprintRows.Close()
			return err
		}
		entityFingerprints = append(entityFingerprints, fingerprint)
	}
	if err := entityFingerprintRows.Err(); err != nil {
		_ = entityFingerprintRows.Close()
		return err
	}
	if err := entityFingerprintRows.Close(); err != nil {
		return err
	}
	for _, fingerprint := range entityFingerprints {
		rows, err := tx.QueryContext(ctx, `SELECT l.entity_id,MAX(l.active),MAX(l.last_seen_at)
			FROM archive_links l JOIN artifacts a ON a.id=l.artifact_id
			WHERE a.central_fingerprint=? COLLATE NOCASE
			GROUP BY l.entity_id ORDER BY MAX(l.active) DESC,MAX(l.last_seen_at) DESC,l.entity_id ASC`, fingerprint)
		if err != nil {
			return err
		}
		type fingerprintEntity struct {
			id, lastSeen string
			active       int
		}
		entities := []fingerprintEntity{}
		for rows.Next() {
			var entity fingerprintEntity
			if err := rows.Scan(&entity.id, &entity.active, &entity.lastSeen); err != nil {
				_ = rows.Close()
				return err
			}
			entities = append(entities, entity)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(entities) < 2 {
			continue
		}
		for _, duplicate := range entities[1:] {
			if err := mergeEntityRecordsTx(ctx, tx, duplicate.id, entities[0].id); err != nil {
				return fmt.Errorf("merge fingerprint entities %q and %q: %w", duplicate.id, entities[0].id, err)
			}
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT workspace_id FROM test_installs WHERE active=1 GROUP BY workspace_id HAVING COUNT(*)>1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var workspaceID string
		if err := rows.Scan(&workspaceID); err != nil {
			return err
		}
		return fmt.Errorf("multiple active test installs for workspace %q", workspaceID)
	}
	return rows.Err()
}

func mergeEntityRecordsTx(ctx context.Context, tx *sql.Tx, fromID, toID string) error {
	fromID = strings.TrimSpace(fromID)
	toID = strings.TrimSpace(toID)
	if fromID == "" || toID == "" || fromID == toID {
		return nil
	}
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities WHERE id=?`, toID).Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		return fmt.Errorf("canonical entity %q does not exist", toID)
	}
	var toName, toKind, toSource, toCreated, toUpdated string
	var fromName, fromKind, fromSource, fromCreated, fromUpdated string
	if err := tx.QueryRowContext(ctx, `SELECT display_name,kind,source_id,created_at,updated_at FROM entities WHERE id=?`, toID).Scan(&toName, &toKind, &toSource, &toCreated, &toUpdated); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT display_name,kind,source_id,created_at,updated_at FROM entities WHERE id=?`, fromID).Scan(&fromName, &fromKind, &fromSource, &fromCreated, &fromUpdated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if toName == "" {
		toName = fromName
	}
	if toKind == "" || toKind == "unknown" {
		toKind = fromKind
	}
	if toSource == "" {
		toSource = fromSource
	}
	if toCreated == "" {
		toCreated = fromCreated
	}
	if toUpdated == "" {
		toUpdated = fromUpdated
	}
	if _, err := tx.ExecContext(ctx, `UPDATE entities SET display_name=?,kind=?,source_id=?,created_at=?,updated_at=? WHERE id=?`, toName, toKind, toSource, toCreated, toUpdated, toID); err != nil {
		return err
	}
	assetRows, err := tx.QueryContext(ctx, `SELECT asset_sha256,role,ordinal FROM entity_assets WHERE entity_id=?`, fromID)
	if err != nil {
		return err
	}
	type entityAssetRef struct {
		sha, role string
		ordinal   int
	}
	assetRefs := []entityAssetRef{}
	for assetRows.Next() {
		var ref entityAssetRef
		if err := assetRows.Scan(&ref.sha, &ref.role, &ref.ordinal); err != nil {
			_ = assetRows.Close()
			return err
		}
		assetRefs = append(assetRefs, ref)
	}
	if err := assetRows.Err(); err != nil {
		_ = assetRows.Close()
		return err
	}
	if err := assetRows.Close(); err != nil {
		return err
	}
	for _, ref := range assetRefs {
		var existingSHA string
		err := tx.QueryRowContext(ctx, `SELECT asset_sha256 FROM entity_assets WHERE entity_id=? AND role=? AND ordinal=?`, toID, ref.role, ref.ordinal).Scan(&existingSHA)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO entity_assets(entity_id,asset_sha256,role,ordinal) VALUES(?,?,?,?)`, toID, ref.sha, ref.role, ref.ordinal); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if existingSHA == ref.sha {
			continue
		}
		var nextOrdinal int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),-1)+1 FROM entity_assets WHERE entity_id=? AND role=?`, toID, ref.role).Scan(&nextOrdinal); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO entity_assets(entity_id,asset_sha256,role,ordinal) VALUES(?,?,?,?)`, toID, ref.sha, ref.role, nextOrdinal); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM entity_assets WHERE entity_id=?`, fromID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO collection_mods(collection_id,entity_id,position)
		SELECT collection_id,?,position FROM collection_mods WHERE entity_id=?`, toID, fromID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM collection_mods WHERE entity_id=?`, fromID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO mod_tag_entities(tag_id,entity_id,created_at)
		SELECT tag_id,?,created_at FROM mod_tag_entities WHERE entity_id=?`, toID, fromID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mod_tag_entities WHERE entity_id=?`, fromID); err != nil {
		return err
	}
	for _, statement := range []string{
		`UPDATE archive_links SET entity_id=? WHERE entity_id=?`,
		`UPDATE events SET entity_id=? WHERE entity_id=?`,
		`UPDATE workspaces SET entity_id=? WHERE entity_id=?`,
		`UPDATE mod_audits SET entity_id=? WHERE entity_id=?`,
		`UPDATE virus_scans SET entity_id=? WHERE entity_id=?`,
		`UPDATE virus_scan_stages SET entity_id=? WHERE entity_id=?`,
		`UPDATE legacy_catalog_mods SET entity_id=? WHERE entity_id=?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, toID, fromID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_search_fts WHERE entity_id=?`, fromID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM entities WHERE id=?`, fromID); err != nil {
		return err
	}
	return nil
}

func mergeArtifactRecordsTx(ctx context.Context, tx *sql.Tx, fromID, toID string) error {
	fromID = strings.TrimSpace(fromID)
	toID = strings.TrimSpace(toID)
	if fromID == "" || toID == "" || fromID == toID {
		return nil
	}
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE id=?`, toID).Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		return fmt.Errorf("canonical artifact %q does not exist", toID)
	}
	var toSHA, toManifest, toAnalyzer, toAnalyzed string
	var fromSHA, fromManifest, fromAnalyzer, fromAnalyzed string
	var toSize, fromSize int64
	if err := tx.QueryRowContext(ctx, `SELECT sha256,size_bytes,manifest_json,analyzer_version,analyzed_at FROM artifacts WHERE id=?`, toID).Scan(&toSHA, &toSize, &toManifest, &toAnalyzer, &toAnalyzed); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT sha256,size_bytes,manifest_json,analyzer_version,analyzed_at FROM artifacts WHERE id=?`, fromID).Scan(&fromSHA, &fromSize, &fromManifest, &fromAnalyzer, &fromAnalyzed); err != nil {
		return err
	}
	if toSHA == "" {
		toSHA = fromSHA
	}
	if toSize == 0 {
		toSize = fromSize
	}
	if toManifest == "" || toManifest == "{}" {
		toManifest = fromManifest
	}
	if toAnalyzer == "" {
		toAnalyzer = fromAnalyzer
	}
	if toAnalyzed == "" {
		toAnalyzed = fromAnalyzed
	}
	if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET sha256=?,size_bytes=?,manifest_json=?,analyzer_version=?,analyzed_at=? WHERE id=?`, toSHA, toSize, toManifest, toAnalyzer, toAnalyzed, toID); err != nil {
		return err
	}
	for _, statement := range []string{
		`UPDATE archive_links SET artifact_id=? WHERE artifact_id=?`,
		`UPDATE workspaces SET artifact_id=? WHERE artifact_id=?`,
		`UPDATE exports SET artifact_id=? WHERE artifact_id=?`,
		`UPDATE mod_audits SET artifact_id=? WHERE artifact_id=?`,
		`UPDATE virus_scans SET artifact_id=? WHERE artifact_id=?`,
		`UPDATE virus_scan_stages SET artifact_id=? WHERE artifact_id=?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, toID, fromID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM artifacts WHERE id=?`, fromID); err != nil {
		return err
	}
	return nil
}

// ensureVersionedAdditiveMigrationsTx keeps the older additive migrations in
// the same transaction as schema_meta. The individual helpers remain
// idempotent, but none can commit independently or leave schema_meta at v4
// while its schema/data backfill is incomplete.
func ensureVersionedAdditiveMigrationsTx(ctx context.Context, tx *sql.Tx) error {
	if err := ensureColumnTx(ctx, tx, "entities", "archived_at", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("entity archived_at column: %w", err)
	}
	if err := ensureColumnTx(ctx, tx, "workspaces", "virgil_configured", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("workspace virgil_configured column: %w", err)
	}
	if err := ensureColumnTx(ctx, tx, "workspaces", "virgil_enabled", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("workspace virgil_enabled column: %w", err)
	}
	if err := ensureColumnTx(ctx, tx, "workspace_drafts", "base_sha256", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("workspace draft base_sha256 column: %w", err)
	}
	if err := ensureColumnTx(ctx, tx, "virgil_sessions", "profile", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("Virgil session profile column: %w", err)
	}
	if err := ensureColumnTx(ctx, tx, "agent_runs", "session_id", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("agent run session_id column: %w", err)
	}
	// Membership rows carry their own enabled flag. Existing rows are enabled,
	// which is what they effectively were before the column existed.
	if err := ensureColumnTx(ctx, tx, "collection_mods", "enabled", `INTEGER NOT NULL DEFAULT 1`); err != nil {
		return fmt.Errorf("collection mod enabled column: %w", err)
	}
	// Archive-driven disablement is tracked separately so restoring a mod
	// re-enables only the memberships that archiving disabled, leaving
	// user-disabled memberships untouched.
	if err := ensureColumnTx(ctx, tx, "collection_mods", "disabled_by_archive", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("collection mod disabled_by_archive column: %w", err)
	}
	if err := ensureColumnTx(ctx, tx, "collection_children", "enabled", `INTEGER NOT NULL DEFAULT 1`); err != nil {
		return fmt.Errorf("collection child enabled column: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS agent_runs_session_idx ON agent_runs(session_id, started_at, id)`); err != nil {
		return fmt.Errorf("agent run session index: %w", err)
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
		return fmt.Errorf("legacy Virgil session backfill: %w", err)
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
		return fmt.Errorf("Virgil session identity backfill: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET session_id='legacy-' || workspace_id WHERE TRIM(COALESCE(session_id,''))=''`); err != nil {
		return fmt.Errorf("agent run session backfill: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE virgil_sessions SET title=CASE
		WHEN TRIM(user_title)<>'' THEN TRIM(user_title)
		WHEN TRIM(omp_title)<>'' THEN TRIM(omp_title)
		ELSE 'Virgil session' END`); err != nil {
		return fmt.Errorf("Virgil session title backfill: %w", err)
	}
	if err := ensureColumnTx(ctx, tx, "virus_scans", "file_sha256", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("virus scan file_sha256 column: %w", err)
	}
	if err := ensureColumnTx(ctx, tx, "virus_scan_stages", "file_sha256", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("virus scan stage file_sha256 column: %w", err)
	}
	if err := ensureColumnTx(ctx, tx, "mod_tags", "color", `TEXT NOT NULL DEFAULT '#7a8791'`); err != nil {
		return fmt.Errorf("mod tag color column: %w", err)
	}
	if err := ensureColumnTx(ctx, tx, "mod_tags", "icon", `TEXT NOT NULL DEFAULT 'tag'`); err != nil {
		return fmt.Errorf("mod tag icon column: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mod_tags SET color=? WHERE color IS NULL OR TRIM(color)=''`, defaultModTagColor); err != nil {
		return fmt.Errorf("mod tag color backfill: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mod_tags SET icon=? WHERE icon IS NULL OR TRIM(icon)=''`, defaultModTagIcon); err != nil {
		return fmt.Errorf("mod tag icon backfill: %w", err)
	}
	if err := ensureColumnTx(ctx, tx, "mod_tags", "origin", `TEXT NOT NULL DEFAULT 'user'`); err != nil {
		return fmt.Errorf("mod tag origin column: %w", err)
	}
	if err := ensureColumnTx(ctx, tx, "mod_tags", "grouped", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("mod tag grouped column: %w", err)
	}
	// Play profile collections carry an excluded flag so saved profiles can
	// remember both included and excluded collection lists.
	if err := ensureColumnTx(ctx, tx, "play_profile_collections", "excluded", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("play profile collection excluded column: %w", err)
	}
	// Sentinel flags on the profile row itself (not the FK-constrained junction
	// table) so the all-mods virtual collection survives a round-trip.
	if err := ensureColumnTx(ctx, tx, "play_profiles", "includes_all_mods", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("play profile includes_all_mods column: %w", err)
	}
	if err := ensureColumnTx(ctx, tx, "play_profiles", "excludes_all_mods", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("play profile excludes_all_mods column: %w", err)
	}
	// IF NOT EXISTS cannot upgrade the older three-column index in place.
	var primaryIndexSQL string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND name='archive_links_entity_idx'`).Scan(&primaryIndexSQL); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect primary archive index: %w", err)
	}
	if !strings.Contains(strings.Join(strings.Fields(strings.ToLower(primaryIndexSQL)), ""), "(entity_id,activedesc,last_seen_atdesc,iddesc)") {
		if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS archive_links_entity_idx`); err != nil {
			return fmt.Errorf("replace primary archive index: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `CREATE INDEX archive_links_entity_idx ON archive_links(entity_id,active DESC,last_seen_at DESC,id DESC)`); err != nil {
			return fmt.Errorf("create primary archive index: %w", err)
		}
	}
	// Backfill artifact_summaries for any existing artifacts that lack a
	// summary row.  The triggers maintain the projection going forward; this
	// INSERT OR IGNORE populates the table exactly once per artifact.
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO artifact_summaries(
		artifact_id,revision,title,author,version,description,
		namespaces_json,issues_json,
		member_count,namespace_count,variant_count,issue_count
	)
	SELECT
		a.id,
		hex(randomblob(16)),
		COALESCE(json_extract(a.manifest_json,'$.title'),''),
		COALESCE(json_extract(a.manifest_json,'$.author'),''),
		COALESCE(json_extract(a.manifest_json,'$.version'),''),
		COALESCE(json_extract(a.manifest_json,'$.description'),''),
		COALESCE(json_extract(a.manifest_json,'$.namespaces'),'{}'),
		COALESCE(json_extract(a.manifest_json,'$.issues'),'[]'),
		COALESCE(json_array_length(json_extract(a.manifest_json,'$.members')),0),
		(SELECT COUNT(*) FROM json_each(COALESCE(json_extract(a.manifest_json,'$.namespaces'),'{}'))),
		COALESCE(json_array_length(json_extract(a.manifest_json,'$.variants')),0),
		COALESCE(json_array_length(json_extract(a.manifest_json,'$.issues')),0)
	FROM artifacts a
	WHERE a.id NOT IN (SELECT artifact_id FROM artifact_summaries)`); err != nil {
		return fmt.Errorf("backfill artifact summaries: %w", err)
	}
	var seeded string
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, exampleTagSeedKey).Scan(&seeded)
	if err == nil {
		if err := ensureAdditionalDefaultModTagsTx(ctx, tx); err != nil {
			return fmt.Errorf("seed additional default tags: %w", err)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check example tag seed marker: %w", err)
	}
	for _, name := range exampleModTagNames {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mod_tags(id,name,color,icon,created_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(name) DO NOTHING`, "example-"+tagIDPart(name), name, defaultModTagColor, defaultModTagIcon, nowUTC(), nowUTC()); err != nil {
			return fmt.Errorf("seed example tag %q: %w", name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, exampleTagSeedKey, "1"); err != nil {
		return fmt.Errorf("record example tag seed marker: %w", err)
	}
	if err := ensureAdditionalDefaultModTagsTx(ctx, tx); err != nil {
		return fmt.Errorf("seed additional default tags: %w", err)
	}
	return nil
}

type legacyOrganizationFolderRow struct {
	id, name, parentID, createdAt, updatedAt string
	position                                 int
}

type legacyOrganizationPresetRow struct {
	id, name, description, createdAt, updatedAt string
}

type legacyOrganizationProfileRow struct {
	id, name, defaultPresetID, createdAt, updatedAt string
}

type legacyOrganizationPresetMember struct {
	entityID string
	position int
}

type legacyOrganizationProfileMember struct {
	profileID, presetID string
	position            int
}

// migrateLegacyOrganizationTx performs the only live-table cutover from the
// former folder/preset/profile model. Every read, copy, graph validation, and
// old-table drop occurs under the caller's migration transaction; returning an
// error therefore rolls the complete conversion back to the untouched legacy
// schema.
func migrateLegacyOrganizationTx(ctx context.Context, tx *sql.Tx) error {
	folders := []legacyOrganizationFolderRow{}
	if present, err := tableExistsTx(ctx, tx, "library_folders"); err != nil {
		return err
	} else if present {
		rows, err := tx.QueryContext(ctx, `SELECT id,name,COALESCE(parent_id,''),position,created_at,updated_at FROM library_folders ORDER BY position,name COLLATE NOCASE,id`)
		if err != nil {
			return fmt.Errorf("read legacy folders: %w", err)
		}
		for rows.Next() {
			var row legacyOrganizationFolderRow
			if err := rows.Scan(&row.id, &row.name, &row.parentID, &row.position, &row.createdAt, &row.updatedAt); err != nil {
				_ = rows.Close()
				return err
			}
			folders = append(folders, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}

	presets := []legacyOrganizationPresetRow{}
	if present, err := tableExistsTx(ctx, tx, "mod_presets"); err != nil {
		return err
	} else if present {
		rows, err := tx.QueryContext(ctx, `SELECT id,name,description,created_at,updated_at FROM mod_presets ORDER BY created_at,name COLLATE NOCASE,id`)
		if err != nil {
			return fmt.Errorf("read legacy presets: %w", err)
		}
		for rows.Next() {
			var row legacyOrganizationPresetRow
			if err := rows.Scan(&row.id, &row.name, &row.description, &row.createdAt, &row.updatedAt); err != nil {
				_ = rows.Close()
				return err
			}
			presets = append(presets, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}

	presetMembers := map[string][]legacyOrganizationPresetMember{}
	if present, err := tableExistsTx(ctx, tx, "mod_preset_entities"); err != nil {
		return err
	} else if present {
		rows, err := tx.QueryContext(ctx, `SELECT preset_id,entity_id,position FROM mod_preset_entities ORDER BY preset_id,position,entity_id`)
		if err != nil {
			return fmt.Errorf("read legacy preset memberships: %w", err)
		}
		for rows.Next() {
			var presetID, entityID string
			var position int
			if err := rows.Scan(&presetID, &entityID, &position); err != nil {
				_ = rows.Close()
				return err
			}
			presetMembers[presetID] = append(presetMembers[presetID], legacyOrganizationPresetMember{entityID: entityID, position: position})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}

	profiles := []legacyOrganizationProfileRow{}
	if present, err := tableExistsTx(ctx, tx, "mod_profiles"); err != nil {
		return err
	} else if present {
		rows, err := tx.QueryContext(ctx, `SELECT id,name,default_preset_id,created_at,updated_at FROM mod_profiles ORDER BY id`)
		if err != nil {
			return fmt.Errorf("read legacy profiles: %w", err)
		}
		for rows.Next() {
			var row legacyOrganizationProfileRow
			if err := rows.Scan(&row.id, &row.name, &row.defaultPresetID, &row.createdAt, &row.updatedAt); err != nil {
				_ = rows.Close()
				return err
			}
			profiles = append(profiles, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}

	profileMembers := []legacyOrganizationProfileMember{}
	if present, err := tableExistsTx(ctx, tx, "mod_profile_presets"); err != nil {
		return err
	} else if present {
		// Deliberately do not filter selected here. Existing organization
		// behavior used row existence to determine the effective set.
		rows, err := tx.QueryContext(ctx, `SELECT profile_id,preset_id,position FROM mod_profile_presets ORDER BY profile_id,position,preset_id`)
		if err != nil {
			return fmt.Errorf("read legacy profile memberships: %w", err)
		}
		for rows.Next() {
			var row legacyOrganizationProfileMember
			if err := rows.Scan(&row.profileID, &row.presetID, &row.position); err != nil {
				_ = rows.Close()
				return err
			}
			profileMembers = append(profileMembers, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}

	legacyAdjacency := map[string][]string{}
	legacyFolderIDs := map[string]struct{}{}
	for _, folder := range folders {
		legacyAdjacency[folder.id] = []string{}
		legacyFolderIDs[folder.id] = struct{}{}
	}
	for _, folder := range folders {
		if folder.parentID == "" {
			continue
		}
		if _, ok := legacyFolderIDs[folder.parentID]; !ok {
			return fmt.Errorf("legacy folder %q references missing parent %q", folder.id, folder.parentID)
		}
		legacyAdjacency[folder.parentID] = append(legacyAdjacency[folder.parentID], folder.id)
	}
	if err := validateCollectionAdjacency(legacyAdjacency); err != nil {
		return fmt.Errorf("legacy folder graph is invalid: %w", err)
	}

	usedCollectionIDs := map[string]struct{}{}
	usedCollectionNames := map[string]struct{}{}
	rows, err := tx.QueryContext(ctx, `SELECT id,name FROM collections`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			_ = rows.Close()
			return err
		}
		usedCollectionIDs[id] = struct{}{}
		usedCollectionNames[sqliteNoCaseKey(name)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	allocateCollectionID := func(namespace, oldID string) string {
		candidate := oldID
		if _, exists := usedCollectionIDs[candidate]; exists {
			candidate = stableLegacyID("legacy-"+namespace, oldID)
			for suffix := 2; ; suffix++ {
				if _, exists := usedCollectionIDs[candidate]; !exists {
					break
				}
				candidate = stableLegacyID(fmt.Sprintf("legacy-%s-%d", namespace, suffix), oldID)
			}
		}
		usedCollectionIDs[candidate] = struct{}{}
		return candidate
	}
	allocateCollectionName := func(base string) string {
		base = strings.TrimSpace(base)
		if base == "" {
			base = "Untitled collection"
		}
		if len(base) > 80 {
			base = base[:80]
		}
		candidate := base
		for suffix := 2; ; suffix++ {
			key := sqliteNoCaseKey(candidate)
			if _, exists := usedCollectionNames[key]; !exists {
				usedCollectionNames[key] = struct{}{}
				return candidate
			}
			extra := fmt.Sprintf(" (%d)", suffix)
			prefix := base
			if len(prefix)+len(extra) > 80 {
				prefix = strings.TrimSpace(prefix[:80-len(extra)])
			}
			candidate = prefix + extra
		}
	}

	folderCollectionIDs := map[string]string{}
	for _, folder := range folders {
		folderCollectionIDs[folder.id] = allocateCollectionID("folder", folder.id)
	}
	extraNameByPreset := map[string]string{}
	skipPreset := map[string]struct{}{}
	for _, profile := range profiles {
		if len(presetMembers[profile.defaultPresetID]) == 0 {
			skipPreset[profile.defaultPresetID] = struct{}{}
			continue
		}
		if _, exists := extraNameByPreset[profile.defaultPresetID]; !exists {
			extraNameByPreset[profile.defaultPresetID] = strings.TrimSpace(profile.name) + " - Extra mods"
		}
	}
	presetCollectionIDs := map[string]string{}
	for _, preset := range presets {
		if _, skip := skipPreset[preset.id]; skip && len(presetMembers[preset.id]) == 0 {
			continue
		}
		presetCollectionIDs[preset.id] = allocateCollectionID("preset", preset.id)
	}

	for index, folder := range folders {
		collectionID := folderCollectionIDs[folder.id]
		name := allocateCollectionName(folder.name)
		createdAt, updatedAt := folder.createdAt, folder.updatedAt
		if createdAt == "" {
			createdAt = nowUTC()
		}
		if updatedAt == "" {
			updatedAt = createdAt
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO collections(id,name,description,position,cover_json,cover_asset_sha,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, collectionID, name, "", index, automaticCollectionCover, "", createdAt, updatedAt); err != nil {
			return fmt.Errorf("insert migrated folder %q: %w", folder.name, err)
		}
	}
	presetIndex := len(folders)
	for _, preset := range presets {
		collectionID, ok := presetCollectionIDs[preset.id]
		if !ok {
			continue
		}
		name := preset.name
		if extraName, exists := extraNameByPreset[preset.id]; exists {
			name = extraName
		}
		name = allocateCollectionName(name)
		createdAt, updatedAt := preset.createdAt, preset.updatedAt
		if createdAt == "" {
			createdAt = nowUTC()
		}
		if updatedAt == "" {
			updatedAt = createdAt
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO collections(id,name,description,position,cover_json,cover_asset_sha,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, collectionID, name, preset.description, presetIndex, automaticCollectionCover, "", createdAt, updatedAt); err != nil {
			return fmt.Errorf("insert migrated preset %q: %w", preset.name, err)
		}
		presetIndex++
	}
	for _, folder := range folders {
		if folder.parentID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO collection_children(parent_id,child_id,position) VALUES(?,?,?)`, folderCollectionIDs[folder.parentID], folderCollectionIDs[folder.id], folder.position); err != nil {
			return fmt.Errorf("migrate folder relationship %q -> %q: %w", folder.parentID, folder.id, err)
		}
	}
	if present, err := tableExistsTx(ctx, tx, "library_folder_entities"); err != nil {
		return err
	} else if present {
		rows, err := tx.QueryContext(ctx, `SELECT folder_id,entity_id,position FROM library_folder_entities ORDER BY folder_id,position,entity_id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var folderID, entityID string
			var position int
			if err := rows.Scan(&folderID, &entityID, &position); err != nil {
				_ = rows.Close()
				return err
			}
			collectionID, ok := folderCollectionIDs[folderID]
			if !ok {
				_ = rows.Close()
				return fmt.Errorf("legacy folder membership references missing folder %q", folderID)
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO collection_mods(collection_id,entity_id,position) VALUES(?,?,?)`, collectionID, entityID, position); err != nil {
				_ = rows.Close()
				return fmt.Errorf("migrate folder membership %q: %w", folderID, err)
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
	for _, preset := range presets {
		collectionID, ok := presetCollectionIDs[preset.id]
		if !ok {
			continue
		}
		for _, member := range presetMembers[preset.id] {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO collection_mods(collection_id,entity_id,position) VALUES(?,?,?)`, collectionID, member.entityID, member.position); err != nil {
				return fmt.Errorf("migrate preset membership %q: %w", preset.id, err)
			}
		}
	}

	usedProfileIDs := map[string]struct{}{}
	usedProfileNames := map[string]struct{}{}
	rows, err = tx.QueryContext(ctx, `SELECT id,name FROM play_profiles`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			_ = rows.Close()
			return err
		}
		usedProfileIDs[id] = struct{}{}
		usedProfileNames[sqliteNoCaseKey(name)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	allocateProfileID := func(oldID string) string {
		candidate := oldID
		if _, exists := usedProfileIDs[candidate]; exists {
			candidate = stableLegacyID("legacy-profile", oldID)
		}
		for suffix := 2; ; suffix++ {
			if _, exists := usedProfileIDs[candidate]; !exists {
				break
			}
			candidate = stableLegacyID(fmt.Sprintf("legacy-profile-%d", suffix), oldID)
		}
		usedProfileIDs[candidate] = struct{}{}
		return candidate
	}
	allocateProfileName := func(base string) string {
		base = strings.TrimSpace(base)
		if base == "" {
			base = "Profile"
		}
		if strings.EqualFold(base, "Default") {
			base = "Default (2)"
		}
		if len(base) > 80 {
			base = base[:80]
		}
		candidate := base
		for suffix := 2; ; suffix++ {
			key := sqliteNoCaseKey(candidate)
			if _, exists := usedProfileNames[key]; !exists && !strings.EqualFold(candidate, "Default") {
				usedProfileNames[key] = struct{}{}
				return candidate
			}
			extra := fmt.Sprintf(" (%d)", suffix)
			prefix := base
			if len(prefix)+len(extra) > 80 {
				prefix = strings.TrimSpace(prefix[:80-len(extra)])
			}
			candidate = prefix + extra
		}
	}
	profileIDs := map[string]string{}
	for _, profile := range profiles {
		profileIDs[profile.id] = allocateProfileID(profile.id)
	}
	for _, profile := range profiles {
		profileID := profileIDs[profile.id]
		name := allocateProfileName(profile.name)
		createdAt, updatedAt := profile.createdAt, profile.updatedAt
		if createdAt == "" {
			createdAt = nowUTC()
		}
		if updatedAt == "" {
			updatedAt = createdAt
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO play_profiles(id,name,created_at,updated_at) VALUES(?,?,?,?)`, profileID, name, createdAt, updatedAt); err != nil {
			return fmt.Errorf("insert migrated profile %q: %w", profile.name, err)
		}
	}
	for _, member := range profileMembers {
		profileID, profileOK := profileIDs[member.profileID]
		collectionID, collectionOK := presetCollectionIDs[member.presetID]
		if !profileOK {
			return fmt.Errorf("legacy profile membership references missing profile %q", member.profileID)
		}
		if !collectionOK {
			// Empty private defaults intentionally disappear.
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO play_profile_collections(profile_id,collection_id,position) VALUES(?,?,?)`, profileID, collectionID, member.position); err != nil {
			return fmt.Errorf("migrate profile membership %q: %w", member.profileID, err)
		}
	}
	for _, table := range []string{"mod_profile_presets", "mod_profiles", "mod_preset_entities", "mod_presets", "library_folder_entities", "library_folders"} {
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+quoteSQLiteIdentifier(table)); err != nil {
			return fmt.Errorf("drop legacy organization table %q: %w", table, err)
		}
	}
	return nil
}

func createStoreSchemaTx(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_meta (
			version INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS source_classifications (
			id TEXT PRIMARY KEY,
			label TEXT NOT NULL UNIQUE COLLATE NOCASE,
			semantics TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS library_index_metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		v4EntitiesTableDDL,
		`CREATE TABLE IF NOT EXISTS legacy_catalog_state (
			id INTEGER PRIMARY KEY CHECK(id=1),
			status TEXT NOT NULL CHECK(status IN ('absent','imported')),
			schema_version INTEGER NOT NULL DEFAULT 0,
			catalog_json TEXT NOT NULL DEFAULT '',
			last_scan_present INTEGER NOT NULL DEFAULT 0 CHECK(last_scan_present IN (0,1)),
			last_scan_is_null INTEGER NOT NULL DEFAULT 0 CHECK(last_scan_is_null IN (0,1)),
			last_scan_json TEXT NOT NULL DEFAULT '',
			imported_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS legacy_catalog_mods (
			ordinal INTEGER PRIMARY KEY,
			entity_id TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			FOREIGN KEY(entity_id) REFERENCES entities(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS artifacts (
			id TEXT PRIMARY KEY,
			central_fingerprint TEXT NOT NULL DEFAULT '',
			sha256 TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL DEFAULT 0,
			manifest_json TEXT NOT NULL DEFAULT '{}',
			analyzer_version TEXT NOT NULL DEFAULT '',
			analyzed_at TEXT NOT NULL DEFAULT ''
		)`,
		v4ArchiveLinksTableDDL,
		`CREATE TABLE IF NOT EXISTS assets (
			sha256 TEXT PRIMARY KEY,
			path TEXT NOT NULL DEFAULT '',
			mime TEXT NOT NULL DEFAULT '',
			width INTEGER NOT NULL DEFAULT 0,
			height INTEGER NOT NULL DEFAULT 0,
			size_bytes INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS entity_assets (
			entity_id TEXT NOT NULL,
			asset_sha256 TEXT NOT NULL,
			role TEXT NOT NULL,
			ordinal INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(entity_id,role,ordinal),
			FOREIGN KEY(entity_id) REFERENCES entities(id) ON DELETE CASCADE,
			FOREIGN KEY(asset_sha256) REFERENCES assets(sha256) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at TEXT NOT NULL DEFAULT '',
			entity_id TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT '',
			data_json TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE TABLE IF NOT EXISTS scans (
			id TEXT PRIMARY KEY,
			started_at TEXT NOT NULL DEFAULT '',
			finished_at TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'running',
			roots_json TEXT NOT NULL DEFAULT '[]',
			discovered INTEGER NOT NULL DEFAULT 0,
			analyzed INTEGER NOT NULL DEFAULT 0,
			failed INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS workspaces (
			id TEXT PRIMARY KEY,
			entity_id TEXT NOT NULL,
			artifact_id TEXT NOT NULL,
			root TEXT NOT NULL DEFAULT '',
			files_root TEXT NOT NULL DEFAULT '',
			source_path TEXT NOT NULL DEFAULT '',
			source_sha256 TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			last_validation_json TEXT NOT NULL DEFAULT '{}',
			virgil_configured INTEGER NOT NULL DEFAULT 0,
			virgil_enabled INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY(entity_id) REFERENCES entities(id) ON DELETE CASCADE,
			FOREIGN KEY(artifact_id) REFERENCES artifacts(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS exports (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			artifact_id TEXT NOT NULL,
			path TEXT NOT NULL DEFAULT '',
			sha256 TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT '',
			FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
			FOREIGN KEY(artifact_id) REFERENCES artifacts(id) ON DELETE CASCADE
		)`,
		collectionsTableDDL,
		collectionModsTableDDL,
		collectionChildrenTableDDL,
		playProfilesTableDDL,
		playProfileCollectionsTableDDL,
		playStateTableDDL,
		v4TestInstallsTableDDL,
		`CREATE TABLE IF NOT EXISTS mod_tags (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL COLLATE NOCASE UNIQUE,
			color TEXT NOT NULL DEFAULT '#7a8791',
			icon TEXT NOT NULL DEFAULT 'tag',
			origin TEXT NOT NULL DEFAULT 'user',
			grouped INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS mod_tag_entities (
			tag_id TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(tag_id,entity_id),
			FOREIGN KEY(tag_id) REFERENCES mod_tags(id) ON DELETE CASCADE,
			FOREIGN KEY(entity_id) REFERENCES entities(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS workspace_drafts (
			workspace_id TEXT NOT NULL,
			path TEXT NOT NULL,
			content TEXT NOT NULL DEFAULT '',
			base_sha256 TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(workspace_id,path),
			FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS mod_audits (
			id TEXT PRIMARY KEY,
			entity_id TEXT NOT NULL,
			artifact_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT '',
			stage TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT '',
			deterministic_json TEXT NOT NULL DEFAULT '{}',
			attack_surface_json TEXT NOT NULL DEFAULT '{}',
			pre_scan_json TEXT NOT NULL DEFAULT '{}',
			final_json TEXT NOT NULL DEFAULT '{}',
			follow_up_json TEXT NOT NULL DEFAULT '[]',
			error TEXT NOT NULL DEFAULT '',
			FOREIGN KEY(entity_id) REFERENCES entities(id) ON DELETE CASCADE,
			FOREIGN KEY(artifact_id) REFERENCES artifacts(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS mod_audit_files (
			audit_id TEXT NOT NULL,
			path TEXT NOT NULL,
			fingerprint TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL DEFAULT 0,
			entrypoint_type TEXT NOT NULL DEFAULT '',
			signals_json TEXT NOT NULL DEFAULT '[]',
			excerpt TEXT NOT NULL DEFAULT '',
			pre_scan_json TEXT NOT NULL DEFAULT '{}',
			PRIMARY KEY(audit_id,path),
			FOREIGN KEY(audit_id) REFERENCES mod_audits(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS virus_scans (
			id TEXT PRIMARY KEY,
			entity_id TEXT NOT NULL,
			artifact_id TEXT NOT NULL,
			file_sha256 TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			current_stage TEXT NOT NULL DEFAULT '',
			verdict TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			FOREIGN KEY(entity_id) REFERENCES entities(id) ON DELETE CASCADE,
			FOREIGN KEY(artifact_id) REFERENCES artifacts(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS virus_scan_stages (
			id TEXT PRIMARY KEY,
			scan_id TEXT NOT NULL,
			entity_id TEXT NOT NULL,
			artifact_id TEXT NOT NULL,
			file_sha256 TEXT NOT NULL DEFAULT '',
			stage TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT '',
			completed_at TEXT NOT NULL DEFAULT '',
			parameters_json TEXT NOT NULL DEFAULT '{}',
			inputs_json TEXT NOT NULL DEFAULT '[]',
			metadata_file TEXT NOT NULL DEFAULT '',
			audit_id TEXT NOT NULL DEFAULT '',
			summary TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			FOREIGN KEY(scan_id) REFERENCES virus_scans(id) ON DELETE CASCADE,
			FOREIGN KEY(entity_id) REFERENCES entities(id) ON DELETE CASCADE,
			FOREIGN KEY(artifact_id) REFERENCES artifacts(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS virgil_sessions (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			profile TEXT NOT NULL DEFAULT '',
			omp_session_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT 'Virgil session',
			omp_title TEXT NOT NULL DEFAULT '',
			user_title TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'idle',
			last_error TEXT NOT NULL DEFAULT '',
			tab_order INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT '',
			FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS agent_runs (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL DEFAULT '',
			workspace_id TEXT NOT NULL,
			prompt TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL DEFAULT '',
			finished_at TEXT NOT NULL DEFAULT '',
			final_text TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			FOREIGN KEY(session_id) REFERENCES virgil_sessions(id) ON DELETE CASCADE,
			FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS agent_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id TEXT NOT NULL,
			at TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL DEFAULT '',
			data_json TEXT NOT NULL DEFAULT '{}',
			FOREIGN KEY(run_id) REFERENCES agent_runs(id) ON DELETE CASCADE
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS library_search_fts USING fts5(entity_id UNINDEXED, content, tokenize='trigram case_sensitive 0')`,
		`CREATE TABLE IF NOT EXISTS artifact_summaries (
			artifact_id TEXT PRIMARY KEY,
			revision TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			author TEXT NOT NULL DEFAULT '',
			version TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			namespaces_json TEXT NOT NULL DEFAULT '{}',
			issues_json TEXT NOT NULL DEFAULT '[]',
			member_count INTEGER NOT NULL DEFAULT 0,
			namespace_count INTEGER NOT NULL DEFAULT 0,
			variant_count INTEGER NOT NULL DEFAULT 0,
			issue_count INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY(artifact_id) REFERENCES artifacts(id) ON DELETE CASCADE
		)`,
		`CREATE TRIGGER IF NOT EXISTS artifact_summaries_ai
		AFTER INSERT ON artifacts BEGIN
			INSERT OR REPLACE INTO artifact_summaries(
				artifact_id,revision,title,author,version,description,
				namespaces_json,issues_json,
				member_count,namespace_count,variant_count,issue_count
			) VALUES(
				NEW.id,
				hex(randomblob(16)),
				COALESCE(json_extract(NEW.manifest_json,'$.title'),''),
				COALESCE(json_extract(NEW.manifest_json,'$.author'),''),
				COALESCE(json_extract(NEW.manifest_json,'$.version'),''),
				COALESCE(json_extract(NEW.manifest_json,'$.description'),''),
				COALESCE(json_extract(NEW.manifest_json,'$.namespaces'),'{}'),
				COALESCE(json_extract(NEW.manifest_json,'$.issues'),'[]'),
				COALESCE(json_array_length(json_extract(NEW.manifest_json,'$.members')),0),
				(SELECT COUNT(*) FROM json_each(COALESCE(json_extract(NEW.manifest_json,'$.namespaces'),'{}'))),
				COALESCE(json_array_length(json_extract(NEW.manifest_json,'$.variants')),0),
				COALESCE(json_array_length(json_extract(NEW.manifest_json,'$.issues')),0)
			);
		END`,
		`CREATE TRIGGER IF NOT EXISTS artifact_summaries_au
		AFTER UPDATE OF manifest_json ON artifacts
		WHEN OLD.manifest_json <> NEW.manifest_json BEGIN
			INSERT OR REPLACE INTO artifact_summaries(
				artifact_id,revision,title,author,version,description,
				namespaces_json,issues_json,
				member_count,namespace_count,variant_count,issue_count
			) VALUES(
				NEW.id,
				hex(randomblob(16)),
				COALESCE(json_extract(NEW.manifest_json,'$.title'),''),
				COALESCE(json_extract(NEW.manifest_json,'$.author'),''),
				COALESCE(json_extract(NEW.manifest_json,'$.version'),''),
				COALESCE(json_extract(NEW.manifest_json,'$.description'),''),
				COALESCE(json_extract(NEW.manifest_json,'$.namespaces'),'{}'),
				COALESCE(json_extract(NEW.manifest_json,'$.issues'),'[]'),
				COALESCE(json_array_length(json_extract(NEW.manifest_json,'$.members')),0),
				(SELECT COUNT(*) FROM json_each(COALESCE(json_extract(NEW.manifest_json,'$.namespaces'),'{}'))),
				COALESCE(json_array_length(json_extract(NEW.manifest_json,'$.variants')),0),
				COALESCE(json_array_length(json_extract(NEW.manifest_json,'$.issues')),0)
			);
		END`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func ensureStoreColumnsTx(ctx context.Context, tx *sql.Tx) error {
	columns := []struct {
		table, column, definition string
	}{
		{"source_classifications", "label", `TEXT NOT NULL DEFAULT ''`},
		{"entities", "kind", `TEXT NOT NULL DEFAULT 'unknown'`},
		{"entities", "source_id", `TEXT NOT NULL DEFAULT 'user-added'`},
		{"entities", "created_at", `TEXT NOT NULL DEFAULT ''`},
		{"entities", "updated_at", `TEXT NOT NULL DEFAULT ''`},
		{"artifacts", "central_fingerprint", `TEXT NOT NULL DEFAULT ''`},
		{"artifacts", "sha256", `TEXT NOT NULL DEFAULT ''`},
		{"artifacts", "size_bytes", `INTEGER NOT NULL DEFAULT 0`},
		{"artifacts", "manifest_json", `TEXT NOT NULL DEFAULT '{}'`},
		{"artifacts", "analyzer_version", `TEXT NOT NULL DEFAULT ''`},
		{"artifacts", "analyzed_at", `TEXT NOT NULL DEFAULT ''`},
		{"archive_links", "entity_id", `TEXT NOT NULL DEFAULT ''`},
		{"archive_links", "artifact_id", `TEXT NOT NULL DEFAULT ''`},
		{"archive_links", "path", `TEXT NOT NULL DEFAULT ''`},
		{"archive_links", "root_path", `TEXT NOT NULL DEFAULT ''`},
		{"archive_links", "active", `INTEGER NOT NULL DEFAULT 1`},
		{"archive_links", "source_id", `TEXT NOT NULL DEFAULT 'user-added'`},
		{"archive_links", "size_bytes", `INTEGER NOT NULL DEFAULT 0`},
		{"archive_links", "modified_at", `TEXT NOT NULL DEFAULT ''`},
		{"archive_links", "discovered_at", `TEXT NOT NULL DEFAULT ''`},
		{"archive_links", "last_seen_at", `TEXT NOT NULL DEFAULT ''`},
		{"archive_links", "last_scan_id", `TEXT NOT NULL DEFAULT ''`},
		{"archive_links", "basename_key", `TEXT NOT NULL DEFAULT ''`},
		{"scans", "finished_at", `TEXT NOT NULL DEFAULT ''`},
		{"scans", "status", `TEXT NOT NULL DEFAULT 'running'`},
		{"scans", "roots_json", `TEXT NOT NULL DEFAULT '[]'`},
		{"scans", "discovered", `INTEGER NOT NULL DEFAULT 0`},
		{"scans", "analyzed", `INTEGER NOT NULL DEFAULT 0`},
		{"scans", "failed", `INTEGER NOT NULL DEFAULT 0`},
		{"scans", "error", `TEXT NOT NULL DEFAULT ''`},
		{"mod_tags", "color", `TEXT NOT NULL DEFAULT '#7a8791'`},
		{"mod_tags", "icon", `TEXT NOT NULL DEFAULT 'tag'`},
		{"mod_tags", "origin", `TEXT NOT NULL DEFAULT 'user'`},
		{"mod_tags", "grouped", `INTEGER NOT NULL DEFAULT 0`},
		{"workspace_drafts", "base_sha256", `TEXT NOT NULL DEFAULT ''`},
		{"virus_scans", "file_sha256", `TEXT NOT NULL DEFAULT ''`},
		{"virus_scan_stages", "file_sha256", `TEXT NOT NULL DEFAULT ''`},
		{"test_installs", "workspace_id", `TEXT NOT NULL DEFAULT ''`},
		{"test_installs", "export_id", `TEXT NOT NULL DEFAULT ''`},
		{"test_installs", "path", `TEXT NOT NULL DEFAULT ''`},
		{"test_installs", "sha256", `TEXT NOT NULL DEFAULT ''`},
		{"test_installs", "installed_at", `TEXT NOT NULL DEFAULT ''`},
		{"test_installs", "log_baseline_at", `TEXT NOT NULL DEFAULT ''`},
		{"test_installs", "log_path", `TEXT NOT NULL DEFAULT ''`},
		{"test_installs", "log_offset", `INTEGER NOT NULL DEFAULT 0`},
		{"test_installs", "active", `INTEGER NOT NULL DEFAULT 1`},
		{"legacy_catalog_state", "status", `TEXT NOT NULL DEFAULT 'absent'`},
		{"legacy_catalog_state", "schema_version", `INTEGER NOT NULL DEFAULT 0`},
		{"legacy_catalog_state", "catalog_json", `TEXT NOT NULL DEFAULT ''`},
		{"legacy_catalog_state", "last_scan_present", `INTEGER NOT NULL DEFAULT 0`},
		{"legacy_catalog_state", "last_scan_is_null", `INTEGER NOT NULL DEFAULT 0`},
		{"legacy_catalog_state", "last_scan_json", `TEXT NOT NULL DEFAULT ''`},
		{"legacy_catalog_state", "imported_at", `TEXT NOT NULL DEFAULT ''`},
		{"legacy_catalog_mods", "entity_id", `TEXT NOT NULL DEFAULT ''`},
		{"legacy_catalog_mods", "payload_json", `TEXT NOT NULL DEFAULT ''`},
		{"virgil_sessions", "profile", `TEXT NOT NULL DEFAULT ''`},
		{"agent_runs", "session_id", `TEXT NOT NULL DEFAULT ''`},
		{"mod_audits", "pre_scan_json", `TEXT NOT NULL DEFAULT '{}'`},
		{"mod_audits", "final_json", `TEXT NOT NULL DEFAULT '{}'`},
		{"mod_audits", "follow_up_json", `TEXT NOT NULL DEFAULT '[]'`},
		{"mod_audit_files", "pre_scan_json", `TEXT NOT NULL DEFAULT '{}'`},
		{"play_profile_collections", "excluded", `INTEGER NOT NULL DEFAULT 0`},
		{"play_profiles", "includes_all_mods", `INTEGER NOT NULL DEFAULT 0`},
		{"play_profiles", "excludes_all_mods", `INTEGER NOT NULL DEFAULT 0`},
	}
	for _, column := range columns {
		if err := ensureColumnTx(ctx, tx, column.table, column.column, column.definition); err != nil {
			return err
		}
	}
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS entities_kind_idx ON entities(kind,updated_at,display_name COLLATE NOCASE)`,
		`CREATE INDEX IF NOT EXISTS entities_source_idx ON entities(source_id,updated_at)`,
		`CREATE INDEX IF NOT EXISTS entities_updated_idx ON entities(updated_at,display_name COLLATE NOCASE)`,
		`CREATE INDEX IF NOT EXISTS archive_links_entity_idx ON archive_links(entity_id,active DESC,last_seen_at DESC,id DESC)`,
		`CREATE INDEX IF NOT EXISTS archive_links_path_idx ON archive_links(path COLLATE NOCASE)`,
		`CREATE INDEX IF NOT EXISTS archive_links_root_idx ON archive_links(root_path COLLATE NOCASE,active,last_scan_id)`,
		`CREATE INDEX IF NOT EXISTS archive_links_source_idx ON archive_links(source_id,active)`,
		`CREATE INDEX IF NOT EXISTS archive_links_scan_idx ON archive_links(last_scan_id,active)`,
		`CREATE INDEX IF NOT EXISTS archive_links_fingerprint_idx ON archive_links(basename_key)`,
		`CREATE INDEX IF NOT EXISTS artifacts_fingerprint_idx ON artifacts(central_fingerprint)`,
		`CREATE INDEX IF NOT EXISTS mod_tag_entities_entity_idx ON mod_tag_entities(entity_id,tag_id)`,
		`CREATE INDEX IF NOT EXISTS collection_mods_entity_idx ON collection_mods(entity_id,collection_id,position)`,
		`CREATE INDEX IF NOT EXISTS collection_children_child_idx ON collection_children(child_id,parent_id,position)`,
		`CREATE INDEX IF NOT EXISTS play_profile_collections_collection_idx ON play_profile_collections(collection_id,profile_id,position)`,
		`CREATE INDEX IF NOT EXISTS scans_status_idx ON scans(status,started_at)`,
		`CREATE INDEX IF NOT EXISTS events_entity_idx ON events(entity_id,id)`,
		`CREATE INDEX IF NOT EXISTS agent_runs_session_idx ON agent_runs(session_id,started_at,id)`,
		`CREATE INDEX IF NOT EXISTS virus_scans_entity_idx ON virus_scans(entity_id,updated_at DESC,id DESC)`,
		`CREATE INDEX IF NOT EXISTS virus_scans_entity_artifact_idx ON virus_scans(entity_id,artifact_id,updated_at DESC,id DESC)`,
	}
	for _, statement := range indexes {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func ensureColumnTx(ctx context.Context, tx *sql.Tx, table, column, definition string) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+quoteSQLiteIdentifier(table)+`)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if strings.EqualFold(strings.TrimSpace(name), column) {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE `+quoteSQLiteIdentifier(table)+` ADD COLUMN `+quoteSQLiteIdentifier(column)+` `+definition)
	return err
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func ensureSourceClassificationsTx(ctx context.Context, tx *sql.Tx) error {
	if err := ensureColumnTx(ctx, tx, "source_classifications", "label", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	rows := []struct{ id, label, semantics string }{
		{"beamng-repository", "BeamNG Repository", "Archive supplied by the BeamNG repository"},
		{"user-added", "User added", "Archive supplied by the user outside the repository"},
	}
	for _, row := range rows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO source_classifications(id,label,semantics) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET label=excluded.label,semantics=excluded.semantics`, row.id, row.label, row.semantics); err != nil {
			return err
		}
	}
	return nil
}

func backfillSourceIDsTx(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"entities", "archive_links"} {
		hasLegacy, err := hasColumnTx(ctx, tx, table, "source_class")
		if err != nil {
			return err
		}
		if hasLegacy {
			if _, err := tx.ExecContext(ctx, `UPDATE `+quoteSQLiteIdentifier(table)+` SET source_id=CASE
				WHEN lower(trim(COALESCE(source_class,''))) IN ('repository','beamng-repository') THEN 'beamng-repository'
				ELSE 'user-added' END
				WHERE trim(COALESCE(source_class,''))<>''`); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE `+quoteSQLiteIdentifier(table)+` SET source_id='user-added'
			WHERE trim(COALESCE(source_id,''))='' OR source_id NOT IN ('beamng-repository','user-added')`); err != nil {
			return err
		}
	}
	return nil
}

func hasColumnTx(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+quoteSQLiteIdentifier(table)+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// verifyIntegrity is read-only by design. A failed check leaves the database,
// catalog, and any import backup untouched so the operator can recover from a
// copy. quick_check catches b-tree corruption while foreign_key_check catches
// orphaned normalized rows that quick_check does not report.
func (s *Store) verifyIntegrity(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("SQLite store is not initialized")
	}
	var quickProblems []string
	quickRows, err := s.db.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return integrityRecoveryError("run PRAGMA quick_check", err)
	}
	for quickRows.Next() {
		var result string
		if err := quickRows.Scan(&result); err != nil {
			_ = quickRows.Close()
			return integrityRecoveryError("read PRAGMA quick_check", err)
		}
		if !strings.EqualFold(strings.TrimSpace(result), "ok") {
			quickProblems = append(quickProblems, result)
		}
	}
	if err := quickRows.Err(); err != nil {
		_ = quickRows.Close()
		return integrityRecoveryError("read PRAGMA quick_check", err)
	}
	if err := quickRows.Close(); err != nil {
		return integrityRecoveryError("close PRAGMA quick_check", err)
	}

	foreignProblems := []string{}
	foreignRows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return integrityRecoveryError("run PRAGMA foreign_key_check", err)
	}
	for foreignRows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var foreignKeyID sql.NullInt64
		if err := foreignRows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			_ = foreignRows.Close()
			return integrityRecoveryError("read PRAGMA foreign_key_check", err)
		}
		foreignProblems = append(foreignProblems, fmt.Sprintf("table=%q rowid=%s parent=%q fk=%s", table, nullableIntString(rowID), parent, nullableIntString(foreignKeyID)))
	}
	if err := foreignRows.Err(); err != nil {
		_ = foreignRows.Close()
		return integrityRecoveryError("read PRAGMA foreign_key_check", err)
	}
	if err := foreignRows.Close(); err != nil {
		return integrityRecoveryError("close PRAGMA foreign_key_check", err)
	}
	if len(quickProblems) > 0 || len(foreignProblems) > 0 {
		parts := []string{}
		if len(quickProblems) > 0 {
			parts = append(parts, "quick_check="+strings.Join(quickProblems, "; "))
		}
		if len(foreignProblems) > 0 {
			parts = append(parts, "foreign_key_check="+strings.Join(foreignProblems, "; "))
		}
		return integrityRecoveryError("database integrity failure: "+strings.Join(parts, " | "), nil)
	}
	return nil
}

// IntegrityCheck retains the existing public-in-package entrypoint while the
// migration contract uses verifyIntegrity.
func (s *Store) IntegrityCheck(ctx context.Context) error { return s.verifyIntegrity(ctx) }

func nullableIntString(value sql.NullInt64) string {
	if !value.Valid {
		return "NULL"
	}
	return strconv.FormatInt(value.Int64, 10)
}

func integrityRecoveryError(operation string, cause error) error {
	if cause != nil {
		return fmt.Errorf("SQLite integrity check could not %s: %w; close Mod Studio without deleting the database, preserve the original database and any catalog backup, then restore the last known-good SQLite backup or copy the database aside for recovery", operation, cause)
	}
	return fmt.Errorf("SQLite integrity check failed (%s); close Mod Studio without deleting the database, preserve the original database and any catalog backup, then restore the last known-good SQLite backup or copy the database aside for recovery", operation)
}

func rebuildLibrarySearchFTSTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_search_fts`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM entities ORDER BY id`)
	if err != nil {
		return err
	}
	entityIDs := []string{}
	for rows.Next() {
		var entityID string
		if err := rows.Scan(&entityID); err != nil {
			_ = rows.Close()
			return err
		}
		entityIDs = append(entityIDs, entityID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, entityID := range entityIDs {
		if err := refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
			return err
		}
	}
	return markLibraryFTSFreshTx(ctx, tx)
}

// refreshLibrarySearchEntryTx rebuilds one aggregate FTS row from canonical
// entity, latest archive/artifact, direct collection, and tag rows. Every value
// is bound as a SQL argument; IDs and searchable text containing quotes are not
// interpolated.
func refreshLibrarySearchEntryTx(ctx context.Context, tx *sql.Tx, entityID string) error {
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return errors.New("cannot refresh an empty library entity ID")
	}
	var displayName, kind, sourceID, pathValue, rootPath, collectionNames string
	var author, description, namespacesJSON string
	err := tx.QueryRowContext(ctx, `SELECT e.display_name,e.kind,e.source_id,
		COALESCE(l.path,''),COALESCE(l.root_path,''),
		COALESCE(s.author,''),COALESCE(s.description,''),COALESCE(s.namespaces_json,'{}'),
		COALESCE((SELECT GROUP_CONCAT(name,' ') FROM (
			SELECT c.name AS name FROM collection_mods cm
			JOIN collections c ON c.id=cm.collection_id
			WHERE cm.entity_id=e.id
			ORDER BY c.name COLLATE NOCASE,c.id
		)), '')
		FROM entities e
		LEFT JOIN archive_links l ON l.id=(
			SELECT l2.id FROM archive_links l2
			WHERE l2.entity_id=e.id
			ORDER BY l2.active DESC,l2.last_seen_at DESC,l2.id DESC LIMIT 1
		)
		LEFT JOIN artifacts a ON a.id=l.artifact_id
		LEFT JOIN artifact_summaries s ON s.artifact_id=a.id
		WHERE e.id=?`, entityID).Scan(&displayName, &kind, &sourceID, &pathValue, &rootPath, &author, &description, &namespacesJSON, &collectionNames)
	if err != nil {
		return err
	}
	var namespaces map[string][]string
	if namespacesJSON != "" && namespacesJSON != "{}" {
		if err := json.Unmarshal([]byte(namespacesJSON), &namespaces); err != nil {
			return fmt.Errorf("decode namespaces for FTS entity %q: %w", entityID, err)
		}
	}
	// Gather paths, root paths, and descriptions from all archive links using
	// the lightweight artifact_summaries projection rather than the full manifest.
	archiveRows, err := tx.QueryContext(ctx, `SELECT COALESCE(l.path,''),COALESCE(l.root_path,''),COALESCE(s.description,'')
		FROM archive_links l
		LEFT JOIN artifacts a ON a.id=l.artifact_id
		LEFT JOIN artifact_summaries s ON s.artifact_id=a.id
		WHERE l.entity_id=?
		ORDER BY l.active DESC,l.last_seen_at DESC,l.id DESC`, entityID)
	if err != nil {
		return err
	}
	paths := []string{}
	rootPaths := []string{}
	descriptions := []string{}
	seenPaths := map[string]struct{}{}
	seenRootPaths := map[string]struct{}{}
	seenDescriptions := map[string]struct{}{}
	for archiveRows.Next() {
		var archivePath, archiveRoot, archiveDescription string
		if err := archiveRows.Scan(&archivePath, &archiveRoot, &archiveDescription); err != nil {
			_ = archiveRows.Close()
			return err
		}
		if archivePath != "" {
			key := strings.ToLower(archivePath)
			if _, seen := seenPaths[key]; !seen {
				seenPaths[key] = struct{}{}
				paths = append(paths, archivePath)
			}
		}
		if archiveRoot != "" {
			key := strings.ToLower(archiveRoot)
			if _, seen := seenRootPaths[key]; !seen {
				seenRootPaths[key] = struct{}{}
				rootPaths = append(rootPaths, archiveRoot)
			}
		}
		if archiveDescription != "" {
			if _, seen := seenDescriptions[archiveDescription]; !seen {
				seenDescriptions[archiveDescription] = struct{}{}
				descriptions = append(descriptions, archiveDescription)
			}
		}
	}
	if err := archiveRows.Err(); err != nil {
		_ = archiveRows.Close()
		return err
	}
	if err := archiveRows.Close(); err != nil {
		return err
	}
	if len(paths) > 0 {
		pathValue = strings.Join(paths, " ")
	}
	if len(rootPaths) > 0 {
		rootPath = strings.Join(rootPaths, " ")
	}
	tagRows, err := tx.QueryContext(ctx, `SELECT t.name FROM mod_tag_entities te JOIN mod_tags t ON t.id=te.tag_id WHERE te.entity_id=? ORDER BY t.name COLLATE NOCASE,t.id`, entityID)
	if err != nil {
		return err
	}
	tagNames := []string{}
	for tagRows.Next() {
		var name string
		if err := tagRows.Scan(&name); err != nil {
			_ = tagRows.Close()
			return err
		}
		tagNames = append(tagNames, name)
	}
	if err := tagRows.Err(); err != nil {
		_ = tagRows.Close()
		return err
	}
	if err := tagRows.Close(); err != nil {
		return err
	}
	content := canonicalLibrarySearchText(displayName, kind, kind, sourceID, pathValue, rootPath, author, strings.Join(descriptions, " "), namespaces, collectionNames, tagNames)
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_search_fts WHERE entity_id=?`, entityID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO library_search_fts(entity_id,content) VALUES(?,?)`, entityID, content); err != nil {
		return fmt.Errorf("insert FTS row for entity %q: %w", entityID, err)
	}
	return markLibraryFTSFreshTx(ctx, tx)
}

func markLibraryFTSFreshTx(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO library_index_metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, libraryFTSFreshnessKey, "1")
	return err
}

func canonicalLibrarySearchText(displayName, manifestKind, storedKind, sourceID, pathValue, rootPath, author, description string, namespaces map[string][]string, collectionNames string, tags []string) string {
	values := []string{displayName, manifestKind, storedKind, sourceID, pathValue, rootPath, author, description, collectionNames}
	if manifestKind != "" {
		values = append(values, libraryKindSearchName(manifestKind))
	}
	if storedKind != "" && storedKind != manifestKind {
		values = append(values, libraryKindSearchName(storedKind))
	}
	keys := make([]string, 0, len(namespaces))
	for key := range namespaces {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && strings.ToLower(keys[j]) < strings.ToLower(keys[j-1]); j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	for _, key := range keys {
		values = append(values, key)
		for _, entry := range namespaces[key] {
			values = append(values, entry)
		}
	}
	values = append(values, tags...)
	return strings.Join(values, " ")
}

func deleteLibrarySearchEntryTx(ctx context.Context, tx *sql.Tx, entityID string) error {
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return errors.New("cannot delete an empty library entity ID")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM library_search_fts WHERE entity_id=?`, entityID); err != nil {
		return err
	}
	return markLibraryFTSFreshTx(ctx, tx)
}

// Store method forms are the transaction-bound contract used by scan/update
// callers. The free helpers keep the schema rebuild code easy to exercise while
// delegating to the same implementation.
func (s *Store) refreshLibrarySearchEntryTx(ctx context.Context, tx *sql.Tx, entityID string) error {
	return refreshLibrarySearchEntryTx(ctx, tx, entityID)
}

func (s *Store) deleteLibrarySearchEntryTx(ctx context.Context, tx *sql.Tx, entityID string) error {
	return deleteLibrarySearchEntryTx(ctx, tx, entityID)
}

// importLegacyCatalogIfNeeded is retained for older callers during the clean
// cutover. New callers pass the explicit path to importLegacyCatalogOnce.
func (s *Store) importLegacyCatalogIfNeeded(ctx context.Context, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	return s.importLegacyCatalogOnce(ctx, paths[0])
}

type legacyCatalogDocument struct {
	SchemaVersion int                `json:"schemaVersion"`
	CreatedAt     string             `json:"createdAt"`
	UpdatedAt     string             `json:"updatedAt"`
	LastScan      json.RawMessage    `json:"lastScan"`
	Mods          []legacyCatalogMod `json:"mods"`
}

type legacyCatalogMod struct {
	ID                 string                    `json:"id"`
	Path               string                    `json:"path"`
	Filename           string                    `json:"filename"`
	OriginalPath       string                    `json:"originalPath"`
	Location           string                    `json:"location"`
	Enabled            bool                      `json:"enabled"`
	Missing            bool                      `json:"missing"`
	Source             string                    `json:"source"`
	ActiveRelativePath string                    `json:"activeRelativePath"`
	Size               int64                     `json:"size"`
	ModifiedAt         string                    `json:"modifiedAt"`
	Fingerprint        string                    `json:"fingerprint"`
	SHA256             string                    `json:"sha256"`
	FullSHA256         string                    `json:"fullSha256"`
	ValidArchive       bool                      `json:"validArchive"`
	EntryCount         int                       `json:"entryCount"`
	CompressedBytes    uint64                    `json:"compressedBytes"`
	UncompressedBytes  uint64                    `json:"uncompressedBytes"`
	Wrapper            *string                   `json:"wrapper"`
	AutoCategory       string                    `json:"autoCategory"`
	Kind               string                    `json:"kind"`
	ContentTags        []string                  `json:"contentTags"`
	Namespaces         map[string][]string       `json:"namespaces"`
	NestedArchiveCount int                       `json:"nestedArchiveCount"`
	Title              string                    `json:"title"`
	ArchiveDescription *string                   `json:"archiveDescription"`
	Description        string                    `json:"description"`
	Author             *string                   `json:"author"`
	Version            *string                   `json:"version"`
	MetadataDocuments  []modkit.MetadataDocument `json:"metadataDocuments"`
	PreviewPath        *string                   `json:"previewPath"`
	Database           json.RawMessage           `json:"database"`
	Issues             []modkit.Issue            `json:"issues"`
	Tags               []json.RawMessage         `json:"tags"`
	Notes              string                    `json:"notes"`
	Problematic        bool                      `json:"problematic"`
	CategoryOverride   *string                   `json:"categoryOverride"`
	RuntimeIssues      []modkit.Issue            `json:"runtimeIssues"`
	Health             string                    `json:"health"`
	Manifest           json.RawMessage           `json:"manifest"`
}

type importedLegacyTag struct {
	ID    string
	Name  string
	Color string
	Icon  string
}

type importedLegacyMod struct {
	Ordinal      int
	PayloadJSON  string
	EntityID     string
	ArtifactID   string
	LinkID       string
	DisplayName  string
	Kind         modkit.Kind
	Path         string
	RootPath     string
	Active       bool
	SourceID     string
	SizeBytes    int64
	ModifiedAt   string
	DiscoveredAt string
	LastSeenAt   string
	Fingerprint  string
	SHA256       string
	Manifest     modkit.Manifest
	ManifestJSON string
	Tags         []importedLegacyTag
}

type importedLegacyCatalog struct {
	CreatedAt       string
	UpdatedAt       string
	PayloadJSON     string
	LastScanJSON    string
	LastScanPresent bool
	LastScanIsNull  bool
	Mods            []importedLegacyMod
	Tags            []importedLegacyTag
}

func snapshotStringIDsTx(ctx context.Context, tx *sql.Tx, query string) (map[string]struct{}, error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *Store) importLegacyCatalogOnce(ctx context.Context, path string) error {
	path = strings.TrimSpace(path)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if path == "" || !filepath.IsAbs(path) {
		return s.recordLegacyCatalogCutover(ctx, legacyCatalogImportAbsent)
	}
	marked, err := s.legacyCatalogImportMarked(ctx)
	if err != nil {
		return fmt.Errorf("check legacy catalog import marker: %w", err)
	}
	if marked {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s.recordLegacyCatalogCutover(ctx, legacyCatalogImportAbsent)
		}
		return fmt.Errorf("inspect legacy catalog %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("legacy catalog path is a directory: %s", path)
	}

	catalog, err := parseAndValidateLegacyCatalog(path)
	if err != nil {
		return err
	}
	backupPath, err := backupLegacyCatalog(path)
	if err != nil {
		return fmt.Errorf("backup legacy catalog %s: %w", path, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin legacy catalog import (backup retained at %s): %w", backupPath, err)
	}
	defer func() { _ = tx.Rollback() }()

	if marked, err := legacyCatalogImportMarkedTx(ctx, tx); err != nil {
		return fmt.Errorf("recheck legacy catalog import marker: %w", err)
	} else if marked {
		return nil
	}
	if err := ensureSourceClassificationsTx(ctx, tx); err != nil {
		return fmt.Errorf("prepare source classifications (backup retained at %s): %w", backupPath, err)
	}
	if err := reconcileLibraryDuplicatesTx(ctx, tx); err != nil {
		return fmt.Errorf("reconcile existing library rows (backup retained at %s): %w", backupPath, err)
	}

	var beforeEntities, beforeArtifacts, beforeLinks, beforeTags map[string]struct{}
	for _, snapshot := range []struct {
		query string
		dest  *map[string]struct{}
	}{
		{`SELECT id FROM entities`, &beforeEntities},
		{`SELECT id FROM artifacts`, &beforeArtifacts},
		{`SELECT id FROM archive_links`, &beforeLinks},
		{`SELECT id FROM mod_tags`, &beforeTags},
	} {
		ids, err := snapshotStringIDsTx(ctx, tx, snapshot.query)
		if err != nil {
			return fmt.Errorf("read pre-import identities (backup retained at %s): %w", backupPath, err)
		}
		*snapshot.dest = ids
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM legacy_catalog_mods`); err != nil {
		return fmt.Errorf("clear legacy catalog payload rows (backup retained at %s): %w", backupPath, err)
	}

	expectedEntityMerges := map[string]string{}
	tagIDs := map[string]string{}
	for _, tag := range catalog.Tags {
		resolvedID, err := importLegacyTagTx(ctx, tx, tag)
		if err != nil {
			return fmt.Errorf("import tag %q (backup retained at %s): %w", tag.Name, backupPath, err)
		}
		tagIDs[strings.ToLower(tag.Name)] = resolvedID
		if tag.ID != "" {
			tagIDs[strings.ToLower(tag.ID)] = resolvedID
		}
	}
	for index := range catalog.Mods {
		mod := &catalog.Mods[index]
		if err := resolveLegacyCanonicalMergesTx(ctx, tx, mod, expectedEntityMerges); err != nil {
			return fmt.Errorf("reconcile legacy mod %q (backup retained at %s): %w", mod.EntityID, backupPath, err)
		}
		artifactID, err := importLegacyArtifactTx(ctx, tx, *mod)
		if err != nil {
			return fmt.Errorf("import artifact for %q (backup retained at %s): %w", mod.EntityID, backupPath, err)
		}
		mod.ArtifactID = artifactID
		if err := importLegacyEntityTx(ctx, tx, *mod); err != nil {
			return fmt.Errorf("import entity %q (backup retained at %s): %w", mod.EntityID, backupPath, err)
		}
		if err := importLegacyLinkTx(ctx, tx, *mod); err != nil {
			return fmt.Errorf("import archive link %q (backup retained at %s): %w", mod.LinkID, backupPath, err)
		}
		for _, tag := range mod.Tags {
			resolvedID := tagIDs[strings.ToLower(tag.ID)]
			if resolvedID == "" {
				resolvedID = tagIDs[strings.ToLower(tag.Name)]
			}
			if resolvedID == "" {
				return fmt.Errorf("tag %q for entity %q was not defined (backup retained at %s)", tag.Name, mod.EntityID, backupPath)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO mod_tag_entities(tag_id,entity_id,created_at) VALUES(?,?,?) ON CONFLICT(tag_id,entity_id) DO NOTHING`, resolvedID, mod.EntityID, mod.LastSeenAt); err != nil {
				return fmt.Errorf("import tag assignment for entity %q (backup retained at %s): %w", mod.EntityID, backupPath, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO legacy_catalog_mods(ordinal,entity_id,payload_json) VALUES(?,?,?)`, mod.Ordinal, mod.EntityID, mod.PayloadJSON); err != nil {
			return fmt.Errorf("persist legacy mod payload %q (backup retained at %s): %w", mod.EntityID, backupPath, err)
		}
		if err := refreshLibrarySearchEntryTx(ctx, tx, mod.EntityID); err != nil {
			return fmt.Errorf("index imported entity %q (backup retained at %s): %w", mod.EntityID, backupPath, err)
		}
	}
	representatives, err := reconcileLegacyRepresentativesTx(ctx, tx, catalog.Mods)
	if err != nil {
		return fmt.Errorf("reconcile legacy representatives (backup retained at %s): %w", backupPath, err)
	}
	for index := range catalog.Mods {
		mod := &catalog.Mods[index]
		representative, ok := representatives[mod.EntityID]
		if !ok || representative.LinkID != mod.LinkID {
			continue
		}
		artifactID, err := importLegacyArtifactTx(ctx, tx, representative)
		if err != nil {
			return fmt.Errorf("finalize representative artifact for %q (backup retained at %s): %w", mod.EntityID, backupPath, err)
		}
		representative.ArtifactID = artifactID
		if err := importLegacyEntityTx(ctx, tx, representative); err != nil {
			return fmt.Errorf("finalize representative entity %q (backup retained at %s): %w", mod.EntityID, backupPath, err)
		}
	}
	if err := rebuildLibrarySearchFTSTx(ctx, tx); err != nil {
		return fmt.Errorf("verify imported search index (backup retained at %s): %w", backupPath, err)
	}

	afterEntities, err := snapshotStringIDsTx(ctx, tx, `SELECT id FROM entities`)
	if err != nil {
		return fmt.Errorf("read post-import entities (backup retained at %s): %w", backupPath, err)
	}
	afterArtifacts, err := snapshotStringIDsTx(ctx, tx, `SELECT id FROM artifacts`)
	if err != nil {
		return fmt.Errorf("read post-import artifacts (backup retained at %s): %w", backupPath, err)
	}
	afterLinks, err := snapshotStringIDsTx(ctx, tx, `SELECT id FROM archive_links`)
	if err != nil {
		return fmt.Errorf("read post-import links (backup retained at %s): %w", backupPath, err)
	}
	afterTags, err := snapshotStringIDsTx(ctx, tx, `SELECT id FROM mod_tags`)
	if err != nil {
		return fmt.Errorf("read post-import tags (backup retained at %s): %w", backupPath, err)
	}
	for id := range beforeEntities {
		if _, present := afterEntities[id]; present {
			continue
		}
		target, merged := expectedEntityMerges[id]
		for merged {
			next, chained := expectedEntityMerges[target]
			if !chained || next == target {
				break
			}
			target = next
		}
		if !merged {
			return fmt.Errorf("legacy catalog parity lost unrelated entity %q (backup retained at %s)", id, backupPath)
		}
		if _, present := afterEntities[target]; !present {
			return fmt.Errorf("legacy catalog parity lost merged entity %q (canonical %q missing; backup retained at %s)", id, target, backupPath)
		}
	}
	for id := range beforeArtifacts {
		if _, present := afterArtifacts[id]; !present {
			return fmt.Errorf("legacy catalog parity lost unrelated artifact %q (backup retained at %s)", id, backupPath)
		}
	}
	for id := range beforeLinks {
		if _, present := afterLinks[id]; !present {
			return fmt.Errorf("legacy catalog parity lost unrelated archive link %q (backup retained at %s)", id, backupPath)
		}
	}
	for id := range beforeTags {
		if _, present := afterTags[id]; !present {
			return fmt.Errorf("legacy catalog parity lost unrelated tag %q (backup retained at %s)", id, backupPath)
		}
	}
	var afterFTS int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM library_search_fts`).Scan(&afterFTS); err != nil {
		return fmt.Errorf("read post-import search index (backup retained at %s): %w", backupPath, err)
	}
	if int64(len(afterEntities)) != afterFTS {
		return fmt.Errorf("legacy catalog parity failed: canonical entities=%d, FTS rows=%d (backup retained at %s)", len(afterEntities), afterFTS, backupPath)
	}
	for _, mod := range catalog.Mods {
		var gotName, gotKind, gotEntitySourceID, gotLinkSourceID, gotPath, ftsContent, payloadJSON string
		if err := tx.QueryRowContext(ctx, `SELECT e.display_name,e.kind,e.source_id,l.source_id,l.path
			FROM entities e JOIN archive_links l ON l.entity_id=e.id
			WHERE e.id=? AND l.path=? COLLATE NOCASE`, mod.EntityID, mod.Path).Scan(&gotName, &gotKind, &gotEntitySourceID, &gotLinkSourceID, &gotPath); err != nil {
			return fmt.Errorf("legacy catalog representative lookup %q failed (backup retained at %s): %w", mod.EntityID, backupPath, err)
		}
		representative, ok := representatives[mod.EntityID]
		if !ok {
			return fmt.Errorf("legacy catalog representative missing for %q (backup retained at %s)", mod.EntityID, backupPath)
		}
		if gotName != representative.DisplayName || gotKind != string(representative.Kind) || gotEntitySourceID != representative.SourceID ||
			gotLinkSourceID != mod.SourceID || !strings.EqualFold(gotPath, mod.Path) {
			return fmt.Errorf("legacy catalog representative parity failed for %q (backup retained at %s)", mod.EntityID, backupPath)
		}
		if err := tx.QueryRowContext(ctx, `SELECT content FROM library_search_fts WHERE entity_id=?`, mod.EntityID).Scan(&ftsContent); err != nil {
			return fmt.Errorf("legacy catalog FTS lookup %q failed (backup retained at %s): %w", mod.EntityID, backupPath, err)
		}
		if !strings.Contains(ftsContent, representative.DisplayName) || (mod.Path != "" && !strings.Contains(ftsContent, mod.Path)) {
			return fmt.Errorf("legacy catalog FTS parity failed for %q (backup retained at %s)", mod.EntityID, backupPath)
		}
		if err := tx.QueryRowContext(ctx, `SELECT payload_json FROM legacy_catalog_mods WHERE ordinal=?`, mod.Ordinal).Scan(&payloadJSON); err != nil {
			return fmt.Errorf("legacy catalog payload lookup %d failed (backup retained at %s): %w", mod.Ordinal, backupPath, err)
		}
		if strings.TrimSpace(payloadJSON) != strings.TrimSpace(mod.PayloadJSON) {
			return fmt.Errorf("legacy catalog payload parity failed for ordinal %d (backup retained at %s)", mod.Ordinal, backupPath)
		}
		for _, tag := range mod.Tags {
			resolvedID := tagIDs[strings.ToLower(tag.ID)]
			if resolvedID == "" {
				resolvedID = tagIDs[strings.ToLower(tag.Name)]
			}
			var assignmentCount int
			if resolvedID == "" {
				return fmt.Errorf("legacy catalog tag %q has no resolved ID (backup retained at %s)", tag.Name, backupPath)
			}
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mod_tag_entities WHERE tag_id=? AND entity_id=?`, resolvedID, mod.EntityID).Scan(&assignmentCount); err != nil {
				return fmt.Errorf("legacy catalog tag lookup %q failed (backup retained at %s): %w", tag.Name, backupPath, err)
			}
			if assignmentCount != 1 {
				return fmt.Errorf("legacy catalog tag parity failed for %q on entity %q (backup retained at %s)", tag.Name, mod.EntityID, backupPath)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO legacy_catalog_state(id,status,schema_version,catalog_json,last_scan_present,last_scan_is_null,last_scan_json,imported_at)
		VALUES(1,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,schema_version=excluded.schema_version,catalog_json=excluded.catalog_json,last_scan_present=excluded.last_scan_present,last_scan_is_null=excluded.last_scan_is_null,last_scan_json=excluded.last_scan_json,imported_at=excluded.imported_at`,
		legacyCatalogImportImported, legacyCatalogSchemaVersion, catalog.PayloadJSON, boolInt(catalog.LastScanPresent), boolInt(catalog.LastScanIsNull), catalog.LastScanJSON, nowUTC()); err != nil {
		return fmt.Errorf("persist legacy catalog state (backup retained at %s): %w", backupPath, err)
	}
	var stateStatus string
	var stateVersion, statePresent, stateNull int
	var statePayload, stateLastScan string
	if err := tx.QueryRowContext(ctx, `SELECT status,schema_version,catalog_json,last_scan_present,last_scan_is_null,last_scan_json FROM legacy_catalog_state WHERE id=1`).Scan(&stateStatus, &stateVersion, &statePayload, &statePresent, &stateNull, &stateLastScan); err != nil {
		return fmt.Errorf("read legacy catalog state parity (backup retained at %s): %w", backupPath, err)
	}
	if stateStatus != legacyCatalogImportImported || stateVersion != legacyCatalogSchemaVersion ||
		strings.TrimSpace(statePayload) != strings.TrimSpace(catalog.PayloadJSON) ||
		statePresent != boolInt(catalog.LastScanPresent) || stateNull != boolInt(catalog.LastScanIsNull) ||
		strings.TrimSpace(stateLastScan) != strings.TrimSpace(catalog.LastScanJSON) {
		return fmt.Errorf("legacy catalog state parity failed (backup retained at %s)", backupPath)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, legacyCatalogImportMarker, "1"); err != nil {
		return fmt.Errorf("mark legacy catalog imported (backup retained at %s): %w", backupPath, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy catalog import (backup retained at %s): %w", backupPath, err)
	}
	return nil
}
func (s *Store) recordLegacyCatalogCutover(ctx context.Context, status string) error {
	if status != legacyCatalogImportAbsent && status != legacyCatalogImportImported {
		return fmt.Errorf("invalid legacy catalog cutover status %q", status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin legacy catalog %s cutover: %w", status, err)
	}
	defer func() { _ = tx.Rollback() }()
	if marked, err := legacyCatalogImportMarkedTx(ctx, tx); err != nil {
		return fmt.Errorf("check legacy catalog cutover marker: %w", err)
	} else if marked {
		return nil
	}
	now := nowUTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO legacy_catalog_state(id,status,schema_version,catalog_json,last_scan_present,last_scan_is_null,last_scan_json,imported_at)
		VALUES(1,?,0,'',0,0,'',?)`, status, now); err != nil {
		return fmt.Errorf("record legacy catalog %s cutover: %w", status, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, legacyCatalogImportMarker, status); err != nil {
		return fmt.Errorf("record legacy catalog %s marker: %w", status, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy catalog %s cutover: %w", status, err)
	}
	return nil
}

func (s *Store) legacyCatalogImportMarked(ctx context.Context) (bool, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM legacy_catalog_state WHERE id=1`).Scan(&status)
	if err == nil {
		if status != legacyCatalogImportAbsent && status != legacyCatalogImportImported {
			return false, fmt.Errorf("invalid legacy catalog cutover status %q", status)
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) && !strings.Contains(strings.ToLower(err.Error()), "no such table") {
		return false, err
	}
	var value string
	err = s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, legacyCatalogImportMarker).Scan(&value)
	if err == nil {
		return strings.TrimSpace(value) != "", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return false, nil
}

func legacyCatalogImportMarkedTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM legacy_catalog_state WHERE id=1`).Scan(&status)
	if err == nil {
		if status != legacyCatalogImportAbsent && status != legacyCatalogImportImported {
			return false, fmt.Errorf("invalid legacy catalog cutover status %q", status)
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) && !strings.Contains(strings.ToLower(err.Error()), "no such table") {
		return false, err
	}
	var value string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, legacyCatalogImportMarker).Scan(&value)
	if err == nil {
		return strings.TrimSpace(value) != "", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return false, nil
}

func parseAndValidateLegacyCatalog(path string) (importedLegacyCatalog, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return importedLegacyCatalog{}, fmt.Errorf("read legacy catalog %s: %w", path, err)
	}
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&raw); err != nil {
		return importedLegacyCatalog{}, fmt.Errorf("legacy catalog is not valid JSON (%s): %w", path, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return importedLegacyCatalog{}, fmt.Errorf("legacy catalog %s contains trailing JSON", path)
		}
		return importedLegacyCatalog{}, fmt.Errorf("legacy catalog %s has trailing data: %w", path, err)
	}
	if len(raw) == 0 {
		return importedLegacyCatalog{}, fmt.Errorf("legacy catalog %s is empty", path)
	}
	versionRaw, ok := raw["schemaVersion"]
	if !ok {
		return importedLegacyCatalog{}, fmt.Errorf("legacy catalog %s is missing schemaVersion", path)
	}
	var version int
	if err := json.Unmarshal(versionRaw, &version); err != nil || version != legacyCatalogSchemaVersion {
		return importedLegacyCatalog{}, fmt.Errorf("unsupported legacy catalog schema in %s: want schemaVersion %d", path, legacyCatalogSchemaVersion)
	}
	modsRaw, ok := raw["mods"]
	if !ok {
		return importedLegacyCatalog{}, fmt.Errorf("legacy catalog %s is missing mods", path)
	}
	if bytes.Equal(bytes.TrimSpace(modsRaw), []byte("null")) {
		return importedLegacyCatalog{}, fmt.Errorf("legacy catalog %s has null mods; expected an array", path)
	}
	if len(modsRaw) == 0 || bytes.TrimSpace(modsRaw)[0] != '[' {
		return importedLegacyCatalog{}, fmt.Errorf("legacy catalog %s has invalid mods; expected an array", path)
	}
	var rawMods []json.RawMessage
	if err := json.Unmarshal(modsRaw, &rawMods); err != nil {
		return importedLegacyCatalog{}, fmt.Errorf("decode legacy catalog mods %s: %w", path, err)
	}
	var document legacyCatalogDocument
	if err := json.Unmarshal(payload, &document); err != nil {
		return importedLegacyCatalog{}, fmt.Errorf("decode legacy catalog %s: %w", path, err)
	}
	for _, field := range []struct {
		name  string
		raw   json.RawMessage
		value string
	}{
		{"createdAt", raw["createdAt"], document.CreatedAt},
		{"updatedAt", raw["updatedAt"], document.UpdatedAt},
	} {
		if field.raw != nil && !bytes.Equal(bytes.TrimSpace(field.raw), []byte("null")) {
			var check string
			if err := json.Unmarshal(field.raw, &check); err != nil {
				return importedLegacyCatalog{}, fmt.Errorf("legacy catalog %s field %s must be text: %w", path, field.name, err)
			}
			if _, err := parseLegacyTimestamp(check, field.name); err != nil {
				return importedLegacyCatalog{}, fmt.Errorf("legacy catalog %s: %w", path, err)
			}
		}
	}
	lastScanJSON := ""
	lastScanPresent := false
	lastScanIsNull := false
	if rawLastScan, exists := raw["lastScan"]; exists {
		lastScanPresent = true
		lastScanJSON = string(bytes.TrimSpace(rawLastScan))
		lastScanIsNull = bytes.Equal([]byte(lastScanJSON), []byte("null"))
		if !lastScanIsNull {
			var lastScan map[string]json.RawMessage
			if err := json.Unmarshal(rawLastScan, &lastScan); err != nil {
				return importedLegacyCatalog{}, fmt.Errorf("legacy catalog %s lastScan must be an object or null: %w", path, err)
			}
		}
	}
	result := importedLegacyCatalog{
		CreatedAt: document.CreatedAt, UpdatedAt: document.UpdatedAt,
		PayloadJSON: string(bytes.TrimSpace(payload)), LastScanJSON: lastScanJSON,
		LastScanPresent: lastScanPresent, LastScanIsNull: lastScanIsNull,
		Mods: make([]importedLegacyMod, 0, len(document.Mods)),
	}
	if len(rawMods) != len(document.Mods) {
		return importedLegacyCatalog{}, fmt.Errorf("legacy catalog %s mods decode count mismatch", path)
	}
	if result.CreatedAt == "" {
		result.CreatedAt = result.UpdatedAt
	}
	if result.UpdatedAt == "" {
		result.UpdatedAt = result.CreatedAt
	}
	createdAt, _ := parseLegacyTimestamp(result.CreatedAt, "createdAt")
	updatedAt, _ := parseLegacyTimestamp(result.UpdatedAt, "updatedAt")
	seenEntity := map[string]int{}
	seenPath := map[string]int{}
	seenLink := map[string]int{}
	seenTagNames := map[string]importedLegacyTag{}
	for index, mod := range document.Mods {
		converted, tags, err := validateLegacyMod(mod, index, createdAt, updatedAt)
		if err != nil {
			return importedLegacyCatalog{}, fmt.Errorf("validate legacy catalog mod %d: %w", index, err)
		}
		converted.Ordinal = index
		converted.PayloadJSON = string(bytes.TrimSpace(rawMods[index]))
		if previous, exists := seenEntity[converted.EntityID]; exists {
			return importedLegacyCatalog{}, fmt.Errorf("duplicate mod identity %q at indexes %d and %d", converted.EntityID, previous, index)
		}
		seenEntity[converted.EntityID] = index
		pathKey := strings.ToLower(filepath.Clean(converted.Path))
		if previous, exists := seenPath[pathKey]; exists {
			return importedLegacyCatalog{}, fmt.Errorf("duplicate archive path %q at indexes %d and %d", converted.Path, previous, index)
		}
		seenPath[pathKey] = index
		if previous, exists := seenLink[converted.LinkID]; exists {
			return importedLegacyCatalog{}, fmt.Errorf("duplicate archive link identity %q at indexes %d and %d", converted.LinkID, previous, index)
		}
		seenLink[converted.LinkID] = index
		for _, tag := range tags {
			key := strings.ToLower(tag.Name)
			if prior, exists := seenTagNames[key]; exists {
				if prior.ID != tag.ID || prior.Color != tag.Color || prior.Icon != tag.Icon {
					return importedLegacyCatalog{}, fmt.Errorf("tag name %q has conflicting definitions", tag.Name)
				}
			} else {
				seenTagNames[key] = tag
				result.Tags = append(result.Tags, tag)
			}
		}
		converted.Tags = tags
		result.Mods = append(result.Mods, converted)
	}
	return result, nil
}

func validateLegacyMod(mod legacyCatalogMod, index int, createdAt, updatedAt time.Time) (importedLegacyMod, []importedLegacyTag, error) {
	textFields := []struct{ name, value string }{
		{"id", mod.ID}, {"path", mod.Path}, {"filename", mod.Filename}, {"originalPath", mod.OriginalPath},
		{"location", mod.Location}, {"source", mod.Source}, {"activeRelativePath", mod.ActiveRelativePath}, {"modifiedAt", mod.ModifiedAt},
		{"fingerprint", mod.Fingerprint}, {"sha256", mod.SHA256}, {"fullSha256", mod.FullSHA256}, {"autoCategory", mod.AutoCategory},
		{"kind", mod.Kind}, {"title", mod.Title}, {"description", mod.Description}, {"notes", mod.Notes}, {"health", mod.Health},
	}
	for _, field := range textFields {
		if err := validateLegacyText(field.value, field.name, false); err != nil {
			return importedLegacyMod{}, nil, err
		}
	}
	if mod.Path == "" {
		return importedLegacyMod{}, nil, errors.New("path must be non-empty")
	}
	if mod.Size < 0 || mod.EntryCount < 0 || mod.NestedArchiveCount < 0 {
		return importedLegacyMod{}, nil, errors.New("size and archive counts must not be negative")
	}
	for _, value := range []struct {
		name string
		data []string
	}{
		{"contentTags", mod.ContentTags},
	} {
		for _, entry := range value.data {
			if err := validateLegacyText(entry, value.name, false); err != nil {
				return importedLegacyMod{}, nil, err
			}
		}
	}
	for root, entries := range mod.Namespaces {
		if err := validateLegacyText(root, "namespace", false); err != nil {
			return importedLegacyMod{}, nil, err
		}
		for _, entry := range entries {
			if err := validateLegacyText(entry, "namespace", false); err != nil {
				return importedLegacyMod{}, nil, err
			}
		}
	}
	modifiedAt, err := parseLegacyTimestamp(mod.ModifiedAt, "modifiedAt")
	if err != nil {
		return importedLegacyMod{}, nil, err
	}
	if mod.ArchiveDescription != nil {
		if err := validateLegacyText(*mod.ArchiveDescription, "archiveDescription", false); err != nil {
			return importedLegacyMod{}, nil, err
		}
	}
	if mod.Author != nil {
		if err := validateLegacyText(*mod.Author, "author", false); err != nil {
			return importedLegacyMod{}, nil, err
		}
	}
	if mod.Version != nil {
		if err := validateLegacyText(*mod.Version, "version", false); err != nil {
			return importedLegacyMod{}, nil, err
		}
	}
	for _, issue := range append(append([]modkit.Issue{}, mod.Issues...), mod.RuntimeIssues...) {
		if err := validateLegacyIssue(issue); err != nil {
			return importedLegacyMod{}, nil, err
		}
	}
	for _, document := range mod.MetadataDocuments {
		if err := validateLegacyText(document.Path, "metadata document path", false); err != nil {
			return importedLegacyMod{}, nil, err
		}
		if document.Data == nil {
			continue
		}
		encoded, err := json.Marshal(document.Data)
		if err != nil {
			return importedLegacyMod{}, nil, fmt.Errorf("metadata document %q is not JSON: %w", document.Path, err)
		}
		if !json.Valid(encoded) {
			return importedLegacyMod{}, nil, fmt.Errorf("metadata document %q is not valid JSON", document.Path)
		}
	}
	if len(mod.Manifest) > 0 && !bytes.Equal(bytes.TrimSpace(mod.Manifest), []byte("null")) {
		var manifest modkit.Manifest
		if err := json.Unmarshal(mod.Manifest, &manifest); err != nil {
			return importedLegacyMod{}, nil, fmt.Errorf("manifest is invalid JSON: %w", err)
		}
	}
	kind, err := legacyModKind(mod.Kind, mod.AutoCategory)
	if err != nil {
		return importedLegacyMod{}, nil, err
	}
	sourceClass, err := legacySourceClass(mod.Source)
	if err != nil {
		return importedLegacyMod{}, nil, err
	}
	entityID := strings.TrimSpace(mod.ID)
	if entityID == "" {
		entityID = stableLegacyID("entity", mod.Path, mod.Fingerprint, mod.Title, strconv.Itoa(index))
	}
	if err := validateLegacyIdentity(entityID, "id"); err != nil {
		return importedLegacyMod{}, nil, err
	}
	fingerprint := strings.TrimSpace(mod.Fingerprint)
	if fingerprint == "" {
		fingerprint = stableLegacyID("fingerprint", entityID, mod.Path)
	}
	artifactID := stableLegacyID("artifact", fingerprint)
	linkID := stableLegacyID("link", entityID, mod.Path)
	filename := mod.Filename
	if filename == "" {
		filename = filepath.Base(mod.Path)
	}
	manifest := modkit.Manifest{
		SchemaVersion:      modkit.SchemaVersion,
		AnalyzerVersion:    modkit.AnalyzerVersion,
		ArchivePath:        mod.Path,
		Filename:           filename,
		SizeBytes:          mod.Size,
		CentralFingerprint: fingerprint,
		FullSHA256:         firstNonEmpty(mod.FullSHA256, mod.SHA256),
		ValidArchive:       mod.ValidArchive,
		Kind:               kind,
		ContentTags:        append([]string(nil), mod.ContentTags...),
		Namespaces:         cloneStringMap(mod.Namespaces),
		Title:              mod.Title,
		Description:        firstNonEmpty(mod.Description, derefString(mod.ArchiveDescription)),
		Author:             derefString(mod.Author),
		Version:            derefString(mod.Version),
		EntryCount:         mod.EntryCount,
		CompressedBytes:    mod.CompressedBytes,
		UncompressedBytes:  mod.UncompressedBytes,
		MetadataDocuments:  append([]modkit.MetadataDocument(nil), mod.MetadataDocuments...),
		Issues:             append(append([]modkit.Issue(nil), mod.Issues...), mod.RuntimeIssues...),
	}
	if mod.Wrapper != nil {
		manifest.Wrapper = *mod.Wrapper
	}
	if !modifiedAt.IsZero() {
		manifest.ModifiedAt = modifiedAt
		manifest.AnalyzedAt = modifiedAt
	}
	if len(mod.Manifest) > 0 && !bytes.Equal(bytes.TrimSpace(mod.Manifest), []byte("null")) {
		var supplied modkit.Manifest
		if err := json.Unmarshal(mod.Manifest, &supplied); err != nil {
			return importedLegacyMod{}, nil, err
		}
		if supplied.ArchivePath != "" {
			manifest.ArchivePath = supplied.ArchivePath
		}
		if supplied.Filename != "" {
			manifest.Filename = supplied.Filename
		}
		if supplied.ModifiedAt.IsZero() == false {
			manifest.ModifiedAt = supplied.ModifiedAt
		}
		if supplied.AnalyzedAt.IsZero() == false {
			manifest.AnalyzedAt = supplied.AnalyzedAt
		}
		if supplied.CentralFingerprint != "" {
			manifest.CentralFingerprint = supplied.CentralFingerprint
		}
		if supplied.FullSHA256 != "" {
			manifest.FullSHA256 = supplied.FullSHA256
		}
	}
	manifestJSONBytes, err := json.Marshal(manifest)
	if err != nil {
		return importedLegacyMod{}, nil, fmt.Errorf("encode canonical manifest: %w", err)
	}
	rootPath := filepath.Dir(mod.Path)
	discoveredAt := formatLegacyTime(createdAt)
	if discoveredAt == "" {
		discoveredAt = formatLegacyTime(modifiedAt)
	}
	if discoveredAt == "" {
		discoveredAt = formatLegacyTime(updatedAt)
	}
	lastSeenAt := formatLegacyTime(updatedAt)
	if lastSeenAt == "" {
		lastSeenAt = formatLegacyTime(modifiedAt)
	}
	if lastSeenAt == "" {
		lastSeenAt = discoveredAt
	}
	tags, tagErr := parseLegacyTags(mod.Tags, entityID)
	if tagErr != nil {
		return importedLegacyMod{}, nil, tagErr
	}
	return importedLegacyMod{
		EntityID: entityID, ArtifactID: artifactID, LinkID: linkID, DisplayName: mod.Title,
		Kind: kind, Path: mod.Path, RootPath: rootPath, Active: !mod.Missing,
		SourceID: sourceClass, SizeBytes: mod.Size, ModifiedAt: mod.ModifiedAt,
		DiscoveredAt: discoveredAt, LastSeenAt: lastSeenAt, Fingerprint: fingerprint,
		SHA256: firstNonEmpty(manifest.FullSHA256, mod.SHA256), Manifest: manifest,
		ManifestJSON: string(manifestJSONBytes),
	}, tags, nil
}

func parseLegacyTags(rawTags []json.RawMessage, entityID string) ([]importedLegacyTag, error) {
	result := make([]importedLegacyTag, 0, len(rawTags))
	seen := map[string]struct{}{}
	for index, raw := range rawTags {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, fmt.Errorf("tag %d for entity %q is null", index, entityID)
		}
		var name string
		if err := json.Unmarshal(raw, &name); err == nil {
			name = strings.TrimSpace(name)
			if err := validateLegacyTagName(name); err != nil {
				return nil, fmt.Errorf("tag %d for entity %q: %w", index, entityID, err)
			}
			tag := importedLegacyTag{ID: stableLegacyID("tag", strings.ToLower(name)), Name: name, Color: defaultModTagColor, Icon: defaultModTagIcon}
			if _, exists := seen[strings.ToLower(name)]; exists {
				return nil, fmt.Errorf("duplicate tag %q for entity %q", name, entityID)
			}
			seen[strings.ToLower(name)] = struct{}{}
			result = append(result, tag)
			continue
		}
		var object struct {
			ID    string  `json:"id"`
			Name  string  `json:"name"`
			Color *string `json:"color"`
			Icon  *string `json:"icon"`
		}
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, fmt.Errorf("tag %d for entity %q must be text or object: %w", index, entityID, err)
		}
		object.Name = strings.TrimSpace(object.Name)
		if err := validateLegacyTagName(object.Name); err != nil {
			return nil, fmt.Errorf("tag %d for entity %q: %w", index, entityID, err)
		}
		if err := validateLegacyIdentity(object.ID, "tag id"); err != nil && object.ID != "" {
			return nil, fmt.Errorf("tag %d for entity %q: %w", index, entityID, err)
		}
		color := defaultModTagColor
		if object.Color != nil && *object.Color != "" {
			color = strings.ToLower(strings.TrimSpace(*object.Color))
			if _, err := normalizeModTagColor(color); err != nil {
				return nil, fmt.Errorf("tag %q: %w", object.Name, err)
			}
		}
		icon := defaultModTagIcon
		if object.Icon != nil && *object.Icon != "" {
			icon = strings.ToLower(strings.TrimSpace(*object.Icon))
			if _, err := normalizeModTagIcon(icon); err != nil {
				return nil, fmt.Errorf("tag %q: %w", object.Name, err)
			}
		}
		if _, exists := seen[strings.ToLower(object.Name)]; exists {
			return nil, fmt.Errorf("duplicate tag %q for entity %q", object.Name, entityID)
		}
		seen[strings.ToLower(object.Name)] = struct{}{}
		if object.ID == "" {
			object.ID = stableLegacyID("tag", strings.ToLower(object.Name))
		}
		result = append(result, importedLegacyTag{ID: object.ID, Name: object.Name, Color: color, Icon: icon})
	}
	return result, nil
}

func importLegacyTagTx(ctx context.Context, tx *sql.Tx, tag importedLegacyTag) (string, error) {
	var existingID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM mod_tags WHERE name=? COLLATE NOCASE`, tag.Name).Scan(&existingID)
	if err == nil {
		var name, color, icon string
		if err := tx.QueryRowContext(ctx, `SELECT name,color,icon FROM mod_tags WHERE id=?`, existingID).Scan(&name, &color, &icon); err != nil {
			return "", err
		}
		if !strings.EqualFold(name, tag.Name) || color != tag.Color || icon != tag.Icon {
			return "", fmt.Errorf("existing tag %q conflicts with imported definition", tag.Name)
		}
		return existingID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM mod_tags WHERE id=?`, tag.ID).Scan(&existingID); err == nil {
		var name, color, icon string
		if err := tx.QueryRowContext(ctx, `SELECT name,color,icon FROM mod_tags WHERE id=?`, existingID).Scan(&name, &color, &icon); err != nil {
			return "", err
		}
		if !strings.EqualFold(name, tag.Name) || color != tag.Color || icon != tag.Icon {
			return "", fmt.Errorf("existing tag identity %q conflicts with imported definition", tag.ID)
		}
		return existingID, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mod_tags(id,name,color,icon,created_at,updated_at) VALUES(?,?,?,?,?,?)`, tag.ID, tag.Name, tag.Color, tag.Icon, nowUTC(), nowUTC()); err != nil {
		return "", err
	}
	return tag.ID, nil
}

func resolveLegacyCanonicalTx(ctx context.Context, tx *sql.Tx, mod *importedLegacyMod) error {
	return resolveLegacyCanonicalMergesTx(ctx, tx, mod, nil)
}

func resolveLegacyCanonicalMergesTx(ctx context.Context, tx *sql.Tx, mod *importedLegacyMod, expectedMerges map[string]string) error {
	if mod == nil {
		return errors.New("legacy mod is nil")
	}
	var pathEntity, pathLink string
	pathErr := tx.QueryRowContext(ctx, `SELECT entity_id,id FROM archive_links WHERE path=? COLLATE NOCASE`, mod.Path).Scan(&pathEntity, &pathLink)
	if pathErr != nil && !errors.Is(pathErr, sql.ErrNoRows) {
		return pathErr
	}
	var fingerprintEntity string
	if mod.Fingerprint != "" {
		fingerprintErr := tx.QueryRowContext(ctx, `SELECT l.entity_id
			FROM archive_links l JOIN artifacts a ON a.id=l.artifact_id
			WHERE a.central_fingerprint=? COLLATE NOCASE
			ORDER BY l.active DESC,l.last_seen_at DESC,l.id ASC LIMIT 1`, mod.Fingerprint).Scan(&fingerprintEntity)
		if fingerprintErr != nil && !errors.Is(fingerprintErr, sql.ErrNoRows) {
			return fingerprintErr
		}
	}
	canonicalEntity := pathEntity
	if canonicalEntity == "" {
		canonicalEntity = fingerprintEntity
	}
	if canonicalEntity == "" {
		var existingEntity string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM entities WHERE id=?`, mod.EntityID).Scan(&existingEntity); err == nil {
			canonicalEntity = existingEntity
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if canonicalEntity == "" {
		return nil
	}
	merged := map[string]struct{}{canonicalEntity: {}}
	for _, candidate := range []string{fingerprintEntity, mod.EntityID} {
		if candidate == "" {
			continue
		}
		if _, alreadyMerged := merged[candidate]; alreadyMerged {
			continue
		}
		merged[candidate] = struct{}{}
		var existing int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities WHERE id=?`, candidate).Scan(&existing); err != nil {
			return err
		}
		if existing != 0 {
			if err := mergeEntityRecordsTx(ctx, tx, candidate, canonicalEntity); err != nil {
				return err
			}
			if expectedMerges != nil {
				expectedMerges[candidate] = canonicalEntity
			}
		}
	}
	mod.EntityID = canonicalEntity
	if pathLink != "" {
		mod.LinkID = pathLink
	}
	return nil
}

func importLegacyArtifactTx(ctx context.Context, tx *sql.Tx, mod importedLegacyMod) (string, error) {
	var existingID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM artifacts WHERE central_fingerprint=? COLLATE NOCASE`, mod.Fingerprint).Scan(&existingID)
	if err == nil {
		var sha string
		if err := tx.QueryRowContext(ctx, `SELECT sha256 FROM artifacts WHERE id=?`, existingID).Scan(&sha); err != nil {
			return "", err
		}
		if mod.SHA256 != "" && sha != "" && sha != mod.SHA256 {
			return "", fmt.Errorf("existing artifact %q conflicts with imported hash", existingID)
		}
		analyzedAt := mod.ModifiedAt
		if !mod.Manifest.AnalyzedAt.IsZero() {
			analyzedAt = mod.Manifest.AnalyzedAt.UTC().Format(time.RFC3339Nano)
		}
		shaValue := mod.SHA256
		if shaValue == "" {
			shaValue = sha
		}
		if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET sha256=?,size_bytes=?,manifest_json=?,analyzer_version=?,analyzed_at=? WHERE id=?`, shaValue, mod.SizeBytes, mod.ManifestJSON, modkit.AnalyzerVersion, analyzedAt, existingID); err != nil {
			return "", err
		}
		return existingID, nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM artifacts WHERE id=?`, mod.ArtifactID).Scan(&existingID); err == nil {
		var fingerprint, sha, manifestJSON string
		if err := tx.QueryRowContext(ctx, `SELECT central_fingerprint,sha256,manifest_json FROM artifacts WHERE id=?`, existingID).Scan(&fingerprint, &sha, &manifestJSON); err != nil {
			return "", err
		}
		if !strings.EqualFold(fingerprint, mod.Fingerprint) || (mod.SHA256 != "" && sha != "" && sha != mod.SHA256) {
			return "", fmt.Errorf("existing artifact identity %q conflicts with imported artifact", existingID)
		}
		shaValue := mod.SHA256
		if shaValue == "" {
			shaValue = sha
		}
		analyzedAt := mod.ModifiedAt
		if !mod.Manifest.AnalyzedAt.IsZero() {
			analyzedAt = mod.Manifest.AnalyzedAt.UTC().Format(time.RFC3339Nano)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET sha256=?,size_bytes=?,manifest_json=?,analyzer_version=?,analyzed_at=? WHERE id=?`, shaValue, mod.SizeBytes, mod.ManifestJSON, modkit.AnalyzerVersion, analyzedAt, existingID); err != nil {
			return "", err
		}
		return existingID, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	analyzedAt := mod.ModifiedAt
	if !mod.Manifest.AnalyzedAt.IsZero() {
		analyzedAt = mod.Manifest.AnalyzedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO artifacts(id,central_fingerprint,sha256,size_bytes,manifest_json,analyzer_version,analyzed_at) VALUES(?,?,?,?,?,?,?)`, mod.ArtifactID, mod.Fingerprint, mod.SHA256, mod.SizeBytes, mod.ManifestJSON, modkit.AnalyzerVersion, analyzedAt)
	return mod.ArtifactID, err
}

func importLegacyEntityTx(ctx context.Context, tx *sql.Tx, mod importedLegacyMod) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO entities(id,display_name,kind,source_id,created_at,updated_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET display_name=excluded.display_name,kind=excluded.kind,source_id=excluded.source_id,
			created_at=CASE WHEN excluded.created_at<>'' THEN excluded.created_at ELSE entities.created_at END,
			updated_at=CASE WHEN excluded.updated_at<>'' THEN excluded.updated_at ELSE entities.updated_at END`,
		mod.EntityID, mod.DisplayName, mod.Kind, mod.SourceID, mod.DiscoveredAt, mod.LastSeenAt)
	return err
}

func importLegacyLinkTx(ctx context.Context, tx *sql.Tx, mod importedLegacyMod) error {
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT id FROM archive_links WHERE id=?`, mod.LinkID).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT id FROM archive_links WHERE path=? COLLATE NOCASE`, mod.Path).Scan(&existing)
	}
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO archive_links(id,entity_id,artifact_id,path,root_path,active,source_id,size_bytes,modified_at,discovered_at,last_seen_at,last_scan_id,basename_key)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, mod.LinkID, mod.EntityID, mod.ArtifactID, mod.Path, mod.RootPath, boolInt(mod.Active), mod.SourceID, mod.SizeBytes, mod.ModifiedAt, mod.DiscoveredAt, mod.LastSeenAt, "", strings.ToLower(filepath.Base(mod.Path))+"\x00"+mod.Fingerprint)
		return err
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE archive_links SET entity_id=?,artifact_id=?,path=?,root_path=?,active=?,source_id=?,size_bytes=?,modified_at=?,discovered_at=?,last_seen_at=?,last_scan_id=?,basename_key=? WHERE id=?`,
		mod.EntityID, mod.ArtifactID, mod.Path, mod.RootPath, boolInt(mod.Active), mod.SourceID, mod.SizeBytes, mod.ModifiedAt, mod.DiscoveredAt, mod.LastSeenAt, "", strings.ToLower(filepath.Base(mod.Path))+"\x00"+mod.Fingerprint, existing)
	return err
}

func reconcileLegacyRepresentativesTx(ctx context.Context, tx *sql.Tx, mods []importedLegacyMod) (map[string]importedLegacyMod, error) {
	representatives := make(map[string]importedLegacyMod, len(mods))
	for index := range mods {
		mod := &mods[index]
		if err := tx.QueryRowContext(ctx, `SELECT id,entity_id,artifact_id FROM archive_links WHERE path=? COLLATE NOCASE`, mod.Path).
			Scan(&mod.LinkID, &mod.EntityID, &mod.ArtifactID); err != nil {
			return nil, err
		}
		current, exists := representatives[mod.EntityID]
		if !exists || legacyRepresentativePreferred(*mod, current) {
			representatives[mod.EntityID] = *mod
		}
	}
	return representatives, nil
}

func legacyRepresentativePreferred(candidate, current importedLegacyMod) bool {
	if candidate.Active != current.Active {
		return candidate.Active
	}
	if candidate.LastSeenAt != current.LastSeenAt {
		return candidate.LastSeenAt > current.LastSeenAt
	}
	return candidate.LinkID > current.LinkID
}

func (s *Store) importLegacyCatalogOnceNoMarker(ctx context.Context, path string) error {
	return s.importLegacyCatalogOnce(ctx, path)
}

func backupLegacyCatalog(path string) (string, error) {
	for index := 0; ; index++ {
		candidate := path + legacyCatalogBackupSuffix
		if index > 0 {
			candidate += "." + strconv.Itoa(index)
		}
		input, err := os.Open(path)
		if err != nil {
			return "", err
		}
		output, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			_ = input.Close()
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", err
		}
		_, copyErr := io.Copy(output, input)
		if copyErr == nil {
			copyErr = output.Sync()
		}
		closeOutErr := output.Close()
		closeInErr := input.Close()
		if copyErr == nil {
			copyErr = closeOutErr
		}
		if copyErr == nil {
			copyErr = closeInErr
		}
		if copyErr != nil {
			_ = os.Remove(candidate)
			return "", copyErr
		}
		return candidate, nil
	}
}

func parseLegacyTimestamp(value, name string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be an RFC3339 timestamp: %w", name, err)
	}
	return parsed.UTC(), nil
}

func formatLegacyTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func validateLegacyText(value, name string, required bool) error {
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s contains invalid UTF-8 or NUL", name)
	}
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must be non-empty", name)
	}
	return nil
}

func validateLegacyIdentity(value, name string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must be non-empty", name)
	}
	return validateLegacyText(value, name, true)
}

func validateLegacyTagName(value string) error {
	if err := validateLegacyText(value, "tag name", true); err != nil {
		return err
	}
	if len(value) > 80 || strings.ContainsAny(value, "\r\n\t") {
		return errors.New("tag name must contain 1 to 80 characters on one line")
	}
	return nil
}

func validateLegacyIssue(issue modkit.Issue) error {
	if err := validateLegacyText(issue.Code, "issue code", true); err != nil {
		return err
	}
	if err := validateLegacyText(issue.Message, "issue message", false); err != nil {
		return err
	}
	switch issue.Severity {
	case modkit.SeverityInfo, modkit.SeverityWarning, modkit.SeverityError:
	default:
		return fmt.Errorf("issue %q has unsupported severity %q", issue.Code, issue.Severity)
	}
	return nil
}

func legacySourceClass(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "user-added", nil
	case "repository":
		return "beamng-repository", nil
	case "third-party":
		return "user-added", nil
	default:
		return "", fmt.Errorf("unsupported legacy source %q; only repository and third-party are accepted", value)
	}
}

func legacyModKind(explicit, category string) (modkit.Kind, error) {
	value := strings.ToLower(strings.TrimSpace(explicit))
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(category))
	}
	switch value {
	case "vehicle", "vehicles", "car", "cars", "truck", "trucks":
		return modkit.KindVehicle, nil
	case "map", "maps", "level", "levels":
		return modkit.KindMap, nil
	case "ui", "uis", "app", "interface":
		return modkit.KindUI, nil
	case "script", "scripts", "lua", "extension":
		return modkit.KindScript, nil
	case "mixed":
		return modkit.KindMixed, nil
	case "", "unknown", "invalid archive":
		return modkit.KindUnknown, nil
	default:
		// autoCategory is a presentation/search field in the old catalog and
		// can contain values such as "vehicle + map". Preserve such records as
		// unknown rather than inventing a new canonical kind.
		if explicit != "" {
			return modkit.Kind(""), fmt.Errorf("unsupported legacy kind %q", explicit)
		}
		return modkit.KindUnknown, nil
	}
}

func stableLegacyID(prefix string, values ...string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, prefix)
	for _, value := range values {
		_, _ = io.WriteString(hash, "\x00")
		_, _ = io.WriteString(hash, value)
	}
	sum := hash.Sum(nil)
	return prefix + "-" + hex.EncodeToString(sum[:16])
}

func cloneStringMap(input map[string][]string) map[string][]string {
	if input == nil {
		return map[string][]string{}
	}
	result := make(map[string][]string, len(input))
	for key, values := range input {
		result[key] = append([]string(nil), values...)
	}
	return result
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
