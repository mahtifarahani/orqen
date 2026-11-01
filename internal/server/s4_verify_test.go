package server

import (
	"context"
	"orqen/internal/runstate"
	"orqen/internal/testfakes/hostfake"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// shellCheck is an approved command that runs a shell script in the verification checkout.
func shellCheck(key, script string) CommandInput {
	return CommandInput{Key: key, Kind: "test", Argv: []string{"/bin/sh", "-c", script}, Approved: ptr(true), TimeoutSecs: ptr(60)}
}

func (e *s4Env) setCommands(cmds ...CommandInput) {
	e.f.t.Helper()
	e.f.json("PUT", e.path(e.pid, "commands"), PutCommandsRequest{Commands: cmds}, map[string]string{"If-Match": e.rev(e.pid)}, 200)
}

func startWith(t *testing.T, o s4Opts, key string, cmds ...CommandInput) *s4Env {
	t.Helper()
	o.JQL = jqlFor(key)
	e := newS4Env(t, o)
	e.setCommands(cmds...)
	e.poll()
	return e
}

func (e *s4Env) evidence(key string) []VerificationEvidenceDoc {
	run, _ := e.runOf(key)
	docs, err := findMany[VerificationEvidenceDoc](context.Background(), e.f.db, "verification_evidence", bson.M{"org_id": e.org, "run_id": run.ID, "kind": "head"}, bson.D{{Key: "created_at", Value: 1}}, 20)
	if err != nil {
		e.f.t.Fatal(err)
	}
	return docs
}

const featureCheck = "grep -q orqen-feature src/feature.txt"

func TestS4VerifyPassesWhenTheCommandsAndTheVerifierPass(t *testing.T) {
	e := startWith(t, s4Opts{}, "ORQ-1", shellCheck("feature", featureCheck+"; pwd"))
	run := e.waitStage("ORQ-1", runstate.StagePublish)
	evs := e.evidence("ORQ-1")
	if len(evs) != 1 || evs[0].Gate.Status != "pass" || evs[0].Verifier == nil || evs[0].Verifier.Verdict != "PASS" || evs[0].HeadSHA != run.Heads.Head {
		t.Fatalf("%+v", evs)
	}
	if run.Heads.Verified != run.Heads.Head || run.Verification == nil || run.Verification.Gate != "pass" || run.State != runstate.PublishingDraft {
		t.Fatalf("%+v", run)
	}
	c := evs[0].Commands[0]
	if c.Outcome != "pass" || c.Sandbox != "enforced" && c.Sandbox != "best_effort" || len(c.OutputSHA256) != 64 {
		t.Fatalf("%+v", c)
	}
	// the commands ran in a detached verification checkout, never in the coder's worktree, and it is gone again
	out := e.artifactText(c.StdoutArtifactID)
	wt := e.worktree("ORQ-1")
	if !strings.Contains(out, "/verify/") || strings.Contains(out, wt) {
		t.Fatalf("checkout path: %q (worktree %s)", out, wt)
	}
	if entries, _ := os.ReadDir(filepath.Join(e.f.a.Config.DataDir, "workspaces", e.org, "verify")); len(entries) != 0 {
		t.Fatalf("verification checkouts left behind: %v", entries)
	}
	if got := e.f.json("GET", e.orgPath("runs", run.ID, "verification"), nil, nil, 200); len(got["items"].([]any)) != 1 {
		t.Fatal(got)
	}
	diff := e.f.json("GET", e.orgPath("runs", run.ID, "diff"), nil, nil, 200)
	if diff["head_sha"] != run.Heads.Head || len(diff["files"].([]any)) != 1 || diff["files"].([]any)[0].(map[string]any)["path"] != "src/feature.txt" {
		t.Fatal(diff)
	}
	e.f.json("GET", e.orgPath("runs", run.ID, "diff")+"?head=0000000", nil, nil, 404)
}

func TestS4WorkerThatLiesFailsTheGateAndGetsAFixRound(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.LieRounds = []int{0}
	e := startWith(t, s4Opts{Scenario: sc}, "ORQ-1", shellCheck("feature", featureCheck))
	run := e.waitStage("ORQ-1", runstate.StagePublish)
	if run.Round != 1 {
		t.Fatalf("round %d", run.Round)
	}
	evs := e.evidence("ORQ-1")
	if len(evs) != 2 || evs[0].Gate.Status != "fail" || evs[1].Gate.Status != "pass" {
		t.Fatalf("%+v", evs)
	}
	// the lie is stored but has no effect
	roles := e.roles("ORQ-1")
	var first, fix RoleExecDoc
	for _, r := range roles {
		if r.Role == "worker" && r.Round == 0 {
			first = r
		}
		if r.Role == "worker" && r.Round == 1 {
			fix = r
		}
	}
	if txt, _ := e.f.a.replyText(context.Background(), first); !strings.Contains(txt, "All tests pass") {
		t.Fatalf("the worker's claim should be stored: %q", txt)
	}
	// the fix round resumed the worker's own thread with the failing output in its brief
	disp := e.cat.CallsOf("dispatch")
	var fixCall map[string]any
	for _, c := range disp {
		if c.Args["name"] == "worker-M1.L1" && c.Args["thread"] != nil {
			fixCall = c.Args
		}
	}
	if fixCall == nil || fixCall["thread"] != first.Catherd.Thread || fix.Catherd == nil || fix.Catherd.Thread != first.Catherd.Thread {
		t.Fatalf("fix call %v first %+v", fixCall, first.Catherd)
	}
	if brief := fixCall["brief"].(string); !strings.Contains(brief, "Fix round 1") || !strings.Contains(brief, "Failing: feature") {
		t.Fatal(brief)
	}
}

func TestS4NewFailureVersusBaselineFailure(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.WorkerEdits = []map[string]string{{"src/broken.txt": "x\n"}, {"src/broken.txt": hostfake.DeleteFile}}
	e := startWith(t, s4Opts{Scenario: sc}, "ORQ-1", shellCheck("nobroken", "test ! -e src/broken.txt"))
	e.waitStage("ORQ-1", runstate.StagePublish)
	evs := e.evidence("ORQ-1")
	if len(evs) != 2 || evs[0].Commands[0].Outcome != "new_failure" || evs[0].Gate.Status != "fail" {
		t.Fatalf("%+v", evs[0])
	}
	if n := e.count("verification_evidence", bson.M{"org_id": e.org, "kind": "baseline"}); n != 1 {
		t.Fatalf("baseline evidence: %d", n)
	}
}

func TestS4BaselineFailureFailsUnlessAllowed(t *testing.T) {
	e := startWith(t, s4Opts{}, "ORQ-1", shellCheck("alwaysred", "exit 1"))
	waitFor(t, "fix rounds exhausted", func() bool { return e.runState("ORQ-1").State == runstate.NeedsInput })
	evs := e.evidence("ORQ-1")
	if len(evs) < 1 || evs[0].Commands[0].Outcome != "baseline_failure" || evs[0].Gate.Status != "fail" {
		t.Fatalf("%+v", evs)
	}
	run := e.runState("ORQ-1")
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["reason"] != "review_disagreement" || item["question_code"] != "fix_rounds_exhausted" {
		t.Fatal(item)
	}
	o := s4Opts{}
	o.Extra = map[string]any{"verification.allow_baseline_failures": true}
	e2 := startWith(t, o, "ORQ-1", shellCheck("alwaysred", "exit 1"))
	e2.waitStage("ORQ-1", runstate.StagePublish)
	if ev := e2.evidence("ORQ-1"); len(ev) != 1 || ev[0].Commands[0].Outcome != "baseline_failure" || ev[0].Gate.Status != "pass" {
		t.Fatalf("%+v", ev)
	}
}

func TestS4DockerCommandWithoutDockerIsIncomplete(t *testing.T) {
	o := s4Opts{}
	o.Setup = func(a *App) { a.dockerCheck = func() bool { return false } }
	cmd := shellCheck("compose", "true")
	cmd.NeedsDocker = true
	e := startWith(t, o, "ORQ-1", cmd)
	run := e.waitState("ORQ-1", runstate.NeedsInput)
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["question_code"] != "verification_incomplete" || e.evidence("ORQ-1")[0].Gate.Status != "incomplete" {
		t.Fatal(item)
	}
}

func TestS4WeakeningStopsAndAnAcceptedWaiverResumes(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.WorkerEdits = []map[string]string{{"pkg/a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { t.Skip(\"later\") }\n"}}
	e := startWith(t, s4Opts{Scenario: sc}, "ORQ-1", shellCheck("ok", "true"))
	run := e.waitState("ORQ-1", runstate.NeedsInput)
	item := e.f.json("GET", e.orgPath("inbox", *run.OpenInboxID), nil, nil, 200)
	if item["reason"] != "protected_change" || item["question_code"] != "test_weakening_suspected" {
		t.Fatal(item)
	}
	opts := map[string]bool{}
	for _, o := range item["options"].([]any) {
		opts[o.(map[string]any)["id"].(string)] = true
	}
	if !opts["send_back"] || !opts["accept"] || !opts["cancel_run"] {
		t.Fatal(opts)
	}
	e.answer(item, nextKey(), AnswerInboxRequest{Choice: "accept"}, 422) // accept needs a text
	e.answer(item, nextKey(), AnswerInboxRequest{Choice: "accept", Text: ptr("the skip is intended: the case needs a service")}, 200)
	e.waitStage("ORQ-1", runstate.StagePublish)
	evs := e.evidence("ORQ-1")
	if len(evs) != 1 || evs[0].Waiver == nil || !strings.Contains(evs[0].Waiver.Text, "intended") || !evs[0].Suspected {
		t.Fatalf("%+v", evs)
	}
}

func TestS4VerifierFailGivesAFixRoundAndBlockedAsksTheUser(t *testing.T) {
	sc := hostfake.DefaultScenario()
	sc.VerifierVerdicts = []string{"FAIL", "PASS"}
	e := startWith(t, s4Opts{Scenario: sc}, "ORQ-1", shellCheck("ok", "true"))
	run := e.waitStage("ORQ-1", runstate.StagePublish)
	if run.Round != 1 {
		t.Fatalf("round %d", run.Round)
	}
	evs := e.evidence("ORQ-1")
	if len(evs) != 2 || evs[0].Verifier.Verdict != "FAIL" || evs[0].Gate.Status != "fail" || evs[1].Verifier.Verdict != "PASS" {
		t.Fatalf("%+v", evs)
	}
	sc2 := hostfake.DefaultScenario()
	sc2.VerifierVerdicts = []string{"BLOCKED"}
	e2 := startWith(t, s4Opts{Scenario: sc2}, "ORQ-1", shellCheck("ok", "true"))
	r2 := e2.waitState("ORQ-1", runstate.NeedsInput)
	item := e2.f.json("GET", e2.orgPath("inbox", *r2.OpenInboxID), nil, nil, 200)
	if item["reason"] != "dependency_unavailable" || item["question_code"] != "verifier_blocked" {
		t.Fatal(item)
	}
}

func TestS4CommandOutputIsScrubbed(t *testing.T) {
	e := startWith(t, s4Opts{}, "ORQ-1", shellCheck("noisy", "echo 'token=abc123 and glpat-ABCDEFGHIJKLMNOP'; echo \"$GITLAB_SENTINEL\"; "+featureCheck))
	e.waitStage("ORQ-1", runstate.StagePublish)
	out := e.artifactText(e.evidence("ORQ-1")[0].Commands[0].StdoutArtifactID)
	if strings.Contains(out, "abc123") || strings.Contains(out, "glpat-ABCDEF") || !strings.Contains(out, "[secret]") {
		t.Fatal(out)
	}
	_ = e.GitlabToken
}
