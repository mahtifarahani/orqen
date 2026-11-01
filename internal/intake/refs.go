package intake

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Refs are links found in issue text. Their content is never fetched in Sprint 3.
type Refs struct {
	Confluence []string `json:"confluence"`
	Figma      []string `json:"figma"`
	URLs       []string `json:"urls"`
}

var urlRe = regexp.MustCompile(`https?://[^\s<>"'\x60)\]|}]+`)

// ExtractRefs classifies every distinct URL in the given texts.
func ExtractRefs(texts ...string) Refs {
	seen := map[string]bool{}
	r := Refs{Confluence: []string{}, Figma: []string{}, URLs: []string{}}
	for _, t := range texts {
		for _, raw := range urlRe.FindAllString(t, -1) {
			raw = strings.TrimRight(raw, ".,;:!?")
			u, err := url.Parse(raw)
			if err != nil || u.Host == "" || seen[raw] {
				continue
			}
			seen[raw] = true
			host := strings.ToLower(u.Hostname())
			switch {
			case host == "figma.com" || strings.HasSuffix(host, ".figma.com"):
				r.Figma = append(r.Figma, raw)
			case strings.Contains(host, "confluence") || strings.HasPrefix(u.Path, "/wiki/") || strings.HasPrefix(u.Path, "/display/") || strings.Contains(u.Path, "/pages/viewpage.action"):
				r.Confluence = append(r.Confluence, raw)
			default:
				r.URLs = append(r.URLs, raw)
			}
		}
	}
	sort.Strings(r.Confluence)
	sort.Strings(r.Figma)
	sort.Strings(r.URLs)
	return r
}
