// Package contextpkt builds the bounded, ordered context packet for a run. It is pure: the server collects the
// sources and passes plain structs in.
package contextpkt

import "regexp"

var scrubbers = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=\-]{8,}`),
	regexp.MustCompile(`glpat-[A-Za-z0-9_\-]{8,}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`xox[abprs]-[A-Za-z0-9\-]{8,}`),
	regexp.MustCompile(`(?i)(?:password|token|secret|api[_-]?key)\s*[=:]\s*\S+`),
}

const secretMark = "[secret]"

// Scrub replaces secret-looking substrings of external text with [secret].
func Scrub(s string) string {
	for _, re := range scrubbers {
		s = re.ReplaceAllString(s, secretMark)
	}
	return s
}
