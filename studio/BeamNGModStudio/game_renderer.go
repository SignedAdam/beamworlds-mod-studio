package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	gameRendererSettingKey = "app.game-renderer.v1"
	defaultGameRenderer    = "default"
)

// The renderers BeamNG's own launcher offers, with the arguments it passes to
// Bin64/BeamNG.drive.x64.exe. "default" passes nothing, so the engine picks
// DirectX 12 and falls back to DirectX 11, like the launcher's main button.
var gameRendererArguments = map[string][]string{
	defaultGameRenderer: nil,
	"vulkan":            {"-gfx", "vk"},
	"d3d12":             {"-gfx", "d3d12"},
	"d3d11":             {"-gfx", "dx11"},
}

// GameRenderer returns the renderer every Studio launch of BeamNG uses.
func (service *AppService) GameRenderer() (string, error) {
	return service.store.gameRenderer(context.Background())
}

// SetGameRenderer remembers the renderer for every later launch: Play,
// workspace tests and isolated game tests.
func (service *AppService) SetGameRenderer(renderer string) (string, error) {
	if _, known := gameRendererArguments[renderer]; !known {
		return "", fmt.Errorf("unknown BeamNG renderer %q", renderer)
	}
	if err := service.store.writeSetting(context.Background(), gameRendererSettingKey, renderer); err != nil {
		return "", fmt.Errorf("save BeamNG renderer: %w", err)
	}
	return renderer, nil
}

func (s *Store) gameRenderer(ctx context.Context) (string, error) {
	renderer, err := s.readSetting(ctx, gameRendererSettingKey)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultGameRenderer, nil
	}
	if err != nil {
		return "", fmt.Errorf("read BeamNG renderer: %w", err)
	}
	if _, known := gameRendererArguments[renderer]; !known {
		return defaultGameRenderer, nil
	}
	return renderer, nil
}

// gameRendererArgs returns the command-line arguments for the saved renderer.
func (service *AppService) gameRendererArgs(ctx context.Context) ([]string, error) {
	renderer, err := service.store.gameRenderer(ctx)
	if err != nil {
		return nil, err
	}
	return gameRendererArguments[renderer], nil
}
