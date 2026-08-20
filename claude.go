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
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	claudeUsageEndpoint     = "https://api.anthropic.com/api/oauth/usage"
	claudeCollectionTimeout = 10 * time.Second
	claudeRequestTimeout    = 5 * time.Second
	maxClaudeResponseSize   = 256 << 10
)

type claudeCredentials struct {
	AccessToken      string `json:"accessToken"`
	ExpiresAt        int64  `json:"expiresAt"`
	RateLimitTier    string `json:"rateLimitTier"`
	SubscriptionType string `json:"subscriptionType"`
}

type claudeCredentialsFile struct {
	ClaudeAIOAuth claudeCredentials `json:"claudeAiOauth"`
}

type claudeUsageResponse struct {
	FiveHour          *claudeUsageWindow `json:"five_hour"`
	SevenDayOAuthApps *claudeUsageWindow `json:"seven_day_oauth_apps"`
	SevenDay          *claudeUsageWindow `json:"seven_day"`
	Limits            []claudeUsageLimit `json:"limits"`
}

type claudeUsageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type claudeUsageLimit struct {
	Scope struct {
		Model struct {
			DisplayName string `json:"display_name"`
			ID          string `json:"id"`
		} `json:"model"`
	} `json:"scope"`
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	ResetsAt *string  `json:"resets_at"`
}

type claudeCredentialLoader func(context.Context) (claudeCredentials, error)

type claudeHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func newClaudeHTTPClient() *http.Client {
	transport := &http.Transport{Proxy: nil}
	return &http.Client{
		Timeout:   claudeRequestTimeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirects are disabled")
		},
	}
}

func collectClaude(ctx context.Context) (cacheFile, error) {
	client := newClaudeHTTPClient()
	if transport, ok := client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}
	return collectClaudeWith(ctx, loadClaudeCredentials, client, time.Now())
}

func collectClaudeWith(parent context.Context, loader claudeCredentialLoader, client claudeHTTPDoer, now time.Time) (cacheFile, error) {
	ctx, cancel := context.WithTimeout(parent, claudeCollectionTimeout)
	defer cancel()

	credentials, err := loader(ctx)
	var quotas []cachedQuota
	if err == nil {
		if strings.TrimSpace(credentials.AccessToken) == "" {
			err = errors.New("Claude OAuth access token is unavailable — run claude login")
		} else if credentials.ExpiresAt <= 0 || !time.UnixMilli(credentials.ExpiresAt).After(now) {
			err = errors.New("Claude OAuth access token is expired — run claude login")
		} else {
			quotas, err = fetchClaudeUsage(ctx, client, credentials.AccessToken, now)
		}
	}
	if parentCollectionCanceled(parent) {
		return cacheFile{}, context.Canceled
	}

	unlock, lockErr := lockClaudeCache()
	if lockErr != nil {
		return cacheFile{}, fmt.Errorf("lock Claude cache: %w", lockErr)
	}
	defer unlock()
	if parentCollectionCanceled(parent) {
		return cacheFile{}, context.Canceled
	}
	previous, readErr := readCache()
	if parentCollectionCanceled(parent) {
		return cacheFile{}, context.Canceled
	}
	if readErr == nil && previous.OAuthAttemptedAt.After(now) {
		return previous, nil
	}
	newerCache := readErr == nil && cacheStateNewerThan(now, previous.UpdatedAt, previous.AttemptedAt)
	cache := cacheFile{Version: cacheVersion, Provider: "Claude", AttemptedAt: now, OAuthAttemptedAt: now}
	if newerCache && err == nil {
		cache = previous
		cache.OAuthAttemptedAt = now
		cache.OAuthFailure = ""
		cache.RateLimitTier = safeClaudeCredentialLabel(credentials.RateLimitTier)
		cache.SubscriptionType = safeClaudeCredentialLabel(credentials.SubscriptionType)
		kept := cache.Quotas[:0]
		for _, quota := range cache.Quotas {
			if quota.Window != "Weekly · Fable" {
				kept = append(kept, quota)
			}
		}
		cache.Quotas = kept
		for _, quota := range quotas {
			if quota.Window == "Weekly · Fable" {
				cache.Quotas = append(cache.Quotas, quota)
			}
		}
	} else if err != nil {
		if readErr == nil {
			cache = previous
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return cacheFile{}, fmt.Errorf("preserve Claude cache after failed collection: %w", readErr)
		}
		failure := safeCollectionError(err)
		if !newerCache {
			cache.AttemptedAt = now
			cache.Failure = failure
		}
		cache.OAuthAttemptedAt = now
		cache.OAuthFailure = failure
	} else {
		cache.UpdatedAt = now
		cache.AttemptedAt = now
		cache.OAuthAttemptedAt = now
		cache.OAuthFailure = ""
		cache.Quotas = quotas
		cache.RateLimitTier = safeClaudeCredentialLabel(credentials.RateLimitTier)
		cache.SubscriptionType = safeClaudeCredentialLabel(credentials.SubscriptionType)
	}
	if validateErr := validateCache(cache, now); validateErr != nil {
		return cacheFile{}, fmt.Errorf("validate Claude OAuth usage: %w", validateErr)
	}
	path, pathErr := cachePath()
	if pathErr != nil {
		return cacheFile{}, pathErr
	}
	if parentCollectionCanceled(parent) {
		return cacheFile{}, context.Canceled
	}
	if writeErr := writeJSONCache(path, cache); writeErr != nil {
		return cacheFile{}, fmt.Errorf("store Claude cache: %w", writeErr)
	}
	return cache, err
}

func fetchClaudeUsage(ctx context.Context, client claudeHTTPDoer, token string, now time.Time) ([]cachedQuota, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeUsageEndpoint, nil)
	if err != nil {
		return nil, errors.New("create Claude OAuth usage request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("anthropic-beta", "oauth-2025-04-20")
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("request Claude OAuth usage failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusTooManyRequests {
			if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds >= 0 && seconds <= 24*60*60 {
				return nil, fmt.Errorf("HTTP 429 · Claude OAuth usage · retry after %ds", seconds)
			}
		}
		return nil, fmt.Errorf("HTTP %d · Claude OAuth usage", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxClaudeResponseSize+1))
	if err != nil {
		return nil, errors.New("read Claude OAuth usage response failed")
	}
	if len(body) > maxClaudeResponseSize {
		return nil, errors.New("Claude OAuth usage response exceeded size limit")
	}
	return parseClaudeUsage(body, now)
}

func parseClaudeUsage(body []byte, now time.Time) ([]cachedQuota, error) {
	var response claudeUsageResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&response); err != nil {
		return nil, errors.New("Claude OAuth usage response was invalid JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("Claude OAuth usage response contained multiple values")
	}

	windows := []struct {
		name string
		data *claudeUsageWindow
	}{
		{"5-hour session", response.FiveHour},
		{"Weekly · all", response.SevenDayOAuthApps},
	}
	if windows[1].data == nil {
		windows[1].data = response.SevenDay
	}
	quotas := make([]cachedQuota, 0, 3)
	for _, window := range windows {
		if window.data == nil {
			continue
		}
		quota, err := parseClaudePercentage(window.name, window.data.Utilization, window.data.ResetsAt, now)
		if err != nil {
			return nil, err
		}
		quotas = append(quotas, quota)
	}
	for _, limit := range response.Limits {
		model := limit.Scope.Model.DisplayName
		if model == "" {
			model = limit.Scope.Model.ID
		}
		kind := strings.ToLower(limit.Kind)
		if !strings.Contains(strings.ToLower(model), "fable") || !strings.Contains(kind, "week") && !strings.Contains(kind, "day") {
			continue
		}
		quota, err := parseClaudePercentage("Weekly · Fable", limit.Percent, limit.ResetsAt, now)
		if err != nil {
			return nil, err
		}
		quotas = append(quotas, quota)
		break
	}
	if len(quotas) == 0 {
		return nil, errors.New("Claude OAuth usage returned no usable quota windows")
	}
	return quotas, nil
}

func parseClaudePercentage(name string, used *float64, resetText *string, now time.Time) (cachedQuota, error) {
	if used == nil || math.IsNaN(*used) || math.IsInf(*used, 0) || *used < 0 || *used > 100 {
		return cachedQuota{}, fmt.Errorf("%s percentage must be between 0 and 100", name)
	}
	if resetText == nil {
		return cachedQuota{}, fmt.Errorf("%s resets_at is missing", name)
	}
	reset, err := time.Parse(time.RFC3339, *resetText)
	if err != nil || reset.Before(now.Add(-5*time.Minute)) || reset.After(now.Add(8*24*time.Hour)) {
		return cachedQuota{}, fmt.Errorf("%s resets_at is invalid", name)
	}
	return cachedQuota{
		Window:              name,
		RemainingPercentage: 100 - *used,
		ResetsAt:            &reset,
		Source:              claudeOAuthSource,
		CollectedAt:         now,
	}, nil
}

func claudePlanTitle(subscriptionType, rateLimitTier string) string {
	switch strings.ToLower(rateLimitTier) {
	case "default_claude_max_5x":
		return "Max 5×"
	case "default_claude_max_20x":
		return "Max 20×"
	}
	switch strings.ToLower(subscriptionType) {
	case "free", "pro", "max", "team", "enterprise":
		return planTitle(strings.ToLower(subscriptionType))
	default:
		return ""
	}
}

func safeClaudeCredentialLabel(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		return ""
	}
	for _, char := range value {
		if !unicode.IsPrint(char) {
			return ""
		}
	}
	return value
}

func parseClaudeCredentials(data []byte) (claudeCredentials, error) {
	var file claudeCredentialsFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&file); err != nil {
		return claudeCredentials{}, errors.New("Claude credentials are invalid — run claude login")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return claudeCredentials{}, errors.New("Claude credentials are invalid — run claude login")
	}
	return file.ClaudeAIOAuth, nil
}

func compactClaudeUsage(cache cacheFile) string { return compactUsage(cache.Quotas) }
