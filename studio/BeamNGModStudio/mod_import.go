package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

const (
	modImportRecentLocationsKey  = "mod_import_recent_locations"
	modImportRecentLimit         = 8
	modImportCopyBufferSize      = 4 << 20
	modImportMaxConflictAttempts = 10_000
)

type ModImportLocation struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type ModImportEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	IsDirectory bool   `json:"isDirectory"`
	SizeBytes   int64  `json:"sizeBytes"`
	ModifiedAt  string `json:"modifiedAt"`
}

type ModImportDirectory struct {
	Path            string              `json:"path"`
	ParentPath      string              `json:"parentPath"`
	Breadcrumbs     []ModImportLocation `json:"breadcrumbs"`
	Locations       []ModImportLocation `json:"locations"`
	RecentLocations []ModImportLocation `json:"recentLocations"`
	Entries         []ModImportEntry    `json:"entries"`
	DestinationPath string              `json:"destinationPath"`
	Warning         string              `json:"warning"`
}

type ModImportFailure struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type ModImportResult struct {
	Items         []LibraryItem      `json:"items"`
	Failures      []ModImportFailure `json:"failures"`
	ImportedCount int                `json:"importedCount"`
	ExistingCount int                `json:"existingCount"`
}

// BrowseModImportDirectory lists one directory without opening archive content.
// An empty path intentionally resolves Downloads on every call; the caller does
// not get an implicit "last visited" directory.
func (service *AppService) BrowseModImportDirectory(path string) (ModImportDirectory, error) {

	result := emptyModImportDirectory(service.config.LibraryDir)
	ctx := context.Background()
	if service.store == nil {
		return result, errors.New("mod import storage is unavailable")
	}
	downloads, downloadsErr := modImportDownloadsDirectory()
	if downloadsErr == nil {
		downloads = absoluteModImportPath(downloads)
	}
	home, homeErr := modImportHomeDirectory()
	if homeErr == nil {
		home = absoluteModImportPath(home)
	}

	warning := ""
	requested := strings.TrimSpace(path)
	if requested == "" {
		if downloadsErr == nil {
			requested = downloads
			if _, err := canonicalModImportDirectory(requested); err != nil {
				downloadsErr = err
			}
		}
		if downloadsErr != nil {
			if homeErr != nil {
				return result, fmt.Errorf("resolve Downloads folder: %v; home fallback unavailable: %w", downloadsErr, homeErr)
			}
			requested = home
			warning = fmt.Sprintf("Downloads folder is unavailable (%v); showing Home instead.", downloadsErr)
		}
	}
	if requested == "" {
		return result, errors.New("directory path is required")
	}

	directory, err := canonicalModImportDirectory(requested)
	if err != nil {
		return result, fmt.Errorf("browse directory %q: %w", path, err)
	}
	if warning == "" && downloadsErr != nil && strings.TrimSpace(path) == "" {
		warning = fmt.Sprintf("Downloads folder is unavailable (%v); showing Home instead.", downloadsErr)
	}

	locations, locationWarning := service.modImportLocations(downloads, home)
	if locationWarning != "" {
		warning = joinModImportWarnings(warning, locationWarning)
	}
	entries, err := readModImportEntries(directory)
	if err != nil {
		return result, fmt.Errorf("list directory %q: %w", directory, err)
	}
	recent, err := service.rememberModImportDirectory(ctx, directory)
	if err != nil {
		return result, fmt.Errorf("save recent import directory: %w", err)
	}
	result = ModImportDirectory{
		Path:            directory,
		ParentPath:      modImportParentPath(directory),
		Breadcrumbs:     buildModImportBreadcrumbs(directory, locations),
		Locations:       locations,
		RecentLocations: recent,
		Entries:         entries,
		DestinationPath: absoluteModImportPath(service.config.LibraryDir),
		Warning:         warning,
	}
	return result, nil
}

func (service *AppService) modImportLocations(downloads, home string) ([]ModImportLocation, string) {
	locations := make([]ModImportLocation, 0, 4)
	seen := make(map[string]struct{})
	add := func(name, path, kind string) {
		if kind == "drive" {
			path = absoluteModImportPath(path)
		} else {
			path = canonicalModImportLocationPath(path)
		}
		if path == "" {
			return
		}
		key := modImportPathKey(path)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		locations = append(locations, ModImportLocation{Name: name, Path: path, Kind: kind})
	}
	add("Downloads", downloads, "downloads")
	add("Home", home, "home")
	add("Library", service.config.LibraryDir, "library")

	drives, err := modImportDriveDirectories()
	warning := ""
	if err != nil {
		warning = fmt.Sprintf("Drives could not be enumerated: %v.", err)
	} else {
		for _, drive := range drives {
			name := filepath.VolumeName(drive)
			if name == "" {
				name = filepath.Base(filepath.Clean(drive))
			}
			if name == "" {
				name = drive
			}
			add(name, drive, "drive")
		}
	}
	return locations, warning
}

func emptyModImportDirectory(destination string) ModImportDirectory {
	return ModImportDirectory{
		Breadcrumbs:     make([]ModImportLocation, 0),
		Locations:       make([]ModImportLocation, 0),
		RecentLocations: make([]ModImportLocation, 0),
		Entries:         make([]ModImportEntry, 0),
		DestinationPath: absoluteModImportPath(destination),
	}
}

func readModImportEntries(directory string) ([]ModImportEntry, error) {
	directoryEntries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	entries := make([]ModImportEntry, 0, len(directoryEntries))
	for _, entry := range directoryEntries {
		if entry.IsDir() && strings.HasPrefix(strings.ToLower(entry.Name()), ".beamworlds-managed-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			// A file can disappear between ReadDir and Info. It is not a
			// navigable/importable entry anymore, so leave it out of this
			// snapshot rather than failing the whole directory listing.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("stat %q: %w", entry.Name(), err)
		}
		isDirectory := info.IsDir()
		if !isDirectory && (!info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".zip")) {
			continue
		}
		entries = append(entries, ModImportEntry{
			Name:        entry.Name(),
			Path:        filepath.Join(directory, entry.Name()),
			IsDirectory: isDirectory,
			SizeBytes:   info.Size(),
			ModifiedAt:  info.ModTime().UTC().Format(time.RFC3339Nano),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDirectory != entries[j].IsDirectory {
			return entries[i].IsDirectory
		}
		left, right := strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name)
		if left != right {
			return left < right
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

func canonicalModImportDirectory(value string) (string, error) {
	path := absoluteModImportPath(value)
	if path == "" {
		return "", errors.New("directory path is required")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	resolved = absoluteModImportPath(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat path: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return resolved, nil
}

func absoluteModImportPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = filepath.Clean(filepath.FromSlash(value))
	if absolute, err := filepath.Abs(value); err == nil {
		return filepath.Clean(absolute)
	}
	return value
}

func canonicalModImportLocationPath(value string) string {
	path := absoluteModImportPath(value)
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return absoluteModImportPath(resolved)
	}
	return path
}

func modImportParentPath(directory string) string {
	parent := filepath.Clean(filepath.Dir(directory))
	if parent == directory || samePath(parent, directory) {
		return ""
	}
	return parent
}

func buildModImportBreadcrumbs(directory string, locations []ModImportLocation) []ModImportLocation {
	byPath := make(map[string]ModImportLocation, len(locations))
	for _, location := range locations {
		byPath[modImportPathKey(location.Path)] = location
	}
	breadcrumbs := make([]ModImportLocation, 0, 8)
	for current := directory; current != ""; {
		if location, ok := byPath[modImportPathKey(current)]; ok {
			breadcrumbs = append(breadcrumbs, location)
		} else {
			breadcrumbs = append(breadcrumbs, ModImportLocation{Name: modImportPathName(current), Path: current, Kind: "recent"})
		}
		parent := modImportParentPath(current)
		if parent == "" {
			break
		}
		current = parent
	}
	for left, right := 0, len(breadcrumbs)-1; left < right; left, right = left+1, right-1 {
		breadcrumbs[left], breadcrumbs[right] = breadcrumbs[right], breadcrumbs[left]
	}
	return breadcrumbs
}

func modImportPathName(path string) string {
	path = filepath.Clean(path)
	if volume := filepath.VolumeName(path); volume != "" {
		trimmed := strings.TrimRight(path, `/\\`)
		if strings.EqualFold(trimmed, strings.TrimRight(volume, `/\\`)) {
			return volume + string(os.PathSeparator)
		}
	}
	name := filepath.Base(path)
	if name == "." || name == string(os.PathSeparator) || name == "" {
		return path
	}
	return name
}

func modImportPathKey(path string) string {
	return strings.ToLower(filepath.Clean(filepath.FromSlash(strings.TrimSpace(path))))
}

func prependModImportRecent(recent []ModImportLocation, directory string) []ModImportLocation {
	result := make([]ModImportLocation, 0, modImportRecentLimit)
	key := modImportPathKey(directory)
	result = append(result, ModImportLocation{Name: modImportPathName(directory), Path: directory, Kind: "recent"})
	for _, location := range recent {
		if len(result) >= modImportRecentLimit || modImportPathKey(location.Path) == key {
			continue
		}
		path := absoluteModImportPath(location.Path)
		if path == "" {
			continue
		}
		result = append(result, ModImportLocation{Name: firstModImportLocationName(location.Name, path), Path: path, Kind: "recent"})
	}
	return result
}

func firstModImportLocationName(name, path string) string {
	if strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return modImportPathName(path)
}

func (service *AppService) loadModImportRecentLocations(ctx context.Context) ([]ModImportLocation, error) {
	value, err := service.store.readSetting(ctx, modImportRecentLocationsKey)
	if errors.Is(err, sql.ErrNoRows) {
		return make([]ModImportLocation, 0), nil
	}
	if err != nil {
		return nil, err
	}
	var stored []ModImportLocation
	if err := json.Unmarshal([]byte(value), &stored); err != nil {
		return nil, fmt.Errorf("decode stored locations: %w", err)
	}
	result := make([]ModImportLocation, 0, modImportRecentLimit)
	seen := make(map[string]struct{}, len(stored))
	for _, location := range stored {
		path := absoluteModImportPath(location.Path)
		if path == "" {
			continue
		}
		key := modImportPathKey(path)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, ModImportLocation{Name: firstModImportLocationName(location.Name, path), Path: path, Kind: "recent"})
		if len(result) >= modImportRecentLimit {
			break
		}
	}
	return result, nil
}

func (service *AppService) rememberModImportDirectory(ctx context.Context, directory string) ([]ModImportLocation, error) {
	service.store.writeMu.Lock()
	defer service.store.writeMu.Unlock()
	recent, err := service.loadModImportRecentLocations(ctx)
	if err != nil {
		return nil, err
	}
	recent = prependModImportRecent(recent, directory)
	encoded, err := json.Marshal(recent)
	if err != nil {
		return nil, err
	}
	if err := service.store.writeSetting(ctx, modImportRecentLocationsKey, string(encoded)); err != nil {
		return nil, err
	}
	return recent, nil
}

func joinModImportWarnings(left, right string) string {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	return left + " " + right
}

// ImportMods copies and indexes selected archives one at a time. The service
// mutex is also held by ScanLibrary, preventing a full scan's reconciliation
// from observing a destination halfway through publication.
func (service *AppService) ImportMods(paths []string) (ModImportResult, error) {
	service.modImportMu.Lock()
	defer service.modImportMu.Unlock()

	result := emptyModImportResult()
	if service.store == nil {
		return result, errors.New("mod import storage is unavailable")
	}
	destination, err := service.modImportDestination()
	if err != nil {
		return result, err
	}
	ctx := context.Background()
	for _, input := range paths {
		item, existing, importErr := service.importOneMod(ctx, destination, input)
		if importErr != nil {
			result.Failures = append(result.Failures, ModImportFailure{Path: strings.TrimSpace(input), Message: importErr.Error()})
			continue
		}
		result.Items = append(result.Items, item)
		if existing {
			result.ExistingCount++
		} else {
			result.ImportedCount++
		}
	}
	return result, nil
}

func emptyModImportResult() ModImportResult {
	return ModImportResult{Items: make([]LibraryItem, 0), Failures: make([]ModImportFailure, 0)}
}

func (service *AppService) modImportDestination() (string, error) {
	configured := strings.TrimSpace(service.config.LibraryDir)
	if configured == "" {
		return "", errors.New("mod library directory is not configured; choose a Library directory in setup before importing")
	}
	destination := absoluteModImportPath(configured)
	info, err := os.Stat(destination)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("mod library directory %q does not exist; choose an existing writable library directory", destination)
		}
		return "", fmt.Errorf("access mod library directory %q: %w", destination, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("mod library destination %q is not a directory", destination)
	}
	if err := verifyDirectoryWritable(destination); err != nil {
		return "", fmt.Errorf("mod library destination %q is not writable: %w", destination, err)
	}
	return destination, nil
}

func (service *AppService) importOneMod(ctx context.Context, destination, input string) (LibraryItem, bool, error) {
	source, err := canonicalModImportFile(input)
	if err != nil {
		return LibraryItem{}, false, err
	}
	baseName := filepath.Base(filepath.FromSlash(strings.TrimSpace(input)))
	if baseName == "." || baseName == string(os.PathSeparator) || baseName == "" {
		return LibraryItem{}, false, errors.New("selected archive has no usable filename")
	}

	stageDirectory, err := os.MkdirTemp(destination, ".beamworlds-managed-import-*")
	if err != nil {
		return LibraryItem{}, false, fmt.Errorf("stage archive in %q: %w", destination, err)
	}
	defer os.RemoveAll(stageDirectory)
	stagePath := filepath.Join(stageDirectory, baseName)
	stage, err := os.OpenFile(stagePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return LibraryItem{}, false, fmt.Errorf("create staged archive: %w", err)
	}

	if err := copyModImportArchive(stage, source); err != nil {
		_ = stage.Close()
		return LibraryItem{}, false, fmt.Errorf("copy %q: %w", source, err)
	}
	if err := stage.Sync(); err != nil {
		_ = stage.Close()
		return LibraryItem{}, false, fmt.Errorf("flush staged archive: %w", err)
	}
	if err := stage.Close(); err != nil {
		return LibraryItem{}, false, fmt.Errorf("close staged archive: %w", err)
	}

	manifest, err := modkit.Inspect(ctx, stagePath)
	if err != nil {
		return LibraryItem{}, false, fmt.Errorf("inspect %q: %w", source, err)
	}
	if !manifest.ValidArchive {
		return LibraryItem{}, false, fmt.Errorf("%q is not a valid BeamNG ZIP archive: %s", source, modImportManifestIssues(manifest))
	}
	checksum, err := modkit.FullSHA256(ctx, stagePath)
	if err != nil {
		return LibraryItem{}, false, fmt.Errorf("checksum %q: %w", source, err)
	}
	stageInfo, err := os.Stat(stagePath)
	if err != nil {
		return LibraryItem{}, false, fmt.Errorf("stat staged archive: %w", err)
	}
	if !stageInfo.Mode().IsRegular() {
		return LibraryItem{}, false, errors.New("staged archive is not a regular file")
	}

	extension := filepath.Ext(baseName)
	stem := strings.TrimSuffix(baseName, extension)
	if stem == "" {
		stem = "mod"
	}
	for index := range modImportMaxConflictAttempts {
		candidateName := baseName
		if index > 0 {
			candidateName = fmt.Sprintf("%s (%d)%s", stem, index, extension)
		}
		candidate := filepath.Join(destination, candidateName)
		occupied, same, existingInfo, matchErr := modImportDestinationMatch(ctx, candidate, stageInfo.Size(), checksum)
		if matchErr != nil {
			return LibraryItem{}, false, matchErr
		}
		if occupied {
			if !same {
				continue
			}
			manifestForIndex := modImportManifestForPath(manifest, candidate, existingInfo, checksum)
			item, err := service.indexImportedArchive(ctx, destination, candidate, manifestForIndex)
			if err != nil {
				// UpsertArchive commits before hydration. Never remove an
				// existing path when indexing reports an error.
				return LibraryItem{}, false, fmt.Errorf("index existing archive %q: %w", candidate, err)
			}
			return item, true, nil
		}

		published, alreadyExists, publishErr := publishModImportArchive(stagePath, candidate)
		if publishErr != nil {
			return LibraryItem{}, false, publishErr
		}
		if alreadyExists {
			continue
		}
		if !published {
			return LibraryItem{}, false, fmt.Errorf("publish archive as %q failed without a result", candidate)
		}
		publishedInfo, statErr := os.Stat(candidate)
		if statErr != nil {
			return LibraryItem{}, false, fmt.Errorf("stat published archive %q: %w", candidate, statErr)
		}
		manifestForIndex := modImportManifestForPath(manifest, candidate, publishedInfo, checksum)
		item, err := service.indexImportedArchive(ctx, destination, candidate, manifestForIndex)
		if err != nil {
			// The database may already contain the committed link. Preserve
			// the final archive so the persisted state remains truthful.
			return LibraryItem{}, false, fmt.Errorf("index imported archive %q: %w", candidate, err)
		}
		return item, false, nil
	}
	return LibraryItem{}, false, fmt.Errorf("could not choose a free destination filename for %q after %d attempts", baseName, modImportMaxConflictAttempts)
}

func canonicalModImportFile(value string) (string, error) {
	original := strings.TrimSpace(value)
	if original == "" {
		return "", errors.New("archive path is required")
	}
	if !strings.EqualFold(filepath.Ext(original), ".zip") {
		return "", errors.New("selected file is not a .zip archive")
	}
	path := absoluteModImportPath(original)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve archive path: %w", err)
	}
	resolved = absoluteModImportPath(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat archive: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("selected archive is not a regular file")
	}
	return resolved, nil
}

func copyModImportArchive(destination io.Writer, sourcePath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	buffer := make([]byte, modImportCopyBufferSize)
	_, err = io.CopyBuffer(destination, source, buffer)
	return err
}

func publishModImportArchive(stagePath, destinationPath string) (published, alreadyExists bool, err error) {
	linkErr := os.Link(stagePath, destinationPath)
	if linkErr == nil {
		return true, false, nil
	}
	if errors.Is(linkErr, fs.ErrExist) {
		return false, true, nil
	}

	target, createErr := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(createErr, fs.ErrExist) {
		return false, true, nil
	}
	if createErr != nil {
		return false, false, fmt.Errorf("publish archive as %q: link failed (%v), exclusive copy failed: %w", destinationPath, linkErr, createErr)
	}
	owned := true
	defer func() {
		if owned {
			_ = os.Remove(destinationPath)
		}
	}()
	copyErr := copyModImportArchive(target, stagePath)
	var syncErr error
	if copyErr == nil {
		syncErr = target.Sync()
	}
	closeErr := target.Close()
	if copyErr != nil {
		return false, false, fmt.Errorf("publish archive as %q: copy failed: %w", destinationPath, copyErr)
	}
	if syncErr != nil {
		return false, false, fmt.Errorf("publish archive as %q: flush failed: %w", destinationPath, syncErr)
	}
	if closeErr != nil {
		return false, false, fmt.Errorf("publish archive as %q: close failed: %w", destinationPath, closeErr)
	}
	owned = false
	return true, false, nil
}

func modImportDestinationMatch(ctx context.Context, path string, size int64, checksum string) (occupied, same bool, info os.FileInfo, err error) {
	entry, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil, nil
	}
	if err != nil {
		return false, false, nil, fmt.Errorf("inspect destination %q: %w", path, err)
	}
	if entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() {
		return true, false, nil, nil
	}
	if entry.Size() != size {
		return true, false, entry, nil
	}
	existingChecksum, err := modkit.FullSHA256(ctx, path)
	if err != nil {
		return false, false, nil, fmt.Errorf("checksum destination %q: %w", path, err)
	}
	return true, strings.EqualFold(existingChecksum, checksum), entry, nil
}

func modImportManifestForPath(manifest modkit.Manifest, path string, info os.FileInfo, checksum string) modkit.Manifest {
	manifest.ArchivePath = path
	manifest.Filename = filepath.Base(path)
	manifest.SizeBytes = info.Size()
	manifest.ModifiedAt = info.ModTime().UTC()
	manifest.FullSHA256 = checksum
	return manifest
}

func (service *AppService) indexImportedArchive(ctx context.Context, destination, archivePath string, manifest modkit.Manifest) (LibraryItem, error) {
	var asset *AssetRecord
	if manifest.SelectedImagePath != "" {
		cached, err := modkit.ExtractImage(archivePath, manifest.SelectedImagePath, service.config.ImageCacheDir)
		if err == nil {
			asset = &AssetRecord{SHA256: cached.ID, Path: cached.Path, MIME: cached.MIME, Width: cached.Width, Height: cached.Height, SizeBytes: cached.SizeBytes}
			_, _ = ensureAssetThumbnail(*asset)
		}
	}
	archiveInfo, err := os.Stat(archivePath)
	if err != nil {
		return LibraryItem{}, fmt.Errorf("stat archive before indexing: %w", err)
	}
	return service.store.UpsertArchive(ctx, "", destination, archivePath, archiveInfo.Size(), archiveInfo.ModTime(), manifest, asset)
}

func modImportManifestIssues(manifest modkit.Manifest) string {
	for _, issue := range manifest.Issues {
		if issue.Severity == modkit.SeverityError && strings.TrimSpace(issue.Message) != "" {
			return issue.Message
		}
	}
	return "archive inspection reported an invalid archive"
}
