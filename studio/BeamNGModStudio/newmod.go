package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

type NewModRequest struct {
	Name        string `json:"name"`
	ModID       string `json:"modId"`
	Kind        string `json:"kind"`
	Author      string `json:"author"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

func (service *AppService) CreateNewMod(request NewModRequest) (WorkspaceDetail, error) {
	ctx := context.Background()
	name := strings.TrimSpace(request.Name)
	if len(name) < 2 || len(name) > 80 {
		return WorkspaceDetail{}, errors.New("mod name must contain 2 to 80 characters")
	}
	kind := modkit.Kind(strings.ToLower(strings.TrimSpace(request.Kind)))
	switch kind {
	case modkit.KindVehicle, modkit.KindMap, modkit.KindUI, modkit.KindScript:
	default:
		return WorkspaceDetail{}, errors.New("mod kind must be vehicle, map, UI, or script")
	}
	modID := normalizeModID(request.ModID)
	if modID == "" {
		modID = normalizeModID(name)
	}
	if len(modID) < 2 {
		return WorkspaceDetail{}, errors.New("mod ID must contain at least two letters or digits")
	}
	version := strings.TrimSpace(request.Version)
	if version == "" {
		version = "0.1.0"
	}
	if len(version) > 32 || strings.ContainsAny(version, "\r\n\t") {
		return WorkspaceDetail{}, errors.New("version must be a single value of at most 32 characters")
	}
	description := strings.TrimSpace(request.Description)
	if len(description) > 1000 {
		return WorkspaceDetail{}, errors.New("description exceeds 1000 characters")
	}
	settings, err := service.store.loadAppSettings(ctx)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	author := strings.TrimSpace(request.Author)
	if author == "" {
		author = strings.TrimSpace(settings.DefaultAuthor)
	}
	if author == "" {
		author = currentAuthor()
	}
	if len(author) > 80 {
		return WorkspaceDetail{}, errors.New("author exceeds 80 characters")
	}
	if settings.DefaultAuthor == "" && strings.TrimSpace(request.Author) != "" {
		settings.DefaultAuthor = author
		if err := service.store.saveAppSettings(ctx, settings); err != nil {
			return WorkspaceDetail{}, err
		}
	}

	workspaceID, err := modkit.NewID()
	if err != nil {
		return WorkspaceDetail{}, err
	}
	draftDir := filepath.Join(service.config.DataDir, "draft-sources")
	if err := os.MkdirAll(draftDir, 0o755); err != nil {
		return WorkspaceDetail{}, err
	}
	sourcePath := filepath.Join(draftDir, fmt.Sprintf("%s-%s.zip", modID, workspaceID[:8]))
	files, err := newModFiles(kind, modID, name, author, version, description, workspaceID)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	if err := writeTemplateArchive(sourcePath, files); err != nil {
		return WorkspaceDetail{}, err
	}
	keepSource := false
	defer func() {
		if !keepSource {
			_ = os.Remove(sourcePath)
		}
	}()
	manifest, err := modkit.Inspect(ctx, sourcePath)
	if err != nil {
		return WorkspaceDetail{}, fmt.Errorf("inspect generated template: %w", err)
	}
	if !manifest.ValidArchive {
		return WorkspaceDetail{}, errors.New("generated template is not a valid BeamNG archive")
	}
	manifest.Title = name
	manifest.Description = description
	manifest.Author = author
	manifest.Version = version
	info, err := os.Stat(sourcePath)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	item, err := service.store.UpsertArchive(ctx, "project:"+workspaceID, draftDir, sourcePath, info.Size(), info.ModTime(), manifest, nil)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	root := filepath.Join(service.config.WorkspaceDir, workspaceID)
	workspaceManifest, err := modkit.CreateWorkspace(ctx, sourcePath, root, workspaceID, item.EntityID, item.ArtifactID, item.Kind)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	if _, err := service.store.SaveWorkspace(ctx, workspaceManifest, root, sourcePath); err != nil {
		_ = os.RemoveAll(root)
		return WorkspaceDetail{}, err
	}
	keepSource = true
	_ = service.store.AppendEvent(ctx, item.EntityID, "mod_project_created", map[string]any{"workspaceId": workspaceID, "kind": kind, "modId": modID})
	return service.GetWorkspace(workspaceID)
}

func normalizeModID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	builder.Grow(min(len(value), 48))
	separator := false
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			if separator && builder.Len() > 0 {
				builder.WriteByte('_')
			}
			separator = false
			builder.WriteByte(byte(character))
		} else {
			separator = true
		}
		if builder.Len() >= 48 {
			break
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result != "" && result[0] >= '0' && result[0] <= '9' {
		result = "mod_" + result
	}
	return result
}

func currentAuthor() string {
	if account, err := user.Current(); err == nil {
		if name := strings.TrimSpace(account.Name); name != "" {
			return name
		}
		if name := strings.TrimSpace(account.Username); name != "" {
			if _, value, found := strings.Cut(name, `\`); found {
				return value
			}
			return name
		}
	}
	return "Mod author"
}

func newModFiles(kind modkit.Kind, modID, name, author, version, description, projectID string) (map[string][]byte, error) {
	metadata, err := prettyJSON(map[string]any{
		"name": name, "author": author, "version": version, "description": description,
		"type": string(kind),
	})
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{"mod_info/" + modID + ".json": metadata}
	switch kind {
	case modkit.KindVehicle:
		vehicleInfo, err := prettyJSON(map[string]any{"Name": name, "Author": author, "Type": "Car", "default_pc": modID})
		if err != nil {
			return nil, err
		}
		configurationInfo, err := prettyJSON(map[string]any{"Configuration": name, "Description": description, "Author": author, "Type": "Factory"})
		if err != nil {
			return nil, err
		}
		configuration, err := prettyJSON(map[string]any{"format": 2, "model": modID, "parts": map[string]string{"main": modID}, "vars": map[string]any{}})
		if err != nil {
			return nil, err
		}
		jbeam, err := prettyJSON(map[string]any{modID: map[string]any{
			"information": map[string]any{"authors": author, "name": name, "value": 0},
			"slotType":    "main",
			"nodes":       [][]any{{"id", "posX", "posY", "posZ"}, {"n1", -0.5, -0.5, 0.0}, {"n2", 0.5, -0.5, 0.0}, {"n3", -0.5, 0.5, 0.0}, {"n4", 0.5, 0.5, 0.0}},
			"beams":       [][]any{{"id1:", "id2:"}, {"n1", "n2"}, {"n2", "n4"}, {"n4", "n3"}, {"n3", "n1"}, {"n1", "n4"}, {"n2", "n3"}},
		}})
		if err != nil {
			return nil, err
		}
		root := "vehicles/" + modID + "/"
		files[root+"info.json"] = vehicleInfo
		files[root+"info_"+modID+".json"] = configurationInfo
		files[root+modID+".pc"] = configuration
		files[root+modID+".jbeam"] = jbeam
	case modkit.KindMap:
		levelInfo, err := prettyJSON(map[string]any{
			"title": name, "description": description, "authors": author, "levelName": modID,
			"size": []int{1024, 1024}, "defaultSpawnPointName": "spawn_default",
		})
		if err != nil {
			return nil, err
		}
		scene, err := prettyJSON(map[string]any{
			"name": "MissionGroup", "class": "SimGroup", "persistentId": projectID, "children": []any{},
		})
		if err != nil {
			return nil, err
		}
		files["levels/"+modID+"/info.json"] = levelInfo
		files["levels/"+modID+"/main/MissionGroup/items.level.json"] = scene
	case modkit.KindUI:
		directive := camelIdentifier(modID)
		appInfo, err := prettyJSON(map[string]any{
			"name": name, "author": author, "version": version, "description": description,
			"directive": directive, "domElement": "<" + directive + "></" + directive + ">",
			"types": []string{"ui.apps.categories.misc"}, "css": map[string]string{"width": "320px", "height": "180px"},
		})
		if err != nil {
			return nil, err
		}
		root := "ui/modules/apps/" + modID + "/"
		files[root+"app.json"] = appInfo
		files[root+"app.html"] = []byte("<div class=\"" + modID + "\">\n  <strong>" + html.EscapeString(name) + "</strong>\n</div>\n")
		files[root+"app.js"] = []byte("angular.module('beamng.apps').directive(" + strconv.Quote(directive) + ", [function () {\n  return {\n    templateUrl: '/ui/modules/apps/" + modID + "/app.html',\n    replace: true,\n    restrict: 'EA',\n    scope: true\n  }\n}])\n")
		files[root+"app.css"] = []byte("." + modID + " { width: 100%; height: 100%; display: grid; place-items: center; }\n")
	case modkit.KindScript:
		files["lua/ge/extensions/"+modID+".lua"] = []byte("local M = {}\n\nlocal function onExtensionLoaded()\n  log('I', " + strconv.Quote(modID) + ", " + strconv.Quote(name+" loaded") + ")\nend\n\nlocal function onExtensionUnloaded()\nend\n\nM.onExtensionLoaded = onExtensionLoaded\nM.onExtensionUnloaded = onExtensionUnloaded\n\nreturn M\n")
	}
	return files, nil
}

func camelIdentifier(value string) string {
	parts := strings.FieldsFunc(value, func(character rune) bool { return character == '_' || character == '-' || unicode.IsSpace(character) })
	if len(parts) == 0 {
		return "modApp"
	}
	result := parts[0]
	for _, part := range parts[1:] {
		if part != "" {
			result += strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return result
}

func prettyJSON(value any) ([]byte, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func writeTemplateArchive(filename string, files map[string][]byte) error {
	if _, err := os.Stat(filename); err == nil {
		return fmt.Errorf("draft source already exists: %s", filename)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(filename), ".draft-*.zip")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryName)
		}
	}()
	writer := zip.NewWriter(temporary)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	modified := time.Date(2020, time.January, 2, 3, 4, 6, 0, time.UTC)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: modified}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err := entry.Write(files[name]); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filename); err != nil {
		return err
	}
	keep = true
	return nil
}
