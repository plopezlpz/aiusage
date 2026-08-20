package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type zaiHTTPDoerFunc func(*http.Request) (*http.Response, error)

func (f zaiHTTPDoerFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

func TestZAIHelperProcess(t *testing.T) {
	if os.Getenv("AIUSAGE_TEST_ZAI_HELPER") != "1" {
		return
	}
	if os.Getenv("AIUSAGE_TEST_ZAI_HANG") == "1" {
		time.Sleep(10 * time.Second)
	}
	fmt.Print(os.Getenv("AIUSAGE_TEST_ZAI_KEY"))
	os.Exit(0)
}

func fakeZAICommand(key string) zaiCommandFactory {
	return func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestZAIHelperProcess$")
		cmd.Env = append(os.Environ(), "AIUSAGE_TEST_ZAI_HELPER=1", "AIUSAGE_TEST_ZAI_KEY="+key)
		return cmd
	}
}

func TestZAICredentialExportHonorsCancellation(t *testing.T) {
	if !zaiProcessGroupsSupported() {
		t.Skip("Z.AI credential export requires Unix process groups")
	}
	ctx, cancel := context.WithCancel(context.Background())
	factory := func(ctx context.Context) *exec.Cmd {
		cmd := fakeZAICommand("secret")(ctx)
		cmd.Env = append(cmd.Env, "AIUSAGE_TEST_ZAI_HANG=1")
		return cmd
	}
	done := make(chan error, 1)
	go func() { _, err := loadZAIAPIKey(ctx, factory); done <- err }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled credential export succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled credential export did not stop")
	}
}

func TestZAICommandUsesPiCredentialExport(t *testing.T) {
	cmd := newZAICommand(context.Background())
	want := []string{"pi", "auth", "print-api-key", "--provider", "zai-coding-cn"}
	if strings.Join(cmd.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("command args = %#v", cmd.Args)
	}
}

func TestZAIHTTPClientRejectsRedirects(t *testing.T) {
	client := newZAIHTTPClient()
	request, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error = %v", err)
	}
}

func TestFetchZAIUsesRawAuthorizationAndParsesQuotaWindows(t *testing.T) {
	if !zaiProcessGroupsSupported() {
		t.Skip("Z.AI credential export requires Unix process groups")
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	secret := "test-zai-secret"
	weeklyReset := now.Add(6 * 24 * time.Hour).UnixMilli()
	body := fmt.Sprintf(`{"success":true,"code":200,"data":{"level":"lite","limits":[
		{"type":"TIME_LIMIT","unit":5,"percentage":50},
		{"type":"TOKENS_LIMIT","unit":6,"number":1,"percentage":9,"nextResetTime":%d},
		{"type":"CREDIT_LIMIT","unit":3,"number":5,"percentage":0}
	]}}`, weeklyReset)
	client := zaiHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != zaiUsageEndpoint {
			t.Fatalf("request = %s %s", request.Method, request.URL)
		}
		if got := request.Header.Get("Authorization"); got != secret || strings.HasPrefix(got, "Bearer ") {
			t.Fatalf("Authorization header was not the raw exported key: %q", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	plan, quotas, err := fetchZAI(context.Background(), fakeZAICommand(secret), client, now)
	if err != nil {
		t.Fatal(err)
	}
	if plan != "lite" || len(quotas) != 2 || quotas[0].Window != "5-hour" || quotas[0].RemainingPercentage != 100 || quotas[0].ResetsAt != nil {
		t.Fatalf("5-hour quota = plan %q, %#v", plan, quotas)
	}
	if quotas[1].Window != "Weekly" || quotas[1].RemainingPercentage != 91 || quotas[1].ResetsAt == nil || quotas[1].ResetsAt.UnixMilli() != weeklyReset {
		t.Fatalf("weekly quota = %#v", quotas[1])
	}
}

func TestParseZAIUsageAllowsLegacyFiveHourOnly(t *testing.T) {
	now := time.Now()
	plan, quotas, err := parseZAIUsage([]byte(`{"success":true,"code":200,"data":{"level":"pro","limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":25}]}}`), now)
	if err != nil || plan != "pro" || len(quotas) != 1 || quotas[0].RemainingPercentage != 75 {
		t.Fatalf("legacy response = plan %q quotas %#v error %v", plan, quotas, err)
	}
}

func TestParseZAIUsageRejectsMalformedKnownWindows(t *testing.T) {
	now := time.Now()
	tests := map[string]string{
		"invalid JSON":      `{`,
		"failed envelope":   `{"success":false,"code":200,"data":{}}`,
		"missing five-hour": `{"success":true,"code":200,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":6,"percentage":2}]}}`,
		"duplicate":         `{"success":true,"code":200,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":2},{"type":"CREDIT_LIMIT","unit":3,"percentage":3}]}}`,
		"negative":          `{"success":true,"code":200,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":-1}]}}`,
		"over 100":          `{"success":true,"code":200,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":101}]}}`,
		"bad reset":         `{"success":true,"code":200,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":1,"nextResetTime":1}]}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseZAIUsage([]byte(body), now); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestZAIPlanLabelsAreSafeAndUsedWhenAvailable(t *testing.T) {
	if _, err := safeZAIPlan("lite\u202e"); err == nil {
		t.Fatal("accepted bidirectional plan label")
	}
	quota := []zaiCachedQuota{{Window: "5-hour", RemainingPercentage: 75}}
	if got := compactZAIUsage(zaiCacheFile{PlanLevel: "lite", Quotas: quota}); got != "Z.AI Lite 5-hour 75% left" {
		t.Fatalf("plan output = %q", got)
	}
	if got := compactZAIUsage(zaiCacheFile{Quotas: quota}); got != "Z.AI Coding Plan 5-hour 75% left" {
		t.Fatalf("fallback output = %q", got)
	}
}

func TestValidateZAICacheRejectsNonfinitePercentage(t *testing.T) {
	now := time.Now()
	cache := zaiCacheFile{
		Version: cacheVersion, Provider: "Z.AI Coding Plan", UpdatedAt: now,
		Quotas: []zaiCachedQuota{{Window: "5-hour", Unit: 3, UsedPercentage: math.NaN(), RemainingPercentage: 50}},
	}
	if err := validateZAICache(cache, now); err == nil {
		t.Fatal("accepted nonfinite percentage")
	}
}

func TestZAICredentialErrorsDoNotLeakSecret(t *testing.T) {
	if !zaiProcessGroupsSupported() {
		t.Skip("Z.AI credential export requires Unix process groups")
	}
	secret := "super-secret-zai-key"
	client := zaiHTTPDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport failure containing " + secret)
	})
	_, _, err := fetchZAI(context.Background(), fakeZAICommand(secret), client, time.Now())
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("credential leaked in error: %v", err)
	}
}

func TestCollectZAIPreservesLastGoodCacheOnFailure(t *testing.T) {
	if !zaiProcessGroupsSupported() {
		t.Skip("Z.AI collection requires Unix process groups")
	}
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	setTestCacheHome(t, temp)
	now := time.Now().Truncate(time.Millisecond)
	reset := now.Add(time.Hour)
	path, err := zaiCachePath()
	if err != nil {
		t.Fatal(err)
	}
	previous := zaiCacheFile{
		Version: cacheVersion, Provider: "Z.AI Coding Plan", PlanLevel: "lite", UpdatedAt: now.Add(-time.Minute), AttemptedAt: now.Add(-time.Minute),
		Quotas: []zaiCachedQuota{{Window: "5-hour", Unit: 3, UsedPercentage: 10, RemainingPercentage: 90, ResetsAt: &reset}},
	}
	if err := writeJSONCache(path, previous); err != nil {
		t.Fatal(err)
	}
	client := zaiHTTPDoerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	got, err := collectZAIWith(context.Background(), fakeZAICommand("secret"), client, now)
	if err == nil || len(got.Quotas) != 1 || got.Quotas[0].RemainingPercentage != 90 || !got.UpdatedAt.Equal(previous.UpdatedAt) || got.Failure == "" || !got.AttemptedAt.Equal(now) {
		t.Fatalf("preserved cache = %#v, error %v", got, err)
	}
}

func TestCollectZAICancellationDoesNotWriteFailure(t *testing.T) {
	if !zaiProcessGroupsSupported() {
		t.Skip("Z.AI collection requires Unix process groups")
	}
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	setTestCacheHome(t, temp)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collectZAIWith(ctx, fakeZAICommand("secret"), zaiHTTPDoerFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("HTTP request started after cancellation")
		return nil, nil
	}), time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	path, _ := zaiCachePath()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancellation wrote cache: %v", err)
	}
}
