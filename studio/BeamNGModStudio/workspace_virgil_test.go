package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkspaceVirgilMigrationIsIdempotent(t *testing.T) {
	t.Parallel()
	filename := filepath.Join(t.TempDir(), "legacy-workspaces.sqlite")
	legacy, err := sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE workspaces (
		id TEXT PRIMARY KEY, entity_id TEXT NOT NULL, artifact_id TEXT NOT NULL,
		root TEXT NOT NULL, files_root TEXT NOT NULL, source_path TEXT NOT NULL, source_sha256 TEXT NOT NULL,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL, status TEXT NOT NULL,
		last_validation_json TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE virgil_sessions (
		id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, omp_session_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT 'Virgil session', omp_title TEXT NOT NULL DEFAULT '',
		user_title TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'idle',
		last_error TEXT NOT NULL DEFAULT '', tab_order INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(filename)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceVirgilColumns(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(filename)
	if err != nil {
		t.Fatalf("idempotent reopen: %v", err)
	}
	assertWorkspaceVirgilColumns(t, reopened)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNewWorkspaceStartsWithVirgilUnconfiguredAndDisabled(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	detail, err := service.CreateNewMod(NewModRequest{Name: "Virgil Defaults", ModID: "virgil_defaults", Kind: "script"})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Workspace.VirgilConfigured || detail.Workspace.VirgilEnabled {
		t.Fatalf("new workspace Virgil state = configured %t, enabled %t", detail.Workspace.VirgilConfigured, detail.Workspace.VirgilEnabled)
	}
}

func TestConfigureWorkspaceVirgilPersistsDisableWithoutDeletingHistory(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	detail, err := service.CreateNewMod(NewModRequest{Name: "Virgil Persistence", ModID: "virgil_persistence", Kind: "script"})
	if err != nil {
		t.Fatal(err)
	}
	const contentUpdatedAt = "2026-09-01T08:00:00Z"
	if _, err := service.store.db.Exec(`UPDATE workspaces SET updated_at=? WHERE id=?`, contentUpdatedAt, detail.Workspace.ID); err != nil {
		t.Fatal(err)
	}
	runID := "virgil-history-run"
	if err := service.store.CreateAgentRun(context.Background(), AgentRunRecord{ID: runID, WorkspaceID: detail.Workspace.ID, Prompt: "Review the generated workspace", StartedAt: "2026-09-02T09:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := service.store.AppendAgentEvent(context.Background(), runID, "progress", "Reviewed generated files", nil); err != nil {
		t.Fatal(err)
	}
	configured, err := service.ConfigureWorkspaceVirgil(detail.Workspace.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !configured.Workspace.VirgilConfigured || !configured.Workspace.VirgilEnabled {
		t.Fatalf("enabled Virgil state = configured %t, enabled %t", configured.Workspace.VirgilConfigured, configured.Workspace.VirgilEnabled)
	}
	if configured.Workspace.UpdatedAt != contentUpdatedAt {
		t.Fatalf("enabling Virgil changed workspace content timestamp to %q", configured.Workspace.UpdatedAt)
	}
	disabled, err := service.ConfigureWorkspaceVirgil(detail.Workspace.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if !disabled.Workspace.VirgilConfigured || disabled.Workspace.VirgilEnabled {
		t.Fatalf("disabled Virgil state = configured %t, enabled %t", disabled.Workspace.VirgilConfigured, disabled.Workspace.VirgilEnabled)
	}
	if disabled.Workspace.UpdatedAt != contentUpdatedAt {
		t.Fatalf("disabling Virgil changed workspace content timestamp to %q", disabled.Workspace.UpdatedAt)
	}
	events, err := service.store.ListAgentEvents(context.Background(), runID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Message != "Reviewed generated files" {
		t.Fatalf("conversation history after disabling Virgil = %#v", events)
	}
}

func TestCreateWorkspaceReusesLatestEntityWorkspace(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	created, err := service.CreateNewMod(NewModRequest{Name: "Workspace Reuse", ModID: "workspace_reuse", Kind: "script"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateWorkspace(created.Entity.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateWorkspace(created.Entity.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Workspace.ID != created.Workspace.ID || second.Workspace.ID != created.Workspace.ID || first.Workspace.ID != second.Workspace.ID {
		t.Fatalf("reopened workspace IDs = %q, %q, initial %q", first.Workspace.ID, second.Workspace.ID, created.Workspace.ID)
	}
	var count int
	if err := service.store.db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE entity_id=?`, created.Entity.EntityID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("workspace rows for entity = %d, want 1", count)
	}
}

func TestListWorkspacesReturnsLatestLegacyWorkspacePerMod(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	created, err := service.CreateNewMod(NewModRequest{Name: "Legacy Workspace", ModID: "legacy_workspace", Kind: "script"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.db.Exec(`UPDATE workspaces SET created_at=?,updated_at=? WHERE id=?`, "2026-09-01T08:00:00Z", "2026-09-01T08:00:00Z", created.Workspace.ID); err != nil {
		t.Fatal(err)
	}
	const latestID = "latest-legacy-workspace"
	if _, err := service.store.db.Exec(`INSERT INTO workspaces(
		id,entity_id,artifact_id,root,files_root,source_path,source_sha256,
		created_at,updated_at,status,last_validation_json,virgil_configured,virgil_enabled
	) SELECT ?,entity_id,artifact_id,root,files_root,source_path,source_sha256,
		?,?,status,last_validation_json,1,0 FROM workspaces WHERE id=?`,
		latestID, "2026-09-02T08:00:00Z", "2026-09-02T08:00:00Z", created.Workspace.ID); err != nil {
		t.Fatal(err)
	}

	workspaces, err := service.store.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(workspaces) != 1 || workspaces[0].ID != latestID {
		t.Fatalf("listed workspaces = %#v, want only latest ID %q", workspaces, latestID)
	}
	var count int
	if err := service.store.db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE entity_id=?`, created.Entity.EntityID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("legacy workspace rows = %d, want retained 2", count)
	}
}

func TestWorkspaceLatestAgentRunIncludesGoalAndProcess(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	detail, err := service.CreateNewMod(NewModRequest{Name: "Agent Activity", ModID: "agent_activity", Kind: "script"})
	if err != nil {
		t.Fatal(err)
	}
	runID := "latest-agent-run"
	if err := service.store.CreateAgentRun(context.Background(), AgentRunRecord{ID: runID, WorkspaceID: detail.Workspace.ID, Prompt: "Inspect the latest archive", StartedAt: "2026-09-02T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := service.store.AppendAgentEvent(context.Background(), runID, "turn_start", "Inspecting archive structure", nil); err != nil {
		t.Fatal(err)
	}
	if err := service.store.AppendAgentEvent(context.Background(), runID, "turn_end", "", nil); err != nil {
		t.Fatal(err)
	}
	workspace, err := service.store.GetWorkspace(context.Background(), detail.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.AgentRunID != runID || workspace.AgentGoal != "Inspect the latest archive" || workspace.AgentStatus != "running" {
		t.Fatalf("latest agent run = %#v", workspace)
	}
	if workspace.AgentProcess != "turn_end" {
		t.Fatalf("latest agent process = %q", workspace.AgentProcess)
	}
	if workspace.AgentUpdatedAt == "" {
		t.Fatal("latest agent update timestamp is empty")
	}
}

func TestVirgilSessionTabsPersistPauseRenameAndForgetIndependently(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	detail, err := service.CreateNewMod(NewModRequest{Name: "Session Tabs", ModID: "session_tabs", Kind: "script"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	workspaceID := detail.Workspace.ID
	sessions := []struct {
		id       string
		runID    string
		ompID    string
		ompTitle string
	}{
		{id: "app-session-a", runID: "run-a", ompID: "01911111-1111-7111-8111-111111111111", ompTitle: "Tune suspension balance"},
		{id: "app-session-b", runID: "run-b", ompID: "01922222-2222-7222-8222-222222222222", ompTitle: "Review vehicle metadata"},
	}
	for index, input := range sessions {
		startedAt := "2026-09-02T10:0" + string(rune('0'+index)) + ":00Z"
		session := VirgilSessionRecord{
			ID: input.id, WorkspaceID: workspaceID, Status: "starting",
			CreatedAt: startedAt, UpdatedAt: startedAt,
		}
		run := AgentRunRecord{
			ID: input.runID, Prompt: "Test turn", Status: "running", StartedAt: startedAt,
		}
		if err := service.store.CreateVirgilSessionAndRun(ctx, session, run); err != nil {
			t.Fatal(err)
		}
		if err := service.store.SetVirgilSessionOMPState(ctx, input.id, input.ompID, input.ompTitle); err != nil {
			t.Fatal(err)
		}
		if err := service.store.FinishAgentRun(ctx, input.runID, "complete", "Done", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.store.AppendAgentEvent(ctx, sessions[0].runID, "turn_end", "Finished", nil); err != nil {
		t.Fatal(err)
	}
	renamed, err := service.store.RenameVirgilSession(ctx, sessions[0].id, "My suspension pass")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.store.SetVirgilSessionOMPTitle(ctx, sessions[0].id, "Later OMP title"); err != nil {
		t.Fatal(err)
	}
	renamed, err = service.store.GetVirgilSession(ctx, renamed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != "My suspension pass" || renamed.UserTitle != "My suspension pass" || renamed.OMPTitle != "Later OMP title" {
		t.Fatalf("renamed title precedence = %#v", renamed)
	}
	beforeRestart, err := service.store.ListVirgilSessions(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeRestart) != 2 || beforeRestart[0].TabOrder != 0 || beforeRestart[1].TabOrder != 1 {
		t.Fatalf("persisted tab order = %#v", beforeRestart)
	}
	if beforeRestart[0].OMPSessionID != sessions[0].ompID || len(beforeRestart[0].Runs) != 1 {
		t.Fatalf("persisted session identity and runs = %#v", beforeRestart[0])
	}

	databasePath := service.config.DatabasePath
	if err := service.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	afterRestart, err := reopened.ListVirgilSessions(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterRestart) != 2 || afterRestart[0].Status != "paused" || afterRestart[1].Status != "paused" {
		t.Fatalf("sessions after restart = %#v", afterRestart)
	}
	if err := reopened.DeleteVirgilSession(ctx, sessions[0].id); err != nil {
		t.Fatal(err)
	}
	remaining, err := reopened.ListVirgilSessions(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].ID != sessions[1].id {
		t.Fatalf("remaining sessions after close = %#v", remaining)
	}
	var deletedEvents int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM agent_events WHERE run_id=?`, sessions[0].runID).Scan(&deletedEvents); err != nil {
		t.Fatal(err)
	}
	if deletedEvents != 0 {
		t.Fatalf("forgotten session retained %d events", deletedEvents)
	}
}

func TestVirgilSessionProfilePersistsNormalized(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	detail, err := service.CreateNewMod(NewModRequest{Name: "Pinned Profile", ModID: "pinned_profile", Kind: "script"})
	if err != nil {
		t.Fatal(err)
	}
	session := VirgilSessionRecord{
		ID: "profile-session", WorkspaceID: detail.Workspace.ID, Profile: "  Anthropic  ",
		Status: "idle", CreatedAt: "2026-09-02T12:00:00Z", UpdatedAt: "2026-09-02T12:00:00Z",
	}
	if err := service.store.CreateVirgilSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	got, err := service.store.GetVirgilSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "anthropic" {
		t.Fatalf("stored profile = %q, want normalized anthropic", got.Profile)
	}
}

func TestAgentLaunchArgumentsPersistAndResumeInAppSessionDirectory(t *testing.T) {
	t.Parallel()
	arguments := strings.Join(agentLaunchArguments(
		"C:/workspace",
		"C:/context.md",
		"01933333-3333-7333-8333-333333333333",
		"C:/studio/ai-runtime/state/sessions",
		agentLaunchSettings{},
	), " ")
	for _, expected := range []string{
		"--mode rpc",
		"--cwd C:/workspace",
		"--session-dir C:/studio/ai-runtime/state/sessions",
		"--resume 01933333-3333-7333-8333-333333333333",
		"--append-system-prompt C:/context.md",
	} {
		if !strings.Contains(arguments, expected) {
			t.Fatalf("launch arguments %q do not include %q", arguments, expected)
		}
	}
	if strings.Contains(arguments, "--no-session") {
		t.Fatalf("launch arguments disable persistence: %q", arguments)
	}
}

type testWriteCloser struct {
	bytes.Buffer
}

func (writer *testWriteCloser) Close() error { return nil }

func TestRPCSendUsesOneFrameAndRejectsOversizedPayloads(t *testing.T) {
	t.Parallel()
	writer := &testWriteCloser{}
	run := &agentRun{
		id:      "frame-test",
		stdin:   writer,
		decoder: &rpcDecoder{maxFrame: 1024, maxReassembled: 1 << 20},
	}
	oversized := strings.Repeat("世界", 1200)
	if err := run.send(map[string]any{"type": "host_tool_result", "value": oversized}); err == nil {
		t.Fatal("oversized outbound RPC payload was accepted")
	}
	if writer.Len() != 0 || bytes.Contains(writer.Bytes(), []byte("rpc_chunk")) {
		t.Fatalf("oversized outbound RPC wrote unexpected data: %q", writer.Bytes())
	}
	if err := run.send(map[string]any{"type": "host_tool_result", "value": "ok"}); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(writer.Bytes()), []byte{'\n'})
	if len(lines) != 1 {
		t.Fatalf("small outbound RPC used %d frames, want one", len(lines))
	}
	if len(lines[0]) > run.decoder.maxFrame {
		t.Fatalf("outbound RPC has %d bytes, limit %d", len(lines[0]), run.decoder.maxFrame)
	}
}
func TestWorkspaceRevisionChecksPreventStaleWrites(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := writeWorkspaceTextChecked(root, "vehicle/main.jbeam", "first", ""); err != nil {
		t.Fatalf("create with absent revision: %v", err)
	}
	if err := writeWorkspaceTextChecked(root, "vehicle/main.jbeam", "stale", ""); err == nil || !strings.Contains(err.Error(), "workspace conflict") {
		t.Fatalf("stale create result = %v, want workspace conflict", err)
	}
	sum := sha256.Sum256([]byte("first"))
	expected := hex.EncodeToString(sum[:])
	if err := writeWorkspaceTextChecked(root, "vehicle/main.jbeam", "second", expected); err != nil {
		t.Fatalf("write with matching revision: %v", err)
	}
	if err := writeWorkspaceTextChecked(root, "vehicle/main.jbeam", "stale", expected); err == nil || !strings.Contains(err.Error(), "workspace conflict") {
		t.Fatalf("stale overwrite result = %v, want workspace conflict", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "vehicle", "main.jbeam"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "second" {
		t.Fatalf("file content after stale write = %q, want second", content)
	}
}

func TestWorkspaceReadReturnsBoundedPagesWithFullSHA(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	content := strings.Repeat("世界\n", 500)
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	run := &agentRun{
		workspace: WorkspaceRecord{FilesRoot: root},
		decoder:   &rpcDecoder{maxFrame: 900, maxReassembled: 1 << 20},
	}
	arguments := map[string]any{"path": "notes.txt", "maxBytes": float64(len(content))}
	firstText, err := run.executeHostToolWithRequest("workspace_read", arguments, "read-1")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(firstText), &first); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	expectedSHA := hex.EncodeToString(sum[:])
	if first["sha256"] != expectedSHA {
		t.Fatalf("first page sha256 = %v, want %s", first["sha256"], expectedSHA)
	}
	if truncated, _ := first["truncated"].(bool); !truncated {
		t.Fatalf("first page truncated = %v, want true", first["truncated"])
	}
	nextOffset, ok := first["nextOffset"].(float64)
	if !ok || nextOffset <= 0 || nextOffset >= float64(len(content)) {
		t.Fatalf("first page nextOffset = %v, want an interior offset", first["nextOffset"])
	}
	if _, err := run.executeHostToolWithRequest("workspace_read", map[string]any{
		"path": "notes.txt", "offset": nextOffset, "maxBytes": float64(len(content)),
	}, "read-missing-revision"); err == nil || !strings.Contains(err.Error(), "expectedSha256 is required") {
		t.Fatalf("continuation without revision = %v, want required revision error", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte(content+"changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run.executeHostToolWithRequest("workspace_read", map[string]any{
		"path": "notes.txt", "offset": nextOffset, "maxBytes": float64(len(content)), "expectedSha256": expectedSHA,
	}, "read-stale"); err == nil || !strings.Contains(err.Error(), "workspace_read conflict") {
		t.Fatalf("continuation after external edit = %v, want revision conflict", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	secondText, err := run.executeHostToolWithRequest("workspace_read", map[string]any{
		"path": "notes.txt", "offset": nextOffset, "maxBytes": float64(len(content)), "expectedSha256": expectedSHA,
	}, "read-2")
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	var second map[string]any
	if err := json.Unmarshal([]byte(secondText), &second); err != nil {
		t.Fatal(err)
	}
	if second["sha256"] != expectedSHA {
		t.Fatalf("second page sha256 = %v, want %s", second["sha256"], expectedSHA)
	}
	if offset, _ := second["offset"].(float64); offset != nextOffset {
		t.Fatalf("second page offset = %v, want %v", offset, nextOffset)
	}
}

func TestHostToolAdmissionGateDrainsAcceptedCalls(t *testing.T) {
	t.Parallel()
	run := &agentRun{}
	if !run.admitHostTool() {
		t.Fatal("initial host tool admission rejected")
	}
	run.stopAdmittingHostTools()
	drained := make(chan struct{})
	go func() {
		run.drainHostTools()
		close(drained)
	}()
	if run.admitHostTool() {
		t.Fatal("host tool admitted after gate closed")
	}
	run.finishHostTool()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("drain did not complete after accepted host tool finished")
	}
}

func TestAgentCompletePersistsTerminalStateAfterHostToolDrainTimeout(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	detail, err := service.CreateNewMod(NewModRequest{Name: "Drain Persistence", ModID: "drain_persistence", Kind: "script"})
	if err != nil {
		t.Fatal(err)
	}
	session := VirgilSessionRecord{
		ID: "drain-session", WorkspaceID: detail.Workspace.ID, Profile: "chatgpt",
		Status: "running", CreatedAt: nowUTC(), UpdatedAt: nowUTC(),
	}
	record := AgentRunRecord{
		ID: "drain-run", SessionID: session.ID, WorkspaceID: session.WorkspaceID,
		Prompt: "test", Status: "running", StartedAt: nowUTC(),
	}
	if err := service.store.CreateVirgilSessionAndRun(context.Background(), session, record); err != nil {
		t.Fatal(err)
	}
	run := &agentRun{
		id: record.ID, sessionID: session.ID, workspace: detail.Workspace,
		manager: service.agents, persist: true, done: make(chan struct{}),
	}
	run.toolWG.Add(1)
	started := time.Now()
	run.complete("failed", errors.New("forced terminal failure"))
	if elapsed := time.Since(started); elapsed < hostToolDrainTimeout {
		t.Fatalf("host tool drain returned before timeout: %v", elapsed)
	}
	run.toolWG.Done()
	if !run.discarded.Load() {
		t.Fatal("timed-out host tool drain did not suppress late activity")
	}
	runs, err := service.store.ListAgentRunsBySession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != "failed" || runs[0].FinishedAt == "" || !strings.Contains(runs[0].Error, "forced terminal failure") {
		t.Fatalf("terminal run was not persisted after drain timeout: %#v", runs)
	}
	storedSession, err := service.store.GetVirgilSession(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedSession.Status != "error" || !strings.Contains(storedSession.LastError, "forced terminal failure") {
		t.Fatalf("terminal session was not persisted after drain timeout: %#v", storedSession)
	}
}
func TestHostToolCancellationWinsBeforeQueuedExecution(t *testing.T) {
	t.Parallel()
	run := &agentRun{}
	run.registerHostTool("write-1")
	run.cancelHostTool("write-1")
	if run.beginHostToolExecution("write-1") {
		t.Fatal("cancelled queued host tool was allowed to begin")
	}

	run.registerHostTool("write-2")
	if !run.beginHostToolExecution("write-2") {
		t.Fatal("uncancelled host tool was rejected")
	}
	run.cancelHostTool("write-2")
	if len(run.toolRequests) != 0 {
		t.Fatalf("late cancellation recreated completed request state: %#v", run.toolRequests)
	}
}

type blockingWriteCloser struct {
	entered chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func newBlockingWriteCloser() *blockingWriteCloser {
	return &blockingWriteCloser{entered: make(chan struct{}), closed: make(chan struct{})}
}

func (writer *blockingWriteCloser) Write(_ []byte) (int, error) {
	writer.once.Do(func() { close(writer.entered) })
	<-writer.closed
	return 0, io.ErrClosedPipe
}

func (writer *blockingWriteCloser) Close() error {
	select {
	case <-writer.closed:
	default:
		close(writer.closed)
	}
	return nil
}
func TestStopAllCancelsRegisteredLaunchBeforeWaiting(t *testing.T) {
	t.Parallel()
	manager := NewAgentManager(nil, AppConfig{}, nil)
	writer := newBlockingWriteCloser()
	processDone := make(chan struct{})
	_, cancel := context.WithCancel(context.Background())
	run := &agentRun{
		id: "registered-launch", stdin: writer,
		decoder: &rpcDecoder{maxFrame: 1024, maxReassembled: 1 << 20},
		cancel:  cancel, manager: manager, processDone: processDone, done: make(chan struct{}),
	}
	run.discarded.Store(true)
	manager.mu.Lock()
	manager.runs[run.id] = run
	manager.launches.Add(1)
	manager.mu.Unlock()

	go func() {
		<-writer.closed
		close(processDone)
		manager.finishLaunch()
	}()
	started := time.Now()
	manager.StopAll()
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("StopAll took %v with a blocked bootstrap write", elapsed)
	}
	select {
	case <-writer.entered:
	default:
		t.Fatal("shutdown did not attempt a graceful abort before closing input")
	}
}

func TestStopAllWaitsForInFlightLaunches(t *testing.T) {
	t.Parallel()
	manager := NewAgentManager(nil, AppConfig{}, nil)
	if err := manager.reserveSession("launching"); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() {
		manager.StopAll()
		close(stopped)
	}()
	deadline := time.Now().Add(time.Second)
	for !manager.shuttingDown.Load() {
		if time.Now().After(deadline) {
			t.Fatal("StopAll did not enter shutdown")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-stopped:
		t.Fatal("StopAll returned before the in-flight launch completed")
	default:
	}
	manager.finishLaunch()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("StopAll did not return after the in-flight launch completed")
	}
	if err := manager.reserveSession("late"); err == nil {
		t.Fatal("session launch admitted after shutdown")
	}
}

func TestStopAllCancelsLaunchWaitingForWorkspaceContextLock(t *testing.T) {
	t.Parallel()
	manager := NewAgentManager(nil, AppConfig{}, nil)
	workspace := WorkspaceRecord{ID: "blocked-workspace", Root: t.TempDir()}
	workspaceLock := manager.workspaceToolMutex(workspace.ID)
	workspaceLock.Lock()
	launchContext, err := manager.reserveSessionContext(context.Background(), "blocked-session")
	if err != nil {
		workspaceLock.Unlock()
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		defer manager.finishLaunch()
		defer manager.releaseSession("blocked-session")
		_, writeErr := manager.writeAgentContextContext(launchContext, "blocked-run", workspace, LibraryItem{}, nil)
		result <- writeErr
	}()
	manager.StopAll()
	workspaceLock.Unlock()
	select {
	case writeErr := <-result:
		if !errors.Is(writeErr, context.Canceled) {
			t.Fatalf("blocked context write error = %v, want cancellation", writeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled context write did not return")
	}
	if _, err := os.Stat(filepath.Join(workspace.Root, ".modstudio", "agent-context-blocked-run.md")); !os.IsNotExist(err) {
		t.Fatalf("cancelled launch wrote an agent context: %v", err)
	}
}

func TestStopAllBoundsUnresponsivePreRegistrationLaunch(t *testing.T) {
	t.Parallel()
	manager := NewAgentManager(nil, AppConfig{}, nil)
	launchContext, err := manager.reserveSessionContext(context.Background(), "stuck-launch")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	manager.StopAll()
	elapsed := time.Since(started)
	if elapsed < launchWaitTimeout || elapsed > launchWaitTimeout+time.Second {
		t.Fatalf("bounded shutdown duration = %v, want about %v", elapsed, launchWaitTimeout)
	}
	if !errors.Is(launchContext.Err(), context.Canceled) {
		t.Fatalf("pre-registration launch context = %v, want cancellation", launchContext.Err())
	}
	manager.finishLaunch()
	manager.releaseSession("stuck-launch")
}

func TestRemoveOMPSessionTranscriptOnlyMatchesStoredSession(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sessionDir := filepath.Join(root, "ai-runtime", "state", "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionID := "01944444-4444-7444-8444-444444444444"
	target := filepath.Join(sessionDir, "20260902_"+sessionID+".jsonl")
	other := filepath.Join(sessionDir, "20260902_other.jsonl")
	for _, filename := range []string{target, other} {
		if err := os.WriteFile(filename, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := removeVirgilSessionTranscript(sessionDir, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("stored transcript stat = %v, want not exist", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("unrelated transcript stat = %v, want present", err)
	}
}

func assertWorkspaceVirgilColumns(t *testing.T, store *Store) {
	t.Helper()
	columns := []struct {
		table  string
		column string
	}{
		{table: "workspaces", column: "virgil_configured"},
		{table: "workspaces", column: "virgil_enabled"},
		{table: "workspace_drafts", column: "base_sha256"},
		{table: "virgil_sessions", column: "profile"},
	}
	for _, item := range columns {
		var count int
		query := `SELECT COUNT(*) FROM pragma_table_info('` + item.table + `') WHERE name='` + item.column + `'`
		if err := store.db.QueryRow(query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s.%s columns = %d, want 1", item.table, item.column, count)
		}
	}
}

func TestVirgilPersistsAssistantOutcomes(t *testing.T) {
	cases := []struct {
		name       string
		frames     []string
		status     string
		text       string
		errorMatch string
	}{
		{
			name: "provider rejection",
			frames: []string{
				`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"The requested model is not supported for this account."}}`,
				`{"type":"agent_end","isTerminal":true}`,
			},
			status: "failed", errorMatch: "not supported for this account",
		},
		{
			name: "successful response after provider retry",
			frames: []string{
				`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"Temporary provider outage"}}`,
				`{"type":"agent_end","isTerminal":false}`,
				`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Inspection complete."}],"stopReason":"stop"}}`,
				`{"type":"agent_end","isTerminal":true}`,
			},
			status: "complete", text: "Inspection complete.",
		},
		{
			name: "final text without streaming deltas",
			frames: []string{
				`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Inspection "},{"type":"text","text":"complete."}],"stopReason":"stop"}}`,
				`{"type":"agent_end","isTerminal":true}`,
			},
			status: "complete", text: "Inspection complete.",
		},
		{
			name: "streamed text is not duplicated by final message",
			frames: []string{
				`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
				`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"Inspection complete."}}`,
				`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Inspection complete."}],"stopReason":"stop"}}`,
				`{"type":"agent_end","isTerminal":true}`,
			},
			status: "complete", text: "Inspection complete.",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := newTestAppService(t)
			detail, err := service.CreateNewMod(NewModRequest{Name: "Provider outcome", ModID: "provider_outcome", Kind: "script"})
			if err != nil {
				t.Fatal(err)
			}
			session := VirgilSessionRecord{
				ID: "outcome-session", WorkspaceID: detail.Workspace.ID, Profile: "chatgpt",
				Status: "running", CreatedAt: nowUTC(), UpdatedAt: nowUTC(),
			}
			record := AgentRunRecord{
				ID: "outcome-run", SessionID: session.ID, WorkspaceID: session.WorkspaceID,
				Prompt: "Inspect this mod", Status: "running", StartedAt: nowUTC(),
			}
			if err := service.store.CreateVirgilSessionAndRun(context.Background(), session, record); err != nil {
				t.Fatal(err)
			}
			run := &agentRun{
				id: record.ID, sessionID: session.ID, workspace: detail.Workspace,
				manager: service.agents, persist: true, done: make(chan struct{}),
				decoder: &rpcDecoder{
					reader:   bufio.NewReader(strings.NewReader(strings.Join(test.frames, "\n") + "\n")),
					maxFrame: maxRPCFrameBytes, maxReassembled: maxRPCReassembledBytes,
				},
			}
			run.readLoop()
			run.waitLoop()
			runs, err := service.store.ListAgentRunsBySession(context.Background(), session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != 1 || runs[0].Status != test.status || runs[0].FinalText != test.text {
				t.Fatalf("persisted response = %#v; want status %q, text %q", runs, test.status, test.text)
			}
			storedSession, err := service.store.GetVirgilSession(context.Background(), session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if test.errorMatch != "" {
				if !strings.Contains(runs[0].Error, test.errorMatch) || storedSession.Status != "error" || !strings.Contains(storedSession.LastError, test.errorMatch) {
					t.Fatalf("provider failure was hidden: run %#v, session %#v", runs[0], storedSession)
				}
			} else if runs[0].Error != "" || storedSession.Status != "idle" || storedSession.LastError != "" {
				t.Fatalf("successful response retained a provider error: run %#v, session %#v", runs[0], storedSession)
			}
		})
	}
}
