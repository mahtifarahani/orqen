package execution

// Gated real tests. They never run in `make check`; the user runs them deliberately (docs/LIVE-TRIAL.md).
//
//	TestCodexHostProbeReal    ORQEN_TEST_CODEX_HOST=1  ORQEN_TEST_CODEX_HOME=<logged-in isolated CODEX_HOME>
//	                          ORQEN_TEST_CODEX_BIN=<codex>   (reads only: initialize, model/list, mcpServerStatus/list)
//	TestCodexCatherdSpikeReal ORQEN_TEST_CODEX_SPIKE=1 ORQEN_TEST_SPIKE_RUNG=<cheap rung> ORQEN_TEST_CODEX_HOME=...
//	                          ORQEN_TEST_CODEX_BIN=... ORQEN_TEST_CATHERD_BIN=...   (SPENDS TOKENS)

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"orqen/internal/catherd"
	"orqen/internal/codexhost"
)

const realOrgID = "org_01M5C6DVPZ76VKRZ7DV3YJ202T"

func needEnv(t *testing.T, names ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, n := range names {
		v := os.Getenv(n)
		if v == "" {
			t.Skipf("%s is not set", n)
		}
		out[n] = v
	}
	return out
}

// manifestOf hashes every regular file below dir (path and content), so a before/after comparison catches any write.
func manifestOf(dir string) string {
	var lines []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		b, _ := os.ReadFile(p)
		s := sha256.Sum256(b)
		rel, _ := filepath.Rel(dir, p)
		lines = append(lines, rel+" "+hex.EncodeToString(s[:]))
		return nil
	})
	sort.Strings(lines)
	s := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(s[:])
}

// personalState hashes the user's own catherd and Codex state; it must be identical before and after a test.
func personalState(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, d := range []string{".config/catherd", ".local/share/catherd", ".claude/agents", "Library/LaunchAgents"} {
		parts = append(parts, d+"="+manifestOf(filepath.Join(home, d)))
	}
	for _, f := range []string{".codex/config.toml", ".codex/hooks.json"} {
		b, _ := os.ReadFile(filepath.Join(home, f))
		s := sha256.Sum256(b)
		parts = append(parts, f+"="+hex.EncodeToString(s[:]))
	}
	return strings.Join(parts, "\n")
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@localhost", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@localhost"}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out.String())
	}
	return strings.TrimSpace(out.String())
}

// TestCodexHostProbeReal starts the real `codex app-server` with an explicit environment and a logged-in isolated
// CODEX_HOME, then reads initialize, model/list and mcpServerStatus/list. It creates no thread and spends no tokens.
func TestCodexHostProbeReal(t *testing.T) {
	if os.Getenv("ORQEN_TEST_CODEX_HOST") != "1" {
		t.Skip("ORQEN_TEST_CODEX_HOST=1 is not set")
	}
	e := needEnv(t, "ORQEN_TEST_CODEX_HOME", "ORQEN_TEST_CODEX_BIN")
	before := personalState(t)
	tmp := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + tmp, "CODEX_HOME=" + e["ORQEN_TEST_CODEX_HOME"], "TMPDIR=" + tmp, "LANG=C"}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rwc, proc, err := ExecSpawn(ctx, HostConfig{CodexBin: e["ORQEN_TEST_CODEX_BIN"], DataDir: tmp, OrgID: realOrgID}, env, []string{"app-server", "--listen", "stdio://", "-c", "analytics.enabled=false"})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Kill()
	client, err := codexhost.Dial(ctx, rwc, codexhost.ClientInfo{Name: "orqen-test", Title: "Orqen test", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	res, err := client.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Version() == "" {
		t.Fatalf("no version in %q", res.UserAgent)
	}
	t.Logf("codex %s, logged in: %v (%s), %d models, %d MCP servers", res.Version(), res.LoggedIn, res.AccountType, len(res.Models), len(res.MCPServers))
	if after := personalState(t); after != before {
		t.Fatal("personal state changed")
	}
}

// TestCodexCatherdSpikeReal is Spike 0: the real host drives the real catherd in an isolated namespace. It spends
// tokens on the rung the user chose and must be run by the user only.
func TestCodexCatherdSpikeReal(t *testing.T) {
	if os.Getenv("ORQEN_TEST_CODEX_SPIKE") != "1" {
		t.Skip("ORQEN_TEST_CODEX_SPIKE=1 is not set")
	}
	e := needEnv(t, "ORQEN_TEST_SPIKE_RUNG", "ORQEN_TEST_CODEX_HOME", "ORQEN_TEST_CODEX_BIN", "ORQEN_TEST_CATHERD_BIN")
	rung := e["ORQEN_TEST_SPIKE_RUNG"]
	before := personalState(t)
	data := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	// Isolated namespace: temp data dir, temp CATHERD_HOME, the user's isolated CODEX_HOME copied in (auth only).
	org := catherd.NewOrgEnv(data, realOrgID)
	if err := org.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := EnsureHostTree(data, realOrgID); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(e["ORQEN_TEST_CODEX_HOME"], "auth.json")); err == nil {
		if err = os.WriteFile(filepath.Join(CodexHome(data, realOrgID), "auth.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rt := catherd.NewRuntime(ctx, catherd.RuntimeConfig{Bin: e["ORQEN_TEST_CATHERD_BIN"], DataDir: data, UserHome: data})
	cli := &catherd.CLI{RT: rt, Neutral: t.TempDir()}

	// A temp repo with one commit and a bare loopback "remote" a worker must not be able to push to.
	repo := filepath.Join(data, "repo")
	bare := filepath.Join(data, "remote.git")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# spike\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "initial")
	gitIn(t, data, "init", "-q", "--bare", bare)

	// A profile that routes every role to the chosen cheap rung.
	const name = "spike"
	if err := cli.Create(ctx, org, catherd.HostCodex, name, nil); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"worker"} {
		if _, err := cli.SetKey(ctx, org, catherd.HostCodex, name, "roles."+role+".rungs", []string{rung}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := cli.ProfileUseRepo(ctx, org, catherd.HostCodex, name, repo); err != nil {
		t.Fatal(err)
	}

	sup := &Supervisor{Spawn: ExecSpawn}
	defer sup.StopAll()
	cfg := HostConfig{CodexBin: e["ORQEN_TEST_CODEX_BIN"], CatherdBin: e["ORQEN_TEST_CATHERD_BIN"], DataDir: data, OrgID: realOrgID, Org: org, RT: rt}
	client, err := sup.Client(ctx, realOrgID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	th, err := client.StartThread(ctx, codexhost.ThreadParams{Cwd: org.Tmp()})
	if err != nil {
		t.Fatal(err)
	}
	call := func(client *codexhost.Client, tool string, args map[string]any) codexhost.ToolCall {
		t.Helper()
		c, err := client.RunDirective(ctx, th.ID, codexhost.NewDirective(tool, args), 5*time.Minute)
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		return c
	}

	// 1. one directed status (read only)
	call(client, "status", map[string]any{})
	// 2. run_start
	start := call(client, "run_start", map[string]any{"repo": repo, "title": "orqen spike", "a_lines": []string{"A1: README.md contains the word spike"}})
	t.Logf("run_start: %s", start.Result)
	runID := catherdRunID(start.Result)
	if runID == "" {
		t.Fatalf("no run id in %s", start.Result)
	}
	// 3. one worker dispatch that writes a file
	call(client, "dispatch", map[string]any{"run": runID, "role": "worker", "name": "worker-spike", "brief": "Create a file named spike.txt containing the word spike. Do not run git push."})

	// 4. kill the app-server mid-role, restart it, reconcile: exactly one dispatch, the record is collected.
	sup.Stop(realOrgID)
	client, err = sup.Client(ctx, realOrgID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Minute)
	var view catherd.RunView
	for time.Now().Before(deadline) {
		if view, err = cli.RunsShow(ctx, org, runID); err != nil {
			t.Fatal(err)
		}
		if len(view.Records) > 0 {
			break
		}
		time.Sleep(5 * time.Second)
	}
	dispatches := 0
	for _, d := range view.Dispatches {
		if d.Name == "worker-spike" {
			dispatches++
		}
	}
	if dispatches != 1 || len(view.Records) != 1 {
		t.Fatalf("want one dispatch and one record, got %d dispatches, %d records", dispatches, len(view.Records))
	}
	_ = client

	// 5. a worker `git push` is blocked: the isolated gitconfig rewrites every push URL.
	cmd := exec.Command("git", "push", "file://"+bare, "main")
	cmd.Dir = repo
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + HostHome(data, realOrgID), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + GitConfigPath(data, realOrgID), "GIT_TERMINAL_PROMPT=0"}
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("a push from the host's environment succeeded:\n%s", out)
	}
	if n := strings.TrimSpace(gitIn(t, data, "-C", bare, "count-objects", "-v")); !strings.Contains(n, "count: 0") && !strings.Contains(n, "in-pack: 0") {
		t.Logf("remote objects: %s", n)
	}

	// 6. catherd's queue attempt reached no personal daemon: delivery receipts say not-submitted.
	receipts, _ := filepath.Glob(filepath.Join(org.Home(), "**", "*receipt*"))
	for _, r := range receipts {
		b, _ := os.ReadFile(r)
		if bytes.Contains(b, []byte("submitted")) && !bytes.Contains(b, []byte("not-submitted")) {
			t.Fatalf("a delivery receipt reports a submission: %s", r)
		}
	}

	// 7. the personal-state guard is unchanged
	if after := personalState(t); after != before {
		t.Fatal("personal state changed")
	}
}

// catherdRunID reads the run id out of run_start's structured result.
func catherdRunID(raw []byte) string {
	s := string(raw)
	for _, key := range []string{`"runId":"`, `"run":"`, `"id":"`} {
		if i := strings.Index(s, key); i >= 0 {
			rest := s[i+len(key):]
			if j := strings.Index(rest, `"`); j > 0 {
				return rest[:j]
			}
		}
	}
	return ""
}
