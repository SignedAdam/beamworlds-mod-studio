package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DeleteWorkspace removes a ModMaker project: its database rows and its
// files on disk. It never touches the library archive the project was
// created from.
func (service *AppService) DeleteWorkspace(workspaceID string) error {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return errors.New("workspace ID is required")
	}
	ctx := context.Background()
	workspace, err := service.store.GetWorkspace(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("workspace %q not found", workspaceID)
		}
		return fmt.Errorf("workspace %q: %w", workspaceID, err)
	}

	// The workspace root must live under the configured workspaces directory.
	// Refusing to delete anything else prevents a corrupted row from wiping
	// an unrelated directory.
	if err := validateWorkspacePath(service.config.WorkspaceDir, workspace.Root); err != nil {
		return err
	}

	// Cancel any in-flight git operations so they do not write into a
	// directory that is about to be removed.
	service.cancelWorkspaceGitOperations(workspaceID)

	// Delete from the database. Foreign-key cascades (PRAGMA foreign_keys=1)
	// handle workspace_drafts, exports, test_installs, virgil_sessions, and
	// agent_runs.
	if err := service.store.DeleteWorkspaceRecord(ctx, workspaceID); err != nil {
		return err
	}

	// Remove workspace files from disk. The directory goes to the Recycle
	// Bin on Windows so the user can recover it; on other platforms it is
	// permanently removed.
	if workspace.Root != "" {
		if err := recycleWorkspaceRoot(workspace.Root); err != nil {
			// Fall back to a hard delete when the Recycle Bin refuses (e.g.
			// network path, full bin). Already-gone is fine either way.
			if rmErr := os.RemoveAll(workspace.Root); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				return fmt.Errorf("workspace files at %s: %w", workspace.Root, rmErr)
			}
		}
	}

	return nil
}

// validateWorkspacePath ensures target is a strict child of workspacesRoot.
func validateWorkspacePath(workspacesRoot, target string) error {
	if workspacesRoot == "" {
		return errors.New("workspace directory is not configured")
	}
	if target == "" {
		return errors.New("workspace has no root path")
	}
	absRoot, err := filepath.Abs(workspacesRoot)
	if err != nil {
		return err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("refusing to delete %s: not inside the workspaces directory %s", absTarget, absRoot)
	}
	return nil
}

// DeleteWorkspaceRecord removes the workspace row. Foreign-key cascades
// clean up child tables.
func (s *Store) DeleteWorkspaceRecord(ctx context.Context, workspaceID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM workspaces WHERE id=?`, workspaceID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("workspace %q not found", workspaceID)
	}
	return nil
}
