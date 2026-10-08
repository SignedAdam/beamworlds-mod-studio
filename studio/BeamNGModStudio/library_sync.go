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
	if service.config.ActiveModsDir != "" && pathWithin(archivePath, filepath.Join(service.config.ActiveModsDir, "unpacked")) {
		return true
	}
	return service.config.DataDir != "" && pathWithin(archivePath, filepath.Join(service.config.DataDir, "draft-sources"))
}

func isSourceFolder(path string) bool {
	kind, err := modkit.SourceKindOf(path)
	return err == nil && kind == modkit.SourceFolder
}

// folderFileRecord tracks the state of one file in a folder mod as Studio
// last wrote or observed it, enabling efficient outside-change detection.
type folderFileRecord struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	ModTimeNS int64  `json:"modTimeNs"`
	SHA256    string `json:"sha256"`
}

func loadFolderState(versionsDir string) ([]folderFileRecord, error) {
	data, err := os.ReadFile(filepath.Join(versionsDir, "folder-state.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []folderFileRecord
	return records, json.Unmarshal(data, &records)
}

func saveFolderState(versionsDir string, records []folderFileRecord) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(versionsDir, "folder-state.json"), data, 0o644)
}

// seedFolderState builds the initial folder-state from the workspace baseline.
// It reads actual file sizes and modification times from the mod folder.
func seedFolderState(folderPath string, baseline []modkit.FileSnapshot) []folderFileRecord {
	records := make([]folderFileRecord, 0, len(baseline))
	for _, file := range baseline {
		rec := folderFileRecord{Path: file.Path, Size: file.SizeBytes, SHA256: file.SHA256}
		if info, err := os.Stat(filepath.Join(folderPath, filepath.FromSlash(file.Path))); err == nil {
			rec.Size = info.Size()
			rec.ModTimeNS = info.ModTime().UnixNano()
		}
		records = append(records, rec)
	}
	return records
}

// folderOriginalSource builds a Source that represents the mod's original state.
// It layers any kept originals over the live folder, then filters to baseline
// paths only. The returned Source must be closed by the caller.
func folderOriginalSource(ctx context.Context, versionsDir, folderPath string, baseline []modkit.FileSnapshot) (modkit.Source, error) {
	keptOriginalsDir := filepath.Join(versionsDir, "original", "files")
	keptSrc, err := modkit.OpenSource(ctx, keptOriginalsDir)
	var layers []modkit.Source
	if err == nil {
		layers = append(layers, keptSrc)
	}
	folderSrc, err := modkit.OpenSource(ctx, folderPath)
	if err != nil {
		if keptSrc != nil {
			keptSrc.Close()
		}
		return nil, err
	}
	layers = append(layers, folderSrc)
	if len(layers) == 1 {
		// No kept originals, filter the folder source to baseline paths.
		return &baselineFilteredSource{inner: layers[0], baseline: baseline}, nil
	}
	return &baselineFilteredSource{inner: modkit.LayeredSource(layers...), baseline: baseline}, nil
}

// baselineFilteredSource limits a Source's entries and Open calls to paths
// that existed in the workspace baseline.
type baselineFilteredSource struct {
	inner    modkit.Source
	baseline []modkit.FileSnapshot
	filtered []modkit.SourceEntry
	once     sync.Once
}

func (s *baselineFilteredSource) Kind() modkit.SourceKind { return s.inner.Kind() }
func (s *baselineFilteredSource) Path() string            { return s.inner.Path() }
func (s *baselineFilteredSource) Close() error            { return s.inner.Close() }

func (s *baselineFilteredSource) Entries() []modkit.SourceEntry {
	s.once.Do(func() {
		allowed := make(map[string]bool, len(s.baseline))
		for _, f := range s.baseline {
			allowed[lowerKey(f.Path)] = true
		}
		for _, e := range s.inner.Entries() {
			if !e.Dir && allowed[lowerKey(e.Path)] {
				s.filtered = append(s.filtered, e)
			}
		}
	})
	return s.filtered
}

func (s *baselineFilteredSource) Open(name string) (io.ReadCloser, error) {
	return s.inner.Open(name)
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
	if isSourceFolder(libraryPath) {
		return service.updateFolderLibraryFromWorkspace(ctx, workspace, baseline, item, author)
	}

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

// updateFolderLibraryFromWorkspace writes workspace changes directly into the
// library folder mod, keeping only the files that differ from what Studio last
// wrote. Before overwriting or deleting a file that existed in the original
// baseline, it keeps a copy under versions/<ws>/original/files/<path>.
func (service *AppService) updateFolderLibraryFromWorkspace(
	ctx context.Context, workspace WorkspaceRecord, baseline modkit.WorkspaceManifest,
	item LibraryItem, author string,
) (string, error) {
	libraryPath := item.ArchivePath
	versionsDir := filepath.Join(service.config.DataDir, "versions", workspace.ID)

	// Load or seed the per-file folder state.
	folderRecs, err := loadFolderState(versionsDir)
	if err != nil {
		return item.EntityID, err
	}
	firstUpdate := folderRecs == nil
	if firstUpdate {
		folderRecs = seedFolderState(libraryPath, baseline.Files)
	}

	// Detect and adopt outside changes.
	row, err := service.store.workspaceLibraryRow(ctx, workspace.ID)
	if err != nil {
		return item.EntityID, err
	}
	folderIsOurs := firstUpdate || row.identity == ""
	if !folderIsOurs {
		fingerprint, err := modkit.FolderListingFingerprint(ctx, libraryPath)
		if err != nil {
			return item.EntityID, err
		}
		folderIsOurs = fingerprint == row.identity
	}

	current, err := modkit.ListWorkspaceFilesContext(ctx, workspace.FilesRoot)
	if err != nil {
		return item.EntityID, err
	}
	conflicts := 0
	if !folderIsOurs {
		adopted, conflicted, err := adoptFolderChanges(ctx, libraryPath, workspace, baseline.Files, current, folderRecs)
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
		// Nothing edited: save the folder state and mark current.
		if err := os.MkdirAll(versionsDir, 0o755); err != nil {
			return item.EntityID, err
		}
		if err := saveFolderState(versionsDir, folderRecs); err != nil {
			return item.EntityID, err
		}
		fingerprint, err := modkit.FolderListingFingerprint(ctx, libraryPath)
		if err != nil {
			return item.EntityID, err
		}
		contentID, err := modkit.SourceContentID(ctx, libraryPath)
		if err != nil {
			return item.EntityID, err
		}
		return item.EntityID, service.store.markWorkspaceLibraryCurrent(ctx, workspace.ID, contentID, fingerprint)
	}

	// Build a set of original paths for keep-original logic.
	originalPaths := make(map[string]modkit.FileSnapshot, len(baseline.Files))
	for _, file := range baseline.Files {
		originalPaths[lowerKey(file.Path)] = file
	}

	// Build a set recording which original files we've already kept.
	keptOriginalsDir := filepath.Join(versionsDir, "original", "files")
	alreadyKept := map[string]bool{}
	_ = filepath.WalkDir(keptOriginalsDir, func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		relative, err := filepath.Rel(keptOriginalsDir, name)
		if err == nil {
			alreadyKept[lowerKey(filepath.ToSlash(relative))] = true
		}
		return nil
	})

	// Prepare the saved version.
	history, err := service.prepareModVersion(ctx, workspace, versionsDir, author, diff)
	if err != nil {
		return item.EntityID, err
	}
	discardHistory := func() {
		if history.dir != "" {
			_ = os.RemoveAll(history.dir)
		}
	}

	// --- Write changed files into the folder, tracking what we write for undo. ---
	type folderWrite struct {
		path       string // absolute path in the library folder
		wasNew     bool   // true if the file didn't exist before
		backupPath string // temporary backup of the file we overwrote
	}
	var writes []folderWrite
	type folderDelete struct {
		path       string
		backupPath string
	}
	var deletes []folderDelete
	undoWrites := func() {
		for i := len(writes) - 1; i >= 0; i-- {
			w := writes[i]
			if w.wasNew {
				_ = os.Remove(w.path)
			} else if w.backupPath != "" {
				_ = os.Rename(w.backupPath, w.path)
			}
		}
		for i := len(deletes) - 1; i >= 0; i-- {
			d := deletes[i]
			if d.backupPath != "" {
				_ = os.Rename(d.backupPath, d.path)
			}
		}
		discardHistory()
	}
	// Determine which files in the folder need to change to match the workspace.
	// Build an index of the current folder state Studio knows about.
	folderStateByKey := make(map[string]folderFileRecord, len(folderRecs))
	for _, rec := range folderRecs {
		folderStateByKey[lowerKey(rec.Path)] = rec
	}
	// Build an index of workspace files.
	currentByKey := make(map[string]modkit.FileSnapshot, len(current))
	for _, file := range current {
		currentByKey[lowerKey(file.Path)] = file
	}

	// Keep the outside version when there are conflicts.
	if conflicts > 0 {
		replacedDir := filepath.Join(versionsDir, "replaced", time.Now().UTC().Format("20060102-150405"), "files")
		for _, file := range diff.changed {
			key := lowerKey(file.Path)
			absPath := filepath.Join(libraryPath, filepath.FromSlash(file.Path))
			info, statErr := os.Stat(absPath)
			if statErr != nil || info.IsDir() {
				continue
			}
			folderRec := folderRecordByKey(folderRecs, key)
			if folderRec != nil && (info.Size() != folderRec.Size || info.ModTime().UnixNano() != folderRec.ModTimeNS) {
				dest := filepath.Join(replacedDir, filepath.FromSlash(file.Path))
				if err := copyFileAtomic(absPath, dest); err != nil {
					discardHistory()
					return item.EntityID, fmt.Errorf("keep outside version of %s: %w", file.Path, err)
				}
			}
		}
	}

	// Write files that differ between workspace and folder. This covers both
	// files that differ from the original and files being restored to the original.
	for _, file := range current {
		key := lowerKey(file.Path)
		absPath := filepath.Join(libraryPath, filepath.FromSlash(file.Path))
		if !pathWithin(absPath, libraryPath) || samePath(absPath, libraryPath) {
			undoWrites()
			return item.EntityID, fmt.Errorf("unsafe path %q", file.Path)
		}
		// Skip files where the folder already holds the workspace content.
		if rec, known := folderStateByKey[key]; known && strings.EqualFold(rec.SHA256, file.SHA256) {
			// Verify the file wasn't changed outside Studio since our last write.
			if info, statErr := os.Stat(absPath); statErr == nil && info.Size() == rec.Size && info.ModTime().UnixNano() == rec.ModTimeNS {
				continue
			}
			// Metadata changed: check actual content.
			if sha, hashErr := computeFileSHA256(absPath); hashErr == nil && strings.EqualFold(sha, file.SHA256) {
				continue
			}
		}

		// If this was an original file and we haven't kept it yet, keep the original.
		if orig, inBaseline := originalPaths[key]; inBaseline && !alreadyKept[key] {
			src := filepath.Join(libraryPath, filepath.FromSlash(orig.Path))
			if info, statErr := os.Stat(src); statErr == nil && !info.IsDir() {
				dest := filepath.Join(keptOriginalsDir, filepath.FromSlash(orig.Path))
				if err := copyFileAtomic(src, dest); err != nil {
					undoWrites()
					return item.EntityID, fmt.Errorf("keep original %s: %w", orig.Path, err)
				}
				alreadyKept[key] = true
			}
		}

		// Write the workspace file to the library folder atomically.
		existing := !isNewFile(absPath)
		var backup string
		if existing {
			tmpBackup, err := os.CreateTemp(filepath.Dir(absPath), ".modstudio-backup-*.tmp")
			if err != nil {
				undoWrites()
				return item.EntityID, err
			}
			backup = tmpBackup.Name()
			_ = tmpBackup.Close()
			if err := os.Rename(absPath, backup); err != nil {
				_ = os.Remove(backup)
				undoWrites()
				return item.EntityID, err
			}
		}
		workspaceFile := filepath.Join(workspace.FilesRoot, filepath.FromSlash(file.Path))
		if err := copyFileAtomic(workspaceFile, absPath); err != nil {
			if backup != "" {
				_ = os.Rename(backup, absPath)
			}
			undoWrites()
			return item.EntityID, fmt.Errorf("write %s to library folder: %w", file.Path, err)
		}
		writes = append(writes, folderWrite{path: absPath, wasNew: !existing, backupPath: backup})
	}

	// Delete files that are in the folder state but not in the workspace.
	for key, rec := range folderStateByKey {
		if _, inWorkspace := currentByKey[key]; inWorkspace {
			continue
		}
		absPath := filepath.Join(libraryPath, filepath.FromSlash(rec.Path))
		if !pathWithin(absPath, libraryPath) || samePath(absPath, libraryPath) {
			continue
		}

		// Keep the original before deleting.
		if orig, inBaseline := originalPaths[key]; inBaseline && !alreadyKept[key] {
			src := filepath.Join(libraryPath, filepath.FromSlash(orig.Path))
			if info, statErr := os.Stat(src); statErr == nil && !info.IsDir() {
				dest := filepath.Join(keptOriginalsDir, filepath.FromSlash(orig.Path))
				if err := copyFileAtomic(src, dest); err != nil {
					undoWrites()
					return item.EntityID, fmt.Errorf("keep original %s: %w", orig.Path, err)
				}
				alreadyKept[key] = true
			}
		}

		info, statErr := os.Stat(absPath)
		if statErr != nil || info.IsDir() {
			continue
		}
		tmpBackup, err := os.CreateTemp(filepath.Dir(absPath), ".modstudio-backup-*.tmp")
		if err != nil {
			undoWrites()
			return item.EntityID, err
		}
		backup := tmpBackup.Name()
		_ = tmpBackup.Close()
		if err := os.Rename(absPath, backup); err != nil {
			_ = os.Remove(backup)
			undoWrites()
			return item.EntityID, err
		}
		deletes = append(deletes, folderDelete{path: absPath, backupPath: backup})
	}

	// Re-inspect the folder and commit.
	manifest, err := modkit.Inspect(ctx, libraryPath)
	if err != nil {
		undoWrites()
		return item.EntityID, fmt.Errorf("inspect the updated folder: %w", err)
	}
	fingerprint, err := modkit.FolderListingFingerprint(ctx, libraryPath)
	if err != nil {
		undoWrites()
		return item.EntityID, err
	}
	contentID, err := modkit.SourceContentID(ctx, libraryPath)
	if err != nil {
		undoWrites()
		return item.EntityID, err
	}
	manifest.FullSHA256 = contentID

	var asset *AssetRecord
	if manifest.SelectedImagePath != "" {
		if cached, extractErr := modkit.ExtractImage(libraryPath, manifest.SelectedImagePath, service.config.ImageCacheDir); extractErr == nil {
			asset = &AssetRecord{SHA256: cached.ID, Path: cached.Path, MIME: cached.MIME, Width: cached.Width, Height: cached.Height, SizeBytes: cached.SizeBytes}
			_, _ = ensureAssetThumbnail(*asset)
		}
	}

	// Do NOT call retireArchiveReferences for folder mods: Play links the live folder.
	if err := service.commitFolderLibraryUpdate(ctx, workspace, item, author, diff, contentID, fingerprint, &manifest, asset, history); err != nil {
		undoWrites()
		return item.EntityID, err
	}

	// Update the folder-state record.
	newFolderRecs := buildFolderStateFromWorkspace(libraryPath, current)
	if err := saveFolderState(versionsDir, newFolderRecs); err != nil {
		log.Printf("save folder state for %s: %v", workspace.ID, err)
	}

	// Clean up backups.
	for _, w := range writes {
		if w.backupPath != "" {
			_ = os.Remove(w.backupPath)
		}
	}
	for _, d := range deletes {
		if d.backupPath != "" {
			_ = os.Remove(d.backupPath)
		}
	}
	history.cleanup()
	return item.EntityID, nil
}

func isNewFile(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}

func folderRecordByKey(records []folderFileRecord, key string) *folderFileRecord {
	for i := range records {
		if lowerKey(records[i].Path) == key {
			return &records[i]
		}
	}
	return nil
}

// buildFolderStateFromWorkspace creates folder-state records from the current
// workspace file list by reading actual file metadata from the library folder.
func buildFolderStateFromWorkspace(folderPath string, current []modkit.FileSnapshot) []folderFileRecord {
	records := make([]folderFileRecord, 0, len(current))
	for _, file := range current {
		rec := folderFileRecord{Path: file.Path, Size: file.SizeBytes, SHA256: file.SHA256}
		if info, err := os.Stat(filepath.Join(folderPath, filepath.FromSlash(file.Path))); err == nil {
			rec.Size = info.Size()
			rec.ModTimeNS = info.ModTime().UnixNano()
		}
		records = append(records, rec)
	}
	return records
}

// commitFolderLibraryUpdate is commitLibraryUpdate for folder mods. It records
// the update using the folder listing fingerprint as the library identity.
func (service *AppService) commitFolderLibraryUpdate(ctx context.Context, workspace WorkspaceRecord, item LibraryItem, author string, diff originalDiff,
	contentID, fingerprint string, manifest *modkit.Manifest, asset *AssetRecord, history modVersionPlan) error {
	service.store.writeMu.Lock()
	defer service.store.writeMu.Unlock()
	tx, err := service.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowUTC()
	if manifest != nil {
		stat, statErr := os.Stat(item.ArchivePath)
		if statErr != nil {
			return statErr
		}
		if _, err := service.store.applyScanArchiveTx(ctx, tx, "", ScanArchive{
			Root: item.RootPath, ArchivePath: item.ArchivePath, SizeBytes: stat.Size(), Modified: stat.ModTime(), Manifest: *manifest, Asset: asset,
		}); err != nil {
			return fmt.Errorf("re-index the library folder: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspaces SET library_sha256=?, library_identity=?, library_synced_at=?,
		library_error='', changed_files=?, library_state_key=?, updated_at=? WHERE id=?`,
		contentID, fingerprint, now, diff.count(), diff.key(), now, workspace.ID); err != nil {
		return err
	}
	if err := history.applyTx(ctx, tx, workspace, author, diff, now); err != nil {
		return err
	}
	if err := appendEventTx(ctx, tx, workspace.EntityID, "mod_library_updated", map[string]any{
		"workspaceId": workspace.ID, "author": author, "changedFiles": diff.count(), "sha256": contentID,
	}); err != nil {
		return err
	}
	return tx.Commit()
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

// adoptFolderChanges is adoptLibraryChanges for folder mods. It detects files
// changed outside Studio by comparing the folder's current listing against the
// per-file records Studio kept (names, sizes, times only; content is read only
// for files that differ). A file changed only outside is brought into the
// workspace. A file changed both outside and in the workspace counts as a
// conflict (ModMaker wins; the caller keeps the outside version).
func adoptFolderChanges(ctx context.Context, folderPath string, workspace WorkspaceRecord,
	baseline, current []modkit.FileSnapshot, folderRecs []folderFileRecord,
) (adopted, conflicts int, err error) {
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
	stateByKey := map[string]folderFileRecord{}
	for _, rec := range folderRecs {
		stateByKey[lowerKey(rec.Path)] = rec
	}

	// Walk the folder to see what's there now.
	inFolder := map[string]bool{}
	if err := filepath.WalkDir(folderPath, func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(folderPath, name)
		if err != nil {
			return nil
		}
		key := lowerKey(filepath.ToSlash(relative))
		inFolder[key] = true
		rec, known := stateByKey[key]
		if !known {
			// New file appeared in the folder.
			if !untouched(key) {
				return nil // workspace also has something here
			}
			dest := filepath.Join(workspace.FilesRoot, filepath.FromSlash(relative))
			if cpErr := copyFileAtomic(name, dest); cpErr != nil {
				return cpErr
			}
			adopted++
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if info.Size() == rec.Size && info.ModTime().UnixNano() == rec.ModTimeNS {
			return nil // unchanged
		}
		// File changed outside Studio.
		if !untouched(key) {
			// Workspace also changed it — conflict.
			sha, hashErr := computeFileSHA256(name)
			if hashErr != nil {
				return nil
			}
			if wsFile, ok := edited[key]; ok && strings.EqualFold(sha, wsFile.SHA256) {
				return nil // same content, no conflict
			}
			conflicts++
			return nil
		}
		// Only outside changed it — adopt.
		displayRelative := filepath.ToSlash(relative)
		if before, ok := original[key]; ok {
			displayRelative = before.Path
		}
		dest := filepath.Join(workspace.FilesRoot, filepath.FromSlash(displayRelative))
		if cpErr := copyFileAtomic(name, dest); cpErr != nil {
			return cpErr
		}
		adopted++
		return nil
	}); err != nil {
		return adopted, conflicts, err
	}

	// Detect deleted files.
	for key, rec := range stateByKey {
		if !inFolder[key] && untouched(key) {
			if err := os.Remove(filepath.Join(workspace.FilesRoot, filepath.FromSlash(rec.Path))); err != nil && !errors.Is(err, fs.ErrNotExist) {
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
	versionsDir := filepath.Join(service.config.DataDir, "versions", workspace.ID)
	lock := service.agents.workspaceToolMutex(workspace.ID)
	lock.Lock()
	err = resetWorkspaceFiles(ctx, workspace, baseline.Files, snapshot, versionsDir)
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
func resetWorkspaceFiles(ctx context.Context, workspace WorkspaceRecord, baseline []modkit.FileSnapshot, snapshot, versionsDir string) error {
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
	// For folder mods, build a layered source from kept originals and the
	// library folder; for ZIPs, open the source archive directly.
	useFolder := isSourceFolder(workspace.SourcePath)
	var src modkit.Source
	if useFolder {
		src, err = folderOriginalSource(ctx, versionsDir, workspace.SourcePath, baseline)
	} else {
		src, err = modkit.OpenSource(ctx, workspace.SourcePath)
	}
	if err != nil {
		return err
	}
	defer src.Close()
	sourceByKey := map[string]modkit.SourceEntry{}
	for _, e := range src.Entries() {
		if !e.Dir {
			sourceByKey[lowerKey(e.Path)] = e
		}
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
			if _, ok := sourceByKey[key]; !ok {
				return fmt.Errorf("the original is missing %s", originalPath)
			}
			if err := extractSourceFile(src, key, workspace.FilesRoot, originalPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// extractSourceFile writes one source entry to root/relative atomically.
func extractSourceFile(src modkit.Source, key, root, relative string) error {
	destination := filepath.Join(root, filepath.FromSlash(relative))
	if !pathWithin(destination, root) || samePath(destination, root) {
		return fmt.Errorf("unsafe archive path %q", relative)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	// Unwrap filtered source.
	inner := src
	if fs, ok := src.(*baselineFilteredSource); ok {
		inner = fs.inner
	}
	reader, err := inner.Open(relative)
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
	if _, err := io.Copy(temporary, reader); err != nil {
		return err
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
		if err := recycleWorkspaceRoot(target); err != nil {
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
