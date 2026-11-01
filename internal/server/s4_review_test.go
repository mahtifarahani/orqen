package server

import (
	"fmt"
	"orqen/internal/runstate"
	"orqen/internal/testfakes/hostfake"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func liveAutoOpts(sc *hostfake.Scenario) s4Opts {
	o := s4Opts{Scenario: sc}
	o.Extra = map[string]any{"automation.publish_draft_mr": "auto", "automation.publish_comments": "auto"}
	o.JQL = jqlFor("ORQ-1")
	return o
}

func (e *s4Env) findings(key string) []ReviewFindingDoc {
	run, _ := e.runOf(key)
	docs, _ := findMany[ReviewFindingDoc](t0(), e.f.db, "review_findings", bson.M{"org_id": e.org, "run_id": run.ID}, bson.D{{Key: "round", Value: 1}, {Key: "finding_key", Value: 1}}, 50)
	return docs
}

func TestS4ReviewApprovesAndTheDryRunEndsAtAwaitingCI(t *testing.T) {
	e := publishEnvFor(t, s4Opts{})
	e.poll()
	e.waitState("ORQ-1", runstate.AwaitingCI)
	run := e.waitHold("ORQ-1", runstate.HoldDryRun) // the hold is set a moment after the state changes
	if run.Hold == nil || run.Hold.Code != runstate.HoldDryRun || run.Checkpoint.Stage != runstate.StageCI || run.Slot != nil {
		t.Fatalf("%+v", run)
	}
	if run.Review == nil || run.Review.Verdict != "approved" || run.Review.Independence.Status != "independent" || run.Heads.Reviewed != run.Heads.Head {
		t.Fatalf("%+v", run.Review)
	}
	roles := e.roles("ORQ-1")
	var rev RoleExecDoc
	for _, r := range roles {
		if r.Role == "reviewer" {
			rev = r
		}
	}
	if rev.Name != "reviewer-M1-r1" || rev.Catherd.Family != "gpt-6.1-sol" {
		t.Fatalf("%+v", rev)
	}
	if len(e.cat.CallsOf("land")) != 1 {
		t.Fatalf("land calls: %d", len(e.cat.CallsOf("land")))
	}
	// the comment operations exist as dry-run artifacts, nothing was posted
	var notes int
	for _, op := range e.ops("ORQ-1") {
		if op.Action == "mr_note" {
			notes++
			if op.State != XopDryRun {
				t.Fatalf("%+v", op)
			}
		}
	}
	if notes != 1 || len(e.gl.Writes()) != 0 {
		t.Fatalf("notes=%d writes=%v", notes, e.gl.Writes())
	}
	// the simulated merge request shows why it can never be ready
	mr, _, _ := e.f.a.mrOfRun(t0(), e.org, run.ID)
	dto := e.f.json("GET", e.orgPath("merge-requests", mr.ID), nil, nil, 200)
	rd := dto["readiness"].(map[string]any)
	if rd["ready"] != false || dto["mode"] != "dry_run" || dto["iid"] != nil {
		t.Fatal(dto)
	}
	first := rd["checks"].([]any)[0].(map[string]any)
	if first["code"] != "remote_mr" || first["status"] != "fail" {
		t.Fatal(first)
	}
}

func TestS4BlockingFindingGivesAFixRoundWithAFreshReviewer(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.Reviews = []hostfake.ReviewSpec{{Verdict: "changes_requested", Findings: []hostfake.FindingSpec{{Severity: "major", File: "src/feature.txt", Line: 1, Title: "Missing newline", Body: "add one", AcceptanceRef: "A1"}}}, {Verdict: "approved"}}
	e := publishEnvFor(t, s4Opts{Scenario: sc})
	e.poll()
	run := e.waitState("ORQ-1", runstate.AwaitingCI)
	if run.Round != 1 || run.Review.Round != 1 || run.Review.Verdict != "approved" {
		t.Fatalf("round=%d %+v", run.Round, run.Review)
	}
	var revs, workers []RoleExecDoc
	for _, r := range e.roles("ORQ-1") {
		switch r.Role {
		case "reviewer":
			revs = append(revs, r)
		case "worker":
			workers = append(workers, r)
		}
	}
	if len(revs) != 2 || revs[0].Name != "reviewer-M1-r1" || revs[1].Name != "reviewer-M1-r2" || revs[0].Catherd.Thread == revs[1].Catherd.Thread {
		t.Fatalf("%+v", revs)
	}
	if len(workers) != 2 || workers[1].Catherd.Thread != workers[0].Catherd.Thread {
		t.Fatalf("the fix round must resume the worker's thread: %+v", workers)
	}
	// the fix brief carries the finding
	found := false
	for _, c := range e.cat.CallsOf("dispatch") {
		if c.Args["thread"] != nil && c.Args["name"] == "worker-M1.L1" && strings.Contains(c.Args["brief"].(string), "Missing newline") {
			found = true
		}
	}
	if !found {
		t.Fatal("the finding did not reach the worker")
	}
	fs := e.findings("ORQ-1")
	if len(fs) != 1 || fs[0].Round != 0 || !fs[0].Blocking || !fs[0].InlineEligible || !fs[0].LocationValid {
		t.Fatalf("%+v", fs)
	}
}

func TestS4FixRoundsExhaustedAskAndAWaiverLands(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.Reviews = []hostfake.ReviewSpec{{Verdict: "changes_requested", Findings: []hostfake.FindingSpec{{Severity: "blocker", File: "src/feature.txt", Line: 1, Title: "Wrong", Body: "x"}}}}
	e := publishEnvFor(t, s4Opts{Scenario: sc})
	e.poll()
	run := e.waitState("ORQ-1", runstate.NeedsInput)
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["reason"] != "review_disagreement" || item["question_code"] != "fix_rounds_exhausted" || run.Round != 2 {
		t.Fatalf("round %d %v", run.Round, item)
	}
	opts := map[string]bool{}
	for _, o := range item["options"].([]any) {
		opts[o.(map[string]any)["id"].(string)] = true
	}
	if !opts["another_round"] || !opts["waive"] || !opts["cancel_run"] {
		t.Fatal(opts)
	}
	expectCode(t, e.answer(item, nextKey(), AnswerInboxRequest{Choice: "waive"}, 422), "answer_invalid")
	e.answer(item, nextKey(), AnswerInboxRequest{Choice: "waive", Text: ptr("accepted by the owner")}, 200)
	e.waitState("ORQ-1", runstate.AwaitingCI)
	end := e.waitHold("ORQ-1", runstate.HoldDryRun)
	if end.Hold == nil || len(e.cat.CallsOf("land")) != 1 {
		t.Fatalf("%+v", end)
	}
	waived := 0
	for _, f := range e.findings("ORQ-1") {
		if f.Status == FindingWaived && f.Waiver != nil && f.Waiver.Text == "accepted by the owner" {
			waived++
		}
	}
	if waived != 1 || len(e.findings("ORQ-1")) != 3 {
		t.Fatalf("waived %d", waived)
	}
}

func TestS4SingleFindingCanBeWaivedThroughTheAPI(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.Reviews = []hostfake.ReviewSpec{{Verdict: "changes_requested", Findings: []hostfake.FindingSpec{{Severity: "minor", File: "src/feature.txt", Line: 1, Title: "Nit", Body: "x"}}}}
	e := publishEnvFor(t, s4Opts{Scenario: sc})
	e.poll()
	e.waitState("ORQ-1", runstate.NeedsInput)
	run := e.runState("ORQ-1")
	fs := e.findings("ORQ-1")
	hdr := func(f ReviewFindingDoc) map[string]string {
		return map[string]string{"If-Match": fmt.Sprint(f.Revision), "Idempotency-Key": nextKey()}
	}
	// a minor finding cannot be waived (it never blocked)
	expectCode(t, e.f.json("POST", e.orgPath("runs", run.ID, "findings", fs[0].ID, "waive"), WaiveFindingRequest{Reason: "x"}, hdr(fs[0]), 409), "finding_not_waivable")
}

func TestS4ReviewerOnTheCodersFamilyIsRefused(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.Rungs = map[string]string{"reviewer": "codex:gpt-6-sol#high"} // the worker's own family
	e := publishEnvFor(t, s4Opts{Scenario: sc})
	e.poll()
	run := e.waitState("ORQ-1", runstate.NeedsInput)
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["reason"] != "model_unavailable" || item["question_code"] != "reviewer_not_independent" {
		t.Fatal(item)
	}
	sc2 := hostfake.DefaultScenario()
	sc2.FailoverTo = map[string]string{"reviewer": "codex:gpt-6-sol#low"} // a stand-in of the coder's family ran the review
	e2 := publishEnvFor(t, s4Opts{Scenario: sc2})
	e2.poll()
	r2 := e2.waitState("ORQ-1", runstate.NeedsInput)
	item = e2.f.json("GET", e2.orgPath("inbox", *r2.OpenInboxID), nil, nil, 200)
	if item["question_code"] != "reviewer_not_independent" {
		t.Fatal(item)
	}
}

func TestS4UnknownReviewerRungRunsButCanNeverBeReady(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.Rungs = map[string]string{"reviewer": "codex:mystery-model#high"}
	e := publishEnvFor(t, s4Opts{Scenario: sc})
	e.poll()
	run := e.waitState("ORQ-1", runstate.AwaitingCI)
	if run.Review.Independence.Status != "unknown" {
		t.Fatalf("%+v", run.Review.Independence)
	}
	mr, _, _ := e.f.a.mrOfRun(t0(), e.org, run.ID)
	dto := e.f.json("GET", e.orgPath("merge-requests", mr.ID), nil, nil, 200)
	for _, c := range dto["readiness"].(map[string]any)["checks"].([]any) {
		if c.(map[string]any)["code"] == "independence" && c.(map[string]any)["status"] != "fail" {
			t.Fatal(c)
		}
	}
}

func TestS4MalformedReviewIsRetriedOnceThenAsks(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.MalformedReviews = 1
	e := publishEnvFor(t, s4Opts{Scenario: sc})
	e.poll()
	e.waitState("ORQ-1", runstate.AwaitingCI)
	var names []string
	for _, r := range e.roles("ORQ-1") {
		if r.Role == "reviewer" {
			names = append(names, r.Name)
		}
	}
	if strings.Join(names, ",") != "reviewer-M1-r1,reviewer-M1-r1-c1" {
		t.Fatal(names)
	}
	sc2 := hostfake.DefaultScenario()
	sc2.MalformedReviews = 2
	e2 := publishEnvFor(t, s4Opts{Scenario: sc2})
	e2.poll()
	run := e2.waitState("ORQ-1", runstate.NeedsInput)
	item := e2.f.json("GET", e2.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["reason"] != "review_disagreement" || item["question_code"] != "review_unparseable" {
		t.Fatal(item)
	}
}

func TestS4LiveCommentsAreInlineOnlyOnDiffLinesAndAdoptedAfterACrash(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.Reviews = []hostfake.ReviewSpec{{Verdict: "approved", Findings: []hostfake.FindingSpec{
		{ID: "A", Severity: "minor", File: "src/feature.txt", Line: 1, Title: "On a changed line", Body: "inline"},
		{ID: "B", Severity: "nit", File: "package.json", Line: 1, Title: "On an unchanged line", Body: "general"},
	}}}
	var calls int
	o := liveAutoOpts(sc)
	o.Setup = func(a *App) {
		a.runHook = func(stage string) error {
			if stage == "extwrite" {
				calls++
				if calls == 4 { // the push, the merge request, the summary note, then the first discussion "dies" after posting
					return errLeaseLost
				}
			}
			return nil
		}
	}
	e := publishEnvFor(t, o)
	e.goLive()
	e.poll()
	run := e.waitState("ORQ-1", runstate.AwaitingCI)
	if run.Hold != nil && run.Hold.Code == runstate.HoldDryRun {
		t.Fatal("live runs do not hold as dry runs")
	}
	mrs := e.gl.MRs(1)
	if len(mrs) != 1 || len(mrs[0].Notes) != 1 || len(mrs[0].Discussions) != 2 {
		t.Fatalf("%+v", mrs)
	}
	inline, general := 0, 0
	for _, d := range mrs[0].Discussions {
		if d.Notes[0].Position != nil {
			inline++
			if !strings.Contains(d.Notes[0].Body, "On a changed line") {
				t.Fatal(d.Notes[0].Body)
			}
		} else {
			general++
			if !strings.Contains(d.Notes[0].Body, "`package.json:1`") {
				t.Fatal(d.Notes[0].Body)
			}
		}
	}
	if inline != 1 || general != 1 {
		t.Fatalf("inline=%d general=%d", inline, general)
	}
	for _, d := range mrs[0].Discussions {
		if !strings.Contains(d.Notes[0].Body, "<!-- orqen:run="+run.ID) {
			t.Fatal("every comment carries a marker")
		}
	}
	if calls < 5 {
		t.Fatal("the crash was not injected")
	}
}
