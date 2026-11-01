package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"orqen/internal/readiness"
	"orqen/internal/testfakes"
)

type docsEnv struct {
	*kenv
	fake *testfakes.Confluence
	conn string
}

func newDocsEnv(t *testing.T) *docsEnv {
	e := &docsEnv{kenv: newKEnv(t)}
	e.fake = testfakes.NewConfluenceCloud("/cloud")
	srv := httptest.NewServer(e.fake.Handler())
	t.Cleanup(srv.Close)
	e.conn = e.f.json("POST", e.base()+"/connections", CreateConnectionRequest{Name: "cloudwiki", Provider: "confluence", Deployment: "cloud", BaseURL: srv.URL + "/cloud/wiki", AuthMethod: "api_token", AuthUsername: ptr(e.fake.User)}, nil, 201)["id"].(string)
	e.f.json("PUT", e.base()+"/connections/"+e.conn+"/credential", bson.M{"secret": e.fake.Token}, map[string]string{"If-Match": "1"}, 200)
	return e
}

func (e *docsEnv) project(pages ...string) ProjectDoc {
	refs := []ConfluenceRef{}
	for _, p := range pages {
		p := p
		refs = append(refs, ConfluenceRef{ConnectionID: e.conn, PageID: &p})
	}
	now := time.Now().UTC()
	p := ProjectDoc{ID: newID("prj"), OrgID: e.org, Name: "docs", NameKey: newID("k"), Active: true, Mapping: Mapping{Confluence: refs}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := e.f.db.Coll("projects").InsertOne(context.Background(), p); err != nil {
		e.f.t.Fatal(err)
	}
	return p
}

func (e *docsEnv) syncNow(p ProjectDoc) {
	e.f.t.Helper()
	err := e.f.a.txnRetryJob(context.Background(), func(sc context.Context) error {
		return e.f.a.ensureJobTx(sc, e.org, "source_sync", "source_sync:"+p.ID, 3, bson.M{"project_id": p.ID})
	})
	if err != nil {
		e.f.t.Fatal(err)
	}
	if !e.f.a.runJobNow(e.f.t, "source_sync") {
		e.f.t.Fatal("source_sync job")
	}
}

func (e *docsEnv) warnings(p ProjectDoc) map[string]bool {
	cur, err := e.f.a.project(context.Background(), e.org, p.ID)
	if err != nil {
		e.f.t.Fatal(err)
	}
	res := readiness.Evaluate(e.f.a.readinessInputs(cur, readinessShared{}))
	out := map[string]bool{}
	for _, w := range res.Warnings {
		out[w.Code] = true
	}
	for _, b := range res.Blockers {
		if b.Code == "confluence_refs_unverified" || b.Code == "confluence_ref_unreachable" || b.Code == "knowledge_index_fallback" {
			e.f.t.Fatalf("%s must never block", b.Code)
		}
	}
	return out
}

func TestS7DocsSyncResultsDriveReadinessWarnings(t *testing.T) {
	e := newDocsEnv(t)
	p := e.project("1001", "9999")
	if w := e.warnings(p); !w["confluence_refs_unverified"] {
		t.Fatalf("before any sync the references are unverified: %v", w)
	}
	e.syncNow(p)
	w := e.warnings(p)
	if !w["confluence_ref_unreachable"] || !w["confluence_refs_unverified"] {
		t.Fatalf("a page that answers 404 gives both warnings: %v", w)
	}
	st, err := findOne[SourceSyncStateDoc](context.Background(), e.f.db, "source_sync_states", bson.M{"_id": p.ID})
	if err != nil || len(st.Refs) != 2 || !st.Refs[0].OK || st.Refs[1].OK || st.Refs[1].Code == "" {
		t.Fatalf("%+v %v", st, err)
	}
	// fix the reference and sync again: both warnings clear
	fixed := "1001"
	if _, err = e.f.db.Coll("projects").UpdateOne(context.Background(), bson.M{"_id": p.ID}, bson.M{"$set": bson.M{"mapping.confluence": []ConfluenceRef{{ConnectionID: e.conn, PageID: &fixed}}}}); err != nil {
		t.Fatal(err)
	}
	e.syncNow(p)
	if w = e.warnings(p); w["confluence_ref_unreachable"] || w["confluence_refs_unverified"] {
		t.Fatalf("both warnings clear after a good sync: %v", w)
	}
}

func (e *docsEnv) publishOrgPolicy(overrides map[string]any) {
	e.f.t.Helper()
	base := e.base() + "/policy"
	draft := e.f.json("GET", base+"/draft", nil, nil, 200)
	rev := int(draft["revision"].(float64))
	e.f.json("PUT", base+"/draft", map[string]any{"overrides": overrides}, map[string]string{"If-Match": fmt.Sprint(rev)}, 200)
	e.f.json("POST", base+"/publish", nil, map[string]string{"If-Match": fmt.Sprint(rev + 1), "Idempotency-Key": nextKey()}, 201)
}

func TestS7SweepSourceSyncHonoursIntervalAndPause(t *testing.T) {
	e := newDocsEnv(t)
	ctx := context.Background()
	p := e.project("1001")
	jobs := func() int { return e.count("jobs", bson.M{"org_id": e.org, "kind": "source_sync", "active": true}) }
	clear := func() {
		if _, err := e.f.db.Coll("jobs").DeleteMany(ctx, bson.M{"org_id": e.org, "kind": "source_sync"}); err != nil {
			t.Fatal(err)
		}
	}
	sweep := func() {
		if err := e.f.a.sweepSourceSync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	lastSync := func(ago time.Duration) {
		if _, err := e.f.db.Coll("source_sync_states").UpdateOne(ctx, bson.M{"_id": p.ID}, bson.M{"$set": bson.M{"org_id": e.org, "refs": []any{}, "last_sync_at": time.Now().UTC().Add(-ago)}}, options.UpdateOne().SetUpsert(true)); err != nil {
			t.Fatal(err)
		}
	}
	e.publishOrgPolicy(map[string]any{"docs.sync_interval_secs": 60})
	lastSync(30 * time.Second)
	sweep()
	if jobs() != 0 {
		t.Fatal("a recent sync queues nothing")
	}
	lastSync(5 * time.Minute)
	sweep()
	sweep()
	if jobs() != 1 {
		t.Fatalf("one job across two sweeps, got %d", jobs())
	}
	clear()
	// automation paused for the organization: nothing is queued
	if _, err := e.f.db.Coll("automation_states").UpdateOne(ctx, bson.M{"_id": automationStateID(e.org)}, bson.M{"$set": bson.M{"org_id": e.org, "paused": true}, "$setOnInsert": bson.M{"revision": int64(1)}}, options.UpdateOne().SetUpsert(true)); err != nil {
		t.Fatal(err)
	}
	sweep()
	if jobs() != 0 {
		t.Fatal("a paused organization queues nothing")
	}
	if _, err := e.f.db.Coll("automation_states").UpdateOne(ctx, bson.M{"_id": automationStateID(e.org)}, bson.M{"$set": bson.M{"paused": false}}); err != nil {
		t.Fatal(err)
	}
	// interval 0 turns the automatic sync off
	e.publishOrgPolicy(map[string]any{"docs.sync_interval_secs": 0})
	sweep()
	if jobs() != 0 {
		t.Fatal("interval 0 queues nothing")
	}
}
