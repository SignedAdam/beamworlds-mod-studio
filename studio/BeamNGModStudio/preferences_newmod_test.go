package main

import (
	"context"
	"encoding/json"
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

func TestSettingsRoundTripProtectsProviderAPIKeys(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	const (
		openRouterSecret = "openrouter-unit-test-secret"
		openAISecret     = "openai-unit-test-secret"
		anthropicSecret  = "anthropic-unit-test-secret"
	)
	baseUpdate := func(profile string) SettingsUpdate {
		return SettingsUpdate{
			Theme:                "light",
			DefaultAuthor:        "Ada Lovelace",
			AgentProfile:         profile,
			AgentModel:           "model-test",
			ContextMode:          "deep",
			ShowAIUsage:          false,
			ShowFileSizes:        true,
			EmphasisColor:        "#ff6a2a",
			ActiveTabColor:       "#f1eee4",
			SubsectionTitleColor: "#4f9fca",
			DarkSurfaceColor:     "#080808",
			DarkBorderColor:      "#393939",
			DarkTextColor:        "#f7f5ef",
			LightSurfaceColor:    "#f8f6f0",
			LightBorderColor:     "#9e9b93",
			LightTextColor:       "#131210",
			PreScanModel:         "vendor/pre-model",
			PreScanReasoning:     "medium",
			FullScanModel:        "vendor/full-model",
			FullScanReasoning:    "xhigh",
		}
	}
	update := baseUpdate("anthropic")
	update.OpenRouterAPIKey = openRouterSecret
	update.OpenAIAPIKey = openAISecret
	update.AnthropicAPIKey = anthropicSecret
	saved, err := service.SaveSettings(update)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Theme != "light" || saved.DefaultAuthor != "Ada Lovelace" || saved.AgentProfile != "anthropic" || saved.AgentModel != "model-test" || saved.ContextMode != "deep" || saved.ShowAIUsage || !saved.ShowFileSizes {
		t.Fatalf("unexpected saved settings: %#v", saved)
	}
	if !saved.HasOpenRouterAPIKey || !saved.HasOpenAIAPIKey || !saved.HasAnthropicAPIKey {
		t.Fatalf("stored key status = %#v", saved)
	}
	responseJSON, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{openRouterSecret, openAISecret, anthropicSecret} {
		if strings.Contains(string(responseJSON), secret) {
			t.Fatalf("settings response exposed provider credential %q", secret)
		}
	}
	if saved.EmphasisColor != "#ff6a2a" || saved.ActiveTabColor != "#f1eee4" || saved.SubsectionTitleColor != "#4f9fca" || saved.DarkSurfaceColor != "#080808" || saved.DarkBorderColor != "#393939" || saved.DarkTextColor != "#f7f5ef" || saved.LightSurfaceColor != "#f8f6f0" || saved.LightBorderColor != "#9e9b93" || saved.LightTextColor != "#131210" {
		t.Fatalf("appearance settings did not round-trip: %#v", saved)
	}
	if saved.PreScanModel != "vendor/pre-model" || saved.PreScanReasoning != "medium" || saved.FullScanModel != "vendor/full-model" || saved.FullScanReasoning != "xhigh" {
		t.Fatalf("Virus Scanner settings did not round-trip: %#v", saved)
	}
	launch, err := service.agentLaunchSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if launch.APIKey != anthropicSecret {
		t.Fatalf("selected provider key = %q", launch.APIKey)
	}
	for key, plain := range map[string]string{
		openRouterAgentSecretKey: openRouterSecret,
		openAIAgentSecretKey:     openAISecret,
		anthropicAgentSecretKey:  anthropicSecret,
	} {
		encoded, err := service.store.readSetting(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		if encoded == plain || strings.Contains(encoded, plain) {
			t.Fatalf("%s was persisted in plaintext", key)
		}
	}

	clearUpdate := baseUpdate("anthropic")
	clearUpdate.ClearAnthropicAPIKey = true
	cleared, err := service.SaveSettings(clearUpdate)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.HasAnthropicAPIKey || !cleared.HasOpenRouterAPIKey || !cleared.HasOpenAIAPIKey {
		t.Fatalf("provider-specific key removal changed the wrong keys: %#v", cleared)
	}
	launch, err = service.agentLaunchSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if launch.APIKey != "" {
		t.Fatal("cleared Anthropic API key is still available")
	}

	switched, err := service.SaveSettings(baseUpdate("openrouter"))
	if err != nil {
		t.Fatal(err)
	}
	if !switched.HasOpenRouterAPIKey {
		t.Fatal("switching providers lost the stored OpenRouter key")
	}
	launch, err = service.agentLaunchSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if launch.APIKey != openRouterSecret {
		t.Fatalf("OpenRouter provider key = %q", launch.APIKey)
	}
}

func TestSettingsLoadMigratesOnlyReleasedScannerDefaults(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	settings := defaultAppSettings()
	settings.AgentProfile = "anthropic"
	settings.PreScanModel = releasedPreScanModel
	settings.FullScanModel = releasedFullScanModel
	if err := service.store.saveAppSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := service.store.loadAppSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PreScanModel != "" || loaded.FullScanModel != "" {
		t.Fatalf("released scanner defaults = %#v, want provider-neutral blanks", loaded)
	}
	var encoded string
	if err := service.store.db.QueryRow(`SELECT value FROM settings WHERE key=?`, preferencesKey).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var persisted AppSettings
	if err := json.Unmarshal([]byte(encoded), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.PreScanModel != "" || persisted.FullScanModel != "" {
		t.Fatalf("persisted scanner defaults = %#v, want blanks", persisted)
	}

	settings.PreScanModel = "vendor/pre-scan"
	settings.FullScanModel = releasedFullScanModel
	if err := service.store.saveAppSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	custom, err := service.store.loadAppSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if custom.PreScanModel != "vendor/pre-scan" || custom.FullScanModel != releasedFullScanModel {
		t.Fatalf("custom scanner models changed = %q/%q", custom.PreScanModel, custom.FullScanModel)
	}
}

func TestPinnedProfileLaunchSettingsSelectCurrentPinnedCredential(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	const anthropicSecret = "pinned-anthropic-secret"
	if _, err := service.SaveSettings(SettingsUpdate{
		Theme: "dark", AgentProfile: "anthropic", ContextMode: "balanced",
		AnthropicAPIKey: anthropicSecret,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveSettings(SettingsUpdate{
		Theme: "dark", AgentProfile: "openrouter", ContextMode: "balanced",
	}); err != nil {
		t.Fatal(err)
	}
	pinned, err := service.agentLaunchSettingsForProfile(context.Background(), " anthropic ")
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Profile != "anthropic" || pinned.APIKey != anthropicSecret {
		t.Fatalf("pinned launch settings = %#v, want anthropic credential", pinned)
	}
}
func TestAgentSelectionArgumentsApplyConnectionProfile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		settings agentLaunchSettings
		want     string
	}{
		{name: "ChatGPT subscription", settings: agentLaunchSettings{Profile: "chatgpt", SelectModel: true}, want: "--model openai-codex/gpt-5.4"},
		{name: "Claude subscription", settings: agentLaunchSettings{Profile: "claude", SelectModel: true}, want: "--model anthropic/claude-sonnet-4-6"},
		{name: "OpenRouter API", settings: agentLaunchSettings{Profile: "openrouter", SelectModel: true}, want: "--model openrouter/auto"},
		{name: "OpenAI API", settings: agentLaunchSettings{Profile: "openai", SelectModel: true}, want: "--model openai/gpt-5.4"},
		{name: "Anthropic API", settings: agentLaunchSettings{Profile: "anthropic", SelectModel: true, APIKey: "secret"}, want: "--model anthropic/claude-sonnet-4-6"},
		{name: "Claude model", settings: agentLaunchSettings{Profile: "claude", Model: "claude-sonnet-test", SelectModel: true}, want: "--model anthropic/claude-sonnet-test"},
		{name: "Qualified model", settings: agentLaunchSettings{Profile: "openrouter", Model: "openrouter/vendor/model", SelectModel: true}, want: "--model openrouter/vendor/model"},
		{name: "Resume with direct key", settings: agentLaunchSettings{Profile: "anthropic", APIKey: "secret"}, want: ""},
	}
	for _, test := range tests {
		if got := strings.Join(agentSelectionArguments(test.settings), " "); got != test.want {
			t.Errorf("%s arguments = %q, want %q", test.name, got, test.want)
		}
	}
	if got := agentSelectionArguments(agentLaunchSettings{Profile: "chatgpt", Model: "gpt-test"}); len(got) != 0 {
		t.Fatalf("resume launch unexpectedly overrides the stored model: %q", got)
	}
	arguments := agentLaunchArguments("C:/workspace", "", "", "C:/sessions", agentLaunchSettings{
		Profile: "anthropic", Model: "claude-test", APIKey: "must-not-appear-in-argv", SelectModel: true,
	})
	if joined := strings.Join(arguments, " "); strings.Contains(joined, "must-not-appear-in-argv") || strings.Contains(joined, "--api-key") {
		t.Fatalf("provider key leaked into process arguments: %q", joined)
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
