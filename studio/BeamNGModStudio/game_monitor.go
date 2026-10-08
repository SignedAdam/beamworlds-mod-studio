package main

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Contract types
// ---------------------------------------------------------------------------

type GameStatus struct {
	Running         bool     `json:"running"`
	StudioSession   bool     `json:"studioSession"`
	CollectionNames []string `json:"collectionNames"`
	ModCount        int      `json:"modCount"`
	Since           string   `json:"since"`
	ArrivingMods    int      `json:"arrivingMods"`
}

type NewModArrival struct {
	Item       LibraryItem `json:"item"`
	Origin     string      `json:"origin"`
	DetectedAt string      `json:"detectedAt"`
}

type NewModsReview struct {
	Arrivals               []NewModArrival `json:"arrivals"`
	SuggestedCollectionIDs []string        `json:"suggestedCollectionIds"`
}

type NewModsResolution struct {
	Added       int      `json:"added"`
	Collections []string `json:"collections"`
}

// gameProcess is returned by the platform-specific beamNGProcesses.
type gameProcess struct {
	PID     uint32
	Started time.Time
}

// ---------------------------------------------------------------------------
// Schema: mod_seen, new_mod_reviews, studio_placed_archives
// ---------------------------------------------------------------------------

func ensureGameMonitorSchemaTx(ctx context.Context, tx *sql.Tx) error {
	const modSeenDDL = `CREATE TABLE IF NOT EXISTS mod_seen (
		entity_id TEXT PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
		seen_at TEXT NOT NULL DEFAULT ''
	)`
	const newModReviewsDDL = `CREATE TABLE IF NOT EXISTS new_mod_reviews (
		entity_id TEXT PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
		detected_at TEXT NOT NULL DEFAULT '',
		origin TEXT NOT NULL DEFAULT ''
	)`
	const studioPlacedDDL = `CREATE TABLE IF NOT EXISTS studio_placed_archives (
		path TEXT PRIMARY KEY COLLATE NOCASE,
		placed_at TEXT NOT NULL DEFAULT ''
	)`
	if _, err := tx.ExecContext(ctx, modSeenDDL); err != nil {
		return fmt.Errorf("create mod_seen: %w", err)
	}
	if _, err := tx.ExecContext(ctx, newModReviewsDDL); err != nil {
		return fmt.Errorf("create new_mod_reviews: %w", err)
	}
	if _, err := tx.ExecContext(ctx, studioPlacedDDL); err != nil {
		return fmt.Errorf("create studio_placed_archives: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Store helpers
// ---------------------------------------------------------------------------

func (s *Store) allEntityIDs(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM entities`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = struct{}{}
	}
	return ids, rows.Err()
}

func (s *Store) markModsSeen(ctx context.Context, entityIDs []string) error {
	if len(entityIDs) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := nowUTC()
	for _, id := range entityIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO mod_seen(entity_id,seen_at) VALUES(?,?) ON CONFLICT(entity_id) DO UPDATE SET seen_at=excluded.seen_at`,
			id, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) insertNewModReviews(ctx context.Context, reviews []newModReviewRow) error {
	if len(reviews) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, r := range reviews {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO new_mod_reviews(entity_id,detected_at,origin) VALUES(?,?,?) ON CONFLICT(entity_id) DO NOTHING`,
			r.entityID, r.detectedAt, r.origin); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type newModReviewRow struct {
	entityID   string
	detectedAt string
	origin     string
}

func (s *Store) pendingNewModReviews(ctx context.Context) ([]newModReviewRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT entity_id, detected_at, origin FROM new_mod_reviews ORDER BY detected_at, entity_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []newModReviewRow
	for rows.Next() {
		var r newModReviewRow
		if err := rows.Scan(&r.entityID, &r.detectedAt, &r.origin); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) clearNewModReviews(ctx context.Context, entityIDs []string) error {
	if len(entityIDs) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range entityIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM new_mod_reviews WHERE entity_id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) recordStudioPlacedArchive(ctx context.Context, path string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO studio_placed_archives(path,placed_at) VALUES(?,?) ON CONFLICT(path) DO NOTHING`,
		path, nowUTC())
	return err
}

func (s *Store) isStudioPlacedArchive(ctx context.Context, path string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM studio_placed_archives WHERE path=? COLLATE NOCASE`, path).Scan(&count)
	return count > 0, err
}

func (s *Store) latestPlayLaunchedEvent(ctx context.Context) (pid int, userPath string, collectionIDs []string, at time.Time, found bool, err error) {
	var dataJSON, atStr string
	err = s.db.QueryRowContext(ctx,
		`SELECT data_json, at FROM events WHERE type='play_launched' ORDER BY id DESC LIMIT 1`).Scan(&dataJSON, &atStr)
	if err == sql.ErrNoRows {
		return 0, "", nil, time.Time{}, false, nil
	}
	if err != nil {
		return 0, "", nil, time.Time{}, false, err
	}
	at, _ = time.Parse(time.RFC3339Nano, atStr)
	var data struct {
		PID           int      `json:"pid"`
		UserPath      string   `json:"userPath"`
		CollectionIDs []string `json:"collectionIds"`
		ModCount      int      `json:"modCount"`
	}
	if jsonErr := parseJSON([]byte(dataJSON), &data); jsonErr != nil {
		return 0, "", nil, time.Time{}, false, jsonErr
	}
	pid = data.PID
	userPath = data.UserPath
	collectionIDs = data.CollectionIDs
	found = true
	return
}

func (s *Store) collectionNamesByIDs(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT name FROM collections WHERE id IN (`+placeholders+`) ORDER BY position, name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if names == nil {
		names = []string{}
	}
	return names, rows.Err()
}

func (s *Store) collectionExistsByID(ctx context.Context, id string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collections WHERE id=?`, id).Scan(&count)
	return count > 0, err
}

// activeArchiveLinkPaths returns the set of active archive_links paths (lowered)
// for use as a cache in the game monitor.
func (s *Store) activeArchiveLinkPaths(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM archive_links WHERE active=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := make(map[string]struct{})
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths[strings.ToLower(filepath.Clean(p))] = struct{}{}
	}
	return paths, rows.Err()
}

// ---------------------------------------------------------------------------
// New flag hydration
// ---------------------------------------------------------------------------

// attachLibraryItemArrivalQuery hydrates AddedAt and New fields.
// Archived mods are never New. New = created within 7 days AND not seen.
func attachLibraryItemArrivalQuery(ctx context.Context, queryer libraryQueryer, items []LibraryItem) error {
	if len(items) == 0 {
		return nil
	}
	const chunkSize = 800
	entityIDs := make([]string, 0, len(items))
	byEntity := make(map[string][]*LibraryItem, len(items))
	for i := range items {
		id := items[i].EntityID
		if _, exists := byEntity[id]; !exists {
			entityIDs = append(entityIDs, id)
		}
		byEntity[id] = append(byEntity[id], &items[i])
	}

	type arrivalInfo struct {
		createdAt string
		seenAt    string
	}
	info := make(map[string]arrivalInfo, len(entityIDs))

	for start := 0; start < len(entityIDs); start += chunkSize {
		end := start + chunkSize
		if end > len(entityIDs) {
			end = len(entityIDs)
		}
		chunk := entityIDs[start:end]
		placeholders := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := queryer.QueryContext(ctx,
			`SELECT e.id, e.created_at, COALESCE(ms.seen_at,'')
			FROM entities e
			LEFT JOIN mod_seen ms ON ms.entity_id=e.id
			WHERE e.id IN (`+placeholders+`)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, createdAt, seenAt string
			if err := rows.Scan(&id, &createdAt, &seenAt); err != nil {
				_ = rows.Close()
				return err
			}
			info[id] = arrivalInfo{createdAt: createdAt, seenAt: seenAt}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	sevenDaysAgo := now.Add(-7 * 24 * time.Hour)

	for _, items := range byEntity {
		for _, item := range items {
			ai, ok := info[item.EntityID]
			if !ok {
				continue
			}
			item.AddedAt = ai.createdAt

			// Archived mods are never New.
			if item.ArchivedAt != "" {
				continue
			}
			// Parse created_at as time.
			createdAt, err := time.Parse(time.RFC3339Nano, ai.createdAt)
			if err != nil {
				continue
			}
			if createdAt.Before(sevenDaysAgo) {
				continue
			}
			if ai.seenAt != "" {
				continue
			}
			item.New = true
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Arrival detection in ScanLibrary
// ---------------------------------------------------------------------------

// detectNewArrivals compares entity IDs before and after a scan, then queues
// arrivals for review. If the library was empty before (first index), all
// entities are marked seen and nothing is queued.
func (service *AppService) detectNewArrivals(ctx context.Context, beforeIDs map[string]struct{}, wasEmpty bool) {
	afterIDs, err := service.store.allEntityIDs(ctx)
	if err != nil {
		log.Printf("game monitor: detect arrivals: list entities: %v", err)
		return
	}

	var newIDs []string
	for id := range afterIDs {
		if _, existed := beforeIDs[id]; !existed {
			newIDs = append(newIDs, id)
		}
	}
	if len(newIDs) == 0 {
		return
	}

	// First index: mark everything seen, queue nothing.
	if wasEmpty {
		if err := service.store.markModsSeen(ctx, newIDs); err != nil {
			log.Printf("game monitor: mark first-index mods seen: %v", err)
		}
		return
	}

	// Check each new entity: if its active link path is studio-placed, auto-see it.
	items, err := service.store.listItemsByIDs(ctx, newIDs)
	if err != nil {
		log.Printf("game monitor: list new items: %v", err)
		return
	}

	now := nowUTC()
	var seenIDs []string
	var reviews []newModReviewRow
	for _, item := range items {
		placed, err := service.store.isStudioPlacedArchive(ctx, item.ArchivePath)
		if err != nil {
			log.Printf("game monitor: check studio-placed %s: %v", item.EntityID, err)
			continue
		}
		if placed {
			seenIDs = append(seenIDs, item.EntityID)
			continue
		}
		origin := classifyModOrigin(item.ArchivePath, service.config.ActiveModsDir)
		reviews = append(reviews, newModReviewRow{
			entityID:   item.EntityID,
			detectedAt: now,
			origin:     origin,
		})
	}

	if len(seenIDs) > 0 {
		if err := service.store.markModsSeen(ctx, seenIDs); err != nil {
			log.Printf("game monitor: mark studio-placed mods seen: %v", err)
		}
	}
	if len(reviews) > 0 {
		if err := service.store.insertNewModReviews(ctx, reviews); err != nil {
			log.Printf("game monitor: insert new mod reviews: %v", err)
			return
		}
		review, err := service.buildPendingNewModsReview(ctx)
		if err != nil {
			log.Printf("game monitor: build review for emit: %v", err)
			return
		}
		service.emit("mods:detected", review)
	}
}

func classifyModOrigin(archivePath, activeModsDir string) string {
	if archivePath == "" || activeModsDir == "" {
		return "library-folder"
	}
	lowerPath := strings.ToLower(filepath.Clean(archivePath))
	lowerActive := strings.ToLower(filepath.Clean(activeModsDir))
	if strings.HasPrefix(lowerPath, lowerActive+string(filepath.Separator)) || lowerPath == lowerActive {
		return "beamng"
	}
	return "library-folder"
}

// ---------------------------------------------------------------------------
// AppService methods (contract)
// ---------------------------------------------------------------------------

func (service *AppService) GetGameStatus() GameStatus {
	service.monitorMu.Lock()
	defer service.monitorMu.Unlock()
	if service.cachedGameStatus == nil {
		status := service.computeGameStatus()
		service.cachedGameStatus = &status
	}
	return *service.cachedGameStatus
}

func (service *AppService) PendingNewMods() (NewModsReview, error) {
	return service.buildPendingNewModsReview(context.Background())
}

func (service *AppService) ResolveNewMods(reviewedIDs, addIDs, collectionIDs []string) (NewModsResolution, error) {
	ctx := context.Background()

	// Validate collection IDs exist.
	for _, cid := range collectionIDs {
		exists, err := service.store.collectionExistsByID(ctx, cid)
		if err != nil {
			return NewModsResolution{}, fmt.Errorf("check collection %s: %w", cid, err)
		}
		if !exists {
			return NewModsResolution{}, fmt.Errorf("collection %s does not exist", cid)
		}
	}

	added := 0
	var collectionNames []string
	if len(addIDs) > 0 && len(collectionIDs) > 0 {
		service.modImportMu.Lock()
		for _, cid := range collectionIDs {
			detail, err := service.store.SetCollectionMods(ctx, cid, addIDs, true)
			if err != nil {
				service.modImportMu.Unlock()
				return NewModsResolution{}, fmt.Errorf("add mods to collection %s: %w", cid, err)
			}
			collectionNames = append(collectionNames, detail.Collection.Name)
			added += len(addIDs)
		}
		service.modImportMu.Unlock()
	}
	if collectionNames == nil {
		collectionNames = []string{}
	}

	// Clear only the reviewed IDs.
	if err := service.store.clearNewModReviews(ctx, reviewedIDs); err != nil {
		return NewModsResolution{}, fmt.Errorf("clear reviewed arrivals: %w", err)
	}

	return NewModsResolution{Added: added, Collections: collectionNames}, nil
}

func (service *AppService) MarkModsSeen(entityIDs []string) error {
	return service.store.markModsSeen(context.Background(), entityIDs)
}

// buildPendingNewModsReview assembles the full pending review.
func (service *AppService) buildPendingNewModsReview(ctx context.Context) (NewModsReview, error) {
	rows, err := service.store.pendingNewModReviews(ctx)
	if err != nil {
		return NewModsReview{}, err
	}
	review := NewModsReview{
		Arrivals:               make([]NewModArrival, 0, len(rows)),
		SuggestedCollectionIDs: []string{},
	}
	if len(rows) == 0 {
		return review, nil
	}

	entityIDs := make([]string, 0, len(rows))
	for _, r := range rows {
		entityIDs = append(entityIDs, r.entityID)
	}
	items, err := service.store.listItemsByIDs(ctx, entityIDs)
	if err != nil {
		return NewModsReview{}, err
	}
	itemMap := make(map[string]LibraryItem, len(items))
	for _, item := range items {
		itemMap[item.EntityID] = item
	}
	for _, r := range rows {
		item, ok := itemMap[r.entityID]
		if !ok {
			continue
		}
		review.Arrivals = append(review.Arrivals, NewModArrival{
			Item:       item,
			Origin:     r.origin,
			DetectedAt: r.detectedAt,
		})
	}

	// Suggested collections = current Play selection's collection IDs.
	playState, err := service.store.GetPlayState(ctx)
	if err == nil && len(playState.CollectionIDs) > 0 {
		for _, cid := range playState.CollectionIDs {
			exists, _ := service.store.collectionExistsByID(ctx, cid)
			if exists {
				review.SuggestedCollectionIDs = append(review.SuggestedCollectionIDs, cid)
			}
		}
	}

	return review, nil
}

// ---------------------------------------------------------------------------
// Game monitor loop
// ---------------------------------------------------------------------------

type candidateSignature struct {
	size    int64
	modTime time.Time
}

type candidateState struct {
	path       string // original-case path, needed for linking
	sig        candidateSignature
	firstSeen  int  // tick number when first seen
	stableSeen int  // tick number when first seen stable (unchanged on two consecutive polls)
	attempted  bool // already attempted scan
}

// candidates and invalidSigs are confined to the monitor goroutine (tests call
// poll directly). Other goroutines read only lastStatus, archiveLinks and
// arriving, all under mu.
type gameMonitor struct {
	service      *AppService
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	lastStatus   *GameStatus
	candidates   map[string]*candidateState    // path key -> state
	invalidSigs  map[string]candidateSignature // path key -> sig that failed zip validation
	archiveLinks map[string]struct{}           // active archive link paths (lowered)
	arriving     int                           // candidates still downloading or waiting to be indexed
	tickCount    int
	processFunc  func() ([]gameProcess, error) // injectable for tests
	clockFunc    func() time.Time              // injectable for tests
}

func newGameMonitor(service *AppService) *gameMonitor {
	ctx, cancel := context.WithCancel(context.Background())
	return &gameMonitor{
		service:      service,
		ctx:          ctx,
		cancel:       cancel,
		candidates:   make(map[string]*candidateState),
		invalidSigs:  make(map[string]candidateSignature),
		archiveLinks: make(map[string]struct{}),
		processFunc:  beamNGProcesses,
		clockFunc:    func() time.Time { return time.Now().UTC() },
	}
}

func (m *gameMonitor) start() {
	go func() {
		// One second keeps the running banner real time; a tick is a process
		// snapshot plus a stat walk of the mods folders.
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-ticker.C:
				m.poll()
			}
		}
	}()
}

func (m *gameMonitor) stop() {
	m.cancel()
}

// refreshArchiveLinks updates the cached archive link paths.
func (m *gameMonitor) refreshArchiveLinks() {
	paths, err := m.service.store.activeArchiveLinkPaths(m.ctx)
	if err != nil {
		log.Printf("game monitor: refresh archive links: %v", err)
		return
	}
	m.mu.Lock()
	m.archiveLinks = paths
	m.mu.Unlock()
}

// poll is the main tick handler.
func (m *gameMonitor) poll() {
	m.tickCount++

	// 1. Status check.
	newStatus := m.service.computeGameStatus()
	m.mu.Lock()
	old := m.lastStatus
	m.lastStatus = &newStatus
	m.mu.Unlock()

	statusChanged := old == nil || !gameStatusEqual(*old, newStatus)
	wasRunning := old != nil && old.Running
	nowStopped := !newStatus.Running

	if statusChanged {
		m.service.monitorMu.Lock()
		m.service.cachedGameStatus = &newStatus
		m.service.monitorMu.Unlock()
		m.service.emit("game:status", newStatus)
	}

	// 2. Transition running → stopped: harvest.
	if wasRunning && nowStopped {
		m.handleExitTransition()
	}

	// 3. Folder polling.
	m.pollFolders()
}

func (m *gameMonitor) handleExitTransition() {
	playRoot, err := playUserPath(m.service.config)
	if err != nil {
		return
	}
	m.service.harvestSessionDownloads(playRoot)
	// Refresh links after harvest scan.
	m.refreshArchiveLinks()
}

func (m *gameMonitor) pollFolders() {
	// TryLock modImportMu for the poll/link step.
	if !m.service.modImportMu.TryLock() {
		return
	}

	// Gather candidate files from ActiveModsDir and profile mods dir.
	dirs := []string{m.service.config.ActiveModsDir}
	if playRoot, err := playUserPath(m.service.config); err == nil {
		dirs = append(dirs, playProfileModsDir(playRoot))
	}

	m.mu.Lock()
	archiveLinks := m.archiveLinks
	m.mu.Unlock()

	// Collect owned paths to skip.
	ownedPaths := make(map[string]bool)
	entries, err := m.service.store.listOwnedArchiveEntries(m.ctx)
	if err == nil {
		for _, entry := range entries {
			if entry.Purpose == archivePurposePlay && entry.OwnerID == playDeploymentOwnerID && entry.State == archiveStateActive {
				ownedPaths[archivePathKey(filepath.Join(entry.TargetRoot, entry.RelativePath))] = true
			}
		}
	}

	// Walk directories, identify candidates.
	currentPaths := make(map[string]bool)
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			base := d.Name()
			if d.IsDir() {
				lower := strings.ToLower(base)
				if strings.HasPrefix(lower, ".beamworlds-") || lower == "unpacked" {
					return filepath.SkipDir
				}
				return nil
			}
			if !isModArchive(base) {
				return nil
			}
			key := archivePathKey(path)
			// Skip if active in archive_links.
			if _, linked := archiveLinks[key]; linked {
				return nil
			}
			// Skip if owned Play entry.
			if ownedPaths[key] {
				return nil
			}
			currentPaths[key] = true
			info, err := d.Info()
			if err != nil {
				return nil
			}
			sig := candidateSignature{size: info.Size(), modTime: info.ModTime()}

			state, exists := m.candidates[key]
			if !exists {
				m.candidates[key] = &candidateState{
					path:      path,
					sig:       sig,
					firstSeen: m.tickCount,
				}
				return nil
			}
			if state.sig != sig {
				// File changed; reset.
				state.sig = sig
				state.firstSeen = m.tickCount
				state.stableSeen = 0
				state.attempted = false
				// Clear invalid sig if it changed.
				delete(m.invalidSigs, key)
				return nil
			}
			// Same sig as last tick.
			if state.stableSeen == 0 && m.tickCount > state.firstSeen+1 {
				state.stableSeen = m.tickCount
			}
			return nil
		})
	}

	// Clean up candidates that disappeared.
	for key := range m.candidates {
		if !currentPaths[key] {
			delete(m.candidates, key)
			delete(m.invalidSigs, key)
		}
	}

	// Collect settled candidates.
	var settledReal []string
	profileModsDir := ""
	if playRoot, err := playUserPath(m.service.config); err == nil {
		profileModsDir = playProfileModsDir(playRoot)
	}

	for key, state := range m.candidates {
		if state.stableSeen == 0 || state.attempted {
			continue
		}
		// Check if this sig was previously invalid.
		if prevSig, invalid := m.invalidSigs[key]; invalid && prevSig == state.sig {
			continue
		}
		originalPath := state.path
		if !isValidZip(originalPath) {
			m.invalidSigs[key] = state.sig
			continue
		}
		state.attempted = true

		// Profile candidate: link into real ActiveModsDir.
		if profileModsDir != "" && isPathWithin(originalPath, profileModsDir) {
			realPath := m.linkProfileToReal(originalPath, profileModsDir)
			if realPath != "" {
				settledReal = append(settledReal, realPath)
			} else {
				// Leave for exit harvest.
				continue
			}
		} else {
			settledReal = append(settledReal, originalPath)
		}
	}

	// A stable file that is not a readable zip (corrupt, or not an archive)
	// is not "arriving"; it counts again once it changes.
	arriving := 0
	for key, state := range m.candidates {
		if state.attempted {
			continue
		}
		if prevSig, invalid := m.invalidSigs[key]; invalid && prevSig == state.sig {
			continue
		}
		arriving++
	}
	m.mu.Lock()
	m.arriving = arriving
	m.mu.Unlock()

	// Release modImportMu before calling ScanLibrary.
	m.service.modImportMu.Unlock()

	if len(settledReal) > 0 {
		if _, err := m.service.ScanLibrary(); err != nil {
			log.Printf("game monitor: scan library: %v", err)
			return
		}
		m.refreshArchiveLinks()

		// Update status to reflect arrivingMods changes.
		newStatus := m.service.computeGameStatus()
		m.mu.Lock()
		old := m.lastStatus
		m.lastStatus = &newStatus
		m.mu.Unlock()
		if old == nil || !gameStatusEqual(*old, newStatus) {
			m.service.monitorMu.Lock()
			m.service.cachedGameStatus = &newStatus
			m.service.monitorMu.Unlock()
			m.service.emit("game:status", newStatus)
		}
	}
}

func (m *gameMonitor) linkProfileToReal(profilePath, profileModsDir string) string {
	rel, err := filepath.Rel(profileModsDir, profilePath)
	if err != nil {
		return ""
	}
	realDest := filepath.Join(m.service.config.ActiveModsDir, rel)

	// Re-check ownership under the lock (we already hold modImportMu).
	entries, err := m.service.store.listOwnedArchiveEntries(m.ctx)
	if err != nil {
		return ""
	}
	profileKey := archivePathKey(profilePath)
	for _, entry := range entries {
		if entry.Purpose == archivePurposePlay && entry.OwnerID == playDeploymentOwnerID && entry.State == archiveStateActive {
			if archivePathKey(filepath.Join(entry.TargetRoot, entry.RelativePath)) == profileKey {
				return "" // owned, skip
			}
		}
	}

	// Check if real destination exists.
	realIdent, realExists, realErr := archiveIdentityIfPresent(realDest)
	if realErr != nil {
		return ""
	}
	srcIdent, srcExists, srcErr := archiveIdentityIfPresent(profilePath)
	if srcErr != nil || !srcExists {
		return ""
	}

	if realExists {
		// Same file already there? Nothing to do, return real path.
		if sameArchiveFileID(srcIdent, realIdent) {
			return realDest
		}
		// Different file there → leave for exit harvest.
		return ""
	}

	// Link profile file into real ActiveModsDir.
	if err := os.MkdirAll(filepath.Dir(realDest), 0o755); err != nil {
		return ""
	}
	if err := os.Link(profilePath, realDest); err != nil {
		return ""
	}
	return realDest
}

func isPathWithin(path, parent string) bool {
	lowerPath := strings.ToLower(filepath.Clean(path))
	lowerParent := strings.ToLower(filepath.Clean(parent))
	return strings.HasPrefix(lowerPath, lowerParent+string(filepath.Separator))
}

func isValidZip(path string) bool {
	r, err := zip.OpenReader(path)
	if err != nil {
		return false
	}
	_ = r.Close()
	return true
}

func (service *AppService) computeGameStatus() GameStatus {
	status := GameStatus{
		CollectionNames: []string{},
	}
	// Mods can arrive in the mods folder while the game is closed (copied in
	// by hand), so the count is independent of the process check.
	if service.monitorInstance != nil {
		service.monitorInstance.mu.Lock()
		status.ArrivingMods = service.monitorInstance.arriving
		service.monitorInstance.mu.Unlock()
	}

	var processes []gameProcess
	if service.monitorInstance != nil && service.monitorInstance.processFunc != nil {
		var err error
		processes, err = service.monitorInstance.processFunc()
		if err != nil {
			return status
		}
	} else {
		// Fallback: use beamNGProcesses directly.
		var err error
		processes, err = beamNGProcesses()
		if err != nil {
			return status
		}
	}

	if len(processes) == 0 {
		return status
	}

	status.Running = true

	// Find earliest process start time.
	earliest := processes[0].Started
	for _, p := range processes[1:] {
		if p.Started.Before(earliest) {
			earliest = p.Started
		}
	}
	if !earliest.IsZero() {
		status.Since = earliest.UTC().Format(time.RFC3339)
	}

	// Check if a Studio session.
	ctx := context.Background()
	pid, _, collectionIDs, launchTime, found, err := service.store.latestPlayLaunchedEvent(ctx)
	if err == nil && found && pid > 0 {
		for _, p := range processes {
			if int(p.PID) == pid {
				// PID reuse guard: start time within 2 minutes.
				timeDiff := p.Started.Sub(launchTime)
				if timeDiff < 0 {
					timeDiff = -timeDiff
				}
				if timeDiff <= 2*time.Minute {
					status.StudioSession = true
					if len(collectionIDs) > 0 {
						names, _ := service.store.collectionNamesByIDs(ctx, collectionIDs)
						status.CollectionNames = names
						status.ModCount = len(collectionIDs) // will be overridden below
					}
					// Get actual mod count from Play runtime marker.
					if activation, exists, _ := service.readPlayRuntimeMarker(); exists {
						status.ModCount = activation.ModCount
					}
				}
				break
			}
		}
	}

	return status
}

func gameStatusEqual(a, b GameStatus) bool {
	if a.Running != b.Running || a.StudioSession != b.StudioSession || a.ModCount != b.ModCount ||
		a.Since != b.Since || a.ArrivingMods != b.ArrivingMods {
		return false
	}
	if len(a.CollectionNames) != len(b.CollectionNames) {
		return false
	}
	for i := range a.CollectionNames {
		if a.CollectionNames[i] != b.CollectionNames[i] {
			return false
		}
	}
	return true
}

// parseJSON is a tiny helper to unmarshal JSON without importing encoding/json
// in callers that don't otherwise need it.
func parseJSON(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
