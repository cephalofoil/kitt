//go:build !windows

package main

import "os/exec"

func setRawCmdLine(cmd *exec.Cmd, line string) {}
