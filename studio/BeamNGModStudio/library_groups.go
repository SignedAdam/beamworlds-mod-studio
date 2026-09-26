package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
	"modernc.org/sqlite"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

// Match the table's case/accent-insensitive, numeric string ordering in both
// SQL pagination and hydrated filtering. Collators have mutable buffers.
var librarySortCollators = sync.Pool{New: func() any {
	return collate.New(language.Und, collate.Loose, collate.Numeric)
}}

func compareLibrarySortText(left, right string) int {
	comparator := librarySortCollators.Get().(*collate.Collator)
	result := comparator.CompareString(left, right)
	librarySortCollators.Put(comparator)
	return result
}

func init() {
	sqlite.MustRegisterCollationUtf8("library_sort", compareLibrarySortText)
}

// Reserved group ID for mods not in any promoted group.
const ungroupedGroupID = "__ungrouped__"

// Settings key prefix for per-group fold state.  Each key is
// groupCollapsePrefix + tagID (or ungroupedGroupID) with value "1"
// for collapsed, absent/empty for expanded.
const groupCollapsePrefix = "library_group_collapsed:"

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

// LibraryGroupRow is one row in the paged group stream.
type LibraryGroupRow struct {
	RowType    string       `json:"rowType"`    // "group" | "mod"
	GroupID    string       `json:"groupId"`    // tag ID, or ungroupedGroupID
	Label      string       `json:"label"`      // group name; empty for mod rows
	ModCount   int          `json:"modCount"`   // group rows only
	SizeBytes  int64        `json:"sizeBytes"`  // group rows only
	Collapsed  bool         `json:"collapsed"`  // group rows only
	MatchCount int          `json:"matchCount"` // group rows only, while a search is active
	Item       *LibraryItem `json:"item"`       // mod rows carry summary metadata; GetEntity returns full inspection details
}

// LibraryGroupPage is the paged result returned by LibraryGroupPage.
type LibraryGroupPage struct {
	Rows         []LibraryGroupRow `json:"rows"`
	TotalRows    int               `json:"totalRows"`
	DistinctMods int               `json:"distinctMods"`
	Page         int               `json:"page"`
	PageSize     int               `json:"pageSize"`
}

// ---------------------------------------------------------------------------
// AppService methods (the public contract surface)
// ---------------------------------------------------------------------------

func (service *AppService) LibraryGroupPage(ctx context.Context, health, kind, query, collectionID, scope string, page, pageSize int, sortKey string, sortDirection int) (LibraryGroupPage, error) {
	return service.store.ListLibraryGroupPage(ctx, health, kind, query, collectionID, scope, page, pageSize, sortKey, sortDirection)
}

func (service *AppService) SetTagGrouped(tagID string, grouped bool) (OrganizationState, error) {
	ctx := context.Background()
	if err := service.store.SetTagGrouped(ctx, tagID, grouped); err != nil {
		return OrganizationState{}, err
	}
	if grouped {
		// R4: promoting a tag creates its group folded.
		if err := service.store.writeGroupCollapsed(ctx, tagID, true); err != nil {
			return OrganizationState{}, err
		}
	} else {
		// R4: demoting drops fold state.
		_ = service.store.deleteGroupCollapsed(ctx, tagID)
	}
	return service.store.Organization(ctx)
}

func (service *AppService) SetGroupCollapsed(tagID string, collapsed bool) error {
	return service.store.writeGroupCollapsed(context.Background(), tagID, collapsed)
}

func (service *AppService) SetAllGroupsCollapsed(collapsed bool) error {
	return service.store.SetAllGroupsCollapsed(context.Background(), collapsed)
}

func (service *AppService) AddModsToGroup(tagID string, entityIDs []string) (OrganizationState, error) {
	ctx := context.Background()
	if err := service.store.AddModsToGroup(ctx, tagID, entityIDs); err != nil {
		return OrganizationState{}, err
	}
	return service.store.Organization(ctx)
}

func (service *AppService) RemoveModsFromGroup(tagID string, entityIDs []string) (OrganizationState, error) {
	ctx := context.Background()
	if err := service.store.RemoveModsFromGroup(ctx, tagID, entityIDs); err != nil {
		return OrganizationState{}, err
	}
	return service.store.Organization(ctx)
}

func (service *AppService) CreateGroupFromSelection(name string, entityIDs []string) (OrganizationState, error) {
	ctx := context.Background()
	if err := service.store.CreateGroupFromSelection(ctx, name, entityIDs); err != nil {
		return OrganizationState{}, err
	}
	return service.store.Organization(ctx)
}

// ---------------------------------------------------------------------------
// Store: fold-state helpers
// ---------------------------------------------------------------------------

func groupCollapseKey(groupID string) string {
	return groupCollapsePrefix + groupID
}

func (s *Store) readGroupCollapsed(ctx context.Context, groupID string) bool {
	val, err := s.readSetting(ctx, groupCollapseKey(groupID))
	if err != nil {
		return false
	}
	return val == "1"
}

func (s *Store) readGroupCollapsedTx(ctx context.Context, tx *sql.Tx, groupID string) bool {
	var val string
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, groupCollapseKey(groupID)).Scan(&val)
	if err != nil {
		return false
	}
	return val == "1"
}

func (s *Store) writeGroupCollapsed(ctx context.Context, groupID string, collapsed bool) error {
	if collapsed {
		return s.writeSetting(ctx, groupCollapseKey(groupID), "1")
	}
	return s.writeSetting(ctx, groupCollapseKey(groupID), "0")
}

func (s *Store) deleteGroupCollapsed(ctx context.Context, groupID string) error {
	return s.deleteSetting(ctx, groupCollapseKey(groupID))
}

// loadAllGroupFoldStatesTx reads every fold-state key inside the transaction.
func loadAllGroupFoldStatesTx(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key, value FROM settings WHERE key LIKE ? ESCAPE '\'`,
		strings.ReplaceAll(groupCollapsePrefix, "%", `\%`)+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[string]bool{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		groupID := strings.TrimPrefix(key, groupCollapsePrefix)
		states[groupID] = value == "1"
	}
	return states, rows.Err()
}

// ---------------------------------------------------------------------------
// Store: SetTagGrouped
// ---------------------------------------------------------------------------

func (s *Store) SetTagGrouped(ctx context.Context, tagID string, grouped bool) error {
	tagID = strings.TrimSpace(tagID)
	if tagID == "" {
		return errors.New("tag ID is required")
	}
	val := 0
	if grouped {
		val = 1
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE mod_tags SET grouped=?, updated_at=? WHERE id=?`, val, nowUTC(), tagID)
	return requireChanged(result, err, "tag")
}

// ---------------------------------------------------------------------------
// Store: SetAllGroupsCollapsed
// ---------------------------------------------------------------------------

func (s *Store) SetAllGroupsCollapsed(ctx context.Context, collapsed bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `SELECT id FROM mod_tags WHERE grouped=1`)
	if err != nil {
		return err
	}
	var tagIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		tagIDs = append(tagIDs, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	val := "0"
	if collapsed {
		val = "1"
	}
	for _, tagID := range tagIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			groupCollapseKey(tagID), val); err != nil {
			return err
		}
	}
	// Also set the ungrouped fold state.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		groupCollapseKey(ungroupedGroupID), val); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Store: AddModsToGroup
// ---------------------------------------------------------------------------

func (s *Store) AddModsToGroup(ctx context.Context, tagID string, entityIDs []string) error {
	tagID = strings.TrimSpace(tagID)
	if tagID == "" {
		return errors.New("tag ID is required")
	}
	if len(entityIDs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Verify tag exists and is grouped.
	var grouped int
	if err := tx.QueryRowContext(ctx, `SELECT grouped FROM mod_tags WHERE id=?`, tagID).Scan(&grouped); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("tag was not found")
		}
		return err
	}
	if grouped != 1 {
		return errors.New("tag is not a group")
	}

	now := nowUTC()
	for _, entityID := range entityIDs {
		entityID = strings.TrimSpace(entityID)
		if entityID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO mod_tag_entities(tag_id,entity_id,created_at) VALUES(?,?,?) ON CONFLICT(tag_id,entity_id) DO NOTHING`,
			tagID, entityID, now); err != nil {
			return err
		}
	}
	// Refresh FTS entries for affected entities.
	for _, entityID := range entityIDs {
		entityID = strings.TrimSpace(entityID)
		if entityID == "" {
			continue
		}
		if err := s.refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Store: RemoveModsFromGroup
// ---------------------------------------------------------------------------

func (s *Store) RemoveModsFromGroup(ctx context.Context, tagID string, entityIDs []string) error {
	tagID = strings.TrimSpace(tagID)
	if tagID == "" {
		return errors.New("tag ID is required")
	}
	if len(entityIDs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, entityID := range entityIDs {
		entityID = strings.TrimSpace(entityID)
		if entityID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM mod_tag_entities WHERE tag_id=? AND entity_id=?`,
			tagID, entityID); err != nil {
			return err
		}
	}
	for _, entityID := range entityIDs {
		entityID = strings.TrimSpace(entityID)
		if entityID == "" {
			continue
		}
		if err := s.refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Store: CreateGroupFromSelection
// ---------------------------------------------------------------------------

func (s *Store) CreateGroupFromSelection(ctx context.Context, name string, entityIDs []string) error {
	name, err := cleanOrganizationName(name, "group name")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Create the tag with origin='user', grouped=1.
	id, err := newGroupID()
	if err != nil {
		return err
	}
	now := nowUTC()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO mod_tags(id,name,color,icon,origin,grouped,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`,
		id, name, defaultModTagColor, defaultModTagIcon, "user", 1, now, now); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return fmt.Errorf("tag %q already exists", name)
		}
		return err
	}

	// Assign entities.
	for _, entityID := range entityIDs {
		entityID = strings.TrimSpace(entityID)
		if entityID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO mod_tag_entities(tag_id,entity_id,created_at) VALUES(?,?,?) ON CONFLICT(tag_id,entity_id) DO NOTHING`,
			id, entityID, now); err != nil {
			return err
		}
	}

	// Set fold state to collapsed (R4/R5).
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		groupCollapseKey(id), "1"); err != nil {
		return err
	}

	// Refresh FTS entries.
	for _, entityID := range entityIDs {
		entityID = strings.TrimSpace(entityID)
		if entityID == "" {
			continue
		}
		if err := s.refreshLibrarySearchEntryTx(ctx, tx, entityID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// newGroupID is a thin wrapper so tests can verify the call site.
func newGroupID() (string, error) {
	return modkit.NewID()
}

// ---------------------------------------------------------------------------
// Store: ListLibraryGroupPage — the core paged group stream
// ---------------------------------------------------------------------------
//
// Two paths:
//
// FAST PATH (default case — no health filter, no is: status, no tag: exact):
// Phase 1 computes the ordered row stream as IDENTITIES ONLY in SQL — group
// headers, member identities for unfolded groups, ungrouped identities — with
// counts, sizes, TotalRows, DistinctMods and the LIMIT/OFFSET window all
// evaluated there. Phase 2 loads only lightweight summaries for the returned page.
//
// SUMMARY PATH (derived health, exact-tag matching or tag/status ordering):
// Load candidate summaries, apply the Go predicates/comparison, then page.
// Neither path reads detailed archive manifests.
//
// Filter combinations and their paths:
//   health=""  query=""                              → fast
//   health=""  query="some text"                     → fast (FTS handles it)
//   health=""  query="in:tag car"                    → fast (LIKE handles it)
//   health=""  query="tag:Exact"                     → slow (tag_exact needs Go EqualFold)
//   health=""  query="is:healthy"                    → slow (health derivation is Go-only)
//   health="clean"                                   → slow (health derivation is Go-only)

// validGroupSortKeys enumerates every library column the caller may sort by.
var validGroupSortKeys = map[string]bool{
	"name": true, "path": true, "kind": true, "source": true,
	"status": true, "author": true, "tags": true, "files": true,
	"variants": true, "size": true, "modified": true, "lastScan": true,
	"issues": true,
}

// sqlGroupSortKeys use scalar entity, archive-link or artifact-summary columns,
// without opening the detailed inspection JSON.
var sqlGroupSortKeys = map[string]bool{
	"name": true, "path": true, "kind": true, "source": true,
	"size": true, "modified": true, "author": true, "files": true,
	"variants": true, "issues": true,
}

// normalizeGroupSortKey validates and defaults the sort key.
func normalizeGroupSortKey(key string) (string, error) {
	if key == "" {
		return "name", nil
	}
	if !validGroupSortKeys[key] {
		return "", fmt.Errorf("invalid sort key: %q", key)
	}
	return key, nil
}

// normalizeGroupSortDirection validates and defaults the sort direction.
func normalizeGroupSortDirection(direction int) (int, error) {
	switch direction {
	case 0, 1:
		return 1, nil
	case -1:
		return -1, nil
	default:
		return 0, fmt.Errorf("invalid sort direction: %d", direction)
	}
}

func (s *Store) ListLibraryGroupPage(ctx context.Context, health, kind, query, collectionID, scope string, page, pageSize int, sortKey string, sortDirection int) (LibraryGroupPage, error) {
	if err := ctx.Err(); err != nil {
		return LibraryGroupPage{}, err
	}
	sk, err := normalizeGroupSortKey(sortKey)
	if err != nil {
		return LibraryGroupPage{}, err
	}
	sd, err := normalizeGroupSortDirection(sortDirection)
	if err != nil {
		return LibraryGroupPage{}, err
	}
	search := parseLibrarySearchQuery(query)
	if groupPageNeedsResidual(health, search, sk) {
		return s.listLibraryGroupPageSlow(ctx, health, kind, query, collectionID, scope, page, pageSize, search, sk, sd)
	}
	return s.listLibraryGroupPageFast(ctx, health, kind, query, collectionID, scope, page, pageSize, search, sk, sd)
}

// groupPageNeedsResidual selects the summary path for Go-derived health,
// exact Unicode tag matching, or ordering not represented by a SQL scalar.
func groupPageNeedsResidual(health string, search librarySearchQuery, sortKey string) bool {
	if !sqlGroupSortKeys[sortKey] {
		return true
	}
	if normalizeLibraryStatus(health) != "" {
		return true
	}
	if search.status != "" {
		return true
	}
	for _, term := range search.terms {
		if term.scope == "tag_exact" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Fast path: SQL identity stream, hydrate only the page
// ---------------------------------------------------------------------------

// groupStat holds per-group aggregate data computed in SQL.
type groupStat struct {
	tagID      string
	tagName    string
	modCount   int
	sizeBytes  int64
	collapsed  bool
	visible    bool // true when members should appear in the stream
	matchCount int
}

func (s *Store) listLibraryGroupPageFast(ctx context.Context, health, kind, query, collectionID, scope string, page, pageSize int, search librarySearchQuery, sortKey string, sortDir int) (LibraryGroupPage, error) {
	archiveScope, err := normalizeLibraryArchiveScope(scope)
	if err != nil {
		return LibraryGroupPage{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LibraryGroupPage{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// --- Phase 1a: SQL candidates (≈1–2 ms) ----------------------------
	entityIDs, err := s.listLibraryQueryTx(ctx, tx, health, kind, query, collectionID, archiveScope)
	if err != nil {
		return LibraryGroupPage{}, err
	}
	distinctMods := len(entityIDs)

	// --- Phase 1b: Populate temp table with candidate IDs ---------------
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS _gp(id TEXT PRIMARY KEY)`); err != nil {
		return LibraryGroupPage{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM _gp`); err != nil {
		return LibraryGroupPage{}, err
	}
	if err := batchInsertIDs(ctx, tx, `INSERT INTO _gp(id) VALUES `, entityIDs); err != nil {
		return LibraryGroupPage{}, err
	}

	// --- Phase 1c: Group stats from SQL ---------------------------------
	groups, err := queryGroupStatsTx(ctx, tx)
	if err != nil {
		return LibraryGroupPage{}, err
	}

	// --- Phase 1d: Ungrouped stats from SQL -----------------------------
	var ungroupedCount int
	var ungroupedSize int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(sz), 0) FROM (
		SELECT COALESCE((SELECT l.size_bytes FROM archive_links l
			WHERE l.entity_id=g.id ORDER BY l.active DESC,l.last_seen_at DESC LIMIT 1),0) AS sz
		FROM _gp g
		WHERE NOT EXISTS (
			SELECT 1 FROM mod_tag_entities mte
			JOIN mod_tags mt ON mt.id=mte.tag_id AND mt.grouped=1
			WHERE mte.entity_id=g.id
		)
	)`).Scan(&ungroupedCount, &ungroupedSize); err != nil {
		return LibraryGroupPage{}, err
	}

	// --- Phase 1e: Fold states ------------------------------------------
	foldStates, err := loadAllGroupFoldStatesTx(ctx, tx)
	if err != nil {
		return LibraryGroupPage{}, err
	}
	searchActive := len(search.terms) > 0 || search.status != ""

	// --- Phase 1f: Compute TotalRows by walking the logical stream ------
	for i := range groups {
		g := &groups[i]
		g.collapsed = foldStates[g.tagID]
		g.visible = !g.collapsed || searchActive
		if searchActive && g.collapsed {
			g.matchCount = g.modCount
		}
	}
	ungroupedCollapsed := foldStates[ungroupedGroupID]
	ungroupedVisible := !ungroupedCollapsed || searchActive
	ungroupedMatch := 0
	if searchActive && ungroupedCollapsed {
		ungroupedMatch = ungroupedCount
	}

	totalRows := 0
	for _, g := range groups {
		if g.modCount == 0 {
			continue
		}
		totalRows++ // header
		if g.visible {
			totalRows += g.modCount
		}
	}
	if ungroupedCount > 0 {
		totalRows++ // header
		if ungroupedVisible {
			totalRows += ungroupedCount
		}
	}

	// --- Phase 1g: Determine page window --------------------------------
	if page < 0 {
		page = 0
	}
	effectivePageSize := pageSize
	if effectivePageSize <= 0 {
		effectivePageSize = totalRows
	}
	start := page * effectivePageSize
	if start > totalRows {
		start = totalRows
	}
	end := start + effectivePageSize
	if end > totalRows {
		end = totalRows
	}

	// --- Phase 1h: Walk the stream, query member IDs only for the page --
	rows := make([]LibraryGroupRow, 0, end-start)
	pageEntityIDs := make([]string, 0, end-start)
	pos := 0

	orderClause := sqlMemberOrderClause(sortKey, sortDir)

	for _, g := range groups {
		if g.modCount == 0 {
			continue
		}
		// Header at pos.
		if pos >= start && pos < end {
			rows = append(rows, LibraryGroupRow{
				RowType:    "group",
				GroupID:    g.tagID,
				Label:      g.tagName,
				ModCount:   g.modCount,
				SizeBytes:  g.sizeBytes,
				Collapsed:  g.collapsed,
				MatchCount: g.matchCount,
			})
		}
		pos++

		if g.visible {
			// Determine overlap of this member block with [start, end).
			memberStart := 0
			memberEnd := g.modCount
			if pos+memberEnd <= start || pos >= end {
				// Entire block outside page — skip.
				pos += g.modCount
				continue
			}
			if pos < start {
				memberStart = start - pos
			}
			if pos+memberEnd > end {
				memberEnd = end - pos
			}
			count := memberEnd - memberStart
			eids, err := queryGroupMemberIDsTx(ctx, tx, g.tagID, memberStart, count, orderClause)
			if err != nil {
				return LibraryGroupPage{}, err
			}
			for _, eid := range eids {
				rows = append(rows, LibraryGroupRow{
					RowType: "mod",
					GroupID: g.tagID,
				})
				pageEntityIDs = append(pageEntityIDs, eid)
			}
			pos += g.modCount
		}
	}

	// Ungrouped.
	if ungroupedCount > 0 {
		if pos >= start && pos < end {
			rows = append(rows, LibraryGroupRow{
				RowType:    "group",
				GroupID:    ungroupedGroupID,
				Label:      "Ungrouped",
				ModCount:   ungroupedCount,
				SizeBytes:  ungroupedSize,
				Collapsed:  ungroupedCollapsed,
				MatchCount: ungroupedMatch,
			})
		}
		pos++

		if ungroupedVisible {
			memberStart := 0
			memberEnd := ungroupedCount
			if pos+memberEnd > start && pos < end {
				if pos < start {
					memberStart = start - pos
				}
				if pos+memberEnd > end {
					memberEnd = end - pos
				}
				count := memberEnd - memberStart
				eids, err := queryUngroupedMemberIDsTx(ctx, tx, memberStart, count, orderClause)
				if err != nil {
					return LibraryGroupPage{}, err
				}
				for _, eid := range eids {
					rows = append(rows, LibraryGroupRow{
						RowType: "mod",
						GroupID: ungroupedGroupID,
					})
					pageEntityIDs = append(pageEntityIDs, eid)
				}
			}
		}
	}

	// --- Phase 2: Hydrate only the page entities ------------------------
	if len(pageEntityIDs) > 0 {
		items, err := s.listLibrarySummaryItemsByIDsTx(ctx, tx, pageEntityIDs)
		if err != nil {
			return LibraryGroupPage{}, err
		}
		itemMap := make(map[string]*LibraryItem, len(items))
		for i := range items {
			itemMap[items[i].EntityID] = &items[i]
		}
		eidIdx := 0
		for i := range rows {
			if rows[i].RowType == "mod" && eidIdx < len(pageEntityIDs) {
				if item, ok := itemMap[pageEntityIDs[eidIdx]]; ok {
					rows[i].Item = item
				}
				eidIdx++
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return LibraryGroupPage{}, err
	}

	reportPageSize := pageSize
	if pageSize <= 0 {
		reportPageSize = totalRows
		page = 0
	}

	return LibraryGroupPage{
		Rows:         rows,
		TotalRows:    totalRows,
		DistinctMods: distinctMods,
		Page:         page,
		PageSize:     reportPageSize,
	}, nil
}

// batchInsertIDs inserts IDs into a temp table using batched multi-row VALUES.
func batchInsertIDs(ctx context.Context, tx *sql.Tx, prefix string, ids []string) error {
	const batchSize = 500
	for i := 0; i < len(ids); i += batchSize {
		end := i + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[i:end]
		placeholders := strings.TrimRight(strings.Repeat("(?),", len(chunk)), ",")
		args := make([]any, len(chunk))
		for j, id := range chunk {
			args[j] = id
		}
		if _, err := tx.ExecContext(ctx, prefix+placeholders, args...); err != nil {
			return err
		}
	}
	return nil
}

// queryGroupStatsTx returns per-group counts and sizes over the candidate set
// in _gp, ordered by group name case-insensitive.
func queryGroupStatsTx(ctx context.Context, tx *sql.Tx) ([]groupStat, error) {
	sqlText := `SELECT mt.id, mt.name, COUNT(DISTINCT dm.entity_id),
		COALESCE(SUM(COALESCE((SELECT l.size_bytes FROM archive_links l
			WHERE l.entity_id=dm.entity_id
			ORDER BY l.active DESC,l.last_seen_at DESC LIMIT 1),0)),0)
	FROM mod_tags mt
	JOIN (
		SELECT DISTINCT mte.tag_id, mte.entity_id
		FROM mod_tag_entities mte
		WHERE mte.entity_id IN (SELECT id FROM _gp)
	) dm ON dm.tag_id = mt.id
	WHERE mt.grouped = 1
	GROUP BY mt.id
	HAVING COUNT(DISTINCT dm.entity_id) > 0
	ORDER BY mt.name COLLATE NOCASE`

	rows, err := tx.QueryContext(ctx, sqlText)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stats []groupStat
	for rows.Next() {
		var g groupStat
		if err := rows.Scan(&g.tagID, &g.tagName, &g.modCount, &g.sizeBytes); err != nil {
			return nil, err
		}
		stats = append(stats, g)
	}
	return stats, rows.Err()
}

// sqlMemberOrderClause returns the SQL ORDER BY body for the given sort key
// and direction.  Every key is paired with a deterministic tie-break on
// entity ID so that LIMIT/OFFSET pagination is stable.
//
// Columns that require archive_links are detected by the callers via
// strings.Contains(orderClause, "l.") and joined as needed.
func sqlMemberOrderClause(sortKey string, sortDir int) string {
	dir := "ASC"
	if sortDir < 0 {
		dir = "DESC"
	}
	var primary string
	switch sortKey {
	case "path":
		primary = "COALESCE(l.path,'') COLLATE library_sort " + dir
	case "kind":
		primary = "e.kind COLLATE library_sort " + dir
	case "source":
		primary = "CASE WHEN lower(COALESCE(NULLIF(l.source_id,''),NULLIF(e.source_id,''),'user-added'))='beamng-repository' THEN 'beamng-repository' ELSE 'user-added' END " + dir
		return primary + ", e.display_name COLLATE library_sort " + dir + ", e.id " + dir
	case "size":
		primary = "COALESCE(l.size_bytes,0) " + dir
	case "modified":
		primary = "COALESCE(julianday(l.modified_at),2440587.5) " + dir
	case "author":
		primary = "COALESCE(a.author,'') COLLATE library_sort " + dir
	case "files":
		primary = "COALESCE(a.member_count,0) " + dir
	case "variants":
		primary = "COALESCE(a.variant_count,0) " + dir
	case "issues":
		primary = "COALESCE(a.issue_count,0) " + dir
	default: // "name"
		primary = "e.display_name COLLATE library_sort " + dir
	}
	return primary + ", e.id " + dir
}

// sqlArchiveLinkJoin is the LEFT JOIN snippet used when the sort key
// requires archive_links columns.  It selects the primary link per entity
// with the same precedence as the hydration query.
const sqlArchiveLinkJoin = `LEFT JOIN archive_links l ON l.id = (
	SELECT l2.id FROM archive_links l2 WHERE l2.entity_id=e.id
	ORDER BY l2.active DESC, l2.last_seen_at DESC, l2.id DESC LIMIT 1
)`

// queryGroupMemberIDsTx returns entity IDs for one group's members in the
// candidate set, ordered by the supplied ORDER BY clause.
func queryGroupMemberIDsTx(ctx context.Context, tx *sql.Tx, tagID string, offset, limit int, orderClause string) ([]string, error) {
	linkJoin := ""
	if strings.Contains(orderClause, "l.") || strings.Contains(orderClause, "a.") {
		linkJoin = sqlArchiveLinkJoin
	}
	if strings.Contains(orderClause, "a.") {
		linkJoin += " LEFT JOIN artifact_summaries a ON a.artifact_id=l.artifact_id"
	}
	// The orderClause is built by sqlMemberOrderClause from a validated sort
	// key, never from user input, so direct interpolation is safe.
	q := `SELECT DISTINCT mte.entity_id
		FROM mod_tag_entities mte
		JOIN _gp ON _gp.id = mte.entity_id
		JOIN entities e ON e.id = mte.entity_id
		` + linkJoin + `
		WHERE mte.tag_id = ?
		ORDER BY ` + orderClause + `
		LIMIT ? OFFSET ?`
	rows, err := tx.QueryContext(ctx, q, tagID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// queryUngroupedMemberIDsTx returns entity IDs for ungrouped candidates,
// ordered by the supplied ORDER BY clause.
func queryUngroupedMemberIDsTx(ctx context.Context, tx *sql.Tx, offset, limit int, orderClause string) ([]string, error) {
	linkJoin := ""
	if strings.Contains(orderClause, "l.") || strings.Contains(orderClause, "a.") {
		linkJoin = sqlArchiveLinkJoin
	}
	if strings.Contains(orderClause, "a.") {
		linkJoin += " LEFT JOIN artifact_summaries a ON a.artifact_id=l.artifact_id"
	}
	q := `SELECT g.id
		FROM _gp g
		JOIN entities e ON e.id = g.id
		` + linkJoin + `
		WHERE NOT EXISTS (
			SELECT 1 FROM mod_tag_entities mte
			JOIN mod_tags mt ON mt.id = mte.tag_id AND mt.grouped = 1
			WHERE mte.entity_id = g.id
		)
		ORDER BY ` + orderClause + `
		LIMIT ? OFFSET ?`
	rows, err := tx.QueryContext(ctx, q, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ---------------------------------------------------------------------------
// Summary path: filter or sort small candidates in Go, then page the stream.
// Used for derived health, exact-tag predicates and tag/status/lastScan sorts.
// It never hydrates archive members or metadata-document contents.

func (s *Store) listLibraryGroupPageSlow(ctx context.Context, health, kind, query, collectionID, scope string, page, pageSize int, search librarySearchQuery, sortKey string, sortDir int) (LibraryGroupPage, error) {
	archiveScope, err := normalizeLibraryArchiveScope(scope)
	if err != nil {
		return LibraryGroupPage{}, err
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LibraryGroupPage{}, err
	}
	defer func() { _ = tx.Rollback() }()

	entityIDs, err := s.listLibraryQueryTx(ctx, tx, health, kind, query, collectionID, archiveScope)
	if err != nil {
		return LibraryGroupPage{}, err
	}
	items, err := s.listLibrarySummaryItemsByIDsTx(ctx, tx, entityIDs)
	if err != nil {
		return LibraryGroupPage{}, err
	}
	byEntity := make(map[string]int, len(items))
	for i := range items {
		byEntity[items[i].EntityID] = i
	}

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
			return LibraryGroupPage{}, err
		}
		effectiveCollectionNames, err = libraryEffectiveCollectionNamesTx(ctx, tx, entityIDs)
		if err != nil {
			return LibraryGroupPage{}, err
		}
	}
	normHealth := normalizeLibraryStatus(health)
	filteredMap := make(map[string]*LibraryItem, len(entityIDs))
	for _, entityID := range entityIDs {
		idx, ok := byEntity[entityID]
		if !ok {
			continue
		}
		item := &items[idx]
		if normHealth != "" && item.HealthStatus != normHealth {
			continue
		}
		if kind != "" && kind != "all" && string(item.Kind) != kind {
			continue
		}
		if collectionID == "unfiled" && len(item.CollectionIDs) > 0 {
			continue
		}
		if (search.status != "" || len(search.terms) > 0) &&
			!search.matches(*item, collectionNames, effectiveCollectionNames[entityID]) {
			continue
		}
		filteredMap[entityID] = item
	}

	groupedTags, err := listGroupedTagsTx(ctx, tx)
	if err != nil {
		return LibraryGroupPage{}, err
	}
	foldStates, err := loadAllGroupFoldStatesTx(ctx, tx)
	if err != nil {
		return LibraryGroupPage{}, err
	}

	if err := tx.Commit(); err != nil {
		return LibraryGroupPage{}, err
	}

	searchActive := len(search.terms) > 0 || search.status != ""

	type groupInfo struct {
		tagID     string
		tagName   string
		members   []*LibraryItem
		collapsed bool
	}
	groups := make([]*groupInfo, 0, len(groupedTags))
	inGroup := map[string]bool{}

	for _, tag := range groupedTags {
		if err := ctx.Err(); err != nil {
			return LibraryGroupPage{}, err
		}
		g := &groupInfo{tagID: tag.ID, tagName: tag.Name, collapsed: foldStates[tag.ID]}
		for _, item := range items {
			if filteredMap[item.EntityID] == nil {
				continue
			}
			for _, t := range item.Tags {
				if t.ID == tag.ID {
					g.members = append(g.members, filteredMap[item.EntityID])
					inGroup[item.EntityID] = true
					break
				}
			}
		}
		groups = append(groups, g)
	}
	// Group headers remain in name order; only members are user-sorted.
	sort.SliceStable(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].tagName) < strings.ToLower(groups[j].tagName)
	})
	memberLess := libraryItemLessFunc(sortKey, sortDir)
	for _, g := range groups {
		sort.SliceStable(g.members, memberLess(g.members))
	}

	ungroupedItems := make([]*LibraryItem, 0)
	for _, item := range filteredMap {
		if !inGroup[item.EntityID] {
			ungroupedItems = append(ungroupedItems, item)
		}
	}
	sort.SliceStable(ungroupedItems, memberLess(ungroupedItems))
	if err := ctx.Err(); err != nil {
		return LibraryGroupPage{}, err
	}
	ungroupedCollapsed := foldStates[ungroupedGroupID]

	stream := make([]LibraryGroupRow, 0, len(filteredMap)+len(groups)+1)
	distinctEntityIDs := map[string]bool{}

	for _, g := range groups {
		if len(g.members) == 0 {
			continue
		}
		memberIDs := map[string]bool{}
		var sizeBytes int64
		for _, m := range g.members {
			if !memberIDs[m.EntityID] {
				memberIDs[m.EntityID] = true
				sizeBytes += m.SizeBytes
			}
		}
		modCount := len(memberIDs)
		showMembers := !g.collapsed
		matchCount := 0
		if searchActive && g.collapsed {
			showMembers = true
			matchCount = modCount
		}
		stream = append(stream, LibraryGroupRow{
			RowType: "group", GroupID: g.tagID, Label: g.tagName,
			ModCount: modCount, SizeBytes: sizeBytes,
			Collapsed: g.collapsed, MatchCount: matchCount,
		})
		if showMembers {
			for _, m := range g.members {
				distinctEntityIDs[m.EntityID] = true
				item := *m
				stream = append(stream, LibraryGroupRow{RowType: "mod", GroupID: g.tagID, Item: &item})
			}
		}
		for _, m := range g.members {
			distinctEntityIDs[m.EntityID] = true
		}
	}

	if len(ungroupedItems) > 0 {
		memberIDs := map[string]bool{}
		var ungroupedSize int64
		for _, item := range ungroupedItems {
			if !memberIDs[item.EntityID] {
				memberIDs[item.EntityID] = true
				ungroupedSize += item.SizeBytes
			}
		}
		ungroupedCount := len(memberIDs)
		showUngrouped := !ungroupedCollapsed
		ungroupedMatch := 0
		if searchActive && ungroupedCollapsed {
			showUngrouped = true
			ungroupedMatch = ungroupedCount
		}
		stream = append(stream, LibraryGroupRow{
			RowType: "group", GroupID: ungroupedGroupID, Label: "Ungrouped",
			ModCount: ungroupedCount, SizeBytes: ungroupedSize,
			Collapsed: ungroupedCollapsed, MatchCount: ungroupedMatch,
		})
		if showUngrouped {
			for _, item := range ungroupedItems {
				distinctEntityIDs[item.EntityID] = true
				itemCopy := *item
				stream = append(stream, LibraryGroupRow{RowType: "mod", GroupID: ungroupedGroupID, Item: &itemCopy})
			}
		}
		for _, item := range ungroupedItems {
			distinctEntityIDs[item.EntityID] = true
		}
	}

	totalRows := len(stream)
	distinctMods := len(distinctEntityIDs)
	if page < 0 {
		page = 0
	}
	if pageSize <= 0 {
		return LibraryGroupPage{Rows: stream, TotalRows: totalRows, DistinctMods: distinctMods, Page: 0, PageSize: totalRows}, nil
	}
	s0 := page * pageSize
	if s0 > totalRows {
		s0 = totalRows
	}
	e0 := s0 + pageSize
	if e0 > totalRows {
		e0 = totalRows
	}
	return LibraryGroupPage{Rows: stream[s0:e0], TotalRows: totalRows, DistinctMods: distinctMods, Page: page, PageSize: pageSize}, nil
}

// ---------------------------------------------------------------------------
// Go sort helpers — used by the slow path and shared with tests
// ---------------------------------------------------------------------------

// healthRank maps a health-status string to a numeric rank matching the
// frontend's healthRank function in ModTable.tsx.
func healthRank(status string) int {
	switch status {
	case "threat":
		return 0
	case "broken":
		return 1
	case "review":
		return 2
	case "scan_failed":
		return 3
	case "scanning":
		return 4
	case "safe":
		return 6
	default: // "unscanned" and anything else
		return 5
	}
}

// normalizedSourceID mirrors the frontend sourceID() normalisation: the only
// two values are "beamng-repository" and "user-added".
func normalizedSourceID(item *LibraryItem) string {
	id := strings.TrimSpace(strings.ToLower(item.SourceID))
	if id == "beamng-repository" || id == "user-added" {
		return id
	}
	label := strings.TrimSpace(strings.ToLower(item.Source))
	if label == "beamng repository" {
		return "beamng-repository"
	}
	return "user-added"
}

// libraryItemSortValue extracts a comparable value from a hydrated item for
// the given sort key. Text uses the shared collation; numeric keys use int64.
func libraryItemSortValue(item *LibraryItem, key string) (str string, num int64, isNum bool) {
	switch key {
	case "name":
		return item.DisplayName, 0, false
	case "path":
		return item.ArchivePath, 0, false
	case "kind":
		return string(item.Kind), 0, false
	case "source":
		return normalizedSourceID(item), 0, false
	case "status":
		return "", int64(healthRank(item.HealthStatus)), true
	case "author":
		return item.Manifest.Author, 0, false
	case "tags":
		names := make([]string, len(item.Tags))
		for i, t := range item.Tags {
			names[i] = t.Name
		}
		return strings.Join(names, " "), 0, false
	case "files":
		return "", int64(item.MemberCount), true
	case "variants":
		return "", int64(item.VariantCount), true
	case "size":
		return "", item.SizeBytes, true
	case "modified":
		parsed, _ := time.Parse(time.RFC3339Nano, item.ModifiedAt)
		if parsed.IsZero() {
			return "", 0, true
		}
		return "", parsed.UnixMilli(), true
	case "lastScan":
		parsed, _ := time.Parse(time.RFC3339Nano, item.LastSecurityScanAt)
		if parsed.IsZero() {
			return "", 0, true
		}
		return "", parsed.UnixMilli(), true
	case "issues":
		return "", int64(item.IssueCount), true
	default:
		return item.DisplayName, 0, false
	}
}

// libraryItemLessFunc returns a sort.SliceStable less-function factory.
// Call the returned function with the slice to sort; it captures the sort
// key, direction, and provides a deterministic entity-ID tie-break.
func libraryItemLessFunc(sortKey string, sortDir int) func([]*LibraryItem) func(i, j int) bool {
	return func(items []*LibraryItem) func(i, j int) bool {
		// Tags and normalized strings are computed once per member, not once
		// per comparison during sorting.
		type sortValue struct {
			text    string
			number  int64
			numeric bool
		}
		values := make(map[string]sortValue, len(items))
		for _, item := range items {
			text, number, numeric := libraryItemSortValue(item, sortKey)
			values[item.EntityID] = sortValue{text, number, numeric}
		}
		return func(i, j int) bool {
			left, right := values[items[i].EntityID], values[items[j].EntityID]
			si, ni, numI := left.text, left.number, left.numeric
			sj, nj, numJ := right.text, right.number, right.numeric
			var cmp int
			if numI && numJ {
				switch {
				case ni < nj:
					cmp = -1
				case ni > nj:
					cmp = 1
				}
			} else {
				cmp = compareLibrarySortText(si, sj)
			}
			if cmp == 0 && sortKey == "source" {
				cmp = compareLibrarySortText(items[i].DisplayName, items[j].DisplayName)
			}
			if cmp != 0 {
				if sortDir < 0 {
					return cmp > 0
				}
				return cmp < 0
			}
			// Deterministic tie-break by entity ID in the same direction.
			tie := strings.Compare(items[i].EntityID, items[j].EntityID)
			if sortDir < 0 {
				return tie > 0
			}
			return tie < 0
		}
	}
}

// ---------------------------------------------------------------------------
// SQL helpers
// ---------------------------------------------------------------------------

// listGroupedTagsTx returns tags with grouped=1, ordered by name.
func listGroupedTagsTx(ctx context.Context, tx *sql.Tx) ([]ModTag, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT t.id, t.name, t.color, t.icon, t.origin, t.grouped, COUNT(te.entity_id)
		 FROM mod_tags t
		 LEFT JOIN mod_tag_entities te ON te.tag_id = t.id
		 WHERE t.grouped = 1
		 GROUP BY t.id
		 ORDER BY t.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []ModTag
	for rows.Next() {
		var tag ModTag
		if err := rows.Scan(&tag.ID, &tag.Name, &tag.Color, &tag.Icon, &tag.Origin, &tag.Grouped, &tag.ModCount); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}
