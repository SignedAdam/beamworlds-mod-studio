package modkit

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alchemy/json5"
)

const (
	// MaxArchiveEntries bounds the amount of central-directory metadata that an
	// edit or read operation will inspect.
	MaxArchiveEntries = 100_000

	// MaxArchiveMemberBytes is the largest member any bounded read operation may
	// consume. Archive rewrites stream untouched members and do not load them.
	MaxArchiveMemberBytes int64 = 256 << 20

	// MaxArchiveJSONBytes bounds JSON metadata both before and after editing.
	MaxArchiveJSONBytes int64 = 2 << 20
)

// NormalizeArchivePath validates and canonicalizes a ZIP member path. It is
// intentionally shared by callers that accept member paths from the UI.
func NormalizeArchivePath(value string) (string, error) {
	return normalizeArchivePath(value)
}

// ReadArchiveMember reads one non-directory member, refusing to allocate or
// consume more than limit bytes. A non-positive limit uses the package maximum.
func ReadArchiveMember(archivePath, memberPath string, limit int64) ([]byte, error) {
	data, truncated, err := ReadArchiveMemberLimited(archivePath, memberPath, limit)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("archive member %q exceeds the read limit", memberPath)
	}
	return data, nil
}

// ReadArchiveMemberLimited reads at most limit bytes and reports whether more
// member data exists. It is suitable for bounded previews where a partial text
// result is useful. The archive and requested member path are both validated.
func ReadArchiveMemberLimited(archivePath, memberPath string, limit int64) ([]byte, bool, error) {
	limit = boundedArchiveLimit(limit)
	reader, files, err := openValidatedArchive(archivePath)
	if err != nil {
		return nil, false, err
	}
	defer reader.Close()

	file, err := archiveMember(files, memberPath)
	if err != nil {
		return nil, false, err
	}
	if file.FileInfo().IsDir() {
		return nil, false, fmt.Errorf("archive member %q is a directory", memberPath)
	}
	return readZipMemberLimited(file, limit)
}

// CopyArchiveMember streams one non-directory member into destination while
// enforcing limit. It never reads an unbounded amount into memory.
func CopyArchiveMember(archivePath, memberPath string, destination io.Writer, limit int64) (int64, error) {
	if destination == nil {
		return 0, fmt.Errorf("archive member destination is nil")
	}
	limit = boundedArchiveLimit(limit)
	reader, files, err := openValidatedArchive(archivePath)
	if err != nil {
		return 0, err
	}
	defer reader.Close()

	file, err := archiveMember(files, memberPath)
	if err != nil {
		return 0, err
	}
	if file.FileInfo().IsDir() {
		return 0, fmt.Errorf("archive member %q is a directory", memberPath)
	}
	if file.UncompressedSize64 > uint64(limit) {
		return 0, fmt.Errorf("archive member %q is %d bytes; limit is %d", memberPath, file.UncompressedSize64, limit)
	}
	input, err := openZipEntry(file)
	if err != nil {
		return 0, err
	}
	defer input.Close()

	copied, err := io.CopyN(destination, input, limit+1)
	if err != nil && err != io.EOF {
		return copied, err
	}
	if copied > limit {
		return copied, fmt.Errorf("archive member %q exceeded %d-byte read limit", memberPath, limit)
	}
	return copied, nil
}

// RewriteArchiveJSONMember atomically replaces or creates one JSON metadata
// member. Existing JSON5 is decoded first; updates replace only the supplied
// keys, and a nil update value deletes that key. Untouched entries are copied
// in their raw compressed form and retain their original headers/content.
func RewriteArchiveJSONMember(archivePath, memberPath string, updates map[string]any, createIfMissing bool, beforeReplace func() error) error {
	target, err := NormalizeArchivePath(memberPath)
	if err != nil {
		return err
	}
	if target == "" {
		return fmt.Errorf("archive member path is empty")
	}
	for key := range updates {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("metadata key is empty")
		}
	}

	sourceInfo, err := os.Stat(archivePath)
	if err != nil {
		return fmt.Errorf("stat archive: %w", err)
	}
	if sourceInfo.IsDir() {
		return fmt.Errorf("archive path is a directory: %s", archivePath)
	}

	reader, files, err := openValidatedArchive(archivePath)
	if err != nil {
		return err
	}
	readerClosed := false
	defer func() {
		if !readerClosed {
			_ = reader.Close()
		}
	}()

	targetFile := files[strings.ToLower(target)]
	if targetFile != nil && targetFile.FileInfo().IsDir() {
		return fmt.Errorf("archive member %q is a directory", target)
	}
	if targetFile == nil && !createIfMissing {
		return fmt.Errorf("archive member %q does not exist", target)
	}

	metadata := map[string]any{}
	if targetFile != nil {
		metadata, err = readJSONMember(targetFile)
		if err != nil {
			return fmt.Errorf("read metadata member %q: %w", target, err)
		}
	}
	for key, value := range updates {
		if value == nil {
			delete(metadata, key)
		} else {
			metadata[key] = value
		}
	}
	encoded, err := encodeJSONDocument(metadata)
	if err != nil {
		return fmt.Errorf("encode metadata member %q: %w", target, err)
	}

	directory := filepath.Dir(archivePath)
	prefix := "." + filepath.Base(archivePath) + "."
	temporary, err := os.CreateTemp(directory, prefix+"*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary archive: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	temporaryClosed := false
	defer func() {
		if !temporaryClosed {
			_ = temporary.Close()
		}
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()

	writer := zip.NewWriter(temporary)
	replaced := false
	for _, file := range reader.File {
		name, pathErr := NormalizeArchivePath(file.Name)
		if pathErr != nil {
			return pathErr
		}
		if strings.EqualFold(name, target) {
			if replaced {
				return fmt.Errorf("archive contains duplicate member %q", target)
			}
			replaced = true
			header := file.FileHeader
			if header.Method != zip.Store && header.Method != zip.Deflate {
				header.Method = zip.Deflate
			}
			output, createErr := writer.CreateHeader(&header)
			if createErr != nil {
				return fmt.Errorf("create replacement member %q: %w", target, createErr)
			}
			if _, writeErr := output.Write(encoded); writeErr != nil {
				return fmt.Errorf("write replacement member %q: %w", target, writeErr)
			}
			continue
		}
		if copyErr := writer.Copy(file); copyErr != nil {
			return fmt.Errorf("copy archive member %q: %w", name, copyErr)
		}
	}
	if !replaced {
		header := &zip.FileHeader{Name: target, Method: zip.Deflate, Modified: time.Now().UTC()}
		output, createErr := writer.CreateHeader(header)
		if createErr != nil {
			return fmt.Errorf("create metadata member %q: %w", target, createErr)
		}
		if _, writeErr := output.Write(encoded); writeErr != nil {
			return fmt.Errorf("write metadata member %q: %w", target, writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close rewritten archive: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync rewritten archive: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary archive: %w", err)
	}
	temporaryClosed = true
	if err := reader.Close(); err != nil {
		return fmt.Errorf("close source archive: %w", err)
	}
	readerClosed = true
	if mode := sourceInfo.Mode().Perm(); mode != 0 {
		if err := os.Chmod(temporaryName, mode); err != nil {
			return fmt.Errorf("preserve archive permissions: %w", err)
		}
	}
	currentInfo, err := os.Stat(archivePath)
	if err != nil {
		return fmt.Errorf("revalidate source archive: %w", err)
	}
	if !os.SameFile(sourceInfo, currentInfo) || sourceInfo.Size() != currentInfo.Size() || !sourceInfo.ModTime().Equal(currentInfo.ModTime()) {
		return fmt.Errorf("source archive changed while preparing metadata update")
	}
	if beforeReplace != nil {
		if err := beforeReplace(); err != nil {
			return err
		}
	}
	if err := os.Rename(temporaryName, archivePath); err != nil {
		return fmt.Errorf("replace archive atomically: %w", err)
	}
	removeTemporary = false
	return nil
}

func boundedArchiveLimit(limit int64) int64 {
	if limit <= 0 || limit > MaxArchiveMemberBytes {
		return MaxArchiveMemberBytes
	}
	return limit
}

func openValidatedArchive(archivePath string) (*zip.ReadCloser, map[string]*zip.File, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open ZIP: %w", err)
	}
	if len(reader.File) > MaxArchiveEntries {
		_ = reader.Close()
		return nil, nil, fmt.Errorf("archive has %d entries; limit is %d", len(reader.File), MaxArchiveEntries)
	}
	files := make(map[string]*zip.File, len(reader.File))
	for _, file := range reader.File {
		name, pathErr := NormalizeArchivePath(file.Name)
		if pathErr != nil {
			_ = reader.Close()
			return nil, nil, pathErr
		}
		if file.Flags&0x1 != 0 {
			_ = reader.Close()
			return nil, nil, fmt.Errorf("archive member %q is encrypted", name)
		}
		key := strings.ToLower(name)
		if _, exists := files[key]; exists {
			_ = reader.Close()
			return nil, nil, fmt.Errorf("archive contains duplicate member %q", name)
		}
		files[key] = file
	}
	return reader, files, nil
}

func archiveMember(files map[string]*zip.File, memberPath string) (*zip.File, error) {
	normalized, err := NormalizeArchivePath(memberPath)
	if err != nil {
		return nil, err
	}
	file := files[strings.ToLower(normalized)]
	if file == nil {
		return nil, fmt.Errorf("archive member %q does not exist", normalized)
	}
	return file, nil
}

func readZipMemberLimited(file *zip.File, limit int64) ([]byte, bool, error) {
	input, err := openZipEntry(file)
	if err != nil {
		return nil, false, err
	}
	defer input.Close()
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil {
		return nil, false, err
	}
	truncated := file.UncompressedSize64 > uint64(limit) || int64(len(data)) > limit
	if int64(len(data)) > limit {
		data = data[:limit]
	}
	return data, truncated, nil
}

func readJSONMember(file *zip.File) (map[string]any, error) {
	if file.UncompressedSize64 > uint64(MaxArchiveJSONBytes) {
		return nil, fmt.Errorf("member is %d bytes; limit is %d", file.UncompressedSize64, MaxArchiveJSONBytes)
	}
	data, truncated, err := readZipMemberLimited(file, MaxArchiveJSONBytes)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("member exceeds %d-byte read limit", MaxArchiveJSONBytes)
	}
	return decodeJSONDocument(data)
}

func decodeJSONDocument(data []byte) (map[string]any, error) {
	cleaned := strings.TrimRight(strings.TrimPrefix(string(data), "\ufeff"), "\x00")
	metadata := map[string]any{}
	if err := json5.Unmarshal([]byte(cleaned), &metadata); err != nil {
		return nil, err
	}
	if metadata == nil {
		return nil, fmt.Errorf("JSON metadata root must be an object")
	}
	return metadata, nil
}

// ApplyJSONUpdates edits a JSON5 metadata document the same way
// RewriteArchiveJSONMember edits an archive member: only the supplied keys
// change, and a nil value deletes its key. An empty document starts a new one.
func ApplyJSONUpdates(document []byte, updates map[string]any) ([]byte, error) {
	if int64(len(document)) > MaxArchiveJSONBytes {
		return nil, fmt.Errorf("metadata document is %d bytes; limit is %d", len(document), MaxArchiveJSONBytes)
	}
	metadata := map[string]any{}
	if strings.TrimSpace(string(document)) != "" {
		decoded, err := decodeJSONDocument(document)
		if err != nil {
			return nil, err
		}
		metadata = decoded
	}
	for key, value := range updates {
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("metadata key is empty")
		}
		if value == nil {
			delete(metadata, key)
		} else {
			metadata[key] = value
		}
	}
	return encodeJSONDocument(metadata)
}

func encodeJSONDocument(metadata map[string]any) ([]byte, error) {
	safe, err := jsonSafeValue(metadata)
	if err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(safe, "", "  ")
	if err != nil {
		return nil, err
	}
	if int64(len(encoded))+1 > MaxArchiveJSONBytes {
		return nil, fmt.Errorf("encoded metadata exceeds %d-byte limit", MaxArchiveJSONBytes)
	}
	encoded = append(encoded, '\n')
	return encoded, nil
}

func jsonSafeValue(value any) (any, error) {
	switch typed := value.(type) {
	case json5.Number:
		result, err := typed.Float64()
		if err != nil {
			return nil, fmt.Errorf("invalid JSON5 number %q: %w", typed, err)
		}
		if math.IsNaN(result) || math.IsInf(result, 0) {
			return nil, fmt.Errorf("JSON5 number %q is not valid JSON", typed)
		}
		return result, nil
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return nil, fmt.Errorf("number is not valid JSON")
		}
		return typed, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, fmt.Errorf("number is not valid JSON")
		}
		return typed, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			safe, err := jsonSafeValue(child)
			if err != nil {
				return nil, err
			}
			result[key] = safe
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			safe, err := jsonSafeValue(child)
			if err != nil {
				return nil, err
			}
			result[index] = safe
		}
		return result, nil
	default:
		return typed, nil
	}
}
