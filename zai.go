package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	zaiUsageEndpoint      = "https://open.bigmodel.cn/api/monitor/usage/quota/limit"
	zaiCollectionTimeout  = 10 * time.Second
	zaiCredentialTimeout  = 4 * time.Second
	zaiRequestTimeout     = 5 * time.Second
	maxZAICredentialSize  = 4096
	maxZAIResponseSize    = 256 << 10
	zaiCredentialProvider = "zai-coding-cn"
)

type zaiCacheFile struct {
	Version     int              `json:"version"`
	Provider    string           `json:"provider"`
	PlanLevel   string           `json:"plan_level,omitempty"`
	UpdatedAt   time.Time        `json:"updated_at"`
	AttemptedAt time.Time        `json:"attempted_at,omitempty"`
	Failure     string           `json:"failure,omitempty"`
	Quotas      []zaiCachedQuota `json:"quotas"`
}

type zaiCachedQuota struct {
	Window              string     `json:"window"`
	Unit                int64      `json:"unit"`
	UsedPercentage      float64    `json:"used_percentage"`
	RemainingPercentage float64    `json:"remaining_percentage"`
	ResetsAt            *time.Time `json:"resets_at,omitempty"`
}

type zaiUsageResponse struct {
	Success *bool `json:"success"`
	Code    *int  `json:"code"`
	Data    *struct {
		Level  string          `json:"level"`
		Limits []zaiUsageLimit `json:"limits"`
	} `json:"data"`
}

type zaiUsageLimit struct {
	Type          string   `json:"type"`
	Unit          *int64   `json:"unit"`
	Percentage    *float64 `json:"percentage"`
	NextResetTime *int64   `json:"nextResetTime"`
}

type zaiCommandFactory func(context.Context) *exec.Cmd
type zaiHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

var newZAICommand zaiCommandFactory = func(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "pi", "auth", "print-api-key", "--provider", zaiCredentialProvider)
}

func zaiCachePath() (string, error) { return providerCachePath("zai.json") }

func newZAIHTTPClient() *http.Client {
	return &http.Client{
		Timeout: zaiRequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func collectZAI(ctx context.Context) (zaiCacheFile, error) {
	return collectZAIWith(ctx, newZAICommand, newZAIHTTPClient(), time.Now())
}

func collectZAIWith(parent context.Context, factory zaiCommandFactory, client zaiHTTPDoer, now time.Time) (zaiCacheFile, error) {
	if !zaiProcessGroupsSupported() {
		return zaiCacheFile{}, errors.New("Z.AI collection is supported only on Unix")
	}
	unlock, err := lockZAICache()
	if err != nil {
		return zaiCacheFile{}, fmt.Errorf("lock Z.AI cache: %w", err)
	}
	defer unlock()
	if parentCollectionCanceled(parent) {
		return zaiCacheFile{}, context.Canceled
	}

	ctx, cancel := context.WithTimeout(parent, zaiCollectionTimeout)
	defer cancel()
	plan, quotas, collectErr := fetchZAI(ctx, factory, client, now)
	if parentCollectionCanceled(parent) {
		return zaiCacheFile{}, context.Canceled
	}
	previous, readErr := readZAICache()
	if parentCollectionCanceled(parent) {
		return zaiCacheFile{}, context.Canceled
	}
	if readErr == nil && cacheStateNewerThan(now, previous.UpdatedAt, previous.AttemptedAt) {
		return previous, nil
	}
	cache := zaiCacheFile{Version: cacheVersion, Provider: "Z.AI Coding Plan", AttemptedAt: now}
	if collectErr != nil {
		if readErr == nil {
			cache = previous
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return zaiCacheFile{}, fmt.Errorf("preserve Z.AI cache after failed collection: %w", readErr)
		}
		cache.AttemptedAt = now
		cache.Failure = safeCollectionError(collectErr)
	} else {
		cache.PlanLevel = plan
		cache.UpdatedAt = now
		cache.Quotas = quotas
	}
	if err := validateZAICache(cache, now); err != nil {
		return zaiCacheFile{}, fmt.Errorf("validate Z.AI data: %w", err)
	}
	path, err := zaiCachePath()
	if err != nil {
		return zaiCacheFile{}, err
	}
	if parentCollectionCanceled(parent) {
		return zaiCacheFile{}, context.Canceled
	}
	if err := writeJSONCache(path, cache); err != nil {
		return zaiCacheFile{}, fmt.Errorf("store Z.AI cache: %w", err)
	}
	return cache, collectErr
}

func fetchZAI(ctx context.Context, factory zaiCommandFactory, client zaiHTTPDoer, now time.Time) (string, []zaiCachedQuota, error) {
	key, err := loadZAIAPIKey(ctx, factory)
	if err != nil {
		return "", nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, zaiUsageEndpoint, nil)
	if err != nil {
		return "", nil, errors.New("create Z.AI usage request")
	}
	request.Header.Set("Authorization", key)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Language", "en-US,en")
	response, err := client.Do(request)
	if err != nil {
		return "", nil, errors.New("request Z.AI usage failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("Z.AI usage endpoint returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxZAIResponseSize+1))
	if err != nil {
		return "", nil, errors.New("read Z.AI usage response failed")
	}
	if len(body) > maxZAIResponseSize {
		return "", nil, errors.New("Z.AI usage response exceeded size limit")
	}
	return parseZAIUsage(body, now)
}

func loadZAIAPIKey(parent context.Context, factory zaiCommandFactory) (string, error) {
	ctx, cancel := context.WithTimeout(parent, zaiCredentialTimeout)
	defer cancel()
	cmd := factory(ctx)
	configureZAICommand(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", errors.New("Pi Z.AI credential output is unavailable")
	}
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		_ = stdout.Close()
		return "", errors.New("Pi Z.AI credential error output is unavailable")
	}
	cmd.Stderr = null
	if err := cmd.Start(); err != nil {
		_ = null.Close()
		_ = stdout.Close()
		return "", errors.New("Pi Z.AI credentials are unavailable — run /login zai-coding-cn in Pi")
	}
	_ = null.Close()
	group, err := zaiCommandProcessGroup(cmd)
	if err != nil {
		_ = stdout.Close()
		terminateZAICommand(cmd, 0)
		_ = cmd.Wait()
		return "", errors.New("Pi Z.AI credential command could not be isolated")
	}
	var terminateOnce sync.Once
	terminate := func() {
		terminateOnce.Do(func() {
			_ = stdout.Close()
			terminateZAICommand(cmd, group)
		})
	}
	type commandResult struct {
		output []byte
		err    error
	}
	done := make(chan commandResult, 1)
	go func() {
		output, readErr := io.ReadAll(io.LimitReader(stdout, maxZAICredentialSize+1))
		waitErr := cmd.Wait()
		if readErr != nil {
			waitErr = readErr
		}
		done <- commandResult{output: output, err: waitErr}
	}()
	var result commandResult
	select {
	case result = <-done:
	case <-ctx.Done():
		terminate()
		<-done
		return "", errors.New("Pi Z.AI credential export timed out")
	}
	if result.err != nil {
		return "", errors.New("Pi Z.AI credentials are unavailable — run /login zai-coding-cn in Pi")
	}
	if len(result.output) > maxZAICredentialSize {
		return "", errors.New("Pi returned an invalid Z.AI API key")
	}
	key := strings.TrimSuffix(strings.TrimSuffix(string(result.output), "\n"), "\r")
	if key == "" || len(key) > maxZAICredentialSize || strings.TrimSpace(key) != key {
		return "", errors.New("Pi returned an invalid Z.AI API key")
	}
	for _, r := range key {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", errors.New("Pi returned an invalid Z.AI API key")
		}
	}
	return key, nil
}

func parseZAIUsage(body []byte, now time.Time) (string, []zaiCachedQuota, error) {
	var response zaiUsageResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&response); err != nil {
		return "", nil, errors.New("Z.AI usage response was invalid JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", nil, errors.New("Z.AI usage response contained multiple values")
	}
	if response.Success == nil || !*response.Success || response.Code == nil || *response.Code != http.StatusOK || response.Data == nil {
		return "", nil, errors.New("Z.AI usage endpoint did not return a successful result")
	}
	plan, err := safeZAIPlan(response.Data.Level)
	if err != nil {
		return "", nil, err
	}
	byUnit := make(map[int64]zaiCachedQuota, 2)
	for _, limit := range response.Data.Limits {
		typeName := strings.ToUpper(strings.TrimSpace(limit.Type))
		if typeName != "TOKENS_LIMIT" && typeName != "CREDIT_LIMIT" {
			continue
		}
		if limit.Unit == nil || (*limit.Unit != 3 && *limit.Unit != 6) {
			continue
		}
		unit := *limit.Unit
		if _, exists := byUnit[unit]; exists {
			return "", nil, fmt.Errorf("Z.AI returned duplicate %s quota window", zaiWindowLabel(unit))
		}
		if limit.Percentage == nil || math.IsNaN(*limit.Percentage) || math.IsInf(*limit.Percentage, 0) || *limit.Percentage < 0 || *limit.Percentage > 100 {
			return "", nil, errors.New("Z.AI quota percentage is invalid")
		}
		reset, err := parseZAIReset(limit.NextResetTime, now)
		if err != nil {
			return "", nil, err
		}
		byUnit[unit] = zaiCachedQuota{Window: zaiWindowLabel(unit), Unit: unit, UsedPercentage: *limit.Percentage, RemainingPercentage: 100 - *limit.Percentage, ResetsAt: reset}
	}
	fiveHour, ok := byUnit[3]
	if !ok {
		return "", nil, errors.New("Z.AI returned no 5-hour Coding Plan quota")
	}
	quotas := []zaiCachedQuota{fiveHour}
	if weekly, ok := byUnit[6]; ok {
		quotas = append(quotas, weekly)
	}
	return plan, quotas, nil
}

func parseZAIReset(milliseconds *int64, now time.Time) (*time.Time, error) {
	if milliseconds == nil {
		return nil, nil
	}
	if *milliseconds <= 0 {
		return nil, errors.New("Z.AI quota reset time is invalid")
	}
	reset := time.UnixMilli(*milliseconds)
	if reset.Year() < 2020 || reset.After(now.Add(8*24*time.Hour)) {
		return nil, errors.New("Z.AI quota reset time is invalid")
	}
	return &reset, nil
}

func safeZAIPlan(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if value != trimmed || len(trimmed) > 64 || strings.IndexFunc(trimmed, unicode.IsControl) >= 0 {
		return "", errors.New("Z.AI plan level is invalid")
	}
	return trimmed, nil
}

func zaiWindowLabel(unit int64) string {
	if unit == 3 {
		return "5-hour"
	}
	return "Weekly"
}

func readZAICache() (zaiCacheFile, error) {
	path, err := zaiCachePath()
	if err != nil {
		return zaiCacheFile{}, err
	}
	data, err := readBoundedCache(path)
	if err != nil {
		return zaiCacheFile{}, err
	}
	var cache zaiCacheFile
	if err := json.Unmarshal(data, &cache); err != nil {
		return zaiCacheFile{}, err
	}
	if err := validateZAICache(cache, time.Now()); err != nil {
		return zaiCacheFile{}, err
	}
	return cache, nil
}

func validateZAICache(cache zaiCacheFile, now time.Time) error {
	if cache.Version != cacheVersion || cache.Provider != "Z.AI Coding Plan" {
		return errors.New("unsupported Z.AI cache format")
	}
	if _, err := safeZAIPlan(cache.PlanLevel); err != nil {
		return err
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
		return errors.New("Z.AI cache failure is missing attempted_at")
	}
	if cache.Failure != safeCollectionError(errors.New(cache.Failure)) {
		return errors.New("Z.AI cache failure is invalid")
	}
	for i, quota := range cache.Quotas {
		wantUnit := int64(3)
		if i == 1 {
			wantUnit = 6
		} else if i > 1 {
			return errors.New("Z.AI cache contains too many quota windows")
		}
		if quota.Unit != wantUnit || quota.Window != zaiWindowLabel(quota.Unit) {
			return errors.New("Z.AI cache window is invalid")
		}
		if math.IsNaN(quota.UsedPercentage) || math.IsInf(quota.UsedPercentage, 0) || quota.UsedPercentage < 0 || quota.UsedPercentage > 100 || math.IsNaN(quota.RemainingPercentage) || math.IsInf(quota.RemainingPercentage, 0) || quota.RemainingPercentage < 0 || quota.RemainingPercentage > 100 || math.Abs(quota.RemainingPercentage-(100-quota.UsedPercentage)) > 1e-9 {
			return errors.New("Z.AI cache percentage is invalid")
		}
		if quota.ResetsAt != nil && (quota.ResetsAt.Year() < 2020 || quota.ResetsAt.After(now.Add(8*24*time.Hour))) {
			return errors.New("Z.AI cache reset time is invalid")
		}
	}
	return nil
}

func compactZAIUsage(cache zaiCacheFile) string {
	parts := make([]string, 0, len(cache.Quotas))
	for _, quota := range cache.Quotas {
		parts = append(parts, fmt.Sprintf("%s %.0f%% left", quota.Window, quota.RemainingPercentage))
	}
	return "Z.AI Coding Plan " + strings.Join(parts, " · ")
}
