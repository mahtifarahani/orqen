package knowledge

import (
	"regexp"
	"sort"
	"strings"
)

// Injection codes. They only flag text for review; no code path reads knowledge into policy or commands.
const (
	InjIgnoreInstructions = "ignore_instructions"
	InjSystemPrompt       = "system_prompt"
	InjRoleOverride       = "role_override"
	InjToolOrCredential   = "tool_or_credential"
	InjPolicyImperative   = "policy_imperative"
	InjSourceForgery      = "source_frame_forgery"
)

var injectionRules = []struct {
	code string
	re   *regexp.Regexp
}{
	{InjIgnoreInstructions, regexp.MustCompile(`(?i)(ignore|disregard|forget)\s+(all\s+)?(the\s+)?(previous|prior|above|earlier)\s+(instructions?|rules?|prompts?|messages?)|(دستور(ات|العمل(‌های)?)?|قوانین)\s*(قبلی|بالا)\s*(را)?\s*(نادیده|فراموش)`)},
	{InjSystemPrompt, regexp.MustCompile(`(?i)system\s*prompt|developer\s*message|پرامپت\s*سیستم|دستور\s*سیستم|پیام\s*سیستم`)},
	{InjRoleOverride, regexp.MustCompile(`(?i)you\s+are\s+now\b|act\s+as\s+(an?\s+)?(admin|root|system)|از\s+این\s+پس\s+تو|تو\s+اکنون`)},
	{InjToolOrCredential, regexp.MustCompile(`(?i)\b(api[_ -]?key|access[_ -]?token|private[_ -]?key|password|credentials?|secret)\b.{0,40}\b(send|print|reveal|show|exfiltrate|output)\b|\b(send|print|reveal|show|exfiltrate|output)\b.{0,40}\b(api[_ -]?key|access[_ -]?token|private[_ -]?key|password|credentials?|secret)\b|(توکن|رمز\s*عبور|کلید)\s.{0,30}(نمایش|ارسال|چاپ)`)},
	{InjPolicyImperative, regexp.MustCompile(`(?i)\b(approve|auto-?merge|disable|bypass|skip)\b.{0,30}\b(policy|review|verification|checks?|gate|approval)\b|\b(set|change|update)\b.{0,20}\bpolicy\b|سیاست.{0,20}(تغییر|غیرفعال)|تایید\s*کن`)},
	{InjSourceForgery, regexp.MustCompile(`<<<\s*/?SOURCE|<<<\s*END`)},
}

// InjectionSuspect returns the sorted, de-duplicated codes of the injection patterns found in text (en and fa).
func InjectionSuspect(text string) []string {
	t := strings.TrimSpace(text)
	if t == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, r := range injectionRules {
		if r.re.MatchString(t) {
			seen[r.code] = true
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// FlagClaims sets the injection_suspect flag on claims whose text (or excerpts) match, and returns the version-level codes.
func FlagClaims(cs []Claim, title, body string) (claims []Claim, codes []string) {
	set := map[string]bool{}
	for _, c := range InjectionSuspect(title + "\n" + body) {
		set[c] = true
	}
	claims = make([]Claim, len(cs))
	for i, c := range cs {
		c.Flags = removeStr(c.Flags, "injection_suspect")
		cc := InjectionSuspect(c.Text)
		for _, e := range c.Evidence {
			cc = append(cc, InjectionSuspect(e.Excerpt)...)
		}
		if len(cc) > 0 {
			c.Flags = append(c.Flags, "injection_suspect")
			for _, x := range cc {
				set[x] = true
			}
		}
		claims[i] = c
	}
	for c := range set {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	return claims, codes
}

func removeStr(xs []string, x string) []string {
	var out []string
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}
