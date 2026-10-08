package modkit

import (
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
)

// SourceKind distinguishes unpacked folder mods from ZIP archives.
type SourceKind string

const (
	SourceZIP    SourceKind = "zip"
	SourceFolder SourceKind = "folder"
)

// SourceKindOf reports whether path is a ZIP file or an unpacked folder
// (Lstat; a directory is a folder source).
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

// FolderListingFingerprint hashes the sorted (relative path, size, modified
// time, is-dir) listing of root. It never opens files. It changes whenever any
// file is added, removed, resized or re-saved.
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
	switch kind {
	case SourceFolder:
		fp, err := FolderListingFingerprint(ctx, path)
		if err != nil {
			return "", err
		}
		return "folder:" + fp, nil
	default:
		return FullSHA256(ctx, path)
	}
}

// folderEntry is the metadata of one file or directory in a folder listing.
type folderEntry struct {
	path    string    // forward-slash relative path
	size    int64     // file size (0 for dirs)
	modTime time.Time // modification time
	isDir   bool
}

// walkFolderListing walks root, skipping .git, symlinks/junctions/reparse
// points, and enforcing maxEntries. Returns sorted entries.
func walkFolderListing(ctx context.Context, root string) ([]folderEntry, error) {
	entries, _, err := walkFolderListingWithIssues(ctx, root)
	return entries, err
}

// walkFolderListingWithIssues walks root like walkFolderListing but also
// returns issues for skipped symlinks/junctions.
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

		// Skip the root .git directory.
		if relative == ".git" || strings.HasPrefix(relative, ".git/") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		// Never follow symlinks/junctions/reparse points.
		if isReparseOrSymlink(d) {
			issues = append(issues, Issue{
				Code:     "unsafe-path",
				Severity: SeverityWarning,
				Message:  "Skipped symlink or junction; only regular files and directories are included",
				Path:     relative,
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
		// Also check the resolved info for non-regular files (e.g. devices).
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

// isReparseOrSymlink checks whether a directory entry is a symlink or (on
// Windows) a reparse point such as a junction. Platform-specific.
func isReparseOrSymlink(d fs.DirEntry) bool {
	if d.Type()&fs.ModeSymlink != 0 {
		return true
	}
	return isReparsePoint(d)
}

// readFolderMember reads a file inside root at the given (forward-slash)
// relative path, with the same validation as ZIP member reads.
func readFolderMember(root, memberPath string, limit int64) ([]byte, bool, error) {
	filename, err := safeFolderJoin(root, memberPath)
	if err != nil {
		return nil, false, err
	}
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("folder member %q is not a regular file", memberPath)
	}
	if info.Size() > limit {
		return nil, true, fmt.Errorf("folder member %q is %d bytes; limit is %d", memberPath, info.Size(), limit)
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, false, err
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	return data, truncated, nil
}

// copyFolderMember streams a file inside root into destination.
func copyFolderMember(root, memberPath string, destination io.Writer, limit int64) (int64, error) {
	filename, err := safeFolderJoin(root, memberPath)
	if err != nil {
		return 0, err
	}
	info, err := os.Lstat(filename)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("folder member %q is not a regular file", memberPath)
	}
	if info.Size() > limit {
		return 0, fmt.Errorf("folder member %q is %d bytes; limit is %d", memberPath, info.Size(), limit)
	}
	file, err := os.Open(filename)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	copied, err := io.CopyN(destination, file, limit+1)
	if err != nil && err != io.EOF {
		return copied, err
	}
	if copied > limit {
		return copied, fmt.Errorf("folder member %q exceeded %d-byte read limit", memberPath, limit)
	}
	return copied, nil
}

// safeFolderJoin validates and joins a relative member path under root,
// using the same normalisation and safety rules as ZIP member paths.
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

// readFolderArchiveText reads a text file from a folder source for diff
// purposes, mirroring readArchiveTextContext.
func readFolderArchiveText(ctx context.Context, root, memberPath string, limit int64) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, _, err := readFolderMember(root, memberPath, limit)
	if err != nil {
		return "", err
	}
	if !isText(data) {
		return "", fmt.Errorf("file is binary")
	}
	return string(data), nil
}
