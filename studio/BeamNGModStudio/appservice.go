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

type AppService struct {
	config       AppConfig
	store        *Store
	library      *LibraryEngine
	agents       *AgentManager
	aiRuntime    *managedAIRuntime
	emit         func(string, any)
	profileMu    sync.Mutex
	auditMu      sync.Mutex
	virusMu      sync.Mutex
	auditAI      auditAIRunner
	aiLoginMu    sync.Mutex
	aiLogins     map[string]*aiLoginProcess
	aiConnecting map[string]bool
	aiAuthCtx    context.Context
	aiAuthCancel context.CancelFunc
	startProcess func(string, []string, string) (ProcessLaunch, error)
	gameRunning  func() (bool, error)
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
	}
	service.auditAI = service.runManagedAIAudit
	return service
}

func (service *AppService) Config() AppConfig { return service.config }

func (service *AppService) Dashboard() (Dashboard, error) {
	return service.store.Dashboard(context.Background(), service.config.DatabasePath)
}

func (service *AppService) ListLibrary(health, kind, query, folderID string) ([]LibraryItem, error) {
	return service.store.ListLibrary(context.Background(), health, kind, query, folderID)
}

func (service *AppService) GetEntity(entityID string) (EntityDetail, error) {
	return service.store.GetEntityDetail(context.Background(), entityID)
}

func (service *AppService) ScanLibrary() (ScanSummary, error) {
	return service.library.Scan(context.Background())
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
	service.cancelAllAIConnections()
	service.agents.StopAll()
}

// ServiceShutdown is invoked by Wails on every Run exit path, including a
// native last-window close that does not dispatch application shutdown hooks.
func (service *AppService) ServiceShutdown() error {
	service.shutdown()
	return service.store.Close()
}
