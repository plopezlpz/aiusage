package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestParseCommandRejectsUnknownAndTrailingArguments(t *testing.T) {
	valid := [][]string{
		nil, {"--demo"}, {"--claude-oauth"}, {"ingest-claude-code"}, {"collect-claude"}, {"collect-codex"}, {"collect-kimi"},
		{"dashboard-json"}, {"dashboard-json", "--refresh=auto"}, {"dashboard-json", "--refresh=force"},
	}
	for _, args := range valid {
		if _, ok := parseCommand(args); !ok {
			t.Fatalf("valid arguments rejected: %#v", args)
		}
	}
	for _, args := range [][]string{
		{"unknown"}, {"--demo", "extra"}, {"collect-codex", "extra"},
		{"dashboard-json", "--refresh"}, {"dashboard-json", "--refresh=stale"}, {"dashboard-json", "--refresh=auto", "extra"}, {"--refresh=force"},
	} {
		if _, ok := parseCommand(args); ok {
			t.Fatalf("invalid arguments accepted: %#v", args)
		}
	}
}

func TestCollectorRuntimeCancelsJoinsAndRejectsLateCommands(t *testing.T) {
	runtime := newCollectorRuntime()
	started := make(chan struct{}, 3)
	finished := make(chan struct{}, 3)
	results := make(chan tea.Msg, 3)
	for range 3 {
		command := runtime.command(func(ctx context.Context) tea.Msg {
			started <- struct{}{}
			<-ctx.Done()
			finished <- struct{}{}
			return ctx.Err()
		})
		go func() { results <- command() }()
	}
	for range 3 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("collector did not start")
		}
	}
	runtime.stop()
	if len(finished) != 3 {
		t.Fatalf("runtime returned before collectors finished: finished=%d", len(finished))
	}
	for range 3 {
		select {
		case <-results:
		case <-time.After(time.Second):
			t.Fatal("collector command did not return")
		}
	}
	lateRan := false
	if msg := runtime.command(func(context.Context) tea.Msg { lateRan = true; return "unexpected" })(); msg != nil || lateRan {
		t.Fatalf("command started after runtime stop: message=%#v ran=%v", msg, lateRan)
	}
}

func TestParentCollectionCanceledDistinguishesShutdownFromDeadline(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if !parentCollectionCanceled(canceled) {
		t.Fatal("shutdown cancellation was not recognized")
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if parentCollectionCanceled(deadline) {
		t.Fatal("deadline timeout was mistaken for shutdown cancellation")
	}
}

func TestCollectorRuntimeStartStopRaceAndNilModelPath(t *testing.T) {
	for range 100 {
		runtime := newCollectorRuntime()
		start := make(chan struct{})
		done := make(chan struct{}, 16)
		for range 16 {
			command := runtime.command(func(ctx context.Context) tea.Msg {
				<-ctx.Done()
				return nil
			})
			go func() {
				<-start
				_ = command()
				done <- struct{}{}
			}()
		}
		close(start)
		runtime.stop()
		for range 16 {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("collector start/stop race did not finish")
			}
		}
	}

	m := model{}
	commands := m.startCollectors(true)
	if m.collectors == nil || len(commands) != 3 {
		t.Fatalf("nil-runtime model did not initialize collectors safely: %#v", m)
	}
	m.collectors.stop()
	for _, command := range commands {
		if message := command(); message != nil {
			t.Fatalf("stopped nil-path runtime started a collector: %#v", message)
		}
	}
}

func TestParseStatusLineNormalizesUsedToRemaining(t *testing.T) {
	input := `{
		"model":{"display_name":"Claude"},
		"rate_limits":{
			"five_hour":{"used_percentage":24.5,"resets_at":"2030-01-02T03:04:05Z"},
			"seven_day":{"used_percentage":63}
		}
	}`

	got, err := parseStatusLine(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d quotas, want 2", len(got))
	}
	if got[0].Window != "5-hour session" || got[0].RemainingPercentage != 75.5 {
		t.Fatalf("five-hour quota = %#v", got[0])
	}
	wantReset := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	if got[0].ResetsAt == nil || !got[0].ResetsAt.Equal(wantReset) {
		t.Fatalf("reset = %v, want %v", got[0].ResetsAt, wantReset)
	}
	if got[1].Window != "Weekly · all" || got[1].RemainingPercentage != 37 || got[1].ResetsAt != nil {
		t.Fatalf("seven-day quota = %#v", got[1])
	}
	if text := compactUsage(got); text != "Claude 5h 76% left · 7d 37% left" {
		t.Fatalf("compact usage = %q", text)
	}
}

func TestParseStatusLineEnforcesInputLimitAndSingleValue(t *testing.T) {
	exact := append([]byte(`{}`), bytes.Repeat([]byte(" "), maxInputSize-2)...)
	if _, err := parseStatusLine(bytes.NewReader(exact)); err != nil {
		t.Fatalf("exact limit: %v", err)
	}
	if _, err := parseStatusLine(bytes.NewReader(append(exact, ' '))); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("over limit error = %v", err)
	}
	if _, err := parseStatusLine(strings.NewReader(`{} {}`)); err == nil || !strings.Contains(err.Error(), "multiple values") {
		t.Fatalf("multiple values error = %v", err)
	}
}

func TestIngestPreservesQuotasWhenRateLimitsAreMissing(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	t.Setenv("XDG_CACHE_HOME", temp)
	path, err := cachePath()
	if err != nil {
		t.Fatal(err)
	}
	previous := cacheFile{
		Version:   cacheVersion,
		Provider:  "Claude",
		UpdatedAt: time.Now().Add(-time.Minute),
		Quotas:    []cachedQuota{{Window: "5-hour session", RemainingPercentage: 42.5}},
	}
	if err := writeJSONCache(path, previous); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := ingest(strings.NewReader(`{"rate_limits":{}}`), &output); err != nil {
		t.Fatal(err)
	}
	got, err := readCache()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Quotas) != 1 || got.Quotas[0].RemainingPercentage != 42.5 || !got.UpdatedAt.Equal(previous.UpdatedAt) {
		t.Fatalf("preserved cache = %#v", got)
	}
	if got.Failure != "rate limits unavailable; send one prompt in Claude Code" || got.AttemptedAt.IsZero() {
		t.Fatalf("attempt state = %#v", got)
	}
	if output.String() != "Claude usage unavailable\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestObsoleteStatusLineIngestDoesNotRegressNewerCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	newer := time.Now().Add(time.Minute)
	path, err := cachePath()
	if err != nil {
		t.Fatal(err)
	}
	cache := cacheFile{Version: cacheVersion, Provider: "Claude", UpdatedAt: newer, AttemptedAt: newer, Quotas: []cachedQuota{{Window: "5-hour session", RemainingPercentage: 88}}}
	if err := writeJSONCache(path, cache); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := ingest(strings.NewReader(`{"rate_limits":{"five_hour":{"used_percentage":99}}}`), &output); err != nil {
		t.Fatal(err)
	}
	persisted, err := readCache()
	if err != nil || persisted.Quotas[0].RemainingPercentage != 88 || !persisted.UpdatedAt.Equal(newer) {
		t.Fatalf("newer cache regressed: %#v, %v", persisted, err)
	}
	if output.String() != "Claude 5h 88% left\n" {
		t.Fatalf("obsolete ingest output = %q", output.String())
	}
}

func TestStatusLineIngestPreservesCollectedFableQuota(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	t.Setenv("XDG_CACHE_HOME", temp)
	now := time.Now()
	reset := now.Add(3 * 24 * time.Hour)
	previous := cacheFile{
		Version:          cacheVersion,
		Provider:         "Claude",
		UpdatedAt:        now.Add(-time.Minute),
		OAuthAttemptedAt: now.Add(-30 * time.Second),
		OAuthFailure:     "HTTP 429 · Claude OAuth usage",
		Quotas: []cachedQuota{{
			Window: "Weekly · Fable", RemainingPercentage: 67, ResetsAt: &reset,
			Source: claudeOAuthSource, CollectedAt: now.Add(-time.Minute),
		}},
	}
	path, err := cachePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONCache(path, previous); err != nil {
		t.Fatal(err)
	}
	input := fmt.Sprintf(`{"rate_limits":{"five_hour":{"used_percentage":10,"resets_at":%q}}}`, now.Add(time.Hour).Format(time.RFC3339))
	if err := ingest(strings.NewReader(input), io.Discard); err != nil {
		t.Fatal(err)
	}
	got, err := readCache()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Quotas) != 2 || got.Quotas[1].Window != "Weekly · Fable" || got.Quotas[1].RemainingPercentage != 67 || !got.Quotas[1].CollectedAt.Equal(previous.Quotas[0].CollectedAt) {
		t.Fatalf("Fable quota not preserved: %#v", got.Quotas)
	}
	if got.OAuthFailure != previous.OAuthFailure || !got.OAuthAttemptedAt.Equal(previous.OAuthAttemptedAt) {
		t.Fatalf("OAuth attempt state not preserved: %#v", got)
	}
	m := model{width: 140, height: 24}
	m.reload()
	if len(m.quotas) != 2 || m.quotas[0].Failure != "" || m.quotas[1].Failure != previous.OAuthFailure || !m.quotas[1].AttemptedAt.Equal(previous.OAuthAttemptedAt) {
		t.Fatalf("OAuth error was not scoped to Fable: %#v", m.quotas)
	}
	if view := m.View(); !strings.Contains(view, "refresh failed today") || !strings.Contains(view, "HTTP 429") {
		t.Fatalf("OAuth error time/status not rendered: %q", view)
	}
}

func TestStatusLineIngestMigratesLegacyOAuthFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	reset := now.Add(3 * 24 * time.Hour)
	path, _ := cachePath()
	if err := writeJSONCache(path, cacheFile{
		Version: cacheVersion, Provider: "Claude", UpdatedAt: now.Add(-time.Minute), AttemptedAt: now.Add(-30 * time.Second),
		Failure: "HTTP 429 · Claude OAuth usage",
		Quotas:  []cachedQuota{{Window: "Weekly · Fable", RemainingPercentage: 67, ResetsAt: &reset, Source: claudeOAuthSource, CollectedAt: now.Add(-time.Minute)}},
	}); err != nil {
		t.Fatal(err)
	}
	input := fmt.Sprintf(`{"rate_limits":{"five_hour":{"used_percentage":10,"resets_at":%q}}}`, now.Add(time.Hour).Format(time.RFC3339))
	if err := ingest(strings.NewReader(input), io.Discard); err != nil {
		t.Fatal(err)
	}
	cache, err := readCache()
	if err != nil {
		t.Fatal(err)
	}
	if cache.OAuthFailure != "HTTP 429 · Claude OAuth usage" || !cache.OAuthAttemptedAt.Equal(now.Add(-30*time.Second)) || cache.Failure != "" {
		t.Fatalf("legacy OAuth failure was not migrated: %#v", cache)
	}
}

func TestValidateCacheRejectsUntrustedValues(t *testing.T) {
	now := time.Now()
	valid := cacheFile{
		Version:   cacheVersion,
		Provider:  "Claude",
		UpdatedAt: now,
		Quotas:    []cachedQuota{{Window: "5-hour session", RemainingPercentage: 50}},
	}
	tests := map[string]func(*cacheFile){
		"percentage": func(c *cacheFile) { c.Quotas[0].RemainingPercentage = math.Inf(1) },
		"window":     func(c *cacheFile) { c.Quotas[0].Window = "monthly" },
		"duplicate": func(c *cacheFile) {
			c.Quotas = append(c.Quotas, c.Quotas[0])
		},
		"updated timestamp": func(c *cacheFile) { c.UpdatedAt = now.Add(time.Hour) },
		"reset timestamp": func(c *cacheFile) {
			reset := now.Add(9 * 24 * time.Hour)
			c.Quotas[0].ResetsAt = &reset
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Quotas = append([]cachedQuota(nil), valid.Quotas...)
			mutate(&candidate)
			if err := validateCache(candidate, now); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestProviderCacheReadsRejectOversizedFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	readers := []struct {
		path func() (string, error)
		read func() error
	}{
		{cachePath, func() error { _, err := readCache(); return err }},
		{codexCachePath, func() error { _, err := readCodexCache(); return err }},
		{kimiCachePath, func() error { _, err := readKimiCache(); return err }},
	}
	for _, test := range readers {
		path, err := test.path()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, maxCacheSize+1), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := test.read(); err == nil || !strings.Contains(err.Error(), "size limit") {
			t.Fatalf("oversized cache error = %v", err)
		}
	}
}

func TestPersistedFailuresMustAlreadyBeSanitized(t *testing.T) {
	now := time.Now()
	for name, failure := range map[string]string{
		"escape":     "bad\x1b[31m",
		"newline":    "bad\nnews",
		"whitespace": " bad ",
		"wide":       strings.Repeat("界", 129),
	} {
		t.Run(name, func(t *testing.T) {
			claude := cacheFile{Version: cacheVersion, Provider: "Claude", AttemptedAt: now, Failure: failure}
			codex := codexCacheFile{Version: cacheVersion, Provider: "OpenAI Codex", AttemptedAt: now, Failure: failure}
			kimi := kimiCacheFile{Version: cacheVersion, Provider: "Kimi Code", AttemptedAt: now, Failure: failure}
			if validateCache(claude, now) == nil || validateCodexCache(codex, now) == nil || validateKimiCache(kimi, now) == nil {
				t.Fatal("accepted unsanitized persisted failure")
			}
		})
	}
	clean := safeCollectionError(errors.New(" bad\x1b\n" + strings.Repeat("界", 200)))
	if clean == "" || lipgloss.Width(clean) > 256 || strings.ContainsAny(clean, "\x1b\n") {
		t.Fatalf("sanitized failure = %q", clean)
	}
	if err := validateCodexCache(codexCacheFile{Version: cacheVersion, Provider: "OpenAI Codex", AttemptedAt: now, Failure: clean}, now); err != nil {
		t.Fatalf("sanitized failure rejected: %v", err)
	}
}

func TestFreshnessUsesLocalCalendarDates(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	oldLocal := time.Local
	time.Local = location
	defer func() { time.Local = oldLocal }()

	now := time.Date(2024, 3, 11, 0, 30, 0, 0, location)
	previousDate := time.Date(2024, 3, 10, 0, 30, 0, 0, location)
	if got := freshnessAt(previousDate, now); got != "yesterday 00:30" {
		t.Fatalf("freshness = %q", got)
	}
}

func TestStaleAfterThresholdOrPassedReset(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Minute)
	past := now.Add(-time.Second)
	if quotaIsStale(now.Add(-15*time.Minute), &future, now) {
		t.Fatal("exact threshold should still be fresh")
	}
	if !quotaIsStale(now.Add(-15*time.Minute-time.Nanosecond), &future, now) {
		t.Fatal("old update should be stale")
	}
	if !quotaIsStale(now, &past, now) {
		t.Fatal("passed reset should be stale")
	}
}

func TestQuotaColorsUseUsageThresholds(t *testing.T) {
	tests := []struct {
		remaining float64
		color     lipgloss.Color
		reverse   bool
	}{
		{0, "1", false},
		{1, "#FF9F0A", false},
		{25, "#FF9F0A", false},
		{26, "2", false},
	}
	for _, test := range tests {
		style := quotaValueStyle(test.remaining)
		color, ok := style.GetForeground().(lipgloss.Color)
		if !ok || color != test.color || style.GetReverse() != test.reverse {
			t.Fatalf("remaining %.0f style = color %v reverse %v", test.remaining, style.GetForeground(), style.GetReverse())
		}
	}
}

func TestWideQuotaResetColumnsAlign(t *testing.T) {
	reset := time.Now().Add(time.Hour)
	m := model{}
	lines := []string{
		m.quotaLine(quota{Window: "5-hour session", Remaining: 100, ResetAt: &reset}, false, 80),
		m.quotaLine(quota{Window: "Weekly · all", Remaining: 9, ResetAt: &reset}, false, 80),
		m.quotaLine(quota{Window: "Weekly · Fable", Remaining: 0, ResetAt: &reset}, false, 80),
	}
	column := -1
	for _, line := range lines {
		index := strings.Index(line, "resets")
		if index < 0 {
			t.Fatalf("reset label missing: %q", line)
		}
		got := lipgloss.Width(line[:index])
		if column < 0 {
			column = got
		} else if got != column {
			t.Fatalf("reset columns = %d and %d", column, got)
		}
	}
}

func TestRenderingFitsTerminalAndDemoHidesReload(t *testing.T) {
	for _, width := range []int{1, 8, 17, 30, 59, 80} {
		for _, height := range []int{1, 2, 4, 8, 24} {
			for _, detail := range []bool{false, true} {
				m := newDemoModel()
				m.width, m.height, m.detail = width, height, detail
				view := m.View()
				lines := strings.Split(view, "\n")
				if len(lines) > height {
					t.Fatalf("%dx%d rendered %d lines", width, height, len(lines))
				}
				for _, line := range lines {
					if got := lipgloss.Width(line); got > width {
						t.Fatalf("%dx%d line width = %d: %q", width, height, got, line)
					}
				}
			}
		}
	}
	m := newDemoModel()
	m.width, m.height = 80, 24
	if view := m.View(); strings.Contains(view, "reload") || strings.Contains(view, " r ") {
		t.Fatalf("demo footer exposes reload: %q", view)
	}

	real := model{width: 20, height: 10, loadState: "Claude cache unavailable", loadDetail: strings.Repeat("long/error-path", 20)}
	for _, line := range strings.Split(real.View(), "\n") {
		if got := lipgloss.Width(line); got > real.width {
			t.Fatalf("error line width = %d: %q", got, line)
		}
	}
}

func TestTwoRowCompactViewsKeepFailureOrStaleStateInsteadOfFooter(t *testing.T) {
	for name, q := range map[string]quota{
		"failure": {Provider: "OpenAI", Window: "Weekly", Remaining: 42, Failure: "offline"},
		"stale":   {Provider: "OpenAI", Window: "Weekly", Remaining: 42, Stale: true},
	} {
		t.Run(name, func(t *testing.T) {
			m := model{quotas: []quota{q}}
			for viewName, view := range map[string]string{
				"dashboard": m.compactDashboard(80, 2),
				"detail":    m.compactDetail(80, 2),
			} {
				if lines := strings.Count(view, "\n") + 1; lines != 2 {
					t.Fatalf("%s lines = %d: %q", viewName, lines, view)
				}
				want := "stale"
				if q.Failure != "" {
					want = "refresh failed"
				}
				if !strings.Contains(strings.ToLower(view), want) || strings.Contains(view, "[q]") {
					t.Fatalf("%s did not prioritize %q over footer: %q", viewName, want, view)
				}
			}
		})
	}
}

func TestDashboardShowsUnavailableProviderAlongsideUsage(t *testing.T) {
	m := model{
		width:      80,
		height:     24,
		loadState:  "Unavailable providers",
		loadDetail: "Claude: rate limits unavailable; send one prompt in Claude Code",
		quotas:     []quota{{Provider: "OpenAI", Product: "Codex", Window: "Weekly", Remaining: 50, UpdatedAt: time.Now()}},
	}
	view := m.View()
	if !strings.Contains(view, "OpenAI Codex") || !strings.Contains(view, "Unavailable providers") || !strings.Contains(view, "Claude:") {
		t.Fatalf("provider status missing: %q", view)
	}
}

func TestResetLabelUsesLessThanMinuteInsteadOfZeroMinutes(t *testing.T) {
	reset := time.Now().Add(30 * time.Second)
	if got := resetLabel(quota{ResetAt: &reset}); got != "resets in <1m" {
		t.Fatalf("reset label = %q", got)
	}
}

func TestResetRefreshDeadlinesUseFiveSecondGraceAndDoNotRearm(t *testing.T) {
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	first := now.Add(time.Hour)
	second := now.Add(2 * time.Hour)
	codex := now.Add(30 * time.Minute)
	quotas := []quota{
		{Provider: "Claude", ResetAt: &second},
		{Provider: "Claude", ResetAt: &first},
		{Provider: "OpenAI", ResetAt: &codex},
	}

	deadlines := resetRefreshDeadlines(quotas, nil)
	firstDeadline := first.Add(resetRefreshDelay)
	if !deadlines["Claude"].Equal(firstDeadline) || !deadlines["OpenAI"].Equal(codex.Add(resetRefreshDelay)) {
		t.Fatalf("reset refresh deadlines = %#v", deadlines)
	}
	fired := map[string]struct{}{resetRefreshKey("Claude", firstDeadline): {}}
	if got := resetRefreshDeadlines(quotas, fired)["Claude"]; !got.Equal(second.Add(resetRefreshDelay)) {
		t.Fatalf("next Claude deadline = %v", got)
	}
}

func TestResetRefreshDefersFarTimersAndInvalidatesChangedDeadline(t *testing.T) {
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	farReset := now.Add(time.Hour)
	m := model{quotas: []quota{{Provider: "Claude", ResetAt: &farReset}}}
	if commands := m.resetRefreshCmds(now); len(commands) != 0 || len(m.resetRefreshScheduled) != 0 {
		t.Fatalf("far reset created a timer: commands=%d scheduled=%#v", len(commands), m.resetRefreshScheduled)
	}

	nearDeadline := now.Add(time.Minute + time.Second)
	nearReset := nearDeadline.Add(-resetRefreshDelay)
	m.quotas[0].ResetAt = &nearReset
	if commands := m.resetRefreshCmds(now); len(commands) != 1 || !m.resetRefreshScheduled["Claude"].Equal(nearDeadline) {
		t.Fatalf("near reset was not scheduled: commands=%d scheduled=%#v", len(commands), m.resetRefreshScheduled)
	}

	m.quotas[0].ResetAt = &farReset
	if commands := m.resetRefreshCmds(now); len(commands) != 0 {
		t.Fatalf("changed far reset created a timer: %d", len(commands))
	}
	if _, ok := m.resetRefreshScheduled["Claude"]; ok {
		t.Fatal("changed deadline did not invalidate the old timer message")
	}
	updated, command := m.Update(resetRefreshMsg{provider: "Claude", deadline: nearDeadline})
	if command != nil || updated.(model).claudeCollecting {
		t.Fatal("invalidated timer started a collector")
	}
}

func TestResetRefreshStartsOnlyDueProviderAndIgnoresDuplicate(t *testing.T) {
	deadline := time.Now().Add(time.Hour)
	for _, provider := range []string{"Claude", "OpenAI", "Kimi"} {
		t.Run(provider, func(t *testing.T) {
			m := model{resetRefreshScheduled: map[string]time.Time{provider: deadline}}
			updated, command := m.Update(resetRefreshMsg{provider: provider, deadline: deadline})
			got := updated.(model)
			if command == nil {
				t.Fatal("reset refresh did not start a collector")
			}
			if got.claudeCollecting != (provider == "Claude") || got.codexCollecting != (provider == "OpenAI") || got.kimiCollecting != (provider == "Kimi") {
				t.Fatalf("reset refresh started wrong provider: %#v", got)
			}
			if _, ok := got.resetRefreshFired[resetRefreshKey(provider, deadline)]; !ok {
				t.Fatal("fired reset was not remembered")
			}
			_, duplicate := got.Update(resetRefreshMsg{provider: provider, deadline: deadline})
			if duplicate != nil {
				t.Fatal("duplicate reset refresh was not ignored")
			}
		})
	}
}

func TestResetRefreshWaitsForInFlightProviderThenRunsOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	reset := now.Add(-resetRefreshDelay)
	deadline := reset.Add(resetRefreshDelay)
	path, err := codexCachePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONCache(path, codexCacheFile{
		Version: cacheVersion, Provider: "OpenAI Codex", PlanType: "plus", UpdatedAt: now, AttemptedAt: now,
		Quotas: []codexCachedQuota{{Window: "5h", WindowDurationMins: 300, RemainingPercentage: 50, ResetsAt: reset}},
	}); err != nil {
		t.Fatal(err)
	}
	m := model{
		codexCollecting:       true,
		resetRefreshScheduled: map[string]time.Time{"OpenAI": deadline},
		quotas:                []quota{{Provider: "OpenAI", ResetAt: &reset}},
	}

	updated, command := m.Update(resetRefreshMsg{provider: "OpenAI", deadline: deadline})
	waiting := updated.(model)
	if command != nil || !waiting.resetRefreshPending["OpenAI"].Equal(deadline) {
		t.Fatalf("busy provider did not retain pending refresh: %#v", waiting)
	}
	updated, command = waiting.Update(codexCollectedMsg{})
	refreshed := updated.(model)
	if command == nil || !refreshed.codexCollecting {
		t.Fatalf("pending reset refresh did not start after completion: %#v", refreshed)
	}
	if _, ok := refreshed.resetRefreshFired[resetRefreshKey("OpenAI", deadline)]; !ok {
		t.Fatal("completed pending reset refresh was not remembered")
	}
}

func TestResetRefreshBootstrapAndMissedDeadline(t *testing.T) {
	now := time.Now()
	reset := now.Add(-10 * time.Second)
	m := model{quotas: []quota{{Provider: "Kimi", ResetAt: &reset}}}
	updated, command := m.Update(scheduleResetRefreshMsg{})
	got := updated.(model)
	if command == nil || !got.resetRefreshScheduled["Kimi"].Equal(reset.Add(resetRefreshDelay)) {
		t.Fatalf("reset refresh bootstrap did not retain deadline: %#v", got)
	}
}

func TestDefaultDashboardSchedulesAllProvidersAndRForcesThem(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dashboard := newDashboardModel()
	if !dashboard.claudeCollecting || !dashboard.codexCollecting || !dashboard.kimiCollecting || dashboard.Init() == nil {
		t.Fatalf("default startup state = %#v", dashboard)
	}

	base := model{}
	if commands := base.startCollectors(true); len(commands) != 3 {
		t.Fatalf("forced collector command count = %d, want 3", len(commands))
	}
	updated, command := (model{}).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	got := updated.(model)
	if command == nil || !got.claudeCollecting || !got.codexCollecting || !got.kimiCollecting {
		t.Fatalf("manual refresh did not force every provider: %#v", got)
	}
}

func TestStartupUsesFreshProviderCachesAndManualRefreshBypassesThem(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	reset := now.Add(time.Hour)

	claudePath, _ := cachePath()
	if err := writeJSONCache(claudePath, cacheFile{
		Version: cacheVersion, Provider: "Claude", UpdatedAt: now, AttemptedAt: now, OAuthAttemptedAt: now,
		Quotas: []cachedQuota{{Window: "5-hour session", RemainingPercentage: 50, ResetsAt: &reset}},
	}); err != nil {
		t.Fatal(err)
	}
	codexPath, _ := codexCachePath()
	codex := codexCacheFile{
		Version: cacheVersion, Provider: "OpenAI Codex", PlanType: "plus", UpdatedAt: now, AttemptedAt: now,
		Quotas: []codexCachedQuota{{Window: "5h", WindowDurationMins: 300, RemainingPercentage: 50, ResetsAt: reset}},
	}
	if err := writeJSONCache(codexPath, codex); err != nil {
		t.Fatal(err)
	}
	kimiPath, _ := kimiCachePath()
	if err := writeJSONCache(kimiPath, kimiCacheFile{
		Version: cacheVersion, Provider: "Kimi Code", UpdatedAt: now, AttemptedAt: now,
		Quotas: []kimiCachedQuota{{Window: "5-hour", WindowDuration: 5, WindowUnit: "hour", Used: 1, Limit: 2, RemainingPercentage: 50, ResetsAt: reset}},
	}); err != nil {
		t.Fatal(err)
	}

	fresh := newDashboardModel()
	if fresh.claudeCollecting || fresh.codexCollecting || fresh.kimiCollecting {
		t.Fatalf("fresh caches scheduled collection: %#v", fresh)
	}
	if fresh.Init() == nil {
		t.Fatal("fresh-cache startup did not schedule timers")
	}
	periodic, periodicCommand := model{}.Update(providerTickMsg(now))
	periodicModel := periodic.(model)
	if periodicCommand == nil || periodicModel.claudeCollecting || periodicModel.codexCollecting || periodicModel.kimiCollecting {
		t.Fatalf("automatic refresh did not reuse fresh caches: %#v", periodicModel)
	}
	updated, command := fresh.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	forced := updated.(model)
	if !forced.claudeCollecting || !forced.codexCollecting || !forced.kimiCollecting || command == nil {
		t.Fatalf("manual refresh did not bypass caches: %#v", forced)
	}

	codex.AttemptedAt = now.Add(-providerCacheTTL - time.Second)
	if err := writeJSONCache(codexPath, codex); err != nil {
		t.Fatal(err)
	}
	mixed := newDashboardModel()
	if mixed.claudeCollecting || !mixed.codexCollecting || mixed.kimiCollecting {
		t.Fatalf("provider caches were not evaluated independently: %#v", mixed)
	}
	if mixed.Init() == nil {
		t.Fatal("mixed-cache startup did not schedule timers and stale Codex")
	}
	periodic, periodicCommand = model{}.Update(providerTickMsg(now))
	periodicModel = periodic.(model)
	if periodicModel.claudeCollecting || !periodicModel.codexCollecting || periodicModel.kimiCollecting || periodicCommand == nil {
		t.Fatalf("automatic refresh did not evaluate provider caches independently: %#v", periodicModel)
	}
}

func TestTickReloadsPushedCacheAndReschedules(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	t.Setenv("XDG_CACHE_HOME", temp)
	path, err := cachePath()
	if err != nil {
		t.Fatal(err)
	}
	cache := cacheFile{Version: cacheVersion, Provider: "Claude", UpdatedAt: time.Now(), Quotas: []cachedQuota{{Window: "5-hour session", RemainingPercentage: 10}}}
	if err := writeJSONCache(path, cache); err != nil {
		t.Fatal(err)
	}
	m := model{}
	m.reload()
	cache.Quotas[0].RemainingPercentage = 20
	if err := writeJSONCache(path, cache); err != nil {
		t.Fatal(err)
	}
	updated, command := m.Update(tickMsg(time.Now()))
	if command == nil || updated.(model).quotas[0].Remaining != 20 {
		t.Fatal("tick did not reload cache and reschedule")
	}
}

func TestHorizontalKeysOpenAndCloseDetails(t *testing.T) {
	base := model{quotas: []quota{{Provider: "Claude", Window: "Weekly"}}}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyRight}, {Type: tea.KeyRunes, Runes: []rune{'l'}}} {
		updated, _ := base.Update(key)
		if !updated.(model).detail {
			t.Fatalf("%q did not open details", key.String())
		}
	}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyLeft}, {Type: tea.KeyRunes, Runes: []rune{'h'}}} {
		updated, _ := (model{detail: true, quotas: base.quotas}).Update(key)
		if updated.(model).detail {
			t.Fatalf("%q did not close details", key.String())
		}
	}
}

func TestDetailPreservesFractionalPercentage(t *testing.T) {
	m := model{width: 80, height: 24, detail: true, quotas: []quota{{Provider: "Claude", Product: "Code", Window: "Weekly · all", Remaining: 75.125, UpdatedAt: time.Now()}}}
	if view := m.View(); !strings.Contains(view, "75.125% remaining") || !strings.Contains(view, "24.875% used") {
		t.Fatalf("detail precision missing: %q", view)
	}
}

func TestDemoKimiWindowSemantics(t *testing.T) {
	m := newDemoModel()
	var kimi []quota
	for _, q := range m.quotas {
		if q.Provider == "Kimi" {
			kimi = append(kimi, q)
		}
	}
	if len(kimi) != 2 || kimi[0].ResetAt == nil || kimi[1].ResetAt == nil {
		t.Fatalf("Kimi demo quotas = %#v", kimi)
	}
}

func TestCacheJSONRecordsAttemptState(t *testing.T) {
	cache := cacheFile{Version: cacheVersion, Provider: "Claude", AttemptedAt: time.Now(), Failure: "no usable rate_limits"}
	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"attempted_at"`)) || !bytes.Contains(data, []byte(`"failure"`)) {
		t.Fatalf("cache JSON = %s", data)
	}
}

func writeSnapshotCaches(t *testing.T, now, reset time.Time) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", os.Getenv("HOME"))
	attemptedAt := now.Add(-time.Minute)
	updatedAt := now.Add(-2 * time.Minute)
	claudePath, _ := cachePath()
	if err := writeJSONCache(claudePath, cacheFile{
		Version: cacheVersion, Provider: "Claude", UpdatedAt: updatedAt, OAuthAttemptedAt: attemptedAt,
		Quotas: []cachedQuota{
			{Window: "5-hour session", RemainingPercentage: 50, ResetsAt: &reset, Source: claudeOAuthSource, CollectedAt: updatedAt},
			{Window: "Weekly · all", RemainingPercentage: 40},
		},
	}); err != nil {
		t.Fatal(err)
	}
	codexPath, _ := codexCachePath()
	if err := writeJSONCache(codexPath, codexCacheFile{
		Version: cacheVersion, Provider: "OpenAI Codex", PlanType: "plus", UpdatedAt: updatedAt, AttemptedAt: attemptedAt,
		Quotas: []codexCachedQuota{{Window: codexWindowLabel(300), WindowDurationMins: 300, RemainingPercentage: 60, ResetsAt: reset}},
	}); err != nil {
		t.Fatal(err)
	}
	kimiPath, _ := kimiCachePath()
	if err := writeJSONCache(kimiPath, kimiCacheFile{
		Version: cacheVersion, Provider: "Kimi Code", UpdatedAt: updatedAt, AttemptedAt: attemptedAt,
		Quotas: []kimiCachedQuota{{Window: kimiWindowLabel(5, "hour"), WindowDuration: 5, WindowUnit: "hour", Used: 2, Limit: 4, RemainingPercentage: 50, ResetsAt: reset}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardJSONSnapshotV1(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	reset := now.Add(2 * time.Hour)
	writeSnapshotCaches(t, now, reset)

	var output bytes.Buffer
	if err := writeDashboardJSON(context.Background(), "", &output, dashboardCollectors{}, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output.Bytes(), []byte("\x1b")) || bytes.Contains(output.Bytes(), []byte("AI Usage")) {
		t.Fatalf("snapshot contains TUI output: %q", output.String())
	}
	var raw struct {
		Version     int                      `json:"version"`
		GeneratedAt int64                    `json:"generatedAt"`
		State       string                   `json:"state"`
		Message     string                   `json:"message"`
		Quotas      []dashboardSnapshotQuota `json:"quotas"`
	}
	if err := json.Unmarshal(output.Bytes(), &raw); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, output.String())
	}
	if raw.Version != 1 || raw.GeneratedAt != now.Unix() || raw.State != "ready" || raw.Message != "" {
		t.Fatalf("snapshot header = %#v", raw)
	}
	wantProviders := []string{"Claude", "Claude", "OpenAI", "Kimi"}
	if len(raw.Quotas) != len(wantProviders) {
		t.Fatalf("quota count = %d, want %d", len(raw.Quotas), len(wantProviders))
	}
	for i, provider := range wantProviders {
		if raw.Quotas[i].Provider != provider {
			t.Fatalf("quota order = %#v", raw.Quotas)
		}
		if raw.Quotas[i].ID != raw.Quotas[i].Provider+":"+raw.Quotas[i].Window || raw.Quotas[i].UpdatedAt == nil {
			t.Fatalf("quota timestamps/id = %#v", raw.Quotas[i])
		}
	}
	if raw.Quotas[0].ResetAt == nil || *raw.Quotas[0].ResetAt != reset.Unix() || raw.Quotas[0].AttemptedAt == nil || raw.Quotas[1].ResetAt != nil || raw.Quotas[1].AttemptedAt != nil {
		t.Fatalf("nullable/Unix timestamps = %#v", raw.Quotas[:2])
	}
	if raw.Quotas[0].Source == "" || raw.Quotas[0].Detail == "" || raw.Quotas[0].Failure != "" || raw.Quotas[0].Stale {
		t.Fatalf("Claude shape = %#v", raw.Quotas[0])
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &object); err != nil {
		t.Fatal(err)
	}
	if len(object) != 5 || object["quotas"] == nil || object["generated_at"] != nil {
		t.Fatalf("top-level JSON fields = %#v", object)
	}
	var quotaFields []map[string]json.RawMessage
	if err := json.Unmarshal(object["quotas"], &quotaFields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"id", "provider", "product", "window", "remainingPercent", "resetAt", "updatedAt", "attemptedAt", "failure", "source", "detail", "stale"} {
		if _, ok := quotaFields[0][field]; !ok {
			t.Fatalf("quota field %q missing from %s", field, output.String())
		}
	}
}

func TestDashboardJSONRefreshSelectionConcurrencyAndPartialFailure(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	reset := now.Add(time.Hour)
	writeSnapshotCaches(t, now, reset)

	if selected := dashboardRefreshProviders("auto", now, nil); selected != [3]bool{} {
		t.Fatalf("fresh auto selection = %v", selected)
	}
	if selected := dashboardRefreshProviders("force", now, nil); selected != [3]bool{true, true, true} {
		t.Fatalf("force selection = %v", selected)
	}
	pastReset := now.Add(-resetRefreshDelay)
	if selected := dashboardRefreshProviders("auto", now, []quota{{Provider: "Claude", ResetAt: &pastReset}}); selected != [3]bool{true, false, false} {
		t.Fatalf("reset override selection = %v", selected)
	}
	expiredReset := now.Add(-2 * resetRefreshDelay)
	beforeDeadline := expiredReset.Add(resetRefreshDelay - time.Second)
	afterDeadline := expiredReset.Add(resetRefreshDelay + time.Second)
	failedAfterReset := []quota{
		{Provider: "Claude", ResetAt: &expiredReset, AttemptedAt: beforeDeadline, Failure: "failed"},
		{Provider: "Claude", AttemptedAt: afterDeadline, Failure: "failed"},
	}
	if selected := dashboardRefreshProviders("auto", now, failedAfterReset); selected != [3]bool{} {
		t.Fatalf("fresh failed attempt after reset selected provider again: %v", selected)
	}
	if selected := dashboardRefreshProviders("auto", afterDeadline.Add(providerRefreshRate), failedAfterReset); selected != [3]bool{true, true, true} {
		t.Fatalf("expired attempt freshness did not restore normal retries: %v", selected)
	}

	started := make(chan string, 3)
	release := make(chan struct{})
	collector := func(provider string, err error) func(context.Context) error {
		return func(context.Context) error {
			started <- provider
			<-release
			return err
		}
	}
	collectors := dashboardCollectors{
		claude: collector("Claude", errors.New("temporary\nfailure")),
		codex:  collector("OpenAI", nil),
		kimi:   collector("Kimi", nil),
	}
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- writeDashboardJSON(context.Background(), "force", &output, collectors, func() time.Time { return now })
	}()
	for range 3 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("refresh providers did not start concurrently")
		}
	}
	select {
	case err := <-done:
		t.Fatalf("snapshot completed before collectors joined: %v", err)
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var snapshot dashboardSnapshot
	if err := json.Unmarshal(output.Bytes(), &snapshot); err != nil {
		t.Fatalf("partial failure emitted invalid JSON: %v\n%s", err, output.String())
	}
	if snapshot.State != "partial" || len(snapshot.Quotas) != 4 || snapshot.Quotas[0].RemainingPercent != 50 || snapshot.Quotas[0].Failure != "temporaryfailure" || !snapshot.Quotas[0].Stale || snapshot.Quotas[0].AttemptedAt == nil {
		t.Fatalf("partial failure snapshot = %#v", snapshot)
	}
}

func writeDashboardCaches(t *testing.T) (string, string) {
	t.Helper()
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	t.Setenv("XDG_CACHE_HOME", temp)
	now := time.Now()
	reset := now.Add(2 * time.Hour)
	claudePath, err := cachePath()
	if err != nil {
		t.Fatal(err)
	}
	claude := cacheFile{
		Version: cacheVersion, Provider: "Claude", UpdatedAt: now,
		Quotas: []cachedQuota{{Window: "5-hour session", RemainingPercentage: 50, ResetsAt: &reset}},
	}
	if err := writeJSONCache(claudePath, claude); err != nil {
		t.Fatal(err)
	}
	codexPath, err := codexCachePath()
	if err != nil {
		t.Fatal(err)
	}
	codex := codexCacheFile{
		Version: cacheVersion, Provider: "OpenAI Codex", PlanType: "plus", UpdatedAt: now,
		Quotas: []codexCachedQuota{{Window: "Weekly", WindowDurationMins: 10080, RemainingPercentage: 60, ResetsAt: reset}},
	}
	if err := writeJSONCache(codexPath, codex); err != nil {
		t.Fatal(err)
	}
	return claudePath, codexPath
}

func TestReloadPreservesSelectionIdentityAndClampsMissingSelection(t *testing.T) {
	claudePath, codexPath := writeDashboardCaches(t)
	m := model{}
	m.reload()
	m.selected = 1 // OpenAI Weekly.

	claude, err := readCache()
	if err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(3 * time.Hour)
	claude.Quotas = append(claude.Quotas, cachedQuota{Window: "Weekly · all", RemainingPercentage: 70, ResetsAt: &reset})
	if err := writeJSONCache(claudePath, claude); err != nil {
		t.Fatal(err)
	}
	m.reload()
	if got := quotaKey(m.quotas[m.selected]); got != quotaKey(quota{Provider: "OpenAI", Window: "Weekly"}) || m.selected != 2 {
		t.Fatalf("selection after insertion = %d %q", m.selected, got)
	}

	m.detail = true
	if err := os.Remove(codexPath); err != nil {
		t.Fatal(err)
	}
	m.reload()
	if m.detail || m.selected != 1 || quotaKey(m.quotas[m.selected]) != quotaKey(quota{Provider: "Claude", Window: "Weekly · all"}) {
		t.Fatalf("missing selection fallback: detail=%v selected=%d quotas=%#v", m.detail, m.selected, m.quotas)
	}
}

func TestQuotaKeyUsesProviderAndWindow(t *testing.T) {
	claude := quotaKey(quota{Provider: "Claude", Window: "Weekly"})
	openAI := quotaKey(quota{Provider: "OpenAI", Window: "Weekly"})
	if claude == openAI || claude == quotaKey(quota{Provider: "Claude", Window: "5-hour"}) {
		t.Fatal("quota identity does not include both provider and window")
	}
}

func TestReloadClearsTransientErrorWhenCacheIsNewer(t *testing.T) {
	_, codexPath := writeDashboardCaches(t)
	now := time.Now()
	cache, err := readCodexCache()
	if err != nil {
		t.Fatal(err)
	}
	cache.AttemptedAt = now
	cache.UpdatedAt = now
	if err := writeJSONCache(codexPath, cache); err != nil {
		t.Fatal(err)
	}
	m := model{codexError: "obsolete failure", codexErrorAt: now.Add(-time.Minute)}
	m.reload()
	if m.codexError != "" || !m.codexErrorAt.IsZero() || len(m.quotas) == 0 || m.quotas[len(m.quotas)-1].Failure != "" {
		t.Fatalf("newer cache did not clear transient error: %#v", m)
	}
}

func TestProviderRefreshErrorSurvivesOtherCompletionAndClearsOnOwnSuccess(t *testing.T) {
	writeDashboardCaches(t)
	m := model{claudeCollecting: true, codexCollecting: true}
	m.reload()
	updated, _ := m.Update(claudeCollectedMsg{err: errors.New("temporary Claude failure\n")})
	m = updated.(model)
	if m.claudeError != "temporary Claude failure" || m.claudeErrorAt.IsZero() || m.quotas[0].Failure != m.claudeError || !m.quotas[0].AttemptedAt.Equal(m.claudeErrorAt) {
		t.Fatalf("Claude refresh error was not retained: %#v", m)
	}
	updated, _ = m.Update(codexCollectedMsg{})
	m = updated.(model)
	if m.claudeError == "" || m.quotas[0].Failure == "" {
		t.Fatal("another provider completion erased the Claude error")
	}
	updated, _ = m.Update(claudeCollectedMsg{})
	m = updated.(model)
	if m.claudeError != "" || !m.claudeErrorAt.IsZero() || m.quotas[0].Failure != "" {
		t.Fatal("Claude success did not clear its refresh error")
	}
}

func TestLoadingRefreshingAndCompactIssueRendering(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	t.Setenv("XDG_CACHE_HOME", temp)
	loading := model{width: 80, height: 24, claudeCollecting: true, codexCollecting: true, kimiCollecting: true}
	loading.reload()
	if view := loading.View(); !strings.Contains(view, "Loading usage data") || strings.Contains(view, "open Claude Code") {
		t.Fatalf("startup loading view = %q", view)
	}
	failed := model{width: 30, height: 3, loadState: "No usage data available", loadDetail: "Claude: credential failure"}
	if view := failed.View(); !strings.Contains(view, "Claude: credential failure") {
		t.Fatalf("compact unavailable detail missing: %q", view)
	}

	reset := time.Now().Add(time.Hour)
	compact := model{
		width: 40, height: 4, codexCollecting: true, loadState: "Unavailable providers",
		quotas: []quota{
			{Provider: "Claude", Window: "Weekly", Remaining: 40, ResetAt: &reset, AttemptedAt: time.Now(), Failure: "temporary failure", Stale: true},
			{Provider: "OpenAI", Window: "Weekly", Remaining: 60, ResetAt: &reset},
		},
	}
	for _, detail := range []bool{false, true} {
		compact.detail = detail
		view := compact.View()
		if !strings.Contains(view, "refresh failed today") || !strings.Contains(view, "Refreshing") || strings.Contains(view, "more quotas") {
			t.Fatalf("compact detail=%v did not prioritize issues: %q", detail, view)
		}
	}
}
