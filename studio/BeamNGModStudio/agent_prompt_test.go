package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestVirgilSystemPromptIncludesConditionalGitCommitGuidance(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := WorkspaceRecord{
		ID:        "prompt-workspace",
		Root:      root,
		FilesRoot: filepath.Join(root, "files"),
	}
	if err := os.MkdirAll(workspace.FilesRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &AgentManager{workspaceToolMu: map[string]*sync.Mutex{}}
	contextPath, err := manager.writeAgentContextContext(context.Background(), "prompt-run", workspace, LibraryItem{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(content)

	required := []string{
		"Follow these rules only when the person asks you to commit completed work or committing is an established part of the current request.",
		"Do not turn ordinary edits into automatic commits.",
		"This guidance is prompt-only",
		"do not add application-side Git behavior",
		"make remote calls",
		"real conventional Git repository",
		"WorkspaceRecord.FilesRoot (workspace/files)",
		".git directory or file",
		"metadata-containing workspace root",
		"only a parent repository",
		"inspect git status and the relevant diff",
		"staged and unstaged changes as needed",
		"git diff and git diff --cached",
		"one coherent, completed change per commit",
		"all files required to deliver one outcome",
		"unrelated edits must be excluded",
		"stage only those paths deliberately",
		"never stage broadly merely for convenience",
		"generated or build output",
		"temporary files",
		"credentials",
		"tokens",
		"private configuration",
		"any other secret material",
		"sensible completed boundary",
		"not after every individual file edit",
		"not by postponing all work into one giant final dump",
		"concise imperative subject",
		"Add a body when the reason, tradeoff, or important context is not obvious from the subject.",
		"After requesting the commit, rely on Git's result",
		"inspect the resulting state as appropriate",
		"Never claim success unless Git confirms the commit succeeded.",
		"report the actual failure",
		"leave the person's work intact",
		"explain what remains to be done",
		"never pretend that a commit exists",
		"If no Git repository is present, clearly say that you cannot create a Git commit there; do not simulate one.",
		"If a repository exists but there are no relevant changes, say there is nothing to commit; do not create an empty or invented commit.",
		"Never push or force-push",
		"amend",
		"rebase",
		"reset",
		"otherwise rewrite history",
		"unless the person explicitly requests that exact operation",
		"A commit request does not implicitly authorize any of those actions.",
	}
	for _, clause := range required {
		if !strings.Contains(prompt, clause) {
			t.Errorf("system prompt is missing Git guidance clause %q", clause)
		}
	}
	if count := strings.Count(prompt, "Git commit guidance (conditional):"); count != 1 {
		t.Fatalf("Git guidance appears %d times, want exactly once", count)
	}

	examplesStart := strings.Index(prompt, "These are the only commit-subject examples:")
	if examplesStart < 0 {
		t.Fatal("system prompt is missing the commit-subject examples heading")
	}
	examplesEnd := strings.Index(prompt[examplesStart:], "- Result and failure:")
	if examplesEnd < 0 {
		t.Fatal("system prompt is missing the result-and-failure section after examples")
	}
	examplesBlock := prompt[examplesStart : examplesStart+examplesEnd]
	wantExamples := []string{
		"feat(vehicle): add adjustable rear suspension",
		"fix(jbeam): correct malformed wheel node references",
		"refactor(lua): simplify boost controller state handling",
	}
	for _, example := range wantExamples {
		if count := strings.Count(examplesBlock, example); count != 1 {
			t.Errorf("commit subject example %q appears %d times in examples block", example, count)
		}
	}
	if count := strings.Count(examplesBlock, "\n  - "); count != len(wantExamples) {
		t.Errorf("examples block contains %d subject examples, want exactly %d", count, len(wantExamples))
	}
}
