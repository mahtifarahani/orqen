package hostfake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"orqen/internal/catherd"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
var roles = map[string]bool{"architect": true, "worker": true, "verifier": true, "reviewer": true, "researcher": true, "writer": true}

// ToolErr is a catherd tool error {code, message, fix}.
type ToolErr struct{ Code, Message, Fix string }

func (e *ToolErr) Error() string { return e.Code + ": " + e.Message }

func terr(code, msg, fix string) *ToolErr { return &ToolErr{code, msg, fix} }

// CallRecord is one tool call the fake served.
type CallRecord struct {
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
	Thread string         `json:"thread"`
	Err    string         `json:"err"`
	At     time.Time      `json:"at"`
}

// Disp is one admitted dispatch.
type Disp struct {
	Name       string    `json:"name"`
	Role       string    `json:"role"`
	Rung       string    `json:"rung"` // as admitted
	DispatchID string    `json:"dispatch_id"`
	AdmittedAt time.Time `json:"admitted_at"`
	Thread     string    `json:"thread"`
	Lane       string    `json:"lane"`
	Brief      string    `json:"brief"`
	Index      int       `json:"index"` // among dispatches of the same name (worker) or role (others)
	NameIndex  int       `json:"name_index"`
	Finalized  bool      `json:"finalized"`
	Collected  bool      `json:"collected"`
}

// Run is one catherd run.
type Run struct {
	ID      string              `json:"id"`
	Repo    string              `json:"repo"`
	Title   string              `json:"title"`
	ALines  []string            `json:"a_lines"`
	Dir     string              `json:"dir"`
	Disps   []*Disp             `json:"disps"`
	Records []catherd.RunRecord `json:"records"`
	Climbs  map[string]int      `json:"climbs"`
	Landed  []LandRow           `json:"landed"`
}

// LandRow is one landed milestone.
type LandRow struct{ Milestone, Commit, Evidence string }

// State is everything the fake keeps. In file-backed mode it is one JSON document.
type State struct {
	N      int               `json:"n"`
	Runs   map[string]*Run   `json:"runs"`
	Bound  map[string]string `json:"bound"`
	Log    []CallRecord      `json:"log"`
	ErrSeq map[string]int    `json:"err_seq"`
	InjSeq map[string]int    `json:"inj_seq"`
	// Threads are the app-server's threads and their history (kept here so the file-backed variant survives restarts).
	Threads map[string]*Thread `json:"threads"`
}

// Thread is a fake Codex thread.
type Thread struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Turns []Turn `json:"turns"`
}

// Turn is one finished or interrupted turn in a thread's history.
type Turn struct {
	ID     string           `json:"id"`
	Status string           `json:"status"`
	Items  []map[string]any `json:"items"`
}

// Catherd is the fake catherd run engine. It implements catherd.RunsPort.
type Catherd struct {
	// Root is where run folders live (inside the organization's catherd home, so Orqen's path check passes).
	Root string
	// StateDir switches on the file-backed variant used by cmd/orqen-fakehost ("" = in memory).
	StateDir string
	// Now is the clock (tests inject a fake one).
	Now func() time.Time

	mu sync.Mutex
	st *State
	sc *Scenario
}

// NewCatherd makes an in-memory fake with the given scenario (nil = default).
func NewCatherd(root string, sc *Scenario) *Catherd {
	if sc == nil {
		sc = DefaultScenario()
	}
	sc.norm()
	return &Catherd{Root: root, sc: sc, st: newState()}
}

// NewFileCatherd makes the file-backed variant: state in stateDir/state.json, scenario in stateDir/scenario.json.
func NewFileCatherd(root, stateDir string) *Catherd {
	return &Catherd{Root: root, StateDir: stateDir}
}

func newState() *State {
	return &State{Runs: map[string]*Run{}, Bound: map[string]string{}, ErrSeq: map[string]int{}, InjSeq: map[string]int{}, Threads: map[string]*Thread{}}
}

func (c *Catherd) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

// SetScenario changes the scenario under the lock (in-memory variant).
func (c *Catherd) SetScenario(f func(*Scenario)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sc == nil {
		c.sc = DefaultScenario()
	}
	f(c.sc)
	c.sc.norm()
}

func (c *Catherd) txn(f func(st *State, sc *Scenario, now time.Time) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, sc := c.st, c.sc
	if c.StateDir != "" {
		if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
			return err
		}
		lf, err := os.OpenFile(filepath.Join(c.StateDir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		defer lf.Close()
		if err = syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
			return err
		}
		defer func() { _ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) }()
		st = newState()
		if b, e := os.ReadFile(filepath.Join(c.StateDir, "state.json")); e == nil {
			if err = json.Unmarshal(b, st); err != nil {
				return err
			}
			if st.Runs == nil {
				st.Runs = map[string]*Run{}
			}
			for _, m := range []*map[string]int{&st.ErrSeq, &st.InjSeq} {
				if *m == nil {
					*m = map[string]int{}
				}
			}
			if st.Bound == nil {
				st.Bound = map[string]string{}
			}
			if st.Threads == nil {
				st.Threads = map[string]*Thread{}
			}
		}
		if sc, err = LoadScenario(filepath.Join(c.StateDir, "scenario.json")); err != nil {
			return err
		}
	}
	now := c.now()
	c.materialize(st, sc, now)
	err := f(st, sc, now)
	if c.StateDir != "" {
		b, e := json.MarshalIndent(st, "", " ")
		if e != nil {
			return e
		}
		tmp := filepath.Join(c.StateDir, "state.json.tmp")
		if e = os.WriteFile(tmp, b, 0o600); e != nil {
			return e
		}
		if e = os.Rename(tmp, filepath.Join(c.StateDir, "state.json")); e != nil {
			return e
		}
	}
	return err
}

// ---------------------------------------------------------------- tools

func str(a map[string]any, k string) string { s, _ := a[k].(string); return s }

// Call executes one catherd MCP tool as the orchestrator. thread is the Codex thread id (_meta.threadId).
func (c *Catherd) Call(tool string, args map[string]any, thread string) (any, *ToolErr) {
	var out any
	var terror *ToolErr
	err := c.txn(func(st *State, sc *Scenario, now time.Time) error {
		out, terror = c.dispatchTool(st, sc, now, tool, args, thread)
		rec := CallRecord{Tool: tool, Args: args, Thread: thread, At: now}
		if terror != nil {
			rec.Err = terror.Code
		}
		st.Log = append(st.Log, rec)
		return nil
	})
	if err != nil {
		return nil, terr("E_IO_UNEXPECTED", err.Error(), "report it")
	}
	return out, terror
}

func (c *Catherd) dispatchTool(st *State, sc *Scenario, now time.Time, tool string, a map[string]any, thread string) (any, *ToolErr) {
	if seq, ok := sc.AdmitErrors[tool]; ok {
		n := st.ErrSeq[tool]
		st.ErrSeq[tool] = n + 1
		if n < len(seq) && seq[n].Code != "" {
			return nil, terr(seq[n].Code, seq[n].Message, seq[n].Fix)
		}
	}
	switch tool {
	case "run_start":
		return c.runStart(st, now, a)
	case "profile_get":
		repo := str(a, "repo")
		here := st.Bound[repo]
		if here == "" {
			here = "default"
		}
		return map[string]any{"here": here, "active": "default", "names": []string{"default", here}}, nil
	case "write_run_file", "read_run_file":
		return c.runFile(st, tool, a)
	case "route":
		return c.route(st, sc, a)
	case "dispatch":
		return c.dispatch(st, sc, now, a, thread)
	case "result":
		return c.result(st, a)
	case "climb":
		return c.climb(st, sc, a)
	case "land":
		return c.land(st, a)
	case "status", "peek":
		return map[string]any{"version": "1.5.0"}, nil
	case "cancel":
		if err := c.cancelLocked(st, now, str(a, "run"), str(a, "name")); err != nil {
			return nil, err
		}
		return map[string]any{"cancelled": str(a, "name")}, nil
	}
	return nil, terr("E_INPUT_INVALID", "unknown tool "+tool, "call a tool the server lists")
}

func (c *Catherd) run(st *State, id string) (*Run, *ToolErr) {
	r := st.Runs[id]
	if r == nil {
		return nil, terr("E_RUN_NOT_FOUND", "no run "+id, "status lists the runs")
	}
	return r, nil
}

func (c *Catherd) runStart(st *State, now time.Time, a map[string]any) (any, *ToolErr) {
	repo, title := str(a, "repo"), str(a, "title")
	lines, _ := a["a_lines"].([]any)
	if fi, err := os.Stat(repo); repo == "" || err != nil || !fi.IsDir() {
		return nil, terr("E_IO_PATH", repo+" is not inside a git repository", "pass the path of the repository to work in")
	}
	if title == "" || len(lines) == 0 {
		return nil, terr("E_INPUT_INVALID", "title and a_lines are required", "pass both")
	}
	st.N++
	id := fmt.Sprintf("r-%04d", st.N)
	dir := filepath.Join(c.Root, "runs", id)
	if err := os.MkdirAll(filepath.Join(dir, "lanes"), 0o700); err != nil {
		return nil, terr("E_IO_UNEXPECTED", err.Error(), "")
	}
	r := &Run{ID: id, Repo: repo, Title: title, Dir: dir, Climbs: map[string]int{}}
	for _, l := range lines {
		r.ALines = append(r.ALines, fmt.Sprint(l))
	}
	st.Runs[id] = r
	_ = os.WriteFile(filepath.Join(dir, "state.md"), []byte("# "+title+"\n"), 0o600)
	return map[string]any{"run": id, "dir": dir, "protocol": map[string]any{"next": "architect", "checklist": []string{"plan", "work", "verify", "review", "commit", "land"}}}, nil
}

var protectedRunFiles = map[string]bool{"state.md": true, "runs.jsonl": true, "routes.jsonl": true, "budget.json": true}

func (c *Catherd) runFile(st *State, tool string, a map[string]any) (any, *ToolErr) {
	r, e := c.run(st, str(a, "run"))
	if e != nil {
		return nil, e
	}
	rel := filepath.Clean(str(a, "path"))
	if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") || protectedRunFiles[rel] || strings.HasPrefix(rel, "roles/") {
		return nil, terr("E_IO_PATH", "path "+str(a, "path")+" is not allowed", "use a path inside the run folder")
	}
	full := filepath.Join(r.Dir, rel)
	if tool == "read_run_file" {
		b, err := os.ReadFile(full)
		if err != nil {
			return nil, terr("E_IO_PATH", "no file "+rel, "")
		}
		return string(b), nil
	}
	content := str(a, "content")
	if m := regexp.MustCompile(`^lanes/[^/]+\.md$`).MatchString(rel); m {
		for _, line := range strings.Split(content, "\n") {
			if v, ok := strings.CutPrefix(line, "Kind:"); ok && !map[string]bool{"repo_code": true, "terminal": true, "ui": true, "prose": true, "research": true}[strings.TrimSpace(v)] {
				return nil, terr("E_LANE_INVALID", "unknown Kind "+strings.TrimSpace(v), "")
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return nil, terr("E_IO_UNEXPECTED", err.Error(), "")
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		return nil, terr("E_IO_UNEXPECTED", err.Error(), "")
	}
	return map[string]any{"path": rel, "bytes": len(content)}, nil
}

func (c *Catherd) laneRung(sc *Scenario, r *Run, lane string) string {
	i := r.Climbs[lane]
	if i >= len(sc.WorkerLadder) {
		i = len(sc.WorkerLadder) - 1
	}
	return sc.WorkerLadder[i]
}

func (c *Catherd) route(st *State, sc *Scenario, a map[string]any) (any, *ToolErr) {
	r, e := c.run(st, str(a, "run"))
	if e != nil {
		return nil, e
	}
	role := str(a, "role")
	if role == "" {
		role = "worker"
	}
	rung := sc.Rungs[role]
	ladder := []string{rung}
	if role == "worker" {
		lane := strings.TrimSuffix(filepath.Base(str(a, "lane_file")), ".md")
		rung, ladder = c.laneRung(sc, r, lane), sc.WorkerLadder
	}
	p, _ := catherd.ParseRung(rung)
	return map[string]any{"rung": rung, "ladder": ladder, "backend": p.Backend, "agent": nil, "why": "scenario"}, nil
}

func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func threadID(n int) string { return fmt.Sprintf("%08x-0000-4000-8000-%012x", n, n) }

func (c *Catherd) dispatch(st *State, sc *Scenario, now time.Time, a map[string]any, hostThread string) (any, *ToolErr) {
	r, e := c.run(st, str(a, "run"))
	if e != nil {
		return nil, e
	}
	role, name, brief := str(a, "role"), str(a, "name"), str(a, "brief")
	if !roles[role] || !namePattern.MatchString(name) || brief == "" {
		return nil, terr("E_INPUT_INVALID", "role, name and brief are required", "correct the arguments")
	}
	for _, d := range r.Disps {
		if d.Name == name && !d.Finalized {
			return nil, terr("E_ADMIT_DUPLICATE", name+" is still running", "wait for it or cancel it")
		}
	}
	if sc.Budget > 0 && len(r.Disps) >= sc.Budget {
		return nil, terr("E_RUN_BUDGET", "the run's budget is used", "raise the budget in the profile")
	}
	lane := str(a, "lane")
	rung := str(a, "rung")
	if rung == "" {
		if lane == "" {
			return nil, terr("E_INPUT_INVALID", "rung is required without a lane", "route(run, role) gives it")
		}
		rung = c.laneRung(sc, r, lane)
	}
	if _, ok := catherd.ParseRung(rung); !ok {
		return nil, terr("E_ADMIT_RUNG", "bad rung "+rung, "use backend:model#effort")
	}
	var prior []*Disp
	idx, nameIdx := 0, 0
	for _, d := range r.Disps {
		if d.Name == name {
			prior = append(prior, d)
			nameIdx++
		}
		if role == "worker" && d.Name == name || role != "worker" && d.Role == role {
			idx++
		}
	}
	thr := ""
	if want := str(a, "thread"); want != "" {
		for _, d := range prior {
			if want == "latest" || d.Thread == want {
				thr = d.Thread
			}
		}
		if want == "latest" && len(prior) > 0 {
			thr = prior[len(prior)-1].Thread
		}
		if thr == "" {
			return nil, terr("E_ADMIT_THREAD", name+" never ran on thread "+want, "resume a thread this name ran on")
		}
	} else {
		st.N++
		thr = threadID(st.N)
	}
	st.N++
	d := &Disp{Name: name, Role: role, Rung: rung, DispatchID: fmt.Sprintf("d-%04d", st.N), AdmittedAt: now, Thread: thr, Lane: lane, Brief: brief, Index: idx, NameIndex: nameIdx}
	r.Disps = append(r.Disps, d)
	return map[string]any{"dispatched": map[string]any{"name": name, "role": role, "rung": rung, "dispatchId": d.DispatchID, "admittedAt": ts(now)}, "hints": []string{}}, nil
}

func (r *Run) record(dispatchID string) *catherd.RunRecord {
	for i := range r.Records {
		if r.Records[i].DispatchID == dispatchID {
			return &r.Records[i]
		}
	}
	return nil
}

func (r *Run) latest(name string) *Disp {
	var out *Disp
	for _, d := range r.Disps {
		if d.Name == name {
			out = d
		}
	}
	return out
}

func (c *Catherd) result(st *State, a map[string]any) (any, *ToolErr) {
	r, e := c.run(st, str(a, "run"))
	if e != nil {
		return nil, e
	}
	d := r.latest(str(a, "name"))
	if d == nil {
		return nil, terr("E_RUN_NOT_FOUND", str(a, "name")+" was never dispatched in this run", "status(run) lists the roles")
	}
	rec := r.record(d.DispatchID)
	out := map[string]any{"name": d.Name, "state": "running", "record": nil, "reply": "", "replyPath": nil, "hints": []string{}}
	if rec != nil {
		d.Collected = true
		out["state"], out["record"], out["replyPath"] = "finished", rec, rec.ReplyPath
		if b, err := os.ReadFile(filepath.Join(r.Dir, rec.ReplyPath)); err == nil {
			s := string(b)
			if len(s) > 20000 {
				s = s[:20000] + "\n[reply capped; read the full file at replyPath]"
			}
			out["reply"] = s
		}
	}
	return out, nil
}

func (c *Catherd) climb(st *State, sc *Scenario, a map[string]any) (any, *ToolErr) {
	r, e := c.run(st, str(a, "run"))
	if e != nil {
		return nil, e
	}
	lane := str(a, "lane")
	if r.Climbs[lane] >= len(sc.WorkerLadder)-1 {
		return map[string]any{"rung": nil, "top": true, "hints": []string{}}, nil // already on the top rung
	}
	r.Climbs[lane]++
	return map[string]any{"rung": sc.WorkerLadder[r.Climbs[lane]], "top": false, "hints": []string{}}, nil
}

func replyFirstLine(r *Run, rec *catherd.RunRecord) string {
	b, err := os.ReadFile(filepath.Join(r.Dir, rec.ReplyPath))
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

func (c *Catherd) land(st *State, a map[string]any) (any, *ToolErr) {
	r, e := c.run(st, str(a, "run"))
	if e != nil {
		return nil, e
	}
	m, commit := str(a, "milestone"), str(a, "commit")
	if !commitPattern.MatchString(commit) || str(a, "what") == "" || str(a, "evidence") == "" || str(a, "next") == "" {
		return nil, terr("E_INPUT_INVALID", "milestone, what, commit, evidence and next are required", "")
	}
	for _, l := range r.Landed {
		if l.Milestone == m && l.Commit == commit {
			return nil, terr("E_LAND_GATE", "land "+m+": already landed at "+commit, "")
		}
	}
	var reviewer, verifier *catherd.RunRecord
	for i := range r.Records {
		rec := &r.Records[i]
		if rec.Role == "reviewer" && strings.HasPrefix(rec.Name, "reviewer-"+m) && rec.Status == "ok" && (rec.ReplyStatus == nil || *rec.ReplyStatus != "partial") {
			reviewer = rec
		}
		if rec.Role == "verifier" && rec.Name == "verifier-"+m {
			verifier = rec
		}
	}
	var missing []string
	if reviewer == nil {
		missing = append(missing, "a reviewer record (a dispatch named reviewer-"+m+")")
	}
	if verifier == nil || verifier.Status != "ok" || !strings.HasPrefix(replyFirstLine(r, verifier), "VERDICT: PASS") {
		missing = append(missing, "a verifier verdict (VERDICT: PASS under exactly verifier-"+m+")")
	}
	if len(missing) > 0 {
		return nil, terr("E_LAND_GATE", "land "+m+": missing "+strings.Join(missing, " and "), "run the reviewer and the verifier on "+m)
	}
	r.Landed = append(r.Landed, LandRow{m, commit, str(a, "evidence")})
	digest := "digests/" + m + ".md"
	_ = os.MkdirAll(filepath.Join(r.Dir, "digests"), 0o700)
	_ = os.WriteFile(filepath.Join(r.Dir, digest), []byte("# "+m+"\nlanded at "+commit+"\n"), 0o600)
	return map[string]any{"digest": "landed " + m, "digestPath": filepath.Join(r.Dir, digest)}, nil
}

// ---------------------------------------------------------------- RunsPort

// RunsShow materializes due records and returns the run's view.
func (c *Catherd) RunsShow(_ context.Context, _ *catherd.OrgEnv, runID string) (catherd.RunView, error) {
	var v catherd.RunView
	var out error
	err := c.txn(func(st *State, sc *Scenario, now time.Time) error {
		r := st.Runs[runID]
		if r == nil {
			out = &catherd.Error{Code: "E_RUN_NOT_FOUND", Message: "no run " + runID, Exit: 1}
			return nil
		}
		v.Records = append(v.Records, r.Records...)
		for _, l := range r.Landed {
			v.Landed = append(v.Landed, l.Milestone)
		}
		for _, d := range r.Disps {
			x := catherd.Dispatch{Name: d.Name, DispatchID: d.DispatchID, Rung: d.Rung, AdmittedAt: ts(d.AdmittedAt)}
			if rec := r.record(d.DispatchID); rec != nil {
				cp := *rec
				x.Record = &cp
			}
			v.Dispatches = append(v.Dispatches, x)
		}
		return nil
	})
	if err != nil {
		return v, err
	}
	return v, out
}

func (c *Catherd) cancelLocked(st *State, now time.Time, runID, name string) *ToolErr {
	r, e := c.run(st, runID)
	if e != nil {
		return e
	}
	d := r.latest(name)
	if d == nil {
		return terr("E_RUN_NOT_FOUND", name+" was never dispatched in this run", "")
	}
	if !d.Finalized {
		c.finalize(st, r, d, nil, now, "cancelled")
	}
	return nil
}

// RunsCancel cancels the live role.
func (c *Catherd) RunsCancel(_ context.Context, _ *catherd.OrgEnv, runID, name string) error {
	var out *ToolErr
	if err := c.txn(func(st *State, sc *Scenario, now time.Time) error {
		out = c.cancelLocked(st, now, runID, name)
		return nil
	}); err != nil {
		return err
	}
	if out != nil {
		return &catherd.Error{Code: out.Code, Message: out.Message, Fix: out.Fix, Exit: 1}
	}
	return nil
}

// ProfileUseRepo binds a profile to a repo directory.
func (c *Catherd) ProfileUseRepo(_ context.Context, _ *catherd.OrgEnv, _ string, name, repoDir string) error {
	return c.txn(func(st *State, sc *Scenario, now time.Time) error { st.Bound[repoDir] = name; return nil })
}

var _ catherd.RunsPort = (*Catherd)(nil)

// ---------------------------------------------------------------- assertions for tests

// Log returns every tool call served so far.
func (c *Catherd) Log() []CallRecord {
	var out []CallRecord
	_ = c.txn(func(st *State, sc *Scenario, now time.Time) error { out = append(out, st.Log...); return nil })
	return out
}

// CallsOf counts the served calls of a tool.
func (c *Catherd) CallsOf(tool string) []CallRecord {
	var out []CallRecord
	for _, r := range c.Log() {
		if r.Tool == tool {
			out = append(out, r)
		}
	}
	return out
}

// Dispatches lists the admitted dispatches of a run (newest last).
func (c *Catherd) Dispatches(runID string) []Disp {
	var out []Disp
	_ = c.txn(func(st *State, sc *Scenario, now time.Time) error {
		if r := st.Runs[runID]; r != nil {
			for _, d := range r.Disps {
				out = append(out, *d)
			}
		}
		return nil
	})
	return out
}

// RunIDs lists the runs.
func (c *Catherd) RunIDs() []string {
	var out []string
	_ = c.txn(func(st *State, sc *Scenario, now time.Time) error {
		for id := range st.Runs {
			out = append(out, id)
		}
		return nil
	})
	return out
}

// nextInject returns the app-server behaviour for the next directive of a tool.
func (c *Catherd) nextInject(tool string) string {
	var mode string
	_ = c.txn(func(st *State, sc *Scenario, now time.Time) error {
		n := st.InjSeq[tool]
		st.InjSeq[tool] = n + 1
		mode, _ = seqAt(sc.Inject, tool, n)
		return nil
	})
	return mode
}

func (c *Catherd) unreadableHistory() bool {
	var v bool
	_ = c.txn(func(st *State, sc *Scenario, now time.Time) error { v = sc.UnreadableHistory; return nil })
	return v
}

var errNoRun = errors.New("no such run")

// threadNew creates a thread.
func (c *Catherd) threadNew(model string) string {
	var id string
	_ = c.txn(func(st *State, sc *Scenario, now time.Time) error {
		st.N++
		id = threadID(st.N)
		st.Threads[id] = &Thread{ID: id, Model: model}
		return nil
	})
	return id
}

// threadGet returns a copy of a thread.
func (c *Catherd) threadGet(id string) (Thread, bool) {
	var t Thread
	var ok bool
	_ = c.txn(func(st *State, sc *Scenario, now time.Time) error {
		if x := st.Threads[id]; x != nil {
			t, ok = *x, true
		}
		return nil
	})
	return t, ok
}

// threadAddTurn appends a turn to a thread's history.
func (c *Catherd) threadAddTurn(id string, t Turn) {
	_ = c.txn(func(st *State, sc *Scenario, now time.Time) error {
		if x := st.Threads[id]; x != nil {
			x.Turns = append(x.Turns, t)
		}
		return nil
	})
}
