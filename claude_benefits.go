package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const claudeBenefitsEndpoint = claudeUsageEndpoint + "?cedar_ember=1&skip_spend=1"

type benefitState struct {
	Groups      []benefitGroup `json:"groups"`
	UpdatedAt   time.Time      `json:"updated_at"`
	AttemptedAt time.Time      `json:"attempted_at"`
	Failure     string         `json:"failure,omitempty"`
}

func (s benefitState) validate(now time.Time) error {
	if err := validateBenefits(s.Groups); err != nil {
		return err
	}
	if len(s.Groups) > 0 {
		if err := validatePastTimestamp(s.UpdatedAt, now, "benefits updated_at"); err != nil {
			return err
		}
	}
	if err := validatePastTimestamp(s.AttemptedAt, now, "benefits attempted_at"); err != nil {
		return err
	}
	if s.Failure != safeCollectionError(errors.New(s.Failure)) {
		return errors.New("invalid benefits failure")
	}
	return nil
}

func fetchClaudeBenefits(ctx context.Context, client claudeHTTPDoer, token string, now time.Time) ([]benefitGroup, error) {
	body, err := fetchClaudeJSON(ctx, client, token, claudeBenefitsEndpoint)
	if err != nil {
		return nil, err
	}
	return parseClaudeBenefits(body, now)
}

// The read-only variant and cedar_ember grant fields are consumed by Claude
// Code 2.1.314. They describe limit-reset inventory, NOT Console API credits.
func parseClaudeBenefits(body []byte, now time.Time) ([]benefitGroup, error) {
	var response struct {
		Resets *struct {
			Eligible *bool           `json:"eligible"`
			Grants   json.RawMessage `json:"grants"`
		} `json:"cedar_ember"`
	}
	if json.Unmarshal(body, &response) != nil {
		return nil, errors.New("Claude limit resets response was invalid")
	}
	unknown := []benefitGroup{{Kind: "limit_resets", Note: "Limit-reset inventory was not reported. Check https://claude.ai/settings/usage."}}
	if response.Resets == nil || response.Resets.Eligible == nil || len(response.Resets.Grants) == 0 || string(response.Resets.Grants) == "null" {
		return unknown, nil
	}
	var grants []struct {
		ID       string  `json:"id"`
		Left     *int64  `json:"resets_left"`
		StartsAt *string `json:"starts_at"`
		EndsAt   *string `json:"ends_at"`
		Paused   *bool   `json:"paused"`
	}
	if json.Unmarshal(response.Resets.Grants, &grants) != nil || len(grants) > 1000 {
		return nil, errors.New("Claude limit reset grants were invalid")
	}
	// Ineligible is not proof of a zero inventory. Avoid presenting grants as
	// spendable when the program reports the account cannot use them.
	if !*response.Resets.Eligible {
		return unknown, nil
	}
	total := 0.0
	group := benefitGroup{Kind: "limit_resets", Total: &total}
	for _, raw := range grants {
		if raw.Left == nil || *raw.Left < 0 || *raw.Left > 1000000 || raw.Paused == nil {
			return nil, errors.New("Claude limit reset grant was incomplete")
		}
		if *raw.Left == 0 || *raw.Paused {
			continue
		}
		if raw.StartsAt != nil {
			start, err := time.Parse(time.RFC3339, *raw.StartsAt)
			if err != nil {
				return nil, errors.New("Claude reset start date was invalid")
			}
			if start.After(now) {
				continue
			}
		}
		grant := benefitGrant{ID: raw.ID, Label: "Limit reset", Amount: float64(*raw.Left)}
		if raw.EndsAt != nil {
			expires, err := time.Parse(time.RFC3339, *raw.EndsAt)
			if err != nil {
				return nil, errors.New("Claude reset expiration was invalid")
			}
			stamp := expires.Unix()
			grant.ExpiresAt = &stamp
		}
		// Missing/null ends_at has no proven non-expiring semantics here.
		total += grant.Amount
		group.Grants = append(group.Grants, grant)
	}
	if validateBenefits([]benefitGroup{group}) != nil {
		return nil, errors.New("Claude limit reset grants were invalid")
	}
	group.Note = "One-time limit resets, separate from scheduled quota resets. Redemption remains in Claude; eligibility conditions may apply."
	return []benefitGroup{group}, nil
}
