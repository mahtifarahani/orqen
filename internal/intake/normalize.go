package intake

import (
	"regexp"
	"strings"
)

var (
	blankRun   = regexp.MustCompile(`\n{4,}`)
	trailingWS = regexp.MustCompile(`[ \t]+\n`)
)

// Normalize makes text comparable: CRLF and CR become LF, trailing spaces per line go, three or more blank
// lines collapse to two, and the result is trimmed. Unicode normalization is deliberately not applied.
func Normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = trailingWS.ReplaceAllString(s+"\n", "\n")
	s = blankRun.ReplaceAllString(s, "\n\n\n")
	return strings.TrimSpace(s)
}
