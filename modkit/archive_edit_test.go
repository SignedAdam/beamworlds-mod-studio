package modkit

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
)

func TestRewriteArchiveJSONMemberPreservesUntouchedEntries(t *testing.T) {
	t.Parallel()
	archivePath := filepath.Join(t.TempDir(), "metadata.zip")
	writeTestZIP(t, archivePath, map[string]string{
		"mod_info/info.json":           "{description:'Original',author:'Someone',nested:{keep:true}}\n",
		"vehicles/test/controller.lua": "return { unchanged = true }\n",
	})

	beforeRaw, beforeHeader := readRawTestZIPEntry(t, archivePath, "vehicles/test/controller.lua")
	if err := RewriteArchiveJSONMember(archivePath, "mod_info/info.json", map[string]any{
		"description": "Changed",
		"author":      nil,
	}, false); err != nil {
		t.Fatal(err)
	}

	afterRaw, afterHeader := readRawTestZIPEntry(t, archivePath, "vehicles/test/controller.lua")
	if !bytes.Equal(beforeRaw, afterRaw) || beforeHeader.CRC32 != afterHeader.CRC32 || beforeHeader.Method != afterHeader.Method || beforeHeader.CompressedSize64 != afterHeader.CompressedSize64 {
		t.Fatal("untouched archive entry was recompressed or modified")
	}
	encoded, err := ReadArchiveMember(archivePath, "mod_info/info.json", MaxArchiveJSONBytes)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(encoded, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["description"] != "Changed" {
		t.Fatalf("description = %#v, want Changed", metadata["description"])
	}
	if _, exists := metadata["author"]; exists {
		t.Fatalf("author was not removed: %#v", metadata)
	}
	nested, ok := metadata["nested"].(map[string]any)
	if !ok || nested["keep"] != true {
		t.Fatalf("unrelated metadata was not preserved: %#v", metadata)
	}
}

func TestArchiveMemberReadsAreBoundedAndPathSafe(t *testing.T) {
	t.Parallel()
	archivePath := filepath.Join(t.TempDir(), "members.zip")
	writeTestZIP(t, archivePath, map[string]string{
		"notes.txt": "0123456789",
	})

	preview, truncated, err := ReadArchiveMemberLimited(archivePath, "notes.txt", 4)
	if err != nil {
		t.Fatal(err)
	}
	if string(preview) != "0123" || !truncated {
		t.Fatalf("bounded preview = %q, truncated=%t", preview, truncated)
	}
	if _, err := ReadArchiveMember(archivePath, "notes.txt", 4); err == nil {
		t.Fatal("unbounded member read was accepted")
	}
	var copied bytes.Buffer
	if _, err := CopyArchiveMember(archivePath, "notes.txt", &copied, 10); err != nil {
		t.Fatal(err)
	}
	if copied.String() != "0123456789" {
		t.Fatalf("copied member = %q", copied.String())
	}
	if _, _, err := ReadArchiveMemberLimited(archivePath, "../notes.txt", 4); err == nil {
		t.Fatal("escaping archive member path was accepted")
	}
	if err := RewriteArchiveJSONMember(archivePath, "../info.json", nil, true); err == nil {
		t.Fatal("escaping metadata path was accepted")
	}
}

func readRawTestZIPEntry(t *testing.T, archivePath, memberPath string) ([]byte, zip.FileHeader) {
	t.Helper()
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, file := range reader.File {
		if file.Name != memberPath {
			continue
		}
		entry, err := file.OpenRaw()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(entry)
		if err != nil {
			t.Fatal(err)
		}
		return data, file.FileHeader
	}
	t.Fatalf("archive member %q was not found", memberPath)
	return nil, zip.FileHeader{}
}
