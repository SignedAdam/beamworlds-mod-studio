//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procFindFirstFileNameW = windows.NewLazySystemDLL("kernel32.dll").NewProc("FindFirstFileNameW")
	procFindNextFileNameW  = windows.NewLazySystemDLL("kernel32.dll").NewProc("FindNextFileNameW")
)

// otherHardLinkPaths returns the other paths that name the same file as path.
// Each candidate is confirmed with os.SameFile, so a name Windows reports
// relative to a differently mounted volume is never mistaken for a copy.
// Windows reports link names in long form, so path is compared in long form
// too; otherwise an 8.3 short path would report the file itself as a copy.
func otherHardLinkPaths(path string) ([]string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	absolute = longPathName(absolute)
	volume := filepath.VolumeName(absolute)
	if volume == "" {
		return nil, fmt.Errorf("no volume for %s", absolute)
	}
	original, err := os.Stat(absolute)
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(normalizeLongPath(absolute))
	if err != nil {
		return nil, err
	}
	buffer := make([]uint16, windows.MAX_PATH)
	length := uint32(len(buffer))
	handle, _, callErr := procFindFirstFileNameW.Call(uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&length)), uintptr(unsafe.Pointer(&buffer[0])))
	if windows.Handle(handle) == windows.InvalidHandle && errors.Is(callErr, windows.ERROR_MORE_DATA) {
		buffer = make([]uint16, length)
		handle, _, callErr = procFindFirstFileNameW.Call(uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&length)), uintptr(unsafe.Pointer(&buffer[0])))
	}
	if windows.Handle(handle) == windows.InvalidHandle {
		return nil, fmt.Errorf("enumerate hard links of %s: %w", absolute, callErr)
	}
	defer windows.FindClose(windows.Handle(handle))
	var others []string
	for {
		candidate := volume + windows.UTF16ToString(buffer[:min(int(length), len(buffer))])
		if !samePath(candidate, absolute) {
			if info, err := os.Stat(candidate); err == nil && os.SameFile(info, original) {
				others = append(others, candidate)
			}
		}
		length = uint32(len(buffer))
		ok, _, callErr := procFindNextFileNameW.Call(handle, uintptr(unsafe.Pointer(&length)), uintptr(unsafe.Pointer(&buffer[0])))
		if ok != 0 {
			continue
		}
		if errors.Is(callErr, windows.ERROR_MORE_DATA) {
			buffer = make([]uint16, length)
			if ok, _, callErr = procFindNextFileNameW.Call(handle, uintptr(unsafe.Pointer(&length)), uintptr(unsafe.Pointer(&buffer[0]))); ok != 0 {
				continue
			}
		}
		if errors.Is(callErr, windows.ERROR_HANDLE_EOF) {
			return others, nil
		}
		return others, fmt.Errorf("enumerate hard links of %s: %w", absolute, callErr)
	}
}

// longPathName expands 8.3 short components (C:\Users\RUNNER~1) to their long
// names. Paths that do not exist, or cannot be expanded, are returned unchanged.
func longPathName(path string) string {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return path
	}
	buffer := make([]uint16, windows.MAX_PATH)
	for {
		length, err := windows.GetLongPathName(name, &buffer[0], uint32(len(buffer)))
		if err != nil || length == 0 {
			return path
		}
		if int(length) < len(buffer) {
			return windows.UTF16ToString(buffer[:length])
		}
		buffer = make([]uint16, length)
	}
}
