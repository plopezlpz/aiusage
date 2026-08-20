//go:build !unix

package main

import (
	"errors"
	"os/exec"
)

func zaiProcessGroupsSupported() bool { return false }
func configureZAICommand(*exec.Cmd)   {}

func zaiCommandProcessGroup(*exec.Cmd) (int, error) {
	return 0, errors.New("Z.AI process groups are unsupported")
}

func terminateZAICommand(cmd *exec.Cmd, _ int) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
