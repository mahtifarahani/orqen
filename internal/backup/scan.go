package backup

import (
	"bytes"
	"encoding/base64"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// PatternsVersion labels the pattern set recorded in the manifest.
const PatternsVersion = "1"

// Hit kinds.
const (
	HitKnownValue = "known_value"
	HitPattern    = "pattern"
)

// Hit is a finding without the value that caused it.
type Hit struct {
	Kind string `json:"kind"`
	Code string `json:"code"` // for patterns: the pattern code; for known values: "known_value"
}

var patterns = []struct {
	code string
	re   *regexp.Regexp
}{
	{"gitlab_token", regexp.MustCompile(`glpat-[A-Za-z0-9_\-]{8,}`)},
	{"github_token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{20,}`)},
	{"api_key_sk", regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{16,}`)},
	{"aws_access_key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"private_key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"slack_token", regexp.MustCompile(`xox[bp]-[A-Za-z0-9\-]{8,}`)},
	{"bearer_header", regexp.MustCompile(`(?i)Authorization:\s*Bearer\s+[A-Za-z0-9._~+/=\-]{20,}`)},
}

// PatternCodes lists the pattern codes.
func PatternCodes() []string {
	out := make([]string, len(patterns))
	for i, p := range patterns {
		out[i] = p.code
	}
	return out
}

const minKnown = 6

// Scanner looks for known credential values (raw, base64 in every byte alignment, base64url, URL-encoded) and for
// secret-looking patterns. It never returns or logs a value, and holds the known values in memory only.
type Scanner struct {
	needles [][]byte
	checked int
}

// NewScanner prepares the search forms of every known value. Values shorter than six characters are ignored (they
// would match noise) and not counted.
func NewScanner(values []string) *Scanner {
	s := &Scanner{}
	seen := map[string]bool{}
	for _, v := range values {
		if len(v) < minKnown || seen[v] {
			continue
		}
		seen[v] = true
		s.checked++
		for _, f := range forms(v) {
			if len(f) >= minKnown {
				s.needles = append(s.needles, []byte(f))
			}
		}
	}
	return s
}

// KnownChecked is the number of distinct known values the scanner searches for.
func (s *Scanner) KnownChecked() int { return s.checked }

func forms(v string) []string {
	out := []string{v, url.QueryEscape(v), url.PathEscape(v)}
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		for k := 0; k < 3; k++ {
			e := enc.EncodeToString([]byte(strings.Repeat("A", k) + v))
			skip := [3]int{0, 2, 3}[k]
			if (k+len(v))%3 != 0 && len(e) > 0 {
				e = e[:len(e)-1]
			}
			if skip < len(e) {
				out = append(out, e[skip:])
			}
		}
	}
	sort.Strings(out)
	uniq := out[:0]
	for i, f := range out {
		if i == 0 || f != out[i-1] {
			uniq = append(uniq, f)
		}
	}
	return uniq
}

// Scan returns the hits in data: at most one known_value hit and at most one hit per pattern code.
func (s *Scanner) Scan(data []byte) []Hit {
	var hits []Hit
	for _, n := range s.needles {
		if indexBytes(data, n) >= 0 {
			hits = append(hits, Hit{Kind: HitKnownValue, Code: HitKnownValue})
			break
		}
	}
	for _, p := range patterns {
		if p.re.Match(data) {
			hits = append(hits, Hit{Kind: HitPattern, Code: p.code})
		}
	}
	return hits
}

func indexBytes(b, sub []byte) int { return bytes.Index(b, sub) }

// SecretError is the failure for a hit in a document or blob. Location names the collection/_id or the artifact id,
// never the value.
type SecretError struct {
	Location string
	Hit      Hit
}

func (e *SecretError) Error() string {
	return "backup: secret detected (" + e.Hit.Kind + " " + e.Hit.Code + ") at " + e.Location
}
