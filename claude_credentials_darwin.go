//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"os/user"
	"time"
)

func loadPlatformClaudeCredentialData(ctx context.Context) ([]byte, error) {
	keychainContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	keychainErr := errors.New("current user is unavailable")
	current, err := user.Current()
	if err == nil && current.Username != "" {
		var output boundedBuffer
		output.limit = maxClaudeCredentialsSize
		cmd := exec.CommandContext(keychainContext, "/usr/bin/security", "find-generic-password", "-a", current.Username, "-s", "Claude Code-credentials", "-w")
		cmd.Stdout = &output
		cmd.Stderr = io.Discard
		if runErr := cmd.Run(); runErr == nil {
			return output.data, nil
		} else {
			keychainErr = runErr
		}
	}
	data, fileErr := readClaudeCredentialsFile()
	if fileErr != nil {
		return nil, fmt.Errorf("Claude OAuth credentials are unavailable (login keychain: %v; private credentials file: %v) — run claude login", keychainErr, fileErr)
	}
	return data, nil
}
