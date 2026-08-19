package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	kimiCollectionTimeout     = 10 * time.Second
	kimiCleanupTimeout        = 2 * time.Second
	kimiRequestTimeout        = 5 * time.Second
	maxKimiServerOutput       = 64 << 10
	maxKimiResponseHeaderSize = 32 << 10
	maxKimiResponseSize       = 256 << 10
	maxKimiTokenSize          = 4096
)

type kimiCacheFile struct {
	Version     int               `json:"version"`
	Provider    string            `json:"provider"`
	UpdatedAt   time.Time         `json:"updated_at"`
	AttemptedAt time.Time         `json:"attempted_at,omitempty"`
	Failure     string            `json:"failure,omitempty"`
	Quotas      []kimiCachedQuota `json:"quotas"`
}

type kimiCachedQuota struct {
	Window              string    `json:"window"`
	WindowDuration      int64     `json:"window_duration"`
	WindowUnit          string    `json:"window_unit"`
	Used                float64   `json:"used"`
	Limit               float64   `json:"limit"`
	RemainingPercentage float64   `json:"remaining_percentage"`
	ResetsAt            time.Time `json:"resets_at"`
}

type kimiUsageResponse struct {
	Code *int `json:"code"`
	Data *struct {
		Kind    string            `json:"kind"`
		Summary *kimiUsageWindow  `json:"summary"`
		Limits  []kimiUsageWindow `json:"limits"`
	} `json:"data"`
}

type kimiUsageWindow struct {
	Window *struct {
		Duration *int64  `json:"duration"`
		Unit     *string `json:"unit"`
	} `json:"window"`
	Used    *float64 `json:"used"`
	Limit   *float64 `json:"limit"`
	ResetAt *string  `json:"reset_at"`
}

type kimiCommandFactory func(context.Context) *exec.Cmd

var newKimiCommand kimiCommandFactory = func(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "kimi", "web", "--no-open", "--port", "0", "--log-level", "silent")
}

func kimiCachePath() (string, error) {
	return providerCachePath("kimi.json")
}

func kimiTokenPath() (string, error) {
	home := os.Getenv("KIMI_CODE_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find user home: %w", err)
		}
		home = filepath.Join(userHome, ".kimi-code")
	}
	return filepath.Join(home, "server.token"), nil
}

func readBoundedKimiToken(reader io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxKimiTokenSize+1))
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	if len(data) > maxKimiTokenSize {
		return "", errors.New("token file exceeds size limit")
	}
	token := strings.TrimRight(string(data), "\r\n")
	if token == "" {
		return "", errors.New("token file is empty")
	}
	for _, char := range token {
		if char < 0x21 || char > 0x7e {
			return "", errors.New("token file contains invalid characters")
		}
	}
	return token, nil
}

func collectKimi(ctx context.Context) (kimiCacheFile, error) {
	return collectKimiWith(ctx, newKimiCommand, time.Now())
}

func collectKimiWith(parent context.Context, factory kimiCommandFactory, now time.Time) (kimiCacheFile, error) {
	if !kimiProcessGroupsSupported() {
		return kimiCacheFile{}, errors.New("Kimi collection is supported only on Unix")
	}
	unlock, err := lockKimiCache()
	if err != nil {
		return kimiCacheFile{}, fmt.Errorf("lock Kimi cache: %w", err)
	}
	defer unlock()
	if parentCollectionCanceled(parent) {
		return kimiCacheFile{}, context.Canceled
	}

	ctx, cancel := context.WithTimeout(parent, kimiCollectionTimeout)
	defer cancel()
	tokenPath, err := kimiTokenPath()
	var quotas []kimiCachedQuota
	if err == nil {
		quotas, err = fetchKimi(ctx, factory, tokenPath, now)
	}
	if parentCollectionCanceled(parent) {
		return kimiCacheFile{}, context.Canceled
	}
	previous, readErr := readKimiCache()
	if parentCollectionCanceled(parent) {
		return kimiCacheFile{}, context.Canceled
	}
	if readErr == nil && cacheStateNewerThan(now, previous.UpdatedAt, previous.AttemptedAt) {
		return previous, nil
	}
	cache := kimiCacheFile{Version: cacheVersion, Provider: "Kimi Code", AttemptedAt: now}
	if err != nil {
		if readErr == nil {
			cache = previous
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return kimiCacheFile{}, fmt.Errorf("preserve Kimi cache after failed collection: %w", readErr)
		}
		cache.AttemptedAt = now
		cache.Failure = safeCollectionError(err)
	} else {
		cache.UpdatedAt = now
		cache.Quotas = quotas
	}
	if validateErr := validateKimiCache(cache, now); validateErr != nil {
		return kimiCacheFile{}, fmt.Errorf("validate Kimi data: %w", validateErr)
	}
	path, pathErr := kimiCachePath()
	if pathErr != nil {
		return kimiCacheFile{}, pathErr
	}
	if parentCollectionCanceled(parent) {
		return kimiCacheFile{}, context.Canceled
	}
	if writeErr := writeJSONCache(path, cache); writeErr != nil {
		return kimiCacheFile{}, fmt.Errorf("store Kimi cache: %w", writeErr)
	}
	return cache, err
}

func fetchKimi(ctx context.Context, factory kimiCommandFactory, tokenPath string, now time.Time) ([]kimiCachedQuota, error) {
	if !kimiProcessGroupsSupported() {
		return nil, errors.New("Kimi collection requires process-group support")
	}
	cmd := factory(ctx)
	configureKimiCommand(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open Kimi server output: %w", err)
	}
	// Server output may contain its bearer token, so it is consumed only for readiness and addressing and never surfaced.
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		_ = stdout.Close()
		return nil, fmt.Errorf("open null device for Kimi server: %w", err)
	}
	cmd.Stderr = null
	if err := cmd.Start(); err != nil {
		_ = null.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start Kimi server: %w", err)
	}
	processGroup, err := kimiCommandProcessGroup(cmd)
	_ = null.Close()
	if err != nil {
		_ = stdout.Close()
		terminateKimiCommand(cmd, 0)
		_ = cmd.Wait()
		return nil, fmt.Errorf("record Kimi server process group: %w", err)
	}

	var terminateOnce sync.Once
	terminate := func() {
		terminateOnce.Do(func() {
			_ = stdout.Close()
			terminateKimiCommand(cmd, processGroup)
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
		case <-time.After(kimiCleanupTimeout):
		}
		select {
		case <-watcherDone:
		case <-time.After(kimiCleanupTimeout):
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), maxKimiServerOutput)
	ready := false
	port := 0
	outputBytes := 0
	for scanner.Scan() {
		outputBytes += len(scanner.Bytes()) + 1
		if outputBytes > maxKimiServerOutput {
			return nil, errors.New("Kimi server readiness output exceeded size limit")
		}
		line := scanner.Text()
		if strings.Contains(line, "  Kimi server ready  ") {
			ready = true
		}
		if parsedPort, ok := parseKimiLocalPort(line); ok {
			port = parsedPort
		}
		if ready && port != 0 {
			break
		}
	}
	if !ready || port == 0 {
		if ctx.Err() != nil {
			return nil, errors.New("Kimi server timed out before reporting readiness and a valid Local address")
		}
		if scanner.Err() != nil {
			return nil, errors.New("Kimi server readiness output was invalid")
		}
		return nil, errors.New("Kimi server exited before reporting readiness and a valid Local address")
	}
	select {
	case <-processDone:
		return nil, errors.New("Kimi server exited before usage request")
	default:
	}

	// Connect before reading the shared token. The request is written only to
	// this socket; there is no transport retry or redial path to a replacement listener.
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, errors.New("connect to Kimi usage server failed")
	}
	defer conn.Close()
	deadline := time.Now().Add(kimiRequestTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, errors.New("set Kimi usage socket deadline")
	}
	select {
	case <-processDone:
		return nil, errors.New("Kimi server exited before token read")
	default:
	}
	token, err := readKimiToken(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("read Kimi server token: %w", err)
	}
	select {
	case <-processDone:
		return nil, errors.New("Kimi server exited before usage request")
	default:
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/v1/oauth/usage", port), nil)
	if err != nil {
		return nil, errors.New("create Kimi usage request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if err := request.Write(conn); err != nil {
		return nil, errors.New("request Kimi usage from local server failed")
	}
	response, err := readKimiHTTPResponse(conn, request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Kimi usage endpoint returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxKimiResponseSize+1))
	if err != nil {
		return nil, errors.New("read Kimi usage response failed")
	}
	if len(body) > maxKimiResponseSize {
		return nil, errors.New("Kimi usage response exceeded size limit")
	}
	return parseKimiUsage(body, now)
}

func readKimiHTTPResponse(conn io.Reader, request *http.Request) (*http.Response, error) {
	limited := &io.LimitedReader{R: conn, N: maxKimiResponseHeaderSize + 1}
	buffered := bufio.NewReader(limited)
	var header bytes.Buffer
	for {
		line, err := buffered.ReadString('\n')
		header.WriteString(line)
		if header.Len() > maxKimiResponseHeaderSize {
			return nil, errors.New("Kimi usage response headers exceeded size limit")
		}
		if bytes.HasSuffix(header.Bytes(), []byte("\r\n\r\n")) {
			break
		}
		if err != nil {
			return nil, errors.New("read Kimi usage response failed")
		}
	}
	response, err := http.ReadResponse(bufio.NewReader(io.MultiReader(bytes.NewReader(header.Bytes()), buffered, conn)), request)
	if err != nil {
		return nil, errors.New("read Kimi usage response failed")
	}
	return response, nil
}

func parseKimiLocalPort(line string) (int, bool) {
	const (
		label  = "Local:"
		prefix = "http://127.0.0.1:"
	)
	address := strings.TrimSpace(line)
	if !strings.HasPrefix(address, label) {
		return 0, false
	}
	address = strings.TrimSpace(address[len(label):])
	if !strings.HasPrefix(address, prefix) {
		return 0, false
	}
	rest := address[len(prefix):]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 || slash+1 < len(rest) && rest[slash+1] != '#' {
		return 0, false
	}
	portText := rest[:slash]
	for _, char := range portText {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portText {
		return 0, false
	}
	return port, true
}

func parseKimiUsage(body []byte, now time.Time) ([]kimiCachedQuota, error) {
	var response kimiUsageResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&response); err != nil {
		return nil, errors.New("Kimi usage response was invalid JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("Kimi usage response contained multiple values")
	}
	if response.Code == nil || *response.Code != 0 || response.Data == nil || response.Data.Kind != "ok" {
		return nil, errors.New("Kimi usage endpoint did not return an ok result")
	}
	windows := make([]kimiUsageWindow, 0, 1+len(response.Data.Limits))
	if response.Data.Summary != nil {
		windows = append(windows, *response.Data.Summary)
	}
	windows = append(windows, response.Data.Limits...)
	quotas := make([]kimiCachedQuota, 0, len(windows))
	seen := make(map[string]bool, len(windows))
	for _, window := range windows {
		quota, err := parseKimiWindow(window, now)
		if err != nil {
			return nil, err
		}
		if seen[quota.Window] {
			return nil, fmt.Errorf("Kimi returned duplicate %s quota window", quota.Window)
		}
		seen[quota.Window] = true
		quotas = append(quotas, quota)
	}
	if len(quotas) == 0 {
		return nil, errors.New("Kimi returned no usable quota windows")
	}
	return quotas, nil
}

func parseKimiWindow(window kimiUsageWindow, now time.Time) (kimiCachedQuota, error) {
	if window.Window == nil || window.Window.Duration == nil || *window.Window.Duration <= 0 {
		return kimiCachedQuota{}, errors.New("Kimi quota window duration is invalid")
	}
	if window.Window.Unit == nil {
		return kimiCachedQuota{}, errors.New("Kimi quota window unit is missing")
	}
	unit := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(*window.Window.Unit)), "s")
	if !knownKimiWindowUnit(unit) {
		return kimiCachedQuota{}, errors.New("Kimi quota window unit is invalid")
	}
	if !validKimiWindowDuration(*window.Window.Duration, unit) {
		return kimiCachedQuota{}, errors.New("Kimi quota window duration is invalid")
	}
	if window.Used == nil || window.Limit == nil || math.IsNaN(*window.Used) || math.IsInf(*window.Used, 0) || math.IsNaN(*window.Limit) || math.IsInf(*window.Limit, 0) || *window.Used < 0 || *window.Limit <= 0 || *window.Used > *window.Limit {
		return kimiCachedQuota{}, errors.New("Kimi quota used/limit values are invalid")
	}
	if window.ResetAt == nil {
		return kimiCachedQuota{}, errors.New("Kimi quota reset_at is missing")
	}
	reset, err := time.Parse(time.RFC3339, *window.ResetAt)
	if err != nil || reset.Year() < 2020 || reset.After(now.Add(366*24*time.Hour)) {
		return kimiCachedQuota{}, errors.New("Kimi quota reset_at is invalid")
	}
	duration := *window.Window.Duration
	return kimiCachedQuota{
		Window:              kimiWindowLabel(duration, unit),
		WindowDuration:      duration,
		WindowUnit:          unit,
		Used:                *window.Used,
		Limit:               *window.Limit,
		RemainingPercentage: 100 * (*window.Limit - *window.Used) / *window.Limit,
		ResetsAt:            reset,
	}, nil
}

func knownKimiWindowUnit(unit string) bool {
	switch unit {
	case "second", "minute", "hour", "day", "week", "month":
		return true
	default:
		return false
	}
}

func validKimiWindowDuration(duration int64, unit string) bool {
	maximum := map[string]int64{
		"second": 366 * 24 * 60 * 60,
		"minute": 366 * 24 * 60,
		"hour":   366 * 24,
		"day":    366,
		"week":   53,
		"month":  12,
	}
	return duration > 0 && duration <= maximum[unit]
}

func kimiWindowLabel(duration int64, unit string) string {
	if duration == 5 && unit == "hour" {
		return "5-hour"
	}
	if duration == 7 && unit == "day" || duration == 1 && unit == "week" {
		return "Weekly"
	}
	if duration == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", duration, unit)
}

func readKimiCache() (kimiCacheFile, error) {
	path, err := kimiCachePath()
	if err != nil {
		return kimiCacheFile{}, err
	}
	data, err := readBoundedCache(path)
	if err != nil {
		return kimiCacheFile{}, err
	}
	var cache kimiCacheFile
	if err := json.Unmarshal(data, &cache); err != nil {
		return kimiCacheFile{}, err
	}
	if err := validateKimiCache(cache, time.Now()); err != nil {
		return kimiCacheFile{}, err
	}
	return cache, nil
}

func validateKimiCache(cache kimiCacheFile, now time.Time) error {
	if cache.Version != cacheVersion || cache.Provider != "Kimi Code" {
		return errors.New("unsupported Kimi cache format")
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
		return errors.New("Kimi cache failure is missing attempted_at")
	}
	if cache.Failure != safeCollectionError(errors.New(cache.Failure)) {
		return errors.New("Kimi cache failure is invalid")
	}
	seen := make(map[string]bool, len(cache.Quotas))
	for _, quota := range cache.Quotas {
		if !knownKimiWindowUnit(quota.WindowUnit) || !validKimiWindowDuration(quota.WindowDuration, quota.WindowUnit) || quota.Window != kimiWindowLabel(quota.WindowDuration, quota.WindowUnit) {
			return errors.New("Kimi cache window is invalid")
		}
		if seen[quota.Window] {
			return fmt.Errorf("duplicate Kimi quota window %q", quota.Window)
		}
		seen[quota.Window] = true
		if math.IsNaN(quota.Used) || math.IsInf(quota.Used, 0) || math.IsNaN(quota.Limit) || math.IsInf(quota.Limit, 0) || quota.Used < 0 || quota.Limit <= 0 || quota.Used > quota.Limit {
			return errors.New("Kimi cache used/limit values are invalid")
		}
		remaining := 100 * (quota.Limit - quota.Used) / quota.Limit
		if math.IsNaN(quota.RemainingPercentage) || math.IsInf(quota.RemainingPercentage, 0) || quota.RemainingPercentage < 0 || quota.RemainingPercentage > 100 || math.Abs(quota.RemainingPercentage-remaining) > 1e-9 {
			return errors.New("Kimi cache remaining_percentage is invalid")
		}
		if quota.ResetsAt.Year() < 2020 || quota.ResetsAt.After(now.Add(366*24*time.Hour)) {
			return errors.New("Kimi cache resets_at is invalid")
		}
	}
	return nil
}

func compactKimiUsage(cache kimiCacheFile) string {
	parts := make([]string, 0, len(cache.Quotas))
	for _, quota := range cache.Quotas {
		parts = append(parts, fmt.Sprintf("%s %.0f%% left", quota.Window, quota.RemainingPercentage))
	}
	return "Kimi Code " + strings.Join(parts, " · ")
}
