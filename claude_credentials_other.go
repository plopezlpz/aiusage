//go:build !darwin

package main

import "context"

func loadPlatformClaudeCredentialData(context.Context) ([]byte, error) {
	return readClaudeCredentialsFile()
}
