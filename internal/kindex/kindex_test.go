package kindex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestHelperProcess re-executes the test binary as the fake `openclaw` CLI (the helper-process pattern).
func TestHelperProcess(t *testing.T) {
	if os.Getenv("KINDEX_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Exit(RunFakeCLI(args, os.Stdin, os.Stdout, os.Getenv("KINDEX_HELPER_STATE")))
}

type rig struct {
	data, bin, state string
	cli              *CLI
	vault            Vault
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	r := &rig{data: filepath.Join(dir, "data"), state: filepath.Join(dir, "fh", "wiki")}
	if err := os.MkdirAll(r.state, 0o700); err != nil {
		t.Fatal(err)
	}
	r.bin = filepath.Join(dir, "openclaw")
	self, _ := os.Executable()
	script := fmt.Sprintf("#!/bin/sh\necho $$ > %s/pid\nenv > %s/env.txt\nprintf '%%s\\n' \"$@\" >> %s/argv.txt\nKINDEX_HELPER=1 KINDEX_HELPER_STATE=%s exec %s -test.run='^TestHelperProcess$' -- \"$@\"\n",
		dir, dir, dir, r.state, self)
	if err := os.WriteFile(r.bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	r.cli = &CLI{Bin: r.bin, DataDir: r.data, ChildPath: "/usr/bin:/bin", Sandbox: SandboxBestEffort, Timeout: 20 * time.Second}
	r.vault = Vault{OrgID: "org_a", Dir: VaultDir(r.data, "org_a")}
	return r
}

func (r *rig) scenario(t *testing.T, s FakeScenario) {
	b, _ := json.Marshal(map[string]any{"wiki": s})
	if err := os.WriteFile(filepath.Join(r.state, "..", "scenario.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) projection(t *testing.T, docs ...DocIn) string {
	dir := filepath.Join(r.vault.Dir, "projection")
	_ = os.RemoveAll(dir)
	for _, d := range docs {
		if err := WriteAtomic(dir, DocRelPath(d.VersionID), RenderDoc(d, nil)); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

func doc(id, title, body string) DocIn {
	return DocIn{OrgID: "org_a", RecordID: "kno_" + id, VersionID: "kvr_" + id, Type: "project", ScopeKey: "project:prj_1", Title: title,
		Body: body, Claims: []string{"claim of " + id}, ContentHash: "hash-" + id, VerifiedAt: "2026-01-01T00:00:00Z"}
}

func TestCLIAllVerbsAndSchemas(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	h, err := r.cli.Health(ctx, r.vault)
	if err != nil || h.Schema != "v1" {
		t.Fatalf("%v %+v", err, h)
	}
	dir := r.projection(t, doc("1", "Payment gateway", "uses REST"), doc("2", "سرویس پرداخت", "از REST استفاده می‌کند"))
	ing, err := r.cli.Ingest(ctx, r.vault, dir)
	if err != nil || len(ing.Accepted) != 2 {
		t.Fatalf("%v %+v", err, ing)
	}
	idx := filepath.Join(r.vault.Dir, "scopes.md")
	_ = os.WriteFile(idx, []byte(RenderScopeIndex("org_a", "project:prj_1", []ScopeIndexEntry{{"kvr_1", "Payment gateway", "project"}}, nil)), 0o600)
	if err = r.cli.RegisterSynthesis(ctx, r.vault, idx); err != nil {
		t.Fatal(err)
	}
	c, err := r.cli.Compile(ctx, r.vault)
	if err != nil || c.Pages != 3 {
		t.Fatalf("%v %+v", err, c)
	}
	l, err := r.cli.Lint(ctx, r.vault)
	if err != nil || len(l.Errors) != 0 {
		t.Fatalf("%v %+v", err, l)
	}
	hits, err := r.cli.Search(ctx, r.vault, "payment REST", 5)
	if err != nil || len(hits) == 0 || hits[0].ID != "kvr_1" {
		t.Fatalf("%v %+v", err, hits)
	}
	fa, err := r.cli.Search(ctx, r.vault, "پرداخت", 5)
	if err != nil || len(fa) != 1 || fa[0].ID != "kvr_2" {
		t.Fatalf("persian search: %v %+v", err, fa)
	}
	s, err := r.cli.Get(ctx, r.vault, "kvr_1", 4096)
	if err != nil || s.ContentHash != "hash-1" || !strings.Contains(s.Text, "uses REST") {
		t.Fatalf("%v %+v", err, s)
	}
	// Removing a projection file removes the document on the next ingest.
	dir = r.projection(t, doc("1", "Payment gateway", "uses REST"))
	if _, err = r.cli.Ingest(ctx, r.vault, dir); err != nil {
		t.Fatal(err)
	}
	if _, err = r.cli.Get(ctx, r.vault, "kvr_2", 100); !IsKind(err, KindFailed) {
		t.Fatalf("deleted doc: %v", err)
	}
	// The argv allowlist: only these verbs ever reached the binary.
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(r.data), "argv.txt"))
	allowed := map[string]bool{"wiki": true, "version": true, "ingest": true, "synthesis": true, "register": true, "compile": true, "lint": true, "search": true, "get": true}
	verbs := 0
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "/") || line == "" {
			continue
		}
		if allowed[line] {
			verbs++
			continue
		}
		// values of flags (queries, ids, numbers)
		if regexp.MustCompile(`^(payment REST|پرداخت|kvr_\d|\d+|org_a)$`).MatchString(line) {
			continue
		}
		t.Errorf("unexpected argv element %q", line)
	}
	if verbs == 0 {
		t.Fatal("no verbs recorded")
	}
}

func TestSourceHasNoForbiddenVerbs(t *testing.T) {
	src, err := os.ReadFile("cli.go")
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`(?i)\b(cron|dream\w*|dispatch|serve|daemon|setup|login|install)\b`).FindString(string(src)); m != "" {
		t.Fatalf("cli.go mentions a verb outside the allowlist: %q", m)
	}
	if len(verbArgs) != 7 {
		t.Fatalf("allowlist has %d verbs", len(verbArgs))
	}
}

func TestExplicitEnvAndNoInheritance(t *testing.T) {
	r := newRig(t)
	t.Setenv("ORQEN_SECRET_SENTINEL", "sentinel-value-1234")
	t.Setenv("OPENAI_API_KEY", "sentinel-key-5678")
	if _, err := r.cli.Health(context.Background(), r.vault); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(r.data), "env.txt"))
	env := string(b)
	for _, bad := range []string{"sentinel-value-1234", "sentinel-key-5678", "SECRET", "API_KEY", "TOKEN"} {
		if strings.Contains(env, bad) {
			t.Fatalf("environment leaked %q:\n%s", bad, env)
		}
	}
	root := OrgRoot(r.data, "org_a")
	for _, want := range []string{"HOME=" + root + "/home", "OPENCLAW_HOME=" + root + "/state", "TMPDIR=" + root + "/tmp", "LANG=C.UTF-8"} {
		if !strings.Contains(env, want) {
			t.Errorf("missing %q in\n%s", want, env)
		}
	}
}

func TestVaultOutsideOrgRefused(t *testing.T) {
	r := newRig(t)
	other := t.TempDir()
	for _, v := range []Vault{
		{OrgID: "org_a", Dir: other},
		{OrgID: "org_a", Dir: OrgRoot(r.data, "org_b") + "/vault"},
		{OrgID: "org_a", Dir: OrgRoot(r.data, "org_a") + "/../org_b/vault"},
		{OrgID: "../org_b", Dir: OrgRoot(r.data, "org_a") + "/vault"},
		{OrgID: "org_a", Dir: "relative/dir"},
	} {
		if _, err := r.cli.Health(context.Background(), v); !IsKind(err, KindBadVault) {
			t.Errorf("%+v: %v", v, err)
		}
	}
	// A symlink out of the org directory is refused as well.
	link := filepath.Join(OrgRoot(r.data, "org_a"), "evil")
	_ = os.MkdirAll(OrgRoot(r.data, "org_a"), 0o700)
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if _, err := r.cli.Health(context.Background(), Vault{OrgID: "org_a", Dir: link}); !IsKind(err, KindBadVault) {
		t.Fatalf("symlink: %v", err)
	}
	// An input directory outside the org is refused for ingest.
	if _, err := r.cli.Ingest(context.Background(), r.vault, other); !IsKind(err, KindBadVault) {
		t.Fatalf("ingest input: %v", err)
	}
	// Not configured, relative bin.
	if _, err := (&CLI{}).Health(context.Background(), r.vault); !IsKind(err, KindNotConfigured) {
		t.Fatal(err)
	}
	if _, err := (&CLI{Bin: "openclaw", DataDir: r.data}).Health(context.Background(), r.vault); !IsKind(err, KindNotConfigured) {
		t.Fatal("no PATH lookup")
	}
}

func TestSchemaViolationAndFailures(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.scenario(t, FakeScenario{WrongSchema: true})
	if _, err := r.cli.Compile(ctx, r.vault); !IsKind(err, KindInvalidOutput) {
		t.Fatalf("wrong schema: %v", err)
	}
	r.scenario(t, FakeScenario{Down: true})
	if _, err := r.cli.Health(ctx, r.vault); !IsKind(err, KindFailed) {
		t.Fatalf("down: %v", err)
	}
	r.scenario(t, FakeScenario{FailVerb: "compile"})
	if _, err := r.cli.Health(ctx, r.vault); err != nil {
		t.Fatal("other verbs still work")
	}
	if _, err := r.cli.Compile(ctx, r.vault); !IsKind(err, KindFailed) {
		t.Fatal("fail_verb")
	}
	r.scenario(t, FakeScenario{LintErrors: 2})
	if l, err := r.cli.Lint(ctx, r.vault); err != nil || len(l.Errors) != 2 {
		t.Fatalf("%v %+v", err, l)
	}
	// A bin that does not exist is unavailable, not a panic.
	bad := *r.cli
	bad.Bin = filepath.Join(r.data, "missing")
	if _, err := bad.Health(ctx, r.vault); !IsKind(err, KindUnavailable) && !IsKind(err, KindFailed) {
		t.Fatalf("missing bin: %v", err)
	}
	// Not JSON.
	notJSON := filepath.Join(filepath.Dir(r.data), "plain")
	_ = os.WriteFile(notJSON, []byte("#!/bin/sh\necho hello\n"), 0o700)
	bad.Bin = notJSON
	if _, err := bad.Health(ctx, r.vault); !IsKind(err, KindInvalidOutput) {
		t.Fatalf("not json: %v", err)
	}
	// Oversize output.
	big := filepath.Join(filepath.Dir(r.data), "big")
	_ = os.WriteFile(big, []byte("#!/bin/sh\nyes aaaaaaaaaaaaaaaa | head -c 5000\n"), 0o700)
	bad.Bin, bad.MaxStdout = big, 1000
	if _, err := bad.Health(ctx, r.vault); !IsKind(err, KindInvalidOutput) {
		t.Fatalf("oversize: %v", err)
	}
}

func TestTimeoutKillsProcessGroup(t *testing.T) {
	r := newRig(t)
	r.scenario(t, FakeScenario{SlowMs: 30000})
	r.cli.Timeout = 700 * time.Millisecond
	start := time.Now()
	_, err := r.cli.Health(context.Background(), r.vault)
	if !IsKind(err, KindTimeout) || time.Since(start) > 10*time.Second {
		t.Fatalf("%v after %s", err, time.Since(start))
	}
	pidb, _ := os.ReadFile(filepath.Join(filepath.Dir(r.data), "pid"))
	var pid int
	fmt.Sscan(strings.TrimSpace(string(pidb)), &pid)
	if pid == 0 {
		t.Fatal("no pid recorded")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("kill", "-0", fmt.Sprint(pid)).Run() != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("process %d survived the timeout", pid)
}

func TestSandboxEnforceDeniesWritesOutside(t *testing.T) {
	if !SandboxAvailable() {
		t.Skip("sandbox-exec is not usable here (nested sandbox or non-macOS)")
	}
	r := newRig(t)
	r.cli.Sandbox = SandboxEnforce
	if _, err := r.cli.Health(context.Background(), r.vault); err != nil {
		t.Fatalf("enforced sandbox must still allow the vault: %v", err)
	}
	outside := filepath.Join(filepath.Dir(r.data), "outside.txt")
	script := filepath.Join(filepath.Dir(r.data), "writer")
	_ = os.WriteFile(script, []byte("#!/bin/sh\necho x > "+outside+"\necho '{\"version\":\"x\",\"schema\":\"v1\"}'\n"), 0o700)
	w := *r.cli
	w.Bin = script
	_, _ = w.Health(context.Background(), r.vault)
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("the sandbox allowed a write outside the org directory")
	}
	// A missing sandbox in enforce mode means unavailable.
}

func TestFakeIdempotentAndFailureInjection(t *testing.T) {
	r := newRig(t)
	f := NewFake()
	ctx := context.Background()
	dir := r.projection(t, doc("1", "A", "x"), doc("2", "B", "y"))
	if _, err := f.Ingest(ctx, r.vault, dir); err != nil {
		t.Fatal(err)
	}
	w := f.Writes(r.vault)
	if _, err := f.Ingest(ctx, r.vault, dir); err != nil || f.Writes(r.vault) != w {
		t.Fatalf("same (id, hash) must not be rewritten: %d vs %d", f.Writes(r.vault), w)
	}
	dir = r.projection(t, doc("1", "A", "x"), func() DocIn { d := doc("2", "B", "y2"); d.ContentHash = "hash-2b"; return d }())
	if _, err := f.Ingest(ctx, r.vault, dir); err != nil || f.Writes(r.vault) != w+1 {
		t.Fatalf("changed hash is one write: %d", f.Writes(r.vault))
	}
	f.Fail["compile"] = errf(KindFailed, "compile", "boom")
	if _, err := f.Compile(ctx, r.vault); !IsKind(err, KindFailed) {
		t.Fatal(err)
	}
	f.Down = true
	if _, err := f.Health(ctx, r.vault); !IsKind(err, KindUnavailable) {
		t.Fatal(err)
	}
	f.Down = false
	f.LintErrors = 1
	if l, _ := f.Lint(ctx, r.vault); len(l.Errors) != 1 {
		t.Fatal("lint errors")
	}
	if got := f.Docs(r.vault); len(got) != 2 {
		t.Fatalf("%v", got)
	}
}

func TestCoalesceAndBatches(t *testing.T) {
	es := []Entry{{1, OpUpsert, "v1", "r", "h1"}, {2, OpDelete, "v1", "r", "h1"}, {3, OpUpsert, "v1", "r", "h1"},
		{4, OpUpsert, "v2", "r", "h2"}, {5, OpDelete, "v2", "r", "h2"}, {6, OpUpsert, "v3", "r", "h3"}}
	got := Coalesce(es)
	if len(got) != 3 || got[0].VersionID != "v1" || got[0].Op != OpUpsert || got[1].Op != OpDelete || got[1].VersionID != "v2" || got[2].VersionID != "v3" {
		t.Fatalf("%+v", got)
	}
	if MaxSeq(es) != 6 || MaxSeq(nil) != 0 {
		t.Fatal("max seq")
	}
	var many []Entry
	for i := 0; i < 450; i++ {
		many = append(many, Entry{Seq: int64(i + 1), Op: OpUpsert, VersionID: fmt.Sprint("v", i)})
	}
	bs := Batches(Coalesce(many), 200)
	if len(bs) != 3 || len(bs[0]) != 200 || len(bs[2]) != 50 {
		t.Fatal(len(bs))
	}
}

func TestSchemaFilesMatchFakeShapes(t *testing.T) {
	r := newRig(t)
	for _, n := range SchemaNames {
		b, err := SchemaFile(n)
		if err != nil || !json.Valid(b) {
			t.Fatalf("schema %s: %v", n, err)
		}
	}
	dir := r.projection(t, doc("1", "A", "x"))
	_ = os.MkdirAll(r.vault.Dir, 0o700)
	idx := filepath.Join(r.vault.Dir, "s.md")
	_ = os.WriteFile(idx, []byte(RenderScopeIndex("org_a", "org", nil, nil)), 0o600)
	runs := map[string][]string{
		"version":   {"wiki", "version"},
		"ingest":    {"wiki", "ingest", "--input", dir},
		"synthesis": {"wiki", "synthesis", "register", "--input", idx},
		"compile":   {"wiki", "compile"},
		"lint":      {"wiki", "lint"},
		"search":    {"wiki", "search", "--query", "A", "--limit", "5"},
		"get":       {"wiki", "get", "--id", "kvr_1", "--max-bytes", "100"},
	}
	for _, name := range SchemaNames {
		var out strings.Builder
		args := append(append([]string{}, runs[name]...), "--vault", r.vault.Dir, "--json")
		if code := RunFakeCLI(args, nil, &out, r.state); code != 0 {
			t.Fatalf("%s exit %d", name, code)
		}
		if d := Diff(name, []byte(out.String())); d != "" {
			t.Errorf("%s: %s\n%s", name, d, out.String())
		}
	}
	// The validator rejects violations.
	for name, bad := range map[string]string{"version": `{"version":"x"}`, "compile": `{"pages":-1,"hash":"h"}`, "search": `{"hits":[{"id":1}]}`, "get": `{"id":"a","content_hash":"b","text":"c","extra":1}`, "lint": `[]`} {
		if Validate(name, []byte(bad)) == nil {
			t.Errorf("%s accepted %s", name, bad)
		}
	}
}

func TestProjectionScrubbedAndStable(t *testing.T) {
	d := doc("1", "Title glpat-abcdefghijklmnop", "password: hunter2 and SENTINELSECRET99")
	a := RenderDoc(d, []string{"SENTINELSECRET99"})
	if strings.Contains(a, "glpat-") || strings.Contains(a, "hunter2") || strings.Contains(a, "SENTINELSECRET99") {
		t.Fatalf("not scrubbed:\n%s", a)
	}
	if a != RenderDoc(d, []string{"SENTINELSECRET99"}) {
		t.Fatal("not deterministic")
	}
	meta, _ := parseFrontMatter([]byte(a))
	if meta["id"] != "kvr_1" || meta["content_hash"] != "hash-1" {
		t.Fatalf("%v", meta)
	}
	i1 := RenderScopeIndex("org_a", "org", []ScopeIndexEntry{{"b", "B", "project"}, {"a", "A", "project"}}, nil)
	i2 := RenderScopeIndex("org_a", "org", []ScopeIndexEntry{{"a", "A", "project"}, {"b", "B", "project"}}, nil)
	if i1 != i2 {
		t.Fatal("scope index must be order independent")
	}
}
