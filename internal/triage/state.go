package triage

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"orqen/internal/contextpkt"
)

// StateLimit is the character budget of the whole state sent to Jev (research: long state rots its answers).
const StateLimit = 6000

type LinkInfo struct{ Type, Summary string }
type ParentInfo struct{ Summary, Description string }

// StateInput is the task evidence for Jev. It never includes code.
type StateInput struct {
	Title, IssueType, Acceptance, Description string
	Parent                                    *ParentInfo
	Links                                     []LinkInfo
	Comments                                  []string // oldest first
	Answers                                   []string
	PackagePath                               string
	HasTestCommand                            bool
}

var fenceOpen = regexp.MustCompile("^[ \\t]*(`{3,}|~{3,})")

// dropFences replaces fenced code with a marker; a fence left open runs to the end of the text.
func dropFences(s string) string {
	var out []string
	marker := ""
	for _, line := range strings.Split(s, "\n") {
		m := fenceOpen.FindStringSubmatch(line)
		switch {
		case marker == "" && m != nil:
			marker = m[1]
			out = append(out, "[code omitted]")
		case marker != "":
			if m != nil && m[1][0] == marker[0] && len(m[1]) >= len(marker) {
				marker = ""
			}
		default:
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// clean scrubs secrets, drops fenced code and caps the text at max characters.
func clean(s string, max int) string {
	s = dropFences(s)
	s = contextpkt.Scrub(strings.TrimSpace(s))
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

// JevState builds the scrubbed, bounded state object.
func JevState(in StateInput) map[string]any {
	build := func(descMax int) map[string]any {
		st := map[string]any{"title": clean(in.Title, 300), "issue_type": clean(in.IssueType, 60), "acceptance": clean(in.Acceptance, 3000), "description": clean(in.Description, descMax),
			"repo": map[string]any{"package_path": clean(in.PackagePath, 200), "has_test_command": in.HasTestCommand}}
		if p := in.Parent; p != nil {
			st["parent"] = map[string]any{"summary": clean(p.Summary, 300), "description": clean(p.Description, 1000)}
		}
		links := []map[string]string{}
		for i, l := range in.Links {
			if i == 10 {
				break
			}
			links = append(links, map[string]string{"type": clean(l.Type, 60), "summary": clean(l.Summary, 200)})
		}
		st["links"] = links
		comments := []string{}
		from := max(len(in.Comments)-5, 0)
		for _, c := range in.Comments[from:] {
			comments = append(comments, clean(c, 500))
		}
		st["comments"] = comments
		answers := []string{}
		budget := 1000
		for _, a := range in.Answers {
			c := clean(a, budget)
			answers = append(answers, c)
			budget -= utf8.RuneCountInString(c)
			if budget <= 0 {
				break
			}
		}
		st["answers"] = answers
		return st
	}
	descMax := 3000
	for {
		st := build(descMax)
		b, _ := json.Marshal(st)
		if utf8.RuneCount(b) <= StateLimit || descMax <= 100 {
			return st
		}
		descMax /= 2
	}
}
