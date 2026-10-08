package modkit

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alchemy/json5"
)

// inspectFolder builds a Manifest for an unpacked folder mod. It reads only
// metadata documents and variant info files (the performance rule) and never
// opens JBeam files. Members are synthesised from the folder listing (name,
// size, modified time; compressed = uncompressed, CRC/method zero).
func inspectFolder(ctx context.Context, root string) (Manifest, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	entries, reparseIssues, err := walkFolderListingWithIssues(ctx, root)
	if err != nil {
		return Manifest{}, fmt.Errorf("walk folder: %w", err)
	}
	fingerprint, err := FolderListingFingerprint(ctx, root)
	if err != nil {
		return Manifest{}, fmt.Errorf("folder fingerprint: %w", err)
	}

	var totalSize int64
	var newestTime time.Time
	members := make([]ArchiveMember, 0, len(entries))
	names := make([]string, 0, len(entries))
	logicalCandidates := make([]string, 0, len(entries))
	entryByLower := make(map[string]folderEntry, len(entries))
	issues := []Issue{}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		// Validate path safety.
		if _, pathErr := normalizeArchivePath(entry.path); pathErr != nil {
			issues = append(issues, Issue{Code: "unsafe-path", Severity: SeverityError, Message: pathErr.Error(), Path: entry.path})
			continue
		}
		names = append(names, entry.path)
		logicalCandidates = append(logicalCandidates, entry.path)
		lower := strings.ToLower(entry.path)
		entryByLower[lower] = entry
		totalSize += entry.size
		if entry.modTime.After(newestTime) {
			newestTime = entry.modTime
		}
		members = append(members, ArchiveMember{
			Path:              entry.path,
			CompressedBytes:   uint64(entry.size),
			UncompressedBytes: uint64(entry.size),
			ModifiedAt:        entry.modTime,
			Directory:         entry.isDir,
		})
	}

	issues = append(issues, reparseIssues...)

	manifest := Manifest{
		SchemaVersion:      SchemaVersion,
		AnalyzerVersion:    AnalyzerVersion,
		AnalyzedAt:         time.Now().UTC(),
		ArchivePath:        root,
		Filename:           filepath.Base(root),
		SizeBytes:          totalSize,
		ModifiedAt:         newestTime,
		CentralFingerprint: fingerprint,
		ValidArchive:       true,
		SourceKind:         SourceFolder,
		EntryCount:         len(entries),
		CompressedBytes:    uint64(totalSize),
		UncompressedBytes:  uint64(totalSize),
		Namespaces:         map[string][]string{"vehicles": {}, "levels": {}, "ui": {}},
		Members:            members,
		MetadataDocuments:  []MetadataDocument{},
		Images:             []ImageCandidate{},
		Variants:           []Variant{},
		SharedAssets:       &SharedAssetStats{},
		Issues:             issues,
	}

	logicalNames, wrapper := unwrapLogicalPaths(logicalCandidates)
	manifest.Wrapper = wrapper
	actualByLogical := make(map[string]string, len(logicalNames))
	for i, logical := range logicalNames {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		actualByLogical[strings.ToLower(logical)] = names[i]
	}

	classify(&manifest, logicalNames)

	// Read metadata documents (same selection as for ZIPs, but reading files).
	metadataFiles := prioritizedFolderMetadata(logicalNames, actualByLogical, entryByLower)
	metadataByLower := map[string]map[string]any{}
	metadataBytes := uint64(0)
	for _, candidate := range metadataFiles {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		if len(manifest.MetadataDocuments) >= maxMetadataDocuments {
			manifest.Issues = append(manifest.Issues, Issue{Code: "metadata-limit", Severity: SeverityInfo, Message: "Additional metadata documents were omitted from normalized analysis"})
			break
		}
		if uint64(candidate.size) > maxMetadataFile || metadataBytes+uint64(candidate.size) > maxMetadataTotal {
			manifest.Issues = append(manifest.Issues, Issue{Code: "metadata-too-large", Severity: SeverityWarning, Message: "Metadata document exceeds analysis limits", Path: candidate.path})
			continue
		}
		data, _, readErr := readFolderMember(root, candidate.path, maxMetadataFile)
		if readErr != nil {
			if err := ctx.Err(); err != nil {
				return Manifest{}, err
			}
			manifest.Issues = append(manifest.Issues, Issue{Code: "metadata-unreadable", Severity: SeverityWarning, Message: readErr.Error(), Path: candidate.path})
			continue
		}
		metadataBytes += uint64(len(data))
		parsed := map[string]any{}
		cleaned := strings.TrimRight(strings.TrimPrefix(string(data), "\ufeff"), "\x00")
		if err := json5.Unmarshal([]byte(cleaned), &parsed); err != nil {
			manifest.Issues = append(manifest.Issues, Issue{Code: "metadata-invalid", Severity: SeverityWarning, Message: err.Error(), Path: candidate.path})
			continue
		}
		manifest.MetadataDocuments = append(manifest.MetadataDocuments, MetadataDocument{Path: candidate.path, Data: parsed})
		metadataByLower[strings.ToLower(candidate.path)] = parsed
	}
	normalizePrimaryMetadata(&manifest)

	// Image candidates from folder listing.
	manifest.Images = findFolderImageCandidates(logicalNames, actualByLogical, entryByLower, manifest.MetadataDocuments)
	if len(manifest.Images) > 0 {
		manifest.SelectedImagePath = manifest.Images[0].Path
	}

	// Variants from folder listing (reads variant info files).
	manifest.Variants = findFolderVariants(root, logicalNames, actualByLogical, entryByLower, metadataByLower)

	// Do NOT run JBeam analysis for folders (performance rule).
	// JBeam stats stay zero.

	analyzeMapAndUI(&manifest, logicalNames)
	analyzeFolderSharedAssets(&manifest, logicalNames, actualByLogical, entryByLower)

	if len(names) == 0 {
		manifest.ValidArchive = false
		manifest.Issues = append(manifest.Issues, Issue{Code: "empty-archive", Severity: SeverityError, Message: "Folder contains no entries"})
	}
	if manifest.Kind == KindUnknown {
		manifest.Issues = append(manifest.Issues, Issue{Code: "unknown-content", Severity: SeverityWarning, Message: "No standard BeamNG content root was recognized"})
	}
	return manifest, nil
}

type folderMetadataCandidate struct {
	path     string
	size     int64
	priority int
}

func prioritizedFolderMetadata(logical []string, actual map[string]string, entries map[string]folderEntry) []folderMetadataCandidate {
	candidates := []folderMetadataCandidate{}
	for _, name := range logical {
		lower := strings.ToLower(name)
		base := strings.ToLower(path.Base(name))
		if !strings.HasSuffix(lower, ".json") {
			continue
		}
		priority := 99
		switch {
		case strings.HasPrefix(lower, "mod_info/") && base == "info.json":
			priority = 0
		case lower == "info.json":
			priority = 1
		case strings.HasPrefix(lower, "vehicles/") && base == "info.json":
			priority = 2
		case strings.HasPrefix(lower, "levels/") && base == "info.json":
			priority = 3
		case strings.HasPrefix(lower, "vehicles/") && strings.HasPrefix(base, "info_"):
			priority = 4
		default:
			continue
		}
		actualName := actual[lower]
		entry, ok := entries[strings.ToLower(actualName)]
		if !ok || entry.isDir {
			continue
		}
		candidates = append(candidates, folderMetadataCandidate{path: actualName, size: entry.size, priority: priority})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		return candidates[i].path < candidates[j].path
	})
	return candidates
}

func findFolderImageCandidates(logical []string, actual map[string]string, entries map[string]folderEntry, documents []MetadataDocument) []ImageCandidate {
	type ranked struct {
		candidate ImageCandidate
		priority  int
	}
	seen := map[string]bool{}
	items := []ranked{}
	add := func(logicalName string, priority int, role string) {
		lower := strings.ToLower(logicalName)
		if seen[lower] {
			return
		}
		ext := strings.ToLower(path.Ext(lower))
		mime := imageExtensions[ext]
		if mime == "" {
			return
		}
		actualName := actual[lower]
		entry, ok := entries[strings.ToLower(actualName)]
		if !ok || entry.isDir {
			return
		}
		seen[lower] = true
		items = append(items, ranked{candidate: ImageCandidate{Path: actualName, Role: role, UncompressedBytes: uint64(entry.size), MIME: mime}, priority: priority})
	}
	for _, name := range logical {
		lower := strings.ToLower(name)
		base := strings.TrimSuffix(strings.ToLower(path.Base(name)), path.Ext(name))
		parts := strings.Split(lower, "/")
		switch {
		case strings.HasPrefix(lower, "mod_info/") && strings.Contains(lower, "/thumbs/"):
			add(name, 1, "repository-thumb")
		case strings.HasPrefix(lower, "mod_info/") && (base == "icon" || base == "logo" || base == "preview" || base == "thumb"):
			add(name, 0, "mod-preview")
		case strings.HasPrefix(lower, "mod_info/") && strings.Contains(lower, "/images/"):
			add(name, 2, "gallery")
		case len(parts) == 3 && parts[0] == "vehicles" && (base == "default" || base == "preview" || base == "logo"):
			add(name, 3, "vehicle-default")
		case len(parts) == 3 && parts[0] == "vehicles":
			add(name, 4, "variant")
		case len(parts) <= 4 && parts[0] == "levels" && (strings.Contains(base, "preview") || base == "main" || base == "logo"):
			add(name, 5, "level-preview")
		}
	}
	for _, document := range documents {
		for _, reference := range collectImageReferences(document.Data) {
			reference = strings.TrimPrefix(strings.ReplaceAll(reference, "\\", "/"), "/")
			if _, ok := actual[strings.ToLower(reference)]; ok {
				add(reference, 1, "metadata-reference")
				continue
			}
			directory := path.Dir(document.Path)
			joined := path.Clean(path.Join(directory, reference))
			if _, ok := actual[strings.ToLower(joined)]; ok {
				add(joined, 1, "metadata-reference")
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].priority != items[j].priority {
			return items[i].priority < items[j].priority
		}
		if items[i].candidate.UncompressedBytes != items[j].candidate.UncompressedBytes {
			return items[i].candidate.UncompressedBytes < items[j].candidate.UncompressedBytes
		}
		return items[i].candidate.Path < items[j].candidate.Path
	})
	result := make([]ImageCandidate, len(items))
	for i := range items {
		result[i] = items[i].candidate
	}
	return result
}

func findFolderVariants(root string, logical []string, actual map[string]string, entries map[string]folderEntry, metadata map[string]map[string]any) []Variant {
	variants := []Variant{}
	for _, name := range logical {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, "vehicles/") || !strings.HasSuffix(lower, ".pc") {
			continue
		}
		parts := strings.Split(name, "/")
		if len(parts) != 3 {
			continue
		}
		base := strings.TrimSuffix(path.Base(name), path.Ext(name))
		directory := path.Dir(name)
		metadataLogical := path.Join(directory, "info_"+base+".json")
		thumbnailLogical := ""
		for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
			candidate := path.Join(directory, base+ext)
			if _, ok := actual[strings.ToLower(candidate)]; ok {
				thumbnailLogical = candidate
				break
			}
		}
		variant := Variant{Namespace: parts[1], BaseName: base, ConfigPath: actual[lower], ThumbnailPath: actual[strings.ToLower(thumbnailLogical)], Fields: map[string]any{}}
		if actualMeta, ok := actual[strings.ToLower(metadataLogical)]; ok {
			variant.MetadataPath = actualMeta
			data := metadata[strings.ToLower(actualMeta)]
			if data == nil {
				entry := entries[strings.ToLower(actualMeta)]
				if !entry.isDir && entry.size <= maxMetadataFile {
					if raw, _, err := readFolderMember(root, actualMeta, maxMetadataFile); err == nil {
						_ = json5.Unmarshal(raw, &data)
					}
				}
			}
			if data != nil {
				variant.Fields = data
				variant.Configuration = firstString(data, "Configuration", "configuration", "Name", "name")
				variant.Description = firstString(data, "Description", "description")
				variant.ConfigType = firstString(data, "Config Type", "configType")
				variant.BodyStyle = firstString(data, "Body Style", "bodyStyle")
				variant.Drivetrain = firstString(data, "Drivetrain", "drivetrain")
				variant.Transmission = firstString(data, "Transmission", "transmission")
				variant.FuelType = firstString(data, "Fuel Type", "fuelType")
				variant.Propulsion = firstString(data, "Propulsion", "propulsion")
				variant.Power = number(data["Power"])
				variant.Torque = number(data["Torque"])
				variant.Weight = number(data["Weight"])
				variant.Value = number(data["Value"])
				variant.TopSpeed = number(data["Top Speed"])
			}
		}
		variants = append(variants, variant)
	}
	sort.Slice(variants, func(i, j int) bool { return variants[i].ConfigPath < variants[j].ConfigPath })
	return variants
}

func analyzeFolderSharedAssets(manifest *Manifest, logical []string, actualByLogical map[string]string, entries map[string]folderEntry) {
	if manifest == nil {
		return
	}
	stats := &SharedAssetStats{}
	for _, name := range logical {
		normalized := normalizeSharedAssetName(name)
		if normalized == "" || !isSharedAssetName(normalized) {
			continue
		}
		actualName := actualByLogical[normalized]
		entry, ok := entries[strings.ToLower(actualName)]
		if ok && entry.isDir {
			continue
		}
		stats.Files++
	}
	manifest.SharedAssets = stats
}

