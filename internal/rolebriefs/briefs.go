// Package rolebriefs renders the role briefs Orqen sends through catherd. They are Orqen-authored and embedded:
// nothing is downloaded and nothing is installed globally. catherd appends its own reply contract to every brief.
package rolebriefs

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"strings"
	"text/template"

	"orqen/internal/contextpkt"
)

// Version pins the brief texts; every role execution stores it with the brief's sha256.
const Version = "s7.1"

//go:embed architect.md worker.md verifier.md reviewer.md writer.md kverifier.md
var files embed.FS

// Roles with a brief.
const (
	Architect = "architect"
	Worker    = "worker"
	Verifier  = "verifier"
	Reviewer  = "reviewer"
	// Writer and KVerifier belong to the detached knowledge author (S7). No S4 stage dispatches them (see DetachedOnly).
	Writer    = "writer"
	KVerifier = "kverifier"
)

// DetachedOnly lists the roles that only the detached knowledge-author session sends; no S4 stage uses them.
var DetachedOnly = map[string]bool{Writer: true, KVerifier: true}

// Command is an approved verification command.
type Command struct{ Key, Display string }

// Lane is the lane a worker implements.
type Lane struct {
	ID    string
	Owns  []string
	Body  string
	Title string
}

// Failure is one failing command of the previous round.
type Failure struct{ Key, Outcome, Tail string }

// Finding is a review finding handed back to the worker.
type Finding struct {
	Severity, File, Title, Body string
	Line                        int
}

// Evidence is one row of Orqen's verification evidence.
type Evidence struct {
	Key, Outcome, OutputSHA12 string
	Exit                      int
}

// Data is everything a brief can use. Free text is scrubbed and wrapped as data when rendered.
type Data struct {
	Key, Summary  string
	Acceptance    []string
	Commands      []Command
	DenyPaths     []string
	Context       string
	Lane          Lane
	Round         int
	Failures      []Failure
	Weakening     []string
	Findings      []Finding
	BaseSHA       string
	HeadSHA       string
	Files         []string
	Evidence      []Evidence
	CriticalRules []string
	// Skills is the pre-rendered block of approved skills (skills.Render); empty = no skills section.
	Skills string
}

// Brief is a rendered brief.
type Brief struct {
	Role    string
	Text    string
	SHA256  string
	Version string
}

type view struct {
	Data
	Version string
}

func neutralize(s string) string {
	s = strings.ReplaceAll(s, "<<<", "‹‹‹")
	return strings.ReplaceAll(s, ">>>", "›››")
}

var funcs = template.FuncMap{
	"join": strings.Join,
	// data wraps untrusted text as a marked data block after scrubbing secrets.
	"data": func(id, text string) string {
		return fmt.Sprintf("<<<DATA %s>>>\n%s\n<<<END %s>>>", id, neutralize(contextpkt.Scrub(text)), id)
	},
}

var templates = map[string]*template.Template{}

func init() {
	for _, role := range []string{Architect, Worker, Verifier, Reviewer, Writer, KVerifier} {
		b, err := files.ReadFile(role + ".md")
		if err != nil {
			panic(err)
		}
		templates[role] = template.Must(template.New(role).Funcs(funcs).Parse(string(b)))
	}
}

// Render builds the brief of a role. It is deterministic: the same data gives the same text and hash.
func Render(role string, d Data) (Brief, error) {
	t, ok := templates[role]
	if !ok {
		return Brief{}, fmt.Errorf("rolebriefs: no brief for role %q", role)
	}
	clean := d
	scrubList := func(in []string) []string {
		out := make([]string, len(in))
		for i, s := range in {
			out[i] = neutralize(contextpkt.Scrub(s))
		}
		return out
	}
	clean.Acceptance = scrubList(d.Acceptance)
	clean.Weakening = scrubList(d.Weakening)
	clean.CriticalRules = scrubList(d.CriticalRules)
	clean.Key = neutralize(contextpkt.Scrub(d.Key))
	clean.Summary = neutralize(contextpkt.Scrub(d.Summary))
	var buf bytes.Buffer
	if err := t.Execute(&buf, view{Data: clean, Version: Version}); err != nil {
		return Brief{}, err
	}
	text := strings.TrimRight(buf.String(), "\n") + "\n"
	sum := sha256.Sum256([]byte(text))
	return Brief{Role: role, Text: text, SHA256: hex.EncodeToString(sum[:]), Version: Version}, nil
}
