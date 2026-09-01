package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	preferencesKey = "app.preferences.v1"
	agentSecretKey = "app.agent-api-key.v1"
)

type AppSettings struct {
	Theme         string `json:"theme"`
	DefaultAuthor string `json:"defaultAuthor"`
	AgentProfile  string `json:"agentProfile"`
	AgentModel    string `json:"agentModel"`
	ContextMode   string `json:"contextMode"`
	ShowAIUsage   bool   `json:"showAIUsage"`
	HasAPIKey     bool   `json:"hasApiKey"`
}

type SettingsUpdate struct {
	Theme         string `json:"theme"`
	DefaultAuthor string `json:"defaultAuthor"`
	AgentProfile  string `json:"agentProfile"`
	AgentModel    string `json:"agentModel"`
	ContextMode   string `json:"contextMode"`
	ShowAIUsage   bool   `json:"showAIUsage"`
	APIKey        string `json:"apiKey"`
	ClearAPIKey   bool   `json:"clearApiKey"`
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
}

func defaultAppSettings() AppSettings {
	return AppSettings{
		Theme:        "dark",
		AgentProfile: "omp",
		ContextMode:  "balanced",
		ShowAIUsage:  true,
	}
}

func validateSettings(update SettingsUpdate) (AppSettings, error) {
	settings := AppSettings{
		Theme:         strings.ToLower(strings.TrimSpace(update.Theme)),
		DefaultAuthor: strings.TrimSpace(update.DefaultAuthor),
		AgentProfile:  strings.ToLower(strings.TrimSpace(update.AgentProfile)),
		AgentModel:    strings.TrimSpace(update.AgentModel),
		ContextMode:   strings.ToLower(strings.TrimSpace(update.ContextMode)),
		ShowAIUsage:   update.ShowAIUsage,
	}
	if settings.Theme != "dark" && settings.Theme != "light" {
		return AppSettings{}, errors.New("theme must be dark or light")
	}
	switch settings.AgentProfile {
	case "omp", "codex", "claude", "openrouter", "openai":
	default:
		return AppSettings{}, errors.New("unsupported AI connection profile")
	}
	switch settings.ContextMode {
	case "focused", "balanced", "deep":
	default:
		return AppSettings{}, errors.New("context mode must be focused, balanced, or deep")
	}
	if len(settings.DefaultAuthor) > 80 {
		return AppSettings{}, errors.New("default author exceeds 80 characters")
	}
	if len(settings.AgentModel) > 120 {
		return AppSettings{}, errors.New("model identifier exceeds 120 characters")
	}
	return settings, nil
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
	return settings, nil
}

func (s *Store) saveAppSettings(ctx context.Context, settings AppSettings) error {
	settings.HasAPIKey = false
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

func (service *AppService) Settings() (AppSettings, error) {
	ctx := context.Background()
	settings, err := service.store.loadAppSettings(ctx)
	if err != nil {
		return AppSettings{}, err
	}
	if _, err := service.store.readSetting(ctx, agentSecretKey); err == nil {
		settings.HasAPIKey = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AppSettings{}, err
	}
	return settings, nil
}

func (service *AppService) SaveSettings(update SettingsUpdate) (AppSettings, error) {
	ctx := context.Background()
	settings, err := validateSettings(update)
	if err != nil {
		return AppSettings{}, err
	}
	apiKey := strings.TrimSpace(update.APIKey)
	if len(apiKey) > 8192 {
		return AppSettings{}, errors.New("API key exceeds 8192 characters")
	}
	if update.ClearAPIKey {
		if err := service.store.deleteSetting(ctx, agentSecretKey); err != nil {
			return AppSettings{}, err
		}
	} else if apiKey != "" {
		protected, err := protectSecret(apiKey, service.config.DataDir)
		if err != nil {
			return AppSettings{}, fmt.Errorf("protect API key: %w", err)
		}
		if err := service.store.writeSetting(ctx, agentSecretKey, protected); err != nil {
			return AppSettings{}, err
		}
	}
	if err := service.store.saveAppSettings(ctx, settings); err != nil {
		return AppSettings{}, err
	}
	return service.Settings()
}

func (service *AppService) agentLaunchSettings(ctx context.Context) (agentLaunchSettings, error) {
	settings, err := service.store.loadAppSettings(ctx)
	if err != nil {
		return agentLaunchSettings{}, err
	}
	result := agentLaunchSettings{Profile: settings.AgentProfile, Model: settings.AgentModel, ContextMode: settings.ContextMode}
	encoded, err := service.store.readSetting(ctx, agentSecretKey)
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

func (service *AppService) ListAgentModels() ([]AgentModelOption, error) {
	ompPath, err := resolveOMPPath(service.config.OMPPath)
	if err != nil {
		return nil, err
	}
	commandContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(commandContext, ompPath, "--no-extensions", "models", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("list OMP models: %w", err)
	}
	var response struct {
		Models []AgentModelOption `json:"models"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("decode OMP models: %w", err)
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
	ompPath, err := resolveOMPPath(service.config.OMPPath)
	if err != nil {
		usage.UsageError = err.Error()
		return usage, nil
	}
	commandContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(commandContext, ompPath, "usage", "--json").Output()
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
