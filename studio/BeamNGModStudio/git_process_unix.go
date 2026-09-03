//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

const (
	gitProcessTerminateGrace = 750 * time.Millisecond
	gitProcessKillWait       = 2 * time.Second
)

func runGitCommandContext(ctx context.Context, command *exec.Cmd) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if command.Process != nil {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		}
		timer := time.NewTimer(gitProcessTerminateGrace)
		select {
		case <-done:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
		if command.Process != nil {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
		timer = time.NewTimer(gitProcessKillWait)
		select {
		case <-done:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		return ctx.Err()
	}
}
