package kindex

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// FakeScenario is the failure scenario of the fake CLI, read from the "wiki" object of <stateDir>/../scenario.json
// (written by PUT /_fakes/host/scenario on orqen-fakes).
type FakeScenario struct {
	Down        bool   `json:"down"`
	FailVerb    string `json:"fail_verb"`
	LintErrors  int    `json:"lint_errors"`
	SlowMs      int    `json:"slow_ms"`
	WrongSchema bool   `json:"wrong_schema"`
}

// LoadFakeScenario reads the scenario file; a missing file means no failures.
func LoadFakeScenario(path string) FakeScenario {
	var s struct {
		Wiki FakeScenario `json:"wiki"`
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s.Wiki
}

func flagVal(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// RunFakeCLI implements exactly the assumed contract v1 (see schema/v1) with the deterministic engine. args are the
// arguments after the binary name: `wiki <verb> --vault <dir> --json ...`. State lives in <vault>/.fake-index.json.
// It returns the process exit code.
func RunFakeCLI(args []string, stdin io.Reader, stdout io.Writer, stateDir string) int {
	_ = stdin
	fail := func(msg string, code int) int { fmt.Fprintln(os.Stderr, "openclaw-fake:", msg); return code }
	if len(args) < 2 || args[0] != "wiki" {
		return fail("usage: wiki <verb> --vault <dir> --json", 2)
	}
	verb, rest := args[1], args[2:]
	if verb == "synthesis" {
		if len(rest) == 0 || rest[0] != "register" {
			return fail("usage: wiki synthesis register", 2)
		}
		rest = rest[1:]
	}
	vault, ok := flagVal(rest, "--vault")
	if !ok || !filepath.IsAbs(vault) {
		return fail("--vault <abs dir> is required", 2)
	}
	sc := LoadFakeScenario(filepath.Join(stateDir, "..", "scenario.json"))
	if sc.SlowMs > 0 {
		time.Sleep(time.Duration(sc.SlowMs) * time.Millisecond)
	}
	if sc.Down || sc.FailVerb == verb {
		return fail("scenario: "+verb+" fails", 3)
	}
	if err := os.MkdirAll(vault, 0o700); err != nil {
		return fail(err.Error(), 1)
	}
	statePath := filepath.Join(vault, ".fake-index.json")
	e := newEngine()
	if b, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(b, e)
		if e.Docs == nil {
			e.Docs = map[string]fakeDoc{}
		}
		if e.Synth == nil {
			e.Synth = map[string]string{}
		}
	}
	var out any
	switch verb {
	case "version":
		out = Health{Version: "fake-1.0", Schema: "v1"}
	case "ingest":
		in, _ := flagVal(rest, "--input")
		res, err := e.ingest(in)
		if err != nil {
			return fail(err.Error(), 1)
		}
		out = res
	case "synthesis":
		in, _ := flagVal(rest, "--input")
		if err := e.synthesis(in); err != nil {
			return fail(err.Error(), 1)
		}
		out = map[string]bool{"registered": true}
	case "compile":
		out = e.compile()
	case "lint":
		r := LintResult{Errors: []Issue{}, Warnings: []Issue{}}
		for i := 0; i < sc.LintErrors; i++ {
			r.Errors = append(r.Errors, Issue{Path: "knowledge/x.md", Code: "lint_error", Message: "synthetic lint error"})
		}
		out = r
	case "search":
		q, _ := flagVal(rest, "--query")
		n, _ := flagVal(rest, "--limit")
		limit, _ := strconv.Atoi(n)
		out = map[string]any{"hits": e.search(q, limit)}
	case "get":
		id, _ := flagVal(rest, "--id")
		n, _ := flagVal(rest, "--max-bytes")
		maxB, _ := strconv.Atoi(n)
		s, found := e.get(id, maxB)
		if !found {
			return fail("no such document", 4)
		}
		out = s
	default:
		return fail("unknown verb "+verb, 2)
	}
	if b, err := json.Marshal(e); err == nil {
		_ = os.WriteFile(statePath, b, 0o600)
	}
	if sc.WrongSchema {
		out = map[string]any{"unexpected": true}
	}
	enc := json.NewEncoder(stdout)
	if err := enc.Encode(out); err != nil {
		return 1
	}
	return 0
}
