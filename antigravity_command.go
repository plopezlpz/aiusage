package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Never feed /usage to older agy versions that may interpret it as a model
// prompt. Non-interactive usage reports were introduced in 1.1.11.
func supportsAntigravityUsage(version string) bool {
	match := regexp.MustCompile(`^(?:agy(?: version)?\s+)?v?(\d+)\.(\d+)\.(\d+)(?:[-+][A-Za-z0-9.-]+)?$`).FindStringSubmatch(strings.TrimSpace(version))
	if match == nil {
		return false
	}
	major, e1 := strconv.Atoi(match[1])
	minor, e2 := strconv.Atoi(match[2])
	patch, e3 := strconv.Atoi(match[3])
	return e1 == nil && e2 == nil && e3 == nil && (major > 1 || major == 1 && (minor > 1 || minor == 1 && patch >= 11))
}

func antigravityExecutable() (string, error) {
	if override, ok := os.LookupEnv("ANTIGRAVITY_CLI_PATH"); ok {
		if override == "" || !filepath.IsAbs(override) {
			return "", errors.New("ANTIGRAVITY_CLI_PATH must be an absolute executable path")
		}
		path, err := exec.LookPath(override)
		if err != nil {
			return "", errors.New("ANTIGRAVITY_CLI_PATH is not executable")
		}
		return path, nil
	}
	path, err := exec.LookPath("agy")
	if err != nil {
		return "", errors.New("Antigravity CLI not found — install agy and sign in")
	}
	return path, nil
}

func fetchAntigravity(ctx context.Context) ([]byte, error) {
	if !antigravityProcessSupported() {
		return nil, errors.New("Antigravity collection is supported only on Unix")
	}
	executable, err := antigravityExecutable()
	if err != nil {
		return nil, err
	}
	return fetchAntigravityExecutable(ctx, executable)
}

func fetchAntigravityExecutable(ctx context.Context, executable string) ([]byte, error) {
	if !antigravityProcessSupported() {
		return nil, errors.New("Antigravity collection is supported only on Unix")
	}
	dir, err := os.MkdirTemp("", "aiusage-antigravity-")
	if err != nil {
		return nil, errors.New("could not create private Antigravity probe directory")
	}
	defer os.RemoveAll(dir)
	// Use documented launcher guards, not browser credential extraction. These
	// block PATH/BROWSER based login launchers; they are not an OS sandbox.
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
			return nil, errors.New("could not install Antigravity browser guards")
		}
	}
	env := antigravityEnvironment(os.Environ(), dir)
	versionCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	version, err := runAntigravityCommand(versionCtx, executable, []string{"--version"}, dir, env, 4096)
	cancel()
	if err != nil {
		return nil, err
	}
	if !supportsAntigravityUsage(string(version)) {
		return nil, errors.New("Antigravity CLI version is unknown or too old — update agy for /usage JSON support")
	}
	return runAntigravityCommand(ctx, executable, []string{
		"-p", "/usage", "--output-format", "json", "--print-timeout", "10s",
		"--log-file", filepath.Join(dir, "usage.log"),
		"--settings", `{"toolPermission":"strict","allowNonWorkspaceAccess":false,"enableTerminalSandbox":true}`,
	}, dir, env, maxInputSize)
}

func antigravityEnvironment(inherited []string, dir string) []string {
	var env []string
	path := "/usr/bin:/bin"
	for _, value := range inherited {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "PATH":
			path = strings.TrimPrefix(value, "PATH=")
		case "BROWSER", "AGY_CLI_DISABLE_AUTO_UPDATE":
		default:
			env = append(env, value)
		}
	}
	return append(env, "PATH="+dir+string(os.PathListSeparator)+path, "BROWSER="+filepath.Join(dir, "open"), "AGY_CLI_DISABLE_AUTO_UPDATE=true")
}

type antigravityOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (b *antigravityOutput) Write(p []byte) (int, error) {
	count := len(p)
	remaining := max(0, b.limit-b.buffer.Len())
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
		b.cancel()
	}
	_, _ = b.buffer.Write(p)
	return count, nil
}

func runAntigravityCommand(parent context.Context, executable string, args []string, dir string, env []string, limit int) ([]byte, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir, cmd.Env = dir, env
	configureAntigravityCommand(cmd)
	cmd.Cancel = func() error { return terminateAntigravityCommand(cmd) }
	cmd.WaitDelay = 500 * time.Millisecond
	output := &antigravityOutput{limit: limit, cancel: cancel}
	cmd.Stdout = output
	// A descriptor (rather than stderr buffering) avoids retaining diagnostics,
	// login URLs, or any account identity in memory/cache/error messages.
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, errors.New("could not prepare Antigravity usage check")
	}
	defer null.Close()
	cmd.Stderr = null
	if err := cmd.Start(); err != nil {
		return nil, errors.New("could not start Antigravity CLI")
	}
	defer terminateAntigravityCommand(cmd)
	err = cmd.Wait()
	if output.overflow {
		return nil, errors.New("Antigravity output exceeded size limit")
	}
	if parent.Err() != nil {
		return nil, errors.New("Antigravity usage check timed out or was canceled")
	}
	if err != nil {
		return nil, errors.New("Antigravity usage check failed — run agy and check /usage; sign in if needed")
	}
	return output.buffer.Bytes(), nil
}
