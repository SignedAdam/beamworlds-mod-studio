package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	modkit "github.com/SignedAdam/beamworlds-modkit"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	maxAgentPromptBytes       = 32 << 10
	maxRPCFrameBytes          = 2 << 20
	maxRPCReassembledBytes    = 64 << 20
	maxPersistedAgentText     = 4 << 20
	maxHostToolResponseBytes  = 8 << 20
	maxWorkspaceReadPageBytes = 256 << 10
	hostToolDrainTimeout      = 1 * time.Second
	launchWaitTimeout         = 1 * time.Second
)

const virgilGameTestingGuidance = `Live BeamNG diagnosis and testing:
- When someone reports a broken mod, console errors, or problems while playing, call game_log before guessing. Read the error and nearby stack trace, then inspect the named workspace files. An extension inventory or another mod's name in a log is not evidence this mod caused the failure.
- Use game_source to check API signatures and extension lifecycle behavior in the installed BeamNG Lua before inventing compatibility fixes. Paths are relative to its lua directory; query searches literal source text. It is read-only: do not copy base-game code or assets into the mod.
- For an ongoing live-game reproduction, save the game_log cursor before the action and read again with that cursor afterward. Check rotation, byte ranges, and output bounds; old errors and missing or empty logs do not prove the current build works.
- When live testing a build, use mod_game_test to validate, export, and exercise it in an isolated BeamNG session. Inspect the returned fresh log AND level/vehicle/simulation observations after every test. Fix the root cause, validate, and retest changed builds before reporting the issue resolved.
- Do not remove features, swallow exceptions, or suppress diagnostics to make a test look clean. Distinguish the mod's errors from unrelated engine or environment messages using concrete evidence.
- Never interrupt an already-running player session. Read its live log instead; if a separate isolated test is needed, explain that the game must be closed first.
- Report exactly what was exercised. Vehicle spawn and simulation are not a driving or crash test. Name the tested export's SHA-256; do not claim a later, untested edit was tested.
- Your workspace edits are the user's library mod: Studio updates it automatically shortly after you write files, and keeps earlier states in Versions so the user can restore them. Never tell the user to export, import, install, or copy a ZIP to use your changes. Use mod_export only when they ask for a separate ZIP, for example to share it.`

const virgilGitCommitGuidance = `Git commit guidance (conditional):
Follow these rules only when the person asks you to commit completed work or committing is an established part of the current request. Do not turn ordinary edits into automatic commits. This guidance is prompt-only: do not add application-side Git behavior or make remote calls.

- Repository detection: First determine whether the project is inside a real conventional Git repository. Use the installed Git implementation and the editable project root, WorkspaceRecord.FilesRoot (workspace/files), as the project/repository root; its .git directory or file and normal Git metadata must be present and usable. Do not mistake the metadata-containing workspace root or only a parent repository for this project's repository.
- Status and diff inspection: Before committing, inspect git status and the relevant diff. Review staged and unstaged changes as needed, including git diff and git diff --cached, so you understand exactly what would be included.
- Completed grouping: Select only one coherent, completed change per commit. A coherent change may span all files required to deliver one outcome, but unrelated edits must be excluded.
- Deliberate staging and exclusions: Identify the intended paths and stage only those paths deliberately; never stage broadly merely for convenience or use a broad add shortcut. Exclude unrelated edits, generated or build output, temporary files, credentials, tokens, private configuration, and any other secret material.
- Boundary: Commit at a sensible completed boundary—not after every individual file edit and not by postponing all work into one giant final dump.
- Message: Use a concise imperative subject that communicates the change. Add a body when the reason, tradeoff, or important context is not obvious from the subject. These are the only commit-subject examples:
  - feat(vehicle): add adjustable rear suspension
  - fix(jbeam): correct malformed wheel node references
  - refactor(lua): simplify boost controller state handling
- Result and failure: After requesting the commit, rely on Git's result and inspect the resulting state as appropriate. Never claim success unless Git confirms the commit succeeded. On failure, report the actual failure, leave the person's work intact, explain what remains to be done, and never pretend that a commit exists.
- No repository: If no Git repository is present, clearly say that you cannot create a Git commit there; do not simulate one.
- No relevant change: If a repository exists but there are no relevant changes, say there is nothing to commit; do not create an empty or invented commit.
- User control: Never push or force-push, amend, rebase, reset, use force-updating operations, or otherwise rewrite history unless the person explicitly requests that exact operation. A commit request does not implicitly authorize any of those actions.`

type AgentActivity struct {
	RunID       string         `json:"runId"`
	SessionID   string         `json:"sessionId"`
	WorkspaceID string         `json:"workspaceId"`
	At          string         `json:"at"`
	Type        string         `json:"type"`
	Message     string         `json:"message,omitempty"`
	Delta       string         `json:"delta,omitempty"`
	ToolName    string         `json:"toolName,omitempty"`
	IsError     bool           `json:"isError,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
}

type AgentManager struct {
	store                    *Store
	config                   AppConfig
	runtime                  *managedAIRuntime
	service                  *AppService
	emit                     func(string, any)
	mu                       sync.Mutex
	runs                     map[string]*agentRun
	busySessions             map[string]bool
	deletingSessions         map[string]bool
	sessionLocks             map[string]*sync.Mutex
	workspaceToolMu          map[string]*sync.Mutex
	launchCancels            map[string]context.CancelFunc
	sessionMetricMu          sync.Mutex
	sessionMutationBaselines map[string]map[string]string
	launches                 sync.WaitGroup
	shuttingDown             atomic.Bool
}
type agentRun struct {
	id                string
	sessionID         string
	workspace         WorkspaceRecord
	item              LibraryItem
	prompt            string
	cmd               *exec.Cmd
	stdin             io.WriteCloser
	decoder           *rpcDecoder
	cancel            context.CancelFunc
	runCtx            context.Context
	sessionNetChanges bool
	manager           *AgentManager
	persist           bool

	writeMu          sync.Mutex
	protocolV2       atomic.Bool
	toolMu           sync.Mutex
	toolGateMu       sync.Mutex
	toolWG           sync.WaitGroup
	toolsClosed      bool
	toolRequestMu    sync.Mutex
	toolRequests     map[string]bool
	toolContexts     map[string]context.Context
	toolCancels      map[string]context.CancelFunc
	textMu           sync.Mutex
	text             strings.Builder
	activityMu       sync.Mutex
	finish           sync.Once
	processOnce      sync.Once
	processDone      chan struct{}
	processErrMu     sync.Mutex
	processErr       error
	finalizerStarted atomic.Bool
	hardCancelOnce   sync.Once
	failureMu        sync.Mutex
	failureErr       error
	cancelRequested  atomic.Bool
	discarded        atomic.Bool
	terminalSignal   atomic.Bool
	done             chan struct{}
}

type rpcDecoder struct {
	reader         *bufio.Reader
	maxFrame       int
	maxReassembled int
	chunk          *rpcChunkState
	pending        []map[string]any
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
		store: store, config: config, emit: emit, runtime: newManagedAIRuntime(config),
		runs: map[string]*agentRun{}, busySessions: map[string]bool{},
		deletingSessions: map[string]bool{}, sessionLocks: map[string]*sync.Mutex{},
		workspaceToolMu: map[string]*sync.Mutex{}, launchCancels: map[string]context.CancelFunc{},
		sessionMutationBaselines: map[string]map[string]string{},
	}
}
func agentSelectionArguments(settings agentLaunchSettings) []string {
	if !settings.SelectModel {
		return nil
	}
	provider := agentProvider(settings.Profile)
	if provider == "" {
		return nil
	}
	model := strings.TrimSpace(settings.Model)
	if model == "" {
		return []string{"--provider", provider}
	}
	if !strings.Contains(model, "/") {
		model = provider + "/" + model
	}
	return []string{"--model", model}
}

func agentProvider(profile string) string {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "chatgpt":
		return "openai-codex"
	case "claude", "anthropic":
		return "anthropic"
	case "openrouter":
		return "openrouter"
	case "openai":
		return "openai"
	default:
		return ""
	}
}

func validateAgentPrompt(prompt string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", errors.New("agent goal is required")
	}
	if len(prompt) > maxAgentPromptBytes {
		return "", fmt.Errorf("agent goal exceeds %d bytes", maxAgentPromptBytes)
	}
	return prompt, nil
}

func publicAIError(err error) error {
	if err == nil {
		return nil
	}
	message := publicAIMessage(err.Error())
	if message == err.Error() {
		return err
	}
	return errors.New(message)
}

func agentLaunchArguments(workspaceRoot, contextPath, resumeSessionID, sessionDir string, settings agentLaunchSettings) []string {
	arguments := []string{"--mode", "rpc", "--cwd", workspaceRoot}
	if sessionDir = strings.TrimSpace(sessionDir); sessionDir != "" {
		arguments = append(arguments, "--session-dir", sessionDir)
	}
	if resumeSessionID = strings.TrimSpace(resumeSessionID); resumeSessionID != "" {
		arguments = append(arguments, "--resume", resumeSessionID)
	}
	arguments = append(arguments,
		"--tools", "todo",
		"--no-extensions",
		"--no-skills",
		"--no-rules",
		"--no-lsp",
		"--no-pty",
		"--max-time", "30m",
	)
	if contextPath != "" {
		arguments = append(arguments, "--append-system-prompt", contextPath)
	}
	return append(arguments, agentSelectionArguments(settings)...)
}

func (manager *AgentManager) sessionMutex(sessionID string) *sync.Mutex {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if lock := manager.sessionLocks[sessionID]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	manager.sessionLocks[sessionID] = lock
	return lock
}
func (manager *AgentManager) reserveSession(sessionID string) error {
	_, err := manager.reserveSessionContext(context.Background(), sessionID)
	return err
}

func (manager *AgentManager) reserveSessionContext(parent context.Context, sessionID string) (context.Context, error) {
	if parent == nil {
		parent = context.Background()
	}
	sessionID = strings.TrimSpace(sessionID)
	if err := parent.Err(); err != nil {
		return nil, err
	}
	launchContext, cancel := context.WithCancel(parent)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.shuttingDown.Load() {
		cancel()
		return nil, errors.New("agent manager is shutting down")
	}
	if manager.deletingSessions[sessionID] {
		cancel()
		return nil, errors.New("Virgil session is being deleted")
	}
	if manager.busySessions[sessionID] {
		cancel()
		return nil, errors.New("a Virgil run is already active for this session")
	}
	manager.busySessions[sessionID] = true
	if manager.launchCancels == nil {
		manager.launchCancels = make(map[string]context.CancelFunc)
	}
	manager.launchCancels[sessionID] = cancel
	manager.launches.Add(1)
	return launchContext, nil
}

func (manager *AgentManager) finishLaunch() {
	manager.launches.Done()
}

func (manager *AgentManager) waitForLaunches(timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = launchWaitTimeout
	}
	finished := make(chan struct{})
	go func() {
		manager.launches.Wait()
		close(finished)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-finished:
		return true
	case <-timer.C:
		return false
	}
}

func (manager *AgentManager) releaseSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	manager.mu.Lock()
	delete(manager.busySessions, sessionID)
	cancel := manager.launchCancels[sessionID]
	delete(manager.launchCancels, sessionID)
	manager.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (manager *AgentManager) workspaceToolMutex(workspaceID string) *sync.Mutex {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if lock := manager.workspaceToolMu[workspaceID]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	manager.workspaceToolMu[workspaceID] = lock
	return lock
}

func (run *agentRun) admitHostTool() bool {
	run.toolGateMu.Lock()
	defer run.toolGateMu.Unlock()
	if run.toolsClosed {
		return false
	}
	run.toolWG.Add(1)
	return true
}

func (run *agentRun) finishHostTool() {
	run.toolWG.Done()
}

func (run *agentRun) stopAdmittingHostTools() {
	run.toolGateMu.Lock()
	run.toolsClosed = true
	run.toolGateMu.Unlock()
}

func (run *agentRun) hostToolBaseContext() context.Context {
	if run.runCtx != nil {
		return run.runCtx
	}
	return context.Background()
}

func (run *agentRun) drainHostTools() bool {
	run.stopAdmittingHostTools()
	run.cancelHostTools()
	drained := make(chan struct{})
	go func() {
		run.toolWG.Wait()
		close(drained)
	}()
	timer := time.NewTimer(hostToolDrainTimeout)
	defer timer.Stop()
	select {
	case <-drained:
		return true
	case <-timer.C:
		run.suppressActivity()
		return false
	}
}

func (run *agentRun) registerHostTool(requestID string) {
	if requestID == "" {
		return
	}
	requestContext, cancel := context.WithCancel(run.hostToolBaseContext())
	run.toolRequestMu.Lock()
	if run.toolRequests == nil {
		run.toolRequests = make(map[string]bool)
	}
	if run.toolContexts == nil {
		run.toolContexts = make(map[string]context.Context)
	}
	if run.toolCancels == nil {
		run.toolCancels = make(map[string]context.CancelFunc)
	}
	if previous := run.toolCancels[requestID]; previous != nil {
		previous()
	}
	run.toolRequests[requestID] = false
	run.toolContexts[requestID] = requestContext
	run.toolCancels[requestID] = cancel
	run.toolRequestMu.Unlock()
}

func (run *agentRun) cancelHostTool(requestID string) {
	if requestID == "" {
		return
	}
	run.toolRequestMu.Lock()
	if _, active := run.toolRequests[requestID]; active {
		run.toolRequests[requestID] = true
	}
	cancel := run.toolCancels[requestID]
	run.toolRequestMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (run *agentRun) cancelHostTools() {
	run.toolRequestMu.Lock()
	for requestID := range run.toolRequests {
		run.toolRequests[requestID] = true
	}
	cancels := make([]context.CancelFunc, 0, len(run.toolCancels))
	for _, cancel := range run.toolCancels {
		if cancel != nil {
			cancels = append(cancels, cancel)
		}
	}
	run.toolRequestMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (run *agentRun) hostToolContext(requestID string) context.Context {
	if requestID != "" {
		run.toolRequestMu.Lock()
		requestContext := run.toolContexts[requestID]
		run.toolRequestMu.Unlock()
		if requestContext != nil {
			return requestContext
		}
	}
	return run.hostToolBaseContext()
}

func (run *agentRun) finishHostToolRequest(requestID string) {
	if requestID == "" {
		return
	}
	run.toolRequestMu.Lock()
	cancel := run.toolCancels[requestID]
	delete(run.toolRequests, requestID)
	delete(run.toolContexts, requestID)
	delete(run.toolCancels, requestID)
	run.toolRequestMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// beginHostToolExecution is the cancellation boundary for one host tool call.
// A cancellation accepted before this method returns prevents the operation
// from starting; operations that have started receive the request context and
// must stop at their next I/O or traversal checkpoint.
func (run *agentRun) beginHostToolExecution(requestID string) bool {
	if requestID == "" {
		return true
	}
	run.toolRequestMu.Lock()
	cancelled := run.toolRequests[requestID]
	delete(run.toolRequests, requestID)
	run.toolRequestMu.Unlock()
	return !cancelled
}

func lockMutexContext(ctx context.Context, lock *sync.Mutex) error {
	if lock == nil {
		return errors.New("host tool lock is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if lock.TryLock() {
			return nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (run *agentRun) armHardCancellation(delay time.Duration) {
	if run.cancel == nil {
		return
	}
	run.hardCancelOnce.Do(func() {
		go func() {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			if run.processDone == nil {
				<-timer.C
				run.cancel()
				return
			}
			select {
			case <-run.processDone:
			case <-timer.C:
				run.cancel()
			}
		}()
	})
}

func (run *agentRun) sendAbortAndCloseInput() {
	if run.stdin == nil {
		return
	}
	sent := make(chan struct{})
	go func() {
		_ = run.send(map[string]any{"id": "abort-" + run.id, "type": "abort"})
		close(sent)
	}()
	timer := time.NewTimer(250 * time.Millisecond)
	select {
	case <-sent:
		if !timer.Stop() {
			<-timer.C
		}
	case <-timer.C:
	}
	_ = run.stdin.Close()
}

func (run *agentRun) markProcessDone() {
	if run.processDone == nil {
		return
	}
	run.processOnce.Do(func() { close(run.processDone) })
}

func (run *agentRun) setFailure(err error) {
	if err == nil {
		return
	}
	run.failureMu.Lock()
	if run.failureErr == nil {
		run.failureErr = err
	}
	run.failureMu.Unlock()
	if run.cancel != nil {
		run.cancel()
	}
}
func (run *agentRun) reapProcess() {
	err := run.cmd.Wait()
	run.processErrMu.Lock()
	run.processErr = err
	run.processErrMu.Unlock()
	run.markProcessDone()
}

func (run *agentRun) processError() error {
	run.processErrMu.Lock()
	defer run.processErrMu.Unlock()
	return run.processErr
}
func (run *agentRun) suppressActivity() {
	run.activityMu.Lock()
	run.discarded.Store(true)
	run.activityMu.Unlock()
}

func (run *agentRun) closeForTerminal() {
	run.terminalSignal.Store(true)
	if run.stdin != nil {
		_ = run.stdin.Close()
	}
}
func (run *agentRun) failure() error {
	run.failureMu.Lock()
	defer run.failureMu.Unlock()
	return run.failureErr
}

func waitForAgentProcess(run *agentRun, graceful time.Duration) {
	if run == nil || run.processDone == nil {
		return
	}
	if graceful <= 0 {
		graceful = hostToolDrainTimeout
	}
	timer := time.NewTimer(graceful)
	select {
	case <-run.processDone:
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		return
	case <-timer.C:
	}
	if run.cancel != nil {
		run.cancel()
	}
	hardTimer := time.NewTimer(graceful)
	defer hardTimer.Stop()
	select {
	case <-run.processDone:
	case <-hardTimer.C:
	}
}

func (manager *AgentManager) loadAgentContext(ctx context.Context, workspaceID string, settings agentLaunchSettings) (WorkspaceRecord, LibraryItem, []KnowledgeDocument, error) {
	workspace, err := manager.store.GetWorkspace(ctx, strings.TrimSpace(workspaceID))
	if err != nil {
		return WorkspaceRecord{}, LibraryItem{}, nil, err
	}
	item, err := manager.store.GetLibraryItem(ctx, workspace.EntityID)
	if err != nil {
		return WorkspaceRecord{}, LibraryItem{}, nil, err
	}
	documents, err := knowledgeFor(item.Kind, item.Manifest.ContentTags)
	if err != nil {
		return WorkspaceRecord{}, LibraryItem{}, nil, err
	}
	switch settings.ContextMode {
	case "focused":
		if len(documents) > 1 {
			documents = documents[1:]
		}
	case "deep":
		documents, err = knowledgeFor(modkit.KindMixed, []string{"vehicle", "map", "ui", "script"})
		if err != nil {
			return WorkspaceRecord{}, LibraryItem{}, nil, err
		}
	}
	return workspace, item, documents, nil
}

func (manager *AgentManager) markPersistedLaunchFailure(sessionID, runID string, launchErr error) {
	errorText := publicAIMessage(errorString(launchErr))
	_ = manager.store.FinishAgentRun(context.Background(), runID, "failed", "", errorText)
	_ = manager.store.SetVirgilSessionStatus(context.Background(), sessionID, "error", errorText)
	manager.releaseSession(sessionID)
}

func (manager *AgentManager) StartSession(ctx context.Context, workspaceID, prompt, userTitle string, settings agentLaunchSettings) (VirgilSessionRecord, error) {
	prompt, err := validateAgentPrompt(prompt)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	userTitle, err = validateVirgilSessionTitle(userTitle, false)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	workspace, item, documents, err := manager.loadAgentContext(ctx, workspaceID, settings)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	sessionID, err := modkit.NewID()
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	runID, err := modkit.NewID()
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	sessionLock := manager.sessionMutex(sessionID)
	sessionLock.Lock()
	defer sessionLock.Unlock()
	launchContext, err := manager.reserveSessionContext(ctx, sessionID)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	defer manager.finishLaunch()
	releaseReservation := true
	defer func() {
		if releaseReservation {
			manager.releaseSession(sessionID)
		}
	}()
	contextPath, err := manager.writeAgentContextContext(launchContext, runID, workspace, item, documents)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	startedAt := nowUTC()
	session := VirgilSessionRecord{
		ID: sessionID, WorkspaceID: workspace.ID, Profile: normalizeVirgilAIProfile(settings.Profile),
		UserTitle: userTitle, Status: "starting", CreatedAt: startedAt, UpdatedAt: startedAt,
	}
	record := AgentRunRecord{
		ID: runID, SessionID: sessionID, WorkspaceID: workspace.ID,
		Prompt: prompt, Status: "running", StartedAt: startedAt,
	}
	if err := manager.store.CreateVirgilSessionAndRun(launchContext, session, record); err != nil {
		return VirgilSessionRecord{}, err
	}
	if _, err := manager.launchRunContext(launchContext, workspace, item, session, record, contextPath, settings, "", true, true); err != nil {
		if !manager.shuttingDown.Load() {
			_ = manager.store.DeleteVirgilSession(context.Background(), sessionID)
		}
		return VirgilSessionRecord{}, err
	}
	releaseReservation = false
	return manager.store.GetVirgilSession(ctx, sessionID)
}
func (manager *AgentManager) SendMessage(ctx context.Context, sessionID, prompt string, settings agentLaunchSettings) (AgentRunRecord, error) {
	prompt, err := validateAgentPrompt(prompt)
	if err != nil {
		return AgentRunRecord{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return AgentRunRecord{}, errors.New("Virgil session ID is required")
	}
	lock := manager.sessionMutex(sessionID)
	lock.Lock()
	defer lock.Unlock()
	session, err := manager.store.GetVirgilSession(ctx, sessionID)
	if err != nil {
		return AgentRunRecord{}, err
	}
	if session.Status != "idle" {
		return AgentRunRecord{}, fmt.Errorf("Virgil session is %s; only idle sessions accept a message", session.Status)
	}
	workspace, item, documents, err := manager.loadAgentContext(ctx, session.WorkspaceID, settings)
	if err != nil {
		return AgentRunRecord{}, err
	}
	runID, err := modkit.NewID()
	if err != nil {
		return AgentRunRecord{}, err
	}
	launchContext, err := manager.reserveSessionContext(ctx, session.ID)
	if err != nil {
		return AgentRunRecord{}, err
	}
	defer manager.finishLaunch()
	releaseReservation := true
	defer func() {
		if releaseReservation {
			manager.releaseSession(session.ID)
		}
	}()
	contextPath, err := manager.writeAgentContextContext(launchContext, runID, workspace, item, documents)
	if err != nil {
		return AgentRunRecord{}, err
	}
	record := AgentRunRecord{
		ID: runID, SessionID: session.ID, WorkspaceID: session.WorkspaceID,
		Prompt: prompt, Status: "running", StartedAt: nowUTC(),
	}
	if err := manager.store.ReserveVirgilSessionRun(launchContext, session.ID, record); err != nil {
		return AgentRunRecord{}, err
	}
	if _, err := manager.launchRunContext(launchContext, workspace, item, session, record, contextPath, settings, session.OMPSessionID, true, true); err != nil {
		return AgentRunRecord{}, err
	}
	releaseReservation = false
	return record, nil
}

func (manager *AgentManager) ResumeSession(ctx context.Context, sessionID string, settings agentLaunchSettings) (VirgilSessionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return VirgilSessionRecord{}, errors.New("Virgil session ID is required")
	}
	lock := manager.sessionMutex(sessionID)
	lock.Lock()
	defer lock.Unlock()
	launchContext, err := manager.reserveSessionContext(ctx, sessionID)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	defer manager.finishLaunch()
	defer manager.releaseSession(sessionID)
	session, err := manager.store.BeginVirgilSessionResume(launchContext, sessionID)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	workspace, item, documents, err := manager.loadAgentContext(launchContext, session.WorkspaceID, settings)
	if err != nil {
		if !manager.shuttingDown.Load() {
			_ = manager.store.SetVirgilSessionStatus(context.Background(), sessionID, "error", err.Error())
		}
		return VirgilSessionRecord{}, err
	}
	contextID := "resume-" + sessionID
	contextPath, err := manager.writeAgentContextContext(launchContext, contextID, workspace, item, documents)
	if err != nil {
		if !manager.shuttingDown.Load() {
			_ = manager.store.SetVirgilSessionStatus(context.Background(), sessionID, "error", err.Error())
		}
		return VirgilSessionRecord{}, err
	}
	ephemeral := AgentRunRecord{ID: contextID, SessionID: session.ID, WorkspaceID: session.WorkspaceID}
	if _, err := manager.launchRunContext(launchContext, workspace, item, session, ephemeral, contextPath, settings, session.OMPSessionID, false, false); err != nil {
		if !manager.shuttingDown.Load() {
			_ = manager.store.SetVirgilSessionStatus(context.Background(), sessionID, "error", err.Error())
		}
		return VirgilSessionRecord{}, err
	}
	if manager.shuttingDown.Load() {
		return VirgilSessionRecord{}, errors.New("agent manager is shutting down")
	}
	if err := manager.store.SetVirgilSessionStatus(ctx, sessionID, "idle", ""); err != nil {
		return VirgilSessionRecord{}, err
	}
	return manager.store.GetVirgilSession(ctx, sessionID)
}

func (manager *AgentManager) RenameSession(ctx context.Context, sessionID, title string) (VirgilSessionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	lock := manager.sessionMutex(sessionID)
	lock.Lock()
	defer lock.Unlock()
	manager.mu.Lock()
	deleting := manager.deletingSessions[sessionID]
	manager.mu.Unlock()
	if deleting {
		return VirgilSessionRecord{}, errors.New("Virgil session is being deleted")
	}
	return manager.store.RenameVirgilSession(ctx, sessionID, title)
}

func (manager *AgentManager) stopRun(run *agentRun) {
	if run == nil {
		return
	}
	run.stopAdmittingHostTools()
	run.cancelHostTools()
	run.armHardCancellation(5 * time.Second)
	if run.cancelRequested.CompareAndSwap(false, true) {
		run.sendAbortAndCloseInput()
	}
	waitForAgentProcess(run, 5*time.Second)
	if run.finalizerStarted.CompareAndSwap(false, true) {
		run.complete("cancelled", nil)
	}
	if run.done != nil {
		timer := time.NewTimer(hostToolDrainTimeout + 250*time.Millisecond)
		select {
		case <-run.done:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}
func (manager *AgentManager) ForgetSession(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("Virgil session ID is required")
	}
	lock := manager.sessionMutex(sessionID)
	lock.Lock()
	defer lock.Unlock()
	session, err := manager.store.GetVirgilSession(ctx, sessionID)
	if err != nil {
		return err
	}
	sessionDir, err := manager.runtime.SessionDir()
	if err != nil {
		return err
	}
	manager.mu.Lock()
	manager.deletingSessions[sessionID] = true
	active := make([]*agentRun, 0, 1)
	for _, run := range manager.runs {
		if run.sessionID == sessionID {
			active = append(active, run)
		}
	}
	manager.mu.Unlock()
	for _, run := range active {
		manager.stopRun(run)
	}
	for _, run := range active {
		// Suppress any defensive late activity before removing the persisted rows.
		run.suppressActivity()
	}
	// Remove the runtime transcript first. If this fails, retain the database
	// identity so the caller can retry without leaving an orphaned transcript.
	if err := removeVirgilSessionTranscript(sessionDir, session.OMPSessionID); err != nil {
		manager.mu.Lock()
		delete(manager.deletingSessions, sessionID)
		manager.mu.Unlock()
		return err
	}
	if err := manager.store.DeleteVirgilSession(ctx, sessionID); err != nil {
		manager.mu.Lock()
		delete(manager.deletingSessions, sessionID)
		manager.mu.Unlock()
		return err
	}
	manager.mu.Lock()
	delete(manager.deletingSessions, sessionID)
	delete(manager.busySessions, sessionID)
	manager.mu.Unlock()
	manager.sessionMetricMu.Lock()
	delete(manager.sessionMutationBaselines, sessionID)
	manager.sessionMetricMu.Unlock()
	return nil
}

func (manager *AgentManager) launchRun(workspace WorkspaceRecord, item LibraryItem, session VirgilSessionRecord, record AgentRunRecord, contextPath string, settings agentLaunchSettings, resumeSessionID string, persist, sendPrompt bool) (*agentRun, error) {
	return manager.launchRunContext(context.Background(), workspace, item, session, record, contextPath, settings, resumeSessionID, persist, sendPrompt)
}

func (manager *AgentManager) launchRunContext(launchContext context.Context, workspace WorkspaceRecord, item LibraryItem, session VirgilSessionRecord, record AgentRunRecord, contextPath string, settings agentLaunchSettings, resumeSessionID string, persist, sendPrompt bool) (*agentRun, error) {
	if launchContext == nil {
		launchContext = context.Background()
	}
	failUnstarted := func(err error) (*agentRun, error) {
		publicErr := publicAIError(err)
		if persist && !manager.shuttingDown.Load() {
			manager.markPersistedLaunchFailure(session.ID, record.ID, publicErr)
		}
		return nil, publicErr
	}
	if err := launchContext.Err(); err != nil {
		return failUnstarted(err)
	}
	if manager.shuttingDown.Load() {
		return failUnstarted(errors.New("agent manager is shutting down"))
	}
	profile, err := managedAIRuntimeProfileForAgent(settings.Profile)
	if err != nil {
		return failUnstarted(err)
	}
	runContext, cancel := context.WithCancel(launchContext)
	arguments := agentLaunchArguments(workspace.FilesRoot, contextPath, resumeSessionID, "", settings)
	var command *exec.Cmd
	var sessionDir string
	sessionDir, err = manager.runtime.SessionDir()
	if err == nil {
		arguments = agentLaunchArguments(workspace.FilesRoot, contextPath, resumeSessionID, sessionDir, settings)
		command, err = manager.runtime.Command(runContext, arguments, launchCredentials(settings), profile)
	}
	if err != nil {
		cancel()
		return failUnstarted(err)
	}
	if err := runContext.Err(); err != nil {
		cancel()
		return failUnstarted(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return failUnstarted(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return failUnstarted(err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		cancel()
		return failUnstarted(err)
	}
	decoder := &rpcDecoder{reader: bufio.NewReaderSize(stdout, 64<<10), maxFrame: maxRPCFrameBytes, maxReassembled: maxRPCReassembledBytes}
	run := &agentRun{
		id: record.ID, sessionID: session.ID, workspace: workspace, item: item,
		prompt: record.Prompt, cmd: command, stdin: stdin, decoder: decoder,
		cancel: cancel, runCtx: runContext, manager: manager, persist: persist, done: make(chan struct{}),
		sessionNetChanges: len(session.Runs) == 0 && resumeSessionID == "",
		processDone:       make(chan struct{}),
	}
	startResult := make(chan error, 1)
	go func() {
		startResult <- command.Start()
	}()
	select {
	case startErr := <-startResult:
		if startErr != nil {
			cancel()
			return failUnstarted(fmt.Errorf("start Virgil AI runtime: %w", startErr))
		}
		manager.mu.Lock()
		if manager.shuttingDown.Load() || runContext.Err() != nil {
			manager.mu.Unlock()
			cancel()
			if command.Process != nil {
				_ = command.Process.Kill()
			}
			_ = command.Wait()
			if manager.shuttingDown.Load() {
				return failUnstarted(errors.New("agent manager is shutting down"))
			}
			return failUnstarted(runContext.Err())
		}
		manager.runs[run.id] = run
		delete(manager.launchCancels, session.ID)
		manager.mu.Unlock()
	case <-runContext.Done():
		cancel()
		go func() {
			if startErr := <-startResult; startErr == nil {
				if command.Process != nil {
					_ = command.Process.Kill()
				}
				_ = command.Wait()
			}
		}()
		return failUnstarted(runContext.Err())
	}
	if persist {
		go run.stderrLoop(stderr)
	} else {
		go func() { _, _ = io.Copy(io.Discard, stderr) }()
	}
	go run.reapProcess()
	failStarted := func(err error) (*agentRun, error) {
		run.cancel()
		waitForAgentProcess(run, 5*time.Second)
		run.complete("failed", err)
		return nil, publicAIError(err)
	}
	ready, err := readRPCFrameWithTimeout(decoder, 15*time.Second)
	if err != nil {
		return failStarted(fmt.Errorf("read Virgil runtime ready frame: %w", err))
	}
	if frameType(ready) != "ready" {
		return failStarted(fmt.Errorf("Virgil runtime returned %q before ready", frameType(ready)))
	}
	decoder.applyReady(ready)
	if persist {
		run.activity("ready", "Virgil AI runtime connected", "", "", false, nil, true)
	}
	state, err := run.bootstrap(ready, strings.TrimSpace(resumeSessionID))
	if err != nil {
		return failStarted(err)
	}
	if run.cancelRequested.Load() || launchContext.Err() != nil {
		return failStarted(context.Canceled)
	}
	if err := manager.store.SetVirgilSessionOMPState(context.Background(), session.ID, state.ID, state.Name); err != nil {
		return failStarted(err)
	}
	if strings.TrimSpace(session.Profile) == "" || (strings.TrimSpace(resumeSessionID) != "" && settings.SelectModel) {
		if err := manager.store.SetVirgilSessionProfile(context.Background(), session.ID, settings.Profile); err != nil {
			return failStarted(err)
		}
	}
	if !sendPrompt {
		run.finalizerStarted.Store(true)
		_ = stdin.Close()
		waitForAgentProcess(run, 5*time.Second)
		waitErr := run.processError()
		if waitErr != nil {
			closeErr := fmt.Errorf("close Virgil resume: %w", waitErr)
			run.complete("failed", closeErr)
			return nil, publicAIError(closeErr)
		}
		run.complete("complete", nil)
		return run, nil
	}
	if run.cancelRequested.Load() {
		return failStarted(context.Canceled)
	}
	if err := manager.store.SetVirgilSessionStatus(context.Background(), session.ID, "running", ""); err != nil {
		return failStarted(err)
	}
	run.finalizerStarted.Store(true)
	go run.readLoop()
	message := buildVirgilPromptMessage(record.Prompt, strings.TrimSpace(resumeSessionID) == "")
	if err := run.send(map[string]any{"id": "prompt-" + run.id, "type": "prompt", "message": message}); err != nil {
		return failStarted(err)
	}
	go run.waitLoop()
	return run, nil
}

func buildVirgilPromptMessage(prompt string, firstTurn bool) string {
	message := "Goal: " + prompt + "\n\nEdit only the provided workspace through the host tools. Use game_log and game_source for read-only game evidence. Inspect before editing. Do not create placeholders, copy third-party/base-game assets, or suppress failures. Before finishing, run mod_validate and workspace_diff, fix validation errors, and report exact changed files and remaining warnings."
	if firstTurn {
		message += "\n\nDuring this first turn, call session_set_title once with a concise 3–8 word title describing the work. Do not include punctuation-only or generic titles."
	}
	return message
}

func readRPCFrameWithTimeout(decoder *rpcDecoder, timeout time.Duration) (map[string]any, error) {
	type result struct {
		frame map[string]any
		err   error
	}
	channel := make(chan result, 1)
	go func() {
		frame, err := decoder.Read()
		channel <- result{frame: frame, err: err}
	}()
	select {
	case result := <-channel:
		return result.frame, result.err
	case <-time.After(timeout):
		return nil, errors.New("Virgil runtime did not become ready within 15 seconds")
	}
}

type rpcAwaitResult struct {
	frame    map[string]any
	deferred []map[string]any
	err      error
}

func awaitRPCResponse(decoder *rpcDecoder, requestID, command string, timeout time.Duration) (map[string]any, error) {
	channel := make(chan rpcAwaitResult, 1)
	go func() {
		deferred := []map[string]any{}
		for {
			frame, err := decoder.Read()
			if err != nil {
				channel <- rpcAwaitResult{deferred: deferred, err: err}
				return
			}
			if rpcResponseMatches(frame, requestID, command) {
				channel <- rpcAwaitResult{frame: frame, deferred: deferred}
				return
			}
			deferred = append(deferred, frame)
		}
	}()
	select {
	case result := <-channel:
		if len(result.deferred) > 0 {
			decoder.pending = append(result.deferred, decoder.pending...)
		}
		if result.err != nil {
			return nil, result.err
		}
		if err := rpcResponseError(result.frame, command); err != nil {
			return nil, err
		}
		return result.frame, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timed out waiting for correlated Virgil %s response", command)
	}
}

func rpcResponseMatches(frame map[string]any, requestID, command string) bool {
	if frameType(frame) != "response" {
		return false
	}
	if nestedString(frame, "id") != requestID {
		return false
	}
	responseCommand := nestedString(frame, "command")
	return responseCommand == "" || responseCommand == command
}

func rpcResponseError(frame map[string]any, command string) error {
	success, exists := frame["success"].(bool)
	if exists && !success {
		return fmt.Errorf("Virgil runtime rejected %s: %s", command, rpcErrorText(frame))
	}
	return nil
}

type ompSessionState struct {
	ID   string
	File string
	Name string
}

func extractOMPSessionState(frame map[string]any) ompSessionState {
	state := ompSessionState{}
	var visit func(map[string]any, int)
	visit = func(value map[string]any, depth int) {
		if depth > 4 {
			return
		}
		for key, raw := range value {
			switch strings.ToLower(key) {
			case "sessionid":
				if state.ID == "" {
					state.ID, _ = raw.(string)
				}
			case "sessionfile":
				if state.File == "" {
					state.File, _ = raw.(string)
				}
			case "sessionname":
				if state.Name == "" {
					state.Name, _ = raw.(string)
				}
			}
			if child, ok := raw.(map[string]any); ok {
				visit(child, depth+1)
			}
		}
	}
	visit(frame, 0)
	state.ID = strings.TrimSpace(state.ID)
	state.File = strings.TrimSpace(state.File)
	state.Name = strings.TrimSpace(state.Name)
	return state
}

func validateOMPSessionState(frame map[string]any, expectedID string) (ompSessionState, error) {
	state := extractOMPSessionState(frame)
	if state.ID == "" {
		return state, errors.New("Virgil runtime state did not include a session ID")
	}
	if expectedID != "" && state.ID != expectedID {
		return state, fmt.Errorf("Virgil resumed session ID %q does not match stored ID %q", state.ID, expectedID)
	}
	return state, nil
}
func (run *agentRun) bootstrap(ready map[string]any, expectedSessionID string) (ompSessionState, error) {
	if supportsProtocolV2(ready) {
		id := "protocol-" + run.id
		if _, err := run.sendAndAwait(map[string]any{"id": id, "type": "negotiate_protocol", "protocolVersion": 2}, id, "negotiate_protocol"); err != nil {
			return ompSessionState{}, err
		}
		run.protocolV2.Store(true)
	}
	toolsID := "tools-" + run.id
	if _, err := run.sendAndAwait(map[string]any{"id": toolsID, "type": "set_host_tools", "tools": hostToolDefinitions()}, toolsID, "set_host_tools"); err != nil {
		return ompSessionState{}, err
	}
	stateID := "state-" + run.id
	stateFrame, err := run.sendAndAwait(map[string]any{"id": stateID, "type": "get_state"}, stateID, "get_state")
	if err != nil {
		return ompSessionState{}, err
	}
	return validateOMPSessionState(stateFrame, expectedSessionID)
}

func (run *agentRun) sendAndAwait(payload map[string]any, requestID, command string) (map[string]any, error) {
	if err := run.send(payload); err != nil {
		return nil, err
	}
	return awaitRPCResponse(run.decoder, requestID, command, 15*time.Second)
}
func (run *agentRun) send(payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if run.decoder == nil {
		return errors.New("Virgil runtime decoder is unavailable")
	}
	if len(encoded) > run.decoder.maxFrame {
		return fmt.Errorf("RPC payload is %d bytes; negotiated single-frame limit is %d bytes", len(encoded), run.decoder.maxFrame)
	}
	run.writeMu.Lock()
	defer run.writeMu.Unlock()
	return writeRPCFrame(run.stdin, encoded)
}

func writeRPCFrame(writer io.Writer, frame []byte) error {
	payload := make([]byte, len(frame)+1)
	copy(payload, frame)
	payload[len(frame)] = '\n'
	for len(payload) > 0 {
		written, err := writer.Write(payload)
		if written > 0 {
			payload = payload[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (manager *AgentManager) Stop(runID string) bool {
	manager.mu.Lock()
	run := manager.runs[runID]
	manager.mu.Unlock()
	if run == nil {
		return false
	}
	run.stopAdmittingHostTools()
	run.cancelHostTools()
	if !run.cancelRequested.CompareAndSwap(false, true) {
		return true
	}
	run.sendAbortAndCloseInput()
	run.activity("cancelling", "Cancellation requested", "", "", false, nil, true)
	go func() {
		waitForAgentProcess(run, 5*time.Second)
		if run.finalizerStarted.CompareAndSwap(false, true) {
			run.complete("cancelled", nil)
		}
	}()
	return true
}

func (manager *AgentManager) StopAll() {
	manager.mu.Lock()
	manager.shuttingDown.Store(true)
	runs := make([]*agentRun, 0, len(manager.runs))
	for _, run := range manager.runs {
		runs = append(runs, run)
	}
	launchCancels := make([]context.CancelFunc, 0, len(manager.launchCancels))
	for _, cancel := range manager.launchCancels {
		if cancel != nil {
			launchCancels = append(launchCancels, cancel)
		}
	}
	manager.mu.Unlock()
	// Mark registered runs as cancelled before cancelling their launch
	// contexts so a process that exits immediately is still finalized as a
	// graceful shutdown.
	abortRuns := make([]*agentRun, 0, len(runs))
	for _, run := range runs {
		run.stopAdmittingHostTools()
		run.cancelHostTools()
		run.armHardCancellation(5 * time.Second)
		if run.cancelRequested.CompareAndSwap(false, true) {
			abortRuns = append(abortRuns, run)
		}
	}
	for _, cancel := range launchCancels {
		cancel()
	}
	// Atomic process start/registration means every process accepted before
	// shutdown is in this snapshot. Cancel those processes before waiting for
	// launch bookkeeping; a launch can otherwise be blocked writing bootstrap
	// RPC frames.
	for _, run := range abortRuns {
		run.sendAbortAndCloseInput()
	}
	manager.waitForLaunches(launchWaitTimeout)
	sessionIDs := map[string]bool{}
	for _, run := range runs {
		waitForAgentProcess(run, 5*time.Second)
		if run.finalizerStarted.CompareAndSwap(false, true) {
			run.complete("cancelled", nil)
		}
		if run.done != nil {
			timer := time.NewTimer(hostToolDrainTimeout + 250*time.Millisecond)
			select {
			case <-run.done:
			case <-timer.C:
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		if run.persist && run.sessionID != "" {
			sessionIDs[run.sessionID] = true
		}
	}
	for sessionID := range sessionIDs {
		_ = manager.store.SetVirgilSessionStatus(context.Background(), sessionID, "paused", "")
	}
}
func (manager *AgentManager) writeAgentContext(runID string, workspace WorkspaceRecord, item LibraryItem, documents []KnowledgeDocument) (string, error) {
	return manager.writeAgentContextContext(context.Background(), runID, workspace, item, documents)
}

func (manager *AgentManager) writeAgentContextContext(ctx context.Context, runID string, workspace WorkspaceRecord, item LibraryItem, documents []KnowledgeDocument) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	workspaceLock := manager.workspaceToolMutex(workspace.ID)
	if err := lockMutexContext(ctx, workspaceLock); err != nil {
		return "", err
	}
	defer workspaceLock.Unlock()
	if err := ctx.Err(); err != nil {
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
	builder.WriteString("Edit only the copied workspace through host-owned tools. Never claim an edit or game test without using those tools. ")
	builder.WriteString("Do not create placeholders, copy third-party/base-game assets, or suppress failures.\n\n")
	builder.WriteString(virgilGameTestingGuidance)
	builder.WriteString("\n\n")
	builder.WriteString(virgilGitCommitGuidance)
	builder.WriteString("\n\n")
	builder.WriteString("<mod-manifest>\n")
	builder.Write(encoded)
	builder.WriteString("\n</mod-manifest>\n\n")
	builder.WriteString(renderKnowledgePrompt(documents))
	filename := filepath.Join(workspace.Root, ".modstudio", "agent-context-"+runID+".md")
	if manager.shuttingDown.Load() {
		return "", errors.New("agent manager is shutting down")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if manager.shuttingDown.Load() {
		return "", errors.New("agent manager is shutting down")
	}
	if err := os.WriteFile(filename, []byte(builder.String()), 0o600); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(filename)
		return "", err
	}
	if manager.shuttingDown.Load() {
		_ = os.Remove(filename)
		return "", errors.New("agent manager is shutting down")
	}
	return filename, nil
}

func (run *agentRun) readLoop() {
	var assistantFailure error
	messageStreamed := false
	for {
		frame, err := run.decoder.Read()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				run.setFailure(fmt.Errorf("Virgil runtime stream: %w", err))
			}
			return
		}
		typeName := frameType(frame)
		switch typeName {
		case "host_tool_call":
			if !run.admitHostTool() {
				continue
			}
			requestID, _ := frame["id"].(string)
			run.registerHostTool(requestID)
			go func() {
				defer run.finishHostTool()
				run.handleHostTool(frame)
			}()
		case "host_tool_cancel":
			targetID, _ := frame["targetId"].(string)
			run.cancelHostTool(targetID)
		case "message_start":
			if nestedString(frame, "message", "role") == "assistant" {
				messageStreamed = false
			}
			run.activity(typeName, activityMessage(frame), "", "", false, compactFrame(frame), false)
		case "message_update":
			if delta := extractAssistantDelta(frame); delta != "" {
				messageStreamed = true
				run.appendAssistantText(delta)
			}
		case "message_end":
			if message, ok := frame["message"].(map[string]any); ok && nestedString(message, "role") == "assistant" {
				assistantFailure = assistantMessageFailure(message)
				if !messageStreamed {
					if content, ok := message["content"].([]any); ok {
						for _, raw := range content {
							if block, ok := raw.(map[string]any); ok && nestedString(block, "type") == "text" {
								run.appendAssistantText(nestedString(block, "text"))
							}
						}
					}
				}
				messageStreamed = false
			}
			message := activityMessage(frame)
			if assistantFailure != nil {
				message = assistantFailure.Error()
			}
			run.activityWithMetrics(typeName, message, "", "", assistantFailure != nil, compactFrame(frame), true, sessionMetricsFromFrame(frame))
		case "tool_execution_start", "tool_execution_update", "tool_execution_end":
			toolName := nestedString(frame, "toolName")
			if toolName == "" {
				toolName = nestedString(frame, "toolCall", "name")
			}
			run.activity(typeName, toolName, "", toolName, false, compactFrame(frame), typeName != "tool_execution_update")
		case "response":
			success, successExists := frame["success"].(bool)
			command, _ := frame["command"].(string)
			if successExists && !success {
				message := rpcErrorText(frame)
				run.activity("rpc_error", message, "", "", true, compactFrame(frame), true)
				if command == "prompt" {
					run.setFailure(errors.New(message))
					return
				}
			}
			if command == "prompt" && success {
				if data, ok := frame["data"].(map[string]any); ok {
					if invoked, exists := data["agentInvoked"].(bool); exists && !invoked {
						run.closeForTerminal()
						return
					}
				}
			}
		case "prompt_result":
			if invoked, ok := frame["agentInvoked"].(bool); ok && !invoked {
				run.closeForTerminal()
				return
			}
		case "agent_end":
			if terminal, exists := frame["isTerminal"].(bool); !exists || terminal {
				if assistantFailure != nil {
					run.setFailure(assistantFailure)
				}
				run.closeForTerminal()
				return
			}

		case "agent_start", "turn_start", "turn_end", "auto_compaction_start", "auto_compaction_end", "auto_retry_start", "auto_retry_end", "notice", "goal_updated", "extension_error":
			var metrics *agentEventMetrics
			if typeName == "turn_end" {
				metrics = sessionMetricsFromFrame(frame)
			}
			run.activityWithMetrics(typeName, activityMessage(frame), "", "", typeName == "extension_error", compactFrame(frame), typeName != "message_start", metrics)
		}
	}
}

func (run *agentRun) appendAssistantText(text string) {
	if text == "" {
		return
	}
	run.textMu.Lock()
	if run.text.Len()+len(text) <= maxPersistedAgentText {
		run.text.WriteString(text)
	}
	run.textMu.Unlock()
	run.activity("message_update", "", text, "", false, nil, false)
}

func assistantMessageFailure(message map[string]any) error {
	switch nestedString(message, "stopReason") {
	case "error", "aborted":
		if message := strings.TrimSpace(nestedString(message, "errorMessage")); message != "" {
			return errors.New(message)
		}
		return errors.New("the AI provider ended the response without completing it")
	default:
		return nil
	}
}
func (run *agentRun) stderrLoop(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		// Drain private runtime diagnostics without exposing implementation
		// details through Virgil's user-facing activity stream.
	}
}
func (run *agentRun) waitLoop() {
	if run.processDone != nil {
		<-run.processDone
	}
	err := run.processError()
	if err != nil {
		if run.cancelRequested.Load() {
			run.complete("cancelled", nil)
			return
		}
		if failure := run.failure(); failure != nil {
			run.complete("failed", failure)
			return
		}
		run.complete("failed", fmt.Errorf("Virgil runtime exited: %w", err))
		return
	}
	if run.cancelRequested.Load() {
		run.complete("cancelled", nil)
		return
	}
	if failure := run.failure(); failure != nil {
		run.complete("failed", failure)
		return
	}
	if !run.terminalSignal.Load() {
		run.complete("failed", errors.New("Virgil runtime exited before a terminal agent result"))
		return
	}
	run.complete("complete", nil)
}

func (run *agentRun) complete(status string, runErr error) {
	run.finish.Do(func() {
		run.drainHostTools()
		defer func() {
			if run.done != nil {
				close(run.done)
			}
		}()
		if run.cancelRequested.Load() {
			status = "cancelled"
			runErr = nil
		}
		if status == "complete" {
			if run.stdin != nil {
				_ = run.stdin.Close()
			}
		} else if run.cancel != nil {
			run.cancel()
		}
		run.textMu.Lock()
		finalText := run.text.String()
		run.textMu.Unlock()
		errorText := ""
		if runErr != nil {
			errorText = publicAIMessage(runErr.Error())
			if strings.Contains(strings.ToLower(errorText), "signal: killed") {
				status = "cancelled"
			}
		}
		if run.persist {
			_ = run.manager.store.FinishAgentRun(context.Background(), run.id, status, finalText, errorText)
			sessionStatus := "error"
			switch status {
			case "complete":
				sessionStatus = "idle"
				errorText = ""
			case "cancelled":
				sessionStatus = "paused"
			}
			_ = run.manager.store.SetVirgilSessionStatus(context.Background(), run.sessionID, sessionStatus, errorText)
		}
		run.manager.mu.Lock()
		delete(run.manager.runs, run.id)
		run.manager.mu.Unlock()
		if !run.discarded.Load() {
			run.activity("finished", status, "", "", runErr != nil, map[string]any{"status": status, "error": errorText, "finalText": finalText}, true)
		}
		run.manager.releaseSession(run.sessionID)
	})
}

func (run *agentRun) activity(eventType, message, delta, toolName string, isError bool, data map[string]any, persist bool) {
	run.activityWithMetrics(eventType, message, delta, toolName, isError, data, persist, nil)
}

func (run *agentRun) activityWithMetrics(eventType, message, delta, toolName string, isError bool, data map[string]any, persist bool, metrics *agentEventMetrics) {
	message = publicAIMessage(message)
	if metrics != nil {
		next := make(map[string]any, len(data)+1)
		for key, value := range data {
			next[key] = value
		}
		next["sessionMetrics"] = metrics
		data = next
	}
	run.activityMu.Lock()
	defer run.activityMu.Unlock()
	if run.discarded.Load() {
		return
	}
	activity := AgentActivity{
		RunID: run.id, SessionID: run.sessionID, WorkspaceID: run.workspace.ID,
		At: nowUTC(), Type: eventType, Message: message, Delta: delta,
		ToolName: toolName, IsError: isError, Data: data,
	}
	if run.manager.emit != nil {
		run.manager.emit("agent:event", activity)
	}
	if persist && run.persist && !run.discarded.Load() {
		_ = run.manager.store.AppendAgentEvent(context.Background(), run.id, eventType, message, data)
	}
}

func marshalHostToolResponse(requestID, text string, isError bool) ([]byte, error) {
	response := map[string]any{
		"type": "host_tool_result",
		"id":   requestID,
		"result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	}
	if isError {
		response["isError"] = true
	}
	return json.Marshal(response)
}

func (run *agentRun) sendHostToolResult(requestID, result string, toolErr error) error {
	text := result
	isError := toolErr != nil
	if toolErr != nil {
		text = toolErr.Error()
	}
	encoded, err := marshalHostToolResponse(requestID, text, isError)
	if err != nil {
		return err
	}
	maxFrame := 0
	if run.decoder != nil {
		maxFrame = run.decoder.maxFrame
	}
	if len(encoded) > maxFrame {
		text = fmt.Sprintf("host tool result exceeded the negotiated %d-byte frame; repeat workspace_read with a smaller offset/maxBytes range or narrow the request", maxFrame)
		encoded, err = marshalHostToolResponse(requestID, text, true)
		if err != nil {
			return err
		}
	}
	if len(encoded) > maxFrame {
		const minimal = "host tool result exceeds frame limit"
		encoded, err = marshalHostToolResponse(requestID, minimal, true)
		if err != nil {
			return err
		}
	}
	if len(encoded) > maxFrame {
		return fmt.Errorf("host tool result cannot fit negotiated single-frame limit %d", maxFrame)
	}
	run.writeMu.Lock()
	defer run.writeMu.Unlock()
	return writeRPCFrame(run.stdin, encoded)
}

func (run *agentRun) handleHostTool(frame map[string]any) {
	requestID, _ := frame["id"].(string)
	toolName, _ := frame["toolName"].(string)
	arguments, _ := frame["arguments"].(map[string]any)
	requestContext := run.hostToolContext(requestID)
	defer run.finishHostToolRequest(requestID)
	message, activityData := hostToolActivity(toolName, arguments)
	activityData["requestId"] = requestID
	run.activity("host_tool_start", message, "", toolName, false, activityData, true)
	result := ""
	var err error
	var changeMetrics *agentEventMetrics
	if lockErr := lockMutexContext(requestContext, &run.toolMu); lockErr != nil {
		err = lockErr
	} else {
		workspaceLock := run.manager.workspaceToolMutex(run.workspace.ID)
		var lockErr error
		lockWorkspace := toolName != "mod_game_test" && toolName != "game_log" && toolName != "game_source"
		if lockWorkspace {
			lockErr = lockMutexContext(requestContext, workspaceLock)
		}
		if lockErr != nil {
			err = lockErr
		} else {
			if !run.beginHostToolExecution(requestID) || run.cancelRequested.Load() {
				err = context.Canceled
			} else if requestContextErr := requestContext.Err(); requestContextErr != nil {
				err = requestContextErr
			} else {
				beforeContent := ""
				beforeAvailable := false
				if isWorkspaceMutation(toolName) {
					beforeContent, beforeAvailable = workspaceTextForMetrics(
						requestContext,
						run.workspace.FilesRoot,
						requiredString(arguments, "path"),
					)
				}
				result, err = run.executeHostToolWithContext(requestContext, toolName, arguments, requestID)
				if err == nil && isWorkspaceMutation(toolName) && beforeAvailable {
					if afterContent, afterAvailable := workspaceTextForMetrics(
						requestContext,
						run.workspace.FilesRoot,
						requiredString(arguments, "path"),
					); afterAvailable {
						changeMetrics = run.workspaceChangeMetrics(
							requiredString(arguments, "path"),
							beforeContent,
							afterContent,
						)
					}
				}
			}
			if lockWorkspace {
				workspaceLock.Unlock()
			}
		}
		run.toolMu.Unlock()
	}
	if sendErr := run.sendHostToolResult(requestID, result, err); sendErr != nil {
		if err == nil {
			err = sendErr
		}
		run.setFailure(sendErr)
	}
	endData := make(map[string]any, len(activityData)+1)
	for key, value := range activityData {
		endData[key] = value
	}
	endData["error"] = errorString(err)
	run.activityWithMetrics("host_tool_end", message, "", toolName, err != nil, endData, true, changeMetrics)
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
		"workspace_list":    "Reviewed workspace files",
		"workspace_read":    "Read",
		"workspace_write":   "Wrote",
		"workspace_replace": "Edited",
		"workspace_search":  "Searched workspace",
		"workspace_diff":    "Reviewed changes",
		"mod_validate":      "Validated mod",
		"mod_manifest":      "Reviewed mod manifest",
		"game_log":          "Read BeamNG log",
		"game_source":       "Read BeamNG Lua API",
		"mod_export":        "Exported mod ZIP",
		"mod_game_test":     "Tested build in BeamNG",
		"session_set_title": "Named Virgil session",
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
	return run.executeHostToolWithRequest(name, arguments, "")
}

func (run *agentRun) executeHostToolWithRequest(name string, arguments map[string]any, requestID string) (string, error) {
	return run.executeHostToolWithContext(run.hostToolContext(requestID), name, arguments, requestID)
}

func (run *agentRun) executeHostToolWithContext(ctx context.Context, name string, arguments map[string]any, requestID string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	filesRoot := run.workspace.FilesRoot
	var value any
	var err error
	switch name {
	case "workspace_list":
		value, err = modkit.ListWorkspaceFilesContext(ctx, filesRoot)
	case "workspace_read":
		return run.executeWorkspaceReadContext(ctx, arguments, requestID)
	case "workspace_write":
		if _, exists := arguments["expectedSha256"]; !exists {
			return "", errors.New("expectedSha256 is required; read the file before writing")
		}
		err = writeWorkspaceTextCheckedContext(ctx, filesRoot, requiredString(arguments, "path"), requiredString(arguments, "content"), requiredString(arguments, "expectedSha256"))
		if err == nil {
			value = map[string]any{"written": true}
		}
	case "workspace_replace":
		if _, exists := arguments["expectedSha256"]; !exists {
			return "", errors.New("expectedSha256 is required; read the file before replacing")
		}
		all, _ := arguments["all"].(bool)
		value, err = replaceWorkspaceTextCheckedContext(ctx, filesRoot, requiredString(arguments, "path"), requiredString(arguments, "oldText"), requiredString(arguments, "newText"), requiredString(arguments, "expectedSha256"), all)
	case "workspace_search":
		maxResults := 200
		if number, ok := arguments["maxResults"].(float64); ok && number > 0 && number <= 1000 {
			maxResults = int(number)
		}
		value, err = modkit.SearchWorkspaceContext(ctx, filesRoot, requiredString(arguments, "query"), maxResults)
	case "workspace_diff":
		var manifest modkit.WorkspaceManifest
		manifest, err = modkit.ReadWorkspaceManifest(run.workspace.Root)
		if err == nil {
			if err = ctx.Err(); err == nil {
				if isSourceFolder(run.workspace.SourcePath) {
					versionsDir := filepath.Join(run.manager.service.config.DataDir, "versions", run.workspace.ID)
					var src modkit.Source
					src, err = folderOriginalSource(ctx, versionsDir, run.workspace.SourcePath, manifest.Files)
					if err == nil {
						value, err = modkit.DiffWorkspaceSource(ctx, src, filesRoot, manifest.Files)
						src.Close()
					}
				} else {
					value, err = modkit.DiffWorkspaceContext(ctx, run.workspace.SourcePath, filesRoot, manifest.Files)
				}
			}
		}
	case "mod_validate":
		validation := modkit.ValidateWorkspaceContext(ctx, filesRoot)
		if err = ctx.Err(); err == nil {
			value = validation
			err = run.manager.store.SetWorkspaceValidation(ctx, run.workspace.ID, validation)
		}
	case "mod_manifest":
		value = run.item.Manifest
	case "game_log", "game_source", "mod_export", "mod_game_test":
		if run.manager.service == nil {
			return "", errors.New("Studio game and export services are unavailable")
		}
		switch name {
		case "game_log":
			value, err = run.manager.service.readLiveGameLog(ctx, RuntimeLogReadOptions{
				Cursor: requiredString(arguments, "cursor"), MaxLines: int(numberValue(arguments["maxLines"])),
			})
		case "game_source":
			value, err = readGameSource(ctx, run.manager.service.config.GameInstallDir, gameSourceOptions{
				Path: requiredString(arguments, "path"), Query: requiredString(arguments, "query"),
				StartLine: int(numberValue(arguments["startLine"])), MaxLines: int(numberValue(arguments["maxLines"])),
			})
		case "mod_export":
			value, err = run.manager.service.exportWorkspace(ctx, run.workspace.ID, requiredString(arguments, "label"), "virgil")
		case "mod_game_test":
			value, err = run.manager.service.runWorkspaceGameTest(ctx, run.workspace.ID, WorkspaceGameTestOptions{
				Level: requiredString(arguments, "level"), Vehicle: requiredString(arguments, "vehicle"),
				Config: requiredString(arguments, "config"), DurationSeconds: int(numberValue(arguments["durationSeconds"])),
			})
		}
	case "session_set_title":
		title, titleErr := validateVirgilSessionTitle(requiredString(arguments, "title"), true)
		if titleErr != nil {
			err = titleErr
		} else if err = ctx.Err(); err == nil {
			err = run.manager.store.SetVirgilSessionOMPTitle(ctx, run.sessionID, title)
			if err == nil {
				value = map[string]any{"title": title}
			}
		}
	default:
		err = fmt.Errorf("unknown host tool: %s", name)
	}
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if name == "workspace_write" || name == "workspace_replace" {
		if err := run.manager.store.DeleteWorkspaceDrafts(ctx, run.workspace.ID, requiredString(arguments, "path")); err != nil {
			return "", err
		}
		touch := run.manager.store.TouchWorkspace(ctx, run.workspace.ID)
		if run.manager.service != nil {
			touch = run.manager.service.workspaceChanged(ctx, run.workspace.ID, "virgil")
		}
		if touch != nil {
			return "", touch
		}
	}
	if text, ok := value.(string); ok {
		if len(text) > maxHostToolResponseBytes {
			return "", fmt.Errorf("tool response exceeds %d bytes; narrow the request", maxHostToolResponseBytes)
		}
		return text, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(encoded) > maxHostToolResponseBytes {
		return "", fmt.Errorf("tool response exceeds %d bytes; narrow the request", maxHostToolResponseBytes)
	}
	return string(encoded), nil
}

func (run *agentRun) executeWorkspaceRead(arguments map[string]any, requestID string) (string, error) {
	return run.executeWorkspaceReadContext(run.hostToolContext(requestID), arguments, requestID)
}

func (run *agentRun) executeWorkspaceReadContext(ctx context.Context, arguments map[string]any, requestID string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	relativePath, err := cleanWorkspaceRelativePath(requiredString(arguments, "path"))
	if err != nil {
		return "", err
	}
	offset, _, err := integerToolArgument(arguments, "offset")
	if err != nil {
		return "", err
	}
	expectedSHA := strings.TrimSpace(requiredString(arguments, "expectedSha256"))
	if offset > 0 && expectedSHA == "" {
		return "", errors.New("workspace_read expectedSha256 is required when offset is greater than zero")
	}
	maxBytes := maxWorkspaceReadPageBytes
	if _, exists := arguments["maxBytes"]; exists {
		maxBytes, _, err = integerToolArgument(arguments, "maxBytes")
	} else if _, exists := arguments["limit"]; exists {
		maxBytes, _, err = integerToolArgument(arguments, "limit")
	}
	if err != nil {
		return "", err
	}
	if maxBytes <= 0 {
		return "", errors.New("workspace_read maxBytes must be greater than zero")
	}
	if maxBytes > maxHostToolResponseBytes {
		maxBytes = maxHostToolResponseBytes
	}
	filename := filepath.Join(run.workspace.FilesRoot, filepath.FromSlash(relativePath))
	info, err := os.Stat(filename)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("workspace path %q is a directory", relativePath)
	}
	if int64(offset) > info.Size() {
		return "", fmt.Errorf("workspace_read offset %d exceeds file size %d", offset, info.Size())
	}
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	pageLimit := maxBytes + utf8.UTFMax
	page := make([]byte, 0, pageLimit)
	buffer := make([]byte, 4<<20)
	var position int64
	textProbe := make([]byte, 0, 8192)
	binaryFile := false
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if _, err := hash.Write(buffer[:count]); err != nil {
				return "", err
			}
			if !binaryFile {
				binaryFile = bytes.IndexByte(buffer[:count], 0) >= 0
			}
			if len(textProbe) < cap(textProbe) {
				take := min(count, cap(textProbe)-len(textProbe))
				textProbe = append(textProbe, buffer[:take]...)
			}
			chunkStart := position
			position += int64(count)
			captureStart := max(int64(offset)-chunkStart, 0)
			captureEnd := min(int64(count), int64(offset)+int64(pageLimit)-chunkStart)
			if captureEnd > captureStart {
				page = append(page, buffer[int(captureStart):int(captureEnd)]...)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if binaryFile || !isTextPage(textProbe) {
		return "", fmt.Errorf("file is binary")
	}
	if int64(offset) > position {
		return "", fmt.Errorf("workspace_read offset %d exceeds file size %d", offset, position)
	}
	fullSHA := hex.EncodeToString(hash.Sum(nil))
	if expectedSHA != "" && !strings.EqualFold(expectedSHA, fullSHA) {
		return "", fmt.Errorf("workspace_read conflict for %q: expected SHA-256 %s, found %s", relativePath, expectedSHA, fullSHA)
	}
	if offset < int(position) && len(page) > 0 && offset > 0 && !utf8.RuneStart(page[0]) {
		return "", fmt.Errorf("workspace_read offset %d is not a UTF-8 boundary", offset)
	}
	maxEnd := utf8PageEnd(page, 0, min(maxBytes, len(page)))
	if maxEnd == 0 && int64(offset) < position && len(page) > 0 {
		_, size := utf8.DecodeRune(page)
		if size == 0 {
			return "", fmt.Errorf("workspace_read offset %d is not a UTF-8 boundary", offset)
		}
		maxEnd = size
	}
	makeResult := func(relativeEnd int) (string, error) {
		absoluteEnd := offset + relativeEnd
		result := map[string]any{
			"path":          relativePath,
			"content":       string(page[:relativeEnd]),
			"sha256":        fullSHA,
			"sizeBytes":     int(position),
			"offset":        offset,
			"endOffset":     absoluteEnd,
			"nextOffset":    absoluteEnd,
			"returnedBytes": relativeEnd,
			"maxBytes":      relativeEnd,
			"truncated":     int64(absoluteEnd) < position,
			"hasMore":       int64(absoluteEnd) < position,
		}
		encoded, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return "", marshalErr
		}
		return string(encoded), nil
	}
	frameLimit := maxRPCFrameBytes
	if run.decoder != nil {
		frameLimit = run.decoder.maxFrame
	}
	fits := func(end int) (string, bool) {
		result, resultErr := makeResult(end)
		if resultErr != nil {
			return "", false
		}
		frame, frameErr := marshalHostToolResponse(requestID, result, false)
		if frameErr != nil || len(frame) > frameLimit {
			return "", false
		}
		return result, true
	}
	if result, ok := fits(maxEnd); ok {
		return result, nil
	}
	low, high, best := 0, maxEnd, 0
	for low <= high {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		mid := low + (high-low)/2
		mid = utf8PageEnd(page, 0, mid)
		if mid <= best {
			low++
			continue
		}
		if _, ok := fits(mid); ok {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if result, ok := fits(best); ok {
		if best == 0 && int64(offset) < position {
			return "", fmt.Errorf("workspace_read metadata cannot fit negotiated %d-byte frame", frameLimit)
		}
		return result, nil
	}
	return "", fmt.Errorf("workspace_read result cannot fit negotiated %d-byte frame", frameLimit)
}

func isTextPage(data []byte) bool {
	for _, value := range data {
		if value == 0 {
			return false
		}
	}
	return true
}

func utf8PageEnd(data []byte, start, end int) int {
	if end >= len(data) {
		return len(data)
	}
	for end > start && !utf8.RuneStart(data[end]) {
		end--
	}
	return end
}

func integerToolArgument(arguments map[string]any, key string) (int, bool, error) {
	raw, exists := arguments[key]
	if !exists {
		return 0, false, nil
	}
	var number float64
	switch value := raw.(type) {
	case float64:
		number = value
	case int:
		if value < 0 {
			return 0, true, fmt.Errorf("workspace_read %s must be a non-negative integer", key)
		}
		return value, true, nil
	case int64:
		if value < 0 {
			return 0, true, fmt.Errorf("workspace_read %s must be a non-negative integer", key)
		}
		if value > int64(^uint(0)>>1) {
			return 0, true, fmt.Errorf("workspace_read %s is too large", key)
		}
		return int(value), true, nil
	default:
		return 0, true, fmt.Errorf("workspace_read %s must be an integer", key)
	}
	if number < 0 || number != float64(int(number)) {
		return 0, true, fmt.Errorf("workspace_read %s must be a non-negative integer", key)
	}
	return int(number), true, nil
}

func hostToolDefinitions() []map[string]any {
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	stringField := map[string]any{"type": "string"}
	nonNegativeInteger := map[string]any{"type": "integer", "minimum": 0}
	readBytes := map[string]any{"type": "integer", "minimum": 1, "maximum": maxHostToolResponseBytes}
	return []map[string]any{
		{"name": "workspace_list", "label": "List workspace files", "description": "List editable mod files with hashes and sizes.", "loadMode": "essential", "parameters": object(map[string]any{})},
		{"name": "workspace_read", "label": "Read workspace file", "description": "Read a bounded UTF-8 byte range and return the full-file SHA-256; while truncated is true, repeat with nextOffset, maxBytes, and expectedSha256 set to the first page's sha256.", "loadMode": "essential", "parameters": object(map[string]any{"path": stringField, "offset": nonNegativeInteger, "maxBytes": readBytes, "expectedSha256": stringField}, "path")},
		{"name": "workspace_write", "label": "Write workspace file", "description": "Create or replace one UTF-8 text file only when expectedSha256 matches the preceding read; empty expectedSha256 creates an absent path.", "loadMode": "essential", "parameters": object(map[string]any{"path": stringField, "content": stringField, "expectedSha256": stringField}, "path", "content", "expectedSha256")},
		{"name": "workspace_replace", "label": "Replace workspace text", "description": "Replace exact text only when expectedSha256 matches the preceding read; all defaults false.", "loadMode": "essential", "parameters": object(map[string]any{"path": stringField, "oldText": stringField, "newText": stringField, "expectedSha256": stringField, "all": map[string]any{"type": "boolean"}}, "path", "oldText", "newText", "expectedSha256")},
		{"name": "workspace_search", "label": "Search workspace", "description": "Search text files in the workspace and return path:line matches.", "loadMode": "essential", "parameters": object(map[string]any{"query": stringField, "maxResults": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}}, "query")},
		{"name": "workspace_diff", "label": "Review workspace diff", "description": "Return added, modified, and deleted files with bounded text patches.", "loadMode": "essential", "parameters": object(map[string]any{})},
		{"name": "mod_validate", "label": "Validate BeamNG mod", "description": "Run safe-path, JSON5, packaging, and category-aware workspace validation.", "loadMode": "essential", "parameters": object(map[string]any{})},
		{"name": "mod_manifest", "label": "Inspect mod manifest", "description": "Return the analyzed source manifest, variants, structural metrics, and issues.", "loadMode": "essential", "parameters": object(map[string]any{})},
		{"name": "game_log", "label": "Read live BeamNG log", "description": "Read recent errors, warnings and stack context from the player's configured BeamNG log, even without an installed test. Save the returned cursor and pass it after a reproduction to read only fresh output. Counts cover the returned byte window; bounded or empty output is not proof a mod works.", "loadMode": "essential", "parameters": object(map[string]any{"cursor": stringField, "maxLines": map[string]any{"type": "integer", "minimum": 1, "maximum": maxLogReadLines}})},
		{"name": "game_source", "label": "Read installed BeamNG Lua", "description": "Read-only access to installed BeamNG Lua APIs. With query, search literal case-sensitive text under path (default all lua); without query, read the named .lua file. Paths are relative to the installation's lua directory. Returns numbered lines, bounded to maxLines; use startLine to read more context. Never copy base-game code into the mod.", "loadMode": "essential", "parameters": object(map[string]any{"path": stringField, "query": stringField, "startLine": map[string]any{"type": "integer", "minimum": 1}, "maxLines": map[string]any{"type": "integer", "minimum": 1, "maximum": 300}})},
		{"name": "mod_export", "label": "Export mod ZIP", "description": "Validate and export the current workspace as a separate ZIP, for sharing. Not needed to use changes: the user's library mod already updates from this workspace automatically. Returns the export path and SHA-256.", "loadMode": "essential", "parameters": object(map[string]any{"label": stringField})},
		{"name": "mod_game_test", "label": "Test mod in BeamNG", "description": "Validate and export this workspace, then launch BeamNG with only that ZIP in an isolated user folder. Load a level and target vehicle, run a bounded simulation, close only the owned test process, and return observations plus fresh log diagnostics and the tested ZIP. Refuses to interrupt an already-running game. Always inspect both observations and log; spawning and simulation do not prove driving or crash features.", "loadMode": "essential", "parameters": object(map[string]any{"level": stringField, "vehicle": stringField, "config": stringField, "durationSeconds": map[string]any{"type": "integer", "minimum": 1, "maximum": gameTestMaxDuration}})},
		{"name": "session_set_title", "label": "Set Virgil session title", "description": "Set a concise 3–8 word title for this Virgil session. Call once during the first turn.", "loadMode": "essential", "parameters": object(map[string]any{"title": stringField}, "title")},
	}
}

func removeVirgilSessionTranscript(sessionDir, sessionID string) error {
	sessionDir = strings.TrimSpace(sessionDir)
	sessionID = strings.TrimSpace(sessionID)
	if sessionDir == "" || sessionID == "" {
		return nil
	}
	if strings.ContainsAny(sessionID, `/\`) {
		return errors.New("refusing to remove a Virgil transcript for an unsafe session ID")
	}
	sessionDir, err := filepath.Abs(sessionDir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(sessionDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	suffix := "_" + sessionID + ".jsonl"
	exact := sessionID + ".jsonl"
	matches := []string{}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if name == exact || strings.HasSuffix(name, suffix) {
			matches = append(matches, name)
		}
	}
	if len(matches) > 1 {
		return fmt.Errorf("refusing to remove ambiguous Virgil transcript for session %q", sessionID)
	}
	if len(matches) == 0 {
		return nil
	}
	target := filepath.Join(sessionDir, matches[0])
	if filepath.Dir(target) != sessionDir {
		return errors.New("refusing to remove a Virgil transcript outside the configured session directory")
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (decoder *rpcDecoder) Read() (map[string]any, error) {
	if len(decoder.pending) > 0 {
		frame := decoder.pending[0]
		decoder.pending = decoder.pending[1:]
		return frame, nil
	}
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
	return "Virgil runtime rejected the RPC command"
}

func compactFrame(frame map[string]any) map[string]any {
	result := map[string]any{"type": frameType(frame)}
	for _, key := range []string{"toolName", "toolCallId", "command", "success", "isTerminal", "reason"} {
		if value, exists := frame[key]; exists {
			if text, ok := value.(string); ok {
				value = publicAIMessage(text)
			}
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
