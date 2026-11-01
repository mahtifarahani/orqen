package skills

import (
	"embed"
	"fmt"
	"sort"
)

//go:embed builtin/*.md
var builtinFS embed.FS

// Builtin is an embedded, Orqen-authored skill. Nothing is fetched: a later Orqen ships newer texts.
type Builtin struct {
	Slug    string
	Content string
	File    File
	SHA256  string
}

var builtins = loadBuiltins()

func loadBuiltins() []Builtin {
	entries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		panic(err)
	}
	var out []Builtin
	for _, e := range entries {
		raw, err := builtinFS.ReadFile("builtin/" + e.Name())
		if err != nil {
			panic(err)
		}
		f, issues := Parse(e.Name(), string(raw))
		if HasErrors(issues) {
			panic(fmt.Sprintf("skills: built-in %s is invalid: %v", e.Name(), issues))
		}
		if e.Name() != f.Name+".md" {
			panic("skills: built-in file name differs from its name: " + e.Name())
		}
		out = append(out, Builtin{Slug: f.Name, Content: string(raw), File: f, SHA256: ContentSHA(string(raw))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// Builtins lists the embedded skills ordered by slug.
func Builtins() []Builtin { return builtins }

// RecommendedSet is the "Use recommended set" action: which built-in is bound to which role, in order.
func RecommendedSet() []Binding {
	var out []Binding
	pos := 0
	for _, b := range builtins {
		for _, r := range b.File.Roles {
			if r == "researcher" {
				continue // the recommended set binds the three roles Orqen dispatches today
			}
			out = append(out, Binding{Role: r, SkillID: b.Slug, Enabled: true, Position: pos})
			pos++
		}
	}
	return out
}
