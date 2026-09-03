//go:build !windows

package main

import (
	"errors"
	"io"
)

func tryAcquireManagedAIRuntimeInstallLock(string) (io.Closer, bool, error) {
	return nil, false, errors.New("managed AI runtime installation lock is only supported on Windows")
}
