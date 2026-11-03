package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orqen/internal/catherd"
	"orqen/internal/claudehost"
	"orqen/internal/codexhost"
)

// TestClaudeCatherdSpikeReal is Spike 0 for the Claude Code host: the real claude drives the real catherd in an
// isolated catherd namespace. It spends tokens on the rung given in ORQEN_TEST_SPIKE_RUNG and must be run by the user
// (or by someone the user told to).
//
// The login of Claude Code is the one of the organization's isolated config directory (the keychain item is named
// after that path), so DataDir is the real data directory; the catherd home, the repository and the run live in a
// temporary directory.
func TestClaudeCatherdSpikeReal(t *testing.T) {
	if os.Getenv("ORQEN_TEST_CLAUDE_SPIKE") != "1" {
		t.Skip("ORQEN_TEST_CLAUDE_SPIKE=1 is not set")
	}
	e := needEnv(t, "ORQEN_TEST_SPIKE_RUNG", "ORQEN_TEST_CLAUDE_BIN", "ORQEN_TEST_CATHERD_BIN", "ORQEN_TEST_CLAUDE_DATA_DIR", "ORQEN_TEST_CLAUDE_ORG")
	rung, orgID, realData := e["ORQEN_TEST_SPIKE_RUNG"], e["ORQEN_TEST_CLAUDE_ORG"], e["ORQEN_TEST_CLAUDE_DATA_DIR"]
	before := personalState(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	tmp := t.TempDir()
	org := catherd.NewOrgEnv(tmp, orgID) // the catherd home of the spike; its base name is the organization id
	if err := org.Ensure(); err != nil {
		t.Fatal(err)
	}
	rt := catherd.NewRuntime(ctx, catherd.RuntimeConfig{Bin: e["ORQEN_TEST_CATHERD_BIN"], DataDir: tmp, UserHome: tmp})
	cli := &catherd.CLI{RT: rt, Neutral: t.TempDir()}

	repo := filepath.Join(tmp, "repo")
	bare := filepath.Join(tmp, "remote.git")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# spike\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "initial")
	gitIn(t, tmp, "init", "-q", "--bare", bare)

	const name = "spike"
	if err := cli.Create(ctx, org, catherd.HostClaudeCode, name, nil); err != nil {
		t.Fatal(err)
	}
	// The worker role is replaced as a whole: its default rung must be on its ladder, and catherd refuses a set that
	// leaves the old default behind (a refusal is an outcome, not an error).
	out, err := cli.SetKey(ctx, org, catherd.HostClaudeCode, name, "roles.worker", map[string]any{"access": "workspace-write", "enabled": true, "rungs": []string{rung}, "defaultRung": rung}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Saved {
		t.Fatalf("catherd refused the worker role: %+v", out.Refused)
	}
	if err := cli.ProfileUseRepo(ctx, org, catherd.HostClaudeCode, name, repo); err != nil {
		t.Fatal(err)
	}

	if prof, perr := cli.ShowResolved(ctx, org, catherd.HostClaudeCode, name); perr == nil {
		raw, _ := json.Marshal(prof)
		if i := strings.Index(string(raw), `"roles":`); i >= 0 {
			t.Logf("profile roles: %.1200s", string(raw)[i:])
		}
	} else {
		t.Logf("show: %v", perr)
	}
	env, err := BuildHostEnvFor(catherd.HostClaudeCode, rt, org, realData, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = EnsureClaudeKeychain(realData, orgID); err != nil {
		t.Fatal(err)
	}
	client := claudehost.New(claudehost.Config{Bin: e["ORQEN_TEST_CLAUDE_BIN"], Env: env, Cwd: org.Tmp(), ConfigDir: ClaudeConfigDir(realData, orgID),
		CatherdBin: e["ORQEN_TEST_CATHERD_BIN"], CatherdEnv: CatherdMCPEnv(catherd.HostClaudeCode, org), Model: "claude-haiku-4-5"})
	defer client.Close()
	th, err := client.StartThread(ctx, codexhost.ThreadParams{Cwd: org.Tmp(), Effort: ""})
	if err != nil {
		t.Fatal(err)
	}
	call := func(tool string, args map[string]any) codexhost.ToolCall {
		t.Helper()
		c, err := client.RunDirective(ctx, th.ID, codexhost.NewDirective(tool, args), 5*time.Minute)
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		return c
	}

	// 1. one directed status (read only), 2. run_start.
	call("status", map[string]any{})
	start := call("run_start", map[string]any{"repo": repo, "title": "orqen spike", "a_lines": []string{"A1: README.md contains the word spike"}})
	t.Logf("run_start: %s", start.Result)
	runID := catherdRunID(start.Result)
	if runID == "" {
		t.Fatalf("no run id in %s", start.Result)
	}

	// 3. A dispatch whose turn is cut short (the host is lost mid-turn). The history of the session decides whether
	// it is safe to send it again, and catherd must agree with that verdict.
	dispatch := codexhost.NewDirective("dispatch", map[string]any{"run": runID, "role": "worker", "name": "worker-spike", "brief": "Create a file named spike.txt containing the word spike. Do not run git push.", "rung": rung})
	_, err = client.RunDirective(ctx, th.ID, dispatch, 7*time.Second)
	t.Logf("cut-short dispatch: %v", err)
	if err != nil && !errors.Is(err, codexhost.ErrOutcomeUnknown) {
		t.Fatalf("a cut-short turn must be an unknown outcome, got %v", err)
	}
	time.Sleep(15 * time.Second) // whatever the killed turn already told catherd has landed by now
	fresh := claudehost.New(claudehost.Config{ConfigDir: ClaudeConfigDir(realData, orgID)})
	hist, herr := fresh.ResumeThread(ctx, th.ID)
	var turns []codexhost.Turn
	if herr == nil {
		turns = hist.Turns
	}
	safe, known := DispatchSafeToRetry(turns, dispatch.ID)
	view, err := cli.RunsShow(ctx, org, runID)
	if err != nil {
		t.Fatal(err)
	}
	n := countDispatches(view, "worker-spike")
	t.Logf("history verdict: safe=%v known=%v; catherd has %d dispatch(es)", safe, known, n)
	switch {
	case known && safe && n != 0:
		t.Fatalf("history said it is safe to retry, but catherd already has %d dispatch(es)", n)
	case known && !safe && n != 1:
		t.Fatalf("history said the dispatch happened, but catherd has %d dispatch(es)", n)
	}
	if n == 0 && (!known || safe) { // nothing happened: send it again, exactly once
		if _, err = client.RunDirective(ctx, th.ID, codexhost.NewDirective("dispatch", dispatch.Args), 5*time.Minute); err != nil {
			t.Fatalf("dispatch retry: %v", err)
		}
	}

	// 4. the worker exits, the host collects it with `result` (the engine does the same), and catherd then holds
	// exactly one dispatch and one record.
	wait := 15 * time.Minute
	if v := os.Getenv("ORQEN_TEST_SPIKE_WAIT"); v != "" {
		if d, derr := time.ParseDuration(v); derr == nil {
			wait = d
		}
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if view, err = cli.RunsShow(ctx, org, runID); err != nil {
			t.Fatal(err)
		}
		if exited(view, "worker-spike") {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if exited(view, "worker-spike") {
		call("result", map[string]any{"run": runID, "name": "worker-spike"})
		if view, err = cli.RunsShow(ctx, org, runID); err != nil {
			t.Fatal(err)
		}
	}
	if len(view.Records) == 0 {
		t.Logf("dispatches: %+v", view.Dispatches)
		dumpRoleLogs(t, org.Home())
	}
	if n = countDispatches(view, "worker-spike"); n != 1 || len(view.Records) != 1 {
		t.Fatalf("want one dispatch and one record, got %d dispatches, %d records", n, len(view.Records))
	}
	t.Logf("worker record: %+v", view.Records[0])

	// 5. a push from the host's environment is blocked.
	cmd := exec.Command("git", "push", "https://git.invalid/spike.git", "main")
	cmd.Dir = repo
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + HostHome(realData, orgID), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + GitConfigPath(realData, orgID), "GIT_TERMINAL_PROMPT=0"}
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "orqen-blocked") {
		t.Fatalf("a push from the host's environment was not blocked by the host gitconfig (err=%v):\n%s", err, out)
	}

	// 6. the personal-state guard is unchanged.
	if after := personalState(t); after != before {
		t.Fatal("personal state changed")
	}
}

func countDispatches(v catherd.RunView, name string) int {
	n := 0
	for _, d := range v.Dispatches {
		if d.Name == name {
			n++
		}
	}
	return n
}

// dumpRoleLogs prints the tail of every log-like file under the catherd home: what a silent worker left behind.
func dumpRoleLogs(t *testing.T, home string) {
	t.Helper()
	_ = filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		n := strings.ToLower(d.Name())
		if !strings.Contains(n, "log") && !strings.Contains(n, "err") && !strings.HasSuffix(n, ".jsonl") && !strings.Contains(n, "out") {
			return nil
		}
		b, _ := os.ReadFile(p)
		if len(b) > 1500 {
			b = b[len(b)-1500:]
		}
		t.Logf("== %s\n%s", strings.TrimPrefix(p, home), b)
		return nil
	})
}

func exited(v catherd.RunView, name string) bool {
	for _, d := range v.Dispatches {
		if d.Name == name && d.ExitReason != "" {
			return true
		}
	}
	return false
}
