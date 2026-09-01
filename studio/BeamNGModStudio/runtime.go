package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

const maxRuntimeLogBytes = int64(16 << 20)

var safeArchiveLabel = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

type ProcessLaunch struct {
	PID        int    `json:"pid"`
	Executable string `json:"executable"`
	StartedAt  string `json:"startedAt"`
}

type RuntimeDiagnostic struct {
	Severity  string `json:"severity"`
	Component string `json:"component"`
	Message   string `json:"message"`
	Line      int    `json:"line"`
	Relevant  bool   `json:"relevant"`
}

type RuntimeReport struct {
	LogPath      string              `json:"logPath"`
	FromOffset   int64               `json:"fromOffset"`
	ToOffset     int64               `json:"toOffset"`
	FreshBytes   int64               `json:"freshBytes"`
	Truncated    bool                `json:"truncated"`
	Errors       int                 `json:"errors"`
	Warnings     int                 `json:"warnings"`
	RelevantHits int                 `json:"relevantHits"`
	Diagnostics  []RuntimeDiagnostic `json:"diagnostics"`
	AnalyzedAt   string              `json:"analyzedAt"`
}

func (service *AppService) exportWorkspace(ctx context.Context, workspaceID, label, exportKind string) (ExportResponse, error) {
	workspace, baseline, err := service.workspaceAndManifest(workspaceID)
	if err != nil {
		return ExportResponse{}, err
	}
	sourceHash, err := modkit.FullSHA256(ctx, workspace.SourcePath)
	if err != nil {
		return ExportResponse{}, fmt.Errorf("verify immutable source: %w", err)
	}
	if !strings.EqualFold(sourceHash, baseline.SourceFingerprint) || !strings.EqualFold(sourceHash, workspace.SourceSHA256) {
		return ExportResponse{}, errors.New("source archive changed after workspace creation; create a new workspace before exporting")
	}
	validation := modkit.ValidateWorkspace(workspace.FilesRoot)
	if err := service.store.SetWorkspaceValidation(ctx, workspace.ID, validation); err != nil {
		return ExportResponse{}, err
	}
	if !validation.Valid {
		messages := []string{}
		for _, issue := range validation.Issues {
			if issue.Severity == modkit.SeverityError {
				messages = append(messages, issue.Message)
			}
			if len(messages) == 3 {
				break
			}
		}
		return ExportResponse{}, fmt.Errorf("workspace validation failed: %s", strings.Join(messages, "; "))
	}
	item, err := service.store.GetLibraryItem(ctx, workspace.EntityID)
	if err != nil {
		return ExportResponse{}, err
	}
	exportID, err := modkit.NewID()
	if err != nil {
		return ExportResponse{}, err
	}
	label = sanitizeArchiveLabel(label)
	if label == "" {
		label = sanitizeArchiveLabel(item.DisplayName)
	}
	if label == "" {
		label = "beamng-mod"
	}
	filename := fmt.Sprintf("%s-modstudio-%s-%s.zip", label, time.Now().UTC().Format("20060102-150405"), exportID[:8])
	outputPath := filepath.Join(service.config.ExportDir, filename)
	result, err := modkit.ExportWorkspace(ctx, workspace.SourcePath, workspace.FilesRoot, outputPath, baseline.Files)
	if err != nil {
		return ExportResponse{}, err
	}
	afterHash, err := modkit.FullSHA256(ctx, workspace.SourcePath)
	if err != nil || !strings.EqualFold(afterHash, sourceHash) {
		_ = os.Remove(outputPath)
		return ExportResponse{}, errors.New("source archive integrity check failed after export")
	}
	exportedManifest, err := modkit.Inspect(ctx, outputPath)
	if err != nil {
		_ = os.Remove(outputPath)
		return ExportResponse{}, fmt.Errorf("inspect exported artifact: %w", err)
	}
	exportedManifest.FullSHA256 = result.SHA256
	artifactID, err := service.store.EnsureArtifact(ctx, exportedManifest)
	if err != nil {
		_ = os.Remove(outputPath)
		return ExportResponse{}, err
	}
	record := ExportRecord{ID: exportID, WorkspaceID: workspace.ID, ArtifactID: artifactID, Path: outputPath, SHA256: result.SHA256, Kind: exportKind, CreatedAt: result.CreatedAt.UTC().Format(time.RFC3339Nano)}
	if err := service.store.AddExport(ctx, record, workspace.EntityID); err != nil {
		_ = os.Remove(outputPath)
		return ExportResponse{}, err
	}
	return ExportResponse{Record: record, Result: result}, nil
}

func (service *AppService) installExportForTest(ctx context.Context, workspaceID, exportID string) (TestInstallRecord, error) {
	workspace, err := service.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return TestInstallRecord{}, err
	}
	export, err := service.store.GetExport(ctx, exportID)
	if err != nil {
		return TestInstallRecord{}, err
	}
	if export.WorkspaceID != workspaceID {
		return TestInstallRecord{}, errors.New("export does not belong to the selected workspace")
	}
	hash, err := modkit.FullSHA256(ctx, export.Path)
	if err != nil {
		return TestInstallRecord{}, err
	}
	if !strings.EqualFold(hash, export.SHA256) {
		return TestInstallRecord{}, errors.New("export checksum no longer matches its artifact record")
	}
	if err := os.MkdirAll(service.config.TestInstallDir, 0o755); err != nil {
		return TestInstallRecord{}, err
	}
	installID, err := modkit.NewID()
	if err != nil {
		return TestInstallRecord{}, err
	}
	filename := fmt.Sprintf("modstudio-test-%s.zip", workspace.ID[:8])
	destination := filepath.Join(service.config.TestInstallDir, filename)
	if err := copyFileAtomic(export.Path, destination); err != nil {
		return TestInstallRecord{}, err
	}
	installedHash, err := modkit.FullSHA256(ctx, destination)
	if err != nil || !strings.EqualFold(installedHash, export.SHA256) {
		_ = os.Remove(destination)
		return TestInstallRecord{}, errors.New("test installation checksum verification failed")
	}
	logPath := service.findRuntimeLog()
	var logOffset int64
	if info, statErr := os.Stat(logPath); statErr == nil {
		logOffset = info.Size()
	}
	record := TestInstallRecord{
		ID: installID, WorkspaceID: workspaceID, ExportID: exportID, Path: destination, SHA256: installedHash,
		InstalledAt: nowUTC(), LogBaselineAt: nowUTC(), LogPath: logPath, LogOffset: logOffset, Active: true,
	}
	previous, previousErr := service.store.GetActiveTestInstall(ctx, workspaceID)
	if err := service.store.SaveTestInstall(ctx, record); err != nil {
		_ = os.Remove(destination)
		return TestInstallRecord{}, err
	}
	if previousErr == nil && previous.Path != destination && safeTestInstallPath(previous.Path, service.config.ActiveModsDir) {
		_ = os.Remove(previous.Path)
	}
	_ = service.store.AppendEvent(ctx, workspace.EntityID, "test_installed", map[string]any{"workspaceId": workspaceID, "exportId": exportID, "path": destination, "sha256": installedHash})
	return record, nil
}

func (service *AppService) uninstallTest(ctx context.Context, workspaceID string) error {
	workspace, err := service.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	record, err := service.store.GetActiveTestInstall(ctx, workspaceID)
	if err != nil {
		return err
	}
	if !safeTestInstallPath(record.Path, service.config.ActiveModsDir) {
		return errors.New("refusing to remove a path outside the managed test-install namespace")
	}
	if err := os.Remove(record.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := service.store.DeactivateTestInstall(ctx, record.ID); err != nil {
		return err
	}
	return service.store.AppendEvent(ctx, workspace.EntityID, "test_uninstalled", map[string]any{"workspaceId": workspaceID, "path": record.Path})
}

func (service *AppService) launchBeamNG(ctx context.Context, workspaceID string) (ProcessLaunch, error) {
	workspace, err := service.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return ProcessLaunch{}, err
	}
	install, err := service.store.GetActiveTestInstall(ctx, workspaceID)
	if err != nil {
		return ProcessLaunch{}, errors.New("install a workspace export for testing before launching BeamNG")
	}
	installedHash, err := modkit.FullSHA256(ctx, install.Path)
	if err != nil || !strings.EqualFold(installedHash, install.SHA256) {
		return ProcessLaunch{}, errors.New("managed test archive is missing or its checksum changed; install the export again")
	}
	if service.config.GameExecutable == "" {
		return ProcessLaunch{}, errors.New("BeamNG executable was not found in the configured game install directory")
	}
	logPath := service.findRuntimeLog()
	var logOffset int64
	if info, statErr := os.Stat(logPath); statErr == nil {
		logOffset = info.Size()
	}
	if err := service.store.UpdateTestLogBaseline(ctx, install.ID, logPath, logOffset, nowUTC()); err != nil {
		return ProcessLaunch{}, err
	}
	command := exec.Command(service.config.GameExecutable)
	command.Dir = service.config.GameInstallDir
	if err := command.Start(); err != nil {
		return ProcessLaunch{}, err
	}
	launch := ProcessLaunch{PID: command.Process.Pid, Executable: service.config.GameExecutable, StartedAt: nowUTC()}
	if err := command.Process.Release(); err != nil {
		return ProcessLaunch{}, err
	}
	_ = service.store.AppendEvent(ctx, workspace.EntityID, "beamng_launched", map[string]any{"workspaceId": workspaceID, "pid": launch.PID})
	return launch, nil
}

func (service *AppService) analyzeRuntime(ctx context.Context, workspaceID string) (RuntimeReport, error) {
	workspace, err := service.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return RuntimeReport{}, err
	}
	install, err := service.store.GetActiveTestInstall(ctx, workspaceID)
	if err != nil {
		return RuntimeReport{}, errors.New("no active test installation exists for this workspace")
	}
	logPath := service.findRuntimeLog()
	if logPath == "" {
		return RuntimeReport{}, errors.New("BeamNG runtime log was not found")
	}
	offset := install.LogOffset
	if !samePath(logPath, install.LogPath) {
		offset = 0
	}
	file, err := os.Open(logPath)
	if err != nil {
		return RuntimeReport{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return RuntimeReport{}, err
	}
	if offset < 0 || offset > info.Size() {
		offset = 0
	}
	truncated := false
	if info.Size()-offset > maxRuntimeLogBytes {
		offset = info.Size() - maxRuntimeLogBytes
		truncated = true
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return RuntimeReport{}, err
	}
	report := RuntimeReport{LogPath: logPath, FromOffset: offset, ToOffset: info.Size(), FreshBytes: info.Size() - offset, Truncated: truncated, Diagnostics: []RuntimeDiagnostic{}, AnalyzedAt: nowUTC()}
	item, _ := service.store.GetLibraryItem(ctx, workspace.EntityID)
	relevanceTerms := []string{strings.ToLower(filepath.Base(install.Path)), strings.ToLower(item.Manifest.Filename)}
	for _, values := range item.Manifest.Namespaces {
		for _, value := range values {
			if value != "" {
				relevanceTerms = append(relevanceTerms, strings.ToLower(value))
			}
		}
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	lineNumber := 0
	relevantDiagnostics := make([]RuntimeDiagnostic, 0, 64)
	otherDiagnostics := make([]RuntimeDiagnostic, 0, 436)
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		lower := strings.ToLower(line)
		severity := runtimeSeverity(line, lower)
		relevant := containsAny(lower, relevanceTerms)
		if relevant {
			report.RelevantHits++
		}
		if severity == "error" {
			report.Errors++
		} else if severity == "warning" {
			report.Warnings++
		}
		if severity != "info" || relevant {
			diagnostic := RuntimeDiagnostic{Severity: severity, Component: logComponent(line), Message: line, Line: lineNumber, Relevant: relevant}
			if relevant && len(relevantDiagnostics) < 500 {
				relevantDiagnostics = append(relevantDiagnostics, diagnostic)
			} else if !relevant && len(otherDiagnostics) < 500 {
				otherDiagnostics = append(otherDiagnostics, diagnostic)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return report, err
	}
	report.Diagnostics = append(report.Diagnostics, relevantDiagnostics...)
	remaining := 500 - len(report.Diagnostics)
	if remaining > 0 {
		report.Diagnostics = append(report.Diagnostics, otherDiagnostics[:min(remaining, len(otherDiagnostics))]...)
	}
	_ = service.store.AppendEvent(ctx, workspace.EntityID, "runtime_analyzed", map[string]any{"workspaceId": workspaceID, "logPath": logPath, "errors": report.Errors, "warnings": report.Warnings, "relevantHits": report.RelevantHits})
	return report, nil
}

func (service *AppService) findRuntimeLog() string {
	preferred := filepath.Join(service.config.BeamNGRoot, "current", "beamng.log")
	if info, err := os.Stat(preferred); err == nil && !info.IsDir() {
		return preferred
	}
	var latest string
	var latestTime time.Time
	_ = filepath.WalkDir(service.config.BeamNGRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if path != service.config.BeamNGRoot {
				name := strings.ToLower(entry.Name())
				if name == "backups" || name == "temp" || name == "modlibrary" || name == "modmanager" {
					return filepath.SkipDir
				}
				relative, _ := filepath.Rel(service.config.BeamNGRoot, path)
				if len(strings.Split(filepath.Clean(relative), string(filepath.Separator))) > 4 {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.EqualFold(entry.Name(), "beamng.log") {
			return nil
		}
		if info, err := entry.Info(); err == nil && info.ModTime().After(latestTime) {
			latest = path
			latestTime = info.ModTime()
		}
		return nil
	})
	return latest
}

func copyFileAtomic(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".modstudio-install-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := io.Copy(temporary, input); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	keep = true
	return nil
}

func sanitizeArchiveLabel(value string) string {
	value = strings.TrimSpace(strings.TrimSuffix(value, filepath.Ext(value)))
	value = safeArchiveLabel.ReplaceAllString(value, "-")
	value = strings.Trim(value, ".-_")
	if len(value) > 64 {
		value = strings.TrimRight(value[:64], ".-_")
	}
	return value
}

func safeTestInstallPath(path, activeModsDir string) bool {
	return pathWithin(path, activeModsDir) && strings.HasPrefix(strings.ToLower(filepath.Base(path)), "modstudio-test-") && strings.EqualFold(filepath.Ext(path), ".zip")
}

func runtimeSeverity(line, lower string) string {
	if strings.Contains(line, "|E|") {
		return "error"
	}
	if strings.Contains(line, "|W|") {
		return "warning"
	}
	if strings.Contains(line, "|D|") || strings.Contains(line, "|I|") || strings.Contains(line, "|T|") {
		return "info"
	}
	if strings.Contains(lower, "exception") || strings.Contains(lower, "stack traceback") || strings.Contains(lower, "failed to") || strings.Contains(lower, "unable to") {
		return "error"
	}
	if strings.Contains(lower, " warning") || strings.HasPrefix(lower, "warning") {
		return "warning"
	}
	return "info"
}

func logComponent(line string) string {
	parts := strings.SplitN(line, "|", 4)
	if len(parts) >= 3 {
		return strings.TrimSpace(parts[2])
	}
	return ""
}

func containsAny(value string, terms []string) bool {
	for _, term := range terms {
		if term != "" && strings.Contains(value, term) {
			return true
		}
	}
	return false
}
