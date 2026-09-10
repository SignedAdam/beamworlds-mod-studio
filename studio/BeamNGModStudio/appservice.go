package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

type appGitRemoteOperation struct {
	cancel context.CancelFunc
}

type AppService struct {
	config        AppConfig
	store         *Store
	library       *LibraryEngine
	agents        *AgentManager
	aiRuntime     *managedAIRuntime
	git           *GitService
	githubPublish *GitHubPublishService
	emit          func(string, any)
	profileMu     sync.Mutex
	auditMu       sync.Mutex
	virusMu       sync.Mutex
	auditAI       auditAIRunner
	aiLoginMu     sync.Mutex
	aiLogins      map[string]*aiLoginProcess
	aiConnecting  map[string]bool
	aiAuthCtx     context.Context
	aiAuthCancel  context.CancelFunc
	startProcess  func(string, []string, string) (ProcessLaunch, error)
	gameRunning   func() (bool, error)
	gitRemoteMu   sync.Mutex
	gitRemoteOps  map[string][]*appGitRemoteOperation
}

type WorkspaceDetail struct {
	Workspace         WorkspaceRecord         `json:"workspace"`
	Entity            LibraryItem             `json:"entity"`
	Files             []modkit.FileSnapshot   `json:"files"`
	Directories       []string                `json:"directories"`
	Drafts            []WorkspaceDraft        `json:"drafts"`
	Validation        modkit.ValidationResult `json:"validation"`
	Exports           []ExportRecord          `json:"exports"`
	VirgilSessions    []VirgilSessionRecord   `json:"virgilSessions"`
	Knowledge         []KnowledgeDocument     `json:"knowledge"`
	ActiveTest        *TestInstallRecord      `json:"activeTest,omitempty"`
	DiskBytes         int64                   `json:"diskBytes"`
	GitInitialization GitInitializationResult `json:"gitInitialization"`
}

type WorkspaceTextFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
}

type WorkspaceSearchOptions struct {
	Query         string `json:"query"`
	Regex         bool   `json:"regex"`
	CaseSensitive bool   `json:"caseSensitive"`
}

type WorkspaceSearchMatch struct {
	RelativePath string `json:"relativePath"`
	Line         int    `json:"line"`
	Column       int    `json:"column"`
	MatchLength  int    `json:"matchLength"`
	Preview      string `json:"preview"`
}

type ExportResponse struct {
	Record ExportRecord        `json:"record"`
	Result modkit.ExportResult `json:"result"`
}

func NewAppService(config AppConfig, store *Store, emit func(string, any)) *AppService {
	agents := NewAgentManager(store, config, emit)
	aiAuthCtx, aiAuthCancel := context.WithCancel(context.Background())
	git := NewGitService()
	service := &AppService{
		config:       config,
		store:        store,
		library:      NewLibraryEngine(store, config, emit),
		agents:       agents,
		aiRuntime:    agents.runtime,
		emit:         emit,
		aiLogins:     make(map[string]*aiLoginProcess),
		aiConnecting: make(map[string]bool),
		aiAuthCtx:    aiAuthCtx,
		aiAuthCancel: aiAuthCancel,
		startProcess: startDetachedProcess,
		gameRunning:  beamNGProcessRunning,
		git:          git,
		gitRemoteOps: make(map[string][]*appGitRemoteOperation),
	}
	service.githubPublish = NewGitHubPublishServiceWithDependencies(GitHubPublishDependencies{Git: git, WorkspaceLock: agents.workspaceToolMutex})
	service.auditAI = service.runManagedAIAudit
	// An already-indexed library never triggers a scan, so seeding has to be
	// attempted here too; it is a no-op once the marker is written.
	service.seedDefaultPlayProfile(context.Background())
	return service
}

func (service *AppService) Config() AppConfig { return service.config }

func (service *AppService) Dashboard() (Dashboard, error) {
	return service.store.Dashboard(context.Background(), service.config.DatabasePath)
}

func (service *AppService) ListLibrary(health, kind, query, collectionID string) ([]LibraryItem, error) {
	return service.store.ListLibrary(context.Background(), health, kind, query, collectionID)
}

func (service *AppService) GetEntity(entityID string) (EntityDetail, error) {
	return service.store.GetEntityDetail(context.Background(), entityID)
}

func (service *AppService) ScanLibrary() (ScanSummary, error) {
	summary, err := service.library.Scan(context.Background())
	if err == nil {
		// The first indexed library is what makes seeding possible: only then
		// can BeamNG's enabled entries be matched to real mods.
		service.seedDefaultPlayProfile(context.Background())
	}
	return summary, err
}

func (service *AppService) CancelScan() bool { return service.library.Cancel() }

func (service *AppService) CreateWorkspace(entityID string) (WorkspaceDetail, error) {
	ctx := context.Background()
	item, err := service.store.GetLibraryItem(ctx, entityID)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	existing, err := service.store.GetLatestWorkspaceByEntity(ctx, item.EntityID)
	if err == nil {
		return service.GetWorkspace(existing.ID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WorkspaceDetail{}, err
	}
	if !item.Linked || item.ArchivePath == "" {
		return WorkspaceDetail{}, errors.New("the selected mod has no linked source archive")
	}
	workspaceID, err := modkit.NewID()
	if err != nil {
		return WorkspaceDetail{}, err
	}
	root := filepath.Join(service.config.WorkspaceDir, workspaceID)
	workspaceLock := service.agents.workspaceToolMutex(workspaceID)
	workspaceLock.Lock()
	manifest, err := modkit.CreateWorkspace(ctx, item.ArchivePath, root, workspaceID, item.EntityID, item.ArtifactID, item.Kind)
	if err != nil {
		workspaceLock.Unlock()
		return WorkspaceDetail{}, err
	}
	if _, err := service.store.SaveWorkspace(ctx, manifest, root, item.ArchivePath); err != nil {
		_ = os.RemoveAll(root)
		workspaceLock.Unlock()
		return WorkspaceDetail{}, err
	}
	workspaceLock.Unlock()
	return service.GetWorkspace(workspaceID)
}
func (service *AppService) ConfigureWorkspaceVirgil(workspaceID string, enabled bool) (WorkspaceDetail, error) {
	ctx := context.Background()
	if err := service.store.SetWorkspaceVirgil(ctx, workspaceID, enabled); err != nil {
		return WorkspaceDetail{}, err
	}
	return service.GetWorkspace(workspaceID)
}

func (service *AppService) ListWorkspaces() ([]WorkspaceRecord, error) {
	return service.store.ListWorkspaces(context.Background())
}

func (service *AppService) workspaceGitService() *GitService {
	if service.git == nil {
		service.git = NewGitService()
	}
	return service.git
}

func (service *AppService) workspaceGitRoot(workspaceID string) (string, error) {
	workspace, err := service.store.GetWorkspace(context.Background(), strings.TrimSpace(workspaceID))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(workspace.FilesRoot) == "" {
		return "", errors.New("workspace FilesRoot is empty")
	}
	return workspace.FilesRoot, nil
}

func (service *AppService) lockWorkspaceGitContext(ctx context.Context, workspaceID string) (string, func(), error) {
	root, err := service.workspaceGitRoot(workspaceID)
	if err != nil {
		return "", func() {}, err
	}
	if service.agents == nil {
		return root, func() {}, nil
	}
	lock := service.agents.workspaceToolMutex(strings.TrimSpace(workspaceID))
	if err := lockMutexContext(ctx, lock); err != nil {
		return "", func() {}, err
	}
	return root, lock.Unlock, nil
}

func (service *AppService) lockWorkspaceGit(workspaceID string) (string, func(), error) {
	return service.lockWorkspaceGitContext(context.Background(), workspaceID)
}

func (service *AppService) beginWorkspaceGitRemote(workspaceID string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	operation := &appGitRemoteOperation{cancel: cancel}
	key := strings.TrimSpace(workspaceID)
	service.gitRemoteMu.Lock()
	if service.gitRemoteOps == nil {
		service.gitRemoteOps = make(map[string][]*appGitRemoteOperation)
	}
	service.gitRemoteOps[key] = append(service.gitRemoteOps[key], operation)
	service.gitRemoteMu.Unlock()
	return ctx, func() {
		service.gitRemoteMu.Lock()
		operations := service.gitRemoteOps[key]
		for index, candidate := range operations {
			if candidate == operation {
				operations = append(operations[:index], operations[index+1:]...)
				break
			}
		}
		if len(operations) == 0 {
			delete(service.gitRemoteOps, key)
		} else {
			service.gitRemoteOps[key] = operations
		}
		service.gitRemoteMu.Unlock()
		cancel()
	}
}

func (service *AppService) cancelWorkspaceGitOperations(workspaceID string) bool {
	key := strings.TrimSpace(workspaceID)
	service.gitRemoteMu.Lock()
	operations := service.gitRemoteOps[key]
	delete(service.gitRemoteOps, key)
	service.gitRemoteMu.Unlock()
	for _, operation := range operations {
		operation.cancel()
	}
	return len(operations) > 0
}

func (service *AppService) cancelAllWorkspaceGitOperations() {
	service.gitRemoteMu.Lock()
	operations := make([]*appGitRemoteOperation, 0)
	for key, workspaceOperations := range service.gitRemoteOps {
		operations = append(operations, workspaceOperations...)
		delete(service.gitRemoteOps, key)
	}
	service.gitRemoteMu.Unlock()
	for _, operation := range operations {
		operation.cancel()
	}
}

func (service *AppService) withWorkspaceGitRemote(workspaceID string, operation func(context.Context, string) (GitOperationResult, error)) (GitOperationResult, error) {
	ctx, unregister := service.beginWorkspaceGitRemote(workspaceID)
	defer unregister()
	root, unlock, err := service.lockWorkspaceGitContext(ctx, workspaceID)
	if err != nil {
		return GitOperationResult{}, err
	}
	defer unlock()
	return operation(ctx, root)
}

func (service *AppService) GetWorkspaceGitStatus(workspaceID string) (GitStatus, error) {
	root, unlock, err := service.lockWorkspaceGit(workspaceID)
	if err != nil {
		return GitStatus{}, err
	}
	defer unlock()
	return service.workspaceGitService().Status(context.Background(), root)
}

func (service *AppService) GetWorkspaceGitDiff(workspaceID, relativePath, comparison string) (GitDiff, error) {
	root, unlock, err := service.lockWorkspaceGit(workspaceID)
	if err != nil {
		return GitDiff{}, err
	}
	defer unlock()
	return service.workspaceGitService().Diff(context.Background(), root, relativePath, comparison)
}

func (service *AppService) StageWorkspaceGitPaths(workspaceID string, paths []string, expectedFingerprint string) (GitStatus, error) {
	root, unlock, err := service.lockWorkspaceGit(workspaceID)
	if err != nil {
		return GitStatus{}, err
	}
	defer unlock()
	return service.workspaceGitService().Stage(context.Background(), root, paths, expectedFingerprint)
}

func (service *AppService) UnstageWorkspaceGitPaths(workspaceID string, paths []string, expectedFingerprint string) (GitStatus, error) {
	root, unlock, err := service.lockWorkspaceGit(workspaceID)
	if err != nil {
		return GitStatus{}, err
	}
	defer unlock()
	return service.workspaceGitService().Unstage(context.Background(), root, paths, expectedFingerprint)
}

func (service *AppService) DiscardWorkspaceGitPaths(workspaceID string, paths []string, expectedFingerprint string) (GitDiscardResult, error) {
	if strings.TrimSpace(expectedFingerprint) == "" {
		return GitDiscardResult{Completed: []string{}, Failed: []GitDiscardFailure{}, Error: "discard requires the confirmed Git status fingerprint"}, errors.New("discard requires the confirmed Git status fingerprint")
	}
	root, unlock, err := service.lockWorkspaceGit(workspaceID)
	if err != nil {
		return GitDiscardResult{}, err
	}
	defer unlock()
	return service.workspaceGitService().Discard(context.Background(), root, paths, expectedFingerprint)
}

func (service *AppService) CommitWorkspaceGit(workspaceID, message string, amend bool, expectedFingerprint string) (GitCommitResult, error) {
	root, unlock, err := service.lockWorkspaceGit(workspaceID)
	if err != nil {
		return GitCommitResult{}, err
	}
	defer unlock()
	return service.workspaceGitService().Commit(context.Background(), root, message, amend, expectedFingerprint)
}

func (service *AppService) ListWorkspaceGitBranches(workspaceID string) ([]GitBranch, error) {
	root, unlock, err := service.lockWorkspaceGit(workspaceID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return service.workspaceGitService().ListBranches(context.Background(), root)
}

func (service *AppService) CreateWorkspaceGitBranch(workspaceID, name, startPoint, expectedFingerprint string) (GitStatus, error) {
	root, unlock, err := service.lockWorkspaceGit(workspaceID)
	if err != nil {
		return GitStatus{}, err
	}
	defer unlock()
	return service.workspaceGitService().CreateBranch(context.Background(), root, name, startPoint, expectedFingerprint)
}

func (service *AppService) SwitchWorkspaceGitBranch(workspaceID, name, expectedFingerprint string) (GitStatus, error) {
	root, unlock, err := service.lockWorkspaceGit(workspaceID)
	if err != nil {
		return GitStatus{}, err
	}
	defer unlock()
	return service.workspaceGitService().SwitchBranch(context.Background(), root, name, expectedFingerprint)
}

func (service *AppService) FetchWorkspaceGit(workspaceID string) (GitOperationResult, error) {
	return service.withWorkspaceGitRemote(workspaceID, func(ctx context.Context, root string) (GitOperationResult, error) {
		return service.workspaceGitService().Fetch(ctx, root)
	})
}

func (service *AppService) PullWorkspaceGit(workspaceID, expectedFingerprint string) (GitOperationResult, error) {
	return service.withWorkspaceGitRemote(workspaceID, func(ctx context.Context, root string) (GitOperationResult, error) {
		return service.workspaceGitService().Pull(ctx, root, expectedFingerprint)
	})
}

func (service *AppService) PushWorkspaceGit(workspaceID string) (GitOperationResult, error) {
	return service.withWorkspaceGitRemote(workspaceID, func(ctx context.Context, root string) (GitOperationResult, error) {
		return service.workspaceGitService().Push(ctx, root)
	})
}

func (service *AppService) SyncWorkspaceGit(workspaceID, expectedFingerprint string) (GitOperationResult, error) {
	return service.withWorkspaceGitRemote(workspaceID, func(ctx context.Context, root string) (GitOperationResult, error) {
		return service.workspaceGitService().Sync(ctx, root, expectedFingerprint)
	})
}

func (service *AppService) CancelWorkspaceGitOperation(workspaceID string) bool {
	cancelled := service.cancelWorkspaceGitOperations(workspaceID)
	root, err := service.workspaceGitRoot(workspaceID)
	if err != nil {
		return cancelled
	}
	return service.workspaceGitService().CancelRemoteOperation(root) || cancelled
}

// GitHubPublishConfiguration reports non-secret OAuth App setup.
func (service *AppService) GitHubPublishConfiguration() GitHubPublishConfiguration {
	if service.githubPublish == nil {
		return GitHubPublishConfiguration{Provider: "github", Scope: githubPublishScope, VisibilityOptions: []string{"private", "public"}, Message: "GitHub publishing is unavailable.", Action: "Restart Mod Studio and try again."}
	}
	return service.githubPublish.Configuration()
}

// StartGitHubPublishAuth starts a fresh GitHub OAuth App device flow.
func (service *AppService) StartGitHubPublishAuth() (GitHubPublishAuthSession, error) {
	return service.githubPublish.StartDeviceAuth(context.Background())
}

// PollGitHubPublishAuth performs at most one protocol-compliant device poll.
func (service *AppService) PollGitHubPublishAuth(sessionID string) (GitHubPublishAuthSession, error) {
	return service.githubPublish.PollDeviceAuth(context.Background(), sessionID)
}

// CancelGitHubPublishAuth cancels the device flow and wipes transient secrets.
func (service *AppService) CancelGitHubPublishAuth(sessionID string) (GitHubPublishAuthSession, error) {
	return service.githubPublish.CancelAuth(sessionID)
}

// PreflightGitHubPublish validates local Git and reads the GitHub target. It
// performs no repository creation or local remote mutation.
func (service *AppService) PreflightGitHubPublish(workspaceID string, draft GitHubPublishDraft) (GitHubPublishPreflight, error) {
	root, err := service.workspaceGitRoot(workspaceID)
	if err != nil {
		return GitHubPublishPreflight{}, err
	}
	draft.WorkspaceID = strings.TrimSpace(workspaceID)
	return service.githubPublish.Preflight(context.Background(), root, draft)
}

// StartGitHubPublish starts the confirmed create/connect, remote, push, and
// verification operation asynchronously.
func (service *AppService) StartGitHubPublish(request GitHubPublishStartRequest) (GitHubPublishOperation, error) {
	return service.githubPublish.Start(context.Background(), request)
}

// GetGitHubPublishOperation polls an asynchronous operation/result.
func (service *AppService) GetGitHubPublishOperation(operationID string) (GitHubPublishOperation, error) {
	return service.githubPublish.GetOperation(operationID)
}

// CancelGitHubPublish cancels without waiting for the workspace mutation
// mutex, allowing a blocked or transferring operation to observe cancellation.
func (service *AppService) CancelGitHubPublish(operationID string) (GitHubPublishOperation, error) {
	return service.githubPublish.CancelOperation(operationID)
}

// ClearGitHubPublishSession wipes the in-memory authentication/session state.
func (service *AppService) ClearGitHubPublishSession(sessionID string) error {
	return service.githubPublish.ClearSession(sessionID)
}

func (service *AppService) GetWorkspace(workspaceID string) (WorkspaceDetail, error) {
	ctx := context.Background()
	workspace, err := service.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	entity, err := service.store.GetLibraryItem(ctx, workspace.EntityID)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	workspaceLock := service.agents.workspaceToolMutex(workspace.ID)
	workspaceLock.Lock()
	files, err := modkit.ListWorkspaceFileInfo(workspace.FilesRoot)
	if err == nil {
		filtered := files[:0]
		for _, file := range files {
			if !isWorkspaceGitMetadataPath(file.Path) {
				filtered = append(filtered, file)
			}
		}
		files = filtered
	}
	var directories []string
	if err == nil {
		directories, err = modkit.ListWorkspaceDirectories(workspace.FilesRoot)
		if err == nil {
			filtered := directories[:0]
			for _, directory := range directories {
				if !isWorkspaceGitMetadataPath(directory) {
					filtered = append(filtered, directory)
				}
			}
			directories = filtered
		}
	}
	workspaceLock.Unlock()
	if err != nil {
		return WorkspaceDetail{}, err
	}
	drafts, err := service.store.ListWorkspaceDrafts(ctx, workspaceID)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	validation := modkit.ValidationResult{Valid: false, Issues: []modkit.Issue{}}
	if workspace.LastValidation != "" {
		_ = json.Unmarshal([]byte(workspace.LastValidation), &validation)
	}
	exports, err := service.store.ListExports(ctx, workspaceID)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	sessions, err := service.store.ListVirgilSessions(ctx, workspaceID)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	knowledge, err := knowledgeFor(entity.Kind, entity.Manifest.ContentTags)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	var diskBytes int64
	for _, file := range files {
		diskBytes += file.SizeBytes
	}
	var activeTest *TestInstallRecord
	if record, testErr := service.store.GetActiveTestInstall(ctx, workspaceID); testErr == nil {
		activeTest = &record
	}
	return WorkspaceDetail{Workspace: workspace, Entity: entity, Files: files, Directories: directories, Drafts: drafts, Validation: validation, Exports: exports, VirgilSessions: sessions, Knowledge: knowledge, ActiveTest: activeTest, DiskBytes: diskBytes}, nil
}

func (service *AppService) ReadWorkspaceFile(workspaceID, relativePath string) (WorkspaceTextFile, error) {
	workspace, err := service.store.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return WorkspaceTextFile{}, err
	}
	relativePath, err = cleanWorkspaceRelativePath(relativePath)
	if err != nil {
		return WorkspaceTextFile{}, err
	}
	workspaceLock := service.agents.workspaceToolMutex(workspace.ID)
	workspaceLock.Lock()
	content, err := modkit.ReadWorkspaceText(workspace.FilesRoot, relativePath)
	workspaceLock.Unlock()
	if err != nil {
		return WorkspaceTextFile{}, err
	}
	sum := sha256.Sum256([]byte(content))
	return WorkspaceTextFile{Path: filepath.ToSlash(relativePath), Content: content, SHA256: hex.EncodeToString(sum[:])}, nil
}

func (service *AppService) WriteWorkspaceFile(workspaceID, relativePath, content, expectedSHA256 string) error {
	ctx := context.Background()
	workspace, err := service.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	relativePath, err = cleanWorkspaceRelativePath(relativePath)
	if err != nil {
		return err
	}
	workspaceLock := service.agents.workspaceToolMutex(workspace.ID)
	workspaceLock.Lock()
	defer workspaceLock.Unlock()
	if err := writeWorkspaceTextChecked(workspace.FilesRoot, relativePath, content, expectedSHA256); err != nil {
		return err
	}
	if err := service.store.DeleteWorkspaceDrafts(ctx, workspaceID, relativePath); err != nil {
		return err
	}
	return service.store.TouchWorkspace(ctx, workspaceID)
}

func (service *AppService) SearchWorkspace(workspaceID string, options WorkspaceSearchOptions, maxResults int) ([]WorkspaceSearchMatch, error) {
	workspace, err := service.store.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return nil, err
	}
	workspaceLock := service.agents.workspaceToolMutex(workspace.ID)
	workspaceLock.Lock()
	defer workspaceLock.Unlock()
	matches, err := modkit.SearchWorkspaceMatches(
		workspace.FilesRoot,
		options.Query,
		maxResults,
		options.Regex,
		options.CaseSensitive,
	)
	if err != nil {
		return nil, err
	}
	result := make([]WorkspaceSearchMatch, len(matches))
	for index, match := range matches {
		result[index] = WorkspaceSearchMatch{
			RelativePath: match.RelativePath,
			Line:         match.Line,
			Column:       match.Column,
			MatchLength:  match.MatchLength,
			Preview:      match.Preview,
		}
	}
	return result, nil
}

func (service *AppService) WorkspaceDiff(workspaceID string) ([]modkit.WorkspaceChange, error) {
	workspace, manifest, err := service.workspaceAndManifest(workspaceID)
	if err != nil {
		return nil, err
	}
	workspaceLock := service.agents.workspaceToolMutex(workspace.ID)
	workspaceLock.Lock()
	defer workspaceLock.Unlock()
	return modkit.DiffWorkspace(workspace.SourcePath, workspace.FilesRoot, manifest.Files)
}

func (service *AppService) ValidateWorkspace(workspaceID string) (modkit.ValidationResult, error) {
	ctx := context.Background()
	workspace, err := service.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return modkit.ValidationResult{}, err
	}
	workspaceLock := service.agents.workspaceToolMutex(workspace.ID)
	workspaceLock.Lock()
	defer workspaceLock.Unlock()
	result := modkit.ValidateWorkspace(workspace.FilesRoot)
	err = service.store.SetWorkspaceValidation(ctx, workspaceID, result)
	return result, err
}

func (service *AppService) SetWorkspaceJSONValue(workspaceID, relativePath, dottedPath string, value any) error {
	ctx := context.Background()
	workspace, err := service.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	relativePath, err = cleanWorkspaceRelativePath(relativePath)
	if err != nil {
		return err
	}
	workspaceLock := service.agents.workspaceToolMutex(workspace.ID)
	workspaceLock.Lock()
	defer workspaceLock.Unlock()
	if err := modkit.SetJSONValue(workspace.FilesRoot, relativePath, dottedPath, value); err != nil {
		return err
	}
	return service.store.TouchWorkspace(ctx, workspaceID)
}

func (service *AppService) CloneVehicleVariant(workspaceID, sourceConfigPath, newBaseName, displayName string) ([]string, error) {
	ctx := context.Background()
	workspace, err := service.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	workspaceLock := service.agents.workspaceToolMutex(workspace.ID)
	workspaceLock.Lock()
	defer workspaceLock.Unlock()
	created, err := modkit.CloneVehicleVariant(workspace.FilesRoot, sourceConfigPath, newBaseName, displayName)
	if err == nil {
		err = service.store.TouchWorkspace(ctx, workspaceID)
	}
	return created, err
}

func (service *AppService) ExportWorkspace(workspaceID, label string) (ExportResponse, error) {
	workspace, err := service.store.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return ExportResponse{}, err
	}
	workspaceLock := service.agents.workspaceToolMutex(workspace.ID)
	workspaceLock.Lock()
	defer workspaceLock.Unlock()
	return service.exportWorkspace(context.Background(), workspaceID, label, "manual")
}

func (service *AppService) InstallExportForTest(workspaceID, exportID string) (TestInstallRecord, error) {
	return service.installExportForTest(context.Background(), workspaceID, exportID)
}

func (service *AppService) UninstallTest(workspaceID string) error {
	return service.uninstallTest(context.Background(), workspaceID)
}

func (service *AppService) LaunchBeamNG(workspaceID string) (ProcessLaunch, error) {
	return service.launchBeamNG(context.Background(), workspaceID)
}

func (service *AppService) AnalyzeRuntime(workspaceID string) (RuntimeReport, error) {
	return service.analyzeRuntime(context.Background(), workspaceID)
}

func (service *AppService) virgilLaunchSettings(modelOverride string) (agentLaunchSettings, error) {
	settings, err := service.agentLaunchSettings(context.Background())
	if err != nil {
		return agentLaunchSettings{}, err
	}
	modelOverride = strings.TrimSpace(modelOverride)
	if len(modelOverride) > 120 {
		return agentLaunchSettings{}, errors.New("model identifier exceeds 120 characters")
	}
	if modelOverride != "" {
		settings.Model = modelOverride
	}
	return settings, nil
}

func (service *AppService) StartVirgilSession(workspaceID, prompt, modelOverride, userTitle string) (VirgilSessionRecord, error) {
	settings, err := service.virgilLaunchSettings(modelOverride)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	settings.SelectModel = true
	return service.agents.StartSession(context.Background(), workspaceID, prompt, userTitle, settings)
}

func (service *AppService) SendVirgilMessage(sessionID, prompt, modelOverride string) (AgentRunRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return AgentRunRecord{}, errors.New("Virgil session ID is required")
	}
	var (
		settings agentLaunchSettings
		err      error
	)
	if strings.TrimSpace(modelOverride) == "" {
		session, sessionErr := service.store.GetVirgilSession(context.Background(), sessionID)
		if sessionErr != nil {
			return AgentRunRecord{}, sessionErr
		}
		settings, err = service.agentLaunchSettingsForProfile(context.Background(), session.Profile)
		if err != nil {
			return AgentRunRecord{}, err
		}
	} else {
		settings, err = service.virgilLaunchSettings(modelOverride)
		if err != nil {
			return AgentRunRecord{}, err
		}
	}
	settings.SelectModel = strings.TrimSpace(modelOverride) != ""
	return service.agents.SendMessage(context.Background(), sessionID, prompt, settings)
}
func (service *AppService) ResumeVirgilSession(sessionID string) (VirgilSessionRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return VirgilSessionRecord{}, errors.New("Virgil session ID is required")
	}
	session, err := service.store.GetVirgilSession(context.Background(), sessionID)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	settings, err := service.agentLaunchSettingsForProfile(context.Background(), session.Profile)
	if err != nil {
		return VirgilSessionRecord{}, err
	}
	return service.agents.ResumeSession(context.Background(), sessionID, settings)
}

func (service *AppService) RenameVirgilSession(sessionID, title string) (VirgilSessionRecord, error) {
	return service.agents.RenameSession(context.Background(), sessionID, title)
}

func (service *AppService) ForgetVirgilSession(sessionID string) error {
	return service.agents.ForgetSession(context.Background(), sessionID)
}

func (service *AppService) StopAgent(runID string) bool {
	return service.agents.Stop(strings.TrimSpace(runID))
}

func (service *AppService) ListAgentEvents(runID string) ([]AgentEventRecord, error) {
	return service.store.ListAgentEvents(context.Background(), runID, 1000)
}

func (service *AppService) GetKnowledge(entityID string) ([]KnowledgeDocument, error) {
	item, err := service.store.GetLibraryItem(context.Background(), entityID)
	if err != nil {
		return nil, err
	}
	return knowledgeFor(item.Kind, item.Manifest.ContentTags)
}

func (service *AppService) workspaceAndManifest(workspaceID string) (WorkspaceRecord, modkit.WorkspaceManifest, error) {
	workspace, err := service.store.GetWorkspace(context.Background(), strings.TrimSpace(workspaceID))
	if err != nil {
		return WorkspaceRecord{}, modkit.WorkspaceManifest{}, err
	}
	manifest, err := modkit.ReadWorkspaceManifest(workspace.Root)
	if err != nil {
		return WorkspaceRecord{}, modkit.WorkspaceManifest{}, fmt.Errorf("read workspace baseline: %w", err)
	}
	if manifest.ID != workspace.ID || manifest.EntityID != workspace.EntityID || manifest.ArtifactID != workspace.ArtifactID {
		return WorkspaceRecord{}, modkit.WorkspaceManifest{}, errors.New("workspace baseline identity does not match persisted state")
	}
	return workspace, manifest, nil
}
func (service *AppService) shutdown() {
	service.library.Cancel()
	service.cancelAllWorkspaceGitOperations()
	if service.git != nil {
		service.git.CancelAllRemoteOperations()
	}
	if service.githubPublish != nil {
		service.githubPublish.Shutdown()
	}
	service.cancelAllAIConnections()
	service.agents.StopAll()
}

// ServiceShutdown is invoked by Wails on every Run exit path, including a
// native last-window close that does not dispatch application shutdown hooks.
func (service *AppService) ServiceShutdown() error {
	service.shutdown()
	return service.store.Close()
}
