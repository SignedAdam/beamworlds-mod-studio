package modkit

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// SourceKind distinguishes unpacked folder mods from ZIP archives.
type SourceKind string

const (
	SourceZIP    SourceKind = "zip"
	SourceFolder SourceKind = "folder"
)

// SourceEntry holds metadata for one file or directory inside a Source.
type SourceEntry struct {
	Path           string
	Dir            bool
	Size           int64
	CompressedSize int64
	ModifiedAt     time.Time
	CRC32          uint32
	Method         uint16
	Flags          uint16
}

// Source is the files of one mod, whether it is a ZIP file or an unpacked folder.
type Source interface {
	Kind() SourceKind
	Path() string
	Entries() []SourceEntry
	Open(name string) (io.ReadCloser, error) // case-insensitive, path-safe
	Close() error
}

// OpenSource opens path as a ZIP file or folder source.
func OpenSource(ctx context.Context, path string) (Source, error) {
	kind, err := SourceKindOf(path)
	if err != nil {
		return nil, err
	}
	switch kind {
	case SourceFolder:
		return openFolderSource(ctx, path)
	default:
		return openZipSource(path)
	}
}

// ReadSourceEntry reads one non-directory entry with bounded-read semantics.
func ReadSourceEntry(src Source, name string, limit int64) ([]byte, bool, error) {
	if limit <= 0 || limit > MaxArchiveMemberBytes {
		limit = MaxArchiveMemberBytes
	}
	rc, err := src.Open(name)
	if err != nil {
		return nil, false, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, false, err
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	return data, truncated, nil
}

// DiffWorkspaceSource is DiffWorkspaceContext against an already-open Source.
func DiffWorkspaceSource(ctx context.Context, src Source, filesRoot string, baseline []FileSnapshot) ([]WorkspaceChange, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	current, err := ListWorkspaceFilesContext(ctx, filesRoot)
	if err != nil {
		return nil, err
	}
	before := make(map[string]FileSnapshot, len(baseline))
	after := make(map[string]FileSnapshot, len(current))
	for _, item := range baseline {
		before[strings.ToLower(item.Path)] = item
	}
	for _, item := range current {
		after[strings.ToLower(item.Path)] = item
	}
	differ := diffmatchpatch.New()
	changes := []WorkspaceChange{}
	for key, oldItem := range before {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		newItem, exists := after[key]
		if !exists {
			change := WorkspaceChange{Path: oldItem.Path, Type: "deleted", BeforeSHA: oldItem.SHA256, SizeBytes: oldItem.SizeBytes}
			if oldItem.SizeBytes <= maxDiffBytes {
				if oldText, oldErr := readSourceText(ctx, src, oldItem.Path, maxDiffBytes); oldErr == nil {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					change.TextDiff = semanticTextDiff(differ, oldItem.Path, oldText, "")
				} else if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			changes = append(changes, change)
			continue
		}
		if oldItem.SHA256 == newItem.SHA256 {
			continue
		}
		change := WorkspaceChange{Path: newItem.Path, Type: "modified", BeforeSHA: oldItem.SHA256, AfterSHA: newItem.SHA256, SizeBytes: newItem.SizeBytes}
		if oldItem.SizeBytes <= maxDiffBytes && newItem.SizeBytes <= maxDiffBytes {
			oldText, oldErr := readSourceText(ctx, src, oldItem.Path, maxDiffBytes)
			newText, newErr := ReadWorkspaceTextContext(ctx, filesRoot, newItem.Path)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if oldErr == nil && newErr == nil {
				change.TextDiff = semanticTextDiff(differ, newItem.Path, oldText, newText)
			}
		}
		changes = append(changes, change)
	}
	for key, newItem := range after {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, exists := before[key]; exists {
			continue
		}
		change := WorkspaceChange{Path: newItem.Path, Type: "added", AfterSHA: newItem.SHA256, SizeBytes: newItem.SizeBytes}
		if newItem.SizeBytes <= maxDiffBytes {
			if newText, newErr := ReadWorkspaceTextContext(ctx, filesRoot, newItem.Path); newErr == nil {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				change.TextDiff = semanticTextDiff(differ, newItem.Path, "", newText)
			} else if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		changes = append(changes, change)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

// readSourceText reads a text file from a Source for diff purposes.
func readSourceText(ctx context.Context, src Source, memberPath string, limit int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, _, err := ReadSourceEntry(src, memberPath, limit)
	if err != nil {
		return "", err
	}
	if !isText(data) {
		return "", fmt.Errorf("file is binary")
	}
	return string(data), nil
}

// sourceEntryByName looks up an entry by normalized case-insensitive name.
func sourceEntryByName(entries []SourceEntry, name string) (SourceEntry, bool) {
	normalized, err := normalizeArchivePath(name)
	if err != nil {
		return SourceEntry{}, false
	}
	lower := strings.ToLower(normalized)
	for _, e := range entries {
		if strings.ToLower(e.Path) == lower {
			return e, true
		}
	}
	return SourceEntry{}, false
}

// SourceKindOf reports whether path is a ZIP file or an unpacked folder.
func SourceKindOf(path string) (SourceKind, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("source kind: %w", err)
	}
	if info.IsDir() {
		return SourceFolder, nil
	}
	if info.Mode().IsRegular() {
		return SourceZIP, nil
	}
	return "", fmt.Errorf("source path is neither a regular file nor a directory: %s", path)
}

// FolderListingFingerprint hashes the sorted listing of root.
func FolderListingFingerprint(ctx context.Context, root string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	entries, err := walkFolderListing(ctx, root)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, entry := range entries {
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%d\x00%t\n", entry.path, entry.size, entry.modTime.UnixNano(), entry.isDir)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// SourceContentID is FullSHA256 for a ZIP and "folder:"+FolderListingFingerprint for a folder.
func SourceContentID(ctx context.Context, path string) (string, error) {
	kind, err := SourceKindOf(path)
	if err != nil {
		return "", err
	}
	if kind == SourceFolder {
		fp, err := FolderListingFingerprint(ctx, path)
		if err != nil {
			return "", err
		}
		return "folder:" + fp, nil
	}
	return FullSHA256(ctx, path)
}

// ---------------------------------------------------------------------------
// zipSource
// ---------------------------------------------------------------------------

type zipSource struct {
	reader   *zip.ReadCloser
	path     string
	entries  []SourceEntry
	rawFiles []*zip.File          // parallel to entries for positional reads
	byName   map[string]*zip.File // lower-cased normalised name → first *zip.File
	issues   []Issue
}

func openZipSource(path string) (*zipSource, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open ZIP: %w", err)
	}
	if len(reader.File) > maxEntries {
		_ = reader.Close()
		return nil, fmt.Errorf("archive has %d entries; limit is %d", len(reader.File), maxEntries)
	}
	entries := make([]SourceEntry, 0, len(reader.File))
	rawFiles := make([]*zip.File, 0, len(reader.File))
	byName := make(map[string]*zip.File, len(reader.File))
	issues := []Issue{}
	for _, file := range reader.File {
		name, pathErr := normalizeArchivePath(file.Name)
		if pathErr != nil {
			issues = append(issues, Issue{Code: "unsafe-path", Severity: SeverityError, Message: pathErr.Error(), Path: file.Name})
			continue
		}
		lower := strings.ToLower(name)
		if _, exists := byName[lower]; !exists {
			byName[lower] = file
		}
		rawFile := file
		entries = append(entries, SourceEntry{
			Path:           name,
			Dir:            file.FileInfo().IsDir(),
			Size:           int64(file.UncompressedSize64),
			CompressedSize: int64(file.CompressedSize64),
			ModifiedAt:     file.Modified.UTC(),
			CRC32:          file.CRC32,
			Method:         file.Method,
			Flags:          file.Flags,
		})
		rawFiles = append(rawFiles, rawFile)
	}
	return &zipSource{reader: reader, path: path, entries: entries, rawFiles: rawFiles, byName: byName, issues: issues}, nil
}

func (s *zipSource) Kind() SourceKind   { return SourceZIP }
func (s *zipSource) Path() string       { return s.path }
func (s *zipSource) Entries() []SourceEntry { return s.entries }
func (s *zipSource) Close() error       { return s.reader.Close() }

func (s *zipSource) Open(name string) (io.ReadCloser, error) {
	normalized, err := normalizeArchivePath(name)
	if err != nil {
		return nil, err
	}
	file := s.byName[strings.ToLower(normalized)]
	if file == nil {
		return nil, fmt.Errorf("source member %q does not exist", normalized)
	}
	if file.FileInfo().IsDir() {
		return nil, fmt.Errorf("source member %q is a directory", normalized)
	}
	return openZipEntry(file)
}

// rawZipFile returns the underlying *zip.File for ZIP-specific optimisations
// (raw copy during export). Only used via type assertion inside modkit.
func (s *zipSource) rawZipFile(name string) *zip.File {
	normalized, err := normalizeArchivePath(name)
	if err != nil {
		return nil
	}
	return s.byName[strings.ToLower(normalized)]
}

// ReadSourceEntryAt reads the entry at position index in src.Entries().
// For ZIP sources this reads the exact ZIP member at that position (important
// when duplicate case-insensitive paths exist). For other sources it falls
// back to ReadSourceEntry by name.
func ReadSourceEntryAt(src Source, index int, limit int64) ([]byte, bool, error) {
	entries := src.Entries()
	if index < 0 || index >= len(entries) {
		return nil, false, fmt.Errorf("entry index %d out of range", index)
	}
	if limit <= 0 || limit > MaxArchiveMemberBytes {
		limit = MaxArchiveMemberBytes
	}
	if zs, ok := src.(*zipSource); ok && index < len(zs.rawFiles) {
		file := zs.rawFiles[index]
		if file.FileInfo().IsDir() {
			return nil, false, fmt.Errorf("source member %q is a directory", entries[index].Path)
		}
		rc, err := openZipEntry(file)
		if err != nil {
			return nil, false, err
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, limit+1))
		if err != nil {
			return nil, false, err
		}
		truncated := int64(len(data)) > limit
		if truncated {
			data = data[:limit]
		}
		return data, truncated, nil
	}
	return ReadSourceEntry(src, entries[index].Path, limit)
}

// ---------------------------------------------------------------------------
// folderSource
// ---------------------------------------------------------------------------

type folderSource struct {
	root    string
	entries []SourceEntry
	issues  []Issue
}

func openFolderSource(ctx context.Context, root string) (*folderSource, error) {
	rawEntries, issues, err := walkFolderListingWithIssues(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("walk folder: %w", err)
	}
	entries := make([]SourceEntry, 0, len(rawEntries))
	for _, e := range rawEntries {
		if _, pathErr := normalizeArchivePath(e.path); pathErr != nil {
			issues = append(issues, Issue{Code: "unsafe-path", Severity: SeverityError, Message: pathErr.Error(), Path: e.path})
			continue
		}
		entries = append(entries, SourceEntry{
			Path:           e.path,
			Dir:            e.isDir,
			Size:           e.size,
			CompressedSize: e.size,
			ModifiedAt:     e.modTime,
			Method:         0, // Store
		})
	}
	return &folderSource{root: root, entries: entries, issues: issues}, nil
}

func (s *folderSource) Kind() SourceKind   { return SourceFolder }
func (s *folderSource) Path() string       { return s.root }
func (s *folderSource) Entries() []SourceEntry { return s.entries }
func (s *folderSource) Close() error       { return nil }

func (s *folderSource) Open(name string) (io.ReadCloser, error) {
	normalized, err := normalizeArchivePath(name)
	if err != nil {
		return nil, err
	}
	// Case-insensitive lookup.
	lower := strings.ToLower(normalized)
	for _, e := range s.entries {
		if strings.ToLower(e.Path) == lower {
			if e.Dir {
				return nil, fmt.Errorf("source member %q is a directory", normalized)
			}
			filename, joinErr := safeFolderJoin(s.root, e.Path)
			if joinErr != nil {
				return nil, joinErr
			}
			return os.Open(filename)
		}
	}
	return nil, fmt.Errorf("source member %q does not exist", normalized)
}

// ---------------------------------------------------------------------------
// LayeredSource
// ---------------------------------------------------------------------------

type layeredSource struct {
	layers  []Source
	merged  []SourceEntry
}

// LayeredSource reads each entry from the first layer that has it.
func LayeredSource(layers ...Source) Source {
	seen := map[string]bool{}
	merged := []SourceEntry{}
	for _, layer := range layers {
		for _, e := range layer.Entries() {
			lower := strings.ToLower(e.Path)
			if !seen[lower] {
				seen[lower] = true
				merged = append(merged, e)
			}
		}
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Path < merged[j].Path })
	return &layeredSource{layers: layers, merged: merged}
}

func (s *layeredSource) Kind() SourceKind   { return s.layers[0].Kind() }
func (s *layeredSource) Path() string       { return s.layers[0].Path() }
func (s *layeredSource) Entries() []SourceEntry { return s.merged }
func (s *layeredSource) Close() error {
	var firstErr error
	for _, layer := range s.layers {
		if err := layer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *layeredSource) Open(name string) (io.ReadCloser, error) {
	normalized, err := normalizeArchivePath(name)
	if err != nil {
		return nil, err
	}
	lower := strings.ToLower(normalized)
	for _, layer := range s.layers {
		for _, e := range layer.Entries() {
			if strings.ToLower(e.Path) == lower {
				return layer.Open(normalized)
			}
		}
	}
	return nil, fmt.Errorf("source member %q does not exist", normalized)
}

// SourceConstructionIssues returns issues found during source construction.
func SourceConstructionIssues(src Source) []Issue {
	switch s := src.(type) {
	case *zipSource:
		return s.issues
	case *folderSource:
		return s.issues
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// folder walking helpers (shared by FolderListingFingerprint and folderSource)
// ---------------------------------------------------------------------------

type folderEntry struct {
	path    string
	size    int64
	modTime time.Time
	isDir   bool
}

func walkFolderListing(ctx context.Context, root string) ([]folderEntry, error) {
	entries, _, err := walkFolderListingWithIssues(ctx, root)
	return entries, err
}

func walkFolderListingWithIssues(ctx context.Context, root string) ([]folderEntry, []Issue, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	entries := make([]folderEntry, 0, 256)
	issues := []Issue{}
	err = filepath.WalkDir(root, func(fullPath string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if fullPath == root {
			return nil
		}
		relative, relErr := filepath.Rel(root, fullPath)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if relative == ".git" || strings.HasPrefix(relative, ".git/") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if isReparseOrSymlink(d) {
			issues = append(issues, Issue{
				Code: "unsafe-path", Severity: SeverityWarning,
				Message: "Skipped symlink or junction; only regular files and directories are included",
				Path: relative,
			})
			if d.IsDir() || d.Type()&fs.ModeDir != 0 {
				return fs.SkipDir
			}
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		if len(entries) >= maxEntries {
			return fmt.Errorf("folder has more than %d entries; limit exceeded", maxEntries)
		}
		entry := folderEntry{path: relative, isDir: info.IsDir(), modTime: info.ModTime().UTC()}
		if !info.IsDir() {
			entry.size = info.Size()
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return entries, issues, nil
}

func isReparseOrSymlink(d fs.DirEntry) bool {
	if d.Type()&fs.ModeSymlink != 0 {
		return true
	}
	return isReparsePoint(d)
}

func safeFolderJoin(root, memberPath string) (string, error) {
	normalized, err := normalizeArchivePath(filepath.ToSlash(memberPath))
	if err != nil {
		return "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	candidate, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(normalized)))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes folder root: %s", memberPath)
	}
	return candidate, nil
}
