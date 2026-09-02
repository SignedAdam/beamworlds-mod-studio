package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

const (
	VirusScanModeSignature = "signature"
	VirusScanModeFull      = "full"

	VirusScanStageSignature = "signature"
	VirusScanStageTriage    = "ai_triage"
	VirusScanStageDeep      = "deep_analysis"
)

type VirusScanProgress struct {
	ScanID     string `json:"scanId"`
	EntityID   string `json:"entityId"`
	Mode       string `json:"mode"`
	Status     string `json:"status"`
	Stage      string `json:"stage"`
	StageIndex int    `json:"stageIndex"`
	StageTotal int    `json:"stageTotal"`
	Message    string `json:"message"`
	Error      string `json:"error"`
}

type VirusScanStage struct {
	ID           string            `json:"id"`
	ScanID       string            `json:"scanId"`
	EntityID     string            `json:"entityId"`
	ArtifactID   string            `json:"artifactId"`
	Stage        string            `json:"stage"`
	Status       string            `json:"status"`
	CreatedAt    string            `json:"createdAt"`
	CompletedAt  string            `json:"completedAt"`
	Parameters   map[string]string `json:"parameters"`
	Inputs       []string          `json:"inputs"`
	MetadataFile string            `json:"metadataFile"`
	AuditID      string            `json:"auditId"`
	Summary      string            `json:"summary"`
	Error        string            `json:"error"`
}

type VirusScanRun struct {
	ID           string           `json:"id"`
	EntityID     string           `json:"entityId"`
	ArtifactID   string           `json:"artifactId"`
	Mode         string           `json:"mode"`
	Status       string           `json:"status"`
	CurrentStage string           `json:"currentStage"`
	Verdict      string           `json:"verdict"`
	CreatedAt    string           `json:"createdAt"`
	UpdatedAt    string           `json:"updatedAt"`
	Error        string           `json:"error"`
	Stages       []VirusScanStage `json:"stages"`
}

type VirusScanStageReference struct {
	ID           string   `json:"id"`
	ScanID       string   `json:"scanId"`
	ArtifactID   string   `json:"artifactId"`
	Stage        string   `json:"stage"`
	CreatedAt    string   `json:"createdAt"`
	MetadataFile string   `json:"metadataFile"`
	Inputs       []string `json:"inputs"`
}

type ModSecurityMetadata struct {
	SchemaVersion int                       `json:"schemaVersion"`
	EntityID      string                    `json:"entityId"`
	UpdatedAt     string                    `json:"updatedAt"`
	SecurityScans []VirusScanStageReference `json:"securityScans"`
}

type VirusScanStageDocument struct {
	SchemaVersion int            `json:"schemaVersion"`
	Stage         VirusScanStage `json:"stage"`
	Result        ModAudit       `json:"result"`
}

func (service *AppService) RunVirusScan(entityID, mode string) (VirusScanRun, error) {
	service.virusMu.Lock()
	defer service.virusMu.Unlock()

	ctx := context.Background()
	entityID = strings.TrimSpace(entityID)
	mode = normalizeVirusScanMode(mode)
	if mode == "" {
		return VirusScanRun{}, errors.New("virus scan mode must be signature or full")
	}
	item, err := service.store.GetLibraryItem(ctx, entityID)
	if err != nil {
		return VirusScanRun{}, err
	}
	if !item.Linked || strings.TrimSpace(item.ArchivePath) == "" {
		return VirusScanRun{}, errors.New("virus scan requires an available source archive")
	}
	scanID, err := modkit.NewID()
	if err != nil {
		return VirusScanRun{}, err
	}
	now := nowUTC()
	run := VirusScanRun{
		ID: scanID, EntityID: item.EntityID, ArtifactID: item.ArtifactID, Mode: mode,
		Status: "running", CurrentStage: VirusScanStageSignature, CreatedAt: now, UpdatedAt: now,
		Stages: []VirusScanStage{},
	}
	if err := service.store.createVirusScan(ctx, run); err != nil {
		return VirusScanRun{}, err
	}
	total := 1
	if mode == VirusScanModeFull {
		total = 3
	}
	service.emitVirusScan(VirusScanProgress{ScanID: run.ID, EntityID: item.EntityID, Mode: mode, Status: "running", Stage: VirusScanStageSignature, StageIndex: 1, StageTotal: total, Message: "Checking signatures and archive safety"})

	signature, err := service.beginVirusScanStage(ctx, run, VirusScanStageSignature, map[string]string{
		"scanner": "BeamWorlds signature engine", "analyzerVersion": modkit.AnalyzerVersion,
		"entryReadLimitBytes": fmt.Sprint(maxAuditEntryRead), "archiveReadLimitBytes": fmt.Sprint(maxAuditTotalRead),
	}, nil)
	if err != nil {
		return service.failVirusScan(ctx, run, VirusScanStageSignature, err)
	}
	audit, scanErr := service.runModAuditLocal(item.EntityID)
	if scanErr != nil {
		_ = service.finishVirusScanStage(ctx, signature, ModAudit{}, "", scanErr)
		return service.failVirusScan(ctx, run, VirusScanStageSignature, scanErr)
	}
	signatureSummary := fmt.Sprintf("%d archive entries checked; %d security signals", audit.Deterministic.ScannedEntries, len(audit.Deterministic.Signals))
	if err := service.finishVirusScanStage(ctx, signature, audit, signatureSummary, nil); err != nil {
		return service.failVirusScan(ctx, run, VirusScanStageSignature, err)
	}
	if mode == VirusScanModeSignature {
		return service.completeVirusScan(ctx, run, VirusScanStageSignature, virusScanVerdict(item, audit))
	}

	latestSignature, err := service.latestVirusScanStageDocument(ctx, item.EntityID, item.ArtifactID, VirusScanStageSignature)
	if err != nil {
		return service.failVirusScan(ctx, run, VirusScanStageTriage, fmt.Errorf("load latest signature metadata: %w", err))
	}
	if latestSignature.Result.ID == "" || latestSignature.Result.ID != audit.ID {
		return service.failVirusScan(ctx, run, VirusScanStageTriage, errors.New("latest signature metadata does not reference the active audit"))
	}
	settings, err := service.Settings()
	if err != nil {
		return service.failVirusScan(ctx, run, VirusScanStageTriage, err)
	}
	triage, err := service.beginVirusScanStage(ctx, run, VirusScanStageTriage, map[string]string{
		"model": settings.PreScanModel, "reasoning": settings.PreScanReasoning, "files": fmt.Sprint(audit.Deterministic.ScannedEntries), "sourceAuditId": latestSignature.Result.ID,
	}, []string{latestSignature.Stage.ID})
	if err != nil {
		return service.failVirusScan(ctx, run, VirusScanStageTriage, err)
	}
	service.emitVirusScan(VirusScanProgress{ScanID: run.ID, EntityID: item.EntityID, Mode: mode, Status: "running", Stage: VirusScanStageTriage, StageIndex: 2, StageTotal: total, Message: "Reading every file before Virgil review"})
	fileCount, err := service.expandFullVirusScanArtifacts(ctx, audit.ID, item.ArchivePath)
	if err != nil {
		_ = service.finishVirusScanStage(ctx, triage, audit, "", err)
		return service.failVirusScan(ctx, run, VirusScanStageTriage, err)
	}
	service.emitVirusScan(VirusScanProgress{ScanID: run.ID, EntityID: item.EntityID, Mode: mode, Status: "running", Stage: VirusScanStageTriage, StageIndex: 2, StageTotal: total, Message: fmt.Sprintf("Virgil is reviewing %d files", fileCount)})
	audit, scanErr = service.runModAuditPreScan(item.EntityID, latestSignature.Result)
	if scanErr == nil && audit.Status == "failed" {
		message := strings.TrimSpace(audit.Error)
		if message == "" {
			message = "Virgil file review failed without an error message"
		}
		scanErr = errors.New(message)
	}
	if scanErr == nil && audit.ID != latestSignature.Result.ID {
		scanErr = errors.New("AI file review did not continue from the latest signature metadata")
	}
	if scanErr != nil {
		_ = service.finishVirusScanStage(ctx, triage, audit, "", scanErr)
		return service.failVirusScan(ctx, run, VirusScanStageTriage, scanErr)
	}
	if err := service.finishVirusScanStage(ctx, triage, audit, audit.PreScan.Summary, nil); err != nil {
		return service.failVirusScan(ctx, run, VirusScanStageTriage, err)
	}

	latestSignature, err = service.latestVirusScanStageDocument(ctx, item.EntityID, item.ArtifactID, VirusScanStageSignature)
	if err != nil {
		return service.failVirusScan(ctx, run, VirusScanStageDeep, fmt.Errorf("reload latest signature metadata: %w", err))
	}
	latestTriage, err := service.latestVirusScanStageDocument(ctx, item.EntityID, item.ArtifactID, VirusScanStageTriage)
	if err != nil {
		return service.failVirusScan(ctx, run, VirusScanStageDeep, fmt.Errorf("load latest AI review metadata: %w", err))
	}
	if latestSignature.Result.ID == "" || latestSignature.Result.ID != latestTriage.Result.ID {
		return service.failVirusScan(ctx, run, VirusScanStageDeep, errors.New("latest scan stages do not share the same source audit"))
	}
	service.emitVirusScan(VirusScanProgress{ScanID: run.ID, EntityID: item.EntityID, Mode: mode, Status: "running", Stage: VirusScanStageDeep, StageIndex: 3, StageTotal: total, Message: "Virgil is producing the final security assessment"})
	deep, err := service.beginVirusScanStage(ctx, run, VirusScanStageDeep, map[string]string{
		"model": settings.FullScanModel, "reasoning": settings.FullScanReasoning, "sourceAuditId": latestTriage.Result.ID,
	}, []string{latestSignature.Stage.ID, latestTriage.Stage.ID})
	if err != nil {
		return service.failVirusScan(ctx, run, VirusScanStageDeep, err)
	}
	audit, scanErr = service.runModAuditFull(item.EntityID, latestTriage.Result)
	if scanErr == nil && audit.Status == "failed" {
		message := strings.TrimSpace(audit.Error)
		if message == "" {
			message = "Virgil final assessment failed without an error message"
		}
		scanErr = errors.New(message)
	}
	if scanErr == nil && audit.ID != latestTriage.Result.ID {
		scanErr = errors.New("final assessment did not continue from the latest prior stages")
	}
	if scanErr != nil {
		_ = service.finishVirusScanStage(ctx, deep, audit, "", scanErr)
		return service.failVirusScan(ctx, run, VirusScanStageDeep, scanErr)
	}
	if err := service.finishVirusScanStage(ctx, deep, audit, audit.Final.Summary, nil); err != nil {
		return service.failVirusScan(ctx, run, VirusScanStageDeep, err)
	}
	return service.completeVirusScan(ctx, run, VirusScanStageDeep, virusScanVerdict(item, audit))
}

func (service *AppService) ListVirusScans(entityID string) ([]VirusScanRun, error) {
	return service.store.listVirusScans(context.Background(), strings.TrimSpace(entityID))
}

func (service *AppService) VirusScanMetadata(entityID string) (ModSecurityMetadata, error) {
	return service.loadModSecurityMetadata(strings.TrimSpace(entityID))
}

func normalizeVirusScanMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case VirusScanModeSignature, "quick", "local":
		return VirusScanModeSignature
	case VirusScanModeFull, "virgil", "deep":
		return VirusScanModeFull
	default:
		return ""
	}
}

func (service *AppService) emitVirusScan(progress VirusScanProgress) {
	if service.emit != nil {
		service.emit("virus:scan", progress)
	}
}

func (service *AppService) beginVirusScanStage(ctx context.Context, run VirusScanRun, stageName string, parameters map[string]string, inputs []string) (VirusScanStage, error) {
	id, err := modkit.NewID()
	if err != nil {
		return VirusScanStage{}, err
	}
	createdAt := nowUTC()
	stage := VirusScanStage{
		ID: id, ScanID: run.ID, EntityID: run.EntityID, ArtifactID: run.ArtifactID,
		Stage: stageName, Status: "running", CreatedAt: createdAt, Parameters: parameters,
		Inputs: append([]string(nil), inputs...), MetadataFile: service.virusScanStagePath(run.EntityID, id),
	}
	if stage.Parameters == nil {
		stage.Parameters = map[string]string{}
	}
	if stage.Inputs == nil {
		stage.Inputs = []string{}
	}
	if err := service.writeVirusScanStageDocument(stage, ModAudit{}); err != nil {
		return VirusScanStage{}, err
	}
	if err := service.appendModSecurityReference(stage); err != nil {
		return VirusScanStage{}, err
	}
	if err := service.store.createVirusScanStage(ctx, stage); err != nil {
		return VirusScanStage{}, err
	}
	if err := service.store.updateVirusScan(ctx, run.ID, "running", stageName, "", ""); err != nil {
		return VirusScanStage{}, err
	}
	return stage, nil
}

func (service *AppService) finishVirusScanStage(ctx context.Context, stage VirusScanStage, audit ModAudit, summary string, stageErr error) error {
	stage.CompletedAt = nowUTC()
	stage.AuditID = audit.ID
	stage.Summary = trimAuditString(summary, 4000)
	stage.Status = "complete"
	if stageErr != nil {
		stage.Status = "failed"
		stage.Error = trimAuditString(stageErr.Error(), 4000)
	}
	if err := service.writeVirusScanStageDocument(stage, audit); err != nil {
		return err
	}
	return service.store.finishVirusScanStage(ctx, stage)
}

func (service *AppService) completeVirusScan(ctx context.Context, run VirusScanRun, stage, verdict string) (VirusScanRun, error) {
	if err := service.store.updateVirusScan(ctx, run.ID, "complete", stage, verdict, ""); err != nil {
		return VirusScanRun{}, err
	}
	_ = service.store.AppendEvent(ctx, run.EntityID, "virus_scan_complete", map[string]any{"scanId": run.ID, "mode": run.Mode, "verdict": verdict})
	stageTotal := 1
	if run.Mode == VirusScanModeFull {
		stageTotal = 3
	}
	service.emitVirusScan(VirusScanProgress{ScanID: run.ID, EntityID: run.EntityID, Mode: run.Mode, Status: "complete", Stage: stage, StageIndex: stageTotal, StageTotal: stageTotal, Message: virusHealthLabel(verdict)})
	return service.store.virusScanByID(ctx, run.ID)
}

func (service *AppService) failVirusScan(ctx context.Context, run VirusScanRun, stage string, scanErr error) (VirusScanRun, error) {
	message := "virus scan failed"
	if scanErr != nil {
		message = trimAuditString(scanErr.Error(), 4000)
	}
	_ = service.store.updateVirusScan(ctx, run.ID, "failed", stage, "scan_failed", message)
	_ = service.store.AppendEvent(ctx, run.EntityID, "virus_scan_failed", map[string]any{"scanId": run.ID, "stage": stage})
	service.emitVirusScan(VirusScanProgress{ScanID: run.ID, EntityID: run.EntityID, Mode: run.Mode, Status: "failed", Stage: stage, Message: "Scan failed", Error: message})
	failed, loadErr := service.store.virusScanByID(ctx, run.ID)
	if loadErr != nil {
		return VirusScanRun{}, scanErr
	}
	return failed, scanErr
}

func virusScanVerdict(item LibraryItem, audit ModAudit) string {
	maxSeverity := 0
	for _, signal := range audit.Deterministic.Signals {
		maxSeverity = max(maxSeverity, auditSeverityRank(signal.Severity))
	}
	for _, finding := range append(append([]ModAuditFinding(nil), audit.Final.Findings...), audit.Final.ContentSignals...) {
		maxSeverity = max(maxSeverity, auditSeverityRank(finding.Severity))
	}
	risk := strings.ToLower(strings.TrimSpace(audit.Final.OverallRisk))
	if maxSeverity >= auditSeverityRank("high") || risk == "high" || risk == "critical" {
		return "threat"
	}
	if libraryItemBroken(item) {
		return "broken"
	}
	if maxSeverity >= auditSeverityRank("low") || risk == "moderate" || risk == "unknown" {
		return "review"
	}
	return "safe"
}

func libraryItemBroken(item LibraryItem) bool {
	for _, issue := range item.Manifest.Issues {
		if strings.EqualFold(fmt.Sprint(issue.Severity), "error") {
			return true
		}
	}
	return false
}

func virusHealthLabel(status string) string {
	switch status {
	case "safe":
		return "Safe"
	case "review":
		return "Review needed"
	case "threat":
		return "Threat found"
	case "broken":
		return "Broken"
	case "scanning":
		return "Scanning"
	case "scan_failed":
		return "Scan failed"
	default:
		return "Not scanned"
	}
}

func (service *AppService) virusMetadataDirectory(entityID string) string {
	return filepath.Join(service.config.DataDir, "metadata", "mods", safeMetadataComponent(entityID))
}

func (service *AppService) virusScanStagePath(entityID, stageID string) string {
	return filepath.Join(service.virusMetadataDirectory(entityID), "scans", safeMetadataComponent(stageID)+".json")
}

func safeMetadataComponent(value string) string {
	value = strings.TrimSpace(value)
	var result strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			result.WriteRune(char)
		} else {
			result.WriteByte('_')
		}
	}
	if result.Len() == 0 {
		return "unknown"
	}
	return result.String()
}

func (service *AppService) writeVirusScanStageDocument(stage VirusScanStage, result ModAudit) error {
	document := VirusScanStageDocument{SchemaVersion: 1, Stage: stage, Result: result}
	return writeIndentedJSON(stage.MetadataFile, document)
}

func writeIndentedJSON(filename string, value any) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return os.WriteFile(filename, encoded, 0o600)
}

func (service *AppService) loadModSecurityMetadata(entityID string) (ModSecurityMetadata, error) {
	metadata := ModSecurityMetadata{SchemaVersion: 1, EntityID: entityID, SecurityScans: []VirusScanStageReference{}}
	filename := filepath.Join(service.virusMetadataDirectory(entityID), "mod.json")
	data, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return metadata, nil
	}
	if err != nil {
		return ModSecurityMetadata{}, err
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return ModSecurityMetadata{}, fmt.Errorf("decode mod security metadata: %w", err)
	}
	if metadata.SecurityScans == nil {
		metadata.SecurityScans = []VirusScanStageReference{}
	}
	return metadata, nil
}

func (service *AppService) appendModSecurityReference(stage VirusScanStage) error {
	metadata, err := service.loadModSecurityMetadata(stage.EntityID)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(service.virusMetadataDirectory(stage.EntityID), stage.MetadataFile)
	if err != nil {
		return err
	}
	for _, reference := range metadata.SecurityScans {
		if reference.ID == stage.ID {
			return nil
		}
	}
	metadata.UpdatedAt = nowUTC()
	metadata.SecurityScans = append(metadata.SecurityScans, VirusScanStageReference{
		ID: stage.ID, ScanID: stage.ScanID, ArtifactID: stage.ArtifactID, Stage: stage.Stage,
		CreatedAt: stage.CreatedAt, MetadataFile: filepath.ToSlash(relative), Inputs: append([]string(nil), stage.Inputs...),
	})
	return writeIndentedJSON(filepath.Join(service.virusMetadataDirectory(stage.EntityID), "mod.json"), metadata)
}

func (service *AppService) latestVirusScanStageDocument(ctx context.Context, entityID, artifactID, stageName string) (VirusScanStageDocument, error) {
	stage, err := service.store.latestVirusScanStage(ctx, entityID, artifactID, stageName)
	if err != nil {
		return VirusScanStageDocument{}, err
	}
	data, err := os.ReadFile(stage.MetadataFile)
	if err != nil {
		return VirusScanStageDocument{}, err
	}
	var document VirusScanStageDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return VirusScanStageDocument{}, err
	}
	if document.Stage.ID != stage.ID {
		return VirusScanStageDocument{}, errors.New("virus scan metadata stage ID mismatch")
	}
	return document, nil
}

func (service *AppService) expandFullVirusScanArtifacts(ctx context.Context, auditID, archivePath string) (int, error) {
	existing, err := service.store.modAuditArtifactRecords(ctx, auditID)
	if err != nil {
		return 0, err
	}
	byPath := make(map[string]modAuditArtifactRecord, len(existing))
	for _, record := range existing {
		byPath[strings.ToLower(normalizeAuditPath(record.Path))] = record
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return 0, fmt.Errorf("open source archive for full virus scan: %w", err)
	}
	defer reader.Close()
	seen := make(map[string]bool, len(reader.File))
	all := make([]modAuditArtifactRecord, 0, len(reader.File))
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		path := normalizeAuditPath(file.Name)
		key := strings.ToLower(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		record, exists := byPath[key]
		if !exists {
			entryType, _ := classifyAuditEntrypoint(path)
			record = modAuditArtifactRecord{ModAuditFileArtifact: ModAuditFileArtifact{
				Path: path, Fingerprint: fmt.Sprintf("%08x:%d", file.CRC32, file.UncompressedSize64),
				SizeBytes: int64(file.UncompressedSize64), EntrypointType: entryType,
				MediaType: auditImageMediaTypes[strings.ToLower(filepath.Ext(path))], Signals: []ModAuditSignal{},
			}}
		}
		if record.Excerpt == "" {
			data, readErr := readAuditZipPrefix(file, maxAuditEntryRead)
			if readErr != nil {
				record.Signals = dedupeAuditSignals(append(record.Signals, ModAuditSignal{Severity: "medium", Category: "archive", Code: "entry_read_failed", Path: path, Detail: "Could not read this file during the full scan", Evidence: trimAuditString(readErr.Error(), 240)}))
			} else if isProbablyAuditText(data) {
				record.Excerpt = sanitizeAuditExcerpt(data)
			}
		}
		all = append(all, record)
	}
	sort.Slice(all, func(left, right int) bool { return all[left].Path < all[right].Path })
	if err := service.store.upsertModAuditArtifacts(ctx, auditID, all); err != nil {
		return 0, err
	}
	return len(all), nil
}

func (store *Store) upsertModAuditArtifacts(ctx context.Context, auditID string, records []modAuditArtifactRecord) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, record := range records {
		signals, err := json.Marshal(record.Signals)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO mod_audit_files(audit_id,path,fingerprint,size_bytes,entrypoint_type,signals_json,excerpt)
			VALUES(?,?,?,?,?,?,?) ON CONFLICT(audit_id,path) DO UPDATE SET fingerprint=excluded.fingerprint,size_bytes=excluded.size_bytes,
			entrypoint_type=excluded.entrypoint_type,signals_json=excluded.signals_json,excerpt=CASE WHEN excluded.excerpt<>'' THEN excluded.excerpt ELSE mod_audit_files.excerpt END`,
			auditID, record.Path, record.Fingerprint, record.SizeBytes, record.EntrypointType, string(signals), record.Excerpt)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (store *Store) createVirusScan(ctx context.Context, run VirusScanRun) error {
	_, err := store.db.ExecContext(ctx, `INSERT INTO virus_scans(id,entity_id,artifact_id,mode,status,current_stage,verdict,created_at,updated_at,error) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.EntityID, run.ArtifactID, run.Mode, run.Status, run.CurrentStage, run.Verdict, run.CreatedAt, run.UpdatedAt, run.Error)
	return err
}

func (store *Store) updateVirusScan(ctx context.Context, scanID, status, stage, verdict, scanError string) error {
	_, err := store.db.ExecContext(ctx, `UPDATE virus_scans SET status=?,current_stage=?,verdict=?,updated_at=?,error=? WHERE id=?`, status, stage, verdict, nowUTC(), trimAuditString(scanError, 4000), scanID)
	return err
}

func (store *Store) createVirusScanStage(ctx context.Context, stage VirusScanStage) error {
	parameters, err := json.Marshal(stage.Parameters)
	if err != nil {
		return err
	}
	inputs, err := json.Marshal(stage.Inputs)
	if err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO virus_scan_stages(id,scan_id,entity_id,artifact_id,stage,status,created_at,completed_at,parameters_json,inputs_json,metadata_file,audit_id,summary,error) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		stage.ID, stage.ScanID, stage.EntityID, stage.ArtifactID, stage.Stage, stage.Status, stage.CreatedAt, stage.CompletedAt, string(parameters), string(inputs), stage.MetadataFile, stage.AuditID, stage.Summary, stage.Error)
	return err
}

func (store *Store) finishVirusScanStage(ctx context.Context, stage VirusScanStage) error {
	_, err := store.db.ExecContext(ctx, `UPDATE virus_scan_stages SET status=?,completed_at=?,metadata_file=?,audit_id=?,summary=?,error=? WHERE id=?`,
		stage.Status, stage.CompletedAt, stage.MetadataFile, stage.AuditID, stage.Summary, stage.Error, stage.ID)
	return err
}

func (store *Store) latestVirusScanStage(ctx context.Context, entityID, artifactID, stageName string) (VirusScanStage, error) {
	row := store.db.QueryRowContext(ctx, `SELECT id,scan_id,entity_id,artifact_id,stage,status,created_at,completed_at,parameters_json,inputs_json,metadata_file,audit_id,summary,error
		FROM virus_scan_stages WHERE entity_id=? AND artifact_id=? AND stage=? AND status='complete' ORDER BY completed_at DESC,id DESC LIMIT 1`, entityID, artifactID, stageName)
	return scanVirusScanStage(row)
}

func (store *Store) virusScanByID(ctx context.Context, scanID string) (VirusScanRun, error) {
	var run VirusScanRun
	err := store.db.QueryRowContext(ctx, `SELECT id,entity_id,artifact_id,mode,status,current_stage,verdict,created_at,updated_at,error FROM virus_scans WHERE id=?`, scanID).
		Scan(&run.ID, &run.EntityID, &run.ArtifactID, &run.Mode, &run.Status, &run.CurrentStage, &run.Verdict, &run.CreatedAt, &run.UpdatedAt, &run.Error)
	if err != nil {
		return VirusScanRun{}, err
	}
	stages, err := store.virusScanStages(ctx, run.ID)
	if err != nil {
		return VirusScanRun{}, err
	}
	run.Stages = stages
	return run, nil
}

func (store *Store) listVirusScans(ctx context.Context, entityID string) ([]VirusScanRun, error) {
	query := `SELECT id,entity_id,artifact_id,mode,status,current_stage,verdict,created_at,updated_at,error FROM virus_scans`
	args := []any{}
	if entityID != "" {
		query += ` WHERE entity_id=?`
		args = append(args, entityID)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT 300`
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []VirusScanRun{}
	for rows.Next() {
		var run VirusScanRun
		if err := rows.Scan(&run.ID, &run.EntityID, &run.ArtifactID, &run.Mode, &run.Status, &run.CurrentStage, &run.Verdict, &run.CreatedAt, &run.UpdatedAt, &run.Error); err != nil {
			return nil, err
		}
		run.Stages = []VirusScanStage{}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return runs, nil
	}
	arguments := make([]any, len(runs))
	indices := make(map[string]int, len(runs))
	for index := range runs {
		arguments[index] = runs[index].ID
		indices[runs[index].ID] = index
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(runs)), ",")
	stageRows, err := store.db.QueryContext(ctx, `SELECT id,scan_id,entity_id,artifact_id,stage,status,created_at,completed_at,parameters_json,inputs_json,metadata_file,audit_id,summary,error
		FROM virus_scan_stages WHERE scan_id IN (`+placeholders+`) ORDER BY created_at,id`, arguments...)
	if err != nil {
		return nil, err
	}
	defer stageRows.Close()
	for stageRows.Next() {
		stage, err := scanVirusScanStage(stageRows)
		if err != nil {
			return nil, err
		}
		if index, ok := indices[stage.ScanID]; ok {
			runs[index].Stages = append(runs[index].Stages, stage)
		}
	}
	if err := stageRows.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}

func (store *Store) virusScanStages(ctx context.Context, scanID string) ([]VirusScanStage, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT id,scan_id,entity_id,artifact_id,stage,status,created_at,completed_at,parameters_json,inputs_json,metadata_file,audit_id,summary,error FROM virus_scan_stages WHERE scan_id=? ORDER BY created_at,id`, scanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stages := []VirusScanStage{}
	for rows.Next() {
		stage, err := scanVirusScanStage(rows)
		if err != nil {
			return nil, err
		}
		stages = append(stages, stage)
	}
	return stages, rows.Err()
}

type virusScanRow interface {
	Scan(...any) error
}

func scanVirusScanStage(row virusScanRow) (VirusScanStage, error) {
	var stage VirusScanStage
	var parameters, inputs string
	err := row.Scan(&stage.ID, &stage.ScanID, &stage.EntityID, &stage.ArtifactID, &stage.Stage, &stage.Status, &stage.CreatedAt, &stage.CompletedAt, &parameters, &inputs, &stage.MetadataFile, &stage.AuditID, &stage.Summary, &stage.Error)
	if err != nil {
		return VirusScanStage{}, err
	}
	stage.Parameters = map[string]string{}
	stage.Inputs = []string{}
	if parameters != "" {
		if err := json.Unmarshal([]byte(parameters), &stage.Parameters); err != nil {
			return VirusScanStage{}, err
		}
	}
	if inputs != "" {
		if err := json.Unmarshal([]byte(inputs), &stage.Inputs); err != nil {
			return VirusScanStage{}, err
		}
	}
	return stage, nil
}

func (store *Store) attachLibraryItemHealth(ctx context.Context, items []LibraryItem) error {
	for index := range items {
		items[index].HealthStatus = "unscanned"
		if libraryItemBroken(items[index]) {
			items[index].HealthStatus = "broken"
		}
		items[index].HealthLabel = virusHealthLabel(items[index].HealthStatus)
	}
	if len(items) == 0 {
		return nil
	}
	rows, err := store.db.QueryContext(ctx, `SELECT entity_id,artifact_id,status,verdict,updated_at FROM (
		SELECT entity_id,artifact_id,status,verdict,updated_at,id,ROW_NUMBER() OVER(PARTITION BY entity_id,artifact_id ORDER BY updated_at DESC,id DESC) AS ordinal FROM virus_scans
	) WHERE ordinal=1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type healthRecord struct{ status, verdict, updatedAt string }
	health := map[string]healthRecord{}
	for rows.Next() {
		var entityID, artifactID string
		var record healthRecord
		if err := rows.Scan(&entityID, &artifactID, &record.status, &record.verdict, &record.updatedAt); err != nil {
			return err
		}
		health[entityID+"\x00"+artifactID] = record
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range items {
		record, ok := health[items[index].EntityID+"\x00"+items[index].ArtifactID]
		if !ok {
			continue
		}
		status := record.verdict
		if record.status == "running" {
			status = "scanning"
		} else if record.status == "failed" {
			status = "scan_failed"
		}
		if status == "" {
			status = "unscanned"
		}
		if status != "threat" && libraryItemBroken(items[index]) {
			status = "broken"
		}
		items[index].HealthStatus = status
		items[index].HealthLabel = virusHealthLabel(status)
		items[index].LastSecurityScanAt = record.updatedAt
	}
	return nil
}
