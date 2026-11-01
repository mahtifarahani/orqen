package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestGraphBuildInfersExactDependenciesAndFreshness(t *testing.T) {
	e := newKEnv(t)
	con := e.f.json("POST", e.base()+"/connections", CreateConnectionRequest{Name: "gl", Provider: "gitlab", Deployment: "self_managed", BaseURL: "http://127.0.0.1:1", AuthMethod: "personal_access_token"}, nil, 201)["id"].(string)
	repo := func(pid string) RepoMapping {
		return RepoMapping{Role: "primary", ConnectionID: con, ProviderProjectID: pid, PathSnapshot: "grp/" + pid, TargetBranch: "main"}
	}
	proj := ProjectDoc{ID: newID("prj"), OrgID: e.org, Name: "P", NameKey: "p", Active: true, Mapping: Mapping{Repos: []RepoMapping{repo("1")}}}
	proj2 := ProjectDoc{ID: newID("prj"), OrgID: e.org, Name: "Q", NameKey: "q", Active: true, Mapping: Mapping{Repos: []RepoMapping{repo("2")}}}
	for _, p := range []ProjectDoc{proj, proj2} {
		if _, err := e.f.db.Coll("projects").InsertOne(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	bDeps := "module example.com/b\nrequire example.com/a v1.0.0\nrequire example.com/ab v1.0.0\n"
	head := "h1"
	read := func(_ context.Context, _ ConnectionDoc, pid, _ string) (string, map[string]string, error) {
		switch pid {
		case "1":
			return "a1", map[string]string{"go.mod": "module example.com/a\n"}, nil
		default:
			return head, map[string]string{"go.mod": bDeps}, nil
		}
	}
	ctx := context.Background()
	if _, err := e.f.a.buildGraph(ctx, e.org, read); err != nil {
		t.Fatal(err)
	}
	a, b := "repo:"+con+":1", "repo:"+con+":2"
	nb := e.f.json("GET", e.base()+"/graph/neighborhood?node="+b+"&depth=1", nil, nil, 200)
	var dep map[string]any
	for _, x := range nb["edges"].([]any) {
		m := x.(map[string]any)
		if m["relation"] == "depends_on" {
			dep = m
		}
	}
	if dep == nil || dep["from"] != b || dep["to"] != a || dep["status"] != "inferred" {
		t.Fatal(nb)
	}
	// The packet item lists the primary repo's neighbors, tagged with their status.
	it := e.f.a.graphNeighbors(ctx, RunDoc{OrgID: e.org}, &RepoMapping{ConnectionID: con, ProviderProjectID: "2"})
	if it == nil || it.Kind != "graph_neighbors" || !strings.Contains(it.Text, "depends_on") || !strings.Contains(it.Text, "(inferred)") || len(it.Text) > 1024 {
		t.Fatal(it)
	}
	if e.f.a.graphNeighbors(ctx, RunDoc{OrgID: e.org}, &RepoMapping{ConnectionID: con, ProviderProjectID: "9"}) != nil {
		t.Fatal("no edges, no item")
	}
	// "example.com/ab" is a similar name, not an identity: exactly one depends_on edge exists.
	if n, _ := e.f.db.Coll("graph_edges").CountDocuments(ctx, bson.M{"org_id": e.org, "relation": "depends_on"}); n != 1 {
		t.Fatal("depends_on edges", n)
	}
	// Limit truncation.
	if tr := e.f.json("GET", e.base()+"/graph/neighborhood?node="+b+"&depth=2&limit=1", nil, nil, 200); tr["truncated"] != true {
		t.Fatal(tr)
	}
	// Confirm makes it verified with user evidence.
	hdr := map[string]string{"If-Match": fmt.Sprintf("\"%d\"", int64(dep["revision"].(float64)))}
	c := e.f.json("POST", e.base()+"/graph/edges/"+dep["id"].(string)+"/confirm", nil, hdr, 200)
	if c["status"] != "verified" || len(c["evidence"].([]any)) < 2 {
		t.Fatal(c)
	}
	// An invalid user edge is refused.
	expectCode(t, e.f.json("POST", e.base()+"/graph/edges", CreateGraphEdgeRequest{From: a, To: "repo:nope", Relation: "related"}, nil, 422), "graph_edge_invalid")
	// The head of B changes and the dependency disappears: the verified edge stays, nothing is resurrected as inferred.
	head, bDeps = "h2", "module example.com/b\n"
	if _, err := e.f.a.buildGraph(ctx, e.org, read); err != nil {
		t.Fatal(err)
	}
	got, _ := findOne[GraphEdgeDoc](ctx, e.f.db, "graph_edges", bson.M{"org_id": e.org, "relation": "depends_on"})
	if got.Status != "verified" {
		t.Fatal(got.Status)
	}
}

// TestSeededFakeGraphFindsTheExactDependency proves the walk fixture: the GitLab fake's SeedGraph gives project 2 a
// package.json that depends on the package project 1 provides, and the default repo reader (CodeHost) finds the edge.
func TestSeededFakeGraphFindsTheExactDependency(t *testing.T) {
	e := newProjectEnv(t)
	e.gl.SeedGraph()
	for _, id := range []string{"1", "2"} {
		created := e.add("graph-"+id, id)["created"].([]any)
		pid := created[0].(map[string]any)["id"].(string)
		e.mapping(pid, RepoMappingInput{Role: "primary", ConnectionID: e.glID, ProviderProjectID: id, TargetBranch: "main"}, nil)
	}
	ctx := context.Background()
	if _, err := e.f.a.buildGraph(ctx, e.org, e.f.a.defaultRepoReader); err != nil {
		t.Fatal(err)
	}
	got, err := findOne[GraphEdgeDoc](ctx, e.f.db, "graph_edges", bson.M{"org_id": e.org, "relation": "depends_on"})
	if err != nil || got.Status != "inferred" || got.From != "repo:"+e.glID+":2" || got.To != "repo:"+e.glID+":1" {
		t.Fatal(err, got)
	}
}
