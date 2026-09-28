//go:build !windows

package main

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// inspectArchiveFile uses Unix stat to obtain device, inode, allocated blocks,
// link count, and regularity. Volume ID is dev, file ID is ino — both encoded
// as hex strings for lossless JSON transport.
//
// Darwin's Stat_t.Dev is int32 (signed); cast to uint64 for consistent hex
// encoding. Zero inode marks IdentityKnown false.
func inspectArchiveFile(path string) (ArchiveFileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return ArchiveFileIdentity{}, fmt.Errorf("stat %s: %w", path, err)
	}

	identity := ArchiveFileIdentity{
		SizeBytes:  info.Size(),
		ModifiedNs: strconv.FormatInt(info.ModTime().UnixNano(), 10),
		Regular:    info.Mode().IsRegular(),
	}

	sys, ok := info.Sys().(*syscall.Stat_t)
	if ok && sys != nil {
		// Cast Dev to uint64 for Darwin compatibility (int32 on Darwin).
		identity.VolumeID = strconv.FormatUint(uint64(sys.Dev), 16)
		identity.Links = int(sys.Nlink)
		// Stat_t.Blocks is in 512-byte units on most Unix systems.
		identity.AllocatedBytes = sys.Blocks * 512
		identity.AllocationKnown = true

		// Guard zero inode — some filesystems report 0 for synthetic entries.
		if sys.Ino != 0 {
			identity.FileID = strconv.FormatUint(sys.Ino, 16)
			identity.IdentityKnown = true
		}
	}

	return identity, nil
}

// availableArchiveBytes returns the available free space on the filesystem
// containing the given path using statfs. Walks up to the nearest existing
// directory for paths that don't exist yet.
func availableArchiveBytes(path string) (int64, error) {
	resolved := nearestExistingDir(path)
	if resolved == "" {
		return 0, fmt.Errorf("no existing ancestor for %s", path)
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(resolved, &stat); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

// Link/unlink is a no-clobber fallback for old Unix kernels. Callers journal
// both names, so an interruption between the two steps remains recoverable.
func linkArchiveNoReplace(from, to string) error {
	source, err := os.Stat(from)
	if err != nil { return err }
	if err := os.Link(from,to); err != nil { return err }
	if err := os.Remove(from); err != nil {
		if current, statErr := os.Lstat(to); statErr == nil && os.SameFile(source,current) {
			if cleanupErr := os.Remove(to); cleanupErr != nil { return fmt.Errorf("remove old name: %v; undo new link: %w",err,cleanupErr) }
		}
		return err
	}
	return nil
}
