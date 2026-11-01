package secscan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"orqen/internal/extensions"
)

// TestStrixReal runs the assumed contract v1 against a real Strix. It is gated, SPENDS MODEL TOKENS and is for the
// user to run; the coder never runs it:
//
//	ORQEN_TEST_STRIX_BIN=<abs> ORQEN_TEST_STRIX_MODEL=<model> ORQEN_TEST_STRIX_KEY_CMD='<command printing the key>' \
//	ORQEN_TEST_ALLOW_MODEL_SPEND=1 go test ./internal/secscan -run TestStrixReal -v
//
// The key command runs at test time through the shell and its output is never printed. The scan covers a deliberately
// vulnerable 2-file fixture with a budget of 1 USD and 5 turns; schema differences are reported.
func TestStrixReal(t *testing.T) {
	bin, model, keyCmd := os.Getenv("ORQEN_TEST_STRIX_BIN"), os.Getenv("ORQEN_TEST_STRIX_MODEL"), os.Getenv("ORQEN_TEST_STRIX_KEY_CMD")
	if bin == "" || model == "" || keyCmd == "" || os.Getenv("ORQEN_TEST_ALLOW_MODEL_SPEND") != "1" {
		t.Skip("set ORQEN_TEST_STRIX_BIN, _MODEL, _KEY_CMD and ORQEN_TEST_ALLOW_MODEL_SPEND=1 to run (spends model tokens)")
	}
	keyOut, err := exec.Command("/bin/sh", "-c", keyCmd).Output()
	if err != nil {
		t.Fatal("the key command failed")
	}
	key := strings.TrimSpace(string(keyOut))
	m, _ := extensions.Get("strix")
	p, reason := extensions.Usable(m, extensions.Snapshot{Status: extensions.StatusEnabled, Path: bin, Version: "real", SHA256: "real", Credentials: map[string]bool{"llm_api_key": true}})
	if reason != "" {
		t.Fatal(reason)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	org := filepath.Join(root, "scans", "org_real")
	tree := filepath.Join(org, "scn_real", "target")
	_ = os.MkdirAll(filepath.Join(tree, "src"), 0o700)
	_ = os.WriteFile(filepath.Join(tree, "src", "app.py"), []byte("import sqlite3\ndef lookup(db, i):\n    return db.execute(\"select * from t where id=\" + i)\n"), 0o600)
	_ = os.WriteFile(filepath.Join(tree, "README.md"), []byte("demo\n"), 0o600)
	cli := NewCLI(p, org)
	if h, err := cli.Version(context.Background()); err != nil {
		t.Errorf("version: %v", err)
	} else {
		t.Logf("version: %s", h.Version)
	}
	raw, err := cli.Scan(context.Background(), Request{Tree: tree, ReportDir: filepath.Join(org, "scn_real", "report"), MaxTurns: 5, BudgetUSD: 1, Model: model, LLMKey: key})
	if err != nil {
		t.Fatal(err)
	}
	var rep *Report
	var perr error
	if raw.Report != nil {
		r, e := Parse(raw.Report, []string{key})
		rep, perr = &r, e
		if e != nil {
			rep = nil
			t.Errorf("schema diff (report): %v", e)
		}
	}
	t.Logf("exit=%d timedOut=%v completion=%s", raw.Exit, raw.TimedOut, Classify(raw.Exit, raw.TimedOut, rep, perr, tree))
}
