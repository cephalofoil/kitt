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
