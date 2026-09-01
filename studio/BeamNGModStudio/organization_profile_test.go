package main

import (
	"context"
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
	for _, table := range []string{"library_folders", "library_folder_entities", "mod_presets", "mod_preset_entities", "mod_profiles", "mod_profile_presets", "workspace_drafts"} {
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
	if err := service.SaveWorkspaceDraft(workspaceID, originalPath, draftContent); err != nil {
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
	if err := service.WriteWorkspaceFile(workspaceID, movedPath, draftContent); err != nil {
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

func TestProfileActivationAndLaunchIsolation(t *testing.T) {
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
	if !strings.HasPrefix(filepath.Clean(activation.UserPath), filepath.Clean(service.config.ProfileDir)+string(os.PathSeparator)) {
		t.Fatalf("profile user path escaped profile storage: %s", activation.UserPath)
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
}

func containsEntity(items []LibraryItem, entityID string) bool {
	return slices.ContainsFunc(items, func(item LibraryItem) bool { return item.EntityID == entityID })
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
