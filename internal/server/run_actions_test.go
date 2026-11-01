package server

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"sync/atomic"
	"testing"
	"time"
)

// act posts a run action with the run's current revision; the engine may move the run between the read and the
// request, so a revision conflict is retried with a fresh revision (a wanted conflict is asserted through f.json).
func (e *projectEnv) act(run RunDoc, verb string, body any, status int) map[string]any {
	e.f.t.Helper()
	for attempt := 0; ; attempt++ {
		resp := e.f.request("POST", e.orgPath("runs", run.ID, verb), body, map[string]string{"If-Match": fmt.Sprint(e.runState(run.IssueKey).Revision), "Idempotency-Key": nextKey()})
		if resp.StatusCode == 409 && status != 409 && attempt < 5 {
			resp.Body.Close()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		return readResponse(e.f.t, resp, status)
	}
}

func TestAutoPlanAndStartReachQueued(t *testing.T) {
	o := autoOpts()
	o.JQL = ptr("key = ORQ-1")
	e := newIntakeEnv(t, o)
	e.poll()
	r := e.waitState("ORQ-1", "queued")
	if r.Hold != nil || r.Plan == nil || !r.Plan.Approved || r.Plan.ApprovedAt == nil || r.Triage.Risk != "low" || r.WorkspaceID == nil {
		t.Fatalf("%+v", r)
	}
}

func TestCatherdOutageBlocksTheStartGateButNotIngestion(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{JQL: ptr("key = ORQ-1"), PlanApproval: "auto", CodingStart: "auto", Scheduler: true, Git: true, Setup: func(a *App) {
		a.runHook = func(stage string) error {
			if stage == "triage" {
				a.CatherdRT.SetVersion("") // catherd disappears after ingestion and triage: readiness reports catherd_unavailable
			}
			return nil
		}
	}})
	held := e.waitHold("ORQ-1", "project_not_ready")
	if held.Checkpoint.Stage != "start_gate" || held.State != "planned" || held.Triage == nil || held.Triage.Outcome != "execute" || !stringsContains(held.Hold.Detail, "catherd_unavailable") {
		t.Fatalf("%+v", held)
	}
	// Start refuses with the blockers; once catherd is back the auto gate releases on the sweep.
	expectCode(t, e.act(held, "start", nil, 409), "run_not_startable")
	e.f.a.CatherdRT.SetVersion("1.5.0")
	e.waitState("ORQ-1", "queued")
}
func stringsContains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestPauseHoldsAtTheNextBoundaryAndUnpauseContinues(t *testing.T) {
	var envPtr atomic.Pointer[intakeEnv]
	var paused atomic.Bool
	e := newIntakeEnv(t, intakeOpts{JQL: ptr("key = ORQ-1"), Scheduler: true, NoEnable: true, Setup: func(a *App) {
		a.runHook = func(stage string) error {
			if ie := envPtr.Load(); ie != nil && stage == "context" && paused.CompareAndSwap(false, true) {
				ie.f.json("PUT", ie.orgPath("automation"), PutAutomationRequest{Paused: true}, map[string]string{"If-Match": "0"}, 200)
			}
			return nil
		}
	}})
	envPtr.Store(e)
	e.enable()
	// The stage that was running when the pause arrived completes (the packet exists); the next boundary holds.
	held := e.waitHold("ORQ-1", "automation_paused")
	if held.Context == nil || held.Checkpoint.Stage != "triage" || held.State != "triaging" {
		t.Fatalf("%+v", held)
	}
	time.Sleep(700 * time.Millisecond)
	if r := e.runState("ORQ-1"); r.Hold == nil || r.Checkpoint.Stage != "triage" {
		t.Fatal("a paused run advanced")
	}
	expectCode(t, e.act(held, "resume", nil, 409), "automation_paused")
	e.f.json("PUT", e.orgPath("automation"), PutAutomationRequest{Paused: false}, map[string]string{"If-Match": "1"}, 200)
	e.waitStage("ORQ-1", "plan")
	if r := e.runState("ORQ-1"); r.Hold != nil {
		t.Fatal("hold not cleared")
	}
}

func TestCancelDuringAStageLosesTheLeaseThenConfirms(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{JQL: ptr("key = ORQ-1"), Scheduler: true, NoEnable: true})
	e.gl.Delay = 4 * time.Second // set before the run exists, so the stage's first GitLab call (the repository HEAD) always blocks
	e.enable()
	waitFor(t, "run created", func() bool { _, ok := e.runOf("ORQ-1"); return ok })
	waitFor(t, "a leased stage job", func() bool {
		r, ok := e.runOf("ORQ-1")
		return ok && e.count("jobs", bson.M{"org_id": e.org, "dedupe_key": "run:" + r.ID, "state": "leased"}) == 1 && r.State == "gathering_context"
	})
	run := e.runState("ORQ-1")
	cancelled := e.act(run, "cancel", CancelRunRequest{Reason: ptr("wrong ticket")}, 200)
	canc := cancelled["cancellation"].(map[string]any)
	if cancelled["state"] != "cancelled" || cancelled["active"] != false || canc["state"] != "requested" || cancelled["terminal"].(map[string]any)["reason"] != "cancelled" {
		t.Fatal(cancelled)
	}
	waitFor(t, "cancellation confirmed", func() bool {
		r := e.runState("ORQ-1")
		return r.Cancellation != nil && r.Cancellation.State == "confirmed" && r.Cancellation.ConfirmedAt != nil
	})
	if r := e.runState("ORQ-1"); r.State != "cancelled" || r.Context != nil {
		t.Fatalf("a cancelled run must not move on: %+v", r.Context)
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "run.cancel_requested"}) != 1 || e.count("events", bson.M{"org_id": e.org, "type": "run.cancel_confirmed"}) != 1 {
		t.Fatal("cancellation events")
	}
	expectCode(t, e.act(e.runState("ORQ-1"), "cancel", nil, 409), "run_state_conflict")
	e.gl.Delay = 0
	// Retry reopens the same run with a new attempt, with no other active run for the issue.
	retried := e.act(e.runState("ORQ-1"), "retry", nil, 200)
	if retried["id"] != run.ID || retried["attempt"] != float64(2) || retried["active"] != true || retried["state"] != "gathering_context" || len(retried["attempts"].([]any)) != 2 {
		t.Fatal(retried)
	}
	e.waitStage("ORQ-1", "plan")
	if e.count("runs", bson.M{"org_id": e.org}) != 1 {
		t.Fatal("retry created another run")
	}
}

func TestCancelAClosedQuestionCancelsTheItemAndRetryNeedsNoOtherRun(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{JQL: ptr("key = ORQ-2")})
	e.poll()
	run := e.waitState("ORQ-2", "needs_input")
	item := e.itemOf("ORQ-2")
	cancelled := e.act(run, "cancel", nil, 200)
	if cancelled["cancellation"].(map[string]any)["state"] != "confirmed" || cancelled["open_inbox_id"] != nil {
		t.Fatal(cancelled)
	}
	if got := e.inboxJSON(item["id"].(string)); got["status"] != "cancelled" {
		t.Fatal(got)
	}
	e.answer(item, nextKey(), AnswerInboxRequest{Choice: "cancel"}, 409)
	// A different run for the same issue blocks Retry: simulate by inserting an active run.
	other := RunDoc{ID: newID("run"), OrgID: e.org, ProjectID: e.pid, ConnectionID: run.ConnectionID, IssueID: run.IssueID, IssueKey: "ORQ-2", TrackedIssueID: run.TrackedIssueID, IntentKey: "x", State: "discovered", Active: true, Attempt: 1, Revision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	other.Checkpoint = RunCheckpoint{Stage: "context", Since: time.Now()}
	if _, err := e.f.db.Coll("runs").InsertOne(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	expectCode(t, e.act(e.runState("ORQ-2"), "retry", nil, 409), "run_state_conflict")
}

func TestStageErrorsAndCredentialExpiredQuestion(t *testing.T) {
	var once atomic.Bool
	e := newIntakeEnv(t, intakeOpts{JQL: ptr("key = ORQ-1"), Setup: func(a *App) {
		a.runHook = func(stage string) error {
			if stage == "context" && once.CompareAndSwap(false, true) {
				return &runFailureError{Code: "invalid_output", Message: "the stage produced something unusable"}
			}
			return nil
		}
	}})
	e.poll()
	failed := e.waitState("ORQ-1", "failed")
	if failed.Active || failed.Failure == nil || failed.Failure.Code != "invalid_output" || failed.Terminal == nil {
		t.Fatalf("%+v", failed)
	}
	// Retry a failed run; the GitLab 401 inside the next stage becomes a credential question.
	e.gl.Fail["projects/1/repository/commits"] = &failure{Status: 401, Count: 1}
	e.act(failed, "retry", nil, 200)
	run := e.waitState("ORQ-1", "needs_input")
	it := e.openItem("ORQ-1")
	if it.Reason != "credential_expired" || it.Priority != "high" || len(it.Options) != 2 || it.Options[0].ID != "resume" || run.Checkpoint.Stage != "context" {
		t.Fatalf("%+v", it)
	}
	// Resolve after the credential works again: the same run continues from its stage.
	res := e.f.json("POST", e.orgPath("inbox", it.ID, "resolve"), ResolveInboxRequest{Action: "resume"}, map[string]string{"If-Match": "1", "Idempotency-Key": nextKey()}, 200)
	if res["item"].(map[string]any)["status"] != "resolved" || res["run"].(map[string]any)["id"] != failed.ID {
		t.Fatal(res)
	}
	e.waitStage("ORQ-1", "plan")
	if r := e.runState("ORQ-1"); r.Attempt != 2 {
		t.Fatal(r.Attempt)
	}
	// A question that needs an answer cannot be resolved by resume.
	e2 := newIntakeEnv(t, intakeOpts{JQL: ptr("key = ORQ-2")})
	e2.poll()
	e2.waitState("ORQ-2", "needs_input")
	item := e2.itemOf("ORQ-2")
	expectCode(t, e2.f.json("POST", e2.orgPath("inbox", item["id"].(string), "resolve"), ResolveInboxRequest{Action: "resume"}, map[string]string{"If-Match": "1", "Idempotency-Key": nextKey()}, 422), "answer_invalid")
	expectCode(t, e2.f.json("POST", e2.orgPath("inbox", item["id"].(string), "resolve"), ResolveInboxRequest{Action: "nope"}, map[string]string{"If-Match": "1", "Idempotency-Key": nextKey()}, 422), "answer_invalid")
}

func TestMaterialChangeSupersedesTheRunAndItsQuestion(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{JQL: ptr("key = ORQ-6"), PlanApproval: "manual"})
	e.poll()
	e.waitState("ORQ-6", "needs_input")
	clar := e.itemOf("ORQ-6")
	e.answer(clar, nextKey(), AnswerInboxRequest{Choice: "clarify", Text: ptr("The DBA has reviewed the migration plan.")}, 200)
	waitFor(t, "approval item", func() bool {
		r := e.runState("ORQ-6")
		return r.State == "needs_input" && r.Checkpoint.Stage == "plan_gate"
	})
	old := e.runState("ORQ-6")
	approval := e.itemOf("ORQ-6")
	// The acceptance changes while the approval waits.
	e.jf.SetIssue("ORQ-6", func(i *testfakesIssue) {
		i.Description += "\n* Passwords are rehashed on first login."
	})
	e.poll()
	waitFor(t, "superseded", func() bool { r := e.runState("ORQ-6"); return r.ID != old.ID })
	fresh := e.runState("ORQ-6")
	was, _ := e.f.a.run(context.Background(), e.org, old.ID)
	if was.State != "superseded" || was.Active || fresh.Origin != "supersede" || fresh.Supersedes == nil || *fresh.Supersedes != old.ID {
		t.Fatalf("%+v %+v", was.State, fresh.Origin)
	}
	if got := e.inboxJSON(approval["id"].(string)); got["status"] != "superseded" || got["superseded_by_run"] != fresh.ID {
		t.Fatal(got)
	}
	stale := e.answer(approval, nextKey(), AnswerInboxRequest{Choice: "approve"}, 409)
	expectCode(t, stale, "inbox_item_stale")
	if stale["current"].(map[string]any)["superseded_by_run"] != fresh.ID {
		t.Fatal(stale)
	}
	carried := false
	for _, s := range fresh.Sources {
		if s.Code == "clarification" && s.Carried && s.Text != nil {
			carried = true
		}
	}
	if !carried {
		t.Fatalf("the clarification must be carried: %+v", fresh.Sources)
	}
	if e.count("runs", bson.M{"org_id": e.org, "active": true}) != 1 {
		t.Fatal("exactly one active run")
	}
}
