package server

import (
	"context"
	"fmt"
	"orqen/internal/runstate"
	"orqen/internal/testfakes/hostfake"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func (e *s4Env) worktree(key string) string {
	run, _ := e.runOf(key)
	ws, found, err := e.f.a.workspaceDoc(context.Background(), e.org, run.ID)
	if err != nil || !found {
		e.f.t.Fatal("no workspace", err)
	}
	return filepath.Join(e.f.a.Config.DataDir, ws.PathRel)
}

func TestS4ImplementCommitsTheWorkOnce(t *testing.T) {
	e := startOne(t, s4Opts{}, "ORQ-1")
	run := e.waitStage("ORQ-1", runstate.StageVerify)
	wt := e.worktree("ORQ-1")
	if run.State != runstate.Verifying || run.Heads.Head == "" || run.Heads.Base == "" || run.Heads.Head == run.Heads.Base {
		t.Fatalf("%+v", run.Heads)
	}
	if got := gitIn(t, wt, "rev-parse", "HEAD"); got != run.Heads.Head {
		t.Fatal(got, run.Heads.Head)
	}
	msg := gitIn(t, wt, "log", "-1", "--format=%B")
	if !strings.Contains(msg, "ORQ-1: ") || !strings.Contains(msg, "Orqen-Run: "+run.ID) {
		t.Fatal(msg)
	}
	if who := gitIn(t, wt, "log", "-1", "--format=%an <%ae>|%cn <%ce>"); who != "Orqen <orqen@localhost>|Orqen <orqen@localhost>" {
		t.Fatal(who)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "src", "feature.txt")); string(b) != "orqen-feature\n" {
		t.Fatalf("%q", b)
	}
	if n := gitIn(t, wt, "rev-list", "--count", run.Heads.Base+"..HEAD"); n != "1" {
		t.Fatal(n)
	}
	roles := e.roles("ORQ-1")
	if len(roles) != 2 || roles[1].Name != "worker-M1.L1" || roles[1].State != RexCollected || roles[1].Catherd.Family != "gpt-6-sol" {
		t.Fatalf("%+v", roles)
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "run.committed"}) != 1 {
		t.Fatal("commit event")
	}
}

func TestS4EmptyChangeClimbsOnceThenAsks(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.NoChange = true
	e := startOne(t, s4Opts{Scenario: sc}, "ORQ-1")
	run := e.waitState("ORQ-1", runstate.NeedsInput)
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["reason"] != "model_unavailable" || item["question_code"] != "role_failed" {
		t.Fatal(item)
	}
	if n := len(e.cat.CallsOf("climb")); n != 1 {
		t.Fatalf("climb calls %d", n)
	}
	if roles := e.roles("ORQ-1"); len(roles) != 3 || roles[2].Name != "worker-M1.L1-c1" {
		t.Fatalf("%+v", roles)
	}
	if e.runState("ORQ-1").Slot != nil {
		t.Fatal("the slot is released at needs_input")
	}
}

func TestS4TimeoutRecordClimbsToTheNextRung(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.RoleStatuses = map[string][]string{"worker-M1.L1": {"timeout"}}
	e := startOne(t, s4Opts{Scenario: sc}, "ORQ-1")
	e.waitStage("ORQ-1", runstate.StageVerify)
	if len(e.cat.CallsOf("climb")) != 1 {
		t.Fatal("one climb expected")
	}
	roles := e.roles("ORQ-1")
	if len(roles) != 3 || roles[2].Catherd.Rung != "codex:gpt-6-sol#high" || roles[1].Climbed != true {
		t.Fatalf("%+v", roles)
	}
}

func TestS4UsageLimitAsksTheUser(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.RoleStatuses = map[string][]string{"worker-M1.L1": {"limit"}}
	e := startOne(t, s4Opts{Scenario: sc}, "ORQ-1")
	run := e.waitState("ORQ-1", runstate.NeedsInput)
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["reason"] != "model_unavailable" || item["question_code"] != "usage_limit" || len(e.cat.CallsOf("climb")) != 0 {
		t.Fatal(item)
	}
}

func TestS4BudgetErrorOpensInboxAndReleasesTheSlot(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.AdmitErrors = map[string][]hostfake.ErrSpec{"dispatch": {{}, {Code: "E_RUN_BUDGET", Message: "budget used", Fix: "raise it"}}}
	e := startOne(t, s4Opts{Scenario: sc}, "ORQ-1")
	run := e.waitState("ORQ-1", runstate.NeedsInput)
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["reason"] != "budget_exceeded" {
		t.Fatal(item)
	}
	if e.runState("ORQ-1").Slot != nil || e.count("coding_slots", bson.M{"holder": bson.M{"$ne": nil}}) != 0 {
		t.Fatal("slot not released")
	}
	roles := e.roles("ORQ-1")
	if roles[len(roles)-1].State != RexPlanned {
		t.Fatalf("%+v", roles)
	}
}

func TestS4DispatchCapAsksBeforeTheNextDispatch(t *testing.T) {
	o := s4Opts{}
	o.Extra = map[string]any{"roles.max_dispatches_per_run": 4}
	sc := hostfake.DefaultScenario()
	sc.NoChange = true // every worker attempt is a dispatch; the cap is hit
	o.Scenario = sc
	e := startOne(t, o, "ORQ-1")
	e.waitState("ORQ-1", runstate.NeedsInput)
	run := e.runState("ORQ-1")
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["reason"] != "model_unavailable" || item["question_code"] != "role_failed" {
		t.Fatal(item) // two attempts: failed before the cap of 4
	}
}

func TestS4LaneOwningAProtectedPathStopsBeforeAnyWorker(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.Lanes = []hostfake.LaneSpec{{ID: "M1.L1", Title: "infra", Owns: []string{"infra/"}}}
	o := s4Opts{Scenario: sc}
	o.NoEnable = false
	e := newS4Env(t, withJQL(o, "ORQ-1"))
	e.f.json("PUT", e.path(e.pid, "protected-paths"), PutProtectedPathsRequest{Rules: []ProtectedPathInput{{Pattern: "infra/**", Mode: "deny"}}}, map[string]string{"If-Match": e.rev(e.pid)}, 200)
	e.poll()
	run := e.waitState("ORQ-1", runstate.NeedsInput)
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["reason"] != "protected_change" {
		t.Fatal(item)
	}
	if n := len(e.cat.CallsOf("dispatch")); n != 1 {
		t.Fatalf("only the architect may be dispatched, got %d", n)
	}
}

func withJQL(o s4Opts, keys ...string) s4Opts { o.JQL = jqlFor(keys...); return o }

func TestS4CommitCrashReusesTheCommit(t *testing.T) {
	var lost bool
	o := s4Opts{}
	o.Setup = func(a *App) {
		a.runHook = func(stage string) error {
			if stage == runstate.StageImplement && !lost {
				lost = true
				return errLeaseLost // after the commit, before the checkpoint
			}
			return nil
		}
	}
	e := startOne(t, o, "ORQ-1")
	run := e.waitStage("ORQ-1", runstate.StageVerify)
	wt := e.worktree("ORQ-1")
	if n := gitIn(t, wt, "rev-list", "--count", run.Heads.Base+"..HEAD"); n != "1" {
		t.Fatalf("commits: %s", n)
	}
	if !lost || e.count("events", bson.M{"org_id": e.org, "type": "run.committed"}) != 1 {
		t.Fatal("one commit event expected")
	}
}

func TestS4ManualPlanApprovalShowsThePlanAndRequestChangesReplans(t *testing.T) {
	o := s4Opts{}
	o.PlanApproval = "manual"
	e := startOne(t, o, "ORQ-1")
	run := e.waitState("ORQ-1", runstate.NeedsInput)
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["kind"] != "approval" || item["question_code"] != "architect_plan_approval" || len(item["evidence"].([]any)) != 1 {
		t.Fatal(item)
	}
	art := item["evidence"].([]any)[0].(map[string]any)["source_id"].(string)
	if txt := e.artifactText(art); !strings.Contains(txt, "Goal:") || !strings.Contains(txt, "M1.L1") {
		t.Fatal(txt)
	}
	first := e.roles("ORQ-1")[0]
	e.answer(item, nextKey(), AnswerInboxRequest{Choice: "request_changes", Text: ptr("split it in two")}, 200)
	waitFor(t, "second architect", func() bool { return len(e.roles("ORQ-1")) == 2 })
	run = e.waitState("ORQ-1", runstate.NeedsInput)
	second := e.roles("ORQ-1")[1]
	if second.Name != "architect-M1-p1" || second.Catherd == nil || first.Catherd.Thread == second.Catherd.Thread {
		t.Fatalf("%+v %+v", first.Catherd, second.Catherd)
	}
	item = e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	e.answer(item, nextKey(), AnswerInboxRequest{Choice: "approve"}, 200)
	e.waitStage("ORQ-1", runstate.StageVerify)
	fmt.Print()
}
