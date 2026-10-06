//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

func modImportHomeDirectory() (string, error) {
	return os.UserHomeDir()
}

func modImportDownloadsDirectory() (string, error) {
	home, err := modImportHomeDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads"), nil
}

func modImportDriveDirectories() ([]string, error) {
	return make([]string, 0), nil
}
