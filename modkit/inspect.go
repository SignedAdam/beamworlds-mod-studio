package modkit

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
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

func Inspect(ctx context.Context, archivePath string) (Manifest, error) {
	stat, err := os.Stat(archivePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("stat archive: %w", err)
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("open ZIP: %w", err)
	}
	defer reader.Close()
	if len(reader.File) > maxEntries {
		return Manifest{}, fmt.Errorf("archive has %d entries; limit is %d", len(reader.File), maxEntries)
	}

	manifest := Manifest{
		SchemaVersion:     SchemaVersion,
		AnalyzerVersion:   AnalyzerVersion,
		AnalyzedAt:        time.Now().UTC(),
		ArchivePath:       archivePath,
		Filename:          filepath.Base(archivePath),
		SizeBytes:         stat.Size(),
		ModifiedAt:        stat.ModTime().UTC(),
		ValidArchive:      true,
		EntryCount:        len(reader.File),
		Namespaces:        map[string][]string{"vehicles": {}, "levels": {}, "ui": {}},
		Members:           make([]ArchiveMember, 0, len(reader.File)),
		MetadataDocuments: []MetadataDocument{},
		Images:            []ImageCandidate{},
		Variants:          []Variant{},
		Issues:            []Issue{},
	}

	fingerprint := sha256.New()
	filesByLower := make(map[string]*zip.File, len(reader.File))
	actualNames := make([]string, 0, len(reader.File))
	logicalCandidates := make([]string, 0, len(reader.File))

	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		name, pathErr := normalizeArchivePath(file.Name)
		if pathErr != nil {
			manifest.ValidArchive = false
			manifest.Issues = append(manifest.Issues, Issue{Code: "unsafe-path", Severity: SeverityError, Message: pathErr.Error(), Path: file.Name})
			continue
		}
		if _, exists := filesByLower[strings.ToLower(name)]; exists {
			manifest.Issues = append(manifest.Issues, Issue{Code: "duplicate-path", Severity: SeverityWarning, Message: "Archive contains duplicate case-insensitive paths", Path: name})
		} else {
			filesByLower[strings.ToLower(name)] = file
		}
		actualNames = append(actualNames, name)
		logicalCandidates = append(logicalCandidates, name)
		manifest.CompressedBytes += file.CompressedSize64
		manifest.UncompressedBytes += file.UncompressedSize64
		manifest.Members = append(manifest.Members, ArchiveMember{
			Path: name, CompressedBytes: file.CompressedSize64, UncompressedBytes: file.UncompressedSize64,
			CRC32: file.CRC32, Method: file.Method, ModifiedAt: file.Modified.UTC(), Directory: file.FileInfo().IsDir(),
		})
		_, _ = fingerprint.Write([]byte(name))
		var facts [26]byte
		binary.LittleEndian.PutUint32(facts[0:4], file.CRC32)
		binary.LittleEndian.PutUint16(facts[4:6], file.Method)
		binary.LittleEndian.PutUint64(facts[6:14], file.CompressedSize64)
		binary.LittleEndian.PutUint64(facts[14:22], file.UncompressedSize64)
		binary.LittleEndian.PutUint32(facts[22:26], uint32(file.Flags))
		_, _ = fingerprint.Write(facts[:])
		if file.Flags&0x1 != 0 {
			manifest.ValidArchive = false
			manifest.Issues = append(manifest.Issues, Issue{Code: "encrypted-entry", Severity: SeverityError, Message: "BeamNG cannot load encrypted ZIP entries", Path: name})
		}
		if !file.FileInfo().IsDir() && file.Method != zip.Store && file.Method != zip.Deflate {
			manifest.Issues = append(manifest.Issues, Issue{Code: "unsupported-compression", Severity: SeverityWarning, Message: fmt.Sprintf("Compression method %d may not load in BeamNG", file.Method), Path: name})
		}
		if file.CompressedSize64 > 0 && file.UncompressedSize64 > 1<<30 && file.UncompressedSize64/file.CompressedSize64 > 1_000 {
			manifest.Issues = append(manifest.Issues, Issue{Code: "extreme-compression", Severity: SeverityWarning, Message: "Entry has an extreme compression ratio", Path: name})
		}
	}
	manifest.CentralFingerprint = hex.EncodeToString(fingerprint.Sum(nil))

	logicalNames, wrapper := unwrapLogicalPaths(logicalCandidates)
	manifest.Wrapper = wrapper
	if wrapper != "" {
		manifest.Issues = append(manifest.Issues, Issue{Code: "nested-root", Severity: SeverityWarning, Message: "Content is wrapped in a top-level directory", Path: wrapper})
	}
	actualByLogical := make(map[string]string, len(logicalNames))
	for i, logical := range logicalNames {
		actualByLogical[strings.ToLower(logical)] = actualNames[i]
	}

	classify(&manifest, logicalNames)
	metadataFiles := prioritizedMetadataFiles(logicalNames, actualByLogical, filesByLower)
	metadataByLower := map[string]map[string]any{}
	metadataBytes := uint64(0)
	for _, candidate := range metadataFiles {
		if len(manifest.MetadataDocuments) >= maxMetadataDocuments {
			manifest.Issues = append(manifest.Issues, Issue{Code: "metadata-limit", Severity: SeverityInfo, Message: "Additional metadata documents were omitted from normalized analysis"})
			break
		}
		if candidate.UncompressedSize64 > maxMetadataFile || metadataBytes+candidate.UncompressedSize64 > maxMetadataTotal {
			manifest.Issues = append(manifest.Issues, Issue{Code: "metadata-too-large", Severity: SeverityWarning, Message: "Metadata document exceeds analysis limits", Path: candidate.Name})
			continue
		}
		data, readErr := readZipEntry(candidate, maxMetadataFile)
		if readErr != nil {
			manifest.Issues = append(manifest.Issues, Issue{Code: "metadata-unreadable", Severity: SeverityWarning, Message: readErr.Error(), Path: candidate.Name})
			continue
		}
		metadataBytes += uint64(len(data))
		parsed := map[string]any{}
		cleaned := strings.TrimRight(strings.TrimPrefix(string(data), "\ufeff"), "\x00")
		if err := json5.Unmarshal([]byte(cleaned), &parsed); err != nil {
			manifest.Issues = append(manifest.Issues, Issue{Code: "metadata-invalid", Severity: SeverityWarning, Message: err.Error(), Path: candidate.Name})
			continue
		}
		normalized, _ := normalizeArchivePath(candidate.Name)
		manifest.MetadataDocuments = append(manifest.MetadataDocuments, MetadataDocument{Path: normalized, Data: parsed})
		metadataByLower[strings.ToLower(normalized)] = parsed
	}
	normalizePrimaryMetadata(&manifest)
	manifest.Images = findImageCandidates(logicalNames, actualByLogical, filesByLower, manifest.MetadataDocuments)
	if len(manifest.Images) > 0 {
		manifest.SelectedImagePath = manifest.Images[0].Path
	}
	manifest.Variants = findVariants(logicalNames, actualByLogical, filesByLower, metadataByLower)
	manifest.JBeam = analyzeJBeam(ctx, logicalNames, actualByLogical, filesByLower, &manifest.Issues)
	analyzeMapAndUI(&manifest, logicalNames)

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

func prioritizedMetadataFiles(logical []string, actual map[string]string, files map[string]*zip.File) []*zip.File {
	type candidate struct {
		file     *zip.File
		priority int
		logical  string
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
		if file := files[strings.ToLower(actualName)]; file != nil {
			candidates = append(candidates, candidate{file: file, priority: priority, logical: lower})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		return candidates[i].logical < candidates[j].logical
	})
	result := make([]*zip.File, len(candidates))
	for i := range candidates {
		result[i] = candidates[i].file
	}
	return result
}

func normalizePrimaryMetadata(manifest *Manifest) {
	for _, document := range manifest.MetadataDocuments {
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

func findImageCandidates(logical []string, actual map[string]string, files map[string]*zip.File, documents []MetadataDocument) []ImageCandidate {
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
		file := files[strings.ToLower(actualName)]
		if file == nil || file.FileInfo().IsDir() {
			return
		}
		seen[lower] = true
		items = append(items, ranked{candidate: ImageCandidate{Path: actualName, Role: role, UncompressedBytes: file.UncompressedSize64, MIME: mime}, priority: priority})
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

func findVariants(logical []string, actual map[string]string, files map[string]*zip.File, metadata map[string]map[string]any) []Variant {
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
				if file := files[strings.ToLower(actualMeta)]; file != nil && file.UncompressedSize64 <= maxMetadataFile {
					if raw, err := readZipEntry(file, maxMetadataFile); err == nil {
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

func analyzeJBeam(ctx context.Context, logical []string, actual map[string]string, files map[string]*zip.File, issues *[]Issue) JBeamStats {
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
		file := files[strings.ToLower(actualName)]
		if file == nil || file.UncompressedSize64 > maxJBeamFile || readBytes+file.UncompressedSize64 > maxJBeamTotal {
			*issues = append(*issues, Issue{Code: "jbeam-limit", Severity: SeverityInfo, Message: "JBeam file omitted from deep counts due to analysis limits", Path: actualName})
			continue
		}
		raw, err := readZipEntry(file, maxJBeamFile)
		if err != nil {
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

func readZipEntry(file *zip.File, limit int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("entry is %d bytes; limit is %d", file.UncompressedSize64, limit)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
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
