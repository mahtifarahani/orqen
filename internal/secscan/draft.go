package secscan

import (
	"fmt"
	"strings"
)

// IssueDraft renders a finding as markdown the user can file themselves ("Copy as issue draft"). Nothing is sent
// anywhere. The text is redacted: known secrets and patterns, the scan tree path (shown as <repo>) and the home
// directory (shown as ~).
func IssueDraft(f Finding, scanTree, home string, known []string) string {
	clean := func(s string) string {
		s = Redact(s, known)
		if scanTree != "" {
			s = strings.ReplaceAll(s, scanTree, "<repo>")
		}
		if home != "" && home != "/" {
			s = strings.ReplaceAll(s, home, "~")
		}
		return s
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", clean(f.Title))
	fmt.Fprintf(&b, "- Severity: %s (scanner: %s)\n", Map(f.Severity), f.Severity)
	if f.CWE != "" {
		fmt.Fprintf(&b, "- CWE: %s\n", clean(f.CWE))
	}
	if f.File != "" {
		loc := clean(f.File)
		if f.Line > 0 {
			loc += fmt.Sprintf(":%d", f.Line)
		}
		fmt.Fprintf(&b, "- Location: `%s`\n", loc)
	}
	if f.Description != "" {
		fmt.Fprintf(&b, "\n### Description\n\n%s\n", clean(f.Description))
	}
	if f.Proof != "" {
		fmt.Fprintf(&b, "\n### Proof\n\n```\n%s\n```\n", strings.ReplaceAll(clean(f.Proof), "```", "'''"))
	}
	b.WriteString("\n_Found by an automated scan of a local copy. Review before filing; nothing was sent anywhere._\n")
	return b.String()
}
