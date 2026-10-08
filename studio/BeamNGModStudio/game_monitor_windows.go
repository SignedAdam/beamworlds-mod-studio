//go:build windows

package main

import (
	"errors"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// beamNGProcesses enumerates running BeamNG processes with their PID and
// approximate start time (via GetProcessTimes).
func beamNGProcesses() ([]gameProcess, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return nil, nil
		}
		return nil, err
	}

	var result []gameProcess
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		if strings.EqualFold(name, "BeamNG.drive.x64.exe") || strings.EqualFold(name, "BeamNG.drive.exe") {
			gp := gameProcess{PID: entry.ProcessID}
			// Try to get process creation time.
			handle, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
			if openErr == nil {
				var creation, exit, kernel, user windows.Filetime
				if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err == nil {
					gp.Started = time.Unix(0, creation.Nanoseconds())
				}
				windows.CloseHandle(handle)
			}
			result = append(result, gp)
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return result, err
		}
	}
	return result, nil
}
