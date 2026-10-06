//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func modImportHomeDirectory() (string, error) {
	return os.UserHomeDir()
}

func modImportDownloadsDirectory() (string, error) {
	path, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return "", fmt.Errorf("Windows KnownFolder Downloads lookup failed: %w", err)
	}
	if path == "" {
		return "", fmt.Errorf("Windows KnownFolder Downloads lookup returned an empty path")
	}
	return filepath.Clean(path), nil
}

func modImportDriveDirectories() ([]string, error) {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil, fmt.Errorf("GetLogicalDrives failed: %w", err)
	}
	drives := make([]string, 0, 26)
	for index := range 26 {
		if mask&(uint32(1)<<uint(index)) == 0 {
			continue
		}
		drives = append(drives, fmt.Sprintf("%c:%c", 'A'+index, filepath.Separator))
	}
	return drives, nil
}
