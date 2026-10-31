package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func testBenefitGroup(now time.Time) benefitGroup {
	total := 4.0
	expired, soon, later := now.Add(-time.Hour).Unix(), now.Add(24*time.Hour).Unix(), now.Add(10*24*time.Hour).Unix()
	return benefitGroup{Kind: "banked_resets", Total: &total, Grants: []benefitGrant{
		{ID: "later", Label: "1 reset", Amount: 1, ExpiresAt: &later},
		{ID: "unknown", Label: "1 reset", Amount: 1},
		{ID: "expired", Label: "1 reset", Amount: 1, ExpiresAt: &expired},
		{ID: "soon", Label: "1 reset", Amount: 1, ExpiresAt: &soon},
	}}
}

func TestBenefitSummaryExpiryAndPartialDetails(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	group := testBenefitGroup(now)
	if err := validateBenefits([]benefitGroup{group}); err != nil {
		t.Fatal(err)
	}
	if got := group.valueText(now); got != "3 available" {
		t.Fatal(got)
	}
	got := group.sortedGrants()
	if got[0].ID != "expired" || got[1].ID != "soon" || got[3].ID != "unknown" || group.Grants[0].ID != "later" {
		t.Fatalf("sort/mutation: %#v", got)
	}
	if text, warn := group.expiry(now); text != "Next known: expires in 1d" || !warn {
		t.Fatalf("%s %v", text, warn)
	}
	total := 5.0
	group.Total = &total
	if group.missingAmount() != 1 || !group.expandable() || group.missingText() != "Expiration details unavailable for 1 reset" {
		t.Fatal(group)
	}
	if text, _ := group.expiry(now); text != "Next known: expires in 1d" {
		t.Fatal(text)
	}
	group.Grants = nil
	if text, _ := group.expiry(now); text != "Expiry unknown" || group.expandable() {
		t.Fatal(text)
	}
	group.Total = nil
	if group.valueText(now) != "Not reported" {
		t.Fatal("missing became zero")
	}
	group.Unlimited = true
	if group.valueText(now) != "Unlimited" {
		t.Fatal("unlimited missing")
	}
}

func TestBenefitWarningBoundary(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		delta time.Duration
		text  string
		warn  bool
	}{
		{-time.Second, "Expired", false}, {0, "Expired", false}, {time.Second, "Expires in <1m", true},
		{time.Minute, "Expires in 1m", true}, {time.Hour, "Expires in 1h", true},
		{24 * time.Hour, "Expires in 1d", true}, {48 * time.Hour, "Expires in 2d", true},
		{48*time.Hour + time.Second, "", false},
	} {
		stamp := now.Add(tc.delta).Unix()
		text, warn := benefitExpiryText(&stamp, false, now)
		if warn != tc.warn || tc.text != "" && text != tc.text {
			t.Fatalf("%v -> %q %v", tc.delta, text, warn)
		}
	}
	if warningStyle.GetForeground() != lowStyle.GetForeground() {
		t.Fatal("expiration warning differs from low quota orange")
	}
	if text, warn := benefitExpiryText(nil, true, now); text != "No expiration" || warn {
		t.Fatal(text, warn)
	}
}

func TestParseCodexBenefitsKnownMissingPartialAndMalformed(t *testing.T) {
	data := []byte(`{"rateLimits":{"credits":{"hasCredits":true,"unlimited":false,"balance":"42.50"}},"rateLimitResetCredits":{"availableCount":4,"credits":[{"id":"b","status":"available","expiresAt":1800000000},{"id":"a","status":"available","expiresAt":null},{"id":"used","status":"redeemed","expiresAt":1790000000},{"id":"missing-date","status":"available"}]}}`)
	groups, err := parseCodexBenefits(data)
	if err != nil {
		t.Fatal(err)
	}
	if *groups[0].Total != 42.5 || *groups[1].Total != 4 || len(groups[1].Grants) != 3 || !groups[1].Grants[1].NoExpiry || groups[1].Grants[2].NoExpiry || groups[1].missingAmount() != 1 {
		t.Fatalf("%#v", groups)
	}
	for _, data := range []string{`{}`, `{"rateLimits":{"credits":null},"rateLimitResetCredits":null}`} {
		got, err := parseCodexBenefits([]byte(data))
		if err != nil || got[0].Total != nil || got[1].Total != nil {
			t.Fatalf("unknown became zero: %#v %v", got, err)
		}
	}
	for _, data := range []string{
		`{"rateLimitResetCredits":{"availableCount":-1}}`,
		`{"rateLimitResetCredits":"secret"}`,
		`{"rateLimits":{"credits":{"hasCredits":true,"unlimited":false,"balance":"NaN"}}}`,
		`{"rateLimits":{"credits":{"hasCredits":true,"unlimited":false,"balance":"secret"}}}`,
		`{"rateLimitResetCredits":{"availableCount":2,"credits":[{"id":"x","status":"available","expiresAt":1800000000},{"id":"x","status":"available","expiresAt":1800000000}]}}`,
		`{"rateLimitResetCredits":{"availableCount":1,"credits":[{"id":"x","status":"available","expiresAt":"secret"}]}}`,
	} {
		if _, err := parseCodexBenefits([]byte(data)); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("malformed benefits not safely rejected: %v", err)
		}
	}
	got, err := parseCodexBenefits([]byte(`{"rateLimits":{"credits":{"hasCredits":false,"unlimited":false,"balance":null}},"rateLimitResetCredits":{"availableCount":0,"credits":[]}}`))
	if err != nil || got[0].Total == nil || *got[0].Total != 0 || got[1].Total == nil || *got[1].Total != 0 {
		t.Fatal("explicit zero lost", err)
	}
	plan, quotas, err := parseCodexRateLimits([]byte(`{"rateLimits":{"planType":"pro","credits":{"hasCredits":true,"unlimited":true}}}`), time.Now())
	if err != nil || plan != "pro" || len(quotas) != 0 {
		t.Fatalf("benefits-only account: %s %#v %v", plan, quotas, err)
	}
}

func TestMalformedCodexBenefitsPreserveSuccessfulCache(t *testing.T) {
	if !codexProcessGroupsSupported() {
		t.Skip("Unix process integration")
	}
	t.Setenv("HOME", t.TempDir())
	setTestCacheHome(t, t.TempDir())
	now := time.Now()
	path, _ := codexCachePath()
	total := 42.5
	previous := codexCacheFile{Version: cacheVersion, Provider: "OpenAI Codex", PlanType: "pro", UpdatedAt: now.Add(-time.Minute), AttemptedAt: now.Add(-time.Minute), Quotas: []codexCachedQuota{{Window: "5h", WindowDurationMins: 300, RemainingPercentage: 80, ResetsAt: now.Add(time.Hour)}}, Benefits: []benefitGroup{{Kind: "credits", Total: &total}, testBenefitGroup(now)}}
	if err := writeJSONCache(path, previous); err != nil {
		t.Fatal(err)
	}
	script := t.TempDir() + "/app-server"
	for _, malformed := range []string{
		`"rateLimits":{"planType":"pro","primary":{"usedPercent":30,"windowDurationMins":300,"resetsAt":` + fmt.Sprint(now.Add(time.Hour).Unix()) + `},"credits":{"hasCredits":true,"unlimited":false,"balance":"secret"}}`,
		`"rateLimits":{"planType":"pro","primary":{"usedPercent":30,"windowDurationMins":300,"resetsAt":` + fmt.Sprint(now.Add(time.Hour).Unix()) + `}},"rateLimitResetCredits":{"availableCount":"secret"}`,
	} {
		body := "#!/bin/sh\nread init\nprintf '%s\\n' '{\"id\":1,\"result\":{}}'\nread initialized\nread account\nread limits\nprintf '%s\\n' '{\"id\":2,\"result\":{\"account\":{\"type\":\"chatgpt\"}}}'\nprintf '%s\\n' '{\"id\":3,\"result\":{" + malformed + "}}'\n"
		if err := os.WriteFile(script, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
		factory := func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, script) }
		cache, err := collectCodexWith(context.Background(), factory, now)
		if err == nil || cache.Failure == "" || strings.Contains(cache.Failure, "secret") || !cache.UpdatedAt.Equal(previous.UpdatedAt) || !reflect.DeepEqual(cache.Benefits, previous.Benefits) || cache.Quotas[0].RemainingPercentage != 80 {
			t.Fatalf("good cache was lost: %#v %v", cache, err)
		}
	}
}

func TestClaudeBenefitsReadOnlySchema(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	body := `{"cedar_ember":{"eligible":true,"grants":[{"id":"reset1","resets_left":2,"paused":false,"starts_at":"2026-10-01T00:00:00Z","ends_at":"2026-11-01T12:00:00Z"},{"id":"reset2","resets_left":1,"paused":false},{"id":"paused","resets_left":9,"paused":true}]}}`
	client := claudeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.String() != claudeBenefitsEndpoint || r.Header.Get("Authorization") != "Bearer fake" {
			t.Fatal("unexpected reset request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	groups, err := fetchClaudeBenefits(context.Background(), client, "fake", now)
	if err != nil || len(groups) != 1 || *groups[0].Total != 3 || len(groups[0].Grants) != 2 || groups[0].Grants[1].NoExpiry {
		t.Fatalf("%#v %v", groups, err)
	}
	for _, body := range []string{`{}`, `{"cedar_ember":{"eligible":true}}`, `{"cedar_ember":{"eligible":false,"grants":[]}}`} {
		got, err := parseClaudeBenefits([]byte(body), now)
		if err != nil || got[0].Total != nil {
			t.Fatal("missing inventory became zero", err)
		}
	}
	zero, err := parseClaudeBenefits([]byte(`{"cedar_ember":{"eligible":true,"grants":[]}}`), now)
	if err != nil || zero[0].Total == nil || *zero[0].Total != 0 {
		t.Fatal("explicit zero lost")
	}
	for _, body := range []string{
		`{"cedar_ember":{"eligible":true,"grants":[{"id":"x","paused":false}]}}`,
		`{"cedar_ember":{"eligible":true,"grants":[{"id":"x","paused":false,"resets_left":2,"ends_at":"bad"}]}}`,
	} {
		if _, err := parseClaudeBenefits([]byte(body), now); err == nil {
			t.Fatal("malformed grant accepted")
		}
	}
}

func TestClaudeBenefitFailurePreservesStateAndIngest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setTestCacheHome(t, t.TempDir())
	now := time.Now()
	loader := func(context.Context) (claudeCredentials, error) {
		return claudeCredentials{AccessToken: "fake", ExpiresAt: now.Add(time.Hour).UnixMilli()}, nil
	}
	calls, fail := 0, false
	client := claudeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body := fmt.Sprintf(`{"five_hour":{"utilization":20,"resets_at":%q}}`, now.Add(time.Hour).Format(time.RFC3339))
		if r.URL.String() == claudeBenefitsEndpoint {
			if fail {
				return nil, errors.New("secret-token")
			}
			body = fmt.Sprintf(`{"cedar_ember":{"eligible":true,"grants":[{"id":"one","resets_left":1,"paused":false,"ends_at":%q}]}}`, now.Add(time.Hour).Format(time.RFC3339))
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	first, err := collectClaudeWith(context.Background(), loader, client, now)
	if err != nil || first.Benefits == nil || *first.Benefits.Groups[0].Total != 1 || calls != 2 {
		t.Fatalf("first: %#v %v", first, err)
	}
	fail = true
	second, err := collectClaudeWith(context.Background(), loader, client, now.Add(time.Second))
	if err != nil || second.Benefits.Failure == "" || strings.Contains(second.Benefits.Failure, "secret") || !second.Benefits.UpdatedAt.Equal(first.Benefits.UpdatedAt) || second.Quotas[0].RemainingPercentage != 80 {
		t.Fatalf("preservation: %#v %v", second, err)
	}
	// Make the existing cache older than the ingest operation.
	path, _ := cachePath()
	second.AttemptedAt = now
	second.OAuthAttemptedAt = now
	second.UpdatedAt = now
	if err := writeJSONCache(path, second); err != nil {
		t.Fatal(err)
	}
	if err := ingest(strings.NewReader(`{"rate_limits":{"five_hour":{"used_percentage":40}}}`), io.Discard); err != nil {
		t.Fatal(err)
	}
	after, err := readCache()
	if err != nil || after.Benefits == nil || *after.Benefits.Groups[0].Total != 1 || after.Benefits.Failure == "" {
		t.Fatal("status-line erased benefit state", err)
	}
}

func TestBenefitUIExpansionAndSnapshotSeparation(t *testing.T) {
	now := time.Now()
	m := model{demo: true, width: 100, height: 40}
	m.appendBenefits("OpenAI", "Codex Pro", []benefitGroup{testBenefitGroup(now)}, now, now, "", "fixture", now)
	key := quotaKey(m.quotas[0])
	collapsed := m.View()
	if !strings.Contains(collapsed, "Banked resets") || strings.Contains(collapsed, "% left") || strings.Contains(collapsed, "(expired)") {
		t.Fatal(collapsed)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if !m.expandedBenefits[key] || m.detail || !strings.Contains(m.View(), "(expired)") {
		t.Fatal(m.View())
	}
	next, _ = m.Update(tickMsg(now))
	_ = next // Live reload behavior is tested separately with caches.
	snapshot := newDashboardSnapshot(m, now)
	if len(snapshot.Quotas) != 0 || len(snapshot.Benefits) != 1 || snapshot.Benefits[0].Group.Kind != "banked_resets" {
		t.Fatal("benefit serialized as quota")
	}
	if len(resetRefreshDeadlines(m.quotas, nil)) != 0 {
		t.Fatal("expiration scheduled as quota reset")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = next.(model)
	if m.expandedBenefits[key] {
		t.Fatal("left failed to collapse")
	}
	for _, size := range [][2]int{{100, 8}, {40, 8}, {18, 5}, {5, 3}, {1, 1}} {
		m.width, m.height = size[0], size[1]
		m.expandedBenefits = map[string]bool{key: true}
		view := m.View()
		if lipgloss.Height(view) > m.height || lipgloss.Width(view) > m.width {
			t.Fatalf("overflow %v: %q", size, view)
		}
	}
}

func TestBenefitDetailScrollReachesEndAndStaysWithinTerminal(t *testing.T) {
	now := time.Now()
	m := model{demo: true, detail: true, width: 24, height: 8}
	m.appendBenefits("OpenAI", "Codex Pro", []benefitGroup{testBenefitGroup(now)}, now, now, "refresh failed", "fixture source", now)
	m.quotas[0].Benefit.Note = strings.Repeat("Long explanatory note. ", 12) + "end of note"
	for _, width := range []int{100, 24, 18, 5, 1} {
		m.width = width
		key := quotaKey(m.quotas[0])
		for page := 0; page < 2000; page++ {
			before := m.benefitScroll[key]
			view := m.View()
			if lipgloss.Height(view) > m.height || lipgloss.Width(view) > m.width {
				t.Fatalf("detail overflow width %d: %q", width, view)
			}
			m.scrollBenefit(true)
			if m.benefitScroll[key] == before {
				break
			}
		}
		end := m.benefitScroll[key]
		m.scrollBenefit(true)
		if m.benefitScroll[key] != end {
			t.Fatal("scroll not bounded")
		}
		m.scrollBenefit(false)
		if end > 0 && m.benefitScroll[key] >= end {
			t.Fatal("page up did not move")
		}
	}
}

func TestDemoBenefitLayout(t *testing.T) {
	m := newDemoModel()
	m.width = 100
	m.height = 24
	m.selected = 2
	m.expandedBenefits = map[string]bool{quotaKey(m.quotas[2]): true}
	view := m.View()
	if !strings.Contains(view, "DEMO DATA") || !strings.Contains(view, "$12.40") || !strings.Contains(view, "$70.00") || lipgloss.Height(view) > 24 {
		t.Fatal(view)
	}
	if os.Getenv("AIUSAGE_TERMINAL_PREVIEW") == "1" {
		t.Log("\n" + view)
	}
}

func TestBenefitExpansionPersistsAcrossReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setTestCacheHome(t, t.TempDir())
	now := time.Now()
	path, _ := codexCachePath()
	cache := codexCacheFile{Version: cacheVersion, Provider: "OpenAI Codex", PlanType: "pro", UpdatedAt: now, AttemptedAt: now, Benefits: []benefitGroup{testBenefitGroup(now)}}
	if err := writeJSONCache(path, cache); err != nil {
		t.Fatal(err)
	}
	m := model{}
	m.reloadAt(now)
	key := quotaKey(m.quotas[0])
	m.expandedBenefits = map[string]bool{key: true}
	m.reloadAt(now.Add(time.Minute))
	if !m.expandedBenefits[key] || quotaKey(m.quotas[m.selected]) != key {
		t.Fatal("reload lost expansion/selection")
	}
	data, err := json.Marshal(newDashboardSnapshot(m, now))
	if err != nil || !strings.Contains(string(data), `"benefits"`) {
		t.Fatal(err)
	}
}

func TestBenefitValidationRejectsInvalidData(t *testing.T) {
	now := time.Now()
	for _, mutate := range []func(*benefitGroup){
		func(g *benefitGroup) { g.Kind = "unknown" },
		func(g *benefitGroup) { g.Grants[0].Amount = -1 },
		func(g *benefitGroup) { g.Grants[0].ID = g.Grants[1].ID },
		func(g *benefitGroup) { g.Grants[0].Label = "bad\x1b" },
		func(g *benefitGroup) { g.Total = nil },
		func(g *benefitGroup) { g.Grants[0].NoExpiry = true },
		func(g *benefitGroup) { g.Unlimited = true },
		func(g *benefitGroup) { g.Grants[0].Amount = 1.5 },
	} {
		group := testBenefitGroup(now)
		mutate(&group)
		if validateBenefits([]benefitGroup{group}) == nil {
			t.Fatalf("accepted %#v", group)
		}
	}
}
