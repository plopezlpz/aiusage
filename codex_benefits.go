package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

// These fields are from codex 0.166.0's generated app-server protocol. The
// optional reset detail list may be capped; its length is NOT the total count.
// Absent/null fields mean unsupported/unavailable. Present but malformed data
// is a collection failure, so it cannot erase a previously known balance.
func parseCodexBenefits(data []byte) ([]benefitGroup, error) {
	groups := []benefitGroup{{Kind: "credits"}, {Kind: "banked_resets"}}
	var envelope struct {
		RateLimits struct {
			Credits json.RawMessage `json:"credits"`
		} `json:"rateLimits"`
		Resets json.RawMessage `json:"rateLimitResetCredits"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return nil, errors.New("Codex benefits response was invalid")
	}
	present := func(raw json.RawMessage) bool {
		return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	}
	if present(envelope.RateLimits.Credits) {
		var credits struct {
			HasCredits *bool   `json:"hasCredits"`
			Unlimited  *bool   `json:"unlimited"`
			Balance    *string `json:"balance"`
		}
		if json.Unmarshal(envelope.RateLimits.Credits, &credits) != nil || credits.HasCredits == nil || credits.Unlimited == nil {
			return nil, errors.New("Codex credit balance was invalid")
		}
		b := benefitGroup{Kind: "credits"}
		if *credits.Unlimited {
			b.Unlimited = true
		} else if credits.Balance != nil {
			n, err := strconv.ParseFloat(*credits.Balance, 64)
			if err != nil {
				return nil, errors.New("Codex credit balance was invalid")
			}
			b.Total = &n
		} else if !*credits.HasCredits {
			zero := 0.0
			b.Total = &zero
		}
		groups[0] = b
	}
	if present(envelope.Resets) {
		var resets struct {
			Count   *int64 `json:"availableCount"`
			Credits []struct {
				ID        string          `json:"id"`
				Status    string          `json:"status"`
				ExpiresAt json.RawMessage `json:"expiresAt"`
			} `json:"credits"`
		}
		if json.Unmarshal(envelope.Resets, &resets) != nil || resets.Count == nil || *resets.Count < 0 || *resets.Count > 1000000 || len(resets.Credits) > 1000 {
			return nil, errors.New("Codex reset inventory was invalid")
		}
		total := float64(*resets.Count)
		b := benefitGroup{Kind: "banked_resets", Total: &total}
		for _, credit := range resets.Credits {
			if credit.Status != "available" {
				continue
			}
			grant := benefitGrant{ID: credit.ID, Label: "1 reset", Amount: 1}
			if bytes.Equal(bytes.TrimSpace(credit.ExpiresAt), []byte("null")) {
				grant.NoExpiry = true // Explicitly documented as non-expiring.
			} else if len(credit.ExpiresAt) > 0 {
				var stamp int64
				if json.Unmarshal(credit.ExpiresAt, &stamp) != nil {
					return nil, errors.New("Codex reset expiration was invalid")
				}
				grant.ExpiresAt = &stamp
			}
			b.Grants = append(b.Grants, grant)
		}
		groups[1] = b
	}
	if err := validateBenefits(groups); err != nil {
		return nil, errors.New("Codex benefit values were invalid")
	}
	groups[0].Note = "Codex credits are separate from included quota. The app-server does not report credit expiration dates."
	if groups[1].Total == nil {
		groups[1].Note = "Banked resets were not reported by this Codex app-server. Check Codex usage settings; updating Codex may expose reset details."
	}
	return groups, nil
}
