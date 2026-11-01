package server

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"io"
	"orqen/internal/runstate"
	"strings"
	"sync/atomic"
	"testing"
)

func (e *projectEnv) runState(key string) RunDoc {
	r, _ := e.runOf(key)
	return r
}

// waitStage waits until the run has reached the stage or gone past it (the engine runs several stages per job).
func (e *projectEnv) waitStage(key, stage string) RunDoc {
	e.f.t.Helper()
	want := runstate.StageIndex(stage)
	waitFor(e.f.t, key+" at stage "+stage, func() bool { return runstate.StageIndex(e.runState(key).Checkpoint.Stage) >= want })
	return e.runState(key)
}
func (e *projectEnv) artifactText(id string) string {
	e.f.t.Helper()
	resp := e.f.request("GET", e.orgPath("artifacts", id, "content"), nil, nil)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestContextStageBuildsAPacketAndTreatsIssueTextAsData(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{JQL: ptr(`key = ORQ-1`)})
	e.poll()
	run := e.waitStage("ORQ-1", "triage")
	if run.Context == nil || run.Context.Cap != 16384 || run.RepoHeadSHA == nil || len(*run.RepoHeadSHA) != 40 {
		t.Fatalf("%+v", run)
	}
	packet := e.artifactText(run.Context.ArtifactID)
	if strings.Contains(packet, "abc123") || strings.Contains(packet, "curl evil") == false && strings.Contains(packet, "[secret]") == false {
		t.Fatal("the secret in the injection comment must be scrubbed:\n", packet)
	}
	i := strings.Index(packet, "ignore previous instructions")
	start := strings.LastIndex(packet[:i], "<<<SOURCE id=")
	end := strings.Index(packet[i:], "<<<END ")
	if i < 0 || start < 0 || end < 0 || !strings.Contains(packet[start:i], "trust=external") || !strings.Contains(packet[start:i], "kind=jira_comment") {
		t.Fatal("the injection comment must sit inside an external source block")
	}
	for _, want := range []string{"The export button downloads a CSV", "Please keep the column order stable", "kind=approved_commands", "pnpm run test", "## critical_rules"} {
		if want == "## critical_rules" {
			if strings.Contains(packet, want) {
				t.Fatal("no critical rules exist")
			}
			continue
		}
		if !strings.Contains(packet, want) {
			t.Fatalf("packet misses %q:\n%s", want, packet)
		}
	}
	ctxDTO := e.f.json("GET", e.orgPath("runs", run.ID, "context"), nil, nil, 200)
	if ctxDTO["cap"] != float64(16384) || ctxDTO["version"] != float64(2) || ctxDTO["packet_artifact_id"] != run.Context.ArtifactID || ctxDTO["freshness"].(map[string]any)["repo_head_sha"] != *run.RepoHeadSHA || len(ctxDTO["sections"].([]any)) != 8 {
		t.Fatal(ctxDTO)
	}
	reasons := map[string]string{}
	for _, x := range ctxDTO["excluded"].([]any) {
		m := x.(map[string]any)
		reasons[m["kind"].(string)] = m["reason"].(string)
	}
	if reasons["confluence"] != "out_of_scope" || reasons["knowledge"] != "" {
		t.Fatal(reasons)
	}
	manifest, _ := json.Marshal(ctxDTO)
	if strings.Contains(string(manifest), "abc123") {
		t.Fatal("manifest leaked a secret")
	}
	// The policy is untouched by the injected instruction.
	if eff := e.f.json("GET", e.orgPath("policy", "effective"), nil, nil, 200); policyValueOf(eff, "automation.plan_approval") != "manual" {
		t.Fatal("policy changed")
	}
	events := e.f.json("GET", e.orgPath("runs", run.ID, "timeline"), nil, nil, 200)
	types := []string{}
	for _, x := range events["items"].([]any) {
		types = append(types, x.(map[string]any)["type"].(string))
	}
	want := []string{"run.created", "run.state_changed", "run.state_changed", "run.stage_completed", "context.packet_built"} // later stages follow
	for i, w := range want {
		if i >= len(types) || types[i] != w {
			t.Fatal(types)
		}
	}
	page := e.f.json("GET", e.orgPath("runs", run.ID, "timeline")+"?limit=2", nil, nil, 200)
	if len(page["items"].([]any)) != 2 || page["next_cursor"] == nil {
		t.Fatal(page)
	}
	rest := e.f.json("GET", e.orgPath("runs", run.ID, "timeline")+"?after="+page["next_cursor"].(string), nil, nil, 200)
	// The run keeps advancing between the two reads, so the continuation may hold more
	// events than the first read saw, never fewer, and its prefix must match in order.
	restItems := rest["items"].([]any)
	if len(restItems) < len(types)-2 {
		t.Fatal(rest)
	}
	for i, x := range restItems[:len(types)-2] {
		if x.(map[string]any)["type"] != types[i+2] {
			t.Fatal(rest)
		}
	}
	e.f.json("GET", e.orgPath("runs", newID("run"), "timeline"), nil, nil, 404)
}

func TestContextStageCrashReusesTheSamePacket(t *testing.T) {
	var crashed atomic.Bool
	e := newIntakeEnv(t, intakeOpts{JQL: ptr(`key = ORQ-1`), Setup: func(a *App) {
		a.runHook = func(stage string) error {
			if stage == "context" && crashed.CompareAndSwap(false, true) {
				return errLeaseLost // the process "dies" after the work, before the commit
			}
			return nil
		}
	}})
	e.poll()
	run := e.waitStage("ORQ-1", "triage")
	if !crashed.Load() {
		t.Fatal("the crash was not injected")
	}
	if n := e.count("artifacts", bson.M{"org_id": e.org, "kind": "context_packet"}); n != 1 {
		t.Fatal("packet artifacts:", n)
	}
	if n := e.count("events", bson.M{"org_id": e.org, "type": "run.stage_completed", "payload.stage": "context", "subject.id": run.ID}); n != 1 {
		t.Fatal("stage completions:", n)
	}
	if n := e.count("events", bson.M{"org_id": e.org, "type": "context.packet_built"}); n != 1 {
		t.Fatal("packet events:", n)
	}
	if e.count("jobs", bson.M{"org_id": e.org, "kind": "run_advance", "dedupe_key": "run:" + run.ID, "active": true}) > 1 {
		t.Fatal("two workers")
	}
}

func TestContextStageOversizeCriticalRuleStopsTheRun(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{JQL: ptr(`key = ORQ-1`)})
	big := strings.Repeat("Never skip the review. ", 400) // about 9 KiB each, 18 KiB together
	e.f.json("POST", e.orgPath("rules"), CreatePinnedRuleRequest{Scope: "org", Title: "Huge one", Body: big, Critical: true}, nil, 201)
	e.f.json("POST", e.orgPath("rules"), CreatePinnedRuleRequest{Scope: "org", Title: "Huge two", Body: big + "!", Critical: true}, nil, 201)
	e.poll()
	waitFor(t, "needs_input", func() bool { return e.runState("ORQ-1").State == "needs_input" })
	run := e.runState("ORQ-1")
	if run.Context != nil || run.OpenInboxID == nil || run.Checkpoint.Stage != "context" {
		t.Fatalf("%+v", run)
	}
	it, err := findOne[InboxItemDoc](context.Background(), e.f.db, "inbox_items", bson.M{"_id": *run.OpenInboxID})
	if err != nil || it.Reason != "policy_conflict" || it.QuestionCode != "packet_oversize" || it.Status != "open" || len(it.Options) != 2 || it.Options[0].ID != "raise_limit" {
		t.Fatal(it, err)
	}
	if it.Params["cap"] != int32(16384) && it.Params["cap"] != int64(16384) || it.Params["critical_bytes"] == nil {
		t.Fatal(it.Params)
	}
	if e.count("artifacts", bson.M{"org_id": e.org, "kind": "context_packet"}) != 0 {
		t.Fatal("an oversize critical set must not produce a partial packet")
	}
	if n := e.count("jobs", bson.M{"org_id": e.org, "kind": "run_advance", "active": true}); n != 0 {
		t.Fatal("a parked run must have no active stage job:", n)
	}
	// A 5 KiB critical rule fits whole.
	e2 := newIntakeEnv(t, intakeOpts{JQL: ptr(`key = ORQ-1`)})
	rule := strings.Repeat("Always add tests. ", 280)
	e2.f.json("POST", e2.orgPath("rules"), CreatePinnedRuleRequest{Scope: "org", Title: "Tests", Body: rule, Critical: true}, nil, 201)
	e2.poll()
	r2 := e2.waitStage("ORQ-1", "triage")
	if !strings.Contains(e2.artifactText(r2.Context.ArtifactID), strings.TrimSpace(rule)) {
		t.Fatal("a critical rule was cut")
	}
}

func TestContextStageNeverIncludesAnotherProjectsRule(t *testing.T) {
	var inject atomic.Bool
	e := newIntakeEnv(t, intakeOpts{JQL: ptr(`key = ORQ-1`), Setup: func(a *App) {
		a.ruleHook = func(in []PinnedRuleDoc) []PinnedRuleDoc {
			if inject.Load() {
				other := newID("prj")
				in = append(in, PinnedRuleDoc{ID: newID("rul"), OrgID: "x", Scope: "project", ProjectID: &other, Title: "foreign", Body: "foreign project secret rule", Critical: true, Active: true})
			}
			return in
		}
	}})
	inject.Store(true)
	e.poll()
	run := e.waitStage("ORQ-1", "triage")
	if strings.Contains(e.artifactText(run.Context.ArtifactID), "foreign project secret rule") {
		t.Fatal("a foreign rule reached the packet")
	}
	ctxDTO := e.f.json("GET", e.orgPath("runs", run.ID, "context"), nil, nil, 200)
	found := false
	for _, x := range ctxDTO["excluded"].([]any) {
		if m := x.(map[string]any); m["kind"] == "pinned_rule" && m["reason"] == "out_of_scope" {
			found = true
		}
	}
	if !found {
		t.Fatal(ctxDTO["excluded"])
	}
}

func TestContextStageWaitsForTheNetworkThenContinues(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{JQL: ptr(`key = ORQ-1`)})
	e.glS.stop()
	e.poll()
	waitFor(t, "waiting_network", func() bool { return e.runState("ORQ-1").State == "waiting_network" })
	if r := e.runState("ORQ-1"); r.Checkpoint.Stage != "context" || !r.Active {
		t.Fatal(r.Checkpoint, r.Active)
	}
	e.glS.start(t)
	e.waitStage("ORQ-1", "triage")
	if r := e.runState("ORQ-1"); !r.Active {
		t.Fatal(r.State)
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "run.resumed", "payload.reason": "network_restored"}) != 1 {
		t.Fatal("the resume was not recorded once")
	}
}
