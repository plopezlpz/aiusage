package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

type dashboardSnapshotBenefit struct {
	ID          string       `json:"id"`
	Provider    string       `json:"provider"`
	Product     string       `json:"product"`
	Group       benefitGroup `json:"group"`
	UpdatedAt   *int64       `json:"updatedAt"`
	AttemptedAt *int64       `json:"attemptedAt"`
	Failure     string       `json:"failure"`
	Source      string       `json:"source"`
	Stale       bool         `json:"stale"`
}

func snapshotBenefit(q quota) dashboardSnapshotBenefit {
	return dashboardSnapshotBenefit{
		ID: q.Provider + ":benefit:" + q.Benefit.Kind, Provider: q.Provider, Product: q.Product,
		Group: *q.Benefit, UpdatedAt: unixTimeValue(q.UpdatedAt), AttemptedAt: unixTimeValue(q.AttemptedAt),
		Failure: q.Failure, Source: q.Source, Stale: q.Stale,
	}
}

func (m *model) appendBenefits(provider, product string, groups []benefitGroup, updated, attempted time.Time, failure, source string, now time.Time) {
	for _, group := range groups {
		rowUpdated, rowAttempted, rowFailure, rowSource := updated, attempted, failure, source
		stale := failure != "" || quotaIsStale(updated, nil, now)
		if provider == "Claude" && group.Kind == "api_promo" && group.Total == nil {
			rowUpdated, rowAttempted, rowFailure = time.Time{}, time.Time{}, ""
			rowSource, stale = "Claude Platform billing (not connected)", false
		}
		m.quotas = append(m.quotas, quota{
			Provider: provider, Product: product, Window: group.title(), Benefit: &group,
			UpdatedAt: rowUpdated, AttemptedAt: rowAttempted, Failure: rowFailure, Source: rowSource,
			Stale: stale,
		})
	}
}

func benefitExpiryView(text string, warning bool) string {
	if warning {
		return warningStyle.Render(text)
	}
	return dimStyle.Render(text)
}

func benefitSummaryLines(prefix, text, expiry string, warning bool, width int) []string {
	if lipgloss.Width(prefix+text+"  "+expiry) <= width {
		return []string{prefix + text + strings.Repeat(" ", max(2, width-lipgloss.Width(prefix+text+expiry))) + benefitExpiryView(expiry, warning)}
	}
	lines := wrapText(prefix+text, width)
	indent := strings.Repeat(" ", min(4, max(0, width-1)))
	for _, line := range wrapText(expiry, max(1, width-len(indent))) {
		lines = append(lines, indent+benefitExpiryView(line, warning))
	}
	return lines
}

func (m model) benefitLine(q quota, selected bool, width int, now time.Time) string {
	b := q.Benefit
	cursor := "  "
	if selected {
		cursor = "> "
	}
	arrow := "  "
	expanded := m.expandedBenefits[quotaKey(q)] && b.expandable()
	if b.expandable() {
		arrow = "▸ "
		if expanded {
			arrow = "▾ "
		}
	}
	expiry, warning := b.expiry(now)
	lines := benefitSummaryLines(cursor+arrow, b.title()+"  "+b.valueText(now), expiry, warning, width)
	if q.Failure != "" {
		lines = append(lines, wrapText("    Stale · "+refreshFailureStatus(q), width)...)
	} else if q.Stale {
		lines = append(lines, dimStyle.Render(truncateText("    Stale · updated "+freshness(q.UpdatedAt), width)))
	}
	if expanded {
		lines = append(lines, benefitGrantLines(*b, width, now)...)
	}
	return strings.Join(lines, "\n")
}

func indentedBenefitLines(text string, indent, width int) []string {
	padding := strings.Repeat(" ", min(indent, max(0, width-1)))
	lines := wrapText(text, max(1, width-len(padding)))
	for i := range lines {
		lines[i] = padding + dimStyle.Render(lines[i])
	}
	return lines
}

func benefitGrantLines(b benefitGroup, width int, now time.Time) []string {
	var lines []string
	for _, grant := range b.sortedGrants() {
		expiry, warning := benefitExpiryText(grant.ExpiresAt, grant.NoExpiry, now)
		amount := b.amountText(grant.Amount)
		if grant.expired(now) {
			amount += " (expired)"
		}
		lines = append(lines, benefitSummaryLines("    ", amount, expiry, warning, width)...)
		if grant.ExpiresAt != nil {
			lines = append(lines, indentedBenefitLines(time.Unix(*grant.ExpiresAt, 0).Local().Format("Jan 2, 2006 15:04 MST"), 6, width)...)
		}
	}
	if missing := b.missingAmount(); missing > 0 {
		lines = append(lines, indentedBenefitLines(b.missingText(), 4, width)...)
	}
	return lines
}

func (m model) benefitDetail(q quota, width, height int) string {
	return m.benefitViewport(q, benefitDetailLines(q, width, time.Now()), width, height, "[←] back [pgup/pgdn] scroll [q] quit")
}

func benefitDetailLines(q quota, width int, now time.Time) []string {
	b := q.Benefit
	expiry, warning := b.expiry(now)
	lines := []string{titleStyle.Render(truncateText(q.Provider+" · "+q.Product, width))}
	lines = append(lines, benefitSummaryLines("", b.title()+"  "+b.valueText(now), expiry, warning, width)...)
	lines = append(lines, benefitGrantLines(*b, width, now)...)
	lines = append(lines, wrapText(b.Note, width)...)
	lines = append(lines, wrapText("Source: "+q.Source, width)...)
	lines = append(lines, wrapText("Last success: "+freshness(q.UpdatedAt), width)...)
	if q.Stale {
		lines = append(lines, wrapText("Values are stale", width)...)
	}
	if q.Failure != "" {
		lines = append(lines, wrapText(refreshFailureStatus(q), width)...)
	}
	return lines
}

func (m model) compactBenefit(q quota, width, height int) string {
	lines := []string{providerStyle.Render(truncateText(q.Provider+" · "+q.Product, width))}
	lines = append(lines, strings.Split(m.benefitLine(q, true, width, time.Now()), "\n")...)
	return m.benefitViewport(q, lines, width, height, "[↑↓] select [enter] expand [pgup/pgdn] scroll [r] [q]")
}

func (m model) benefitViewport(q quota, lines []string, width, height int, footer string) string {
	if height <= 0 {
		return ""
	}
	if height == 1 {
		return truncateText(q.Benefit.title()+" "+q.Benefit.valueText(time.Now()), width)
	}
	count := height - 1
	offset := min(m.benefitScroll[quotaKey(q)], max(0, len(lines)-count))
	shown := append([]string(nil), lines[offset:min(len(lines), offset+count)]...)
	if len(lines) > count {
		footer = fmt.Sprintf("%d-%d/%d ", offset+1, min(len(lines), offset+count), len(lines)) + footer
	}
	shown = append(shown, dimStyle.Render(truncateText(footer, width)))
	return strings.Join(shown, "\n")
}

// Keep the regular provider layout in short terminals; only use the focused
// fallback when a single expanded benefit itself exceeds the available space.
func (m model) scrolledDashboard(width, height int) string {
	if height < 6 || len(m.quotas) == 0 {
		return m.compactDashboard(width, height)
	}
	q := m.quotas[m.selected]
	if q.Benefit != nil && m.expandedBenefits[quotaKey(q)] && strings.Count(m.benefitLine(q, true, width, time.Now()), "\n")+1 > height-4 {
		return m.compactBenefit(q, width, height)
	}
	var body []string
	selectedStart, selectedEnd := 0, 0
	lastProvider := ""
	for i, row := range m.quotas {
		if row.Provider != lastProvider {
			body = append(body, strings.Split(providerLine(row, width), "\n")...)
			lastProvider = row.Provider
		}
		if i == m.selected {
			selectedStart = len(body)
		}
		body = append(body, strings.Split(m.quotaLine(row, i == m.selected, width), "\n")...)
		if i == m.selected {
			selectedEnd = len(body)
		}
	}
	count := height - 3
	start := max(0, selectedEnd-count)
	start = min(start, selectedStart)
	end := min(len(body), start+count)
	header := "AI Usage"
	if m.demo {
		header += " · DEMO DATA"
	}
	lines := []string{titleStyle.Render(truncateText(header, width))}
	lines = append(lines, body[start:end]...)
	status := fmt.Sprintf("Rows %d–%d/%d", start+1, end, len(body))
	if global := m.globalStatus(); global != "" {
		status += " · " + global
	}
	lines = append(lines, dimStyle.Render(truncateText(status, width)), dimStyle.Render(truncateText("[↑↓] select [enter] details/expand [←] collapse [d] details [r] [q]", width)))
	return strings.Join(lines, "\n")
}

func (m *model) addDemoBenefits(now time.Time) {
	near, later := now.Add(24*time.Hour).Unix(), now.Add(14*24*time.Hour).Unix()
	promo, resets := 82.40, 3.0
	original := append([]quota(nil), m.quotas...)
	m.quotas = original[:2:2]
	m.appendBenefits("Claude", "Max", []benefitGroup{
		{Kind: "api_promo", Total: &promo, Grants: []benefitGrant{
			{ID: "demo-promo-1", Label: "API promotional credit", Amount: 12.40, ExpiresAt: &near},
			{ID: "demo-promo-2", Label: "API promotional credit", Amount: 70, ExpiresAt: &later},
		}},
		{Kind: "limit_resets", Total: &resets, Grants: []benefitGrant{
			{ID: "demo-reset-1", Label: "Limit reset", Amount: 1, ExpiresAt: &near},
			{ID: "demo-reset-2", Label: "Limit reset", Amount: 2, ExpiresAt: &later},
		}},
	}, now, now, "", "demo fixture — no account accessed", now)
	m.quotas = append(m.quotas, original[2:]...)
}

func (m *model) scrollBenefit(down bool) {
	if len(m.quotas) == 0 {
		return
	}
	q := m.quotas[m.selected]
	if q.Benefit == nil {
		return
	}
	if m.benefitScroll == nil {
		m.benefitScroll = map[string]int{}
	}
	key := quotaKey(q)
	step := max(1, m.height-2)
	// Bound the offset to the rendered content, including wrapped detail text.
	padding := 2
	if m.width < 40 {
		padding = 1
	}
	if m.width < 20 {
		padding = 0
	}
	width := max(1, m.width-2*padding)
	lines := strings.Count(m.benefitLine(q, true, width, time.Now()), "\n") + 2
	if m.detail {
		lines = len(benefitDetailLines(q, width, time.Now()))
	}
	if down {
		m.benefitScroll[key] = min(max(0, lines-max(1, m.height-1)), m.benefitScroll[key]+step)
	} else {
		m.benefitScroll[key] = max(0, m.benefitScroll[key]-step)
	}
}
