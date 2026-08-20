//go:build unix

package main

import (
	"errors"
	"os/exec"
	"syscall"
)

func zaiProcessGroupsSupported() bool { return true }

func configureZAICommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func zaiCommandProcessGroup(cmd *exec.Cmd) (int, error) {
	if cmd.Process == nil {
		return 0, errors.New("Z.AI credential process is unavailable")
	}
	return cmd.Process.Pid, nil
}

func terminateZAICommand(cmd *exec.Cmd, group int) {
	if cmd.Process == nil {
		return
	}
	if group <= 0 || syscall.Kill(-group, syscall.SIGKILL) != nil {
		_ = cmd.Process.Kill()
	}
}
