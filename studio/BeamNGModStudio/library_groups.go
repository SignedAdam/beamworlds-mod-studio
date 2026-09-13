package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

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
	Item       *LibraryItem `json:"item"`       // mod rows only
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

func (service *AppService) LibraryGroupPage(health, kind, query, collectionID, scope string, page, pageSize int) (LibraryGroupPage, error) {
	return service.store.ListLibraryGroupPage(context.Background(), health, kind, query, collectionID, scope, page, pageSize)
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
// evaluated there.  Phase 2 hydrates only the identities on the returned page.
// Nothing outside the page is hydrated.
//
// SLOW PATH (residual filter active — health derivation or tag_exact):
// Falls back to full materialisation: hydrate all candidates, apply the Go
// residual filter, build and page the stream in memory.  This path is used
// only when a predicate genuinely cannot be expressed in SQL today.
//
// Filter combinations and their paths:
//   health=""  query=""                              → fast
//   health=""  query="some text"                     → fast (FTS handles it)
//   health=""  query="in:tag car"                    → fast (LIKE handles it)
//   health=""  query="tag:Exact"                     → slow (tag_exact needs Go EqualFold)
//   health=""  query="is:healthy"                    → slow (health derivation is Go-only)
//   health="clean"                                   → slow (health derivation is Go-only)

func (s *Store) ListLibraryGroupPage(ctx context.Context, health, kind, query, collectionID, scope string, page, pageSize int) (LibraryGroupPage, error) {
	search := parseLibrarySearchQuery(query)
	if groupPageNeedsResidual(health, search) {
		return s.listLibraryGroupPageSlow(ctx, health, kind, query, collectionID, scope, page, pageSize, search)
	}
	return s.listLibraryGroupPageFast(ctx, health, kind, query, collectionID, scope, page, pageSize, search)
}

// groupPageNeedsResidual returns true when any active predicate requires
// hydrated item fields that SQL alone cannot provide.
func groupPageNeedsResidual(health string, search librarySearchQuery) bool {
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

func (s *Store) listLibraryGroupPageFast(ctx context.Context, health, kind, query, collectionID, scope string, page, pageSize int, search librarySearchQuery) (LibraryGroupPage, error) {
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
			eids, err := queryGroupMemberIDsTx(ctx, tx, g.tagID, memberStart, count)
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
				eids, err := queryUngroupedMemberIDsTx(ctx, tx, memberStart, count)
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
		items, err := s.listItemsByIDsTx(ctx, tx, pageEntityIDs)
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
					cp := *item
					rows[i].Item = &cp
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

// queryGroupMemberIDsTx returns entity IDs for one group's members in the
// candidate set, ordered by display_name COLLATE NOCASE, entity_id.
func queryGroupMemberIDsTx(ctx context.Context, tx *sql.Tx, tagID string, offset, limit int) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT mte.entity_id
		FROM mod_tag_entities mte
		JOIN _gp ON _gp.id = mte.entity_id
		JOIN entities e ON e.id = mte.entity_id
		WHERE mte.tag_id = ?
		ORDER BY e.display_name COLLATE NOCASE, mte.entity_id
		LIMIT ? OFFSET ?`, tagID, limit, offset)
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
// ordered by display_name COLLATE NOCASE, entity_id.
func queryUngroupedMemberIDsTx(ctx context.Context, tx *sql.Tx, offset, limit int) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT g.id
		FROM _gp g
		JOIN entities e ON e.id = g.id
		WHERE NOT EXISTS (
			SELECT 1 FROM mod_tag_entities mte
			JOIN mod_tags mt ON mt.id = mte.tag_id AND mt.grouped = 1
			WHERE mte.entity_id = g.id
		)
		ORDER BY e.display_name COLLATE NOCASE, g.id
		LIMIT ? OFFSET ?`, limit, offset)
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
// Slow path: materialise all candidates, filter in Go, page in memory
// ---------------------------------------------------------------------------
// Used only when a residual Go predicate is active (health filter, is:status,
// tag:exact).  This path is identical to the original implementation.

func (s *Store) listLibraryGroupPageSlow(ctx context.Context, health, kind, query, collectionID, scope string, page, pageSize int, search librarySearchQuery) (LibraryGroupPage, error) {
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
	items, err := s.listItemsByIDsTx(ctx, tx, entityIDs)
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
	sort.SliceStable(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].tagName) < strings.ToLower(groups[j].tagName)
	})
	for _, g := range groups {
		sort.SliceStable(g.members, func(i, j int) bool {
			di := strings.ToLower(g.members[i].DisplayName)
			dj := strings.ToLower(g.members[j].DisplayName)
			if di != dj {
				return di < dj
			}
			return g.members[i].EntityID < g.members[j].EntityID
		})
	}

	ungroupedItems := make([]*LibraryItem, 0)
	for _, item := range filteredMap {
		if !inGroup[item.EntityID] {
			ungroupedItems = append(ungroupedItems, item)
		}
	}
	sort.SliceStable(ungroupedItems, func(i, j int) bool {
		di := strings.ToLower(ungroupedItems[i].DisplayName)
		dj := strings.ToLower(ungroupedItems[j].DisplayName)
		if di != dj {
			return di < dj
		}
		return ungroupedItems[i].EntityID < ungroupedItems[j].EntityID
	})
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
