package modkit

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
)

const maxCachedImageBytes = 64 << 20

type CachedAsset struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	MIME      string `json:"mime"`
	Extension string `json:"extension"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	SizeBytes int64  `json:"sizeBytes"`
}

func ExtractImage(archivePath, memberPath, cacheRoot string) (CachedAsset, error) {
	normalizedTarget, err := normalizeArchivePath(memberPath)
	if err != nil {
		return CachedAsset{}, err
	}

	var data []byte
	kind, kindErr := SourceKindOf(archivePath)
	if kindErr != nil {
		return CachedAsset{}, kindErr
	}
	if kind == SourceFolder {
		raw, _, readErr := readFolderMember(archivePath, normalizedTarget, maxCachedImageBytes)
		if readErr != nil {
			return CachedAsset{}, fmt.Errorf("read image: %w", readErr)
		}
		data = raw
	} else {
		reader, zipErr := zip.OpenReader(archivePath)
		if zipErr != nil {
			return CachedAsset{}, fmt.Errorf("open ZIP: %w", zipErr)
		}
		defer reader.Close()

		var selected *zip.File
		for _, file := range reader.File {
			name, pathErr := normalizeArchivePath(file.Name)
			if pathErr == nil && strings.EqualFold(name, normalizedTarget) {
				selected = file
				break
			}
		}
		if selected == nil {
			return CachedAsset{}, fmt.Errorf("image entry not found: %s", memberPath)
		}
		if selected.UncompressedSize64 > maxCachedImageBytes {
			return CachedAsset{}, fmt.Errorf("image is %d bytes; limit is %d", selected.UncompressedSize64, maxCachedImageBytes)
		}
		raw, readErr := readZipEntry(selected, maxCachedImageBytes)
		if readErr != nil {
			return CachedAsset{}, fmt.Errorf("read image: %w", readErr)
		}
		data = raw
	}

	extension := strings.ToLower(filepath.Ext(normalizedTarget))
	mime := imageExtensions[extension]
	if mime == "" {
		return CachedAsset{}, fmt.Errorf("unsupported thumbnail format: %s", extension)
	}
	width, height := 0, 0
	if config, _, decodeErr := image.DecodeConfig(bytes.NewReader(data)); decodeErr == nil {
		width, height = config.Width, config.Height
		if width <= 0 || height <= 0 || width > 16_384 || height > 16_384 || int64(width)*int64(height) > 268_435_456 {
			return CachedAsset{}, fmt.Errorf("unsafe image dimensions: %dx%d", width, height)
		}
	}

	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:])
	directory := filepath.Join(cacheRoot, id[:2])
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return CachedAsset{}, err
	}
	destination := filepath.Join(directory, id+extension)
	if _, err := os.Stat(destination); os.IsNotExist(err) {
		temporary, createErr := os.CreateTemp(directory, id+"-*.tmp")
		if createErr != nil {
			return CachedAsset{}, createErr
		}
		temporaryName := temporary.Name()
		keep := false
		defer func() {
			_ = temporary.Close()
			if !keep {
				_ = os.Remove(temporaryName)
			}
		}()
		if _, err := temporary.Write(data); err != nil {
			return CachedAsset{}, err
		}
		if err := temporary.Sync(); err != nil {
			return CachedAsset{}, err
		}
		if err := temporary.Close(); err != nil {
			return CachedAsset{}, err
		}
		if err := os.Rename(temporaryName, destination); err != nil {
			if _, statErr := os.Stat(destination); statErr != nil {
				return CachedAsset{}, err
			}
		}
		keep = true
	}
	return CachedAsset{ID: id, Path: destination, MIME: mime, Extension: extension, Width: width, Height: height, SizeBytes: int64(len(data))}, nil
}
