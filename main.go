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
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	cacheVersion        = 1
	maxInputSize        = 1 << 20
	maxCacheSize        = 1 << 20
	staleAfter          = 15 * time.Minute
	providerRefreshRate = 5 * time.Minute
	providerCacheTTL    = 5 * time.Minute
	resetRefreshDelay   = 5 * time.Second
	claudeOAuthSource   = "Claude private OAuth usage API"
)

var (
	// ANSI colors inherit the terminal palette instead of imposing a fixed theme.
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	providerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	dimStyle      = lipgloss.NewStyle().Faint(true)
	valueStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("2"))
	lowStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF9F0A"))
	emptyStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	trackStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	warningStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF9F0A"))
)

type statusInput struct {
	RateLimits struct {
		FiveHour *statusWindow `json:"five_hour"`
		SevenDay *statusWindow `json:"seven_day"`
	} `json:"rate_limits"`
}

type statusWindow struct {
	UsedPercentage *float64        `json:"used_percentage"`
	ResetsAt       json.RawMessage `json:"resets_at"`
}

type cacheFile struct {
	Version          int           `json:"version"`
	Provider         string        `json:"provider"`
	RateLimitTier    string        `json:"rate_limit_tier,omitempty"`
	SubscriptionType string        `json:"subscription_type,omitempty"`
	UpdatedAt        time.Time     `json:"updated_at"`
	AttemptedAt      time.Time     `json:"attempted_at,omitempty"`
	Failure          string        `json:"failure,omitempty"`
	OAuthAttemptedAt time.Time     `json:"oauth_attempted_at,omitempty"`
	OAuthFailure     string        `json:"oauth_failure,omitempty"`
	Quotas           []cachedQuota `json:"quotas"`
}

type cachedQuota struct {
	Window              string     `json:"window"`
	RemainingPercentage float64    `json:"remaining_percentage"`
	ResetsAt            *time.Time `json:"resets_at,omitempty"`
	Source              string     `json:"source,omitempty"`
	CollectedAt         time.Time  `json:"collected_at,omitempty"`
}

type quota struct {
	Provider    string
	Product     string
	Window      string
	Remaining   float64
	ResetAt     *time.Time
	UpdatedAt   time.Time
	AttemptedAt time.Time
	Failure     string
	Source      string
	Detail      string
	Stale       bool
}

type dashboardSnapshot struct {
	Version     int                      `json:"version"`
	GeneratedAt int64                    `json:"generatedAt"`
	State       string                   `json:"state"`
	Message     string                   `json:"message"`
	Quotas      []dashboardSnapshotQuota `json:"quotas"`
}

type dashboardSnapshotQuota struct {
	ID               string  `json:"id"`
	Provider         string  `json:"provider"`
	Product          string  `json:"product"`
	Window           string  `json:"window"`
	RemainingPercent float64 `json:"remainingPercent"`
	ResetAt          *int64  `json:"resetAt"`
	UpdatedAt        *int64  `json:"updatedAt"`
	AttemptedAt      *int64  `json:"attemptedAt"`
	Failure          string  `json:"failure"`
	Source           string  `json:"source"`
	Detail           string  `json:"detail"`
	Stale            bool    `json:"stale"`
}

type dashboardCollectors struct {
	claude func(context.Context) error
	codex  func(context.Context) error
	kimi   func(context.Context) error
	zai    func(context.Context) error
}

var liveDashboardCollectors = dashboardCollectors{
	claude: func(ctx context.Context) error { _, err := collectClaude(ctx); return err },
	codex:  func(ctx context.Context) error { _, err := collectCodex(ctx); return err },
	kimi:   func(ctx context.Context) error { _, err := collectKimi(ctx); return err },
	zai:    func(ctx context.Context) error { _, err := collectZAI(ctx); return err },
}

// tickMsg triggers countdown rerenders and cache reloads while the TUI is open.
type tickMsg time.Time
type providerTickMsg time.Time
type scheduleResetRefreshMsg struct{}
type resetRefreshMsg struct {
	provider string
	deadline time.Time
}

type claudeCollectedMsg struct{ err error }
type codexCollectedMsg struct{ err error }
type kimiCollectedMsg struct{ err error }
type zaiCollectedMsg struct{ err error }

type collectorRuntime struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	active sync.WaitGroup
}

type model struct {
	width                 int
	height                int
	selected              int
	detail                bool
	demo                  bool
	claudeCollecting      bool
	codexCollecting       bool
	kimiCollecting        bool
	zaiCollecting         bool
	claudeError           string
	codexError            string
	kimiError             string
	zaiError              string
	claudeErrorAt         time.Time
	codexErrorAt          time.Time
	kimiErrorAt           time.Time
	zaiErrorAt            time.Time
	resetRefreshScheduled map[string]time.Time
	resetRefreshPending   map[string]time.Time
	resetRefreshFired     map[string]struct{}
	collectors            *collectorRuntime
	quotas                []quota
	loadState             string
	loadDetail            string
}

func main() {
	command, ok := parseCommand(os.Args[1:])
	if !ok {
		fmt.Fprintf(os.Stderr, "usage: %s [--demo|--claude-oauth|ingest-claude-code|collect-claude|collect-codex|collect-kimi|collect-zai|dashboard-json [--refresh=auto|--refresh=force]]\n", filepath.Base(os.Args[0]))
		os.Exit(2)
	}
	switch command {
	case "", "--claude-oauth":
		m := newDashboardModel()
		m.reload()
		runTUI(m)
	case "--demo":
		runTUI(newDemoModel())
	case "ingest-claude-code":
		if err := ingest(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "aiusage:", err)
			os.Exit(1)
		}
	case "collect-claude":
		cache, err := collectClaude(context.Background())
		if err != nil {
			fmt.Fprintln(os.Stderr, "aiusage:", err)
			os.Exit(1)
		}
		fmt.Println(compactClaudeUsage(cache))
	case "collect-codex":
		cache, err := collectCodex(context.Background())
		if err != nil {
			fmt.Fprintln(os.Stderr, "aiusage:", err)
			os.Exit(1)
		}
		fmt.Println(compactCodexUsage(cache))
	case "collect-kimi":
		cache, err := collectKimi(context.Background())
		if err != nil {
			fmt.Fprintln(os.Stderr, "aiusage:", err)
			os.Exit(1)
		}
		fmt.Println(compactKimiUsage(cache))
	case "collect-zai":
		cache, err := collectZAI(context.Background())
		if err != nil {
			fmt.Fprintln(os.Stderr, "aiusage:", err)
			os.Exit(1)
		}
		fmt.Println(compactZAIUsage(cache))
	case "dashboard-json", "dashboard-json --refresh=auto", "dashboard-json --refresh=force":
		if err := isolateDashboardHelper(); err != nil {
			fmt.Fprintln(os.Stderr, "aiusage:", err)
			os.Exit(1)
		}
		ctx := context.Background()
		stop := func() {}
		refresh := ""
		if command != "dashboard-json" {
			refresh = strings.TrimPrefix(command, "dashboard-json --refresh=")
			ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		}
		defer stop()
		if err := writeDashboardJSON(ctx, refresh, os.Stdout, liveDashboardCollectors, time.Now); err != nil {
			fmt.Fprintln(os.Stderr, "aiusage:", err)
			os.Exit(1)
		}
	}
}

func parseCommand(args []string) (string, bool) {
	if len(args) == 0 {
		return "", true
	}
	if len(args) == 2 && args[0] == "dashboard-json" && (args[1] == "--refresh=auto" || args[1] == "--refresh=force") {
		return strings.Join(args, " "), true
	}
	if len(args) != 1 {
		return "", false
	}
	switch args[0] {
	case "--demo", "--claude-oauth", "ingest-claude-code", "collect-claude", "collect-codex", "collect-kimi", "collect-zai", "dashboard-json":
		return args[0], true
	default:
		return "", false
	}
}

func writeDashboardJSON(ctx context.Context, refresh string, output io.Writer, collectors dashboardCollectors, now func() time.Time) error {
	m := model{}
	if refresh != "" {
		m.reloadAt(now())
		selected := dashboardRefreshProviders(refresh, now(), m.quotas)
		type result struct {
			provider string
			err      error
		}
		results := make(chan result, 4)
		var active sync.WaitGroup
		start := func(provider string, selected bool, collect func(context.Context) error) {
			if !selected {
				return
			}
			active.Add(1)
			go func() {
				defer active.Done()
				results <- result{provider: provider, err: collect(ctx)}
			}()
		}
		start("Claude", selected[0], collectors.claude)
		start("OpenAI", selected[1], collectors.codex)
		start("Kimi", selected[2], collectors.kimi)
		start("Z.AI", selected[3], collectors.zai)
		active.Wait()
		close(results)
		attemptedAt := now()
		for result := range results {
			failure := safeCollectionError(result.err)
			switch result.provider {
			case "Claude":
				m.claudeError, m.claudeErrorAt = failure, errorAttempt(failure, attemptedAt)
			case "OpenAI":
				m.codexError, m.codexErrorAt = failure, errorAttempt(failure, attemptedAt)
			case "Kimi":
				m.kimiError, m.kimiErrorAt = failure, errorAttempt(failure, attemptedAt)
			case "Z.AI":
				m.zaiError, m.zaiErrorAt = failure, errorAttempt(failure, attemptedAt)
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	generatedAt := now()
	m.reloadAt(generatedAt)
	return json.NewEncoder(output).Encode(newDashboardSnapshot(m, generatedAt))
}

func dashboardRefreshProviders(refresh string, now time.Time, quotas []quota) [4]bool {
	if refresh == "force" {
		return [4]bool{true, true, true, true}
	}
	claudeFresh, codexFresh, kimiFresh, zaiFresh := freshProviderCaches(now)
	selected := [4]bool{!claudeFresh, !codexFresh, !kimiFresh, !zaiFresh}
	latestAttempts := make(map[string]time.Time)
	for _, quota := range quotas {
		if quota.AttemptedAt.After(latestAttempts[quota.Provider]) {
			latestAttempts[quota.Provider] = quota.AttemptedAt
		}
	}
	for _, quota := range quotas {
		if quota.ResetAt == nil {
			continue
		}
		provider, deadline := quota.Provider, quota.ResetAt.Add(resetRefreshDelay)
		if deadline.After(now) || !latestAttempts[provider].Before(deadline) {
			continue
		}
		switch provider {
		case "Claude":
			selected[0] = true
		case "OpenAI":
			selected[1] = true
		case "Kimi":
			selected[2] = true
		case "Z.AI":
			selected[3] = true
		}
	}
	return selected
}

func errorAttempt(failure string, attemptedAt time.Time) time.Time {
	if failure == "" {
		return time.Time{}
	}
	return attemptedAt
}

func newDashboardSnapshot(m model, generatedAt time.Time) dashboardSnapshot {
	state, message := "ready", ""
	if len(m.quotas) == 0 {
		state, message = "unavailable", m.loadState
		if m.loadDetail != "" {
			message += ": " + m.loadDetail
		}
	} else {
		issues := []string{}
		if m.loadDetail != "" {
			issues = append(issues, m.loadDetail)
		}
		seenFailures := make(map[string]bool)
		for _, q := range m.quotas {
			failure := safeCollectionError(errors.New(q.Failure))
			key := q.Provider + "\x00" + failure
			if failure != "" && !seenFailures[key] {
				issues = append(issues, q.Provider+": "+failure)
				seenFailures[key] = true
			}
		}
		if len(issues) > 0 {
			state, message = "partial", strings.Join(issues, " · ")
		}
	}
	snapshot := dashboardSnapshot{
		Version:     1,
		GeneratedAt: generatedAt.Unix(),
		State:       state,
		Message:     message,
		Quotas:      make([]dashboardSnapshotQuota, 0, len(m.quotas)),
	}
	for _, q := range m.quotas {
		snapshot.Quotas = append(snapshot.Quotas, dashboardSnapshotQuota{
			ID:               q.Provider + ":" + q.Window,
			Provider:         q.Provider,
			Product:          q.Product,
			Window:           q.Window,
			RemainingPercent: q.Remaining,
			ResetAt:          unixTime(q.ResetAt),
			UpdatedAt:        unixTimeValue(q.UpdatedAt),
			AttemptedAt:      unixTimeValue(q.AttemptedAt),
			Failure:          safeCollectionError(errors.New(q.Failure)),
			Source:           q.Source,
			Detail:           q.Detail,
			Stale:            q.Stale,
		})
	}
	return snapshot
}

func unixTime(value *time.Time) *int64 {
	if value == nil {
		return nil
	}
	return unixTimeValue(*value)
}

func unixTimeValue(value time.Time) *int64 {
	if value.IsZero() {
		return nil
	}
	seconds := value.Unix()
	return &seconds
}

func newCollectorRuntime() *collectorRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &collectorRuntime{ctx: ctx, cancel: cancel}
}

func (r *collectorRuntime) command(run func(context.Context) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil
		}
		// The same mutex gates stop before Wait, so Add cannot race a zero-count Wait.
		r.active.Add(1)
		r.mu.Unlock()
		defer r.active.Done()
		return run(r.ctx)
	}
}

func (r *collectorRuntime) stop() {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		r.cancel()
	}
	r.mu.Unlock()
	r.active.Wait()
}

func newDashboardModel() model {
	claudeFresh, codexFresh, kimiFresh, zaiFresh := freshProviderCaches(time.Now())
	return model{
		claudeCollecting: !claudeFresh,
		codexCollecting:  !codexFresh,
		kimiCollecting:   !kimiFresh,
		zaiCollecting:    !zaiFresh,
		collectors:       newCollectorRuntime(),
	}
}

func runTUI(m model) {
	if m.collectors == nil {
		m.collectors = newCollectorRuntime()
	}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	m.collectors.stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "aiusage:", err)
		os.Exit(1)
	}
}

func ingest(r io.Reader, w io.Writer) error {
	operationAt := time.Now()
	quotas, err := parseStatusLine(r)
	if err != nil {
		return err
	}
	unlock, err := lockClaudeCache()
	if err != nil {
		return fmt.Errorf("lock Claude cache: %w", err)
	}
	defer unlock()
	now := operationAt
	previous, readErr := readCache()
	if readErr == nil && cacheStateNewerThan(now, previous.UpdatedAt, previous.AttemptedAt, previous.OAuthAttemptedAt) {
		if previous.Failure != "" {
			_, err = fmt.Fprintln(w, "Claude usage unavailable")
		} else {
			_, err = fmt.Fprintln(w, compactUsage(previous.Quotas))
		}
		return err
	}
	cache := cacheFile{Version: cacheVersion, Provider: "Claude", AttemptedAt: now}
	if len(quotas) == 0 {
		if readErr == nil {
			cache = previous
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return fmt.Errorf("preserve cache after unusable update: %w", readErr)
		}
		cache.AttemptedAt = now
		cache.Failure = "rate limits unavailable; send one prompt in Claude Code"
	} else {
		for i := range quotas {
			quotas[i].CollectedAt = now
		}
		if readErr == nil {
			cache.RateLimitTier = previous.RateLimitTier
			cache.SubscriptionType = previous.SubscriptionType
			cache.OAuthAttemptedAt = previous.OAuthAttemptedAt
			cache.OAuthFailure = previous.OAuthFailure
			for _, quota := range previous.Quotas {
				if cache.OAuthAttemptedAt.IsZero() && previous.Failure != "" && quota.Source == claudeOAuthSource {
					cache.OAuthAttemptedAt = previous.AttemptedAt
					cache.OAuthFailure = previous.Failure
				}
				if quota.Window == "Weekly · Fable" {
					quotas = append(quotas, quota)
					break
				}
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return fmt.Errorf("preserve OAuth quota during status-line update: %w", readErr)
		}
		cache.UpdatedAt = now
		cache.AttemptedAt = now
		cache.Quotas = quotas
	}
	if err := validateCache(cache, now); err != nil {
		return fmt.Errorf("validate Claude status-line data: %w", err)
	}
	path, err := cachePath()
	if err != nil {
		return err
	}
	if err := writeJSONCache(path, cache); err != nil {
		return fmt.Errorf("store cache: %w", err)
	}
	if cache.Failure != "" {
		_, err = fmt.Fprintln(w, "Claude usage unavailable")
	} else {
		_, err = fmt.Fprintln(w, compactUsage(cache.Quotas))
	}
	return err
}

func parseStatusLine(r io.Reader) ([]cachedQuota, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxInputSize+1))
	if err != nil {
		return nil, fmt.Errorf("read Claude status-line JSON: %w", err)
	}
	if len(data) > maxInputSize {
		return nil, fmt.Errorf("read Claude status-line JSON: input exceeds %d bytes", maxInputSize)
	}
	var input statusInput
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&input); err != nil {
		return nil, fmt.Errorf("read Claude status-line JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("read Claude status-line JSON: multiple values")
	}

	windows := []struct {
		name string
		data *statusWindow
	}{
		{"5-hour session", input.RateLimits.FiveHour},
		{"Weekly · all", input.RateLimits.SevenDay},
	}
	result := make([]cachedQuota, 0, len(windows))
	for _, window := range windows {
		if window.data == nil || window.data.UsedPercentage == nil {
			continue
		}
		used := *window.data.UsedPercentage
		if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 {
			return nil, fmt.Errorf("%s used_percentage must be between 0 and 100", window.name)
		}
		reset, err := parseReset(window.data.ResetsAt)
		if err != nil {
			return nil, fmt.Errorf("%s resets_at: %w", window.name, err)
		}
		result = append(result, cachedQuota{
			Window:              window.name,
			RemainingPercentage: 100 - used,
			ResetsAt:            reset,
		})
	}
	return result, nil
}

func parseReset(raw json.RawMessage) (*time.Time, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		t, err := time.Parse(time.RFC3339, text)
		if err != nil {
			return nil, errors.New("must be an RFC3339 timestamp")
		}
		return &t, nil
	}
	var stamp float64
	if err := json.Unmarshal(raw, &stamp); err != nil || math.IsNaN(stamp) || math.IsInf(stamp, 0) {
		return nil, errors.New("must be an RFC3339 timestamp or Unix timestamp")
	}
	seconds, fraction := math.Modf(stamp)
	t := time.Unix(int64(seconds), int64(fraction*1e9))
	return &t, nil
}

func compactUsage(quotas []cachedQuota) string {
	if len(quotas) == 0 {
		return "Claude usage unavailable"
	}
	parts := make([]string, 0, len(quotas))
	for _, q := range quotas {
		name := "7d"
		if q.Window == "5-hour session" {
			name = "5h"
		} else if q.Window == "Weekly · Fable" {
			name = "Fable"
		}
		parts = append(parts, fmt.Sprintf("%s %.0f%% left", name, q.RemainingPercentage))
	}
	return "Claude " + strings.Join(parts, " · ")
}

func providerCachePath(name string) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find user cache: %w", err)
	}
	return filepath.Join(dir, "aiusage", name), nil
}

func cachePath() (string, error) {
	return providerCachePath("usage.json")
}

func writeJSONCache(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cache-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func readBoundedCache(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCacheSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCacheSize {
		return nil, errors.New("cache exceeds size limit")
	}
	return data, nil
}

func readCache() (cacheFile, error) {
	path, err := cachePath()
	if err != nil {
		return cacheFile{}, err
	}
	data, err := readBoundedCache(path)
	if err != nil {
		return cacheFile{}, err
	}
	var cache cacheFile
	if err := json.Unmarshal(data, &cache); err != nil {
		return cacheFile{}, err
	}
	if err := validateCache(cache, time.Now()); err != nil {
		return cacheFile{}, err
	}
	return cache, nil
}

func validateCache(cache cacheFile, now time.Time) error {
	if cache.Version != cacheVersion || cache.Provider != "Claude" {
		return errors.New("unsupported cache format")
	}
	if len(cache.Quotas) > 0 {
		if err := validatePastTimestamp(cache.UpdatedAt, now, "updated_at"); err != nil {
			return err
		}
	}
	if !cache.AttemptedAt.IsZero() {
		if err := validatePastTimestamp(cache.AttemptedAt, now, "attempted_at"); err != nil {
			return err
		}
	}
	if cache.Failure != "" && cache.AttemptedAt.IsZero() {
		return errors.New("cache failure is missing attempted_at")
	}
	if cache.Failure != safeCollectionError(errors.New(cache.Failure)) {
		return errors.New("cache failure is invalid")
	}
	if !cache.OAuthAttemptedAt.IsZero() {
		if err := validatePastTimestamp(cache.OAuthAttemptedAt, now, "oauth_attempted_at"); err != nil {
			return err
		}
	}
	if cache.OAuthFailure != "" && cache.OAuthAttemptedAt.IsZero() {
		return errors.New("cache OAuth failure is missing oauth_attempted_at")
	}
	if cache.OAuthFailure != safeCollectionError(errors.New(cache.OAuthFailure)) {
		return errors.New("cache OAuth failure is invalid")
	}
	if safeClaudeCredentialLabel(cache.RateLimitTier) != cache.RateLimitTier || safeClaudeCredentialLabel(cache.SubscriptionType) != cache.SubscriptionType {
		return errors.New("Claude cache account metadata is invalid")
	}
	known := map[string]bool{"5-hour session": true, "Weekly · all": true, "Weekly · Fable": true}
	seen := make(map[string]bool, len(cache.Quotas))
	for _, q := range cache.Quotas {
		if !known[q.Window] {
			return fmt.Errorf("unknown quota window %q", q.Window)
		}
		if seen[q.Window] {
			return fmt.Errorf("duplicate quota window %q", q.Window)
		}
		seen[q.Window] = true
		if q.Source != "" && q.Source != claudeOAuthSource {
			return fmt.Errorf("%s source is invalid", q.Window)
		}
		if !q.CollectedAt.IsZero() {
			if err := validatePastTimestamp(q.CollectedAt, now, "quota collected_at"); err != nil {
				return err
			}
		}
		if q.Window == "Weekly · Fable" && q.Source != claudeOAuthSource {
			return errors.New("Weekly · Fable requires the Claude OAuth source")
		}
		if math.IsNaN(q.RemainingPercentage) || math.IsInf(q.RemainingPercentage, 0) || q.RemainingPercentage < 0 || q.RemainingPercentage > 100 {
			return fmt.Errorf("%s remaining_percentage must be between 0 and 100", q.Window)
		}
		if q.ResetsAt != nil {
			if q.ResetsAt.Year() < 2020 || q.ResetsAt.After(now.Add(8*24*time.Hour)) {
				return fmt.Errorf("%s resets_at is implausible", q.Window)
			}
		}
	}
	return nil
}

func validatePastTimestamp(t, now time.Time, name string) error {
	if t.IsZero() || t.Year() < 2020 || t.After(now.Add(5*time.Minute)) {
		return fmt.Errorf("cache %s is implausible", name)
	}
	return nil
}

func tick() tea.Cmd {
	return tea.Tick(time.Minute, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func providerTick() tea.Cmd {
	return tea.Tick(providerRefreshRate, func(t time.Time) tea.Msg { return providerTickMsg(t) })
}

func collectClaudeCmd(runtime *collectorRuntime) tea.Cmd {
	return runtime.command(func(ctx context.Context) tea.Msg {
		_, err := collectClaude(ctx)
		return claudeCollectedMsg{err: err}
	})
}

func collectCodexCmd(runtime *collectorRuntime) tea.Cmd {
	return runtime.command(func(ctx context.Context) tea.Msg {
		_, err := collectCodex(ctx)
		return codexCollectedMsg{err: err}
	})
}

func collectKimiCmd(runtime *collectorRuntime) tea.Cmd {
	return runtime.command(func(ctx context.Context) tea.Msg {
		_, err := collectKimi(ctx)
		return kimiCollectedMsg{err: err}
	})
}

func collectZAICmd(runtime *collectorRuntime) tea.Cmd {
	return runtime.command(func(ctx context.Context) tea.Msg {
		_, err := collectZAI(ctx)
		return zaiCollectedMsg{err: err}
	})
}

func (m model) Init() tea.Cmd {
	if m.demo {
		return tick()
	}
	if m.collectors == nil {
		m.collectors = newCollectorRuntime()
	}
	commands := []tea.Cmd{tick(), providerTick()}
	if m.claudeCollecting {
		commands = append(commands, collectClaudeCmd(m.collectors))
	}
	if m.codexCollecting {
		commands = append(commands, collectCodexCmd(m.collectors))
	}
	if m.kimiCollecting {
		commands = append(commands, collectKimiCmd(m.collectors))
	}
	if m.zaiCollecting {
		commands = append(commands, collectZAICmd(m.collectors))
	}
	commands = append(commands, func() tea.Msg { return scheduleResetRefreshMsg{} })
	return tea.Batch(commands...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		commands := []tea.Cmd{tick()}
		if !m.demo {
			m.reload()
			commands = append(commands, m.resetRefreshCmds(time.Now())...)
		}
		return m, tea.Batch(commands...)
	case providerTickMsg:
		commands := append([]tea.Cmd{providerTick()}, m.startCollectors(false)...)
		return m, tea.Batch(commands...)
	case scheduleResetRefreshMsg:
		return m, tea.Batch(m.resetRefreshCmds(time.Now())...)
	case resetRefreshMsg:
		deadline, ok := m.resetRefreshScheduled[msg.provider]
		if !ok || !deadline.Equal(msg.deadline) {
			return m, nil
		}
		delete(m.resetRefreshScheduled, msg.provider)
		command := m.startProviderCollector(msg.provider)
		if command == nil && m.providerCollecting(msg.provider) {
			if m.resetRefreshPending == nil {
				m.resetRefreshPending = make(map[string]time.Time)
			}
			m.resetRefreshPending[msg.provider] = msg.deadline
		} else {
			m.markResetRefreshFired(msg.provider, msg.deadline)
		}
		commands := m.resetRefreshCmds(time.Now())
		if command != nil {
			commands = append(commands, command)
		}
		return m, tea.Batch(commands...)
	case claudeCollectedMsg:
		m.claudeCollecting = false
		m.claudeError = safeCollectionError(msg.err)
		m.claudeErrorAt = errorTime(m.claudeError)
		m.reload()
		commands := []tea.Cmd{m.startPendingResetRefresh("Claude")}
		commands = append(commands, m.resetRefreshCmds(time.Now())...)
		return m, tea.Batch(commands...)
	case codexCollectedMsg:
		m.codexCollecting = false
		m.codexError = safeCollectionError(msg.err)
		m.codexErrorAt = errorTime(m.codexError)
		m.reload()
		commands := []tea.Cmd{m.startPendingResetRefresh("OpenAI")}
		commands = append(commands, m.resetRefreshCmds(time.Now())...)
		return m, tea.Batch(commands...)
	case kimiCollectedMsg:
		m.kimiCollecting = false
		m.kimiError = safeCollectionError(msg.err)
		m.kimiErrorAt = errorTime(m.kimiError)
		m.reload()
		commands := []tea.Cmd{m.startPendingResetRefresh("Kimi")}
		commands = append(commands, m.resetRefreshCmds(time.Now())...)
		return m, tea.Batch(commands...)
	case zaiCollectedMsg:
		m.zaiCollecting = false
		m.zaiError = safeCollectionError(msg.err)
		m.zaiErrorAt = errorTime(m.zaiError)
		m.reload()
		commands := []tea.Cmd{m.startPendingResetRefresh("Z.AI")}
		commands = append(commands, m.resetRefreshCmds(time.Now())...)
		return m, tea.Batch(commands...)
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc", "left", "h":
			m.detail = false
		case "enter", "right", "l":
			if len(m.quotas) > 0 {
				m.detail = true
			}
		case "up", "k":
			if !m.detail && m.selected > 0 {
				m.selected--
			}
		case "down", "j":
			if !m.detail && m.selected < len(m.quotas)-1 {
				m.selected++
			}
		case "r":
			if !m.demo {
				commands := m.startCollectors(true)
				m.reload()
				commands = append(commands, m.resetRefreshCmds(time.Now())...)
				return m, tea.Batch(commands...)
			}
		}
	}
	return m, nil
}

func (m *model) startCollectors(force bool) []tea.Cmd {
	if m.demo {
		return nil
	}
	if m.collectors == nil {
		m.collectors = newCollectorRuntime()
	}
	claudeFresh, codexFresh, kimiFresh, zaiFresh := false, false, false, false
	if !force {
		claudeFresh, codexFresh, kimiFresh, zaiFresh = freshProviderCaches(time.Now())
	}
	var commands []tea.Cmd
	if !claudeFresh {
		if command := m.startProviderCollector("Claude"); command != nil {
			commands = append(commands, command)
		}
	}
	if !codexFresh {
		if command := m.startProviderCollector("OpenAI"); command != nil {
			commands = append(commands, command)
		}
	}
	if !kimiFresh {
		if command := m.startProviderCollector("Kimi"); command != nil {
			commands = append(commands, command)
		}
	}
	if !zaiFresh {
		if command := m.startProviderCollector("Z.AI"); command != nil {
			commands = append(commands, command)
		}
	}
	return commands
}

func (m *model) startProviderCollector(provider string) tea.Cmd {
	if m.collectors == nil {
		m.collectors = newCollectorRuntime()
	}
	switch provider {
	case "Claude":
		if !m.claudeCollecting {
			m.claudeCollecting = true
			return collectClaudeCmd(m.collectors)
		}
	case "OpenAI":
		if !m.codexCollecting {
			m.codexCollecting = true
			return collectCodexCmd(m.collectors)
		}
	case "Kimi":
		if !m.kimiCollecting {
			m.kimiCollecting = true
			return collectKimiCmd(m.collectors)
		}
	case "Z.AI":
		if !m.zaiCollecting {
			m.zaiCollecting = true
			return collectZAICmd(m.collectors)
		}
	}
	return nil
}

func (m model) providerCollecting(provider string) bool {
	switch provider {
	case "Claude":
		return m.claudeCollecting
	case "OpenAI":
		return m.codexCollecting
	case "Kimi":
		return m.kimiCollecting
	case "Z.AI":
		return m.zaiCollecting
	default:
		return false
	}
}

func (m *model) markResetRefreshFired(provider string, deadline time.Time) {
	if m.resetRefreshFired == nil {
		m.resetRefreshFired = make(map[string]struct{})
	}
	m.resetRefreshFired[resetRefreshKey(provider, deadline)] = struct{}{}
}

func (m *model) startPendingResetRefresh(provider string) tea.Cmd {
	deadline, ok := m.resetRefreshPending[provider]
	if !ok {
		return nil
	}
	delete(m.resetRefreshPending, provider)
	if current, ok := resetRefreshDeadlines(m.quotas, m.resetRefreshFired)[provider]; !ok || !current.Equal(deadline) {
		return nil
	}
	m.markResetRefreshFired(provider, deadline)
	return m.startProviderCollector(provider)
}

func resetRefreshKey(provider string, deadline time.Time) string {
	return provider + "\x00" + deadline.UTC().Format(time.RFC3339Nano)
}

func resetRefreshDeadlines(quotas []quota, fired map[string]struct{}) map[string]time.Time {
	deadlines := make(map[string]time.Time)
	for _, quota := range quotas {
		if quota.ResetAt == nil {
			continue
		}
		deadline := quota.ResetAt.Add(resetRefreshDelay)
		if _, ok := fired[resetRefreshKey(quota.Provider, deadline)]; ok {
			continue
		}
		if current, ok := deadlines[quota.Provider]; !ok || deadline.Before(current) {
			deadlines[quota.Provider] = deadline
		}
	}
	return deadlines
}

func (m *model) resetRefreshCmds(now time.Time) []tea.Cmd {
	if m.demo {
		return nil
	}
	deadlines := resetRefreshDeadlines(m.quotas, m.resetRefreshFired)
	if m.resetRefreshScheduled == nil {
		m.resetRefreshScheduled = make(map[string]time.Time)
	}
	for provider := range m.resetRefreshScheduled {
		if _, ok := deadlines[provider]; !ok {
			delete(m.resetRefreshScheduled, provider)
		}
	}
	for provider, pending := range m.resetRefreshPending {
		if deadline, ok := deadlines[provider]; !ok || !deadline.Equal(pending) {
			delete(m.resetRefreshPending, provider)
		}
	}
	var commands []tea.Cmd
	for provider, deadline := range deadlines {
		if pending, ok := m.resetRefreshPending[provider]; ok && pending.Equal(deadline) {
			continue
		}
		if scheduled, ok := m.resetRefreshScheduled[provider]; ok {
			if scheduled.Equal(deadline) {
				continue
			}
			delete(m.resetRefreshScheduled, provider) // Invalidate the old timer message.
		}
		delay := deadline.Sub(now)
		if delay > 2*time.Minute {
			continue // A minute tick will schedule it closer to the deadline.
		}
		if delay < 0 {
			delay = 0
		}
		m.resetRefreshScheduled[provider] = deadline
		msg := resetRefreshMsg{provider: provider, deadline: deadline}
		commands = append(commands, tea.Tick(delay, func(time.Time) tea.Msg { return msg }))
	}
	return commands
}

func freshProviderCaches(now time.Time) (claude, codex, kimi, zai bool) {
	if cache, err := readCache(); err == nil {
		attemptedAt := cache.OAuthAttemptedAt
		if attemptedAt.IsZero() {
			for _, quota := range cache.Quotas {
				if quota.Source == claudeOAuthSource && quota.CollectedAt.After(attemptedAt) {
					attemptedAt = quota.CollectedAt
				}
			}
		}
		claude = providerCacheFresh(attemptedAt, time.Time{}, now)
	}
	if cache, err := readCodexCache(); err == nil {
		codex = providerCacheFresh(cache.AttemptedAt, cache.UpdatedAt, now)
	}
	if cache, err := readKimiCache(); err == nil {
		kimi = providerCacheFresh(cache.AttemptedAt, cache.UpdatedAt, now)
	}
	if cache, err := readZAICache(); err == nil {
		zai = providerCacheFresh(cache.AttemptedAt, cache.UpdatedAt, now)
	}
	return
}

func providerCacheFresh(attemptedAt, updatedAt, now time.Time) bool {
	if attemptedAt.IsZero() {
		attemptedAt = updatedAt
	}
	return !attemptedAt.IsZero() && attemptedAt.Add(providerCacheTTL).After(now)
}

func parentCollectionCanceled(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.Canceled)
}

func cacheStateNewerThan(operation time.Time, timestamps ...time.Time) bool {
	for _, timestamp := range timestamps {
		if timestamp.After(operation) {
			return true
		}
	}
	return false
}

func safeCollectionError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(err.Error()))
	return truncateText(text, 256)
}

func (m model) collecting() bool {
	return m.claudeCollecting || m.codexCollecting || m.kimiCollecting || m.zaiCollecting
}

func errorTime(text string) time.Time {
	if text == "" {
		return time.Time{}
	}
	return time.Now()
}

func (m model) globalStatus() string {
	var status []string
	if m.collecting() {
		status = append(status, "Refreshing…")
	}
	if m.loadDetail != "" {
		status = append(status, m.loadDetail)
	} else if m.loadState != "" && m.loadState != "Loading usage data" {
		status = append(status, m.loadState)
	}
	return strings.Join(status, " · ")
}

func (m model) View() string {
	width, height := m.width, m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	padding := 2
	if width < 40 {
		padding = 1
	}
	if width < 20 {
		padding = 0
	}
	contentWidth := max(1, width-2*padding)
	var body string
	if m.detail && len(m.quotas) > 0 {
		body = m.detailView(contentWidth, height)
	} else {
		body = m.dashboardView(contentWidth, height)
	}
	return lipgloss.NewStyle().Padding(0, padding).Render(body)
}

func (m model) dashboardView(width, height int) string {
	var lines []string
	header := titleStyle.Render(truncateText("AI Usage", width))
	if m.demo {
		demo := "DEMO DATA — not real usage"
		if lipgloss.Width("AI Usage  "+demo) <= width {
			header += "  " + warningStyle.Render(demo)
		} else {
			lines = append(lines, header)
			header = warningStyle.Render(truncateText(demo, width))
		}
	}
	lines = append(lines, header, dimStyle.Render(strings.Repeat("─", width)))
	if len(m.quotas) > 0 && m.collecting() {
		lines = append(lines, dimStyle.Render(truncateText("Refreshing…", width)))
	}
	if len(m.quotas) == 0 {
		lines = append(lines, "", warningStyle.Render(truncateText(m.loadState, width)))
		for _, line := range wrapText(m.loadDetail, width) {
			lines = append(lines, dimStyle.Render(line))
		}
	} else {
		lastProvider := ""
		for i, q := range m.quotas {
			if q.Provider != lastProvider {
				lines = append(lines, "")
				lines = append(lines, strings.Split(providerLine(q, width), "\n")...)
				lastProvider = q.Provider
			}
			lines = append(lines, strings.Split(m.quotaLine(q, i == m.selected, width), "\n")...)
		}
		if m.loadDetail != "" {
			lines = append(lines, "", warningStyle.Render(truncateText(m.loadState, width)))
			for _, line := range wrapText(m.loadDetail, width) {
				lines = append(lines, dimStyle.Render(line))
			}
		}
	}
	lines = append(lines, dimStyle.Render(strings.Repeat("─", width)))
	lines = append(lines, m.footerLines(width, false)...)
	if len(lines) > height {
		return m.compactDashboard(width, height)
	}
	return strings.Join(lines, "\n")
}

func providerLine(q quota, width int) string {
	name := q.Provider + " " + q.Product
	status := "updated " + freshness(q.UpdatedAt)
	if q.Failure != "" {
		status += " · " + refreshFailureStatus(q)
	} else if q.Stale {
		status = "stale · " + status
	}
	if lipgloss.Width(name)+3+lipgloss.Width(status) > width {
		return providerStyle.Render(truncateText(name, width)) + "\n" + dimStyle.Render(truncateText(status, width))
	}
	return providerStyle.Render(name) + dimStyle.Render(" · "+status)
}

func (m model) quotaLine(q quota, selected bool, width int) string {
	cursor := "  "
	if selected {
		cursor = selectedStyle.Render("> ")
	}
	percentageText := fmt.Sprintf("%.0f%% left", q.Remaining)
	percentage := quotaValueStyle(q.Remaining).Render(percentageText)
	status := resetLabel(q)
	if q.Failure != "" {
		status = refreshFailureStatus(q) + " · " + status
	} else if q.Stale {
		status = "stale · " + status
	}
	if width < 60 {
		label := shortWindow(q.Window)
		available := width - lipgloss.Width(cursor) - lipgloss.Width(percentageText) - 1
		if available < 1 {
			prefix := "  "
			if selected {
				prefix = "> "
			}
			return truncateText(prefix+percentageText, width)
		}
		label = truncateText(label, available)
		gap := max(1, width-lipgloss.Width(cursor)-lipgloss.Width(label)-lipgloss.Width(percentageText))
		indent := min(4, max(0, width-1))
		status = truncateText(status, max(1, width-indent))
		statusLine := dimStyle.Render(status)
		if q.Stale {
			statusLine = warningStyle.Render(status)
		}
		return cursor + label + strings.Repeat(" ", gap) + percentage + "\n" + strings.Repeat(" ", indent) + statusLine
	}
	const percentageWidth = 9 // "100% left"
	percentage = quotaValueStyle(q.Remaining).Render(fmt.Sprintf("%-*s", percentageWidth, percentageText))
	barWidth := min(20, max(8, width-47))
	fixed := 2 + 12 + 1 + barWidth + 2 + percentageWidth + 2
	if fixed >= width {
		return m.quotaLine(q, selected, 59)
	}
	status = truncateText(status, width-fixed)
	statusView := dimStyle.Render(status)
	if q.Stale {
		statusView = warningStyle.Render(status)
	}
	return fmt.Sprintf("%s%-12s %s  %s  %s", cursor, truncateText(shortWindow(q.Window), 12), renderBar(q.Remaining, barWidth), percentage, statusView)
}

func refreshFailureStatus(q quota) string {
	if q.AttemptedAt.IsZero() {
		return "refresh failed: " + q.Failure
	}
	return "refresh failed " + freshness(q.AttemptedAt) + ": " + q.Failure
}

func shortWindow(window string) string {
	switch window {
	case "5-hour session":
		return "5-hour"
	case "Weekly · all":
		return "Weekly"
	case "Weekly · Fable":
		return "Fable"
	default:
		return window
	}
}

func quotaValueStyle(remaining float64) lipgloss.Style {
	if remaining <= 0 {
		return emptyStyle
	}
	if remaining <= 25 {
		return lowStyle
	}
	return valueStyle
}

func renderBar(remaining float64, width int) string {
	filled := int(math.Round(remaining / 100 * float64(width)))
	filled = max(0, min(width, filled))
	if filled == 0 {
		return trackStyle.Render(strings.Repeat("▇", width))
	}
	return quotaValueStyle(remaining).Render(strings.Repeat("▇", filled)) + trackStyle.Render(strings.Repeat("▇", width-filled))
}

func (m model) detailView(width, height int) string {
	q := m.quotas[m.selected]
	lines := []string{
		titleStyle.Render(truncateText("AI Usage · Details", width)),
		dimStyle.Render(strings.Repeat("─", width)),
	}
	if m.collecting() {
		lines = append(lines, dimStyle.Render(truncateText("Refreshing…", width)))
	}
	lines = append(lines,
		"",
		providerStyle.Render(truncateText(q.Provider+" "+q.Product, width)),
		truncateText(q.Window, width),
		"",
	)
	remaining := formatPercent(q.Remaining) + "% remaining"
	used := "(" + formatPercent(100-q.Remaining) + "% used)"
	if lipgloss.Width(remaining+"  "+used) <= width {
		lines = append(lines, quotaValueStyle(q.Remaining).Render(remaining)+"  "+used)
	} else {
		lines = append(lines, quotaValueStyle(q.Remaining).Render(truncateText(remaining, width)), truncateText(used, width))
	}
	if q.ResetAt != nil {
		lines = append(lines, wrapText("Reset: "+q.ResetAt.Local().Format("Mon 2 Jan 2006, 15:04 MST"), width)...)
		lines = append(lines, wrapText(resetLabel(q), width)...)
	} else {
		lines = append(lines, "Reset: unknown")
	}
	state := "Last success: " + freshness(q.UpdatedAt)
	if q.Stale {
		state = "Stale — " + state
	}
	lines = append(lines, wrapText(state, width)...)
	if q.Failure != "" {
		lines = append(lines, wrapText("Last attempt: "+freshness(q.AttemptedAt)+" — "+q.Failure, width)...)
	}
	lines = append(lines, wrapText("Source: "+q.Source, width)...)
	if q.Detail != "" {
		lines = append(lines, "")
		lines = append(lines, wrapText(q.Detail, width)...)
	}
	lines = append(lines, "", dimStyle.Render(strings.Repeat("─", width)))
	lines = append(lines, m.footerLines(width, true)...)
	if len(lines) > height {
		return m.compactDetail(width, height)
	}
	return strings.Join(lines, "\n")
}

func (m model) footerLines(width int, detail bool) []string {
	items := []string{"[↑↓/jk] select", "[enter/→/l] details"}
	if detail {
		items = []string{"[esc/←/h] back"}
	}
	if !m.demo {
		items = append(items, "[r] refresh")
	}
	items = append(items, "[q] quit")
	lines := wrapText(strings.Join(items, "  "), width)
	for i := range lines {
		lines[i] = dimStyle.Render(lines[i])
	}
	return lines
}

func (m model) compactDashboard(width, height int) string {
	if height <= 0 {
		return ""
	}
	footer := "[↑↓] [→]"
	if !m.demo {
		footer += " [r]"
	}
	footer += " [q]"
	footer = truncateText(footer, width)
	if len(m.quotas) == 0 {
		state := m.loadState
		if m.collecting() {
			state = "Loading usage data"
		}
		lines := []string{warningStyle.Render(truncateText(state, width))}
		if m.loadDetail != "" && height > 2 {
			lines = append(lines, dimStyle.Render(truncateText(m.loadDetail, width)))
		}
		if height > 1 {
			lines = append(lines, dimStyle.Render(footer))
		}
		return strings.Join(lines[:min(height, len(lines))], "\n")
	}
	q := m.quotas[m.selected]
	value := fmt.Sprintf("> %s %s %.0f%%", q.Provider, shortWindow(q.Window), q.Remaining)
	status := resetLabel(q)
	if q.Failure != "" {
		status = refreshFailureStatus(q)
	} else if q.Stale {
		status = "stale · " + status
	}
	lines := []string{
		quotaValueStyle(q.Remaining).Render(truncateText(value, width)),
		warningStyle.Render(truncateText(status, width)),
	}
	if global := m.globalStatus(); global != "" {
		lines = append(lines, warningStyle.Render(truncateText(global, width)))
	}
	if len(m.quotas) > 1 {
		lines = append(lines, dimStyle.Render(truncateText(fmt.Sprintf("… %d more quotas", len(m.quotas)-1), width)))
	}
	if height > 2 {
		lines = append(lines[:min(height-1, len(lines))], dimStyle.Render(footer))
	} else {
		lines = lines[:min(height, len(lines))]
	}
	return strings.Join(lines, "\n")
}

func (m model) compactDetail(width, height int) string {
	q := m.quotas[m.selected]
	footer := "[←/esc]"
	if !m.demo {
		footer += " [r]"
	}
	footer += " [q]"
	state := resetLabel(q)
	if q.Failure != "" {
		state = refreshFailureStatus(q)
	} else if q.Stale {
		state = "stale · " + state
	}
	lines := []string{
		quotaValueStyle(q.Remaining).Render(truncateText(formatPercent(q.Remaining)+"% remaining", width)),
		warningStyle.Render(truncateText(state, width)),
	}
	if global := m.globalStatus(); global != "" {
		lines = append(lines, warningStyle.Render(truncateText(global, width)))
	}
	lines = append(lines, providerStyle.Render(truncateText(q.Provider+" · "+q.Window, width)))
	if height > 2 {
		lines = append(lines[:min(height-1, len(lines))], dimStyle.Render(truncateText(footer, width)))
	} else {
		lines = lines[:min(height, len(lines))]
	}
	return strings.Join(lines, "\n")
}

func truncateText(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if lipgloss.Width(b.String()+string(r)) > width {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func wrapText(s string, width int) []string {
	if width <= 0 {
		return []string{""}
	}
	var lines []string
	for _, paragraph := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(paragraph) {
			for lipgloss.Width(word) > width {
				if line != "" {
					lines = append(lines, line)
					line = ""
				}
				part := truncateText(word, width)
				if part == "" {
					_, size := utf8.DecodeRuneInString(word)
					word = word[size:]
					continue
				}
				lines = append(lines, part)
				word = strings.TrimPrefix(word, part)
			}
			candidate := word
			if line != "" {
				candidate = line + " " + word
			}
			if lipgloss.Width(candidate) > width {
				lines = append(lines, line)
				line = word
			} else {
				line = candidate
			}
		}
		if line != "" || paragraph == "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func formatPercent(n float64) string {
	return strconv.FormatFloat(n, 'f', -1, 64)
}

func resetLabel(q quota) string {
	if q.ResetAt == nil {
		return "reset unknown"
	}
	d := time.Until(*q.ResetAt)
	if d <= 0 {
		return "reset due"
	}
	if d < time.Minute {
		return "resets in <1m"
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("resets in %dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("resets in %dd", int(math.Ceil(d.Hours()/24)))
}

func freshness(t time.Time) string {
	return freshnessAt(t, time.Now())
}

func freshnessAt(t, now time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	local, today := t.Local(), now.Local()
	date := func(v time.Time) time.Time {
		y, m, d := v.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	days := int(date(today).Sub(date(local)) / (24 * time.Hour))
	switch days {
	case 0:
		return "today " + local.Format("15:04")
	case 1:
		return "yesterday " + local.Format("15:04")
	default:
		return local.Format("2 Jan 2006 15:04")
	}
}

func (m *model) reload() {
	m.reloadAt(time.Now())
}

func (m *model) reloadAt(now time.Time) {
	selectedKey := ""
	previousSelected := m.selected
	if m.selected >= 0 && m.selected < len(m.quotas) {
		selectedKey = quotaKey(m.quotas[m.selected])
	}
	m.quotas = nil
	m.loadState, m.loadDetail = "", ""
	claudeCache, claudeErr := readCache()
	if claudeErr == nil && cacheStateNewerThan(m.claudeErrorAt, claudeCache.AttemptedAt, claudeCache.UpdatedAt) {
		m.claudeError, m.claudeErrorAt = "", time.Time{}
	}
	if claudeErr == nil {
		oauthAttemptedAt := claudeCache.OAuthAttemptedAt
		oauthFailure := currentFailure(claudeCache.OAuthFailure, m.claudeError)
		if oauthAttemptedAt.IsZero() && claudeCache.OAuthFailure == "" {
			oauthAttemptedAt = claudeCache.AttemptedAt
			oauthFailure = currentFailure(claudeCache.Failure, m.claudeError)
		}
		hasOAuthQuota := false
		for _, q := range claudeCache.Quotas {
			hasOAuthQuota = hasOAuthQuota || q.Source == claudeOAuthSource
		}
		statusFailure := claudeCache.Failure
		if !claudeCache.OAuthAttemptedAt.IsZero() && claudeCache.AttemptedAt.Equal(claudeCache.OAuthAttemptedAt) && claudeCache.Failure == claudeCache.OAuthFailure {
			statusFailure = ""
		}
		for i, q := range claudeCache.Quotas {
			updatedAt := q.CollectedAt
			if updatedAt.IsZero() {
				updatedAt = claudeCache.UpdatedAt
			}
			attemptedAt, failure := claudeCache.AttemptedAt, statusFailure
			source := "Claude Code status-line JSON"
			detail := ""
			if q.Source == claudeOAuthSource {
				attemptedAt = currentAttempt(oauthAttemptedAt, m.claudeError, m.claudeErrorAt)
				failure = oauthFailure
				source = claudeOAuthSource + " (unofficial/private)"
				detail = "Uses Claude Code's raw OAuth access token with Anthropic's private usage endpoint."
				if claudeCache.SubscriptionType != "" {
					detail += " Subscription: " + claudeCache.SubscriptionType + "."
				}
				if claudeCache.RateLimitTier != "" {
					detail += " Rate-limit tier: " + claudeCache.RateLimitTier + "."
				}
			} else if !hasOAuthQuota && i == 0 && oauthFailure != "" {
				attemptedAt, failure = currentAttempt(oauthAttemptedAt, m.claudeError, m.claudeErrorAt), oauthFailure
			}
			m.quotas = append(m.quotas, quota{
				Provider:    "Claude",
				Product:     "Code",
				Window:      q.Window,
				Remaining:   q.RemainingPercentage,
				ResetAt:     q.ResetsAt,
				UpdatedAt:   updatedAt,
				AttemptedAt: attemptedAt,
				Failure:     failure,
				Source:      source,
				Detail:      detail,
				Stale:       q.Source == claudeOAuthSource && failure != "" || quotaIsStale(updatedAt, q.ResetsAt, now),
			})
		}
	}

	codexCache, codexErr := readCodexCache()
	if codexErr == nil && cacheStateNewerThan(m.codexErrorAt, codexCache.AttemptedAt, codexCache.UpdatedAt) {
		m.codexError, m.codexErrorAt = "", time.Time{}
	}
	codexFailure := currentFailure(codexCache.Failure, m.codexError)
	if codexErr == nil {
		product := "Codex"
		if plan := codexPlanTitle(codexCache.PlanType); plan != "" {
			product += " " + plan
		}
		for _, q := range codexCache.Quotas {
			reset := q.ResetsAt
			m.quotas = append(m.quotas, quota{
				Provider:    "OpenAI",
				Product:     product,
				Window:      q.Window,
				Remaining:   q.RemainingPercentage,
				ResetAt:     &reset,
				UpdatedAt:   codexCache.UpdatedAt,
				AttemptedAt: currentAttempt(codexCache.AttemptedAt, m.codexError, m.codexErrorAt),
				Failure:     codexFailure,
				Source:      "Codex app-server (experimental)",
				Detail:      "Plan type: " + codexCache.PlanType,
				Stale:       codexFailure != "" || quotaIsStale(codexCache.UpdatedAt, &reset, now),
			})
		}
	}

	kimiCache, kimiErr := readKimiCache()
	if kimiErr == nil && cacheStateNewerThan(m.kimiErrorAt, kimiCache.AttemptedAt, kimiCache.UpdatedAt) {
		m.kimiError, m.kimiErrorAt = "", time.Time{}
	}
	kimiFailure := currentFailure(kimiCache.Failure, m.kimiError)
	if kimiErr == nil {
		for _, q := range kimiCache.Quotas {
			reset := q.ResetsAt
			m.quotas = append(m.quotas, quota{
				Provider:    "Kimi",
				Product:     "Code",
				Window:      q.Window,
				Remaining:   q.RemainingPercentage,
				ResetAt:     &reset,
				UpdatedAt:   kimiCache.UpdatedAt,
				AttemptedAt: currentAttempt(kimiCache.AttemptedAt, m.kimiError, m.kimiErrorAt),
				Failure:     kimiFailure,
				Source:      "Kimi local server (experimental)",
				Detail:      fmt.Sprintf("Used: %s of %s", formatPercent(q.Used), formatPercent(q.Limit)),
				Stale:       kimiFailure != "" || quotaIsStale(kimiCache.UpdatedAt, &reset, now),
			})
		}
	}

	zaiCache, zaiErr := readZAICache()
	if zaiErr == nil && cacheStateNewerThan(m.zaiErrorAt, zaiCache.AttemptedAt, zaiCache.UpdatedAt) {
		m.zaiError, m.zaiErrorAt = "", time.Time{}
	}
	zaiFailure := currentFailure(zaiCache.Failure, m.zaiError)
	if zaiErr == nil {
		detail := "Plan level: " + zaiCache.PlanLevel
		for _, q := range zaiCache.Quotas {
			m.quotas = append(m.quotas, quota{
				Provider:    "Z.AI",
				Product:     "Coding Plan",
				Window:      q.Window,
				Remaining:   q.RemainingPercentage,
				ResetAt:     q.ResetsAt,
				UpdatedAt:   zaiCache.UpdatedAt,
				AttemptedAt: currentAttempt(zaiCache.AttemptedAt, m.zaiError, m.zaiErrorAt),
				Failure:     zaiFailure,
				Source:      "Z.AI quota API via Pi (experimental)",
				Detail:      detail,
				Stale:       zaiFailure != "" || quotaIsStale(zaiCache.UpdatedAt, q.ResetsAt, now),
			})
		}
	}

	details := m.unavailableProviderDetails(claudeCache, claudeErr, codexCache, codexErr, kimiCache, kimiErr, zaiCache, zaiErr)
	if len(m.quotas) == 0 {
		m.selected = 0
		m.detail = false
		if m.collecting() {
			m.loadState = "Loading usage data"
		} else {
			m.loadState = "No usage data available"
		}
		m.loadDetail = strings.Join(details, " · ")
		return
	}
	if len(details) > 0 {
		m.loadState, m.loadDetail = "Unavailable providers", strings.Join(details, " · ")
	}
	if selectedKey != "" {
		for i, q := range m.quotas {
			if quotaKey(q) == selectedKey {
				m.selected = i
				return
			}
		}
		m.detail = false
	}
	m.selected = max(0, min(previousSelected, len(m.quotas)-1))
}

func quotaKey(q quota) string {
	return q.Provider + "\x00" + q.Window
}

func currentFailure(cached, transient string) string {
	if transient != "" {
		return transient
	}
	return cached
}

func currentAttempt(cached time.Time, transient string, transientAt time.Time) time.Time {
	if transient != "" {
		return transientAt
	}
	return cached
}

func (m model) unavailableProviderDetails(claude cacheFile, claudeErr error, codex codexCacheFile, codexErr error, kimi kimiCacheFile, kimiErr error, zai zaiCacheFile, zaiErr error) []string {
	var details []string
	if len(claude.Quotas) == 0 {
		if m.claudeError != "" {
			details = append(details, "Claude: "+m.claudeError)
		} else if claudeErr == nil && claude.Failure != "" {
			details = append(details, "Claude: "+claude.Failure)
		} else if claudeErr != nil && !errors.Is(claudeErr, os.ErrNotExist) {
			details = append(details, "Claude cache: "+safeCollectionError(claudeErr))
		} else if !m.claudeCollecting {
			details = append(details, "Claude: open Claude Code and send one prompt")
		}
	}
	if len(codex.Quotas) == 0 {
		if m.codexError != "" {
			details = append(details, "Codex: "+m.codexError)
		} else if codexErr == nil && codex.Failure != "" {
			details = append(details, "Codex: "+codex.Failure)
		} else if codexErr != nil && !errors.Is(codexErr, os.ErrNotExist) {
			details = append(details, "Codex cache: "+safeCollectionError(codexErr))
		} else if !m.codexCollecting {
			details = append(details, "Codex: install the Codex CLI and run codex login; collection is experimental")
		}
	}
	if len(kimi.Quotas) == 0 {
		if m.kimiError != "" {
			details = append(details, "Kimi: "+m.kimiError)
		} else if kimiErr == nil && kimi.Failure != "" {
			details = append(details, "Kimi: "+kimi.Failure)
		} else if kimiErr != nil && !errors.Is(kimiErr, os.ErrNotExist) {
			details = append(details, "Kimi cache: "+safeCollectionError(kimiErr))
		} else if !m.kimiCollecting {
			details = append(details, "Kimi: install Kimi Code and sign in; collection is experimental")
		}
	}
	if len(zai.Quotas) == 0 {
		if m.zaiError != "" {
			details = append(details, "Z.AI: "+m.zaiError)
		} else if zaiErr == nil && zai.Failure != "" {
			details = append(details, "Z.AI: "+zai.Failure)
		} else if zaiErr != nil && !errors.Is(zaiErr, os.ErrNotExist) {
			details = append(details, "Z.AI cache: "+safeCollectionError(zaiErr))
		} else if !m.zaiCollecting {
			details = append(details, "Z.AI: install Pi and run /login zai-coding-cn; collection is experimental")
		}
	}
	return details
}

func quotaIsStale(updatedAt time.Time, resetAt *time.Time, now time.Time) bool {
	return updatedAt.IsZero() || now.Sub(updatedAt) > staleAfter || resetAt != nil && !resetAt.After(now)
}

func newDemoModel() model {
	now := time.Now()
	inTwoHours := now.Add(2*time.Hour + 8*time.Minute)
	inThreeDays := now.Add(3 * 24 * time.Hour)
	return model{demo: true, quotas: []quota{
		{Provider: "Claude", Product: "Max", Window: "5-hour session", Remaining: 24, ResetAt: &inTwoHours, UpdatedAt: now, Source: "demo fixture", Detail: "Demo values only; no account was accessed."},
		{Provider: "Claude", Product: "Max", Window: "Weekly · all", Remaining: 63, ResetAt: &inThreeDays, UpdatedAt: now, Source: "demo fixture", Detail: "Demo values only; no account was accessed."},
		{Provider: "Kimi", Product: "Code", Window: "5-hour", Remaining: 72, ResetAt: &inTwoHours, UpdatedAt: now.Add(-time.Minute), Source: "demo fixture", Detail: "Demo values only; live Kimi collection is available in normal mode."},
		{Provider: "Kimi", Product: "Code", Window: "Weekly", Remaining: 41, ResetAt: &inThreeDays, UpdatedAt: now.Add(-time.Minute), Source: "demo fixture", Detail: "Demo values only; live Kimi collection is available in normal mode."},
		{Provider: "Z.AI", Product: "Coding Plan", Window: "5-hour", Remaining: 88, ResetAt: &inTwoHours, UpdatedAt: now.Add(-time.Minute), Source: "demo fixture", Detail: "Demo values only; live Z.AI collection is available in normal mode."},
		{Provider: "Z.AI", Product: "Coding Plan", Window: "Weekly", Remaining: 91, ResetAt: &inThreeDays, UpdatedAt: now.Add(-time.Minute), Source: "demo fixture", Detail: "Demo values only; live Z.AI collection is available in normal mode."},
	}}
}
