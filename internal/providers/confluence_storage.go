package providers

import (
	"encoding/xml"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ConfluenceStorageToText converts the Confluence storage format to plain text with encoding/xml (non-strict, HTML
// entities). It keeps headings ("# "), lists ("- "), tables (cells joined by " | ") and the body of code macros
// (fenced); every other macro is dropped with its content. Links are collected from ri:page titles, ri:url values and
// anchor hrefs (sorted, unique). Text is data: nothing here interprets it.
func ConfluenceStorageToText(storage string) (string, []string) {
	dec := xml.NewDecoder(strings.NewReader("<root>" + storage + "</root>"))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity
	var out strings.Builder
	links := map[string]bool{}
	skip := 0       // depth inside a dropped macro
	inCode := false // inside a code macro body
	macroStack := []bool{}
	atLineStart := true
	pending := false
	listDepth := 0
	write := func(s string) {
		if s == "" {
			return
		}
		out.WriteString(s)
		atLineStart = strings.HasSuffix(s, "\n")
	}
	nl := func() {
		if !atLineStart {
			write("\n")
		}
	}
	attr := func(se xml.StartElement, space, local string) string {
		for _, a := range se.Attr {
			if a.Name.Local == local && (a.Name.Space == space || space == "*") {
				return a.Value
			}
		}
		return ""
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF || err != nil && tok == nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if skip > 0 {
				skip++
				continue
			}
			if t.Name.Space == "ac" && name == "structured-macro" {
				if attr(t, "ac", "name") == "code" {
					macroStack = append(macroStack, true)
					nl()
					write("```\n")
					inCode = true
				} else {
					macroStack = append(macroStack, false)
					skip = 1
				}
				continue
			}
			switch {
			case t.Name.Space == "ac" && (name == "parameter" || name == "image" || name == "task-list"):
				skip = 1
			case t.Name.Space == "ri" && name == "page":
				if title := attr(t, "ri", "content-title"); title != "" {
					links["page:"+title] = true
				}
			case t.Name.Space == "ri" && name == "url":
				if v := attr(t, "ri", "value"); v != "" {
					links[v] = true
				}
			case name == "a":
				if h := attr(t, "", "href"); h != "" {
					links[h] = true
				}
			case len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6':
				nl()
				n, _ := strconv.Atoi(name[1:])
				write(strings.Repeat("#", n) + " ")
			case name == "p" || name == "div" || name == "table" || name == "tr":
				nl()
			case name == "ul" || name == "ol":
				nl()
				listDepth++
			case name == "li":
				nl()
				write(strings.Repeat("  ", max(listDepth-1, 0)) + "- ")
			case name == "br":
				write("\n")
			case name == "td" || name == "th":
				if !atLineStart {
					write(" | ")
				}
			}
		case xml.EndElement:
			name := strings.ToLower(t.Name.Local)
			if skip > 0 {
				skip--
				if skip == 0 && t.Name.Space == "ac" && name == "structured-macro" && len(macroStack) > 0 {
					macroStack = macroStack[:len(macroStack)-1]
				}
				continue
			}
			switch {
			case t.Name.Space == "ac" && name == "structured-macro":
				if n := len(macroStack); n > 0 && macroStack[n-1] {
					macroStack = macroStack[:n-1]
					nl()
					write("```\n")
					inCode = false
				}
			case len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6', name == "p", name == "div", name == "li", name == "tr", name == "table":
				nl()
			case name == "ul" || name == "ol":
				listDepth = max(listDepth-1, 0)
				nl()
			}
		case xml.CharData:
			if skip > 0 {
				continue
			}
			s := string(t)
			if inCode {
				write(s)
				continue
			}
			fields := strings.Fields(s)
			if len(fields) == 0 {
				if s != "" && !atLineStart {
					pending = true
				}
				continue
			}
			if !atLineStart && (pending || unicode.IsSpace(rune(s[0]))) {
				write(" ")
			}
			write(strings.Join(fields, " "))
			pending = unicode.IsSpace(rune(s[len(s)-1]))
		}
	}
	ls := make([]string, 0, len(links))
	for l := range links {
		ls = append(ls, l)
	}
	sort.Strings(ls)
	return strings.TrimSpace(out.String()) + "\n", ls
}
