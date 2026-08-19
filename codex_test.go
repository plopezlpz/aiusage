package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestParseCodexRateLimitsNormalizesAndLabelsWindows(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	primaryReset := now.Add(4 * time.Hour).Unix()
	secondaryReset := now.Add(6 * 24 * time.Hour).Unix()
	input := fmt.Sprintf(`{"rateLimits":{"planType":"plus","primary":{"usedPercent":24,"windowDurationMins":300,"resetsAt":%d},"secondary":{"usedPercent":63,"windowDurationMins":10080,"resetsAt":%d}}}`, primaryReset, secondaryReset)

	plan, got, err := parseCodexRateLimits([]byte(input), now)
	if err != nil {
		t.Fatal(err)
	}
	if plan != "plus" || len(got) != 2 {
		t.Fatalf("plan/quotas = %q %#v", plan, got)
	}
	if got[0].Window != "5h" || got[0].RemainingPercentage != 76 || got[0].WindowDurationMins != 300 {
		t.Fatalf("primary = %#v", got[0])
	}
	if got[1].Window != "Weekly" || got[1].RemainingPercentage != 37 || !got[1].ResetsAt.Equal(time.Unix(secondaryReset, 0)) {
		t.Fatalf("secondary = %#v", got[1])
	}
	if codexWindowLabel(90) != "1h 30m" || codexWindowLabel(30) != "30m" {
		t.Fatal("compact duration labels changed")
	}
}

func TestParseCodexRateLimitsRejectsInvalidValues(t *testing.T) {
	now := time.Now()
	reset := now.Add(time.Hour).Unix()
	tests := map[string]string{
		"plan":     fmt.Sprintf(`{"rateLimits":{"planType":"made-up","primary":{"usedPercent":1,"windowDurationMins":60,"resetsAt":%d}}}`, reset),
		"used":     fmt.Sprintf(`{"rateLimits":{"planType":"plus","primary":{"usedPercent":101,"windowDurationMins":60,"resetsAt":%d}}}`, reset),
		"fraction": fmt.Sprintf(`{"rateLimits":{"planType":"plus","primary":{"usedPercent":1.5,"windowDurationMins":60,"resetsAt":%d}}}`, reset),
		"duration": fmt.Sprintf(`{"rateLimits":{"planType":"plus","primary":{"usedPercent":1,"windowDurationMins":0,"resetsAt":%d}}}`, reset),
		"reset":    `{"rateLimits":{"planType":"plus","primary":{"usedPercent":1,"windowDurationMins":60}}}`,
		"windows":  `{"rateLimits":{"planType":"plus"}}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseCodexRateLimits([]byte(input), now); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestFetchCodexUsesBoundedAppServerRPCAndTerminatesIt(t *testing.T) {
	if !codexProcessGroupsSupported() {
		t.Skip("Unix shell/process-group integration test")
	}
	dir := t.TempDir()
	script := dir + "/fake-codex-app-server"
	pidFile := dir + "/sleep.pid"
	if err := os.WriteFile(script, []byte(`#!/bin/sh
sleep 3600 &
echo $! > "$1"
read initialize || exit 3
printf '%s\n' '{"method":"unrelated/notification","params":{"ignored":true}}'
printf '%s\n' '{"id":1,"result":{}}'
read initialized || exit 4
read account || exit 5
read limits || exit 6
reset=$(( $(date +%s) + 3600 ))
weekly=$(( reset + 86400 ))
printf '%s\n' "{\"id\":3,\"result\":{\"rateLimits\":{\"planType\":\"pro\",\"primary\":{\"usedPercent\":20,\"windowDurationMins\":300,\"resetsAt\":$reset},\"secondary\":{\"usedPercent\":40,\"windowDurationMins\":10080,\"resetsAt\":$weekly}}}}"
printf '%s\n' '{"method":"account/rateLimits/updated","params":{}}'
printf '%s\n' '{"id":2,"result":{"account":{"type":"chatgpt","email":"must-not-be-read@example.invalid"},"requiresOpenaiAuth":true}}'
wait
`), 0o700); err != nil {
		t.Fatal(err)
	}
	factory := func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, script, pidFile) }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	plan, quotas, err := fetchCodex(ctx, factory)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("app-server was not terminated promptly: %v", elapsed)
	}
	if plan != "pro" || len(quotas) != 2 || quotas[0].RemainingPercentage != 80 || quotas[1].Window != "Weekly" {
		t.Fatalf("result = %q %#v", plan, quotas)
	}
	pidText, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidText)))
	if err != nil {
		t.Fatal(err)
	}
	assertTestProcessExited(t, pid)

	cmd := newCodexCommand(context.Background())
	want := []string{"codex", "-s", "read-only", "-a", "untrusted", "app-server"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("Codex command = %#v, want %#v", cmd.Args, want)
	}
}

func TestFetchCodexDeadlineClosesStdoutInheritedByDescendant(t *testing.T) {
	if !codexProcessGroupsSupported() {
		t.Skip("Unix shell/process-group integration test")
	}
	script := t.TempDir() + "/fake-codex-app-server"
	if err := os.WriteFile(script, []byte(`#!/bin/sh
sleep 2 &
read initialize || exit 3
printf '%s\n' '{"id":1,"result":{}}'
read initialized || exit 4
read account || exit 5
read limits || exit 6
wait
`), 0o700); err != nil {
		t.Fatal(err)
	}
	factory := func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, script) }
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, _, err := fetchCodex(ctx, factory)
	elapsed := time.Since(started)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want timeout", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("collection did not return after its deadline: %v", elapsed)
	}
}

func TestCollectorRuntimeCancellationJoinsCodexProcessGroup(t *testing.T) {
	if !codexProcessGroupsSupported() {
		t.Skip("Unix shell/process-group integration test")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	setTestCacheHome(t, dir)
	now := time.Now()
	cachePath, _ := codexCachePath()
	previous := codexCacheFile{
		Version: cacheVersion, Provider: "OpenAI Codex", PlanType: "plus", UpdatedAt: now.Add(-time.Minute), AttemptedAt: now.Add(-time.Minute),
		Quotas: []codexCachedQuota{{Window: "5h", WindowDurationMins: 300, RemainingPercentage: 42, ResetsAt: now.Add(time.Hour)}},
	}
	if err := writeJSONCache(cachePath, previous); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	script := dir + "/blocking-codex-app-server"
	pidFile := dir + "/sleep.pid"
	if err := os.WriteFile(script, []byte(`#!/bin/sh
sleep 3600 &
echo $! > "$1"
wait
`), 0o700); err != nil {
		t.Fatal(err)
	}
	factory := func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, script, pidFile) }
	runtime := newCollectorRuntime()
	result := make(chan tea.Msg, 1)
	command := runtime.command(func(ctx context.Context) tea.Msg {
		_, err := collectCodexWith(ctx, factory, now)
		return codexCollectedMsg{err: err}
	})
	go func() { result <- command() }()

	var pid int
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Codex descendant did not start")
		}
	}
	runtime.stop()
	select {
	case message := <-result:
		if err := message.(codexCollectedMsg).err; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled collection error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runtime did not join canceled Codex collector")
	}
	assertTestProcessExited(t, pid)
	after, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("canceled Codex collection mutated cache")
	}
}

func TestRPCErrorDoesNotExposeServerMessage(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("{\"id\":2,\"error\":{\"code\":-32001,\"message\":\"secret token\\ninternal diagnostic\"}}\n"))
	_, err := readRPCResponses(context.Background(), scanner, map[int]string{2: "account/read"})
	if err == nil {
		t.Fatal("expected RPC error")
	}
	if got, want := err.Error(), "Codex app-server account/read failed (-32001)"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestCodexFailurePreservesPrivateCacheAndClaudeCache(t *testing.T) {
	if !codexProcessGroupsSupported() {
		t.Skip("Unix process-group integration test")
	}
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	setTestCacheHome(t, temp)
	now := time.Now()
	reset := now.Add(time.Hour)
	codexPath, _ := codexCachePath()
	previous := codexCacheFile{
		Version: cacheVersion, Provider: "OpenAI Codex", PlanType: "plus", UpdatedAt: now.Add(-time.Minute),
		Quotas: []codexCachedQuota{{Window: "5h", WindowDurationMins: 300, RemainingPercentage: 42, ResetsAt: reset}},
	}
	if err := writeJSONCache(codexPath, previous); err != nil {
		t.Fatal(err)
	}
	claudePath, _ := cachePath()
	claude := cacheFile{Version: cacheVersion, Provider: "Claude", UpdatedAt: now, Quotas: []cachedQuota{{Window: "5-hour session", RemainingPercentage: 55}}}
	if err := writeJSONCache(claudePath, claude); err != nil {
		t.Fatal(err)
	}
	claudeBefore, _ := os.ReadFile(claudePath)

	factory := func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, filepathThatDoesNotExist(t)) }
	if _, err := collectCodexWith(context.Background(), factory, now); err == nil {
		t.Fatal("expected collection failure")
	}
	got, err := readCodexCache()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Quotas) != 1 || got.Quotas[0].RemainingPercentage != 42 || !got.UpdatedAt.Equal(previous.UpdatedAt) || got.Failure == "" {
		t.Fatalf("preserved Codex cache = %#v", got)
	}
	info, err := os.Stat(codexPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("Codex cache mode = %v", info.Mode().Perm())
	}
	claudeAfter, _ := os.ReadFile(claudePath)
	if string(claudeAfter) != string(claudeBefore) {
		t.Fatal("Codex collection changed the Claude cache")
	}
}

func TestObsoleteCodexFailureCannotRegressNewerCache(t *testing.T) {
	if !codexProcessGroupsSupported() {
		t.Skip("Unix process-group integration test")
	}
	t.Setenv("HOME", t.TempDir())
	setTestCacheHome(t, t.TempDir())
	now := time.Now()
	newer := now.Add(time.Minute)
	reset := now.Add(time.Hour)
	path, _ := codexCachePath()
	cache := codexCacheFile{Version: cacheVersion, Provider: "OpenAI Codex", PlanType: "plus", UpdatedAt: newer, AttemptedAt: newer, Quotas: []codexCachedQuota{{Window: "5h", WindowDurationMins: 300, RemainingPercentage: 66, ResetsAt: reset}}}
	if err := writeJSONCache(path, cache); err != nil {
		t.Fatal(err)
	}
	factory := func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, filepathThatDoesNotExist(t)) }
	got, err := collectCodexWith(context.Background(), factory, now)
	if err != nil || !got.AttemptedAt.Equal(newer) || got.Failure != "" || got.Quotas[0].RemainingPercentage != 66 {
		t.Fatalf("obsolete Codex result = %#v, %v", got, err)
	}
}

func filepathThatDoesNotExist(t *testing.T) string {
	return t.TempDir() + "/missing-codex"
}

func TestTUIRendersCodexAloneAndRefreshesAsynchronously(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	setTestCacheHome(t, temp)
	now := time.Now()
	cache := codexCacheFile{
		Version: cacheVersion, Provider: "OpenAI Codex", PlanType: "plus", UpdatedAt: now,
		Quotas: []codexCachedQuota{{Window: "5h", WindowDurationMins: 300, RemainingPercentage: 75, ResetsAt: now.Add(time.Hour)}},
	}
	path, _ := codexCachePath()
	if err := writeJSONCache(path, cache); err != nil {
		t.Fatal(err)
	}
	m := model{width: 80, height: 24}
	m.reload()
	if len(m.quotas) != 1 || m.quotas[0].Provider != "OpenAI" || !strings.Contains(m.View(), "Codex Plus") {
		t.Fatalf("Codex-only model = %#v\n%s", m.quotas, m.View())
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil || !updated.(model).codexCollecting {
		t.Fatal("r did not schedule a non-blocking Codex refresh")
	}
	updated, cmd = m.Update(providerTickMsg(time.Now()))
	if cmd == nil || updated.(model).codexCollecting {
		t.Fatal("five-minute tick did not reuse the fresh Codex cache")
	}
}
