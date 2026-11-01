package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"orqen/internal/canon"
	"orqen/internal/intake"
)

// NormText is the S11 text normalization used for every comparison and hash.
func NormText(s string) string { return intake.Normalize(s) }

func SHA(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

type hashClaim struct {
	ID       string     `json:"id"`
	Text     string     `json:"text"`
	Template *Template  `json:"template,omitempty"`
	Fact     *Fact      `json:"fact,omitempty"`
	Critical bool       `json:"critical"`
	Evidence []Evidence `json:"evidence"`
}

// ContentHash covers the immutable content of a version (not verdicts, status or flags).
func ContentHash(v Version) string {
	cs := make([]hashClaim, 0, len(v.Claims))
	for _, c := range v.Claims {
		ev := c.Evidence
		if ev == nil {
			ev = []Evidence{}
		}
		cs = append(cs, hashClaim{c.ID, c.Text, c.Template, c.Fact, c.Critical, ev})
	}
	ts := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	return SHA(canon.JSON(map[string]any{
		"type": v.Type, "scope_key": v.ScopeKey, "title": v.Title, "body": v.Body, "claims": cs,
		"valid_from": ts(v.ValidFrom), "valid_until": ts(v.ValidUntil),
	}))
}
