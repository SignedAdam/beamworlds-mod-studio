package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

func TestCreateNewModBuildsUsableProjects(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	tests := []struct {
		kind         string
		name         string
		modID        string
		expectedPath string
	}{
		{kind: "vehicle", name: "Test Vehicle", modID: "test_vehicle", expectedPath: "vehicles/test_vehicle/test_vehicle.jbeam"},
		{kind: "map", name: "Test Map", modID: "test_map", expectedPath: "levels/test_map/main/MissionGroup/items.level.json"},
		{kind: "ui", name: "Test UI", modID: "test_ui", expectedPath: "ui/modules/apps/test_ui/app.js"},
		{kind: "script", name: "Test Script", modID: "test_script", expectedPath: "lua/ge/extensions/test_script.lua"},
	}

	for index, test := range tests {
		author := ""
		if index == 0 {
			author = "Test Author"
		}
		detail, err := service.CreateNewMod(NewModRequest{
			Name: test.name, ModID: test.modID, Kind: test.kind, Author: author,
			Version: "0.1.0", Description: "Generated project contract test.",
		})
		if err != nil {
			t.Fatalf("CreateNewMod(%s): %v", test.kind, err)
		}
		if detail.Entity.DisplayName != test.name {
			t.Errorf("CreateNewMod(%s) display name = %q, want %q", test.kind, detail.Entity.DisplayName, test.name)
		}
		if detail.Entity.Kind != modkit.Kind(test.kind) {
			t.Errorf("CreateNewMod(%s) kind = %q", test.kind, detail.Entity.Kind)
		}
		if !detail.Entity.Linked {
			t.Errorf("CreateNewMod(%s) generated source is not linked", test.kind)
		}
		if _, err := os.Stat(detail.Workspace.SourcePath); err != nil {
			t.Errorf("CreateNewMod(%s) source archive: %v", test.kind, err)
		}
		if _, err := service.ReadWorkspaceFile(detail.Workspace.ID, test.expectedPath); err != nil {
			t.Errorf("CreateNewMod(%s) expected file %q: %v", test.kind, test.expectedPath, err)
		}
		if _, err := service.ReadWorkspaceFile(detail.Workspace.ID, "mod_info/"+test.modID+".json"); err != nil {
			t.Errorf("CreateNewMod(%s) mod metadata: %v", test.kind, err)
		}
		validation, err := service.ValidateWorkspace(detail.Workspace.ID)
		if err != nil {
			t.Errorf("ValidateWorkspace(%s): %v", test.kind, err)
		} else if !validation.Valid {
			t.Errorf("ValidateWorkspace(%s) failed: %#v", test.kind, validation.Issues)
		}
	}

	settings, err := service.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.DefaultAuthor != "Test Author" {
		t.Errorf("default author = %q, want Test Author", settings.DefaultAuthor)
	}
	workspaces, err := service.ListWorkspaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(workspaces) != len(tests) {
		t.Fatalf("workspace count = %d, want %d", len(workspaces), len(tests))
	}
	for _, workspace := range workspaces {
		if workspace.DisplayName == "" || workspace.Kind == modkit.KindUnknown {
			t.Errorf("project browser metadata missing: %#v", workspace)
		}
		if workspace.AgentStatus != "idle" || workspace.AgentUpdatedAt != "" {
			t.Errorf("new project AI activity = %q at %q, want idle without timestamp", workspace.AgentStatus, workspace.AgentUpdatedAt)
		}
	}
	startedAt := "2026-09-01T20:15:00Z"
	if err := service.store.CreateAgentRun(context.Background(), AgentRunRecord{ID: "browser-status-run", WorkspaceID: workspaces[0].ID, Prompt: "Review the project", StartedAt: startedAt}); err != nil {
		t.Fatal(err)
	}
	refreshed, err := service.ListWorkspaces()
	if err != nil {
		t.Fatal(err)
	}
	var running WorkspaceRecord
	for _, workspace := range refreshed {
		if workspace.ID == workspaces[0].ID {
			running = workspace
			break
		}
	}
	if running.AgentStatus != "running" || running.AgentUpdatedAt != startedAt {
		t.Fatalf("project browser AI activity = %q at %q, want running at %q", running.AgentStatus, running.AgentUpdatedAt, startedAt)
	}
	if err := service.RevealWorkspacePath(workspaces[0].ID, "../outside"); err == nil {
		t.Fatal("RevealWorkspacePath accepted a path outside the project")
	}
}

func TestSettingsRoundTripProtectsAPIKey(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	const secret = "unit-test-secret-value"

	saved, err := service.SaveSettings(SettingsUpdate{
		Theme:             "light",
		DefaultAuthor:     "Ada Lovelace",
		AgentProfile:      "codex",
		AgentModel:        "gpt-test",
		ContextMode:       "deep",
		ShowAIUsage:       false,
		ShowFileSizes:     true,
		EmphasisColor:     "#ff6a2a",
		ActiveTabColor:    "#f1eee4",
		DarkSurfaceColor:  "#080808",
		DarkBorderColor:   "#393939",
		DarkTextColor:     "#f7f5ef",
		LightSurfaceColor: "#f8f6f0",
		LightBorderColor:  "#9e9b93",
		LightTextColor:    "#131210",
		PreScanModel:      "gpt-5.6-luna",
		PreScanReasoning:  "medium",
		FullScanModel:     "gpt-5.6-sol",
		FullScanReasoning: "xhigh",
		APIKey:            secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Theme != "light" || saved.DefaultAuthor != "Ada Lovelace" || saved.AgentProfile != "codex" || saved.AgentModel != "gpt-test" || saved.ContextMode != "deep" || saved.ShowAIUsage || !saved.ShowFileSizes || !saved.HasAPIKey {
		t.Fatalf("unexpected saved settings: %#v", saved)
	}
	if saved.EmphasisColor != "#ff6a2a" || saved.ActiveTabColor != "#f1eee4" || saved.DarkSurfaceColor != "#080808" || saved.DarkBorderColor != "#393939" || saved.DarkTextColor != "#f7f5ef" || saved.LightSurfaceColor != "#f8f6f0" || saved.LightBorderColor != "#9e9b93" || saved.LightTextColor != "#131210" {
		t.Fatalf("appearance settings did not round-trip: %#v", saved)
	}
	if saved.PreScanModel != "gpt-5.6-luna" || saved.PreScanReasoning != "medium" || saved.FullScanModel != "gpt-5.6-sol" || saved.FullScanReasoning != "xhigh" {
		t.Fatalf("Virus Scanner settings did not round-trip: %#v", saved)
	}
	launch, err := service.agentLaunchSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if launch.APIKey != secret {
		t.Fatalf("decrypted API key = %q", launch.APIKey)
	}
	encoded, err := service.store.readSetting(context.Background(), agentSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	if encoded == secret || strings.Contains(encoded, secret) {
		t.Fatal("API key was persisted in plaintext")
	}

	cleared, err := service.SaveSettings(SettingsUpdate{
		Theme:             saved.Theme,
		DefaultAuthor:     saved.DefaultAuthor,
		AgentProfile:      saved.AgentProfile,
		AgentModel:        saved.AgentModel,
		ContextMode:       saved.ContextMode,
		ShowAIUsage:       saved.ShowAIUsage,
		ShowFileSizes:     saved.ShowFileSizes,
		EmphasisColor:     saved.EmphasisColor,
		ActiveTabColor:    saved.ActiveTabColor,
		DarkSurfaceColor:  saved.DarkSurfaceColor,
		DarkBorderColor:   saved.DarkBorderColor,
		DarkTextColor:     saved.DarkTextColor,
		LightSurfaceColor: saved.LightSurfaceColor,
		LightBorderColor:  saved.LightBorderColor,
		LightTextColor:    saved.LightTextColor,
		PreScanModel:      saved.PreScanModel,
		PreScanReasoning:  saved.PreScanReasoning,
		FullScanModel:     saved.FullScanModel,
		FullScanReasoning: saved.FullScanReasoning,
		ClearAPIKey:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.HasAPIKey {
		t.Fatal("cleared settings still report a stored API key")
	}
	launch, err = service.agentLaunchSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if launch.APIKey != "" {
		t.Fatal("cleared API key is still available to agent launch")
	}
}
func TestAgentSelectionArgumentsApplyConnectionProfile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		settings agentLaunchSettings
		want     string
	}{
		{name: "OMP default", settings: agentLaunchSettings{Profile: "omp"}, want: ""},
		{name: "Codex default", settings: agentLaunchSettings{Profile: "codex"}, want: "--provider openai-codex"},
		{name: "Claude default", settings: agentLaunchSettings{Profile: "claude"}, want: "--provider anthropic"},
		{name: "OpenRouter default", settings: agentLaunchSettings{Profile: "openrouter"}, want: "--provider openrouter"},
		{name: "OpenAI default", settings: agentLaunchSettings{Profile: "openai"}, want: "--provider openai"},
		{name: "Claude model", settings: agentLaunchSettings{Profile: "claude", Model: "claude-sonnet-test"}, want: "--model anthropic/claude-sonnet-test"},
		{name: "Qualified model", settings: agentLaunchSettings{Profile: "openrouter", Model: "openrouter/vendor/model"}, want: "--model openrouter/vendor/model"},
	}
	for _, test := range tests {
		if got := strings.Join(agentSelectionArguments(test.settings), " "); got != test.want {
			t.Errorf("%s arguments = %q, want %q", test.name, got, test.want)
		}
	}
}

func newTestAppService(t *testing.T) *AppService {
	t.Helper()
	root := t.TempDir()
	beamNGRoot := filepath.Join(root, "beamng")
	activeModsDir := filepath.Join(beamNGRoot, "current", "mods")
	config := AppConfig{
		SetupComplete: true, BeamNGRoot: beamNGRoot,
		DataDir: root, DatabasePath: filepath.Join(root, "modstudio.sqlite"),
		ImageCacheDir: filepath.Join(root, "cache", "images"), WorkspaceDir: filepath.Join(root, "workspaces"),
		ExportDir: filepath.Join(root, "exports"), ProfileDir: filepath.Join(root, "profiles"),
		ActiveModsDir: activeModsDir, TestInstallDir: activeModsDir, ScanConcurrency: 1,
	}
	for _, directory := range []string{config.BeamNGRoot, config.ImageCacheDir, config.WorkspaceDir, config.ExportDir, config.ProfileDir, config.ActiveModsDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store, err := OpenStore(config.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := NewAppService(config, store, func(string, any) {})
	service.gameRunning = func() (bool, error) { return false, nil }
	return service
}
