package main

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

func TestModAuditLocalScanMapsExecutableAndScriptAttackSurface(t *testing.T) {
	t.Parallel()
	archivePath := writeModAuditFixture(t)
	baseline := auditBaseline{mods: 100, patterns: map[string]int{"game-extension": 42, "ui-app": 18}}

	report, surface, artifacts, err := scanArchiveForModAudit(archivePath, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if report.ScannedEntries != 7 || report.ExecutableFiles < 2 || report.SuspiciousFiles < 3 {
		t.Fatalf("unexpected local scan summary: %#v", report)
	}
	for _, code := range []string{"host_executable", "disguised_pe", "image_type_mismatch", "process_launch", "network_access", "dynamic_code", "long_encoded_blob", "hateful_content_signal", "unsafe_archive_path"} {
		if !hasAuditSignal(report.Signals, code) {
			t.Errorf("local scan did not report %s: %#v", code, report.Signals)
		}
	}
	if len(artifacts) < 4 {
		t.Fatalf("persisted candidate artifacts = %d, want at least 4", len(artifacts))
	}
	var imageArtifact *modAuditArtifactRecord
	for index := range artifacts {
		if artifacts[index].Path == "art/bad.png" {
			imageArtifact = &artifacts[index]
			break
		}
	}
	if imageArtifact == nil || imageArtifact.MediaType != "image/png" {
		t.Fatalf("raster image was not retained for visual review: %#v", imageArtifact)
	}
	var extension, executable *ModAuditAttackSurfaceEntry
	for index := range surface.Entries {
		entry := &surface.Entries[index]
		switch entry.Type {
		case "game-extension":
			extension = entry
		case "host-executable":
			executable = entry
		}
	}
	if extension == nil || extension.LibraryOccurrences != 42 || extension.Novel {
		t.Fatalf("game-extension baseline was not learned: %#v", extension)
	}
	if executable == nil || !executable.Novel || executable.LibraryMods != 100 {
		t.Fatalf("novel executable surface was not identified: %#v", executable)
	}
}

func TestModAuditLocalScanFlagsDuplicateNormalizedPaths(t *testing.T) {
	t.Parallel()
	archivePath := filepath.Join(t.TempDir(), "duplicate-paths.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, entry := range []struct {
		path    string
		content string
	}{
		{path: "lua/ge/extensions/duplicate.lua", content: "return true\n"},
		{path: "LUA/GE/EXTENSIONS/DUPLICATE.LUA", content: "os.execute(command)\n"},
	} {
		member, createErr := writer.Create(entry.path)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := member.Write([]byte(entry.content)); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	report, _, artifacts, err := scanArchiveForModAudit(archivePath, auditBaseline{patterns: map[string]int{}})
	if err != nil {
		t.Fatal(err)
	}
	if report.ScannedEntries != 2 || !hasAuditSignal(report.Signals, "duplicate_archive_path") {
		t.Fatalf("duplicate path was not surfaced: %#v", report)
	}
	if len(artifacts) != 1 || !strings.Contains(artifacts[0].Excerpt, "os.execute") {
		t.Fatalf("duplicate candidates were not safely merged: %#v", artifacts)
	}
}

func TestModAuditFocusedReadUsesRequestedLargerExcerpt(t *testing.T) {
	t.Parallel()
	archivePath := filepath.Join(t.TempDir(), "focused-excerpt.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	member, err := writer.Create("lua/ge/extensions/large.lua")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write([]byte(strings.Repeat("local value = 'evidence'\n", 3000))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	files, err := readFocusedAuditFiles(archivePath, []string{"lua/ge/extensions/large.lua"}, 128<<10, 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || len(files[0].Excerpt) <= maxAuditExcerpt {
		t.Fatalf("focused excerpt length = %d, want more than pre-scan limit %d", len(files[0].Excerpt), maxAuditExcerpt)
	}
}

func TestModAuditPreScanBatchesEveryCandidate(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	artifacts := make([]modAuditArtifactRecord, 205)
	for index := range artifacts {
		artifacts[index].Path = "lua/ge/extensions/candidate-" + strconv.Itoa(index) + ".lua"
		artifacts[index].EntrypointType = "game-extension"
	}
	calls, supplied := 0, 0
	service.auditAI = func(_ context.Context, request auditAIRequest) (string, error) {
		calls++
		var payload struct {
			Files []auditPromptFile `json:"files"`
		}
		if err := json.Unmarshal([]byte(request.Prompt), &payload); err != nil {
			t.Fatal(err)
		}
		supplied += len(payload.Files)
		return `{"summary":"batch inspected","files":[]}`, nil
	}
	report, err := service.runModAuditPreScanBatches(context.Background(), ModAudit{}, artifacts, defaultAppSettings())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || supplied != len(artifacts) || report.Model != "" {
		t.Fatalf("pre-scan batches calls=%d supplied=%d report=%#v", calls, supplied, report)
	}
}

func TestModAuditStagesPersistAndReuseFileArtifacts(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	archivePath := writeModAuditFixtureAt(t, service.config.BeamNGRoot)
	item := insertModAuditFixture(t, service, archivePath)

	preCalls, fullCalls, followUpCalls := 0, 0, 0
	visualTempPaths := []string{}
	service.auditAI = func(_ context.Context, request auditAIRequest) (string, error) {
		switch request.SystemPrompt {
		case preScanSystemPrompt:
			preCalls++
			if request.Model != "" || request.Reasoning != "medium" {
				t.Fatalf("pre-scan selection = %s/%s", request.Model, request.Reasoning)
			}
			if strings.Contains(strings.ToLower(request.SystemPrompt), "verdict") == false {
				t.Fatal("pre-scan system prompt does not prohibit verdicts")
			}
			if len(request.Attachments) != 0 {
				t.Fatalf("cheap pre-scan received visual attachments: %#v", request.Attachments)
			}
			return `{"summary":"The extension launches a process and contacts a remote URL.","files":[{"path":"lua/ge/extensions/audit.lua","observations":["Calls os.execute and fetches an HTTPS URL"],"behaviors":["Can launch a host process"],"followUp":["Inspect the complete command construction"]}]}`, nil
		case fullAuditSystemPrompt:
			fullCalls++
			if request.Model != "" || request.Reasoning != "xhigh" {
				t.Fatalf("full-scan selection = %s/%s", request.Model, request.Reasoning)
			}
			if strings.Contains(request.Prompt, "focusedFiles") {
				if len(request.Attachments) != 0 {
					t.Fatalf("script-focused pass received unrelated images: %#v", request.Attachments)
				}
				return `{"overallRisk":"high","summary":"Focused inspection confirms an externally supplied command reaches os.execute and visual review found prohibited imagery.","findings":[{"severity":"high","title":"Command execution path","path":"lua/ge/extensions/audit.lua","evidence":"External input is concatenated into os.execute","impact":"Host command execution","recommendation":"Remove process launch and constrain input"},{"severity":"critical","title":"Invented path","path":"not/in/the/archive.exe","evidence":"Unsupported model claim","impact":"Unknown","recommendation":"None"}],"contentSignals":[{"severity":"high","title":"Hateful visual content","path":"art/bad.png","evidence":"The attached image visibly contains hateful material","impact":"Prohibited visual content","recommendation":"Remove the image"}],"followUpPaths":[]}`, nil
			}
			if len(request.Attachments) != 1 || !strings.Contains(request.Prompt, `"sourcePath": "art/bad.png"`) || !strings.Contains(request.Prompt, `"imageInventory"`) {
				t.Fatalf("full scan did not receive mapped visual evidence: attachments=%#v prompt=%s", request.Attachments, request.Prompt)
			}
			if _, err := os.Stat(request.Attachments[0]); err != nil {
				t.Fatalf("visual attachment was not readable during model call: %v", err)
			}
			visualTempPaths = append(visualTempPaths, request.Attachments[0])
			return `{"overallRisk":"high","summary":"Process launch requires focused inspection; the attached image contains hateful material.","findings":[],"contentSignals":[{"severity":"high","title":"Hateful visual content","path":"art/bad.png","evidence":"The attached image visibly contains hateful material","impact":"Prohibited visual content","recommendation":"Remove the image"}],"followUpPaths":["lua/ge/extensions/audit.lua"]}`, nil
		case followUpSystemPrompt:
			followUpCalls++
			if len(request.Attachments) > 0 {
				if len(request.Attachments) != 1 {
					t.Fatalf("image-focused follow-up attachments = %#v", request.Attachments)
				}
				if _, err := os.Stat(request.Attachments[0]); err != nil {
					t.Fatalf("focused visual attachment was not readable: %v", err)
				}
				visualTempPaths = append(visualTempPaths, request.Attachments[0])
				return "The attached image mapped to art/bad.png visibly contains hateful material.", nil
			}
			if len(request.Attachments) != 0 {
				t.Fatalf("text-only follow-up received visual attachments: %#v", request.Attachments)
			}
			return "The selected extension passes a constructed string to os.execute; no other file is needed for this answer.", nil
		default:
			t.Fatalf("unexpected audit system prompt: %q", request.SystemPrompt)
			return "", nil
		}
	}

	preScan, err := service.runModAuditPreScan(item.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if preScan.Status != "pre_scan_complete" || preScan.PreScan.Model != "" || len(preScan.PreScan.Files) != 1 {
		t.Fatalf("unexpected pre-scan: %#v", preScan)
	}
	var persistedFiles int
	if err := service.store.db.QueryRow(`SELECT COUNT(*) FROM mod_audit_files WHERE audit_id=? AND excerpt<>''`, preScan.ID).Scan(&persistedFiles); err != nil {
		t.Fatal(err)
	}
	if persistedFiles == 0 {
		t.Fatal("pre-scan did not persist reusable file excerpts")
	}

	full, err := service.runModAuditFull(item.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if full.Status != "complete" || full.Final.OverallRisk != "high" || len(full.Final.Findings) != 1 || len(full.Final.ContentSignals) != 1 || len(full.Final.FocusedPaths) != 1 {
		t.Fatalf("unexpected final Virus Scanner report: %#v", full.Final)
	}
	if !containsAuditWarning(full.Final.Warnings, "outside the persisted audit artifacts") || !containsAuditWarning(full.Final.Warnings, "Image signature did not match") {
		t.Fatalf("model-path or visual-attachment warnings were not preserved: %#v", full.Final.Warnings)
	}
	if preCalls != 1 || fullCalls != 2 {
		t.Fatalf("AI calls pre=%d full=%d, want persisted pre=1 and focused full=2", preCalls, fullCalls)
	}
	for _, path := range visualTempPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("initial visual attachment was not removed: %s, err=%v", path, err)
		}
	}

	followed, err := service.followUpModAudit(item.EntityID, []string{"lua/ge/extensions/audit.lua"}, "Can the command include user-controlled input?")
	if err != nil {
		t.Fatal(err)
	}
	if followUpCalls != 1 || len(followed.FollowUps) != 1 || !strings.Contains(followed.FollowUps[0].Response, "os.execute") {
		t.Fatalf("unexpected focused follow-up: %#v", followed.FollowUps)
	}

	imageFollowed, err := service.followUpModAudit(item.EntityID, []string{"art/bad.png"}, "Does the image contain hateful, illegal, or graphic material?")
	if err != nil {
		t.Fatal(err)
	}
	if followUpCalls != 2 || len(imageFollowed.FollowUps) != 2 || !strings.Contains(imageFollowed.FollowUps[1].Response, "art/bad.png") {
		t.Fatalf("unexpected visual follow-up: %#v", imageFollowed.FollowUps)
	}
	for _, path := range visualTempPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("focused visual attachment was not removed: %s, err=%v", path, err)
		}
	}

	reloaded, err := service.getModAudit(item.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ID != full.ID || len(reloaded.Files) == 0 || len(reloaded.FollowUps) != 2 {
		t.Fatalf("persisted audit did not reload: %#v", reloaded)
	}
	var reloadedImage *ModAuditFileArtifact
	for index := range reloaded.Files {
		if reloaded.Files[index].Path == "art/bad.png" {
			reloadedImage = &reloaded.Files[index]
			break
		}
	}
	if reloadedImage == nil || reloadedImage.MediaType != "image/png" {
		t.Fatalf("persisted visual artifact did not reload: %#v", reloadedImage)
	}
}

func writeModAuditFixture(t *testing.T) string {
	t.Helper()
	return writeModAuditFixtureAt(t, t.TempDir())
}

func writeModAuditFixtureAt(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "audit-fixture.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entries := map[string][]byte{
		"lua/ge/extensions/audit.lua":  []byte("local remote = 'https://example.invalid/payload'\nlocal command = input .. ' powershell'\nos.execute(command)\nloadstring(remote)()\nlocal encoded = '" + strings.Repeat("A", 280) + "'\n-- white supremacy propaganda marker\n"),
		"ui/modules/apps/audit/app.js": []byte("export async function load() { return fetch('https://example.invalid/state') }\n"),
		"bin/helper.exe":               portableExecutableFixture(),
		"assets/preview.dat":           portableExecutableFixture(),
		"../escape.exe":                portableExecutableFixture(),
		"art/bad.png":                  rasterImageFixture(),
		"assets/fake.png":              portableExecutableFixture(),
	}
	for name, content := range entries {
		entry, createErr := writer.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := entry.Write(content); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func portableExecutableFixture() []byte {
	data := make([]byte, 128)
	data[0], data[1] = 'M', 'Z'
	data[0x3c] = 64
	copy(data[64:], []byte{'P', 'E', 0, 0})
	return data
}

func rasterImageFixture() []byte {
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		panic(err)
	}
	return data
}

func insertModAuditFixture(t *testing.T, service *AppService, archivePath string) LibraryItem {
	t.Helper()
	ctx := context.Background()
	manifest, err := modkit.Inspect(ctx, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	scanID, err := service.store.BeginScan(ctx, []string{filepath.Dir(archivePath)})
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.store.UpsertArchive(ctx, scanID, filepath.Dir(archivePath), archivePath, info.Size(), info.ModTime(), manifest, nil)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func hasAuditSignal(signals []ModAuditSignal, code string) bool {
	for _, signal := range signals {
		if signal.Code == code {
			return true
		}
	}
	return false
}

func containsAuditWarning(warnings []string, fragment string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, fragment) {
			return true
		}
	}
	return false
}

func TestManagedAuditUsesPrivateRuntimeAndSelectedProviderState(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("managed desktop AI runtime is currently packaged for Windows")
	}
	service := newTestAppService(t)
	managed := newManagedAIRuntime(AppConfig{
		DataDir: service.config.DataDir, AIRuntimePath: stagedAIRuntimePath(t),
	})
	service.aiRuntime = managed
	_, err := service.SaveSettings(SettingsUpdate{
		Theme: "dark", AgentProfile: "openai", ContextMode: "balanced",
		OpenAIAPIKey: "scanner-managed-runtime-test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.runManagedAIAudit(context.Background(), auditAIRequest{
		Model: "gpt-test", Reasoning: "low", SystemPrompt: "Return JSON.",
		Prompt: `{}`, Timeout: time.Nanosecond,
	})
	if err == nil {
		t.Fatal("expired managed scanner invocation unexpectedly succeeded")
	}
	providerState := filepath.Join(service.config.DataDir, managedAIRuntimeRootName, managedAIRuntimeStateName, managedAIRuntimeAPIStateName, "openai")
	if info, statErr := os.Stat(providerState); statErr != nil || !info.IsDir() {
		t.Fatalf("scanner provider state = %q: %v", providerState, statErr)
	}
	promptEntries, readErr := os.ReadDir(filepath.Join(service.config.DataDir, "audit-prompts"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(promptEntries) != 0 {
		t.Fatalf("scanner left %d managed prompt files behind", len(promptEntries))
	}
}
