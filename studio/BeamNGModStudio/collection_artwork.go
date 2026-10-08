package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"

	modkit "github.com/SignedAdam/beamworlds-modkit"
	_ "golang.org/x/image/webp"
)

const (
	maxCollectionArtworkBytes  = int64(8 << 20)
	maxCollectionArtworkPixels = int64(64 << 20)
	collectionCoverWidth       = 1200
	collectionCoverHeight      = 675
)

// GetModArtwork returns every bounded image candidate discovered while the
// archive was indexed. Candidates are intentionally returned without reading
// archive members; only assets already present in the cache get a URL.
func (service *AppService) GetModArtwork(entityID string) ([]CollectionArtworkCandidate, error) {
	ctx := context.Background()
	item, err := service.store.GetLibraryItem(ctx, strings.TrimSpace(entityID))
	if err != nil {
		return nil, err
	}
	return service.modArtworkCandidates(ctx, item)
}

// CacheModArtwork extracts one discovered image through modkit's bounded ZIP
// reader and registers the content-addressed file in the existing asset cache.
// The operation never changes collection membership or the mod's selected
// thumbnail.
func (service *AppService) CacheModArtwork(entityID, memberPath string) (CollectionCoverImage, error) {
	ctx := context.Background()
	item, err := service.store.GetLibraryItem(ctx, strings.TrimSpace(entityID))
	if err != nil {
		return CollectionCoverImage{}, err
	}
	normalized, err := service.validateArtworkMember(item, memberPath)
	if err != nil {
		return CollectionCoverImage{}, err
	}
	if strings.EqualFold(normalized, item.Manifest.SelectedImagePath) {
		if asset, assetErr := service.store.entityThumbnailAsset(ctx, item.EntityID); assetErr == nil &&
			assetIDPattern.MatchString(strings.ToLower(asset.SHA256)) &&
			pathWithin(asset.Path, service.config.ImageCacheDir) {
			if info, statErr := os.Stat(asset.Path); statErr == nil && info.Mode().IsRegular() && info.Size() == asset.SizeBytes {
				return CollectionCoverImage{AssetID: strings.ToLower(asset.SHA256), FocalX: 0.5, FocalY: 0.5}, nil
			}
		}
	}
	if info, statErr := os.Stat(item.ArchivePath); statErr != nil {
		return CollectionCoverImage{}, fmt.Errorf("artwork source is unavailable: %w", statErr)
	} else if !info.IsDir() && !info.Mode().IsRegular() {
		return CollectionCoverImage{}, errors.New("artwork source is not a file or folder")
	}
	member, err := findArchiveMember(item.Manifest, normalized)
	if err != nil {
		return CollectionCoverImage{}, err
	}
	if member.UncompressedBytes > uint64(maxCollectionArtworkBytes) {
		return CollectionCoverImage{}, fmt.Errorf("artwork image %q is %d bytes; limit is %d", normalized, member.UncompressedBytes, maxCollectionArtworkBytes)
	}
	cached, err := modkit.ExtractImage(item.ArchivePath, normalized, service.config.ImageCacheDir)
	if err != nil {
		return CollectionCoverImage{}, fmt.Errorf("cache artwork %q: %w", normalized, err)
	}
	asset := AssetRecord{
		SHA256:    strings.ToLower(strings.TrimSpace(cached.ID)),
		Path:      cached.Path,
		MIME:      cached.MIME,
		Width:     cached.Width,
		Height:    cached.Height,
		SizeBytes: cached.SizeBytes,
	}
	if !assetIDPattern.MatchString(asset.SHA256) {
		return CollectionCoverImage{}, errors.New("cached artwork returned an invalid asset identity")
	}
	if err := service.store.registerArtworkAsset(ctx, asset); err != nil {
		return CollectionCoverImage{}, fmt.Errorf("register cached artwork: %w", err)
	}
	return CollectionCoverImage{AssetID: asset.SHA256, FocalX: 0.5, FocalY: 0.5}, nil
}

// SetCollectionCover validates and persists a stable artwork recipe. Single
// and collage recipes are rendered once into the same content-addressed cache
// used by library thumbnails; cards therefore never reopen source archives.
func (service *AppService) SetCollectionCover(collectionID string, cover CollectionCover) (CollectionDetail, error) {
	ctx := context.Background()
	collectionID = strings.TrimSpace(collectionID)
	if collectionID == "" {
		return CollectionDetail{}, errors.New("collection ID is required")
	}
	if _, err := service.store.collectionExists(ctx, collectionID); err != nil {
		return CollectionDetail{}, err
	}
	normalized, err := normalizeCollectionCover(cover)
	if err != nil {
		return CollectionDetail{}, err
	}
	if err := service.validateCollectionCoverAssets(ctx, normalized); err != nil {
		return CollectionDetail{}, err
	}
	assetSHA := ""
	switch normalized.Mode {
	case "automatic":
		// The core resolver chooses the deterministic effective-member preview.
	case "single", "collage":
		asset, renderErr := service.renderCollectionDerivative(ctx, normalized.Images)
		if renderErr != nil {
			return CollectionDetail{}, renderErr
		}
		if err := service.store.registerArtworkAsset(ctx, asset); err != nil {
			return CollectionDetail{}, fmt.Errorf("register %s cover artwork: %w", normalized.Mode, err)
		}
		assetSHA = asset.SHA256
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return CollectionDetail{}, fmt.Errorf("encode collection cover: %w", err)
	}
	if err := service.store.updateCollectionCover(ctx, collectionID, string(encoded), assetSHA); err != nil {
		return CollectionDetail{}, err
	}
	return service.store.CollectionDetail(ctx, collectionID)
}

func (service *AppService) modArtworkCandidates(ctx context.Context, item LibraryItem) ([]CollectionArtworkCandidate, error) {
	seen := make(map[string]struct{})
	result := make([]CollectionArtworkCandidate, 0, len(item.Manifest.Images)+len(item.Manifest.Variants))
	thumbnailAsset := ""
	if item.Manifest.SelectedImagePath != "" {
		if asset, err := service.store.entityThumbnailAsset(ctx, item.EntityID); err == nil {
			thumbnailAsset = asset.SHA256
		}
	}
	add := func(memberPath, name string) {
		normalized, err := modkit.NormalizeArchivePath(memberPath)
		if err != nil || normalized == "" {
			return
		}
		key := strings.ToLower(normalized)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		assetID := ""
		url := ""
		if thumbnailAsset != "" && strings.EqualFold(normalized, item.Manifest.SelectedImagePath) {
			assetID = thumbnailAsset
			url = "/cache/" + thumbnailAsset
		}
		result = append(result, CollectionArtworkCandidate{Path: normalized, Name: name, URL: url, AssetID: assetID})
	}
	for _, candidate := range item.Manifest.Images {
		role := strings.TrimSpace(candidate.Role)
		name := artworkCandidateName(candidate.Path, role)
		add(candidate.Path, name)
	}
	for _, variant := range item.Manifest.Variants {
		if strings.TrimSpace(variant.ThumbnailPath) == "" {
			continue
		}
		label := strings.TrimSpace(variant.Configuration)
		if label == "" {
			label = strings.TrimSpace(variant.BaseName)
		}
		if label == "" {
			label = "Vehicle variant"
		}
		add(variant.ThumbnailPath, label+" preview")
	}
	return result, nil
}

func artworkCandidateName(memberPath, role string) string {
	base := filepath.Base(filepath.FromSlash(strings.ReplaceAll(memberPath, "\\", "/")))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(base, "_", " "), "-", " ")), " ")
	if base == "" {
		base = "Preview"
	}
	if role != "" {
		return base + " · " + strings.ReplaceAll(role, "-", " ")
	}
	return base
}

func (service *AppService) validateArtworkMember(item LibraryItem, memberPath string) (string, error) {
	normalized, err := modkit.NormalizeArchivePath(memberPath)
	if err != nil {
		return "", err
	}
	for _, candidate := range item.Manifest.Images {
		if strings.EqualFold(candidate.Path, normalized) {
			return normalized, nil
		}
	}
	for _, variant := range item.Manifest.Variants {
		if strings.TrimSpace(variant.ThumbnailPath) != "" && strings.EqualFold(variant.ThumbnailPath, normalized) {
			return normalized, nil
		}
	}
	return "", fmt.Errorf("artwork member %q was not discovered in this mod", normalized)
}

func normalizeCollectionCover(cover CollectionCover) (CollectionCover, error) {
	mode := strings.ToLower(strings.TrimSpace(cover.Mode))
	if mode == "" {
		mode = "automatic"
	}
	if mode != "automatic" && mode != "single" && mode != "collage" {
		return CollectionCover{}, fmt.Errorf("unsupported collection cover mode %q", mode)
	}
	images := make([]CollectionCoverImage, len(cover.Images))
	copy(images, cover.Images)
	if mode == "automatic" {
		if len(images) != 0 {
			return CollectionCover{}, errors.New("automatic covers cannot include selected images")
		}
		return CollectionCover{Mode: mode, Images: []CollectionCoverImage{}}, nil
	}
	if mode == "single" && len(images) != 1 {
		return CollectionCover{}, errors.New("single covers require exactly one image")
	}
	if mode == "collage" && (len(images) < 2 || len(images) > 9) {
		return CollectionCover{}, errors.New("collages require between two and nine images")
	}
	seen := make(map[string]struct{}, len(images))
	for index := range images {
		image := &images[index]
		image.AssetID = strings.ToLower(strings.TrimSpace(image.AssetID))
		if !assetIDPattern.MatchString(image.AssetID) {
			return CollectionCover{}, fmt.Errorf("cover image %d has an invalid cached asset ID", index+1)
		}
		if _, exists := seen[image.AssetID]; exists {
			return CollectionCover{}, fmt.Errorf("cover image %d duplicates another selected image", index+1)
		}
		seen[image.AssetID] = struct{}{}
		if math.IsNaN(image.FocalX) || math.IsInf(image.FocalX, 0) || image.FocalX < 0 || image.FocalX > 1 ||
			math.IsNaN(image.FocalY) || math.IsInf(image.FocalY, 0) || image.FocalY < 0 || image.FocalY > 1 {
			return CollectionCover{}, fmt.Errorf("cover image %d focal position must be between 0 and 1", index+1)
		}
		if image.FocalX == 0 && image.FocalY == 0 {
			// Zero is a valid focal point, so do not replace it; the editor sends
			// 0.5 explicitly for newly selected images.
		}
	}
	return CollectionCover{Mode: mode, Images: images}, nil
}

func (service *AppService) validateCollectionCoverAssets(ctx context.Context, cover CollectionCover) error {
	if cover.Mode == "automatic" {
		return nil
	}
	for index, selection := range cover.Images {
		asset, err := service.store.GetAsset(ctx, selection.AssetID)
		if err != nil {
			return fmt.Errorf("selected artwork %d is unavailable: %w", index+1, err)
		}
		if !strings.HasPrefix(strings.ToLower(asset.MIME), "image/") {
			return fmt.Errorf("selected artwork %d is not an image asset", index+1)
		}
		if !pathWithin(asset.Path, service.config.ImageCacheDir) {
			return fmt.Errorf("selected artwork %d is outside the image cache", index+1)
		}
		info, statErr := os.Stat(asset.Path)
		if statErr != nil {
			return fmt.Errorf("selected artwork %d is unavailable: %w", index+1, statErr)
		}
		if !info.Mode().IsRegular() || info.Size() != asset.SizeBytes || filepath.Ext(asset.Path) == "" {
			return fmt.Errorf("selected artwork %d is corrupt or incomplete", index+1)
		}
	}
	return nil
}

func (s *Store) collectionExists(ctx context.Context, collectionID string) (bool, error) {
	var present int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM collections WHERE id=?`, collectionID).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("collection %q does not exist", collectionID)
	}
	return err == nil && present == 1, err
}

func (s *Store) entityThumbnailAsset(ctx context.Context, entityID string) (AssetRecord, error) {
	var asset AssetRecord
	err := s.db.QueryRowContext(ctx, `SELECT a.sha256,a.path,a.mime,a.width,a.height,a.size_bytes
		FROM entity_assets ea JOIN assets a ON a.sha256=ea.asset_sha256
		WHERE ea.entity_id=? AND ea.role='thumbnail' AND ea.ordinal=0`, entityID).
		Scan(&asset.SHA256, &asset.Path, &asset.MIME, &asset.Width, &asset.Height, &asset.SizeBytes)
	return asset, err
}

func (s *Store) registerArtworkAsset(ctx context.Context, asset AssetRecord) error {
	if !assetIDPattern.MatchString(strings.ToLower(strings.TrimSpace(asset.SHA256))) {
		return errors.New("asset SHA-256 is required")
	}
	if strings.TrimSpace(asset.Path) == "" || strings.TrimSpace(asset.MIME) == "" || asset.SizeBytes <= 0 {
		return errors.New("cached artwork asset metadata is incomplete")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO assets(sha256,path,mime,width,height,size_bytes,created_at)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(sha256) DO UPDATE SET path=excluded.path,mime=excluded.mime,
		width=excluded.width,height=excluded.height,size_bytes=excluded.size_bytes`,
		strings.ToLower(asset.SHA256), asset.Path, asset.MIME, asset.Width, asset.Height, asset.SizeBytes, nowUTC())
	return err
}

func (s *Store) updateCollectionCover(ctx context.Context, collectionID, encoded, assetSHA string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE collections SET cover_json=?,cover_asset_sha=?,updated_at=? WHERE id=?`, encoded, assetSHA, nowUTC(), collectionID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (service *AppService) renderCollectionDerivative(ctx context.Context, images []CollectionCoverImage) (AssetRecord, error) {
	if len(images) < 1 || len(images) > 9 {
		return AssetRecord{}, errors.New("cover artwork requires between one and nine images")
	}
	decoded := make([]image.Image, len(images))
	for index, selection := range images {
		asset, err := service.store.GetAsset(ctx, selection.AssetID)
		if err != nil {
			return AssetRecord{}, fmt.Errorf("selected artwork %d is unavailable: %w", index+1, err)
		}
		if !pathWithin(asset.Path, service.config.ImageCacheDir) {
			return AssetRecord{}, fmt.Errorf("selected artwork %d is outside the image cache", index+1)
		}
		if asset.SizeBytes <= 0 || asset.SizeBytes > maxCollectionArtworkBytes {
			return AssetRecord{}, fmt.Errorf("selected artwork %d exceeds the bounded cache limit", index+1)
		}
		file, err := os.Open(asset.Path)
		if err != nil {
			return AssetRecord{}, fmt.Errorf("open selected artwork %d: %w", index+1, err)
		}
		config, _, err := image.DecodeConfig(file)
		_ = file.Close()
		if err != nil {
			return AssetRecord{}, fmt.Errorf("inspect selected artwork %d: %w", index+1, err)
		}
		if err := validateArtworkDimensions(config.Width, config.Height); err != nil {
			return AssetRecord{}, fmt.Errorf("selected artwork %d: %w", index+1, err)
		}
		file, err = os.Open(asset.Path)
		if err != nil {
			return AssetRecord{}, fmt.Errorf("open selected artwork %d: %w", index+1, err)
		}
		decodedImage, _, err := image.Decode(file)
		_ = file.Close()
		if err != nil {
			return AssetRecord{}, fmt.Errorf("decode selected artwork %d (%s): %w", index+1, asset.MIME, err)
		}
		decoded[index] = decodedImage
	}
	canvas := image.NewRGBA(image.Rect(0, 0, collectionCoverWidth, collectionCoverHeight))
	tiles := collectionCoverTiles(len(images))
	for index, tile := range tiles {
		drawArtworkTile(canvas, decoded[index], tile, images[index].FocalX, images[index].FocalY)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		return AssetRecord{}, fmt.Errorf("encode cover artwork: %w", err)
	}
	data := encoded.Bytes()
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	directory := filepath.Join(service.config.ImageCacheDir, sha[:2])
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return AssetRecord{}, err
	}
	destination := filepath.Join(directory, sha+".png")
	if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
		temporary, err := os.CreateTemp(directory, sha+"-*.tmp")
		if err != nil {
			return AssetRecord{}, err
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
			return AssetRecord{}, err
		}
		if err := temporary.Sync(); err != nil {
			return AssetRecord{}, err
		}
		if err := temporary.Close(); err != nil {
			return AssetRecord{}, err
		}
		if err := os.Rename(temporaryName, destination); err != nil {
			if _, statErr := os.Stat(destination); statErr != nil {
				return AssetRecord{}, err
			}
		}
		keep = true
	} else if err != nil {
		return AssetRecord{}, err
	}
	return AssetRecord{SHA256: sha, Path: destination, MIME: "image/png", Width: collectionCoverWidth, Height: collectionCoverHeight, SizeBytes: int64(len(data))}, nil
}

func validateArtworkDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return errors.New("image dimensions are unavailable")
	}
	if width > 16_384 || height > 16_384 || int64(width)*int64(height) > maxCollectionArtworkPixels {
		return fmt.Errorf("unsafe image dimensions %dx%d", width, height)
	}
	return nil
}

type collectionCoverTile struct{ x, y, width, height int }

func collectionCoverTiles(count int) []collectionCoverTile {
	if count < 1 || count > 9 {
		return nil
	}
	if count == 1 {
		return []collectionCoverTile{{x: 0, y: 0, width: collectionCoverWidth, height: collectionCoverHeight}}
	}
	rows := []int{count}
	switch count {
	case 3:
		rows = []int{1, 2}
	case 4, 6, 8:
		rows = []int{count / 2, count / 2}
	case 5:
		rows = []int{2, 3}
	case 7:
		rows = []int{3, 4}
	case 9:
		rows = []int{3, 3, 3}
	}
	tiles := make([]collectionCoverTile, 0, count)
	y := 0
	for row, columns := range rows {
		height := collectionCoverHeight / len(rows)
		if row == len(rows)-1 {
			height = collectionCoverHeight - y
		}
		for column := range columns {
			x := column * collectionCoverWidth / columns
			width := (column+1)*collectionCoverWidth/columns - x
			tiles = append(tiles, collectionCoverTile{x: x, y: y, width: width, height: height})
		}
		y += height
	}
	return tiles
}

func drawArtworkTile(destination draw.Image, source image.Image, tile collectionCoverTile, focalX, focalY float64) {
	if source == nil || tile.width <= 0 || tile.height <= 0 {
		return
	}
	bounds := source.Bounds()
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()
	if sourceWidth <= 0 || sourceHeight <= 0 {
		return
	}
	if math.IsNaN(focalX) || math.IsInf(focalX, 0) {
		focalX = 0.5
	}
	if math.IsNaN(focalY) || math.IsInf(focalY, 0) {
		focalY = 0.5
	}
	focalX = math.Max(0, math.Min(1, focalX))
	focalY = math.Max(0, math.Min(1, focalY))
	scale := math.Max(float64(tile.width)/float64(sourceWidth), float64(tile.height)/float64(sourceHeight))
	cropWidth := float64(tile.width) / scale
	cropHeight := float64(tile.height) / scale
	centerX := focalX * float64(sourceWidth)
	centerY := focalY * float64(sourceHeight)
	left := centerX - cropWidth/2
	top := centerY - cropHeight/2
	if left < 0 {
		left = 0
	}
	if top < 0 {
		top = 0
	}
	if maxLeft := float64(sourceWidth) - cropWidth; left > maxLeft {
		left = math.Max(0, maxLeft)
	}
	if maxTop := float64(sourceHeight) - cropHeight; top > maxTop {
		top = math.Max(0, maxTop)
	}
	for y := range tile.height {
		sourceY := bounds.Min.Y + int(top+float64(y)/scale)
		if sourceY >= bounds.Max.Y {
			sourceY = bounds.Max.Y - 1
		}
		for x := range tile.width {
			sourceX := bounds.Min.X + int(left+float64(x)/scale)
			if sourceX >= bounds.Max.X {
				sourceX = bounds.Max.X - 1
			}
			destination.Set(tile.x+x, tile.y+y, source.At(sourceX, sourceY))
		}
	}
}
