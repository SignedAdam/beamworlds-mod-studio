package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// nativeFileManagerLauncher is kept injectable so platform command selection can
// be tested without starting a desktop process.
type nativeFileManagerLauncher func(context.Context, string, ...string) error

var launchNativeFileManager nativeFileManagerLauncher = startNativeFileManager

func startNativeFileManager(ctx context.Context, executable string, args ...string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	command := exec.CommandContext(ctx, executable, args...)
	return command.Run()
}

func (service *AppService) FileManagerActionLabel() string {
	return fileManagerActionLabel(runtime.GOOS)
}

func fileManagerActionLabel(goos string) string {
	switch goos {
	case "windows":
		return "Open in Explorer"
	case "darwin":
		return "Reveal in Finder"
	default:
		return "Open in File Manager"
	}
}

func (service *AppService) RevealWorkspacePath(workspaceID, relativePath string) error {
	workspace, err := service.store.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return fmt.Errorf("open workspace path %s in the file manager: load workspace: %w", relativePath, err)
	}
	relativePath, err = cleanWorkspaceRelativePath(relativePath)
	if err != nil {
		return err
	}
	target := filepath.Join(workspace.FilesRoot, filepath.FromSlash(relativePath))
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("open workspace path %s in the file manager: stat %s: %w", relativePath, target, err)
	}
	return revealWorkspacePathNative(runtime.GOOS, target, info.IsDir(), launchNativeFileManager)
}

// revealWorkspacePathNative chooses native selection for files where supported,
// and falls back to opening the containing folder when selection cannot launch.
// Directory targets are always opened directly.
func revealWorkspacePathNative(goos, target string, isDir bool, launch nativeFileManagerLauncher) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("open in the file manager: empty target path")
	}
	if launch == nil {
		launch = startNativeFileManager
	}

	primaryExecutable, primaryArgs := nativeFileManagerCommand(goos, target, isDir)
	if err := launch(context.Background(), primaryExecutable, primaryArgs...); err == nil {
		return nil
	} else if isDir || !supportsNativeSelection(goos) {
		return fmt.Errorf("unable to open %s in the file manager (%s): %w", target, primaryExecutable, err)
	} else {
		primaryErr := err
		folder := filepath.Dir(target)
		fallbackExecutable, fallbackArgs := nativeFileManagerFolderCommand(goos, folder)
		if fallbackErr := launch(context.Background(), fallbackExecutable, fallbackArgs...); fallbackErr == nil {
			return nil
		} else {
			return fmt.Errorf("unable to reveal %s with %s (%w); opening containing folder %s with %s also failed: %w", target, primaryExecutable, primaryErr, folder, fallbackExecutable, fallbackErr)
		}
	}
}

func supportsNativeSelection(goos string) bool {
	return goos == "windows" || goos == "darwin"
}

func nativeFileManagerCommand(goos, target string, isDir bool) (string, []string) {
	switch goos {
	case "windows":
		if isDir {
			return "explorer.exe", []string{target}
		}
		// Explorer's select syntax is one argument, but remains argv-safe because
		// the path is never interpreted by a shell.
		return "explorer.exe", []string{"/select," + target}
	case "darwin":
		if isDir {
			return "open", []string{target}
		}
		return "open", []string{"-R", target}
	default:
		if !isDir {
			target = filepath.Dir(target)
		}
		return "xdg-open", []string{target}
	}
}

func nativeFileManagerFolderCommand(goos, folder string) (string, []string) {
	switch goos {
	case "windows":
		return "explorer.exe", []string{folder}
	case "darwin":
		return "open", []string{folder}
	default:
		return "xdg-open", []string{folder}
	}
}
