package skills

import (
	"regexp"
	"sort"
	"strings"

	"orqen/internal/knowledge"
)

// Lint flag codes. secret_like is blocking; every other flag can be approved after an explicit acknowledgement.
const (
	FlagOrchestrator = "orchestrator_instruction"
	FlagToolOrCred   = "tool_or_credential"
	FlagPolicyChange = "policy_change"
	FlagNetworkFetch = "network_fetch"
	FlagInjection    = "injection_suspect"
	FlagSecretLike   = "secret_like"
)

// FlagCodes lists the lint codes (the i18n catalog adds unknown_key, which is a parse warning).
var FlagCodes = []string{FlagOrchestrator, FlagToolOrCred, FlagPolicyChange, FlagNetworkFetch, FlagInjection, FlagSecretLike}

// Flag is one lint finding with the 1-based line of the original file.
type Flag struct {
	Code     string `json:"code" bson:"code"`
	Line     int    `json:"line" bson:"line"`
	Blocking bool   `json:"blocking,omitempty" bson:"blocking,omitempty"`
}

var lintRules = []struct {
	code string
	re   *regexp.Regexp
}{
	{FlagOrchestrator, regexp.MustCompile(`(?i)\bsub-?agents?\b|\bdispatch(ing|es)?\b|\btask\s+tool\b|\bspawn(ing)?\s+(an?\s+|other\s+)?(sub-?)?agents?\b|\b(start|launch)(ing)?\s+(another|a\s+new|other|an?)\s+agents?\b|\bparallel\s+agents?\b|زیر\s*عامل|(ایجنت|عامل)\s*(دیگر|جدید|موازی)|اجرای\s*موازی\s*(ایجنت|عامل)|اعزام`)},
	{FlagToolOrCred, regexp.MustCompile(`(?i)\b(tokens?|api[_ -]?keys?|credentials?|secrets?|passwords?)\b|\.env\b|\b(webfetch|websearch|bash\s+tool|mcp)\b|توکن|رمز\s*عبور|اعتبارنامه|کلید\s*(api|دسترسی)`)},
	{FlagPolicyChange, regexp.MustCompile(`(?i)\b(approve|publish|merge|push)\b|\bskip\s+(the\s+)?(code\s+)?(review|tests?|verification|checks?)\b|تایید\s*کن|منتشر\s*کن|ادغام\s*کن|پوش\s*کن|بدون\s*(بازبینی|تست)`)},
	{FlagNetworkFetch, regexp.MustCompile(`(?i)https?://|\b(curl|wget)\b|\b(npm|pnpm|yarn|pip3?|pipx|brew|apt(-get)?|go)\s+(install|get)\b|\bgit\s+clone\b|دانلود\s*کن`)},
	{FlagSecretLike, regexp.MustCompile(`glpat-[A-Za-z0-9_\-]{8,}|gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{10,}|\bsk-[A-Za-z0-9_\-]{16,}|\bAKIA[0-9A-Z]{16}\b|-----BEGIN|Bearer\s+[A-Za-z0-9._~+/=\-]{20,}`)},
}

// Lint scans the original file text line by line and returns the flags sorted by line and code.
func Lint(content string) []Flag {
	text := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	seen := map[Flag]bool{}
	var out []Flag
	add := func(code string, line int) {
		f := Flag{Code: code, Line: line, Blocking: code == FlagSecretLike}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	for i, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		for _, r := range lintRules {
			if r.re.MatchString(line) {
				add(r.code, i+1)
			}
		}
		if len(knowledge.InjectionSuspect(line)) > 0 {
			add(FlagInjection, i+1)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// Approval outcomes.
const (
	ApprovalOK             = ""
	ApprovalBlocked        = "skill_lint_blocked"
	ApprovalUnacknowledged = "skill_flags_unacknowledged"
)

// CheckApproval decides whether a version with these flags can be approved given the acknowledged codes. It returns
// the outcome and, for unacknowledged flags, the sorted codes still missing.
func CheckApproval(flags []Flag, acknowledged []string) (string, []string) {
	ack := map[string]bool{}
	for _, a := range acknowledged {
		ack[a] = true
	}
	missing := map[string]bool{}
	for _, f := range flags {
		if f.Blocking {
			return ApprovalBlocked, nil
		}
		if !ack[f.Code] {
			missing[f.Code] = true
		}
	}
	if len(missing) == 0 {
		return ApprovalOK, nil
	}
	codes := make([]string, 0, len(missing))
	for c := range missing {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	return ApprovalUnacknowledged, codes
}
