package modkit

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectIndexesSharedAssetFiles(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "./ASSETS/texture.DDS", want: true},
		{name: "art/vehicle/model.DAE", want: true},
		{name: "common/materials.materials.json", want: true},
		{name: "vehicles\\common\\audio.ogg", want: true},
		{name: "vehicles/coupe/texture.dds", want: false},
		{name: "levels/test/texture.dds", want: false},
		{name: "assets/readme.txt", want: false},
		{name: "assets/texture.dds/", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized := normalizeSharedAssetName(test.name)
			got := normalized != "" && isSharedAssetName(normalized)
			if got != test.want {
				t.Fatalf("shared asset classification for %q = %v, want %v (normalized %q)", test.name, got, test.want, normalized)
			}
		})
	}

	archivePath := writeInspectArchive(t, []string{
		"Release/assets/",
		"Release/assets/model.DAE",
		"Release/art/materials.materials.json",
		"Release/common/sound.ogg",
		"Release/vehicles/common/texture.DDS",
		"Release/vehicles/coupe/texture.dds",
		"Release/levels/test/texture.dds",
		"Release/assets/readme.txt",
		"Release/assets/texture.dds/",
	})
	manifest, err := Inspect(context.Background(), archivePath)
	if err != nil {
		t.Fatalf("inspect shared asset fixture: %v", err)
	}
	if manifest.SharedAssets == nil {
		t.Fatal("inspect did not persist shared asset stats")
	}
	if manifest.SharedAssets.Files != 4 {
		t.Fatalf("shared asset file count = %d, want 4", manifest.SharedAssets.Files)
	}
}

func TestInspectIndexesZeroSharedAssets(t *testing.T) {
	archivePath := writeInspectArchive(t, []string{"Release/info.txt"})
	manifest, err := Inspect(context.Background(), archivePath)
	if err != nil {
		t.Fatalf("inspect zero shared asset fixture: %v", err)
	}
	if manifest.SharedAssets == nil {
		t.Fatal("inspect did not persist zero shared asset stats")
	}
	if manifest.SharedAssets.Files != 0 {
		t.Fatalf("shared asset file count = %d, want 0", manifest.SharedAssets.Files)
	}
}

func writeInspectArchive(t *testing.T, names []string) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "shared-assets.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive fixture: %v", err)
	}
	writer := zip.NewWriter(file)
	for _, name := range names {
		if strings.HasSuffix(name, "/") {
			header := &zip.FileHeader{Name: name}
			header.SetMode(os.ModeDir | 0o755)
			if _, err := writer.CreateHeader(header); err != nil {
				t.Fatalf("create directory entry %q: %v", name, err)
			}
			continue
		}
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create file entry %q: %v", name, err)
		}
		if _, err := io.WriteString(entry, "fixture"); err != nil {
			t.Fatalf("write file entry %q: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive fixture: %v", err)
	}
	return archivePath
}
