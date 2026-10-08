package main

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

func newLibrarySyncTestService(t *testing.T) *AppService {
	t.Helper()
	root := t.TempDir()
	beamNGRoot := filepath.Join(root, "beamng")
	activeModsDir := filepath.Join(beamNGRoot, "current", "mods")
	config := AppConfig{
		SetupComplete: true, BeamNGRoot: beamNGRoot,
		LibraryDir:    filepath.Join(root, "library"),
		DataDir:       filepath.Join(root, "data"),
		DatabasePath:  filepath.Join(root, "data", "modstudio.sqlite"),
		ImageCacheDir: filepath.Join(root, "data", "cache", "images"),
		WorkspaceDir:  filepath.Join(root, "data", "workspaces"),
		ExportDir:     filepath.Join(root, "data", "exports"),
		ProfileDir:    filepath.Join(root, "data", "profiles"),
		ActiveModsDir: activeModsDir, TestInstallDir: activeModsDir,
		ScanConcurrency: 1,
		ScanRoots:       []string{filepath.Join(root, "library"), beamNGRoot},
	}
	for _, dir := range []string{config.LibraryDir, config.ImageCacheDir, config.WorkspaceDir, config.ExportDir, config.ProfileDir, config.ActiveModsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store, err := OpenStore(config.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := NewAppService(config, store, func(string, any) {})
	service.gameRunning = func() (bool, error) { return false, nil }
	return service
}

func writeTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err == nil {
			_, err = entry.Write([]byte(content))
		}
		if err != nil {
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

func zipMember(t *testing.T, path, name string) string {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, file := range reader.File {
		if strings.EqualFold(file.Name, name) {
			opened, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(opened)
			opened.Close()
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
	}
	return ""
}

func scanTestLibrary(t *testing.T, service *AppService) {
	t.Helper()
	service.modImportMu.Lock()
	_, err := service.library.Scan(context.Background())
	service.modImportMu.Unlock()
	if err != nil {
		t.Fatal("scan:", err)
	}
}

// openEditedMod indexes a small mod at archivePath and opens it in ModMaker.
func openEditedMod(t *testing.T, service *AppService, archivePath string) (LibraryItem, WorkspaceDetail) {
	t.Helper()
	writeTestZip(t, archivePath, map[string]string{
		"mod_info/dummy/info.json":    `{"title":"Dummy","tag_line":"before"}`,
		"lua/ge/extensions/dummy.lua": "return {}\n",
	})
	scanTestLibrary(t, service)
	items, err := service.ListLibrary("", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if samePath(item.ArchivePath, archivePath) {
			detail, err := service.CreateWorkspace(item.EntityID)
			if err != nil {
				t.Fatal("open in ModMaker:", err)
			}
			return item, detail
		}
	}
	t.Fatalf("%s was not indexed", archivePath)
	return LibraryItem{}, WorkspaceDetail{}
}

func editWorkspaceFile(t *testing.T, service *AppService, workspaceID, relative, content string) {
	t.Helper()
	current, err := service.ReadWorkspaceFile(workspaceID, relative)
	expected := ""
	if err == nil {
		expected = current.SHA256
	}
	if err := service.WriteWorkspaceFile(workspaceID, relative, content, expected); err != nil {
		t.Fatal("save:", err)
	}
	_, _ = service.takePendingLibrarySync(workspaceID)
}

func libraryItemFor(t *testing.T, service *AppService, entityID string) LibraryItem {
	t.Helper()
	items, err := service.ListLibrary("", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.EntityID == entityID {
			return item
		}
	}
	t.Fatalf("mod %s missing from library", entityID)
	return LibraryItem{}
}

func TestSavingInModMakerUpdatesTheLibraryModAndKeepsTheOriginal(t *testing.T) {
	service := newLibrarySyncTestService(t)
	archivePath := filepath.Join(service.config.LibraryDir, "dummy.zip")
	item, detail := openEditedMod(t, service, archivePath)
	originalSHA := fileSHAForTest(t, archivePath)

	editWorkspaceFile(t, service, detail.Workspace.ID, "lua/ge/extensions/dummy.lua", "return { fixed = true }\n")
	if err := service.syncWorkspaceLibrary(context.Background(), detail.Workspace.ID, "you"); err != nil {
		t.Fatal("update library:", err)
	}

	if got := zipMember(t, archivePath, "lua/ge/extensions/dummy.lua"); got != "return { fixed = true }\n" {
		t.Fatalf("library mod holds %q, want the saved edit", got)
	}
	after := libraryItemFor(t, service, item.EntityID)
	if !samePath(after.ArchivePath, archivePath) || !after.Edited || after.HistoryCount != 1 {
		t.Fatalf("library mod after edit = path %s edited %v history %d; want same file, edited, one saved version", after.ArchivePath, after.Edited, after.HistoryCount)
	}
	refreshed, err := service.GetWorkspace(detail.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sha := fileSHAForTest(t, refreshed.Workspace.SourcePath); sha != originalSHA || samePath(refreshed.Workspace.SourcePath, archivePath) {
		t.Fatalf("original kept at %s with %s; want the original bytes outside the library", refreshed.Workspace.SourcePath, sha)
	}
	if refreshed.Library.State != LibraryStateSynced || refreshed.Library.ChangedFiles != 1 {
		t.Fatalf("library status = %+v, want synced with one changed file", refreshed.Library)
	}
	versions, err := service.ListModVersions(item.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].Kind != "saved" || !versions[0].Current || versions[1].Kind != "original" || versions[1].Current {
		t.Fatalf("versions = %+v, want the current saved version then the original", versions)
	}
	// Rescanning sees the same mod, not a new one.
	scanTestLibrary(t, service)
	if items, _ := service.ListLibrary("", "", "", "", ""); len(items) != 1 {
		t.Fatalf("library has %d mods after rescanning, want 1", len(items))
	}
	// ModMaker can still export a separate copy from the kept original.
	if _, err := service.ExportWorkspace(detail.Workspace.ID, "copy"); err != nil {
		t.Fatalf("export after the library update: %v", err)
	}
}

func TestRestoringVersionsSwitchesTheLibraryModBothWays(t *testing.T) {
	service := newLibrarySyncTestService(t)
	archivePath := filepath.Join(service.config.LibraryDir, "dummy.zip")
	item, detail := openEditedMod(t, service, archivePath)
	originalSHA := fileSHAForTest(t, archivePath)
	editWorkspaceFile(t, service, detail.Workspace.ID, "lua/ge/extensions/dummy.lua", "return { fixed = true }\n")
	if err := service.syncWorkspaceLibrary(context.Background(), detail.Workspace.ID, "virgil"); err != nil {
		t.Fatal(err)
	}
	versions, _ := service.ListModVersions(item.EntityID)
	fixedID := versions[0].ID

	if _, err := service.RestoreModVersion(item.EntityID, modVersionOriginalID); err != nil {
		t.Fatal("restore original:", err)
	}
	if sha := fileSHAForTest(t, archivePath); sha != originalSHA {
		t.Fatalf("library file after restoring the original = %s, want the original %s", sha, originalSHA)
	}
	if after := libraryItemFor(t, service, item.EntityID); after.Edited {
		t.Fatal("mod still marked edited after restoring the original")
	}
	if text, _ := service.ReadWorkspaceFile(detail.Workspace.ID, "lua/ge/extensions/dummy.lua"); text.Content != "return {}\n" {
		t.Fatalf("workspace after restoring the original holds %q", text.Content)
	}
	versions, _ = service.ListModVersions(item.EntityID)
	if len(versions) != 2 || !versions[1].Current || versions[0].Current {
		t.Fatalf("versions after restoring the original = %+v, want the original current and the fix kept", versions)
	}

	if _, err := service.RestoreModVersion(item.EntityID, fixedID); err != nil {
		t.Fatal("restore the fix:", err)
	}
	if got := zipMember(t, archivePath, "lua/ge/extensions/dummy.lua"); got != "return { fixed = true }\n" {
		t.Fatalf("library mod after restoring the fix holds %q", got)
	}
	versions, _ = service.ListModVersions(item.EntityID)
	if len(versions) != 2 || versions[0].ID != fixedID || !versions[0].Current {
		t.Fatalf("versions after restoring the fix = %+v, want the fix current with no duplicate", versions)
	}
}

func TestEditsWithinMinutesShareOneSavedVersion(t *testing.T) {
	service := newLibrarySyncTestService(t)
	item, detail := openEditedMod(t, service, filepath.Join(service.config.LibraryDir, "dummy.zip"))
	for _, content := range []string{"return 1\n", "return 2\n"} {
		editWorkspaceFile(t, service, detail.Workspace.ID, "lua/ge/extensions/dummy.lua", content)
		if err := service.syncWorkspaceLibrary(context.Background(), detail.Workspace.ID, "you"); err != nil {
			t.Fatal(err)
		}
	}
	if versions, _ := service.ListModVersions(item.EntityID); len(versions) != 2 {
		t.Fatalf("two quick saves produced %d entries, want one saved version plus the original", len(versions))
	}
	// A different author starts a new version.
	editWorkspaceFile(t, service, detail.Workspace.ID, "lua/ge/extensions/dummy.lua", "return 3\n")
	if err := service.syncWorkspaceLibrary(context.Background(), detail.Workspace.ID, "virgil"); err != nil {
		t.Fatal(err)
	}
	versions, _ := service.ListModVersions(item.EntityID)
	if len(versions) != 3 || versions[0].Author != "virgil" || versions[1].Author != "you" {
		t.Fatalf("versions = %+v, want Virgil's change saved separately from yours", versions)
	}
}

func TestChangesMadeOutsideModMakerAreKept(t *testing.T) {
	service := newLibrarySyncTestService(t)
	archivePath := filepath.Join(service.config.LibraryDir, "dummy.zip")
	_, detail := openEditedMod(t, service, archivePath)
	editWorkspaceFile(t, service, detail.Workspace.ID, "lua/ge/extensions/dummy.lua", "return { fixed = true }\n")
	if err := service.syncWorkspaceLibrary(context.Background(), detail.Workspace.ID, "you"); err != nil {
		t.Fatal(err)
	}
	// Another tool rewrites the library file with the fix and a new description.
	writeTestZip(t, archivePath, map[string]string{
		"mod_info/dummy/info.json":    `{"title":"Dummy","tag_line":"from outside"}`,
		"lua/ge/extensions/dummy.lua": "return { fixed = true }\n",
	})
	editWorkspaceFile(t, service, detail.Workspace.ID, "lua/ge/extensions/extra.lua", "return {}\n")
	if err := service.syncWorkspaceLibrary(context.Background(), detail.Workspace.ID, "you"); err != nil {
		t.Fatal(err)
	}
	if got := zipMember(t, archivePath, "mod_info/dummy/info.json"); !strings.Contains(got, "from outside") {
		t.Fatalf("library lost the outside change: info.json = %q", got)
	}
	if text, _ := service.ReadWorkspaceFile(detail.Workspace.ID, "mod_info/dummy/info.json"); !strings.Contains(text.Content, "from outside") {
		t.Fatalf("workspace did not take the outside change: %q", text.Content)
	}
	if got := zipMember(t, archivePath, "lua/ge/extensions/extra.lua"); got != "return {}\n" {
		t.Fatalf("library lost the new ModMaker file: %q", got)
	}
}

func TestInspectorEditsOfAModInModMakerGoThroughItsWorkspace(t *testing.T) {
	service := newLibrarySyncTestService(t)
	archivePath := filepath.Join(service.config.LibraryDir, "dummy.zip")
	item, detail := openEditedMod(t, service, archivePath)
	editWorkspaceFile(t, service, detail.Workspace.ID, "lua/ge/extensions/dummy.lua", "return { fixed = true }\n")

	description := "edited in the Inspector"
	if _, err := service.UpdateLibraryItemDetails(item.EntityID, LibraryItemDetailsUpdate{Description: description}); err != nil {
		t.Fatal("inspector edit:", err)
	}
	if got := zipMember(t, archivePath, "mod_info/dummy/info.json"); !strings.Contains(got, description) {
		t.Fatalf("library info.json = %q, want the Inspector edit", got)
	}
	if text, _ := service.ReadWorkspaceFile(detail.Workspace.ID, "mod_info/dummy/info.json"); !strings.Contains(text.Content, description) {
		t.Fatalf("workspace info.json = %q, want the Inspector edit", text.Content)
	}
	if got := zipMember(t, archivePath, "lua/ge/extensions/dummy.lua"); got != "return { fixed = true }\n" {
		t.Fatalf("library lost the unsaved-to-library ModMaker edit: %q", got)
	}
}

func TestModsInBeamNGFoldersAreNeverRewritten(t *testing.T) {
	service := newLibrarySyncTestService(t)
	archivePath := filepath.Join(service.config.ActiveModsDir, "repo", "dummy.zip")
	item, detail := openEditedMod(t, service, archivePath)
	before := fileSHAForTest(t, archivePath)
	editWorkspaceFile(t, service, detail.Workspace.ID, "lua/ge/extensions/dummy.lua", "return { fixed = true }\n")
	if err := service.syncWorkspaceLibrary(context.Background(), detail.Workspace.ID, "you"); err != nil {
		t.Fatal(err)
	}
	if sha := fileSHAForTest(t, archivePath); sha != before {
		t.Fatal("a mod in BeamNG's folder was rewritten")
	}
	refreshed, _ := service.GetWorkspace(detail.Workspace.ID)
	if refreshed.Library.State != LibraryStateReadOnly {
		t.Fatalf("status = %+v, want read-only", refreshed.Library)
	}
	if versions, _ := service.ListModVersions(item.EntityID); len(versions) != 0 {
		t.Fatalf("read-only mod has versions %+v", versions)
	}
}

func TestFailedLibraryUpdateLeavesTheLibraryUntouched(t *testing.T) {
	service := newLibrarySyncTestService(t)
	archivePath := filepath.Join(service.config.LibraryDir, "dummy.zip")
	item, detail := openEditedMod(t, service, archivePath)
	originalSHA := fileSHAForTest(t, archivePath)
	editWorkspaceFile(t, service, detail.Workspace.ID, "lua/ge/extensions/dummy.lua", "return { fixed = true }\n")
	// Recording the saved version fails after the library file was replaced.
	if _, err := service.store.db.Exec(`CREATE TRIGGER reject_history BEFORE INSERT ON mod_history BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := service.syncWorkspaceLibrary(context.Background(), detail.Workspace.ID, "you"); err == nil {
		t.Fatal("update succeeded although recording its version failed")
	}
	if sha := fileSHAForTest(t, archivePath); sha != originalSHA {
		t.Fatalf("library file after a failed update = %s, want the original %s", sha, originalSHA)
	}
	refreshed, _ := service.store.GetWorkspace(context.Background(), detail.Workspace.ID)
	if !samePath(refreshed.SourcePath, archivePath) {
		t.Fatalf("workspace source moved to %s by a failed update", refreshed.SourcePath)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(service.config.LibraryDir, ".beamworlds-staging", "*")); len(leftovers) != 0 {
		t.Fatalf("staging files left in the library: %v", leftovers)
	}
	if _, err := os.Stat(filepath.Join(service.config.DataDir, "versions", detail.Workspace.ID, "original")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original copy left in version storage after a failed update: %v", err)
	}
	if after := libraryItemFor(t, service, item.EntityID); after.Edited {
		t.Fatal("failed update marked the mod edited")
	}
}

func TestPlayPicksUpScheduledEditsImmediately(t *testing.T) {
	service := newLibrarySyncTestService(t)
	archivePath := filepath.Join(service.config.LibraryDir, "dummy.zip")
	_, detail := openEditedMod(t, service, archivePath)
	current, _ := service.ReadWorkspaceFile(detail.Workspace.ID, "lua/ge/extensions/dummy.lua")
	if err := service.WriteWorkspaceFile(detail.Workspace.ID, "lua/ge/extensions/dummy.lua", "return { fixed = true }\n", current.SHA256); err != nil {
		t.Fatal(err)
	}
	// Play's preview applies pending edits without waiting for the background
	// delay; an empty selection is enough to trigger it.
	_, _ = service.ResolvePlaySelection(nil, nil)
	if got := zipMember(t, archivePath, "lua/ge/extensions/dummy.lua"); got != "return { fixed = true }\n" {
		t.Fatalf("library mod at Play time holds %q, want the saved edit", got)
	}
}

func fileSHAForTest(t *testing.T, path string) string {
	t.Helper()
	sha, err := computeFileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

// ---------------------------------------------------------------------------
// Folder (unpacked) mod helpers
// ---------------------------------------------------------------------------

// writeTestFolder creates a small folder mod at modDir.
func writeTestFolder(t *testing.T, modDir string, files map[string]string) {
	t.Helper()
	for relative, content := range files {
		dest := filepath.Join(modDir, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// indexTestFolder manually indexes a folder mod via UpsertArchive.
func indexTestFolder(t *testing.T, service *AppService, modDir string) {
	t.Helper()
	ctx := context.Background()
	manifest, err := modkit.Inspect(ctx, modDir)
	if err != nil {
		t.Fatal("inspect folder:", err)
	}
	contentID, err := modkit.SourceContentID(ctx, modDir)
	if err != nil {
		t.Fatal("source content ID:", err)
	}
	manifest.FullSHA256 = contentID
	stat, err := os.Stat(modDir)
	if err != nil {
		t.Fatal(err)
	}
	// Find which scan root contains this mod.
	root := ""
	for _, r := range service.config.ScanRoots {
		if pathWithin(modDir, r) {
			root = r
			break
		}
	}
	if root == "" {
		root = filepath.Dir(modDir)
	}
	if _, err := service.store.UpsertArchive(ctx, "", root, modDir, stat.Size(), stat.ModTime(), manifest, nil); err != nil {
		t.Fatal("upsert folder:", err)
	}
}

// openEditedFolderMod indexes a small folder mod at modDir and opens it in ModMaker.
func openEditedFolderMod(t *testing.T, service *AppService, modDir string) (LibraryItem, WorkspaceDetail) {
	t.Helper()
	writeTestFolder(t, modDir, map[string]string{
		"mod_info/dummy/info.json":    `{"title":"Dummy","tag_line":"before"}`,
		"lua/ge/extensions/dummy.lua": "return {}\n",
	})
	indexTestFolder(t, service, modDir)
	items, err := service.ListLibrary("", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if samePath(item.ArchivePath, modDir) {
			detail, err := service.CreateWorkspace(item.EntityID)
			if err != nil {
				t.Fatal("open in ModMaker:", err)
			}
			return item, detail
		}
	}
	t.Fatalf("%s was not indexed", modDir)
	return LibraryItem{}, WorkspaceDetail{}
}

func folderFileContent(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func folderFileMtime(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime().UnixNano()
}

// ---------------------------------------------------------------------------
// Folder mod tests (acceptance criteria a–e)
// ---------------------------------------------------------------------------

// (a) Edit one file → sync writes only that file into the folder; other files untouched.
func TestFolderModSyncWritesOnlyChangedFile(t *testing.T) {
	service := newLibrarySyncTestService(t)
	modDir := filepath.Join(service.config.LibraryDir, "unpacked", "dummy")
	item, detail := openEditedFolderMod(t, service, modDir)

	// Record the untouched file's mtime before editing.
	infoPath := filepath.Join(modDir, "mod_info", "dummy", "info.json")
	infoBefore := folderFileMtime(t, infoPath)
	infoBytesBefore := folderFileContent(t, infoPath)

	editWorkspaceFile(t, service, detail.Workspace.ID, "lua/ge/extensions/dummy.lua", "return { fixed = true }\n")
	if err := service.syncWorkspaceLibrary(context.Background(), detail.Workspace.ID, "you"); err != nil {
		t.Fatal("update library:", err)
	}

	// The edited file must be written to the folder.
	got := folderFileContent(t, filepath.Join(modDir, "lua", "ge", "extensions", "dummy.lua"))
	if got != "return { fixed = true }\n" {
		t.Fatalf("library folder holds %q, want the saved edit", got)
	}

	// The untouched file must be byte-identical and its mtime unchanged.
	if content := folderFileContent(t, infoPath); content != infoBytesBefore {
		t.Fatalf("untouched info.json changed: %q", content)
	}
	if mtime := folderFileMtime(t, infoPath); mtime != infoBefore {
		t.Fatalf("untouched info.json mtime changed: %d → %d", infoBefore, mtime)
	}

	// Only the original of the edited file should be kept.
	versionsDir := filepath.Join(service.config.DataDir, "versions", detail.Workspace.ID)
	keptOriginal := filepath.Join(versionsDir, "original", "files", "lua", "ge", "extensions", "dummy.lua")
	if folderFileContent(t, keptOriginal) != "return {}\n" {
		t.Fatal("kept original has wrong content")
	}
	// The info.json original should NOT have been kept (it wasn't overwritten).
	keptInfo := filepath.Join(versionsDir, "original", "files", "mod_info", "dummy", "info.json")
	if _, err := os.Stat(keptInfo); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("info.json should not have been kept as original since it wasn't changed")
	}

	// Library state should be synced with one changed file.
	refreshed, err := service.GetWorkspace(detail.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Library.State != LibraryStateSynced || refreshed.Library.ChangedFiles != 1 {
		t.Fatalf("library status = %+v, want synced with 1 changed file", refreshed.Library)
	}
	// SourcePath stays at the library folder.
	if !samePath(refreshed.Workspace.SourcePath, modDir) {
		t.Fatalf("sourcePath = %s, want the library folder %s", refreshed.Workspace.SourcePath, modDir)
	}

	// Edited badge on the library item.
	after := libraryItemFor(t, service, item.EntityID)
	if !after.Edited || after.HistoryCount != 1 {
		t.Fatalf("library mod after edit = edited %v history %d; want edited with one saved version", after.Edited, after.HistoryCount)
	}
}

// (b) Restore Original → folder file back, added file removed, deleted file returns.
//     Restore saved version → edited bytes again.
func TestFolderModRestoreOriginalAndSavedVersion(t *testing.T) {
	service := newLibrarySyncTestService(t)
	modDir := filepath.Join(service.config.LibraryDir, "unpacked", "dummy")
	item, detail := openEditedFolderMod(t, service, modDir)

	// Edit a file, add a new file, and delete a file.
	editWorkspaceFile(t, service, detail.Workspace.ID, "lua/ge/extensions/dummy.lua", "return { fixed = true }\n")
	editWorkspaceFile(t, service, detail.Workspace.ID, "lua/ge/extensions/extra.lua", "return {}\n")
	// Delete info.json from workspace.
	wsInfoPath := filepath.Join(detail.Workspace.FilesRoot, "mod_info", "dummy", "info.json")
	if err := os.Remove(wsInfoPath); err != nil {
		t.Fatal(err)
	}
	if err := service.workspaceChanged(context.Background(), detail.Workspace.ID, "you"); err != nil {
		t.Fatal(err)
	}
	_, _ = service.takePendingLibrarySync(detail.Workspace.ID)
	if err := service.syncWorkspaceLibrary(context.Background(), detail.Workspace.ID, "you"); err != nil {
		t.Fatal("sync:", err)
	}

	// Verify the folder state after sync.
	editedLua := folderFileContent(t, filepath.Join(modDir, "lua", "ge", "extensions", "dummy.lua"))
	if editedLua != "return { fixed = true }\n" {
		t.Fatalf("folder holds %q after edit", editedLua)
	}
	extraPath := filepath.Join(modDir, "lua", "ge", "extensions", "extra.lua")
	if _, err := os.Stat(extraPath); err != nil {
		t.Fatalf("added file not in folder: %v", err)
	}
	deletedInfoPath := filepath.Join(modDir, "mod_info", "dummy", "info.json")
	if _, err := os.Stat(deletedInfoPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deleted info.json still in folder")
	}

	versions, _ := service.ListModVersions(item.EntityID)
	savedID := versions[0].ID

	// Restore Original.
	if _, err := service.RestoreModVersion(item.EntityID, modVersionOriginalID); err != nil {
		t.Fatal("restore original:", err)
	}

	// Verify folder returns to original state.
	restoredLua := folderFileContent(t, filepath.Join(modDir, "lua", "ge", "extensions", "dummy.lua"))
	if restoredLua != "return {}\n" {
		t.Fatalf("folder after restore holds %q, want original", restoredLua)
	}
	restoredInfo := folderFileContent(t, deletedInfoPath)
	if restoredInfo != `{"title":"Dummy","tag_line":"before"}` {
		t.Fatalf("deleted info.json not restored: %q", restoredInfo)
	}
	if _, err := os.Stat(extraPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("added file should be removed after restoring original")
	}

	// Restore the saved version → edited bytes again.
	if _, err := service.RestoreModVersion(item.EntityID, savedID); err != nil {
		t.Fatal("restore saved:", err)
	}
	if got := folderFileContent(t, filepath.Join(modDir, "lua", "ge", "extensions", "dummy.lua")); got != "return { fixed = true }\n" {
		t.Fatalf("folder after restoring saved version holds %q", got)
	}
}

// (c) Outside change to another file between saves is kept; outside change to
//     the same file → ModMaker wins, outside version kept under replaced.
func TestFolderModOutsideChangesAdoptedAndConflictsKept(t *testing.T) {
	service := newLibrarySyncTestService(t)
	modDir := filepath.Join(service.config.LibraryDir, "unpacked", "dummy")
	_, detail := openEditedFolderMod(t, service, modDir)

	// First sync to establish folder-state.
	editWorkspaceFile(t, service, detail.Workspace.ID, "lua/ge/extensions/dummy.lua", "return { fixed = true }\n")
	if err := service.syncWorkspaceLibrary(context.Background(), detail.Workspace.ID, "you"); err != nil {
		t.Fatal(err)
	}

	// Simulate an outside change to info.json (a file the workspace didn't touch).
	infoPath := filepath.Join(modDir, "mod_info", "dummy", "info.json")
	if err := os.WriteFile(infoPath, []byte(`{"title":"Dummy","tag_line":"from outside"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate an outside change to dummy.lua (same file the workspace changed).
	luaPath := filepath.Join(modDir, "lua", "ge", "extensions", "dummy.lua")
	if err := os.WriteFile(luaPath, []byte("return { outside = true }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Edit another workspace file and sync again.
	editWorkspaceFile(t, service, detail.Workspace.ID, "lua/ge/extensions/extra.lua", "return {}\n")
	if err := service.syncWorkspaceLibrary(context.Background(), detail.Workspace.ID, "you"); err != nil {
		t.Fatal(err)
	}

	// The outside-only change to info.json should be adopted into the workspace.
	wsInfo, err := service.ReadWorkspaceFile(detail.Workspace.ID, "mod_info/dummy/info.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wsInfo.Content, "from outside") {
		t.Fatalf("workspace did not adopt outside change: %q", wsInfo.Content)
	}

	// ModMaker's version of dummy.lua wins in the folder.
	if got := folderFileContent(t, luaPath); got != "return { fixed = true }\n" {
		t.Fatalf("folder dummy.lua = %q, want ModMaker version", got)
	}

	// The outside version should be kept under replaced/.
	versionsDir := filepath.Join(service.config.DataDir, "versions", detail.Workspace.ID)
	replacedDirs, _ := filepath.Glob(filepath.Join(versionsDir, "replaced", "*", "files", "lua", "ge", "extensions", "dummy.lua"))
	if len(replacedDirs) == 0 {
		t.Fatal("outside version of dummy.lua not kept under replaced/")
	}
	replacedContent := folderFileContent(t, replacedDirs[0])
	if replacedContent != "return { outside = true }\n" {
		t.Fatalf("replaced content = %q, want outside version", replacedContent)
	}
}

// (d) Folder under mods/repo read-only, under mods/unpacked editable.
func TestFolderModRepoReadOnlyUnpackedEditable(t *testing.T) {
	service := newLibrarySyncTestService(t)

	// A folder mod under mods/repo should be read-only.
	repoDir := filepath.Join(service.config.ActiveModsDir, "repo", "mymod")
	writeTestFolder(t, repoDir, map[string]string{
		"mod_info/mymod/info.json": `{"title":"RepoMod"}`,
		"lua/ge/extensions/m.lua": "return {}\n",
	})
	indexTestFolder(t, service, repoDir)
	items, _ := service.ListLibrary("", "", "", "", "")
	var repoItem LibraryItem
	for _, item := range items {
		if samePath(item.ArchivePath, repoDir) {
			repoItem = item
			break
		}
	}
	if repoItem.EntityID == "" {
		t.Fatal("repo mod not found in library")
	}
	repoDetail, err := service.CreateWorkspace(repoItem.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if repoDetail.Library.State != LibraryStateReadOnly {
		t.Fatalf("repo folder mod status = %+v, want read-only", repoDetail.Library)
	}

	// A folder mod under mods/unpacked should be editable.
	unpackedDir := filepath.Join(service.config.ActiveModsDir, "unpacked", "mymod2")
	writeTestFolder(t, unpackedDir, map[string]string{
		"mod_info/mymod2/info.json": `{"title":"UnpackedMod"}`,
		"lua/ge/extensions/m2.lua": "return {}\n",
	})
	indexTestFolder(t, service, unpackedDir)
	items, _ = service.ListLibrary("", "", "", "", "")
	var unpackedItem LibraryItem
	for _, item := range items {
		if samePath(item.ArchivePath, unpackedDir) {
			unpackedItem = item
			break
		}
	}
	if unpackedItem.EntityID == "" {
		t.Fatal("unpacked mod not found in library")
	}
	unpackedDetail, err := service.CreateWorkspace(unpackedItem.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if unpackedDetail.Library.State == LibraryStateReadOnly {
		t.Fatalf("unpacked folder mod should be editable, got read-only")
	}
}

// (e) Changes diff after a save still lists the edit against the original.
func TestFolderModChangesDiffAfterSaveShowsOriginalDiff(t *testing.T) {
	service := newLibrarySyncTestService(t)
	modDir := filepath.Join(service.config.LibraryDir, "unpacked", "dummy")
	_, detail := openEditedFolderMod(t, service, modDir)

	editWorkspaceFile(t, service, detail.Workspace.ID, "lua/ge/extensions/dummy.lua", "return { fixed = true }\n")
	if err := service.syncWorkspaceLibrary(context.Background(), detail.Workspace.ID, "you"); err != nil {
		t.Fatal(err)
	}

	// After the folder has been written, the Changes view should still show
	// the diff against the original, not an empty diff.
	changes, err := service.WorkspaceDiff(detail.Workspace.ID)
	if err != nil {
		t.Fatal("diff:", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %+v", len(changes), changes)
	}
	if changes[0].Path != "lua/ge/extensions/dummy.lua" || changes[0].Type != "modified" {
		t.Fatalf("unexpected change: %+v", changes[0])
	}
}

// Inspector metadata edit on a folder mod without a workspace.
func TestFolderModInspectorEditWithoutWorkspace(t *testing.T) {
	service := newLibrarySyncTestService(t)
	modDir := filepath.Join(service.config.LibraryDir, "unpacked", "dummy")
	writeTestFolder(t, modDir, map[string]string{
		"mod_info/dummy/info.json":    `{"title":"Dummy","tag_line":"before"}`,
		"lua/ge/extensions/dummy.lua": "return {}\n",
	})
	indexTestFolder(t, service, modDir)
	items, _ := service.ListLibrary("", "", "", "", "")
	var item LibraryItem
	for _, it := range items {
		if samePath(it.ArchivePath, modDir) {
			item = it
			break
		}
	}
	if item.EntityID == "" {
		t.Fatal("mod not found")
	}

	// Edit description without a workspace.
	description := "edited by inspector"
	if _, err := service.UpdateLibraryItemDetails(item.EntityID, LibraryItemDetailsUpdate{Description: description}); err != nil {
		t.Fatal("inspector edit:", err)
	}

	// Verify the folder was rewritten.
	infoContent := folderFileContent(t, filepath.Join(modDir, "mod_info", "dummy", "info.json"))
	if !strings.Contains(infoContent, description) {
		t.Fatalf("info.json = %q, want inspector edit", infoContent)
	}
}
