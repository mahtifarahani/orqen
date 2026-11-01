package server

import (
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func (e *projectEnv) answer(item map[string]any, key string, body AnswerInboxRequest, status int) map[string]any {
	e.f.t.Helper()
	return e.f.json("POST", e.orgPath("inbox", item["id"].(string), "answer"), body, map[string]string{"If-Match": fmt.Sprint(int(item["revision"].(float64))), "Idempotency-Key": key}, status)
}
func (e *projectEnv) inboxJSON(id string) map[string]any {
	return e.f.json("GET", e.orgPath("inbox", id), nil, nil, 200)
}
func (e *projectEnv) itemOf(key string) map[string]any {
	e.f.t.Helper()
	return e.inboxJSON(e.openItem(key).ID)
}

const goodAcceptance = "Search results must be filtered by the chosen date range and sorted by relevance."

func TestInboxAnswerResumesTheSameRunWithoutDuplicates(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{JQL: ptr("key = ORQ-2"), PlanApproval: "auto"})
	e.poll()
	e.waitState("ORQ-2", "needs_input")
	first, _ := e.runOf("ORQ-2")
	item := e.itemOf("ORQ-2")
	if item["reason"] != "missing_acceptance" || item["status"] != "open" || item["revision"] != float64(1) || item["task_id"] != first.TrackedIssueID {
		t.Fatal(item)
	}
	list := e.f.walk(e.orgPath("inbox"))
	if len(list) != 1 || list[0]["id"] != item["id"] {
		t.Fatal(list)
	}
	if got := e.f.walk(e.orgPath("inbox") + "?reason=policy_conflict"); len(got) != 0 {
		t.Fatal(got)
	}
	// Validation first: nothing changes on a refused answer.
	expectCode(t, e.answer(item, nextKey(), AnswerInboxRequest{Choice: "approve"}, 422), "answer_invalid")
	expectCode(t, e.answer(item, nextKey(), AnswerInboxRequest{Choice: "provide_acceptance"}, 422), "answer_invalid")
	expectCode(t, e.answer(item, nextKey(), AnswerInboxRequest{Choice: "provide_acceptance", Text: ptr("  ")}, 422), "answer_invalid")
	expectCode(t, e.answer(item, nextKey(), AnswerInboxRequest{Choice: "provide_acceptance", Text: ptr(strings.Repeat("a", 4001))}, 422), "answer_invalid")
	expectCode(t, e.answer(item, nextKey(), AnswerInboxRequest{Choice: "provide_acceptance", Text: ptr("bad\x00text")}, 422), "answer_invalid")
	stale := map[string]any{"id": item["id"], "revision": float64(7)}
	conflict := e.answer(stale, nextKey(), AnswerInboxRequest{Choice: "provide_acceptance", Text: ptr(goodAcceptance)}, 409)
	expectCode(t, conflict, "revision_conflict")
	if conflict["current"].(map[string]any)["revision"] != float64(1) {
		t.Fatal(conflict)
	}
	if e.runState("ORQ-2").State != "needs_input" {
		t.Fatal("a refused answer changed the run")
	}
	// The real answer.
	key := nextKey()
	resp := e.f.request("POST", e.orgPath("inbox", item["id"].(string), "answer"), AnswerInboxRequest{Choice: "provide_acceptance", Text: ptr(goodAcceptance)}, map[string]string{"If-Match": "1", "Idempotency-Key": key})
	res := readResponse(t, resp, 200)
	if res["item"].(map[string]any)["status"] != "resolved" || res["item"].(map[string]any)["revision"] != float64(2) || res["run"].(map[string]any)["id"] != first.ID || res["run"].(map[string]any)["attempt"] != float64(1) {
		t.Fatal(res)
	}
	if ans := res["item"].(map[string]any)["answers"].([]any); len(ans) != 1 || ans[0].(map[string]any)["choice"] != "provide_acceptance" || ans[0].(map[string]any)["n"] != float64(1) {
		t.Fatal(ans)
	}
	// The same run continues through the packet and triage to the plan stage.
	run := e.waitHold("ORQ-2", "coding_start")
	if run.ID != first.ID || run.Attempt != 1 || run.State != "planned" || run.SourcesRevision != 2 || run.Triage == nil || run.Triage.Outcome != "execute" || run.Triage.Source != "fixed_rules" {
		t.Fatalf("%+v", run)
	}
	var answered *RunSource
	for i := range run.Sources {
		if run.Sources[i].Code == "missing_acceptance" {
			answered = &run.Sources[i]
		}
	}
	if answered == nil || answered.Text == nil || *answered.Text != goodAcceptance || answered.InboxID == nil || *answered.InboxID != item["id"] || answered.Carried || answered.Kind != "user_answer" || answered.RevisionAdded != 2 {
		t.Fatalf("%+v", run.Sources)
	}
	if !strings.Contains(e.artifactText(run.Context.ArtifactID), goodAcceptance) || run.Context.SourcesRevision != 2 {
		t.Fatal("the rebuilt packet must contain the answer")
	}
	// Replaying the same key gives the identical response, marked as a replay.
	replay := e.f.request("POST", e.orgPath("inbox", item["id"].(string), "answer"), AnswerInboxRequest{Choice: "provide_acceptance", Text: ptr(goodAcceptance)}, map[string]string{"If-Match": "1", "Idempotency-Key": key})
	if replay.Header.Get("Idempotent-Replayed") != "true" || replay.StatusCode != 200 {
		t.Fatal(replay.StatusCode, replay.Header)
	}
	replay.Body.Close()
	// A new key on the resolved item is stale.
	late := e.f.json("POST", e.orgPath("inbox", item["id"].(string), "answer"), AnswerInboxRequest{Choice: "provide_acceptance", Text: ptr(goodAcceptance)}, map[string]string{"If-Match": "1", "Idempotency-Key": nextKey()}, 409)
	expectCode(t, late, "inbox_item_stale")
	if late["current"].(map[string]any)["status"] != "resolved" {
		t.Fatal(late)
	}
	// Over a few seconds there is never a second worker or a second run, and no rule was created from the answer.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n := e.count("jobs", bson.M{"org_id": e.org, "dedupe_key": "run:" + first.ID, "active": true}); n > 1 {
			t.Fatal("two stage workers:", n)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if e.count("runs", bson.M{"org_id": e.org}) != 1 || e.count("pinned_rules", bson.M{"org_id": e.org}) != 0 {
		t.Fatal("duplicate run or rule")
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "run.answered"}) != 1 || e.count("events", bson.M{"org_id": e.org, "type": "inbox.answered"}) != 1 {
		t.Fatal("answer events")
	}
	if len(e.f.walk(e.orgPath("inbox"))) != 0 || len(e.f.walk(e.orgPath("inbox")+"?status=resolved")) != 1 {
		t.Fatal("inbox listing by status")
	}
}

func TestInboxConcurrentAnswersCreateOneWorker(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{JQL: ptr("key = ORQ-6")})
	e.poll()
	e.waitState("ORQ-6", "needs_input")
	item := e.itemOf("ORQ-6")
	if item["question_code"] != "needs_clarification" {
		t.Fatal(item)
	}
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := e.f.request("POST", e.orgPath("inbox", item["id"].(string), "answer"), AnswerInboxRequest{Choice: "clarify", Text: ptr("The DBA has reviewed the migration plan.")}, map[string]string{"If-Match": "1", "Idempotency-Key": nextKey()})
			codes <- resp.StatusCode
			resp.Body.Close()
		}()
	}
	wg.Wait()
	close(codes)
	var ok, conflict int
	for c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal("ok", ok, "conflict", conflict)
	}
	run := e.runState("ORQ-6")
	if n := e.count("jobs", bson.M{"org_id": e.org, "dedupe_key": "run:" + run.ID, "active": true}); n > 1 {
		t.Fatal(n)
	}
	e.waitState("ORQ-6", "needs_input") // the plan gate asks next: sensitive work always needs approval
	if it := e.openItem("ORQ-6"); it.Reason != "plan_approval" || it.Kind != "approval" {
		t.Fatalf("%+v", it)
	}
}

func TestPlanApprovalStartGateAndStart(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{JQL: ptr("key = ORQ-1"), PlanApproval: "manual", CodingStart: "manual", Git: true})
	e.poll()
	e.waitState("ORQ-1", "needs_input")
	item := e.itemOf("ORQ-1")
	run := e.runState("ORQ-1")
	if item["reason"] != "plan_approval" || item["kind"] != "approval" || run.Checkpoint.Stage != "plan_gate" || run.Plan == nil || run.Plan.Approved {
		t.Fatalf("%v %+v", item, run.Plan)
	}
	brief := e.artifactText(run.Plan.ArtifactID)
	for _, want := range []string{`"goal":"Add CSV export to the invoice list"`, `"architect":"sprint4"`, `"test_contract"`, "pnpm", `"likely_files":[]`} {
		if !strings.Contains(brief, want) {
			t.Fatalf("plan brief misses %s: %s", want, brief)
		}
	}
	// request_changes goes back to the plan stage and asks again.
	e.answer(item, nextKey(), AnswerInboxRequest{Choice: "request_changes", Text: ptr("Use semicolons as separators.")}, 200)
	waitFor(t, "second approval", func() bool {
		r := e.runState("ORQ-1")
		return r.State == "needs_input" && r.OpenInboxID != nil && *r.OpenInboxID != item["id"]
	})
	again := e.itemOf("ORQ-1")
	if !strings.Contains(e.artifactText(e.runState("ORQ-1").Plan.ArtifactID), "semicolons") {
		t.Fatal("the requested change must reach the plan constraints")
	}
	e.answer(again, nextKey(), AnswerInboxRequest{Choice: "approve"}, 200)
	held := e.waitHold("ORQ-1", "coding_start")
	if held.State != "planned" || held.Checkpoint.Stage != "start_gate" || held.Plan == nil || !held.Plan.Approved {
		t.Fatalf("%+v", held)
	}
	// Start needs the current revision, an Idempotency-Key and a Ready project.
	runPath := e.orgPath("runs", held.ID)
	expectCode(t, e.f.json("POST", runPath+"/start", nil, map[string]string{"If-Match": "1", "Idempotency-Key": nextKey()}, 409), "revision_conflict")
	expectCode(t, e.f.json("POST", runPath+"/start", nil, map[string]string{"If-Match": fmt.Sprint(held.Revision)}, 422), "validation_failed")
	e.f.json("POST", e.path(e.pid, "automation", "disable"), nil, map[string]string{"If-Match": e.rev(e.pid)}, 200)
	notReady := e.f.json("POST", runPath+"/start", nil, map[string]string{"If-Match": fmt.Sprint(held.Revision), "Idempotency-Key": nextKey()}, 409)
	expectCode(t, notReady, "run_not_startable")
	if errs := notReady["errors"].([]any); len(errs) == 0 || errs[0].(map[string]any)["field"] != "project_not_enabled" {
		t.Fatal(notReady)
	}
	e.enable()
	key := nextKey()
	started := e.f.json("POST", runPath+"/start", nil, map[string]string{"If-Match": fmt.Sprint(held.Revision), "Idempotency-Key": key}, 200)
	if started["hold"] != nil || started["state"] != "planned" {
		t.Fatal(started)
	}
	e.waitState("ORQ-1", "queued")
	// A double Start: the same key replays, a new key finds the run past the gate.
	replay := e.f.request("POST", runPath+"/start", nil, map[string]string{"If-Match": fmt.Sprint(held.Revision), "Idempotency-Key": key})
	if replay.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal(replay.Header)
	}
	replay.Body.Close()
	cur := e.runState("ORQ-1")
	expectCode(t, e.f.json("POST", runPath+"/start", nil, map[string]string{"If-Match": fmt.Sprint(cur.Revision), "Idempotency-Key": nextKey()}, 409), "run_state_conflict")
	if e.count("events", bson.M{"org_id": e.org, "type": "run.resumed", "payload.reason": "started"}) != 1 {
		t.Fatal("Start must have exactly one effect")
	}
}
func (e *projectEnv) waitHold(key, code string) RunDoc {
	e.f.t.Helper()
	waitFor(e.f.t, key+" hold "+code, func() bool { r := e.runState(key); return r.Hold != nil && r.Hold.Code == code })
	return e.runState(key)
}
