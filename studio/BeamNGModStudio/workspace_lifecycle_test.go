package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteWorkspaceRemovesRowsAndFiles(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	mod, err := service.CreateNewMod(NewModRequest{
		Name: "Lifecycle Test", ModID: "lifecycle_test", Kind: "script", Version: "0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	wsID := mod.Workspace.ID
	wsRoot := mod.Workspace.Root

	if _, err := os.Stat(wsRoot); err != nil {
		t.Fatalf("workspace directory should exist before deletion: %v", err)
	}

	if err := service.DeleteWorkspace(wsID); err != nil {
		t.Fatal(err)
	}

	// Database row must be gone.
	if _, err := service.store.GetWorkspace(context.Background(), wsID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("workspace row should be gone, got: %v", err)
	}

	// Workspace files must be gone.
	if _, err := os.Stat(wsRoot); !os.IsNotExist(err) {
		t.Fatalf("workspace directory should be removed, stat: %v", err)
	}

	// The library entity must survive — DeleteWorkspace never touches it.
	if _, err := service.store.GetLibraryItem(context.Background(), mod.Entity.EntityID); err != nil {
		t.Fatal("deleting a workspace must not remove the library index entry")
	}
}

func TestDeleteWorkspacePreservesLibraryArchive(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	mod, err := service.CreateNewMod(NewModRequest{
		Name: "Keep Archive", ModID: "keep_archive", Kind: "script", Version: "0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}

	archivePath := mod.Workspace.SourcePath
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("source archive should exist before workspace deletion: %v", err)
	}

	if err := service.DeleteWorkspace(mod.Workspace.ID); err != nil {
		t.Fatal(err)
	}

	// The archive must survive — DeleteWorkspace never touches the library.
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatal("deleting a workspace must never remove the library archive")
	}

	// Entity must still be indexed.
	if _, err := service.store.GetLibraryItem(context.Background(), mod.Entity.EntityID); err != nil {
		t.Fatal("deleting a workspace must not remove the library index entry")
	}
}

func TestDeleteWorkspaceSafeWhenDirectoryAlreadyGone(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	mod, err := service.CreateNewMod(NewModRequest{
		Name: "Gone Dir", ModID: "gone_dir", Kind: "script", Version: "0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Pre-remove the workspace directory.
	if err := os.RemoveAll(mod.Workspace.Root); err != nil {
		t.Fatal(err)
	}

	// DeleteWorkspace must succeed even though the directory is already gone.
	if err := service.DeleteWorkspace(mod.Workspace.ID); err != nil {
		t.Fatalf("should succeed when workspace directory is already gone: %v", err)
	}
}

func TestValidateWorkspacePathRefusesTraversal(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "workspaces")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		target     string
		shouldFail bool
	}{
		{"child", filepath.Join(root, "abc123"), false},
		{"nested child", filepath.Join(root, "abc123", "subdir"), false},
		{"same as root", root, true},
		{"parent", filepath.Dir(root), true},
		{"sibling", filepath.Join(filepath.Dir(root), "library"), true},
		{"traversal", filepath.Join(root, "..", "library"), true},
		{"empty target", "", true},
	}
	for _, tc := range cases {
		err := validateWorkspacePath(root, tc.target)
		if tc.shouldFail && err == nil {
			t.Errorf("%s: expected an error for target %q", tc.name, tc.target)
		}
		if !tc.shouldFail && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
	}

	// Empty root must also fail.
	if err := validateWorkspacePath("", filepath.Join(root, "abc")); err == nil {
		t.Error("empty workspaces root should be rejected")
	}
}
