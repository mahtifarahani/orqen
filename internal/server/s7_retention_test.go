package server

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"orqen/internal/extensions"
	"orqen/internal/kindex"
	"orqen/internal/secscan"
)

func TestS7WikiRetentionAndFallbackPurge(t *testing.T) {
	e := newKEnv(t)
	ctx := context.Background()
	src := e.source(e.org, "Doc", "Gnu migration notes are kept here.")
	e.verified("gnu", "Gnu migration notes are kept here", src)
	rows, _ := findMany[bson.M](ctx, e.f.db, "wiki_outbox", bson.M{"org_id": e.org}, bson.D{{Key: "seq", Value: 1}}, 10)
	if len(rows) == 0 {
		t.Fatal("verification must leave an outbox row")
	}
	old := rows[0]["_id"].(string)
	if _, err := e.f.db.Coll("wiki_outbox").UpdateOne(ctx, bson.M{"_id": old}, bson.M{"$set": bson.M{"created_at": time.Now().UTC().Add(-40 * 24 * time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	fresh := bson.M{"_id": newID("wob"), "org_id": e.org, "seq": int64(999), "op": "upsert", "version_id": "x", "record_id": "y", "content_hash": "h", "state": "pending", "attempts": 0, "created_at": time.Now().UTC()}
	if _, err := e.f.db.Coll("wiki_outbox").InsertOne(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	// no Wiki configured: the sweep drops the old pending row, keeps the recent one and asks for a rebuild
	if err := e.f.a.sweepWiki(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count("wiki_outbox", bson.M{"_id": old}); n != 0 {
		t.Fatal("the old pending row must be deleted")
	}
	if n := e.count("wiki_outbox", bson.M{"_id": fresh["_id"]}); n != 1 {
		t.Fatal("a recent pending row is kept")
	}
	if !e.f.a.wikiState(ctx, e.org).RebuildRequested {
		t.Fatal("rebuild_requested must be set")
	}
	if n := e.count("knowledge_versions", bson.M{"org_id": e.org, "status": "verified"}); n != 1 {
		t.Fatal("knowledge itself is never touched")
	}
	// with a Wiki, the rebuild projects the verified version and an applied row's purge date follows the policy days
	fake := kindex.NewFake()
	e.f.a.Index = fake
	if err := e.f.a.maintainWiki(ctx, e.org); err != nil {
		t.Fatal(err)
	}
	applied, _ := findMany[bson.M](ctx, e.f.db, "wiki_outbox", bson.M{"org_id": e.org, "state": "applied"}, bson.D{{Key: "seq", Value: 1}}, 10)
	if len(applied) == 0 {
		t.Fatal("rows applied")
	}
	for _, r := range applied {
		d := time.Until(r["purge_at"].(interface{ Time() time.Time }).Time())
		if d < 29*24*time.Hour || d > 31*24*time.Hour {
			t.Fatalf("purge_at is %v away, wanted the policy's 30 days", d)
		}
	}
	if r := e.search("gnu"); r["mode"] != "wiki" {
		t.Fatal(r)
	}
}

func TestS7GraphRetentionSandboxAndScanTimeout(t *testing.T) {
	e := newScanEnv(t)
	ctx := context.Background()
	e.a.noSandbox = false
	// the Wiki and the extension launcher enforce the sandbox by default, and fall back only under the test seam
	if e.a.wikiSandboxMode(ctx, e.org) != kindex.SandboxEnforce || e.a.extSandboxMode(ctx, e.org) != extensions.SandboxEnforce {
		t.Fatal("the default policy enforces the sandbox")
	}
	m, _ := extensions.Get("strix")
	p, why, err := e.a.extUsable(ctx, e.org, m, "")
	if err != nil || why != "" {
		t.Fatal(why, err)
	}
	e.a.Scanner = nil
	cli, ok := e.a.secScanner(ctx, e.org, p).(*secscan.CLI)
	if !ok || cli.Timeout != 3600*time.Second || cli.Mode != extensions.SandboxEnforce {
		t.Fatalf("%+v", cli)
	}
	e.a.noSandbox = true
	if e.a.secScanner(ctx, e.org, p).(*secscan.CLI).Mode != extensions.SandboxBestEffort || e.a.wikiSandboxMode(ctx, e.org) != kindex.SandboxBestEffort {
		t.Fatal("the test seam relaxes the sandbox")
	}
	if e.a.graphRetentionDays(ctx, e.org) != 30 || e.a.compactionThreshold(ctx, e.org) != 5 || e.a.outboxRetention(ctx, e.org) != 30*24*time.Hour {
		t.Fatal("the registry defaults apply")
	}
}

func TestS7GraphStaleInferredEdgeRetention(t *testing.T) {
	e := newKEnv(t)
	con := e.f.json("POST", e.base()+"/connections", CreateConnectionRequest{Name: "gl", Provider: "gitlab", Deployment: "self_managed", BaseURL: "http://127.0.0.1:1", AuthMethod: "personal_access_token"}, nil, 201)["id"].(string)
	repo := func(pid string) RepoMapping {
		return RepoMapping{Role: "primary", ConnectionID: con, ProviderProjectID: pid, PathSnapshot: "grp/" + pid, TargetBranch: "main"}
	}
	for i, pid := range []string{"1", "2"} {
		p := ProjectDoc{ID: newID("prj"), OrgID: e.org, Name: "P" + pid, NameKey: "p" + pid, Active: true, Mapping: Mapping{Repos: []RepoMapping{repo(pid)}}}
		if _, err := e.f.db.Coll("projects").InsertOne(context.Background(), p); err != nil {
			t.Fatal(i, err)
		}
	}
	bDeps, head := "module example.com/b\nrequire example.com/a v1.0.0\n", "h1"
	read := func(_ context.Context, _ ConnectionDoc, pid, _ string) (string, map[string]string, error) {
		if pid == "1" {
			return "a1", map[string]string{"go.mod": "module example.com/a\n"}, nil
		}
		return head, map[string]string{"go.mod": bDeps}, nil
	}
	ctx := context.Background()
	build := func() {
		if _, err := e.f.a.buildGraph(ctx, e.org, read); err != nil {
			t.Fatal(err)
		}
	}
	build()
	// the dependency disappears at a new head: the inferred edge turns stale
	bDeps, head = "module example.com/b\n", "h2"
	build()
	edge, err := findOne[GraphEdgeDoc](ctx, e.f.db, "graph_edges", bson.M{"org_id": e.org, "relation": "depends_on"})
	if err != nil || edge.Status != "stale" {
		t.Fatalf("%+v %v", edge, err)
	}
	setStale := func(ago time.Duration) {
		if _, err := e.f.db.Coll("graph_edges").UpdateOne(ctx, bson.M{"_id": edge.ID}, bson.M{"$set": bson.M{"stale_since": time.Now().UTC().Add(-ago)}}); err != nil {
			t.Fatal(err)
		}
	}
	setStale(5 * 24 * time.Hour)
	build()
	if e.count("graph_edges", bson.M{"org_id": e.org, "relation": "depends_on"}) != 1 {
		t.Fatal("a younger stale edge is kept")
	}
	setStale(40 * 24 * time.Hour)
	build()
	if e.count("graph_edges", bson.M{"org_id": e.org, "relation": "depends_on"}) != 0 {
		t.Fatal("a stale edge older than graph.inferred_retention_days is removed")
	}
}
