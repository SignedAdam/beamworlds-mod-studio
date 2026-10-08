//go:build !windows

package main

// beamNGProcesses is a no-op on non-Windows platforms.
func beamNGProcesses() ([]gameProcess, error) {
	return nil, nil
}
