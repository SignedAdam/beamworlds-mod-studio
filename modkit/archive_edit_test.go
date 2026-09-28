package modkit

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
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
	}, false, nil); err != nil {
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
	if err := RewriteArchiveJSONMember(archivePath, "../info.json", nil, true, nil); err == nil {
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

func TestArchiveRewriteGuardAndAtomicReplacementPreserveLinkedBytes(t *testing.T) {
	root:=t.TempDir()
	source:=filepath.Join(root,"source.zip")
	linked:=filepath.Join(root,"game.zip")
	writeTestZIP(t,source,map[string]string{"mod_info/info.json":`{"description":"Original"}`})
	if err:=os.Link(source,linked);err!=nil{t.Fatal(err)}
	original,err:=os.ReadFile(source);if err!=nil{t.Fatal(err)}
	blocked:=errors.New("game started during preparation")
	if err:=RewriteArchiveJSONMember(source,"mod_info/info.json",map[string]any{"description":"Changed"},false,func()error{return blocked});!errors.Is(err,blocked){t.Fatalf("rewrite guard = %v",err)}
	for _,path:=range []string{source,linked}{
		data,err:=os.ReadFile(path);if err!=nil || !bytes.Equal(data,original){t.Fatalf("aborted rewrite changed %s: %v",path,err)}
	}
	if err:=RewriteArchiveJSONMember(source,"mod_info/info.json",map[string]any{"description":"Changed"},false,nil);err!=nil{t.Fatal(err)}
	data,err:=os.ReadFile(linked);if err!=nil || !bytes.Equal(data,original){t.Fatal("atomic source update mutated linked game bytes",err)}
	sourceInfo,err:=os.Stat(source);if err!=nil{t.Fatal(err)}
	linkedInfo,err:=os.Stat(linked);if err!=nil{t.Fatal(err)}
	if os.SameFile(sourceInfo,linkedInfo){t.Fatal("archive was edited in place instead of replaced")}
}
