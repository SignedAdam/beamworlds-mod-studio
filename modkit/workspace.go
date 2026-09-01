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
	"sort"
	"strings"
	"time"
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
	sourceFingerprint, err := FullSHA256(ctx, sourceArchive)
	if err != nil {
		return WorkspaceManifest{}, fmt.Errorf("fingerprint source ZIP: %w", err)
	}
	reader, err := zip.OpenReader(sourceArchive)
	if err != nil {
		return WorkspaceManifest{}, fmt.Errorf("open source ZIP: %w", err)
	}
	defer reader.Close()
	if len(reader.File) > maxEntries {
		return WorkspaceManifest{}, fmt.Errorf("archive has %d entries; workspace limit is %d", len(reader.File), maxEntries)
	}
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

	snapshots := make([]FileSnapshot, 0, len(reader.File))
	var extracted int64
	for _, entry := range reader.File {
		if err := ctx.Err(); err != nil {
			return WorkspaceManifest{}, err
		}
		name, pathErr := normalizeArchivePath(entry.Name)
		if pathErr != nil {
			return WorkspaceManifest{}, pathErr
		}
		destinationPath, pathErr := safeJoin(filesRoot, name)
		if pathErr != nil {
			return WorkspaceManifest{}, pathErr
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(destinationPath, 0o755); err != nil {
				return WorkspaceManifest{}, err
			}
			continue
		}
		if entry.Flags&0x1 != 0 {
			return WorkspaceManifest{}, fmt.Errorf("cannot create workspace from encrypted entry: %s", name)
		}
		if entry.UncompressedSize64 > uint64(maxWorkspaceFile) {
			return WorkspaceManifest{}, fmt.Errorf("entry %s exceeds workspace file limit", name)
		}
		extracted += int64(entry.UncompressedSize64)
		if extracted > maxWorkspaceBytes {
			return WorkspaceManifest{}, fmt.Errorf("workspace exceeds %d-byte extraction limit", maxWorkspaceBytes)
		}
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
			return WorkspaceManifest{}, err
		}
		input, err := entry.Open()
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
		if written != int64(entry.UncompressedSize64) || written > maxWorkspaceFile {
			return WorkspaceManifest{}, fmt.Errorf("entry size mismatch for %s", name)
		}
		if !entry.Modified.IsZero() {
			_ = os.Chtimes(destinationPath, entry.Modified, entry.Modified)
		}
		snapshots = append(snapshots, FileSnapshot{Path: name, SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: written, ModifiedNS: entry.Modified.UnixNano()})
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Path < snapshots[j].Path })
	manifest := WorkspaceManifest{
		ID: id, EntityID: entityID, ArtifactID: artifactID, CreatedAt: time.Now().UTC(), Kind: kind, Files: snapshots,
	}
	afterFingerprint, err := FullSHA256(ctx, sourceArchive)
	if err != nil {
		return WorkspaceManifest{}, err
	}
	if !strings.EqualFold(sourceFingerprint, afterFingerprint) {
		return WorkspaceManifest{}, errors.New("source archive changed while creating the workspace")
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
	return listWorkspaceFiles(filesRoot, true)
}

func ListWorkspaceFileInfo(filesRoot string) ([]FileSnapshot, error) {
	return listWorkspaceFiles(filesRoot, false)
}
func ListWorkspaceDirectories(filesRoot string) ([]string, error) {
	result := []string{}
	err := filepath.WalkDir(filesRoot, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() || filename == filesRoot {
			return nil
		}
		relative, err := filepath.Rel(filesRoot, filename)
		if err != nil {
			return err
		}
		result = append(result, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(result)
	return result, nil
}

func listWorkspaceFiles(filesRoot string, includeHashes bool) ([]FileSnapshot, error) {
	result := []FileSnapshot{}
	err := filepath.WalkDir(filesRoot, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(filesRoot, filename)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		hash := ""
		if includeHashes {
			hash, err = hashFile(filename)
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

func ReadWorkspaceText(filesRoot, relativePath string) (string, error) {
	filename, err := safeJoin(filesRoot, relativePath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(filename)
	if err != nil {
		return "", err
	}
	if info.Size() > maxEditorBytes {
		return "", fmt.Errorf("file is %d bytes; editor limit is %d", info.Size(), maxEditorBytes)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	if !isText(data) {
		return "", fmt.Errorf("file is binary")
	}
	return string(data), nil
}

func WriteWorkspaceText(filesRoot, relativePath, content string) error {
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
	if _, err := temporary.WriteString(content); err != nil {
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
	return os.Rename(temporaryName, filename)
}

func ReplaceWorkspaceText(filesRoot, relativePath, oldText, newText string, all bool) (int, error) {
	content, err := ReadWorkspaceText(filesRoot, relativePath)
	if err != nil {
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
	if err := WriteWorkspaceText(filesRoot, relativePath, strings.Replace(content, oldText, newText, limit)); err != nil {
		return 0, err
	}
	if all {
		return count, nil
	}
	return 1, nil
}

func DiffWorkspace(sourceArchive, filesRoot string, baseline []FileSnapshot) ([]WorkspaceChange, error) {
	current, err := ListWorkspaceFiles(filesRoot)
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
		newItem, exists := after[key]
		if !exists {
			change := WorkspaceChange{Path: oldItem.Path, Type: "deleted", BeforeSHA: oldItem.SHA256, SizeBytes: oldItem.SizeBytes}
			if oldItem.SizeBytes <= maxDiffBytes {
				if oldText, oldErr := readArchiveText(sourceArchive, oldItem.Path, maxDiffBytes); oldErr == nil {
					change.TextDiff = semanticTextDiff(differ, oldItem.Path, oldText, "")
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
			oldText, oldErr := readArchiveText(sourceArchive, oldItem.Path, maxDiffBytes)
			newText, newErr := ReadWorkspaceText(filesRoot, newItem.Path)
			if oldErr == nil && newErr == nil {
				change.TextDiff = semanticTextDiff(differ, newItem.Path, oldText, newText)
			}
		}
		changes = append(changes, change)
	}
	for key, newItem := range after {
		if _, exists := before[key]; exists {
			continue
		}
		change := WorkspaceChange{Path: newItem.Path, Type: "added", AfterSHA: newItem.SHA256, SizeBytes: newItem.SizeBytes}
		if newItem.SizeBytes <= maxDiffBytes {
			if newText, newErr := ReadWorkspaceText(filesRoot, newItem.Path); newErr == nil {
				change.TextDiff = semanticTextDiff(differ, newItem.Path, "", newText)
			}
		}
		changes = append(changes, change)
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
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
	if _, err := os.Stat(outputPath); err == nil {
		return ExportResult{}, fmt.Errorf("export already exists: %s", outputPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ExportResult{}, err
	}
	current, err := ListWorkspaceFiles(filesRoot)
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
	reader, err := zip.OpenReader(sourceArchive)
	if err != nil {
		return ExportResult{}, err
	}
	defer reader.Close()
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
	for _, source := range reader.File {
		if err := ctx.Err(); err != nil {
			_ = writer.Close()
			return ExportResult{}, err
		}
		name, pathErr := normalizeArchivePath(source.Name)
		if pathErr != nil {
			_ = writer.Close()
			return ExportResult{}, pathErr
		}
		key := strings.ToLower(name)
		if source.FileInfo().IsDir() {
			if err := writer.Copy(source); err != nil {
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
			if err := writer.Copy(source); err != nil {
				_ = writer.Close()
				return ExportResult{}, err
			}
		} else if err := writeWorkspaceEntry(writer, source.FileHeader, filesRoot, currentItem.Path); err != nil {
			_ = writer.Close()
			return ExportResult{}, err
		}
		written[key] = true
		entryCount++
	}
	added := []FileSnapshot{}
	for key, item := range currentByLower {
		if !written[key] && baselineByLower[key].SHA256 == "" {
			added = append(added, item)
		}
	}
	sort.Slice(added, func(i, j int) bool { return added[i].Path < added[j].Path })
	for _, item := range added {
		header := zip.FileHeader{Name: item.Path, Method: zip.Deflate, Modified: time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)}
		if err := writeWorkspaceEntry(writer, header, filesRoot, item.Path); err != nil {
			_ = writer.Close()
			return ExportResult{}, err
		}
		entryCount++
	}
	if err := writer.Close(); err != nil {
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
	_, err = io.Copy(output, input)
	return err
}

func readArchiveText(archivePath, memberPath string, limit int64) (string, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	for _, file := range reader.File {
		name, pathErr := normalizeArchivePath(file.Name)
		if pathErr != nil || !strings.EqualFold(name, memberPath) {
			continue
		}
		data, err := readZipEntry(file, limit)
		if err != nil {
			return "", err
		}
		if !isText(data) {
			return "", fmt.Errorf("file is binary")
		}
		return string(data), nil
	}
	return "", os.ErrNotExist
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
	file, err := os.Open(filename)
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

func isText(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	limit := len(data)
	if limit > 8_192 {
		limit = 8_192
	}
	for _, value := range data[:limit] {
		if value == 0 {
			return false
		}
	}
	return true
}

func SearchWorkspace(filesRoot, query string, maxResults int) ([]string, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if maxResults <= 0 || maxResults > 500 {
		maxResults = 200
	}
	results := []string{}
	errStop := errors.New("result limit reached")
	err := filepath.WalkDir(filesRoot, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
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
		relative, _ := filepath.Rel(filesRoot, filename)
		scanErr := func() error {
			defer file.Close()
			scanner := bufio.NewScanner(io.LimitReader(file, maxEditorBytes+1))
			scanner.Buffer(make([]byte, 64*1024), int(maxEditorBytes))
			line := 0
			for scanner.Scan() {
				line++
				text := scanner.Text()
				if strings.Contains(strings.ToLower(text), query) {
					results = append(results, fmt.Sprintf("%s:%d:%s", filepath.ToSlash(relative), line, searchSnippet(text, query)))
					if len(results) >= maxResults {
						return errStop
					}
				}
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

func searchSnippet(text, query string) string {
	const (
		maxBytes     = 480
		leadingBytes = 160
	)
	text = strings.TrimSpace(text)
	if len(text) <= maxBytes {
		return text
	}
	match := strings.Index(strings.ToLower(text), query)
	if match < 0 {
		match = 0
	}
	start := max(match-leadingBytes, 0)
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
