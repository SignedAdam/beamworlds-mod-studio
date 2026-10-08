package modkit

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// createWorkspaceFromFolder copies every regular file from the source folder
// into the workspace files root. Uses SourceContentID as the fingerprint and
// verifies the folder listing did not change during the copy.
func createWorkspaceFromFolder(ctx context.Context, sourceFolder, destination, id, entityID, artifactID string, kind Kind) (WorkspaceManifest, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sourceFingerprint, err := SourceContentID(ctx, sourceFolder)
	if err != nil {
		return WorkspaceManifest{}, fmt.Errorf("fingerprint source folder: %w", err)
	}
	entries, err := walkFolderListing(ctx, sourceFolder)
	if err != nil {
		return WorkspaceManifest{}, fmt.Errorf("walk source folder: %w", err)
	}
	if len(entries) > maxEntries {
		return WorkspaceManifest{}, fmt.Errorf("folder has %d entries; workspace limit is %d", len(entries), maxEntries)
	}
	if _, err := os.Stat(destination); err == nil {
		return WorkspaceManifest{}, fmt.Errorf("workspace already exists: %s", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return WorkspaceManifest{}, err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return WorkspaceManifest{}, err
	}
	temporary, err := os.MkdirTemp(parent, ".workspace-*")
	if err != nil {
		return WorkspaceManifest{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(temporary)
		}
	}()
	filesRoot := filepath.Join(temporary, "files")
	if err := os.MkdirAll(filesRoot, 0o755); err != nil {
		return WorkspaceManifest{}, err
	}

	snapshots := make([]FileSnapshot, 0, len(entries))
	var extracted int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return WorkspaceManifest{}, err
		}
		destinationPath, pathErr := safeJoin(filesRoot, entry.path)
		if pathErr != nil {
			return WorkspaceManifest{}, pathErr
		}
		if entry.isDir {
			if err := os.MkdirAll(destinationPath, 0o755); err != nil {
				return WorkspaceManifest{}, err
			}
			continue
		}
		if entry.size > maxWorkspaceFile {
			return WorkspaceManifest{}, fmt.Errorf("entry %s exceeds workspace file limit", entry.path)
		}
		extracted += entry.size
		if extracted > maxWorkspaceBytes {
			return WorkspaceManifest{}, fmt.Errorf("workspace exceeds %d-byte extraction limit", maxWorkspaceBytes)
		}
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
			return WorkspaceManifest{}, err
		}
		sourcePath, joinErr := safeFolderJoin(sourceFolder, entry.path)
		if joinErr != nil {
			return WorkspaceManifest{}, joinErr
		}
		input, err := os.Open(sourcePath)
		if err != nil {
			return WorkspaceManifest{}, err
		}
		output, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			_ = input.Close()
			return WorkspaceManifest{}, err
		}
		hash := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(contextReader{ctx: ctx, reader: input}, maxWorkspaceFile+1))
		closeOutErr := output.Close()
		closeInErr := input.Close()
		if copyErr != nil {
			return WorkspaceManifest{}, copyErr
		}
		if closeOutErr != nil {
			return WorkspaceManifest{}, closeOutErr
		}
		if closeInErr != nil {
			return WorkspaceManifest{}, closeInErr
		}
		if written > maxWorkspaceFile {
			return WorkspaceManifest{}, fmt.Errorf("entry size limit exceeded for %s", entry.path)
		}
		if !entry.modTime.IsZero() {
			_ = os.Chtimes(destinationPath, entry.modTime, entry.modTime)
		}
		snapshots = append(snapshots, FileSnapshot{
			Path: entry.path, SHA256: hex.EncodeToString(hash.Sum(nil)),
			SizeBytes: written, ModifiedNS: entry.modTime.UnixNano(),
		})
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Path < snapshots[j].Path })
	manifest := WorkspaceManifest{
		ID: id, EntityID: entityID, ArtifactID: artifactID, CreatedAt: time.Now().UTC(), Kind: kind, Files: snapshots,
	}
	// Verify the folder listing fingerprint did not change during the copy.
	afterFingerprint, err := SourceContentID(ctx, sourceFolder)
	if err != nil {
		return WorkspaceManifest{}, err
	}
	if sourceFingerprint != afterFingerprint {
		return WorkspaceManifest{}, errors.New("source folder changed while creating the workspace")
	}
	manifest.SourceFingerprint = sourceFingerprint
	metadataDir := filepath.Join(temporary, ".modstudio")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		return WorkspaceManifest{}, err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return WorkspaceManifest{}, err
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "workspace.json"), encoded, 0o644); err != nil {
		return WorkspaceManifest{}, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return WorkspaceManifest{}, err
	}
	keep = true
	return manifest, nil
}

// exportWorkspaceFromFolder writes a deterministic ZIP of the workspace files
// (same spirit as ExportWorkspace for ZIPs) and verifies the export.
func exportWorkspaceFromFolder(ctx context.Context, filesRoot, outputPath string) (ExportResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ExportResult{}, err
	}
	if _, err := os.Stat(outputPath); err == nil {
		return ExportResult{}, fmt.Errorf("export already exists: %s", outputPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ExportResult{}, err
	}
	current, err := ListWorkspaceFilesContext(ctx, filesRoot)
	if err != nil {
		return ExportResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return ExportResult{}, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(outputPath), ".export-*.zip")
	if err != nil {
		return ExportResult{}, err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	writer := zip.NewWriter(temporary)
	entryCount := 0
	sort.Slice(current, func(i, j int) bool { return current[i].Path < current[j].Path })
	for _, item := range current {
		if err := ctx.Err(); err != nil {
			_ = writer.Close()
			return ExportResult{}, err
		}
		header := zip.FileHeader{Name: item.Path, Method: zip.Deflate, Modified: time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)}
		if err := writeWorkspaceEntryContext(ctx, writer, header, filesRoot, item.Path); err != nil {
			_ = writer.Close()
			return ExportResult{}, err
		}
		entryCount++
	}
	if err := writer.Close(); err != nil {
		return ExportResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ExportResult{}, err
	}
	if err := temporary.Sync(); err != nil {
		return ExportResult{}, err
	}
	if err := temporary.Close(); err != nil {
		return ExportResult{}, err
	}
	verification, err := Inspect(ctx, temporaryPath)
	if err != nil {
		return ExportResult{}, fmt.Errorf("verify export: %w", err)
	}
	if !verification.ValidArchive {
		return ExportResult{}, fmt.Errorf("export verification reported an invalid archive")
	}
	hash, err := FullSHA256(ctx, temporaryPath)
	if err != nil {
		return ExportResult{}, err
	}
	info, err := os.Stat(temporaryPath)
	if err != nil {
		return ExportResult{}, err
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return ExportResult{}, err
	}
	keep = true
	return ExportResult{Path: outputPath, SHA256: hash, SizeBytes: info.Size(), CreatedAt: time.Now().UTC(), EntryCount: entryCount, Fingerprint: verification.CentralFingerprint}, nil
}
