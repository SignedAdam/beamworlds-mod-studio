package modkit

import (
	"io/fs"
	"syscall"
)

// isReparsePoint detects Windows reparse points (junctions, symlinks, etc.)
// by inspecting the file attributes from the DirEntry.
func isReparsePoint(d fs.DirEntry) bool {
	info, err := d.Info()
	if err != nil {
		return false
	}
	sys := info.Sys()
	if sys == nil {
		return false
	}
	if data, ok := sys.(*syscall.Win32FileAttributeData); ok {
		return data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return false
}
