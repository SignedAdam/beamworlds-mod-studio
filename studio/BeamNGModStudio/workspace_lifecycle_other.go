//go:build !windows

package main

import "os"

// recycleWorkspaceRoot permanently removes a workspace directory on platforms
// without a Recycle Bin API. The Windows build (workspace_lifecycle_windows.go)
// sends it to the Recycle Bin instead.
func recycleWorkspaceRoot(path string) error {
	return os.RemoveAll(path)
}
