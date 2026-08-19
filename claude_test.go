package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type claudeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f claudeRoundTripFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

func TestFetchClaudeUsageUsesExactPrivateOAuthRequestAndParsesPercentages(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	body := `{
		"five_hour":{"utilization":25.5,"resets_at":"2026-01-01T14:00:00Z"},
		"seven_day_oauth_apps":{"utilization":40,"resets_at":"2026-01-05T12:00:00Z"},
		"seven_day":{"utilization":99,"resets_at":"2026-01-06T12:00:00Z"},
		"limits":[
			{"scope":{"model":{"display_name":"Other"}},"kind":"weekly","percent":80,"resets_at":"2026-01-05T12:00:00Z"},
			{"scope":{"model":{"display_name":"Fable 5"}},"kind":"weekly_scoped","percent":12.5,"resets_at":"2026-01-05T12:00:00Z"}
		]
	}`
	client := claudeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != claudeUsageEndpoint {
			t.Fatalf("request = %s %s", request.Method, request.URL)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Fatalf("authorization header = %q", got)
		}
		if request.Header.Get("Accept") != "application/json" || request.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Fatalf("headers = %#v", request.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	quotas, err := fetchClaudeUsage(context.Background(), client, "test-secret", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(quotas) != 3 {
		t.Fatalf("quotas = %#v", quotas)
	}
	if quotas[0].Window != "5-hour session" || quotas[0].RemainingPercentage != 74.5 || quotas[1].RemainingPercentage != 60 {
		t.Fatalf("standard quotas = %#v", quotas[:2])
	}
	if quotas[2].Window != "Weekly · Fable" || quotas[2].RemainingPercentage != 87.5 || quotas[2].Source != claudeOAuthSource {
		t.Fatalf("Fable quota = %#v", quotas[2])
	}
	if got := compactUsage(quotas); got != "Claude 5h 74% left · 7d 60% left · Fable 88% left" {
		t.Fatalf("compact usage = %q", got)
	}
}

func TestClaudeUsageDoesNotScaleFractionsOrLeakTokenAndBody(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	quotas, err := parseClaudeUsage([]byte(`{"five_hour":{"utilization":0.5,"resets_at":"2026-01-01T14:00:00Z"}}`), now)
	if err != nil || len(quotas) != 1 || quotas[0].RemainingPercentage != 99.5 {
		t.Fatalf("fractional percentage was scaled: quotas=%#v err=%v", quotas, err)
	}
	if _, err := parseClaudeUsage([]byte(`{"five_hour":{"utilization":1,"resets_at":"2026-01-01T11:00:00Z"}}`), now); err == nil {
		t.Fatal("accepted an expired reset as a successful collection")
	}

	const secret = "never-print-this-token"
	client := claudeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New(secret)
	})
	_, err = fetchClaudeUsage(context.Background(), client, secret, now)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("request error leaked token: %v", err)
	}
	client = claudeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"` + secret + `"`))}, nil
	})
	_, err = fetchClaudeUsage(context.Background(), client, secret, now)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("response error leaked body: %v", err)
	}
	client = claudeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"120"}}, Body: http.NoBody}, nil
	})
	_, err = fetchClaudeUsage(context.Background(), client, secret, now)
	if err == nil || err.Error() != "HTTP 429 · Claude OAuth usage · retry after 120s" {
		t.Fatalf("rate-limit error = %v", err)
	}
}

func TestCollectClaudeUsesInjectedCredentialsAndPreservesGoodCacheOnFailure(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	t.Setenv("XDG_CACHE_HOME", temp)
	now := time.Now()
	loader := func(context.Context) (claudeCredentials, error) {
		return claudeCredentials{AccessToken: "secret", ExpiresAt: now.Add(time.Hour).UnixMilli(), RateLimitTier: "default", SubscriptionType: "max"}, nil
	}
	responseBody := fmt.Sprintf(`{"five_hour":{"utilization":20,"resets_at":%q}}`, now.Add(2*time.Hour).Format(time.RFC3339))
	client := claudeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(responseBody))}, nil
	})
	first, err := collectClaudeWith(context.Background(), loader, client, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.SubscriptionType != "max" || len(first.Quotas) != 1 || !first.OAuthAttemptedAt.Equal(now) || first.OAuthFailure != "" {
		t.Fatalf("cache = %#v", first)
	}
	failedLoader := func(context.Context) (claudeCredentials, error) {
		return claudeCredentials{}, errors.New("credentials unavailable")
	}
	second, err := collectClaudeWith(context.Background(), failedLoader, client, now.Add(time.Minute))
	if err == nil {
		t.Fatal("expected collection failure")
	}
	if len(second.Quotas) != 1 || second.Quotas[0].RemainingPercentage != 80 || !second.UpdatedAt.Equal(first.UpdatedAt) || second.Failure == "" || second.OAuthFailure != second.Failure || !second.OAuthAttemptedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("good cache was not preserved: %#v", second)
	}
}

func TestCanceledClaudeCollectionDoesNotMutateCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	reset := now.Add(time.Hour)
	path, _ := cachePath()
	previous := cacheFile{
		Version: cacheVersion, Provider: "Claude", UpdatedAt: now.Add(-time.Minute), AttemptedAt: now.Add(-time.Minute),
		Quotas: []cachedQuota{{Window: "5-hour session", RemainingPercentage: 42, ResetsAt: &reset}},
	}
	if err := writeJSONCache(path, previous); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	loader := func(context.Context) (claudeCredentials, error) {
		cancel()
		return claudeCredentials{}, context.Canceled
	}
	client := claudeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("canceled credential load reached HTTP client")
		return nil, nil
	})
	if _, err := collectClaudeWith(ctx, loader, client, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled collection error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("canceled Claude collection mutated cache")
	}
}

func TestClaudeSuccessMergesFableIntoNewerStatusLineCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	loader := func(context.Context) (claudeCredentials, error) {
		return claudeCredentials{AccessToken: "secret", ExpiresAt: now.Add(time.Hour).UnixMilli()}, nil
	}
	client := claudeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		newer := now.Add(time.Second)
		reset := now.Add(time.Hour)
		path, _ := cachePath()
		if err := writeJSONCache(path, cacheFile{
			Version: cacheVersion, Provider: "Claude", UpdatedAt: newer, AttemptedAt: newer,
			Quotas: []cachedQuota{{Window: "5-hour session", RemainingPercentage: 90, ResetsAt: &reset, CollectedAt: newer}},
		}); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"limits":[{"scope":{"model":{"display_name":"Fable"}},"kind":"weekly","percent":25,"resets_at":%q}]}`, now.Add(4*24*time.Hour).Format(time.RFC3339))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	cache, err := collectClaudeWith(context.Background(), loader, client, now)
	if err != nil {
		t.Fatal(err)
	}
	if !cache.UpdatedAt.Equal(now.Add(time.Second)) || !cache.OAuthAttemptedAt.Equal(now) || cache.OAuthFailure != "" || len(cache.Quotas) != 2 || cache.Quotas[0].RemainingPercentage != 90 || cache.Quotas[1].Window != "Weekly · Fable" {
		t.Fatalf("OAuth result was not merged into newer status-line cache: %#v", cache)
	}
}

func TestClaudeFailurePreservesNewerStatusLineAndRecordsOAuthError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	loader := func(context.Context) (claudeCredentials, error) {
		return claudeCredentials{AccessToken: "secret", ExpiresAt: now.Add(time.Hour).UnixMilli()}, nil
	}
	client := claudeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		newer := now.Add(time.Second)
		reset := now.Add(time.Hour)
		path, _ := cachePath()
		if err := writeJSONCache(path, cacheFile{
			Version: cacheVersion, Provider: "Claude", UpdatedAt: newer, AttemptedAt: newer,
			Quotas: []cachedQuota{{Window: "5-hour session", RemainingPercentage: 90, ResetsAt: &reset, CollectedAt: newer}},
		}); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusTooManyRequests, Body: http.NoBody}, nil
	})
	cache, err := collectClaudeWith(context.Background(), loader, client, now)
	if err == nil {
		t.Fatal("expected rate-limit failure")
	}
	if cache.Failure != "" || !cache.AttemptedAt.Equal(now.Add(time.Second)) || cache.OAuthFailure != "HTTP 429 · Claude OAuth usage" || !cache.OAuthAttemptedAt.Equal(now) || cache.Quotas[0].RemainingPercentage != 90 {
		t.Fatalf("newer status-line cache or OAuth failure was lost: %#v", cache)
	}
}

func TestObsoleteClaudeFailureCannotRegressNewerCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Now()
	newer := now.Add(time.Minute)
	path, err := cachePath()
	if err != nil {
		t.Fatal(err)
	}
	cache := cacheFile{Version: cacheVersion, Provider: "Claude", UpdatedAt: newer, AttemptedAt: newer, Quotas: []cachedQuota{{Window: "5-hour session", RemainingPercentage: 77}}}
	if err := writeJSONCache(path, cache); err != nil {
		t.Fatal(err)
	}
	loader := func(context.Context) (claudeCredentials, error) {
		return claudeCredentials{}, errors.New("obsolete failure\n\x1b")
	}
	got, err := collectClaudeWith(context.Background(), loader, claudeRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil }), now)
	if err == nil || !got.AttemptedAt.Equal(newer) || got.Failure != "" || !got.OAuthAttemptedAt.Equal(now) || got.OAuthFailure != "obsolete failure" || got.Quotas[0].RemainingPercentage != 77 {
		t.Fatalf("obsolete collection result = %#v, %v", got, err)
	}
	persisted, readErr := readCache()
	if readErr != nil || !persisted.AttemptedAt.Equal(newer) || persisted.Failure != "" || persisted.OAuthFailure != "obsolete failure" {
		t.Fatalf("persisted newer cache = %#v, %v", persisted, readErr)
	}
}

func TestPrivateClaudeCredentialFileRejectsSymlinkAndLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix ownership and permission checks are not available")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(path, []byte(`{"claudeAiOauth":{"accessToken":"secret"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := openPrivateClaudeCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if file, err = openPrivateClaudeCredentials(path); err == nil {
		file.Close()
		t.Fatal("accepted group/world-readable credentials")
	}
	link := filepath.Join(dir, "credentials-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if file, err = openPrivateClaudeCredentials(link); err == nil {
		file.Close()
		t.Fatal("accepted credentials symlink")
	}
}

func TestDefaultClaudeClientDisablesProxyAndRedirects(t *testing.T) {
	client := newClaudeHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || client.Timeout != claudeRequestTimeout || client.CheckRedirect == nil {
		t.Fatalf("unsafe Claude client = %#v", client)
	}
}

func TestProviderLocksSerializeSameProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", os.Getenv("HOME"))
	for name, lock := range map[string]func() (func(), error){"Codex": lockCodexCache, "Kimi": lockKimiCache} {
		t.Run(name, func(t *testing.T) {
			unlock, err := lock()
			if err != nil {
				if strings.Contains(err.Error(), "unsupported") {
					t.Skip(err)
				}
				t.Fatal(err)
			}
			acquired := make(chan error, 1)
			go func() {
				secondUnlock, err := lock()
				if err == nil {
					secondUnlock()
				}
				acquired <- err
			}()
			select {
			case err := <-acquired:
				unlock()
				t.Fatalf("same-provider lock did not block: %v", err)
			case <-time.After(50 * time.Millisecond):
				unlock()
			}
			select {
			case err := <-acquired:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("same-provider lock did not acquire after release")
			}
		})
	}
}

func TestClaudeCacheLockWaitIsBounded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", os.Getenv("HOME"))
	unlock, err := lockClaudeCache()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	oldTimeout := claudeCacheLockTimeout
	claudeCacheLockTimeout = 40 * time.Millisecond
	defer func() { claudeCacheLockTimeout = oldTimeout }()
	started := time.Now()
	if secondUnlock, err := lockClaudeCache(); err == nil {
		secondUnlock()
		t.Fatal("acquired an already-held Claude cache lock")
	} else if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("lock error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("bounded lock took %v", elapsed)
	}
}
