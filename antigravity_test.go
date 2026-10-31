package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func antigravityFixture(now time.Time) string {
	return fmt.Sprintf(`{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[
	{"name":"Gemini Models","buckets":[{"id":"gemini-weekly","window":"weekly","remaining_fraction":0.86,"reset_time":%q,"description":"Weekly limit"},{"id":"gemini-5h","window":"5h","remaining_fraction":0,"reset_time":%q},{"id":"unknown","window":"weekly"},{"id":"disabled","disabled":true,"remaining_fraction":1}]},
	{"name":"Claude and GPT models","buckets":[{"id":"claude-weekly","window":"weekly","remaining_fraction":1}]},
	{"name":"Gemini 4 Argon","buckets":[{"id":"argon-weekly","window":"weekly","remaining_fraction":0.5}]}
	]}}}`, now.Add(6*24*time.Hour).UTC().Format(time.RFC3339), now.Add(2*time.Hour).UTC().Format(time.RFC3339))
}

func TestAntigravityUsageReportPreservesMeasuredGroupsAndResets(t *testing.T) {
	now := time.Now()
	quotas, err := parseAntigravityUsage([]byte(antigravityFixture(now)), now)
	if err != nil || len(quotas) != 4 {
		t.Fatalf("%#v %v", quotas, err)
	}
	if quotas[0].Window != "Gemini · Weekly" || quotas[0].Remaining != 86 || quotas[0].DurationMins != 10080 || quotas[0].ResetsAt == nil {
		t.Fatal(quotas[0])
	}
	if quotas[1].Remaining != 0 || quotas[1].DurationMins != 300 {
		t.Fatal("explicit exhausted bucket lost")
	}
	if quotas[2].Window != "Claude + GPT · Weekly" || quotas[2].Remaining != 100 || quotas[2].ResetsAt != nil {
		t.Fatal("invented or lost reset")
	}
	if quotas[3].Window != "Gemini 4 Argon · Weekly" {
		t.Fatal("dynamic model group lost")
	}
	if strings.Contains(compactAntigravityUsage(antigravityCacheFile{Quotas: quotas}), "unknown") {
		t.Fatal("unknown quota shown as measured")
	}
}

func TestAntigravityRejectsUnmeasuredAndMalformedReports(t *testing.T) {
	now := time.Now()
	good := antigravityFixture(now)
	for name, data := range map[string]string{
		"plain model response": `{"status":"SUCCESS","response":"100 percent remaining"}`,
		"failure":              strings.Replace(good, `"SUCCESS"`, `"ERROR"`, 1),
		"not usage":            strings.Replace(good, `"name":"usage"`, `"name":"model"`, 1),
		"fraction":             strings.Replace(good, `"remaining_fraction":0.86`, `"remaining_fraction":1.01`, 1),
		"negative":             strings.Replace(good, `"remaining_fraction":0.86`, `"remaining_fraction":-0.1`, 1),
		"fraction string":      strings.Replace(good, `"remaining_fraction":0.86`, `"remaining_fraction":"secret"`, 1),
		"reset":                strings.Replace(good, now.Add(6*24*time.Hour).UTC().Format(time.RFC3339), "secret", 1),
		"duplicate":            strings.Replace(good, `"id":"gemini-5h"`, `"id":"gemini-weekly"`, 1),
		"unsafe label":         strings.Replace(good, `"Gemini Models"`, `"Gemini\u001bModels"`, 1),
		"missing percentages":  `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Gemini","buckets":[{"id":"a"}]}]}}}`,
		"empty":                `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[]}}}`,
		"trailing JSON":        good + `{}`,
		"oversize":             strings.Repeat(" ", maxInputSize+1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseAntigravityUsage([]byte(data), now)
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe acceptance/error: %v", err)
			}
		})
	}
}

func TestAntigravityPreservesCacheAndCancellationDoesNotWrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setTestCacheHome(t, t.TempDir())
	now := time.Now()
	fetch := func(context.Context) ([]byte, error) { return []byte(antigravityFixture(now)), nil }
	first, err := collectAntigravityWith(context.Background(), fetch, now)
	if err != nil {
		t.Fatal(err)
	}
	if !antigravityCacheFresh(now) {
		t.Fatal("successful cache not fresh")
	}
	bad := func(context.Context) ([]byte, error) { return []byte(`{"status":"ERROR"}`), nil }
	second, err := collectAntigravityWith(context.Background(), bad, now.Add(time.Second))
	if err == nil || second.Failure == "" || !second.UpdatedAt.Equal(first.UpdatedAt) || len(second.Quotas) != 4 || second.Quotas[0].Remaining != 86 {
		t.Fatalf("preservation: %#v %v", second, err)
	}
	m := model{}
	m.reloadAt(now.Add(time.Second))
	if len(m.quotas) != 4 || !m.quotas[0].Stale || m.quotas[0].Failure == "" {
		t.Fatal("failed refresh not shown stale")
	}
	path, _ := antigravityCachePath()
	before, _ := os.ReadFile(path)
	ctx, cancel := context.WithCancel(context.Background())
	canceled := func(context.Context) ([]byte, error) { cancel(); return nil, ctx.Err() }
	if _, err := collectAntigravityWith(ctx, canceled, now.Add(2*time.Second)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("cancellation mutated cache")
	}
	if _, err := collectAntigravityWith(context.Background(), fetch, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("obsolete collection overwrote newer cache")
	}
	info, _ := os.Stat(path)
	if antigravityProcessSupported() && info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}

func TestAntigravityRefreshSchedulingSnapshotAndTerminal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setTestCacheHome(t, t.TempDir())
	now := time.Now()
	fetch := func(context.Context) ([]byte, error) { return []byte(antigravityFixture(now)), nil }
	if _, err := collectAntigravityWith(context.Background(), fetch, now); err != nil {
		t.Fatal(err)
	}
	m := newDashboardModel()
	defer m.collectors.stop()
	m.reloadAt(now)
	if m.antigravityCollecting {
		t.Fatal("startup ignored fresh cache")
	}
	if dashboardRefreshProviders("auto", now, m.quotas)[4] {
		t.Fatal("auto ignored fresh cache")
	}
	past := now.Add(-2 * resetRefreshDelay)
	if !dashboardRefreshProviders("auto", now, []quota{{Provider: "Antigravity", ResetAt: &past, AttemptedAt: now.Add(-time.Minute)}})[4] {
		t.Fatal("reset did not trigger refresh")
	}
	m.resetRefreshScheduled = map[string]time.Time{"Antigravity": now}
	updated, cmd := m.Update(resetRefreshMsg{provider: "Antigravity", deadline: now})
	if cmd == nil || !updated.(model).antigravityCollecting {
		t.Fatal("reset timer did not schedule collector")
	}
	snapshot := newDashboardSnapshot(m, now)
	if len(snapshot.Quotas) != 4 || snapshot.Quotas[0].Provider != "Antigravity" || snapshot.Quotas[0].ElapsedPercent == nil {
		t.Fatal(snapshot)
	}
	m.claudeCollecting, m.codexCollecting, m.kimiCollecting, m.zaiCollecting = false, false, false, false
	m.width, m.height = 100, 24
	if view := m.View(); !strings.Contains(view, "Gemini 4 Argon · Weekly") || !strings.Contains(view, "Claude + GPT · Weekly") {
		t.Fatal(view)
	}
	for _, size := range [][2]int{{80, 24}, {60, 15}, {35, 12}, {18, 5}, {1, 1}} {
		m.width, m.height = size[0], size[1]
		if view := m.View(); lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height {
			t.Fatalf("overflow at %v: %q", size, view)
		}
	}
	if _, ok := parseCommand([]string{"collect-antigravity"}); !ok {
		t.Fatal("missing command")
	}
}

func TestAntigravityVersionGate(t *testing.T) {
	for _, v := range []string{"1.1.11", "1.4.12\n", "agy 1.2.0", "agy version v1.4.12", "2.0.0"} {
		if !supportsAntigravityUsage(v) {
			t.Fatal(v)
		}
	}
	for _, v := range []string{"", "1.1.10", "0.9.99", "garbage 1.4.12", "1.4", "1.4.12 trailing"} {
		if supportsAntigravityUsage(v) {
			t.Fatal(v)
		}
	}
}

func fakeAntigravity(t *testing.T, body string) string {
	t.Helper()
	if !antigravityProcessSupported() {
		t.Skip("Unix process integration")
	}
	path := filepath.Join(t.TempDir(), "agy")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAntigravityCommandGuardsAndReadOnlyReport(t *testing.T) {
	now := time.Now()
	executable := fakeAntigravity(t, `test "$AGY_CLI_DISABLE_AUTO_UPDATE" = true || exit 1
case "$PWD" in */aiusage-antigravity-*) ;; *) exit 2;; esac
test "$BROWSER" -ef "$PWD/open" || exit 3
if open https://example.invalid; then exit 4; fi
if xdg-open https://example.invalid; then exit 5; fi
if test "$1" = --version; then printf '1.4.12\n'; exit; fi
test "$1" = -p && test "$2" = /usage && test "$3" = --output-format && test "$4" = json || exit 6
test "$5" = --print-timeout && test "$6" = 10s || exit 7
test "$7" = --log-file && test "$(cd "$(dirname "$8")" && pwd -P)" = "$(pwd -P)" || exit 8
test "$9" = --settings || exit 9
printf '%s\n' '`+antigravityFixture(now)+`'
`)
	t.Setenv("AGY_CLI_DISABLE_AUTO_UPDATE", "false")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	data, err := fetchAntigravityExecutable(ctx, executable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAntigravityUsage(data, now); err != nil {
		t.Fatal(err)
	}
}

func TestAntigravityOldVersionsNeverReceivePrompt(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "prompted")
	executable := fakeAntigravity(t, `if test "$1" = --version; then printf '1.1.10\n'; exit; fi
printf 'bad' > '`+marker+`'
`)
	if _, err := fetchAntigravityExecutable(context.Background(), executable); err == nil {
		t.Fatal("old version accepted")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old CLI received prompt")
	}
}

func TestAntigravityOutputBoundAndFailureSanitization(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`if test "$1" = --version; then printf '1.4.12\n'; exit; fi
head -c 1100000 /dev/zero
`, "size limit"},
		{`if test "$1" = --version; then printf '1.4.12\n'; exit; fi
printf 'secret-login-url' >&2
exit 2
`, "usage check failed"},
	} {
		exe := fakeAntigravity(t, tc.body)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := fetchAntigravityExecutable(ctx, exe)
		cancel()
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("%v", err)
		}
	}
}

func TestAntigravityCancellationKillsOwnedProcessGroup(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	exe := fakeAntigravity(t, `if test "$1" = --version; then printf '1.4.12\n'; exit; fi
sleep 3600 &
echo $! > '`+pidPath+`'
wait
`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := fetchAntigravityExecutable(ctx, exe); done <- err }()
	var data []byte
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var err error
		data, err = os.ReadFile(pidPath)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation succeeded unexpectedly")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not bound check")
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		t.Fatal(err)
	}
	assertTestProcessExited(t, pid)
}

func TestAntigravityExplicitPathIsAuthoritative(t *testing.T) {
	t.Setenv("ANTIGRAVITY_CLI_PATH", "")
	if _, err := antigravityExecutable(); err == nil {
		t.Fatal("empty override fell back")
	}
	t.Setenv("ANTIGRAVITY_CLI_PATH", "relative/agy")
	if _, err := antigravityExecutable(); err == nil {
		t.Fatal("relative override accepted")
	}
}

func TestAntigravityCacheRoundTrip(t *testing.T) {
	now := time.Now()
	quotas, err := parseAntigravityUsage([]byte(antigravityFixture(now)), now)
	if err != nil {
		t.Fatal(err)
	}
	cache := antigravityCacheFile{Version: cacheVersion, Provider: "Antigravity", UpdatedAt: now, Quotas: quotas}
	data, _ := json.Marshal(cache)
	var decoded antigravityCacheFile
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := validateAntigravityCache(decoded, now); err != nil {
		t.Fatal(err)
	}
	decoded.Quotas[0].Remaining = 101
	if validateAntigravityCache(decoded, now) == nil {
		t.Fatal("invalid cached quota accepted")
	}
}
