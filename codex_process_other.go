//go:build !unix

package main

import (
	"errors"
	"os/exec"
)

func codexProcessGroupsSupported() bool { return false }

func configureCodexCommand(*exec.Cmd) {}

func codexCommandProcessGroup(*exec.Cmd) (int, error) {
	return 0, errors.New("Codex process groups are unsupported")
}

func terminateCodexCommand(cmd *exec.Cmd, _ int) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
