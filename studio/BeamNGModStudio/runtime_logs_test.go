package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestLog writes content to a temp log file and returns its path.
func writeTestLog(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "beamng.log")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

const sampleLogHeader = `  0.00253|D|logging| -- Log started - v 0.39.4.0 - x64 - build 20972 - 2026-10-07 06:06:56 +0200 -----
  0.00253|D|logging| -- buildbot build 20972 on winbuildbot - 07/08/2026 - 18:16:47
  0.00253|D|logging| -- Log Format: Time since startup | Message level: D(ebug), I(nfo), W(arning), E(rror), A(lways) | Message
`

const sampleLogBody = `  2.04780|E|OnlineServiceProvider| Could not initialize online service provider: Steam - Error code 1
  2.34244|I|GELua.| ============== GELUA VM loading ===============
  5.21679|D|engine::ShaderGen::initShaderGen| Failed to remove file /temp/shaders/procedural/autogenConditioners.h
  7.52989|E|GELua.core_input_actions.bindings| Couldn't find action openPhone in actions lookup table
  7.56889|E|GELua.core_input_actions.bindings| Couldn't find action openPhone in actions lookup table
 15.08398|E|GELua.core_levels.| No entry point for level found: /levels/GridMap. Ignoring level.
 20.66474|E|GELua.lua| TorqueScriptLua.getVar is deprecated. Switch to VariableRegistry.get.
 20.66479|E|GELua.lua| Simple Stack Trace:
(1) lua/ge/extensions/qualityAdjuster.lua:32: onExtensionLoaded
(2) lua/common/extensions.lua:608: processLoadedFreshList
(3) lua/common/extensions.lua:650: old_loadModule
`

func TestReadBeamNGRuntimeLogInitialRead(t *testing.T) {
	t.Parallel()
	path := writeTestLog(t, sampleLogHeader+sampleLogBody)
	result, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.LogPath != path {
		t.Errorf("LogPath = %q, want %q", result.LogPath, path)
	}
	if result.Cursor == "" {
		t.Error("Cursor is empty")
	}
	if result.TotalErrors == 0 {
		t.Error("TotalErrors should be > 0")
	}
	if len(result.Diagnostics) == 0 {
		t.Error("Diagnostics should not be empty")
	}
	hasError := false
	for _, d := range result.Diagnostics {
		if d.Severity == "error" {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Error("initial read should include error diagnostics")
	}
}

func TestReadBeamNGRuntimeLogCursorAppend(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "beamng.log")

	initial := sampleLogHeader + "  2.04780|E|OnlineServiceProvider| First error\n"
	if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	r1, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	// Append new content.
	appended := "  5.00000|E|GELua.| Second error after append\n  5.00001|W|GELua.| A warning too\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(appended); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	r2, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{Cursor: r1.Cursor})
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if r2.Rotated {
		t.Error("append should not set Rotated")
	}
	if r2.TotalLines == 0 {
		t.Error("second read should have new lines")
	}

	foundSecondError := false
	foundWarning := false
	for _, d := range r2.Diagnostics {
		if strings.Contains(d.Message, "Second error after append") {
			foundSecondError = true
		}
		if strings.Contains(d.Message, "A warning too") {
			foundWarning = true
		}
	}
	if !foundSecondError {
		t.Error("second read should contain the appended error")
	}
	if !foundWarning {
		t.Error("second read should contain the appended warning")
	}
	for _, d := range r2.Diagnostics {
		if strings.Contains(d.Message, "First error") {
			t.Error("second read should not repeat content before cursor")
		}
	}
}

// TestReadBeamNGRuntimeLogShortLogAppend verifies that appending to a log file
// shorter than cursorHeaderBytes does not cause a false rotation. The header
// fingerprint must be compared at the cursor's recorded length, not the
// current file size.
func TestReadBeamNGRuntimeLogShortLogAppend(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "beamng.log")

	// Short log well under 512 bytes.
	short := "  0.1|D|logging| short\n  0.2|E|A| short error\n"
	if err := os.WriteFile(path, []byte(short), 0644); err != nil {
		t.Fatal(err)
	}

	r1, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r1.FileSize >= cursorHeaderBytes {
		t.Fatalf("test requires a short log, got %d bytes", r1.FileSize)
	}

	// Append more content. File grows but header is unchanged.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString("  1.0|E|B| appended error\n")
	f.Close()

	r2, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{Cursor: r1.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Rotated {
		t.Error("appending to a short log should not trigger rotation")
	}
	foundAppended := false
	for _, d := range r2.Diagnostics {
		if strings.Contains(d.Message, "appended error") {
			foundAppended = true
		}
		if strings.Contains(d.Message, "short error") {
			t.Error("short-log append should not replay old errors")
		}
	}
	if !foundAppended {
		t.Error("short-log append should return the new error")
	}
}

// TestReadBeamNGRuntimeLogCompletedLineBoundary verifies that a cursor read
// does not skip the first line. The cursor sits at a completed-line boundary;
// skipping a "partial" line there would lose a real diagnostic.
func TestReadBeamNGRuntimeLogCompletedLineBoundary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "beamng.log")

	initial := sampleLogHeader + "  1.0|E|A| first error\n"
	if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	r1, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Append exactly one line.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString("  2.0|E|B| second error at boundary\n")
	f.Close()

	r2, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{Cursor: r1.Cursor})
	if err != nil {
		t.Fatal(err)
	}

	// The single appended line must appear — it must not be skipped as a "partial".
	found := false
	for _, d := range r2.Diagnostics {
		if strings.Contains(d.Message, "second error at boundary") {
			found = true
		}
	}
	if !found {
		t.Error("cursor read must not skip the first complete line after the cursor boundary")
	}
	if r2.TotalLines != 1 {
		t.Errorf("expected exactly 1 new line, got %d", r2.TotalLines)
	}
}

// TestReadBeamNGRuntimeLogSplitWritePartialLine verifies that an incomplete
// trailing line (no terminating newline) is preserved for the next read
// rather than being consumed/dropped.
func TestReadBeamNGRuntimeLogSplitWritePartialLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "beamng.log")

	initial := sampleLogHeader
	if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}
	r1, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Write an incomplete line (no trailing newline).
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString("  5.0|E|GELua.| Partial err")
	f.Close()

	r2, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{Cursor: r1.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	// The partial line should NOT appear yet.
	for _, d := range r2.Diagnostics {
		if strings.Contains(d.Message, "Partial err") {
			t.Error("incomplete trailing line should not appear until completed")
		}
	}

	// Now complete the line and add another.
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString("or completed\n  6.0|W|GELua.| Next warning\n")
	f.Close()

	r3, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{Cursor: r2.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	foundComplete := false
	foundWarning := false
	for _, d := range r3.Diagnostics {
		if strings.Contains(d.Message, "Partial error completed") {
			foundComplete = true
		}
		if strings.Contains(d.Message, "Next warning") {
			foundWarning = true
		}
	}
	if !foundComplete {
		t.Error("completed partial line should appear in the next read")
	}
	if !foundWarning {
		t.Error("line after the completed partial should also appear")
	}
}

// TestReadBeamNGRuntimeLogForeignCursorSameHeader verifies that a cursor from a
// different file path is detected as rotation even when both files have identical
// header content.
func TestReadBeamNGRuntimeLogForeignCursorSameHeader(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.log")
	pathB := filepath.Join(dir, "b.log")
	content := sampleLogHeader + "  1.0|E|X| shared error\n"
	if err := os.WriteFile(pathA, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	// Identical content in a different file.
	if err := os.WriteFile(pathB, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	rA, err := readBeamNGRuntimeLog(context.Background(), pathA, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Use A's cursor on B — same header bytes but different path.
	rB, err := readBeamNGRuntimeLog(context.Background(), pathB, RuntimeLogReadOptions{Cursor: rA.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if !rB.Rotated {
		t.Error("cursor from a different path should be detected as rotation even with identical header")
	}
	foundError := false
	for _, d := range rB.Diagnostics {
		if strings.Contains(d.Message, "shared error") {
			foundError = true
		}
	}
	if !foundError {
		t.Error("should return B's diagnostics with a foreign cursor")
	}
}

func TestReadBeamNGRuntimeLogRotation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "beamng.log")

	oldLog := sampleLogHeader + "  2.04780|E|OnlineServiceProvider| Old session error\n"
	if err := os.WriteFile(path, []byte(oldLog), 0644); err != nil {
		t.Fatal(err)
	}

	r1, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	// Replace with a completely new log (different header = rotation).
	newLog := `  0.00100|D|logging| -- Log started - v 0.40.0.0 - x64 - build 21000 - 2026-10-08 10:00:00 +0200 -----
  0.00100|D|logging| -- new session
  1.00000|E|GELua.| New session error after rotation
`
	if err := os.WriteFile(path, []byte(newLog), 0644); err != nil {
		t.Fatal(err)
	}

	r2, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{Cursor: r1.Cursor})
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if !r2.Rotated {
		t.Error("should detect rotation when header changes")
	}

	foundNew := false
	for _, d := range r2.Diagnostics {
		if strings.Contains(d.Message, "New session error after rotation") {
			foundNew = true
		}
	}
	if !foundNew {
		t.Error("rotated read should contain new session's errors")
	}
	for _, d := range r2.Diagnostics {
		if strings.Contains(d.Message, "Old session error") {
			t.Error("rotated read should not contain old session content")
		}
	}
}

func TestReadBeamNGRuntimeLogTruncation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "beamng.log")

	original := sampleLogHeader + strings.Repeat("  9.00000|I|GELua.| padding line for size\n", 100)
	original += "  9.99999|E|GELua.| Error before truncation\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	r1, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	// Truncate: rewrite same header but much shorter body.
	truncated := sampleLogHeader + "  1.00000|E|GELua.| Error in truncated log\n"
	if err := os.WriteFile(path, []byte(truncated), 0644); err != nil {
		t.Fatal(err)
	}

	r2, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{Cursor: r1.Cursor})
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if !r2.Rotated {
		t.Error("should detect truncation (file shrank below cursor offset)")
	}
	if !r2.Truncated {
		t.Error("Truncated should be true on file truncation")
	}
	if r2.FromOffset != 0 {
		t.Errorf("truncation should reset to offset 0, got %d", r2.FromOffset)
	}

	foundTruncError := false
	for _, d := range r2.Diagnostics {
		if strings.Contains(d.Message, "Error in truncated log") {
			foundTruncError = true
		}
	}
	if !foundTruncError {
		t.Error("truncated read should contain the new error")
	}
}

func TestReadBeamNGRuntimeLogNewLogLargerThanOldCursor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "beamng.log")

	small := sampleLogHeader + "  1.00000|I|GELua.| Small log\n"
	if err := os.WriteFile(path, []byte(small), 0644); err != nil {
		t.Fatal(err)
	}

	r1, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	// Replace with a LARGER log that has a different header.
	larger := `  0.00500|D|logging| -- Log started - v 0.41.0.0 - DIFFERENT SESSION -----
` + strings.Repeat("  2.00000|I|GELua.| Lots of output in new log\n", 50) + `  3.00000|E|GELua.| Critical new error
`
	if err := os.WriteFile(path, []byte(larger), 0644); err != nil {
		t.Fatal(err)
	}

	r2, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{Cursor: r1.Cursor})
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if !r2.Rotated {
		t.Error("should detect rotation even when new log is larger than old cursor")
	}

	foundNew := false
	for _, d := range r2.Diagnostics {
		if strings.Contains(d.Message, "Critical new error") {
			foundNew = true
		}
	}
	if !foundNew {
		t.Error("should find errors from the new larger log")
	}
}

func TestReadBeamNGRuntimeLogMissingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nonexistent.log")
	_, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error should mention non-existence, got: %v", err)
	}
}

func TestReadBeamNGRuntimeLogEmptyPath(t *testing.T) {
	t.Parallel()
	_, err := readBeamNGRuntimeLog(context.Background(), "", RuntimeLogReadOptions{})
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestReadBeamNGRuntimeLogInvalidCursor(t *testing.T) {
	t.Parallel()
	path := writeTestLog(t, sampleLogHeader+"  1.00000|E|GELua.| Some error\n")
	result, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{Cursor: "not-a-valid-cursor!!!"})
	if err != nil {
		t.Fatalf("invalid cursor should not cause a hard error, got: %v", err)
	}
	if !result.Rotated {
		t.Error("invalid cursor should be treated as rotation (fresh read)")
	}
	if len(result.Diagnostics) == 0 {
		t.Error("should still return diagnostics with invalid cursor")
	}
}

func TestReadBeamNGRuntimeLogForeignCursorDifferentHeader(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.log")
	pathB := filepath.Join(dir, "b.log")
	if err := os.WriteFile(pathA, []byte(sampleLogHeader+"  1.0|E|A| error A\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, []byte("  0.0|D|logging| -- DIFFERENT LOG HEADER --\n  1.0|E|B| error B\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rA, err := readBeamNGRuntimeLog(context.Background(), pathA, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatalf("read A: %v", err)
	}

	rB, err := readBeamNGRuntimeLog(context.Background(), pathB, RuntimeLogReadOptions{Cursor: rA.Cursor})
	if err != nil {
		t.Fatalf("read B with A's cursor: %v", err)
	}
	if !rB.Rotated {
		t.Error("foreign cursor should be detected as rotation")
	}
	foundB := false
	for _, d := range rB.Diagnostics {
		if strings.Contains(d.Message, "error B") {
			foundB = true
		}
	}
	if !foundB {
		t.Error("should return B's diagnostics even with foreign cursor")
	}
}

func TestReadBeamNGRuntimeLogMaxLines(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	sb.WriteString(sampleLogHeader)
	for range 100 {
		sb.WriteString("  1.00000|E|GELua.| Error number ")
		sb.WriteString(strings.Repeat("x", 10))
		sb.WriteString("\n")
	}
	path := writeTestLog(t, sb.String())

	result, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{MaxLines: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Diagnostics) > 5 {
		t.Errorf("ReturnedLines = %d, want <= 5", len(result.Diagnostics))
	}
	if result.TotalErrors != 100 {
		t.Errorf("TotalErrors = %d, want 100", result.TotalErrors)
	}
}

func TestReadBeamNGRuntimeLogLineTruncation(t *testing.T) {
	t.Parallel()
	longLine := "  1.00000|E|GELua.| " + strings.Repeat("x", maxLogLineBytes+500)
	path := writeTestLog(t, sampleLogHeader+longLine+"\n")

	result, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, d := range result.Diagnostics {
		if len(d.Message) > maxLogLineBytes+50 {
			t.Errorf("line length %d exceeds max %d + marker", len(d.Message), maxLogLineBytes)
		}
	}
}

func TestReadBeamNGRuntimeLogContextualStackTrace(t *testing.T) {
	t.Parallel()
	log := sampleLogHeader + `  5.00000|I|GELua.| Some info before
  5.00001|I|GELua.| More info
  5.00002|I|GELua.| Yet more info
 20.66474|E|GELua.lua| TorqueScriptLua.getVar is deprecated.
 20.66479|E|GELua.lua| Simple Stack Trace:
(1) lua/ge/extensions/qualityAdjuster.lua:32: onExtensionLoaded
(2) lua/common/extensions.lua:608: processLoadedFreshList
(3) lua/common/extensions.lua:650: old_loadModule
 20.70000|I|GELua.| Normal info after stack trace
`
	path := writeTestLog(t, log)

	result, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hasDeprecatedError := false
	hasStackTraceHeader := false
	hasStackFrame := false
	for _, d := range result.Diagnostics {
		if strings.Contains(d.Message, "deprecated") {
			hasDeprecatedError = true
		}
		if strings.Contains(d.Message, "Simple Stack Trace") {
			hasStackTraceHeader = true
		}
		if strings.Contains(d.Message, "qualityAdjuster.lua") {
			hasStackFrame = true
		}
	}
	if !hasDeprecatedError {
		t.Error("should include the error diagnostic")
	}
	if !hasStackTraceHeader {
		t.Error("should include the stack trace header as context")
	}
	if !hasStackFrame {
		t.Error("should include stack trace frames as context")
	}
}

func TestReadBeamNGRuntimeLogBeamNGStackTracebackContext(t *testing.T) {
	t.Parallel()
	log := sampleLogHeader + ` 10.00000|I|GELua.| Unrelated info
241.07291|W|GELua.globals| set new global variable: "C"  to "table: 0x015f97698d10"
=============== Stack Traceback >> START >>
(2) main chunk of lua/ge/extensions/core/enhanceddriver/perlin.lua at line 12
--------------- << END <<

241.07741|E|GELua.extensions| extension unavailable: "_edc_cam_wrap"
`
	path := writeTestLog(t, log)

	result, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hasWarningGlobal := false
	hasTracebackStart := false
	hasTracebackFrame := false
	hasTracebackEnd := false
	hasExtUnavailable := false
	for _, d := range result.Diagnostics {
		if strings.Contains(d.Message, "set new global variable") {
			hasWarningGlobal = true
		}
		if strings.Contains(d.Message, "Stack Traceback >> START") {
			hasTracebackStart = true
		}
		if strings.Contains(d.Message, "perlin.lua") {
			hasTracebackFrame = true
		}
		if strings.Contains(d.Message, "<< END <<") {
			hasTracebackEnd = true
		}
		if strings.Contains(d.Message, "extension unavailable") {
			hasExtUnavailable = true
		}
	}
	if !hasWarningGlobal {
		t.Error("should include the warning about global variable")
	}
	if !hasTracebackStart {
		t.Error("should include stack traceback start marker")
	}
	if !hasTracebackFrame {
		t.Error("should include stack traceback frame")
	}
	if !hasTracebackEnd {
		t.Error("should include stack traceback end marker")
	}
	if !hasExtUnavailable {
		t.Error("should include the extension unavailable error")
	}
}

func TestReadBeamNGRuntimeLogCancellation(t *testing.T) {
	t.Parallel()
	path := writeTestLog(t, sampleLogHeader+sampleLogBody)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := readBeamNGRuntimeLog(ctx, path, RuntimeLogReadOptions{})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestReadBeamNGRuntimeLogEmptyLog(t *testing.T) {
	t.Parallel()
	path := writeTestLog(t, "")
	result, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error on empty log: %v", err)
	}
	if len(result.Diagnostics) != 0 {
		t.Errorf("empty log should have 0 diagnostics, got %d", len(result.Diagnostics))
	}
	if result.Cursor == "" {
		t.Error("should still produce a cursor for an empty log")
	}
}

func TestReadBeamNGRuntimeLogNoFalseCleanVerdict(t *testing.T) {
	t.Parallel()
	infoOnly := sampleLogHeader + `  5.00000|I|GELua.| All good here
  6.00000|I|GELua.| Still good
  7.00000|D|GELua.| Debug message
`
	path := writeTestLog(t, infoOnly)
	result, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalErrors != 0 {
		t.Errorf("TotalErrors = %d, want 0", result.TotalErrors)
	}
	if result.TotalWarnings != 0 {
		t.Errorf("TotalWarnings = %d, want 0", result.TotalWarnings)
	}
	if len(result.Diagnostics) != 0 {
		t.Errorf("info-only log should produce 0 diagnostics on initial read, got %d", len(result.Diagnostics))
	}
}

func TestReadBeamNGRuntimeLogIncrementalRetainsInfoLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "beamng.log")

	initial := sampleLogHeader
	if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	r1, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatal(err)
	}

	appended := "  8.00000|I|GELua.| Info after cursor\n  9.00000|E|GELua.| Error after cursor\n"
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString(appended)
	f.Close()

	r2, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{Cursor: r1.Cursor})
	if err != nil {
		t.Fatal(err)
	}

	hasInfo := false
	hasError := false
	for _, d := range r2.Diagnostics {
		if strings.Contains(d.Message, "Info after cursor") {
			hasInfo = true
		}
		if strings.Contains(d.Message, "Error after cursor") {
			hasError = true
		}
	}
	if !hasError {
		t.Error("incremental read should include errors")
	}
	if !hasInfo {
		t.Error("incremental read should include info lines")
	}
}

func TestReadBeamNGRuntimeLogMultipleSequentialReads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "beamng.log")

	content := sampleLogHeader + "  1.00000|E|A| error one\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	r1, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatal(err)
	}

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString("  2.00000|E|B| error two\n")
	f.Close()

	r2, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{Cursor: r1.Cursor})
	if err != nil {
		t.Fatal(err)
	}

	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString("  3.00000|E|C| error three\n")
	f.Close()

	r3, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{Cursor: r2.Cursor})
	if err != nil {
		t.Fatal(err)
	}

	for _, d := range r2.Diagnostics {
		if strings.Contains(d.Message, "error one") {
			t.Error("r2 should not contain error one")
		}
	}
	for _, d := range r3.Diagnostics {
		if strings.Contains(d.Message, "error one") || strings.Contains(d.Message, "error two") {
			t.Error("r3 should not contain previous errors")
		}
	}
	foundThree := false
	for _, d := range r3.Diagnostics {
		if strings.Contains(d.Message, "error three") {
			foundThree = true
		}
	}
	if !foundThree {
		t.Error("r3 should contain error three")
	}
}

func TestReadBeamNGRuntimeLogNoChangeSinceCursor(t *testing.T) {
	t.Parallel()
	path := writeTestLog(t, sampleLogHeader+"  1.00000|E|A| some error\n")

	r1, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatal(err)
	}

	r2, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{Cursor: r1.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if r2.TotalLines != 0 {
		t.Errorf("no-change read should have 0 total lines, got %d", r2.TotalLines)
	}
	if len(r2.Diagnostics) != 0 {
		t.Errorf("no-change read should have 0 diagnostics, got %d", len(r2.Diagnostics))
	}
	if r2.Rotated {
		t.Error("no-change read should not flag rotation")
	}
}

func TestReadBeamNGRuntimeLogDistinguishesCounts(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	sb.WriteString(sampleLogHeader)
	for range 20 {
		sb.WriteString("  1.00000|E|GELua.| Error\n")
	}
	for range 10 {
		sb.WriteString("  1.00000|W|GELua.| Warning\n")
	}
	path := writeTestLog(t, sb.String())

	result, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{MaxLines: 3})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalErrors != 20 {
		t.Errorf("TotalErrors = %d, want 20", result.TotalErrors)
	}
	if result.TotalWarnings != 10 {
		t.Errorf("TotalWarnings = %d, want 10", result.TotalWarnings)
	}
	if result.ReturnedLines != 3 {
		t.Errorf("ReturnedLines = %d, want 3", result.ReturnedLines)
	}
	if result.TotalLines != 33 { // 3 header + 20 errors + 10 warnings
		t.Errorf("TotalLines = %d, want 33", result.TotalLines)
	}
}

// TestReadBeamNGRuntimeLogBoundedInitialTail verifies that an initial tail-window
// read is flagged as Bounded but not Truncated (the file was not truncated, we
// just chose to read a window).
func TestReadBeamNGRuntimeLogBoundedInitialTail(t *testing.T) {
	t.Parallel()
	// Create a log larger than initialTailBytes so the tail window kicks in.
	var sb strings.Builder
	sb.WriteString(sampleLogHeader)
	line := "  1.0|I|GELua.| " + strings.Repeat("x", 200) + "\n"
	// Each line is ~220 bytes. We need > 512KiB = ~2400 lines.
	for range 2600 {
		sb.WriteString(line)
	}
	sb.WriteString("  9.0|E|GELua.| tail error\n")
	path := writeTestLog(t, sb.String())

	result, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Bounded {
		t.Error("initial tail-window read should set Bounded = true")
	}
	if result.Truncated {
		t.Error("initial tail-window read should not set Truncated (file was not truncated)")
	}
	if result.FromOffset == 0 {
		t.Error("tail-window read should not start from offset 0")
	}
	foundTail := false
	for _, d := range result.Diagnostics {
		if strings.Contains(d.Message, "tail error") {
			foundTail = true
		}
	}
	if !foundTail {
		t.Error("tail-window should include the error at the end")
	}
}

func TestReadBeamNGRuntimeLogPrioritizesErrorOverContext(t *testing.T) {
	t.Parallel()
	path := writeTestLog(t, "0.1|I|setup| preparing\n0.2|E|GELua| target failed\n0.3|I|setup| cleanup\n")
	result, err := readBeamNGRuntimeLog(context.Background(), path, RuntimeLogReadOptions{MaxLines: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Bounded || result.TotalErrors != 1 || len(result.Diagnostics) != 1 || result.Diagnostics[0].Severity != "error" {
		t.Fatalf("bounded context hid the actual error: %#v", result)
	}
}
