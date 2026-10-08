package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

// ModMaker edits the library mod itself. When workspace files change, Studio
// rebuilds the mod's library archive in place shortly afterwards (and before
// Play). The first rebuild moves the original archive into
// <DataDir>/versions/<workspace>/original, and each rebuild records a saved
// version: the files that differ from the original. Any saved version, or the
// original, can be restored. Archives in folders BeamNG manages are never
// rewritten; their edits stay in the workspace.

// ExportTestSummary records the outcome of the game test that produced an export.
type ExportTestSummary struct {
	TestedAt         string  `json:"testedAt"`
	Level            string  `json:"level"`
	Vehicle          string  `json:"vehicle"`
	SimSeconds       float64 `json:"simSeconds"`
	ScenarioComplete bool    `json:"scenarioComplete"`
	LogErrors        int     `json:"logErrors"`
	Passed           bool    `json:"passed"`
}

const (
	LibraryStateSynced   = "synced"
	LibraryStatePending  = "pending"
	LibraryStateSyncing  = "syncing"
	LibraryStateError    = "error"
	LibraryStateReadOnly = "readonly"

	modVersionOriginalID = "original"

	librarySyncDelay      = 3 * time.Second
	librarySyncRetryDelay = 15 * time.Second
	historyCoalesceWindow = 10 * time.Minute
	historyKeep           = 30

	libraryReadOnlyMessage    = "BeamNG manages this mod's folder, so your changes stay in ModMaker. Use Export to save a copy."
	libraryGameRunningMessage = "BeamNG is running. Your changes are saved here and reach your library once BeamNG closes."
	libraryLegacyMessage      = "These changes aren't in your library yet."
)

var errLibraryGameRunning = errors.New(libraryGameRunningMessage)

// WorkspaceLibraryStatus tells ModMaker whether the library mod matches the workspace.
type WorkspaceLibraryStatus struct {
	State        string `json:"state"`
	ChangedFiles int    `json:"changedFiles"`
	SyncedAt     string `json:"syncedAt,omitempty"`
	Message      string `json:"message,omitempty"`
}

// ModVersion is one restorable state of an edited mod.
type ModVersion struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"` // "original" | "saved"
	Author       string `json:"author"`
	ChangedFiles int    `json:"changedFiles"`
	SavedAt      string `json:"savedAt"`
	Current      bool   `json:"current"`
}

type librarySyncer struct {
	mu      sync.Mutex
	pending map[string]*pendingLibrarySync
	running map[string]bool
}

type pendingLibrarySync struct {
	timer  *time.Timer
	author string
}

// ---------------------------------------------------------------------------
// Scheduling
// ---------------------------------------------------------------------------

// workspaceChanged records that a workspace's files changed and schedules the
// library update. author is "you" or "virgil".
func (service *AppService) workspaceChanged(ctx context.Context, workspaceID, author string) error {
	now := nowUTC()
	if _, err := service.store.db.ExecContext(ctx, `UPDATE workspaces SET updated_at=?, files_changed_at=? WHERE id=?`, now, now, workspaceID); err != nil {
		return err
	}
	service.scheduleLibrarySync(workspaceID, author, librarySyncDelay)
	return nil
}

func (service *AppService) scheduleLibrarySync(workspaceID, author string, delay time.Duration) {
	syncer := &service.librarySync
	syncer.mu.Lock()
	if syncer.pending == nil {
		syncer.pending = map[string]*pendingLibrarySync{}
	}
	entry := syncer.pending[workspaceID]
	if entry == nil {
		entry = &pendingLibrarySync{author: author}
		syncer.pending[workspaceID] = entry
	} else if author == "virgil" {
		entry.author = author
	}
	if entry.timer != nil {
		entry.timer.Stop()
	}
	entry.timer = time.AfterFunc(delay, func() { service.runScheduledLibrarySync(workspaceID) })
	syncer.mu.Unlock()
	service.emitLibraryState(workspaceID, "", LibraryStatePending, "")
}

// takePendingLibrarySync claims a scheduled update so it runs exactly once.
func (service *AppService) takePendingLibrarySync(workspaceID string) (string, bool) {
	syncer := &service.librarySync
	syncer.mu.Lock()
	defer syncer.mu.Unlock()
	entry, ok := syncer.pending[workspaceID]
	if !ok {
		return "", false
	}
	if entry.timer != nil {
		entry.timer.Stop()
	}
	delete(syncer.pending, workspaceID)
	return entry.author, true
}

func (service *AppService) libraryPendingAndRunning(workspaceID string) (pending, running bool) {
	syncer := &service.librarySync
	syncer.mu.Lock()
	defer syncer.mu.Unlock()
	_, pending = syncer.pending[workspaceID]
	return pending, syncer.running[workspaceID]
}

func (service *AppService) runScheduledLibrarySync(workspaceID string) {
	author, ok := service.takePendingLibrarySync(workspaceID)
	if !ok {
		return
	}
	if running, err := service.currentGameRunning(); err == nil && running {
		service.scheduleLibrarySync(workspaceID, author, librarySyncRetryDelay)
		return
	}
	if err := service.syncWorkspaceLibrary(context.Background(), workspaceID, author); err != nil {
		log.Printf("update library from workspace %s: %v", workspaceID, err)
	}
}

// flushLibrarySyncs runs every scheduled update now, so Play and other library
// readers see the latest edits. Updates wait while BeamNG runs.
func (service *AppService) flushLibrarySyncs(ctx context.Context) {
	if running, err := service.currentGameRunning(); err != nil || running {
		return
	}
	syncer := &service.librarySync
	syncer.mu.Lock()
	ids := make([]string, 0, len(syncer.pending))
	for id := range syncer.pending {
		ids = append(ids, id)
	}
	syncer.mu.Unlock()
	for _, id := range ids {
		author, ok := service.takePendingLibrarySync(id)
		if !ok {
			continue
		}
		if err := service.syncWorkspaceLibrary(ctx, id, author); err != nil {
			log.Printf("update library from workspace %s: %v", id, err)
		}
	}
}

// resumeLibrarySyncs schedules updates that were saved but never reached the
// library: edits made just before Studio closed, and mods whose library file an
// earlier Studio version replaced before history existed.
func (service *AppService) resumeLibrarySyncs(ctx context.Context) {
	versionsDir := filepath.Join(service.config.DataDir, "versions")
	rows, err := service.store.db.QueryContext(ctx, `SELECT id, source_path, library_sha256, files_changed_at, library_synced_at FROM workspaces`)
	if err != nil {
		log.Printf("resume library updates: %v", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id, source, sha, changed, synced string
		if rows.Scan(&id, &source, &sha, &changed, &synced) != nil {
			continue
		}
		if (sha == "" && pathWithin(source, versionsDir)) || (sha != "" && changed > synced) {
			ids = append(ids, id)
		}
	}
	_ = rows.Close()
	for _, id := range ids {
		service.scheduleLibrarySync(id, "you", librarySyncDelay)
	}
}

// SyncWorkspaceLibrary updates the library mod from the workspace now.
func (service *AppService) SyncWorkspaceLibrary(workspaceID string) (WorkspaceDetail, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	author, ok := service.takePendingLibrarySync(workspaceID)
	if !ok {
		author = "you"
	}
	if err := service.syncWorkspaceLibrary(context.Background(), workspaceID, author); err != nil {
		return WorkspaceDetail{}, err
	}
	return service.GetWorkspace(workspaceID)
}

// LibraryUpdateEvent reports a ModMaker workspace's library update state.
type LibraryUpdateEvent struct {
	WorkspaceID string `json:"workspaceId"`
	EntityID    string `json:"entityId"`
	State       string `json:"state"`
	Message     string `json:"message"`
}

func (service *AppService) emitLibraryState(workspaceID, entityID, state, message string) {
	if service.emit != nil {
		service.emit("mod:library", LibraryUpdateEvent{WorkspaceID: workspaceID, EntityID: entityID, State: state, Message: message})
	}
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

func (service *AppService) libraryArchiveEditable(archivePath string) bool {
	if archivePath == "" {
		return false
	}
	if service.config.LibraryDir != "" && pathWithin(archivePath, service.config.LibraryDir) {
		return true
	}
	return service.config.DataDir != "" && pathWithin(archivePath, filepath.Join(service.config.DataDir, "draft-sources"))
}

type workspaceLibraryRow struct {
	sha, syncedAt, message, stateKey, identity, filesChangedAt string
	changed                                                    int
}

func (s *Store) workspaceLibraryRow(ctx context.Context, workspaceID string) (workspaceLibraryRow, error) {
	var row workspaceLibraryRow
	err := s.db.QueryRowContext(ctx, `SELECT library_sha256, library_synced_at, library_error, library_state_key, library_identity, files_changed_at, changed_files
		FROM workspaces WHERE id=?`, workspaceID).Scan(&row.sha, &row.syncedAt, &row.message, &row.stateKey, &row.identity, &row.filesChangedAt, &row.changed)
	return row, err
}

// fileIdentity is the size and modification time of a file Studio wrote, so a
// later update can tell whether anything else replaced it since.
func fileIdentity(info os.FileInfo) string {
	return fmt.Sprintf("%d:%s", info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano))
}

// markWorkspaceLibraryCurrent records that the library archive holds the
// workspace's source unchanged, as it does right after a workspace is created.
func (s *Store) markWorkspaceLibraryCurrent(ctx context.Context, workspaceID, sha, identity string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workspaces SET library_sha256=?, library_identity=?, library_synced_at=?, library_error='', changed_files=0, library_state_key=''
		WHERE id=?`, sha, identity, nowUTC(), workspaceID)
	return err
}

func (service *AppService) workspaceLibraryStatus(ctx context.Context, workspace WorkspaceRecord, item LibraryItem) WorkspaceLibraryStatus {
	if !service.libraryArchiveEditable(item.ArchivePath) {
		return WorkspaceLibraryStatus{State: LibraryStateReadOnly, Message: libraryReadOnlyMessage}
	}
	row, err := service.store.workspaceLibraryRow(ctx, workspace.ID)
	if err != nil {
		return WorkspaceLibraryStatus{State: LibraryStateError, Message: err.Error()}
	}
	status := WorkspaceLibraryStatus{State: LibraryStateSynced, ChangedFiles: row.changed, SyncedAt: row.syncedAt}
	pending, running := service.libraryPendingAndRunning(workspace.ID)
	switch {
	case running:
		status.State = LibraryStateSyncing
	case pending:
		status.State = LibraryStatePending
	case row.message != "":
		status.State, status.Message = LibraryStateError, row.message
	case (row.sha == "" && workspace.UpdatedAt > workspace.CreatedAt) || (row.sha != "" && row.filesChangedAt > row.syncedAt):
		// Saved edits that never reached the library, for example from
		// before ModMaker edited the library mod directly.
		status.State, status.Message = LibraryStatePending, libraryLegacyMessage
	}
	return status
}

// ---------------------------------------------------------------------------
// Comparing a workspace with its original
// ---------------------------------------------------------------------------

// originalDiff lists how a workspace differs from the archive it came from.
type originalDiff struct {
	changed []modkit.FileSnapshot // differ from the original or are new
	deleted []string              // original paths no longer present
}

func lowerKey(relativePath string) string {
	return strings.ToLower(strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(relativePath, "\\", "/")), "/"))
}

func compareWithOriginal(baseline, current []modkit.FileSnapshot) originalDiff {
	original := make(map[string]modkit.FileSnapshot, len(baseline))
	for _, file := range baseline {
		original[lowerKey(file.Path)] = file
	}
	present := make(map[string]bool, len(current))
	var diff originalDiff
	for _, file := range current {
		key := lowerKey(file.Path)
		present[key] = true
		if before, ok := original[key]; !ok || !strings.EqualFold(before.SHA256, file.SHA256) {
			diff.changed = append(diff.changed, file)
		}
	}
	for _, file := range baseline {
		if !present[lowerKey(file.Path)] {
			diff.deleted = append(diff.deleted, file.Path)
		}
	}
	sort.Slice(diff.changed, func(i, j int) bool { return lowerKey(diff.changed[i].Path) < lowerKey(diff.changed[j].Path) })
	sort.Slice(diff.deleted, func(i, j int) bool { return lowerKey(diff.deleted[i]) < lowerKey(diff.deleted[j]) })
	return diff
}

func (diff originalDiff) count() int { return len(diff.changed) + len(diff.deleted) }

// key identifies the state a diff describes, so identical states are saved once.
func (diff originalDiff) key() string {
	if diff.count() == 0 {
		return ""
	}
	hash := sha256.New()
	for _, file := range diff.changed {
		fmt.Fprintf(hash, "c\x00%s\x00%s\n", lowerKey(file.Path), strings.ToLower(file.SHA256))
	}
	for _, deleted := range diff.deleted {
		fmt.Fprintf(hash, "d\x00%s\n", lowerKey(deleted))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// ---------------------------------------------------------------------------
// Updating the library archive
// ---------------------------------------------------------------------------

func (service *AppService) syncWorkspaceLibrary(ctx context.Context, workspaceID, author string) error {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	return service.syncWorkspaceLibraryHeld(ctx, workspaceID, author)
}

// syncWorkspaceLibraryHeld is syncWorkspaceLibrary for callers that already
// hold modImportMu.
func (service *AppService) syncWorkspaceLibraryHeld(ctx context.Context, workspaceID, author string) error {
	syncer := &service.librarySync
	syncer.mu.Lock()
	if syncer.running == nil {
		syncer.running = map[string]bool{}
	}
	syncer.running[workspaceID] = true
	syncer.mu.Unlock()
	service.emitLibraryState(workspaceID, "", LibraryStateSyncing, "")

	entityID, err := service.updateLibraryFromWorkspace(ctx, workspaceID, author)

	syncer.mu.Lock()
	delete(syncer.running, workspaceID)
	syncer.mu.Unlock()
	if err != nil {
		message := err.Error()
		if !errors.Is(err, errLibraryGameRunning) {
			message = "Couldn't update your library: " + message
		}
		_, _ = service.store.db.ExecContext(ctx, `UPDATE workspaces SET library_error=? WHERE id=?`, message, workspaceID)
		service.emitLibraryState(workspaceID, entityID, LibraryStateError, message)
		return errors.New(message)
	}
	service.emitLibraryState(workspaceID, entityID, LibraryStateSynced, "")
	return nil
}

// fileMove records a completed move so a failed update can be undone.
type fileMove struct{ from, to, sha string }

func (service *AppService) updateLibraryFromWorkspace(ctx context.Context, workspaceID, author string) (string, error) {
	workspace, baseline, err := service.workspaceAndManifest(workspaceID)
	if err != nil {
		return "", err
	}
	item, err := service.store.GetLibraryItem(ctx, workspace.EntityID)
	if err != nil {
		return workspace.EntityID, err
	}
	libraryPath := item.ArchivePath
	if !service.libraryArchiveEditable(libraryPath) {
		return item.EntityID, nil
	}
	if !item.Linked {
		return item.EntityID, errors.New("the mod's library file is missing; rescan the library")
	}
	if running, err := service.currentGameRunning(); err != nil {
		return item.EntityID, err
	} else if running {
		return item.EntityID, errLibraryGameRunning
	}
	lock := service.agents.workspaceToolMutex(workspace.ID)
	lock.Lock()
	defer lock.Unlock()

	row, err := service.store.workspaceLibraryRow(ctx, workspace.ID)
	if err != nil {
		return item.EntityID, err
	}
	libraryInfo, err := os.Stat(libraryPath)
	if err != nil {
		return item.EntityID, err
	}
	firstUpdate := samePath(workspace.SourcePath, libraryPath)
	if firstUpdate {
		// The library file is still the workspace's source; rebuilding from a
		// changed source would mix two different mods.
		if sha, err := computeFileSHA256(libraryPath); err != nil || !strings.EqualFold(sha, workspace.SourceSHA256) {
			return item.EntityID, errors.New("the library file was changed outside ModMaker after you opened this mod, so Studio can't safely combine the two. Delete this ModMaker project and open the mod again")
		}
	}
	// The library file is ours when it is exactly the file the last update wrote.
	libraryIsOurs := firstUpdate || (row.identity != "" && row.identity == fileIdentity(libraryInfo))

	current, err := modkit.ListWorkspaceFilesContext(ctx, workspace.FilesRoot)
	if err != nil {
		return item.EntityID, err
	}
	conflicts := 0
	if !libraryIsOurs {
		// Something other than ModMaker changed the library file (or an older
		// Studio replaced it). Bring those changes into the workspace first.
		adopted, conflicted, err := adoptLibraryChanges(ctx, libraryPath, workspace, baseline.Files, current)
		if err != nil {
			return item.EntityID, fmt.Errorf("keep changes made outside ModMaker: %w", err)
		}
		conflicts = conflicted
		if adopted > 0 {
			if current, err = modkit.ListWorkspaceFilesContext(ctx, workspace.FilesRoot); err != nil {
				return item.EntityID, err
			}
		}
	}
	diff := compareWithOriginal(baseline.Files, current)
	if firstUpdate && diff.count() == 0 {
		// Nothing edited: the library still holds the original.
		return item.EntityID, service.store.markWorkspaceLibraryCurrent(ctx, workspace.ID, workspace.SourceSHA256, fileIdentity(libraryInfo))
	}

	versionsDir := filepath.Join(service.config.DataDir, "versions", workspace.ID)
	stagingDir := filepath.Join(filepath.Dir(libraryPath), ".beamworlds-staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return item.EntityID, err
	}
	defer os.Remove(stagingDir)
	id, err := modkit.NewID()
	if err != nil {
		return item.EntityID, err
	}
	built := filepath.Join(stagingDir, id+".zip")
	defer os.Remove(built)
	builtSHA := workspace.SourceSHA256
	if diff.count() == 0 {
		if err := copyFileAtomic(workspace.SourcePath, built); err != nil {
			return item.EntityID, err
		}
	} else {
		result, err := modkit.ExportWorkspace(ctx, workspace.SourcePath, workspace.FilesRoot, built, baseline.Files)
		if err != nil {
			return item.EntityID, fmt.Errorf("rebuild the mod: %w", err)
		}
		builtSHA = result.SHA256
	}

	history, err := service.prepareModVersion(ctx, workspace, versionsDir, author, diff)
	if err != nil {
		return item.EntityID, err
	}
	discardHistory := func() {
		if history.dir != "" {
			_ = os.RemoveAll(history.dir)
		}
	}
	if libraryIsOurs && !firstUpdate && strings.EqualFold(builtSHA, row.sha) {
		// The library already holds these bytes; only the record changes.
		if err := service.commitLibraryUpdate(ctx, workspace, item, author, diff, builtSHA, "", nil, nil, history); err != nil {
			discardHistory()
			return item.EntityID, err
		}
		history.cleanup()
		return item.EntityID, nil
	}

	manifest, err := modkit.Inspect(ctx, built)
	if err != nil {
		discardHistory()
		return item.EntityID, fmt.Errorf("inspect the rebuilt mod: %w", err)
	}
	manifest.FullSHA256 = builtSHA
	var asset *AssetRecord
	if manifest.SelectedImagePath != "" {
		if cached, extractErr := modkit.ExtractImage(built, manifest.SelectedImagePath, service.config.ImageCacheDir); extractErr == nil {
			asset = &AssetRecord{SHA256: cached.ID, Path: cached.Path, MIME: cached.MIME, Width: cached.Width, Height: cached.Height, SizeBytes: cached.SizeBytes}
			_, _ = ensureAssetThumbnail(*asset)
		}
	}
	// Play's deployed copy of this mod is retired before its source changes.
	if err := service.retireArchiveReferences(ctx, []string{item.EntityID}); err != nil {
		discardHistory()
		return item.EntityID, err
	}

	var moves []fileMove
	undo := func() {
		for index := len(moves) - 1; index >= 0; index-- {
			if err := moveOrCopy(moves[index].to, moves[index].from, moves[index].sha); err != nil {
				log.Printf("undo library update %s -> %s: %v", moves[index].to, moves[index].from, err)
			}
		}
		discardHistory()
		for _, dir := range []string{filepath.Join(versionsDir, "original"), filepath.Join(versionsDir, "replaced"), filepath.Join(versionsDir, "saved"), versionsDir} {
			_ = os.Remove(dir) // only empty folders go
		}
	}
	newSource := ""
	var parked string
	switch {
	case firstUpdate:
		// The original leaves the library folder so it can always be restored.
		newSource = filepath.Join(versionsDir, "original", filepath.Base(libraryPath))
		if err := moveOrCopy(libraryPath, newSource, workspace.SourceSHA256); err != nil {
			discardHistory()
			return item.EntityID, fmt.Errorf("keep the original mod file: %w", err)
		}
		moves = append(moves, fileMove{libraryPath, newSource, workspace.SourceSHA256})
	case conflicts > 0:
		// The outside change and the workspace both edited some files; the
		// workspace wins, and the outside version is kept, not discarded.
		kept := filepath.Join(versionsDir, "replaced", time.Now().UTC().Format("20060102-150405")+"-"+filepath.Base(libraryPath))
		sha, err := computeFileSHA256(libraryPath)
		if err == nil {
			err = moveOrCopy(libraryPath, kept, sha)
		}
		if err != nil {
			discardHistory()
			return item.EntityID, fmt.Errorf("keep the changed library file: %w", err)
		}
		moves = append(moves, fileMove{libraryPath, kept, sha})
	default:
		// Park the previous build so a failed commit can put it back.
		parked = filepath.Join(stagingDir, id+".previous")
		if err := os.Rename(libraryPath, parked); err != nil {
			discardHistory()
			return item.EntityID, fmt.Errorf("replace the library file: %w", err)
		}
		moves = append(moves, fileMove{libraryPath, parked, item.SHA256})
	}
	if err := os.Rename(built, libraryPath); err != nil {
		undo()
		return item.EntityID, fmt.Errorf("replace the library file: %w", err)
	}
	moves = append(moves, fileMove{built, libraryPath, builtSHA})
	if err := service.commitLibraryUpdate(ctx, workspace, item, author, diff, builtSHA, newSource, &manifest, asset, history); err != nil {
		undo()
		return item.EntityID, err
	}
	if parked != "" {
		_ = os.Remove(parked)
	}
	history.cleanup()
	return item.EntityID, nil
}

// commitLibraryUpdate records an update in one transaction: the re-indexed
// library archive (when manifest is set), the workspace's relocated source and
// library state, the saved version, and the event.
func (service *AppService) commitLibraryUpdate(ctx context.Context, workspace WorkspaceRecord, item LibraryItem, author string, diff originalDiff,
	librarySHA, newSource string, manifest *modkit.Manifest, asset *AssetRecord, history modVersionPlan) error {
	service.store.writeMu.Lock()
	defer service.store.writeMu.Unlock()
	tx, err := service.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowUTC()
	if newSource != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE workspaces SET source_path=? WHERE id=?`, newSource, workspace.ID); err != nil {
			return err
		}
	}
	identity := ""
	if manifest != nil {
		info, err := os.Stat(item.ArchivePath)
		if err != nil {
			return err
		}
		identity = fileIdentity(info)
		if _, err := service.store.applyScanArchiveTx(ctx, tx, "", ScanArchive{
			Root: item.RootPath, ArchivePath: item.ArchivePath, SizeBytes: info.Size(), Modified: info.ModTime(), Manifest: *manifest, Asset: asset,
		}); err != nil {
			return fmt.Errorf("re-index the library file: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspaces SET library_sha256=?, library_identity=COALESCE(NULLIF(?,''),library_identity), library_synced_at=?,
		library_error='', changed_files=?, library_state_key=?, updated_at=? WHERE id=?`,
		librarySHA, identity, now, diff.count(), diff.key(), now, workspace.ID); err != nil {
		return err
	}
	if err := history.applyTx(ctx, tx, workspace, author, diff, now); err != nil {
		return err
	}
	if err := appendEventTx(ctx, tx, workspace.EntityID, "mod_library_updated", map[string]any{
		"workspaceId": workspace.ID, "author": author, "changedFiles": diff.count(), "sha256": librarySHA,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// adoptLibraryChanges copies into the workspace each file the library archive
// changed relative to the original while the workspace still has the original
// (or lacks it in both). Files the workspace also changed keep the workspace's
// version and are counted as conflicts.
func adoptLibraryChanges(ctx context.Context, libraryPath string, workspace WorkspaceRecord, baseline, current []modkit.FileSnapshot) (adopted, conflicts int, err error) {
	original := map[string]modkit.FileSnapshot{}
	for _, file := range baseline {
		original[lowerKey(file.Path)] = file
	}
	edited := map[string]modkit.FileSnapshot{}
	for _, file := range current {
		edited[lowerKey(file.Path)] = file
	}
	untouched := func(key string) bool {
		before, inOriginal := original[key]
		now, inWorkspace := edited[key]
		return inOriginal == inWorkspace && (!inOriginal || strings.EqualFold(before.SHA256, now.SHA256))
	}
	source, err := zip.OpenReader(workspace.SourcePath)
	if err != nil {
		return 0, 0, err
	}
	defer source.Close()
	sourceEntries := map[string]*zip.File{}
	for _, file := range source.File {
		sourceEntries[lowerKey(file.Name)] = file
	}
	library, err := zip.OpenReader(libraryPath)
	if err != nil {
		return 0, 0, err
	}
	defer library.Close()
	inLibrary := map[string]bool{}
	for _, file := range library.File {
		if err := ctx.Err(); err != nil {
			return adopted, conflicts, err
		}
		if file.FileInfo().IsDir() {
			continue
		}
		key := lowerKey(file.Name)
		inLibrary[key] = true
		if before := sourceEntries[key]; before != nil && before.CRC32 == file.CRC32 && before.UncompressedSize64 == file.UncompressedSize64 {
			continue // still the original
		}
		if !untouched(key) {
			if now, ok := edited[key]; !ok || !sameZipContent(file, now.SHA256) {
				conflicts++
			}
			continue
		}
		relative := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(file.Name, "\\", "/")), "/")
		if before, ok := original[key]; ok {
			relative = before.Path
		}
		if err := extractZipFile(file, workspace.FilesRoot, relative); err != nil {
			return adopted, conflicts, err
		}
		adopted++
	}
	for key, file := range original {
		if !inLibrary[key] && untouched(key) {
			if err := os.Remove(filepath.Join(workspace.FilesRoot, filepath.FromSlash(file.Path))); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return adopted, conflicts, err
			}
			adopted++
		}
	}
	return adopted, conflicts, nil
}

func sameZipContent(file *zip.File, sha string) bool {
	reader, err := file.Open()
	if err != nil {
		return false
	}
	defer reader.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, reader); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), sha)
}

// extractZipFile writes one archive member to root/relative atomically,
// verifying its checksum.
func extractZipFile(file *zip.File, root, relative string) error {
	destination := filepath.Join(root, filepath.FromSlash(relative))
	if !pathWithin(destination, root) || samePath(destination, root) {
		return fmt.Errorf("unsafe archive path %q", relative)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	reader, err := file.Open()
	if err != nil {
		return err
	}
	defer reader.Close()
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".modstudio-restore-*.tmp")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporary.Name())
		}
	}()
	checksum := crc32.NewIEEE()
	if _, err := io.Copy(io.MultiWriter(temporary, checksum), reader); err != nil {
		return err
	}
	if checksum.Sum32() != file.CRC32 {
		return fmt.Errorf("%s failed its checksum", relative)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), destination); err != nil {
		return err
	}
	keep = true
	return nil
}

// ---------------------------------------------------------------------------
// Saved versions
// ---------------------------------------------------------------------------

// modVersionPlan is a saved version prepared on disk, recorded in the
// commit's transaction, with superseded snapshot folders removed afterwards.
type modVersionPlan struct {
	skip     bool
	id       string
	insert   bool
	dir      string
	obsolete []string
	pruned   []string
}

func (plan modVersionPlan) cleanup() {
	for _, dir := range plan.obsolete {
		_ = os.RemoveAll(dir)
	}
}

// prepareModVersion snapshots the files that differ from the original. Edits
// by the same author within historyCoalesceWindow update one saved version, so
// history holds meaningful steps rather than every save.
func (service *AppService) prepareModVersion(ctx context.Context, workspace WorkspaceRecord, versionsDir, author string, diff originalDiff) (modVersionPlan, error) {
	if diff.count() == 0 {
		return modVersionPlan{skip: true}, nil
	}
	var existing int
	if err := service.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mod_history WHERE workspace_id=? AND state_key=?`, workspace.ID, diff.key()).Scan(&existing); err != nil {
		return modVersionPlan{}, err
	}
	if existing > 0 {
		return modVersionPlan{skip: true}, nil
	}
	var plan modVersionPlan
	var latestID, latestAuthor, latestPath, latestCreated string
	var latestOpen bool
	err := service.store.db.QueryRowContext(ctx, `SELECT id, author, path, open, created_at FROM mod_history WHERE workspace_id=? ORDER BY updated_at DESC, id DESC LIMIT 1`,
		workspace.ID).Scan(&latestID, &latestAuthor, &latestPath, &latestOpen, &latestCreated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return modVersionPlan{}, err
	}
	created, _ := time.Parse(time.RFC3339Nano, latestCreated)
	if err == nil && latestOpen && latestAuthor == author && time.Since(created) < historyCoalesceWindow {
		plan.id = latestID
		plan.obsolete = append(plan.obsolete, latestPath)
	} else {
		if plan.id, err = modkit.NewID(); err != nil {
			return modVersionPlan{}, err
		}
		plan.insert = true
	}
	folder, err := modkit.NewID()
	if err != nil {
		return modVersionPlan{}, err
	}
	plan.dir = filepath.Join(versionsDir, "saved", folder)
	for _, file := range diff.changed {
		if err := copyFileAtomic(filepath.Join(workspace.FilesRoot, filepath.FromSlash(file.Path)), filepath.Join(plan.dir, "files", filepath.FromSlash(file.Path))); err != nil {
			_ = os.RemoveAll(plan.dir)
			return modVersionPlan{}, fmt.Errorf("save version: %w", err)
		}
	}
	deleted, _ := json.Marshal(append([]string{}, diff.deleted...))
	if err := os.MkdirAll(plan.dir, 0o755); err == nil {
		err = os.WriteFile(filepath.Join(plan.dir, "deleted.json"), deleted, 0o644)
	}
	if err != nil {
		_ = os.RemoveAll(plan.dir)
		return modVersionPlan{}, fmt.Errorf("save version: %w", err)
	}
	if plan.insert {
		rows, err := service.store.db.QueryContext(ctx, `SELECT id, path FROM mod_history WHERE workspace_id=? ORDER BY updated_at DESC, id DESC LIMIT -1 OFFSET ?`,
			workspace.ID, historyKeep-1)
		if err != nil {
			_ = os.RemoveAll(plan.dir)
			return modVersionPlan{}, err
		}
		for rows.Next() {
			var id, dir string
			if rows.Scan(&id, &dir) == nil {
				plan.pruned = append(plan.pruned, id)
				plan.obsolete = append(plan.obsolete, dir)
			}
		}
		_ = rows.Close()
	}
	return plan, nil
}

func (plan modVersionPlan) applyTx(ctx context.Context, tx *sql.Tx, workspace WorkspaceRecord, author string, diff originalDiff, now string) error {
	if plan.skip {
		return nil
	}
	if !plan.insert {
		_, err := tx.ExecContext(ctx, `UPDATE mod_history SET path=?, state_key=?, changed_files=?, updated_at=? WHERE id=?`,
			plan.dir, diff.key(), diff.count(), now, plan.id)
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mod_history SET open=0 WHERE workspace_id=?`, workspace.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mod_history(id,entity_id,workspace_id,author,changed_files,state_key,path,open,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,1,?,?)`, plan.id, workspace.EntityID, workspace.ID, author, diff.count(), diff.key(), plan.dir, now, now); err != nil {
		return err
	}
	for _, id := range plan.pruned {
		if _, err := tx.ExecContext(ctx, `DELETE FROM mod_history WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

// ListModVersions returns the mod's restorable states, newest first, ending
// with its original. It is empty for mods that were never edited in ModMaker.
func (service *AppService) ListModVersions(entityID string) ([]ModVersion, error) {
	ctx := context.Background()
	workspace, err := service.store.GetLatestWorkspaceByEntity(ctx, strings.TrimSpace(entityID))
	if errors.Is(err, sql.ErrNoRows) {
		return []ModVersion{}, nil
	}
	if err != nil {
		return nil, err
	}
	row, err := service.store.workspaceLibraryRow(ctx, workspace.ID)
	if err != nil {
		return nil, err
	}
	pending, running := service.libraryPendingAndRunning(workspace.ID)
	inSync := row.sha != "" && row.message == "" && !pending && !running
	rows, err := service.store.db.QueryContext(ctx, `SELECT id, author, changed_files, updated_at, state_key FROM mod_history
		WHERE workspace_id=? ORDER BY updated_at DESC, id DESC`, workspace.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := []ModVersion{}
	for rows.Next() {
		var version ModVersion
		var stateKey string
		if err := rows.Scan(&version.ID, &version.Author, &version.ChangedFiles, &version.SavedAt, &stateKey); err != nil {
			return nil, err
		}
		version.Kind = "saved"
		version.Current = inSync && stateKey == row.stateKey
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return versions, nil
	}
	return append(versions, ModVersion{ID: modVersionOriginalID, Kind: "original", Current: inSync && row.changed == 0}), nil
}

// RestoreModVersion returns the mod to a saved version or its original. The
// current state is saved first, so a restore can itself be undone.
func (service *AppService) RestoreModVersion(entityID, versionID string) (WorkspaceDetail, error) {
	ctx := context.Background()
	workspace, err := service.store.GetLatestWorkspaceByEntity(ctx, strings.TrimSpace(entityID))
	if err != nil {
		return WorkspaceDetail{}, errors.New("this mod has no saved versions")
	}
	if running, err := service.currentGameRunning(); err != nil {
		return WorkspaceDetail{}, err
	} else if running {
		return WorkspaceDetail{}, errors.New("Close BeamNG before restoring a version.")
	}
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	item, err := service.store.GetLibraryItem(ctx, workspace.EntityID)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	if !service.libraryArchiveEditable(item.ArchivePath) {
		return WorkspaceDetail{}, errors.New(libraryReadOnlyMessage)
	}
	snapshot := ""
	if versionID = strings.TrimSpace(versionID); versionID != modVersionOriginalID {
		if err := service.store.db.QueryRowContext(ctx, `SELECT path FROM mod_history WHERE id=? AND workspace_id=?`, versionID, workspace.ID).Scan(&snapshot); err != nil {
			return WorkspaceDetail{}, errors.New("that version no longer exists")
		}
	}
	// Save the state being replaced as its own version.
	_, _ = service.takePendingLibrarySync(workspace.ID)
	if _, err := service.store.db.ExecContext(ctx, `UPDATE mod_history SET open=0 WHERE workspace_id=?`, workspace.ID); err != nil {
		return WorkspaceDetail{}, err
	}
	if err := service.syncWorkspaceLibraryHeld(ctx, workspace.ID, "you"); err != nil {
		return WorkspaceDetail{}, fmt.Errorf("save the current version first: %w", err)
	}
	workspace, baseline, err := service.workspaceAndManifest(workspace.ID)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	lock := service.agents.workspaceToolMutex(workspace.ID)
	lock.Lock()
	err = resetWorkspaceFiles(ctx, workspace, baseline.Files, snapshot)
	lock.Unlock()
	if err != nil {
		return WorkspaceDetail{}, fmt.Errorf("restore version: %w", err)
	}
	if err := service.syncWorkspaceLibraryHeld(ctx, workspace.ID, "you"); err != nil {
		return WorkspaceDetail{}, err
	}
	_ = service.store.AppendEvent(ctx, workspace.EntityID, "mod_version_restored", map[string]any{"workspaceId": workspace.ID, "versionId": versionID})
	return service.GetWorkspace(workspace.ID)
}

// resetWorkspaceFiles makes the workspace equal the original plus a saved
// version's files (snapshot == "" restores the original).
func resetWorkspaceFiles(ctx context.Context, workspace WorkspaceRecord, baseline []modkit.FileSnapshot, snapshot string) error {
	current, err := modkit.ListWorkspaceFilesContext(ctx, workspace.FilesRoot)
	if err != nil {
		return err
	}
	diff := compareWithOriginal(baseline, current)
	original := map[string]string{}
	for _, file := range baseline {
		original[lowerKey(file.Path)] = file.Path
	}
	targetFiles := map[string]string{}
	targetDeleted := map[string]bool{}
	filesDir := filepath.Join(snapshot, "files")
	if snapshot != "" {
		if err := filepath.WalkDir(filesDir, func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			relative, err := filepath.Rel(filesDir, name)
			if err != nil {
				return err
			}
			targetFiles[lowerKey(relative)] = filepath.ToSlash(relative)
			return nil
		}); err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(snapshot, "deleted.json"))
		if err != nil {
			return err
		}
		var deleted []string
		if err := json.Unmarshal(data, &deleted); err != nil {
			return err
		}
		for _, relative := range deleted {
			targetDeleted[lowerKey(relative)] = true
		}
	}
	touched := map[string]string{}
	for _, file := range diff.changed {
		touched[lowerKey(file.Path)] = file.Path
	}
	for _, relative := range diff.deleted {
		touched[lowerKey(relative)] = relative
	}
	for key, relative := range targetFiles {
		touched[key] = relative
	}
	for key := range targetDeleted {
		if _, ok := touched[key]; !ok {
			touched[key] = original[key]
		}
	}
	source, err := zip.OpenReader(workspace.SourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	sourceEntries := map[string]*zip.File{}
	for _, file := range source.File {
		sourceEntries[lowerKey(file.Name)] = file
	}
	for key, relative := range touched {
		if err := ctx.Err(); err != nil {
			return err
		}
		destination := filepath.Join(workspace.FilesRoot, filepath.FromSlash(relative))
		if !pathWithin(destination, workspace.FilesRoot) || samePath(destination, workspace.FilesRoot) {
			return fmt.Errorf("unsafe workspace path %q", relative)
		}
		switch originalPath, inOriginal := original[key]; {
		case targetFiles[key] != "":
			if err := copyFileAtomic(filepath.Join(filesDir, filepath.FromSlash(targetFiles[key])), destination); err != nil {
				return err
			}
		case targetDeleted[key] || !inOriginal:
			if err := os.Remove(destination); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		default:
			entry := sourceEntries[key]
			if entry == nil {
				return fmt.Errorf("the original is missing %s", originalPath)
			}
			if err := extractZipFile(entry, workspace.FilesRoot, originalPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// removeWorkspaceVersions recycles a deleted workspace's saved versions and
// the original Studio kept for it.
func (service *AppService) removeWorkspaceVersions(workspace WorkspaceRecord) {
	versionsRoot := filepath.Join(service.config.DataDir, "versions")
	targets := []string{filepath.Join(versionsRoot, workspace.ID)}
	if pathWithin(workspace.SourcePath, versionsRoot) && !pathWithin(workspace.SourcePath, targets[0]) {
		targets = append(targets, filepath.Dir(workspace.SourcePath))
	}
	for _, target := range targets {
		if _, err := os.Stat(target); err != nil {
			continue
		}
		if err := recycleFile(target); err != nil {
			log.Printf("recycle saved versions %s: %v", target, err)
		}
	}
}

// ---------------------------------------------------------------------------
// File helpers
// ---------------------------------------------------------------------------

func computeFileSHA256(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// moveOrCopy moves src to dst. When rename fails (for example across volumes)
// it copies, verifies the checksum, and removes src; a failed removal deletes
// the copy, so the move either fully happens or leaves src untouched.
func moveOrCopy(src, dst, expectedSHA string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFileAtomic(src, dst); err != nil {
		return err
	}
	if sha, err := computeFileSHA256(dst); err != nil || !strings.EqualFold(sha, expectedSHA) {
		_ = os.Remove(dst)
		return errors.New("copied file checksum mismatch")
	}
	if err := os.Remove(src); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}
