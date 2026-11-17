//go:build windows

package main

import (
	"os/exec"
	"syscall"
	"unsafe"
)

// setRawCmdLine hands cmd.exe the line exactly as written; Go's own argument
// quoting would escape the quotes inside it.
func setRawCmdLine(cmd *exec.Cmd, line string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
}

// processAlive reports whether a process with this pid is still running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const queryLimitedInformation = 0x1000
	const stillActive = 259
	handle, err := syscall.OpenProcess(queryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == stillActive
}

// parentOf is the pid of a process's parent, read from a snapshot of all
// processes, or 0. Windows keeps a parent's pid after it ended; the caller
// checks that it is alive.
func parentOf(pid int) int {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer syscall.CloseHandle(snap)
	var entry syscall.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = syscall.Process32First(snap, &entry); err == nil; err = syscall.Process32Next(snap, &entry) {
		if int(entry.ProcessID) == pid {
			return int(entry.ParentProcessID)
		}
	}
	return 0
}
