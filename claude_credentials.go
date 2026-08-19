package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxClaudeCredentialsSize = 64 << 10

func loadClaudeCredentials(ctx context.Context) (claudeCredentials, error) {
	data, err := loadPlatformClaudeCredentialData(ctx)
	if err != nil {
		return claudeCredentials{}, err
	}
	return parseClaudeCredentials(data)
}

func claudeCredentialsPath() (string, error) {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("find Claude configuration directory failed")
		}
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, ".credentials.json"), nil
}

func readClaudeCredentialsFile() ([]byte, error) {
	path, err := claudeCredentialsPath()
	if err != nil {
		return nil, err
	}
	file, err := openPrivateClaudeCredentials(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("Claude OAuth credentials are unavailable — run claude login")
		}
		return nil, errors.New("Claude OAuth credentials file is not a private current-user regular file")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxClaudeCredentialsSize+1))
	if err != nil {
		return nil, errors.New("read Claude OAuth credentials failed")
	}
	if len(data) > maxClaudeCredentialsSize {
		return nil, fmt.Errorf("Claude OAuth credentials exceed %d bytes", maxClaudeCredentialsSize)
	}
	return data, nil
}

type boundedBuffer struct {
	data  []byte
	limit int
}

func (w *boundedBuffer) Write(p []byte) (int, error) {
	if len(w.data)+len(p) > w.limit {
		return 0, errors.New("output exceeds limit")
	}
	w.data = append(w.data, p...)
	return len(p), nil
}
