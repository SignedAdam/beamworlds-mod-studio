package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Limits for the log reader.
const (
	// maxLogReadBytes caps total bytes read in one call.
	maxLogReadBytes = int64(4 << 20) // 4 MiB

	// maxLogReadLines caps how many lines are returned.
	maxLogReadLines = 2000

	// maxLogLineBytes truncates any individual line to this length.
	maxLogLineBytes = 2048

	// cursorHeaderBytes is how many bytes of the file header we hash to detect rotation.
	cursorHeaderBytes = 512

	// initialTailBytes is how far from the end we start an initial (cursorless) read.
	initialTailBytes = int64(512 << 10) // 512 KiB

	// contextLinesBefore is how many lines before a diagnostic we keep for stack-trace context.
	contextLinesBefore = 3

	// contextLinesAfter is how many lines after a diagnostic we keep for continuation/stack-trace.
	contextLinesAfter = 6
)

// RuntimeLogReadOptions configures a single readBeamNGRuntimeLog call.
type RuntimeLogReadOptions struct {
	Cursor    string `json:"cursor"`
	MaxLines  int    `json:"maxLines"`
	FromStart bool   `json:"-"`
}

// RuntimeLogReadResult is the bounded result of reading a BeamNG runtime log.
type RuntimeLogReadResult struct {
	LogPath       string              `json:"logPath"`
	Cursor        string              `json:"cursor"`
	Diagnostics   []RuntimeDiagnostic `json:"diagnostics"`
	TotalLines    int                 `json:"totalLines"`
	ReturnedLines int                 `json:"returnedLines"`
	TotalErrors   int                 `json:"totalErrors"`
	TotalWarnings int                 `json:"totalWarnings"`
	BytesRead     int64               `json:"bytesRead"`
	FileSize      int64               `json:"fileSize"`
	FromOffset    int64               `json:"fromOffset"`
	ToOffset      int64               `json:"toOffset"`
	Rotated       bool                `json:"rotated"`
	Truncated     bool                `json:"truncated"`
	Bounded       bool                `json:"bounded"`
	PartialLine   bool                `json:"partialLine"`
	ReadAt        string              `json:"readAt"`
}

// logCursorState is the internal cursor encoded/decoded as an opaque string.
type logCursorState struct {
	Offset     int64  `json:"o"`
	HeaderHash string `json:"h"`
	HeaderLen  int    `json:"n"`
	FileSize   int64  `json:"s"`
	LogPath    string `json:"p"`
}

type classifiedLine struct {
	text      string
	severity  string
	component string
	lineNum   int // 1-based within the read window
}

func encodeLogCursor(state logCursorState) string {
	raw, _ := json.Marshal(state)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeLogCursor(cursor string) (logCursorState, error) {
	if cursor == "" || len(cursor) > 48<<10 {
		return logCursorState{}, errors.New("invalid cursor length")
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return logCursorState{}, fmt.Errorf("invalid cursor encoding: %w", err)
	}
	var state logCursorState
	if err := json.Unmarshal(raw, &state); err != nil {
		return logCursorState{}, fmt.Errorf("invalid cursor data: %w", err)
	}
	if state.Offset < 0 || state.FileSize < 0 || state.Offset > state.FileSize ||
		state.HeaderLen != currentHeaderLen(state.FileSize) {
		return logCursorState{}, errors.New("invalid cursor bounds")
	}
	return state, nil
}

// hashFileHeader hashes the first headerLen bytes of the file for identity.
func hashFileHeader(file *os.File, headerLen int) (string, error) {
	if headerLen <= 0 {
		return "", nil
	}
	buf := make([]byte, headerLen)
	n, err := file.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		return "", err
	}
	h := sha256.Sum256(buf[:n])
	return base64.RawURLEncoding.EncodeToString(h[:16]), nil
}

// currentHeaderLen returns the number of header bytes to fingerprint for a file
// of the given size.
func currentHeaderLen(fileSize int64) int {
	if fileSize < int64(cursorHeaderBytes) {
		return int(fileSize)
	}
	return cursorHeaderBytes
}

// normalizeLogPath returns a cleaned, lowercased, forward-slash path for cursor
// path comparison so that OS casing and separator differences do not cause
// false rotation.
func normalizeLogPath(p string) string {
	return strings.ToLower(filepath.ToSlash(filepath.Clean(p)))
}

// readBeamNGRuntimeLog reads bounded diagnostics from a BeamNG log file.
// It supports cursor-based incremental reads and detects log rotation/truncation.
func readBeamNGRuntimeLog(ctx context.Context, logPath string, options RuntimeLogReadOptions) (RuntimeLogReadResult, error) {
	if logPath == "" {
		return RuntimeLogReadResult{}, errors.New("log path is empty")
	}

	file, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return RuntimeLogReadResult{
				LogPath:     logPath,
				Diagnostics: []RuntimeDiagnostic{},
				ReadAt:      nowUTC(),
			}, fmt.Errorf("log file does not exist: %s", logPath)
		}
		return RuntimeLogReadResult{}, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return RuntimeLogReadResult{}, err
	}
	fileSize := info.Size()

	maxLines := options.MaxLines
	if maxLines <= 0 || maxLines > maxLogReadLines {
		maxLines = maxLogReadLines
	}

	var fromOffset int64
	var rotated bool
	var truncated bool
	// cursorAligned is true when fromOffset comes from a valid cursor that was
	// placed at a known completed-line boundary. When true we must NOT skip the
	// first line as a partial.
	cursorAligned := false

	if options.Cursor != "" {
		prev, cursorErr := decodeLogCursor(options.Cursor)
		if cursorErr != nil {
			// Invalid cursor: treat as fresh initial read but flag it.
			rotated = true
			fromOffset = tailOffset(fileSize)
		} else if prev.LogPath != normalizeLogPath(logPath) {
			// Cursor is for a different file path.
			rotated = true
			fromOffset = 0
		} else {
			truncated = fileSize < prev.FileSize
			// Verify header using the cursor's recorded header length so that
			// appending to a <512-byte log does not change the fingerprint.
			verifyLen := prev.HeaderLen
			if int64(verifyLen) > fileSize {
				verifyLen = int(fileSize)
			}
			verifyHash, hashErr := hashFileHeader(file, verifyLen)
			if hashErr != nil {
				return RuntimeLogReadResult{}, hashErr
			}
			if verifyHash != prev.HeaderHash {
				// Header changed: the log was rotated/replaced.
				rotated = true
				fromOffset = 0
			} else if truncated {
				// File shrank: truncation within the same log session.
				rotated = true
				fromOffset = 0
			} else {
				fromOffset = prev.Offset
				cursorAligned = true
			}
		}
	} else if !options.FromStart {
		fromOffset = tailOffset(fileSize)
	}

	bounded := fromOffset > 0 && !cursorAligned

	// Bound the read size.
	readSize := fileSize - fromOffset
	if readSize > maxLogReadBytes {
		fromOffset = fileSize - maxLogReadBytes
		readSize = maxLogReadBytes
		bounded = true
		cursorAligned = false // We shifted fromOffset away from cursor boundary.
	}

	if err := ctx.Err(); err != nil {
		return RuntimeLogReadResult{}, err
	}

	// Read the byte range.
	buf := make([]byte, readSize)
	n, err := file.ReadAt(buf, fromOffset)
	if err != nil && err != io.EOF {
		return RuntimeLogReadResult{}, err
	}
	buf = buf[:n]

	if err := ctx.Err(); err != nil {
		return RuntimeLogReadResult{}, err
	}

	if fromOffset > 0 && !cursorAligned {
		var preceding [1]byte
		if _, err := file.ReadAt(preceding[:], fromOffset-1); err != nil {
			return RuntimeLogReadResult{}, err
		}
		cursorAligned = preceding[0] == '\n'
	}
	// If we started mid-file at a position that is NOT a known line boundary,
	// skip the first partial line to align to a complete line.
	startSkip := 0
	if fromOffset > 0 && !cursorAligned {
		idx := bytes.IndexByte(buf, '\n')
		if idx >= 0 {
			startSkip = idx + 1
		} else {
			// Entire buffer is one partial line with no newline.
			startSkip = len(buf)
		}
	}

	data := buf[startSkip:]
	toOffset := fromOffset + int64(len(buf))

	// Preserve incomplete trailing lines: if the data does not end with a
	// newline, the last line is still being written. Exclude it from this read
	// and set the cursor before it so the next call re-reads it complete.
	partialLine := len(data) > 0 && data[len(data)-1] != '\n'
	if partialLine {
		lastNL := bytes.LastIndexByte(data, '\n')
		if lastNL >= 0 {
			toOffset = fromOffset + int64(startSkip) + int64(lastNL) + 1
			data = data[:lastNL+1]
		} else {
			// All of data is one incomplete line.
			toOffset = fromOffset + int64(startSkip)
			data = nil
		}
	}

	// Split into lines and classify.
	lines := splitLines(data)
	totalLines := len(lines)

	// Classify every line. For initial reads (no cursor) we apply contextual
	// filtering: keep errors/warnings and their neighboring context. For cursor
	// reads, return everything (bounded by maxLines).
	isInitial := options.Cursor == "" && !rotated

	classified := make([]classifiedLine, 0, totalLines)
	totalErrors := 0
	totalWarnings := 0
	for i, line := range lines {
		if ctx.Err() != nil {
			return RuntimeLogReadResult{}, ctx.Err()
		}
		severity := runtimeSeverity(line, strings.ToLower(line))
		truncatedLine := truncateLine(line, maxLogLineBytes)
		if severity == "error" {
			totalErrors++
		} else if severity == "warning" {
			totalWarnings++
		}
		classified = append(classified, classifiedLine{
			text:      truncatedLine,
			severity:  severity,
			component: logComponent(truncatedLine),
			lineNum:   i + 1,
		})
	}

	diagnostics, omitted := selectRuntimeDiagnostics(classified, maxLines, !isInitial)
	bounded = bounded || omitted

	// Generate new cursor with the current header fingerprint length.
	newHeaderLen := currentHeaderLen(fileSize)
	newHeaderHash, err := hashFileHeader(file, newHeaderLen)
	if err != nil {
		return RuntimeLogReadResult{}, err
	}
	newCursor := encodeLogCursor(logCursorState{
		Offset:     toOffset,
		HeaderHash: newHeaderHash,
		HeaderLen:  newHeaderLen,
		FileSize:   fileSize,
		LogPath:    normalizeLogPath(logPath),
	})

	return RuntimeLogReadResult{
		LogPath:       logPath,
		Cursor:        newCursor,
		Diagnostics:   diagnostics,
		TotalLines:    totalLines,
		ReturnedLines: len(diagnostics),
		TotalErrors:   totalErrors,
		TotalWarnings: totalWarnings,
		BytesRead:     int64(n),
		FileSize:      fileSize,
		FromOffset:    fromOffset,
		ToOffset:      toOffset,
		Rotated:       rotated,
		Truncated:     truncated,
		Bounded:       bounded,
		PartialLine:   partialLine,
		ReadAt:        nowUTC(),
	}, nil
}

// readLiveGameLog resolves the configured BeamNG log and reads it.
// It exposes no arbitrary path parameter; the path comes from findRuntimeLog.
func (service *AppService) readLiveGameLog(ctx context.Context, options RuntimeLogReadOptions) (RuntimeLogReadResult, error) {
	logPath := service.findRuntimeLog()
	if logPath == "" {
		return RuntimeLogReadResult{
			Diagnostics: []RuntimeDiagnostic{},
			ReadAt:      nowUTC(),
		}, errors.New("BeamNG runtime log was not found; check that beamngRoot is configured and the game has been run at least once")
	}
	return readBeamNGRuntimeLog(ctx, logPath, options)
}

// tailOffset returns a read offset that captures the last initialTailBytes of a file.
func tailOffset(fileSize int64) int64 {
	if fileSize <= initialTailBytes {
		return 0
	}
	return fileSize - initialTailBytes
}

// truncateLine caps a line to maxBytes, appending an ellipsis marker if truncated.
func truncateLine(line string, maxBytes int) string {
	if len(line) <= maxBytes {
		return line
	}
	return line[:maxBytes] + "…[truncated]"
}

// splitLines splits data by newlines. Trailing empty line is dropped.
func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	s := string(data)
	lines := strings.Split(s, "\n")
	// Drop trailing empty element from a final newline.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// isStructuredLogLine returns true if the line starts with the BeamNG structured
// timestamp|level|component pattern. Continuation lines (stack traces, blank lines
// between diagnostics) do not match this pattern.
func isStructuredLogLine(line string) bool {
	// Structured lines start with optional whitespace, then a float timestamp,
	// then a pipe. Quick heuristic: find the first '|'.
	trimmed := strings.TrimLeft(line, " ")
	idx := strings.IndexByte(trimmed, '|')
	if idx < 1 || idx > 20 {
		return false
	}
	// The part before | should be numeric (timestamp like "241.07741").
	for _, c := range trimmed[:idx] {
		if c != '.' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// Prefer actual errors over surrounding context when output is bounded, then
// restore chronological order so stack traces remain readable.
func selectRuntimeDiagnostics(lines []classifiedLine, maxLines int, includeInfo bool) ([]RuntimeDiagnostic, bool) {
	priority := make([]uint8, len(lines))
	for i, line := range lines {
		if includeInfo {
			priority[i] = max(priority[i], 1)
		}
		if line.severity == "error" || line.severity == "warning" {
			for j := max(0, i-contextLinesBefore); j <= min(len(lines)-1, i+contextLinesAfter); j++ {
				priority[j] = max(priority[j], 1)
			}
			priority[i] = 2
			if line.severity == "error" {
				priority[i] = 3
			}
		} else if i > 0 && priority[i-1] > 0 && !isStructuredLogLine(line.text) {
			priority[i] = max(priority[i], 1)
		}
	}
	eligible := 0
	for _, value := range priority {
		if value > 0 {
			eligible++
		}
	}
	selected := 0
	for rank := uint8(3); rank > 0 && selected < maxLines; rank-- {
		for i := len(lines) - 1; i >= 0 && selected < maxLines; i-- {
			if priority[i] == rank {
				priority[i] = 4
				selected++
			}
		}
	}
	result := make([]RuntimeDiagnostic, 0, selected)
	for i, line := range lines {
		if priority[i] == 4 {
			result = append(result, RuntimeDiagnostic{
				Severity: line.severity, Component: line.component,
				Message: line.text, Line: line.lineNum,
			})
		}
	}
	return result, selected < eligible
}
