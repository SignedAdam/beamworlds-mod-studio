//go:build !windows

package main

func beamNGProcessRunning() (bool, error) {
	return false, nil
}
