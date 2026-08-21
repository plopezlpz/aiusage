package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	codexCollectionTimeout = 10 * time.Second
	codexCleanupTimeout    = 2 * time.Second
	maxRPCMessages         = 1000
)

type codexCacheFile struct {
	Version     int                `json:"version"`
	Provider    string             `json:"provider"`
	PlanType    string             `json:"plan_type,omitempty"`
	UpdatedAt   time.Time          `json:"updated_at"`
	AttemptedAt time.Time          `json:"attempted_at,omitempty"`
	Failure     string             `json:"failure,omitempty"`
	Quotas      []codexCachedQuota `json:"quotas"`
}

type codexCachedQuota struct {
	Window              string    `json:"window"`
	WindowDurationMins  int64     `json:"window_duration_mins"`
	RemainingPercentage float64   `json:"remaining_percentage"`
	ResetsAt            time.Time `json:"resets_at"`
}

type codexRateLimitResult struct {
	RateLimits struct {
		PlanType  *string      `json:"planType"`
		Primary   *codexWindow `json:"primary"`
		Secondary *codexWindow `json:"secondary"`
	} `json:"rateLimits"`
}

type codexWindow struct {
	UsedPercent        *float64 `json:"usedPercent"`
	WindowDurationMins *int64   `json:"windowDurationMins"`
	ResetsAt           *int64   `json:"resetsAt"`
}

type accountReadResult struct {
	Account *struct {
		Type string `json:"type"`
	} `json:"account"`
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code int `json:"code"`
	} `json:"error"`
}

type codexCommandFactory func(context.Context) *exec.Cmd

var newCodexCommand codexCommandFactory = func(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "codex", "-s", "read-only", "-a", "never", "app-server")
}

func codexCachePath() (string, error) {
	return providerCachePath("codex.json")
}

func collectCodex(ctx context.Context) (codexCacheFile, error) {
	return collectCodexWith(ctx, newCodexCommand, time.Now())
}

func collectCodexWith(parent context.Context, factory codexCommandFactory, now time.Time) (codexCacheFile, error) {
	if !codexProcessGroupsSupported() {
		return codexCacheFile{}, errors.New("Codex collection is supported only on Unix")
	}
	unlock, err := lockCodexCache()
	if err != nil {
		return codexCacheFile{}, fmt.Errorf("lock Codex cache: %w", err)
	}
	defer unlock()
	if parentCollectionCanceled(parent) {
		return codexCacheFile{}, context.Canceled
	}

	ctx, cancel := context.WithTimeout(parent, codexCollectionTimeout)
	defer cancel()
	plan, quotas, collectErr := fetchCodex(ctx, factory)
	if parentCollectionCanceled(parent) {
		return codexCacheFile{}, context.Canceled
	}
	previous, readErr := readCodexCache()
	if parentCollectionCanceled(parent) {
		return codexCacheFile{}, context.Canceled
	}
	if readErr == nil && cacheStateNewerThan(now, previous.UpdatedAt, previous.AttemptedAt) {
		return previous, nil
	}
	cache := codexCacheFile{Version: cacheVersion, Provider: "OpenAI Codex", AttemptedAt: now}
	if collectErr != nil {
		if readErr == nil {
			cache = previous
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return codexCacheFile{}, fmt.Errorf("preserve Codex cache after failed collection: %w", readErr)
		}
		cache.AttemptedAt = now
		cache.Failure = safeCollectionError(collectErr)
	} else {
		cache.PlanType = plan
		cache.UpdatedAt = now
		cache.Quotas = quotas
	}
	if err := validateCodexCache(cache, now); err != nil {
		return codexCacheFile{}, fmt.Errorf("validate Codex data: %w", err)
	}
	path, err := codexCachePath()
	if err != nil {
		return codexCacheFile{}, err
	}
	if parentCollectionCanceled(parent) {
		return codexCacheFile{}, context.Canceled
	}
	if err := writeJSONCache(path, cache); err != nil {
		return codexCacheFile{}, fmt.Errorf("store Codex cache: %w", err)
	}
	return cache, collectErr
}

func fetchCodex(ctx context.Context, factory codexCommandFactory) (string, []codexCachedQuota, error) {
	if !codexProcessGroupsSupported() {
		return "", nil, errors.New("Codex collection is supported only on Unix")
	}
	cmd := factory(ctx)
	configureCodexCommand(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", nil, fmt.Errorf("open Codex app-server input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return "", nil, fmt.Errorf("open Codex app-server output: %w", err)
	}
	// A direct null-device descriptor avoids os/exec's copy goroutine, which can
	// otherwise outlive the child when a descendant inherits stderr.
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return "", nil, fmt.Errorf("open null device for Codex app-server: %w", err)
	}
	cmd.Stderr = null
	if err := cmd.Start(); err != nil {
		_ = null.Close()
		_ = stdin.Close()
		_ = stdout.Close()
		return "", nil, fmt.Errorf("start Codex app-server: %w", err)
	}
	processGroup, err := codexCommandProcessGroup(cmd)
	_ = null.Close()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		terminateCodexCommand(cmd, 0)
		_ = cmd.Wait()
		return "", nil, fmt.Errorf("record Codex app-server process group: %w", err)
	}

	var terminateOnce sync.Once
	terminate := func() {
		terminateOnce.Do(func() {
			// Closing both pipes makes blocked scanner/encoder IO observe cancellation even
			// when an app-server descendant inherited the other end of a pipe.
			_ = stdin.Close()
			_ = stdout.Close()
			terminateCodexCommand(cmd, processGroup)
		})
	}
	processDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(processDone)
	}()
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			terminate()
		case <-processDone:
		}
	}()
	defer func() {
		terminate()
		select {
		case <-processDone:
		case <-time.After(codexCleanupTimeout):
		}
		select {
		case <-watcherDone:
		case <-time.After(codexCleanupTimeout):
		}
	}()

	encoder := json.NewEncoder(stdin)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), maxInputSize)
	if err := encoder.Encode(map[string]any{
		"id": 1, "method": "initialize",
		"params": map[string]any{"clientInfo": map[string]string{"name": "aiusage", "version": "1"}},
	}); err != nil {
		return "", nil, fmt.Errorf("initialize Codex app-server: %w", err)
	}
	if _, err := readRPCResponses(ctx, scanner, map[int]string{1: "initialize"}); err != nil {
		return "", nil, err
	}
	if err := encoder.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return "", nil, fmt.Errorf("notify Codex app-server initialization: %w", err)
	}
	if err := encoder.Encode(map[string]any{"id": 2, "method": "account/read", "params": map[string]any{}}); err != nil {
		return "", nil, fmt.Errorf("request Codex account: %w", err)
	}
	if err := encoder.Encode(map[string]any{"id": 3, "method": "account/rateLimits/read", "params": map[string]any{}}); err != nil {
		return "", nil, fmt.Errorf("request Codex rate limits: %w", err)
	}
	responses, err := readRPCResponses(ctx, scanner, map[int]string{2: "account/read", 3: "account/rateLimits/read"})
	if err != nil {
		return "", nil, err
	}
	var account accountReadResult
	if err := json.Unmarshal(responses[2], &account); err != nil {
		return "", nil, fmt.Errorf("parse Codex account response: %w", err)
	}
	if account.Account == nil {
		return "", nil, errors.New("Codex is not signed in — run codex login")
	}
	return parseCodexRateLimits(responses[3], time.Now())
}

func readRPCResponses(ctx context.Context, scanner *bufio.Scanner, wanted map[int]string) (map[int]json.RawMessage, error) {
	results := make(map[int]json.RawMessage, len(wanted))
	for messages := 0; len(results) < len(wanted) && scanner.Scan(); messages++ {
		if messages >= maxRPCMessages {
			return nil, errors.New("Codex app-server sent too many messages")
		}
		var response rpcResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			return nil, fmt.Errorf("parse Codex app-server response: %w", err)
		}
		if len(response.ID) == 0 || string(response.ID) == "null" {
			continue // Ignore unrelated notifications.
		}
		var id int
		if err := json.Unmarshal(response.ID, &id); err != nil {
			continue
		}
		method, ok := wanted[id]
		if !ok {
			continue
		}
		if response.Error != nil {
			return nil, fmt.Errorf("Codex app-server %s failed (%d)", method, response.Error.Code)
		}
		if len(response.Result) == 0 || string(response.Result) == "null" {
			return nil, fmt.Errorf("Codex app-server %s returned no result", method)
		}
		results[id] = append(json.RawMessage(nil), response.Result...)
	}
	if len(results) == len(wanted) {
		return results, nil
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("Codex app-server timed out")
		}
		return nil, fmt.Errorf("read Codex app-server response: %w", err)
	}
	if ctx.Err() != nil {
		return nil, errors.New("Codex app-server timed out")
	}
	return nil, errors.New("Codex app-server exited before replying")
}

func parseCodexRateLimits(data []byte, now time.Time) (string, []codexCachedQuota, error) {
	var result codexRateLimitResult
	if err := json.Unmarshal(data, &result); err != nil {
		return "", nil, fmt.Errorf("parse Codex rate limits: %w", err)
	}
	if result.RateLimits.PlanType == nil || !knownCodexPlan(*result.RateLimits.PlanType) {
		return "", nil, errors.New("Codex rate limits have an invalid planType")
	}
	windows := []*codexWindow{result.RateLimits.Primary, result.RateLimits.Secondary}
	quotas := make([]codexCachedQuota, 0, len(windows))
	for _, window := range windows {
		if window == nil {
			continue
		}
		if window.UsedPercent == nil || math.IsNaN(*window.UsedPercent) || math.IsInf(*window.UsedPercent, 0) || *window.UsedPercent < 0 || *window.UsedPercent > 100 || math.Trunc(*window.UsedPercent) != *window.UsedPercent {
			return "", nil, errors.New("Codex usedPercent must be an integer between 0 and 100")
		}
		if window.WindowDurationMins == nil || *window.WindowDurationMins <= 0 || *window.WindowDurationMins > 365*24*60 {
			return "", nil, errors.New("Codex windowDurationMins is invalid")
		}
		if window.ResetsAt == nil {
			return "", nil, errors.New("Codex resetsAt is missing")
		}
		reset := time.Unix(*window.ResetsAt, 0)
		if reset.Year() < 2020 || reset.After(now.Add(366*24*time.Hour)) {
			return "", nil, errors.New("Codex resetsAt is implausible")
		}
		quotas = append(quotas, codexCachedQuota{
			Window:              codexWindowLabel(*window.WindowDurationMins),
			WindowDurationMins:  *window.WindowDurationMins,
			RemainingPercentage: 100 - *window.UsedPercent,
			ResetsAt:            reset,
		})
	}
	if len(quotas) == 0 {
		return "", nil, errors.New("Codex returned no usable rate limits")
	}
	return *result.RateLimits.PlanType, quotas, nil
}

func codexWindowLabel(minutes int64) string {
	if minutes == 10080 {
		return "Weekly"
	}
	if minutes%1440 == 0 {
		return fmt.Sprintf("%dd", minutes/1440)
	}
	if minutes%60 == 0 {
		return fmt.Sprintf("%dh", minutes/60)
	}
	if minutes > 60 {
		return fmt.Sprintf("%dh %dm", minutes/60, minutes%60)
	}
	return fmt.Sprintf("%dm", minutes)
}

func knownCodexPlan(plan string) bool {
	switch plan {
	case "free", "go", "plus", "pro", "prolite", "team", "self_serve_business_prolite", "self_serve_business_usage_based", "business", "ent26", "enterprise_cbp_automation", "enterprise_cbp_usage_based", "enterprise", "edu", "unknown":
		return true
	default:
		return false
	}
}

func validateCodexCache(cache codexCacheFile, now time.Time) error {
	if cache.Version != cacheVersion || cache.Provider != "OpenAI Codex" {
		return errors.New("unsupported Codex cache format")
	}
	if len(cache.Quotas) > 0 {
		if !knownCodexPlan(cache.PlanType) {
			return errors.New("Codex cache plan_type is invalid")
		}
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
		return errors.New("Codex cache failure is missing attempted_at")
	}
	if cache.Failure != safeCollectionError(errors.New(cache.Failure)) {
		return errors.New("Codex cache failure is invalid")
	}
	seen := make(map[string]bool, len(cache.Quotas))
	for _, q := range cache.Quotas {
		if q.WindowDurationMins <= 0 || q.WindowDurationMins > 365*24*60 || q.Window != codexWindowLabel(q.WindowDurationMins) {
			return errors.New("Codex cache window is invalid")
		}
		if seen[q.Window] {
			return fmt.Errorf("duplicate Codex quota window %q", q.Window)
		}
		seen[q.Window] = true
		if math.IsNaN(q.RemainingPercentage) || math.IsInf(q.RemainingPercentage, 0) || q.RemainingPercentage < 0 || q.RemainingPercentage > 100 {
			return fmt.Errorf("%s remaining_percentage must be between 0 and 100", q.Window)
		}
		if q.ResetsAt.Year() < 2020 || q.ResetsAt.After(now.Add(366*24*time.Hour)) {
			return fmt.Errorf("%s resets_at is implausible", q.Window)
		}
	}
	return nil
}

func readCodexCache() (codexCacheFile, error) {
	path, err := codexCachePath()
	if err != nil {
		return codexCacheFile{}, err
	}
	data, err := readBoundedCache(path)
	if err != nil {
		return codexCacheFile{}, err
	}
	var cache codexCacheFile
	if err := json.Unmarshal(data, &cache); err != nil {
		return codexCacheFile{}, err
	}
	if err := validateCodexCache(cache, time.Now()); err != nil {
		return codexCacheFile{}, err
	}
	return cache, nil
}

func planTitle(plan string) string {
	if plan == "unknown" || plan == "" {
		return ""
	}
	parts := strings.Split(plan, "_")
	for i, part := range parts {
		if part != "" {
			runes := []rune(part)
			runes[0] = unicode.ToUpper(runes[0])
			parts[i] = string(runes)
		}
	}
	return strings.Join(parts, " ")
}

func compactCodexUsage(cache codexCacheFile) string {
	parts := make([]string, 0, len(cache.Quotas))
	for _, q := range cache.Quotas {
		parts = append(parts, fmt.Sprintf("%s %.0f%% left", q.Window, q.RemainingPercentage))
	}
	return "OpenAI Codex " + strings.Join(parts, " · ")
}
