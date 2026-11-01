package intake

import (
	"regexp"
	"strings"
)

const (
	SourceField   = "field"
	SourceSection = "section"
	SourceNone    = "none"
)

// Acceptance is the acceptance criteria found for an issue and where they came from.
type Acceptance struct {
	Text   string   `json:"text"`
	Source string   `json:"source"`
	Items  []string `json:"items"`
}

const titlePattern = `(?:acceptance\s+criteria|acceptance|ac|definition\s+of\s+done|dod|معیار(?:های)?\s+پذیرش)`

var (
	wikiHeading = regexp.MustCompile(`(?i)^h([1-6])\.\s*(.*?)\s*$`)
	mdHeading   = regexp.MustCompile(`^(#{1,6})\s+(\S.*?)\s*#*\s*$`)
	boldHeading = regexp.MustCompile(`^\s*(?:\*\*?|__)([^*_]+?)(?:\*\*?|__)\s*:?\s*$`)
	titleRe     = regexp.MustCompile(`(?i)^` + titlePattern + `\s*:?\s*$`)
	itemRe      = regexp.MustCompile(`^\s*(?:[*\-#]+|\d+[.)])\s+(.*\S)\s*$`)
	checkboxRe  = regexp.MustCompile(`^\[[ xX]\]\s*`)
)

type headingKind int

const (
	notHeading headingKind = iota
	wikiKind
	mdKind
	boldKind
)

func classifyHeading(line string) (kind headingKind, level int, title string) {
	if m := wikiHeading.FindStringSubmatch(line); m != nil {
		return wikiKind, int(m[1][0] - '0'), strings.Trim(m[2], "*_ ")
	}
	if m := mdHeading.FindStringSubmatch(line); m != nil {
		return mdKind, len(m[1]), strings.Trim(m[2], "*_ ")
	}
	if m := boldHeading.FindStringSubmatch(line); m != nil {
		return boldKind, 0, strings.TrimSpace(m[1])
	}
	return notHeading, 0, ""
}

func itemsOf(lines []string) []string {
	items := []string{}
	for _, l := range lines {
		if m := itemRe.FindStringSubmatch(l); m != nil {
			t := strings.TrimSpace(checkboxRe.ReplaceAllString(m[1], ""))
			if t != "" {
				items = append(items, t)
			}
		}
	}
	return items
}

// ExtractAcceptance prefers the mapped Jira field. Otherwise it looks for a section headed (case-insensitive)
// acceptance criteria, acceptance, AC, definition of done or its Persian equivalent, as a wiki hN. heading, a
// markdown # heading or a bold line, and reads until the next heading. Items are lines starting with *, -, # or
// "1." (a field without any such marker contributes one item per non-empty line).
func ExtractAcceptance(desc string, field *string) Acceptance {
	if field != nil {
		if text := Normalize(*field); text != "" {
			lines := strings.Split(text, "\n")
			items := itemsOf(lines)
			if len(items) == 0 {
				for _, l := range lines {
					if l = strings.TrimSpace(l); l != "" {
						items = append(items, l)
					}
				}
			}
			return Acceptance{Text: text, Source: SourceField, Items: items}
		}
	}
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(desc, "\r\n", "\n"), "\r", "\n"), "\n")
	for i, line := range lines {
		kind, level, title := classifyHeading(strings.TrimSpace(line))
		if kind == notHeading || !titleRe.MatchString(title) {
			continue
		}
		var body []string
		for _, next := range lines[i+1:] {
			nk, nl, _ := classifyHeading(strings.TrimSpace(next))
			if nk != notHeading && sectionEnds(kind, level, nk, nl) {
				break
			}
			body = append(body, next)
		}
		text := Normalize(strings.Join(body, "\n"))
		if text == "" {
			continue
		}
		return Acceptance{Text: text, Source: SourceSection, Items: itemsOf(strings.Split(text, "\n"))}
	}
	return Acceptance{Source: SourceNone, Items: []string{}}
}

// sectionEnds reports whether a heading of kind nk/nl ends a section opened by kind/level. Inside a wiki or bold
// section "# item" is a numbered list item, so markdown headings only end markdown sections.
func sectionEnds(kind headingKind, level int, nk headingKind, nl int) bool {
	switch kind {
	case mdKind:
		return nk == mdKind && nl <= level || nk == wikiKind && nl <= level || nk == boldKind
	case wikiKind:
		return nk == wikiKind && nl <= level || nk == boldKind
	default:
		return nk == wikiKind || nk == boldKind
	}
}
