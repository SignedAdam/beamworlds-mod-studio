package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSignatureVirusScansKeepEveryHistoricalArtifact(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	archivePath := writeModAuditFixtureAt(t, service.config.BeamNGRoot)
	item := insertModAuditFixture(t, service, archivePath)

	first, err := service.RunVirusScan(item.EntityID, VirusScanModeSignature)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.RunVirusScan(item.EntityID, VirusScanModeSignature)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || len(first.Stages) != 1 || len(second.Stages) != 1 {
		t.Fatalf("signature scan history = first %#v, second %#v", first, second)
	}
	for _, id := range []string{first.ID, first.Stages[0].ID, second.ID, second.Stages[0].ID} {
		if _, err := uuid.Parse(id); err != nil {
			t.Fatalf("scan identifier %q is not a UUID: %v", id, err)
		}
	}

	metadata, err := service.VirusScanMetadata(item.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.SecurityScans) != 2 {
		t.Fatalf("mod metadata retained %d security scan artifacts, want 2", len(metadata.SecurityScans))
	}
	for _, reference := range metadata.SecurityScans {
		filename := filepath.Join(service.virusMetadataDirectory(item.EntityID), filepath.FromSlash(reference.MetadataFile))
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatalf("read referenced stage metadata %s: %v", filename, err)
		}
		var document VirusScanStageDocument
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		if document.Stage.ID != reference.ID || document.Stage.CreatedAt == "" || document.Stage.CompletedAt == "" || document.Stage.Parameters["scanner"] == "" {
			t.Fatalf("incomplete signature stage metadata: %#v", document.Stage)
		}
	}

	filtered, err := service.ListLibrary("all", "all", "is:"+second.Verdict, "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].EntityID != item.EntityID || filtered[0].HealthStatus != second.Verdict || filtered[0].LastSecurityScanAt == "" {
		t.Fatalf("health status did not derive from latest signature scan: %#v", filtered)
	}
	directHealthFilter, err := service.ListLibrary(second.Verdict, "all", "", "all")
	if err != nil || len(directHealthFilter) != 1 || directHealthFilter[0].EntityID != item.EntityID {
		t.Fatalf("Library health parameter did not filter latest scan status: %#v, err %v", directHealthFilter, err)
	}
}

func TestFullVirusScanRunsEveryStageAndUsesMetadataLineage(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	archivePath := writeModAuditFixtureAt(t, service.config.BeamNGRoot)
	item := insertModAuditFixture(t, service, archivePath)
	preScanPrompts := []string{}
	service.auditAI = func(_ context.Context, request auditAIRequest) (string, error) {
		switch request.SystemPrompt {
		case preScanSystemPrompt:
			preScanPrompts = append(preScanPrompts, request.Prompt)
			return `{"summary":"Every archived file was reviewed in batches.","files":[{"path":"lua/ge/extensions/audit.lua","observations":["Calls os.execute"],"behaviors":["Can launch a host process"],"followUp":[]}]}`, nil
		case fullAuditSystemPrompt:
			return `{"overallRisk":"high","summary":"The extension contains a host command execution path.","findings":[{"severity":"high","title":"Host command execution","path":"lua/ge/extensions/audit.lua","evidence":"The file calls os.execute","impact":"Can launch a host command","recommendation":"Remove the process launch"}],"contentSignals":[],"followUpPaths":[]}`, nil
		default:
			t.Fatalf("unexpected AI scan prompt: %q", request.SystemPrompt)
			return "", nil
		}
	}

	run, err := service.RunVirusScan(item.EntityID, VirusScanModeFull)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "complete" || run.Verdict != "threat" || len(run.Stages) != 3 {
		t.Fatalf("unexpected full virus scan: %#v", run)
	}
	signature, triage, deep := run.Stages[0], run.Stages[1], run.Stages[2]
	if signature.Stage != VirusScanStageSignature || triage.Stage != VirusScanStageTriage || deep.Stage != VirusScanStageDeep {
		t.Fatalf("stage order = %#v", run.Stages)
	}
	if len(signature.Inputs) != 0 || len(triage.Inputs) != 1 || triage.Inputs[0] != signature.ID || len(deep.Inputs) != 2 || deep.Inputs[0] != signature.ID || deep.Inputs[1] != triage.ID {
		t.Fatalf("stage lineage = signature %#v, triage %#v, deep %#v", signature.Inputs, triage.Inputs, deep.Inputs)
	}
	if signature.AuditID == "" || signature.AuditID != triage.AuditID || triage.AuditID != deep.AuditID {
		t.Fatalf("stages did not reuse one persisted audit: %#v", run.Stages)
	}
	for _, stage := range run.Stages {
		if _, err := uuid.Parse(stage.ID); err != nil {
			t.Fatalf("stage identifier %q is not a UUID: %v", stage.ID, err)
		}
		if stage.CreatedAt == "" || stage.CompletedAt == "" || stage.MetadataFile == "" || len(stage.Parameters) == 0 {
			t.Fatalf("stage metadata is incomplete: %#v", stage)
		}
		var document VirusScanStageDocument
		data, err := os.ReadFile(stage.MetadataFile)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		if document.Stage.ID != stage.ID || document.Result.ID != signature.AuditID {
			t.Fatalf("stage file does not preserve its analysis result: %#v", document)
		}
	}

	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	expectedFiles := 0
	for _, file := range reader.File {
		if !file.FileInfo().IsDir() {
			expectedFiles++
			supplied := false
			for _, prompt := range preScanPrompts {
				if strings.Contains(prompt, normalizeAuditPath(file.Name)) {
					supplied = true
					break
				}
			}
			if !supplied {
				t.Fatalf("full scan did not supply archive file %q to Virgil review", file.Name)
			}
		}
	}
	reader.Close()
	var persistedFiles int
	if err := service.store.db.QueryRow(`SELECT COUNT(*) FROM mod_audit_files WHERE audit_id=?`, signature.AuditID).Scan(&persistedFiles); err != nil {
		t.Fatal(err)
	}
	if persistedFiles != expectedFiles {
		t.Fatalf("full scan persisted %d file analyses, want all %d archive files", persistedFiles, expectedFiles)
	}

	metadata, err := service.VirusScanMetadata(item.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.SecurityScans) != 3 {
		t.Fatalf("mod metadata references %d stages, want 3", len(metadata.SecurityScans))
	}
}
