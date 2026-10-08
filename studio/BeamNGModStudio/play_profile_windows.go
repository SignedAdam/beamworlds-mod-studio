//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createDirectoryJunction creates a directory junction at link pointing to target.
// The link must not already exist. Uses native NTFS reparse points; no elevation required.
func createDirectoryJunction(link, target string) error {
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve junction target: %w", err)
	}
	// Strip extended-length prefix if present; the substitute name adds its own.
	absTarget = strings.TrimPrefix(absTarget, `\\?\`)

	absLink, err := filepath.Abs(link)
	if err != nil {
		return fmt.Errorf("resolve junction link: %w", err)
	}
	if err := os.Mkdir(absLink, 0o755); err != nil {
		return fmt.Errorf("create junction directory: %w", err)
	}
	if err := setMountPoint(absLink, absTarget); err != nil {
		_ = os.Remove(absLink)
		return err
	}
	return nil
}

func setMountPoint(link, target string) error {
	linkPtr, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		linkPtr,
		windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return fmt.Errorf("open junction directory: %w", err)
	}
	defer windows.CloseHandle(handle)

	// Substitute name: \??\<absTarget>  (NUL-terminated UTF-16)
	subName := windows.StringToUTF16(`\??\` + target)
	subBytes := len(subName) * 2 // includes NUL

	// Print name: <absTarget>  (NUL-terminated UTF-16)
	printName := windows.StringToUTF16(target)
	printBytes := len(printName) * 2 // includes NUL

	// MountPointReparseBuffer fields (8 bytes) + PathBuffer
	pathBuf := subBytes + printBytes
	reparseDataLen := 8 + pathBuf

	// Full buffer: ReparseTag(4) + ReparseDataLength(2) + Reserved(2) + fields(8) + paths
	bufSize := 8 + reparseDataLen
	buf := make([]byte, bufSize)

	// REPARSE_DATA_BUFFER header
	*(*uint32)(unsafe.Pointer(&buf[0])) = 0xA0000003 // IO_REPARSE_TAG_MOUNT_POINT
	*(*uint16)(unsafe.Pointer(&buf[4])) = uint16(reparseDataLen)
	// Reserved at buf[6..7] = 0

	// MountPointReparseBuffer
	*(*uint16)(unsafe.Pointer(&buf[8])) = 0                          // SubstituteNameOffset
	*(*uint16)(unsafe.Pointer(&buf[10])) = uint16(subBytes - 2)      // SubstituteNameLength (excl NUL)
	*(*uint16)(unsafe.Pointer(&buf[12])) = uint16(subBytes)          // PrintNameOffset (after sub NUL)
	*(*uint16)(unsafe.Pointer(&buf[14])) = uint16(printBytes - 2)    // PrintNameLength (excl NUL)

	// PathBuffer: substitute name then print name (both NUL-terminated)
	off := 16
	for _, c := range subName {
		*(*uint16)(unsafe.Pointer(&buf[off])) = c
		off += 2
	}
	for _, c := range printName {
		*(*uint16)(unsafe.Pointer(&buf[off])) = c
		off += 2
	}

	var bytesReturned uint32
	const fsctlSetReparsePoint = 0x000900A4
	if err := windows.DeviceIoControl(handle, fsctlSetReparsePoint, &buf[0], uint32(bufSize), nil, 0, &bytesReturned, nil); err != nil {
		return fmt.Errorf("set junction reparse point: %w", err)
	}
	return nil
}

// removeDirectoryJunction removes a directory junction (the link itself) without
// following or deleting the target's contents.
func removeDirectoryJunction(link string) error {
	if _, err := os.Lstat(link); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return os.Remove(link)
}

// isDirectoryJunction returns true if path is a directory junction or symlink.
func isDirectoryJunction(path string) bool {
	_, err := os.Readlink(path)
	return err == nil
}

// junctionTarget returns the target of a directory junction/symlink at path,
// or "" if it is not a junction/symlink. The returned target is cleaned.
func junctionTarget(path string) string {
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return filepath.Clean(target)
}
