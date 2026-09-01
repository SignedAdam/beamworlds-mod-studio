package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

const (
	maxAgentPromptBytes      = 32 << 10
	maxRPCFrameBytes         = 2 << 20
	maxRPCReassembledBytes   = 64 << 20
	maxPersistedAgentText    = 4 << 20
	maxHostToolResponseBytes = 8 << 20
)

type AgentActivity struct {
	RunID    string         `json:"runId"`
	At       string         `json:"at"`
	Type     string         `json:"type"`
	Message  string         `json:"message,omitempty"`
	Delta    string         `json:"delta,omitempty"`
	ToolName string         `json:"toolName,omitempty"`
	IsError  bool           `json:"isError,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

type AgentManager struct {
	store          *Store
	config         AppConfig
	emit           func(string, any)
	mu             sync.Mutex
	runs           map[string]*agentRun
	busyWorkspaces map[string]bool
}

type agentRun struct {
	id        string
	workspace WorkspaceRecord
	item      LibraryItem
	prompt    string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	decoder   *rpcDecoder
	cancel    context.CancelFunc
	manager   *AgentManager

	writeMu         sync.Mutex
	toolMu          sync.Mutex
	textMu          sync.Mutex
	text            strings.Builder
	finish          sync.Once
	cancelRequested atomic.Bool
}

type rpcDecoder struct {
	reader         *bufio.Reader
	maxFrame       int
	maxReassembled int
	chunk          *rpcChunkState
}

type rpcChunkState struct {
	id         string
	count      int
	byteLength int
	next       int
	data       bytes.Buffer
}

func NewAgentManager(store *Store, config AppConfig, emit func(string, any)) *AgentManager {
	return &AgentManager{
		store: store, config: config, emit: emit,
		runs: map[string]*agentRun{}, busyWorkspaces: map[string]bool{},
	}
}
func agentSelectionArguments(settings agentLaunchSettings) []string {
	provider := ""
	switch settings.Profile {
	case "codex":
		provider = "openai-codex"
	case "claude":
		provider = "anthropic"
	case "openrouter":
		provider = "openrouter"
	case "openai":
		provider = "openai"
	}
	if settings.Model == "" {
		if provider == "" {
			return nil
		}
		return []string{"--provider", provider}
	}
	model := settings.Model
	if !strings.Contains(model, "/") && provider != "" {
		model = provider + "/" + model
	}
	return []string{"--model", model}
}

func (manager *AgentManager) Start(ctx context.Context, workspaceID, prompt string, settings agentLaunchSettings) (AgentRunRecord, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return AgentRunRecord{}, errors.New("agent goal is required")
	}
	if len(prompt) > maxAgentPromptBytes {
		return AgentRunRecord{}, fmt.Errorf("agent goal exceeds %d bytes", maxAgentPromptBytes)
	}
	manager.mu.Lock()
	if manager.busyWorkspaces[workspaceID] {
		manager.mu.Unlock()
		return AgentRunRecord{}, errors.New("an OMP agent is already active for this workspace")
	}
	manager.busyWorkspaces[workspaceID] = true
	manager.mu.Unlock()
	reserved := true
	defer func() {
		if !reserved {
			return
		}
		manager.mu.Lock()
		delete(manager.busyWorkspaces, workspaceID)
		manager.mu.Unlock()
	}()
	workspace, err := manager.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return AgentRunRecord{}, err
	}
	item, err := manager.store.GetLibraryItem(ctx, workspace.EntityID)
	if err != nil {
		return AgentRunRecord{}, err
	}
	documents, err := knowledgeFor(item.Kind, item.Manifest.ContentTags)
	if err != nil {
		return AgentRunRecord{}, err
	}
	switch settings.ContextMode {
	case "focused":
		if len(documents) > 1 {
			documents = documents[1:]
		}
	case "deep":
		documents, err = knowledgeFor(modkit.KindMixed, []string{"vehicle", "map", "ui", "script"})
		if err != nil {
			return AgentRunRecord{}, err
		}
	}
	runID, err := modkit.NewID()
	if err != nil {
		return AgentRunRecord{}, err
	}
	startedAt := nowUTC()
	record := AgentRunRecord{ID: runID, WorkspaceID: workspaceID, Prompt: prompt, Status: "running", StartedAt: startedAt}
	if err := manager.store.CreateAgentRun(ctx, record); err != nil {
		return AgentRunRecord{}, err
	}
	contextPath, err := manager.writeAgentContext(runID, workspace, item, documents)
	if err != nil {
		_ = manager.store.FinishAgentRun(context.Background(), runID, "failed", "", err.Error())
		return AgentRunRecord{}, err
	}
	ompPath, err := resolveOMPPath(manager.config.OMPPath)
	if err != nil {
		_ = manager.store.FinishAgentRun(context.Background(), runID, "failed", "", err.Error())
		return AgentRunRecord{}, err
	}
	runContext, cancel := context.WithCancel(context.Background())
	arguments := []string{
		"--mode", "rpc",
		"--cwd", workspace.FilesRoot,
		"--no-session",
		"--tools", "todo",
		"--no-extensions",
		"--no-skills",
		"--no-rules",
		"--no-lsp",
		"--no-pty",
		"--max-time", "30m",
		"--append-system-prompt", contextPath,
	}
	arguments = append(arguments, agentSelectionArguments(settings)...)
	command := exec.CommandContext(runContext, ompPath, arguments...)
	command.Env = os.Environ()
	if settings.APIKey != "" {
		switch settings.Profile {
		case "openrouter":
			command.Env = append(command.Env, "OPENROUTER_API_KEY="+settings.APIKey)
		case "claude":
			command.Env = append(command.Env, "ANTHROPIC_API_KEY="+settings.APIKey)
		case "openai":
			command.Env = append(command.Env, "OPENAI_API_KEY="+settings.APIKey)
		}
	}
	command.Dir = workspace.FilesRoot
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		_ = manager.store.FinishAgentRun(context.Background(), runID, "failed", "", err.Error())
		return AgentRunRecord{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		_ = manager.store.FinishAgentRun(context.Background(), runID, "failed", "", err.Error())
		return AgentRunRecord{}, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		cancel()
		_ = manager.store.FinishAgentRun(context.Background(), runID, "failed", "", err.Error())
		return AgentRunRecord{}, err
	}
	if err := command.Start(); err != nil {
		cancel()
		_ = manager.store.FinishAgentRun(context.Background(), runID, "failed", "", err.Error())
		return AgentRunRecord{}, fmt.Errorf("start OMP: %w", err)
	}
	decoder := &rpcDecoder{reader: bufio.NewReaderSize(stdout, 64<<10), maxFrame: maxRPCFrameBytes, maxReassembled: maxRPCReassembledBytes}
	readyChannel := make(chan struct {
		frame map[string]any
		err   error
	}, 1)
	go func() {
		frame, readErr := decoder.Read()
		readyChannel <- struct {
			frame map[string]any
			err   error
		}{frame: frame, err: readErr}
	}()
	var ready map[string]any
	select {
	case result := <-readyChannel:
		if result.err != nil {
			cancel()
			_ = command.Wait()
			_ = manager.store.FinishAgentRun(context.Background(), runID, "failed", "", result.err.Error())
			return AgentRunRecord{}, fmt.Errorf("read OMP ready frame: %w", result.err)
		}
		ready = result.frame
	case <-time.After(15 * time.Second):
		cancel()
		_ = command.Wait()
		err := errors.New("OMP RPC did not become ready within 15 seconds")
		_ = manager.store.FinishAgentRun(context.Background(), runID, "failed", "", err.Error())
		return AgentRunRecord{}, err
	}
	if frameType(ready) != "ready" {
		cancel()
		_ = command.Wait()
		err := fmt.Errorf("OMP returned %q before ready", frameType(ready))
		_ = manager.store.FinishAgentRun(context.Background(), runID, "failed", "", err.Error())
		return AgentRunRecord{}, err
	}
	decoder.applyReady(ready)
	run := &agentRun{id: runID, workspace: workspace, item: item, prompt: prompt, cmd: command, stdin: stdin, decoder: decoder, cancel: cancel, manager: manager}
	manager.mu.Lock()
	manager.runs[runID] = run
	manager.mu.Unlock()
	reserved = false
	run.activity("ready", "OMP RPC connected", "", "", false, ready, true)
	if supportsProtocolV2(ready) {
		if err := run.send(map[string]any{"id": "protocol-" + runID, "type": "negotiate_protocol", "protocolVersion": 2}); err != nil {
			run.complete("failed", err)
			return record, err
		}
	}
	if err := run.send(map[string]any{"id": "tools-" + runID, "type": "set_host_tools", "tools": hostToolDefinitions()}); err != nil {
		run.complete("failed", err)
		return record, err
	}
	userMessage := "Goal: " + prompt + "\n\nWork only in the provided editable workspace through the host tools. Inspect before editing. Do not export or install. Before finishing, run mod_validate and workspace_diff, fix validation errors, and report exact changed files and remaining warnings."
	if err := run.send(map[string]any{"id": "prompt-" + runID, "type": "prompt", "message": userMessage}); err != nil {
		run.complete("failed", err)
		return record, err
	}
	go run.readLoop()
	go run.stderrLoop(stderr)
	go run.waitLoop()
	return record, nil
}

func (manager *AgentManager) Stop(runID string) bool {
	manager.mu.Lock()
	run := manager.runs[runID]
	manager.mu.Unlock()
	if run == nil {
		return false
	}
	if !run.cancelRequested.CompareAndSwap(false, true) {
		return true
	}
	_ = run.send(map[string]any{"id": "abort-" + runID, "type": "abort"})
	_ = run.stdin.Close()
	run.activity("cancelling", "Cancellation requested", "", "", false, nil, true)
	time.AfterFunc(5*time.Second, run.cancel)
	return true
}

func (manager *AgentManager) StopAll() {
	manager.mu.Lock()
	runs := make([]*agentRun, 0, len(manager.runs))
	for _, run := range manager.runs {
		runs = append(runs, run)
	}
	manager.mu.Unlock()
	for _, run := range runs {
		run.cancelRequested.Store(true)
		_ = run.send(map[string]any{"id": "abort-" + run.id, "type": "abort"})
		_ = run.stdin.Close()
		run.cancel()
		run.complete("cancelled", nil)
	}
}

func (manager *AgentManager) writeAgentContext(runID string, workspace WorkspaceRecord, item LibraryItem, documents []KnowledgeDocument) (string, error) {
	metadataDir := filepath.Join(workspace.Root, ".modstudio")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		return "", err
	}
	manifestSummary := map[string]any{
		"entityId": item.EntityID, "artifactId": item.ArtifactID, "displayName": item.DisplayName,
		"kind": item.Kind, "contentTags": item.Manifest.ContentTags, "namespaces": item.Manifest.Namespaces,
		"title": item.Manifest.Title, "description": item.Manifest.Description, "author": item.Manifest.Author,
		"version": item.Manifest.Version, "entryCount": item.Manifest.EntryCount,
		"variants": item.Manifest.Variants, "jbeam": item.Manifest.JBeam, "map": item.Manifest.Map,
		"ui": item.Manifest.UI, "issues": item.Manifest.Issues,
	}
	encoded, _ := json.MarshalIndent(manifestSummary, "", "  ")
	var builder strings.Builder
	builder.WriteString("You are the BeamWorlds ModMaker agent. The source archive is immutable and unavailable. ")
	builder.WriteString("You can access only the copied workspace through host-owned tools. Never claim an edit without using those tools. ")
	builder.WriteString("Do not create placeholders, copy third-party/base-game assets, suppress failures, export, install, or launch the game.\n\n")
	builder.WriteString("<mod-manifest>\n")
	builder.Write(encoded)
	builder.WriteString("\n</mod-manifest>\n\n")
	builder.WriteString(renderKnowledgePrompt(documents))
	filename := filepath.Join(metadataDir, "agent-context-"+runID+".md")
	if err := os.WriteFile(filename, []byte(builder.String()), 0o600); err != nil {
		return "", err
	}
	return filename, nil
}

func (run *agentRun) readLoop() {
	for {
		frame, err := run.decoder.Read()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				run.complete("failed", fmt.Errorf("OMP RPC stream: %w", err))
			}
			return
		}
		typeName := frameType(frame)
		switch typeName {
		case "host_tool_call":
			go run.handleHostTool(frame)
		case "message_update":
			delta := extractAssistantDelta(frame)
			if delta != "" {
				run.textMu.Lock()
				if run.text.Len()+len(delta) <= maxPersistedAgentText {
					run.text.WriteString(delta)
				}
				run.textMu.Unlock()
				run.activity(typeName, "", delta, "", false, nil, false)
			}
		case "tool_execution_start", "tool_execution_update", "tool_execution_end":
			toolName := nestedString(frame, "toolName")
			if toolName == "" {
				toolName = nestedString(frame, "toolCall", "name")
			}
			run.activity(typeName, toolName, "", toolName, false, compactFrame(frame), typeName != "tool_execution_update")
		case "response":
			success, _ := frame["success"].(bool)
			command, _ := frame["command"].(string)
			if !success {
				message := rpcErrorText(frame)
				run.activity("rpc_error", message, "", "", true, compactFrame(frame), true)
				if command == "prompt" {
					run.complete("failed", errors.New(message))
					return
				}
			}
		case "prompt_result":
			if invoked, ok := frame["agentInvoked"].(bool); ok && !invoked {
				run.complete("complete", nil)
				return
			}
		case "agent_end":
			if terminal, exists := frame["isTerminal"].(bool); !exists || terminal {
				run.complete("complete", nil)
				return
			}
		case "agent_start", "turn_start", "turn_end", "message_start", "message_end", "auto_compaction_start", "auto_compaction_end", "auto_retry_start", "auto_retry_end", "notice", "goal_updated", "extension_error":
			run.activity(typeName, activityMessage(frame), "", "", typeName == "extension_error", compactFrame(frame), typeName != "message_start")
		}
	}
}

func (run *agentRun) stderrLoop(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		message := strings.TrimSpace(scanner.Text())
		if message != "" {
			run.activity("stderr", message, "", "", true, nil, true)
		}
	}
}

func (run *agentRun) waitLoop() {
	err := run.cmd.Wait()
	if err != nil {
		run.complete("failed", fmt.Errorf("OMP exited: %w", err))
		return
	}
	run.complete("complete", nil)
}

func (run *agentRun) complete(status string, runErr error) {
	run.finish.Do(func() {
		if run.cancelRequested.Load() {
			status = "cancelled"
			runErr = nil
		}
		if status == "complete" {
			_ = run.stdin.Close()
		} else {
			run.cancel()
		}
		run.textMu.Lock()
		finalText := run.text.String()
		run.textMu.Unlock()
		errorText := ""
		if runErr != nil {
			errorText = runErr.Error()
			if strings.Contains(strings.ToLower(errorText), "signal: killed") {
				status = "cancelled"
			}
		}
		_ = run.manager.store.FinishAgentRun(context.Background(), run.id, status, finalText, errorText)
		run.manager.mu.Lock()
		delete(run.manager.runs, run.id)
		delete(run.manager.busyWorkspaces, run.workspace.ID)
		run.manager.mu.Unlock()
		run.activity("finished", status, "", "", runErr != nil, map[string]any{"status": status, "error": errorText, "finalText": finalText}, true)
	})
}

func (run *agentRun) activity(eventType, message, delta, toolName string, isError bool, data map[string]any, persist bool) {
	activity := AgentActivity{RunID: run.id, At: nowUTC(), Type: eventType, Message: message, Delta: delta, ToolName: toolName, IsError: isError, Data: data}
	run.manager.emit("agent:event", activity)
	if persist {
		_ = run.manager.store.AppendAgentEvent(context.Background(), run.id, eventType, message, data)
	}
}

func (run *agentRun) send(payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	run.writeMu.Lock()
	defer run.writeMu.Unlock()
	_, err = run.stdin.Write(append(encoded, '\n'))
	return err
}

func (run *agentRun) handleHostTool(frame map[string]any) {
	requestID, _ := frame["id"].(string)
	toolName, _ := frame["toolName"].(string)
	arguments, _ := frame["arguments"].(map[string]any)
	message, activityData := hostToolActivity(toolName, arguments)
	activityData["requestId"] = requestID
	run.activity("host_tool_start", message, "", toolName, false, activityData, true)
	run.toolMu.Lock()
	result := ""
	var err error
	if run.cancelRequested.Load() {
		err = context.Canceled
	} else {
		result, err = run.executeHostTool(toolName, arguments)
	}
	run.toolMu.Unlock()
	response := map[string]any{
		"type": "host_tool_result", "id": requestID,
		"result": map[string]any{"content": []map[string]any{{"type": "text", "text": result}}},
	}
	if err != nil {
		response["isError"] = true
		response["result"] = map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}}
	}
	if sendErr := run.send(response); sendErr != nil && err == nil {
		err = sendErr
	}
	endData := make(map[string]any, len(activityData)+1)
	for key, value := range activityData {
		endData[key] = value
	}
	endData["error"] = errorString(err)
	run.activity("host_tool_end", message, "", toolName, err != nil, endData, true)
}

func hostToolActivity(name string, arguments map[string]any) (string, map[string]any) {
	data := map[string]any{}
	pathValue := requiredString(arguments, "path")
	if pathValue != "" {
		data["path"] = filepath.ToSlash(pathValue)
	}
	if query := requiredString(arguments, "query"); query != "" {
		data["query"] = query
	}
	if content, ok := arguments["content"].(string); ok {
		data["bytes"] = len(content)
	}
	if replacement, ok := arguments["newText"].(string); ok {
		data["bytes"] = len(replacement)
	}
	label := map[string]string{
		"workspace_list":    "Reviewed project files",
		"workspace_read":    "Read",
		"workspace_write":   "Wrote",
		"workspace_replace": "Edited",
		"workspace_search":  "Searched project",
		"workspace_diff":    "Reviewed changes",
		"mod_validate":      "Validated mod",
		"mod_manifest":      "Reviewed mod manifest",
	}[name]
	if label == "" {
		label = name
	}
	if pathValue != "" {
		label += " " + filepath.ToSlash(pathValue)
	}
	return label, data
}

func (run *agentRun) executeHostTool(name string, arguments map[string]any) (string, error) {
	filesRoot := run.workspace.FilesRoot
	var value any
	var err error
	switch name {
	case "workspace_list":
		value, err = modkit.ListWorkspaceFiles(filesRoot)
	case "workspace_read":
		value, err = modkit.ReadWorkspaceText(filesRoot, requiredString(arguments, "path"))
	case "workspace_write":
		err = modkit.WriteWorkspaceText(filesRoot, requiredString(arguments, "path"), requiredString(arguments, "content"))
		if err == nil {
			value = map[string]any{"written": true}
		}
	case "workspace_replace":
		all, _ := arguments["all"].(bool)
		value, err = modkit.ReplaceWorkspaceText(filesRoot, requiredString(arguments, "path"), requiredString(arguments, "oldText"), requiredString(arguments, "newText"), all)
	case "workspace_search":
		maxResults := 200
		if number, ok := arguments["maxResults"].(float64); ok && number > 0 && number <= 1000 {
			maxResults = int(number)
		}
		value, err = modkit.SearchWorkspace(filesRoot, requiredString(arguments, "query"), maxResults)
	case "workspace_diff":
		var manifest modkit.WorkspaceManifest
		manifest, err = modkit.ReadWorkspaceManifest(run.workspace.Root)
		if err == nil {
			value, err = modkit.DiffWorkspace(run.workspace.SourcePath, filesRoot, manifest.Files)
		}
	case "mod_validate":
		validation := modkit.ValidateWorkspace(filesRoot)
		value = validation
		err = run.manager.store.SetWorkspaceValidation(context.Background(), run.workspace.ID, validation)
	case "mod_manifest":
		value = run.item.Manifest
	default:
		err = fmt.Errorf("unknown host tool: %s", name)
	}
	if err != nil {
		return "", err
	}
	if name == "workspace_write" || name == "workspace_replace" {
		_ = run.manager.store.TouchWorkspace(context.Background(), run.workspace.ID)
	}
	if text, ok := value.(string); ok {
		if len(text) > maxHostToolResponseBytes {
			return "", fmt.Errorf("tool response exceeds %d bytes", maxHostToolResponseBytes)
		}
		return text, nil
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	if len(encoded) > maxHostToolResponseBytes {
		return "", fmt.Errorf("tool response exceeds %d bytes; narrow the request", maxHostToolResponseBytes)
	}
	return string(encoded), nil
}

func hostToolDefinitions() []map[string]any {
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	stringField := map[string]any{"type": "string"}
	return []map[string]any{
		{"name": "workspace_list", "label": "List workspace files", "description": "List editable mod files with hashes and sizes.", "loadMode": "essential", "parameters": object(map[string]any{})},
		{"name": "workspace_read", "label": "Read workspace file", "description": "Read one UTF-8 text file inside the editable workspace.", "loadMode": "essential", "parameters": object(map[string]any{"path": stringField}, "path")},
		{"name": "workspace_write", "label": "Write workspace file", "description": "Create or replace one UTF-8 text file inside the editable workspace.", "loadMode": "essential", "parameters": object(map[string]any{"path": stringField, "content": stringField}, "path", "content")},
		{"name": "workspace_replace", "label": "Replace workspace text", "description": "Replace an exact text occurrence in one workspace file; all defaults false.", "loadMode": "essential", "parameters": object(map[string]any{"path": stringField, "oldText": stringField, "newText": stringField, "all": map[string]any{"type": "boolean"}}, "path", "oldText", "newText")},
		{"name": "workspace_search", "label": "Search workspace", "description": "Search text files in the workspace and return path:line matches.", "loadMode": "essential", "parameters": object(map[string]any{"query": stringField, "maxResults": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}}, "query")},
		{"name": "workspace_diff", "label": "Review workspace diff", "description": "Return added, modified, and deleted files with bounded text patches.", "loadMode": "essential", "parameters": object(map[string]any{})},
		{"name": "mod_validate", "label": "Validate BeamNG mod", "description": "Run safe-path, JSON5, packaging, and category-aware workspace validation.", "loadMode": "essential", "parameters": object(map[string]any{})},
		{"name": "mod_manifest", "label": "Inspect mod manifest", "description": "Return the analyzed source manifest, variants, structural metrics, and issues.", "loadMode": "essential", "parameters": object(map[string]any{})},
	}
}

func resolveOMPPath(configured string) (string, error) {
	candidates := []string{strings.TrimSpace(configured)}
	if discovered, err := exec.LookPath("omp"); err == nil {
		candidates = append(candidates, discovered)
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		candidates = append(candidates, filepath.Join(local, "omp", "omp.exe"))
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("OMP CLI was not found; install OMP or set ompPath in config.json")
}

func (decoder *rpcDecoder) Read() (map[string]any, error) {
	for {
		line, err := decoder.reader.ReadBytes('\n')
		if err != nil && len(line) == 0 {
			return nil, err
		}
		if len(line) > decoder.maxFrame+1 {
			return nil, fmt.Errorf("RPC frame exceeds %d bytes", decoder.maxFrame)
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			if err != nil {
				return nil, err
			}
			continue
		}
		if !utf8.Valid(line) {
			return nil, errors.New("RPC frame is not valid UTF-8")
		}
		var frame map[string]any
		if decodeErr := json.Unmarshal(line, &frame); decodeErr != nil {
			return nil, decodeErr
		}
		if frameType(frame) != "rpc_chunk" {
			if decoder.chunk != nil {
				return nil, errors.New("RPC chunk sequence was interrupted")
			}
			return frame, nil
		}
		complete, chunkErr := decoder.consumeChunk(frame)
		if chunkErr != nil {
			return nil, chunkErr
		}
		if complete != nil {
			return complete, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func (decoder *rpcDecoder) consumeChunk(frame map[string]any) (map[string]any, error) {
	id, _ := frame["chunkId"].(string)
	index := int(numberValue(frame["index"]))
	count := int(numberValue(frame["count"]))
	byteLength := int(numberValue(frame["byteLength"]))
	data, _ := frame["data"].(string)
	if id == "" || count < 1 || count > 4096 || index < 0 || index >= count || byteLength < 1 || byteLength > decoder.maxReassembled {
		return nil, errors.New("invalid RPC chunk metadata")
	}
	if decoder.chunk == nil {
		if index != 0 {
			return nil, errors.New("RPC chunk sequence did not start at zero")
		}
		decoder.chunk = &rpcChunkState{id: id, count: count, byteLength: byteLength}
	}
	chunk := decoder.chunk
	if chunk.id != id || chunk.count != count || chunk.byteLength != byteLength || chunk.next != index {
		return nil, errors.New("RPC chunk sequence mismatch")
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, err
	}
	if chunk.data.Len()+len(decoded) > decoder.maxReassembled {
		return nil, errors.New("RPC reassembly limit exceeded")
	}
	_, _ = chunk.data.Write(decoded)
	chunk.next++
	if chunk.next != chunk.count {
		return nil, nil
	}
	if chunk.data.Len() != chunk.byteLength {
		return nil, errors.New("RPC reassembled byte length mismatch")
	}
	payload := append([]byte(nil), chunk.data.Bytes()...)
	decoder.chunk = nil
	if !utf8.Valid(payload) {
		return nil, errors.New("RPC reassembled payload is not valid UTF-8")
	}
	var result map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (decoder *rpcDecoder) applyReady(frame map[string]any) {
	if value := int(numberValue(frame["maxFrameBytes"])); value > 0 && value <= maxRPCFrameBytes {
		decoder.maxFrame = value
	}
	if value := int(numberValue(frame["maxReassembledFrameBytes"])); value > 0 && value <= maxRPCReassembledBytes {
		decoder.maxReassembled = value
	}
}

func supportsProtocolV2(frame map[string]any) bool {
	versions, _ := frame["supportedProtocolVersions"].([]any)
	for _, version := range versions {
		if numberValue(version) == 2 {
			return true
		}
	}
	return false
}

func frameType(frame map[string]any) string {
	value, _ := frame["type"].(string)
	return value
}

func extractAssistantDelta(frame map[string]any) string {
	event, _ := frame["assistantMessageEvent"].(map[string]any)
	eventType, _ := event["type"].(string)
	if eventType != "text_delta" {
		return ""
	}
	delta, _ := event["delta"].(string)
	return delta
}

func nestedString(root map[string]any, keys ...string) string {
	var current any = root
	for _, key := range keys {
		mapped, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mapped[key]
	}
	value, _ := current.(string)
	return value
}

func activityMessage(frame map[string]any) string {
	for _, key := range []string{"message", "error", "reason"} {
		if value, ok := frame[key].(string); ok {
			return value
		}
	}
	return frameType(frame)
}

func rpcErrorText(frame map[string]any) string {
	if value, ok := frame["error"].(string); ok && value != "" {
		return value
	}
	if data, ok := frame["data"].(map[string]any); ok {
		if value, ok := data["error"].(string); ok && value != "" {
			return value
		}
	}
	return "OMP rejected the RPC command"
}

func compactFrame(frame map[string]any) map[string]any {
	result := map[string]any{"type": frameType(frame)}
	for _, key := range []string{"toolName", "toolCallId", "command", "success", "isTerminal", "reason"} {
		if value, exists := frame[key]; exists {
			result[key] = value
		}
	}
	return result
}

func requiredString(arguments map[string]any, key string) string {
	value, _ := arguments[key].(string)
	return value
}

func numberValue(value any) float64 {
	switch number := value.(type) {
	case float64:
		return number
	case int:
		return float64(number)
	default:
		return 0
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
