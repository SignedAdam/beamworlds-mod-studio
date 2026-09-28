//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileIDInfo is the layout of FILE_ID_INFO returned by GetFileInformationByHandleEx.
// VolumeSerialNumber is 64-bit; FileId is 128-bit (FILE_ID_128).
type fileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

// fileStandardInfo is the layout of FILE_STANDARD_INFO.
type fileStandardInfo struct {
	AllocationSize int64
	EndOfFile      int64
	NumberOfLinks  uint32
	DeletePending  uint8
	Directory      uint8
	_              [2]byte // padding
}

const (
	fileIDInfoClass       = 18 // FileIdInfo
	fileStandardInfoClass = 1  // FileStandardInfo

	// FILE_READ_ATTRIBUTES (0x80) is sufficient for metadata inspection.
	// GENERIC_READ requests content-read permission which may be denied for
	// audits/probes of files the user can see but not open for reading.
	fileReadAttributes = 0x80
)

// inspectArchiveFile uses Windows FILE_ID_INFO and FILE_STANDARD_INFO to
// obtain authoritative volume ID, 128-bit file ID, allocated size, and link
// count. Opens with FILE_READ_ATTRIBUTES so metadata inspection works
// independently of content-read permission. IDs are encoded as hex strings
// so they survive JSON/JS boundaries without precision loss.
func inspectArchiveFile(path string) (ArchiveFileIdentity, error) {
	// Normalize to extended-length path for long-path support.
	normalized := normalizeLongPath(path)
	pathUTF16, err := windows.UTF16PtrFromString(normalized)
	if err != nil {
		return ArchiveFileIdentity{}, fmt.Errorf("encode path: %w", err)
	}
	handle, err := windows.CreateFile(
		pathUTF16,
		fileReadAttributes,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return ArchiveFileIdentity{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer windows.CloseHandle(handle)

	var identity ArchiveFileIdentity

	// FILE_ID_INFO: volume serial + 128-bit file ID.
	var idInfo fileIDInfo
	err = windows.GetFileInformationByHandleEx(
		handle,
		fileIDInfoClass,
		(*byte)(unsafe.Pointer(&idInfo)),
		uint32(unsafe.Sizeof(idInfo)),
	)
	if err == nil {
		identity.VolumeID = strconv.FormatUint(idInfo.VolumeSerialNumber, 16)
		fileIDHex := fmt.Sprintf("%x", idInfo.FileID[:])
		// Mark IdentityKnown false for all-zero FileId (unavailable/unsupported).
		allZero := true
		for _, b := range idInfo.FileID {
			if b != 0 {
				allZero = false
				break
			}
		}
		if !allZero {
			identity.FileID = fileIDHex
			identity.IdentityKnown = true
		}
	}
	if !identity.IdentityKnown {
		var legacy windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(handle,&legacy); err == nil {
			index := uint64(legacy.FileIndexHigh)<<32 | uint64(legacy.FileIndexLow)
			if index != 0 {
				identity.VolumeID = "legacy-"+strconv.FormatUint(uint64(legacy.VolumeSerialNumber),16)
				identity.FileID = "legacy-"+strconv.FormatUint(index,16)
				identity.IdentityKnown = true
				identity.Links = int(legacy.NumberOfLinks)
			}
		}
	}

	// FILE_STANDARD_INFO: allocation, links, regularity.
	var stdInfo fileStandardInfo
	err = windows.GetFileInformationByHandleEx(
		handle,
		fileStandardInfoClass,
		(*byte)(unsafe.Pointer(&stdInfo)),
		uint32(unsafe.Sizeof(stdInfo)),
	)
	if err == nil {
		identity.AllocatedBytes = stdInfo.AllocationSize
		identity.AllocationKnown = true
		identity.Links = int(stdInfo.NumberOfLinks)
		identity.Regular = stdInfo.Directory == 0
	}

	// Size and mtime from os.Stat — always available.
	info, statErr := os.Stat(path)
	if statErr != nil {
		return identity, fmt.Errorf("stat %s: %w", path, statErr)
	}
	identity.SizeBytes = info.Size()
	identity.ModifiedNs = strconv.FormatInt(info.ModTime().UnixNano(), 10)
	if !identity.Regular {
		identity.Regular = info.Mode().IsRegular()
	}

	return identity, nil
}

// normalizeLongPath converts a path to the extended-length \\?\ form when it
// exceeds MAX_PATH or contains segments that benefit from verbatim handling.
func normalizeLongPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	abs = filepath.Clean(abs)
	// Already extended-length.
	if strings.HasPrefix(abs, `\\?\`) {
		return abs
	}
	// UNC path → \\?\UNC\...
	if strings.HasPrefix(abs, `\\`) {
		return `\\?\UNC\` + abs[2:]
	}
	// Only apply the prefix when the path is long enough to need it.
	if len(abs) >= 260 {
		return `\\?\` + abs
	}
	return abs
}

// availableArchiveBytes returns the available free space on the volume
// containing the given path using GetDiskFreeSpaceExW. Walks up to the
// nearest existing directory for paths that don't exist yet.
func availableArchiveBytes(path string) (int64, error) {
	resolved := nearestExistingDir(path)
	if resolved == "" {
		return 0, fmt.Errorf("no existing ancestor for %s", path)
	}

	pathUTF16, err := windows.UTF16PtrFromString(resolved)
	if err != nil {
		return 0, err
	}
	var freeBytesAvailable uint64
	err = windows.GetDiskFreeSpaceEx(pathUTF16, &freeBytesAvailable, nil, nil)
	if err != nil {
		return 0, fmt.Errorf("query free space for %s: %w", path, err)
	}
	return int64(freeBytesAvailable), nil
}

// renameArchiveNoReplace atomically moves from → to, failing if to already
// exists. Uses MoveFileExW WITHOUT MOVEFILE_REPLACE_EXISTING so the
// destination is never silently overwritten. MOVEFILE_WRITE_THROUGH ensures
// the rename is flushed to disk before returning.
func renameArchiveNoReplace(from, to string) error {
	fromNorm := normalizeLongPath(from)
	toNorm := normalizeLongPath(to)
	fromUTF16, err := windows.UTF16PtrFromString(fromNorm)
	if err != nil {
		return fmt.Errorf("encode source path: %w", err)
	}
	toUTF16, err := windows.UTF16PtrFromString(toNorm)
	if err != nil {
		return fmt.Errorf("encode destination path: %w", err)
	}
	// MOVEFILE_WRITE_THROUGH = 0x8: flush to disk.
	// No MOVEFILE_REPLACE_EXISTING: fail if destination exists.
	const movefileWriteThrough = 0x8
	return windows.MoveFileEx(fromUTF16, toUTF16, movefileWriteThrough)
}

// Returns a known per-file size restriction, or zero when no special limit was
// identified. The handle resolves mount points and junctions on the real volume.
func archiveDestinationFileLimit(path string)(int64,error){
	root:=nearestExistingDir(path)
	if root==""{return 0,fmt.Errorf("destination is unavailable: %s",path)}
	name,err:=windows.UTF16PtrFromString(normalizeLongPath(root));if err!=nil{return 0,err}
	handle,err:=windows.CreateFile(name,fileReadAttributes,windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,nil,windows.OPEN_EXISTING,windows.FILE_FLAG_BACKUP_SEMANTICS,0)
	if err!=nil{return 0,err};defer windows.CloseHandle(handle)
	var filesystem [64]uint16
	if err:=windows.GetVolumeInformationByHandle(handle,nil,0,nil,nil,nil,&filesystem[0],uint32(len(filesystem)));err!=nil{return 0,err}
	switch windows.UTF16ToString(filesystem[:]){case "FAT","FAT32":return (1<<32)-1,nil}
	return 0,nil
}
