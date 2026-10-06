package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testGitRun(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func newTestGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	command := exec.Command("git", "-c", "init.defaultBranch=main", "init")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	testGitRun(t, root, "config", "user.name", "Mod Maker Tests")
	testGitRun(t, root, "config", "user.email", "mod-maker-tests@example.invalid")
	return root
}

func writeTestFile(t *testing.T, root, relativePath, content string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func currentGitTestFingerprint(t *testing.T, service *GitService, root string) string {
	t.Helper()
	status, err := service.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return status.Fingerprint
}

func commitTestFile(t *testing.T, root, relativePath, content, message string) {
	t.Helper()
	writeTestFile(t, root, relativePath, content)
	testGitRun(t, root, "add", "--", relativePath)
	testGitRun(t, root, "commit", "-m", message)
}

func TestGitServiceStatusSeparatesDualChangesAndUntracked(t *testing.T) {
	root := newTestGitRepo(t)
	commitTestFile(t, root, "tracked.txt", "base\n", "base")
	commitTestFile(t, root, "rename-source.txt", "same content\n", "rename source")
	commitTestFile(t, root, "binary.dat", "old", "binary base")

	writeTestFile(t, root, "tracked.txt", "staged\n")
	service := NewGitService()
	if _, err := service.Stage(context.Background(), root, []string{"tracked.txt"}, currentGitTestFingerprint(t, service, root)); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "tracked.txt", "staged and unstaged\n")
	writeTestFile(t, root, "untracked.txt", "new\n")
	if err := os.Remove(filepath.Join(root, "binary.dat")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "rename-source.txt"), filepath.Join(root, "rename-destination.txt")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "binary.dat", string([]byte{0, 1, 2, 3, 4, 5}))

	status, err := service.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Repository || status.State != GitRepositoryStateRepository || status.Branch != "main" || status.Detached {
		t.Fatalf("unexpected repository state: %#v", status)
	}
	if len(status.Staged) < 1 || len(status.Unstaged) < 1 || len(status.Untracked) < 1 {
		t.Fatalf("status groups = staged %d, unstaged %d, untracked %d; want staged, unstaged, and untracked entries", len(status.Staged), len(status.Unstaged), len(status.Untracked))
	}
	if status.Staged[0].Path != "tracked.txt" {
		t.Fatalf("staged state was not retained: %#v", status.Staged)
	}
	foundDual := false
	foundBinary := false
	for _, entry := range status.Unstaged {
		if entry.Path == "tracked.txt" && entry.Unstaged {
			foundDual = true
		}
		if entry.Path == "binary.dat" && entry.Binary {
			foundBinary = true
		}
	}
	if !foundDual {
		t.Fatalf("dual state was not retained: %#v", status.Unstaged)
	}
	if !foundBinary {
		t.Fatalf("binary metadata was not retained: %#v", status.Unstaged)
	}
	if status.Fingerprint == "" {
		t.Fatal("status fingerprint is empty")
	}
}

func TestGitServiceDiffAndDiscardPreserveStagedContent(t *testing.T) {
	root := newTestGitRepo(t)
	commitTestFile(t, root, "file.txt", "base\n", "base")
	service := NewGitService()
	writeTestFile(t, root, "file.txt", "staged\n")
	if _, err := service.Stage(context.Background(), root, []string{"file.txt"}, currentGitTestFingerprint(t, service, root)); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "file.txt", "working\n")
	writeTestFile(t, root, "new.txt", "untracked\n")

	working, err := service.Diff(context.Background(), root, "file.txt", GitDiffComparisonWorking)
	if err != nil {
		t.Fatal(err)
	}
	if !working.Available || working.Patch == "" || !strings.Contains(working.Patch, "working") {
		t.Fatalf("working diff = %#v", working)
	}
	index, err := service.Diff(context.Background(), root, "file.txt", GitDiffComparisonIndex)
	if err != nil {
		t.Fatal(err)
	}
	if !index.Available || !strings.Contains(index.Patch, "staged") || strings.Contains(index.Patch, "working") {
		t.Fatalf("index diff = %#v", index)
	}
	untracked, err := service.Diff(context.Background(), root, "new.txt", GitDiffComparisonUntracked)
	if err != nil {
		t.Fatal(err)
	}
	if !untracked.Available || !strings.Contains(untracked.Patch, "untracked") {
		t.Fatalf("untracked diff = %#v", untracked)
	}

	if _, err := service.Discard(context.Background(), root, []string{"file.txt"}, currentGitTestFingerprint(t, service, root)); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(content), "\r\n", "\n") != "staged\n" {
		t.Fatalf("discard replaced staged content: %q", content)
	}
	status, err := service.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Staged) != 1 || status.Staged[0].Path != "file.txt" || len(status.Unstaged) != 0 {
		t.Fatalf("status after staged-preserving discard = %#v", status)
	}
	if _, err := service.Discard(context.Background(), root, []string{"file.txt"}, currentGitTestFingerprint(t, service, root)); err == nil {
		t.Fatal("staged-only discard unexpectedly succeeded")
	}
	if _, err := service.Discard(context.Background(), root, []string{"new.txt"}, currentGitTestFingerprint(t, service, root)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untracked file remains after discard: %v", err)
	}
}

func TestGitServiceCommitBranchesAndDetachedHead(t *testing.T) {
	root := newTestGitRepo(t)
	commitTestFile(t, root, "file.txt", "base\n", "base")
	service := NewGitService()
	writeTestFile(t, root, "file.txt", "first\n")
	if _, err := service.Stage(context.Background(), root, []string{"file.txt"}, currentGitTestFingerprint(t, service, root)); err != nil {
		t.Fatal(err)
	}
	commit, err := service.Commit(context.Background(), root, "first commit", false, currentGitTestFingerprint(t, service, root))
	if err != nil {
		t.Fatal(err)
	}
	if commit.Hash == "" || commit.Amended || commit.Detached || commit.Branch != "main" {
		t.Fatalf("commit result = %#v", commit)
	}
	writeTestFile(t, root, "file.txt", "amended\n")
	if _, err := service.Stage(context.Background(), root, []string{"file.txt"}, currentGitTestFingerprint(t, service, root)); err != nil {
		t.Fatal(err)
	}
	amended, err := service.Commit(context.Background(), root, "amended commit", true, currentGitTestFingerprint(t, service, root))
	if err != nil {
		t.Fatal(err)
	}
	if !amended.Amended || amended.Hash == commit.Hash {
		t.Fatalf("amend result = %#v, original=%#v", amended, commit)
	}
	branches, err := service.ListBranches(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 || !branches[0].Current || branches[0].Name != "main" {
		t.Fatalf("branches = %#v", branches)
	}
	created, err := service.CreateBranch(context.Background(), root, "feature/test", "", currentGitTestFingerprint(t, service, root))
	if err != nil {
		t.Fatal(err)
	}
	if created.Branch != "feature/test" {
		t.Fatalf("created branch status = %#v", created)
	}
	if _, err := service.SwitchBranch(context.Background(), root, "main", currentGitTestFingerprint(t, service, root)); err != nil {
		t.Fatal(err)
	}
	testGitRun(t, root, "checkout", "--detach", amended.Hash)
	status, err := service.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Detached || status.Branch != "" {
		t.Fatalf("detached status = %#v", status)
	}
}

func TestGitServiceRejectsNoRepositoryAndUnsafePaths(t *testing.T) {
	root := t.TempDir()
	service := NewGitService()
	status, err := service.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if status.Repository || status.Clean || status.State != GitRepositoryStateNoRepository || status.Root != root {
		t.Fatalf("no-repository status = %#v", status)
	}
	if _, err := service.Stage(context.Background(), root, []string{"../outside"}, ""); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("unsafe no-repository path error = %v", err)
	}
	repo := newTestGitRepo(t)
	commitTestFile(t, repo, "file.txt", "content\n", "base")
	if _, err := service.Stage(context.Background(), repo, []string{"../outside"}, ""); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("outside path error = %v", err)

	}
	if _, err := service.Stage(context.Background(), repo, []string{".git/config"}, ""); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("metadata path error = %v", err)
	}
	if _, err := service.Stage(context.Background(), repo, nil, ""); err == nil || !strings.Contains(err.Error(), "explicit") {
		t.Fatalf("empty stage paths error = %v", err)
	}
	fingerprint := currentGitTestFingerprint(t, service, repo)
	for _, malformed := range []string{"./", "dir/.."} {
		if _, err := service.Stage(context.Background(), repo, []string{malformed}, fingerprint); err == nil || !strings.Contains(err.Error(), "workspace root") {
			t.Fatalf("root-normalizing stage path %q error = %v", malformed, err)
		}
	}
	if _, err := service.Discard(context.Background(), repo, []string{"."}, ""); err == nil || !strings.Contains(err.Error(), "explicit") {
		t.Fatalf("discard-all sentinel error = %v", err)
	}
}

func TestGitServiceLiteralPathspecsProtectSpecialFilenames(t *testing.T) {
	if runtimeIsWindows() {
		t.Skip("special wildcard and pathspec-magic filenames are not valid on Windows")
	}
	root := newTestGitRepo(t)
	cases := []struct {
		selected string
		sibling  string
	}{
		{selected: "literal*.txt", sibling: "literal-other.txt"},
		{selected: "question?.txt", sibling: "questionA.txt"},
		{selected: "bracket[1].txt", sibling: "bracket1.txt"},
		{selected: ":(glob)*.txt", sibling: "magic-file.txt"},
	}
	for _, item := range cases {
		writeTestFile(t, root, item.selected, "base selected\n")
		writeTestFile(t, root, item.sibling, "base sibling\n")
	}
	testGitRun(t, root, "add", "-A")
	testGitRun(t, root, "commit", "-m", "special path fixtures")
	service := NewGitService()
	untrackedSelected := "untracked*.txt"
	untrackedSibling := "untracked-other.txt"
	writeTestFile(t, root, untrackedSelected, "untracked selected\n")
	writeTestFile(t, root, untrackedSibling, "untracked sibling\n")
	untrackedDiff, err := service.Diff(context.Background(), root, untrackedSelected, GitDiffComparisonUntracked)
	if err != nil {
		t.Fatalf("untracked diff %q: %v", untrackedSelected, err)
	}
	if !untrackedDiff.Available || untrackedDiff.Binary || !strings.Contains(untrackedDiff.Patch, "untracked selected") {
		t.Fatalf("untracked diff %q = %#v", untrackedSelected, untrackedDiff)
	}
	for _, item := range cases {
		writeTestFile(t, root, item.selected, "changed selected\n")
		writeTestFile(t, root, item.sibling, "changed sibling\n")
		status, err := service.Status(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		workingDiff, diffErr := service.Diff(context.Background(), root, item.selected, GitDiffComparisonWorking)
		if diffErr != nil {
			t.Fatalf("working diff %q: %v", item.selected, diffErr)
		}
		if !workingDiff.Available || workingDiff.Binary || !strings.Contains(workingDiff.Patch, "changed selected") || strings.Contains(workingDiff.Patch, "changed sibling") {
			t.Fatalf("working diff %q = %#v", item.selected, workingDiff)
		}
		if _, err := service.Stage(context.Background(), root, []string{item.selected}, status.Fingerprint); err != nil {
			t.Fatalf("stage %q: %v", item.selected, err)
		}
		staged, err := service.Status(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		indexDiff, diffErr := service.Diff(context.Background(), root, item.selected, GitDiffComparisonIndex)
		if diffErr != nil {
			t.Fatalf("index diff %q: %v", item.selected, diffErr)
		}
		if !indexDiff.Available || indexDiff.Binary || !strings.Contains(indexDiff.Patch, "changed selected") || strings.Contains(indexDiff.Patch, "changed sibling") {
			t.Fatalf("index diff %q = %#v", item.selected, indexDiff)
		}
		selectedStaged := false
		for _, entry := range staged.Staged {
			if entry.Path == item.selected {
				selectedStaged = true
			}
			if entry.Path == item.sibling {
				t.Fatalf("literal stage of %q also staged sibling %q: %#v", item.selected, item.sibling, staged.Staged)
			}
		}
		if !selectedStaged {
			t.Fatalf("literal stage did not stage %q: %#v", item.selected, staged.Staged)
		}
		if _, err := service.Unstage(context.Background(), root, []string{item.selected}, staged.Fingerprint); err != nil {
			t.Fatalf("unstage %q: %v", item.selected, err)
		}
		unstaged, err := service.Status(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.Discard(context.Background(), root, []string{item.selected}, unstaged.Fingerprint)
		if err != nil {
			t.Fatalf("discard %q: %v", item.selected, err)
		}
		if !result.Success || len(result.Completed) != 1 || result.Completed[0] != item.selected {
			t.Fatalf("literal discard %q: %#v", item.selected, result)
		}
		selectedContent, err := os.ReadFile(filepath.Join(root, item.selected))
		if err != nil {
			t.Fatal(err)
		}
		if string(selectedContent) != "base selected\n" {
			t.Fatalf("literal discard changed %q content = %q", item.selected, selectedContent)
		}
		siblingContent, err := os.ReadFile(filepath.Join(root, item.sibling))
		if err != nil {
			t.Fatal(err)
		}
		if string(siblingContent) != "changed sibling\n" {
			t.Fatalf("literal discard changed sibling %q content = %q", item.sibling, siblingContent)
		}
	}
}
func TestGitServiceReportsStagedRename(t *testing.T) {
	root := newTestGitRepo(t)
	commitTestFile(t, root, "old-name.txt", "rename me\n", "base")
	if err := os.Rename(filepath.Join(root, "old-name.txt"), filepath.Join(root, "new-name.txt")); err != nil {
		t.Fatal(err)
	}
	service := NewGitService()
	if _, err := service.Stage(context.Background(), root, []string{"."}, currentGitTestFingerprint(t, service, root)); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var rename *GitStatusEntry
	for _, entry := range status.Staged {
		if entry.Path == "new-name.txt" {
			copy := entry
			rename = &copy
			break
		}
	}
	if rename == nil || !rename.Renamed || rename.OriginalPath != "old-name.txt" {
		t.Fatalf("rename status = %#v", status)
	}
	diff, err := service.Diff(context.Background(), root, "new-name.txt", GitDiffComparisonIndex)
	if err != nil {
		t.Fatal(err)
	}
	if diff.OriginalPath != "old-name.txt" || diff.Status != "renamed" || !diff.Available {
		t.Fatalf("rename diff = %#v", diff)
	}
}

func TestGitServiceReportsConflictAndConflictDiff(t *testing.T) {
	root := newTestGitRepo(t)
	commitTestFile(t, root, "conflict.txt", "base\n", "base")
	testGitRun(t, root, "switch", "-c", "feature")
	writeTestFile(t, root, "conflict.txt", "feature\n")
	testGitRun(t, root, "commit", "-am", "feature")
	testGitRun(t, root, "switch", "main")
	writeTestFile(t, root, "conflict.txt", "main\n")
	testGitRun(t, root, "commit", "-am", "main")
	merge := exec.Command("git", "merge", "feature")
	merge.Dir = root
	if output, err := merge.CombinedOutput(); err == nil {
		t.Fatalf("merge unexpectedly succeeded: %s", output)
	}
	service := NewGitService()
	status, err := service.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != GitRepositoryStateMerge || len(status.Conflicts) != 1 || status.Conflicts[0].Path != "conflict.txt" {
		t.Fatalf("conflict status = %#v", status)
	}
	diff, err := service.Diff(context.Background(), root, "conflict.txt", GitDiffComparisonWorking)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.Conflict || !diff.Available || diff.Message == "" {
		t.Fatalf("conflict diff = %#v", diff)
	}
	if _, err := service.Unstage(context.Background(), root, []string{"conflict.txt"}, status.Fingerprint); err == nil {
		t.Fatal("unstage unexpectedly resolved a conflict")
	}
	stillConflict, err := service.Status(context.Background(), root)
	if err != nil || len(stillConflict.Conflicts) != 1 {
		t.Fatalf("conflict changed after rejected unstage: %#v, err=%v", stillConflict, err)
	}
	testGitRun(t, root, "merge", "--abort")
}

func TestRedactGitSecretsKeepsActionableHostAndMessage(t *testing.T) {
	input := "fatal: unable to access 'https://alice:password@example.invalid/repo.git?token=secret': authentication failed"
	output := redactGitSecrets(input)
	if strings.Contains(output, "password") || strings.Contains(output, "secret") {
		t.Fatalf("credentials leaked from redacted Git message: %q", output)
	}
	if !strings.Contains(output, "example.invalid") || !strings.Contains(output, "authentication failed") {
		t.Fatalf("redaction removed actionable Git context: %q", output)
	}
}

func TestParseGitStatusPorcelainV2PreservesPathnames(t *testing.T) {
	raw := strings.Join([]string{
		"# branch.oid abc",
		"# branch.head main",
		"1 M. N... 100644 100644 100644 abc def path with spaces.txt",
		"2 R. N... 100644 100644 100644 abc def R100 renamed name.txt",
		"old\nname.txt",
		"? untracked\nname.txt",
	}, "\x00") + "\x00"
	parsed, err := parseGitStatus(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.branch != "main" || len(parsed.entries) != 3 {
		t.Fatalf("parsed = %#v", parsed)
	}
	if parsed.entries[0].Path != "path with spaces.txt" {
		t.Fatalf("space-containing path = %#v", parsed.entries[0])
	}
	if parsed.entries[1].Path != "renamed name.txt" || parsed.entries[1].OriginalPath != "old\nname.txt" || !parsed.entries[1].Renamed {
		t.Fatalf("rename path metadata = %#v", parsed.entries[1])
	}
	if parsed.entries[2].Path != "untracked\nname.txt" || !parsed.entries[2].Untracked {
		t.Fatalf("newline-containing path = %#v", parsed.entries[2])
	}
}

func TestGitServiceUnstageWorksBeforeFirstCommit(t *testing.T) {
	root := newTestGitRepo(t)
	service := NewGitService()
	writeTestFile(t, root, "before-first-commit.txt", "draft\n")
	if _, err := service.Stage(context.Background(), root, []string{"before-first-commit.txt"}, currentGitTestFingerprint(t, service, root)); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != GitRepositoryStateUnborn || len(status.Staged) != 1 {
		t.Fatalf("unborn staged status = %#v", status)
	}
	if _, err := service.Unstage(context.Background(), root, []string{"before-first-commit.txt"}, currentGitTestFingerprint(t, service, root)); err != nil {
		t.Fatal(err)
	}
	status, err = service.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if status.Clean || status.State != GitRepositoryStateUnborn || len(status.Untracked) != 1 {
		t.Fatalf("unborn unstage status = %#v", status)
	}
}

type recordingGitRunner struct {
	mu        sync.Mutex
	calls     [][]string
	root      string
	blockOp   string
	started   chan struct{}
	release   chan struct{}
	active    int
	maxActive int
}

func (runner *recordingGitRunner) Run(ctx context.Context, _ string, directory string, args ...string) (string, string, error) {
	runner.mu.Lock()
	runner.calls = append(runner.calls, append([]string{directory}, args...))
	if runner.blockOp != "" && len(args) > 0 && args[0] == runner.blockOp {
		runner.active++
		if runner.active > runner.maxActive {
			runner.maxActive = runner.active
		}
		if runner.started != nil {
			select {
			case <-runner.started:
			default:
				close(runner.started)
			}
		}
		runner.mu.Unlock()
		select {
		case <-ctx.Done():
			runner.mu.Lock()
			runner.active--
			runner.mu.Unlock()
			return "", "", ctx.Err()
		case <-runner.release:
		}
		runner.mu.Lock()
		runner.active--
		runner.mu.Unlock()
		return "", "", nil
	}
	runner.mu.Unlock()
	if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--show-toplevel" {
		return runner.root + "\n", "", nil
	}
	if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--is-inside-work-tree" {
		return "true\n", "", nil
	}
	if len(args) > 0 && args[0] == "status" {
		return "# branch.oid 0123456789012345678901234567890123456789\x00# branch.head main\x00", "", nil
	}
	if len(args) > 0 && args[0] == "remote" {
		return "origin https://user:secret@example.invalid/repo.git (fetch)\norigin https://user:secret@example.invalid/repo.git (push)\n", "", nil
	}
	if len(args) > 0 && args[0] == "diff" {
		return "", "", nil
	}
	return "", "", nil
}

func TestGitServiceCommandBoundaryCancellationConcurrencyAndRedaction(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingGitRunner{root: root, blockOp: "fetch", started: make(chan struct{}), release: make(chan struct{})}
	service := NewGitServiceWithRunner(runner)
	status, err := service.Status(context.Background(), root)
	if err != nil || !status.Repository || len(status.Remotes) != 1 {
		t.Fatalf("fake status = %#v, err=%v", status, err)
	}
	if strings.Contains(status.Remotes[0].FetchURL, "secret") || !strings.Contains(status.Remotes[0].FetchURL, "<redacted>") {
		t.Fatalf("remote credential was not redacted: %#v", status.Remotes)
	}

	ctx, cancel := context.WithCancel(context.Background())
	fetchDone := make(chan error, 1)
	go func() {
		_, fetchErr := service.Fetch(ctx, root)
		fetchDone <- fetchErr
	}()
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("fake fetch did not start")
	}
	cancel()
	select {
	case fetchErr := <-fetchDone:
		if !errors.Is(fetchErr, context.Canceled) {
			t.Fatalf("fetch cancellation error = %v", fetchErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled fetch did not return")
	}
	close(runner.release)

	runner.mu.Lock()
	calls := fmt.Sprint(runner.calls)
	runner.mu.Unlock()
	if strings.Contains(calls, "shell") || strings.Contains(calls, "&&") {
		t.Fatalf("command boundary looked shell-like: %s", calls)
	}
}

func TestGitServiceDiscardPreservesWhitespacePathIdentity(t *testing.T) {
	root := newTestGitRepo(t)
	commitTestFile(t, root, "victim.txt", "base\n", "victim")
	commitTestFile(t, root, " victim.txt", "space base\n", "space victim")
	service := NewGitService()
	writeTestFile(t, root, "victim.txt", "changed\n")
	writeTestFile(t, root, " victim.txt", "space changed\n")
	if _, err := service.Discard(context.Background(), root, []string{" victim.txt"}, currentGitTestFingerprint(t, service, root)); err != nil {
		t.Fatal(err)
	}
	regular, err := os.ReadFile(filepath.Join(root, "victim.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(regular) != "changed\n" {
		t.Fatalf("discard retargeted regular path: %q", regular)
	}
	spaced, err := os.ReadFile(filepath.Join(root, " victim.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(string(spaced), "\r\n", "\n") != "space base\n" {
		t.Fatalf("selected whitespace path was not discarded: %q", spaced)
	}
}

func TestGitServiceDiscardStagedRenameWithWorktreeDeletion(t *testing.T) {
	root := newTestGitRepo(t)
	commitTestFile(t, root, "rename-source.txt", "base\n", "base")
	testGitRun(t, root, "mv", "rename-source.txt", "indexed-name.txt")
	if err := os.Rename(filepath.Join(root, "indexed-name.txt"), filepath.Join(root, "working-name.txt")); err != nil {
		t.Fatal(err)
	}
	service := NewGitService()
	status, err := service.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var renameEntry GitStatusEntry
	found := false
	for _, entry := range status.Unstaged {
		if entry.Path == "indexed-name.txt" && entry.OriginalPath == "rename-source.txt" {
			renameEntry = entry
			found = true
			break
		}
	}
	if !found || !renameEntry.Staged || !renameEntry.Unstaged || renameEntry.StatusCode != "RD" {
		t.Fatalf("staged rename with worktree deletion status = %#v", status)
	}
	result, err := service.Discard(context.Background(), root, []string{"indexed-name.txt"}, status.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || len(result.Completed) != 1 || len(result.Failed) != 0 {
		t.Fatalf("staged rename discard result = %#v", result)
	}
	content, err := os.ReadFile(filepath.Join(root, "indexed-name.txt"))
	if err != nil {
		t.Fatalf("indexed rename target was not restored: %v", err)
	}
	if strings.ReplaceAll(string(content), "\r\n", "\n") != "base\n" {
		t.Fatalf("indexed rename target content = %q", content)
	}
	if _, err := os.Stat(filepath.Join(root, "working-name.txt")); err != nil {
		t.Fatalf("unselected worktree path changed: %v", err)
	}
}

func TestGitServiceDiscardUnstagedRenameUsesOriginalPath(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "new-name.txt", "working\n")
	statusRaw := "# branch.oid 0123456789012345678901234567890123456789\x00# branch.head main\x002 .R N... 100644 100644 100644 abc def R100 new-name.txt\x00old-name.txt\x00"
	var calls [][]string
	runner := gitCommandRunnerFunc(func(_ context.Context, _ string, _ string, args ...string) (string, string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--show-toplevel":
			return root + "\n", "", nil
		case len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--is-inside-work-tree":
			return "true\n", "", nil
		case len(args) > 0 && args[0] == "status":
			return statusRaw, "", nil
		case len(args) > 0 && args[0] == "remote":
			return "", "", nil
		case len(args) > 0 && args[0] == "diff":
			return "", "", nil
		case len(args) > 0 && (args[0] == "restore" || args[0] == "clean"):
			return "", "", nil
		default:
			return "", "", nil
		}
	})
	service := NewGitServiceWithRunner(runner)
	status, err := service.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Unstaged) != 1 || status.Unstaged[0].Path != "new-name.txt" || status.Unstaged[0].OriginalPath != "old-name.txt" || status.Unstaged[0].Staged {
		t.Fatalf("unstaged rename status = %#v", status)
	}
	result, err := service.Discard(context.Background(), root, []string{"new-name.txt"}, status.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || len(result.Completed) != 1 || result.Completed[0] != "new-name.txt" {
		t.Fatalf("unstaged rename discard result = %#v", result)
	}
	foundRestore := false
	foundClean := false
	hasArg := func(args []string, value string) bool {
		for _, arg := range args {
			if arg == value {
				return true
			}
		}
		return false
	}
	for _, args := range calls {
		if len(args) > 0 && args[0] == "restore" && hasArg(args, literalGitPath("old-name.txt")) {
			foundRestore = true
		}
		if len(args) > 0 && args[0] == "clean" && hasArg(args, literalGitPath("new-name.txt")) {
			foundClean = true
		}
	}
	if !foundRestore || !foundClean {
		t.Fatalf("unstaged rename discard commands = %#v", calls)
	}
}

func TestGitServiceDiscardRejectsStaleFingerprint(t *testing.T) {
	root := newTestGitRepo(t)
	commitTestFile(t, root, "file.txt", "base\n", "base")
	service := NewGitService()
	before, err := service.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "file.txt", "changed\n")
	result, err := service.Discard(context.Background(), root, []string{"file.txt"}, before.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Stale || result.Success || len(result.Completed) != 0 || len(result.Failed) != 1 || result.Failed[0].Path != "file.txt" {
		t.Fatalf("stale discard result = %#v", result)
	}
	if result.Status.Fingerprint == before.Fingerprint || result.Status.Clean {
		t.Fatalf("stale discard did not refresh status: %#v", result.Status)
	}
	content, err := os.ReadFile(filepath.Join(root, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "changed\n" {
		t.Fatalf("stale discard mutated the working tree: %q", content)
	}
}

func TestGitServiceDiffCapsOutputAndReportsStatus(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	var capturedDiffArgs []string
	runner := gitCommandRunnerFunc(func(_ context.Context, _ string, _ string, args ...string) (string, string, error) {
		switch {
		case len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--show-toplevel":
			return root + "\n", "", nil
		case len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--is-inside-work-tree":
			return "true\n", "", nil
		case len(args) > 0 && args[0] == "status":
			return "# branch.oid 0123456789012345678901234567890123456789\x00# branch.head main\x001 .M N... 100644 100644 100644 abc def file.txt\x00", "", nil
		case len(args) > 0 && args[0] == "remote":
			return "", "", nil
		case len(args) > 0 && args[0] == "diff":
			hasNumstat := false
			hasTextconv := false
			for _, arg := range args {
				hasNumstat = hasNumstat || arg == "--numstat"
				hasTextconv = hasTextconv || arg == "--no-textconv"
			}
			if hasNumstat {
				return "", "", nil
			}
			if hasTextconv {
				capturedDiffArgs = append([]string(nil), args...)
				return strings.Repeat("x", maxGitCommandOutputBytes+64), "", nil
			}
		}
		return "", "", nil
	})
	service := NewGitServiceWithRunner(runner)
	diff, err := service.Diff(context.Background(), root, "file.txt", GitDiffComparisonWorking)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Available || !diff.Unsupported || diff.Patch != "" || diff.Status != "modified" {
		t.Fatalf("capped diff = %#v", diff)
	}
	for _, arg := range capturedDiffArgs {
		if arg == "--binary" {
			t.Fatalf("diff command still requests binary patch output: %#v", capturedDiffArgs)
		}
	}
	foundNoTextconv := false
	for _, arg := range capturedDiffArgs {
		foundNoTextconv = foundNoTextconv || arg == "--no-textconv"
	}
	if !foundNoTextconv {
		t.Fatalf("diff command omitted --no-textconv: %#v", capturedDiffArgs)
	}
}

func TestGitServiceDiffUsesComparisonSpecificBinaryState(t *testing.T) {
	root := newTestGitRepo(t)
	commitTestFile(t, root, "mixed.dat", "base text\n", "base")
	service := NewGitService()
	writeTestFile(t, root, "mixed.dat", "staged text\n")
	if _, err := service.Stage(context.Background(), root, []string{"mixed.dat"}, currentGitTestFingerprint(t, service, root)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mixed.dat"), []byte("work\x00binary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	index, err := service.Diff(context.Background(), root, "mixed.dat", GitDiffComparisonIndex)
	if err != nil {
		t.Fatal(err)
	}
	if !index.Available || index.Binary || index.Patch == "" || !strings.Contains(index.Patch, "staged text") {
		t.Fatalf("index diff with binary worktree = %#v", index)
	}
	working, err := service.Diff(context.Background(), root, "mixed.dat", GitDiffComparisonWorking)
	if err != nil {
		t.Fatal(err)
	}
	if !working.Available || !working.Binary || !working.Unsupported || working.Patch != "" {
		t.Fatalf("working diff with binary worktree = %#v", working)
	}
}

func TestGitServiceRemoteFailureReturnsStructuredResult(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := gitCommandRunnerFunc(func(_ context.Context, _ string, _ string, args ...string) (string, string, error) {
		switch {
		case len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--show-toplevel":
			return root + "\n", "", nil
		case len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--is-inside-work-tree":
			return "true\n", "", nil
		case len(args) > 0 && args[0] == "status":
			return "# branch.oid 0123456789012345678901234567890123456789\x00# branch.head main\x00", "", nil
		case len(args) > 0 && args[0] == "remote":
			return "", "", nil
		case len(args) > 0 && args[0] == "diff":
			return "", "", nil
		case len(args) > 0 && args[0] == "fetch":
			return "", "fatal: authentication failed", errors.New("exit status 1")
		}
		return "", "", nil
	})
	service := NewGitServiceWithRunner(runner)
	result, err := service.Fetch(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || len(result.Steps) != 1 || result.Steps[0].Success || result.Error == "" || !strings.Contains(result.Error, "authentication failed") {
		t.Fatalf("structured remote failure = %#v", result)
	}
	if !result.Status.Repository || result.Status.State != GitRepositoryStateRepository {
		t.Fatalf("remote failure status = %#v", result.Status)
	}
}

func TestGitServiceRemoteCancellationCanBeRequestedByWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingGitRunner{
		root:    root,
		blockOp: "fetch",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	service := NewGitServiceWithRunner(runner)
	fetchDone := make(chan error, 1)
	go func() {
		_, fetchErr := service.Fetch(context.Background(), root)
		fetchDone <- fetchErr
	}()
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("workspace fetch did not start")
	}
	if !service.CancelRemoteOperation(root) {
		t.Fatal("workspace cancellation did not find the active operation")
	}
	select {
	case fetchErr := <-fetchDone:
		if !errors.Is(fetchErr, context.Canceled) {
			t.Fatalf("workspace cancellation error = %v", fetchErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("workspace cancellation did not return")
	}
	close(runner.release)
}

func TestGitServiceSuccessfulMutationTruncationIsMarked(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	var addCalled bool
	runner := gitCommandRunnerFunc(func(_ context.Context, _ string, _ string, args ...string) (string, string, error) {
		switch {
		case len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--show-toplevel":
			return root + "\n", "", nil
		case len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--is-inside-work-tree":
			return "true\n", "", nil
		case len(args) > 0 && args[0] == "status":
			return "# branch.oid 0123456789012345678901234567890123456789\x00# branch.head main\x00", "", nil
		case len(args) > 0 && args[0] == "remote":
			return "", "", nil
		case len(args) > 0 && args[0] == "fetch":
			return strings.Repeat("x", maxGitCommandOutputBytes+64), "", nil
		case len(args) > 0 && args[0] == "diff":
			return "", "", nil
		case len(args) > 0 && args[0] == "add":
			addCalled = true
			return strings.Repeat("x", maxGitCommandOutputBytes+64), "", nil
		}
		return "", "", nil
	})
	service := NewGitServiceWithRunner(runner)
	status, err := service.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Stage(context.Background(), root, []string{"."}, status.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	remoteResult, err := service.Fetch(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !remoteResult.Success || !remoteResult.OutputTruncated || len(remoteResult.Steps) != 1 || !remoteResult.Steps[0].OutputTruncated {
		t.Fatalf("successful truncated fetch = %#v", remoteResult)
	}
	if !addCalled || !result.OutputTruncated {
		t.Fatalf("successful truncated stage = %#v, addCalled=%v", result, addCalled)
	}
}

func TestGitServiceExpectedFingerprintGuardsStaging(t *testing.T) {
	root := newTestGitRepo(t)
	commitTestFile(t, root, "file.txt", "base\n", "base")
	service := NewGitService()
	before, err := service.Status(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "file.txt", "changed\n")
	status, err := service.Stage(context.Background(), root, []string{"file.txt"}, before.Fingerprint)
	if err == nil || status.Clean || len(status.Unstaged) != 1 {
		t.Fatalf("stale stage result = %#v, err=%v", status, err)
	}
	indexed := strings.TrimSpace(testGitRun(t, root, "diff", "--cached", "--name-only"))
	if indexed != "" {
		t.Fatalf("stale stage changed index: %q", indexed)
	}
}

func TestGitServicePreservesPosixBackslashPathIdentity(t *testing.T) {
	if runtimeIsWindows() {
		t.Skip("POSIX path identity is covered on non-Windows builders")
	}
	root := newTestGitRepo(t)
	relativePath := `dir\name.txt`
	commitTestFile(t, root, relativePath, "base\n", "base")
	service := NewGitService()
	writeTestFile(t, root, relativePath, "changed\n")
	if _, err := service.Discard(context.Background(), root, []string{relativePath}, currentGitTestFingerprint(t, service, root)); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, relativePath))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "base\n" {
		t.Fatalf("POSIX backslash path was not discarded exactly: %q", content)
	}
}
