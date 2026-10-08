//go:build !windows

package modkit

import "io/fs"

// isReparsePoint is a no-op on non-Windows platforms; symlinks are already
// caught by checking ModeSymlink in isReparseOrSymlink.
func isReparsePoint(d fs.DirEntry) bool {
	return false
}
