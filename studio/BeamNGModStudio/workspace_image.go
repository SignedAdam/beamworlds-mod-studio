package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"path"
	"strings"

	modkit "github.com/SignedAdam/beamworlds-modkit"
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
)

const (
	// DDS textures with a full mip chain decode only the level that fits the preview,
	// but the whole file still has to be read; 4K uncompressed RGBA with mips is ~85 MiB.
	maxWorkspaceImageBytes int64 = 128 << 20
	// Browser-native formats are shipped to the webview untouched as a data URL.
	maxWorkspaceImagePassthroughBytes int64 = 32 << 20
	workspaceImagePreviewEdge               = 2048
)

var workspaceImageMIMEs = map[string]string{
	".bmp":  "image/bmp",
	".dds":  "image/vnd-ms.dds",
	".gif":  "image/gif",
	".jpeg": "image/jpeg",
	".jpg":  "image/jpeg",
	".png":  "image/png",
	".svg":  "image/svg+xml",
	".webp": "image/webp",
}

// WorkspaceImageFile is a viewer-ready rendering of a workspace image. Width and Height
// describe the source image; DataURL may be a smaller decoded rendition (Downscaled).
type WorkspaceImageFile struct {
	Path       string `json:"path"`
	MIME       string `json:"mime"`
	Format     string `json:"format"`
	DataURL    string `json:"dataUrl"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	SizeBytes  int64  `json:"sizeBytes"`
	SHA256     string `json:"sha256"`
	Downscaled bool   `json:"downscaled"`
}

func (service *AppService) ReadWorkspaceImage(workspaceID, relativePath string) (WorkspaceImageFile, error) {
	ctx := context.Background()
	workspace, err := service.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return WorkspaceImageFile{}, err
	}
	relativePath, err = cleanWorkspaceRelativePath(relativePath)
	if err != nil {
		return WorkspaceImageFile{}, err
	}
	extension := strings.ToLower(path.Ext(relativePath))
	mime, ok := workspaceImageMIMEs[extension]
	if !ok {
		return WorkspaceImageFile{}, fmt.Errorf("%s is not a supported image format", path.Base(relativePath))
	}
	workspaceLock := service.agents.workspaceToolMutex(workspace.ID)
	workspaceLock.Lock()
	data, err := modkit.ReadWorkspaceBytesContext(ctx, workspace.FilesRoot, relativePath, maxWorkspaceImageBytes)
	workspaceLock.Unlock()
	if err != nil {
		return WorkspaceImageFile{}, err
	}
	return renderWorkspaceImage(relativePath, extension, mime, data)
}

func renderWorkspaceImage(relativePath, extension, mime string, data []byte) (WorkspaceImageFile, error) {
	sum := sha256.Sum256(data)
	result := WorkspaceImageFile{
		Path:      relativePath,
		MIME:      mime,
		Format:    strings.ToUpper(strings.TrimPrefix(extension, ".")),
		SizeBytes: int64(len(data)),
		SHA256:    hex.EncodeToString(sum[:]),
	}

	if extension == ".dds" {
		decoded, width, height, format, err := decodeDDS(data, workspaceImagePreviewEdge)
		if err != nil {
			return WorkspaceImageFile{}, err
		}
		preview := fitImage(decoded, workspaceImagePreviewEdge)
		var encoded bytes.Buffer
		encoder := png.Encoder{CompressionLevel: png.BestSpeed}
		if err := encoder.Encode(&encoded, preview); err != nil {
			return WorkspaceImageFile{}, fmt.Errorf("encode DDS preview: %w", err)
		}
		bounds := preview.Bounds()
		result.Format = "DDS · " + format
		result.Width, result.Height = width, height
		result.Downscaled = bounds.Dx() != width || bounds.Dy() != height
		result.DataURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
		return result, nil
	}

	if int64(len(data)) > maxWorkspaceImagePassthroughBytes {
		return WorkspaceImageFile{}, fmt.Errorf("image is %d bytes; preview limit is %d", len(data), maxWorkspaceImagePassthroughBytes)
	}
	if extension != ".svg" {
		// Dimensions are informational; the webview still renders formats Go cannot decode.
		if config, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			result.Width, result.Height = config.Width, config.Height
		}
	}
	result.DataURL = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	return result, nil
}

// fitImage scales source so its longest edge is at most longestEdge, keeping alpha.
func fitImage(source image.Image, longestEdge int) image.Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= longestEdge && height <= longestEdge {
		return source
	}
	if width >= height {
		height = max(1, height*longestEdge/width)
		width = longestEdge
	} else {
		width = max(1, width*longestEdge/height)
		height = longestEdge
	}
	destination := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.ApproxBiLinear.Scale(destination, destination.Bounds(), source, bounds, draw.Src, nil)
	return destination
}
