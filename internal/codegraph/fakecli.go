package codegraph

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

// FakeScenario is read from the "gitnexus" object of the scenario file (written by PUT /_fakes/host/scenario).
type FakeScenario struct {
	FailVerb    string `json:"fail_verb"`
	SlowMs      int    `json:"slow_ms"`
	WrongSchema bool   `json:"wrong_schema"`
	Version     string `json:"version"`
}

// LoadFakeScenario reads the scenario file; a missing file means no failures.
func LoadFakeScenario(path string) FakeScenario {
	var s struct {
		G FakeScenario `json:"gitnexus"`
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s.G
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
// arguments after the binary name. It returns the process exit code.
func RunFakeCLI(args []string, stdout io.Writer, scenarioPath string) int {
	fail := func(msg string, code int) int { fmt.Fprintln(os.Stderr, "gitnexus-fake:", msg); return code }
	sc := LoadFakeScenario(scenarioPath)
	if sc.SlowMs > 0 {
		time.Sleep(time.Duration(sc.SlowMs) * time.Millisecond)
	}
	var verb string
	var out any
	var err error
	switch {
	case len(args) == 1 && args[0] == "--version":
		verb = "version"
	case len(args) > 0 && args[0] == "analyze":
		verb = "analyze"
	case len(args) > 2 && args[0] == "query" && (args[1] == "impact" || args[1] == "context"):
		verb = args[1]
	default:
		return fail("unsupported arguments", 2)
	}
	if sc.FailVerb == verb {
		return fail("scenario: "+verb+" fails", 3)
	}
	switch verb {
	case "version":
		v := sc.Version
		if v == "" {
			v = "fake-1.0"
		}
		out = Health{Version: v, Schema: "v1"}
	case "analyze":
		tree, _ := flagVal(args, "--path")
		dst, _ := flagVal(args, "--output")
		if tree == "" || dst == "" {
			return fail("--path and --output are required", 2)
		}
		if out, err = analyzeTree(tree, dst); err != nil {
			return fail(err.Error(), 1)
		}
	case "impact":
		idx, _ := flagVal(args, "--index")
		target, _ := flagVal(args, "--target")
		d, _ := flagVal(args, "--depth")
		l, _ := flagVal(args, "--limit")
		depth, _ := strconv.Atoi(d)
		limit, _ := strconv.Atoi(l)
		// the contract has one --target for files and symbols: try the file first, then the symbol
		if out, err = impactOf(idx, target, KindFile, depth, limit); err != nil {
			return fail(err.Error(), 1)
		}
		if g, _ := loadGraph(idx); len(targetFiles(g, target, KindFile)) == 0 {
			if out, err = impactOf(idx, target, KindSymbol, depth, limit); err != nil {
				return fail(err.Error(), 1)
			}
		}
	case "context":
		idx, _ := flagVal(args, "--index")
		target, _ := flagVal(args, "--target")
		l, _ := flagVal(args, "--limit")
		limit, _ := strconv.Atoi(l)
		if out, err = contextOf(idx, target, limit); err != nil {
			return fail(err.Error(), 1)
		}
	}
	if sc.WrongSchema {
		out = map[string]any{"unexpected": true}
	}
	if err = json.NewEncoder(stdout).Encode(out); err != nil {
		return 1
	}
	return 0
}
