//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// parentOf is the pid of a process's parent, as ps sees it, or 0.
func parentOf(pid int) int {
	out, err := run("", "ps", "-o", "ppid=", "-p", strconv.Itoa(pid))
	if err != nil {
		return 0
	}
	parent, _ := strconv.Atoi(strings.TrimSpace(out))
	return parent
}

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
