package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// GitCommandRunner is the narrow command boundary used by GitService. The
// implementation always receives argv and a working directory; it must not
// interpret args as a shell command.
type GitCommandRunner interface {
	Run(ctx context.Context, executable, directory string, args ...string) (stdout, stderr string, err error)
}

type gitCommandRunnerFunc func(context.Context, string, string, ...string) (string, string, error)

func (runner gitCommandRunnerFunc) Run(ctx context.Context, executable, directory string, args ...string) (string, string, error) {
	return runner(ctx, executable, directory, args...)
}

const maxGitCommandOutputBytes = 2 << 20

type cappedGitOutput struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (output *cappedGitOutput) Write(value []byte) (int, error) {
	if output.limit <= 0 {
		output.truncated = output.truncated || len(value) != 0
		return len(value), nil
	}
	if len(value) > output.limit-output.Len() {
		remaining := output.limit - output.Len()
		if remaining > 0 {
			_, _ = output.Buffer.Write(value[:remaining])
		}
		output.truncated = true
		return len(value), nil
	}
	return output.Buffer.Write(value)
}

type gitOutputLimitError struct {
	err error
}

func (failure *gitOutputLimitError) Error() string {
	if failure == nil || failure.err == nil {
		return "Git command output exceeded the response limit"
	}
	return fmt.Sprintf("Git command output exceeded the response limit: %v", failure.err)
}

func (failure *gitOutputLimitError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.err
}

type execGitCommandRunner struct{}

func (execGitCommandRunner) Run(ctx context.Context, executable, directory string, args ...string) (string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = directory
	var stdout, stderr cappedGitOutput
	stdout.limit = maxGitCommandOutputBytes
	stderr.limit = maxGitCommandOutputBytes
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := runGitCommandContext(ctx, command)
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if stdout.truncated || stderr.truncated {
		err = &gitOutputLimitError{err: err}
	}
	return stdout.String(), stderr.String(), err
}

type gitWorkspaceGate struct {
	token chan struct{}
}

func newGitWorkspaceGate() *gitWorkspaceGate {
	gate := &gitWorkspaceGate{token: make(chan struct{}, 1)}
	gate.token <- struct{}{}
	return gate
}

func (gate *gitWorkspaceGate) lock(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gate.token:
		return nil
	}
}

func (gate *gitWorkspaceGate) unlock() {
	gate.token <- struct{}{}
}

type gitRemoteOperation struct {
	cancel context.CancelFunc
}

// GitService executes standard Git commands against exactly one workspace
// FilesRoot at a time. It has no metadata store and never starts a watcher.
type GitService struct {
	mu         sync.Mutex
	gates      map[string]*gitWorkspaceGate
	remoteOps  map[string][]*gitRemoteOperation
	runner     GitCommandRunner
	executable string
}

func NewGitService() *GitService {
	return &GitService{
		gates:      make(map[string]*gitWorkspaceGate),
		remoteOps:  make(map[string][]*gitRemoteOperation),
		runner:     execGitCommandRunner{},
		executable: "",
	}
}

// NewGitServiceWithRunner is useful for isolated command-boundary tests. The
// supplied runner still receives the exact executable, argv, and Dir that the
// production service would use.
func NewGitServiceWithRunner(runner GitCommandRunner) *GitService {
	service := NewGitService()
	if runner != nil {
		service.runner = runner
		service.executable = "git"
	}
	return service
}

func (service *GitService) gateFor(root string) *gitWorkspaceGate {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.gates == nil {
		service.gates = make(map[string]*gitWorkspaceGate)
	}
	gate := service.gates[root]
	if gate == nil {
		gate = newGitWorkspaceGate()
		service.gates[root] = gate
	}
	return gate
}

func (service *GitService) command(ctx context.Context, root string, args ...string) (string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	runner := service.runner
	if runner == nil {
		runner = execGitCommandRunner{}
	}
	executable := service.executable
	if executable == "" {
		var err error
		executable, err = gitLookPath("git")
		if err != nil {
			return "", "", fmt.Errorf("locate Git executable: %w", err)
		}
	}
	stdout, stderr, err := runner.Run(ctx, executable, root, args...)
	stdout, stdoutTruncated := capGitOutputString(stdout)
	stderr, stderrTruncated := capGitOutputString(stderr)
	if err != nil && ctx.Err() != nil {
		return stdout, stderr, ctx.Err()
	}
	if stdoutTruncated || stderrTruncated {
		err = &gitOutputLimitError{err: err}
	}
	return stdout, stderr, err
}

func capGitOutputString(value string) (string, bool) {
	if len(value) <= maxGitCommandOutputBytes {
		return value, false
	}
	return value[:maxGitCommandOutputBytes], true
}

func gitCommandSucceededWithTruncatedOutput(err error) bool {
	var outputLimit *gitOutputLimitError
	return errors.As(err, &outputLimit) && outputLimit.err == nil
}
func gitCommandOutputWasTruncated(err error) bool {
	var outputLimit *gitOutputLimitError
	return errors.As(err, &outputLimit)
}

func (service *GitService) withGate(ctx context.Context, root string, fn func() error) error {
	gate := service.gateFor(root)
	if err := gate.lock(ctx); err != nil {
		return err
	}
	defer gate.unlock()
	return fn()
}
func (service *GitService) withRemoteGate(ctx context.Context, root string, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	operationContext, cancel := context.WithCancel(ctx)
	operation := &gitRemoteOperation{cancel: cancel}
	service.mu.Lock()
	if service.remoteOps == nil {
		service.remoteOps = make(map[string][]*gitRemoteOperation)
	}
	service.remoteOps[root] = append(service.remoteOps[root], operation)
	service.mu.Unlock()

	gate := service.gateFor(root)
	if err := gate.lock(operationContext); err != nil {
		service.removeRemoteOperation(root, operation)
		cancel()
		return err
	}
	defer func() {
		gate.unlock()
		service.removeRemoteOperation(root, operation)
		cancel()
	}()
	return fn(operationContext)
}

func (service *GitService) removeRemoteOperation(root string, target *gitRemoteOperation) {
	service.mu.Lock()
	defer service.mu.Unlock()
	operations := service.remoteOps[root]
	for index, operation := range operations {
		if operation != target {
			continue
		}
		operations = append(operations[:index], operations[index+1:]...)
		break
	}
	if len(operations) == 0 {
		delete(service.remoteOps, root)
	} else {
		service.remoteOps[root] = operations
	}
}

func (service *GitService) CancelRemoteOperation(filesRoot string) bool {
	root, err := normalizeGitRoot(filesRoot)
	if err != nil {
		return false
	}
	service.mu.Lock()
	operations := append([]*gitRemoteOperation(nil), service.remoteOps[root]...)
	service.mu.Unlock()
	for _, operation := range operations {
		operation.cancel()
	}
	return len(operations) > 0
}

func (service *GitService) CancelAllRemoteOperations() {
	service.mu.Lock()
	operations := make([]*gitRemoteOperation, 0)
	for _, workspaceOperations := range service.remoteOps {
		operations = append(operations, workspaceOperations...)
	}
	service.remoteOps = make(map[string][]*gitRemoteOperation)
	service.mu.Unlock()
	for _, operation := range operations {
		operation.cancel()
	}
}

type gitCommandFailure struct {
	op     string
	stdout string
	stderr string
	err    error
}

func (failure *gitCommandFailure) Error() string {
	if failure == nil {
		return ""
	}
	message := strings.TrimSpace(failure.stderr)
	if message == "" {
		message = strings.TrimSpace(failure.stdout)
	}
	message = redactGitSecrets(message)
	if message == "" {
		if failure.err == nil {
			return failure.op
		}
		return fmt.Sprintf("%s: %v", failure.op, failure.err)
	}
	if failure.err == nil {
		return fmt.Sprintf("%s: %s", failure.op, message)
	}
	return fmt.Sprintf("%s: %s", failure.op, message)
}

func (failure *gitCommandFailure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.err
}

func commandFailure(op, stdout, stderr string, err error) error {
	if err == nil {
		return nil
	}
	return &gitCommandFailure{op: op, stdout: stdout, stderr: stderr, err: err}
}

var (
	gitUserInfoURLPattern = regexp.MustCompile(`(?i)((?:[a-z][a-z0-9+.-]*)://)[^/\s@]+@`)
	gitSecretQueryPattern = regexp.MustCompile(`(?i)([?&](?:access[_-]?token|api[_-]?key|password|passwd|secret|token)=[^&\s]+)`)
	gitSecretKVPattern    = regexp.MustCompile(`(?i)(\b(?:authorization|credential|password|passwd|secret|token)\s*[:=]\s*)[^\s]+`)
)

func redactGitSecrets(value string) string {
	value = gitUserInfoURLPattern.ReplaceAllString(value, `${1}<redacted>@`)
	value = gitSecretQueryPattern.ReplaceAllStringFunc(value, func(match string) string {
		equal := strings.IndexByte(match, '=')
		if equal < 0 {
			return match
		}
		return match[:equal+1] + "<redacted>"
	})
	value = gitSecretKVPattern.ReplaceAllString(value, `${1}<redacted>`)
	return value
}

func normalizeGitRoot(filesRoot string) (string, error) {
	if strings.TrimSpace(filesRoot) == "" {
		return "", errors.New("workspace files root is empty")
	}
	root, err := filepath.Abs(filepath.Clean(filesRoot))
	if err != nil {
		return "", fmt.Errorf("resolve workspace files root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("inspect workspace files root %q: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace files root %q is not a directory", root)
	}
	return root, nil
}
func sameGitPath(left, right string) bool {
	left, leftErr := filepath.Abs(filepath.Clean(left))
	right, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(left); err == nil {
		left = resolved
	}
	if resolved, err := filepath.EvalSymlinks(right); err == nil {
		right = resolved
	}
	if runtimeIsWindows() {
		return strings.EqualFold(filepath.ToSlash(left), filepath.ToSlash(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

// runtimeIsWindows is kept as a function to make path comparison explicit and
// testable without introducing platform-specific build files.
func runtimeIsWindows() bool {
	return os.PathSeparator == '\\'
}

func (service *GitService) repositoryState(ctx context.Context, root string) (bool, error) {
	metadataPath := filepath.Join(root, ".git")
	metadata, err := os.Lstat(metadataPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect Git metadata %q: %w", metadataPath, err)
	}
	if !metadata.IsDir() && !metadata.Mode().IsRegular() {
		return false, fmt.Errorf("Git metadata %q is neither a directory nor a Git worktree file", metadataPath)
	}

	stdout, stderr, err := service.command(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		return false, commandFailure("inspect Git repository root", stdout, stderr, err)
	}
	actual := strings.TrimSpace(strings.SplitN(stdout, "\n", 2)[0])
	if actual == "" {
		return false, errors.New("Git returned an empty repository root")
	}
	if !sameGitPath(root, actual) {
		return false, fmt.Errorf("Git repository root %q does not exactly match workspace FilesRoot %q", actual, root)
	}
	stdout, stderr, err = service.command(ctx, root, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false, commandFailure("verify Git work tree", stdout, stderr, err)
	}
	if strings.TrimSpace(stdout) != "true" {
		return false, errors.New("workspace Git repository is not a work tree")
	}
	return true, nil
}

func (service *GitService) Status(ctx context.Context, filesRoot string) (GitStatus, error) {
	root, err := normalizeGitRoot(filesRoot)
	if err != nil {
		return GitStatus{}, err
	}
	var result GitStatus
	err = service.withGate(ctx, root, func() error {
		result, err = service.statusLocked(ctx, root)
		return err
	})
	return result, err
}

func noRepositoryStatus(root string) GitStatus {
	fingerprint := sha256.Sum256([]byte(GitRepositoryStateNoRepository + "\x00" + root))
	return GitStatus{
		Repository:  false,
		State:       GitRepositoryStateNoRepository,
		Clean:       false,
		Root:        root,
		Remotes:     []GitRemote{},
		Conflicts:   []GitStatusEntry{},
		Staged:      []GitStatusEntry{},
		Unstaged:    []GitStatusEntry{},
		Untracked:   []GitStatusEntry{},
		Fingerprint: hex.EncodeToString(fingerprint[:]),
	}
}

type parsedGitStatus struct {
	branch   string
	detached bool
	unborn   bool
	upstream string
	ahead    int
	behind   int
	entries  []GitStatusEntry
	raw      string
}

func (service *GitService) statusLocked(ctx context.Context, root string) (GitStatus, error) {
	isRepo, err := service.repositoryState(ctx, root)
	if err != nil {
		return GitStatus{}, err
	}
	if !isRepo {
		return noRepositoryStatus(root), nil
	}
	stdout, stderr, err := service.command(ctx, root, "status", "--porcelain=v2", "--branch", "--untracked-files=all", "--renames", "-z", "--")
	if err != nil {
		return GitStatus{}, commandFailure("read Git status", stdout, stderr, err)
	}
	parsed, err := parseGitStatus(stdout)
	if err != nil {
		return GitStatus{}, err
	}
	remotes, remoteRaw, err := service.readGitRemotes(ctx, root)
	if err != nil {
		return GitStatus{}, err
	}
	workingNumstat, workingErr := service.optionalGitNumstat(ctx, root, false)
	if workingErr != nil {
		var outputLimit *gitOutputLimitError
		if errors.As(workingErr, &outputLimit) {
			return GitStatus{}, fmt.Errorf("Git working-tree diff metadata exceeded the response limit: %w", workingErr)
		}
		if ctx != nil && ctx.Err() != nil {
			return GitStatus{}, workingErr
		}
	}
	indexNumstat, indexErr := service.optionalGitNumstat(ctx, root, true)
	if indexErr != nil {
		var outputLimit *gitOutputLimitError
		if errors.As(indexErr, &outputLimit) {
			return GitStatus{}, fmt.Errorf("Git index diff metadata exceeded the response limit: %w", indexErr)
		}
		if ctx != nil && ctx.Err() != nil {
			return GitStatus{}, indexErr
		}
	}
	applyGitBinaryMetadata(parsed.entries, workingNumstat)
	applyGitBinaryMetadata(parsed.entries, indexNumstat)

	status := GitStatus{
		Repository: true,
		State:      GitRepositoryStateRepository,
		Root:       root,
		Branch:     parsed.branch,
		Detached:   parsed.detached,
		Upstream:   parsed.upstream,
		Ahead:      parsed.ahead,
		Behind:     parsed.behind,
		Remotes:    remotes,
		Conflicts:  make([]GitStatusEntry, 0),
		Staged:     make([]GitStatusEntry, 0),
		Unstaged:   make([]GitStatusEntry, 0),
		Untracked:  make([]GitStatusEntry, 0),
	}
	for _, entry := range parsed.entries {
		switch {
		case entry.Conflict:
			status.Conflicts = append(status.Conflicts, entry)
		case entry.Untracked:
			status.Untracked = append(status.Untracked, entry)
		default:
			if entry.Staged {
				status.Staged = append(status.Staged, entry)
			}
			if entry.Unstaged {
				status.Unstaged = append(status.Unstaged, entry)
			}
		}
	}
	status.Clean = len(status.Conflicts) == 0 && len(status.Staged) == 0 && len(status.Unstaged) == 0 && len(status.Untracked) == 0
	if len(status.Conflicts) != 0 {
		status.State = GitRepositoryStateMerge
	} else if parsed.unborn {
		status.State = GitRepositoryStateUnborn
	}
	sortGitStatusEntries(status.Conflicts)
	sortGitStatusEntries(status.Staged)
	sortGitStatusEntries(status.Unstaged)
	sortGitStatusEntries(status.Untracked)
	status.Fingerprint = gitStatusFingerprint(status, parsed.raw, remoteRaw, workingNumstat, indexNumstat)
	return status, nil
}

func parseGitStatus(raw string) (parsedGitStatus, error) {
	parsed := parsedGitStatus{raw: raw}
	nulDelimited := strings.Contains(raw, "\x00")
	tokens := strings.Split(raw, "\x00")
	if !nulDelimited {
		tokens = strings.Split(raw, "\n")
	}
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if !nulDelimited {
			token = strings.TrimSuffix(token, "\r")
		}
		if token == "" {
			continue
		}
		if strings.HasPrefix(token, "# ") {
			parseGitBranchHeader(&parsed, token)
			continue
		}
		if strings.HasPrefix(token, "1 ") {
			parts := strings.SplitN(token, " ", 9)
			if len(parts) != 9 {
				return parsed, fmt.Errorf("invalid Git porcelain v2 ordinary status record %q", token)
			}
			entry := makeGitStatusEntry(parts[1], parts[8], "")
			parsed.entries = append(parsed.entries, entry)
			continue
		}
		if strings.HasPrefix(token, "2 ") {
			parts := strings.SplitN(token, " ", 10)
			if len(parts) != 10 {
				return parsed, fmt.Errorf("invalid Git porcelain v2 rename status record %q", token)
			}
			original := ""
			if index+1 < len(tokens) {
				index++
				original = filepath.ToSlash(tokens[index])
			}
			entry := makeGitStatusEntry(parts[1], parts[9], original)
			entry.Renamed = parts[1][0] == 'R' || parts[1][1] == 'R'
			entry.Copied = parts[1][0] == 'C' || parts[1][1] == 'C'
			parsed.entries = append(parsed.entries, entry)
			continue
		}
		if strings.HasPrefix(token, "u ") {
			parts := strings.SplitN(token, " ", 11)
			if len(parts) != 11 {
				return parsed, fmt.Errorf("invalid Git porcelain v2 unmerged status record %q", token)
			}
			entry := makeGitStatusEntry(parts[1], parts[10], "")
			entry.Conflict = true
			entry.Status = "conflict"
			parsed.entries = append(parsed.entries, entry)
			continue
		}
		if strings.HasPrefix(token, "? ") {
			entry := makeGitStatusEntry("??", strings.TrimPrefix(token, "? "), "")
			entry.Untracked = true
			entry.Status = "untracked"
			parsed.entries = append(parsed.entries, entry)
			continue
		}
		if strings.HasPrefix(token, "! ") {
			continue
		}
		// A fake command or an older Git may provide porcelain v1. Accepting it
		// costs nothing and keeps the command boundary straightforward to test.
		if len(token) >= 3 && token[2] == ' ' {
			entry := makeGitStatusEntry(token[:2], token[3:], "")
			if token[:2] == "??" {
				entry.Untracked = true
				entry.Status = "untracked"
			}
			if (token[0] == 'R' || token[1] == 'R') && index+1 < len(tokens) {
				index++
				entry.OriginalPath = filepath.ToSlash(tokens[index])
				entry.Renamed = true
			}
			parsed.entries = append(parsed.entries, entry)
		}
	}
	return parsed, nil
}

func parseGitBranchHeader(parsed *parsedGitStatus, token string) {
	fields := strings.Fields(token)
	if len(fields) < 3 {
		return
	}
	switch fields[1] {
	case "branch.oid":
		parsed.unborn = fields[2] == "(initial)"
	case "branch.head":
		if fields[2] == "(detached)" {
			parsed.detached = true
			parsed.branch = ""
		} else {
			parsed.branch = fields[2]
		}
	case "branch.upstream":
		parsed.upstream = strings.Join(fields[2:], " ")
	case "branch.ab":
		if len(fields) >= 4 {
			parsed.ahead = parseGitCount(fields[2], '+')
			parsed.behind = parseGitCount(fields[3], '-')
		}
	}
}

func parseGitCount(value string, prefix byte) int {
	if len(value) < 2 || value[0] != prefix {
		return 0
	}
	count, err := strconv.Atoi(value[1:])
	if err != nil || count < 0 {
		return 0
	}
	return count
}

func makeGitStatusEntry(code, entryPath, originalPath string) GitStatusEntry {
	entryPath = filepath.ToSlash(entryPath)
	originalPath = filepath.ToSlash(originalPath)
	if len(code) < 2 {
		code = "  "
	}
	indexCode, worktreeCode := code[:1], code[1:2]
	entry := GitStatusEntry{
		Path:         entryPath,
		OriginalPath: originalPath,
		IndexCode:    indexCode,
		WorktreeCode: worktreeCode,
		StatusCode:   code[:2],
		Staged:       indexCode != " " && indexCode != "." && indexCode != "?" && indexCode != "!",
		Unstaged:     worktreeCode != " " && worktreeCode != "." && worktreeCode != "?" && worktreeCode != "!",
		Renamed:      indexCode == "R" || worktreeCode == "R",
		Copied:       indexCode == "C" || worktreeCode == "C",
		Deleted:      indexCode == "D" || worktreeCode == "D",
		Conflict:     indexCode == "U" || worktreeCode == "U" || strings.Contains(code, "U"),
	}
	if code == "??" {
		entry.Untracked = true
		entry.Staged = false
		entry.Unstaged = false
	}
	entry.Status = gitSemanticStatus(indexCode, worktreeCode, entry)
	if entry.Conflict {
		entry.Status = "conflict"
	}
	return entry
}

func gitSemanticStatus(indexCode, worktreeCode string, entry GitStatusEntry) string {
	for _, code := range []string{indexCode, worktreeCode} {
		switch code {
		case "A":
			return "added"
		case "M":
			return "modified"
		case "D":
			return "deleted"
		case "R":
			return "renamed"
		case "C":
			return "copied"
		case "T":
			return "type_changed"
		}
	}
	if entry.Untracked {
		return "untracked"
	}
	return "changed"
}

func sortGitStatusEntries(entries []GitStatusEntry) {
	sort.SliceStable(entries, func(left, right int) bool {
		return entries[left].Path < entries[right].Path
	})
}

type gitNumstat struct {
	binary bool
}

func (service *GitService) optionalGitNumstat(ctx context.Context, root string, cached bool) (map[string]gitNumstat, error) {
	args := []string{"diff", "--numstat", "--no-renames", "-z"}
	if cached {
		args = append(args, "--cached")
	}
	args = append(args, "--")
	stdout, stderr, err := service.command(ctx, root, args...)
	if err != nil {
		return nil, err
	}
	result := make(map[string]gitNumstat)
	for _, token := range strings.Split(stdout, "\x00") {
		fields := strings.SplitN(token, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		result[filepath.ToSlash(fields[2])] = gitNumstat{binary: fields[0] == "-" || fields[1] == "-"}
	}
	_ = stderr
	return result, nil
}

func applyGitBinaryMetadata(entries []GitStatusEntry, numstat map[string]gitNumstat) {
	if len(numstat) == 0 {
		return
	}
	for index := range entries {
		metadata, ok := numstat[entries[index].Path]
		if !ok && entries[index].OriginalPath != "" {
			metadata, ok = numstat[entries[index].OriginalPath]
		}
		if !ok || !metadata.binary {
			continue
		}
		entries[index].Binary = true
		entries[index].Unsupported = true
	}
}

func (service *GitService) readGitRemotes(ctx context.Context, root string) ([]GitRemote, string, error) {
	stdout, stderr, err := service.command(ctx, root, "remote", "-v")
	if err != nil {
		return nil, "", commandFailure("read Git remotes", stdout, stderr, err)
	}
	remotes := make([]GitRemote, 0)
	byName := make(map[string]int)
	for _, line := range strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		name, remoteURL, direction := fields[0], redactGitSecrets(fields[1]), fields[len(fields)-1]
		index, ok := byName[name]
		if !ok {
			byName[name] = len(remotes)
			remotes = append(remotes, GitRemote{Name: name})
			index = len(remotes) - 1
		}
		if direction == "(fetch)" {
			remotes[index].FetchURL = remoteURL
		} else if direction == "(push)" {
			remotes[index].PushURL = remoteURL
		}
	}
	sort.SliceStable(remotes, func(left, right int) bool { return remotes[left].Name < remotes[right].Name })
	return remotes, stdout, nil
}

func gitStatusFingerprint(status GitStatus, rawStatus, rawRemotes string, workingNumstat, indexNumstat map[string]gitNumstat) string {
	builder := strings.Builder{}
	builder.WriteString(rawStatus)
	builder.WriteByte(0)
	builder.WriteString(rawRemotes)
	builder.WriteByte(0)
	for _, group := range [][]GitStatusEntry{status.Conflicts, status.Staged, status.Unstaged, status.Untracked} {
		for _, entry := range group {
			builder.WriteString(entry.Path)
			builder.WriteByte(0)
			builder.WriteString(entry.OriginalPath)
			builder.WriteByte(0)
			builder.WriteString(entry.StatusCode)
			builder.WriteByte(0)
			builder.WriteString(strconv.FormatBool(entry.Binary))
			builder.WriteByte(0)
			appendGitPathStat(&builder, status.Root, entry.Path)
		}
	}
	appendGitNumstatFingerprint(&builder, workingNumstat)
	appendGitNumstatFingerprint(&builder, indexNumstat)
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

func appendGitPathStat(builder *strings.Builder, root, relativePath string) {
	filename := filepath.Join(root, filepath.FromSlash(relativePath))
	info, err := os.Lstat(filename)
	if err != nil {
		builder.WriteString("missing:")
		builder.WriteString(err.Error())
		builder.WriteByte(0)
		return
	}
	builder.WriteString(strconv.FormatInt(info.Size(), 10))
	builder.WriteByte(':')
	builder.WriteString(strconv.FormatInt(info.ModTime().UnixNano(), 10))
	builder.WriteByte(':')
	builder.WriteString(strconv.FormatUint(uint64(info.Mode()), 10))
	builder.WriteByte(0)
}

func appendGitNumstatFingerprint(builder *strings.Builder, values map[string]gitNumstat) {
	paths := make([]string, 0, len(values))
	for value := range values {
		paths = append(paths, value)
	}
	sort.Strings(paths)
	for _, value := range paths {
		builder.WriteString(value)
		builder.WriteByte(':')
		builder.WriteString(strconv.FormatBool(values[value].binary))
		builder.WriteByte(0)
	}
}

func validateGitRelativePath(root, value string, allowRoot bool) (string, error) {
	original := value
	if runtimeIsWindows() {
		value = strings.ReplaceAll(value, "\\", "/")
	}
	if value == "" || strings.IndexByte(value, 0) >= 0 || strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("unsafe Git path %q", original)
	}
	if runtimeIsWindows() && (strings.Contains(value, ":") || filepath.VolumeName(value) != "") {
		return "", fmt.Errorf("unsafe Git path %q", original)
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		if allowRoot {
			return ".", nil
		}
		return "", fmt.Errorf("Git path %q refers to the workspace root", original)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("Git path %q escapes the workspace root", original)
	}
	for _, component := range strings.Split(cleaned, "/") {
		if strings.EqualFold(component, ".git") {
			return "", fmt.Errorf("Git metadata path %q is not an editable workspace path", original)
		}
	}
	candidate := filepath.Join(root, filepath.FromSlash(cleaned))
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("Git path %q escapes the workspace root", original)
	}
	if err := validateGitResolvedPath(root, candidate); err != nil {
		return "", fmt.Errorf("unsafe Git path %q: %w", original, err)
	}
	return filepath.ToSlash(cleaned), nil
}

func validateGitResolvedPath(root, candidate string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return err
	}
	probe := candidate
	for {
		resolved, resolveErr := filepath.EvalSymlinks(probe)
		if resolveErr == nil {
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return err
			}
			if !pathWithinRoot(resolvedRoot, resolved) {
				return errors.New("path resolves outside the workspace root")
			}
			return nil
		}
		if !errors.Is(resolveErr, os.ErrNotExist) {
			return resolveErr
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return nil
		}
		probe = parent
	}
}

func pathWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return false
	}
	if runtimeIsWindows() {
		return strings.EqualFold(filepath.VolumeName(root), filepath.VolumeName(candidate))
	}
	return true
}

func normalizeGitPaths(root string, values []string) ([]string, bool, error) {
	if len(values) == 0 {
		return nil, false, errors.New("Git paths must be explicit; use \".\" to select all workspace paths")
	}
	paths := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	all := false
	for _, value := range values {
		if value == "." {
			if len(values) != 1 {
				return nil, false, errors.New("Git all-path sentinel \".\" must be the only selected path")
			}
			all = true
			continue
		}
		normalized, err := validateGitRelativePath(root, value, false)
		if err != nil {
			return nil, false, err
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		paths = append(paths, normalized)
	}
	if all {
		return nil, true, nil
	}
	sort.Strings(paths)
	return paths, false, nil
}

func literalGitPath(value string) string {
	return ":(literal)" + value
}

func appendGitPathArgs(args []string, all bool, paths []string) []string {
	args = append(args, "--")
	if all {
		return append(args, ".")
	}
	for _, value := range paths {
		args = append(args, literalGitPath(value))
	}
	return args
}

func isGitUnbornRestoreError(output string) bool {
	output = strings.ToLower(output)
	return strings.Contains(output, "could not resolve head") ||
		strings.Contains(output, "could not resolve 'head'") ||
		strings.Contains(output, "ambiguous argument 'head'") ||
		strings.Contains(output, "does not have any commits yet")
}

func requireGitStatusFingerprint(expectedFingerprint, currentFingerprint string) error {
	if strings.TrimSpace(expectedFingerprint) == "" {
		return errors.New("Git operation requires the confirmed status fingerprint")
	}
	if expectedFingerprint != currentFingerprint {
		return fmt.Errorf("Git status changed since confirmation (expected fingerprint %q, current %q)", expectedFingerprint, currentFingerprint)
	}
	return nil
}

func (service *GitService) stageOrUnstage(ctx context.Context, filesRoot string, paths []string, stage bool, expectedFingerprint string) (GitStatus, error) {
	root, err := normalizeGitRoot(filesRoot)
	if err != nil {
		return GitStatus{}, err
	}
	var result GitStatus
	err = service.withGate(ctx, root, func() error {
		normalized, all, pathErr := normalizeGitPaths(root, paths)
		if pathErr != nil {
			return pathErr
		}
		status, statusErr := service.statusLocked(ctx, root)
		if statusErr != nil {
			return statusErr
		}
		result = status
		if fingerprintErr := requireGitStatusFingerprint(expectedFingerprint, status.Fingerprint); fingerprintErr != nil {
			return fingerprintErr
		}
		if !status.Repository {
			return ErrGitNoRepository
		}
		if !stage {
			conflicts := make(map[string]struct{}, len(status.Conflicts))
			for _, entry := range status.Conflicts {
				conflicts[entry.Path] = struct{}{}
			}
			if all && len(conflicts) != 0 {
				normalized = normalized[:0]
				for _, entry := range status.Staged {
					if _, conflict := conflicts[entry.Path]; !conflict {
						normalized = append(normalized, entry.Path)
					}
				}
				sort.Strings(normalized)
				all = false
				if len(normalized) == 0 {
					return nil
				}
			} else if !all {
				for _, entryPath := range normalized {
					if _, conflict := conflicts[entryPath]; conflict {
						return fmt.Errorf("cannot unstage conflicted Git path %q; resolve it through normal editing and Git operations", entryPath)
					}
				}
			}
		}
		args := make([]string, 0, 4+len(normalized))
		if stage {
			args = append(args, "add")
			if all {
				args = append(args, "-A")
			}
		} else {
			args = append(args, "restore", "--staged")
		}
		args = appendGitPathArgs(args, all, normalized)
		stdout, stderr, commandErr := service.command(ctx, root, args...)
		outputTruncated := gitCommandSucceededWithTruncatedOutput(commandErr)
		if outputTruncated {
			commandErr = nil
		}
		if commandErr != nil && !stage && isGitUnbornRestoreError(stderr+"\n"+stdout) {
			fallbackArgs := appendGitPathArgs([]string{"reset"}, all, normalized)
			stdout, stderr, commandErr = service.command(ctx, root, fallbackArgs...)
			if gitCommandSucceededWithTruncatedOutput(commandErr) {
				outputTruncated = true
				commandErr = nil
			}
		}
		if commandErr != nil {
			return commandFailure(map[bool]string{true: "stage Git paths", false: "unstage Git paths"}[stage], stdout, stderr, commandErr)
		}
		result, statusErr = service.statusLocked(ctx, root)
		if statusErr != nil {
			return statusErr
		}
		result.OutputTruncated = outputTruncated
		return nil
	})
	return result, err
}
func (service *GitService) Stage(ctx context.Context, filesRoot string, paths []string, expectedFingerprint string) (GitStatus, error) {
	return service.stageOrUnstage(ctx, filesRoot, paths, true, expectedFingerprint)
}

func (service *GitService) Unstage(ctx context.Context, filesRoot string, paths []string, expectedFingerprint string) (GitStatus, error) {
	return service.stageOrUnstage(ctx, filesRoot, paths, false, expectedFingerprint)
}

func collectGitStatusEntries(status GitStatus) map[string]GitStatusEntry {
	entries := make(map[string]GitStatusEntry)
	for _, group := range [][]GitStatusEntry{status.Conflicts, status.Staged, status.Unstaged, status.Untracked} {
		for _, entry := range group {
			previous, exists := entries[entry.Path]
			if !exists {
				entries[entry.Path] = entry
				continue
			}
			previous.Staged = previous.Staged || entry.Staged
			previous.Unstaged = previous.Unstaged || entry.Unstaged
			previous.Untracked = previous.Untracked || entry.Untracked
			previous.Conflict = previous.Conflict || entry.Conflict
			previous.Binary = previous.Binary || entry.Binary
			previous.Unsupported = previous.Unsupported || entry.Unsupported
			if previous.OriginalPath == "" {
				previous.OriginalPath = entry.OriginalPath
			}
			entries[entry.Path] = previous
		}
	}
	return entries
}

func appendDiscardFailures(result *GitDiscardResult, paths []string, message string) {
	for _, entryPath := range paths {
		result.Failed = append(result.Failed, GitDiscardFailure{Path: entryPath, Error: message})
	}
}

func (service *GitService) Discard(ctx context.Context, filesRoot string, paths []string, expectedFingerprint string) (GitDiscardResult, error) {
	root, err := normalizeGitRoot(filesRoot)
	if err != nil {
		return GitDiscardResult{}, err
	}
	result := GitDiscardResult{
		Completed: []string{},
		Failed:    []GitDiscardFailure{},
	}
	err = service.withGate(ctx, root, func() error {
		normalized, all, pathErr := normalizeGitPaths(root, paths)
		if pathErr != nil {
			result.Error = pathErr.Error()
			return pathErr
		}
		if all {
			result.Error = "discard requires explicit changed paths; \".\" is not allowed"
			return errors.New(result.Error)
		}
		status, statusErr := service.statusLocked(ctx, root)
		if statusErr != nil {
			return statusErr
		}
		result.Status = status
		if strings.TrimSpace(expectedFingerprint) == "" {
			result.Error = "discard requires the confirmed Git status fingerprint"
			return errors.New(result.Error)
		}
		if expectedFingerprint != status.Fingerprint {
			result.Stale = true
			result.Error = fmt.Sprintf("Git status changed since confirmation (expected fingerprint %q, current %q)", expectedFingerprint, status.Fingerprint)
			appendDiscardFailures(&result, normalized, result.Error)
			return nil
		}
		if !status.Repository {
			result.Error = ErrGitNoRepository.Error()
			return ErrGitNoRepository
		}
		entries := collectGitStatusEntries(status)
		if len(normalized) == 0 {
			result.Error = "no discardable working-tree changes selected"
			return errors.New(result.Error)
		}
		kind := make(map[string]bool, len(normalized))
		renameSources := make(map[string]string, len(normalized))
		for _, entryPath := range normalized {
			entry, exists := entries[entryPath]
			if !exists {
				result.Error = fmt.Sprintf("Git path %q has no pending change", entryPath)
				return errors.New(result.Error)
			}
			if entry.Conflict {
				result.Error = fmt.Sprintf("cannot discard conflicted Git path %q; resolve it through normal editing and Git operations", entryPath)
				return errors.New(result.Error)
			}
			if entry.Untracked {
				if entry.Staged || entry.Unstaged {
					result.Error = fmt.Sprintf("cannot discard staged Git path %q implicitly", entryPath)
					return errors.New(result.Error)
				}
				kind[entryPath] = true
				continue
			}
			if !entry.Unstaged {
				result.Error = fmt.Sprintf("Git path %q has no discardable working-tree change; staged content was preserved", entryPath)
				return errors.New(result.Error)
			}
			if entry.OriginalPath != "" && !entry.Staged {
				originalPath, originalErr := validateGitRelativePath(root, entry.OriginalPath, false)
				if originalErr != nil {
					result.Error = originalErr.Error()
					return originalErr
				}
				renameSources[entryPath] = originalPath
			}
		}

		lastFingerprint := status.Fingerprint
		for index, entryPath := range normalized {
			current, currentErr := service.statusLocked(ctx, root)
			if currentErr != nil {
				return currentErr
			}
			result.Status = current
			if current.Fingerprint != lastFingerprint {
				result.Stale = true
				result.Error = fmt.Sprintf("Git status changed before discarding %q", entryPath)
				appendDiscardFailures(&result, normalized[index:], result.Error)
				return nil
			}
			currentEntries := collectGitStatusEntries(current)
			currentEntry, exists := currentEntries[entryPath]
			if !exists || currentEntry.Conflict || (!currentEntry.Untracked && !currentEntry.Unstaged) {
				result.Stale = true
				result.Error = fmt.Sprintf("Git path %q changed before discard; confirmation is stale", entryPath)
				appendDiscardFailures(&result, normalized[index:], result.Error)
				return nil
			}
			if _, pathErr := validateGitRelativePath(root, entryPath, false); pathErr != nil {
				result.Error = pathErr.Error()
				appendDiscardFailures(&result, normalized[index:], result.Error)
				return nil
			}
			if originalPath, selectedRename := renameSources[entryPath]; selectedRename {
				if currentEntry.Staged || !currentEntry.Unstaged || currentEntry.OriginalPath == "" {
					result.Stale = true
					result.Error = fmt.Sprintf("Git path %q changed before discard; confirmation is stale", entryPath)
					appendDiscardFailures(&result, normalized[index:], result.Error)
					return nil
				}
				currentOriginal, originalErr := validateGitRelativePath(root, currentEntry.OriginalPath, false)
				if originalErr != nil {
					result.Error = originalErr.Error()
					appendDiscardFailures(&result, normalized[index:], result.Error)
					return nil
				}
				if currentOriginal != originalPath {
					result.Stale = true
					result.Error = fmt.Sprintf("Git path %q changed before discard; confirmation is stale", entryPath)
					appendDiscardFailures(&result, normalized[index:], result.Error)
					return nil
				}
			}
			discardCommands := make([][]string, 0, 2)
			if kind[entryPath] {
				discardCommands = append(discardCommands, appendGitPathArgs([]string{"clean", "-f"}, false, []string{entryPath}))
			} else if originalPath, selectedRename := renameSources[entryPath]; selectedRename {
				discardCommands = append(discardCommands, appendGitPathArgs([]string{"restore", "--worktree"}, false, []string{originalPath}))
				discardCommands = append(discardCommands, appendGitPathArgs([]string{"clean", "-f"}, false, []string{entryPath}))
			} else {
				discardCommands = append(discardCommands, appendGitPathArgs([]string{"restore", "--worktree"}, false, []string{entryPath}))
			}
			for _, args := range discardCommands {
				stdout, stderr, commandErr := service.command(ctx, root, args...)
				outputTruncated := gitCommandOutputWasTruncated(commandErr)
				if gitCommandSucceededWithTruncatedOutput(commandErr) {
					commandErr = nil
				}
				if commandErr != nil {
					failure := commandFailure("discard Git path", stdout, stderr, commandErr)
					result.Error = failure.Error()
					result.Failed = append(result.Failed, GitDiscardFailure{Path: entryPath, Error: result.Error})
					result.OutputTruncated = result.OutputTruncated || outputTruncated
					appendDiscardFailures(&result, normalized[index+1:], "not attempted after a previous discard failure")
					if ctx != nil && ctx.Err() != nil {
						return ctx.Err()
					}
					refreshed, refreshErr := service.statusLocked(ctx, root)
					if refreshErr != nil {
						return refreshErr
					}
					result.Status = refreshed
					return nil
				}
				result.OutputTruncated = result.OutputTruncated || outputTruncated
			}
			result.Completed = append(result.Completed, entryPath)
			refreshed, refreshErr := service.statusLocked(ctx, root)
			if refreshErr != nil {
				return refreshErr
			}
			result.Status = refreshed
			lastFingerprint = refreshed.Fingerprint
		}
		result.Success = len(result.Failed) == 0 && len(result.Completed) == len(normalized)
		if !result.Success && result.Error == "" {
			result.Error = "one or more Git paths could not be discarded"
		}
		return nil
	})
	if err != nil && result.Error == "" {
		result.Error = redactGitSecrets(err.Error())
	}
	return result, err
}

func gitPathLooksBinary(root, relativePath string) bool {
	file, err := os.Open(filepath.Join(root, filepath.FromSlash(relativePath)))
	if err != nil {
		return false
	}
	defer file.Close()
	buffer := make([]byte, 8192)
	count, err := file.Read(buffer)
	if count == 0 && err != nil {
		return false
	}
	return bytes.IndexByte(buffer[:count], 0) >= 0
}

func (service *GitService) Diff(ctx context.Context, filesRoot, relativePath, comparison string) (GitDiff, error) {
	root, err := normalizeGitRoot(filesRoot)
	if err != nil {
		return GitDiff{}, err
	}
	var result GitDiff
	err = service.withGate(ctx, root, func() error {
		if comparison != GitDiffComparisonWorking && comparison != GitDiffComparisonIndex && comparison != GitDiffComparisonUntracked {
			return fmt.Errorf("unsupported Git diff comparison %q", comparison)
		}
		cleanPath, pathErr := validateGitRelativePath(root, relativePath, false)
		if pathErr != nil {
			return pathErr
		}
		status, statusErr := service.statusLocked(ctx, root)
		if statusErr != nil {
			return statusErr
		}
		if !status.Repository {
			return ErrGitNoRepository
		}
		entry, found := collectGitStatusEntries(status)[cleanPath]
		if comparison == GitDiffComparisonUntracked && (!found || !entry.Untracked) {
			return fmt.Errorf("Git path %q is not untracked", cleanPath)
		}
		if !found {
			return fmt.Errorf("Git path %q has no pending change", cleanPath)
		}
		binary := false
		if comparison == GitDiffComparisonUntracked && !entry.Conflict {
			binary = gitPathLooksBinary(root, cleanPath)
		}
		if binary && !entry.Conflict {
			result = GitDiff{
				Path:         cleanPath,
				OriginalPath: entry.OriginalPath,
				Comparison:   comparison,
				Available:    true,
				Binary:       true,
				Unsupported:  true,
				Status:       entry.Status,
				Message:      "Git reports binary content; a textual diff is unavailable",
			}
			return nil
		}
		args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--find-renames"}
		if entry.Conflict {
			args = append(args, "--cc")
		}
		switch comparison {
		case GitDiffComparisonIndex:
			args = append(args, "--cached")
		case GitDiffComparisonUntracked:
			args = []string{"diff", "--no-index", "--no-ext-diff", "--no-textconv", "--no-color", "--", os.DevNull, filepath.ToSlash(filepath.Join(root, filepath.FromSlash(cleanPath)))}
		}
		if comparison != GitDiffComparisonUntracked {
			args = append(args, "--", literalGitPath(cleanPath))
		}
		stdout, stderr, commandErr := service.command(ctx, root, args...)
		if commandErr != nil {
			var outputLimit *gitOutputLimitError
			if errors.As(commandErr, &outputLimit) {
				result = GitDiff{
					Path:            cleanPath,
					OriginalPath:    entry.OriginalPath,
					Comparison:      comparison,
					Available:       false,
					Binary:          binary,
					Unsupported:     true,
					Conflict:        entry.Conflict,
					Status:          entry.Status,
					OutputTruncated: true,
					Message:         "Git diff exceeded the response limit; textual preview is unavailable",
				}
				return nil
			}
			if comparison != GitDiffComparisonUntracked || gitCommandExitCodeFromError(commandErr) != 1 {
				return commandFailure("read Git diff", stdout, stderr, commandErr)
			}
		}
		patch := stdout
		if strings.TrimSpace(patch) == "" && comparison == GitDiffComparisonUntracked {
			patch = stderr
		}
		binary = gitDiffLooksBinary(patch)
		message := ""
		unsupported := false
		if binary {
			patch = ""
			unsupported = true
		}
		if entry.Conflict {
			message = "Git path is unresolved; no side was selected"
		}
		if patch == "" && !binary && !entry.Conflict {
			unsupported = true
			message = "Git did not provide a textual diff for this path"
		}
		result = GitDiff{
			Path:         cleanPath,
			OriginalPath: entry.OriginalPath,
			Comparison:   comparison,
			Available:    patch != "" || binary || entry.Conflict,
			Binary:       binary,
			Unsupported:  unsupported,
			Status:       entry.Status,
			Conflict:     entry.Conflict,
			Patch:        patch,
			Message:      message,
		}
		return nil
	})
	return result, err
}

func gitDiffLooksBinary(patch string) bool {
	lower := strings.ToLower(patch)
	return strings.Contains(lower, "binary files ") || strings.Contains(lower, "git binary patch")
}

func gitCommandExitCodeFromError(err error) int {
	var exitError interface{ ExitCode() int }
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return 0
}

func validateGitBranchName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "-" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "\x00\r\n") {
		return "", fmt.Errorf("unsafe Git branch name %q", name)
	}
	return name, nil
}

func (service *GitService) ListBranches(ctx context.Context, filesRoot string) ([]GitBranch, error) {
	root, err := normalizeGitRoot(filesRoot)
	if err != nil {
		return nil, err
	}
	var branches []GitBranch
	err = service.withGate(ctx, root, func() error {
		status, statusErr := service.statusLocked(ctx, root)
		if statusErr != nil {
			return statusErr
		}
		if !status.Repository {
			return ErrGitNoRepository
		}
		stdout, stderr, commandErr := service.command(ctx, root, "for-each-ref", "--format=%(refname:short)\t%(objectname)", "refs/heads")
		if commandErr != nil {
			return commandFailure("list Git branches", stdout, stderr, commandErr)
		}
		branches = make([]GitBranch, 0)
		for _, line := range strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n") {
			fields := strings.SplitN(line, "\t", 2)
			if len(fields) == 0 || strings.TrimSpace(fields[0]) == "" {
				continue
			}
			branch := GitBranch{Name: fields[0], Current: !status.Detached && fields[0] == status.Branch}
			if len(fields) == 2 {
				branch.Commit = strings.TrimSpace(fields[1])
			}
			branches = append(branches, branch)
		}
		sort.SliceStable(branches, func(left, right int) bool { return branches[left].Name < branches[right].Name })
		return nil
	})
	return branches, err
}

func (service *GitService) CreateBranch(ctx context.Context, filesRoot, name, startPoint, expectedFingerprint string) (GitStatus, error) {
	root, err := normalizeGitRoot(filesRoot)
	if err != nil {
		return GitStatus{}, err
	}
	name, err = validateGitBranchName(name)
	if err != nil {
		return GitStatus{}, err
	}
	startPoint = strings.TrimSpace(startPoint)
	if strings.HasPrefix(startPoint, "-") || strings.ContainsAny(startPoint, "\x00\r\n") {
		return GitStatus{}, fmt.Errorf("unsafe Git branch start point %q", startPoint)
	}
	var result GitStatus
	err = service.withGate(ctx, root, func() error {
		status, statusErr := service.statusLocked(ctx, root)
		if statusErr != nil {
			return statusErr
		}
		result = status
		if fingerprintErr := requireGitStatusFingerprint(expectedFingerprint, status.Fingerprint); fingerprintErr != nil {
			return fingerprintErr
		}
		if !status.Repository {
			return ErrGitNoRepository
		}
		args := []string{"switch", "--create", name}
		if startPoint != "" {
			args = append(args, startPoint)
		}
		stdout, stderr, commandErr := service.command(ctx, root, args...)
		outputTruncated := gitCommandSucceededWithTruncatedOutput(commandErr)
		if outputTruncated {
			commandErr = nil
		}
		if commandErr != nil {
			return commandFailure("create Git branch", stdout, stderr, commandErr)
		}
		result, statusErr = service.statusLocked(ctx, root)
		if statusErr != nil {
			return statusErr
		}
		result.OutputTruncated = outputTruncated
		return nil
	})
	return result, err
}

func (service *GitService) SwitchBranch(ctx context.Context, filesRoot, name, expectedFingerprint string) (GitStatus, error) {
	root, err := normalizeGitRoot(filesRoot)
	if err != nil {
		return GitStatus{}, err
	}
	name, err = validateGitBranchName(name)
	if err != nil {
		return GitStatus{}, err
	}
	var result GitStatus
	err = service.withGate(ctx, root, func() error {
		status, statusErr := service.statusLocked(ctx, root)
		if statusErr != nil {
			return statusErr
		}
		result = status
		if fingerprintErr := requireGitStatusFingerprint(expectedFingerprint, status.Fingerprint); fingerprintErr != nil {
			return fingerprintErr
		}
		if !status.Repository {
			return ErrGitNoRepository
		}
		stdout, stderr, commandErr := service.command(ctx, root, "switch", "--", name)
		outputTruncated := gitCommandSucceededWithTruncatedOutput(commandErr)
		if outputTruncated {
			commandErr = nil
		}
		if commandErr != nil {
			return commandFailure("switch Git branch", stdout, stderr, commandErr)
		}
		result, statusErr = service.statusLocked(ctx, root)
		if statusErr != nil {
			return statusErr
		}
		result.OutputTruncated = outputTruncated
		return nil
	})
	return result, err
}

func (service *GitService) Commit(ctx context.Context, filesRoot, message string, amend bool, expectedFingerprint string) (GitCommitResult, error) {
	root, err := normalizeGitRoot(filesRoot)
	if err != nil {
		return GitCommitResult{}, err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return GitCommitResult{}, errors.New("Git commit message cannot be empty")
	}
	var result GitCommitResult
	err = service.withGate(ctx, root, func() error {
		status, statusErr := service.statusLocked(ctx, root)
		if statusErr != nil {
			return statusErr
		}
		result = GitCommitResult{Message: message, Amended: amend, Status: status}
		if fingerprintErr := requireGitStatusFingerprint(expectedFingerprint, status.Fingerprint); fingerprintErr != nil {
			return fingerprintErr
		}
		if !status.Repository {
			return ErrGitNoRepository
		}
		if len(status.Staged) == 0 && len(status.Conflicts) == 0 && !amend {
			return errors.New("Git commit requires at least one staged change")
		}
		if len(status.Conflicts) > 0 {
			return errors.New("Git commit is blocked while conflicts remain unresolved")
		}
		args := []string{"commit"}
		if amend {
			args = append(args, "--amend")
		}
		args = append(args, "--message", message)
		stdout, stderr, commandErr := service.command(ctx, root, args...)
		outputTruncated := gitCommandSucceededWithTruncatedOutput(commandErr)
		if outputTruncated {
			commandErr = nil
		}
		if commandErr != nil {
			refreshed, refreshErr := service.statusLocked(ctx, root)
			if refreshErr != nil {
				return refreshErr
			}
			result.Status = refreshed
			return commandFailure("commit staged Git changes", stdout, stderr, commandErr)
		}
		hashOutput, hashStderr, hashErr := service.command(ctx, root, "rev-parse", "HEAD")
		if hashErr != nil {
			refreshed, refreshErr := service.statusLocked(ctx, root)
			if refreshErr != nil {
				return refreshErr
			}
			result.Status = refreshed
			return commandFailure("read created Git commit", hashOutput, hashStderr, hashErr)
		}
		result.Hash = strings.TrimSpace(hashOutput)
		if len(result.Hash) > 7 {
			result.ShortHash = result.Hash[:7]
		} else {
			result.ShortHash = result.Hash
		}
		result.Status, statusErr = service.statusLocked(ctx, root)
		if statusErr != nil {
			return statusErr
		}
		result.Branch = result.Status.Branch
		result.Detached = result.Status.Detached
		result.OutputTruncated = outputTruncated
		return nil
	})
	return result, err
}

func (service *GitService) remoteOperation(ctx context.Context, filesRoot, operation, expectedFingerprint string, requireFingerprint bool, args ...string) (GitOperationResult, error) {
	root, err := normalizeGitRoot(filesRoot)
	if err != nil {
		return GitOperationResult{}, err
	}
	result := GitOperationResult{Operation: operation, Steps: []GitOperationStep{}}
	err = service.withRemoteGate(ctx, root, func(operationContext context.Context) error {
		status, statusErr := service.statusLocked(operationContext, root)
		if statusErr != nil {
			return statusErr
		}
		result.Status = status
		if !status.Repository {
			return ErrGitNoRepository
		}
		if requireFingerprint {
			if fingerprintErr := requireGitStatusFingerprint(expectedFingerprint, status.Fingerprint); fingerprintErr != nil {
				return fingerprintErr
			}
		}
		stdout, stderr, commandErr := service.command(operationContext, root, args...)
		outputTruncated := gitCommandOutputWasTruncated(commandErr)
		if gitCommandSucceededWithTruncatedOutput(commandErr) {
			commandErr = nil
		}
		step := GitOperationStep{
			Operation:       operation,
			Success:         commandErr == nil,
			Output:          redactGitSecrets(strings.TrimSpace(stdout)),
			Stderr:          redactGitSecrets(strings.TrimSpace(stderr)),
			OutputTruncated: outputTruncated,
		}
		if commandErr != nil {
			step.Error = commandFailure(operation+" Git repository", stdout, stderr, commandErr).Error()
			result.Steps = append(result.Steps, step)
			result.Error = step.Error
			result.Output = step.Output
			result.Stderr = step.Stderr
			result.OutputTruncated = outputTruncated
			refreshed, refreshErr := service.statusLocked(operationContext, root)
			if refreshErr != nil {
				return refreshErr
			}
			result.Status = refreshed
			if operationContext.Err() != nil {
				return operationContext.Err()
			}
			return nil
		}
		result.Steps = append(result.Steps, step)
		result.Success = true
		result.Output = step.Output
		result.Stderr = step.Stderr
		result.OutputTruncated = outputTruncated
		result.Status, statusErr = service.statusLocked(operationContext, root)
		return statusErr
	})
	if err != nil && result.Error == "" {
		result.Error = redactGitSecrets(err.Error())
	}
	return result, err
}

func (service *GitService) Fetch(ctx context.Context, filesRoot string) (GitOperationResult, error) {
	return service.remoteOperation(ctx, filesRoot, "fetch", "", false, "fetch", "--prune")
}

func (service *GitService) Pull(ctx context.Context, filesRoot, expectedFingerprint string) (GitOperationResult, error) {
	return service.remoteOperation(ctx, filesRoot, "pull", expectedFingerprint, true, "pull")
}

func (service *GitService) Push(ctx context.Context, filesRoot string) (GitOperationResult, error) {
	return service.remoteOperation(ctx, filesRoot, "push", "", false, "push")
}

func (service *GitService) Sync(ctx context.Context, filesRoot, expectedFingerprint string) (GitOperationResult, error) {
	root, err := normalizeGitRoot(filesRoot)
	if err != nil {
		return GitOperationResult{}, err
	}
	result := GitOperationResult{Operation: "sync", Steps: []GitOperationStep{}}
	err = service.withRemoteGate(ctx, root, func(operationContext context.Context) error {
		status, statusErr := service.statusLocked(operationContext, root)
		if statusErr != nil {
			return statusErr
		}
		result.Status = status
		if !status.Repository {
			return ErrGitNoRepository
		}
		if fingerprintErr := requireGitStatusFingerprint(expectedFingerprint, status.Fingerprint); fingerprintErr != nil {
			return fingerprintErr
		}
		for _, stepSpec := range []struct {
			name string
			args []string
		}{
			{name: "fetch", args: []string{"fetch", "--prune"}},
			{name: "pull", args: []string{"pull"}},
			{name: "push", args: []string{"push"}},
		} {
			stdout, stderr, commandErr := service.command(operationContext, root, stepSpec.args...)
			outputTruncated := gitCommandOutputWasTruncated(commandErr)
			if gitCommandSucceededWithTruncatedOutput(commandErr) {
				commandErr = nil
			}
			step := GitOperationStep{
				Operation:       stepSpec.name,
				Success:         commandErr == nil,
				Output:          redactGitSecrets(strings.TrimSpace(stdout)),
				Stderr:          redactGitSecrets(strings.TrimSpace(stderr)),
				OutputTruncated: outputTruncated,
			}
			if commandErr != nil {
				step.Error = commandFailure(stepSpec.name+" Git repository", stdout, stderr, commandErr).Error()
				result.Steps = append(result.Steps, step)
				result.Error = step.Error
				result.Output = step.Output
				result.Stderr = step.Stderr
				result.OutputTruncated = outputTruncated
				refreshed, refreshErr := service.statusLocked(operationContext, root)
				if refreshErr != nil {
					return refreshErr
				}
				result.Status = refreshed
				if operationContext.Err() != nil {
					return operationContext.Err()
				}
				return nil
			}
			result.Steps = append(result.Steps, step)
			result.Output = step.Output
			result.Stderr = step.Stderr
			result.OutputTruncated = result.OutputTruncated || outputTruncated
		}
		result.Success = true
		result.Status, statusErr = service.statusLocked(operationContext, root)
		return statusErr
	})
	if err != nil && result.Error == "" {
		result.Error = redactGitSecrets(err.Error())
	}
	return result, err
}
