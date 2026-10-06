//go:build windows

package main

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
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
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create Git process job: %w", err)
	}
	defer windows.CloseHandle(job)
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		return fmt.Errorf("configure Git process job: %w", err)
	}
	if err := command.Start(); err != nil {
		return err
	}
	var assignErr error
	processHandle, openErr := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(command.Process.Pid),
	)
	if openErr != nil {
		assignErr = openErr
	} else {
		assignErr = windows.AssignProcessToJobObject(job, processHandle)
		_ = windows.CloseHandle(processHandle)
	}
	if assignErr != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("attach Git process job: %w", assignErr)
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = windows.TerminateJobObject(job, 1)
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
		_ = windows.TerminateJobObject(job, 1)
		if command.Process != nil {
			_ = command.Process.Kill()
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
