package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestOrganizationMigrationAndMembershipContracts(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	ctx := context.Background()
	for _, table := range []string{"library_folders", "library_folder_entities", "mod_tags", "mod_tag_entities", "mod_presets", "mod_preset_entities", "mod_profiles", "mod_profile_presets", "workspace_drafts"} {
		var name string
		if err := service.store.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("migration table %s: %v", table, err)
		}
	}
	first, err := service.CreateNewMod(NewModRequest{Name: "Organization One", ModID: "organization_one", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateNewMod(NewModRequest{Name: "Organization Two", ModID: "organization_two", Kind: "ui", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := service.CreateLibraryFolder("Favorites", "")
	if err != nil {
		t.Fatal(err)
	}
	folder := state.Folders[0]
	if err := service.MoveLibraryItem(first.Entity.EntityID, folder.ID); err != nil {
		t.Fatal(err)
	}
	folderItems, err := service.ListLibrary("all", "all", "", folder.ID)
	if err != nil || len(folderItems) != 1 || folderItems[0].EntityID != first.Entity.EntityID {
		t.Fatalf("folder listing = %#v, err %v", folderItems, err)
	}
	unfiled, err := service.ListLibrary("all", "all", "", "unfiled")
	if err != nil || !containsEntity(unfiled, second.Entity.EntityID) || containsEntity(unfiled, first.Entity.EntityID) {
		t.Fatalf("unfiled listing does not honor memberships: %#v, err %v", unfiled, err)
	}

	state, err = service.CreatePreset("Off-road", "Reusable off-road selection")
	if err != nil {
		t.Fatal(err)
	}
	preset := findReusablePreset(t, state, "Off-road")
	if err := service.SetPresetMod(preset.ID, second.Entity.EntityID, true); err != nil {
		t.Fatal(err)
	}
	presetDetail, err := service.GetPreset(preset.ID)
	if err != nil || !slices.Contains(presetDetail.EntityIDs, second.Entity.EntityID) {
		t.Fatalf("preset membership = %#v, err %v", presetDetail, err)
	}

	state, err = service.CreateProfile("Career")
	if err != nil {
		t.Fatal(err)
	}
	profile := state.Profiles[0]
	profileDetail, err := service.GetProfile(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(profileDetail.Presets) == 0 || !profileDetail.Presets[0].Default || !profileDetail.Presets[0].Selected || profileDetail.Presets[0].ID != profile.DefaultPresetID {
		t.Fatalf("profile default preset invariant failed: %#v", profileDetail.Presets)
	}
	if err := service.SetProfileMod(profile.ID, first.Entity.EntityID, true); err != nil {
		t.Fatal(err)
	}
	if err := service.SetProfilePreset(profile.ID, preset.ID, true); err != nil {
		t.Fatal(err)
	}
	profileDetail, err = service.GetProfile(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(profileDetail.Mods) != 2 {
		t.Fatalf("effective profile mods = %d, want 2: %#v", len(profileDetail.Mods), profileDetail.Mods)
	}
	firstMod := findProfileMod(t, profileDetail, first.Entity.EntityID)
	if !slices.Contains(firstMod.PresetIDs, profile.DefaultPresetID) {
		t.Fatalf("direct selection was not routed to default preset: %#v", firstMod)
	}
	secondMod := findProfileMod(t, profileDetail, second.Entity.EntityID)
	if !slices.Contains(secondMod.PresetIDs, preset.ID) {
		t.Fatalf("reusable preset membership missing: %#v", secondMod)
	}
	if _, err := service.DeletePreset(profile.DefaultPresetID); err == nil {
		t.Fatal("profile default preset was deletable")
	}
	state, err = service.DeleteLibraryFolder(folder.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Folders) != 0 {
		t.Fatalf("folder was not deleted: %#v", state.Folders)
	}
	unfiled, err = service.ListLibrary("all", "all", "", "unfiled")
	if err != nil || !containsEntity(unfiled, first.Entity.EntityID) {
		t.Fatalf("deleting a folder did not safely unfile its mod: %#v, err %v", unfiled, err)
	}
}

func TestModTagVisualMigrationUpgradesExistingDatabase(t *testing.T) {
	t.Parallel()
	filename := filepath.Join(t.TempDir(), "legacy.sqlite")
	legacy, err := sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE mod_tags (
		id TEXT PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO mod_tags(id,name,created_at,updated_at) VALUES('legacy','Legacy','before','before')`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(filename)
	if err != nil {
		t.Fatal(err)
	}
	var color, icon string
	if err := store.db.QueryRow(`SELECT color,icon FROM mod_tags WHERE id='legacy'`).Scan(&color, &icon); err != nil {
		t.Fatal(err)
	}
	if color != defaultModTagColor || icon != defaultModTagIcon {
		t.Fatalf("migrated tag visuals = %q/%q", color, icon)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(filename)
	if err != nil {
		t.Fatalf("idempotent reopen: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestVirusScanHashMigrationUpgradesExistingDatabase(t *testing.T) {
	t.Parallel()
	filename := filepath.Join(t.TempDir(), "legacy-scans.sqlite")
	legacy, err := sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE virus_scans (
		id TEXT PRIMARY KEY, entity_id TEXT NOT NULL, artifact_id TEXT NOT NULL,
		mode TEXT NOT NULL, status TEXT NOT NULL, current_stage TEXT NOT NULL, verdict TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL, error TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE virus_scan_stages (
		id TEXT PRIMARY KEY, scan_id TEXT NOT NULL, entity_id TEXT NOT NULL, artifact_id TEXT NOT NULL,
		stage TEXT NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '',
		parameters_json TEXT NOT NULL DEFAULT '{}', inputs_json TEXT NOT NULL DEFAULT '[]',
		metadata_file TEXT NOT NULL, audit_id TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(filename)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"virus_scans", "virus_scan_stages"} {
		var count int
		query := `SELECT COUNT(*) FROM pragma_table_info('` + table + `') WHERE name='file_sha256'`
		if err := store.db.QueryRow(query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s file_sha256 columns = %d, want 1", table, count)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(filename)
	if err != nil {
		t.Fatalf("idempotent reopen: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCustomTagsAndStructuredLibrarySearch(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	state, err := service.Organization()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tags) != len(exampleModTagNames) {
		t.Fatalf("seeded tags = %d, want %d: %#v", len(state.Tags), len(exampleModTagNames), state.Tags)
	}
	for _, name := range exampleModTagNames {
		tag := findModTag(t, state, name)
		if tag.ModCount != 0 {
			t.Fatalf("fresh tag %q unexpectedly has %d assignments", name, tag.ModCount)
		}
		if tag.Color != defaultModTagColor || tag.Icon != defaultModTagIcon {
			t.Fatalf("seeded tag %q visuals = %q/%q", name, tag.Color, tag.Icon)
		}
	}

	first, err := service.CreateNewMod(NewModRequest{Name: "Road Mission", ModID: "road_mission", Kind: "script", Author: "Ava Builder", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateNewMod(NewModRequest{Name: "Interface Pack", ModID: "interface_pack", Kind: "ui", Author: "Bea Builder", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	firstItem, err := service.store.GetLibraryItem(context.Background(), first.Entity.EntityID)
	if err != nil || len(firstItem.Tags) != 0 {
		t.Fatalf("fresh mod tags = %#v, err %v", firstItem.Tags, err)
	}

	state, err = service.CreateModTag("Favorite", "#e85d8f", "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	car := findModTag(t, state, "Car")
	gameplay := findModTag(t, state, "Gameplay Overhaul")
	favorite := findModTag(t, state, "Favorite")
	if favorite.Color != "#e85d8f" || favorite.Icon != "vehicle" {
		t.Fatalf("created tag visuals = %#v", favorite)
	}
	state, err = service.UpdateModTagVisual(favorite.ID, "#a978e5", "shield")
	if err != nil {
		t.Fatal(err)
	}
	favorite = findModTag(t, state, "Favorite")
	if favorite.Color != "#a978e5" || favorite.Icon != "shield" {
		t.Fatalf("updated tag visuals = %#v", favorite)
	}
	if _, err := service.SetLibraryItemTags(first.Entity.EntityID, []string{car.ID, gameplay.ID, favorite.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateModTag("favorite", "#7a8791", "tag"); err == nil {
		t.Fatal("case-insensitive duplicate tag was accepted")
	}
	if _, err := service.CreateModTag("Invalid Color", "purple", "tag"); err == nil {
		t.Fatal("invalid tag color was accepted")
	}
	if _, err := service.UpdateModTagVisual(favorite.ID, "#a978e5", "star"); err == nil {
		t.Fatal("non-preset tag icon was accepted")
	}
	if _, err := service.SetLibraryItemTags(first.Entity.EntityID, []string{car.ID, "missing-tag"}); err == nil {
		t.Fatal("unknown tag assignment was accepted")
	}
	firstItem, err = service.store.GetLibraryItem(context.Background(), first.Entity.EntityID)
	if err != nil || len(firstItem.Tags) != 3 {
		t.Fatalf("failed assignment changed existing tags: %#v, err %v", firstItem.Tags, err)
	}
	detail, err := service.GetEntity(first.Entity.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	added := 0
	for _, event := range detail.History {
		if event.Type == "tag_added" {
			added++
		}
	}
	if added != 3 {
		t.Fatalf("tag assignment history has %d additions, want 3: %#v", added, detail.History)
	}

	for query, entityID := range map[string]string{
		`in:tag "Gameplay Overhaul"`: first.Entity.EntityID,
		`in:tag Car is:unscanned`:    first.Entity.EntityID,
		`in:author "Ava Builder"`:    first.Entity.EntityID,
		`car`:                        first.Entity.EntityID,
		`in:name "Interface Pack"`:   second.Entity.EntityID,
	} {
		items, err := service.ListLibrary("all", "all", query, "all")
		if err != nil || len(items) != 1 || items[0].EntityID != entityID {
			t.Fatalf("query %q = %#v, err %v", query, items, err)
		}
	}
	missing, err := service.ListLibrary("all", "all", "in:source missing", "all")
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing-source scope query = %#v, err %v", missing, err)
	}

	state, err = service.CreateLibraryFolder("Road Tests", "")
	if err != nil {
		t.Fatal(err)
	}
	collection := state.Folders[0]
	if err := service.MoveLibraryItem(first.Entity.EntityID, collection.ID); err != nil {
		t.Fatal(err)
	}
	items, err := service.ListLibrary("all", "all", `in:collection "Road Tests"`, "all")
	if err != nil || len(items) != 1 || items[0].EntityID != first.Entity.EntityID {
		t.Fatalf("collection query = %#v, err %v", items, err)
	}

	state, err = service.RenameModTag(favorite.ID, "Must Play")
	if err != nil {
		t.Fatal(err)
	}
	renamed := findModTag(t, state, "Must Play")
	items, err = service.ListLibrary("all", "all", `in:tag "Must Play"`, "all")
	if err != nil || len(items) != 1 || items[0].EntityID != first.Entity.EntityID {
		t.Fatalf("renamed tag query = %#v, err %v", items, err)
	}
	if _, err := service.SetLibraryItemTags(first.Entity.EntityID, []string{car.ID, gameplay.ID}); err != nil {
		t.Fatal(err)
	}
	detail, err = service.GetEntity(first.Entity.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	for _, event := range detail.History {
		if event.Type == "tag_removed" && event.Data["tagName"] == "Must Play" {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("tag removal missing from history: %#v", detail.History)
	}
	state, err = service.DeleteModTag(renamed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(state.Tags, func(tag ModTag) bool { return tag.ID == renamed.ID }) {
		t.Fatal("deleted tag remains in organization state")
	}
	firstItem, err = service.store.GetLibraryItem(context.Background(), first.Entity.EntityID)
	if err != nil || slices.ContainsFunc(firstItem.Tags, func(tag ModTag) bool { return tag.ID == renamed.ID }) {
		t.Fatalf("deleted tag assignment remains: %#v, err %v", firstItem.Tags, err)
	}
	state, err = service.DeleteModTag(gameplay.ID)
	if err != nil {
		t.Fatal(err)
	}
	detail, err = service.GetEntity(first.Entity.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	deletedAssignmentRecorded := false
	for _, event := range detail.History {
		if event.Type == "tag_removed" && event.Data["tagName"] == "Gameplay Overhaul" {
			deletedAssignmentRecorded = true
		}
	}
	if !deletedAssignmentRecorded {
		t.Fatalf("tag deletion did not record removed assignments: %#v", detail.History)
	}
}

func TestDraftRecoveryAndWorkspaceTreeOperations(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	detail, err := service.CreateNewMod(NewModRequest{Name: "Draft Test", ModID: "draft_test", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := detail.Workspace.ID
	originalPath := "lua/ge/extensions/draft_test.lua"
	file, err := service.ReadWorkspaceFile(workspaceID, originalPath)
	if err != nil {
		t.Fatal(err)
	}
	draftContent := file.Content + "\n-- unsaved across restart\n"
	if err := service.SaveWorkspaceDraft(workspaceID, originalPath, draftContent, file.SHA256); err != nil {
		t.Fatal(err)
	}

	databasePath := service.config.DatabasePath
	config := service.config
	if err := service.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedStore, err := OpenStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopenedStore.Close() })
	service = NewAppService(config, reopenedStore, func(string, any) {})
	recovered, err := service.GetWorkspace(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Drafts) != 1 || recovered.Drafts[0].Content != draftContent {
		t.Fatalf("draft was not recovered after store restart: %#v", recovered.Drafts)
	}

	directory := "lua/ge/extensions/nested"
	if err := service.CreateWorkspaceDirectory(workspaceID, directory); err != nil {
		t.Fatal(err)
	}
	movedPath := directory + "/draft_test.lua"
	if err := service.RenameWorkspacePath(workspaceID, originalPath, movedPath); err != nil {
		t.Fatal(err)
	}
	recovered, err = service.GetWorkspace(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(recovered.Directories, directory) || len(recovered.Drafts) != 1 || recovered.Drafts[0].Path != movedPath {
		t.Fatalf("tree move did not migrate directory and draft state: directories=%#v drafts=%#v", recovered.Directories, recovered.Drafts)
	}
	if err := service.WriteWorkspaceFile(workspaceID, movedPath, draftContent, file.SHA256); err != nil {
		t.Fatal(err)
	}
	recovered, err = service.GetWorkspace(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Drafts) != 0 {
		t.Fatalf("saved draft remains persisted: %#v", recovered.Drafts)
	}
	movedDirectory := "lua/ge/extensions/moved"
	if err := service.RenameWorkspacePath(workspaceID, directory, movedDirectory); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadWorkspaceFile(workspaceID, movedDirectory+"/draft_test.lua"); err != nil {
		t.Fatalf("file did not move with its folder: %v", err)
	}
	if err := service.DeleteWorkspacePath(workspaceID, movedDirectory); err != nil {
		t.Fatal(err)
	}
	recovered, err = service.GetWorkspace(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, directoryPath := range recovered.Directories {
		if directoryPath == movedDirectory || strings.HasPrefix(directoryPath, movedDirectory+"/") {
			t.Fatalf("deleted directory remains in tree: %s", directoryPath)
		}
	}
}

func TestModProfileActivationUsesSharedBeamNGData(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	first, err := service.CreateNewMod(NewModRequest{Name: "Profile One", ModID: "profile_one", Kind: "script", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateNewMod(NewModRequest{Name: "Profile Two", ModID: "profile_two", Kind: "ui", Version: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := service.CreatePreset("Shared", "")
	if err != nil {
		t.Fatal(err)
	}
	preset := findReusablePreset(t, state, "Shared")
	if err := service.SetPresetMod(preset.ID, second.Entity.EntityID, true); err != nil {
		t.Fatal(err)
	}
	state, err = service.CreateProfile("Isolated")
	if err != nil {
		t.Fatal(err)
	}
	profile := state.Profiles[0]
	if err := service.SetProfileMod(profile.ID, first.Entity.EntityID, true); err != nil {
		t.Fatal(err)
	}
	if err := service.SetProfilePreset(profile.ID, preset.ID, true); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(service.config.ActiveModsDir, "existing-user-mod.zip")
	if err := os.WriteFile(sentinel, []byte("existing user mod"), 0o644); err != nil {
		t.Fatal(err)
	}
	sentinelKey, err := beamNGModKey(sentinel, service.config.ActiveModsDir)
	if err != nil {
		t.Fatal(err)
	}
	nativeDB, err := json.Marshal(map[string]any{"header": map[string]any{"version": 1.1}, "mods": map[string]any{sentinelKey: map[string]any{"active": true, "fullpath": "/mods/existing-user-mod.zip"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.config.ActiveModsDir, "db.json"), nativeDB, 0o644); err != nil {
		t.Fatal(err)
	}
	progress := []ProfileProgress{}
	service.emit = func(name string, value any) {
		if name == "profile:progress" {
			progress = append(progress, value.(ProfileProgress))
		}
	}
	activation, err := service.ActivateProfile(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(activation.ModsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || activation.ModCount != 2 {
		t.Fatalf("activated mods = %d entries / %d record, want 2", len(entries), activation.ModCount)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("existing active mod was touched: %v", err)
	}
	if !samePath(activation.UserPath, service.config.BeamNGRoot) {
		t.Fatalf("mod profile changed BeamNG user data root: %s", activation.UserPath)
	}
	if !samePath(activation.ModsPath, filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)) {
		t.Fatalf("managed mod path = %s", activation.ModsPath)
	}
	var appliedDB struct {
		Mods map[string]struct {
			Active bool `json:"active"`
		} `json:"mods"`
	}
	appliedPayload, err := os.ReadFile(filepath.Join(service.config.ActiveModsDir, "db.json"))
	if err != nil || json.Unmarshal(appliedPayload, &appliedDB) != nil || appliedDB.Mods[sentinelKey].Active {
		t.Fatalf("existing native mod was not disabled in BeamNG state: %s, err %v", appliedPayload, err)
	}
	if len(progress) == 0 || !progress[len(progress)-1].Done || progress[len(progress)-1].Phase != "ready" {
		t.Fatalf("terminal activation progress missing: %#v", progress)
	}
	for _, source := range []string{first.Workspace.SourcePath, second.Workspace.SourcePath} {
		if err := os.Remove(source); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.ActivateProfile(profile.ID); err != nil {
		t.Fatalf("content-addressed profile cache did not survive unavailable cross-root sources: %v", err)
	}

	service.config.GameExecutable = "BeamNG.drive.x64.exe"
	service.config.GameInstallDir = t.TempDir()
	var gotExecutable, gotDirectory string
	var gotArguments []string
	service.startProcess = func(executable string, arguments []string, directory string) (ProcessLaunch, error) {
		gotExecutable, gotArguments, gotDirectory = executable, append([]string(nil), arguments...), directory
		return ProcessLaunch{PID: 4242, Executable: executable, StartedAt: nowUTC()}, nil
	}
	launch, err := service.LaunchProfile(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Process.PID != 4242 || gotExecutable != service.config.GameExecutable || gotDirectory != service.config.GameInstallDir || len(gotArguments) != 2 || gotArguments[0] != "-userpath" || gotArguments[1] != launch.Activation.UserPath {
		t.Fatalf("profile launch contract mismatch: launch=%#v executable=%q arguments=%#v directory=%q", launch, gotExecutable, gotArguments, gotDirectory)
	}
	if !service.HasAppliedModProfile() {
		t.Fatal("applied mod profile did not preserve the normal BeamNG state")
	}
	if err := service.RestoreNormalModSelection(); err != nil {
		t.Fatal(err)
	}
	if service.HasAppliedModProfile() {
		t.Fatal("normal BeamNG state still reports as managed after restore")
	}
	if _, err := os.Stat(filepath.Join(service.config.ActiveModsDir, managedModDirectoryName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed mod directory remains after restore: %v", err)
	}
	appliedPayload, err = os.ReadFile(filepath.Join(service.config.ActiveModsDir, "db.json"))
	appliedDB.Mods = nil
	if err != nil || json.Unmarshal(appliedPayload, &appliedDB) != nil || !appliedDB.Mods[sentinelKey].Active {
		t.Fatalf("normal BeamNG mod selection was not restored: %s, err %v", appliedPayload, err)
	}
}

func containsEntity(items []LibraryItem, entityID string) bool {
	return slices.ContainsFunc(items, func(item LibraryItem) bool { return item.EntityID == entityID })
}

func findModTag(t *testing.T, state OrganizationState, name string) ModTag {
	t.Helper()
	for _, tag := range state.Tags {
		if strings.EqualFold(tag.Name, name) {
			return tag
		}
	}
	t.Fatalf("tag %q missing from %#v", name, state.Tags)
	return ModTag{}
}

func findReusablePreset(t *testing.T, state OrganizationState, name string) ModPreset {
	t.Helper()
	for _, preset := range state.Presets {
		if preset.Name == name && preset.DefaultForProfileCount == 0 {
			return preset
		}
	}
	t.Fatalf("reusable preset %q missing from %#v", name, state.Presets)
	return ModPreset{}
}

func findProfileMod(t *testing.T, detail ProfileDetail, entityID string) ProfileMod {
	t.Helper()
	for _, mod := range detail.Mods {
		if mod.EntityID == entityID {
			return mod
		}
	}
	t.Fatalf("profile mod %q missing from %#v", entityID, detail.Mods)
	return ProfileMod{}
}
