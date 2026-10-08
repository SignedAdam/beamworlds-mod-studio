package main

import "os"

// sourceIsFolder reports whether path is a directory that is not a reparse
// point or junction. It will be replaced by modkit.SourceKindOf at
// integration.
func sourceIsFolder(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.IsDir() && info.Mode()&os.ModeSymlink == 0
}
