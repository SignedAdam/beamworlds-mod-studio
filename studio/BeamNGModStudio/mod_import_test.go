package main

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newModImportTestService(t *testing.T) (*AppService, *Store) {
	t.Helper()
	root := t.TempDir()
	config := AppConfig{
		DataDir:         root,
		DatabasePath:    filepath.Join(root, "modstudio.sqlite"),
		ImageCacheDir:   filepath.Join(root, "cache", "images"),
		WorkspaceDir:    filepath.Join(root, "workspaces"),
		ExportDir:       filepath.Join(root, "exports"),
		ProfileDir:      filepath.Join(root, "profiles"),
		LibraryDir:      filepath.Join(root, "library"),
		ScanConcurrency: 1,
	}
	for _, directory := range []string{config.ImageCacheDir, config.WorkspaceDir, config.ExportDir, config.ProfileDir, config.LibraryDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("create %s: %v", directory, err)
		}
	}
	store, err := OpenStore(config.DatabasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &AppService{config: config, store: store, library: NewLibraryEngine(store, config, func(string, any) {})}, store
}

func writeModImportTestArchive(t *testing.T, path, title string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create archive directory: %v", err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(file)
	metadata, err := writer.Create("mod_info/info.json")
	if err == nil {
		_, err = metadata.Write([]byte(`{"title":` + `"` + title + `"` + `,"author":"Import Test"}`))
	}
	if err == nil {
		member, memberErr := writer.Create("vehicles/import_test/main.jbeam")
		if memberErr != nil {
			err = memberErr
		} else {
			_, err = member.Write([]byte(`{}`))
		}
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("write archive: %v", err)
	}
}

func TestImportModsPreservesSourceAndIndexesArchive(t *testing.T) {
	service, store := newModImportTestService(t)
	source := filepath.Join(t.TempDir(), "downloads", "source.zip")
	writeModImportTestArchive(t, source, "Source Vehicle")
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.ImportMods([]string{source})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.ImportedCount != 1 || result.ExistingCount != 0 || len(result.Failures) != 0 || len(result.Items) != 1 {
		t.Fatalf("import result = %#v", result)
	}
	destination := filepath.Join(service.config.LibraryDir, "source.zip")
	afterSource, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterSource) != string(before) {
		t.Fatal("source archive changed during import")
	}
	copied, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read copied archive: %v", err)
	}
	if string(copied) != string(before) {
		t.Fatal("copied archive differs from source")
	}
	items, err := store.ListLibrary(context.Background(), "all", "all", "", "all", "active")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !samePath(items[0].ArchivePath, destination) {
		t.Fatalf("indexed items = %#v, want %s", items, destination)
	}
}

func TestImportModsUsesSafeConflictNamesAndRecognizesReimport(t *testing.T) {
	service, store := newModImportTestService(t)
	sourceOne := filepath.Join(t.TempDir(), "one", "same.zip")
	sourceTwo := filepath.Join(t.TempDir(), "two", "same.zip")
	writeModImportTestArchive(t, sourceOne, "First Vehicle")
	writeModImportTestArchive(t, sourceTwo, "Second Vehicle")
	firstBytes, err := os.ReadFile(sourceOne)
	if err != nil {
		t.Fatal(err)
	}

	first, err := service.ImportMods([]string{sourceOne})
	if err != nil || first.ImportedCount != 1 || len(first.Failures) != 0 {
		t.Fatalf("first import = %#v, err=%v", first, err)
	}
	second, err := service.ImportMods([]string{sourceTwo})
	if err != nil || second.ImportedCount != 1 || len(second.Failures) != 0 {
		t.Fatalf("conflicting import = %#v, err=%v", second, err)
	}
	conflictPath := filepath.Join(service.config.LibraryDir, "same (1).zip")
	conflictBytes, err := os.ReadFile(conflictPath)
	if err != nil {
		t.Fatalf("read conflict copy: %v", err)
	}
	secondSource, err := os.ReadFile(sourceTwo)
	if err != nil {
		t.Fatal(err)
	}
	if string(conflictBytes) != string(secondSource) {
		t.Fatal("conflicting archive was not copied to a new filename")
	}
	items, err := store.ListLibrary(context.Background(), "all", "all", "", "all", "active")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("conflicting import pruned unrelated library row: %#v", items)
	}
	reimport, err := service.ImportMods([]string{sourceOne})
	if err != nil || reimport.ImportedCount != 0 || reimport.ExistingCount != 1 || len(reimport.Failures) != 0 {
		t.Fatalf("reimport = %#v, err=%v", reimport, err)
	}
	originalCopy, err := os.ReadFile(filepath.Join(service.config.LibraryDir, "same.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if string(originalCopy) != string(firstBytes) {
		t.Fatal("filename collision overwrote the original destination archive")
	}
}

func TestImportModsRejectsCorruptArchiveWithoutPublishingArtifact(t *testing.T) {
	service, store := newModImportTestService(t)
	source := filepath.Join(t.TempDir(), "corrupt.zip")
	if err := os.WriteFile(source, []byte("not a ZIP archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := service.ImportMods([]string{source})
	if err != nil {
		t.Fatalf("corrupt archive returned global error: %v", err)
	}
	if len(result.Failures) != 1 || result.ImportedCount != 0 || result.ExistingCount != 0 {
		t.Fatalf("corrupt import result = %#v", result)
	}
	entries, err := os.ReadDir(service.config.LibraryDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("destination contains failed-import artifacts: %#v", entries)
	}
	items, err := store.ListLibrary(context.Background(), "all", "all", "", "all", "active")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("corrupt archive was indexed: %#v", items)
	}
}

func TestBrowseModImportDirectoryPersistsMRUAndSkipsFailedNavigation(t *testing.T) {
	service, store := newModImportTestService(t)
	root := t.TempDir()
	directories := []string{filepath.Join(root, "one"), filepath.Join(root, "two"), filepath.Join(root, "three")}
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, directory := range directories[:2] {
		if _, err := service.BrowseModImportDirectory(directory); err != nil {
			t.Fatalf("browse %s: %v", directory, err)
		}
	}
	missing := filepath.Join(root, "does-not-exist")
	if _, err := service.BrowseModImportDirectory(missing); err == nil {
		t.Fatal("missing directory browse unexpectedly succeeded")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(service.config.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedService := &AppService{config: service.config, store: reopened, library: NewLibraryEngine(reopened, service.config, func(string, any) {})}
	result, err := reopenedService.BrowseModImportDirectory(directories[2])
	if err != nil {
		t.Fatalf("browse after reopen: %v", err)
	}
	if len(result.RecentLocations) != 3 {
		t.Fatalf("recent locations = %#v, want three successful directories", result.RecentLocations)
	}
	if !samePath(result.RecentLocations[0].Path, directories[2]) || !samePath(result.RecentLocations[1].Path, directories[1]) || !samePath(result.RecentLocations[2].Path, directories[0]) {
		t.Fatalf("recent order = %#v", result.RecentLocations)
	}
	for _, location := range result.RecentLocations {
		if strings.Contains(location.Path, "does-not-exist") {
			t.Fatal("failed navigation was persisted as a recent location")
		}
	}
}
