package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

const maxDiscoveredArchives = 25_000

type ScanProgress struct {
	ScanID     string `json:"scanId"`
	Phase      string `json:"phase"`
	Path       string `json:"path"`
	Discovered int64  `json:"discovered"`
	Analyzed   int64  `json:"analyzed"`
	Cached     int64  `json:"cached"`
	Failed     int64  `json:"failed"`
	Done       bool   `json:"done"`
	Error      string `json:"error,omitempty"`
}

type ScanSummary struct {
	ScanID     string    `json:"scanId"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	Discovered int       `json:"discovered"`
	Analyzed   int       `json:"analyzed"`
	Cached     int       `json:"cached"`
	Failed     int       `json:"failed"`
	Cancelled  bool      `json:"cancelled"`
	Error      string    `json:"error,omitempty"`
}

type archiveJob struct {
	root string
	path string
	info fs.FileInfo
}

type LibraryEngine struct {
	store  *Store
	config AppConfig
	emit   func(string, any)

	mu     sync.Mutex
	cancel context.CancelFunc
}

func NewLibraryEngine(store *Store, config AppConfig, emit func(string, any)) *LibraryEngine {
	return &LibraryEngine{store: store, config: config, emit: emit}
}

type beamNGSourceIndex struct {
	byPath     map[string]bool
	byFilename map[string][]bool
}

func loadBeamNGSourceIndex(config AppConfig) (beamNGSourceIndex, error) {
	index := beamNGSourceIndex{
		byPath:     make(map[string]bool),
		byFilename: make(map[string][]bool),
	}
	activeModsDir := strings.TrimSpace(config.ActiveModsDir)
	if activeModsDir == "" {
		return index, nil
	}
	payload, err := os.ReadFile(filepath.Join(activeModsDir, "db.json"))
	if errors.Is(err, os.ErrNotExist) {
		return index, nil
	}
	if err != nil {
		return index, fmt.Errorf("read BeamNG mod database: %w", err)
	}
	payload = bytes.TrimPrefix(payload, []byte{0xef, 0xbb, 0xbf})
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		return index, fmt.Errorf("parse BeamNG mod database: %w", err)
	}
	var mods map[string]json.RawMessage
	if raw := document["mods"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &mods); err != nil {
			return index, fmt.Errorf("parse BeamNG mod entries: %w", err)
		}
	}
	for _, raw := range mods {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entry); err != nil || entry == nil {
			continue
		}
		filename := beamNGDatabaseString(entry, "filename")
		if filename == "" {
			continue
		}
		dirname := beamNGDatabaseString(entry, "dirname")
		dirname = strings.ToLower(strings.ReplaceAll(dirname, "\\", "/"))
		repository := strings.Contains(dirname, "/repo")
		archivePath := beamNGDatabaseArchivePath(activeModsDir, beamNGDatabaseString(entry, "fullpath"), filename)
		index.byPath[archiveSourcePathKey(archivePath)] = repository
		filenameKey := archiveSourceFilenameKey(filename)
		index.byFilename[filenameKey] = append(index.byFilename[filenameKey], repository)
	}
	return index, nil
}

func beamNGDatabaseString(entry map[string]json.RawMessage, key string) string {
	raw := entry[key]
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func beamNGDatabaseArchivePath(activeModsDir, fullPath, filename string) string {
	normalized := strings.TrimLeft(strings.TrimSpace(fullPath), "/\\")
	normalized = strings.ReplaceAll(normalized, "\\", "/")
	switch {
	case strings.EqualFold(normalized, "mods"):
		normalized = ""
	case len(normalized) >= len("mods/") && strings.EqualFold(normalized[:len("mods/")], "mods/"):
		normalized = strings.TrimLeft(normalized[len("mods/"):], "/")
	}
	if normalized == "" {
		normalized = strings.ReplaceAll(filename, "\\", "/")
	}
	archivePath := filepath.FromSlash(normalized)
	if filepath.IsAbs(archivePath) {
		return filepath.Clean(archivePath)
	}
	return filepath.Join(activeModsDir, archivePath)
}

func archiveSourcePathKey(value string) string {
	normalized := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(value, "\\", "/")))
	if absolute, err := filepath.Abs(normalized); err == nil {
		normalized = absolute
	}
	return strings.ToLower(normalized)
}

func archiveSourceFilenameKey(value string) string {
	return strings.ToLower(filepath.Base(filepath.FromSlash(strings.ReplaceAll(value, "\\", "/"))))
}

func (index beamNGSourceIndex) repositoryFor(path string) bool {
	if repository, ok := index.byPath[archiveSourcePathKey(path)]; ok {
		return repository
	}
	candidates := index.byFilename[archiveSourceFilenameKey(path)]
	return len(candidates) == 1 && candidates[0]
}

func configuredRepositoryPath(config AppConfig, path string) bool {
	activeModsDir := strings.TrimSpace(config.ActiveModsDir)
	if activeModsDir == "" {
		return false
	}
	return pathWithin(path, filepath.Join(activeModsDir, "repo"))
}

func canonicalArchiveSource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "beamng-repository":
		return "beamng-repository"
	case "user-added":
		return "user-added"
	default:
		return ""
	}
}

func storedArchiveSource(item LibraryItem) string {
	if source := canonicalArchiveSource(item.SourceID); source != "" {
		return source
	}
	switch strings.ToLower(strings.TrimSpace(item.Source)) {
	case "beamng repository":
		return "beamng-repository"
	case "user added":
		return "user-added"
	default:
		return ""
	}
}

func archiveSourceClass(config AppConfig, index beamNGSourceIndex, path string, stored *LibraryItem) string {
	if stored != nil {
		if source := storedArchiveSource(*stored); source != "" {
			return source
		}
	}
	if index.repositoryFor(path) || configuredRepositoryPath(config, path) {
		return "beamng-repository"
	}
	return "user-added"
}

func (engine *LibraryEngine) Scan(parent context.Context) (ScanSummary, error) {
	engine.mu.Lock()
	if engine.cancel != nil {
		engine.mu.Unlock()
		return ScanSummary{}, errors.New("a library scan is already running")
	}
	ctx, cancel := context.WithCancel(parent)
	engine.cancel = cancel
	engine.mu.Unlock()
	defer func() {
		cancel()
		engine.mu.Lock()
		engine.cancel = nil
		engine.mu.Unlock()
	}()

	started := time.Now().UTC()
	scanID, err := engine.store.BeginScan(ctx, engine.config.ScanRoots)
	if err != nil {
		return ScanSummary{}, err
	}
	var discovered atomic.Int64
	var analyzed atomic.Int64
	var failed atomic.Int64
	var cached atomic.Int64
	var archivesMu sync.Mutex
	archives := make([]ScanArchive, 0)
	var processingMu sync.Mutex
	var processingErr error
	recordProcessingError := func(err error) {
		if err == nil {
			return
		}
		processingMu.Lock()
		if processingErr == nil {
			processingErr = err
		}
		processingMu.Unlock()
	}
	appendArchive := func(archive ScanArchive) {
		archivesMu.Lock()
		archives = append(archives, archive)
		archivesMu.Unlock()
	}
	jobs := make(chan archiveJob, engine.config.ScanConcurrency*2)
	discoveryResult := make(chan error, 1)
	progress := func(phase, currentPath string, done bool, scanErr error) {
		update := ScanProgress{ScanID: scanID, Phase: phase, Path: currentPath, Discovered: discovered.Load(), Analyzed: analyzed.Load(), Cached: cached.Load(), Failed: failed.Load(), Done: done}
		if scanErr != nil {
			update.Error = scanErr.Error()
		}
		engine.emit("library:scan", update)
	}
	progress("discovering", "", false, nil)
	sourceIndex, sourceErr := loadBeamNGSourceIndex(engine.config)
	if sourceErr != nil {
		recordProcessingError(fmt.Errorf("prepare archive source classification: %w", sourceErr))
	}

	go func() {
		defer close(jobs)
		for _, root := range engine.config.ScanRoots {
			if err := engine.discoverRoot(ctx, root, jobs, &discovered, progress); err != nil {
				discoveryResult <- err
				return
			}
		}
		discoveryResult <- nil
	}()

	var workers sync.WaitGroup
	workers.Add(engine.config.ScanConcurrency)
	for range engine.config.ScanConcurrency {
		go func() {
			defer workers.Done()
			for job := range jobs {
				if ctx.Err() != nil {
					return
				}
				reusedItem, reused, reuseErr := engine.store.LookupArchiveAnalysis(ctx, job.root, job.path, job.info.Size(), job.info.ModTime())
				if reuseErr != nil {
					recordProcessingError(reuseErr)
					failed.Add(1)
					progress("analyzing", job.path, false, reuseErr)
					continue
				}
				if reused {
					sourceClass := archiveSourceClass(engine.config, sourceIndex, job.path, &reusedItem)
					appendArchive(ScanArchive{
						Root: job.root, ArchivePath: job.path, SizeBytes: job.info.Size(),
						Modified: job.info.ModTime(), SourceClass: sourceClass, Reused: true,
					})
					cached.Add(1)
					analyzed.Add(1)
					progress("analyzing", job.path, false, nil)
					continue
				}
				manifest, inspectErr := modkit.Inspect(ctx, job.path)
				if inspectErr != nil {
					scanErr := fmt.Errorf("inspect archive %q: %w", job.path, inspectErr)
					recordProcessingError(scanErr)
					failed.Add(1)
					progress("analyzing", job.path, false, scanErr)
					continue
				}
				var asset *AssetRecord
				if manifest.SelectedImagePath != "" {
					cachedAsset, imageErr := modkit.ExtractImage(job.path, manifest.SelectedImagePath, engine.config.ImageCacheDir)
					if imageErr == nil {
						asset = &AssetRecord{SHA256: cachedAsset.ID, Path: cachedAsset.Path, MIME: cachedAsset.MIME, Width: cachedAsset.Width, Height: cachedAsset.Height, SizeBytes: cachedAsset.SizeBytes}
					}
				}
				appendArchive(ScanArchive{
					Root: job.root, ArchivePath: job.path, SizeBytes: job.info.Size(),
					Modified: job.info.ModTime(), Manifest: manifest, Asset: asset,
					SourceClass: archiveSourceClass(engine.config, sourceIndex, job.path, &reusedItem),
				})
				analyzed.Add(1)
				progress("analyzing", job.path, false, nil)
			}
		}()
	}
	workers.Wait()
	discoverErr := <-discoveryResult
	if discoverErr == nil {
		discoverErr = ctx.Err()
	}
	processingMu.Lock()
	if discoverErr == nil {
		discoverErr = processingErr
	}
	processingMu.Unlock()

	var finishErr error
	var committedItems []LibraryItem
	if discoverErr == nil {
		archivesMu.Lock()
		batch := append([]ScanArchive(nil), archives...)
		archivesMu.Unlock()
		committedItems, finishErr = engine.store.ApplyScanBatch(ctx, scanID, engine.config.ScanRoots, batch, int(discovered.Load()), int(analyzed.Load()), int(failed.Load()))
		if finishErr != nil {
			discoverErr = finishErr
			finishErr = engine.store.FinishScan(context.Background(), scanID, engine.config.ScanRoots, int(discovered.Load()), int(analyzed.Load()), int(failed.Load()), discoverErr)
		}
	} else {
		finishErr = engine.store.FinishScan(context.Background(), scanID, engine.config.ScanRoots, int(discovered.Load()), int(analyzed.Load()), int(failed.Load()), discoverErr)
	}
	if discoverErr == nil && finishErr != nil {
		discoverErr = finishErr
	}
	finished := time.Now().UTC()
	summary := ScanSummary{
		ScanID: scanID, StartedAt: started, FinishedAt: finished,
		Discovered: int(discovered.Load()), Analyzed: int(analyzed.Load()), Cached: int(cached.Load()), Failed: int(failed.Load()),
		Cancelled: errors.Is(discoverErr, context.Canceled),
	}
	if discoverErr != nil {
		summary.Error = discoverErr.Error()
	}
	if discoverErr == nil {
		for _, item := range committedItems {
			engine.emit("library:item", item)
		}
	}
	progress("complete", "", true, discoverErr)
	return summary, discoverErr
}

func (engine *LibraryEngine) Cancel() bool {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.cancel == nil {
		return false
	}
	engine.cancel()
	return true
}

func (engine *LibraryEngine) discoverRoot(ctx context.Context, root string, jobs chan<- archiveJob, discovered *atomic.Int64, progress func(string, string, bool, error)) error {
	root = filepath.Clean(root)
	return filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if current != root && engine.skipDirectory(current, entry.Name()) {
				return fs.SkipDir
			}
			if entry.Type()&osModeSymlink != 0 {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&osModeSymlink != 0 || !isModArchive(entry.Name()) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		count := discovered.Add(1)
		if count > maxDiscoveredArchives {
			return fmt.Errorf("archive discovery limit exceeded: %d", maxDiscoveredArchives)
		}
		progress("discovering", current, false, nil)
		select {
		case jobs <- archiveJob{root: root, path: filepath.Clean(current), info: info}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}

const osModeSymlink = fs.ModeSymlink

func (engine *LibraryEngine) skipDirectory(current, name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case ".git", "node_modules", "backups", "temp", "$recycle.bin", "system volume information", "windows.old", "__macosx":
		return true
	}
	if lower == managedModDirectoryName || strings.HasPrefix(lower, ".beamworlds-managed-") {
		return true
	}
	for _, excluded := range []string{engine.config.ProjectRoot, engine.config.DataDir} {
		if excluded != "" && pathWithin(current, excluded) {
			return true
		}
	}
	return false
}

func isModArchive(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".zip.stop")
}

func pathWithin(candidate, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
