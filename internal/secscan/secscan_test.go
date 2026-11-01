package secscan

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orqen/internal/extensions"
)

// TestMain doubles as the fake Strix: the wrapper script runs this test binary with __fake_strix__ <scenario> <args>.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "__fake_strix__" {
		os.Exit(RunFakeCLI(os.Args[3:], os.Stdout, os.Args[2]))
	}
	os.Exit(m.Run())
}

const secretKey = "test-not-a-secret-Zk39QwX7"

type env struct {
	org, tree, reports, scn string
	cli                     *CLI
}

func newEnv(t *testing.T) env {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	org := filepath.Join(root, "scans", "org_1")
	_ = os.MkdirAll(org, 0o700)
	scn := filepath.Join(root, "scenario.json")
	exe, _ := os.Executable()
	wrapper := filepath.Join(root, "strix")
	if err := os.WriteFile(wrapper, []byte(fmt.Sprintf("#!/bin/sh\nexec %q __fake_strix__ %q \"$@\"\n", exe, scn)), 0o755); err != nil {
		t.Fatal(err)
	}
	m, _ := extensions.Get("strix")
	p, reason := extensions.Usable(m, extensions.Snapshot{Status: extensions.StatusEnabled, Path: wrapper, Version: "fake", SHA256: "x", Credentials: map[string]bool{"llm_api_key": true}})
	if reason != "" {
		t.Fatal(reason)
	}
	cli := NewCLI(p, org)
	cli.Mode = extensions.SandboxBestEffort
	tree := filepath.Join(org, "scn_1", "target")
	_ = os.MkdirAll(filepath.Join(tree, "src"), 0o700)
	_ = os.WriteFile(filepath.Join(tree, "src", "app.py"), []byte("def lookup(i):\n    pass\n    q = 'select ' + i\n"), 0o600)
	_ = os.WriteFile(filepath.Join(tree, "README.md"), []byte("readme\n"), 0o600)
	return env{org: org, tree: tree, reports: filepath.Join(org, "scn_1", "report"), scn: scn, cli: cli}
}

func (e env) scan(t *testing.T, scenario string) (RawResult, error) {
	t.Helper()
	_ = os.WriteFile(e.scn, []byte(fmt.Sprintf(`{"strix":{"scenario":%q}}`, scenario)), 0o600)
	_ = os.RemoveAll(e.reports)
	return e.cli.Scan(context.Background(), Request{Tree: e.tree, ReportDir: e.reports, MaxTurns: 5, BudgetUSD: 1, Model: "fake/model", LLMKey: secretKey})
}

func classify(t *testing.T, e env, scenario string) (Completion, *Report, RawResult) {
	t.Helper()
	raw, err := e.scan(t, scenario)
	if err != nil {
		t.Fatal(scenario, err)
	}
	var rep *Report
	var perr error
	if raw.Report != nil {
		r, err := Parse(raw.Report, []string{secretKey})
		if err != nil {
			perr = err
		} else {
			rep = &r
		}
	}
	return Classify(raw.Exit, raw.TimedOut, rep, perr, e.tree), rep, raw
}

func TestClassifyMatrix(t *testing.T) {
	e := newEnv(t)
	for scenario, want := range map[string]Completion{
		"": Complete, "findings": Complete, "budget_stop_exit0": StoppedBudget, "turns_stop": StoppedTurns,
		"crash": Failed, "no_report": Incomplete, "wrong_schema": InvalidOutput, "wrong_target": Incomplete,
	} {
		if got, _, _ := classify(t, e, scenario); got != want {
			t.Errorf("scenario %q: %s, want %s", scenario, got, want)
		}
	}
	rep := &Report{Status: StatusCompleted}
	rep.Scope.Target = "/t"
	cases := []struct {
		exit     int
		timedOut bool
		status   string
		target   string
		nilRep   bool
		perr     error
		want     Completion
	}{
		{0, false, StatusCompleted, "/t", false, nil, Complete},
		{0, false, StatusBudgetExceeded, "/t", false, nil, StoppedBudget},
		{1, false, StatusBudgetExceeded, "/t", false, nil, StoppedBudget},
		{0, false, StatusMaxTurns, "/t", false, nil, StoppedTurns},
		{0, false, StatusCompleted, "/other", false, nil, Incomplete},
		{3, false, StatusCompleted, "/t", false, nil, Failed},
		{0, false, StatusError, "/t", false, nil, Failed},
		{0, true, StatusCompleted, "/t", false, nil, Timeout},
		{-1, true, "", "", true, nil, Timeout},
		{0, false, "", "", true, nil, Incomplete},
		{2, false, "", "", true, nil, Failed},
		{0, false, "", "", true, errors.New("bad"), InvalidOutput},
		{0, false, "weird", "/t", false, nil, InvalidOutput},
	}
	for i, c := range cases {
		r := *rep
		r.Status, r.Scope.Target = c.status, c.target
		var rp *Report = &r
		if c.nilRep {
			rp = nil
		}
		if got := Classify(c.exit, c.timedOut, rp, c.perr, "/t"); got != c.want {
			t.Errorf("case %d: %s, want %s", i, got, c.want)
		}
	}
	for _, c := range Completions {
		if c.Passes() != (c == Complete) {
			t.Errorf("%s: only complete passes a gate", c)
		}
	}
	if len(Completions) != 7 {
		t.Fatal("seven completions")
	}
}

func TestSeverityMapAndThreshold(t *testing.T) {
	for raw, want := range map[string]string{"critical": SevBlocker, "high": SevMajor, "medium": SevMinor, "low": SevNit, "info": SevNit, "weird": SevMajor} {
		if Map(raw) != want {
			t.Errorf("%s -> %s", raw, Map(raw))
		}
	}
	type c struct {
		raw, thr string
		want     bool
	}
	for _, x := range []c{{"critical", "high", true}, {"high", "high", true}, {"medium", "high", false}, {"medium", "medium", true}, {"low", "medium", false},
		{"low", "low", true}, {"info", "low", false}, {"info", "critical", false}, {"critical", "critical", true}, {"high", "critical", false}, {"weird", "high", true}} {
		if Blocking(x.raw, x.thr) != x.want {
			t.Errorf("Blocking(%s,%s) != %v", x.raw, x.thr, x.want)
		}
	}
}

func TestKeyNeverAppearsInStoredOutput(t *testing.T) {
	e := newEnv(t)
	got, rep, raw := classify(t, e, "leak_key")
	if got != Complete || rep == nil {
		t.Fatalf("leak_key scan: %s", got)
	}
	if strings.Contains(raw.Stdout, secretKey) || strings.Contains(raw.StderrTail, secretKey) {
		t.Fatalf("stdout leaks the key: %q", raw.Stdout)
	}
	if !strings.Contains(raw.Stdout, "[secret]") {
		t.Fatal("the key must be replaced by a mark")
	}
	for _, f := range rep.Findings {
		if strings.Contains(f.Title+f.Description+f.Proof, secretKey) {
			t.Fatalf("finding leaks the key: %+v", f)
		}
	}
	// the raw report on disk still holds what the tool wrote; the stored form is the parsed (redacted) one
	if !strings.Contains(string(raw.Report), secretKey) {
		t.Fatal("the fake should echo the key into its report")
	}
	// the in-process fake behaves the same
	f := &Fake{Scenario: FakeScenario{Scenario: "leak_key"}}
	res, _ := f.Scan(context.Background(), Request{Tree: e.tree, ReportDir: filepath.Join(e.org, "r2"), MaxTurns: 1, LLMKey: secretKey})
	if strings.Contains(res.Stdout, secretKey) {
		t.Fatal("fake stdout leaks the key")
	}
}

func TestEnvIsAllowlistedAndPanicsOtherwise(t *testing.T) {
	e := newEnv(t)
	req := Request{Model: "m", LLMKey: secretKey, APIBase: "https://llm.example.invalid/v1", DockerHost: "unix:///x.sock"}
	seen := map[string]string{}
	for _, kv := range e.cli.scanEnv(req) {
		k, v, _ := strings.Cut(kv, "=")
		seen[k] = v
	}
	for _, k := range []string{"HOME", "TMPDIR", "PATH", "LANG", "NO_COLOR", "STRIX_LLM", "LLM_API_KEY", "LLM_API_BASE", "DOCKER_HOST"} {
		if _, ok := seen[k]; !ok {
			t.Errorf("missing %s", k)
		}
		delete(seen, k)
	}
	if len(seen) != 0 {
		t.Errorf("unexpected variables: %v", seen)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a name outside the allowlist must panic")
		}
	}()
	extensions.BuildEnv(nil, AllowedEnv, map[string]string{"OPENAI_API_KEY": "x"})
}

func TestArgvAndRequestRefusals(t *testing.T) {
	e := newEnv(t)
	argv := e.cli.argv(Request{Tree: e.tree, ReportDir: e.reports, MaxTurns: 7, BudgetUSD: 2.5})
	if strings.Join(argv, " ") != fmt.Sprintf("--target %s --non-interactive --output-dir %s --max-turns 7 --budget-usd 2.5", e.tree, e.reports) {
		t.Fatalf("argv: %v", argv)
	}
	if got := e.cli.argv(Request{Tree: e.tree, ReportDir: e.reports, MaxTurns: 7}); strings.Contains(strings.Join(got, " "), "budget") {
		t.Fatal("unlimited sends no budget flag")
	}
	for _, a := range argv {
		if strings.Contains(a, "://") {
			t.Fatalf("argv contains a URL: %s", a)
		}
	}
	outside := t.TempDir()
	bad := []Request{
		{Tree: "https://github.com/x/y", ReportDir: e.reports, MaxTurns: 5},
		{Tree: "relative/dir", ReportDir: e.reports, MaxTurns: 5},
		{Tree: outside, ReportDir: e.reports, MaxTurns: 5},
		{Tree: e.tree, ReportDir: filepath.Join(outside, "r"), MaxTurns: 5},
		{Tree: e.tree, ReportDir: e.reports, MaxTurns: 0},
		{Tree: e.tree, ReportDir: e.reports, MaxTurns: 5, BudgetUSD: -1},
		{Tree: e.tree, ReportDir: e.reports, MaxTurns: 5, APIBase: "file:///etc/passwd"},
		{Tree: e.tree, ReportDir: e.reports, MaxTurns: 5, APIBase: "not a url"},
	}
	for i, r := range bad {
		if _, err := e.cli.Scan(context.Background(), r); !IsKind(err, KindBadRequest) {
			t.Errorf("request %d must be refused: %v", i, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "r")); err == nil {
		t.Fatal("a report directory was created outside the org dir")
	}
	// cli.go never mentions the global verbs
	f, err := parser.ParseFile(token.NewFileSet(), "cli.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if l, ok := n.(*ast.BasicLit); ok && l.Kind == token.STRING {
			switch strings.Trim(l.Value, "\"`") {
			case "install", "setup", "update", "login", "init", "serve":
				t.Errorf("cli.go contains the literal %s", l.Value)
			}
		}
		return true
	})
}

func TestModifyTreeIsVisibleInTheDiff(t *testing.T) {
	e := newEnv(t)
	before, err := Manifest(e.tree)
	if err != nil {
		t.Fatal(err)
	}
	if got, _, _ := classify(t, e, "modify_tree"); got != Complete {
		t.Fatalf("modify_tree: %s", got)
	}
	after, _ := Manifest(e.tree)
	d := Diff(before, after)
	if strings.Join(d.Added, ",") != "scan-notes.txt" || strings.Join(d.Changed, ",") != "src/app.py" || len(d.Removed) != 0 || d.Empty() {
		t.Fatalf("diff: %+v", d)
	}
	if !Diff(after, after).Empty() {
		t.Fatal("identical manifests have an empty diff")
	}
	_ = os.Remove(filepath.Join(e.tree, "README.md"))
	m3, _ := Manifest(e.tree)
	if d = Diff(after, m3); len(d.Removed) != 1 || d.Removed[0] != "README.md" {
		t.Fatalf("removed: %+v", d)
	}
	// a planted symlink shows
	_ = os.Symlink("/etc/hosts", filepath.Join(e.tree, "ln"))
	m4, _ := Manifest(e.tree)
	if d = Diff(m3, m4); len(d.Added) != 1 || d.Added[0] != "ln" {
		t.Fatalf("symlink: %+v", d)
	}
}

func TestReportParseScrubsAndKeysAreStable(t *testing.T) {
	e := newEnv(t)
	_, rep, _ := classify(t, e, "findings")
	if rep == nil || len(rep.Findings) != 2 {
		t.Fatalf("report: %+v", rep)
	}
	a, b := FindingKey(rep.Findings[0]), FindingKey(rep.Findings[0])
	if a != b || a == FindingKey(rep.Findings[1]) || len(a) != 64 {
		t.Fatal("finding key")
	}
	moved := rep.Findings[0]
	moved.Line++
	if FindingKey(moved) == a {
		t.Fatal("the line is part of the key")
	}
	raw := `{"status":"completed","scope":{"target":"/t"},"findings":[{"title":"` + strings.Repeat("a", 500) + ` token=hunter2value","severity":"low","description":"glpat-AAAAAAAAAAAAAAAAAAAA ` + secretKey + `"}]}`
	r, err := Parse([]byte(raw), []string{secretKey})
	if err != nil {
		t.Fatal(err)
	}
	f := r.Findings[0]
	if len([]rune(f.Title)) > MaxTitle || strings.Contains(f.Description, "glpat") || strings.Contains(f.Description, secretKey) {
		t.Fatalf("clip/scrub: %+v", f)
	}
	for _, bad := range []string{`{}`, `{"status":"done","scope":{"target":"/t"},"findings":[]}`, `{"status":"completed","scope":{"target":"/t"},"findings":[{"title":"x","severity":"urgent"}]}`, `not json`} {
		if _, err := Parse([]byte(bad), nil); err == nil {
			t.Errorf("%s must not parse", bad)
		}
	}
}

func TestIssueDraftHasNoHomePathAndNoSecret(t *testing.T) {
	f := Finding{Title: "SQLi in /Users/someone/work/scan/src/app.py", Severity: "high", CWE: "CWE-89", File: "src/app.py", Line: 3,
		Description: "found at /srv/scan/target/src/app.py with " + secretKey + " and Bearer abcdefghijklmnopqrstuvwxyz0123", Proof: "cat /Users/someone/.ssh/id_rsa ``` end"}
	md := IssueDraft(f, "/srv/scan/target", "/Users/someone", []string{secretKey})
	for _, bad := range []string{"/Users/someone", "/srv/scan/target", secretKey, "abcdefghijklmnopqrstuvwxyz0123"} {
		if strings.Contains(md, bad) {
			t.Errorf("draft contains %q:\n%s", bad, md)
		}
	}
	for _, want := range []string{"~/work", "<repo>/src/app.py", "Severity: major (scanner: high)", "`src/app.py:3`", "nothing was sent"} {
		if !strings.Contains(md, want) {
			t.Errorf("draft lacks %q", want)
		}
	}
	if strings.Count(md, "```") != 2 {
		t.Error("proof must not break out of its code fence")
	}
}

func TestVersionAndSandboxMode(t *testing.T) {
	e := newEnv(t)
	h, err := e.cli.Version(context.Background())
	if err != nil || !strings.Contains(h.Version, "strix") {
		t.Fatalf("version: %+v %v", h, err)
	}
	e.cli.Mode = extensions.SandboxEnforce
	_, err = e.cli.Version(context.Background())
	if extensions.SandboxAvailable() != (err == nil) {
		t.Fatalf("enforce mode: available=%v err=%v", extensions.SandboxAvailable(), err)
	}
	if err != nil && !IsKind(err, KindSandbox) {
		t.Fatal(err)
	}
}
