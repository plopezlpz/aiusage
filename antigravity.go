package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const antigravitySource = "Antigravity CLI /usage JSON"

// agy's command report supplies quota groups, not account or subscription identity.
type antigravityCacheFile struct {
	Version     int                      `json:"version"`
	Provider    string                   `json:"provider"`
	UpdatedAt   time.Time                `json:"updated_at"`
	AttemptedAt time.Time                `json:"attempted_at,omitempty"`
	Failure     string                   `json:"failure,omitempty"`
	Quotas      []antigravityCachedQuota `json:"quotas"`
}

type antigravityCachedQuota struct {
	ID           string     `json:"id"`
	Window       string     `json:"window"`
	DurationMins int64      `json:"duration_mins"`
	Remaining    float64    `json:"remaining_percentage"`
	ResetsAt     *time.Time `json:"resets_at,omitempty"`
	Detail       string     `json:"detail,omitempty"`
}

func antigravityCachePath() (string, error) { return providerCachePath("antigravity.json") }

func readAntigravityCache() (antigravityCacheFile, error) {
	path, err := antigravityCachePath()
	if err != nil {
		return antigravityCacheFile{}, err
	}
	data, err := readBoundedCache(path)
	if err != nil {
		return antigravityCacheFile{}, err
	}
	var cache antigravityCacheFile
	if err := json.Unmarshal(data, &cache); err != nil {
		return antigravityCacheFile{}, errors.New("invalid Antigravity cache JSON")
	}
	if err := validateAntigravityCache(cache, time.Now()); err != nil {
		return antigravityCacheFile{}, err
	}
	return cache, nil
}

func validateAntigravityCache(cache antigravityCacheFile, now time.Time) error {
	if cache.Version != cacheVersion || cache.Provider != "Antigravity" {
		return errors.New("unsupported Antigravity cache format")
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
	if cache.Failure != "" && cache.AttemptedAt.IsZero() || cache.Failure != safeCollectionError(errors.New(cache.Failure)) {
		return errors.New("invalid Antigravity failure metadata")
	}
	if len(cache.Quotas) > 100 {
		return errors.New("too many Antigravity quota buckets")
	}
	ids, windows := map[string]bool{}, map[string]bool{}
	for _, q := range cache.Quotas {
		if q.ID == "" || !validBenefitText(q.ID, 128) || q.Window == "" || !validBenefitText(q.Window, 160) || !validBenefitText(q.Detail, 256) || ids[q.ID] || windows[q.Window] {
			return errors.New("invalid Antigravity quota labels")
		}
		ids[q.ID], windows[q.Window] = true, true
		if math.IsNaN(q.Remaining) || math.IsInf(q.Remaining, 0) || q.Remaining < 0 || q.Remaining > 100 {
			return errors.New("invalid Antigravity remaining percentage")
		}
		if q.DurationMins != 0 && q.DurationMins != 300 && q.DurationMins != 10080 {
			return errors.New("invalid Antigravity window duration")
		}
		if q.ResetsAt != nil && (q.ResetsAt.Year() < 2020 || q.ResetsAt.After(now.Add(366*24*time.Hour))) {
			return errors.New("invalid Antigravity reset timestamp")
		}
	}
	return nil
}

func parseAntigravityUsage(data []byte, now time.Time) ([]antigravityCachedQuota, error) {
	var report struct {
		Status  string `json:"status"`
		Command *struct {
			Name string `json:"name"`
			Data struct {
				Groups []struct {
					Name    string `json:"name"`
					Buckets []struct {
						ID          string   `json:"id"`
						Name        string   `json:"name"`
						Window      string   `json:"window"`
						Description string   `json:"description"`
						Remaining   *float64 `json:"remaining_fraction"`
						Reset       *string  `json:"reset_time"`
						Disabled    bool     `json:"disabled"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	if len(data) > maxInputSize || json.Unmarshal(data, &report) != nil {
		return nil, errors.New("Antigravity usage report was invalid JSON")
	}
	if report.Status != "SUCCESS" || report.Command == nil || report.Command.Name != "usage" {
		return nil, errors.New("Antigravity did not return a successful /usage report — run agy and sign in")
	}
	var quotas []antigravityCachedQuota
	for _, group := range report.Command.Data.Groups {
		if group.Name == "" || !validBenefitText(group.Name, 80) {
			return nil, errors.New("Antigravity quota group was invalid")
		}
		for _, bucket := range group.Buckets {
			if bucket.Disabled || bucket.Remaining == nil {
				continue
			} // Unknown is NOT an untouched 100% allowance.
			if *bucket.Remaining < 0 || *bucket.Remaining > 1 {
				return nil, errors.New("Antigravity remaining_fraction must be between 0 and 1")
			}
			name, duration := bucket.Name, int64(0)
			switch strings.ToLower(bucket.Window) {
			case "weekly":
				name, duration = "Weekly", 10080
			case "5h":
				name, duration = "5-hour", 300
			}
			if name == "" {
				name = bucket.ID
			}
			family := group.Name
			switch family {
			case "Gemini Models":
				family = "Gemini"
			case "Claude and GPT models":
				family = "Claude + GPT"
			}
			q := antigravityCachedQuota{ID: bucket.ID, Window: family + " · " + name, DurationMins: duration, Remaining: *bucket.Remaining * 100, Detail: bucket.Description}
			if bucket.Reset != nil && *bucket.Reset != "" {
				reset, err := time.Parse(time.RFC3339, *bucket.Reset)
				if err != nil {
					return nil, errors.New("Antigravity reset timestamp was invalid")
				}
				q.ResetsAt = &reset
			}
			quotas = append(quotas, q)
		}
	}
	if len(quotas) == 0 {
		return nil, errors.New("Antigravity returned no measured, enabled quota buckets")
	}
	cache := antigravityCacheFile{Version: cacheVersion, Provider: "Antigravity", UpdatedAt: now, Quotas: quotas}
	if err := validateAntigravityCache(cache, now); err != nil {
		return nil, err
	}
	return quotas, nil
}

type antigravityFetcher func(context.Context) ([]byte, error)

func collectAntigravity(ctx context.Context) (antigravityCacheFile, error) {
	return collectAntigravityWith(ctx, fetchAntigravity, time.Now())
}

func collectAntigravityWith(parent context.Context, fetch antigravityFetcher, now time.Time) (antigravityCacheFile, error) {
	unlock, err := lockProviderCache(".antigravity.lock", "Antigravity", 2*time.Second)
	if err != nil {
		return antigravityCacheFile{}, err
	}
	defer unlock()
	if parentCollectionCanceled(parent) {
		return antigravityCacheFile{}, context.Canceled
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	data, collectErr := fetch(ctx)
	var quotas []antigravityCachedQuota
	if collectErr == nil {
		quotas, collectErr = parseAntigravityUsage(data, now)
	}
	if parentCollectionCanceled(parent) {
		return antigravityCacheFile{}, context.Canceled
	}
	previous, readErr := readAntigravityCache()
	if readErr == nil && cacheStateNewerThan(now, previous.UpdatedAt, previous.AttemptedAt) {
		return previous, nil
	}
	cache := antigravityCacheFile{Version: cacheVersion, Provider: "Antigravity", AttemptedAt: now}
	if collectErr != nil {
		if readErr == nil {
			cache = previous
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return antigravityCacheFile{}, fmt.Errorf("preserve Antigravity cache: %w", readErr)
		}
		cache.AttemptedAt = now
		cache.Failure = safeCollectionError(collectErr)
	} else {
		cache.Quotas = quotas
		cache.UpdatedAt = now
	}
	if err := validateAntigravityCache(cache, now); err != nil {
		return antigravityCacheFile{}, err
	}
	path, err := antigravityCachePath()
	if err != nil {
		return antigravityCacheFile{}, err
	}
	if parentCollectionCanceled(parent) {
		return antigravityCacheFile{}, context.Canceled
	}
	if err := writeJSONCache(path, cache); err != nil {
		return antigravityCacheFile{}, err
	}
	return cache, collectErr
}

func antigravityCacheFresh(now time.Time) bool {
	cache, err := readAntigravityCache()
	return err == nil && providerCacheFresh(cache.AttemptedAt, cache.UpdatedAt, now)
}

func collectAntigravityCmd(runtime *collectorRuntime) tea.Cmd {
	return runtime.command(func(ctx context.Context) tea.Msg {
		_, err := collectAntigravity(ctx)
		return antigravityCollectedMsg{err: err}
	})
}

func compactAntigravityUsage(cache antigravityCacheFile) string {
	parts := make([]string, 0, len(cache.Quotas))
	for _, q := range cache.Quotas {
		parts = append(parts, fmt.Sprintf("%s %.0f%% left", q.Window, q.Remaining))
	}
	return "Antigravity " + strings.Join(parts, " · ")
}

func (m *model) reloadAntigravity(now time.Time) string {
	cache, err := readAntigravityCache()
	if err == nil && cacheStateNewerThan(m.antigravityErrorAt, cache.UpdatedAt, cache.AttemptedAt) {
		m.antigravityError, m.antigravityErrorAt = "", time.Time{}
	}
	failure := currentFailure(cache.Failure, m.antigravityError)
	if err == nil {
		for _, q := range cache.Quotas {
			m.quotas = append(m.quotas, quota{Provider: "Antigravity", Product: "Google", Window: q.Window, Duration: time.Duration(q.DurationMins) * time.Minute, Remaining: q.Remaining, ResetAt: q.ResetsAt, UpdatedAt: cache.UpdatedAt, AttemptedAt: currentAttempt(cache.AttemptedAt, m.antigravityError, m.antigravityErrorAt), Failure: failure, Source: antigravitySource, Detail: q.Detail, Stale: failure != "" || quotaIsStale(cache.UpdatedAt, q.ResetsAt, now)})
		}
	}
	if len(cache.Quotas) == 0 && failure != "" {
		return "Antigravity: " + failure
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "Antigravity cache: " + safeCollectionError(err)
	}
	return ""
}
