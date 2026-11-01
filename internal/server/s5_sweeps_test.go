package server

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestUsageRollupOnTerminalRunsAndRexContext(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{JQL: ptr(`key = ORQ-1`)})
	e.poll()
	run := e.waitStage("ORQ-1", "triage")
	ctx := context.Background()
	_, man, _, err := e.f.a.rolePacket(ctx, run, "worker")
	if err != nil {
		t.Fatal(err)
	}
	e.f.a.recordUsage(ctx, run, "worker", man)
	rc := e.f.a.rexContext(ctx, run, "worker")
	if rc == nil || rc["est_method"] != "bytes_div_4" {
		t.Fatal(rc)
	}
	// An active run gets no outcome.
	if err := e.f.a.rollupUsage(ctx); err != nil {
		t.Fatal(err)
	}
	var u bson.M
	_ = e.f.db.Coll("knowledge_usage").FindOne(ctx, bson.M{"run_id": run.ID}).Decode(&u)
	if _, has := u["outcome"]; has {
		t.Fatal("active run must not be rolled up", u)
	}
	if _, err := e.f.db.Coll("runs").UpdateOne(ctx, bson.M{"_id": run.ID}, bson.M{"$set": bson.M{"state": "merged", "active": false}}); err != nil {
		t.Fatal(err)
	}
	if err := e.f.a.rollupUsage(ctx); err != nil {
		t.Fatal(err)
	}
	var row struct {
		Outcome bson.M `bson:"outcome"`
	}
	if err := e.f.db.Coll("knowledge_usage").FindOne(ctx, bson.M{"run_id": run.ID}).Decode(&row); err != nil || row.Outcome["merged"] != true {
		t.Fatal(err, row)
	}
}

func TestDailyCompactionSweepQueuesOncePerOrgAndDay(t *testing.T) {
	e := newKEnv(t)
	ctx := context.Background()
	if err := e.f.a.sweepCompaction(ctx); err != nil {
		t.Fatal(err)
	}
	count := func() int64 {
		n, _ := e.f.db.Coll("jobs").CountDocuments(ctx, bson.M{"org_id": e.org, "kind": "knowledge_compaction"})
		return n
	}
	if count() != 0 {
		t.Fatal("no experience, no job")
	}
	if _, err := e.f.db.Coll("knowledge_versions").InsertOne(ctx, bson.M{"_id": newID("kvr"), "org_id": e.org, "status": "verified", "type": "experience"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := e.f.a.sweepCompaction(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if count() != 1 {
		t.Fatal("expected exactly one job, got", count())
	}
}

func TestPinnedRuleAppliesToRolesInPackets(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{JQL: ptr(`key = ORQ-1`)})
	e.poll()
	run := e.waitStage("ORQ-1", "triage")
	k := &kenv{f: e.f, org: e.org}
	expectCode(t, e.f.json("POST", k.base()+"/rules", map[string]any{"scope": "org", "title": "T", "body": "b", "critical": true, "applies_to_roles": []string{"nobody"}}, nil, 422), "validation_failed")
	r := e.f.json("POST", k.base()+"/rules", map[string]any{"scope": "org", "title": "Worker only", "body": "Only workers read this sentinel rule", "critical": true, "applies_to_roles": []string{"worker"}}, nil, 201)
	if roles, _ := r["applies_to_roles"].([]any); len(roles) != 1 {
		t.Fatal(r)
	}
	ctx := context.Background()
	wp, _, _, err := e.f.a.rolePacket(ctx, run, "worker")
	if err != nil || !strings.Contains(wp.Text, "Only workers read this sentinel rule") {
		t.Fatal(err, wp.Text)
	}
	rp, _, _, err := e.f.a.rolePacket(ctx, run, "reviewer")
	if err != nil || strings.Contains(rp.Text, "Only workers read this sentinel rule") {
		t.Fatal(err, rp.Text)
	}
}
