package modkit

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alchemy/json5"
)

const (
	maxEntries           = 100_000
	maxMetadataFile      = 2 << 20
	maxMetadataTotal     = 64 << 20
	maxMetadataDocuments = 512
	maxJBeamFile         = 8 << 20
	maxJBeamTotal        = 128 << 20
)

var knownRoots = map[string]bool{
	"art": true, "campaigns": true, "core": true, "gameplay": true,
	"levels": true, "lua": true, "mod_info": true, "music": true,
	"scripts": true, "settings": true, "sound": true, "tech": true,
	"ui": true, "vehicles": true,
}

var imageExtensions = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
	".gif": "image/gif", ".webp": "image/webp",
}

var sharedAssetExtensions = map[string]bool{
	".bmp":  true,
	".cdae": true,
	".dae":  true,
	".dds":  true,
	".fbx":  true,
	".flac": true,
	".gif":  true,
	".glb":  true,
	".gltf": true,
	".jpeg": true,
	".jpg":  true,
	".mp3":  true,
	".mp4":  true,
	".obj":  true,
	".ogg":  true,
	".png":  true,
	".tga":  true,
	".wav":  true,
	".webm": true,
}

func Inspect(ctx context.Context, archivePath string) (Manifest, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	src, err := OpenSource(ctx, archivePath)
	if err != nil {
		return Manifest{}, err
	}
	defer src.Close()
	return InspectSource(ctx, src)
}

// InspectSource builds a Manifest from an already-opened Source.
func InspectSource(ctx context.Context, src Source) (Manifest, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}

	entries := src.Entries()
	isFolder := src.Kind() == SourceFolder

	// Collect issues that the source found during construction.
	constructionIssues := SourceConstructionIssues(src)

	manifest := Manifest{
		SchemaVersion:     SchemaVersion,
		AnalyzerVersion:   AnalyzerVersion,
		AnalyzedAt:        time.Now().UTC(),
		ArchivePath:       src.Path(),
		Filename:          filepath.Base(src.Path()),
		ValidArchive:      true,
		EntryCount:        len(entries),
		Namespaces:        map[string][]string{"vehicles": {}, "levels": {}, "ui": {}},
		Members:           make([]ArchiveMember, 0, len(entries)),
		MetadataDocuments: []MetadataDocument{},
		Images:            []ImageCandidate{},
		Variants:          []Variant{},
		SharedAssets:      &SharedAssetStats{},
		Issues:            append([]Issue{}, constructionIssues...),
	}
	if isFolder {
		manifest.SourceKind = SourceFolder
	}

	// For ZIP: stat gives size/mtime; for folder: compute from entries.
	if !isFolder {
		stat, err := os.Stat(src.Path())
		if err != nil {
			return Manifest{}, fmt.Errorf("stat archive: %w", err)
		}
		manifest.SizeBytes = stat.Size()
		manifest.ModifiedAt = stat.ModTime().UTC()
	}

	// ZIP central-directory fingerprint hash (unchanged formula).
	var zipFingerprint = sha256.New()

	actualNames := make([]string, 0, len(entries))
	logicalCandidates := make([]string, 0, len(entries))
	entryByLower := make(map[string]SourceEntry, len(entries))
	seenLower := make(map[string]bool, len(entries))

	var totalSize int64
	var newestTime time.Time

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}

		lower := strings.ToLower(entry.Path)
		if seenLower[lower] {
			manifest.Issues = append(manifest.Issues, Issue{Code: "duplicate-path", Severity: SeverityWarning, Message: "Archive contains duplicate case-insensitive paths", Path: entry.Path})
		} else {
			seenLower[lower] = true
			entryByLower[lower] = entry
		}

		actualNames = append(actualNames, entry.Path)
		logicalCandidates = append(logicalCandidates, entry.Path)
		manifest.CompressedBytes += uint64(entry.CompressedSize)
		manifest.UncompressedBytes += uint64(entry.Size)
		manifest.Members = append(manifest.Members, ArchiveMember{
			Path: entry.Path, CompressedBytes: uint64(entry.CompressedSize), UncompressedBytes: uint64(entry.Size),
			CRC32: entry.CRC32, Method: entry.Method, ModifiedAt: entry.ModifiedAt, Directory: entry.Dir,
		})

		if isFolder {
			totalSize += entry.Size
			if entry.ModifiedAt.After(newestTime) {
				newestTime = entry.ModifiedAt
			}
		} else {
			// ZIP fingerprint formula — identical to the original.
			_, _ = zipFingerprint.Write([]byte(entry.Path))
			var facts [26]byte
			binary.LittleEndian.PutUint32(facts[0:4], entry.CRC32)
			binary.LittleEndian.PutUint16(facts[4:6], entry.Method)
			binary.LittleEndian.PutUint64(facts[6:14], uint64(entry.CompressedSize))
			binary.LittleEndian.PutUint64(facts[14:22], uint64(entry.Size))
			binary.LittleEndian.PutUint32(facts[22:26], uint32(entry.Flags))
			_, _ = zipFingerprint.Write(facts[:])
		}

		// ZIP-only checks: encrypted, unsupported compression, extreme ratio.
		// For folders: Flags=0, Method=Store, CompressedSize=Size → none fire.
		if entry.Flags&0x1 != 0 {
			manifest.ValidArchive = false
			manifest.Issues = append(manifest.Issues, Issue{Code: "encrypted-entry", Severity: SeverityError, Message: "BeamNG cannot load encrypted ZIP entries", Path: entry.Path})
		}
		if !entry.Dir && entry.Method != zip.Store && entry.Method != zip.Deflate {
			manifest.Issues = append(manifest.Issues, Issue{Code: "unsupported-compression", Severity: SeverityWarning, Message: fmt.Sprintf("Compression method %d may not load in BeamNG", entry.Method), Path: entry.Path})
		}
		if entry.CompressedSize > 0 && entry.Size > 1<<30 && entry.Size/entry.CompressedSize > 1_000 {
			manifest.Issues = append(manifest.Issues, Issue{Code: "extreme-compression", Severity: SeverityWarning, Message: "Entry has an extreme compression ratio", Path: entry.Path})
		}
	}

	// Fingerprint.
	if isFolder {
		manifest.SizeBytes = totalSize
		manifest.ModifiedAt = newestTime
		fp, err := FolderListingFingerprint(ctx, src.Path())
		if err != nil {
			return Manifest{}, fmt.Errorf("folder fingerprint: %w", err)
		}
		manifest.CentralFingerprint = fp
	} else {
		manifest.CentralFingerprint = hex.EncodeToString(zipFingerprint.Sum(nil))
	}

	logicalNames, wrapper := unwrapLogicalPaths(logicalCandidates)
	manifest.Wrapper = wrapper
	actualByLogical := make(map[string]string, len(logicalNames))
	for i, logical := range logicalNames {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		actualByLogical[strings.ToLower(logical)] = actualNames[i]
	}

	classify(&manifest, logicalNames)

	// Metadata documents — one code path using Source.Open.
	metadataNames := prioritizedMetadataNames(logicalNames, actualByLogical, entryByLower)
	metadataByLower := map[string]map[string]any{}
	metadataBytes := uint64(0)
	for _, candidate := range metadataNames {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		if len(manifest.MetadataDocuments) >= maxMetadataDocuments {
			manifest.Issues = append(manifest.Issues, Issue{Code: "metadata-limit", Severity: SeverityInfo, Message: "Additional metadata documents were omitted from normalized analysis"})
			break
		}
		entry := entryByLower[strings.ToLower(candidate)]
		if uint64(entry.Size) > maxMetadataFile || metadataBytes+uint64(entry.Size) > maxMetadataTotal {
			manifest.Issues = append(manifest.Issues, Issue{Code: "metadata-too-large", Severity: SeverityWarning, Message: "Metadata document exceeds analysis limits", Path: candidate})
			continue
		}
		data, _, readErr := ReadSourceEntry(src, candidate, maxMetadataFile)
		if readErr != nil {
			if err := ctx.Err(); err != nil {
				return Manifest{}, err
			}
			manifest.Issues = append(manifest.Issues, Issue{Code: "metadata-unreadable", Severity: SeverityWarning, Message: readErr.Error(), Path: candidate})
			continue
		}
		metadataBytes += uint64(len(data))
		parsed := map[string]any{}
		cleaned := strings.TrimRight(strings.TrimPrefix(string(data), "\ufeff"), "\x00")
		if err := json5.Unmarshal([]byte(cleaned), &parsed); err != nil {
			manifest.Issues = append(manifest.Issues, Issue{Code: "metadata-invalid", Severity: SeverityWarning, Message: err.Error(), Path: candidate})
			continue
		}
		manifest.MetadataDocuments = append(manifest.MetadataDocuments, MetadataDocument{Path: candidate, Data: parsed})
		metadataByLower[strings.ToLower(candidate)] = parsed
	}
	normalizePrimaryMetadata(&manifest)

	// Image candidates — one path using entryByLower.
	manifest.Images = findImageCandidatesFromEntries(logicalNames, actualByLogical, entryByLower, manifest.MetadataDocuments)
	if len(manifest.Images) > 0 {
		manifest.SelectedImagePath = manifest.Images[0].Path
	}

	// Variants — one path reading through Source.
	manifest.Variants = findVariantsFromSource(src, logicalNames, actualByLogical, entryByLower, metadataByLower)

	// JBeam — skip for folders (performance rule).
	if !isFolder {
		manifest.JBeam = analyzeJBeamFromSource(ctx, src, logicalNames, actualByLogical, entryByLower, &manifest.Issues)
	}

	analyzeMapAndUI(&manifest, logicalNames)
	analyzeSharedAssetsFromEntries(&manifest, logicalNames, actualByLogical, entryByLower)

	if len(actualNames) == 0 {
		manifest.ValidArchive = false
		manifest.Issues = append(manifest.Issues, Issue{Code: "empty-archive", Severity: SeverityError, Message: "Archive contains no entries"})
	}
	if manifest.Kind == KindUnknown {
		manifest.Issues = append(manifest.Issues, Issue{Code: "unknown-content", Severity: SeverityWarning, Message: "No standard BeamNG content root was recognized"})
	}
	return manifest, nil
}

func normalizeArchivePath(value string) (string, error) {
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.TrimPrefix(value, "./")
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, ":") {
		return "", fmt.Errorf("unsafe archive path %q", value)
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("archive path escapes its root: %q", value)
	}
	return cleaned, nil
}

func unwrapLogicalPaths(names []string) ([]string, string) {
	top := ""
	wrapped := true
	for _, name := range names {
		parts := strings.Split(name, "/")
		if len(parts) < 2 || knownRoots[strings.ToLower(parts[0])] || strings.EqualFold(parts[0], "info.json") {
			wrapped = false
			break
		}
		if top == "" {
			top = parts[0]
		} else if !strings.EqualFold(top, parts[0]) {
			wrapped = false
			break
		}
	}
	if !wrapped || top == "" {
		return append([]string(nil), names...), ""
	}
	logical := make([]string, len(names))
	hasKnownRoot := false
	for i, name := range names {
		logical[i] = strings.TrimPrefix(name, top+"/")
		root := strings.ToLower(strings.Split(logical[i], "/")[0])
		hasKnownRoot = hasKnownRoot || knownRoots[root]
	}
	if !hasKnownRoot {
		return append([]string(nil), names...), ""
	}
	return logical, top
}

func classify(manifest *Manifest, logical []string) {
	vehicle, level, ui, script := false, false, false, false
	namespaces := map[string]map[string]bool{"vehicles": {}, "levels": {}, "ui": {}}
	for _, name := range logical {
		lower := strings.ToLower(name)
		parts := strings.Split(name, "/")
		root := strings.ToLower(parts[0])
		switch root {
		case "vehicles":
			vehicle = true
			if len(parts) > 1 && !strings.EqualFold(parts[1], "common") {
				namespaces["vehicles"][parts[1]] = true
			}
		case "levels":
			level = true
			if len(parts) > 1 {
				namespaces["levels"][parts[1]] = true
			}
		case "ui":
			ui = true
			if len(parts) > 1 {
				namespaces["ui"][parts[1]] = true
			}
		case "lua", "scripts":
			script = true
		}
		if strings.Contains(lower, "/ui/") || strings.Contains(lower, "/extensions/ui") {
			ui = true
		}
	}
	flags := 0
	for _, value := range []bool{vehicle, level, ui, script} {
		if value {
			flags++
		}
	}
	switch {
	case flags > 1:
		manifest.Kind = KindMixed
	case vehicle:
		manifest.Kind = KindVehicle
	case level:
		manifest.Kind = KindMap
	case ui:
		manifest.Kind = KindUI
	case script:
		manifest.Kind = KindScript
	default:
		manifest.Kind = KindUnknown
	}
	for key, values := range namespaces {
		for value := range values {
			manifest.Namespaces[key] = append(manifest.Namespaces[key], value)
		}
		sort.Strings(manifest.Namespaces[key])
	}
	for _, pair := range []struct {
		value bool
		tag   string
	}{{vehicle, "vehicles"}, {level, "levels"}, {ui, "ui"}, {script, "scripts"}} {
		if pair.value {
			manifest.ContentTags = append(manifest.ContentTags, pair.tag)
		}
	}
}

func prioritizedMetadataNames(logical []string, actual map[string]string, entries map[string]SourceEntry) []string {
	type candidate struct {
		path     string
		priority int
	}
	candidates := []candidate{}
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
		if !ok || entry.Dir {
			continue
		}
		candidates = append(candidates, candidate{path: actualName, priority: priority})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		return candidates[i].path < candidates[j].path
	})
	result := make([]string, len(candidates))
	for i := range candidates {
		result[i] = candidates[i].path
	}
	return result
}

func normalizePrimaryMetadata(manifest *Manifest) {
	hasPrimaryDocument := false
	for _, document := range manifest.MetadataDocuments {
		if strings.EqualFold(path.Base(document.Path), "info.json") {
			hasPrimaryDocument = true
			break
		}
	}
	for _, document := range manifest.MetadataDocuments {
		if hasPrimaryDocument && !strings.EqualFold(path.Base(document.Path), "info.json") {
			continue
		}
		data := document.Data
		if manifest.Title == "" {
			manifest.Title = firstString(data, "title", "Name", "name", "Configuration")
		}
		if manifest.Description == "" {
			manifest.Description = firstString(data, "tag_line", "description", "Description")
		}
		if manifest.Author == "" {
			manifest.Author = firstString(data, "username", "author", "Author", "authors")
		}
		if manifest.Version == "" {
			manifest.Version = firstString(data, "version_string", "version", "Version")
		}
	}
	if manifest.Title == "" {
		manifest.Title = prettifyFilename(manifest.Filename)
	}
}

func firstString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := data[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if text := strings.TrimSpace(typed); text != "" {
				return text
			}
		case []any:
			parts := []string{}
			for _, item := range typed {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, strings.TrimSpace(text))
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, ", ")
			}
		}
	}
	return ""
}

func prettifyFilename(filename string) string {
	name := strings.TrimSuffix(strings.TrimSuffix(filename, ".stop"), filepath.Ext(strings.TrimSuffix(filename, ".stop")))
	name = strings.ReplaceAll(name, "_", " ")
	return strings.Join(strings.Fields(name), " ")
}

func findImageCandidatesFromEntries(logical []string, actual map[string]string, entries map[string]SourceEntry, documents []MetadataDocument) []ImageCandidate {
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
		if !ok || entry.Dir {
			return
		}
		seen[lower] = true
		items = append(items, ranked{candidate: ImageCandidate{Path: actualName, Role: role, UncompressedBytes: uint64(entry.Size), MIME: mime}, priority: priority})
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

func collectImageReferences(value any) []string {
	result := []string{}
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				lowerKey := strings.ToLower(key)
				if text, ok := child.(string); ok && (strings.Contains(lowerKey, "preview") || strings.Contains(lowerKey, "thumb") || strings.Contains(lowerKey, "image") || strings.Contains(lowerKey, "icon")) {
					result = append(result, text)
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return result
}

func findVariantsFromSource(src Source, logical []string, actual map[string]string, entries map[string]SourceEntry, metadata map[string]map[string]any) []Variant {
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
				entry, found := entries[strings.ToLower(actualMeta)]
				if found && !entry.Dir && entry.Size <= maxMetadataFile {
					if raw, _, err := ReadSourceEntry(src, actualMeta, maxMetadataFile); err == nil {
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

func number(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json5.Number:
		result, _ := typed.Float64()
		return result
	case string:
		result, _ := strconv.ParseFloat(typed, 64)
		return result
	default:
		return 0
	}
}

func analyzeJBeamFromSource(ctx context.Context, src Source, logical []string, actual map[string]string, entries map[string]SourceEntry, issues *[]Issue) JBeamStats {
	stats := JBeamStats{}
	readBytes := uint64(0)
	controllerSet := map[string]bool{}
	for _, name := range logical {
		if err := ctx.Err(); err != nil {
			break
		}
		if !strings.HasSuffix(strings.ToLower(name), ".jbeam") {
			continue
		}
		stats.Files++
		actualName := actual[strings.ToLower(name)]
		entry, found := entries[strings.ToLower(actualName)]
		if !found || uint64(entry.Size) > maxJBeamFile || readBytes+uint64(entry.Size) > maxJBeamTotal {
			*issues = append(*issues, Issue{Code: "jbeam-limit", Severity: SeverityInfo, Message: "JBeam file omitted from deep counts due to analysis limits", Path: actualName})
			continue
		}
		raw, _, err := ReadSourceEntry(src, actualName, maxJBeamFile)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			*issues = append(*issues, Issue{Code: "jbeam-unreadable", Severity: SeverityWarning, Message: err.Error(), Path: actualName})
			continue
		}
		readBytes += uint64(len(raw))
		var parsed any
		if err := json5.Unmarshal(raw, &parsed); err != nil {
			*issues = append(*issues, Issue{Code: "jbeam-invalid", Severity: SeverityWarning, Message: err.Error(), Path: actualName})
			continue
		}
		stats.ParsedFiles++
		walkJBeam(parsed, &stats, controllerSet)
	}
	for controller := range controllerSet {
		if err := ctx.Err(); err != nil {
			break
		}
		stats.Controllers = append(stats.Controllers, controller)
	}
	sort.Strings(stats.Controllers)
	return stats
}

func walkJBeam(value any, stats *JBeamStats, controllers map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			switch lower {
			case "nodes":
				stats.DeclaredNodes += tableRows(child)
			case "beams":
				stats.DeclaredBeams += tableRows(child)
			case "triangles":
				stats.DeclaredTriangles += tableRows(child)
			case "slots":
				stats.DeclaredSlots += tableRows(child)
			case "hydros":
				stats.DeclaredHydros += tableRows(child)
			case "controller", "controllers":
				collectControllerNames(child, controllers)
			}
			walkJBeam(child, stats, controllers)
		}
	case []any:
		for _, child := range typed {
			walkJBeam(child, stats, controllers)
		}
	}
}

func tableRows(value any) int {
	rows, ok := value.([]any)
	if !ok || len(rows) == 0 {
		return 0
	}
	if _, header := rows[0].([]any); header {
		return len(rows) - 1
	}
	return len(rows)
}

func collectControllerNames(value any, result map[string]bool) {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) != "" {
			result[typed] = true
		}
	case map[string]any:
		for key, child := range typed {
			if strings.EqualFold(key, "fileName") || strings.EqualFold(key, "name") {
				collectControllerNames(child, result)
			}
		}
	case []any:
		start := 0
		if len(typed) > 0 {
			if header, ok := typed[0].([]any); ok {
				for _, column := range header {
					if name, ok := column.(string); ok && (strings.EqualFold(name, "fileName") || strings.EqualFold(name, "name")) {
						start = 1
						break
					}
				}
			}
		}
		for _, child := range typed[start:] {
			collectControllerNames(child, result)
		}
	}
}

func analyzeSharedAssetsFromEntries(manifest *Manifest, logical []string, actualByLogical map[string]string, entries map[string]SourceEntry) {
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
		if entry, ok := entries[strings.ToLower(actualName)]; ok && entry.Dir {
			continue
		}
		stats.Files++
	}
	manifest.SharedAssets = stats
}

func normalizeSharedAssetName(name string) string {
	normalized := strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	normalized = strings.TrimPrefix(normalized, "./")
	normalized = strings.TrimLeft(normalized, "/")
	if normalized == "" || strings.HasSuffix(normalized, "/") {
		return ""
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return strings.ToLower(cleaned)
}

func isSharedAssetName(name string) bool {
	if !strings.HasPrefix(name, "assets/") &&
		!strings.HasPrefix(name, "art/") &&
		!strings.HasPrefix(name, "common/") &&
		!strings.HasPrefix(name, "vehicles/common/") {
		return false
	}
	return strings.HasSuffix(name, ".materials.json") || sharedAssetExtensions[path.Ext(name)]
}

func analyzeMapAndUI(manifest *Manifest, logical []string) {
	levels := map[string]bool{}
	uiRoots := map[string]bool{}
	for _, name := range logical {
		lower := strings.ToLower(name)
		parts := strings.Split(name, "/")
		ext := strings.ToLower(path.Ext(lower))
		if len(parts) > 1 && strings.EqualFold(parts[0], "levels") {
			levels[parts[1]] = true
		}
		switch {
		case strings.HasSuffix(lower, ".ter"):
			manifest.Map.TerrainFiles++
		case strings.HasSuffix(lower, "items.level.json"):
			manifest.Map.LevelObjectFiles++
		case strings.Contains(lower, "forest") && strings.HasSuffix(lower, ".json"):
			manifest.Map.ForestFiles++
		case strings.Contains(lower, "/facilities/") && strings.HasSuffix(lower, ".json"):
			manifest.Map.FacilityFiles++
		case strings.HasSuffix(lower, ".materials.json"):
			manifest.Map.MaterialFiles++
		case ext == ".dae" || ext == ".cdae":
			manifest.Map.ModelFiles++
		case ext == ".dds" || ext == ".png" || ext == ".jpg" || ext == ".jpeg":
			if strings.HasPrefix(lower, "levels/") {
				manifest.Map.TextureFiles++
			}
		}
		if strings.HasPrefix(lower, "ui/") || strings.Contains(lower, "/extensions/ui") {
			if len(parts) > 1 {
				uiRoots[parts[1]] = true
			}
		}
		switch ext {
		case ".html":
			manifest.UI.HTMLFiles++
		case ".css", ".scss":
			manifest.UI.CSSFiles++
		case ".js", ".ts", ".vue":
			manifest.UI.JavaScriptFiles++
		case ".lua":
			manifest.UI.LuaFiles++
		}
		if strings.HasPrefix(lower, "settings/") {
			manifest.UI.SettingsFiles++
		}
		if strings.HasPrefix(lower, "scripts/") {
			manifest.UI.ScriptFiles++
		}
	}
	for level := range levels {
		manifest.Map.LevelIDs = append(manifest.Map.LevelIDs, level)
	}
	for root := range uiRoots {
		manifest.UI.AppRoots = append(manifest.UI.AppRoots, root)
	}
	sort.Strings(manifest.Map.LevelIDs)
	sort.Strings(manifest.UI.AppRoots)
	for _, document := range manifest.MetadataDocuments {
		if !strings.HasPrefix(strings.ToLower(document.Path), "levels/") {
			continue
		}
		if points, ok := document.Data["spawnPoints"].([]any); ok {
			manifest.Map.SpawnPoints += len(points)
		}
	}
}

// Some ZIP producers set the data-descriptor flag but put the next ZIP header
// immediately after the compressed payload. Recover that container defect only;
// the central directory's uncompressed size and CRC must still match the data.
func openZipEntry(file *zip.File) (io.ReadCloser, error) {
	if file.Flags&0x8 == 0 {
		return file.Open()
	}
	raw, err := file.OpenRaw()
	if err != nil {
		return nil, err
	}
	section, ok := raw.(*io.SectionReader)
	if !ok {
		return file.Open()
	}
	source, offset, size := section.Outer()
	var next [20]byte
	n, _ := source.ReadAt(next[:], offset+size)
	if n < 12 {
		return file.Open()
	}
	signature := binary.LittleEndian.Uint32(next[:4])
	if (signature != 0x04034b50 && signature != 0x02014b50) || signature == file.CRC32 {
		return file.Open()
	}
	// A signatureless descriptor can itself contain a header-like CRC. Keep
	// real descriptor checksum failures fatal when its size fields are present.
	if file.CompressedSize64 >= 1<<32-1 || file.UncompressedSize64 >= 1<<32-1 {
		if n < 20 || (binary.LittleEndian.Uint64(next[4:12]) == file.CompressedSize64 &&
			binary.LittleEndian.Uint64(next[12:20]) == file.UncompressedSize64) {
			return file.Open()
		}
	} else if uint64(binary.LittleEndian.Uint32(next[4:8])) == file.CompressedSize64 &&
		uint64(binary.LittleEndian.Uint32(next[8:12])) == file.UncompressedSize64 {
		return file.Open()
	}
	recovered := *file
	recovered.Flags &^= 0x8
	reader, err := recovered.Open()
	if err != nil || recovered.CRC32 != 0 {
		return reader, err
	}
	// archive/zip skips a zero CRC without a descriptor; recovery must not.
	return &zeroCRCZipReader{ReadCloser: reader}, nil
}

type zeroCRCZipReader struct {
	io.ReadCloser
	crc uint32
}

func (reader *zeroCRCZipReader) Read(buffer []byte) (int, error) {
	n, err := reader.ReadCloser.Read(buffer)
	reader.crc = crc32.Update(reader.crc, crc32.IEEETable, buffer[:n])
	if err == io.EOF && reader.crc != 0 {
		err = zip.ErrChecksum
	}
	return n, err
}

func readZipEntry(file *zip.File, limit int64) ([]byte, error) {
	return readZipEntryContext(context.Background(), file, limit)
}

func readZipEntryContext(ctx context.Context, file *zip.File, limit int64) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if file.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("entry is %d bytes; limit is %d", file.UncompressedSize64, limit)
	}
	reader, err := openZipEntry(file)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: reader}, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("entry exceeded %d-byte read limit", limit)
	}
	return data, nil
}

func FullSHA256(ctx context.Context, filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 4<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			_, _ = hash.Write(buffer[:count])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
