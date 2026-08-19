package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestParseKimiUsageNormalizesSummaryAndLimits(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	input := fmt.Sprintf(`{
		"code":0,
		"data":{"kind":"ok","summary":{"window":{"duration":5,"unit":"hour"},"used":25,"limit":100,"reset_at":%q,"ignored":true},
		"limits":[{"window":{"duration":7,"unit":"days"},"used":1,"limit":4,"reset_at":%q}],"extra_usage":{"ignored":true}},
		"unknown":"ignored"
	}`, now.Add(4*time.Hour).Format(time.RFC3339), now.Add(6*24*time.Hour).Format(time.RFC3339))

	got, err := parseKimiUsage([]byte(input), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Window != "5-hour" || got[0].RemainingPercentage != 75 || got[0].WindowDuration != 5 || got[0].WindowUnit != "hour" {
		t.Fatalf("summary = %#v", got)
	}
	if got[1].Window != "Weekly" || got[1].RemainingPercentage != 75 || got[1].Used != 1 || got[1].Limit != 4 {
		t.Fatalf("limit = %#v", got[1])
	}
}

func TestParseKimiUsageRejectsNonOKAndInvalidWindows(t *testing.T) {
	now := time.Now()
	reset := now.Add(time.Hour).Format(time.RFC3339)
	tests := map[string]string{
		"code":                 `{"code":1,"data":{"kind":"ok"}}`,
		"kind":                 `{"code":0,"data":{"kind":"error"}}`,
		"no windows":           `{"code":0,"data":{"kind":"ok"}}`,
		"duration":             fmt.Sprintf(`{"code":0,"data":{"kind":"ok","summary":{"window":{"duration":0,"unit":"hour"},"used":1,"limit":2,"reset_at":%q}}}`, reset),
		"long window":          fmt.Sprintf(`{"code":0,"data":{"kind":"ok","summary":{"window":{"duration":13,"unit":"month"},"used":1,"limit":2,"reset_at":%q}}}`, reset),
		"unit":                 fmt.Sprintf(`{"code":0,"data":{"kind":"ok","summary":{"window":{"duration":5,"unit":"fortnight"},"used":1,"limit":2,"reset_at":%q}}}`, reset),
		"used":                 fmt.Sprintf(`{"code":0,"data":{"kind":"ok","summary":{"window":{"duration":5,"unit":"hour"},"used":3,"limit":2,"reset_at":%q}}}`, reset),
		"limit":                fmt.Sprintf(`{"code":0,"data":{"kind":"ok","summary":{"window":{"duration":5,"unit":"hour"},"used":0,"limit":0,"reset_at":%q}}}`, reset),
		"reset":                `{"code":0,"data":{"kind":"ok","summary":{"window":{"duration":5,"unit":"hour"},"used":1,"limit":2,"reset_at":"not-a-date"}}}`,
		"duplicate":            fmt.Sprintf(`{"code":0,"data":{"kind":"ok","summary":{"window":{"duration":5,"unit":"hour"},"used":1,"limit":2,"reset_at":%q},"limits":[{"window":{"duration":5,"unit":"hours"},"used":1,"limit":2,"reset_at":%q}]}}`, reset, reset),
		"duplicate normalized": fmt.Sprintf(`{"code":0,"data":{"kind":"ok","summary":{"window":{"duration":7,"unit":"day"},"used":1,"limit":2,"reset_at":%q},"limits":[{"window":{"duration":1,"unit":"week"},"used":1,"limit":2,"reset_at":%q}]}}`, reset, reset),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseKimiUsage([]byte(input), now); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateKimiCacheRejectsDuplicateNormalizedLabels(t *testing.T) {
	now := time.Now()
	reset := now.Add(time.Hour)
	cache := kimiCacheFile{
		Version: cacheVersion, Provider: "Kimi Code", UpdatedAt: now,
		Quotas: []kimiCachedQuota{
			{Window: "Weekly", WindowDuration: 7, WindowUnit: "day", Used: 1, Limit: 2, RemainingPercentage: 50, ResetsAt: reset},
			{Window: "Weekly", WindowDuration: 1, WindowUnit: "week", Used: 1, Limit: 2, RemainingPercentage: 50, ResetsAt: reset},
		},
	}
	if err := validateKimiCache(cache, now); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate normalized label error = %v", err)
	}
}

func TestFetchKimiWaitsForChildOwnedPortAndUsesLoopbackToken(t *testing.T) {
	if !kimiProcessGroupsSupported() {
		t.Skip("Unix process-group integration test")
	}
	token := "fake-test-token"
	tokenPath := writeTestKimiToken(t, token, 0o600)
	requestMarker := t.TempDir() + "/request"
	factory := kimiTestServerFactory("valid", token, requestMarker, 150*time.Millisecond)

	started := time.Now()
	quotas, err := fetchKimi(context.Background(), factory, tokenPath, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 100*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("readiness/termination timing = %v", elapsed)
	}
	if _, err := os.Stat(requestMarker); err != nil || len(quotas) != 1 || quotas[0].RemainingPercentage != 50 {
		t.Fatalf("request/quotas = %v %#v", err, quotas)
	}

	cmd := newKimiCommand(context.Background())
	want := []string{"kimi", "web", "--no-open", "--port", "0", "--log-level", "silent"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("Kimi command = %#v, want %#v", cmd.Args, want)
	}
}

func TestFetchKimiCleanupIsBoundedAndKillsDescendants(t *testing.T) {
	if !kimiProcessGroupsSupported() {
		t.Skip("Unix process-group integration test")
	}
	dir := t.TempDir()
	pidFile := dir + "/sleep.pid"
	factory := func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKimiServerHelper$")
		cmd.Env = append(os.Environ(), "GO_WANT_KIMI_SERVER_HELPER=1", "KIMI_TEST_OUTPUT=hang", "KIMI_TEST_PID_FILE="+pidFile, "KIMI_TEST_DELAY=0s")
		return cmd
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := fetchKimi(ctx, factory, "unused", time.Now())
		result <- err
	}()
	var pid int
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		pidText, err := os.ReadFile(pidFile)
		if err == nil {
			pid, err = strconv.Atoi(strings.TrimSpace(string(pidText)))
			if err == nil && pid > 0 {
				break
			}
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			t.Fatalf("wait for Kimi descendant: %v", err)
		}
	}
	canceledAt := time.Now()
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled Kimi collection returned no error")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Kimi cleanup exceeded bound after %v", time.Since(canceledAt))
	}
	assertTestProcessExited(t, pid)
}

func TestFetchKimiNeverSendsTokenWithoutTrustedChildOutput(t *testing.T) {
	if !kimiProcessGroupsSupported() {
		t.Skip("Unix process-group integration test")
	}
	for _, output := range []string{"malformed", "non-loopback", "inexact-readiness"} {
		t.Run(output, func(t *testing.T) {
			token := "fake-test-token"
			tokenPath := writeTestKimiToken(t, token, 0o600)
			requestMarker := t.TempDir() + "/request"
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()

			_, err := fetchKimi(ctx, kimiTestServerFactory(output, token, requestMarker, 0), tokenPath, time.Now())
			if err == nil || !strings.Contains(err.Error(), "readiness") {
				t.Fatalf("error = %v", err)
			}
			if _, err := os.Stat(requestMarker); !os.IsNotExist(err) {
				t.Fatalf("untrusted Local output caused a token request: %v", err)
			}
		})
	}
}

func TestKimiResponseHeadersAreBounded(t *testing.T) {
	client, server := net.Pipe()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer server.Close()
		_, _ = fmt.Fprintf(server, "HTTP/1.1 200 OK\r\nX-Oversized: %s\r\n\r\n{}", strings.Repeat("x", maxKimiResponseHeaderSize))
	}()
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/api/v1/oauth/usage", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readKimiHTTPResponse(client, request)
	_ = client.Close()
	<-writerDone
	if err == nil || !strings.Contains(err.Error(), "headers exceeded size limit") {
		t.Fatalf("oversized header error = %v", err)
	}
}

func TestFetchKimiDoesNotRedialReplacementListener(t *testing.T) {
	if !kimiProcessGroupsSupported() {
		t.Skip("Unix process-group integration test")
	}
	token := "must-not-reach-replacement"
	tokenPath := writeTestKimiToken(t, token, 0o600)
	portFile := t.TempDir() + "/port"
	factory := func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKimiServerHelper$")
		cmd.Env = append(os.Environ(), "GO_WANT_KIMI_SERVER_HELPER=1", "KIMI_TEST_OUTPUT=handoff", "KIMI_TEST_PORT_FILE="+portFile, "KIMI_TEST_DELAY=0s")
		return cmd
	}

	type replacementResult struct {
		bound         bool
		authorization string
	}
	result := make(chan replacementResult, 1)
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			data, err := os.ReadFile(portFile)
			if err != nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			listener, err := net.Listen("tcp4", "127.0.0.1:"+string(data))
			if err != nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			tcp := listener.(*net.TCPListener)
			_ = tcp.SetDeadline(time.Now().Add(500 * time.Millisecond))
			conn, err := tcp.Accept()
			if err == nil {
				_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
				request, _ := http.ReadRequest(bufio.NewReader(conn))
				replacement := replacementResult{bound: true}
				if request != nil {
					replacement.authorization = request.Header.Get("Authorization")
				}
				result <- replacement
				_ = conn.Close()
			} else {
				result <- replacementResult{bound: true}
			}
			_ = listener.Close()
			return
		}
		result <- replacementResult{}
	}()

	_, err := fetchKimi(context.Background(), factory, tokenPath, time.Now())
	if err == nil {
		t.Fatal("expected relinquished child connection to fail")
	}
	replacement := <-result
	if !replacement.bound {
		t.Fatal("replacement listener never acquired the relinquished port")
	}
	if replacement.authorization != "" {
		t.Fatalf("replacement listener received Authorization %q", replacement.authorization)
	}
}

func kimiTestServerFactory(output, token, requestMarker string, delay time.Duration) kimiCommandFactory {
	return func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKimiServerHelper$")
		cmd.Env = append(os.Environ(),
			"GO_WANT_KIMI_SERVER_HELPER=1",
			"KIMI_TEST_OUTPUT="+output,
			"KIMI_TEST_TOKEN="+token,
			"KIMI_TEST_REQUEST_MARKER="+requestMarker,
			"KIMI_TEST_DELAY="+delay.String(),
		)
		return cmd
	}
}

func TestKimiServerHelper(t *testing.T) {
	if os.Getenv("GO_WANT_KIMI_SERVER_HELPER") != "1" {
		return
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	delay, err := time.ParseDuration(os.Getenv("KIMI_TEST_DELAY"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(delay)
	if os.Getenv("KIMI_TEST_OUTPUT") == "hang" {
		child := exec.Command("sleep", "3600")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("KIMI_TEST_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			_ = child.Process.Kill()
			t.Fatal(err)
		}
		time.Sleep(time.Hour)
		return
	}
	if os.Getenv("KIMI_TEST_OUTPUT") == "inexact-readiness" {
		fmt.Println("Kimi server is ready")
	} else {
		fmt.Println("  ▐█▛█▛█▌  Kimi server ready  0.37.0")
	}
	switch os.Getenv("KIMI_TEST_OUTPUT") {
	case "valid", "inexact-readiness", "handoff":
		fmt.Printf("Local:    http://127.0.0.1:%d/#token=never-retain-this\n", port)
	case "malformed":
		fmt.Println("Local:    http://127.0.0.1:not-a-port/#token=never-retain-this")
	case "non-loopback":
		fmt.Printf("Local:    http://127.0.0.2:%d/#token=never-retain-this\n", port)
	default:
		t.Fatal("unknown helper output")
	}
	if os.Getenv("KIMI_TEST_OUTPUT") == "handoff" {
		if err := os.WriteFile(os.Getenv("KIMI_TEST_PORT_FILE"), []byte(strconv.Itoa(port)), 0o600); err != nil {
			t.Fatal(err)
		}
		conn, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		_ = listener.Close()
		_ = conn.Close()
		return
	}
	if err := http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := os.WriteFile(os.Getenv("KIMI_TEST_REQUEST_MARKER"), nil, 0o600); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/api/v1/oauth/usage" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+os.Getenv("KIMI_TEST_TOKEN") {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		reset := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		fmt.Fprintf(w, `{"code":0,"data":{"kind":"ok","summary":{"window":{"duration":5,"unit":"hour"},"used":1,"limit":2,"reset_at":%q}}}`, reset)
	})); err != nil {
		t.Fatal(err)
	}
}

func TestKimiTokenRequiresPrivateRegularFile(t *testing.T) {
	if token, err := readKimiToken(writeTestKimiToken(t, "safe-token\n", 0o600)); err != nil || token != "safe-token" {
		t.Fatalf("private token = %q, %v", token, err)
	}
	if _, err := readKimiToken(writeTestKimiToken(t, "unsafe-token", 0o644)); err == nil {
		t.Fatal("accepted group/world-readable token")
	}
	dir := t.TempDir()
	target := dir + "/target"
	if err := os.WriteFile(target, []byte("symlink-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := dir + "/server.token"
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readKimiToken(link); err == nil {
		t.Fatal("accepted symlink token")
	}
}

func TestKimiFailurePreservesPrivateProviderCache(t *testing.T) {
	if !kimiProcessGroupsSupported() {
		t.Skip("Unix process-group integration test")
	}
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	t.Setenv("XDG_CACHE_HOME", temp)
	t.Setenv("KIMI_CODE_HOME", temp+"/kimi-home")
	if err := os.MkdirAll(temp+"/kimi-home", 0o700); err != nil {
		t.Fatal(err)
	}
	writePathToken(t, temp+"/kimi-home/server.token", "fake-token", 0o600)
	now := time.Now()
	previous := kimiCacheFile{
		Version: cacheVersion, Provider: "Kimi Code", UpdatedAt: now.Add(-time.Minute),
		Quotas: []kimiCachedQuota{{Window: "5-hour", WindowDuration: 5, WindowUnit: "hour", Used: 1, Limit: 2, RemainingPercentage: 50, ResetsAt: now.Add(time.Hour)}},
	}
	path, _ := kimiCachePath()
	if err := writeJSONCache(path, previous); err != nil {
		t.Fatal(err)
	}
	factory := func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, t.TempDir()+"/missing-kimi")
	}
	if _, err := collectKimiWith(context.Background(), factory, now); err == nil {
		t.Fatal("expected collection failure")
	}
	got, err := readKimiCache()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Quotas) != 1 || got.Quotas[0].RemainingPercentage != 50 || !got.UpdatedAt.Equal(previous.UpdatedAt) || got.Failure == "" {
		t.Fatalf("preserved cache = %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("Kimi cache permissions = %v, %v", info, err)
	}
}

func TestCanceledKimiCollectionDoesNotMutateCache(t *testing.T) {
	if !kimiProcessGroupsSupported() {
		t.Skip("Unix process-group integration test")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	path, _ := kimiCachePath()
	previous := kimiCacheFile{
		Version: cacheVersion, Provider: "Kimi Code", UpdatedAt: now.Add(-time.Minute), AttemptedAt: now.Add(-time.Minute),
		Quotas: []kimiCachedQuota{{Window: "5-hour", WindowDuration: 5, WindowUnit: "hour", Used: 1, Limit: 2, RemainingPercentage: 50, ResetsAt: now.Add(time.Hour)}},
	}
	if err := writeJSONCache(path, previous); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	factoryCalled := false
	factory := func(ctx context.Context) *exec.Cmd {
		factoryCalled = true
		return exec.CommandContext(ctx, t.TempDir()+"/must-not-start")
	}
	if _, err := collectKimiWith(ctx, factory, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled collection error = %v", err)
	}
	if factoryCalled {
		t.Fatal("canceled Kimi collection started a process")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("canceled Kimi collection mutated cache")
	}
}

func TestObsoleteKimiFailureCannotRegressNewerCache(t *testing.T) {
	if !kimiProcessGroupsSupported() {
		t.Skip("Unix process-group integration test")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("KIMI_CODE_HOME", t.TempDir())
	now := time.Now()
	newer := now.Add(time.Minute)
	reset := now.Add(time.Hour)
	path, _ := kimiCachePath()
	cache := kimiCacheFile{Version: cacheVersion, Provider: "Kimi Code", UpdatedAt: newer, AttemptedAt: newer, Quotas: []kimiCachedQuota{{Window: "5-hour", WindowDuration: 5, WindowUnit: "hour", Used: 1, Limit: 2, RemainingPercentage: 50, ResetsAt: reset}}}
	if err := writeJSONCache(path, cache); err != nil {
		t.Fatal(err)
	}
	factory := func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, t.TempDir()+"/missing-kimi") }
	got, err := collectKimiWith(context.Background(), factory, now)
	if err != nil || !got.AttemptedAt.Equal(newer) || got.Failure != "" || got.Quotas[0].RemainingPercentage != 50 {
		t.Fatalf("obsolete Kimi result = %#v, %v", got, err)
	}
}

func TestTUIRendersKimiCacheAndSchedulesRefresh(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	t.Setenv("XDG_CACHE_HOME", temp)
	now := time.Now()
	cache := kimiCacheFile{
		Version: cacheVersion, Provider: "Kimi Code", UpdatedAt: now,
		Quotas: []kimiCachedQuota{{Window: "5-hour", WindowDuration: 5, WindowUnit: "hour", Used: 1, Limit: 4, RemainingPercentage: 75, ResetsAt: now.Add(time.Hour)}},
	}
	path, _ := kimiCachePath()
	if err := writeJSONCache(path, cache); err != nil {
		t.Fatal(err)
	}
	m := model{width: 80, height: 24}
	m.reload()
	if len(m.quotas) != 1 || m.quotas[0].Provider != "Kimi" || !strings.Contains(m.View(), "Kimi Code") {
		t.Fatalf("Kimi-only model = %#v\n%s", m.quotas, m.View())
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil || !updated.(model).claudeCollecting || !updated.(model).kimiCollecting || !updated.(model).codexCollecting {
		t.Fatal("r did not schedule the default asynchronous collectors")
	}
	updated, cmd = model{}.Update(providerTickMsg(time.Now()))
	if cmd == nil || !updated.(model).claudeCollecting || updated.(model).kimiCollecting || !updated.(model).codexCollecting {
		t.Fatal("five-minute tick did not reuse Kimi's fresh cache independently")
	}
}

func writeTestKimiToken(t *testing.T, token string, mode os.FileMode) string {
	t.Helper()
	path := t.TempDir() + "/server.token"
	writePathToken(t, path, token, mode)
	return path
}

func writePathToken(t *testing.T, path, token string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(token), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestKimiResponseSizeBound(t *testing.T) {
	if maxKimiResponseSize <= 0 || maxKimiResponseSize >= maxInputSize {
		t.Fatalf("unexpected Kimi response bound: %s", strconv.Itoa(maxKimiResponseSize))
	}
}
