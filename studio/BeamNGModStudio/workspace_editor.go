package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	modkit "github.com/SignedAdam/beamworlds-modkit"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const maxWorkspaceDraftBytes = 4 << 20

type WorkspaceDraft struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	BaseSHA256 string `json:"baseSha256"`
	UpdatedAt  string `json:"updatedAt"`
}

func (service *AppService) CreateWorkspaceDirectory(workspaceID, relativePath string) error {
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
	if err := modkit.CreateWorkspaceDirectory(workspace.FilesRoot, relativePath); err != nil {
		return err
	}
	return service.store.TouchWorkspace(ctx, workspaceID)
}

func (service *AppService) RenameWorkspacePath(workspaceID, oldPath, newPath string) error {
	ctx := context.Background()
	workspace, err := service.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	oldPath, err = cleanWorkspaceRelativePath(oldPath)
	if err != nil {
		return err
	}
	newPath, err = cleanWorkspaceRelativePath(newPath)
	if err != nil {
		return err
	}
	if oldPath == newPath {
		return nil
	}
	workspaceLock := service.agents.workspaceToolMutex(workspace.ID)
	workspaceLock.Lock()
	defer workspaceLock.Unlock()
	if err := modkit.RenameWorkspacePath(workspace.FilesRoot, oldPath, newPath); err != nil {
		return err
	}
	if err := service.store.MoveWorkspaceDrafts(ctx, workspaceID, oldPath, newPath); err != nil {
		_ = modkit.RenameWorkspacePath(workspace.FilesRoot, newPath, oldPath)
		return err
	}
	return service.store.TouchWorkspace(ctx, workspaceID)
}

func (service *AppService) DeleteWorkspacePath(workspaceID, relativePath string) error {
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
	if err := modkit.DeleteWorkspacePath(workspace.FilesRoot, relativePath); err != nil {
		return err
	}
	if err := service.store.DeleteWorkspaceDrafts(ctx, workspaceID, relativePath); err != nil {
		return err
	}
	return service.store.TouchWorkspace(ctx, workspaceID)
}

func (service *AppService) RevealWorkspacePath(workspaceID, relativePath string) error {
	workspace, err := service.store.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return err
	}
	relativePath, err = cleanWorkspaceRelativePath(relativePath)
	if err != nil {
		return err
	}
	target := filepath.Join(workspace.FilesRoot, filepath.FromSlash(relativePath))
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		if info.IsDir() {
			command = exec.Command("explorer.exe", target)
		} else {
			command = exec.Command("explorer.exe", "/select,"+target)
		}
	case "darwin":
		if info.IsDir() {
			command = exec.Command("open", target)
		} else {
			command = exec.Command("open", "-R", target)
		}
	default:
		if !info.IsDir() {
			target = filepath.Dir(target)
		}
		command = exec.Command("xdg-open", target)
	}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func (service *AppService) SaveWorkspaceDraft(workspaceID, relativePath, content, baseSHA256 string) error {
	if len(content) > maxWorkspaceDraftBytes {
		return fmt.Errorf("draft exceeds %d-byte editor limit", maxWorkspaceDraftBytes)
	}
	relativePath, err := cleanWorkspaceRelativePath(relativePath)
	if err != nil {
		return err
	}
	if _, err := service.store.GetWorkspace(context.Background(), workspaceID); err != nil {
		return err
	}
	return service.store.SaveWorkspaceDraft(context.Background(), workspaceID, relativePath, content, baseSHA256)
}

func (service *AppService) DeleteWorkspaceDraft(workspaceID, relativePath string) error {
	relativePath, err := cleanWorkspaceRelativePath(relativePath)
	if err != nil {
		return err
	}
	return service.store.DeleteWorkspaceDrafts(context.Background(), workspaceID, relativePath)
}

func cleanWorkspaceRelativePath(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, ":") {
		return "", fmt.Errorf("unsafe workspace path %q", value)
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("workspace path escapes its root: %q", value)
	}
	return cleaned, nil
}

func workspaceCurrentSHA(filesRoot, relativePath string) (string, bool, error) {
	return workspaceCurrentSHAContext(context.Background(), filesRoot, relativePath)
}

func workspaceCurrentSHAContext(ctx context.Context, filesRoot, relativePath string) (string, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	relativePath, err := cleanWorkspaceRelativePath(relativePath)
	if err != nil {
		return "", false, err
	}
	filename := filepath.Join(filesRoot, filepath.FromSlash(relativePath))
	info, err := os.Stat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", true, err
	}
	if info.IsDir() {
		return "", true, fmt.Errorf("workspace path %q is a directory", relativePath)
	}
	content, err := modkit.ReadWorkspaceTextContext(ctx, filesRoot, relativePath)
	if err != nil {
		return "", true, err
	}
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", sum[:]), true, nil
}

func workspaceRevisionConflict(relativePath, expectedSHA256, currentSHA256 string, exists bool) error {
	if strings.TrimSpace(expectedSHA256) == "" {
		return fmt.Errorf("workspace conflict for %q: expected the path to be absent, but it already exists", relativePath)
	}
	if !exists {
		currentSHA256 = "<missing>"
	}
	if currentSHA256 == "" {
		currentSHA256 = "<unavailable>"
	}
	return fmt.Errorf("workspace conflict for %q: expected SHA-256 %q, found %q", relativePath, strings.TrimSpace(expectedSHA256), currentSHA256)
}

func checkWorkspaceRevision(filesRoot, relativePath, expectedSHA256 string) error {
	return checkWorkspaceRevisionContext(context.Background(), filesRoot, relativePath, expectedSHA256)
}

func checkWorkspaceRevisionContext(ctx context.Context, filesRoot, relativePath, expectedSHA256 string) error {
	expectedSHA256 = strings.TrimSpace(expectedSHA256)
	currentSHA256, exists, err := workspaceCurrentSHAContext(ctx, filesRoot, relativePath)
	if err != nil {
		if expectedSHA256 == "" && exists {
			return workspaceRevisionConflict(relativePath, expectedSHA256, "", true)
		}
		return fmt.Errorf("workspace conflict: cannot verify current SHA-256: %w", err)
	}
	if expectedSHA256 == "" {
		if exists {
			return workspaceRevisionConflict(relativePath, expectedSHA256, currentSHA256, true)
		}
		return nil
	}
	if !exists || !strings.EqualFold(currentSHA256, expectedSHA256) {
		return workspaceRevisionConflict(relativePath, expectedSHA256, currentSHA256, exists)
	}
	return nil
}

func writeWorkspaceTextChecked(filesRoot, relativePath, content, expectedSHA256 string) error {
	return writeWorkspaceTextCheckedContext(context.Background(), filesRoot, relativePath, content, expectedSHA256)
}

func writeWorkspaceTextCheckedContext(ctx context.Context, filesRoot, relativePath, content, expectedSHA256 string) error {
	if err := checkWorkspaceRevisionContext(ctx, filesRoot, relativePath, expectedSHA256); err != nil {
		return err
	}
	relativePath, err := cleanWorkspaceRelativePath(relativePath)
	if err != nil {
		return err
	}
	return modkit.WriteWorkspaceTextContext(ctx, filesRoot, relativePath, content)
}

func replaceWorkspaceTextChecked(filesRoot, relativePath, oldText, newText, expectedSHA256 string, all bool) (int, error) {
	return replaceWorkspaceTextCheckedContext(context.Background(), filesRoot, relativePath, oldText, newText, expectedSHA256, all)
}

func replaceWorkspaceTextCheckedContext(ctx context.Context, filesRoot, relativePath, oldText, newText, expectedSHA256 string, all bool) (int, error) {
	if strings.TrimSpace(expectedSHA256) == "" {
		return 0, errors.New("expectedSha256 is required; read the file before replacing")
	}
	relativePath, err := cleanWorkspaceRelativePath(relativePath)
	if err != nil {
		return 0, err
	}
	if err := checkWorkspaceRevisionContext(ctx, filesRoot, relativePath, expectedSHA256); err != nil {
		return 0, err
	}
	return modkit.ReplaceWorkspaceTextContext(ctx, filesRoot, relativePath, oldText, newText, all)
}

func (s *Store) ListWorkspaceDrafts(ctx context.Context, workspaceID string) ([]WorkspaceDraft, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path,content,base_sha256,updated_at FROM workspace_drafts WHERE workspace_id=? ORDER BY updated_at,path`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []WorkspaceDraft{}
	for rows.Next() {
		var draft WorkspaceDraft
		if err := rows.Scan(&draft.Path, &draft.Content, &draft.BaseSHA256, &draft.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, draft)
	}
	return result, rows.Err()
}

func (s *Store) SaveWorkspaceDraft(ctx context.Context, workspaceID, relativePath, content, baseSHA256 string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspace_drafts(workspace_id,path,content,base_sha256,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(workspace_id,path) DO UPDATE SET content=excluded.content,base_sha256=excluded.base_sha256,updated_at=excluded.updated_at`, workspaceID, relativePath, content, strings.TrimSpace(baseSHA256), nowUTC())
	return err
}

func (s *Store) DeleteWorkspaceDrafts(ctx context.Context, workspaceID, relativePrefix string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM workspace_drafts WHERE workspace_id=? AND (path=? OR path LIKE ? ESCAPE '\')`, workspaceID, relativePrefix, escapeLike(relativePrefix)+"/%")
	return err
}

func (s *Store) MoveWorkspaceDrafts(ctx context.Context, workspaceID, oldPrefix, newPrefix string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT path,content,base_sha256,updated_at FROM workspace_drafts WHERE workspace_id=? AND (path=? OR path LIKE ? ESCAPE '\') ORDER BY path`, workspaceID, oldPrefix, escapeLike(oldPrefix)+"/%")
	if err != nil {
		return err
	}
	drafts := []WorkspaceDraft{}
	for rows.Next() {
		var draft WorkspaceDraft
		if err := rows.Scan(&draft.Path, &draft.Content, &draft.BaseSHA256, &draft.UpdatedAt); err != nil {
			rows.Close()
			return err
		}
		drafts = append(drafts, draft)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_drafts WHERE workspace_id=? AND (path=? OR path LIKE ? ESCAPE '\')`, workspaceID, oldPrefix, escapeLike(oldPrefix)+"/%"); err != nil {
		return err
	}
	for _, draft := range drafts {
		suffix := strings.TrimPrefix(draft.Path, oldPrefix)
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_drafts(workspace_id,path,content,base_sha256,updated_at) VALUES(?,?,?,?,?)`, workspaceID, newPrefix+suffix, draft.Content, draft.BaseSHA256, draft.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}
