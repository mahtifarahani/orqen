// Package skills is the pure core of role-scoped skills (Sprint 6): the skill file format, lint, hash, the built-in
// texts, resolution with a byte budget, and the fixed frame in which approved skills reach a brief. A skill is guidance
// the Orqen user approved. It never changes the plan, tools, access, credentials or policy, and nothing in this
// package imports the server, policy, extwrite or catherd.
package skills

import (
	"regexp"
	"sort"
	"strings"

	"orqen/internal/knowledge"
	"orqen/internal/semver"
)

// Roles a skill can be written for.
var Roles = []string{"architect", "worker", "verifier", "reviewer", "researcher", "writer"}

// When values.
const (
	WhenAlways   = "always"
	WhenFixRound = "fix_round"
)

// Size limits.
const (
	MaxFileBytes = 16 << 10
	MaxBodyBytes = 8 << 10
	MaxDescLen   = 300
)

// Frontmatter is the parsed header of a skill file.
type Frontmatter struct {
	Name        string   `json:"name" bson:"name"`
	Description string   `json:"description" bson:"description"`
	Version     string   `json:"version" bson:"version"`
	Roles       []string `json:"roles" bson:"roles"`
	When        string   `json:"when" bson:"when"`
	License     string   `json:"license" bson:"license"`
	Source      string   `json:"source" bson:"source"`
}

// File is a parsed skill file.
type File struct {
	Frontmatter
	Body       string
	BodyLine   int    // 1-based line of the first body line in the original file
	Normalized string // the S11-normalized file text that ContentSHA hashes
}

// Issue is one parse problem. Errors make the file invalid (skill_invalid with a line); warnings are informative.
type Issue struct {
	Code    string `json:"code"`
	Line    int    `json:"line"`
	Warning bool   `json:"warning,omitempty"`
}

// Parse error codes.
const (
	IssueTooLarge       = "skill_too_large"
	IssueNoFrontmatter  = "no_frontmatter"
	IssueUnterminated   = "frontmatter_unterminated"
	IssueBadLine        = "bad_frontmatter_line"
	IssueDuplicateKey   = "duplicate_key"
	IssueQuoteMultiline = "quote_spans_lines"
	IssueBadName        = "bad_name"
	IssueBadDescription = "bad_description"
	IssueBadVersion     = "bad_version"
	IssueBadRole        = "bad_roles"
	IssueBadWhen        = "bad_when"
	IssueEmptyBody      = "empty_body"
	IssueUnknownKey     = "unknown_key" // warning
)

var (
	slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)
	keyRe  = regexp.MustCompile(`^[a-z_]+$`)
)

func unquote(v string) (string, bool) {
	if v == "" {
		return v, true
	}
	q := v[0]
	if q != '"' && q != '\'' {
		return v, true
	}
	if len(v) < 2 || v[len(v)-1] != q {
		return v, false
	}
	return v[1 : len(v)-1], true
}

// Parse reads a skill file. The frontmatter sits between two `---` lines and holds only `key: value` lines: lists,
// nesting and quotes that span lines are errors. Unknown keys are warnings and ignored.
func Parse(filename, content string) (File, []Issue) {
	var issues []Issue
	if len(content) > MaxFileBytes {
		return File{}, []Issue{{Code: IssueTooLarge, Line: 0}}
	}
	text := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(text, "\n")
	f := File{Normalized: knowledge.NormText(content)}
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return f, []Issue{{Code: IssueNoFrontmatter, Line: 1}}
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return f, []Issue{{Code: IssueUnterminated, Line: 1}}
	}
	seen := map[string]bool{}
	for i := 1; i < end; i++ {
		raw := lines[i]
		ln := i + 1
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(strings.TrimSpace(raw), "#") {
			continue
		}
		if raw != strings.TrimLeft(raw, " \t") || strings.HasPrefix(raw, "- ") {
			issues = append(issues, Issue{Code: IssueBadLine, Line: ln})
			continue
		}
		k, v, ok := strings.Cut(raw, ":")
		k = strings.TrimSpace(k)
		if !ok || !keyRe.MatchString(k) {
			issues = append(issues, Issue{Code: IssueBadLine, Line: ln})
			continue
		}
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "[") || strings.HasPrefix(v, "{") || strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">") {
			issues = append(issues, Issue{Code: IssueBadLine, Line: ln})
			continue
		}
		val, ok := unquote(v)
		if !ok {
			issues = append(issues, Issue{Code: IssueQuoteMultiline, Line: ln})
			continue
		}
		if seen[k] {
			issues = append(issues, Issue{Code: IssueDuplicateKey, Line: ln})
			continue
		}
		seen[k] = true
		switch k {
		case "name":
			f.Name = val
			if !slugRe.MatchString(val) {
				issues = append(issues, Issue{Code: IssueBadName, Line: ln})
			}
		case "description":
			f.Description = val
			if val == "" || len([]rune(val)) > MaxDescLen {
				issues = append(issues, Issue{Code: IssueBadDescription, Line: ln})
			}
		case "version":
			f.Version = val
			if !strictSemver(val) {
				issues = append(issues, Issue{Code: IssueBadVersion, Line: ln})
			}
		case "roles":
			for _, r := range strings.Split(val, ",") {
				r = strings.TrimSpace(r)
				if !validRole(r) {
					issues = append(issues, Issue{Code: IssueBadRole, Line: ln})
					break
				}
				f.Roles = append(f.Roles, r)
			}
			sort.Strings(f.Roles)
			f.Roles = dedupe(f.Roles)
		case "when":
			f.When = val
			if val != WhenAlways && val != WhenFixRound {
				issues = append(issues, Issue{Code: IssueBadWhen, Line: ln})
			}
		case "license":
			f.License = val
		case "source":
			f.Source = val
		default:
			issues = append(issues, Issue{Code: IssueUnknownKey, Line: ln, Warning: true})
		}
	}
	for _, req := range []struct {
		key, code string
		missing   bool
	}{
		{"name", IssueBadName, f.Name == ""},
		{"description", IssueBadDescription, f.Description == ""},
		{"version", IssueBadVersion, f.Version == ""},
		{"roles", IssueBadRole, len(f.Roles) == 0},
	} {
		if req.missing && !seen[req.key] {
			issues = append(issues, Issue{Code: req.code, Line: 1})
		}
	}
	if f.When == "" {
		f.When = WhenAlways
	}
	if f.License == "" {
		f.License = "unknown"
	}
	f.BodyLine = end + 2
	f.Body = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	switch {
	case f.Body == "":
		issues = append(issues, Issue{Code: IssueEmptyBody, Line: f.BodyLine})
	case len(f.Body) > MaxBodyBytes:
		issues = append(issues, Issue{Code: IssueTooLarge, Line: f.BodyLine})
	}
	_ = filename
	return f, issues
}

// HasErrors reports whether any issue is not a warning.
func HasErrors(issues []Issue) bool {
	for _, i := range issues {
		if !i.Warning {
			return true
		}
	}
	return false
}

var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func strictSemver(v string) bool {
	if !semverRe.MatchString(v) {
		return false
	}
	_, ok := semver.Find(v)
	return ok
}

func validRole(r string) bool {
	for _, x := range Roles {
		if x == r {
			return true
		}
	}
	return false
}

func dedupe(in []string) []string {
	out := in[:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}
