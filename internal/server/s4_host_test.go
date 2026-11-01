package server

import (
	"context"
	"fmt"
	"orqen/internal/runstate"
	"orqen/internal/testfakes/hostfake"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func startOne(t *testing.T, o s4Opts, key string) *s4Env {
	t.Helper()
	o.JQL = jqlFor(key)
	e := newS4Env(t, o)
	e.poll()
	return e
}

func TestS4HostRunReachesImplementWithOneArchitect(t *testing.T) {
	e := startOne(t, s4Opts{}, "ORQ-1")
	run := e.waitStage("ORQ-1", runstate.StageImplement)
	if run.State != runstate.Coding || run.Slot == nil || run.CatherdRunID == "" || run.HostSessionID == "" || len(run.Lanes) != 1 || run.Lanes[0].ID != "M1.L1" {
		t.Fatalf("%+v", run)
	}
	if !strings.HasPrefix(run.CatherdRunDir, e.cat.Root) && !strings.Contains(run.CatherdRunDir, "/data/runs/") {
		t.Fatal(run.CatherdRunDir)
	}
	roles := e.roles("ORQ-1")
	if len(roles) < 1 || roles[0].Name != "architect-M1" || roles[0].State != RexCollected || roles[0].Catherd.Rung != "codex:gpt-6.1-sol#high" || roles[0].Catherd.Family != "gpt-6.1-sol" || roles[0].ReplyArtifactID == "" {
		t.Fatalf("%+v", roles)
	}
	if roles[0].RolebriefVer != "s7.1" || len(roles[0].BriefSHA) != 64 {
		t.Fatalf("%+v", roles[0])
	}
	if got := e.cat.CallsOf("dispatch"); len(got) < 1 || got[0].Args["role"] != "architect" {
		t.Fatalf("dispatch calls: %d", len(got))
	}
	// the pinned profile was bound to the worktree before the run started
	if e.fake.BoundProfile(strings.TrimSpace(e.cat.CallsOf("run_start")[0].Args["repo"].(string))) == "" {
		t.Fatal("no profile binding")
	}
	dto := e.f.json("GET", e.orgPath("runs", run.ID, "roles"), nil, nil, 200)
	if len(dto["roles"].([]any)) < 1 || dto["host_session"].(map[string]any)["thread_id"] == "" {
		t.Fatal(dto)
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "host.session_started"}) != 1 || e.count("events", bson.M{"org_id": e.org, "type": "role.dispatched"}) < 1 {
		t.Fatal("events")
	}
}

func TestS4CodingSlotAllowsOneRunAtATime(t *testing.T) {
	o := s4Opts{CodingSlots: 1}
	o.Extra = map[string]any{"concurrency.coding_runs": 2}
	e := newS4Env(t, o) // the default intake filter queues several issues
	e.poll()
	var first, second string
	waitFor(t, "one run coding and another waiting for the slot", func() bool {
		first, second = "", ""
		for _, k := range eligibleKeys {
			r, ok := e.runOf(k)
			if !ok {
				continue
			}
			if r.Slot != nil {
				first = k
			} else if r.State == runstate.Queued && r.Hold != nil && r.Hold.Code == runstate.HoldAwaitingCodingSlot && second == "" {
				second = k
			}
		}
		return first != "" && second != ""
	})
	if e.runState(second).State != runstate.Queued || e.runState(second).Slot != nil {
		t.Fatalf("%+v", e.runState(second))
	}
	dto := e.f.json("GET", e.orgPath("runs", e.runState(second).ID), nil, nil, 200)
	if dto["slot_queue_position"] != float64(1) {
		t.Fatal(dto["slot_queue_position"])
	}
	if e.count("runs", bson.M{"org_id": e.org, "slot": bson.M{"$ne": nil}}) != 1 {
		t.Fatal("two slots in use")
	}
	// cancelling the first run frees the slot, the second one starts
	e.act(e.runState(first), "cancel", CancelRunRequest{}, 200)
	waitFor(t, "the slot is handed on", func() bool { return e.count("events", bson.M{"org_id": e.org, "type": "coding_slot.acquired"}) >= 2 })
	if e.runState(first).Slot != nil || e.count("runs", bson.M{"org_id": e.org, "slot": bson.M{"$ne": nil}}) != 1 {
		t.Fatal("slot hand-over")
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "coding_slot.released"}) < 1 {
		t.Fatal("slot events")
	}
}

func TestS4HostUnavailableNeverDispatches(t *testing.T) {
	e := startOne(t, s4Opts{NoHost: true}, "ORQ-1")
	// an unprobed host holds the start gate: readiness names host_not_probed
	waitFor(t, "held by readiness", func() bool {
		r := e.runState("ORQ-1")
		return r.Hold != nil && r.Hold.Code == "project_not_ready" && strings.Contains(r.Hold.Detail, "host_not_probed")
	})
	time.Sleep(700 * time.Millisecond)
	if e.app.TurnStarts.Load() != 0 || e.spawned.Load() != 0 || e.runState("ORQ-1").Slot != nil {
		t.Fatal("nothing may start without a probed host")
	}
	e.markHostProbed(false)
	time.Sleep(500 * time.Millisecond)
	if e.app.TurnStarts.Load() != 0 {
		t.Fatal("a failed probe is not an available host")
	}
	e.markHostProbed(true)
	e.waitStage("ORQ-1", runstate.StageImplement)
}

func TestS4CrashAfterAdmitReconcilesWithoutASecondDispatch(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.Inject = map[string][]string{"dispatch": {"crash_after_admit"}}
	e := startOne(t, s4Opts{Scenario: sc}, "ORQ-1")
	e.waitStage("ORQ-1", runstate.StageImplement)
	if n := len(e.cat.CallsOf("dispatch")); n != 1 {
		t.Fatalf("dispatch was called %d times", n)
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "role.reconciled", "payload.adopted": true}) != 1 {
		t.Fatal("the dispatch was not adopted")
	}
	if e.spawned.Load() < 2 {
		t.Fatal("the host should have been restarted")
	}
}

func TestS4LeaseLossAfterDispatchIsAdopted(t *testing.T) {
	var lost atomic.Bool
	e := startOne(t, s4Opts{intakeOpts: intakeOpts{Setup: func(a *App) {
		a.runHook = func(stage string) error {
			if stage == "architect" && lost.CompareAndSwap(false, true) {
				return errLeaseLost
			}
			return nil
		}
	}}}, "ORQ-1")
	e.waitStage("ORQ-1", runstate.StageImplement)
	if !lost.Load() || len(e.cat.CallsOf("dispatch")) != 1 {
		t.Fatal(lost.Load(), len(e.cat.CallsOf("dispatch")))
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "role.reconciled"}) != 1 {
		t.Fatal("not reconciled")
	}
}

func TestS4HostDisconnectWhileARoleRunsResumesTheThread(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.RecordDelayMs = 1500
	e := startOne(t, s4Opts{Scenario: sc}, "ORQ-1")
	waitFor(t, "dispatched", func() bool {
		r := e.roles("ORQ-1")
		return len(r) == 1 && r[0].State == RexDispatched
	})
	e.sup.Stop(e.org) // the app-server dies
	e.waitStage("ORQ-1", runstate.StageImplement)
	if n := len(e.cat.CallsOf("dispatch")); n != 1 {
		t.Fatalf("dispatch calls: %d", n)
	}
	if e.app.Resumes.Load() != 1 {
		t.Fatalf("thread/resume count %d", e.app.Resumes.Load())
	}
}

func TestS4HungDispatchWithoutAdmissionIsDispatchedOnce(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.Inject = map[string][]string{"dispatch": {"hang"}}
	e := startOne(t, s4Opts{Scenario: sc, TurnTimeout: 300 * time.Millisecond}, "ORQ-1")
	e.waitStage("ORQ-1", runstate.StageImplement)
	if n := len(e.cat.CallsOf("dispatch")); n != 1 {
		t.Fatalf("dispatch calls: %d", n)
	}
}

func TestS4HungDispatchWithUnreadableHistoryAsksTheUser(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.Inject = map[string][]string{"dispatch": {"hang"}}
	sc.UnreadableHistory = true
	e := startOne(t, s4Opts{Scenario: sc, TurnTimeout: 300 * time.Millisecond}, "ORQ-1")
	run := e.waitState("ORQ-1", runstate.NeedsInput)
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["reason"] != "dependency_unavailable" || item["question_code"] != "role_dispatch_uncertain" {
		t.Fatal(item)
	}
	if rx := e.roles("ORQ-1"); len(rx) != 1 || rx[0].State != RexAmbiguous {
		t.Fatalf("%+v", rx)
	}
	if len(e.cat.CallsOf("dispatch")) != 0 {
		t.Fatal("nothing may be dispatched while the outcome is unknown")
	}
	// the user retries: the history is readable again, the role is dispatched once
	e.cat.SetScenario(func(s *hostfake.Scenario) { s.UnreadableHistory = false })
	e.answer(item, nextKey(), AnswerInboxRequest{Choice: "retry_dispatch"}, 200)
	e.waitStage("ORQ-1", runstate.StageImplement)
	if n := len(e.cat.CallsOf("dispatch")); n != 1 {
		t.Fatalf("dispatch calls after the retry: %d", n)
	}
}

func TestS4DeviatingHostIsRefusedThenAsksTheUser(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.Inject = map[string][]string{"dispatch": {"no_tool_call", "extra_command"}}
	e := startOne(t, s4Opts{Scenario: sc}, "ORQ-1")
	run := e.waitState("ORQ-1", runstate.NeedsInput)
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["question_code"] != "role_dispatch_uncertain" {
		t.Fatal(item)
	}
	if len(e.cat.CallsOf("dispatch")) != 0 {
		t.Fatal("a deviating host must not dispatch")
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "host.deviation"}) != 2 {
		t.Fatal("deviation events")
	}
	e.answer(item, nextKey(), AnswerInboxRequest{Choice: "retry_dispatch"}, 200)
	e.waitStage("ORQ-1", runstate.StageImplement)
	if n := len(e.cat.CallsOf("dispatch")); n != 1 {
		t.Fatalf("dispatch calls: %d", n)
	}
}

func TestS4CancelDuringALiveRoleCancelsItInCatherd(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.RecordDelayMs = 60000
	e := startOne(t, s4Opts{Scenario: sc}, "ORQ-1")
	defer func() {
		if t.Failed() {
			debugRun(t, e, "ORQ-1")
		}
	}()
	waitFor(t, "dispatched", func() bool {
		r := e.roles("ORQ-1")
		return len(r) == 1 && r[0].State == RexDispatched
	})
	run := e.runState("ORQ-1")
	out := e.act(run, "cancel", CancelRunRequest{}, 200)
	if out["state"] != "cancelled" {
		t.Fatal(out)
	}
	waitFor(t, "role cancelled", func() bool {
		r := e.roles("ORQ-1")
		return len(r) == 1 && r[0].State == RexCancelled
	})
	waitFor(t, "cancellation confirmed", func() bool {
		r := e.runState("ORQ-1")
		return r.Cancellation != nil && r.Cancellation.State == "confirmed"
	})
	v, _ := e.cat.RunsShow(context.Background(), nil, run.CatherdRunID)
	if len(v.Records) != 1 || v.Records[0].Status != "cancelled" {
		t.Fatalf("%+v", v.Records)
	}
	if e.runState("ORQ-1").Slot != nil {
		t.Fatal("the slot must be free")
	}
}

func TestS4PauseStopsNewDispatchesButCollectsLiveRoles(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.RecordDelayMs = 800
	sc.Lanes = nil
	e := startOne(t, s4Opts{Scenario: sc}, "ORQ-1")
	waitFor(t, "dispatched", func() bool {
		r := e.roles("ORQ-1")
		return len(r) == 1 && r[0].State == RexDispatched
	})
	aut := e.f.json("GET", e.orgPath("automation"), nil, nil, 200)
	e.f.json("PUT", e.orgPath("automation"), PutAutomationRequest{Paused: true}, map[string]string{"If-Match": fmt.Sprint(int(aut["revision"].(float64)))}, 200)
	waitFor(t, "architect collected", func() bool {
		r := e.roles("ORQ-1")
		return len(r) == 1 && r[0].State == RexCollected
	})
	time.Sleep(300 * time.Millisecond)
	if n := len(e.cat.CallsOf("dispatch")); n != 1 {
		t.Fatalf("a paused organization dispatched %d roles", n)
	}
}
