package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
				item, reused, reuseErr := engine.store.ReuseArchiveAnalysis(ctx, scanID, job.root, job.path, job.info.Size(), job.info.ModTime())
				if reuseErr != nil {
					failed.Add(1)
					progress("analyzing", job.path, false, nil)
					continue
				}
				if reused {
					cached.Add(1)
					analyzed.Add(1)
					engine.emit("library:item", item)
					progress("analyzing", job.path, false, nil)
					continue
				}
				manifest, inspectErr := modkit.Inspect(ctx, job.path)
				if inspectErr != nil {
					failed.Add(1)
					progress("analyzing", job.path, false, nil)
					continue
				}
				var asset *AssetRecord
				if manifest.SelectedImagePath != "" {
					cached, imageErr := modkit.ExtractImage(job.path, manifest.SelectedImagePath, engine.config.ImageCacheDir)
					if imageErr == nil {
						asset = &AssetRecord{SHA256: cached.ID, Path: cached.Path, MIME: cached.MIME, Width: cached.Width, Height: cached.Height, SizeBytes: cached.SizeBytes}
					}
				}
				item, persistErr := engine.store.UpsertArchive(ctx, scanID, job.root, job.path, job.info.Size(), job.info.ModTime(), manifest, asset)
				if persistErr != nil {
					failed.Add(1)
					progress("analyzing", job.path, false, nil)
					continue
				}
				analyzed.Add(1)
				engine.emit("library:item", item)
				progress("analyzing", job.path, false, nil)
			}
		}()
	}
	workers.Wait()
	discoverErr := <-discoveryResult
	if discoverErr == nil {
		discoverErr = ctx.Err()
	}
	finishErr := engine.store.FinishScan(context.Background(), scanID, engine.config.ScanRoots, int(discovered.Load()), int(analyzed.Load()), int(failed.Load()), discoverErr)
	if discoverErr == nil {
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
