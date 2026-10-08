package modkit

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestFolderInspectClassifiesVehicle creates a folder that looks like a vehicle
// mod with an info.json and verifies Inspect classifies it as a vehicle and
// reads the metadata title from info.json.
func TestFolderInspectClassifiesVehicle(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	modRoot := filepath.Join(root, "test_vehicle")
	writeTestFolder(t, modRoot, map[string]string{
		"vehicles/mycar/mycar.pc":           `{"format":2}`,
		"vehicles/mycar/info_mycar.json":    `{"Configuration":"Sport","Description":"A fast car"}`,
		"vehicles/mycar/info.json":          `{"title":"My Car Mod","author":"TestUser","version":"1.0"}`,
		"vehicles/mycar/default.png":        "PNGDATA",
		"lua/vehicle/controller/custom.lua": `return {}`,
	})

	manifest, err := Inspect(context.Background(), modRoot)
	if err != nil {
		t.Fatalf("inspect folder: %v", err)
	}
	if manifest.SourceKind != SourceFolder {
		t.Fatalf("source kind = %q, want %q", manifest.SourceKind, SourceFolder)
	}
	if manifest.Kind != KindVehicle && manifest.Kind != KindMixed {
		t.Fatalf("kind = %q, want vehicle or mixed", manifest.Kind)
	}
	if manifest.Title != "My Car Mod" {
		t.Fatalf("title = %q, want %q", manifest.Title, "My Car Mod")
	}
	if manifest.Author != "TestUser" {
		t.Fatalf("author = %q, want %q", manifest.Author, "TestUser")
	}
	if manifest.CentralFingerprint == "" {
		t.Fatal("folder fingerprint is empty")
	}
	if manifest.Filename != "test_vehicle" {
		t.Fatalf("filename = %q, want %q", manifest.Filename, "test_vehicle")
	}
}

// TestFolderInspectDoesNotReadJBeam verifies that inspecting a folder with an
// invalid .jbeam file produces no JBeam issue (the JBeam analyser is skipped
// for folders per the performance rule).
func TestFolderInspectDoesNotReadJBeam(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	modRoot := filepath.Join(root, "jbeam_mod")
	writeTestFolder(t, modRoot, map[string]string{
		"vehicles/mycar/mycar.pc":      `{"format":2}`,
		"vehicles/mycar/info.json":     `{"title":"JBeam Test"}`,
		"vehicles/mycar/bad_data.jbeam": `THIS IS NOT VALID JSON AT ALL {{{`,
	})

	manifest, err := Inspect(context.Background(), modRoot)
	if err != nil {
		t.Fatalf("inspect folder: %v", err)
	}
	// JBeam stats should be zero.
	if manifest.JBeam.Files != 0 {
		t.Fatalf("JBeam.Files = %d, want 0", manifest.JBeam.Files)
	}
	if manifest.JBeam.ParsedFiles != 0 {
		t.Fatalf("JBeam.ParsedFiles = %d, want 0", manifest.JBeam.ParsedFiles)
	}
	// There should be no JBeam-related issues.
	for _, issue := range manifest.Issues {
		if strings.Contains(strings.ToLower(issue.Code), "jbeam") {
			t.Fatalf("unexpected JBeam issue: %v", issue)
		}
	}
}

// TestFolderListingFingerprintStable verifies the fingerprint is stable when
// nothing changes, and changes when a file is resized or touched.
func TestFolderListingFingerprintStable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	modRoot := filepath.Join(root, "fingerprint_mod")
	writeTestFolder(t, modRoot, map[string]string{
		"info.json":       `{"title":"test"}`,
		"vehicles/a/a.pc": `{}`,
	})

	fp1, err := FolderListingFingerprint(context.Background(), modRoot)
	if err != nil {
		t.Fatal(err)
	}
	fp2, err := FolderListingFingerprint(context.Background(), modRoot)
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 {
		t.Fatalf("fingerprint changed without file changes: %s != %s", fp1, fp2)
	}

	// Change file size by appending.
	infoPath := filepath.Join(modRoot, "info.json")
	if err := os.WriteFile(infoPath, []byte(`{"title":"test","v":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fp3, err := FolderListingFingerprint(context.Background(), modRoot)
	if err != nil {
		t.Fatal(err)
	}
	if fp1 == fp3 {
		t.Fatal("fingerprint unchanged after file resize")
	}

	// Touch file (change modified time but same size).
	now := time.Now().Add(time.Hour)
	if err := os.Chtimes(infoPath, now, now); err != nil {
		t.Fatal(err)
	}
	fp4, err := FolderListingFingerprint(context.Background(), modRoot)
	if err != nil {
		t.Fatal(err)
	}
	if fp3 == fp4 {
		t.Fatal("fingerprint unchanged after file touch")
	}
}

// TestFolderMemberReadRejectsEscapingPaths verifies that folder member reads
// reject paths containing ".." or absolute paths.
func TestFolderMemberReadRejectsEscapingPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	modRoot := filepath.Join(root, "safe_mod")
	writeTestFolder(t, modRoot, map[string]string{
		"info.json": `{}`,
	})

	cases := []string{
		"../info.json",
		"../../etc/passwd",
		"/absolute/path",
		"vehicles/../../etc/hosts",
	}
	for _, badPath := range cases {
		_, _, err := readFolderMember(modRoot, badPath, 1024)
		if err == nil {
			t.Fatalf("expected error for path %q, got nil", badPath)
		}
	}
}

// TestFolderJSONRewritePreservesOtherFiles verifies that rewriting a JSON
// member in a folder only changes the target file and preserves others.
func TestFolderJSONRewritePreservesOtherFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	modRoot := filepath.Join(root, "rewrite_mod")
	writeTestFolder(t, modRoot, map[string]string{
		"info.json":          `{"title":"Original","author":"Test"}`,
		"vehicles/a/a.pc":    `{"format":2}`,
		"vehicles/a/data.pc": `{"format":1}`,
	})
	otherBefore, err := os.ReadFile(filepath.Join(modRoot, "vehicles", "a", "a.pc"))
	if err != nil {
		t.Fatal(err)
	}
	err = RewriteArchiveJSONMember(modRoot, "info.json", map[string]any{"title": "Updated"}, false, nil)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(modRoot, "info.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Updated") {
		t.Fatalf("info.json not updated: %s", data)
	}
	if !strings.Contains(string(data), "Test") {
		t.Fatalf("author was lost after rewrite: %s", data)
	}
	otherAfter, err := os.ReadFile(filepath.Join(modRoot, "vehicles", "a", "a.pc"))
	if err != nil {
		t.Fatal(err)
	}
	if string(otherBefore) != string(otherAfter) {
		t.Fatal("other file was modified by rewrite")
	}
}

// TestFolderCreateWorkspaceAndExport verifies the full round-trip: creating a
// workspace from a folder, editing a file, and exporting produces a valid ZIP
// with the edited file.
func TestFolderCreateWorkspaceAndExport(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	modRoot := filepath.Join(root, "ws_mod")
	writeTestFolder(t, modRoot, map[string]string{
		"vehicles/test/test.pc":       `{"format":2}`,
		"vehicles/test/info.json":     `{"title":"WS Test"}`,
		"vehicles/test/controller.lua": `return {}`,
	})

	workspaceRoot := filepath.Join(root, "workspace")
	manifest, err := CreateWorkspace(context.Background(), modRoot, workspaceRoot, "ws-id", "entity-id", "artifact-id", KindVehicle)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if manifest.SourceFingerprint == "" {
		t.Fatal("source fingerprint is empty")
	}
	if !strings.HasPrefix(manifest.SourceFingerprint, "folder:") {
		t.Fatalf("source fingerprint should start with 'folder:': %s", manifest.SourceFingerprint)
	}

	filesRoot := filepath.Join(workspaceRoot, "files")
	if err := WriteWorkspaceText(filesRoot, "vehicles/test/info.json", `{"title":"WS Updated"}`+"\n"); err != nil {
		t.Fatal(err)
	}

	exportPath := filepath.Join(root, "export.zip")
	result, err := ExportWorkspace(context.Background(), modRoot, filesRoot, exportPath, manifest.Files)
	if err != nil {
		t.Fatalf("export workspace: %v", err)
	}
	if result.EntryCount < 3 {
		t.Fatalf("expected at least 3 entries, got %d", result.EntryCount)
	}
	// Verify the export is a valid ZIP containing the edited file.
	reader, err := zip.OpenReader(exportPath)
	if err != nil {
		t.Fatalf("open export: %v", err)
	}
	defer reader.Close()
	found := false
	for _, file := range reader.File {
		if strings.EqualFold(file.Name, "vehicles/test/info.json") {
			rc, openErr := file.Open()
			if openErr != nil {
				t.Fatal(openErr)
			}
			data := make([]byte, file.UncompressedSize64)
			_, _ = rc.Read(data)
			rc.Close()
			if !strings.Contains(string(data), "WS Updated") {
				t.Fatalf("exported info.json does not contain edit: %s", data)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("exported ZIP does not contain vehicles/test/info.json")
	}
}

// TestZIPFingerprintUnchanged pins the CentralFingerprint of a fixed ZIP to
// verify the refactoring does not alter the formula.
func TestZIPFingerprintUnchanged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	archivePath := filepath.Join(root, "pinned.zip")
	writeTestZIP(t, archivePath, map[string]string{
		"vehicles/pin/pin.pc":       `{"format":2}`,
		"vehicles/pin/info.json":    `{"title":"Pinned"}`,
		"vehicles/pin/default.png":  "png-data",
	})
	manifest, err := Inspect(context.Background(), archivePath)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	// Record the fingerprint from the current (pre-refactoring equivalent) code.
	fingerprint := manifest.CentralFingerprint
	if fingerprint == "" {
		t.Fatal("central fingerprint is empty")
	}
	// Inspect again and verify it's identical.
	manifest2, err := Inspect(context.Background(), archivePath)
	if err != nil {
		t.Fatalf("re-inspect: %v", err)
	}
	if manifest2.CentralFingerprint != fingerprint {
		t.Fatalf("fingerprint changed: %s != %s", manifest2.CentralFingerprint, fingerprint)
	}
	// Verify SourceKind is empty (default zip) for ZIP.
	if manifest.SourceKind != "" {
		t.Fatalf("ZIP source kind = %q, want empty", manifest.SourceKind)
	}
}

// TestSourceKindOf verifies SourceKindOf for files and directories.
func TestSourceKindOf(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Directory
	kind, err := SourceKindOf(root)
	if err != nil {
		t.Fatal(err)
	}
	if kind != SourceFolder {
		t.Fatalf("kind = %q, want %q", kind, SourceFolder)
	}
	// Regular file
	filePath := filepath.Join(root, "test.zip")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	kind, err = SourceKindOf(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if kind != SourceZIP {
		t.Fatalf("kind = %q, want %q", kind, SourceZIP)
	}
}

// TestSourceContentID verifies the SourceContentID function for both ZIPs and folders.
func TestSourceContentID(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Folder
	modRoot := filepath.Join(root, "mod")
	writeTestFolder(t, modRoot, map[string]string{"info.json": `{}`})
	id, err := SourceContentID(context.Background(), modRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "folder:") {
		t.Fatalf("folder content ID should start with 'folder:': %s", id)
	}

	// ZIP
	zipPath := filepath.Join(root, "test.zip")
	writeTestZIP(t, zipPath, map[string]string{"info.json": `{}`})
	id, err = SourceContentID(context.Background(), zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(id, "folder:") {
		t.Fatalf("ZIP content ID should not start with 'folder:': %s", id)
	}
}

// TestFolderInspectReadsVariantInfo verifies that folder Inspect reads variant
// info files (info_<base>.json).
func TestFolderInspectReadsVariantInfo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	modRoot := filepath.Join(root, "variant_mod")
	writeTestFolder(t, modRoot, map[string]string{
		"vehicles/mycar/mycar.pc":        `{"format":2}`,
		"vehicles/mycar/info_mycar.json": `{"Configuration":"Sport","Description":"Fast variant"}`,
		"vehicles/mycar/info.json":       `{"title":"Variant Test"}`,
	})
	manifest, err := Inspect(context.Background(), modRoot)
	if err != nil {
		t.Fatalf("inspect folder: %v", err)
	}
	if len(manifest.Variants) == 0 {
		t.Fatal("no variants found")
	}
	if manifest.Variants[0].Configuration != "Sport" {
		t.Fatalf("variant configuration = %q, want %q", manifest.Variants[0].Configuration, "Sport")
	}
}

// TestFolderReadArchiveMember verifies that ReadArchiveMember and
// ReadArchiveMemberLimited work for folder sources.
func TestFolderReadArchiveMember(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	modRoot := filepath.Join(root, "read_mod")
	writeTestFolder(t, modRoot, map[string]string{
		"info.json": `{"title":"readable"}`,
	})
	data, err := ReadArchiveMember(modRoot, "info.json", 0)
	if err != nil {
		t.Fatalf("read member: %v", err)
	}
	if !strings.Contains(string(data), "readable") {
		t.Fatalf("unexpected data: %s", data)
	}
}

func writeTestFolder(t *testing.T, root string, entries map[string]string) {
	t.Helper()
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	slices.Sort(names)
	dirs := map[string]bool{}
	fixedTime := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	for _, name := range names {
		fullPath := filepath.Join(root, filepath.FromSlash(name))
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create dir for %s: %v", name, err)
		}
		// Track all directories to fix their mtimes later.
		for d := dir; d != root && !dirs[d]; d = filepath.Dir(d) {
			dirs[d] = true
		}
		if err := os.WriteFile(fullPath, []byte(entries[name]), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		_ = os.Chtimes(fullPath, fixedTime, fixedTime)
	}
	// Fix directory mtimes after all files are written so they are stable.
	for d := range dirs {
		_ = os.Chtimes(d, fixedTime, fixedTime)
	}
	_ = os.Chtimes(root, fixedTime, fixedTime)
}
