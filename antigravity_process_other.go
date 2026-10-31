//go:build !unix

package main

import (
	"os"
	"os/exec"
)

func antigravityProcessSupported() bool     { return false }
func configureAntigravityCommand(*exec.Cmd) {}
func terminateAntigravityCommand(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}
