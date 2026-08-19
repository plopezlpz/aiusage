//go:build unix

package main

import (
	"fmt"
	"os/exec"
	"syscall"
)

func codexProcessGroupsSupported() bool { return true }

func configureCodexCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func codexCommandProcessGroup(cmd *exec.Cmd) (int, error) {
	if cmd.Process == nil {
		return 0, fmt.Errorf("Codex process is unavailable")
	}
	group, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return 0, err
	}
	return group, nil
}

func terminateCodexCommand(cmd *exec.Cmd, group int) {
	if cmd.Process == nil {
		return
	}
	if group <= 0 || syscall.Kill(-group, syscall.SIGKILL) != nil {
		_ = cmd.Process.Kill()
	}
}
