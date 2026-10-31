package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

const benefitWarningWindow = 48 * time.Hour

// Benefits are balances/grants, not recurring quota windows. A nil total is
// unavailable, not zero; a nil expiry is unknown unless NoExpiry is explicit.
type benefitGroup struct {
	Kind      string         `json:"kind"`
	Total     *float64       `json:"total,omitempty"`
	Unlimited bool           `json:"unlimited,omitempty"`
	Grants    []benefitGrant `json:"grants,omitempty"`
	Note      string         `json:"note,omitempty"`
}

type benefitGrant struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	Amount    float64 `json:"amount"`
	ExpiresAt *int64  `json:"expiresAt,omitempty"`
	NoExpiry  bool    `json:"noExpiry,omitempty"`
}

func (b benefitGroup) title() string {
	switch b.Kind {
	case "api_promo":
		return "API promo credits"
	case "limit_resets":
		return "Limit resets"
	case "banked_resets":
		return "Banked resets"
	default:
		return "Credits"
	}
}

func (b benefitGroup) amountText(amount float64) string {
	switch b.Kind {
	case "api_promo":
		return fmt.Sprintf("$%.2f", amount)
	case "limit_resets", "banked_resets":
		return fmt.Sprintf("%.0f available", amount)
	default:
		return formatPercent(amount) + " credits"
	}
}

func (b benefitGroup) available(now time.Time) *float64 {
	if b.Total == nil {
		return nil
	}
	total := *b.Total
	for _, grant := range b.Grants {
		if grant.expired(now) {
			total -= grant.Amount
		}
	}
	total = max(0, total)
	return &total
}

func (b benefitGroup) valueText(now time.Time) string {
	if b.Unlimited {
		return "Unlimited"
	}
	if total := b.available(now); total != nil {
		return b.amountText(*total)
	}
	return "Not reported"
}

func (b benefitGroup) missingAmount() float64 {
	if b.Total == nil {
		return 0
	}
	known := 0.0
	for _, grant := range b.Grants {
		known += grant.Amount
	}
	return max(0, *b.Total-known)
}

func (b benefitGroup) missingText() string {
	amount := b.amountText(b.missingAmount())
	if b.Kind == "banked_resets" || b.Kind == "limit_resets" {
		amount = fmt.Sprintf("%.0f resets", b.missingAmount())
		if b.missingAmount() == 1 {
			amount = "1 reset"
		}
	}
	return "Expiration details unavailable for " + amount
}

func (b benefitGroup) expandable() bool {
	return len(b.Grants) > 1 || len(b.Grants) > 0 && b.missingAmount() > 0
}

func (g benefitGrant) expired(now time.Time) bool {
	return g.ExpiresAt != nil && *g.ExpiresAt <= now.Unix()
}

func (b benefitGroup) sortedGrants() []benefitGrant {
	grants := append([]benefitGrant(nil), b.Grants...)
	sort.SliceStable(grants, func(i, j int) bool {
		a, z := grants[i].ExpiresAt, grants[j].ExpiresAt
		if a == nil {
			return false
		}
		return z == nil || *a < *z
	})
	return grants
}

// Never imply the earliest known expiry covers grants whose details are missing.
func (b benefitGroup) expiry(now time.Time) (string, bool) {
	var nearest *int64
	active, noExpiry, expired := 0, 0, 0
	unknownExpiry := b.missingAmount() > 0
	for _, grant := range b.Grants {
		if grant.expired(now) {
			expired++
			continue
		}
		active++
		unknownExpiry = unknownExpiry || grant.ExpiresAt == nil && !grant.NoExpiry
		if grant.NoExpiry {
			noExpiry++
		}
		if grant.ExpiresAt != nil && (nearest == nil || *grant.ExpiresAt < *nearest) {
			nearest = grant.ExpiresAt
		}
	}
	if nearest != nil {
		text, warning := benefitExpiryText(nearest, false, now)
		if unknownExpiry {
			text = "Next known: " + strings.ToLower(text)
		} else if len(b.Grants) > 1 {
			text = "Next " + strings.ToLower(text)
		}
		return text, warning
	}
	if active > 0 && active == noExpiry && b.missingAmount() == 0 {
		return "No expiration", false
	}
	if expired > 0 && active == 0 && b.missingAmount() == 0 {
		return "Expired", false
	}
	return "Expiry unknown", false
}

func benefitExpiryText(stamp *int64, noExpiry bool, now time.Time) (string, bool) {
	if noExpiry {
		return "No expiration", false
	}
	if stamp == nil {
		return "Expiry unknown", false
	}
	expires := time.Unix(*stamp, 0)
	d := expires.Sub(now)
	switch {
	case d <= 0:
		return "Expired", false
	case d < time.Minute:
		return "Expires in <1m", true
	case d < time.Hour:
		return fmt.Sprintf("Expires in %dm", int(math.Ceil(d.Minutes()))), true
	case d < 24*time.Hour:
		return fmt.Sprintf("Expires in %dh", int(math.Ceil(d.Hours()))), true
	case d <= benefitWarningWindow:
		return fmt.Sprintf("Expires in %dd", int(math.Ceil(d.Hours()/24))), true
	default:
		return "Expires " + expires.Local().Format("Jan 2, 2006"), false
	}
}

func validBenefitText(text string, limit int) bool {
	if len(text) > limit || strings.TrimSpace(text) != text {
		return false
	}
	for _, r := range text {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func validateBenefits(groups []benefitGroup) error {
	if len(groups) > 4 {
		return errors.New("too many benefit groups")
	}
	seen := map[string]bool{}
	for _, b := range groups {
		switch b.Kind {
		case "api_promo", "limit_resets", "banked_resets", "credits":
		default:
			return errors.New("invalid benefit kind")
		}
		if seen[b.Kind] || !validBenefitText(b.Note, 512) || len(b.Grants) > 1000 {
			return errors.New("invalid benefit group")
		}
		seen[b.Kind] = true
		if b.Unlimited && (b.Total != nil || len(b.Grants) > 0) {
			return errors.New("conflicting unlimited benefit")
		}
		validAmount := func(n float64) bool {
			return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 1e12 && (b.Kind != "banked_resets" && b.Kind != "limit_resets" || math.Trunc(n) == n)
		}
		if b.Total != nil && !validAmount(*b.Total) {
			return errors.New("invalid benefit total")
		}
		ids, sum := map[string]bool{}, 0.0
		for _, grant := range b.Grants {
			if grant.ID == "" || !validBenefitText(grant.ID, 256) || ids[grant.ID] || !validBenefitText(grant.Label, 128) || !validAmount(grant.Amount) || grant.Amount == 0 {
				return errors.New("invalid benefit grant")
			}
			ids[grant.ID] = true
			if grant.NoExpiry && grant.ExpiresAt != nil {
				return errors.New("conflicting benefit expiration")
			}
			if grant.ExpiresAt != nil && (*grant.ExpiresAt < 1577836800 || *grant.ExpiresAt > 7258118400) {
				return errors.New("invalid benefit expiration")
			}
			sum += grant.Amount
		}
		if len(b.Grants) > 0 && (b.Total == nil || sum > *b.Total+0.000001) {
			return errors.New("benefit grants exceed total")
		}
	}
	return nil
}

func unavailableClaudeBenefits() []benefitGroup {
	return []benefitGroup{
		{Kind: "api_promo", Note: "API billing is not connected. Check promotional balances and expirations at https://platform.claude.com/settings/billing."},
		{Kind: "limit_resets", Note: "Not reported by the connected usage source. Check https://claude.ai/settings/usage for available limit resets and their expirations."},
	}
}
