//go:build windows

package main

import (
	"errors"
	"io"

	"golang.org/x/sys/windows"
)

type managedAIRuntimeInstallWindowsLock struct {
	handle windows.Handle
}

func tryAcquireManagedAIRuntimeInstallLock(lockPath string) (io.Closer, bool, error) {
	path, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, false, err
	}
	handle, err := windows.CreateFile(
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_HIDDEN,
		0,
	)
	if err == nil {
		return &managedAIRuntimeInstallWindowsLock{handle: handle}, false, nil
	}
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return nil, true, err
	}
	return nil, false, err
}

func (lock *managedAIRuntimeInstallWindowsLock) Close() error {
	if lock == nil || lock.handle == windows.InvalidHandle {
		return nil
	}
	handle := lock.handle
	lock.handle = windows.InvalidHandle
	return windows.CloseHandle(handle)
}
