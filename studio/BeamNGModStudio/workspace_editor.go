package main

import (
	"context"
	"fmt"
	"path"
	"strings"

	modkit "github.com/SignedAdam/beamworlds-modkit"
)

const maxWorkspaceDraftBytes = 4 << 20

type WorkspaceDraft struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	UpdatedAt string `json:"updatedAt"`
}

func (service *AppService) CreateWorkspaceDirectory(workspaceID, relativePath string) error {
	workspace, err := service.store.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return err
	}
	relativePath, err = cleanWorkspaceRelativePath(relativePath)
	if err != nil {
		return err
	}
	if err := modkit.CreateWorkspaceDirectory(workspace.FilesRoot, relativePath); err != nil {
		return err
	}
	return service.store.TouchWorkspace(context.Background(), workspaceID)
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
	if err := modkit.DeleteWorkspacePath(workspace.FilesRoot, relativePath); err != nil {
		return err
	}
	if err := service.store.DeleteWorkspaceDrafts(ctx, workspaceID, relativePath); err != nil {
		return err
	}
	return service.store.TouchWorkspace(ctx, workspaceID)
}

func (service *AppService) SaveWorkspaceDraft(workspaceID, relativePath, content string) error {
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
	return service.store.SaveWorkspaceDraft(context.Background(), workspaceID, relativePath, content)
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

func (s *Store) ListWorkspaceDrafts(ctx context.Context, workspaceID string) ([]WorkspaceDraft, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path,content,updated_at FROM workspace_drafts WHERE workspace_id=? ORDER BY updated_at,path`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []WorkspaceDraft{}
	for rows.Next() {
		var draft WorkspaceDraft
		if err := rows.Scan(&draft.Path, &draft.Content, &draft.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, draft)
	}
	return result, rows.Err()
}

func (s *Store) SaveWorkspaceDraft(ctx context.Context, workspaceID, relativePath, content string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspace_drafts(workspace_id,path,content,updated_at) VALUES(?,?,?,?) ON CONFLICT(workspace_id,path) DO UPDATE SET content=excluded.content,updated_at=excluded.updated_at`, workspaceID, relativePath, content, nowUTC())
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
	rows, err := tx.QueryContext(ctx, `SELECT path,content,updated_at FROM workspace_drafts WHERE workspace_id=? AND (path=? OR path LIKE ? ESCAPE '\') ORDER BY path`, workspaceID, oldPrefix, escapeLike(oldPrefix)+"/%")
	if err != nil {
		return err
	}
	drafts := []WorkspaceDraft{}
	for rows.Next() {
		var draft WorkspaceDraft
		if err := rows.Scan(&draft.Path, &draft.Content, &draft.UpdatedAt); err != nil {
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_drafts(workspace_id,path,content,updated_at) VALUES(?,?,?,?)`, workspaceID, newPrefix+suffix, draft.Content, draft.UpdatedAt); err != nil {
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
