package server

import (
	"context"
	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/decision"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// jevResponse builds a valid Jev answer set.
func jevResponse(clarity string, clarityP float64, scope string, testable, done float64, areas map[string]float64) decision.Response {
	r := decision.Response{Model: "jev-1.13.0", RequestID: "req-test", LatencyMS: 12, Attempts: 1, Answers: map[string]decision.Answer{
		"clarity":      {Type: "choice", Choice: clarity, Probabilities: map[string]float64{clarity: clarityP}},
		"scope":        {Type: "choice", Choice: scope, Probabilities: map[string]float64{scope: 0.95}},
		"testable":     {Type: "noul", Noul: testable},
		"already_done": {Type: "noul", Noul: done},
	}}
	for _, id := range []string{"touches_auth", "touches_payment", "touches_migration", "touches_data_deletion", "touches_ci_config", "cross_service"} {
		r.Answers[id] = decision.Answer{Type: "noul", Noul: areas[id]}
	}
	return r
}
func titleOf(r decision.Request) string { s, _ := r.State["title"].(string); return s }

func (e *projectEnv) openItem(key string) InboxItemDoc {
	e.f.t.Helper()
	r, _ := e.runOf(key)
	if r.OpenInboxID == nil {
		e.f.t.Fatalf("%s has no open inbox item (state %s)", key, r.State)
	}
	it, err := findOne[InboxItemDoc](context.Background(), e.f.db, "inbox_items", bson.M{"_id": *r.OpenInboxID})
	if err != nil {
		e.f.t.Fatal(err)
	}
	return it
}
func (e *projectEnv) waitState(key, state string) RunDoc {
	e.f.t.Helper()
	waitFor(e.f.t, key+" in state "+state, func() bool { return e.runState(key).State == state })
	return e.runState(key)
}
func (e *projectEnv) protect(pattern, mode string) {
	e.f.t.Helper()
	e.f.json("PUT", e.path(e.pid, "protected-paths"), PutProtectedPathsRequest{Rules: []ProtectedPathInput{{Pattern: pattern, Mode: mode}}}, map[string]string{"If-Match": e.rev(e.pid)}, 200)
}

func TestTriageFixedRulesOutcomes(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{Scheduler: true, PlanApproval: "auto"})
	e.protect(".gitlab-ci.yml", "deny")
	// The scheduler polls at once (full scan); a Done issue enters afterwards through an ingest job, because the
	// intake filter never lists Done issues.
	waitFor(t, "the first scheduled poll", func() bool { return e.intakeDoc().LastSuccessStartedAt != nil })
	now := time.Now().UTC()
	if err := e.f.a.enqueue(context.Background(), JobDoc{ID: newID("job"), OrgID: e.org, Kind: "issue_ingest", DedupeKey: "issue_ingest:" + e.jiID + ":20009", State: "queued", Active: true, MaxAttempts: 5, NextAt: now, CreatedAt: now, UpdatedAt: now,
		Payload: bson.M{"project_id": e.pid, "connection_id": e.jiID, "issue_id": "20009", "key": "ORQ-9", "poll_id": "manual"}}); err != nil {
		t.Fatal(err)
	}
	// ORQ-1: clear, low risk, acceptance items, approved test command: the fixed rules execute.
	r1 := e.waitStage("ORQ-1", "plan")
	if r1.State != "planned" || r1.Triage == nil || r1.Triage.Outcome != "execute" || r1.Triage.Source != "fixed_rules" || r1.Triage.Risk != "low" || r1.Triage.DecidedAt.IsZero() {
		t.Fatalf("%+v", r1.Triage)
	}
	// ORQ-2: no acceptance -> one question with evidence, no workspace, no stage job.
	r2 := e.waitState("ORQ-2", "needs_input")
	it := e.openItem("ORQ-2")
	if it.Reason != "missing_acceptance" || it.QuestionCode != "missing_acceptance" || it.Kind != "question" || it.Status != "open" || r2.Checkpoint.Stage != "triage" {
		t.Fatalf("%+v", it)
	}
	hasDescription := false
	for _, ev := range it.Evidence {
		if ev.Kind == "jira_description" && strings.Contains(ev.Excerpt, "Search is not good") && ev.URL != "" {
			hasDescription = true
		}
	}
	if !hasDescription || len(it.Options) != 2 || it.Options[0].ID != "provide_acceptance" || !it.Options[0].RequiresText {
		t.Fatalf("%+v", it)
	}
	if n := e.count("inbox_items", bson.M{"org_id": e.org, "run_id": r2.ID}); n != 1 {
		t.Fatal("exactly one inbox item expected:", n)
	}
	if e.count("workspaces", bson.M{"org_id": e.org, "run_id": r2.ID}) != 0 {
		t.Fatal("a stopped run must have no workspace")
	}
	// The stage job is completed right after the run stops; it must not stay active.
	waitFor(t, "the stopped run has no active stage job", func() bool {
		return e.count("jobs", bson.M{"org_id": e.org, "dedupe_key": "run:" + r2.ID, "active": true}) == 0
	})
	// ORQ-4: blocked by ORQ-5 (In Progress) -> waiting_dependency; after ORQ-5 is done the run triages again.
	e.waitState("ORQ-4", "waiting_dependency")
	e.jf.SetIssue("ORQ-5", func(i *testfakesIssue) { i.StatusName, i.StatusCategory = "Done", "done" })
	if _, err := e.f.db.Coll("runs").UpdateOne(context.Background(), bson.M{"org_id": e.org, "issue_key": "ORQ-4"}, bson.M{"$set": bson.M{"dep_checked_at": time.Now().Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	r4 := e.waitStage("ORQ-4", "plan")
	if r4.State != "planned" || r4.Triage == nil || r4.Triage.Outcome != "execute" {
		t.Fatalf("%+v", r4.Triage)
	}
	// ORQ-9: Done -> cancelled as already addressed.
	r9 := e.waitState("ORQ-9", "cancelled")
	if r9.Active || r9.Terminal == nil || r9.Terminal.Reason != "already_addressed" || r9.Triage == nil || r9.Triage.Outcome != "already_addressed" {
		t.Fatalf("%+v", r9)
	}
	// ORQ-7: names a deny-protected path -> protected_change.
	e.waitState("ORQ-7", "needs_input")
	if it7 := e.openItem("ORQ-7"); it7.Reason != "protected_change" || it7.Params["paths"] == nil || it7.Options[0].ID != "proceed_without_protected" {
		t.Fatalf("%+v", it7)
	}
	// ORQ-6: sensitive (migration) and the fixed rules never execute sensitive work without a person.
	e.waitState("ORQ-6", "needs_input")
	r6 := e.runState("ORQ-6")
	it6 := e.openItem("ORQ-6")
	if it6.Reason != "ambiguous_behavior" || it6.QuestionCode != "needs_clarification" || r6.Triage.Risk != "sensitive" || !contains(r6.Triage.RiskReasons, "area:migration") {
		t.Fatalf("%+v %+v", it6, r6.Triage)
	}
	if e.count("inbox_items", bson.M{"org_id": e.org, "status": "open"}) != 3 {
		t.Fatal("open items:", e.count("inbox_items", bson.M{"org_id": e.org, "status": "open"}))
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "inbox.opened"}) != 3 {
		t.Fatal("inbox.opened events")
	}
}

func TestTriageJevDecisions(t *testing.T) {
	var asked atomic.Int64
	fake := &decision.Fake{Fn: func(r decision.Request) (decision.Response, error) {
		asked.Add(1)
		switch titleOf(r) {
		case "Add CSV export to the invoice list":
			return jevResponse("ambiguous", 0.9, "single_repo", 0.9, 0.05, nil), nil
		case "Migrate users table":
			return jevResponse("clear", 0.95, "single_repo", 0.9, 0.05, map[string]float64{"touches_migration": 0.95}), nil
		case "Show invoice totals in the footer":
			return jevResponse("clear", 0.95, "single_repo", 0.95, 0.02, nil), nil
		case "Speed up the pipeline":
			return jevResponse("clear", 0.5, "single_repo", 0.9, 0.05, nil), nil // unsure -> fixed rules
		}
		return decision.Response{}, decision.ErrUnavailable
	}}
	e := newIntakeEnv(t, intakeOpts{Setup: func(a *App) { a.SetDecision(fake) }})
	e.putSecret("jev", "jev-key-sentinel-1")
	e.poll()
	e.waitState("ORQ-1", "needs_input")
	if it := e.openItem("ORQ-1"); it.Reason != "ambiguous_behavior" || it.QuestionCode != "ambiguous_requirements" {
		t.Fatalf("%+v", it)
	}
	if r := e.runState("ORQ-1"); r.Triage.Source != "jev" || r.Triage.DecisionID == nil {
		t.Fatalf("%+v", r.Triage)
	}
	r3 := e.waitStage("ORQ-3", "plan")
	if r3.Triage.Source != "jev" || r3.Triage.Outcome != "execute" || r3.Triage.Risk != "low" {
		t.Fatalf("%+v", r3.Triage)
	}
	// Jev says clear but flags a migration: executes, and the risk is escalated to sensitive.
	r6 := e.waitStage("ORQ-6", "plan")
	if r6.Triage.Source != "jev" || r6.Triage.Risk != "sensitive" || !contains(r6.Triage.RiskReasons, "area:migration") {
		t.Fatalf("%+v", r6.Triage)
	}
	e.waitState("ORQ-7", "needs_input")
	if r7 := e.runState("ORQ-7"); r7.Triage.Source != "fixed_rules" || e.openItem("ORQ-7").QuestionCode != "needs_clarification" {
		t.Fatalf("%+v", r7.Triage)
	}
	// ORQ-2 (no acceptance) and ORQ-4 (blocked) stop at the guards: Jev is never asked about them.
	e.waitState("ORQ-2", "needs_input")
	e.waitState("ORQ-4", "waiting_dependency")
	if got := asked.Load(); got != 4 {
		t.Fatal("Jev should have been asked for ORQ-1, 3, 6 and 7 only, asked", got)
	}
	if n := e.count("decisions", bson.M{"org_id": e.org, "status": "ok"}); n != 4 {
		t.Fatal("decisions:", n)
	}
	sum := e.f.json("GET", e.orgPath("tasks", "summary"), nil, nil, 200)
	d := sum["decisions_24h"].(map[string]any)
	if d["jev"] != float64(4) || d["fixed_rules"] != float64(1) {
		t.Fatal(d)
	}
}

func TestTriageJevOutageFallsBackToFixedRules(t *testing.T) {
	fake := &decision.Fake{Fn: func(decision.Request) (decision.Response, error) { return decision.Response{}, decision.ErrTimeout }}
	e := newIntakeEnv(t, intakeOpts{JQL: ptr("key = ORQ-3"), Setup: func(a *App) { a.SetDecision(fake) }})
	e.putSecret("jev", "jev-key-sentinel-2")
	e.poll()
	r := e.waitStage("ORQ-3", "plan")
	if r.Triage.Source != "fixed_rules" || r.Triage.Outcome != "execute" {
		t.Fatalf("%+v", r.Triage)
	}
	dec, err := findOne[DecisionDoc](context.Background(), e.f.db, "decisions", bson.M{"run_id": r.ID})
	if err != nil || dec.Status != "error" || dec.Why != "timeout" {
		t.Fatal(dec, err)
	}
	// decision.jev=off never asks and uses the fixed rules too.
	asked := &decision.Fake{Fn: func(decision.Request) (decision.Response, error) {
		return jevResponse("clear", 0.95, "single_repo", 0.9, 0.05, nil), nil
	}}
	e2 := newIntakeEnv(t, intakeOpts{JQL: ptr("key = ORQ-3"), Extra: map[string]any{"decision.jev": "off"}, Setup: func(a *App) { a.SetDecision(asked) }})
	e2.putSecret("jev", "jev-key-sentinel-3")
	e2.poll()
	if r := e2.waitStage("ORQ-3", "plan"); r.Triage.Source != "fixed_rules" || asked.Calls.Load() != 0 {
		t.Fatalf("%+v calls %d", r.Triage, asked.Calls.Load())
	}
	// No key: the fixed rules decide and Jev is not called either.
	asked3 := &decision.Fake{Fn: asked.Fn}
	e3 := newIntakeEnv(t, intakeOpts{JQL: ptr("key = ORQ-3"), Setup: func(a *App) { a.SetDecision(asked3) }})
	e3.poll()
	if r := e3.waitStage("ORQ-3", "plan"); r.Triage.Source != "fixed_rules" || asked3.Calls.Load() != 0 {
		t.Fatalf("%+v calls %d", r.Triage, asked3.Calls.Load())
	}
}

func TestTriageCrashAfterJevAnswerDoesNotPayTwice(t *testing.T) {
	fake := &decision.Fake{Fn: func(decision.Request) (decision.Response, error) {
		return jevResponse("clear", 0.95, "single_repo", 0.9, 0.05, nil), nil
	}}
	var crashed atomic.Bool
	e := newIntakeEnv(t, intakeOpts{JQL: ptr("key = ORQ-3"), Setup: func(a *App) {
		a.SetDecision(fake)
		a.runHook = func(stage string) error {
			if stage == "triage" && crashed.CompareAndSwap(false, true) {
				return errLeaseLost
			}
			return nil
		}
	}})
	e.putSecret("jev", "jev-key-sentinel-4")
	e.poll()
	r := e.waitStage("ORQ-3", "plan")
	if !crashed.Load() || fake.Calls.Load() != 1 || r.Triage.Source != "jev" {
		t.Fatal("crashed", crashed.Load(), "calls", fake.Calls.Load())
	}
	if n := e.count("decisions", bson.M{"org_id": e.org}); n != 1 {
		t.Fatal("decisions:", n)
	}
}

// keyPort records the key it was given and answers like a clear Jev.
type keyPort struct {
	keys atomic.Value
	n    atomic.Int64
}

func (k *keyPort) Ask(_ context.Context, r decision.Request, key string) (decision.Response, error) {
	k.keys.Store(key)
	k.n.Add(1)
	return jevResponse("clear", 0.95, "single_repo", 0.9, 0.05, nil), nil
}

func TestTriageNeverLeaksTheJevKeyOrIssueText(t *testing.T) {
	const sentinel = "JEV-SENTINEL-0123456789abcdef"
	logs := captureLogs(t)
	port := &keyPort{}
	e := newIntakeEnv(t, intakeOpts{Setup: func(a *App) { a.SetDecision(port) }})
	e.putSecret("jev", sentinel)
	e.poll()
	e.waitStage("ORQ-3", "plan")
	e.waitStage("ORQ-1", "plan")
	if got, _ := port.keys.Load().(string); got != sentinel || port.n.Load() < 2 {
		t.Fatal("the adapter did not receive the key at call time")
	}
	for _, p := range []string{"tasks", "tasks/summary", "inbox"} {
		e.f.request("GET", e.orgPath(p), nil, nil).Body.Close()
	}
	r, _ := e.runOf("ORQ-1")
	e.f.json("GET", e.orgPath("runs", r.ID), nil, nil, 200)
	e.f.json("GET", e.orgPath("runs", r.ID, "timeline"), nil, nil, 200)
	e.f.json("GET", e.orgPath("events")+"?limit=500", nil, nil, 200)
	docs, files := e.leakScan([]string{sentinel}, logs.String())
	if docs < 20 || files < 3 {
		t.Fatal("scanned too little", docs, files)
	}
	// Positive control: the key is in the credential store.
	stored := 0
	e.f.creds.mu.RLock()
	for _, v := range e.f.creds.values {
		if v == sentinel {
			stored++
		}
	}
	e.f.creds.mu.RUnlock()
	if stored != 1 {
		t.Fatal("the key must be stored exactly once, in the credential store:", stored)
	}
}
