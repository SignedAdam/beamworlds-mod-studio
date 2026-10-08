package main

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

func TestPlayLaunchesWithTheSavedRenderer(t *testing.T) {
	service := newTestAppService(t)
	service.config.GameExecutable = filepath.Join(t.TempDir(), "BeamNG.drive.x64.exe")
	_, collectionID := scanAndCreateCollection(t, service, "Renderer", 1, 9990)
	var got []string
	service.startProcess = func(_ string, arguments []string, _ string) (ProcessLaunch, error) {
		got = slices.Clone(arguments)
		return ProcessLaunch{PID: 7}, nil
	}
	launch := func() PlayResult {
		t.Helper()
		result, err := service.LaunchPlaySelection(context.Background(), resolveAndFingerprint(t, service, collectionID))
		if err != nil || !result.Started {
			t.Fatalf("launch failed: %#v %v", result, err)
		}
		return result
	}

	if renderer, err := service.GameRenderer(); err != nil || renderer != "default" {
		t.Fatalf("first-run renderer = %q, %v", renderer, err)
	}
	result := launch()
	if want := []string{"-userpath", result.Activation.UserPath}; !slices.Equal(got, want) {
		t.Fatalf("default renderer must leave the choice to BeamNG: got %q, want %q", got, want)
	}

	if _, err := service.SetGameRenderer("vulkan"); err != nil {
		t.Fatal(err)
	}
	result = launch()
	if want := []string{"-userpath", result.Activation.UserPath, "-gfx", "vk"}; !slices.Equal(got, want) {
		t.Fatalf("Vulkan launch arguments = %q, want %q", got, want)
	}

	if _, err := service.SetGameRenderer("opengl"); err == nil {
		t.Fatal("an unknown renderer was accepted")
	}
	if renderer, err := service.GameRenderer(); err != nil || renderer != "vulkan" {
		t.Fatalf("a rejected choice replaced the saved renderer: %q, %v", renderer, err)
	}
}
