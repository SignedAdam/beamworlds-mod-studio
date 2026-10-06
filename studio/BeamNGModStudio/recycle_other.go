//go:build !windows

package main

import "errors"

// Deleting an archive is only offered where it can be undone. Elsewhere the
// user keeps the file and can forget the mod instead.
func recycleFile(string) error {
	return errors.New("deleting archives to the Recycle Bin is only supported on Windows")
}
