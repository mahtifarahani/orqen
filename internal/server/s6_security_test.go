package server

import (
	"context"
	"fmt"
	"orqen/internal/extensions"
	"orqen/internal/mrready"
	"orqen/internal/secscan"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const scanSecret = "sk-test-SENTINEL-scan-key-0123456789abcdef"

type scanEnv struct {
	*cgEnv
	fake *secscan.Fake
	rn   RunDoc
	n    int
	sha  string
}

func newScanEnv(t *testing.T, setup ...func(*App)) *scanEnv {
	cg := newCGEnv(t, setup...)
	e := &scanEnv{cgEnv: cg, fake: &secscan.Fake{}}
	if err := os.MkdirAll(filepath.Join(cg.repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.sha = cg.commit(t, "src/app.py", "line1\nline2\nline3\nline4\n")
	bin := filepath.Join(filepath.Dir(cg.repo), "strix")
	writeScript(t, bin, "#!/bin/sh\necho 'strix 0.9.0'\n")
	e.register("strix", bin)
	e.run("strix", "detect", 202)
	e.run("strix", "test", 202)
	e.json("PUT", e.path("strix", "/credentials/llm_api_key"), map[string]string{"value": scanSecret}, nil, 200)
	e.json("POST", e.path("strix", "/enable"), nil, e.rev("strix"), 200)
	e.a.Scanner = e.fake
	now := time.Now().UTC()
	e.rn = RunDoc{ID: newID("run"), IssueID: "iss-1", OrgID: e.org, ProjectID: e.prj, IssueKey: "ORQ-1", IntentKey: "x", State: "discovered", Active: true, Attempt: 1, Revision: 1, CreatedAt: now, UpdatedAt: now,
		Heads: RunHeads{Reviewed: e.sha}}
	if _, err := e.db.Coll("runs").InsertOne(context.Background(), e.rn); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *scanEnv) scanDoc() SecurityScanDoc {
	d := SecurityScanDoc{ID: newID("scn"), OrgID: e.org, ProjectID: e.prj, RunID: e.rn.ID, HeadSHA: e.sha, TargetKind: "local_clone", State: "queued", CreatedAt: time.Now().UTC(), Revision: 1,
		Config: ScanConfigRef{Model: "fake/model", BudgetMode: "capped", BudgetUSD: 1, MaxTurns: 5}}
	if _, err := e.db.Coll("security_scans").InsertOne(context.Background(), d); err != nil {
		e.t.Fatal(err)
	}
	return d
}

func (e *scanEnv) scan(t *testing.T, scenario string) SecurityScanDoc {
	e.fake.Scenario = secscan.FakeScenario{Scenario: scenario}
	d := e.scanDoc()
	ctx := context.Background()
	mgr, _ := e.a.workspaceManager(e.org, nil, 0)
	fence := func(f func(context.Context) error) error { return e.a.DB.Txn(ctx, f) }
	if err := e.a.scanCore(ctx, e.org, d, e.repo, mgr.Git, e.fake, "strix 0.9.0", scanSecret, fence); err != nil {
		t.Fatal(err)
	}
	out, _ := e.a.scan(ctx, e.org, d.ID)
	return out
}

func TestSecurityScanCompletionAndGateFacts(t *testing.T) {
	e := newScanEnv(t)
	ctx := context.Background()
	d := e.scan(t, "findings")
	if d.State != "finished" || d.Completion != "complete" || d.TreeModified || d.Counts["blocking"].(int32) != 1 {
		t.Fatalf("%+v", d)
	}
	if _, err := os.Stat(filepath.Join(secscan.OrgDir(e.a.Config.DataDir, e.org), d.ID, "target")); err == nil {
		t.Fatal("the disposable target must be removed")
	}
	pv := policyValues{"security.gate": "required"}
	f := e.a.securityFacts(ctx, e.rn, pv)
	if f == nil || f.Gate != mrready.GateRequired || f.ScanHead != e.sha || f.Completion != "complete" || f.BlockingOpen != 1 || f.Running {
		t.Fatalf("%+v", f)
	}
	if e.a.securityFacts(ctx, e.rn, policyValues{"security.gate": "off"}) != nil {
		t.Fatal("gate off builds no facts")
	}
	// waive the blocking finding through the route: the gate input drops to zero
	list := e.json("GET", "/api/v1/orgs/"+e.org+"/runs/"+e.rn.ID+"/security-findings", nil, nil, 200)["items"].([]any)
	if len(list) != 2 {
		t.Fatal(list)
	}
	var blocking map[string]any
	for _, x := range list {
		if m := x.(map[string]any); m["blocking"] == true {
			blocking = m
		}
	}
	if blocking == nil || blocking["source"] != "security" || blocking["severity"] != "major" || blocking["severity_raw"] != "high" || blocking["location_valid"] != true {
		t.Fatal(blocking)
	}
	path := "/api/v1/orgs/" + e.org + "/security-findings/" + blocking["id"].(string)
	e.json("POST", path+"/waive", map[string]string{"text": ""}, map[string]string{"If-Match": "1"}, 422)
	e.json("POST", path+"/waive", map[string]string{"text": "accepted risk"}, map[string]string{"If-Match": "1"}, 200)
	if f = e.a.securityFacts(ctx, e.rn, pv); f.BlockingOpen != 0 {
		t.Fatalf("%+v", f)
	}
	draft := e.json("GET", path+"/issue-draft", nil, nil, 200)["markdown"].(string)
	if !strings.Contains(draft, "SQL injection") || strings.Contains(draft, e.a.Config.DataDir) {
		t.Fatal(draft)
	}
	// a second complete scan with the same findings keeps the waiver and supersedes nothing that reappears
	e.scan(t, "findings")
	if f = e.a.securityFacts(ctx, e.rn, pv); f.BlockingOpen != 0 {
		t.Fatalf("a repeated finding keeps its waiver: %+v", f)
	}
}

func TestSecurityScanOutcomesNeverPassUnlessComplete(t *testing.T) {
	e := newScanEnv(t)
	cases := map[string]string{"budget_stop_exit0": "stopped_budget", "turns_stop": "stopped_turns", "crash": "failed", "no_report": "incomplete", "wrong_schema": "invalid_output", "wrong_target": "incomplete"}
	for scenario, want := range cases {
		d := e.scan(t, scenario)
		if scenario == "wrong_target" {
			want = string(d.Completion) // classifier decides; it must not be complete
			if d.Completion == "complete" {
				t.Fatal("a report for another target must not be complete")
			}
			continue
		}
		if string(d.Completion) != want {
			t.Fatalf("%s: %s, wanted %s", scenario, d.Completion, want)
		}
	}
	// a scan that edits its target is flagged, the diff is an artifact, and nothing reaches the repository
	refs := gitIn(t, e.repo, "status", "--porcelain")
	d := e.scan(t, "modify_tree")
	if !d.TreeModified || d.TreeDiffArtifactID == "" || gitIn(t, e.repo, "status", "--porcelain") != refs {
		t.Fatalf("%+v", d)
	}
	// the LLM key never reaches a document, an artifact or an event
	e.scan(t, "leak_key")
	for _, c := range []string{"security_scans", "security_findings", "events", "artifacts"} {
		cur, _ := e.db.Coll(c).Find(context.Background(), bson.M{})
		var docs []bson.M
		_ = cur.All(context.Background(), &docs)
		if strings.Contains(fmt.Sprint(docs), scanSecret) {
			t.Fatalf("the key leaked into %s", c)
		}
	}
	filepath.Walk(filepath.Join(e.a.Config.DataDir, "artifacts"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), scanSecret) {
				t.Fatal("the key leaked into an artifact blob", p)
			}
		}
		return nil
	})
}

func TestSecurityScanStartRefusals(t *testing.T) {
	e := newScanEnv(t)
	start := func(run string, body map[string]any, status int) map[string]any {
		e.n++
		return e.json("POST", "/api/v1/orgs/"+e.org+"/runs/"+run+"/security-scans", body, map[string]string{"Idempotency-Key": fmt.Sprintf("s-%d", e.n)}, status)
	}
	expectCode(t, start(e.rn.ID, map[string]any{"target_kind": "url"}, 422), "scan_target_unsupported")
	expectCode(t, start(e.rn.ID, map[string]any{"target_kind": "local_clone"}, 422), "security_model_unset")
	// no reviewed head
	now := time.Now().UTC()
	other := RunDoc{ID: newID("run"), IssueID: "iss-2", OrgID: e.org, ProjectID: e.prj, IssueKey: "ORQ-2", IntentKey: "y", State: "discovered", Active: true, Attempt: 1, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := e.db.Coll("runs").InsertOne(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	expectCode(t, start(other.ID, map[string]any{"target_kind": "local_clone"}, 409), "scan_head_unreviewed")
	// disabling the extension makes it unusable
	e.json("POST", e.path("strix", "/disable"), nil, e.rev("strix"), 200)
	expectCode(t, start(e.rn.ID, map[string]any{"target_kind": "local_clone"}, 409), "extension_not_usable")
	m, _ := extensions.Get("strix")
	if _, why, _ := e.a.extUsable(context.Background(), e.org, m, ""); why != "disabled" {
		t.Fatal(why)
	}
	st := e.json("GET", "/api/v1/orgs/"+e.org+"/security/status", nil, nil, 200)
	if st["usable"].(map[string]any)["ok"] != false {
		t.Fatal(st)
	}
}
