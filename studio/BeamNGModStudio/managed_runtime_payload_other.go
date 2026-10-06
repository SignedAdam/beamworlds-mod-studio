//go:build !production || !windows

package main

import "io/fs"

func embeddedAIRuntimeFS() (fs.FS, bool) {
	return nil, false
}

func productionAIRuntimePayloadAvailable() bool {
	return false
}
