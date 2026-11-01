package server

import (
	"orqen/internal/runstate"
	"orqen/internal/testfakes"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func liveEnv(t *testing.T, extra map[string]any, setup func(*App)) *s4Env {
	o := liveAutoOpts(nil)
	for k, v := range extra {
		o.Extra[k] = v
	}
	o.Setup = func(a *App) {
		a.ciPoll = 100 * time.Millisecond
		a.reconcileDelays = []time.Duration{20 * time.Millisecond}
		if setup != nil {
			setup(a)
		}
	}
	e := publishEnvFor(t, o)
	e.goLive()
	e.poll()
	return e
}

func (e *s4Env) mr(t *testing.T) MergeRequestDoc {
	run, _ := e.runOf("ORQ-1")
	mr, ok, err := e.f.a.mrOfRun(t0(), e.org, run.ID)
	if err != nil || !ok {
		t.Fatal("no merge request", err)
	}
	return mr
}

func okJobs() []testfakes.FakeJob {
	return []testfakes.FakeJob{{Name: "build", Status: "success"}, {Name: "test", Status: "success"}, {Name: "lint", Status: "failed", AllowFailure: true}}
}

func TestS4MRWatchReadyWhenThePipelineOfTheHeadSucceedsThenMerge(t *testing.T) {
	e := liveEnv(t, nil, nil)
	run := e.waitState("ORQ-1", runstate.AwaitingCI)
	head := run.Heads.Head
	waitFor(t, "a readiness evaluation", func() bool { m := e.mr(t); return m.Readiness != nil && m.Pipeline != nil })
	if m := e.mr(t); m.Readiness.Ready || m.Pipeline.SHA != head || m.Pipeline.Status != "running" {
		t.Fatalf("%+v %+v", m.Readiness, m.Pipeline)
	}
	e.gl.SetPipeline(head, "success", okJobs())
	run = e.waitState("ORQ-1", runstate.ReadyForMerge)
	m := e.mr(t)
	if !m.Readiness.Ready || m.Readiness.Head != head || len(m.Readiness.Checks) != 13 {
		t.Fatalf("%+v", m.Readiness)
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "mr.ready", "payload.head": head}) != 1 {
		t.Fatal("mr.ready")
	}
	// a failing pipeline for the head takes the readiness away again
	e.gl.SetPipeline(head, "failed", nil)
	e.waitState("ORQ-1", runstate.AwaitingCI)
	if e.count("events", bson.M{"org_id": e.org, "type": "mr.readiness_lost"}) != 1 {
		t.Fatal("mr.readiness_lost")
	}
	e.gl.SetPipeline(head, "success", okJobs())
	e.waitState("ORQ-1", runstate.ReadyForMerge)
	// merging is the person's: Orqen sees it and ends the run, it writes nothing
	writes := len(e.gl.Writes())
	e.gl.Merge(1, 1)
	end := e.waitState("ORQ-1", runstate.Merged)
	if end.Active || end.Terminal == nil || end.Terminal.Reason != "merged" {
		t.Fatalf("%+v", end)
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "mr.merged"}) != 1 || len(e.gl.Writes()) != writes {
		t.Fatal("merge")
	}
	if e.mr2(t).State != "merged" {
		t.Fatal("merge request state")
	}
}

func (e *s4Env) mr2(t *testing.T) MergeRequestDoc { return e.mr(t) }

func TestS4MRWatchSecondPusherInvalidatesReadiness(t *testing.T) {
	e := liveEnv(t, nil, nil)
	run := e.waitState("ORQ-1", runstate.AwaitingCI)
	e.gl.SetPipeline(run.Heads.Head, "success", okJobs())
	e.waitState("ORQ-1", runstate.ReadyForMerge)
	other, err := e.repo.PushAsOther("group-1/proj-1", e.mr(t).SourceBranch)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the foreign push is seen", func() bool {
		return e.count("events", bson.M{"org_id": e.org, "type": "mr.head_changed", "payload.foreign": true}) == 1
	})
	if m := e.mr(t); m.Readiness != nil && m.Readiness.Ready && m.Readiness.Head == other {
		t.Fatal("readiness must be lost at once")
	}
	// the run verifies and reviews the new head again, then waits for a pipeline of that sha
	waitFor(t, "the new head is reviewed", func() bool {
		r := e.runState("ORQ-1")
		return r.Heads.Head == other && r.Heads.Reviewed == other && r.Heads.Verified == other && r.State == runstate.AwaitingCI && r.Checkpoint.Stage == runstate.StageCI
	})
	revs := 0
	for _, x := range e.roles("ORQ-1") {
		if x.Role == "reviewer" {
			revs++
		}
	}
	if revs != 2 {
		t.Fatalf("reviewers: %d", revs)
	}
	time.Sleep(400 * time.Millisecond)
	if e.runState("ORQ-1").State == runstate.ReadyForMerge {
		t.Fatal("ready without a pipeline for the new head")
	}
	e.gl.SetPipeline(other, "success", okJobs())
	e.waitState("ORQ-1", runstate.ReadyForMerge)
	// no second push of ours was needed: the head was already on the remote
	if e.repo.PushCount() != 2 { // ours and the other person's
		t.Fatalf("pushes: %d", e.repo.PushCount())
	}
	for _, w := range e.gl.Writes() {
		if w.Method == "PUSH" && w.TokenKind == "write" && strings.HasSuffix(w.Path, other) {
			t.Fatal("Orqen pushed the foreign head")
		}
	}
}

func TestS4MRWatchRewrittenHistoryAsksTheUser(t *testing.T) {
	e := liveEnv(t, nil, nil)
	run := e.waitState("ORQ-1", runstate.AwaitingCI)
	e.gl.SetPipeline(run.Heads.Head, "success", okJobs())
	e.waitState("ORQ-1", runstate.ReadyForMerge)
	if _, err := e.repo.PushRewritten("group-1/proj-1", e.mr(t).SourceBranch); err != nil {
		t.Fatal(err)
	}
	cur := e.waitState("ORQ-1", runstate.NeedsInput)
	item := e.f.json("GET", e.orgPath("inbox", *cur.OpenInboxID), nil, nil, 200)
	if item["reason"] != "policy_conflict" || item["question_code"] != "branch_rewritten" {
		t.Fatal(item)
	}
	if m := e.mr(t); m.Readiness == nil || m.Readiness.Ready {
		t.Fatal("not ready after a rewrite")
	}
	e.answer(item, nextKey(), AnswerInboxRequest{Choice: "adopt_remote"}, 200)
	waitFor(t, "adopted and reviewed again", func() bool {
		r := e.runState("ORQ-1")
		return r.Heads.Head == r.Heads.Reviewed && r.Heads.Head != run.Heads.Head && r.Checkpoint.Stage == runstate.StageCI
	})
}

func TestS4MRWatchClosedMergeRequestCancelsTheRun(t *testing.T) {
	e := liveEnv(t, nil, nil)
	e.waitState("ORQ-1", runstate.AwaitingCI)
	waitFor(t, "watching", func() bool { return e.mr(t).Readiness != nil })
	e.gl.CloseMR(1, 1)
	end := e.waitState("ORQ-1", runstate.Cancelled)
	if end.Terminal == nil || end.Terminal.Reason != "mr_closed_by_user" || end.Active {
		t.Fatalf("%+v", end)
	}
}

func TestS4MRWatchCITimeoutAsksTheUser(t *testing.T) {
	e := liveEnv(t, nil, func(a *App) { a.ciMaxWait = 400 * time.Millisecond })
	e.waitState("ORQ-1", runstate.AwaitingCI)
	run := e.waitState("ORQ-1", runstate.NeedsInput)
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["reason"] != "dependency_unavailable" || item["question_code"] != "ci_timeout" {
		t.Fatal(item)
	}
}

func TestS4MRWatchNoCIPolicyAfterTheGracePeriod(t *testing.T) {
	e := liveEnv(t, map[string]any{"ci.require_pipeline": false, "ci.no_pipeline_grace_secs": 0}, nil)
	_ = e
	// AutoPipeline gives the push a running pipeline; with the policy a pipeline that exists must still pass
	e.waitState("ORQ-1", runstate.AwaitingCI)
	time.Sleep(500 * time.Millisecond)
	if e.runState("ORQ-1").State == runstate.ReadyForMerge {
		t.Fatal("a running pipeline must be waited for even under the no-CI policy")
	}
	head := e.runState("ORQ-1").Heads.Head
	e.gl.SetPipeline(head, "success", okJobs())
	e.waitState("ORQ-1", runstate.ReadyForMerge)
}
