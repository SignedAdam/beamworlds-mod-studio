//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

// createDirectoryJunction creates a symlink at link pointing to target.
func createDirectoryJunction(link, target string) error {
	return os.Symlink(target, link)
}

// removeDirectoryJunction removes a symlink (the link itself) without following
// or deleting the target's contents. Returns nil if the link does not exist.
func removeDirectoryJunction(link string) error {
	info, err := os.Lstat(link)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return os.Remove(link)
	}
	return os.Remove(link)
}

// isDirectoryJunction returns true if path is a symlink.
func isDirectoryJunction(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSymlink != 0
}

// junctionTarget returns the target of a symlink at path, or "" if it is not
// a symlink. The returned target is cleaned.
func junctionTarget(path string) string {
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return filepath.Clean(target)
}
