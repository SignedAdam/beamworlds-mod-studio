package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

const (
	entityPreviewSelectionPrefix = "entity_preview_selection:"
	maxEntityPreviewFileBytes    = int64(64 << 20)
)

type EntityPreviewCandidate struct {
	MemberPath string `json:"memberPath"`
	Label      string `json:"label"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	SizeBytes  int64  `json:"sizeBytes"`
	AssetSHA   string `json:"assetSha"`
	Selected   bool   `json:"selected"`
	Automatic  bool   `json:"automatic"`
}

func entityPreviewSelectionKey(entityID string) string {
	return entityPreviewSelectionPrefix + strings.TrimSpace(entityID)
}

func (service *AppService) EntityPreviewCandidates(entityID string) ([]EntityPreviewCandidate, error) {
	ctx := context.Background()
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return nil, errors.New("entity ID is required")
	}
	item, err := service.store.GetLibraryItem(ctx, entityID)
	if err != nil {
		return nil, err
	}
	artwork, err := service.modArtworkCandidates(ctx, item)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(artwork)+1)
	for _, candidate := range artwork {
		seen[strings.ToLower(candidate.Path)] = struct{}{}
	}
	if automatic := strings.TrimSpace(item.Manifest.SelectedImagePath); automatic != "" {
		normalized, normalizeErr := modkit.NormalizeArchivePath(automatic)
		if normalizeErr == nil {
			if _, exists := seen[strings.ToLower(normalized)]; !exists {
				artwork = append(artwork, CollectionArtworkCandidate{Path: normalized, Name: artworkCandidateName(normalized, "")})
			}
		}
	}

	selection, selectionErr := service.store.entityPreviewSelection(ctx, entityID)
	if selectionErr != nil && !errors.Is(selectionErr, sql.ErrNoRows) {
		return nil, selectionErr
	}
	currentAsset, currentAssetErr := service.store.entityThumbnailAsset(ctx, entityID)
	if currentAssetErr != nil && !errors.Is(currentAssetErr, sql.ErrNoRows) {
		return nil, currentAssetErr
	}
	manualArchivePath := strings.TrimPrefix(strings.TrimSpace(selection), "archive:")
	manualFileSelection := strings.HasPrefix(strings.TrimSpace(selection), "file:")
	automaticPath := strings.TrimSpace(item.Manifest.SelectedImagePath)
	if normalized, normalizeErr := modkit.NormalizeArchivePath(automaticPath); normalizeErr == nil {
		automaticPath = normalized
	}
	metadata := make(map[string]modkit.ImageCandidate, len(item.Manifest.Images))
	for _, imageCandidate := range item.Manifest.Images {
		normalized, normalizeErr := modkit.NormalizeArchivePath(imageCandidate.Path)
		if normalizeErr == nil {
			metadata[strings.ToLower(normalized)] = imageCandidate
		}
	}

	result := make([]EntityPreviewCandidate, 0, len(artwork))
	for _, discovered := range artwork {
		normalized, normalizeErr := modkit.NormalizeArchivePath(discovered.Path)
		if normalizeErr != nil || normalized == "" {
			continue
		}
		candidate := EntityPreviewCandidate{MemberPath: normalized, Label: discovered.Name}
		if imageCandidate, ok := metadata[strings.ToLower(normalized)]; ok {
			candidate.Width = imageCandidate.Width
			candidate.Height = imageCandidate.Height
			if imageCandidate.UncompressedBytes <= uint64(^uint64(0)>>1) {
				candidate.SizeBytes = int64(imageCandidate.UncompressedBytes)
			}
		}
		if candidate.SizeBytes == 0 {
			if member, memberErr := findArchiveMember(item.Manifest, normalized); memberErr == nil && member.UncompressedBytes <= uint64(^uint64(0)>>1) {
				candidate.SizeBytes = int64(member.UncompressedBytes)
			}
		}
		if candidate.Width <= 0 || candidate.Height <= 0 {
			if member, memberErr := findArchiveMember(item.Manifest, normalized); memberErr == nil &&
				member.UncompressedBytes <= uint64(maxEntityPreviewFileBytes) {
				if data, truncated, readErr := modkit.ReadArchiveMemberLimited(item.ArchivePath, normalized, maxEntityPreviewFileBytes); readErr == nil && !truncated {
					if config, _, decodeErr := image.DecodeConfig(bytes.NewReader(data)); decodeErr == nil {
						candidate.Width, candidate.Height = config.Width, config.Height
					}
				}
			}
		}
		candidate.Automatic = automaticPath != "" && strings.EqualFold(normalized, automaticPath)
		candidate.Selected = manualArchivePath != "" && strings.EqualFold(normalized, manualArchivePath)
		if !manualFileSelection && strings.TrimSpace(selection) == "" && candidate.Automatic {
			candidate.Selected = true
		}
		if candidate.Selected && strings.TrimSpace(currentAsset.SHA256) != "" {
			candidate.AssetSHA = strings.ToLower(currentAsset.SHA256)
		}
		result = append(result, candidate)
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].Automatic != result[right].Automatic {
			return result[left].Automatic
		}
		leftPath, rightPath := strings.ToLower(result[left].MemberPath), strings.ToLower(result[right].MemberPath)
		if leftPath != rightPath {
			return leftPath < rightPath
		}
		return result[left].MemberPath < result[right].MemberPath
	})
	return result, nil
}
func (service *AppService) SetEntityPreviewFromArchive(entityID, memberPath string) (LibraryItem, error) {
	ctx := context.Background()
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return LibraryItem{}, errors.New("entity ID is required")
	}
	item, err := service.store.GetLibraryItem(ctx, entityID)
	if err != nil {
		return LibraryItem{}, err
	}
	normalized, err := modkit.NormalizeArchivePath(memberPath)
	if err != nil {
		return LibraryItem{}, err
	}
	if !strings.EqualFold(normalized, item.Manifest.SelectedImagePath) {
		normalized, err = service.validateArtworkMember(item, normalized)
		if err != nil {
			return LibraryItem{}, err
		}
	}
	cached, err := modkit.ExtractImage(item.ArchivePath, normalized, service.config.ImageCacheDir)
	if err != nil {
		return LibraryItem{}, fmt.Errorf("cache preview %q: %w", normalized, err)
	}
	asset := AssetRecord{SHA256: strings.ToLower(strings.TrimSpace(cached.ID)), Path: cached.Path, MIME: cached.MIME, Width: cached.Width, Height: cached.Height, SizeBytes: cached.SizeBytes}
	if err := validateCachedPreviewAsset(asset); err != nil {
		return LibraryItem{}, fmt.Errorf("preview member %q is not a decodable image: %w", normalized, err)
	}
	if !pathWithin(asset.Path, service.config.ImageCacheDir) {
		return LibraryItem{}, errors.New("cached preview is outside the image cache")
	}
	_, _ = ensureAssetThumbnail(asset)
	return service.store.SetEntityPreview(ctx, entityID, asset, "archive:"+normalized)
}

func (service *AppService) SetEntityPreviewFromFile(entityID, sourcePath string) (LibraryItem, error) {
	ctx := context.Background()
	entityID = strings.TrimSpace(entityID)
	sourcePath = strings.TrimSpace(sourcePath)
	if entityID == "" {
		return LibraryItem{}, errors.New("entity ID is required")
	}
	if sourcePath == "" {
		return LibraryItem{}, errors.New("preview source path is required")
	}
	if _, err := service.store.GetLibraryItem(ctx, entityID); err != nil {
		return LibraryItem{}, err
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return LibraryItem{}, fmt.Errorf("open preview source: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return LibraryItem{}, fmt.Errorf("stat preview source: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return LibraryItem{}, errors.New("preview source is not a regular file")
	}
	if info.Size() <= 0 || info.Size() > maxEntityPreviewFileBytes {
		_ = file.Close()
		return LibraryItem{}, fmt.Errorf("preview source is %d bytes; limit is %d", info.Size(), maxEntityPreviewFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxEntityPreviewFileBytes+1))
	_ = file.Close()
	if err != nil {
		return LibraryItem{}, fmt.Errorf("read preview source: %w", err)
	}
	if int64(len(data)) > maxEntityPreviewFileBytes {
		return LibraryItem{}, fmt.Errorf("preview source exceeds %d bytes", maxEntityPreviewFileBytes)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		if err == nil {
			err = fmt.Errorf("unsupported image format %q", format)
		}
		return LibraryItem{}, fmt.Errorf("preview source is not a PNG or JPEG: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return LibraryItem{}, errors.New("preview source has invalid dimensions")
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return LibraryItem{}, fmt.Errorf("decode preview source: %w", err)
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	extension, mime := ".png", "image/png"
	if format == "jpeg" {
		extension, mime = ".jpg", "image/jpeg"
	}
	destination := filepath.Join(service.config.ImageCacheDir, sha[:2], sha+extension)
	if !pathWithin(destination, service.config.ImageCacheDir) {
		return LibraryItem{}, errors.New("cached preview destination is outside the image cache")
	}
	if existing, statErr := os.Stat(destination); statErr != nil {
		if !errors.Is(statErr, os.ErrNotExist) {
			return LibraryItem{}, statErr
		}
		if err := writeFileAtomic(destination, data, 0o644); err != nil {
			return LibraryItem{}, fmt.Errorf("cache preview source: %w", err)
		}
	} else if !existing.Mode().IsRegular() || existing.Size() != int64(len(data)) {
		if err := writeFileAtomic(destination, data, 0o644); err != nil {
			return LibraryItem{}, fmt.Errorf("replace cached preview source: %w", err)
		}
	}
	asset := AssetRecord{SHA256: sha, Path: destination, MIME: mime, Width: config.Width, Height: config.Height, SizeBytes: int64(len(data))}
	_, _ = ensureAssetThumbnail(asset)
	return service.store.SetEntityPreview(ctx, entityID, asset, "file:"+sha)
}

func (service *AppService) ResetEntityPreview(entityID string) (LibraryItem, error) {
	ctx := context.Background()
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return LibraryItem{}, errors.New("entity ID is required")
	}
	item, err := service.store.GetLibraryItem(ctx, entityID)
	if err != nil {
		return LibraryItem{}, err
	}
	if strings.TrimSpace(item.Manifest.SelectedImagePath) == "" {
		return service.store.ResetEntityPreview(ctx, entityID, nil)
	}
	cached, err := modkit.ExtractImage(item.ArchivePath, item.Manifest.SelectedImagePath, service.config.ImageCacheDir)
	if err != nil {
		return LibraryItem{}, fmt.Errorf("reset preview: %w", err)
	}
	asset := AssetRecord{SHA256: strings.ToLower(strings.TrimSpace(cached.ID)), Path: cached.Path, MIME: cached.MIME, Width: cached.Width, Height: cached.Height, SizeBytes: cached.SizeBytes}
	if err := validateCachedPreviewAsset(asset); err != nil {
		return LibraryItem{}, fmt.Errorf("reset preview is not a decodable image: %w", err)
	}
	if !pathWithin(asset.Path, service.config.ImageCacheDir) {
		return LibraryItem{}, errors.New("cached preview is outside the image cache")
	}
	_, _ = ensureAssetThumbnail(asset)
	return service.store.ResetEntityPreview(ctx, entityID, &asset)
}

func validateCachedPreviewAsset(asset AssetRecord) error {
	if !assetIDPattern.MatchString(strings.ToLower(strings.TrimSpace(asset.SHA256))) {
		return errors.New("cached asset SHA-256 is invalid")
	}
	if strings.TrimSpace(asset.Path) == "" {
		return errors.New("cached asset path is empty")
	}
	file, err := os.Open(asset.Path)
	if err != nil {
		return err
	}
	_, _, decodeErr := image.Decode(file)
	_ = file.Close()
	return decodeErr
}

func (s *Store) entityPreviewSelection(ctx context.Context, entityID string) (string, error) {
	var selection string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, entityPreviewSelectionKey(entityID)).Scan(&selection)
	return strings.TrimSpace(selection), err
}

func (s *Store) SetEntityPreview(ctx context.Context, entityID string, asset AssetRecord, selection string) (LibraryItem, error) {
	entityID = strings.TrimSpace(entityID)
	selection = strings.TrimSpace(selection)
	asset.SHA256 = strings.ToLower(strings.TrimSpace(asset.SHA256))
	if entityID == "" {
		return LibraryItem{}, errors.New("entity ID is required")
	}
	if !assetIDPattern.MatchString(asset.SHA256) || strings.TrimSpace(asset.Path) == "" || asset.SizeBytes <= 0 {
		return LibraryItem{}, errors.New("preview asset metadata is incomplete")
	}
	if selection == "" {
		return LibraryItem{}, errors.New("preview selection marker is required")
	}
	if err := s.writeEntityPreview(ctx, entityID, &asset, selection); err != nil {
		return LibraryItem{}, err
	}
	return s.GetLibraryItem(ctx, entityID)
}

func (s *Store) ResetEntityPreview(ctx context.Context, entityID string, asset *AssetRecord) (LibraryItem, error) {
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return LibraryItem{}, errors.New("entity ID is required")
	}
	if asset != nil {
		asset.SHA256 = strings.ToLower(strings.TrimSpace(asset.SHA256))
		if !assetIDPattern.MatchString(asset.SHA256) || strings.TrimSpace(asset.Path) == "" || asset.SizeBytes <= 0 {
			return LibraryItem{}, errors.New("preview asset metadata is incomplete")
		}
	}
	if err := s.writeEntityPreview(ctx, entityID, asset, ""); err != nil {
		return LibraryItem{}, err
	}
	return s.GetLibraryItem(ctx, entityID)
}

func (s *Store) writeEntityPreview(ctx context.Context, entityID string, asset *AssetRecord, selection string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if asset != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO assets(sha256,path,mime,width,height,size_bytes,created_at)
			VALUES(?,?,?,?,?,?,?) ON CONFLICT(sha256) DO UPDATE SET path=excluded.path,mime=excluded.mime,
			width=excluded.width,height=excluded.height,size_bytes=excluded.size_bytes`,
			asset.SHA256, asset.Path, asset.MIME, asset.Width, asset.Height, asset.SizeBytes, nowUTC()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO entity_assets(entity_id,asset_sha256,role,ordinal)
			VALUES(?,?, 'thumbnail', 0) ON CONFLICT(entity_id,role,ordinal) DO UPDATE SET asset_sha256=excluded.asset_sha256`, entityID, asset.SHA256); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `DELETE FROM entity_assets WHERE entity_id=? AND role='thumbnail' AND ordinal=0`, entityID); err != nil {
		return err
	}
	if selection == "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, entityPreviewSelectionKey(entityID)); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, entityPreviewSelectionKey(entityID), selection); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE entities SET updated_at=? WHERE id=?`, nowUTC(), entityID)
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
	return tx.Commit()
}
