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
	file, err := os.Open(asset.Path)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != asset.SizeBytes || filepath.Ext(asset.Path) == "" {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", asset.MIME)
	response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	response.Header().Set("ETag", `"`+asset.SHA256+`"`)
	http.ServeContent(response, request, filepath.Base(asset.Path), info.ModTime(), file)
}
