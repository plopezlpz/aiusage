//go:build !unix

package main

import (
	"errors"
	"os"
	"os/exec"
)

func kimiProcessGroupsSupported() bool { return false }

func configureKimiCommand(*exec.Cmd) {}

func kimiCommandProcessGroup(*exec.Cmd) (int, error) {
	return 0, errors.New("Kimi process groups are unsupported")
}

func terminateKimiCommand(cmd *exec.Cmd, _ int) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func readKimiToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("token file must be a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("token file is unavailable")
	}
	defer file.Close()
	return readBoundedKimiToken(file)
}
