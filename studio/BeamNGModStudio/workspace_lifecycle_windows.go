//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// recycleWorkspaceRoot sends a workspace directory to the Recycle Bin so
// the user can recover it from Windows if needed. Uses the same
// SHFileOperationW API as recycleFile (recycle_windows.go) but accepts a
// directory instead of a regular file.
func recycleWorkspaceRoot(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // already gone
		}
		return err
	}
	if !info.IsDir() {
		return os.RemoveAll(absolute) // unexpected non-directory, just remove
	}
	encoded, err := windows.UTF16FromString(absolute)
	if err != nil {
		return err
	}
	// SHFileOperationW takes a double-null-terminated list.
	encoded = append(encoded, 0)
	operation := shellFileOperation{
		function: shellFileOperationDelete,
		from:     &encoded[0],
		flags:    shellAllowUndo | shellNoConfirmation | shellSilent | shellNoErrorUI | shellNoConfirmMkDir,
	}
	status, _, callErr := shellFileOperationW.Call(uintptr(unsafe.Pointer(&operation)))
	if status != 0 {
		_ = callErr
		return errors.New("the Recycle Bin refused the workspace directory")
	}
	if operation.anyOperationsFail != 0 {
		return errors.New("the delete was aborted before the workspace reached the Recycle Bin")
	}
	return nil
}
