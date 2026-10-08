package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLargePNGThumbnailDerivativeIsSmallJPEG(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sourceImage := image.NewRGBA(image.Rect(0, 0, 4000, 2000))
	for y := range 2000 {
		for x := range 4000 {
			sourceImage.SetRGBA(x, y, colorRGBAFrom16(25*257, 45*257, 75*257))
		}
	}
	var source bytes.Buffer
	if err := png.Encode(&source, sourceImage); err != nil {
		t.Fatal(err)
	}
	sha := sha256.Sum256(source.Bytes())
	assetID := hex.EncodeToString(sha[:])
	sourcePath := filepath.Join(root, assetID[:2], assetID+".png")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, source.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	derivative, err := ensureAssetThumbnail(AssetRecord{SHA256: assetID, Path: sourcePath, MIME: "image/png", Width: 4000, Height: 2000, SizeBytes: int64(source.Len())})
	if err != nil {
		t.Fatal(err)
	}
	if derivative == sourcePath {
		t.Fatal("large source unexpectedly selected as its own thumbnail")
	}
	thumbnailFile, err := os.ReadFile(derivative)
	if err != nil {
		t.Fatal(err)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(thumbnailFile))
	if err != nil {
		t.Fatal(err)
	}
	if format != "jpeg" || config.Width != 256 || config.Height != 128 {
		t.Fatalf("thumbnail = %s %dx%d, want JPEG 256x128", format, config.Width, config.Height)
	}
	// A solid-colour PNG compresses to a few KB, and how far depends on the Go
	// version, so check the thumbnail against a fixed budget instead of a ratio.
	if len(thumbnailFile) > 16<<10 {
		t.Fatalf("thumbnail bytes = %d, want at most 16 KiB", len(thumbnailFile))
	}
	if _, err := jpeg.Decode(bytes.NewReader(thumbnailFile)); err != nil {
		t.Fatal(err)
	}
}

func TestCacheHandlerThumbnailAndOriginalVariants(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "library.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	original := encodeTestPNG(t, 800, 400, color.RGBA{R: 32, G: 52, B: 72, A: 255})
	hash := sha256.Sum256(original)
	assetID := hex.EncodeToString(hash[:])
	sourcePath := filepath.Join(root, assetID[:2], assetID+".png")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.registerArtworkAsset(context.Background(), AssetRecord{SHA256: assetID, Path: sourcePath, MIME: "image/png", Width: 800, Height: 400, SizeBytes: int64(len(original))}); err != nil {
		t.Fatal(err)
	}
	handler := &cacheAssetHandler{store: store, cacheDir: root, fallback: http.NotFoundHandler()}
	request := func(query string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/cache/"+assetID+query, nil))
		return recorder
	}
	plain := request("")
	if plain.Code != http.StatusOK || !bytes.Equal(plain.Body.Bytes(), original) {
		t.Fatalf("original response = %d (%d bytes)", plain.Code, plain.Body.Len())
	}
	if got := plain.Header().Get("ETag"); got != `"`+assetID+`"` {
		t.Fatalf("original ETag = %q", got)
	}
	thumbnail := request("?size=thumb")
	if thumbnail.Code != http.StatusOK || thumbnail.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumbnail response = %d %q", thumbnail.Code, thumbnail.Header().Get("Content-Type"))
	}
	if bytes.Equal(thumbnail.Body.Bytes(), original) || thumbnail.Header().Get("ETag") != `"`+assetID+`-thumb"` {
		t.Fatal("thumbnail response did not use a distinct derivative")
	}
	if _, _, err := image.Decode(bytes.NewReader(thumbnail.Body.Bytes())); err != nil {
		t.Fatalf("decode thumbnail response: %v", err)
	}
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/cache/"+assetID+"?size=thumb", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != thumbnail.Header().Get("ETag") {
		t.Fatalf("thumbnail HEAD response = %d body=%d etag=%q", head.Code, head.Body.Len(), head.Header().Get("ETag"))
	}
	unknown := request("?size=unexpected")
	if unknown.Code != http.StatusOK || !bytes.Equal(unknown.Body.Bytes(), original) || unknown.Header().Get("ETag") != plain.Header().Get("ETag") {
		t.Fatal("unknown size did not fall back to the original")
	}
}

func TestSmallPNGThumbnailRequestServesOriginal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	imageData := encodeTestPNG(t, 128, 96, colorRGBAFrom16(80*257, 90*257, 100*257))
	hash := sha256.Sum256(imageData)
	assetID := hex.EncodeToString(hash[:])
	sourcePath := filepath.Join(root, assetID[:2], assetID+".png")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, imageData, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(root, "library.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.registerArtworkAsset(context.Background(), AssetRecord{SHA256: assetID, Path: sourcePath, MIME: "image/png", Width: 128, Height: 96, SizeBytes: int64(len(imageData))}); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	(&cacheAssetHandler{store: store, cacheDir: root, fallback: http.NotFoundHandler()}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/cache/"+assetID+"?size=thumb", nil))
	if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), imageData) {
		t.Fatalf("small thumbnail response = %d (%d bytes)", recorder.Code, recorder.Body.Len())
	}
	if got := recorder.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("small thumbnail content type = %q", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(sourcePath), assetID+".thumb.jpg")); !os.IsNotExist(err) {
		t.Fatalf("small source generated derivative: %v", err)
	}
}

func encodeTestPNG(t *testing.T, width, height int, pixel color.RGBA) []byte {
	t.Helper()
	imageData := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			imageData.SetRGBA(x, y, pixel)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, imageData); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}
