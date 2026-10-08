package modkit

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
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

func TestWorkspaceWalkersExcludeOnlyRootGitMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "source.zip")
	writeTestZIP(t, source, map[string]string{
		"vehicles/test/example.jbeam": "{\"format\":2}\n",
	})
	workspaceRoot := filepath.Join(root, "workspace")
	manifest, err := CreateWorkspace(context.Background(), source, workspaceRoot, "workspace-id", "entity-id", "artifact-id", KindVehicle)
	if err != nil {
		t.Fatal(err)
	}
	filesRoot := filepath.Join(workspaceRoot, "files")
	rootGitSecret := filepath.Join(filesRoot, ".git", "config")
	if err := os.MkdirAll(filepath.Dir(rootGitSecret), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootGitSecret, []byte("root-private-token"), 0o644); err != nil {
		t.Fatal(err)
	}
	nestedGitAsset := filepath.Join(filesRoot, "assets", ".git", "kept.txt")
	if err := os.MkdirAll(filepath.Dir(nestedGitAsset), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nestedGitAsset, []byte("nested-git-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := ListWorkspaceFiles(filesRoot)
	if err != nil {
		t.Fatal(err)
	}
	if containsWorkspacePath(files, ".git/config") {
		t.Fatal("root .git metadata appeared in workspace files")
	}
	if !containsWorkspacePath(files, "assets/.git/kept.txt") {
		t.Fatal("nested assets/.git content was incorrectly excluded")
	}
	directories, err := ListWorkspaceDirectories(filesRoot)
	if err != nil {
		t.Fatal(err)
	}
	if containsString(directories, ".git") {
		t.Fatal("root .git metadata appeared in workspace directories")
	}
	if !containsString(directories, "assets/.git") {
		t.Fatal("nested assets/.git directory was incorrectly excluded")
	}

	matches, err := SearchWorkspaceMatches(filesRoot, "root-private-token", 10, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("search returned root Git metadata: %#v", matches)
	}
	matches, err = SearchWorkspaceMatches(filesRoot, "nested-git-content", 10, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].RelativePath != "assets/.git/kept.txt" {
		t.Fatalf("search omitted nested .git content: %#v", matches)
	}

	changes, err := DiffWorkspace(source, filesRoot, manifest.Files)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range changes {
		if strings.HasPrefix(change.Path, ".git/") {
			t.Fatalf("diff included root Git metadata: %#v", change)
		}
	}
	exportPath := filepath.Join(root, "export.zip")
	if _, err := ExportWorkspace(context.Background(), source, filesRoot, exportPath, manifest.Files); err != nil {
		t.Fatal(err)
	}
	exported, err := zip.OpenReader(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	defer exported.Close()
	for _, entry := range exported.File {
		if entry.Name == ".git" || strings.HasPrefix(entry.Name, ".git/") {
			t.Fatalf("export included root Git metadata: %q", entry.Name)
		}
	}
}

func containsWorkspacePath(files []FileSnapshot, want string) bool {
	for _, file := range files {
		if file.Path == want {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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

func TestWorkspaceRecoversMissingZIPDescriptorsWithoutChangingSource(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "missing-descriptors.zip")
	contents := []string{`{"Name":"Dummy"}`, "return {enabled = true}\n"}
	raw := missingDescriptorZIP(t, contents, false)
	if err := os.WriteFile(source, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	manifest, err := CreateWorkspace(context.Background(), source, workspace, "workspace", "entity", "artifact", KindScript)
	if err != nil {
		t.Fatal(err)
	}
	exportPath := filepath.Join(root, "repaired.zip")
	if _, err := ExportWorkspace(context.Background(), source, filepath.Join(workspace, "files"), exportPath, manifest.Files); err != nil {
		t.Fatal(err)
	}
	exported, err := zip.OpenReader(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	defer exported.Close()
	if len(exported.File) != len(contents) {
		t.Fatalf("export lost entries: %d", len(exported.File))
	}
	for i, file := range exported.File {
		input, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(input)
		_ = input.Close()
		if readErr != nil || string(data) != contents[i] {
			t.Fatalf("ordinary ZIP reader could not verify exported payload %q: %q, %v", file.Name, data, readErr)
		}
	}
	after, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(after, raw) {
		t.Fatalf("original archive changed: %v", err)
	}
}

func TestMissingZIPDescriptorRecoveryRejectsBadPayloadCRC(t *testing.T) {
	for _, zeroCRC := range []bool{false, true} {
		t.Run(map[bool]string{false: "damaged payload", true: "zero central CRC"}[zeroCRC], func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			raw := missingDescriptorZIP(t, []string{"original payload"}, zeroCRC)
			if !zeroCRC {
				index := bytes.Index(raw, []byte("original payload"))
				raw[index] = 'X'
			}
			source := filepath.Join(root, "corrupt.zip")
			if err := os.WriteFile(source, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			workspace := filepath.Join(root, "workspace")
			_, err := CreateWorkspace(context.Background(), source, workspace, "workspace", "entity", "artifact", KindScript)
			if !errors.Is(err, zip.ErrChecksum) {
				t.Fatalf("corrupt payload accepted or misclassified: %v", err)
			}
			if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed import left a workspace: %v", err)
			}
		})
	}
}

func TestZIPHeaderLikeDescriptorCRCIsNotTreatedAsMissing(t *testing.T) {
	t.Parallel()
	const payload = "a payload with a damaged signatureless descriptor"
	raw := missingDescriptorZIP(t, []string{payload}, false)
	central := bytes.Index(raw, []byte("PK\x01\x02"))
	var descriptor [12]byte
	binary.LittleEndian.PutUint32(descriptor[:4], 0x04034b50)
	binary.LittleEndian.PutUint32(descriptor[4:8], uint32(len(payload)))
	binary.LittleEndian.PutUint32(descriptor[8:12], uint32(len(payload)))
	archive := append([]byte(nil), raw[:central]...)
	archive = append(archive, descriptor[:]...)
	archive = append(archive, raw[central:]...)
	end := bytes.LastIndex(archive, []byte("PK\x05\x06"))
	binary.LittleEndian.PutUint32(archive[end+16:end+20], uint32(central+len(descriptor)))
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = readZipEntry(reader.File[0], 1024)
	if !errors.Is(err, zip.ErrChecksum) {
		t.Fatalf("damaged descriptor was silently discarded: %v", err)
	}
}

func missingDescriptorZIP(t *testing.T, contents []string, zeroCRC bool) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	names := []string{"info.json", "lua/test.lua"}
	for i, content := range contents {
		checksum := crc32.ChecksumIEEE([]byte(content))
		if zeroCRC {
			checksum = 0
		}
		entry, err := writer.CreateRaw(&zip.FileHeader{
			Name: names[i], Method: zip.Store, CRC32: checksum,
			CompressedSize64: uint64(len(content)), UncompressedSize64: uint64(len(content)),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buffer.Bytes()
	// Deliberately declare descriptors without adding any, as the broken
	// producer does. The local and central sizes/CRCs remain populated.
	for offset := 0; offset+10 <= len(raw); offset++ {
		switch string(raw[offset : offset+4]) {
		case "PK\x03\x04":
			binary.LittleEndian.PutUint16(raw[offset+6:offset+8], 0x8)
		case "PK\x01\x02":
			binary.LittleEndian.PutUint16(raw[offset+8:offset+10], 0x8)
		}
	}
	return raw
}
