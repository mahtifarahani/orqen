package secscan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FakeScenario is read from the "strix" object of the scenario file (written by PUT /_fakes/host/scenario). Scenario:
// "" or "clean", "findings", "budget_stop_exit0", "turns_stop", "crash", "no_report", "wrong_schema", "wrong_target",
// "modify_tree", "leak_key".
type FakeScenario struct {
	Scenario string `json:"scenario"`
	SlowMs   int    `json:"slow_ms"`
	Version  string `json:"version"`
}

// LoadFakeScenario reads the scenario file; a missing file means a clean scan.
func LoadFakeScenario(path string) FakeScenario {
	var s struct {
		S FakeScenario `json:"strix"`
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s.S
}

func findingsFixture() []Finding {
	return []Finding{
		{Title: "SQL injection in the lookup handler", Severity: "high", CWE: "CWE-89", File: "src/app.py", Line: 3, Description: "User input reaches the query string.", Proof: "GET /lookup?id=1%27 OR 1=1"},
		{Title: "Verbose error page", Severity: "info", CWE: "CWE-209", File: "src/app.py", Line: 8, Description: "The handler prints the stack trace."},
	}
}

// fakeRun is the engine: it behaves like the contract and writes <reportDir>/report.json. It returns the exit code and
// the stdout text. key is the LLM key the process received (only the leak_key scenario echoes it).
func fakeRun(sc FakeScenario, tree, reportDir, key string) (int, string) {
	if sc.SlowMs > 0 {
		time.Sleep(time.Duration(sc.SlowMs) * time.Millisecond)
	}
	rep := Report{Status: StatusCompleted, Turns: 3, CostUSD: 0.01, Findings: []Finding{}}
	rep.Scope.Target = tree
	stdout := "scan started\nscan finished\n"
	exit := 0
	write := true
	var raw []byte
	switch sc.Scenario {
	case "findings":
		rep.Findings = findingsFixture()
	case "budget_stop_exit0":
		rep.Status, rep.CostUSD = StatusBudgetExceeded, 1.00
		rep.Findings = findingsFixture()[:1]
	case "turns_stop":
		rep.Status, rep.Turns = StatusMaxTurns, 50
	case "crash":
		return 1, "scan started\ncrashed\n"
	case "no_report":
		write = false
	case "wrong_schema":
		raw = []byte(`{"unexpected":true}`)
	case "wrong_target":
		rep.Scope.Target = "/somewhere/else"
	case "modify_tree":
		rep.Findings = findingsFixture()[:1]
		_ = os.WriteFile(filepath.Join(tree, "scan-notes.txt"), []byte("created by the scan\n"), 0o600)
		if f, err := os.OpenFile(filepath.Join(tree, "src", "app.py"), os.O_APPEND|os.O_WRONLY, 0); err == nil {
			_, _ = f.WriteString("# touched by the scan\n")
			f.Close()
		}
	case "leak_key":
		rep.Findings = []Finding{{Title: "Key in config", Severity: "medium", Description: "found " + key}}
		stdout += "using key " + key + "\n"
	}
	if write {
		if raw == nil {
			raw, _ = json.Marshal(rep)
		}
		if err := os.MkdirAll(reportDir, 0o700); err == nil {
			_ = os.WriteFile(filepath.Join(reportDir, "report.json"), raw, 0o600)
		}
	}
	return exit, stdout
}

// Fake is the in-process Scanner for tests; it runs the same engine as the fake CLI.
type Fake struct {
	mu       sync.Mutex
	Scenario FakeScenario
	Calls    []Request
}

var _ Scanner = (*Fake)(nil)

func (f *Fake) Version(ctx context.Context) (Health, error) {
	v := f.Scenario.Version
	if v == "" {
		v = "strix 0.1.0-fake"
	}
	return Health{Version: v}, nil
}

func (f *Fake) Scan(ctx context.Context, req Request) (RawResult, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, req)
	sc := f.Scenario
	f.mu.Unlock()
	if !filepath.IsAbs(req.Tree) {
		return RawResult{}, errf(KindBadRequest, "scan", "tree must be absolute")
	}
	exit, out := fakeRun(sc, req.Tree, req.ReportDir, req.LLMKey)
	res := RawResult{Exit: exit, Stdout: Redact(out, []string{req.LLMKey})}
	if b, err := os.ReadFile(filepath.Join(req.ReportDir, "report.json")); err == nil {
		res.Report = b
	}
	return res, nil
}

// RunFakeCLI implements the assumed contract v1 for the wrapper script `orqen-fakehost strix`. args are the arguments
// after the binary name; the LLM key is read from the environment like the real tool would.
func RunFakeCLI(args []string, stdout *os.File, scenarioPath string) int {
	sc := LoadFakeScenario(scenarioPath)
	if len(args) == 1 && args[0] == "--version" {
		v := sc.Version
		if v == "" {
			v = "strix 0.1.0-fake"
		}
		fmt.Fprintln(stdout, v)
		return 0
	}
	flag := func(name string) string {
		for i, a := range args {
			if a == name && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	tree, dir := flag("--target"), flag("--output-dir")
	if tree == "" || dir == "" || !filepath.IsAbs(tree) || !filepath.IsAbs(dir) || !hasArg(args, "--non-interactive") {
		fmt.Fprintln(os.Stderr, "strix-fake: usage: --target <abs dir> --non-interactive --output-dir <abs dir> --max-turns <n>")
		return 2
	}
	exit, out := fakeRun(sc, tree, dir, os.Getenv("LLM_API_KEY"))
	fmt.Fprint(stdout, out)
	return exit
}

func hasArg(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}
