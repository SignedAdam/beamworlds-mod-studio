package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	GitInitializationStatusInitialized = "initialized"
	GitInitializationStatusSkipped     = "skipped"
	GitInitializationStatusAmbiguous   = "ambiguous"
	GitInitializationStatusFailed      = "failed"

	GitInitializationStageDetect     = "detect"
	GitInitializationStageTool       = "tool"
	GitInitializationStageInitialize = "initialize"
	GitInitializationStageBranch     = "branch"
	GitInitializationStageIgnore     = "ignore"
	GitInitializationStageIdentity   = "identity"
	GitInitializationStageStage      = "stage"
	GitInitializationStageCommit     = "commit"
	GitInitializationStageVerify     = "verify"
	GitInitializationStageExisting   = "existing"
	GitInitializationStageComplete   = "complete"
)

const initialModCommitMessage = "chore: initialize mod project"

// GitInitializationResult is the recoverable outcome of automatic repository
// setup for a new mod. A failed result never implies that the scaffold was
// removed; the path remains available for manual recovery.
type GitInitializationResult struct {
	Status     string `json:"status"`
	Stage      string `json:"stage"`
	Path       string `json:"path"`
	Message    string `json:"message"`
	NextAction string `json:"nextAction"`
}

const projectGitIgnore = `# Mod Maker generated output (safe to regenerate)
/build/
/dist/
/out/

# Tool caches
/.cache/

# Temporary files and logs
/tmp/
/temp/
*.tmp
*.temp
*.log

# Private editor and workspace state
/.vscode/
/.idea/
`

// These hooks are variables so focused tests can exercise every recovery
// state without replacing the installed Git implementation used in production.
var gitLookPath = exec.LookPath
var gitRunCommand = runGitCommand

type gitCommandError struct {
	err    error
	output string
}

func (failure *gitCommandError) Error() string {
	if failure == nil {
		return ""
	}
	if failure.output == "" {
		return failure.err.Error()
	}
	return fmt.Sprintf("%v: %s", failure.err, failure.output)
}

func (failure *gitCommandError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.err
}

func runGitCommand(ctx context.Context, executable, directory string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = directory
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		text := strings.TrimSpace(output.String())
		return text, &gitCommandError{err: err, output: text}
	}
	return strings.TrimSpace(output.String()), nil
}

func initializeWorkspaceGit(ctx context.Context, filesRoot string) GitInitializationResult {
	if ctx == nil {
		ctx = context.Background()
	}
	root := filepath.Clean(filesRoot)
	if filesRoot == "" {
		return gitFailureResult(GitInitializationStageDetect, filesRoot, errors.New("workspace files root is empty"), "Choose a valid workspace files path and retry Git setup.")
	}

	rootInfo, err := os.Stat(root)
	if err != nil {
		return gitFailureResult(GitInitializationStageDetect, root, fmt.Errorf("inspect workspace files root: %w", err), "Keep the scaffold, correct the workspace path, and retry Git setup.")
	}
	if !rootInfo.IsDir() {
		return gitFailureResult(GitInitializationStageDetect, root, errors.New("workspace files root is not a directory"), "Keep the scaffold, choose a directory for workspace files, and retry Git setup.")
	}

	metadataPath := filepath.Join(root, ".git")
	metadata, metadataErr := os.Lstat(metadataPath)
	switch {
	case metadataErr == nil && (metadata.IsDir() || metadata.Mode().IsRegular()):
		return GitInitializationResult{
			Status:     GitInitializationStatusSkipped,
			Stage:      GitInitializationStageExisting,
			Path:       root,
			Message:    fmt.Sprintf("existing Git metadata at %s was preserved; repository setup was skipped", metadataPath),
			NextAction: "none",
		}
	case metadataErr == nil:
		return GitInitializationResult{
			Status:     GitInitializationStatusAmbiguous,
			Stage:      GitInitializationStageDetect,
			Path:       root,
			Message:    fmt.Sprintf("Git metadata at %s has an unsupported filesystem type (%s); no changes were made", metadataPath, metadata.Mode().Type()),
			NextAction: "Inspect the .git path, resolve its filesystem type, and retry Git setup.",
		}
	case !errors.Is(metadataErr, os.ErrNotExist):
		return GitInitializationResult{
			Status:     GitInitializationStatusAmbiguous,
			Stage:      GitInitializationStageDetect,
			Path:       root,
			Message:    fmt.Sprintf("could not determine whether %s is Git metadata: %v; no changes were made", metadataPath, metadataErr),
			NextAction: "Check access to the .git path, resolve the metadata ambiguity, and retry Git setup.",
		}
	}

	executable, err := gitLookPath("git")
	if err != nil {
		return gitFailureResult(GitInitializationStageTool, root, fmt.Errorf("locate installed Git: %w", err), "Install Git, put its executable on PATH, and retry Git setup.")
	}

	if _, err := gitRunCommand(ctx, executable, root, "init"); err != nil {
		return gitFailureResult(GitInitializationStageInitialize, root, fmt.Errorf("git init: %w", err), "Inspect the preserved workspace and run git init manually from this path.")
	}

	if _, err := gitRunCommand(ctx, executable, root, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return gitFailureResult(GitInitializationStageBranch, root, fmt.Errorf("set default branch to main: %w", err), "From this path, set HEAD to main with git symbolic-ref HEAD refs/heads/main, then retry Git setup.")
	}
	branch, err := gitRunCommand(ctx, executable, root, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return gitFailureResult(GitInitializationStageBranch, root, fmt.Errorf("verify default branch: %w", err), "From this path, set HEAD to main with git symbolic-ref HEAD refs/heads/main, then retry Git setup.")
	}
	if strings.TrimSpace(branch) != "main" {
		return gitFailureResult(GitInitializationStageBranch, root, fmt.Errorf("Git HEAD resolved to %q instead of main", strings.TrimSpace(branch)), "From this path, set HEAD to main with git symbolic-ref HEAD refs/heads/main, then retry Git setup.")
	}

	if err := ensureProjectGitIgnore(ctx, executable, root); err != nil {
		return gitFailureResult(GitInitializationStageIgnore, root, err, "Review or create the conservative root .gitignore, then retry Git setup.")
	}

	if _, _, err := resolveGitIdentity(ctx, executable, root); err != nil {
		return gitFailureResult(GitInitializationStageIdentity, root, err, "Configure user.name and user.email, without ModMaker changing them, then retry Git setup.")
	}

	if _, err := gitRunCommand(ctx, executable, root, "add", "--all", "--", "."); err != nil {
		return gitFailureResult(GitInitializationStageStage, root, fmt.Errorf("stage scaffold: %w", err), "Inspect the preserved workspace, run git add manually from this path, and then commit it.")
	}
	staged, err := gitRunCommand(ctx, executable, root, "diff", "--cached", "--name-only", "-z", "--")
	if err != nil {
		return gitFailureResult(GitInitializationStageStage, root, fmt.Errorf("verify staged scaffold: %w", err), "Inspect the preserved workspace and staged files, then run git add manually from this path.")
	}
	stagedPaths := splitGitPaths(staged)
	if len(stagedPaths) == 0 || !containsGitPath(stagedPaths, ".gitignore") {
		return gitFailureResult(GitInitializationStageStage, root, errors.New("git add completed without staging the scaffold and .gitignore"), "Inspect the preserved workspace and staged files, then run git add manually from this path.")
	}

	if _, err := gitRunCommand(ctx, executable, root, "commit", "--message", initialModCommitMessage); err != nil {
		return gitFailureResult(GitInitializationStageCommit, root, fmt.Errorf("create initial commit: %w", err), "Review the preserved staged files and run git commit -m \"chore: initialize mod project\" manually.")
	}

	subject, err := gitRunCommand(ctx, executable, root, "log", "-1", "--format=%s")
	if err != nil {
		return gitFailureResult(GitInitializationStageVerify, root, fmt.Errorf("verify initial commit log: %w", err), "Run git log and git status manually from this path, then complete any needed recovery.")
	}
	if strings.TrimSpace(subject) != initialModCommitMessage {
		return gitFailureResult(GitInitializationStageVerify, root, fmt.Errorf("latest commit message is %q, want %q", strings.TrimSpace(subject), initialModCommitMessage), "Run git log and inspect the preserved repository before making further commits.")
	}
	count, err := gitRunCommand(ctx, executable, root, "rev-list", "--count", "HEAD")
	if err != nil {
		return gitFailureResult(GitInitializationStageVerify, root, fmt.Errorf("verify initial commit count: %w", err), "Run git log and git status manually from this path, then complete any needed recovery.")
	}
	if strings.TrimSpace(count) != "1" {
		return gitFailureResult(GitInitializationStageVerify, root, fmt.Errorf("repository has %q commits after initialization, want 1", strings.TrimSpace(count)), "Run git log and inspect the preserved repository before making further commits.")
	}
	status, err := gitRunCommand(ctx, executable, root, "status", "--porcelain", "--untracked-files=all", "--")
	if err != nil {
		return gitFailureResult(GitInitializationStageVerify, root, fmt.Errorf("verify clean status: %w", err), "Run git status manually from this path, inspect any preserved changes, and complete recovery.")
	}
	if strings.TrimSpace(status) != "" {
		return gitFailureResult(GitInitializationStageVerify, root, fmt.Errorf("repository status is not clean: %s", strings.TrimSpace(status)), "Run git status manually from this path, inspect the preserved changes, and complete recovery.")
	}

	return GitInitializationResult{
		Status:     GitInitializationStatusInitialized,
		Stage:      GitInitializationStageComplete,
		Path:       root,
		Message:    fmt.Sprintf("initialized independent Git repository at %s on main with one commit", root),
		NextAction: "none",
	}
}

func gitFailureResult(stage, root string, err error, nextAction string) GitInitializationResult {
	message := "Git setup failed"
	if err != nil {
		message = err.Error()
	}
	return GitInitializationResult{
		Status:     GitInitializationStatusFailed,
		Stage:      stage,
		Path:       root,
		Message:    message,
		NextAction: nextAction,
	}
}

func ensureProjectGitIgnore(ctx context.Context, executable, root string) error {
	ignorePath := filepath.Join(root, ".gitignore")
	metadata, err := os.Lstat(ignorePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect %s: %w", ignorePath, err)
		}
		file, createErr := os.OpenFile(ignorePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if createErr != nil {
			if !errors.Is(createErr, os.ErrExist) {
				return fmt.Errorf("create %s: %w", ignorePath, createErr)
			}
		} else {
			written, writeErr := file.WriteString(projectGitIgnore)
			if writeErr == nil && written != len(projectGitIgnore) {
				writeErr = io.ErrShortWrite
			}
			syncErr := file.Sync()
			closeErr := file.Close()
			if writeErr != nil {
				return fmt.Errorf("write %s: %w", ignorePath, writeErr)
			}
			if syncErr != nil {
				return fmt.Errorf("sync %s: %w", ignorePath, syncErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close %s: %w", ignorePath, closeErr)
			}
		}
		metadata, err = os.Lstat(ignorePath)
		if err != nil {
			return fmt.Errorf("inspect created %s: %w", ignorePath, err)
		}
	}
	if !metadata.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular .gitignore file", ignorePath)
	}
	content, err := os.ReadFile(ignorePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", ignorePath, err)
	}
	if string(content) != projectGitIgnore {
		return fmt.Errorf("%s does not contain the conservative ModMaker ignore rules; refusing to stage an unsafe ignore file", ignorePath)
	}
	if err := validateProjectGitIgnore(ctx, executable, root); err != nil {
		return fmt.Errorf("validate %s: %w", ignorePath, err)
	}
	return nil
}

type gitIgnoreProbe struct {
	path    string
	ignored bool
}

var projectGitIgnoreProbes = []gitIgnoreProbe{
	{path: "build/generated.bin", ignored: true},
	{path: "dist/generated.bin", ignored: true},
	{path: "out/generated.bin", ignored: true},
	{path: ".cache/tool-state", ignored: true},
	{path: "tmp/work.tmp", ignored: true},
	{path: "temp/work.temp", ignored: true},
	{path: "debug.log", ignored: true},
	{path: ".vscode/workspace.json", ignored: true},
	{path: ".idea/workspace.xml", ignored: true},
	{path: "vehicles/example/example.jbeam", ignored: false},
	{path: "levels/example/info.json", ignored: false},
	{path: "lua/ge/extensions/example.lua", ignored: false},
	{path: "ui/modules/apps/example/app.js", ignored: false},
	{path: "mod_info/example.json", ignored: false},
	{path: "assets/example.png", ignored: false},
	{path: "public/config.json", ignored: false},
}

func validateProjectGitIgnore(ctx context.Context, executable, root string) error {
	for _, probe := range projectGitIgnoreProbes {
		ignored, err := gitPathIgnored(ctx, executable, root, probe.path)
		if err != nil {
			return err
		}
		if ignored != probe.ignored {
			return fmt.Errorf("path %q ignore result was %t, want %t", probe.path, ignored, probe.ignored)
		}
	}
	return nil
}

func gitPathIgnored(ctx context.Context, executable, root, relativePath string) (bool, error) {
	_, err := gitRunCommand(ctx, executable, root, "check-ignore", "--no-index", "--quiet", "--", relativePath)
	if err == nil {
		return true, nil
	}
	if code, ok := gitCommandExitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check whether %s is ignored: %w", relativePath, err)
}

func gitCommandExitCode(err error) (int, bool) {
	var commandFailure *gitCommandError
	if errors.As(err, &commandFailure) {
		err = commandFailure.err
	}
	var exitError interface{ ExitCode() int }
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), true
	}
	return 0, false
}

func resolveGitIdentity(ctx context.Context, executable, root string) (string, string, error) {
	name, nameErr := gitRunCommand(ctx, executable, root, "config", "--get", "user.name")
	email, emailErr := gitRunCommand(ctx, executable, root, "config", "--get", "user.email")
	if nameErr != nil && !isMissingGitConfigValue(nameErr) {
		return "", "", fmt.Errorf("resolve user.name: %w", nameErr)
	}
	if emailErr != nil && !isMissingGitConfigValue(emailErr) {
		return "", "", fmt.Errorf("resolve user.email: %w", emailErr)
	}
	missing := make([]string, 0, 2)
	if strings.TrimSpace(name) == "" {
		missing = append(missing, "user.name")
	}
	if strings.TrimSpace(email) == "" {
		missing = append(missing, "user.email")
	}
	if len(missing) != 0 {
		return "", "", fmt.Errorf("Git author identity is missing: %s", strings.Join(missing, " and "))
	}
	return strings.TrimSpace(name), strings.TrimSpace(email), nil
}

func isMissingGitConfigValue(err error) bool {
	if code, ok := gitCommandExitCode(err); ok && code == 1 {
		var commandFailure *gitCommandError
		if errors.As(err, &commandFailure) {
			return strings.TrimSpace(commandFailure.output) == ""
		}
		return true
	}
	return false
}

func splitGitPaths(value string) []string {
	parts := strings.Split(value, "\x00")
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			paths = append(paths, filepath.ToSlash(part))
		}
	}
	return paths
}

func containsGitPath(paths []string, want string) bool {
	want = filepath.ToSlash(want)
	for _, path := range paths {
		if filepath.ToSlash(path) == want {
			return true
		}
	}
	return false
}

func isWorkspaceGitMetadataPath(relativePath string) bool {
	normalized := filepath.ToSlash(filepath.Clean(relativePath))
	return normalized == ".git" || strings.HasPrefix(normalized, ".git/")
}
