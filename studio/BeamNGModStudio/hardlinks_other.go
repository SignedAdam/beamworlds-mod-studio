//go:build !windows

package main

import "errors"

// otherHardLinkPaths cannot enumerate link names portably; callers treat the
// error as "no other copy proven" and keep the data safe another way.
func otherHardLinkPaths(string) ([]string, error) {
	return nil, errors.New("hard link enumeration is only supported on Windows")
}
