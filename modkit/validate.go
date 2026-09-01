package modkit

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alchemy/json5"
)

func ValidateWorkspace(filesRoot string) ValidationResult {
	issues := []Issue{}
	files, err := ListWorkspaceFiles(filesRoot)
	if err != nil {
		return ValidationResult{Valid: false, Issues: []Issue{{Code: "workspace-unreadable", Severity: SeverityError, Message: err.Error()}}}
	}
	seen := map[string]string{}
	knownRootFound := false
	fileSet := map[string]bool{}
	for _, file := range files {
		normalized, pathErr := normalizeArchivePath(file.Path)
		if pathErr != nil {
			issues = append(issues, Issue{Code: "unsafe-path", Severity: SeverityError, Message: pathErr.Error(), Path: file.Path})
			continue
		}
		lower := strings.ToLower(normalized)
		if previous, exists := seen[lower]; exists && previous != normalized {
			issues = append(issues, Issue{Code: "case-collision", Severity: SeverityError, Message: "Path collides case-insensitively with " + previous, Path: normalized})
		}
		seen[lower] = normalized
		fileSet[lower] = true
		root := strings.ToLower(strings.Split(normalized, "/")[0])
		knownRootFound = knownRootFound || knownRoots[root] || strings.EqualFold(normalized, "info.json")
		if file.SizeBytes > maxWorkspaceFile {
			issues = append(issues, Issue{Code: "file-too-large", Severity: SeverityError, Message: "File exceeds workspace export limit", Path: normalized})
		}
		if shouldParseJSON5(lower) {
			filename, joinErr := safeJoin(filesRoot, normalized)
			if joinErr != nil {
				issues = append(issues, Issue{Code: "unsafe-path", Severity: SeverityError, Message: joinErr.Error(), Path: normalized})
				continue
			}
			if file.SizeBytes > maxJBeamFile {
				issues = append(issues, Issue{Code: "parse-limit", Severity: SeverityWarning, Message: "Structured file exceeds parser limit", Path: normalized})
				continue
			}
			data, readErr := os.ReadFile(filename)
			if readErr != nil {
				issues = append(issues, Issue{Code: "file-unreadable", Severity: SeverityError, Message: readErr.Error(), Path: normalized})
				continue
			}
			var parsed any
			if parseErr := json5.Unmarshal(data, &parsed); parseErr != nil {
				issues = append(issues, Issue{Code: "structured-file-invalid", Severity: SeverityError, Message: parseErr.Error(), Path: normalized})
			}
		}
	}
	if !knownRootFound {
		issues = append(issues, Issue{Code: "unknown-content", Severity: SeverityError, Message: "Workspace has no recognized BeamNG content root"})
	}
	for _, file := range files {
		lower := strings.ToLower(file.Path)
		if !strings.HasPrefix(lower, "vehicles/") || !strings.HasSuffix(lower, ".pc") {
			continue
		}
		directory := path.Dir(lower)
		base := strings.TrimSuffix(path.Base(lower), ".pc")
		metadataPath := path.Join(directory, "info_"+base+".json")
		if !fileSet[metadataPath] {
			issues = append(issues, Issue{Code: "variant-metadata-missing", Severity: SeverityWarning, Message: "Vehicle configuration has no matching info_<name>.json", Path: file.Path})
		}
		hasThumbnail := false
		for _, extension := range []string{".jpg", ".jpeg", ".png", ".webp"} {
			hasThumbnail = hasThumbnail || fileSet[path.Join(directory, base+extension)]
		}
		if !hasThumbnail {
			issues = append(issues, Issue{Code: "variant-thumbnail-missing", Severity: SeverityInfo, Message: "Vehicle configuration has no matching preview image", Path: file.Path})
		}
	}
	valid := true
	for _, issue := range issues {
		if issue.Severity == SeverityError {
			valid = false
			break
		}
	}
	return ValidationResult{Valid: valid, Issues: issues}
}

func SetJSONValue(filesRoot, relativePath, dottedPath string, value any) error {
	content, err := ReadWorkspaceText(filesRoot, relativePath)
	if err != nil {
		return err
	}
	root := map[string]any{}
	if err := json5.Unmarshal([]byte(content), &root); err != nil {
		return fmt.Errorf("parse %s: %w", relativePath, err)
	}
	parts := strings.Split(strings.Trim(dottedPath, "."), ".")
	if len(parts) == 0 || parts[0] == "" {
		return fmt.Errorf("field path is required")
	}
	current := root
	for _, part := range parts[:len(parts)-1] {
		child, exists := current[part]
		if !exists {
			created := map[string]any{}
			current[part] = created
			current = created
			continue
		}
		mapped, ok := child.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is not an object", part)
		}
		current = mapped
	}
	current[parts[len(parts)-1]] = value
	encoded, err := json5.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return WriteWorkspaceText(filesRoot, relativePath, string(encoded)+"\n")
}

func CloneVehicleVariant(filesRoot, sourceConfigPath, newBaseName, newDisplayName string) ([]string, error) {
	sourceConfigPath = filepath.ToSlash(sourceConfigPath)
	if !strings.HasSuffix(strings.ToLower(sourceConfigPath), ".pc") || !strings.HasPrefix(strings.ToLower(sourceConfigPath), "vehicles/") {
		return nil, fmt.Errorf("source must be a vehicle .pc configuration")
	}
	newBaseName = strings.TrimSpace(newBaseName)
	if newBaseName == "" || strings.ContainsAny(newBaseName, "/\\:") || newBaseName == "." || newBaseName == ".." {
		return nil, fmt.Errorf("new variant basename is unsafe")
	}
	directory := path.Dir(sourceConfigPath)
	sourceBase := strings.TrimSuffix(path.Base(sourceConfigPath), path.Ext(sourceConfigPath))
	created := []string{}
	keepCreated := false
	defer func() {
		if keepCreated {
			return
		}
		for _, relativePath := range created {
			filename, pathErr := safeJoin(filesRoot, relativePath)
			if pathErr == nil {
				_ = os.Remove(filename)
			}
		}
	}()
	copyFile := func(sourceRelative, targetRelative string) error {
		source, err := safeJoin(filesRoot, sourceRelative)
		if err != nil {
			return err
		}
		target, err := safeJoin(filesRoot, targetRelative)
		if err != nil {
			return err
		}
		if _, err := os.Stat(target); err == nil {
			return fmt.Errorf("target already exists: %s", targetRelative)
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			_ = os.Remove(target)
			return err
		}
		created = append(created, targetRelative)
		return nil
	}
	newConfig := path.Join(directory, newBaseName+".pc")
	if err := copyFile(sourceConfigPath, newConfig); err != nil {
		return nil, err
	}
	sourceMetadata := path.Join(directory, "info_"+sourceBase+".json")
	newMetadata := path.Join(directory, "info_"+newBaseName+".json")
	if sourceMetadataPath, _ := safeJoin(filesRoot, sourceMetadata); fileExists(sourceMetadataPath) {
		if err := copyFile(sourceMetadata, newMetadata); err != nil {
			return created, err
		}
		if strings.TrimSpace(newDisplayName) != "" {
			if err := SetJSONValue(filesRoot, newMetadata, "Configuration", newDisplayName); err != nil {
				return created, err
			}
		}
	}
	for _, extension := range []string{".jpg", ".jpeg", ".png", ".webp"} {
		sourceThumbnail := path.Join(directory, sourceBase+extension)
		sourceThumbnailPath, _ := safeJoin(filesRoot, sourceThumbnail)
		if fileExists(sourceThumbnailPath) {
			if err := copyFile(sourceThumbnail, path.Join(directory, newBaseName+extension)); err != nil {
				return created, err
			}
			break
		}
	}
	sort.Strings(created)
	keepCreated = true
	return created, nil
}

func RenameWorkspacePath(filesRoot, oldRelative, newRelative string) error {
	oldPath, err := safeJoin(filesRoot, oldRelative)
	if err != nil {
		return err
	}
	newPath, err := safeJoin(filesRoot, newRelative)
	if err != nil {
		return err
	}
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("target already exists: %s", newRelative)
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		return err
	}
	return os.Rename(oldPath, newPath)
}

func CreateWorkspaceDirectory(filesRoot, relativePath string) error {
	directory, err := safeJoin(filesRoot, relativePath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(directory); err == nil {
		return fmt.Errorf("path already exists: %s", relativePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.MkdirAll(directory, 0o755)
}

func DeleteWorkspacePath(filesRoot, relativePath string) error {
	filename, err := safeJoin(filesRoot, relativePath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filename); err != nil {
		return err
	}
	return os.RemoveAll(filename)
}

func shouldParseJSON5(lower string) bool {
	return strings.HasSuffix(lower, ".jbeam") || strings.HasSuffix(lower, ".pc") || strings.HasSuffix(lower, ".json")
}

func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	return err == nil && !info.IsDir()
}
