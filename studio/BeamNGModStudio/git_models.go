package main

import "errors"

const (
	GitRepositoryStateRepository   = "repository"
	GitRepositoryStateNoRepository = "no_repository"
	GitRepositoryStateUnborn       = "unborn"
	GitRepositoryStateMerge        = "merge"

	GitDiffComparisonWorking   = "working"
	GitDiffComparisonIndex     = "index"
	GitDiffComparisonUntracked = "untracked"
)

// ErrGitNoRepository is returned by operations that require a repository when
// WorkspaceRecord.FilesRoot does not contain its own usable .git metadata.
var ErrGitNoRepository = errors.New("workspace files root is not a Git repository")

// GitRemote describes one remote configured in the workspace repository.
type GitRemote struct {
	Name     string `json:"name"`
	FetchURL string `json:"fetchURL"`
	PushURL  string `json:"pushURL"`
}

// GitStatusEntry is one path in a Git status group. IndexCode and
// WorktreeCode retain Git's two-column porcelain state so a path can be in
// both Staged and Unstaged at the same time.
type GitStatusEntry struct {
	Path         string `json:"path"`
	OriginalPath string `json:"originalPath,omitempty"`
	Status       string `json:"status"`
	IndexCode    string `json:"indexCode"`
	WorktreeCode string `json:"worktreeCode"`
	StatusCode   string `json:"statusCode"`
	Staged       bool   `json:"staged"`
	Unstaged     bool   `json:"unstaged"`
	Untracked    bool   `json:"untracked"`
	Conflict     bool   `json:"conflict"`
	Renamed      bool   `json:"renamed"`
	Copied       bool   `json:"copied"`
	Binary       bool   `json:"binary"`
	Deleted      bool   `json:"deleted"`
	Unsupported  bool   `json:"unsupported"`
}

// GitStatus is a snapshot of the exact repository rooted at a workspace's
// FilesRoot. Fingerprint is stable for an unchanged poll and changes when Git
// state or the relevant working-tree metadata changes.
type GitStatus struct {
	Repository      bool             `json:"repository"`
	State           string           `json:"state"`
	Clean           bool             `json:"clean"`
	Root            string           `json:"root"`
	Branch          string           `json:"branch"`
	Detached        bool             `json:"detached"`
	Upstream        string           `json:"upstream"`
	Ahead           int              `json:"ahead"`
	Behind          int              `json:"behind"`
	Remotes         []GitRemote      `json:"remotes"`
	Conflicts       []GitStatusEntry `json:"conflicts"`
	Staged          []GitStatusEntry `json:"staged"`
	Unstaged        []GitStatusEntry `json:"unstaged"`
	Untracked       []GitStatusEntry `json:"untracked"`
	Fingerprint     string           `json:"fingerprint"`
	OutputTruncated bool             `json:"outputTruncated"`
}

// GitDiscardFailure identifies a selected path that could not be discarded.
// Completed paths are reported separately so a partial command failure cannot
// be mistaken for an atomic batch.
type GitDiscardFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

type GitDiscardResult struct {
	Success         bool                `json:"success"`
	Stale           bool                `json:"stale"`
	Completed       []string            `json:"completed"`
	Failed          []GitDiscardFailure `json:"failed"`
	Error           string              `json:"error"`
	Status          GitStatus           `json:"status"`
	OutputTruncated bool                `json:"outputTruncated"`
}

// GitDiff is a real Git diff for one path. Patch is empty when Git has no
// textual patch to show; Binary, Unsupported, and Conflict explain why.
type GitDiff struct {
	Path            string `json:"path"`
	OriginalPath    string `json:"originalPath,omitempty"`
	Comparison      string `json:"comparison"`
	Available       bool   `json:"available"`
	Binary          bool   `json:"binary"`
	Unsupported     bool   `json:"unsupported"`
	Conflict        bool   `json:"conflict"`
	Status          string `json:"status"`
	Patch           string `json:"patch"`
	Message         string `json:"message"`
	OutputTruncated bool   `json:"outputTruncated"`
}

// GitCommitResult reports the commit made from the staged index only.
type GitCommitResult struct {
	Hash            string    `json:"hash"`
	ShortHash       string    `json:"shortHash"`
	Message         string    `json:"message"`
	Branch          string    `json:"branch"`
	Detached        bool      `json:"detached"`
	Amended         bool      `json:"amended"`
	Status          GitStatus `json:"status"`
	OutputTruncated bool      `json:"outputTruncated"`
}

// GitOperationStep records one conventional Git command in a remote or sync
// operation. Sync always records fetch, then pull, then push until a step
// fails.
type GitOperationStep struct {
	Operation       string `json:"operation"`
	Success         bool   `json:"success"`
	Output          string `json:"output"`
	Stderr          string `json:"stderr"`
	Error           string `json:"error"`
	OutputTruncated bool   `json:"outputTruncated"`
}

// GitOperationResult reports a remote operation and its refreshed status.
type GitOperationResult struct {
	Operation       string             `json:"operation"`
	Success         bool               `json:"success"`
	Output          string             `json:"output"`
	Stderr          string             `json:"stderr"`
	Error           string             `json:"error"`
	Steps           []GitOperationStep `json:"steps"`
	Status          GitStatus          `json:"status"`
	OutputTruncated bool               `json:"outputTruncated"`
}
type GitBranch struct {
	Name    string `json:"name"`
	Commit  string `json:"commit"`
	Current bool   `json:"current"`
}
