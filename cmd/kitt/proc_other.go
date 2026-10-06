//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"syscall"
)

func setRawCmdLine(cmd *exec.Cmd, line string) {}

// processAlive reports whether a process with this pid exists. A process of
// another user answers EPERM: it exists all the same.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
