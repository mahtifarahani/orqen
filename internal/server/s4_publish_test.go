package server

import (
	"fmt"
	"orqen/internal/extwrite"
	"orqen/internal/runstate"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const testWriteToken = "glpat-WRITE-publish-sentinel"

// wire connects the fake GitLab to the smart-HTTP repositories and gives both the write token.
func (e *s4Env) wire() {
	e.gl.WriteToken = testWriteToken
	e.repo.PushToken = testWriteToken
	e.gl.AutoPipeline = true
	e.gl.LinkRepos(e.repo)
}

// goLive stores the write credential, allowlists the project and switches the organization to live writes.
func (e *s4Env) goLive() {
	e.f.t.Helper()
	e.replaceWriteCred(e.glID, testWriteToken)
	cur := e.xws()
	e.putXws(PutExternalWritesRequest{Mode: "live", ConfirmPhrase: ptr(LivePhrase), Allowlist: []AllowInput{{ConnectionID: e.glID, ProviderProjectID: "1"}}}, revOf(cur), 200)
}

func (e *s4Env) ops(key string) []ExternalOpDoc {
	run, _ := e.runOf(key)
	docs, err := findMany[ExternalOpDoc](t0(), e.f.db, "external_operations", bson.M{"org_id": e.org, "run_id": run.ID}, bson.D{{Key: "created_at", Value: 1}}, 50)
	if err != nil {
		e.f.t.Fatal(err)
	}
	return docs
}

func publishEnvFor(t *testing.T, o s4Opts) *s4Env {
	o.JQL = jqlFor("ORQ-1")
	e := newS4Env(t, o)
	e.wire()
	e.setCommands(shellCheck("ok", "true"))
	return e
}

func (e *s4Env) approveAll(run RunDoc, status int) map[string]any {
	var in []ApproveOpInput
	for _, op := range e.ops(run.IssueKey) {
		if op.State == XopAwaitingApproval {
			in = append(in, ApproveOpInput{ID: op.ID, IntentHash: op.IntentHash})
		}
	}
	return e.f.json("POST", e.orgPath("runs", run.ID, "external-operations", "approve"), ApproveOperationsRequest{Operations: in}, map[string]string{"If-Match": fmt.Sprint(run.Revision), "Idempotency-Key": nextKey()}, status)
}

func TestS4PublishDefaultSettingsWriteNothingAnywhere(t *testing.T) {
	e := publishEnvFor(t, s4Opts{})
	e.poll()
	run := e.waitStage("ORQ-1", runstate.StageReview)
	ops := e.ops("ORQ-1")
	if len(ops) != 2 || ops[0].Action != "git_push" || ops[1].Action != "mr_create" {
		t.Fatalf("%+v", ops)
	}
	for _, op := range ops {
		if op.State != XopDryRun || op.Decision == nil || op.Decision.Outcome != "dry_run" || op.Decision.Reasons[0] != "mode_dry_run" || op.PayloadArtifactID == "" || len(op.IntentHash) != 64 {
			t.Fatalf("%+v", op)
		}
	}
	mr, ok, _ := e.f.a.mrOfRun(t0(), e.org, run.ID)
	if !ok || mr.Mode != "dry_run" || mr.IID != 0 || mr.CurrentHead != run.Heads.Head || !mr.Draft {
		t.Fatalf("%+v", mr)
	}
	if run.PublishMode != "dry_run" || run.Heads.Pushed != "" {
		t.Fatalf("%+v", run)
	}
	// the proof: the fake GitLab saw no write and no push, not even an attempt at one
	if w := e.gl.Writes(); len(w) != 0 {
		t.Fatalf("writes: %+v", w)
	}
	if e.repo.PushCount() != 0 || e.repo.ReceivePackRequests() != 0 {
		t.Fatal("a push reached the fake")
	}
	for _, r := range e.gl.Requests() {
		if strings.Contains(r, "merge_requests") {
			t.Fatalf("the merge request API was touched: %s", r)
		}
	}
	// the exact payload is stored and readable; the description holds no local path
	op := e.f.json("GET", e.orgPath("external-operations", ops[1].ID), nil, nil, 200)
	payload := op["payload"].(map[string]any)
	desc := payload["description"].(string)
	if payload["title"] != "Draft: ORQ-1: "+payload["title"].(string)[len("Draft: ORQ-1: "):] || !strings.Contains(desc, "orqen:run="+run.ID) || strings.Contains(desc, e.f.a.Config.DataDir) || strings.Contains(desc, e.GitlabToken) {
		t.Fatalf("%v", payload)
	}
	if payload["remove_source_branch"] != false || payload["draft"] != true {
		t.Fatal(payload)
	}
	if op["preview"].(map[string]any)["title"] != payload["title"] {
		t.Fatal(op["preview"])
	}
}

func TestS4PublishLiveNeedsAnExactApprovalThenWritesOnce(t *testing.T) {
	e := publishEnvFor(t, s4Opts{})
	e.goLive()
	e.poll()
	run := e.waitState("ORQ-1", runstate.NeedsInput)
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["kind"] != "approval" || item["question_code"] != "publish_approval" || len(item["params"].(map[string]any)["operations"].([]any)) != 2 {
		t.Fatal(item)
	}
	// the generic answer refuses approve_all
	expectCode(t, e.answer(item, nextKey(), AnswerInboxRequest{Choice: "approve_all"}, 422), "answer_invalid")
	if len(e.gl.Writes()) != 0 {
		t.Fatal("nothing may be written before the approval")
	}
	// a stale hash
	ops := e.ops("ORQ-1")
	bad := []ApproveOpInput{{ID: ops[0].ID, IntentHash: strings.Repeat("0", 64)}, {ID: ops[1].ID, IntentHash: ops[1].IntentHash}}
	resp := e.f.request("POST", e.orgPath("runs", run.ID, "external-operations", "approve"), ApproveOperationsRequest{Operations: bad}, map[string]string{"If-Match": fmt.Sprint(run.Revision), "Idempotency-Key": nextKey()})
	expectCode(t, readResponse(t, resp, 409), "intent_changed")
	key := nextKey()
	var in []ApproveOpInput
	for _, op := range ops {
		in = append(in, ApproveOpInput{ID: op.ID, IntentHash: op.IntentHash})
	}
	hdr := map[string]string{"If-Match": fmt.Sprint(run.Revision), "Idempotency-Key": key}
	first := e.f.json("POST", e.orgPath("runs", run.ID, "external-operations", "approve"), ApproveOperationsRequest{Operations: in}, hdr, 200)
	e.waitStage("ORQ-1", runstate.StageReview)
	w := e.gl.Writes()
	pushes, creates := 0, 0
	for _, x := range w {
		if x.TokenKind != "write" {
			t.Fatalf("%+v", x)
		}
		if x.Method == "PUSH" {
			pushes++
		}
		if x.Method == "POST" && strings.HasSuffix(x.Path, "/merge_requests") {
			creates++
		}
	}
	if pushes != 1 || creates != 1 || len(e.gl.MRs(1)) != 1 || !e.gl.MRs(1)[0].Draft {
		t.Fatalf("pushes=%d creates=%d writes=%+v", pushes, creates, w)
	}
	after := e.runState("ORQ-1")
	if after.PublishMode != "live" || after.Heads.Pushed != after.Heads.Head {
		t.Fatalf("%+v", after.Heads)
	}
	mr, _, _ := e.f.a.mrOfRun(t0(), e.org, run.ID)
	if mr.Mode != "live" || mr.IID != 1 || mr.LastPushedSHA != after.Heads.Head {
		t.Fatalf("%+v", mr)
	}
	// replaying the Idempotency-Key gives the same answer and no second write
	again := e.f.json("POST", e.orgPath("runs", run.ID, "external-operations", "approve"), ApproveOperationsRequest{Operations: in}, hdr, 200)
	if fmt.Sprint(again["id"]) != fmt.Sprint(first["id"]) || len(e.gl.Writes()) != len(w) {
		t.Fatal("the replay wrote again or answered differently")
	}
}

func TestS4PublishNotAllowlistedStaysDryRun(t *testing.T) {
	e := publishEnvFor(t, s4Opts{})
	e.goLive()
	if _, err := e.f.db.Coll("external_write_settings").UpdateOne(t0(), bson.M{"org_id": e.org}, bson.M{"$set": bson.M{"allowlist": []XwsAllow{{ConnectionID: e.glID, ProviderProjectID: "999"}}}}); err != nil {
		t.Fatal(err)
	}
	e.poll()
	e.waitStage("ORQ-1", runstate.StageReview)
	for _, op := range e.ops("ORQ-1") {
		if op.State != XopDryRun || op.Decision.Reasons[0] != "not_allowlisted" {
			t.Fatalf("%+v", op.Decision)
		}
	}
	if len(e.gl.Writes()) != 0 {
		t.Fatal("writes")
	}
}

func TestS4PublishKillSwitchBeatsApprovalAndLive(t *testing.T) {
	t.Setenv("ORQEN_EXTERNAL_WRITES", "off")
	e := publishEnvFor(t, s4Opts{})
	e.goLive()
	e.poll()
	run := e.waitStage("ORQ-1", runstate.StageReview)
	for _, op := range e.ops("ORQ-1") {
		if op.State != XopDryRun || op.Decision.Reasons[0] != "kill_switch" {
			t.Fatalf("%+v", op.Decision)
		}
	}
	if len(e.gl.Writes()) != 0 || run.PublishMode != "dry_run" {
		t.Fatal("kill switch")
	}
	g := e.xws()["env_guard"].(map[string]any)
	if g["kill_switch"] != true {
		t.Fatal(g)
	}
}

func TestS4PublishPauseBetweenDecideAndExecuteWritesNothing(t *testing.T) {
	var paused bool
	var e *s4Env
	o := s4Opts{}
	o.Extra = map[string]any{"automation.publish_draft_mr": "auto"}
	o.Setup = func(a *App) {
		a.runHook = func(stage string) error {
			if stage == "pre_execute" && !paused {
				paused = true
				cur := e.xws()
				e.f.json("PUT", e.orgPath("external-writes", "pause"), PutExternalWritesPauseRequest{Paused: true, Reason: ptr("incident")}, map[string]string{"If-Match": fmt.Sprint(revOf(cur))}, 200)
			}
			return nil
		}
	}
	e = publishEnvFor(t, o)
	e.goLive()
	e.poll()
	e.waitStage("ORQ-1", runstate.StageReview)
	if len(e.gl.Writes()) != 0 || e.repo.PushCount() != 0 {
		t.Fatalf("a write slipped through the pause: %+v", e.gl.Writes())
	}
	if !paused {
		t.Fatal("the hook did not run")
	}
}

func TestS4PublishRejectedWriteCredentialAsksTheUser(t *testing.T) {
	o := s4Opts{}
	o.Extra = map[string]any{"automation.publish_draft_mr": "auto"}
	e := publishEnvFor(t, o)
	e.replaceWriteCred(e.glID, "glpat-not-the-write-token")
	cur := e.xws()
	e.putXws(PutExternalWritesRequest{Mode: "live", ConfirmPhrase: ptr(LivePhrase), Allowlist: []AllowInput{{ConnectionID: e.glID, ProviderProjectID: "1"}}}, revOf(cur), 200)
	e.poll()
	run := e.waitState("ORQ-1", runstate.NeedsInput)
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["reason"] != "credential_expired" || item["question_code"] != "write_credential_expired" {
		t.Fatal(item)
	}
	if e.repo.PushCount() != 0 {
		t.Fatal("a push landed with the wrong token")
	}
}

func TestS4PublishRecoversEveryStepWithoutDuplicates(t *testing.T) {
	var calls int
	o := s4Opts{}
	o.Extra = map[string]any{"automation.publish_draft_mr": "auto"}
	o.Setup = func(a *App) {
		a.runHook = func(stage string) error {
			if stage == "extwrite" {
				calls++
				if calls == 1 || calls == 3 {
					return errLeaseLost // the write happened, the commit did not: the process "died"
				}
			}
			return nil
		}
	}
	e := publishEnvFor(t, o)
	e.goLive()
	e.poll()
	e.waitStage("ORQ-1", runstate.StageReview)
	if e.repo.PushCount() != 1 || len(e.gl.MRs(1)) != 1 {
		t.Fatalf("pushes=%d mrs=%d", e.repo.PushCount(), len(e.gl.MRs(1)))
	}
	ops := e.ops("ORQ-1")
	if ops[0].State != XopReconciled || ops[1].State != XopReconciled {
		t.Fatalf("%s %s", ops[0].State, ops[1].State)
	}
}

func TestS4PublishLostResponseIsReconciledByMarker(t *testing.T) {
	old := extwrite.WriteTimeout
	extwrite.WriteTimeout = 600 * time.Millisecond
	defer func() { extwrite.WriteTimeout = old }()
	o := s4Opts{}
	o.Extra = map[string]any{"automation.publish_draft_mr": "auto"}
	o.Setup = func(a *App) {
		a.reconcileDelays = []time.Duration{50 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond}
	}
	e := publishEnvFor(t, o)
	e.gl.HangAfterWrite("/projects/1/merge_requests", 1)
	e.goLive()
	e.poll()
	e.waitStage("ORQ-1", runstate.StageReview)
	if n := len(e.gl.MRs(1)); n != 1 {
		t.Fatalf("%d merge requests", n)
	}
	ops := e.ops("ORQ-1")
	if ops[1].State != XopReconciled || ops[1].Remote.MRIID != 1 {
		t.Fatalf("%+v", ops[1])
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "extwrite.outcome_unknown"}) != 1 || e.count("events", bson.M{"org_id": e.org, "type": "extwrite.reconciled"}) < 1 {
		t.Fatal("events")
	}
}

// A summary note planned while the merge request was still simulated has no iid; when live writes are approved later
// it must find the merge request it belongs to (found in the manual walk: the note answered 404).
func TestS4DryRunNoteFindsTheMergeRequestOnceLive(t *testing.T) {
	e := publishEnvFor(t, liveAutoOpts(nil))
	e.poll()
	e.waitState("ORQ-1", runstate.AwaitingCI)
	run := e.waitHold("ORQ-1", runstate.HoldDryRun)
	e.goLive()
	var in []ApproveOpInput
	for _, op := range e.ops("ORQ-1") {
		if op.State == XopDryRun {
			in = append(in, ApproveOpInput{ID: op.ID, IntentHash: op.IntentHash})
		}
	}
	if len(in) < 3 {
		t.Fatalf("want push, MR and note simulated, got %d", len(in))
	}
	e.f.json("POST", e.orgPath("runs", run.ID, "external-operations", "approve"), ApproveOperationsRequest{Operations: in}, map[string]string{"If-Match": fmt.Sprint(run.Revision), "Idempotency-Key": nextKey()}, 200)
	waitFor(t, "the note is written to the merge request", func() bool {
		mrs := e.gl.MRs(1)
		return len(mrs) == 1 && len(mrs[0].Notes) >= 1
	})
	for _, op := range e.ops("ORQ-1") {
		if op.State == XopFailed {
			t.Fatalf("%s failed: %+v", op.Action, op.Error)
		}
	}
}
