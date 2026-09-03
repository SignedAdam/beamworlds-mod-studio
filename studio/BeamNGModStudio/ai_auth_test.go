package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func stagedAIRuntimePath(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workingDirectory, "bin", "runtime", managedAIRuntimeAssetName)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("private development runtime is not staged: %v", err)
	}
	return path
}

func TestManagedProviderStatusAndChatGPTConnectionLifecycle(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("managed desktop AI runtime is currently packaged for Windows")
	}
	service := newTestAppService(t)
	managed := newManagedAIRuntime(AppConfig{
		DataDir: service.config.DataDir, AIRuntimePath: stagedAIRuntimePath(t),
	})
	service.aiRuntime = managed
	service.agents.runtime = managed

	t.Setenv("OPENROUTER_API_KEY", "must-not-leak")
	t.Setenv("OPENAI_CODEX_OAUTH_TOKEN", "must-not-leak")
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "must-not-leak")
	state, err := service.AIConnections()
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveProfile != "chatgpt" || len(state.Providers) != 5 {
		t.Fatalf("connection state = %#v", state)
	}
	for _, provider := range state.Providers {
		if provider.Connected {
			t.Fatalf("isolated fresh provider %q inherited external authentication", provider.ID)
		}
	}

	started, err := service.StartAIConnection("chatgpt")
	if err != nil {
		t.Fatal(err)
	}
	if started.LoginID == "" || started.ProviderID != "chatgpt" {
		t.Fatalf("connection start = %#v", started)
	}
	if !strings.HasPrefix(started.URL, "http://localhost:1455/") && !strings.HasPrefix(started.URL, "https://auth.openai.com/") {
		t.Fatalf("unexpected ChatGPT authorization URL %q", started.URL)
	}
	if _, err := service.StartAIConnection("chatgpt"); err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("second ChatGPT connection error = %v", err)
	}
	if err := service.CancelAIConnection(started.LoginID); err != nil {
		t.Fatal(err)
	}
	service.aiLoginMu.Lock()
	remaining := len(service.aiLogins)
	connecting := len(service.aiConnecting)
	service.aiLoginMu.Unlock()
	if remaining != 0 || connecting != 0 {
		t.Fatalf("%d provider sign-ins and %d provider reservations remain after cancellation", remaining, connecting)
	}

	claude, err := service.StartAIConnection("claude")
	if err != nil {
		t.Fatal(err)
	}
	if claude.LoginID == "" || claude.ProviderID != "claude" {
		t.Fatalf("Claude connection start = %#v", claude)
	}
	if !strings.Contains(claude.URL, "anthropic.com") && !strings.HasPrefix(claude.URL, "http://localhost:54545/") {
		t.Fatalf("unexpected Claude authorization URL %q", claude.URL)
	}
	if err := service.CancelAIConnection(claude.LoginID); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAIConnectionURL(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"https://auth.openai.com/oauth/authorize?client_id=test",
		"http://localhost:1455/auth/callback",
	} {
		if got, err := validateAIConnectionURL(value); err != nil || got != value {
			t.Errorf("valid authorization URL %q = %q, %v", value, got, err)
		}
	}
	for _, value := range []string{
		"", "/relative", "file:///C:/Windows/System32/calc.exe",
		"javascript:alert(1)", "http://example.com/login",
		"https://user:secret@example.com/login",
		"https://example.com/" + strings.Repeat("x", 33<<10),
	} {
		if _, err := validateAIConnectionURL(value); err == nil {
			t.Errorf("unsafe authorization URL %q was accepted", value)
		}
	}
}

func TestAIConnectionContinuationInitialEventOrdering(t *testing.T) {
	for _, continuationStatus := range []string{"input", "connected", "error", "cancelled"} {
		t.Run(continuationStatus, func(t *testing.T) {
			events := make([]AIConnectionEvent, 0, 2)
			continuationStarted := false

			startAIConnectionContinuation(
				func(event AIConnectionEvent) {
					events = append(events, event)
				},
				AIConnectionEvent{
					LoginID: "login-1", ProviderID: "chatgpt", Status: "waiting",
					Message: "Open the authorization page in your browser",
				},
				func() {
					continuationStarted = true
					events = append(events, AIConnectionEvent{
						LoginID: "login-1", ProviderID: "chatgpt", Status: continuationStatus,
					})
				},
			)

			if !continuationStarted {
				t.Fatal("connection continuation did not start")
			}
			got := make([]string, len(events))
			for index, event := range events {
				got[index] = event.Status
			}
			want := []string{"waiting", continuationStatus}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("connection event statuses = %v, want %v", got, want)
			}
		})
	}
}
