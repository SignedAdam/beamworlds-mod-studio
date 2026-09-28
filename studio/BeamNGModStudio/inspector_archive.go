package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	modkit "github.com/SignedAdam/beamworlds-modkit"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	maxArchiveTextPreviewBytes  int64 = 512 << 10
	maxArchiveImagePreviewBytes int64 = 8 << 20
	maxArchiveExtractBytes      int64 = modkit.MaxArchiveMemberBytes
)

var inspectorArchiveMu sync.Mutex

var archivePreviewImageMIMEs = map[string]string{
	".gif":  "image/gif",
	".jpeg": "image/jpeg",
	".jpg":  "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
}

var archivePreviewTextMIMEs = map[string]string{
	".cfg":   "text/plain",
	".conf":  "text/plain",
	".csv":   "text/csv",
	".css":   "text/css",
	".htm":   "text/html",
	".html":  "text/html",
	".ini":   "text/plain",
	".jbeam": "application/json",
	".js":    "text/javascript",
	".json":  "application/json",
	".json5": "application/json",
	".lua":   "text/plain",
	".md":    "text/markdown",
	".mis":   "text/plain",
	".pc":    "application/json",
	".toml":  "text/plain",
	".ts":    "text/typescript",
	".tsx":   "text/typescript",
	".txt":   "text/plain",
	".xml":   "application/xml",
	".yaml":  "text/yaml",
	".yml":   "text/yaml",
}

type LibraryItemDetailsUpdate struct {
	Description string `json:"description"`
	Author      string `json:"author"`
	Version     string `json:"version"`
}

type LibraryVariantUpdate struct {
	ConfigPath    string `json:"configPath"`
	Configuration string `json:"configuration"`
	Description   string `json:"description"`
	ConfigType    string `json:"configType"`
	BodyStyle     string `json:"bodyStyle"`
	Drivetrain    string `json:"drivetrain"`
	Transmission  string `json:"transmission"`
	FuelType      string `json:"fuelType"`
	Propulsion    string `json:"propulsion"`
	Power         string `json:"power"`
	Torque        string `json:"torque"`
	Weight        string `json:"weight"`
	Value         string `json:"value"`
	TopSpeed      string `json:"topSpeed"`
}

type ArchiveMemberPreview struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	MIME      string `json:"mime"`
	Text      string `json:"text"`
	DataURL   string `json:"dataUrl"`
	SizeBytes int64  `json:"sizeBytes"`
	Truncated bool   `json:"truncated"`
}

func (service *AppService) UpdateLibraryItemDetails(entityID string, update LibraryItemDetailsUpdate) (EntityDetail, error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	if err := service.requireGameStopped(); err != nil { return EntityDetail{}, err }
	ctx := context.Background()
	item, err := service.store.GetLibraryItem(ctx, strings.TrimSpace(entityID))
	if err != nil {
		return EntityDetail{}, err
	}

	inspectorArchiveMu.Lock()
	defer inspectorArchiveMu.Unlock()

	manifest, err := modkit.Inspect(ctx, item.ArchivePath)
	if err != nil {
		return EntityDetail{}, err
	}
	metadataPath, metadata := primaryMetadataTarget(manifest)
	updates := map[string]any{}
	putMetadataUpdate(updates, metadata, []string{"tag_line", "description", "Description"}, "description", optionalStringUpdate(update.Description))
	putMetadataUpdate(updates, metadata, []string{"username", "author", "Author", "authors"}, "author", optionalStringUpdate(update.Author))
	putMetadataUpdate(updates, metadata, []string{"version_string", "version", "Version"}, "version", optionalStringUpdate(update.Version))
	if err := modkit.RewriteArchiveJSONMember(item.ArchivePath, metadataPath, updates, true, func() error {
		if err := service.retireArchiveReferences(ctx, []string{item.EntityID}); err != nil { return err }
		return service.requireGameStopped()
	}); err != nil {
		return EntityDetail{}, err
	}

	return service.reindexEditedArchive(ctx, item, "metadata_updated", map[string]any{
		"archivePath": item.ArchivePath,
		"memberPath":  metadataPath,
		"path":        metadataPath,
		"fields":      []string{"description", "author", "version"},
	})
}

func (service *AppService) UpdateLibraryVariant(entityID string, update LibraryVariantUpdate) (EntityDetail, error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()
	if err := service.requireGameStopped(); err != nil { return EntityDetail{}, err }
	ctx := context.Background()
	item, err := service.store.GetLibraryItem(ctx, strings.TrimSpace(entityID))
	if err != nil {
		return EntityDetail{}, err
	}
	requestedPath, err := modkit.NormalizeArchivePath(update.ConfigPath)
	if err != nil {
		return EntityDetail{}, err
	}
	if requestedPath == "" {
		return EntityDetail{}, errors.New("variant config path is required")
	}

	inspectorArchiveMu.Lock()
	defer inspectorArchiveMu.Unlock()

	manifest, err := modkit.Inspect(ctx, item.ArchivePath)
	if err != nil {
		return EntityDetail{}, err
	}
	variant, err := findVariantForUpdate(manifest, requestedPath)
	if err != nil {
		return EntityDetail{}, err
	}
	metadataPath, metadata := variantMetadataTarget(manifest, variant)
	updates, err := variantMetadataUpdates(metadata, update)
	if err != nil {
		return EntityDetail{}, err
	}
	if err := modkit.RewriteArchiveJSONMember(item.ArchivePath, metadataPath, updates, true, func() error {
		if err := service.retireArchiveReferences(ctx, []string{item.EntityID}); err != nil { return err }
		return service.requireGameStopped()
	}); err != nil {
		return EntityDetail{}, err
	}

	return service.reindexEditedArchive(ctx, item, "variant_updated", map[string]any{
		"archivePath": item.ArchivePath,
		"memberPath":  metadataPath,
		"path":        metadataPath,
		"configPath":  variant.ConfigPath,
		"fields": []string{
			"configuration", "description", "configType", "bodyStyle", "drivetrain", "transmission", "fuelType", "propulsion",
			"power", "torque", "weight", "value", "topSpeed",
		},
	})
}

func (service *AppService) PreviewLibraryArchiveMember(entityID, memberPath string) (*ArchiveMemberPreview, error) {
	ctx := context.Background()
	item, err := service.store.GetLibraryItem(ctx, strings.TrimSpace(entityID))
	if err != nil {
		return nil, err
	}
	member, err := findArchiveMember(item.Manifest, memberPath)
	if err != nil {
		return nil, err
	}
	if member.UncompressedBytes > uint64(math.MaxInt64) {
		return nil, fmt.Errorf("archive member %q is too large", member.Path)
	}
	preview := &ArchiveMemberPreview{Path: member.Path, SizeBytes: int64(member.UncompressedBytes)}
	if member.Directory {
		preview.Kind = "directory"
		preview.MIME = "inode/directory"
		return preview, nil
	}

	extension := strings.ToLower(path.Ext(member.Path))
	if imageMIME, ok := archivePreviewImageMIMEs[extension]; ok {
		preview.Kind = "image"
		preview.MIME = imageMIME
		if member.UncompressedBytes > uint64(maxArchiveImagePreviewBytes) {
			preview.Truncated = true
			return preview, nil
		}
		data, truncated, readErr := modkit.ReadArchiveMemberLimited(item.ArchivePath, member.Path, maxArchiveImagePreviewBytes)
		if readErr != nil {
			return nil, readErr
		}
		if truncated {
			preview.Truncated = true
			return preview, nil
		}
		preview.DataURL = "data:" + imageMIME + ";base64," + base64.StdEncoding.EncodeToString(data)
		return preview, nil
	}
	if textMIME, ok := archivePreviewTextMIMEs[extension]; ok {
		data, truncated, readErr := modkit.ReadArchiveMemberLimited(item.ArchivePath, member.Path, maxArchiveTextPreviewBytes)
		if readErr != nil {
			return nil, readErr
		}
		if !utf8.Valid(data) {
			preview.Kind = "binary"
			preview.MIME = "application/octet-stream"
			return preview, nil
		}
		preview.Kind = "text"
		preview.MIME = textMIME
		preview.Text = string(data)
		preview.Truncated = truncated
		return preview, nil
	}
	preview.Kind = "binary"
	preview.MIME = "application/octet-stream"
	return preview, nil
}

func (service *AppService) ExtractLibraryArchiveMember(entityID, memberPath string) (string, error) {
	ctx := context.Background()
	item, err := service.store.GetLibraryItem(ctx, strings.TrimSpace(entityID))
	if err != nil {
		return "", err
	}
	member, err := findArchiveMember(item.Manifest, memberPath)
	if err != nil {
		return "", err
	}
	if member.Directory {
		return "", fmt.Errorf("archive member %q is a directory", member.Path)
	}
	if member.UncompressedBytes > uint64(maxArchiveExtractBytes) {
		return "", fmt.Errorf("archive member %q is %d bytes; limit is %d", member.Path, member.UncompressedBytes, maxArchiveExtractBytes)
	}

	destination, err := chooseArchiveExtractionPath(item.ArchivePath, member.Path)
	if err != nil {
		if isDialogCancellation(err) {
			return "", nil
		}
		return "", err
	}
	if destination == "" {
		return "", nil
	}
	destination, err = filepath.Abs(filepath.Clean(destination))
	if err != nil {
		return "", err
	}
	sourcePath, err := filepath.Abs(filepath.Clean(item.ArchivePath))
	if err != nil {
		return "", err
	}
	if samePath(destination, sourcePath) {
		return "", errors.New("extraction destination cannot be the source archive")
	}
	if info, statErr := os.Stat(destination); statErr == nil && info.IsDir() {
		return "", fmt.Errorf("extraction destination is a directory: %s", destination)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	destinationDirectory := filepath.Dir(destination)
	if info, statErr := os.Stat(destinationDirectory); statErr != nil {
		return "", statErr
	} else if !info.IsDir() {
		return "", fmt.Errorf("extraction destination parent is not a directory: %s", destinationDirectory)
	}

	temporary, err := os.CreateTemp(destinationDirectory, "."+filepath.Base(destination)+".*.tmp")
	if err != nil {
		return "", err
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	temporaryClosed := false
	defer func() {
		if !temporaryClosed {
			_ = temporary.Close()
		}
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if _, err := modkit.CopyArchiveMember(item.ArchivePath, member.Path, temporary, maxArchiveExtractBytes); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	temporaryClosed = true
	if err := os.Rename(temporaryName, destination); err != nil {
		return "", fmt.Errorf("save extracted member: %w", err)
	}
	removeTemporary = false
	return destination, nil
}

func (service *AppService) RevealLibraryArchive(entityID string) error {
	ctx := context.Background()
	item, err := service.store.GetLibraryItem(ctx, strings.TrimSpace(entityID))
	if err != nil {
		return err
	}
	archivePath := strings.TrimSpace(item.ArchivePath)
	if archivePath == "" {
		return errors.New("archive path is empty")
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("archive path is a directory: %s", archivePath)
	}
	if app := application.Get(); app != nil && app.Env != nil {
		return app.Env.OpenFileManager(archivePath, true)
	}
	return revealArchivePath(archivePath)
}

func (service *AppService) reindexEditedArchive(ctx context.Context, previous LibraryItem, eventType string, eventData map[string]any) (EntityDetail, error) {
	stat, err := os.Stat(previous.ArchivePath)
	if err != nil {
		return EntityDetail{}, fmt.Errorf("stat edited archive: %w", err)
	}
	manifest, err := modkit.Inspect(ctx, previous.ArchivePath)
	if err != nil {
		return EntityDetail{}, fmt.Errorf("reinspect edited archive: %w", err)
	}
	sha256, err := modkit.FullSHA256(ctx, previous.ArchivePath)
	if err != nil {
		return EntityDetail{}, fmt.Errorf("hash edited archive: %w", err)
	}
	manifest.FullSHA256 = sha256
	updated, err := service.store.UpsertArchive(ctx, "", previous.RootPath, previous.ArchivePath, stat.Size(), stat.ModTime(), manifest, nil)
	if err != nil {
		return EntityDetail{}, err
	}
	if updated.EntityID != previous.EntityID {
		return EntityDetail{}, fmt.Errorf("edited archive changed entity identity")
	}
	if eventData == nil {
		eventData = map[string]any{}
	}
	eventData["artifactId"] = updated.ArtifactID
	eventData["sha256"] = sha256
	eventData["sizeBytes"] = stat.Size()
	if err := service.store.AppendEvent(ctx, previous.EntityID, eventType, eventData); err != nil {
		return EntityDetail{}, err
	}
	if service.emit != nil {
		service.emit("library:item", updated)
	}
	return service.store.GetEntityDetail(ctx, previous.EntityID)
}

func primaryMetadataTarget(manifest modkit.Manifest) (string, map[string]any) {
	for _, document := range manifest.MetadataDocuments {
		if !isPrimaryMetadataPath(document.Path, manifest.Wrapper) {
			continue
		}
		if normalized, err := modkit.NormalizeArchivePath(document.Path); err == nil {
			return normalized, document.Data
		}
	}
	target := path.Join("mod_info", "info.json")
	if wrapper, err := modkit.NormalizeArchivePath(manifest.Wrapper); err == nil && wrapper != "" {
		target = path.Join(wrapper, target)
	}
	return target, nil
}

func isPrimaryMetadataPath(value, wrapper string) bool {
	normalized, err := modkit.NormalizeArchivePath(value)
	if err != nil {
		return false
	}
	logical := stripArchiveWrapper(normalized, wrapper)
	lower := strings.ToLower(logical)
	base := path.Base(lower)
	if base != "info.json" {
		return false
	}
	return lower == "info.json" || strings.HasPrefix(lower, "mod_info/") || strings.HasPrefix(lower, "vehicles/") || strings.HasPrefix(lower, "levels/")
}

func stripArchiveWrapper(value, wrapper string) string {
	if wrapper == "" {
		return value
	}
	prefix := strings.TrimSuffix(strings.ReplaceAll(wrapper, "\\", "/"), "/") + "/"
	if len(value) >= len(prefix) && strings.EqualFold(value[:len(prefix)], prefix) {
		return value[len(prefix):]
	}
	return value
}

func metadataKey(metadata map[string]any, candidates []string, fallback string) string {
	for _, candidate := range candidates {
		if _, exists := metadata[candidate]; exists {
			return candidate
		}
	}
	if len(metadata) > 0 {
		keys := make([]string, 0, len(metadata))
		for key := range metadata {
			keys = append(keys, key)
		}
		for _, candidate := range candidates {
			for _, key := range keys {
				if strings.EqualFold(key, candidate) {
					return key
				}
			}
		}
	}
	return fallback
}
func putMetadataUpdate(updates map[string]any, metadata map[string]any, candidates []string, fallback string, value any) {
	if value != nil {
		updates[metadataKey(metadata, candidates, fallback)] = value
		return
	}
	found := false
	for key := range metadata {
		if metadataKeyMatches(key, candidates) {
			updates[key] = nil
			found = true
		}
	}
	if !found {
		updates[metadataKey(metadata, candidates, fallback)] = nil
	}
}

func metadataKeyMatches(key string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(key, candidate) {
			return true
		}
	}
	return false
}

func optionalStringUpdate(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func findVariantForUpdate(manifest modkit.Manifest, requestedPath string) (modkit.Variant, error) {
	candidates := []string{requestedPath}
	if manifest.Wrapper != "" && !strings.HasPrefix(strings.ToLower(requestedPath), strings.ToLower(strings.TrimSuffix(manifest.Wrapper, "/")+"/")) {
		candidates = append(candidates, path.Join(manifest.Wrapper, requestedPath))
	}
	for _, candidate := range candidates {
		for _, variant := range manifest.Variants {
			if strings.EqualFold(variant.ConfigPath, candidate) {
				return variant, nil
			}
		}
	}
	return modkit.Variant{}, fmt.Errorf("variant config %q does not exist", requestedPath)
}

func variantMetadataTarget(manifest modkit.Manifest, variant modkit.Variant) (string, map[string]any) {
	if variant.MetadataPath != "" {
		for _, document := range manifest.MetadataDocuments {
			if strings.EqualFold(document.Path, variant.MetadataPath) {
				if normalized, err := modkit.NormalizeArchivePath(variant.MetadataPath); err == nil {
					return normalized, document.Data
				}
			}
		}
		if normalized, err := modkit.NormalizeArchivePath(variant.MetadataPath); err == nil {
			return normalized, variant.Fields
		}
	}
	base := strings.TrimSuffix(path.Base(variant.ConfigPath), path.Ext(variant.ConfigPath))
	return path.Join(path.Dir(variant.ConfigPath), "info_"+base+".json"), variant.Fields
}

func variantMetadataUpdates(metadata map[string]any, update LibraryVariantUpdate) (map[string]any, error) {
	updates := map[string]any{}
	putMetadataUpdate(updates, metadata, []string{"Configuration", "configuration", "Name", "name"}, "Configuration", optionalStringUpdate(update.Configuration))
	putMetadataUpdate(updates, metadata, []string{"Description", "description"}, "Description", optionalStringUpdate(update.Description))
	putMetadataUpdate(updates, metadata, []string{"Config Type", "configType"}, "Config Type", optionalStringUpdate(update.ConfigType))
	putMetadataUpdate(updates, metadata, []string{"Body Style", "bodyStyle"}, "Body Style", optionalStringUpdate(update.BodyStyle))
	putMetadataUpdate(updates, metadata, []string{"Drivetrain", "drivetrain"}, "Drivetrain", optionalStringUpdate(update.Drivetrain))
	putMetadataUpdate(updates, metadata, []string{"Transmission", "transmission"}, "Transmission", optionalStringUpdate(update.Transmission))
	putMetadataUpdate(updates, metadata, []string{"Fuel Type", "fuelType"}, "Fuel Type", optionalStringUpdate(update.FuelType))
	putMetadataUpdate(updates, metadata, []string{"Propulsion", "propulsion"}, "Propulsion", optionalStringUpdate(update.Propulsion))

	numericFields := []struct {
		value      string
		candidates []string
		fallback   string
		name       string
	}{
		{update.Power, []string{"Power", "power"}, "Power", "power"},
		{update.Torque, []string{"Torque", "torque"}, "Torque", "torque"},
		{update.Weight, []string{"Weight", "weight"}, "Weight", "weight"},
		{update.Value, []string{"Value", "value"}, "Value", "value"},
		{update.TopSpeed, []string{"Top Speed", "topSpeed"}, "Top Speed", "topSpeed"},
	}
	for _, field := range numericFields {
		value := strings.TrimSpace(field.value)
		if value == "" {
			putMetadataUpdate(updates, metadata, field.candidates, field.fallback, nil)
			continue
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			if err == nil {
				err = errors.New("number must be finite")
			}
			return nil, fmt.Errorf("invalid %s value %q: %w", field.name, field.value, err)
		}
		putMetadataUpdate(updates, metadata, field.candidates, field.fallback, parsed)
	}
	return updates, nil
}

func findArchiveMember(manifest modkit.Manifest, requestedPath string) (modkit.ArchiveMember, error) {
	normalized, err := modkit.NormalizeArchivePath(requestedPath)
	if err != nil {
		return modkit.ArchiveMember{}, err
	}
	for _, member := range manifest.Members {
		if strings.EqualFold(member.Path, normalized) {
			return member, nil
		}
	}
	return modkit.ArchiveMember{}, fmt.Errorf("archive member %q does not exist", normalized)
}

func chooseArchiveExtractionPath(archivePath, memberPath string) (string, error) {
	app := application.Get()
	if app == nil || app.Dialog == nil {
		return "", errors.New("native save dialog is unavailable")
	}
	dialog := app.Dialog.SaveFile()
	dialog.SetOptions(&application.SaveFileDialogOptions{
		CanCreateDirectories: true,
		AllowOtherFileTypes:  true,
		Title:                "Extract archive member",
		Message:              "Choose where to save the selected archive member",
		Directory:            filepath.Dir(archivePath),
		Filename:             path.Base(memberPath),
		ButtonText:           "Extract",
		Filters:              []application.FileFilter{{DisplayName: "All files", Pattern: "*.*"}},
	})
	return dialog.PromptForSingleSelection()
}

func isDialogCancellation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "cancel") || strings.Contains(message, "aborted")
}

func revealArchivePath(archivePath string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("explorer.exe", "/select,"+archivePath)
	case "darwin":
		command = exec.Command("open", "-R", archivePath)
	default:
		command = exec.Command("xdg-open", filepath.Dir(archivePath))
	}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
