package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeGitExitError int

func (failure fakeGitExitError) Error() string {
	return fmt.Sprintf("fake git exited %d", int(failure))
}
func (failure fakeGitExitError) ExitCode() int { return int(failure) }

type fakeGitRunner struct {
	calls         [][]string
	directories   []string
	failStage     string
	identityName  string
	identityEmail string
	branch        string
}

func (fake *fakeGitRunner) run(_ context.Context, _ string, directory string, args ...string) (string, error) {
	fake.directories = append(fake.directories, directory)
	fake.calls = append(fake.calls, append([]string(nil), args...))
	if len(args) == 0 {
		return "", errors.New("fake git received no arguments")
	}
	if fake.failStage != "" && args[0] == fake.failStage {
		return "fake failure at " + fake.failStage, errors.New("fake git failure")
	}
	switch args[0] {
	case "init":
		if err := os.MkdirAll(filepath.Join(directory, ".git"), 0o755); err != nil {
			return "", err
		}
		return "", nil
	case "symbolic-ref":
		if len(args) > 1 && args[1] == "--short" {
			if fake.branch == "" {
				return "main", nil
			}
			return fake.branch, nil
		}
		return "", nil
	case "check-ignore":
		path := args[len(args)-1]
		for _, probe := range projectGitIgnoreProbes {
			if probe.path == path {
				if probe.ignored {
					return "", nil
				}
				return "", fakeGitExitError(1)
			}
		}
		return "", fakeGitExitError(1)
	case "config":
		key := args[len(args)-1]
		switch key {
		case "user.name":
			if fake.identityName == "" {
				return "", fakeGitExitError(1)
			}
			return fake.identityName, nil
		case "user.email":
			if fake.identityEmail == "" {
				return "", fakeGitExitError(1)
			}
			return fake.identityEmail, nil
		}
		return "", fakeGitExitError(1)
	case "add":
		return "", nil
	case "diff":
		return ".gitignore\x00vehicles/example/example.jbeam\x00", nil
	case "commit":
		return "", nil
	case "log":
		return initialModCommitMessage, nil
	case "rev-list":
		return "1", nil
	case "status":
		return "", nil
	default:
		return "", fmt.Errorf("unexpected fake git operation %q", args[0])
	}
}

func useFakeGit(t *testing.T, fake *fakeGitRunner) {
	t.Helper()
	previousLookPath := gitLookPath
	previousRun := gitRunCommand
	gitLookPath = func(string) (string, error) { return "fake-git", nil }
	gitRunCommand = fake.run
	t.Cleanup(func() {
		gitLookPath = previousLookPath
		gitRunCommand = previousRun
	})
}

func TestInitializeWorkspaceGitSuccessAndSafeArguments(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Mod Café 世界")
	if err := os.MkdirAll(filepath.Join(root, "vehicles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vehicles", "example.jbeam"), []byte("scaffold"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGitRunner{identityName: "Test Author", identityEmail: "author@example.test"}
	useFakeGit(t, fake)

	result := initializeWorkspaceGit(context.Background(), root)
	if result.Status != GitInitializationStatusInitialized || result.Stage != GitInitializationStageComplete {
		t.Fatalf("result = %#v, want initialized/complete", result)
	}
	if result.Path != root {
		t.Errorf("result path = %q, want %q", result.Path, root)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Fatalf("initialized metadata missing: %v", err)
	}
	ignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ignore) != projectGitIgnore {
		t.Fatalf(".gitignore = %q, want generated conservative rules", string(ignore))
	}
	for _, directory := range fake.directories {
		if directory != root {
			t.Fatalf("Git command directory = %q, want %q", directory, root)
		}
	}
	for _, call := range fake.calls {
		if strings.Contains(strings.Join(call, "\x00"), root) {
			t.Fatalf("workspace path was concatenated into Git argv: %#v", call)
		}
	}
}

func TestInitializeWorkspaceGitPreservesExactExistingMetadata(t *testing.T) {
	root := t.TempDir()
	metadataPath := filepath.Join(root, ".git")
	if err := os.MkdirAll(metadataPath, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(metadataPath, "sentinel")
	if err := os.WriteFile(sentinel, []byte("history"), 0o644); err != nil {
		t.Fatal(err)
	}
	previousLookPath := gitLookPath
	gitLookPath = func(string) (string, error) { return "", errors.New("must not locate Git") }
	t.Cleanup(func() { gitLookPath = previousLookPath })

	result := initializeWorkspaceGit(context.Background(), root)
	if result.Status != GitInitializationStatusSkipped || result.Stage != GitInitializationStageExisting {
		t.Fatalf("result = %#v, want skipped/existing", result)
	}
	if result.NextAction != "none" {
		t.Errorf("skip next action = %q, want none", result.NextAction)
	}
	content, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "history" {
		t.Errorf("existing metadata changed to %q", string(content))
	}
	if _, err := os.Stat(filepath.Join(root, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf("existing repository received .gitignore, stat error = %v", err)
	}
}

func TestInitializeWorkspaceGitDoesNotUseParentRepository(t *testing.T) {
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "new mod")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGitRunner{identityName: "Test Author", identityEmail: "author@example.test"}
	useFakeGit(t, fake)

	result := initializeWorkspaceGit(context.Background(), root)
	if result.Status != GitInitializationStatusInitialized {
		t.Fatalf("result = %#v, want initialized", result)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Fatalf("child repository metadata missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, ".git")); err != nil {
		t.Fatalf("parent repository metadata missing: %v", err)
	}
}

func TestInitializeWorkspaceGitReportsMissingToolAndIdentity(t *testing.T) {
	root := t.TempDir()
	previousLookPath := gitLookPath
	gitLookPath = func(string) (string, error) { return "", errors.New("git not installed") }
	result := initializeWorkspaceGit(context.Background(), root)
	gitLookPath = previousLookPath
	if result.Status != GitInitializationStatusFailed || result.Stage != GitInitializationStageTool {
		t.Fatalf("missing tool result = %#v, want failed/tool", result)
	}
	if result.NextAction == "" || !strings.Contains(result.NextAction, "Install Git") {
		t.Errorf("missing tool next action = %q", result.NextAction)
	}

	fake := &fakeGitRunner{}
	useFakeGit(t, fake)
	result = initializeWorkspaceGit(context.Background(), root)
	if result.Status != GitInitializationStatusFailed || result.Stage != GitInitializationStageIdentity {
		t.Fatalf("missing identity result = %#v, want failed/identity", result)
	}
	if !strings.Contains(result.Message, "user.name") || !strings.Contains(result.Message, "user.email") {
		t.Errorf("missing identity message = %q", result.Message)
	}
	if _, err := os.Stat(filepath.Join(root, ".gitignore")); err != nil {
		t.Errorf("missing identity removed .gitignore: %v", err)
	}
}

func TestInitializeWorkspaceGitPreservesScaffoldOnStageAndCommitFailure(t *testing.T) {
	for _, failureStage := range []string{"add", "commit"} {
		t.Run(failureStage, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "vehicles", "example.jbeam")
			if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("scaffold"), 0o644); err != nil {
				t.Fatal(err)
			}
			fake := &fakeGitRunner{failStage: failureStage, identityName: "Test Author", identityEmail: "author@example.test"}
			useFakeGit(t, fake)

			result := initializeWorkspaceGit(context.Background(), root)
			wantStage := GitInitializationStageStage
			if failureStage == "commit" {
				wantStage = GitInitializationStageCommit
			}
			if result.Status != GitInitializationStatusFailed || result.Stage != wantStage {
				t.Fatalf("result = %#v, want failed/%s", result, wantStage)
			}
			if _, err := os.Stat(source); err != nil {
				t.Errorf("scaffold source was removed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
				t.Errorf("usable Git metadata was removed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, ".gitignore")); err != nil {
				t.Errorf(".gitignore was removed: %v", err)
			}
		})
	}
}

func TestWorkspaceGitMetadataFilterOnlyMatchesExactRoot(t *testing.T) {
	cases := map[string]bool{
		".git":                                   true,
		".git/config":                            true,
		filepath.Join(".git", "objects", "pack"): true,
		"nested/.git":                            false,
		"nested/.git/config":                     false,
		".gitignore":                             false,
		"assets/.gitkeep":                        false,
		"git/config":                             false,
	}
	for path, want := range cases {
		if got := isWorkspaceGitMetadataPath(path); got != want {
			t.Errorf("isWorkspaceGitMetadataPath(%q) = %t, want %t", path, got, want)
		}
	}
}

func TestInitializeWorkspaceGitPreservesExistingGitFile(t *testing.T) {
	root := t.TempDir()
	metadataPath := filepath.Join(root, ".git")
	if err := os.WriteFile(metadataPath, []byte("gitdir: C:/shared/worktree"), 0o644); err != nil {
		t.Fatal(err)
	}
	previousLookPath := gitLookPath
	gitLookPath = func(string) (string, error) { return "", errors.New("must not locate Git") }
	t.Cleanup(func() { gitLookPath = previousLookPath })

	result := initializeWorkspaceGit(context.Background(), root)
	if result.Status != GitInitializationStatusSkipped || result.Stage != GitInitializationStageExisting {
		t.Fatalf("result = %#v, want skipped/existing", result)
	}
	content, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "gitdir: C:/shared/worktree" {
		t.Errorf("existing .git file changed to %q", string(content))
	}
}
