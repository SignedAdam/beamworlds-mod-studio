package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var assetIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type cacheAssetHandler struct {
	store    *Store
	cacheDir string
	fallback http.Handler
}

func (handler *cacheAssetHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if !strings.HasPrefix(request.URL.Path, "/cache/") {
		handler.fallback.ServeHTTP(response, request)
		return
	}
	assetID := strings.TrimPrefix(request.URL.Path, "/cache/")
	if !assetIDPattern.MatchString(assetID) || request.Method != http.MethodGet && request.Method != http.MethodHead {
		http.NotFound(response, request)
		return
	}
	asset, err := handler.store.GetAsset(context.Background(), assetID)
	if err != nil || !pathWithin(asset.Path, handler.cacheDir) {
		http.NotFound(response, request)
		return
	}
	sourceFile, err := os.Open(asset.Path)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	sourceInfo, err := sourceFile.Stat()
	_ = sourceFile.Close()
	if err != nil || !sourceInfo.Mode().IsRegular() || sourceInfo.Size() != asset.SizeBytes || filepath.Ext(asset.Path) == "" {
		http.NotFound(response, request)
		return
	}

	servedPath := asset.Path
	contentType := asset.MIME
	etag := `"` + asset.SHA256 + `"`
	if request.URL.Query().Get("size") == "thumb" {
		if derivative, thumbnailErr := ensureAssetThumbnail(asset); thumbnailErr == nil &&
			derivative != asset.Path && pathWithin(derivative, handler.cacheDir) {
			servedPath = derivative
			contentType = "image/jpeg"
			etag = `"` + asset.SHA256 + `-thumb"`
		}
	}
	file, err := os.Open(servedPath)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || filepath.Ext(servedPath) == "" {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", contentType)
	response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	response.Header().Set("ETag", etag)
	http.ServeContent(response, request, filepath.Base(servedPath), info.ModTime(), file)
}
