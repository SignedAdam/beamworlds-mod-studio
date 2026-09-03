//go:build production && windows

package main

import (
	"embed"
	"io/fs"
)

// The release payload is staged by the ai-runtime:fetch task. Keeping the
// payload private and embedded here makes production Windows builds
// self-contained; runtime code never downloads it.
//
//go:embed bin/runtime/omp-windows-x64.exe bin/runtime/LICENSE bin/runtime/THIRD-PARTY-NOTICES.txt
var embeddedAIRuntimePayload embed.FS

func embeddedAIRuntimeFS() (fs.FS, bool) {
	return embeddedAIRuntimePayload, true
}

func productionAIRuntimePayloadAvailable() bool {
	return true
}
