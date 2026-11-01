package catherd

import (
	"regexp"
	"strings"
)

var (
	errLine = regexp.MustCompile(`(?m)^error (E_[A-Z_]+): (.*)$`)
	fixLine = regexp.MustCompile(`(?m)^fix: (.*)$`)
	// A refusal joins "<path>: <message>" pieces with "; "; a new piece starts at a profile path.
	pieceStart = regexp.MustCompile(`; ((?:objective|jev|billing|roles|harness|failover|budget|timeouts|preflight|lock|notify|profile)[^ ]*: )`)
	secretish  = regexp.MustCompile(`(?i)((?:token|secret|password|authorization|api[_-]?key|cookie)[=: ]+)\S+`)
	longToken  = regexp.MustCompile(`\b[A-Za-z0-9_\-]{40,}\b`)
)

// ScrubText removes secret-looking values and caps catherd's stderr at 2 KiB before it is stored.
func ScrubText(s string) string {
	s = secretish.ReplaceAllString(s, "${1}[REDACTED]")
	s = longToken.ReplaceAllString(s, "[REDACTED]")
	if len(s) > 2048 {
		s = s[:2048]
	}
	return s
}
func parseStderr(stderr string) (code, message, fix string) {
	if m := errLine.FindStringSubmatch(stderr); m != nil {
		code, message = m[1], strings.TrimSpace(m[2])
	}
	if m := fixLine.FindStringSubmatch(stderr); m != nil {
		fix = strings.TrimSpace(m[1])
	}
	return
}

// SplitPath splits a dotted profile path; failover and billing keep the rest as one key (rungs hold dots).
func SplitPath(p string) []string {
	segs := strings.Split(p, ".")
	if len(segs) > 1 && (segs[0] == "failover" || segs[0] == "billing") {
		return []string{segs[0], strings.Join(segs[1:], ".")}
	}
	return segs
}

const refusedPrefix = "the profile was not saved: "

// refusalIssues splits "the profile was not saved: a: x; b: y" into issues; an unparsable message is one issue.
func refusalIssues(message, fix string) []Issue {
	body := strings.TrimPrefix(message, refusedPrefix)
	var pieces []string
	cut := []int{0}
	for _, m := range pieceStart.FindAllStringSubmatchIndex(body, -1) {
		cut = append(cut, m[0])
	}
	cut = append(cut, len(body)+2)
	for i := 0; i+1 < len(cut); i++ {
		start := cut[i]
		if i > 0 {
			start += 2
		}
		end := cut[i+1]
		if end > len(body) {
			end = len(body)
		}
		pieces = append(pieces, body[start:end])
	}
	var out []Issue
	for _, p := range pieces {
		path, msg, ok := strings.Cut(p, ": ")
		if !ok || strings.ContainsAny(path, " ") {
			return []Issue{{Path: []string{"profile"}, PathDisplay: "profile", Code: "catherd_refused", Message: ScrubText(body), Fix: fix, Origin: "catherd", Severity: "error"}}
		}
		out = append(out, Issue{Path: SplitPath(path), PathDisplay: path, Code: "catherd_refused", Message: ScrubText(strings.TrimSpace(msg)), Fix: fix, Origin: "catherd", Severity: "error"})
	}
	if len(out) == 0 {
		return []Issue{{Path: []string{"profile"}, PathDisplay: "profile", Code: "catherd_refused", Message: ScrubText(body), Fix: fix, Origin: "catherd", Severity: "error"}}
	}
	return out
}
