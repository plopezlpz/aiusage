//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func antigravityProcessSupported() bool { return true }

func configureAntigravityCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateAntigravityCommand(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	// Setpgid creates a new group with the child's PID; never signal other agy sessions.
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return os.ErrProcessDone
	}
	return err
}
