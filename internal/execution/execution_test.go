package execution

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"orqen/internal/catherd"
	"orqen/internal/codexhost"
	"orqen/internal/testfakes"
	"orqen/internal/testfakes/hostfake"
	"orqen/internal/workspace"
)

const orgID = "org_01HZZZZZZZZZZZZZZZZZZZZZZZ"

func fixture(t *testing.T) (HostConfig, string) {
	data := t.TempDir()
	rt := catherd.NewRuntime(context.Background(), catherd.RuntimeConfig{Bin: "/bin/echo", Path: "/usr/bin:/bin", DataDir: data, UserHome: t.TempDir(), Getenv: os.Getenv})
	org := catherd.NewOrgEnv(data, orgID)
	return HostConfig{CodexBin: "/bin/echo", CatherdBin: "/opt/catherd/bin/catherd", DataDir: data, OrgID: orgID, Org: org, RT: rt, Secrets: catherd.SecretEnv{"TYPESAFE_API_KEY": "sentinel-typesafe"}}, data
}

func TestHostEnvIsExactAndInheritsNothing(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent-sentinel")
	t.Setenv("CODEX_THREAD_ID", "thread-sentinel")
	t.Setenv("CODEX_API_KEY", "codex-key-sentinel")
	t.Setenv("CLAUDE_CODE_SESSION", "claude-sentinel")
	t.Setenv("HTTPS_PROXY", "http://proxy-sentinel:3128")
	t.Setenv("GITLAB_TOKEN", "gitlab-sentinel")
	t.Setenv("JIRA_TOKEN", "jira-sentinel")
	t.Setenv("CATHERD_ROLE", "worker")
	cfg, data := fixture(t)
	env, err := BuildHostEnv(cfg.RT, cfg.Org, data, cfg.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	want := map[string]bool{"TYPESAFE_API_KEY": true}
	for _, n := range HostEnvNames {
		want[n] = true
	}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected variable %s", k)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing variable %s", k)
		}
	}
	joined := strings.Join(env, "\n")
	for _, s := range []string{"agent-sentinel", "thread-sentinel", "codex-key-sentinel", "claude-sentinel", "proxy-sentinel", "gitlab-sentinel", "jira-sentinel"} {
		if strings.Contains(joined, s) {
			t.Errorf("environment leaks %s", s)
		}
	}
	if got["CATHERD_ORCHESTRATION_HOST"] != "codex" || got["GIT_SSH_COMMAND"] != "false" || got["GIT_TERMINAL_PROMPT"] != "0" || got["GIT_CONFIG_NOSYSTEM"] != "1" {
		t.Fatalf("%v", got)
	}
	if got["HOME"] != filepath.Join(data, "host-home", orgID) || got["CODEX_HOME"] != filepath.Join(data, "codex", orgID, "home") || got["TMPDIR"] != cfg.Org.Tmp() || got["CATHERD_HOME"] != cfg.Org.Home() {
		t.Fatalf("%v", got)
	}
	for _, d := range []string{got["HOME"], got["CODEX_HOME"]} {
		if st, err := os.Stat(d); err != nil || st.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", d, st, err)
		}
	}
	conf, _ := os.ReadFile(got["GIT_CONFIG_GLOBAL"])
	for _, s := range []string{"helper =", `[url "orqen-blocked://"]`, "pushInsteadOf = https://", "pushInsteadOf = http://", "pushInsteadOf = ssh://", "pushInsteadOf = git@"} {
		if !strings.Contains(string(conf), s) {
			t.Errorf("gitconfig lacks %q", s)
		}
	}
	// a secret that is not on the K3 list is refused, so a GitLab or Jira credential cannot be handed over
	if _, err = BuildHostEnv(cfg.RT, cfg.Org, data, catherd.SecretEnv{"GITLAB_TOKEN": "x"}); err == nil {
		t.Fatal("a GitLab credential must not be accepted")
	}
}

func TestHostArgs(t *testing.T) {
	cfg, _ := fixture(t)
	args := HostArgs(cfg.CatherdBin, cfg.Org, cfg)
	joined := strings.Join(args, " ")
	for _, s := range []string{"app-server --listen stdio:// --strict-config", `approval_policy="never"`, `sandbox_mode="read-only"`, `mcp_servers.catherd.command="/opt/catherd/bin/catherd"`, `mcp_servers.catherd.args=["mcp"]`, "analytics.enabled=false", "shell_environment_policy.inherit=all", "shell_environment_policy.set.GIT_CONFIG_GLOBAL=", `CATHERD_ORCHESTRATION_HOST="codex"`, `"TYPESAFE_API_KEY"`} {
		if !strings.Contains(joined, s) {
			t.Errorf("missing %q in %s", s, joined)
		}
	}
	if strings.Contains(joined, "sentinel-typesafe") {
		t.Fatal("a secret value must never be on the command line")
	}
}

// pushable builds a worktree of the fake GitLab repository through Orqen's own workspace manager.
func pushable(t *testing.T) (repos *testfakes.GitRepos, remote, worktree string) {
	if !testfakes.GitAvailable() {
		t.Skip("git is not available")
	}
	repos, err := testfakes.NewGitRepos(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(repos.Handler())
	t.Cleanup(srv.Close)
	data := t.TempDir()
	root := filepath.Join(data, "workspaces", "org_x")
	_ = os.MkdirAll(root, 0o700)
	if _, err = workspace.EnsureAskPass(data); err != nil {
		t.Fatal(err)
	}
	m := &workspace.Manager{Root: root, Git: &workspace.Git{DataDir: data, Ceiling: filepath.Join(data, "workspaces"), AllowHTTP: true}}
	remote = srv.URL + "/gitlab/group-1/proj-1.git"
	ctx := context.Background()
	mirror, err := m.EnsureMirror(ctx, "con_1", "1", remote)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Fetch(ctx, mirror, "main", workspace.Creds{Host: "127.0.0.1", Username: "oauth2", Password: repos.Token}); err != nil {
		t.Fatal(err)
	}
	wt, err := m.AddWorktree(ctx, mirror, "run_1", "orqen/orq-1/abc", "main")
	if err != nil {
		t.Fatal(err)
	}
	return repos, remote, wt.Path
}

func TestHostTreeCannotPush(t *testing.T) {
	repos, remote, worktree := pushable(t)
	cfg, data := fixture(t)
	env, err := BuildHostEnv(cfg.RT, cfg.Org, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := repos.ReceivePackRequests()
	run := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = worktree
		cmd.Env = env
		return cmd.Run()
	}
	_ = os.WriteFile(filepath.Join(worktree, "x.txt"), []byte("x"), 0o600)
	_ = run("add", "x.txt")
	_ = run("-c", "user.name=t", "-c", "user.email=t@l", "commit", "-q", "-m", "x")
	for _, args := range [][]string{
		{"push", "origin", "HEAD:refs/heads/x"},
		{"push", remote, "HEAD:refs/heads/y"},
		{"push", strings.Replace(remote, "http://", "http://oauth2:"+repos.Token+"@", 1), "HEAD:refs/heads/z"},
	} {
		if err := run(args...); err == nil {
			t.Fatalf("git %v must fail in the host tree", args)
		}
	}
	if repos.ReceivePackRequests() != before {
		t.Fatalf("the fake received %d receive-pack requests", repos.ReceivePackRequests()-before)
	}
}

func TestWorkerPushAttemptThroughTheFakeHost(t *testing.T) {
	repos, _, worktree := pushable(t)
	cfg, data := fixture(t)
	env, err := BuildHostEnv(cfg.RT, cfg.Org, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	sc := hostfake.DefaultScenario()
	sc.WorkerPushAttempt = true
	sc.PushEnv = func() []string { return env }
	cat := hostfake.NewCatherd(filepath.Join(t.TempDir(), "d"), sc)
	app := hostfake.NewAppServer(cat)
	cl, err := codexhost.Dial(context.Background(), app.Pipe(), codexhost.ClientInfo{Name: "orqen", Version: "t"})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	th, _ := cl.StartThread(context.Background(), codexhost.ThreadParams{})
	do := func(tool string, a map[string]any) {
		if _, err := cl.RunDirective(context.Background(), th.ID, codexhost.NewDirective(tool, a), 3*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	run, _ := cat.Call("run_start", map[string]any{"repo": worktree, "title": "t", "a_lines": []any{"a"}}, th.ID)
	runID := run.(map[string]any)["run"].(string)
	before := repos.ReceivePackRequests()
	do("dispatch", map[string]any{"run": runID, "role": "worker", "name": "w", "brief": "x", "rung": "codex:gpt-6-sol#low"})
	if v, _ := cat.RunsShow(context.Background(), nil, runID); len(v.Records) != 1 {
		t.Fatal("the worker record should exist")
	}
	if repos.ReceivePackRequests() != before {
		t.Fatal("the worker's push attempt reached the fake GitLab")
	}
}

type fakeProc struct {
	done   chan struct{}
	killed atomic.Bool
}

func (p *fakeProc) Kill() {
	if p.killed.CompareAndSwap(false, true) {
		close(p.done)
	}
}
func (p *fakeProc) Done() <-chan struct{} { return p.done }

func fakeSpawn(spawned *atomic.Int64, procs *[]*fakeProc) SpawnFunc {
	return func(ctx context.Context, cfg HostConfig, env, args []string) (io.ReadWriteCloser, Proc, error) {
		cat := hostfake.NewCatherd(filepath.Join(cfg.DataDir, "fake"), nil)
		app := hostfake.NewAppServer(cat)
		spawned.Add(1)
		p := &fakeProc{done: make(chan struct{})}
		*procs = append(*procs, p)
		return app.Pipe(), p, nil
	}
}

func TestSupervisorIdleStopAndRespawn(t *testing.T) {
	cfg, _ := fixture(t)
	var spawned atomic.Int64
	var procs []*fakeProc
	s := &Supervisor{Spawn: fakeSpawn(&spawned, &procs), IdleStop: 80 * time.Millisecond}
	defer s.StopAll()
	c1, err := s.Client(context.Background(), orgID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c2, _ := s.Client(context.Background(), orgID, cfg); c2 != c1 || spawned.Load() != 1 {
		t.Fatal("the same client should be reused")
	}
	s.Pin(orgID)
	time.Sleep(200 * time.Millisecond)
	if !s.Running(orgID) {
		t.Fatal("a pinned host must not stop")
	}
	s.Unpin(orgID)
	deadline := time.Now().Add(2 * time.Second)
	for s.Running(orgID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s.Running(orgID) || !procs[0].killed.Load() {
		t.Fatal("the idle host should have been stopped and killed")
	}
	if _, err = s.Client(context.Background(), orgID, cfg); err != nil || spawned.Load() != 2 {
		t.Fatal("a new use starts a new process", err, spawned.Load())
	}
}

func TestSecondSupervisorOnTheSameHomeIsBusy(t *testing.T) {
	cfg, _ := fixture(t)
	var n atomic.Int64
	var procs []*fakeProc
	a := &Supervisor{Spawn: fakeSpawn(&n, &procs)}
	b := &Supervisor{Spawn: fakeSpawn(&n, &procs)}
	defer a.StopAll()
	defer b.StopAll()
	if _, err := a.Client(context.Background(), orgID, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Client(context.Background(), orgID, cfg); !errors.Is(err, ErrHostBusy) {
		t.Fatal(err)
	}
	a.Stop(orgID)
	if _, err := b.Client(context.Background(), orgID, cfg); err != nil {
		t.Fatal("the lock is released when the first host stops", err)
	}
	// without a spawner the host is unavailable
	var none Supervisor
	if _, err := none.Client(context.Background(), "other", cfg); !errors.Is(err, ErrHostUnavailable) {
		t.Fatal(err)
	}
}

func TestExecSpawnKillsTheProcessGroup(t *testing.T) {
	cfg, data := fixture(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "fake-codex")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 300 &\necho $! > "+pidFile+"\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg.CodexBin = script
	env, _ := BuildHostEnv(cfg.RT, cfg.Org, data, nil)
	rwc, proc, err := ExecSpawn(context.Background(), cfg, env, []string{"app-server"})
	if err != nil {
		t.Fatal(err)
	}
	defer rwc.Close()
	var pid int
	for i := 0; i < 100 && pid == 0; i++ {
		if b, e := os.ReadFile(pidFile); e == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the child did not start")
	}
	proc.Kill()
	for i := 0; i < 100; i++ {
		if exec.Command("kill", "-0", strconv.Itoa(pid)).Run() != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the child of the host survived Kill")
}

func TestReconcileDispatch(t *testing.T) {
	at := func(s string) string { return s }
	view := catherd.RunView{Dispatches: []catherd.Dispatch{
		{Name: "w", DispatchID: "d1", AdmittedAt: at("2026-10-19T10:00:00.000Z")},
		{Name: "w", DispatchID: "d2", AdmittedAt: at("2026-10-19T10:05:00.000Z")},
		{Name: "x", DispatchID: "d3", AdmittedAt: at("2026-10-19T10:06:00.000Z")},
	}}
	since, _ := time.Parse(time.RFC3339, "2026-10-19T10:04:00Z")
	if id, ok := ReconcileDispatch(view, "w", since, nil); !ok || id != "d2" {
		t.Fatal(id, ok)
	}
	if _, ok := ReconcileDispatch(view, "w", since, map[string]bool{"d2": true}); ok {
		t.Fatal("a claimed dispatch is not adopted twice")
	}
	if _, ok := ReconcileDispatch(view, "w", since.Add(2*time.Hour), nil); ok {
		t.Fatal("older dispatches are not adopted")
	}
	if id, ok := ReconcileDispatch(view, "w", since.Add(-time.Hour), nil); !ok || id != "d1" {
		t.Fatal("the earliest unclaimed one", id)
	}
	if _, ok := ReconcileDispatch(view, "nobody", since, nil); ok {
		t.Fatal("a different name")
	}
}

func TestDispatchSafeToRetry(t *testing.T) {
	user := func(id string) codexhost.Item {
		return codexhost.Item{Type: "userMessage", Content: []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{{Type: "text", Text: "ORQEN-DIRECTIVE v1 id=" + id}}}
	}
	call := func(status string) codexhost.Item {
		return codexhost.Item{Type: "mcpToolCall", Server: "catherd", Tool: "dispatch", Status: status, ID: "i"}
	}
	cases := []struct {
		name        string
		history     []codexhost.Turn
		safe, known bool
	}{
		{"unreadable", nil, false, false},
		{"turn never recorded", []codexhost.Turn{}, true, true},
		{"other directive only", []codexhost.Turn{{ID: "t", Items: []codexhost.Item{user("dir_other")}}}, true, true},
		{"completed call", []codexhost.Turn{{ID: "t", Status: "completed", Items: []codexhost.Item{user("dir_1"), call("completed")}}}, false, true},
		{"call still running", []codexhost.Turn{{ID: "t", Status: "inProgress", Items: []codexhost.Item{user("dir_1"), call("inProgress")}}}, false, false},
		{"turn ended without a call", []codexhost.Turn{{ID: "t", Status: "completed", Items: []codexhost.Item{user("dir_1")}}}, true, true},
		{"interrupted before any call", []codexhost.Turn{{ID: "t", Status: "interrupted", Items: []codexhost.Item{user("dir_1")}}}, true, true},
		{"turn still running", []codexhost.Turn{{ID: "t", Status: "inProgress", Items: []codexhost.Item{user("dir_1")}}}, false, false},
	}
	for _, c := range cases {
		safe, known := DispatchSafeToRetry(c.history, "dir_1")
		if safe != c.safe || known != c.known {
			t.Errorf("%s: got safe=%v known=%v, want %v %v", c.name, safe, known, c.safe, c.known)
		}
	}
}

func TestPlanParsingAndRunDirValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("plan.md", "# plan\nM1\n")
	write("lanes/M1.L2.md", "# M1.L2: second\nOwns: docs/\nAfter: M1.L1\n\nbody\n")
	write("lanes/M1.L1.md", "# M1.L1: first\nOwns: src/, scripts/\n\nbody\n")
	p, err := ParsePlan(dir)
	if err != nil || len(p.Lanes) != 2 || p.Lanes[0].ID != "M1.L1" || p.Lanes[1].ID != "M1.L2" || len(p.Lanes[0].Owns) != 2 || p.Lanes[1].After[0] != "M1.L1" || p.Lanes[0].Title != "M1.L1: first" {
		t.Fatalf("%+v %v", p, err)
	}
	bad := func(files map[string]string, code string) {
		d := t.TempDir()
		for rel, c := range files {
			p := filepath.Join(d, rel)
			_ = os.MkdirAll(filepath.Dir(p), 0o700)
			_ = os.WriteFile(p, []byte(c), 0o600)
		}
		_ = os.MkdirAll(filepath.Join(d, "lanes"), 0o700)
		_, err := ParsePlan(d)
		var pe *PlanError
		if !errors.As(err, &pe) || pe.Code != code {
			t.Errorf("want %s, got %v", code, err)
		}
	}
	bad(map[string]string{}, "plan_missing")
	bad(map[string]string{"plan.md": "x"}, "no_lanes")
	bad(map[string]string{"plan.md": "x", "lanes/M2.L1.md": "# a\nOwns: a\n"}, "multiple_milestones")
	bad(map[string]string{"plan.md": "x", "lanes/foo.md": "# a\n"}, "bad_lane_id")
	bad(map[string]string{"plan.md": "x", "lanes/M1.L1.md": "# a\nAfter: M1.L2\n", "lanes/M1.L2.md": "# b\nAfter: M1.L1\n"}, "cycle")
	bad(map[string]string{"plan.md": "x", "lanes/M1.L1.md": "# a\nAfter: M1.L9\n"}, "bad_after")
	bad(map[string]string{"plan.md": "x", "lanes/M1.L1.md": "# a\n", "lanes/M1.L2.md": "# a\n", "lanes/M1.L3.md": "# a\n", "lanes/M1.L4.md": "# a\n"}, "too_many_lanes")

	// the run folder must lie inside the organization's data directory, symlinks resolved
	root := t.TempDir()
	inside := filepath.Join(root, "runs", "r1")
	_ = os.MkdirAll(inside, 0o700)
	if _, err := ValidateRunDir(root, inside); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if _, err := ValidateRunDir(root, outside); err == nil {
		t.Fatal("outside must be refused")
	}
	link := filepath.Join(root, "runs", "escape")
	_ = os.Symlink(outside, link)
	if _, err := ValidateRunDir(root, link); err == nil {
		t.Fatal("a symlink out must be refused")
	}
	// reading: escapes, symlinks and size
	_ = os.WriteFile(filepath.Join(inside, "reply.md"), []byte("hello"), 0o600)
	if b, err := ReadRunFile(inside, "reply.md", 100); err != nil || string(b) != "hello" {
		t.Fatal(err)
	}
	for _, rel := range []string{"../x", "/etc/passwd", "."} {
		if _, err := ReadRunFile(inside, rel, 100); err == nil {
			t.Errorf("%q must be refused", rel)
		}
	}
	_ = os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600)
	_ = os.Symlink(filepath.Join(outside, "secret"), filepath.Join(inside, "link"))
	if _, err := ReadRunFile(inside, "link", 100); err == nil {
		t.Fatal("a symlink out of the run folder must be refused")
	}
	if _, err := ReadRunFile(inside, "reply.md", 3); err == nil {
		t.Fatal("over the size cap")
	}
}
