package modkit

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/sergi/go-diff/diffmatchpatch"
)

const (
	maxWorkspaceBytes = int64(20) << 30
	maxWorkspaceFile  = int64(8) << 30
	maxEditorBytes    = int64(4) << 20
	maxDiffBytes      = int64(1) << 20
)

type ExportResult struct {
	Path        string    `json:"path"`
	SHA256      string    `json:"sha256"`
	SizeBytes   int64     `json:"sizeBytes"`
	CreatedAt   time.Time `json:"createdAt"`
	EntryCount  int       `json:"entryCount"`
	Fingerprint string    `json:"fingerprint"`
}

func CreateWorkspace(ctx context.Context, sourceArchive, destination, id, entityID, artifactID string, kind Kind) (WorkspaceManifest, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sourceFingerprint, err := SourceContentID(ctx, sourceArchive)
	if err != nil {
		return WorkspaceManifest{}, fmt.Errorf("fingerprint source: %w", err)
	}
	src, err := OpenSource(ctx, sourceArchive)
	if err != nil {
		return WorkspaceManifest{}, fmt.Errorf("open source: %w", err)
	}
	defer src.Close()
	// Reject sources with unsafe paths (e.g. ../escape.txt in a ZIP).
	if issues := SourceConstructionIssues(src); len(issues) > 0 {
		for _, issue := range issues {
			if issue.Code == "unsafe-path" && issue.Severity == SeverityError {
				return WorkspaceManifest{}, fmt.Errorf("source contains unsafe path: %s", issue.Path)
			}
		}
	}
	entries := src.Entries()
	if _, err := os.Stat(destination); err == nil {
		return WorkspaceManifest{}, fmt.Errorf("workspace already exists: %s", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return WorkspaceManifest{}, err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return WorkspaceManifest{}, err
	}
	temporary, err := os.MkdirTemp(parent, ".workspace-*")
	if err != nil {
		return WorkspaceManifest{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(temporary)
		}
	}()
	filesRoot := filepath.Join(temporary, "files")
	if err := os.MkdirAll(filesRoot, 0o755); err != nil {
		return WorkspaceManifest{}, err
	}
	snapshots := make([]FileSnapshot, 0, len(entries))
	var extracted int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return WorkspaceManifest{}, err
		}
		destinationPath, pathErr := safeJoin(filesRoot, entry.Path)
		if pathErr != nil {
			return WorkspaceManifest{}, pathErr
		}
		if entry.Dir {
			if err := os.MkdirAll(destinationPath, 0o755); err != nil {
				return WorkspaceManifest{}, err
			}
			continue
		}
		if entry.Flags&0x1 != 0 {
			return WorkspaceManifest{}, fmt.Errorf("cannot create workspace from encrypted entry: %s", entry.Path)
		}
		if entry.Size > maxWorkspaceFile {
			return WorkspaceManifest{}, fmt.Errorf("entry %s exceeds workspace file limit", entry.Path)
		}
		extracted += entry.Size
		if extracted > maxWorkspaceBytes {
			return WorkspaceManifest{}, fmt.Errorf("workspace exceeds %d-byte extraction limit", maxWorkspaceBytes)
		}
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
			return WorkspaceManifest{}, err
		}
		input, err := src.Open(entry.Path)
		if err != nil {
			return WorkspaceManifest{}, err
		}
		output, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			_ = input.Close()
			return WorkspaceManifest{}, err
		}
		hash := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, maxWorkspaceFile+1))
		closeOutErr := output.Close()
		closeInErr := input.Close()
		if copyErr != nil {
			return WorkspaceManifest{}, copyErr
		}
		if closeOutErr != nil {
			return WorkspaceManifest{}, closeOutErr
		}
		if closeInErr != nil {
			return WorkspaceManifest{}, closeInErr
		}
		if written > maxWorkspaceFile {
			return WorkspaceManifest{}, fmt.Errorf("entry size limit exceeded for %s", entry.Path)
		}
		if !entry.ModifiedAt.IsZero() {
			_ = os.Chtimes(destinationPath, entry.ModifiedAt, entry.ModifiedAt)
		}
		snapshots = append(snapshots, FileSnapshot{Path: entry.Path, SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: written, ModifiedNS: entry.ModifiedAt.UnixNano()})
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Path < snapshots[j].Path })
	manifest := WorkspaceManifest{
		ID: id, EntityID: entityID, ArtifactID: artifactID, CreatedAt: time.Now().UTC(), Kind: kind, Files: snapshots,
	}
	afterFingerprint, err := SourceContentID(ctx, sourceArchive)
	if err != nil {
		return WorkspaceManifest{}, err
	}
	if sourceFingerprint != afterFingerprint {
		return WorkspaceManifest{}, errors.New("source changed while creating the workspace")
	}
	manifest.SourceFingerprint = sourceFingerprint
	metadataDir := filepath.Join(temporary, ".modstudio")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		return WorkspaceManifest{}, err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return WorkspaceManifest{}, err
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "workspace.json"), encoded, 0o644); err != nil {
		return WorkspaceManifest{}, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return WorkspaceManifest{}, err
	}
	keep = true
	return manifest, nil
}

func ReadWorkspaceManifest(workspaceRoot string) (WorkspaceManifest, error) {
	data, err := os.ReadFile(filepath.Join(workspaceRoot, ".modstudio", "workspace.json"))
	if err != nil {
		return WorkspaceManifest{}, err
	}
	var manifest WorkspaceManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return WorkspaceManifest{}, err
	}
	return manifest, nil
}

func ListWorkspaceFiles(filesRoot string) ([]FileSnapshot, error) {
	return ListWorkspaceFilesContext(context.Background(), filesRoot)
}

func ListWorkspaceFilesContext(ctx context.Context, filesRoot string) ([]FileSnapshot, error) {
	return listWorkspaceFiles(ctx, filesRoot, true)
}

func ListWorkspaceFileInfo(filesRoot string) ([]FileSnapshot, error) {
	return ListWorkspaceFileInfoContext(context.Background(), filesRoot)
}

func ListWorkspaceFileInfoContext(ctx context.Context, filesRoot string) ([]FileSnapshot, error) {
	return listWorkspaceFiles(ctx, filesRoot, false)
}

func ListWorkspaceDirectories(filesRoot string) ([]string, error) {
	return ListWorkspaceDirectoriesContext(context.Background(), filesRoot)
}

func ListWorkspaceDirectoriesContext(ctx context.Context, filesRoot string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result := []string{}
	err := filepath.WalkDir(filesRoot, func(filename string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if filename == filesRoot {
			return nil
		}
		relative, err := filepath.Rel(filesRoot, filename)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if isWorkspaceGitMetadataPath(relative) {
			return fs.SkipDir
		}
		result = append(result, relative)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(result)
	return result, nil
}

func isWorkspaceGitMetadataPath(relativePath string) bool {
	normalized := filepath.ToSlash(filepath.Clean(relativePath))
	return normalized == ".git" || strings.HasPrefix(normalized, ".git/")
}

func listWorkspaceFiles(ctx context.Context, filesRoot string, includeHashes bool) ([]FileSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result := []FileSnapshot{}
	err := filepath.WalkDir(filesRoot, func(filename string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if filename == filesRoot {
				return nil
			}
			relative, err := filepath.Rel(filesRoot, filename)
			if err != nil {
				return err
			}
			if isWorkspaceGitMetadataPath(relative) {
				return fs.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(filesRoot, filename)
		if err != nil {
			return err
		}
		if isWorkspaceGitMetadataPath(relative) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		hash := ""
		if includeHashes {
			hash, err = hashFileContext(ctx, filename)
			if err != nil {
				return err
			}
		}
		result = append(result, FileSnapshot{Path: filepath.ToSlash(relative), SHA256: hash, SizeBytes: info.Size(), ModifiedNS: info.ModTime().UnixNano()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if reader.ctx != nil {
		if err := reader.ctx.Err(); err != nil {
			return 0, err
		}
	}
	count, err := reader.reader.Read(buffer)
	if reader.ctx != nil {
		if contextErr := reader.ctx.Err(); contextErr != nil {
			return count, contextErr
		}
	}
	return count, err
}

func hashFileContext(ctx context.Context, filename string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 4<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if _, err := hash.Write(buffer[:count]); err != nil {
				return "", err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func ReadWorkspaceText(filesRoot, relativePath string) (string, error) {
	return ReadWorkspaceTextContext(context.Background(), filesRoot, relativePath)
}

func ReadWorkspaceTextContext(ctx context.Context, filesRoot, relativePath string) (string, error) {
	data, err := readWorkspaceLimited(ctx, filesRoot, relativePath, maxEditorBytes, "editor limit")
	if err != nil {
		return "", err
	}
	if !isText(data) {
		return "", fmt.Errorf("file is binary")
	}
	return string(data), nil
}

// ReadWorkspaceBytesContext reads a regular workspace file of at most limit bytes.
func ReadWorkspaceBytesContext(ctx context.Context, filesRoot, relativePath string, limit int64) ([]byte, error) {
	return readWorkspaceLimited(ctx, filesRoot, relativePath, limit, "limit")
}

func readWorkspaceLimited(ctx context.Context, filesRoot, relativePath string, limit int64, limitName string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	filename, err := safeJoin(filesRoot, relativePath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", relativePath)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("file is %d bytes; %s is %d", info.Size(), limitName, limit)
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: file}, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file is %d bytes; %s is %d", len(data), limitName, limit)
	}
	return data, nil
}

func WriteWorkspaceText(filesRoot, relativePath, content string) error {
	return WriteWorkspaceTextContext(context.Background(), filesRoot, relativePath, content)
}

func WriteWorkspaceTextContext(ctx context.Context, filesRoot, relativePath, content string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if int64(len(content)) > maxEditorBytes {
		return fmt.Errorf("content exceeds %d-byte editor limit", maxEditorBytes)
	}
	filename, err := safeJoin(filesRoot, relativePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(filename), ".edit-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := io.Copy(temporary, contextReader{ctx: ctx, reader: strings.NewReader(content)}); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(temporaryName, filename)
}

func ReplaceWorkspaceText(filesRoot, relativePath, oldText, newText string, all bool) (int, error) {
	return ReplaceWorkspaceTextContext(context.Background(), filesRoot, relativePath, oldText, newText, all)
}

func ReplaceWorkspaceTextContext(ctx context.Context, filesRoot, relativePath, oldText, newText string, all bool) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	content, err := ReadWorkspaceTextContext(ctx, filesRoot, relativePath)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	count := strings.Count(content, oldText)
	if count == 0 {
		return 0, fmt.Errorf("text to replace was not found")
	}
	if !all && count != 1 {
		return 0, fmt.Errorf("text occurs %d times; request replace-all or provide more context", count)
	}
	limit := 1
	if all {
		limit = -1
	}
	replacement := strings.Replace(content, oldText, newText, limit)
	if err := WriteWorkspaceTextContext(ctx, filesRoot, relativePath, replacement); err != nil {
		return 0, err
	}
	if all {
		return count, nil
	}
	return 1, nil
}

func DiffWorkspace(sourceArchive, filesRoot string, baseline []FileSnapshot) ([]WorkspaceChange, error) {
	return DiffWorkspaceContext(context.Background(), sourceArchive, filesRoot, baseline)
}

func DiffWorkspaceContext(ctx context.Context, sourceArchive, filesRoot string, baseline []FileSnapshot) ([]WorkspaceChange, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	src, err := OpenSource(ctx, sourceArchive)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	return DiffWorkspaceSource(ctx, src, filesRoot, baseline)
}

func semanticTextDiff(differ *diffmatchpatch.DiffMatchPatch, relativePath, before, after string) string {
	const (
		contextLines = 3
		maxDiffLine  = 4 << 10
		maxOutput    = 256 << 10
	)
	beforeLabel := "a/" + relativePath
	afterLabel := "b/" + relativePath
	if before == "" {
		beforeLabel = "/dev/null"
	}
	if after == "" {
		afterLabel = "/dev/null"
	}
	var builder strings.Builder
	builder.WriteString("--- " + beforeLabel + "\n")
	builder.WriteString("+++ " + afterLabel + "\n")
	writeLine := func(prefix, line string) bool {
		if len(line) > maxDiffLine {
			end := maxDiffLine
			for end > 0 && !utf8.RuneStart(line[end]) {
				end--
			}
			line = line[:end] + "… [line truncated]"
		}
		rendered := prefix + line + "\n"
		if builder.Len()+len(rendered) > maxOutput {
			marker := " … diff truncated …\n"
			if builder.Len()+len(marker) <= maxOutput {
				builder.WriteString(marker)
			}
			return false
		}
		builder.WriteString(rendered)
		return true
	}
	beforeLines, afterLines, lineIndex := differ.DiffLinesToChars(before, after)
	diffs := differ.DiffMain(beforeLines, afterLines, false)
	diffs = differ.DiffCharsToLines(diffs, lineIndex)
	for index, diff := range diffs {
		lines := strings.Split(diff.Text, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) == 0 {
			continue
		}
		prefix := " "
		if diff.Type == diffmatchpatch.DiffDelete {
			prefix = "-"
		} else if diff.Type == diffmatchpatch.DiffInsert {
			prefix = "+"
		}
		if diff.Type != diffmatchpatch.DiffEqual || len(lines) <= contextLines*2 {
			for _, line := range lines {
				if !writeLine(prefix, line) {
					return builder.String()
				}
			}
			continue
		}
		if index == 0 {
			if !writeLine(" ", "… unchanged lines …") {
				return builder.String()
			}
			lines = lines[len(lines)-contextLines:]
		} else if index == len(diffs)-1 {
			lines = lines[:contextLines]
		} else {
			for _, line := range lines[:contextLines] {
				if !writeLine(" ", line) {
					return builder.String()
				}
			}
			if !writeLine(" ", "… unchanged lines …") {
				return builder.String()
			}
			lines = lines[len(lines)-contextLines:]
		}
		for _, line := range lines {
			if !writeLine(" ", line) {
				return builder.String()
			}
		}
		if index == len(diffs)-1 && !writeLine(" ", "… unchanged lines …") {
			return builder.String()
		}
	}
	return builder.String()
}

func ExportWorkspace(ctx context.Context, sourceArchive, filesRoot, outputPath string, baseline []FileSnapshot) (ExportResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ExportResult{}, err
	}
	if _, err := os.Stat(outputPath); err == nil {
		return ExportResult{}, fmt.Errorf("export already exists: %s", outputPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ExportResult{}, err
	}
	current, err := ListWorkspaceFilesContext(ctx, filesRoot)
	if err != nil {
		return ExportResult{}, err
	}
	currentByLower := make(map[string]FileSnapshot, len(current))
	baselineByLower := make(map[string]FileSnapshot, len(baseline))
	for _, item := range current {
		currentByLower[strings.ToLower(item.Path)] = item
	}
	for _, item := range baseline {
		baselineByLower[strings.ToLower(item.Path)] = item
	}

	// Open source for ZIP raw-copy optimisation.
	src, srcErr := OpenSource(ctx, sourceArchive)
	if srcErr != nil {
		return ExportResult{}, srcErr
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return ExportResult{}, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(outputPath), ".export-*.zip")
	if err != nil {
		return ExportResult{}, err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	writer := zip.NewWriter(temporary)
	written := map[string]bool{}
	entryCount := 0
	for _, entry := range src.Entries() {
		if err := ctx.Err(); err != nil {
			_ = writer.Close()
			return ExportResult{}, err
		}
		key := strings.ToLower(entry.Path)
		if entry.Dir {
			// ZIP raw-copy for directory entries when available.
			if zs, ok := src.(*zipSource); ok {
				if rawFile := zs.rawZipFile(entry.Path); rawFile != nil {
					if err := writer.Copy(rawFile); err != nil {
						_ = writer.Close()
						return ExportResult{}, err
					}
					entryCount++
					continue
				}
			}
			header := &zip.FileHeader{Name: entry.Path + "/", Modified: entry.ModifiedAt}
			if _, err := writer.CreateHeader(header); err != nil {
				_ = writer.Close()
				return ExportResult{}, err
			}
			entryCount++
			continue
		}
		currentItem, exists := currentByLower[key]
		if !exists {
			continue
		}
		baselineItem := baselineByLower[key]
		if baselineItem.SHA256 != "" && baselineItem.SHA256 == currentItem.SHA256 {
			// Unchanged: raw-copy from ZIP source if possible.
			if zs, ok := src.(*zipSource); ok {
				if rawFile := zs.rawZipFile(entry.Path); rawFile != nil {
					if err := copyArchiveEntryContext(ctx, writer, rawFile); err != nil {
						_ = writer.Close()
						return ExportResult{}, err
					}
					written[key] = true
					entryCount++
					continue
				}
			}
			// Folder or no raw file: write from workspace (unchanged file is identical).
			if err := writeWorkspaceEntryContext(ctx, writer, zip.FileHeader{Name: entry.Path, Method: zip.Deflate, Modified: entry.ModifiedAt}, filesRoot, currentItem.Path); err != nil {
				_ = writer.Close()
				return ExportResult{}, err
			}
		} else if err := writeWorkspaceEntryContext(ctx, writer, zip.FileHeader{Name: entry.Path, Method: zip.Deflate, Modified: entry.ModifiedAt}, filesRoot, currentItem.Path); err != nil {
			_ = writer.Close()
			return ExportResult{}, err
		}
		written[key] = true
		entryCount++
	}
	added := []FileSnapshot{}
	for key, item := range currentByLower {
		if err := ctx.Err(); err != nil {
			_ = writer.Close()
			return ExportResult{}, err
		}
		if !written[key] && baselineByLower[key].SHA256 == "" {
			added = append(added, item)
		}
	}
	sort.Slice(added, func(i, j int) bool { return added[i].Path < added[j].Path })
	for _, item := range added {
		if err := ctx.Err(); err != nil {
			_ = writer.Close()
			return ExportResult{}, err
		}
		header := zip.FileHeader{Name: item.Path, Method: zip.Deflate, Modified: time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)}
		if err := writeWorkspaceEntryContext(ctx, writer, header, filesRoot, item.Path); err != nil {
			_ = writer.Close()
			return ExportResult{}, err
		}
		entryCount++
	}
	if err := writer.Close(); err != nil {
		return ExportResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ExportResult{}, err
	}
	if err := temporary.Sync(); err != nil {
		return ExportResult{}, err
	}
	if err := temporary.Close(); err != nil {
		return ExportResult{}, err
	}
	verification, err := Inspect(ctx, temporaryPath)
	if err != nil {
		return ExportResult{}, fmt.Errorf("verify export: %w", err)
	}
	if !verification.ValidArchive {
		return ExportResult{}, fmt.Errorf("export verification reported an invalid archive")
	}
	hash, err := FullSHA256(ctx, temporaryPath)
	if err != nil {
		return ExportResult{}, err
	}
	info, err := os.Stat(temporaryPath)
	if err != nil {
		return ExportResult{}, err
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return ExportResult{}, err
	}
	keep = true
	return ExportResult{Path: outputPath, SHA256: hash, SizeBytes: info.Size(), CreatedAt: time.Now().UTC(), EntryCount: entryCount, Fingerprint: verification.CentralFingerprint}, nil
}

func writeWorkspaceEntry(writer *zip.Writer, original zip.FileHeader, filesRoot, relativePath string) error {
	return writeWorkspaceEntryContext(context.Background(), writer, original, filesRoot, relativePath)
}

func writeWorkspaceEntryContext(ctx context.Context, writer *zip.Writer, original zip.FileHeader, filesRoot, relativePath string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	filename, err := safeJoin(filesRoot, relativePath)
	if err != nil {
		return err
	}
	input, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer input.Close()
	header := original
	header.Name = filepath.ToSlash(relativePath)
	header.Flags &^= 0x8
	header.CRC32 = 0
	header.CompressedSize = 0
	header.CompressedSize64 = 0
	header.UncompressedSize = 0
	header.UncompressedSize64 = 0
	if header.Method != zip.Store && header.Method != zip.Deflate {
		header.Method = zip.Deflate
	}
	output, err := writer.CreateHeader(&header)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, contextReader{ctx: ctx, reader: input})
	return err
}

func copyArchiveEntryContext(ctx context.Context, writer *zip.Writer, source *zip.File) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if source.FileInfo().IsDir() {
		return writer.Copy(source)
	}
	input, err := openZipEntry(source)
	if err != nil {
		return err
	}
	defer input.Close()
	header := source.FileHeader
	output, err := writer.CreateHeader(&header)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, contextReader{ctx: ctx, reader: input})
	return err
}

func readArchiveText(archivePath, memberPath string, limit int64) (string, error) {
	return readArchiveTextContext(context.Background(), archivePath, memberPath, limit)
}

func readArchiveTextContext(ctx context.Context, archivePath, memberPath string, limit int64) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	src, err := OpenSource(ctx, archivePath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	return readSourceText(ctx, src, memberPath, limit)
}

func safeJoin(root, relativePath string) (string, error) {
	normalized, err := normalizeArchivePath(filepath.ToSlash(relativePath))
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
		return "", fmt.Errorf("path escapes workspace: %s", relativePath)
	}
	return candidate, nil
}

func hashFile(filename string) (string, error) {
	return hashFileContext(context.Background(), filename)
}

func isText(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	limit := len(data)
	if limit > 8_192 {
		limit = 8_192
	}
	if !utf8.Valid(data[:limit]) {
		return false
	}
	for _, value := range data[:limit] {
		if value == 0 ||
			(value < 0x20 &&
				value != '\t' &&
				value != '\n' &&
				value != '\r' &&
				value != '\f') ||
			value == 0x7f {
			return false
		}
	}
	return true
}

type WorkspaceSearchMatch struct {
	RelativePath string `json:"relativePath"`
	Line         int    `json:"line"`
	// Column is a one-based UTF-16 code-unit column, matching CodeMirror positions.
	Column int `json:"column"`
	// MatchLength is the UTF-16 code-unit length of the first match on the line.
	MatchLength int    `json:"matchLength"`
	Preview     string `json:"preview"`
}

func utf16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func formatWorkspaceSearchMatches(matches []WorkspaceSearchMatch) []string {
	results := make([]string, len(matches))
	for index, match := range matches {
		results[index] = fmt.Sprintf(
			"%s:%d:%s",
			match.RelativePath,
			match.Line,
			match.Preview,
		)
	}
	return results
}

func SearchWorkspace(filesRoot, query string, maxResults int) ([]string, error) {
	return SearchWorkspaceWithOptions(filesRoot, query, maxResults, false, false)
}

func SearchWorkspaceContext(ctx context.Context, filesRoot, query string, maxResults int) ([]string, error) {
	return SearchWorkspaceWithOptionsContext(ctx, filesRoot, query, maxResults, false, false)
}

func SearchWorkspaceWithOptions(filesRoot, query string, maxResults int, regex, caseSensitive bool) ([]string, error) {
	return SearchWorkspaceWithOptionsContext(context.Background(), filesRoot, query, maxResults, regex, caseSensitive)
}

func SearchWorkspaceWithOptionsContext(ctx context.Context, filesRoot, query string, maxResults int, regex, caseSensitive bool) ([]string, error) {
	matches, err := SearchWorkspaceMatchesContext(
		ctx,
		filesRoot,
		query,
		maxResults,
		regex,
		caseSensitive,
	)
	if err != nil {
		return nil, err
	}
	return formatWorkspaceSearchMatches(matches), nil
}

func SearchWorkspaceMatches(filesRoot, query string, maxResults int, regex, caseSensitive bool) ([]WorkspaceSearchMatch, error) {
	return SearchWorkspaceMatchesContext(
		context.Background(),
		filesRoot,
		query,
		maxResults,
		regex,
		caseSensitive,
	)
}

func SearchWorkspaceMatchesContext(ctx context.Context, filesRoot, query string, maxResults int, regex, caseSensitive bool) ([]WorkspaceSearchMatch, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if maxResults <= 0 || maxResults > 500 {
		maxResults = 200
	}

	patternQuery := query
	if !regex {
		patternQuery = regexp.QuoteMeta(patternQuery)
	}
	if !caseSensitive {
		patternQuery = "(?i)" + patternQuery
	}
	pattern, err := regexp.Compile(patternQuery)
	if err != nil {
		return nil, fmt.Errorf("invalid search pattern: %w", err)
	}

	results := []WorkspaceSearchMatch{}
	errStop := errors.New("result limit reached")
	err = filepath.WalkDir(filesRoot, func(filename string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(filesRoot, filename)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if isWorkspaceGitMetadataPath(relative) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxEditorBytes {
			return nil
		}
		file, err := os.Open(filename)
		if err != nil {
			return nil
		}
		probe := make([]byte, 8_192)
		probeCount, probeErr := file.Read(probe)
		if probeErr != nil && !errors.Is(probeErr, io.EOF) {
			_ = file.Close()
			return nil
		}
		if !isText(probe[:probeCount]) {
			_ = file.Close()
			return nil
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			_ = file.Close()
			return nil
		}
		scanErr := func() error {
			defer file.Close()
			scanner := bufio.NewScanner(io.LimitReader(contextReader{ctx: ctx, reader: file}, maxEditorBytes+1))
			scanner.Buffer(make([]byte, 64*1024), int(maxEditorBytes))
			line := 0
			for scanner.Scan() {
				if err := ctx.Err(); err != nil {
					return err
				}
				line++
				text := scanner.Text()
				location := pattern.FindStringIndex(text)
				if location == nil {
					continue
				}
				matchIndex, matchEnd := location[0], location[1]
				results = append(results, WorkspaceSearchMatch{
					RelativePath: filepath.ToSlash(relative),
					Line:         line,
					Column:       utf16Length(text[:matchIndex]) + 1,
					MatchLength:  utf16Length(text[matchIndex:matchEnd]),
					Preview:      searchSnippetAt(text, matchIndex),
				})
				if len(results) >= maxResults {
					return errStop
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			return scanner.Err()
		}()
		return scanErr
	})
	if err != nil && !errors.Is(err, errStop) {
		return nil, err
	}
	return results, nil
}

func searchSnippetAt(text string, matchIndex int) string {
	const (
		maxBytes     = 480
		leadingBytes = 160
	)
	leadingTrimmed := len(text) - len(strings.TrimLeft(text, " \t\r\n"))
	text = strings.TrimSpace(text)
	matchIndex -= leadingTrimmed
	if len(text) <= maxBytes {
		return text
	}
	if matchIndex < 0 {
		matchIndex = 0
	}
	if matchIndex > len(text) {
		matchIndex = len(text)
	}
	start := max(matchIndex-leadingBytes, 0)
	end := min(start+maxBytes, len(text))
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	prefix := ""
	if start > 0 {
		prefix = "…"
	}
	suffix := ""
	if end < len(text) {
		suffix = "…"
	}
	return prefix + text[start:end] + suffix
}
