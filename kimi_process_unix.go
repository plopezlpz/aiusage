//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func kimiProcessGroupsSupported() bool { return true }

func configureKimiCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func kimiCommandProcessGroup(cmd *exec.Cmd) (int, error) {
	if cmd.Process == nil {
		return 0, errors.New("Kimi process is unavailable")
	}
	return syscall.Getpgid(cmd.Process.Pid)
}

func terminateKimiCommand(cmd *exec.Cmd, group int) {
	if cmd.Process == nil {
		return
	}
	if group <= 0 || syscall.Kill(-group, syscall.SIGKILL) != nil {
		_ = cmd.Process.Kill()
	}
}

func readKimiToken(path string) (string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", errors.New("token file is unavailable or unsafe")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return "", errors.New("inspect token file")
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Mode&0o077 != 0 || stat.Uid != uint32(os.Geteuid()) {
		return "", errors.New("token file must be a private regular file owned by the current user")
	}
	return readBoundedKimiToken(file)
}
