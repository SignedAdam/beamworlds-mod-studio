package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type AIProviderConnection struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Method    string `json:"method"`
	Connected bool   `json:"connected"`
}

type AIConnectionState struct {
	Providers     []AIProviderConnection `json:"providers"`
	ActiveProfile string                 `json:"activeProfile"`
}

type AIConnectionStart struct {
	LoginID      string `json:"loginId"`
	ProviderID   string `json:"providerId"`
	URL          string `json:"url"`
	Instructions string `json:"instructions"`
}

type AIConnectionEvent struct {
	LoginID    string `json:"loginId"`
	ProviderID string `json:"providerId"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	RequestID  string `json:"requestId,omitempty"`
	InputLabel string `json:"inputLabel,omitempty"`
}

type aiRPCProcess struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	decoder *rpcDecoder
	cancel  context.CancelFunc
	writeMu sync.Mutex
}

type aiLoginProcess struct {
	*aiRPCProcess
	id         string
	providerID string
	pendingMu  sync.Mutex
	pendingID  string
	cancelled  atomic.Bool
	done       chan struct{}
}

func managedRPCArguments(dataDir string) []string {
	return []string{
		"--mode", "rpc",
		"--cwd", dataDir,
		"--provider", "openrouter",
		"--no-session",
		"--no-tools",
		"--no-extensions",
		"--no-skills",
		"--no-rules",
		"--no-lsp",
		"--no-pty",
	}
}

func (service *AppService) startAIRPCProcess(ctx context.Context) (*aiRPCProcess, error) {
	processContext, cancel := context.WithCancel(ctx)
	command, err := service.aiRuntime.AuthCommand(processContext, managedRPCArguments(service.config.DataDir), map[string]string{
		"OPENROUTER_API_KEY": "beamworlds-auth-bootstrap",
	})
	if err != nil {
		cancel()
		return nil, err
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		cancel()
		return nil, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		cancel()
		return nil, fmt.Errorf("start AI connection service: %w", err)
	}
	process := &aiRPCProcess{
		cmd: command, stdin: stdin,
		decoder: &rpcDecoder{reader: newRPCBufferedReader(stdout), maxFrame: maxRPCFrameBytes, maxReassembled: maxRPCReassembledBytes},
		cancel:  cancel,
	}
	ready, err := readRPCFrameWithTimeout(process.decoder, 15*time.Second)
	if err != nil {
		process.stop()
		return nil, fmt.Errorf("connect AI service: %w", err)
	}
	if frameType(ready) != "ready" {
		process.stop()
		return nil, fmt.Errorf("AI service returned %q before ready", frameType(ready))
	}
	process.decoder.applyReady(ready)
	return process, nil
}

func newRPCBufferedReader(reader io.Reader) *bufio.Reader {
	return bufio.NewReaderSize(reader, 64<<10)
}

func (process *aiRPCProcess) send(value map[string]any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(encoded) > process.decoder.maxFrame {
		return fmt.Errorf("AI connection request exceeds %d bytes", process.decoder.maxFrame)
	}
	process.writeMu.Lock()
	defer process.writeMu.Unlock()
	return writeRPCFrame(process.stdin, encoded)
}

func (process *aiRPCProcess) stop() {
	_ = process.stdin.Close()
	done := make(chan struct{})
	go func() {
		_ = process.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		process.cancel()
		<-done
	}
	process.cancel()
}

func aiResponseError(frame map[string]any) error {
	if success, _ := frame["success"].(bool); success {
		return nil
	}
	message, _ := frame["error"].(string)
	if strings.TrimSpace(message) == "" {
		message = "AI connection failed"
	}
	return errors.New(publicAIMessage(message))
}

func (service *AppService) loginProviderStatus(ctx context.Context) (map[string]bool, error) {
	process, err := service.startAIRPCProcess(ctx)
	if err != nil {
		return nil, err
	}
	defer process.stop()
	const requestID = "beamworlds-login-providers"
	if err := process.send(map[string]any{"id": requestID, "type": "get_login_providers"}); err != nil {
		return nil, err
	}
	response, err := awaitRPCResponse(process.decoder, requestID, "get_login_providers", 15*time.Second)
	if err != nil {
		return nil, err
	}
	if err := aiResponseError(response); err != nil {
		return nil, err
	}
	status := map[string]bool{}
	data, _ := response["data"].(map[string]any)
	providers, _ := data["providers"].([]any)
	for _, entry := range providers {
		provider, _ := entry.(map[string]any)
		id, _ := provider["id"].(string)
		authenticated, _ := provider["authenticated"].(bool)
		if id != "" {
			status[id] = authenticated
		}
	}
	return status, nil
}

func (service *AppService) AIConnections() (AIConnectionState, error) {
	settings, err := service.Settings()
	if err != nil {
		return AIConnectionState{}, err
	}
	ctx, cancel := context.WithTimeout(service.aiAuthCtx, 20*time.Second)
	defer cancel()
	subscriptions, err := service.loginProviderStatus(ctx)
	if err != nil {
		return AIConnectionState{}, err
	}
	return AIConnectionState{
		ActiveProfile: settings.AgentProfile,
		Providers: []AIProviderConnection{
			{ID: "chatgpt", Label: "ChatGPT subscription", Method: "subscription", Connected: subscriptions["openai-codex"]},
			{ID: "claude", Label: "Claude Code subscription", Method: "subscription", Connected: subscriptions["anthropic"]},
			{ID: "openrouter", Label: "OpenRouter API", Method: "apiKey", Connected: settings.HasOpenRouterAPIKey},
			{ID: "openai", Label: "OpenAI API", Method: "apiKey", Connected: settings.HasOpenAIAPIKey},
			{ID: "anthropic", Label: "Anthropic API", Method: "apiKey", Connected: settings.HasAnthropicAPIKey},
		},
	}, nil
}

func subscriptionRuntimeProvider(providerID string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "chatgpt":
		return "openai-codex", nil
	case "claude":
		return "anthropic", nil
	default:
		return "", errors.New("only ChatGPT and Claude Code subscription connections use browser sign-in")
	}
}

func extensionUIString(frame map[string]any, key string) string {
	value, _ := frame[key].(string)
	return strings.TrimSpace(value)
}

func validateAIConnectionURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("provider sign-in returned an empty authorization URL")
	}
	if len(value) > 32<<10 {
		return "", errors.New("provider sign-in returned an oversized authorization URL")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("provider sign-in returned an invalid authorization URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return value, nil
	case "http":
		switch strings.ToLower(parsed.Hostname()) {
		case "localhost", "127.0.0.1", "::1":
			return value, nil
		default:
			return "", errors.New("provider sign-in returned a non-local insecure authorization URL")
		}
	default:
		return "", errors.New("provider sign-in returned an unsupported authorization URL")
	}
}

func (service *AppService) StartAIConnection(providerID string) (AIConnectionStart, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	runtimeProvider, err := subscriptionRuntimeProvider(providerID)
	if err != nil {
		return AIConnectionStart{}, err
	}
	service.aiLoginMu.Lock()
	if service.aiConnecting[providerID] {
		service.aiLoginMu.Unlock()
		return AIConnectionStart{}, fmt.Errorf("a %s connection is already in progress", providerID)
	}
	service.aiConnecting[providerID] = true
	service.aiLoginMu.Unlock()
	handedOff := false
	defer func() {
		if handedOff {
			return
		}
		service.aiLoginMu.Lock()
		delete(service.aiConnecting, providerID)
		service.aiLoginMu.Unlock()
	}()

	process, err := service.startAIRPCProcess(service.aiAuthCtx)
	if err != nil {
		return AIConnectionStart{}, err
	}
	login := &aiLoginProcess{
		aiRPCProcess: process,
		id:           uuid.NewString(), providerID: providerID, done: make(chan struct{}),
	}
	requestID := "beamworlds-login-" + login.id
	if err := process.send(map[string]any{"id": requestID, "type": "login", "providerId": runtimeProvider}); err != nil {
		process.stop()
		return AIConnectionStart{}, err
	}

	deadline := time.Now().Add(20 * time.Second)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			process.stop()
			return AIConnectionStart{}, errors.New("provider sign-in did not produce an authorization URL")
		}
		frame, err := readRPCFrameWithTimeout(process.decoder, remaining)
		if err != nil {
			process.stop()
			return AIConnectionStart{}, fmt.Errorf("start provider sign-in: %w", err)
		}
		if rpcResponseMatches(frame, requestID, "login") {
			process.stop()
			if err := aiResponseError(frame); err != nil {
				return AIConnectionStart{}, err
			}
			return AIConnectionStart{}, errors.New("provider sign-in completed without browser authorization")
		}
		if frameType(frame) != "extension_ui_request" || extensionUIString(frame, "method") != "open_url" {
			continue
		}
		rawURL := extensionUIString(frame, "launchUrl")
		if rawURL == "" {
			rawURL = extensionUIString(frame, "url")
		}
		connectionURL, err := validateAIConnectionURL(rawURL)
		if err != nil {
			process.stop()
			return AIConnectionStart{}, err
		}
		start := AIConnectionStart{
			LoginID: login.id, ProviderID: providerID, URL: connectionURL,
			Instructions: publicAIMessage(extensionUIString(frame, "instructions")),
		}
		service.aiLoginMu.Lock()
		if err := service.aiAuthCtx.Err(); err != nil {
			service.aiLoginMu.Unlock()
			process.stop()
			return AIConnectionStart{}, err
		}
		service.aiLogins[login.id] = login
		handedOff = true
		service.aiLoginMu.Unlock()
		startAIConnectionContinuation(
			service.emitAIConnection,
			AIConnectionEvent{LoginID: login.id, ProviderID: providerID, Status: "waiting", Message: start.Instructions},
			func() { go service.continueAIConnection(login, requestID) },
		)
		return start, nil
	}
}

// startAIConnectionContinuation publishes the initial state before the
// continuation launcher can emit buffered or terminal frames.
func startAIConnectionContinuation(
	emit func(AIConnectionEvent),
	initial AIConnectionEvent,
	startContinuation func(),
) {
	emit(initial)
	startContinuation()
}

func (service *AppService) emitAIConnection(event AIConnectionEvent) {
	if service.emit != nil {
		service.emit("ai:connection", event)
	}
}

func (login *aiLoginProcess) setPendingRequest(requestID string) {
	login.pendingMu.Lock()
	login.pendingID = requestID
	login.pendingMu.Unlock()
}

func (login *aiLoginProcess) takePendingRequest(requestID string) bool {
	login.pendingMu.Lock()
	defer login.pendingMu.Unlock()
	if requestID == "" || login.pendingID != requestID {
		return false
	}
	login.pendingID = ""
	return true
}

func (service *AppService) continueAIConnection(login *aiLoginProcess, requestID string) {
	status := "error"
	message := "Provider sign-in ended before authentication completed"
	for {
		frame, err := login.decoder.Read()
		if err != nil {
			if login.cancelled.Load() {
				status, message = "cancelled", "Connection cancelled"
			} else if !errors.Is(err, io.EOF) {
				message = publicAIMessage(err.Error())
			}
			break
		}
		if rpcResponseMatches(frame, requestID, "login") {
			if responseErr := aiResponseError(frame); responseErr != nil {
				message = responseErr.Error()
			} else {
				status, message = "connected", "Connection complete"
			}
			break
		}
		if frameType(frame) != "extension_ui_request" {
			continue
		}
		method := extensionUIString(frame, "method")
		switch method {
		case "input":
			uiRequestID := extensionUIString(frame, "id")
			login.setPendingRequest(uiRequestID)
			service.emitAIConnection(AIConnectionEvent{
				LoginID: login.id, ProviderID: login.providerID, Status: "input",
				Message:   "Complete sign-in in your browser. If it does not return automatically, paste the authorization code.",
				RequestID: uiRequestID, InputLabel: publicAIMessage(extensionUIString(frame, "title")),
			})
		case "notify":
			service.emitAIConnection(AIConnectionEvent{
				LoginID: login.id, ProviderID: login.providerID, Status: "waiting",
				Message: publicAIMessage(extensionUIString(frame, "message")),
			})
		case "open_url":
			service.emitAIConnection(AIConnectionEvent{
				LoginID: login.id, ProviderID: login.providerID, Status: "waiting",
				Message: publicAIMessage(extensionUIString(frame, "instructions")),
			})
		}
	}
	login.stop()
	service.aiLoginMu.Lock()
	if service.aiLogins[login.id] == login {
		delete(service.aiLogins, login.id)
	}
	delete(service.aiConnecting, login.providerID)
	service.aiLoginMu.Unlock()
	service.emitAIConnection(AIConnectionEvent{LoginID: login.id, ProviderID: login.providerID, Status: status, Message: message})
	close(login.done)
}

func (service *AppService) SubmitAIConnection(loginID, requestID, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("authorization code is required")
	}
	if len(value) > 8192 {
		return errors.New("authorization code exceeds 8192 characters")
	}
	service.aiLoginMu.Lock()
	login := service.aiLogins[strings.TrimSpace(loginID)]
	service.aiLoginMu.Unlock()
	if login == nil {
		return errors.New("provider sign-in is no longer active")
	}
	if !login.takePendingRequest(strings.TrimSpace(requestID)) {
		return errors.New("authorization request is no longer active")
	}
	if err := login.send(map[string]any{"type": "extension_ui_response", "id": requestID, "value": value}); err != nil {
		return err
	}
	service.emitAIConnection(AIConnectionEvent{LoginID: login.id, ProviderID: login.providerID, Status: "waiting", Message: "Verifying authorization"})
	return nil
}

func (service *AppService) CancelAIConnection(loginID string) error {
	service.aiLoginMu.Lock()
	login := service.aiLogins[strings.TrimSpace(loginID)]
	service.aiLoginMu.Unlock()
	if login == nil {
		return nil
	}
	login.cancelled.Store(true)
	login.cancel()
	_ = login.stdin.Close()
	select {
	case <-login.done:
	case <-time.After(5 * time.Second):
		return errors.New("provider sign-in did not stop")
	}
	return nil
}

func (service *AppService) cancelAllAIConnections() {
	service.aiAuthCancel()
	service.aiLoginMu.Lock()
	logins := make([]*aiLoginProcess, 0, len(service.aiLogins))
	for _, login := range service.aiLogins {
		logins = append(logins, login)
	}
	service.aiLoginMu.Unlock()
	for _, login := range logins {
		login.cancelled.Store(true)
		login.cancel()
		_ = login.stdin.Close()
	}
	for _, login := range logins {
		select {
		case <-login.done:
		case <-time.After(5 * time.Second):
		}
	}
}
