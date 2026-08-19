//go:build unix

package main

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestSensitiveFilesRejectFIFOsPromptly(t *testing.T) {
	for name, open := range map[string]func(string) error{
		"Claude credentials": func(path string) error {
			file, err := openPrivateClaudeCredentials(path)
			if file != nil {
				file.Close()
			}
			return err
		},
		"Kimi token": func(path string) error {
			_, err := readKimiToken(path)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := t.TempDir() + "/fifo"
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- open(path) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("accepted FIFO")
				}
			case <-time.After(time.Second):
				t.Fatal("FIFO open blocked")
			}
		})
	}
}

func assertTestProcessExited(t *testing.T, pid int) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); ; {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatalf("check test helper process %d: %v", pid, err)
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("test helper process %d survived cleanup", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
