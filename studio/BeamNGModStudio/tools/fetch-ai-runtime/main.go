package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	releaseVersion = "18.1.2"
	releaseBaseURL = "https://github.com/can1357/oh-my-pi/releases/download/v" + releaseVersion + "/"
)

type releaseAsset struct {
	name       string
	sha256     string
	size       int64
	executable bool
}

var releaseAssets = [...]releaseAsset{
	{name: "omp-windows-x64.exe", sha256: "8a36c4a4be135ff94f5eed287f62b5eb39e65e78d18bdeccf8a493d02d82722b", size: 160749568, executable: true},
	{name: "LICENSE", sha256: "16c45f9d667442781f03fa198914cc39abcaa48ec5ed8f644643e554ca2fbf63", size: 1144},
	{name: "THIRD-PARTY-NOTICES.txt", sha256: "104142244b8781b7828e64aa79a61b03fdf16e8a3464278647c3d84ad22cbce0", size: 1077086},
}

func main() {
	if err := fetch(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "managed AI runtime fetch:", err)
		os.Exit(1)
	}
}

func fetch(ctx context.Context) error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	payloadDir := filepath.Join(root, "bin", "runtime")
	if err := os.MkdirAll(payloadDir, 0o700); err != nil {
		return fmt.Errorf("create private payload directory: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	for _, asset := range releaseAssets {
		if err := fetchAsset(ctx, client, payloadDir, asset); err != nil {
			return err
		}
	}
	return nil
}

func moduleRoot() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	current, err = filepath.Abs(current)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("could not locate studio go.mod")
		}
		current = parent
	}
}

func fetchAsset(ctx context.Context, client *http.Client, destinationDir string, asset releaseAsset) error {
	destination := filepath.Join(destinationDir, asset.name)
	if validAsset(destination, asset) {
		return nil
	}
	staging, err := os.CreateTemp(destinationDir, "."+asset.name+"-*.download")
	if err != nil {
		return fmt.Errorf("create temporary download for %s: %w", asset.name, err)
	}
	stagingPath := staging.Name()
	removeStaging := true
	defer func() {
		if removeStaging {
			_ = os.Remove(stagingPath)
		}
	}()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseBaseURL+asset.name, nil)
	if err != nil {
		_ = staging.Close()
		return fmt.Errorf("create download request for %s: %w", asset.name, err)
	}
	request.Header.Set("User-Agent", "BeamWorlds-ModStudio-managed-runtime-fetch/1")
	response, err := client.Do(request)
	if err != nil {
		_ = staging.Close()
		return fmt.Errorf("download %s: %w", asset.name, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_ = staging.Close()
		return fmt.Errorf("download %s: unexpected HTTP status %s", asset.name, response.Status)
	}
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(staging, digest), response.Body)
	syncErr := staging.Sync()
	closeErr := staging.Close()
	if copyErr != nil {
		return fmt.Errorf("write download %s: %w", asset.name, copyErr)
	}
	if syncErr != nil {
		return fmt.Errorf("flush download %s: %w", asset.name, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close download %s: %w", asset.name, closeErr)
	}
	digestText := hex.EncodeToString(digest.Sum(nil))
	if written != asset.size || digestText != asset.sha256 {
		return fmt.Errorf("checksum verification failed for %s (size=%d sha256=%s)", asset.name, written, digestText)
	}
	if err := replaceFile(stagingPath, destination); err != nil {
		return fmt.Errorf("install %s: %w", asset.name, err)
	}
	removeStaging = false
	if asset.executable {
		if err := os.Chmod(destination, 0o700); err != nil && runtime.GOOS != "windows" {
			return fmt.Errorf("mark %s executable: %w", asset.name, err)
		}
	}
	return nil
}

func validAsset(path string, asset releaseAsset) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() != asset.size {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), asset.sha256)
}

func replaceFile(stagingPath, destination string) error {
	if err := os.Rename(stagingPath, destination); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}
	backup := destination + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(destination, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(stagingPath, destination); err != nil {
		_ = os.Rename(backup, destination)
		return err
	}
	_ = os.Remove(backup)
	return nil
}
