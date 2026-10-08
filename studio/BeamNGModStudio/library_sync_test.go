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
