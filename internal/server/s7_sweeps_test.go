package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// s7Policy stores a pinned policy version with the given resolved values and returns its id.
func s7Policy(t *testing.T, db *DB, org string, kv map[string]any) string {
	t.Helper()
	res := bson.M{}
	for k, v := range kv {
		res[k] = bson.M{"value": v}
	}
	id := newID("pvr")
	d := PolicyVersionDoc{ID: id, OrgID: org, Kind: "resolved", ScopeID: id, Number: 1, Resolved: res, Overrides: bson.M{}, CreatedAt: time.Now().UTC()}
	if _, err := db.Coll("policy_versions").InsertOne(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return d.ID
}

func s7Count(t *testing.T, db *DB, coll string, f bson.M) int {
	t.Helper()
	n, err := db.Coll(coll).CountDocuments(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func s7Sweeps(t *testing.T, a *App) {
	t.Helper()
	ctx := context.Background()
	if err := a.sweepCodeIndexAuto(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.sweepSecurityAuto(ctx); err != nil {
		t.Fatal(err)
	}
}

func s7InsertRun(t *testing.T, db *DB, r RunDoc) RunDoc {
	t.Helper()
	now := time.Now().UTC()
	r.ID, r.Active, r.Attempt, r.Revision, r.CreatedAt, r.UpdatedAt = newID("run"), true, 1, 1, now, now
	r.IssueID, r.IssueKey, r.IntentKey = newID("iss"), "ORQ-7", r.ID
	if _, err := db.Coll("runs").InsertOne(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestS7SweepCodeIndexAuto(t *testing.T) {
	e := newCGEnv(t)
	ctx := context.Background()
	on := s7Policy(t, e.db, e.org, map[string]any{"codegraph.auto_index": "on_run"})
	off := s7Policy(t, e.db, e.org, map[string]any{"codegraph.auto_index": "manual"})
	mk := func(sha, pol string, hold string) RunDoc {
		r := RunDoc{OrgID: e.org, ProjectID: e.prj, State: "planned", RepoHeadSHA: &sha, Pinned: RunPinned{PolicyVersionID: pol}}
		if hold != "" {
			r.Hold = &RunHold{Code: hold, Since: time.Now().UTC()}
		}
		return s7InsertRun(t, e.db, r)
	}
	cix := func(sha string) int {
		return s7Count(t, e.db, "code_indexes", bson.M{"org_id": e.org, "commit_sha": sha})
	}

	// negatives first (each has its own head); none may leave an index or a job
	shaOff := e.commit(t, "a.txt", "off")
	mk(shaOff, off, "dry_run")
	shaRestored := e.commit(t, "b.txt", "restored")
	mk(shaRestored, on, "restored")
	shaFailed := e.commit(t, "c.txt", "failed")
	mk(shaFailed, on, "dry_run")
	failed := CodeIndexDoc{ID: newID("cix"), OrgID: e.org, ProjectID: e.prj, RepoKey: e.key(), CommitSHA: shaFailed, State: "failed", ErrorCode: "index_failed", CreatedAt: time.Now().UTC(), Revision: 1}
	if _, err := e.db.Coll("code_indexes").InsertOne(ctx, failed); err != nil {
		t.Fatal(err)
	}
	s7Sweeps(t, e.a)
	if cix(shaOff) != 0 || cix(shaRestored) != 0 || cix(shaFailed) != 1 || s7Count(t, e.db, "jobs", bson.M{"kind": "code_index"}) != 0 {
		t.Fatal("a skipped candidate must write nothing")
	}
	cur, _ := e.a.codeIndex(ctx, e.org, failed.ID)
	if cur.State != "failed" {
		t.Fatal("a failed index is never retried automatically")
	}

	// the positive case: two sweeps give exactly one index and one job, attributed to the auto trigger
	sha := e.commit(t, "d.txt", "go")
	mk(sha, on, "dry_run")
	s7Sweeps(t, e.a)
	s7Sweeps(t, e.a)
	if cix(sha) != 1 || s7Count(t, e.db, "jobs", bson.M{"kind": "code_index"}) != 1 || s7Count(t, e.db, "events", bson.M{"type": "code_index.auto_queued"}) != 1 {
		t.Fatalf("indexes %d jobs %d", cix(sha), s7Count(t, e.db, "jobs", bson.M{"kind": "code_index"}))
	}
	got, _ := findOne[CodeIndexDoc](ctx, e.db, "code_indexes", bson.M{"commit_sha": sha})
	if got.Trigger != "auto" || got.DTO()["trigger"] != "auto" {
		t.Fatalf("%+v", got)
	}
	// the same dedupe key as the route: the job kind and key
	if s7Count(t, e.db, "jobs", bson.M{"kind": "code_index", "dedupe_key": "code_index:" + got.ID}) != 1 {
		t.Fatal("route and sweep must share the dedupe key")
	}

	// a paused organization and a disabled extension queue nothing
	sha2 := e.commit(t, "e.txt", "paused")
	mk(sha2, on, "dry_run")
	paused := AutomationStateDoc{ID: automationStateID(e.org), OrgID: e.org, Paused: true, Revision: 1, UpdatedAt: time.Now().UTC()}
	if _, err := e.db.Coll("automation_states").InsertOne(ctx, paused); err != nil {
		t.Fatal(err)
	}
	s7Sweeps(t, e.a)
	if cix(sha2) != 0 {
		t.Fatal("a paused organization queues nothing")
	}
	if _, err := e.db.Coll("automation_states").UpdateOne(ctx, bson.M{"_id": paused.ID}, bson.M{"$set": bson.M{"paused": false}}); err != nil {
		t.Fatal(err)
	}
	e.json("POST", e.path("gitnexus", "/disable"), nil, e.rev("gitnexus"), 200)
	s7Sweeps(t, e.a)
	if cix(sha2) != 0 {
		t.Fatal("an unusable extension queues nothing")
	}
}

func TestS7SweepSecurityAuto(t *testing.T) {
	e := newScanEnv(t, func(a *App) { a.dockerCheck = func() bool { return true } })
	ctx := context.Background()
	base := "/api/v1/orgs/" + e.org + "/policy"
	publish := func(ov map[string]any) {
		d := e.json("GET", base+"/draft", nil, nil, 200)
		rev := int(d["revision"].(float64))
		e.json("PUT", base+"/draft", map[string]any{"overrides": ov}, map[string]string{"If-Match": fmt.Sprint(rev)}, 200)
		e.json("POST", base+"/publish", nil, map[string]string{"If-Match": fmt.Sprint(rev + 1), "Idempotency-Key": fmt.Sprintf("p-%d", time.Now().UnixNano())}, 201)
	}
	auto := s7Policy(t, e.db, e.org, map[string]any{"automation.security_scan": "auto"})
	manual := s7Policy(t, e.db, e.org, map[string]any{"automation.security_scan": "manual"})
	mk := func(pol, verdict, reviewHead string) RunDoc {
		return s7InsertRun(t, e.db, RunDoc{OrgID: e.org, ProjectID: e.prj, State: "awaiting_ci", Hold: &RunHold{Code: "dry_run", Since: time.Now().UTC()}, Pinned: RunPinned{PolicyVersionID: pol},
			Heads: RunHeads{Reviewed: e.sha}, Review: &RunReview{Verdict: verdict, Head: reviewHead}})
	}
	scans := func() int { return s7Count(t, e.db, "security_scans", bson.M{"trigger": "auto"}) }

	// model unset: nothing, then the person chooses a model and a capped budget
	pass := mk(auto, "approved", e.sha)
	s7Sweeps(t, e.a)
	if scans() != 0 {
		t.Fatal("no model, no scan")
	}
	publish(map[string]any{"security.model": "fake/model", "security.budget_mode": "capped", "security.budget_usd": 1})

	// not scanned: manual, changes requested, review head differs from the reviewed head
	mk(manual, "approved", e.sha)
	mk(auto, "changes_requested", e.sha)
	mk(auto, "approved", "0000000000000000000000000000000000000000")
	before, _ := e.a.run(ctx, e.org, pass.ID)
	s7Sweeps(t, e.a)
	if n := scans(); n != 1 {
		t.Fatalf("only the passing run is scanned, got %d", n)
	}
	s7Sweeps(t, e.a)
	if n := scans(); n != 1 {
		t.Fatalf("a second sweep must not repeat the scan, got %d", n)
	}
	sc, _ := findOne[SecurityScanDoc](ctx, e.db, "security_scans", bson.M{"trigger": "auto"})
	if sc.RunID != pass.ID || sc.HeadSHA != e.sha || sc.DTO()["trigger"] != "auto" {
		t.Fatalf("%+v", sc)
	}
	// a scan that stopped early still counts: an incomplete auto scan is never repeated
	if _, err := e.db.Coll("security_scans").UpdateOne(ctx, bson.M{"_id": sc.ID}, bson.M{"$set": bson.M{"state": "finished", "completion": "stopped_budget"}}); err != nil {
		t.Fatal(err)
	}
	s7Sweeps(t, e.a)
	if scans() != 1 {
		t.Fatal("a stopped_budget scan must not be repeated")
	}

	// safety: sweeps never write the run, never reach the external write path, and no secret is stored
	after, _ := e.a.run(ctx, e.org, pass.ID)
	if after.Revision != before.Revision {
		t.Fatalf("the sweeps changed the run: %d -> %d", before.Revision, after.Revision)
	}
	if s7Count(t, e.db, "events", bson.M{"type": "security.scan_auto_queued"}) != 1 {
		t.Fatal("one auto-queued event")
	}
	for _, c := range []string{"security_scans", "jobs", "events", "runs", "code_indexes"} {
		cur, _ := e.db.Coll(c).Find(ctx, bson.M{})
		var docs []bson.M
		_ = cur.All(ctx, &docs)
		if strings.Contains(fmt.Sprint(docs), scanSecret) {
			t.Fatalf("the key leaked into %s", c)
		}
	}
}
