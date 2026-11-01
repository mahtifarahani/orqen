package hostfake

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
	"orqen/internal/codexhost"
)

func repo(t *testing.T) string {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@l", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	return dir
}

type harness struct {
	t    *testing.T
	cat  *Catherd
	app  *AppServer
	cl   *codexhost.Client
	th   codexhost.Thread
	repo string
}

func newHarness(t *testing.T, sc *Scenario) *harness {
	cat := NewCatherd(filepath.Join(t.TempDir(), "data"), sc)
	app := NewAppServer(cat)
	h := &harness{t: t, cat: cat, app: app, repo: repo(t)}
	h.connect()
	return h
}

func (h *harness) connect() {
	cl, err := codexhost.Dial(context.Background(), h.app.Pipe(), codexhost.ClientInfo{Name: "orqen", Version: "test"})
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = cl.Close() })
	h.cl = cl
}

func (h *harness) start() {
	th, err := h.cl.StartThread(context.Background(), codexhost.ThreadParams{})
	if err != nil {
		h.t.Fatal(err)
	}
	h.th = th
}

func (h *harness) do(tool string, args map[string]any) (map[string]any, error) {
	call, err := h.cl.RunDirective(context.Background(), h.th.ID, codexhost.NewDirective(tool, args), 3*time.Second)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if len(call.Result) > 0 {
		_ = json.Unmarshal(call.Result, &out)
	}
	return out, nil
}

func (h *harness) must(tool string, args map[string]any) map[string]any {
	out, err := h.do(tool, args)
	if err != nil {
		h.t.Fatalf("%s: %v", tool, err)
	}
	return out
}

func (h *harness) runStart() string {
	out := h.must("run_start", map[string]any{"repo": h.repo, "title": "ORQ-1: demo", "a_lines": []string{"the feature file exists"}})
	return out["run"].(string)
}

func (h *harness) view(run string) catherd.RunView {
	v, err := h.cat.RunsShow(context.Background(), nil, run)
	if err != nil {
		h.t.Fatal(err)
	}
	return v
}

func TestFullFlowThroughTheProtocol(t *testing.T) {
	h := newHarness(t, nil)
	h.start()
	if err := h.cat.ProfileUseRepo(context.Background(), nil, "codex", "o-acme-v1", h.repo); err != nil {
		t.Fatal(err)
	}
	if got := h.must("profile_get", map[string]any{"repo": h.repo}); got["here"] != "o-acme-v1" {
		t.Fatalf("%v", got)
	}
	run := h.runStart()
	rt := h.must("route", map[string]any{"run": run, "role": "architect"})
	if rt["rung"] != "codex:gpt-6.1-sol#high" {
		t.Fatalf("%v", rt)
	}
	d := h.must("dispatch", map[string]any{"run": run, "role": "architect", "name": "architect-M1", "brief": "plan it", "rung": rt["rung"]})
	adm := d["dispatched"].(map[string]any)
	if adm["name"] != "architect-M1" || adm["dispatchId"] == "" {
		t.Fatalf("%v", d)
	}
	// the record exists after the delay, and plan.md and the lanes are in the run folder
	v := h.view(run)
	rec, ok := v.RecordFor(adm["dispatchId"].(string))
	if !ok || rec.Status != "ok" || rec.Role != "architect" || rec.Thread == nil {
		t.Fatalf("%+v", v)
	}
	res := h.must("result", map[string]any{"run": run, "name": "architect-M1"})
	if res["state"] != "finished" || !strings.Contains(res["reply"].(string), "STATUS: complete") {
		t.Fatalf("%v", res)
	}
	runDir := h.must("run_start", map[string]any{"repo": h.repo, "title": "second", "a_lines": []string{"x"}})["dir"].(string)
	if _, err := os.Stat(filepath.Join(runDir, "lanes")); err != nil {
		t.Fatal(err)
	}
	plan, err := os.ReadFile(filepath.Join(h.cat.Root, "runs", run, "plan.md"))
	if err != nil || !strings.Contains(string(plan), "M1") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.cat.Root, "runs", run, "lanes", "M1.L1.md")); err != nil {
		t.Fatal(err)
	}
	// worker: edits land in the worktree, the fix round resumes the same thread
	w := h.must("dispatch", map[string]any{"run": run, "role": "worker", "name": "worker-M1.L1", "brief": "do it", "lane": "M1.L1"})
	if w["dispatched"].(map[string]any)["rung"] != "codex:gpt-6-sol#medium" {
		t.Fatalf("%v", w)
	}
	v = h.view(run)
	wr, _ := v.RecordFor(w["dispatched"].(map[string]any)["dispatchId"].(string))
	if b, err := os.ReadFile(filepath.Join(h.repo, "src", "feature.txt")); err != nil || string(b) != "orqen-feature\n" {
		t.Fatal(string(b), err)
	}
	if wr.ChangedOwned[0] != "src/feature.txt" || wr.ReplyStatus == nil || *wr.ReplyStatus != "complete" || wr.Thread == nil {
		t.Fatalf("%+v", wr)
	}
	fix := h.must("dispatch", map[string]any{"run": run, "role": "worker", "name": "worker-M1.L1", "brief": "fix", "lane": "M1.L1", "thread": *wr.Thread})
	fr, _ := h.view(run).RecordFor(fix["dispatched"].(map[string]any)["dispatchId"].(string))
	if fr.Thread == nil || *fr.Thread != *wr.Thread || fr.Attempt != 2 {
		t.Fatalf("%+v", fr)
	}
	if _, err := h.do("dispatch", map[string]any{"run": run, "role": "worker", "name": "worker-M1.L1", "brief": "fix", "lane": "M1.L1", "thread": "nope"}); err == nil {
		t.Fatal("a thread the name never ran on is refused")
	} else {
		var te *codexhost.ToolError
		if !errors.As(err, &te) || te.Code != "E_ADMIT_THREAD" {
			t.Fatal(err)
		}
	}
	// land is refused until a reviewer record and a passing verifier exist
	land := map[string]any{"run": run, "milestone": "M1", "what": "feature", "commit": "abcdef1", "evidence": "tests", "next": "merge"}
	var te *codexhost.ToolError
	if _, err = h.do("land", land); !errors.As(err, &te) || te.Code != "E_LAND_GATE" {
		t.Fatal(err)
	}
	h.must("dispatch", map[string]any{"run": run, "role": "verifier", "name": "verifier-M1", "brief": "verify", "rung": "codex:gpt-6-luna#medium"})
	h.must("dispatch", map[string]any{"run": run, "role": "reviewer", "name": "reviewer-M1-r1", "brief": "review", "rung": "codex:gpt-6.1-sol#high"})
	h.view(run)
	out := h.must("land", land)
	if out["digest"] == nil {
		t.Fatalf("%v", out)
	}
	if _, err = h.do("land", land); !errors.As(err, &te) || te.Code != "E_LAND_GATE" {
		t.Fatal("a duplicate land is refused", err)
	}
	if v = h.view(run); len(v.Landed) != 1 {
		t.Fatalf("%+v", v.Landed)
	}
	if len(h.cat.CallsOf("dispatch")) != 6 {
		t.Fatalf("dispatch calls: %d", len(h.cat.CallsOf("dispatch")))
	}
	// every directive carried the Codex thread id as _meta
	for _, c := range h.cat.Log() {
		if c.Thread != h.th.ID {
			t.Fatalf("%+v", c)
		}
	}
}

func TestScenarioKnobs(t *testing.T) {
	sc := DefaultScenario()
	sc.LieRounds = []int{0}
	sc.VerifierVerdicts = []string{"FAIL", "PASS"}
	sc.Reviews = []ReviewSpec{{Verdict: "changes_requested", Findings: []FindingSpec{{Severity: "major", File: "src/feature.txt", Line: 1, Title: "t", Body: "b"}}}, {Verdict: "approved"}}
	sc.MalformedReviews = 0
	sc.RoleStatuses = map[string][]string{"worker-M1.L1": {"limit"}}
	sc.FailoverTo = map[string]string{"worker": "codex:gpt-6.1-sol#medium"}
	sc.RecordDelayMs = 0
	h := newHarness(t, sc)
	h.start()
	run := h.runStart()
	w := h.must("dispatch", map[string]any{"run": run, "role": "worker", "name": "worker-M1.L1", "brief": "x", "lane": "M1.L1"})
	rec, _ := h.view(run).RecordFor(w["dispatched"].(map[string]any)["dispatchId"].(string))
	if rec.Status != "limit" || rec.Rung != "codex:gpt-6.1-sol#medium" || rec.FailoverFrom == nil || *rec.FailoverFrom != "codex:gpt-6-sol#medium" {
		t.Fatalf("%+v", rec)
	}
	if _, err := os.Stat(filepath.Join(h.repo, "src", "feature.txt")); err == nil {
		t.Fatal("a limit record applies no edits")
	}
	w2 := h.must("dispatch", map[string]any{"run": run, "role": "worker", "name": "worker-M1.L1", "brief": "x", "lane": "M1.L1", "thread": "latest"})
	rec2, _ := h.view(run).RecordFor(w2["dispatched"].(map[string]any)["dispatchId"].(string))
	if b, _ := os.ReadFile(filepath.Join(h.repo, "src", "feature.txt")); string(b) != "orqen-feature\n" && rec2.Status != "ok" {
		t.Fatalf("%s %+v", b, rec2)
	}
	v1 := h.must("dispatch", map[string]any{"run": run, "role": "verifier", "name": "verifier-M1", "brief": "v", "rung": sc.Rungs["verifier"]})
	vr, _ := h.view(run).RecordFor(v1["dispatched"].(map[string]any)["dispatchId"].(string))
	first := replyFirst(t, h, run, vr)
	if !strings.HasPrefix(first, "VERDICT: FAIL") {
		t.Fatal(first)
	}
	r1 := h.must("dispatch", map[string]any{"run": run, "role": "reviewer", "name": "reviewer-M1-r1", "brief": "r", "rung": sc.Rungs["reviewer"]})
	rr, _ := h.view(run).RecordFor(r1["dispatched"].(map[string]any)["dispatchId"].(string))
	b, _ := os.ReadFile(filepath.Join(h.cat.Root, "runs", run, rr.ReplyPath))
	if !strings.Contains(string(b), "ORQEN-REVIEW v1\n{") || !strings.Contains(string(b), `"changes_requested"`) {
		t.Fatal(string(b))
	}
}

func replyFirst(t *testing.T, h *harness, run string, rec catherd.RunRecord) string {
	b, err := os.ReadFile(filepath.Join(h.cat.Root, "runs", run, rec.ReplyPath))
	if err != nil {
		t.Fatal(err)
	}
	return strings.SplitN(string(b), "\n", 2)[0]
}

func TestAdmitErrorsBudgetAndDuplicate(t *testing.T) {
	sc := DefaultScenario()
	sc.RecordDelayMs = 60000 // roles stay live
	sc.Budget = 2
	sc.AdmitErrors = map[string][]ErrSpec{"dispatch": {{Code: "E_ADMIT_RUNG", Message: "rung refused", Fix: "pick another"}}}
	h := newHarness(t, sc)
	h.start()
	run := h.runStart()
	args := func(name string) map[string]any {
		return map[string]any{"run": run, "role": "worker", "name": name, "brief": "x", "rung": "codex:gpt-6-sol#low"}
	}
	var te *codexhost.ToolError
	if _, err := h.do("dispatch", args("a")); !errors.As(err, &te) || te.Code != "E_ADMIT_RUNG" || te.Fix != "pick another" {
		t.Fatal(err)
	}
	h.must("dispatch", args("a"))
	if _, err := h.do("dispatch", args("a")); !errors.As(err, &te) || te.Code != "E_ADMIT_DUPLICATE" {
		t.Fatal(err)
	}
	h.must("dispatch", args("b"))
	if _, err := h.do("dispatch", args("c")); !errors.As(err, &te) || te.Code != "E_RUN_BUDGET" {
		t.Fatal(err)
	}
	// RunsCancel records the live role cancelled
	if err := h.cat.RunsCancel(context.Background(), nil, run, "a"); err != nil {
		t.Fatal(err)
	}
	v := h.view(run)
	if len(v.Records) != 1 || v.Records[0].Status != "cancelled" || v.Records[0].Name != "a" {
		t.Fatalf("%+v", v.Records)
	}
}

func TestInjectionModes(t *testing.T) {
	sc := DefaultScenario()
	sc.RecordDelayMs = 60000
	sc.Inject = map[string][]string{"dispatch": {"no_tool_call", "wrong_args", "extra_command", "hang", "crash_after_admit", "tool_error:E_BACKEND_DOWN", ""}}
	h := newHarness(t, sc)
	h.start()
	run := h.runStart()
	args := map[string]any{"run": run, "role": "worker", "name": "w", "brief": "x", "rung": "codex:gpt-6-sol#low"}
	try := func() error { _, err := h.do("dispatch", args); return err }
	if err := try(); !errors.Is(err, codexhost.ErrNoToolCall) {
		t.Fatal("no_tool_call", err)
	}
	if err := try(); !errors.Is(err, codexhost.ErrArgsMismatch) {
		t.Fatal("wrong_args", err)
	}
	if err := try(); !errors.Is(err, codexhost.ErrDeviation) {
		t.Fatal("extra_command", err)
	}
	if len(h.cat.CallsOf("dispatch")) != 0 {
		t.Fatal("nothing was admitted so far")
	}
	if err := h.tryWithTimeout(args, 200*time.Millisecond); !errors.Is(err, codexhost.ErrOutcomeUnknown) {
		t.Fatal("hang", err)
	}
	if len(h.cat.CallsOf("dispatch")) != 0 {
		t.Fatal("hang must not admit")
	}
	// crash_after_admit: the dispatch happened, the connection died, the history shows the completed call
	if err := try(); !errors.Is(err, codexhost.ErrOutcomeUnknown) {
		t.Fatal("crash", err)
	}
	if len(h.cat.CallsOf("dispatch")) != 1 || len(h.view(run).Dispatches) != 1 {
		t.Fatal("exactly one dispatch")
	}
	h.connect() // a restarted app-server over the same state
	th, err := h.cl.ResumeThread(context.Background(), h.th.ID)
	if err != nil || len(th.Turns) == 0 {
		t.Fatal(th, err)
	}
	last := th.Turns[len(th.Turns)-1]
	if len(codexhost.ToolCallsOf(last)) != 1 || h.app.Resumes.Load() != 1 {
		t.Fatalf("%+v", last)
	}
	var te *codexhost.ToolError
	args["name"] = "w2"
	if err = try(); !errors.As(err, &te) || te.Code != "E_BACKEND_DOWN" {
		t.Fatal("tool_error", err)
	}
	args["name"] = "w3"
	if err = try(); err != nil {
		t.Fatal(err)
	}
	sc2 := h.cat
	sc2.SetScenario(func(s *Scenario) { s.UnreadableHistory = true })
	if _, err = h.cl.ResumeThread(context.Background(), h.th.ID); err == nil {
		t.Fatal("unreadable history")
	}
}

func (h *harness) tryWithTimeout(args map[string]any, d time.Duration) error {
	_, err := h.cl.RunDirective(context.Background(), h.th.ID, codexhost.NewDirective("dispatch", args), d)
	return err
}

func TestProbeReportsCatherdTools(t *testing.T) {
	h := newHarness(t, nil)
	res, err := h.cl.Probe(context.Background())
	if err != nil || !res.LoggedIn || res.Version() != "0.160.0" || len(res.MCPServers) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	have := strings.Join(res.MCPServers[0].Tools, ",")
	for _, tool := range []string{"run_start", "dispatch", "result", "route", "cancel", "land", "profile_get"} {
		if !strings.Contains(have, tool) {
			t.Fatalf("%s missing from %s", tool, have)
		}
	}
	h.app.NotLoggedIn, h.app.NoCatherd = true, true
	res, _ = h.cl.Probe(context.Background())
	if res.LoggedIn || len(res.MCPServers) != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestFileBackedStateSurvivesRestarts(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "data")
	state := filepath.Join(dir, "state")
	_ = os.MkdirAll(state, 0o700)
	_ = os.WriteFile(filepath.Join(state, "scenario.json"), []byte(`{"record_delay_ms":0,"lanes":[{"id":"M1.L1","title":"x","owns":["src/"]},{"id":"M1.L2","title":"y","owns":["docs/"],"after":["M1.L1"]}]}`), 0o600)
	rp := repo(t)
	call := func(tool string, a map[string]any) map[string]any {
		c := NewFileCatherd(root, state) // a new process each time
		out, e := c.Call(tool, a, "thr")
		if e != nil {
			t.Fatal(e)
		}
		b, _ := json.Marshal(out)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	run := call("run_start", map[string]any{"repo": rp, "title": "t", "a_lines": []any{"a"}})["run"].(string)
	call("dispatch", map[string]any{"run": run, "role": "architect", "name": "architect-M1", "brief": "b", "rung": "codex:gpt-6.1-sol#high"})
	v, err := NewFileCatherd(root, state).RunsShow(context.Background(), nil, run)
	if err != nil || len(v.Records) != 1 {
		t.Fatal(v, err)
	}
	if _, err = os.Stat(filepath.Join(root, "runs", run, "lanes", "M1.L2.md")); err != nil {
		t.Fatal("the scenario file shaped the plan", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "runs", run, "lanes", "M1.L2.md")); !strings.Contains(string(b), "After: M1.L1") {
		t.Fatal(string(b))
	}
}

func TestWorkerPushAttemptIsBlockedByTheHostEnv(t *testing.T) {
	// The attempt runs with the environment the test provides; here a push to a missing remote fails and is recorded
	// by the absence of any effect. WP56 repeats this against testfakes.GitRepos with the real host environment.
	sc := DefaultScenario()
	sc.WorkerPushAttempt = true
	sc.PushEnv = func() []string {
		return []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir()}
	}
	h := newHarness(t, sc)
	h.start()
	run := h.runStart()
	h.must("dispatch", map[string]any{"run": run, "role": "worker", "name": "w", "brief": "x", "rung": "codex:gpt-6-sol#low"})
	if v := h.view(run); len(v.Records) != 1 || v.Records[0].Status != "ok" {
		t.Fatalf("%+v", v)
	}
}
