package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type nativeLaunchCall struct {
	executable string
	args       []string
}

func TestFileManagerActionLabelUsesNativePlatformWording(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"windows": "Open in Explorer",
		"darwin":  "Reveal in Finder",
		"linux":   "Open in File Manager",
		"freebsd": "Open in File Manager",
	}
	for goos, want := range tests {
		if got := fileManagerActionLabel(goos); got != want {
			t.Errorf("fileManagerActionLabel(%q) = %q, want %q", goos, got, want)
		}
	}
}

func TestNativeFileManagerCommandsUsePlatformNativeArguments(t *testing.T) {
	target := filepath.Join(t.TempDir(), "mods", "café folder", "mod file [v1].jbeam")
	folder := filepath.Dir(target)
	tests := []struct {
		name       string
		goos       string
		isDir      bool
		executable string
		args       []string
	}{
		{name: "windows file", goos: "windows", executable: "explorer.exe", args: []string{"/select," + target}},
		{name: "windows folder", goos: "windows", isDir: true, executable: "explorer.exe", args: []string{folder}},
		{name: "mac file", goos: "darwin", executable: "open", args: []string{"-R", target}},
		{name: "mac folder", goos: "darwin", isDir: true, executable: "open", args: []string{folder}},
		{name: "linux file fallback", goos: "linux", executable: "xdg-open", args: []string{folder}},
		{name: "linux folder", goos: "linux", isDir: true, executable: "xdg-open", args: []string{folder}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []nativeLaunchCall
			launcher := func(_ context.Context, executable string, args ...string) error {
				calls = append(calls, nativeLaunchCall{executable: executable, args: append([]string(nil), args...)})
				return nil
			}
			targetPath := target
			if test.isDir {
				targetPath = folder
			}
			if err := revealWorkspacePathNative(test.goos, targetPath, test.isDir, launcher); err != nil {
				t.Fatalf("revealWorkspacePathNative: %v", err)
			}
			if len(calls) != 1 {
				t.Fatalf("launcher calls = %d, want 1", len(calls))
			}
			if calls[0].executable != test.executable {
				t.Fatalf("executable = %q, want %q", calls[0].executable, test.executable)
			}
			if len(calls[0].args) != len(test.args) {
				t.Fatalf("argv = %#v, want %#v", calls[0].args, test.args)
			}
			for index := range test.args {
				if calls[0].args[index] != test.args[index] {
					t.Fatalf("argv[%d] = %q, want %q", index, calls[0].args[index], test.args[index])
				}
			}
		})
	}
}

func TestNativeFileManagerSelectionFallsBackToContainingFolder(t *testing.T) {
	target := filepath.Join(t.TempDir(), "空 folder", "space name.txt")
	folder := filepath.Dir(target)
	var calls []nativeLaunchCall
	launcher := func(_ context.Context, executable string, args ...string) error {
		calls = append(calls, nativeLaunchCall{executable: executable, args: append([]string(nil), args...)})
		if len(calls) == 1 {
			return errors.New("selection is unavailable")
		}
		return nil
	}
	if err := revealWorkspacePathNative("darwin", target, false, launcher); err != nil {
		t.Fatalf("revealWorkspacePathNative fallback: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("launcher calls = %d, want 2", len(calls))
	}
	if got := calls[0]; got.executable != "open" || len(got.args) != 2 || got.args[0] != "-R" || got.args[1] != target {
		t.Fatalf("selection call = %#v, want open -R %q", got, target)
	}
	if got := calls[1]; got.executable != "open" || len(got.args) != 1 || got.args[0] != folder {
		t.Fatalf("fallback call = %#v, want open %q", got, folder)
	}
}

func TestNativeFileManagerErrorIdentifiesTargetAndBothFailures(t *testing.T) {
	target := filepath.Join(t.TempDir(), "folder with spaces", "非ASCII.txt")
	launcher := func(_ context.Context, _ string, _ ...string) error {
		return errors.New("manager unavailable")
	}
	err := revealWorkspacePathNative("windows", target, false, launcher)
	if err == nil {
		t.Fatal("revealWorkspacePathNative returned nil after both launches failed")
	}
	message := err.Error()
	for _, want := range []string{target, filepath.Dir(target), "manager unavailable", "containing folder"} {
		if !strings.Contains(message, want) {
			t.Errorf("error %q does not contain %q", message, want)
		}
	}
}

func TestNativeFileManagerFolderErrorIsActionable(t *testing.T) {
	target := filepath.Join(t.TempDir(), "folder with spaces")
	launcher := func(_ context.Context, _ string, _ ...string) error {
		return errors.New("xdg-open missing")
	}
	err := revealWorkspacePathNative("linux", target, true, launcher)
	if err == nil {
		t.Fatal("revealWorkspacePathNative returned nil after folder launch failed")
	}
	message := err.Error()
	if !strings.Contains(message, target) || !strings.Contains(message, "xdg-open missing") {
		t.Fatalf("error %q lacks path or launch failure", message)
	}
}

func TestRevealStoragePathOnlyOpensStorageFolders(t *testing.T) {
	service := newTestAppService(t)
	inside := filepath.Join(service.config.ProfileDir, legacyArchiveCacheDirectory, "leftover.zip")
	outside := filepath.Join(t.TempDir(), "elsewhere.zip")
	for _, path := range []string{inside, outside} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("zip"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var opened []string
	previous := launchNativeFileManager
	launchNativeFileManager = func(_ context.Context, _ string, args ...string) error {
		opened = append(opened, strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() { launchNativeFileManager = previous })

	if err := service.RevealStoragePath(inside); err != nil {
		t.Fatalf("RevealStoragePath(inside): %v", err)
	}
	for _, path := range []string{outside, "leftover.zip"} {
		if err := service.RevealStoragePath(path); err == nil {
			t.Fatalf("RevealStoragePath(%q) succeeded; want refusal", path)
		}
	}
	if len(opened) != 1 || !strings.Contains(opened[0], filepath.Base(inside)) {
		t.Fatalf("file manager calls = %q, want one call for %s", opened, inside)
	}
}
