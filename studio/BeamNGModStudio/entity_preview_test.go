package main

import (
	"archive/zip"
	"context"
	"image/color"
	"os"
	"path/filepath"
	"testing"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

func TestEntityPreviewArchiveRoundTripAndManualRescan(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cacheDir := filepath.Join(root, "cache", "images")
	archivePath := filepath.Join(root, "mods", "preview.zip")
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatal(err)
	}
	automaticData := encodeTestPNG(t, 320, 180, color.RGBA{R: 220, G: 30, B: 30, A: 255})
	alternateData := encodeTestPNG(t, 360, 200, color.RGBA{R: 30, G: 80, B: 220, A: 255})
	writePreviewTestZIP(t, archivePath, map[string][]byte{
		"mod_info/preview.png":   automaticData,
		"mod_info/alternate.png": alternateData,
	})
	autoCached, err := modkit.ExtractImage(archivePath, "mod_info/preview.png", cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	modified := time.Unix(1_700_000_000, 0).UTC()
	manifest := modkit.Manifest{
		SchemaVersion:      modkit.SchemaVersion,
		AnalyzerVersion:    modkit.AnalyzerVersion,
		AnalyzedAt:         modified,
		ArchivePath:        archivePath,
		Filename:           filepath.Base(archivePath),
		SizeBytes:          info.Size(),
		ModifiedAt:         modified,
		CentralFingerprint: "preview-test-fingerprint",
		FullSHA256:         "preview-test-sha",
		ValidArchive:       true,
		Kind:               modkit.KindVehicle,
		Namespaces:         map[string][]string{"vehicles": {"preview-test"}},
		Members: []modkit.ArchiveMember{
			{Path: "mod_info/preview.png", UncompressedBytes: uint64(len(automaticData))},
			{Path: "mod_info/alternate.png", UncompressedBytes: uint64(len(alternateData))},
		},
		Images: []modkit.ImageCandidate{
			{Path: "mod_info/preview.png", Role: "mod-preview", UncompressedBytes: uint64(len(automaticData)), MIME: "image/png"},
			{Path: "mod_info/alternate.png", Role: "gallery", UncompressedBytes: uint64(len(alternateData)), MIME: "image/png"},
		},
		SelectedImagePath: "mod_info/preview.png",
	}
	store, err := OpenStore(filepath.Join(root, "library.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	scanID, err := store.BeginScan(ctx, []string{filepath.Dir(archivePath)})
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.ApplyScanBatch(ctx, scanID, []string{filepath.Dir(archivePath)}, nil, []ScanArchive{{
		Root:        filepath.Dir(archivePath),
		ArchivePath: archivePath,
		SizeBytes:   info.Size(),
		Modified:    modified,
		Manifest:    manifest,
		Asset:       &AssetRecord{SHA256: autoCached.ID, Path: autoCached.Path, MIME: autoCached.MIME, Width: autoCached.Width, Height: autoCached.Height, SizeBytes: autoCached.SizeBytes},
	}}, 1, 1, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("apply initial scan: %v (%d items)", err, len(items))
	}
	service := &AppService{config: AppConfig{ImageCacheDir: cacheDir}, store: store}
	candidates, err := service.EntityPreviewCandidates(items[0].EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || !candidates[0].Automatic || candidates[0].MemberPath != "mod_info/preview.png" {
		t.Fatalf("preview candidates = %#v", candidates)
	}
	if candidates[0].Width != 320 || candidates[0].Height != 180 || candidates[1].Width != 360 || candidates[1].Height != 200 {
		t.Fatalf("candidate dimensions = %#v", candidates)
	}
	initial := items[0]
	selected, err := service.SetEntityPreviewFromArchive(initial.EntityID, "mod_info/alternate.png")
	if err != nil {
		t.Fatal(err)
	}
	if selected.ThumbnailURL == initial.ThumbnailURL || selected.ThumbnailURL == "" {
		t.Fatalf("selected thumbnail URL = %q, initial %q", selected.ThumbnailURL, initial.ThumbnailURL)
	}
	var selectedSHA string
	if err := store.db.QueryRowContext(ctx, `SELECT asset_sha256 FROM entity_assets WHERE entity_id=? AND role='thumbnail' AND ordinal=0`, initial.EntityID).Scan(&selectedSHA); err != nil {
		t.Fatal(err)
	}
	if selectedSHA == autoCached.ID || selectedSHA == "" {
		t.Fatalf("selected asset SHA = %q, automatic %q", selectedSHA, autoCached.ID)
	}
	rescanned, err := store.BeginScan(ctx, []string{filepath.Dir(archivePath)})
	if err != nil {
		t.Fatal(err)
	}
	rescannedItems, err := store.ApplyScanBatch(ctx, rescanned, []string{filepath.Dir(archivePath)}, nil, []ScanArchive{{
		Root:        filepath.Dir(archivePath),
		ArchivePath: archivePath,
		SizeBytes:   info.Size(),
		Modified:    modified,
		Manifest:    manifest,
		Asset:       &AssetRecord{SHA256: autoCached.ID, Path: autoCached.Path, MIME: autoCached.MIME, Width: autoCached.Width, Height: autoCached.Height, SizeBytes: autoCached.SizeBytes},
	}}, 1, 1, 0)
	if err != nil || len(rescannedItems) != 1 {
		t.Fatalf("apply rescan: %v (%d items)", err, len(rescannedItems))
	}
	if rescannedItems[0].ThumbnailURL != selected.ThumbnailURL {
		t.Fatalf("manual thumbnail changed after rescan: got %q want %q", rescannedItems[0].ThumbnailURL, selected.ThumbnailURL)
	}
	reset, err := service.ResetEntityPreview(initial.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if reset.ThumbnailURL != initial.ThumbnailURL {
		t.Fatalf("reset thumbnail URL = %q, automatic %q", reset.ThumbnailURL, initial.ThumbnailURL)
	}
}

func writePreviewTestZIP(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, data := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
