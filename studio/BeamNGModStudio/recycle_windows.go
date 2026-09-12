//go:build windows

package main

// Deleting a mod archive sends it to the Recycle Bin rather than unlinking it.
// A mod library is not disposable: an accidental bulk delete has to be
// recoverable by the user without a backup.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	shellFileOperationDelete = 0x0003
	shellAllowUndo           = 0x0040
	shellNoConfirmation      = 0x0010
	shellSilent              = 0x0004
	shellNoErrorUI           = 0x0400
	shellNoConfirmMkDir      = 0x0200
)

type shellFileOperation struct {
	window            windows.Handle
	function          uint32
	from              *uint16
	to                *uint16
	flags             uint16
	anyOperationsFail int32
	nameMappings      uintptr
	progressTitle     *uint16
}

var shellFileOperationW = syscall.NewLazyDLL("shell32.dll").NewProc("SHFileOperationW")

func recycleFile(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", absolute)
	}
	// SHFileOperationW takes a double-null-terminated list, so the terminator
	// UTF16FromString adds is not enough on its own.
	encoded, err := windows.UTF16FromString(absolute)
	if err != nil {
		return err
	}
	encoded = append(encoded, 0)
	operation := shellFileOperation{
		function: shellFileOperationDelete,
		from:     &encoded[0],
		flags:    shellAllowUndo | shellNoConfirmation | shellSilent | shellNoErrorUI | shellNoConfirmMkDir,
	}
	status, _, callErr := shellFileOperationW.Call(uintptr(unsafe.Pointer(&operation)))
	if status != 0 {
		return fmt.Errorf("the Recycle Bin refused the file (code %d)", status)
	}
	if operation.anyOperationsFail != 0 {
		return errors.New("the delete was aborted before the file reached the Recycle Bin")
	}
	if _, err := os.Stat(absolute); err == nil {
		return errors.New("the file is still on disk after the delete reported success")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = callErr
	return nil
}
