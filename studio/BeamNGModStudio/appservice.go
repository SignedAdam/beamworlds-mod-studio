package main

import (
	"context"
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
	emit         func(string, any)
	profileMu    sync.Mutex
	auditMu      sync.Mutex
	auditAI      auditAIRunner
	startProcess func(string, []string, string) (ProcessLaunch, error)
	gameRunning  func() (bool, error)
}

type WorkspaceDetail struct {
	Workspace   WorkspaceRecord         `json:"workspace"`
	Entity      LibraryItem             `json:"entity"`
	Files       []modkit.FileSnapshot   `json:"files"`
	Directories []string                `json:"directories"`
	Drafts      []WorkspaceDraft        `json:"drafts"`
	Validation  modkit.ValidationResult `json:"validation"`
	Exports     []ExportRecord          `json:"exports"`
	AgentRuns   []AgentRunRecord        `json:"agentRuns"`
	Knowledge   []KnowledgeDocument     `json:"knowledge"`
	ActiveTest  *TestInstallRecord      `json:"activeTest,omitempty"`
	DiskBytes   int64                   `json:"diskBytes"`
}

type WorkspaceTextFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type ExportResponse struct {
	Record ExportRecord        `json:"record"`
	Result modkit.ExportResult `json:"result"`
}

func NewAppService(config AppConfig, store *Store, emit func(string, any)) *AppService {
	service := &AppService{
		config:       config,
		store:        store,
		library:      NewLibraryEngine(store, config, emit),
		agents:       NewAgentManager(store, config, emit),
		emit:         emit,
		startProcess: startDetachedProcess,
		gameRunning:  beamNGProcessRunning,
	}
	service.auditAI = service.runOMPAudit
	return service
}

func (service *AppService) Config() AppConfig { return service.config }

func (service *AppService) Dashboard() (Dashboard, error) {
	return service.store.Dashboard(context.Background(), service.config.DatabasePath)
}

func (service *AppService) ListLibrary(status, kind, query, folderID string) ([]LibraryItem, error) {
	return service.store.ListLibrary(context.Background(), status, kind, query, folderID)
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
	if !item.Linked || item.ArchivePath == "" {
		return WorkspaceDetail{}, errors.New("the selected mod has no linked source archive")
	}
	if !item.Manifest.ValidArchive {
		return WorkspaceDetail{}, errors.New("cannot create a workspace from an invalid archive")
	}
	workspaceID, err := modkit.NewID()
	if err != nil {
		return WorkspaceDetail{}, err
	}
	root := filepath.Join(service.config.WorkspaceDir, workspaceID)
	manifest, err := modkit.CreateWorkspace(ctx, item.ArchivePath, root, workspaceID, item.EntityID, item.ArtifactID, item.Kind)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	if _, err := service.store.SaveWorkspace(ctx, manifest, root, item.ArchivePath); err != nil {
		_ = os.RemoveAll(root)
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
	files, err := modkit.ListWorkspaceFileInfo(workspace.FilesRoot)
	if err != nil {
		return WorkspaceDetail{}, err
	}
	directories, err := modkit.ListWorkspaceDirectories(workspace.FilesRoot)
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
	runs, err := service.store.ListAgentRuns(ctx, workspaceID)
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
	return WorkspaceDetail{Workspace: workspace, Entity: entity, Files: files, Directories: directories, Drafts: drafts, Validation: validation, Exports: exports, AgentRuns: runs, Knowledge: knowledge, ActiveTest: activeTest, DiskBytes: diskBytes}, nil
}

func (service *AppService) ReadWorkspaceFile(workspaceID, relativePath string) (WorkspaceTextFile, error) {
	workspace, err := service.store.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return WorkspaceTextFile{}, err
	}
	content, err := modkit.ReadWorkspaceText(workspace.FilesRoot, relativePath)
	return WorkspaceTextFile{Path: filepath.ToSlash(relativePath), Content: content}, err
}

func (service *AppService) WriteWorkspaceFile(workspaceID, relativePath, content string) error {
	ctx := context.Background()
	workspace, err := service.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	relativePath, err = cleanWorkspaceRelativePath(relativePath)
	if err != nil {
		return err
	}
	if err := modkit.WriteWorkspaceText(workspace.FilesRoot, relativePath, content); err != nil {
		return err
	}
	if err := service.store.DeleteWorkspaceDrafts(ctx, workspaceID, relativePath); err != nil {
		return err
	}
	return service.store.TouchWorkspace(ctx, workspaceID)
}

func (service *AppService) SearchWorkspace(workspaceID, query string, maxResults int) ([]string, error) {
	workspace, err := service.store.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return nil, err
	}
	return modkit.SearchWorkspace(workspace.FilesRoot, query, maxResults)
}

func (service *AppService) WorkspaceDiff(workspaceID string) ([]modkit.WorkspaceChange, error) {
	workspace, manifest, err := service.workspaceAndManifest(workspaceID)
	if err != nil {
		return nil, err
	}
	return modkit.DiffWorkspace(workspace.SourcePath, workspace.FilesRoot, manifest.Files)
}

func (service *AppService) ValidateWorkspace(workspaceID string) (modkit.ValidationResult, error) {
	workspace, err := service.store.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return modkit.ValidationResult{}, err
	}
	result := modkit.ValidateWorkspace(workspace.FilesRoot)
	err = service.store.SetWorkspaceValidation(context.Background(), workspaceID, result)
	return result, err
}

func (service *AppService) SetWorkspaceJSONValue(workspaceID, relativePath, dottedPath string, value any) error {
	workspace, err := service.store.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return err
	}
	if err := modkit.SetJSONValue(workspace.FilesRoot, relativePath, dottedPath, value); err != nil {
		return err
	}
	return service.store.TouchWorkspace(context.Background(), workspaceID)
}

func (service *AppService) CloneVehicleVariant(workspaceID, sourceConfigPath, newBaseName, displayName string) ([]string, error) {
	workspace, err := service.store.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return nil, err
	}
	created, err := modkit.CloneVehicleVariant(workspace.FilesRoot, sourceConfigPath, newBaseName, displayName)
	if err == nil {
		err = service.store.TouchWorkspace(context.Background(), workspaceID)
	}
	return created, err
}

func (service *AppService) ExportWorkspace(workspaceID, label string) (ExportResponse, error) {
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

func (service *AppService) StartAgent(workspaceID, prompt, modelOverride string) (AgentRunRecord, error) {
	ctx := context.Background()
	settings, err := service.agentLaunchSettings(ctx)
	if err != nil {
		return AgentRunRecord{}, err
	}
	modelOverride = strings.TrimSpace(modelOverride)
	if len(modelOverride) > 120 {
		return AgentRunRecord{}, errors.New("model identifier exceeds 120 characters")
	}
	if modelOverride != "" {
		settings.Model = modelOverride
	}
	return service.agents.Start(ctx, workspaceID, prompt, settings)
}

func (service *AppService) StopAgent(runID string) bool { return service.agents.Stop(runID) }

func (service *AppService) ListAgentRuns(workspaceID string) ([]AgentRunRecord, error) {
	return service.store.ListAgentRuns(context.Background(), workspaceID)
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
	service.agents.StopAll()
}
