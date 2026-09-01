package modkit

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceExportIsDeterministicAndLeavesSourceUntouched(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "source.zip")
	writeTestZIP(t, source, map[string]string{
		"vehicles/test/config.pc":          "{\"format\":2}\n",
		"vehicles/test/info_config.json":   "{\"Configuration\":\"Original\"}\n",
		"vehicles/test/lua/controller.lua": "return { value = 1 }\n",
	})
	before, err := FullSHA256(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Join(root, "workspace")
	manifest, err := CreateWorkspace(context.Background(), source, workspaceRoot, "workspace-id", "entity-id", "artifact-id", KindVehicle)
	if err != nil {
		t.Fatal(err)
	}
	filesRoot := filepath.Join(workspaceRoot, "files")
	if err := WriteWorkspaceText(filesRoot, "vehicles/test/info_config.json", "{\"Configuration\":\"Changed\"}\n"); err != nil {
		t.Fatal(err)
	}
	if err := WriteWorkspaceText(filesRoot, "vehicles/test/notes.txt", "deterministic addition\n"); err != nil {
		t.Fatal(err)
	}

	changes, err := DiffWorkspace(source, filesRoot, manifest.Files)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 changes, got %d: %#v", len(changes), changes)
	}
	for _, change := range changes {
		if strings.Contains(change.TextDiff, "%22") {
			t.Fatalf("diff for %s is URI-encoded: %q", change.Path, change.TextDiff)
		}
		if !strings.Contains(change.TextDiff, "--- ") || !strings.Contains(change.TextDiff, "+++ ") {
			t.Fatalf("diff for %s lacks readable file headers: %q", change.Path, change.TextDiff)
		}
	}

	firstPath := filepath.Join(root, "first.zip")
	secondPath := filepath.Join(root, "second.zip")
	first, err := ExportWorkspace(context.Background(), source, filesRoot, firstPath, manifest.Files)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExportWorkspace(context.Background(), source, filesRoot, secondPath, manifest.Files)
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 != second.SHA256 {
		t.Fatalf("deterministic exports differ: %s != %s", first.SHA256, second.SHA256)
	}
	firstBytes, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("deterministic exports have different bytes")
	}
	after, err := FullSHA256(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if before != after || before != manifest.SourceFingerprint {
		t.Fatalf("source fingerprint changed: before=%s manifest=%s after=%s", before, manifest.SourceFingerprint, after)
	}
}

func TestCreateWorkspaceRejectsEscapingZIPPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "unsafe.zip")
	writeTestZIP(t, source, map[string]string{"../escape.txt": "not allowed"})
	workspaceRoot := filepath.Join(root, "workspace")
	if _, err := CreateWorkspace(context.Background(), source, workspaceRoot, "workspace-id", "entity-id", "artifact-id", KindUnknown); err == nil {
		t.Fatal("expected unsafe ZIP path to be rejected")
	}
	if _, err := os.Stat(filepath.Join(root, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("escaping file was created: %v", err)
	}
}

func TestSearchWorkspaceReturnsBoundedMatchContext(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	line := strings.Repeat("a", 4_000) + "needle" + strings.Repeat("b", 4_000)
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	matches, err := SearchWorkspace(root, "needle", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || !strings.Contains(matches[0], "needle") {
		t.Fatalf("unexpected search matches: %#v", matches)
	}
	if len(matches[0]) > 600 {
		t.Fatalf("search result was not bounded: %d bytes", len(matches[0]))
	}
}

func writeTestZIP(t *testing.T, filename string, entries map[string]string) {
	t.Helper()
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Date(2020, time.January, 2, 3, 4, 6, 0, time.UTC)}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(entries[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
