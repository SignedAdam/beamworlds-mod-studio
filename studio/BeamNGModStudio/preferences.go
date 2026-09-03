package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	preferencesKey           = "app.preferences.v1"
	legacyAgentSecretKey     = "app.agent-api-key.v1"
	openRouterAgentSecretKey = "app.ai.openrouter-api-key.v1"
	openAIAgentSecretKey     = "app.ai.openai-api-key.v1"
	anthropicAgentSecretKey  = "app.ai.anthropic-api-key.v1"
	releasedPreScanModel     = "gpt-5.6-luna"
	releasedFullScanModel    = "gpt-5.6-sol"
	autoFormatDelayDefaultMs = 200
	autoFormatDelayMinMs     = 50
	autoFormatDelayMaxMs     = 2000
)

type AppSettings struct {
	Theme                string `json:"theme"`
	DefaultAuthor        string `json:"defaultAuthor"`
	AgentProfile         string `json:"agentProfile"`
	AgentModel           string `json:"agentModel"`
	ContextMode          string `json:"contextMode"`
	ShowAIUsage          bool   `json:"showAIUsage"`
	ShowFileSizes        bool   `json:"showFileSizes"`
	AutoFormatDelayMs    int    `json:"autoFormatDelayMs"`
	EmphasisColor        string `json:"emphasisColor"`
	ActiveTabColor       string `json:"activeTabColor"`
	SubsectionTitleColor string `json:"subsectionTitleColor"`
	DarkSurfaceColor     string `json:"darkSurfaceColor"`
	DarkBorderColor      string `json:"darkBorderColor"`
	DarkTextColor        string `json:"darkTextColor"`
	LightSurfaceColor    string `json:"lightSurfaceColor"`
	LightBorderColor     string `json:"lightBorderColor"`
	LightTextColor       string `json:"lightTextColor"`
	PreScanModel         string `json:"preScanModel"`
	PreScanReasoning     string `json:"preScanReasoning"`
	FullScanModel        string `json:"fullScanModel"`
	FullScanReasoning    string `json:"fullScanReasoning"`
	HasOpenRouterAPIKey  bool   `json:"hasOpenRouterApiKey"`
	HasOpenAIAPIKey      bool   `json:"hasOpenAIApiKey"`
	HasAnthropicAPIKey   bool   `json:"hasAnthropicApiKey"`
}

type SettingsUpdate struct {
	Theme                 string `json:"theme"`
	DefaultAuthor         string `json:"defaultAuthor"`
	AgentProfile          string `json:"agentProfile"`
	AgentModel            string `json:"agentModel"`
	ContextMode           string `json:"contextMode"`
	ShowAIUsage           bool   `json:"showAIUsage"`
	ShowFileSizes         bool   `json:"showFileSizes"`
	AutoFormatDelayMs     int    `json:"autoFormatDelayMs"`
	EmphasisColor         string `json:"emphasisColor"`
	ActiveTabColor        string `json:"activeTabColor"`
	SubsectionTitleColor  string `json:"subsectionTitleColor"`
	DarkSurfaceColor      string `json:"darkSurfaceColor"`
	DarkBorderColor       string `json:"darkBorderColor"`
	DarkTextColor         string `json:"darkTextColor"`
	LightSurfaceColor     string `json:"lightSurfaceColor"`
	LightBorderColor      string `json:"lightBorderColor"`
	LightTextColor        string `json:"lightTextColor"`
	PreScanModel          string `json:"preScanModel"`
	PreScanReasoning      string `json:"preScanReasoning"`
	FullScanModel         string `json:"fullScanModel"`
	FullScanReasoning     string `json:"fullScanReasoning"`
	OpenRouterAPIKey      string `json:"openRouterApiKey"`
	ClearOpenRouterAPIKey bool   `json:"clearOpenRouterApiKey"`
	OpenAIAPIKey          string `json:"openAIApiKey"`
	ClearOpenAIAPIKey     bool   `json:"clearOpenAIApiKey"`
	AnthropicAPIKey       string `json:"anthropicApiKey"`
	ClearAnthropicAPIKey  bool   `json:"clearAnthropicApiKey"`
}

type UsageLimit struct {
	Provider  string  `json:"provider"`
	Label     string  `json:"label"`
	WindowID  string  `json:"windowId"`
	Used      float64 `json:"used"`
	Remaining float64 `json:"remaining"`
	Limit     float64 `json:"limit"`
	Unit      string  `json:"unit"`
	ResetsAt  int64   `json:"resetsAt"`
	Status    string  `json:"status"`
}

type AIUsage struct {
	HasRuns     bool         `json:"hasRuns"`
	RunCount    int          `json:"runCount"`
	TotalTokens int64        `json:"totalTokens"`
	Limits      []UsageLimit `json:"limits"`
	UsageError  string       `json:"usageError,omitempty"`
}
type AgentModelOption struct {
	Provider      string `json:"provider"`
	ID            string `json:"id"`
	Selector      string `json:"selector"`
	Name          string `json:"name"`
	ContextWindow int64  `json:"contextWindow"`
	Reasoning     bool   `json:"reasoning"`
}

type agentLaunchSettings struct {
	Profile     string
	Model       string
	ContextMode string
	APIKey      string
	SelectModel bool
}

func defaultAppSettings() AppSettings {
	return AppSettings{
		Theme:                "dark",
		AgentProfile:         "chatgpt",
		ContextMode:          "balanced",
		ShowAIUsage:          true,
		ShowFileSizes:        true,
		EmphasisColor:        "#f26522",
		ActiveTabColor:       "#e8e4d8",
		SubsectionTitleColor: "#3f93c5",
		DarkSurfaceColor:     "#090909",
		DarkBorderColor:      "#343434",
		DarkTextColor:        "#f2f0ea",
		LightSurfaceColor:    "#f4f2ed",
		LightBorderColor:     "#aaa69d",
		LightTextColor:       "#171614",
		PreScanModel:         "",
		PreScanReasoning:     "medium",
		FullScanModel:        "",
		FullScanReasoning:    "xhigh",
		AutoFormatDelayMs:    autoFormatDelayDefaultMs,
	}
}

func validateSettings(update SettingsUpdate) (AppSettings, error) {
	defaults := defaultAppSettings()
	settings := AppSettings{
		Theme:                strings.ToLower(strings.TrimSpace(update.Theme)),
		DefaultAuthor:        strings.TrimSpace(update.DefaultAuthor),
		AgentProfile:         strings.ToLower(strings.TrimSpace(update.AgentProfile)),
		AgentModel:           strings.TrimSpace(update.AgentModel),
		ContextMode:          strings.ToLower(strings.TrimSpace(update.ContextMode)),
		ShowAIUsage:          update.ShowAIUsage,
		ShowFileSizes:        update.ShowFileSizes,
		AutoFormatDelayMs:    clampAutoFormatDelay(update.AutoFormatDelayMs),
		EmphasisColor:        colorOrDefault(update.EmphasisColor, defaults.EmphasisColor),
		ActiveTabColor:       colorOrDefault(update.ActiveTabColor, defaults.ActiveTabColor),
		SubsectionTitleColor: colorOrDefault(update.SubsectionTitleColor, defaults.SubsectionTitleColor),
		DarkSurfaceColor:     colorOrDefault(update.DarkSurfaceColor, defaults.DarkSurfaceColor),
		DarkBorderColor:      colorOrDefault(update.DarkBorderColor, defaults.DarkBorderColor),
		DarkTextColor:        colorOrDefault(update.DarkTextColor, defaults.DarkTextColor),
		LightSurfaceColor:    colorOrDefault(update.LightSurfaceColor, defaults.LightSurfaceColor),
		LightBorderColor:     colorOrDefault(update.LightBorderColor, defaults.LightBorderColor),
		LightTextColor:       colorOrDefault(update.LightTextColor, defaults.LightTextColor),
		PreScanModel:         firstValue(update.PreScanModel, defaults.PreScanModel),
		PreScanReasoning:     firstValue(strings.ToLower(update.PreScanReasoning), defaults.PreScanReasoning),
		FullScanModel:        firstValue(update.FullScanModel, defaults.FullScanModel),
		FullScanReasoning:    firstValue(strings.ToLower(update.FullScanReasoning), defaults.FullScanReasoning),
	}
	if settings.Theme != "dark" && settings.Theme != "light" {
		return AppSettings{}, errors.New("theme must be dark or light")
	}
	switch settings.AgentProfile {
	case "chatgpt", "claude", "openrouter", "openai", "anthropic":
	default:
		return AppSettings{}, errors.New("unsupported AI connection profile")
	}
	switch settings.ContextMode {
	case "focused", "balanced", "deep":
	default:
		return AppSettings{}, errors.New("context mode must be focused, balanced, or deep")
	}
	for _, value := range []string{settings.PreScanReasoning, settings.FullScanReasoning} {
		switch value {
		case "low", "medium", "high", "xhigh":
		default:
			return AppSettings{}, errors.New("analysis reasoning must be low, medium, high, or xhigh")
		}
	}
	if len(settings.DefaultAuthor) > 80 {
		return AppSettings{}, errors.New("default author exceeds 80 characters")
	}
	for _, model := range []string{settings.AgentModel, settings.PreScanModel, settings.FullScanModel} {
		if len(model) > 120 {
			return AppSettings{}, errors.New("model identifier exceeds 120 characters")
		}
	}
	return settings, nil
}

func clampAutoFormatDelay(value int) int {
	if value <= 0 {
		return autoFormatDelayDefaultMs
	}
	if value < autoFormatDelayMinMs {
		return autoFormatDelayMinMs
	}
	if value > autoFormatDelayMaxMs {
		return autoFormatDelayMaxMs
	}
	return value
}

func firstValue(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func colorOrDefault(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	if len(value) != 7 || value[0] != '#' {
		return fallback
	}
	for _, character := range value[1:] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return fallback
		}
	}
	return value
}

func (s *Store) loadAppSettings(ctx context.Context) (AppSettings, error) {
	settings := defaultAppSettings()
	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, preferencesKey).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return AppSettings{}, err
	}
	if err := json.Unmarshal([]byte(encoded), &settings); err != nil {
		return AppSettings{}, fmt.Errorf("decode application settings: %w", err)
	}
	profile := normalizeVirgilAIProfile(settings.AgentProfile)
	switch profile {
	case "", "omp", "codex":
		settings.AgentProfile = "chatgpt"
	default:
		settings.AgentProfile = profile
	}
	settings.SubsectionTitleColor = colorOrDefault(settings.SubsectionTitleColor, defaultAppSettings().SubsectionTitleColor)
	settings.AutoFormatDelayMs = clampAutoFormatDelay(settings.AutoFormatDelayMs)
	if settings.PreScanModel == releasedPreScanModel && settings.FullScanModel == releasedFullScanModel {
		settings.PreScanModel = ""
		settings.FullScanModel = ""
		if err := s.saveAppSettings(ctx, settings); err != nil {
			return AppSettings{}, fmt.Errorf("migrate released scanner defaults: %w", err)
		}
	}
	return settings, nil
}

func (s *Store) saveAppSettings(ctx context.Context, settings AppSettings) error {
	settings.HasOpenRouterAPIKey = false
	settings.HasOpenAIAPIKey = false
	settings.HasAnthropicAPIKey = false
	encoded, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, preferencesKey, string(encoded))
	return err
}

func (s *Store) readSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&value)
	return value, err
}

func (s *Store) writeSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) deleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, key)
	return err
}

func secretSettingForProfile(profile string) string {
	switch normalizeVirgilAIProfile(profile) {
	case "openrouter":
		return openRouterAgentSecretKey
	case "openai":
		return openAIAgentSecretKey
	case "anthropic":
		return anthropicAgentSecretKey
	default:
		return ""
	}
}

func (service *AppService) migrateLegacyAgentSecret(ctx context.Context, settings *AppSettings) error {
	encoded, err := service.store.readSetting(ctx, legacyAgentSecretKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	target := secretSettingForProfile(settings.AgentProfile)
	if settings.AgentProfile == "claude" {
		target = anthropicAgentSecretKey
		settings.AgentProfile = "anthropic"
	}
	if target != "" {
		if _, err := service.store.readSetting(ctx, target); errors.Is(err, sql.ErrNoRows) {
			if err := service.store.writeSetting(ctx, target, encoded); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	if err := service.store.deleteSetting(ctx, legacyAgentSecretKey); err != nil {
		return err
	}
	return service.store.saveAppSettings(ctx, *settings)
}

func hasStoredSecret(ctx context.Context, store *Store, key string) (bool, error) {
	_, err := store.readSetting(ctx, key)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
}

func (service *AppService) Settings() (AppSettings, error) {
	ctx := context.Background()
	settings, err := service.store.loadAppSettings(ctx)
	if err != nil {
		return AppSettings{}, err
	}
	if err := service.migrateLegacyAgentSecret(ctx, &settings); err != nil {
		return AppSettings{}, err
	}
	if settings.HasOpenRouterAPIKey, err = hasStoredSecret(ctx, service.store, openRouterAgentSecretKey); err != nil {
		return AppSettings{}, err
	}
	if settings.HasOpenAIAPIKey, err = hasStoredSecret(ctx, service.store, openAIAgentSecretKey); err != nil {
		return AppSettings{}, err
	}
	if settings.HasAnthropicAPIKey, err = hasStoredSecret(ctx, service.store, anthropicAgentSecretKey); err != nil {
		return AppSettings{}, err
	}
	return settings, nil
}

func (service *AppService) SaveSettings(update SettingsUpdate) (AppSettings, error) {
	ctx := context.Background()
	current, err := service.store.loadAppSettings(ctx)
	if err != nil {
		return AppSettings{}, err
	}
	if err := service.migrateLegacyAgentSecret(ctx, &current); err != nil {
		return AppSettings{}, err
	}
	settings, err := validateSettings(update)
	if err != nil {
		return AppSettings{}, err
	}
	type preparedSecret struct {
		key       string
		protected string
		clear     bool
	}
	secretInputs := []struct {
		key   string
		value string
		clear bool
	}{
		{openRouterAgentSecretKey, update.OpenRouterAPIKey, update.ClearOpenRouterAPIKey},
		{openAIAgentSecretKey, update.OpenAIAPIKey, update.ClearOpenAIAPIKey},
		{anthropicAgentSecretKey, update.AnthropicAPIKey, update.ClearAnthropicAPIKey},
	}
	prepared := make([]preparedSecret, 0, len(secretInputs))
	for _, secret := range secretInputs {
		value := strings.TrimSpace(secret.value)
		if len(value) > 8192 {
			return AppSettings{}, errors.New("API key exceeds 8192 characters")
		}
		item := preparedSecret{key: secret.key, clear: secret.clear}
		if !item.clear && value != "" {
			item.protected, err = protectSecret(value, service.config.DataDir)
			if err != nil {
				return AppSettings{}, fmt.Errorf("protect API key: %w", err)
			}
		}
		prepared = append(prepared, item)
	}
	settings.HasOpenRouterAPIKey = false
	settings.HasOpenAIAPIKey = false
	settings.HasAnthropicAPIKey = false
	encoded, err := json.Marshal(settings)
	if err != nil {
		return AppSettings{}, err
	}
	transaction, err := service.store.db.BeginTx(ctx, nil)
	if err != nil {
		return AppSettings{}, err
	}
	defer transaction.Rollback()
	for _, secret := range prepared {
		switch {
		case secret.clear:
			if _, err := transaction.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, secret.key); err != nil {
				return AppSettings{}, err
			}
		case secret.protected != "":
			if _, err := transaction.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, secret.key, secret.protected); err != nil {
				return AppSettings{}, err
			}
		}
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, preferencesKey, string(encoded)); err != nil {
		return AppSettings{}, err
	}
	if err := transaction.Commit(); err != nil {
		return AppSettings{}, err
	}
	return service.Settings()
}

func (service *AppService) agentLaunchSettings(ctx context.Context) (agentLaunchSettings, error) {
	return service.agentLaunchSettingsForProfile(ctx, "")
}

func (service *AppService) agentLaunchSettingsForProfile(ctx context.Context, requestedProfile string) (agentLaunchSettings, error) {
	settings, err := service.store.loadAppSettings(ctx)
	if err != nil {
		return agentLaunchSettings{}, err
	}
	if err := service.migrateLegacyAgentSecret(ctx, &settings); err != nil {
		return agentLaunchSettings{}, err
	}
	profile := normalizeVirgilAIProfile(requestedProfile)
	if profile == "" {
		profile = normalizeVirgilAIProfile(settings.AgentProfile)
	}
	result := agentLaunchSettings{Profile: profile, Model: settings.AgentModel, ContextMode: settings.ContextMode}
	secretKey := secretSettingForProfile(profile)
	if secretKey == "" {
		return result, nil
	}
	encoded, err := service.store.readSetting(ctx, secretKey)
	if err == nil {
		result.APIKey, err = unprotectSecret(encoded, service.config.DataDir)
		if err != nil {
			return agentLaunchSettings{}, fmt.Errorf("unprotect API key: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return agentLaunchSettings{}, err
	}
	return result, nil
}

func launchCredentials(settings agentLaunchSettings) map[string]string {
	if settings.APIKey == "" {
		return nil
	}
	switch settings.Profile {
	case "openrouter":
		return map[string]string{"OPENROUTER_API_KEY": settings.APIKey}
	case "openai":
		return map[string]string{"OPENAI_API_KEY": settings.APIKey}
	case "anthropic":
		return map[string]string{"ANTHROPIC_API_KEY": settings.APIKey}
	default:
		return nil
	}
}

func (service *AppService) ListAgentModels() ([]AgentModelOption, error) {
	commandContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	launch, err := service.agentLaunchSettings(commandContext)
	if err != nil {
		return nil, err
	}
	profile, err := managedAIRuntimeProfileForAgent(launch.Profile)
	if err != nil {
		return nil, err
	}
	arguments := append(agentSelectionArguments(launch), "--no-extensions", "models", "--json")
	command, err := service.aiRuntime.Command(commandContext, arguments, launchCredentials(launch), profile)
	if err != nil {
		return nil, err
	}
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list AI models: %w", err)
	}
	var response struct {
		Models []AgentModelOption `json:"models"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("decode AI models: %w", err)
	}
	result := make([]AgentModelOption, 0, len(response.Models))
	seen := map[string]bool{}
	for _, model := range response.Models {
		model.Provider = strings.TrimSpace(model.Provider)
		model.ID = strings.TrimSpace(model.ID)
		model.Selector = strings.TrimSpace(model.Selector)
		model.Name = strings.TrimSpace(model.Name)
		if model.Selector == "" || seen[model.Selector] {
			continue
		}
		seen[model.Selector] = true
		result = append(result, model)
	}
	return result, nil
}

func (service *AppService) AIUsage() (AIUsage, error) {
	ctx := context.Background()
	usage, err := service.store.localAgentUsage(ctx)
	if err != nil || !usage.HasRuns {
		return usage, err
	}
	launch, err := service.agentLaunchSettings(ctx)
	if err != nil {
		usage.UsageError = err.Error()
		return usage, nil
	}
	commandContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	profile, profileErr := managedAIRuntimeProfileForAgent(launch.Profile)
	if profileErr != nil {
		usage.UsageError = profileErr.Error()
		return usage, nil
	}
	arguments := append(agentSelectionArguments(launch), "usage", "--json")
	command, err := service.aiRuntime.Command(commandContext, arguments, launchCredentials(launch), profile)
	if err != nil {
		usage.UsageError = err.Error()
		return usage, nil
	}
	output, err := command.Output()
	if err != nil {
		usage.UsageError = fmt.Sprintf("provider usage unavailable: %v", err)
		return usage, nil
	}
	var response struct {
		Reports []struct {
			Provider string `json:"provider"`
			Limits   []struct {
				Label string `json:"label"`
				Scope struct {
					WindowID string `json:"windowId"`
				} `json:"scope"`
				Window struct {
					ResetsAt int64 `json:"resetsAt"`
				} `json:"window"`
				Amount struct {
					Used      float64 `json:"used"`
					Remaining float64 `json:"remaining"`
					Limit     float64 `json:"limit"`
					Unit      string  `json:"unit"`
				} `json:"amount"`
				Status string `json:"status"`
			} `json:"limits"`
		} `json:"reports"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		usage.UsageError = "provider usage returned invalid JSON"
		return usage, nil
	}
	for _, report := range response.Reports {
		for _, limit := range report.Limits {
			usage.Limits = append(usage.Limits, UsageLimit{
				Provider: report.Provider, Label: limit.Label, WindowID: limit.Scope.WindowID,
				Used: limit.Amount.Used, Remaining: limit.Amount.Remaining, Limit: limit.Amount.Limit,
				Unit: limit.Amount.Unit, ResetsAt: limit.Window.ResetsAt, Status: limit.Status,
			})
		}
	}
	return usage, nil
}

func (s *Store) localAgentUsage(ctx context.Context) (AIUsage, error) {
	usage := AIUsage{Limits: []UsageLimit{}}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs`).Scan(&usage.RunCount); err != nil {
		return AIUsage{}, err
	}
	usage.HasRuns = usage.RunCount > 0
	if !usage.HasRuns {
		return usage, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data_json FROM agent_events WHERE type='turn_end'`)
	if err != nil {
		return AIUsage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return AIUsage{}, err
		}
		var value any
		if json.Unmarshal([]byte(encoded), &value) == nil {
			usage.TotalTokens += tokenCount(value)
		}
	}
	return usage, rows.Err()
}

func tokenCount(value any) int64 {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
			if normalized == "totaltokens" {
				if number, ok := child.(float64); ok && number > 0 {
					return int64(number)
				}
			}
		}
		var sum int64
		for key, child := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
			if normalized == "inputtokens" || normalized == "outputtokens" {
				if number, ok := child.(float64); ok && number > 0 {
					sum += int64(number)
				}
				continue
			}
			if _, nested := child.(map[string]any); nested {
				sum += tokenCount(child)
			} else if _, nested := child.([]any); nested {
				sum += tokenCount(child)
			}
		}
		return sum
	case []any:
		var sum int64
		for _, child := range typed {
			sum += tokenCount(child)
		}
		return sum
	default:
		return 0
	}
}
