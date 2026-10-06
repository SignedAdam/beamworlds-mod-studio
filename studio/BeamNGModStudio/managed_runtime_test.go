package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func environmentValues(entries []string) map[string]string {
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[strings.ToUpper(name)] = value
		}
	}
	return values
}

func TestManagedAIRuntimeEnvironmentIsolatesStateAndCredentials(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", `C:\\external-agent`)
	t.Setenv("OMP_PROFILE", "external-profile")
	t.Setenv("OPENAI_API_KEY", "external-openai-secret")
	t.Setenv("OPENAI_CODEX_OAUTH_TOKEN", "external-subscription-secret")
	t.Setenv("ANTHROPIC_API_KEY", "external-anthropic-secret")
	t.Setenv("OPENROUTER_API_KEY", "external-openrouter-secret")
	t.Setenv("GROQ_API_KEY", "external-groq-secret")
	t.Setenv("MISTRAL_API_KEY", "external-mistral-secret")
	t.Setenv("XAI_API_KEY", "external-xai-secret")
	t.Setenv("DEEPSEEK_API_KEY", "external-deepseek-secret")

	managed := newManagedAIRuntime(AppConfig{DataDir: root})
	environment, err := managedAIRuntimeEnvironment(managed, managedAIRuntimeProfileOpenAI, map[string]string{"OPENAI_API_KEY": "managed-openai-secret"})
	if err != nil {
		t.Fatal(err)
	}
	values := environmentValues(environment)
	if values["OPENAI_API_KEY"] != "managed-openai-secret" {
		t.Fatalf("managed OpenAI key = %q", values["OPENAI_API_KEY"])
	}
	for _, name := range []string{"OPENAI_CODEX_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "GROQ_API_KEY", "MISTRAL_API_KEY", "XAI_API_KEY", "DEEPSEEK_API_KEY", "OMP_PROFILE"} {
		if value, exists := values[name]; exists {
			t.Fatalf("inherited %s leaked into managed runtime as %q", name, value)
		}
	}
	for _, name := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "PI_CODING_AGENT_DIR"} {
		value := values[name]
		if value == "" || !pathWithin(value, filepath.Join(root, managedAIRuntimeRootName)) {
			t.Fatalf("%s = %q, want app-owned runtime path", name, value)
		}
	}
	if values["PI_CONFIG_DIR"] != managedAIRuntimeConfigDirName {
		t.Fatalf("PI_CONFIG_DIR = %q", values["PI_CONFIG_DIR"])
	}
	if values["PI_CODING_AGENT_DIR"] != filepath.Join(root, managedAIRuntimeRootName, managedAIRuntimeStateName, managedAIRuntimeAPIStateName, "openai") {
		t.Fatalf("direct API state = %q", values["PI_CODING_AGENT_DIR"])
	}
	authDir, err := managedAIRuntimeAgentDirForProfile(managed, nil, managedAIRuntimeProfileSubscription)
	if err != nil {
		t.Fatal(err)
	}
	if authDir != filepath.Join(root, managedAIRuntimeRootName, managedAIRuntimeStateName, managedAIRuntimeProviderAuthName) {
		t.Fatalf("provider authentication state = %q", authDir)
	}
	if err := validateManagedAIRuntimeCredentials(managedAIRuntimeProfileOpenAI, map[string]string{"OPENAI_API_KEY": "one", "ANTHROPIC_API_KEY": "two"}); err == nil {
		t.Fatal("multiple provider credentials shared one process state")
	}
	if values["OTEL_SDK_DISABLED"] != "true" {
		t.Fatalf("OTEL_SDK_DISABLED = %q", values["OTEL_SDK_DISABLED"])
	}
	for _, entry := range environment {
		if strings.Contains(entry, "external-") {
			t.Fatalf("external credential leaked in environment entry %q", entry)
		}
	}
	if _, err := managedAIRuntimeEnvironment(managed, managedAIRuntimeProfileOpenAI, map[string]string{"UNSAFE_SECRET": "value"}); err == nil {
		t.Fatal("arbitrary credential environment variable was accepted")
	}
}

func TestManagedAIRuntimeDirectProfileDoesNotReuseSubscriptionState(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "runtime.exe")
	if err := os.WriteFile(executable, []byte("test runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	managed := newManagedAIRuntime(AppConfig{DataDir: root, AIRuntimePath: executable})
	if _, err := managed.Command(t.Context(), nil, nil, managedAIRuntimeProfileOpenAI); err == nil || !strings.Contains(err.Error(), "missing managed AI credential") {
		t.Fatalf("direct profile without key error = %v", err)
	}
	directState := filepath.Join(managed.StateDir(), managedAIRuntimeAPIStateName, "openai")
	if info, err := os.Stat(directState); err != nil || !info.IsDir() {
		t.Fatalf("direct profile state = %q: %v", directState, err)
	}
	subscriptionState := filepath.Join(managed.StateDir(), managedAIRuntimeProviderAuthName)
	if _, err := os.Stat(subscriptionState); !os.IsNotExist(err) {
		t.Fatalf("direct profile created or reused subscription state %q: %v", subscriptionState, err)
	}
}

func TestManagedAIRuntimeRejectsQualifiedModelFromAnotherProvider(t *testing.T) {
	if err := validateManagedAIRuntimeModelArguments([]string{"--model", "anthropic/claude-sonnet-4-6"}, managedAIRuntimeProfileOpenAI); err == nil {
		t.Fatal("OpenAI profile accepted a qualified Anthropic model")
	}
	if err := validateManagedAIRuntimeModelArguments([]string{"--model=openrouter/auto"}, managedAIRuntimeProfileOpenRouter); err != nil {
		t.Fatalf("OpenRouter profile rejected its own qualified model: %v", err)
	}
	if err := validateManagedAIRuntimeModelArguments([]string{"--model", "custom-model"}, managedAIRuntimeProfileAnthropic); err != nil {
		t.Fatalf("Anthropic profile rejected an unqualified model override: %v", err)
	}
}

func TestPublicAIMessageHidesPrivateRuntimeNames(t *testing.T) {
	message := publicAIMessage(`Oh My Pi failed in OMP_PROFILE via omp-windows-x64.exe; prompt remains`)
	lower := strings.ToLower(message)
	for _, privateName := range []string{"oh my pi", "omp_profile", "omp-windows"} {
		if strings.Contains(lower, privateName) {
			t.Fatalf("private runtime name leaked in %q", message)
		}
	}
	if !strings.Contains(message, "prompt remains") {
		t.Fatalf("sanitizer changed unrelated text: %q", message)
	}
}

func TestManagedAIRuntimeNeverFallsBackToPathInstall(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows runtime resolution contract")
	}
	root := t.TempDir()
	pathRuntime := filepath.Join(root, "omp.exe")
	if err := os.WriteFile(pathRuntime, []byte("external"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	managed := newManagedAIRuntime(AppConfig{DataDir: root, ProjectRoot: root})
	path, err := managed.Path()
	if err == nil {
		t.Fatalf("runtime unexpectedly resolved to %q", path)
	}
	if strings.Contains(strings.ToLower(path), "omp.exe") || !strings.Contains(err.Error(), "private development payload") && !strings.Contains(err.Error(), "development payload") {
		t.Fatalf("runtime resolution = %q, %v", path, err)
	}
}

func TestManagedAIRuntimeInstallLockContendsAndReleases(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows runtime installation lock contract")
	}
	binDir := t.TempDir()
	versionDir := filepath.Join(binDir, "v"+managedAIRuntimeVersion)
	lockPath := managedAIRuntimeInstallLockPath(versionDir)
	if filepath.Dir(lockPath) != filepath.Clean(binDir) || !strings.Contains(filepath.Base(lockPath), managedAIRuntimeVersion) {
		t.Fatalf("runtime installation lock path = %q, want version-scoped lock beside %q", lockPath, versionDir)
	}
	first, err := acquireManagedAIRuntimeInstallLockWithTimeout(lockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, err := acquireManagedAIRuntimeInstallLockWithTimeout(lockPath, 100*time.Millisecond)
	if err == nil {
		_ = second.Close()
		t.Fatal("second runtime installation lock acquisition unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("lock contention error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err = acquireManagedAIRuntimeInstallLockWithTimeout(lockPath, time.Second)
	if err != nil {
		t.Fatalf("runtime installation lock was not released: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedAIRuntimeMigratesLegacySessionDirectory(t *testing.T) {
	root := t.TempDir()
	legacyDir := filepath.Join(root, "omp-sessions")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const name = "20260902_01944444-4444-7444-8444-444444444444.jsonl"
	if err := os.WriteFile(filepath.Join(legacyDir, name), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	managed := newManagedAIRuntime(AppConfig{DataDir: root})
	sessionDir, err := managed.SessionDir()
	if err != nil {
		t.Fatal(err)
	}
	if sessionDir != filepath.Join(root, managedAIRuntimeRootName, managedAIRuntimeStateName, managedAIRuntimeSessionName) {
		t.Fatalf("session directory = %q", sessionDir)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, name)); err != nil {
		t.Fatalf("migrated transcript: %v", err)
	}
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("legacy session directory still exists: %v", err)
	}
}

func TestProductionManagedAIRuntimeExtractsAndExecutesPrivatePayload(t *testing.T) {
	if runtime.GOOS != "windows" || !productionAIRuntimePayloadAvailable() {
		t.Skip("production Windows embedded payload contract")
	}
	root := t.TempDir()
	externalBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(externalBin, "omp.exe"), []byte("external"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", externalBin)
	managed := newManagedAIRuntime(AppConfig{DataDir: root})
	versionDir := filepath.Join(root, managedAIRuntimeRootName, "bin", "v"+managedAIRuntimeVersion)
	if err := os.MkdirAll(versionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versionDir, managedAIRuntimeExecutableName), []byte("corrupt"), 0o700); err != nil {
		t.Fatal(err)
	}
	staleMarker := filepath.Join(versionDir, "stale")
	if err := os.WriteFile(staleMarker, []byte("old install"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := managed.Path()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != managedAIRuntimeExecutableName || !pathWithin(path, filepath.Join(root, managedAIRuntimeRootName)) {
		t.Fatalf("production runtime path = %q", path)
	}
	valid, err := managedAIRuntimeFileValid(path, managedAIRuntimePayloadSHA256, managedAIRuntimePayloadSize)
	if err != nil || !valid {
		t.Fatalf("extracted runtime integrity = %v, %v", valid, err)
	}
	if _, err := os.Stat(staleMarker); !os.IsNotExist(err) {
		t.Fatalf("invalid prior runtime directory was not replaced: %v", err)
	}
	revalidatedMarker := filepath.Join(versionDir, "revalidated")
	if err := os.WriteFile(revalidatedMarker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractManagedAIRuntime(versionDir); err != nil {
		t.Fatalf("revalidate completed runtime: %v", err)
	}
	if _, err := os.Stat(revalidatedMarker); err != nil {
		t.Fatalf("valid runtime was replaced instead of revalidated: %v", err)
	}
	for _, name := range []string{managedAIRuntimeLicenseName, managedAIRuntimeNoticesName} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), name)); err != nil {
			t.Fatalf("extracted %s: %v", name, err)
		}
	}
	command, err := managed.Command(t.Context(), []string{"--version"}, nil, managedAIRuntimeProfileSubscription)
	if err != nil {
		t.Fatal(err)
	}
	if command.Path != path {
		t.Fatalf("command path = %q, want %q", command.Path, path)
	}
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), managedAIRuntimeVersion) {
		t.Fatalf("runtime version output = %q", output)
	}
}
