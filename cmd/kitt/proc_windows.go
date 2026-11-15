//go:build windows

package main

import (
	"os/exec"
	"syscall"
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
