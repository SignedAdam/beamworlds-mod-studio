package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	managedAIRuntimeVersion          = "18.1.2"
	managedAIRuntimeAssetName        = "omp-windows-x64.exe"
	managedAIRuntimeExecutableName   = "beamworlds-ai-runtime-v" + managedAIRuntimeVersion + ".exe"
	managedAIRuntimePayloadSHA256    = "8a36c4a4be135ff94f5eed287f62b5eb39e65e78d18bdeccf8a493d02d82722b"
	managedAIRuntimePayloadSize      = int64(160749568)
	managedAIRuntimeRootName         = "ai-runtime"
	managedAIRuntimeStateName        = "state"
	managedAIRuntimeSessionName      = "sessions"
	managedAIRuntimeCommandName      = "command"
	managedAIRuntimeHomeName         = "home"
	managedAIRuntimeLocalName        = "local-app-data"
	managedAIRuntimeConfigDirName    = ".beamworlds-ai"
	managedAIRuntimeProviderAuthName = "provider-auth"
	managedAIRuntimeAPIStateName     = "api"
)

type managedAIRuntimeProfile string

const (
	managedAIRuntimeProfileSubscription managedAIRuntimeProfile = "subscription"
	managedAIRuntimeProfileChatGPT      managedAIRuntimeProfile = "chatgpt"
	managedAIRuntimeProfileClaude       managedAIRuntimeProfile = "claude"
	managedAIRuntimeProfileOpenRouter   managedAIRuntimeProfile = "openrouter"
	managedAIRuntimeProfileOpenAI       managedAIRuntimeProfile = "openai"
	managedAIRuntimeProfileAnthropic    managedAIRuntimeProfile = "anthropic"
)

var managedAIRuntimeInstallMu sync.Mutex

const (
	managedAIRuntimeLicenseName      = "LICENSE"
	managedAIRuntimeNoticesName      = "THIRD-PARTY-NOTICES.txt"
	managedAIRuntimeLicenseSHA256    = "16c45f9d667442781f03fa198914cc39abcaa48ec5ed8f644643e554ca2fbf63"
	managedAIRuntimeLicenseSize      = int64(1144)
	managedAIRuntimeNoticesSHA256    = "104142244b8781b7828e64aa79a61b03fdf16e8a3464278647c3d84ad22cbce0"
	managedAIRuntimeNoticesSize      = int64(1077086)
	managedAIRuntimeEmbeddedPathBase = "bin/runtime/"
)

var managedAIRuntimePayloads = [...]managedAIRuntimePayloadFile{
	{name: managedAIRuntimeExecutableName, embeddedPath: managedAIRuntimeEmbeddedPathBase + managedAIRuntimeAssetName, sha256: managedAIRuntimePayloadSHA256, size: managedAIRuntimePayloadSize, executable: true},
	{name: managedAIRuntimeLicenseName, embeddedPath: managedAIRuntimeEmbeddedPathBase + managedAIRuntimeLicenseName, sha256: managedAIRuntimeLicenseSHA256, size: managedAIRuntimeLicenseSize},
	{name: managedAIRuntimeNoticesName, embeddedPath: managedAIRuntimeEmbeddedPathBase + managedAIRuntimeNoticesName, sha256: managedAIRuntimeNoticesSHA256, size: managedAIRuntimeNoticesSize},
}

var managedAIRuntimePrivateNamePattern = regexp.MustCompile(`(?i)\boh(?:[-_\s]+)my(?:[-_\s]+)pi\b|\bomp(?:[-_.][a-z0-9_.-]+)?\b`)

func publicAIMessage(value string) string {
	return managedAIRuntimePrivateNamePattern.ReplaceAllString(value, "AI service")
}

type managedAIRuntimePayloadFile struct {
	name         string
	embeddedPath string
	sha256       string
	size         int64
	executable   bool
}

type managedAIRuntime struct {
	config      AppConfig
	pathOnce    sync.Once
	path        string
	pathErr     error
	sessionOnce sync.Once
	sessionDir  string
	sessionErr  error
}

// newManagedAIRuntime creates the app-owned runtime resolver. The only
// executable locations it considers are the test seam in AppConfig, the
// production embedded payload, and the private development staging directory.
func newManagedAIRuntime(config AppConfig) *managedAIRuntime {
	return &managedAIRuntime{config: config}
}

// Path resolves the runtime executable without consulting PATH or any user
// installation. AIRuntimePath is intentionally a test-only AppConfig seam.
func (runtimeInstance *managedAIRuntime) Path() (string, error) {
	if runtimeInstance == nil {
		return "", errors.New("managed AI runtime is not initialized")
	}
	runtimeInstance.pathOnce.Do(func() {
		runtimeInstance.path, runtimeInstance.pathErr = runtimeInstance.resolvePath()
	})
	return runtimeInstance.path, runtimeInstance.pathErr
}

func (runtimeInstance *managedAIRuntime) resolvePath() (string, error) {
	if override := strings.TrimSpace(runtimeInstance.config.AIRuntimePath); override != "" {
		return validateManagedAIRuntimePath(override)
	}
	if runtime.GOOS != "windows" {
		return "", errors.New("managed AI runtime is only available on Windows")
	}
	if productionAIRuntimePayloadAvailable() {
		return runtimeInstance.ensureProductionPath()
	}
	return runtimeInstance.resolveDevelopmentPath()
}

// StateDir is the isolated coding-agent state root. It deliberately lives
// below DataDir and never points at a user home or global application folder.
func (runtimeInstance *managedAIRuntime) StateDir() string {
	if runtimeInstance == nil {
		return ""
	}
	root := runtimeInstance.runtimeRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, managedAIRuntimeStateName)
}

// SessionDir returns an app-owned session directory and creates it on demand.
func (runtimeInstance *managedAIRuntime) SessionDir() (string, error) {
	if runtimeInstance == nil {
		return "", errors.New("managed AI runtime is not initialized")
	}
	runtimeInstance.sessionOnce.Do(func() {
		stateDir := runtimeInstance.StateDir()
		if stateDir == "" {
			runtimeInstance.sessionErr = errors.New("application data directory is not configured")
			return
		}
		runtimeInstance.sessionDir = filepath.Join(stateDir, managedAIRuntimeSessionName)
		if err := ensureManagedAIRuntimeDir(runtimeInstance.sessionDir); err != nil {
			runtimeInstance.sessionErr = fmt.Errorf("create managed AI session directory: %w", err)
			return
		}
		runtimeInstance.sessionErr = runtimeInstance.migrateLegacySessionDirs(runtimeInstance.sessionDir)
	})
	return runtimeInstance.sessionDir, runtimeInstance.sessionErr
}

func (runtimeInstance *managedAIRuntime) migrateLegacySessionDirs(sessionDir string) error {
	dataDir := strings.TrimSpace(runtimeInstance.config.DataDir)
	if dataDir == "" {
		return nil
	}
	legacyDirs := []string{
		filepath.Join(dataDir, "omp-sessions"),
		filepath.Join(dataDir, managedAIRuntimeRootName, managedAIRuntimeSessionName),
	}
	for _, legacyDir := range legacyDirs {
		entries, err := os.ReadDir(legacyDir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read legacy Virgil session directory: %w", err)
		}
		for _, entry := range entries {
			source := filepath.Join(legacyDir, entry.Name())
			destination := filepath.Join(sessionDir, entry.Name())
			if _, err := os.Stat(destination); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("check migrated Virgil session: %w", err)
			}
			if err := os.Rename(source, destination); err != nil {
				return fmt.Errorf("migrate Virgil session %q: %w", entry.Name(), err)
			}
		}
		_ = os.Remove(legacyDir)
	}
	return nil
}

// Command builds a command with an explicit executable, app-data working
// directory, isolated profile roots, disabled telemetry, and only the
// allowlisted credentials supplied by the caller. The selected profile is
// carried separately from credentials so a missing direct key cannot select
// subscription state.
func (runtimeInstance *managedAIRuntime) Command(ctx context.Context, args []string, credentials map[string]string, profile managedAIRuntimeProfile) (*exec.Cmd, error) {
	return runtimeInstance.command(ctx, args, credentials, profile)
}

// AuthCommand uses the subscription state even though the bootstrap process
// carries a non-secret placeholder key solely to enter RPC mode.
func (runtimeInstance *managedAIRuntime) AuthCommand(ctx context.Context, args []string, credentials map[string]string) (*exec.Cmd, error) {
	return runtimeInstance.command(ctx, args, credentials, managedAIRuntimeProfileSubscription)
}

func (runtimeInstance *managedAIRuntime) command(ctx context.Context, args []string, credentials map[string]string, profile managedAIRuntimeProfile) (*exec.Cmd, error) {
	if runtimeInstance == nil {
		return nil, errors.New("managed AI runtime is not initialized")
	}
	if ctx == nil {
		return nil, errors.New("managed AI command context is nil")
	}
	profile = managedAIRuntimeProfile(strings.ToLower(strings.TrimSpace(string(profile))))
	if !managedAIRuntimeProfileValid(profile) {
		return nil, fmt.Errorf("unsupported managed AI profile %q", profile)
	}
	if err := validateManagedAIRuntimeModelArguments(args, profile); err != nil {
		return nil, err
	}
	executable, err := runtimeInstance.Path()
	if err != nil {
		return nil, err
	}
	root := runtimeInstance.runtimeRoot()
	if root == "" {
		return nil, errors.New("application data directory is not configured")
	}
	agentDir, err := managedAIRuntimeAgentDirForProfile(runtimeInstance, credentials, profile)
	if err != nil {
		return nil, err
	}
	privateDirs := []string{
		root,
		runtimeInstance.StateDir(),
		agentDir,
		filepath.Join(root, managedAIRuntimeCommandName),
		filepath.Join(root, managedAIRuntimeHomeName),
		filepath.Join(root, managedAIRuntimeHomeName, managedAIRuntimeConfigDirName),
		filepath.Join(root, managedAIRuntimeLocalName),
		filepath.Join(root, managedAIRuntimeLocalName, "app-data"),
		filepath.Join(root, managedAIRuntimeLocalName, "cache"),
		filepath.Join(root, managedAIRuntimeLocalName, "data"),
		filepath.Join(root, managedAIRuntimeLocalName, "logs"),
	}
	for _, directory := range privateDirs {
		if err := ensureManagedAIRuntimeDir(directory); err != nil {
			return nil, fmt.Errorf("create managed AI runtime directory: %w", err)
		}
	}
	commandDir := filepath.Join(root, managedAIRuntimeCommandName)
	if err := validateManagedAIRuntimeCredentials(profile, credentials); err != nil {
		return nil, err
	}
	environment, err := managedAIRuntimeEnvironmentForAgentDir(runtimeInstance, credentials, agentDir)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = commandDir
	command.Env = environment
	return command, nil
}

func managedAIRuntimeProfileValid(profile managedAIRuntimeProfile) bool {
	switch profile {
	case managedAIRuntimeProfileSubscription, managedAIRuntimeProfileChatGPT, managedAIRuntimeProfileClaude,
		managedAIRuntimeProfileOpenRouter, managedAIRuntimeProfileOpenAI, managedAIRuntimeProfileAnthropic:
		return true
	default:
		return false
	}
}

func managedAIRuntimeProfileForAgent(profile string) (managedAIRuntimeProfile, error) {
	normalized := managedAIRuntimeProfile(strings.ToLower(strings.TrimSpace(profile)))
	if !managedAIRuntimeProfileValid(normalized) || normalized == managedAIRuntimeProfileSubscription {
		return "", fmt.Errorf("unsupported managed AI profile %q", profile)
	}
	return normalized, nil
}

func validateManagedAIRuntimeModelArguments(args []string, profile managedAIRuntimeProfile) error {
	expectedProvider := ""
	switch profile {
	case managedAIRuntimeProfileChatGPT:
		expectedProvider = "openai-codex"
	case managedAIRuntimeProfileClaude, managedAIRuntimeProfileAnthropic:
		expectedProvider = "anthropic"
	case managedAIRuntimeProfileOpenRouter:
		expectedProvider = "openrouter"
	case managedAIRuntimeProfileOpenAI:
		expectedProvider = "openai"
	}
	if expectedProvider == "" {
		return nil
	}
	for index, argument := range args {
		model := ""
		switch {
		case argument == "--model" && index+1 < len(args):
			model = strings.TrimSpace(args[index+1])
		case strings.HasPrefix(argument, "--model="):
			model = strings.TrimSpace(strings.TrimPrefix(argument, "--model="))
		}
		if model == "" {
			continue
		}
		if slash := strings.IndexByte(model, '/'); slash > 0 {
			provider := model[:slash]
			if !strings.EqualFold(provider, expectedProvider) {
				return fmt.Errorf("model %q does not belong to selected managed AI profile %q", model, profile)
			}
		}
	}
	return nil
}

func (runtimeInstance *managedAIRuntime) runtimeRoot() string {
	if runtimeInstance == nil {
		return ""
	}
	dataDir := strings.TrimSpace(runtimeInstance.config.DataDir)
	if dataDir == "" {
		return ""
	}
	dataDir = filepath.Clean(filepath.FromSlash(dataDir))
	if absolute, err := filepath.Abs(dataDir); err == nil {
		dataDir = absolute
	}
	return filepath.Join(dataDir, managedAIRuntimeRootName)
}

// The lock file is outside the version and backup directories. Its existence
// is not the lock state; the platform helper holds an OS-owned lock handle.
func managedAIRuntimeInstallLockPath(versionDir string) string {
	versionDir = filepath.Clean(filepath.FromSlash(versionDir))
	return filepath.Join(filepath.Dir(versionDir), ".install-v"+managedAIRuntimeVersion+".lock")
}

const (
	managedAIRuntimeInstallLockTimeout       = 15 * time.Second
	managedAIRuntimeInstallLockRetryInterval = 50 * time.Millisecond
)

func acquireManagedAIRuntimeInstallLock(lockPath string) (io.Closer, error) {
	return acquireManagedAIRuntimeInstallLockWithTimeout(lockPath, managedAIRuntimeInstallLockTimeout)
}

func acquireManagedAIRuntimeInstallLockWithTimeout(lockPath string, timeout time.Duration) (io.Closer, error) {
	if strings.TrimSpace(lockPath) == "" {
		return nil, errors.New("managed AI runtime installation lock path is required")
	}
	lockPath = filepath.Clean(filepath.FromSlash(lockPath))
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		installLock, retry, err := tryAcquireManagedAIRuntimeInstallLock(lockPath)
		if err == nil {
			if installLock == nil {
				return nil, errors.New("managed AI runtime installation lock returned an empty handle")
			}
			return installLock, nil
		}
		lastErr = err
		if !retry {
			return nil, fmt.Errorf("acquire managed AI runtime installation lock: %w", err)
		}
		if timeout <= 0 || !time.Now().Before(deadline) {
			return nil, fmt.Errorf("managed AI runtime installation lock timed out after %s: %w", timeout, lastErr)
		}
		wait := managedAIRuntimeInstallLockRetryInterval
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		if wait > 0 {
			time.Sleep(wait)
		}
	}
}

func (runtimeInstance *managedAIRuntime) ensureProductionPath() (string, error) {
	root := runtimeInstance.runtimeRoot()
	if root == "" {
		return "", errors.New("application data directory is not configured")
	}
	binDir := filepath.Join(root, "bin")
	if err := ensureManagedAIRuntimeDir(binDir); err != nil {
		return "", fmt.Errorf("create managed AI runtime bin directory: %w", err)
	}
	versionDir := filepath.Join(binDir, "v"+managedAIRuntimeVersion)
	executable := filepath.Join(versionDir, managedAIRuntimeExecutableName)
	if err := extractManagedAIRuntime(versionDir); err != nil {
		return "", err
	}
	return executable, nil
}

func (runtimeInstance *managedAIRuntime) resolveDevelopmentPath() (string, error) {
	projectRoot := strings.TrimSpace(runtimeInstance.config.ProjectRoot)
	if projectRoot == "" {
		return "", errors.New("managed AI runtime development payload requires a project root")
	}
	projectRoot = filepath.Clean(filepath.FromSlash(projectRoot))
	if absolute, err := filepath.Abs(projectRoot); err == nil {
		projectRoot = absolute
	}
	candidate := filepath.Join(projectRoot, "bin", "runtime", managedAIRuntimeAssetName)
	valid, err := managedAIRuntimeFileValid(candidate, managedAIRuntimePayloadSHA256, managedAIRuntimePayloadSize)
	if err != nil {
		return "", fmt.Errorf("check managed AI runtime development payload: %w", err)
	}
	if !valid {
		return "", fmt.Errorf("managed AI runtime development payload is missing or has an invalid checksum: %s", candidate)
	}
	return candidate, nil
}

func validateManagedAIRuntimePath(path string) (string, error) {
	path = filepath.Clean(filepath.FromSlash(path))
	if !filepath.IsAbs(path) {
		return "", errors.New("managed AI runtime path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat managed AI runtime executable: %w", err)
	}
	if info.IsDir() {
		return "", errors.New("managed AI runtime path is a directory")
	}
	return path, nil
}

func managedAIRuntimeDirectoryValid(directory string) bool {
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return false
	}
	for _, payload := range managedAIRuntimePayloads {
		path := filepath.Join(directory, payload.name)
		valid, validErr := managedAIRuntimeFileValid(path, payload.sha256, payload.size)
		if validErr != nil || !valid {
			return false
		}
	}
	return true
}

func managedAIRuntimeFileValid(path, expectedSHA256 string, expectedSize int64) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if info.IsDir() || info.Size() != expectedSize {
		return false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return false, err
	}
	return hex.EncodeToString(digest.Sum(nil)) == expectedSHA256, nil
}

func extractManagedAIRuntime(versionDir string) error {
	managedAIRuntimeInstallMu.Lock()
	defer managedAIRuntimeInstallMu.Unlock()

	installLock, err := acquireManagedAIRuntimeInstallLock(managedAIRuntimeInstallLockPath(versionDir))
	if err != nil {
		return fmt.Errorf("managed AI runtime installation lock: %w", err)
	}
	defer installLock.Close()

	// The interprocess lock covers this integrity check through replacement and
	// the final validation. A second process therefore reuses an install that
	// completed while it was waiting for the lock.
	if managedAIRuntimeDirectoryValid(versionDir) {
		return nil
	}
	payloadFS, available := embeddedAIRuntimeFS()
	if !available {
		return errors.New("managed AI runtime embedded payload is unavailable")
	}
	binDir := filepath.Dir(versionDir)
	stagingDir, err := os.MkdirTemp(binDir, ".staging-"+managedAIRuntimeVersion+"-")
	if err != nil {
		return fmt.Errorf("create managed AI runtime staging directory: %w", err)
	}
	defer os.RemoveAll(stagingDir)
	for _, payload := range managedAIRuntimePayloads {
		destination := filepath.Join(stagingDir, payload.name)
		if err := copyEmbeddedManagedAIRuntimeFile(payloadFS, payload, destination); err != nil {
			return err
		}
	}
	if err := replaceManagedAIRuntimeDirectory(stagingDir, versionDir); err != nil {
		return fmt.Errorf("install managed AI runtime %s: %w", managedAIRuntimeVersion, err)
	}
	if !managedAIRuntimeDirectoryValid(versionDir) {
		return errors.New("managed AI runtime installation failed integrity validation")
	}
	return nil
}

func copyEmbeddedManagedAIRuntimeFile(payloadFS fs.FS, payload managedAIRuntimePayloadFile, destination string) error {
	source, err := payloadFS.Open(payload.embeddedPath)
	if err != nil {
		return fmt.Errorf("open embedded managed AI runtime %s: %w", payload.name, err)
	}
	defer source.Close()
	mode := os.FileMode(0o600)
	if payload.executable {
		mode = 0o700
	}
	destinationFile, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create managed AI runtime payload %s: %w", payload.name, err)
	}
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(destinationFile, digest), source)
	syncErr := destinationFile.Sync()
	closeErr := destinationFile.Close()
	if copyErr != nil {
		return fmt.Errorf("extract managed AI runtime payload %s: %w", payload.name, copyErr)
	}
	if syncErr != nil {
		return fmt.Errorf("flush managed AI runtime payload %s: %w", payload.name, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close managed AI runtime payload %s: %w", payload.name, closeErr)
	}
	if written != payload.size || hex.EncodeToString(digest.Sum(nil)) != payload.sha256 {
		return fmt.Errorf("managed AI runtime payload %s checksum mismatch", payload.name)
	}
	return nil
}

func replaceManagedAIRuntimeDirectory(stagingDir, destinationDir string) error {
	if strings.TrimSpace(stagingDir) == "" || strings.TrimSpace(destinationDir) == "" {
		return errors.New("managed AI runtime staging and destination directories are required")
	}
	if filepath.Clean(stagingDir) == filepath.Clean(destinationDir) {
		return errors.New("managed AI runtime staging and destination directories must differ")
	}
	backupDir := destinationDir + ".old-" + managedAIRuntimeVersion
	_ = os.RemoveAll(backupDir)
	movedExisting := false
	if _, err := os.Lstat(destinationDir); err == nil {
		if err := os.Rename(destinationDir, backupDir); err != nil {
			return fmt.Errorf("move invalid managed AI runtime aside: %w", err)
		}
		movedExisting = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(stagingDir, destinationDir); err != nil {
		if movedExisting {
			if _, destinationErr := os.Lstat(destinationDir); errors.Is(destinationErr, os.ErrNotExist) {
				if restoreErr := os.Rename(backupDir, destinationDir); restoreErr != nil {
					return fmt.Errorf("%w (restore invalid managed AI runtime: %v)", err, restoreErr)
				}
			} else {
				return fmt.Errorf("%w (original managed AI runtime retained at %s)", err, backupDir)
			}
		}
		return err
	}
	if !managedAIRuntimeDirectoryValid(destinationDir) {
		_ = os.RemoveAll(destinationDir)
		if movedExisting {
			if restoreErr := os.Rename(backupDir, destinationDir); restoreErr != nil {
				return fmt.Errorf("new managed AI runtime failed integrity validation; restore failed: %w", restoreErr)
			}
		}
		return errors.New("new managed AI runtime failed integrity validation")
	}
	if movedExisting {
		_ = os.RemoveAll(backupDir)
	}
	return nil
}

func ensureManagedAIRuntimeDir(directory string) error {
	if directory == "" {
		return errors.New("empty directory")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	return nil
}

func managedAIRuntimeEnvironment(runtimeInstance *managedAIRuntime, profile managedAIRuntimeProfile, credentials map[string]string) ([]string, error) {
	agentDir, err := managedAIRuntimeAgentDirForProfile(runtimeInstance, credentials, profile)
	if err != nil {
		return nil, err
	}
	return managedAIRuntimeEnvironmentForAgentDir(runtimeInstance, credentials, agentDir)
}

func managedAIRuntimeAgentDirForProfile(runtimeInstance *managedAIRuntime, credentials map[string]string, profile managedAIRuntimeProfile) (string, error) {
	if runtimeInstance == nil || runtimeInstance.StateDir() == "" {
		return "", errors.New("managed AI runtime state directory is not configured")
	}
	switch profile {
	case managedAIRuntimeProfileSubscription, managedAIRuntimeProfileChatGPT, managedAIRuntimeProfileClaude:
		return filepath.Join(runtimeInstance.StateDir(), managedAIRuntimeProviderAuthName), nil
	case managedAIRuntimeProfileOpenRouter, managedAIRuntimeProfileOpenAI, managedAIRuntimeProfileAnthropic:
		if managedAIRuntimeCredentialNameForProfile(profile) == "" {
			return "", fmt.Errorf("unsupported managed AI profile %q", profile)
		}
		return filepath.Join(runtimeInstance.StateDir(), managedAIRuntimeAPIStateName, string(profile)), nil
	default:
		return "", fmt.Errorf("unsupported managed AI profile %q", profile)
	}
}

func validateManagedAIRuntimeCredentials(profile managedAIRuntimeProfile, credentials map[string]string) error {
	expectedCredential := managedAIRuntimeCredentialNameForProfile(profile)
	if expectedCredential == "" {
		return nil
	}
	if len(credentials) == 0 {
		return fmt.Errorf("missing managed AI credential for profile %q", profile)
	}
	if len(credentials) != 1 {
		return errors.New("managed AI runtime accepts one provider credential per process")
	}
	value, exists := credentials[expectedCredential]
	if !exists {
		return fmt.Errorf("managed AI credential %q is required for profile %q", expectedCredential, profile)
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("managed AI credential %q is empty", expectedCredential)
	}
	return nil
}

func managedAIRuntimeCredentialNameForProfile(profile managedAIRuntimeProfile) string {
	switch profile {
	case managedAIRuntimeProfileOpenRouter:
		return "OPENROUTER_API_KEY"
	case managedAIRuntimeProfileOpenAI:
		return "OPENAI_API_KEY"
	case managedAIRuntimeProfileAnthropic:
		return "ANTHROPIC_API_KEY"
	default:
		return ""
	}
}

func managedAIRuntimeInheritedEnvironmentName(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "COMSPEC", "LANG", "LC_ALL", "LC_CTYPE", "NUMBER_OF_PROCESSORS",
		"OS", "PATHEXT", "PROCESSOR_ARCHITECTURE", "PROCESSOR_IDENTIFIER",
		"PROCESSOR_LEVEL", "PROCESSOR_REVISION", "SYSTEMROOT", "TEMP", "TMP",
		"TZ", "WINDIR":
		return true
	default:
		return false
	}
}

func managedAIRuntimeEnvironmentForAgentDir(runtimeInstance *managedAIRuntime, credentials map[string]string, agentDir string) ([]string, error) {
	if runtimeInstance == nil {
		return nil, errors.New("managed AI runtime is not initialized")
	}
	environment := make([]string, 0, len(os.Environ())+16)
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "" || !managedAIRuntimeInheritedEnvironmentName(name) || scrubManagedAIRuntimeEnvironmentName(name) {
			continue
		}
		environment = append(environment, entry)
	}
	privateRoot := runtimeInstance.runtimeRoot()
	privateHome := filepath.Join(privateRoot, managedAIRuntimeHomeName)
	privateLocal := filepath.Join(privateRoot, managedAIRuntimeLocalName)
	privateConfig := filepath.Join(privateHome, managedAIRuntimeConfigDirName)
	privateAppData := filepath.Join(privateLocal, "app-data")
	privateCache := filepath.Join(privateLocal, "cache")
	privateState := runtimeInstance.StateDir()
	managedValues := []managedAIRuntimeEnvironmentValue{
		{name: "HOME", value: privateHome},
		{name: "USERPROFILE", value: privateHome},
		{name: "LOCALAPPDATA", value: privateLocal},
		{name: "APPDATA", value: privateAppData},
		{name: "PI_CONFIG_DIR", value: managedAIRuntimeConfigDirName},
		{name: "PI_CODING_AGENT_DIR", value: agentDir},
		{name: "XDG_CONFIG_HOME", value: privateConfig},
		{name: "XDG_DATA_HOME", value: filepath.Join(privateLocal, "data")},
		{name: "XDG_STATE_HOME", value: privateState},
		{name: "XDG_CACHE_HOME", value: privateCache},
		{name: "DO_NOT_TRACK", value: "1"},
		{name: "OTEL_SDK_DISABLED", value: "true"},
		{name: "OTEL_TRACES_EXPORTER", value: "none"},
		{name: "OTEL_METRICS_EXPORTER", value: "none"},
		{name: "OTEL_LOGS_EXPORTER", value: "none"},
	}
	if volume := filepath.VolumeName(privateHome); volume != "" {
		managedValues = append(managedValues,
			managedAIRuntimeEnvironmentValue{name: "HOMEDRIVE", value: volume},
			managedAIRuntimeEnvironmentValue{name: "HOMEPATH", value: strings.TrimPrefix(privateHome, volume)},
		)
	}
	for _, value := range managedValues {
		environment = replaceManagedAIRuntimeEnvironmentValue(environment, value.name, value.value)
	}
	credentialNames := make([]string, 0, len(credentials))
	for name := range credentials {
		credentialNames = append(credentialNames, name)
	}
	sort.Strings(credentialNames)
	for _, name := range credentialNames {
		if !managedAIRuntimeCredentialNameAllowed(name) {
			return nil, fmt.Errorf("unsupported managed AI credential environment variable %q", name)
		}
		value := credentials[name]
		if strings.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("managed AI credential %q contains NUL", name)
		}
		environment = replaceManagedAIRuntimeEnvironmentValue(environment, name, value)
	}
	return environment, nil
}

type managedAIRuntimeEnvironmentValue struct {
	name  string
	value string
}

func replaceManagedAIRuntimeEnvironmentValue(environment []string, name, value string) []string {
	filtered := environment[:0]
	for _, entry := range environment {
		entryName, _, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(entryName, name) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, name+"="+value)
}

func managedAIRuntimeCredentialNameAllowed(name string) bool {
	switch name {
	case "OPENROUTER_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY":
		return true
	default:
		return false
	}
}

func scrubManagedAIRuntimeEnvironmentName(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	if upper == "" {
		return true
	}
	if upper == "OMP" || upper == "PI" || strings.HasPrefix(upper, "OMP_") || strings.HasPrefix(upper, "PI_") {
		return true
	}
	for _, prefix := range []string{
		"OPENAI_", "ANTHROPIC_", "OPENROUTER_", "AZURE_OPENAI_", "CLAUDE_",
		"CODEX_", "GROQ_", "MISTRAL_", "XAI_", "DEEPSEEK_", "COHERE_",
		"TOGETHER_", "PERPLEXITY_", "FIREWORKS_", "CEREBRAS_", "SAMBANOVA_",
		"MINIMAX_", "MOONSHOT_", "ZHIPU_", "ALIBABA_", "VERTEXAI_",
		"GOOGLE_GENAI_", "GEMINI_", "HUGGINGFACE_",
	} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	if strings.HasSuffix(upper, "_API_KEY") || strings.HasSuffix(upper, "_API_TOKEN") ||
		strings.HasSuffix(upper, "_OAUTH_TOKEN") || strings.HasSuffix(upper, "_ACCESS_TOKEN") ||
		strings.HasSuffix(upper, "_SECRET") {
		return true
	}
	switch upper {
	case "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE",
		"GOOGLE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "GEMINI_API_KEY",
		"HF_TOKEN", "HUGGINGFACEHUB_API_TOKEN", "VERCEL_AI_GATEWAY_API_KEY",
		"AI_GATEWAY_API_KEY":
		return true
	default:
		return false
	}
}
