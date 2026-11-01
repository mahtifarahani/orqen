// Package review parses and validates the reviewer's answer. It is pure.
package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// Marker introduces the machine-readable part of a review.
const Marker = "ORQEN-REVIEW v1"

// Verdicts and severities.
const (
	VerdictApproved         = "approved"
	VerdictChangesRequested = "changes_requested"
	VerdictApprovedScope    = "approved_with_scope"
)

// Finding is one finding as the reviewer wrote it, plus what validation found out.
type Finding struct {
	ID            string `json:"id"`
	Severity      string `json:"severity"`
	File          string `json:"file"`
	Line          int    `json:"line"`
	Side          string `json:"side"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	AcceptanceRef string `json:"acceptance_ref"`

	// Filled by Validate.
	Blocking       bool `json:"-"`
	LocationValid  bool `json:"-"`
	InlineEligible bool `json:"-"`
}

// Result is a parsed review.
type Result struct {
	Verdict  string    `json:"verdict"`
	Scope    string    `json:"scope"`
	Findings []Finding `json:"findings"`
}

// ErrUnparseable means the reply has no valid ORQEN-REVIEW block.
var ErrUnparseable = errors.New("review: no valid ORQEN-REVIEW v1 block")

var severities = map[string]bool{"blocker": true, "major": true, "minor": true, "nit": true}

func clip(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}

// Parse finds the last marker in the reply and decodes the JSON that follows (anything after the JSON, such as
// catherd's STATUS line, is ignored).
func Parse(reply string) (Result, error) {
	i := strings.LastIndex(reply, Marker)
	if i < 0 {
		return Result{}, ErrUnparseable
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(reply[i+len(Marker):])))
	var r Result
	if err := dec.Decode(&r); err != nil {
		return Result{}, ErrUnparseable
	}
	switch r.Verdict {
	case VerdictApproved, VerdictChangesRequested, VerdictApprovedScope:
	default:
		return Result{}, ErrUnparseable
	}
	seen := map[string]bool{}
	for i := range r.Findings {
		f := &r.Findings[i]
		if f.ID == "" || seen[f.ID] || !severities[f.Severity] || strings.TrimSpace(f.Title) == "" {
			return Result{}, ErrUnparseable
		}
		seen[f.ID] = true
		if f.Side != "old" {
			f.Side = "new"
		}
		f.Title, f.Body = clip(strings.TrimSpace(f.Title), 200), clip(f.Body, 4000)
		f.Blocking = f.Severity == "blocker" || f.Severity == "major"
	}
	r.Scope = clip(r.Scope, 2000)
	return r, nil
}

// Blocking counts the blocking findings.
func (r Result) Blocking() int {
	n := 0
	for _, f := range r.Findings {
		if f.Blocking {
			n++
		}
	}
	return n
}

// Effective is the verdict that counts: an approval that carries a blocking finding is a request for changes.
func (r Result) Effective() string {
	if r.Verdict != VerdictChangesRequested && r.Blocking() > 0 {
		return VerdictChangesRequested
	}
	return r.Verdict
}
