package orggraph

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

func goMod(module string, reqs ...string) string {
	s := "module " + module + "\n\ngo 1.22\n\nrequire (\n"
	for _, r := range reqs {
		s += "\t" + r + " v1.0.0\n"
	}
	return s + ")\n"
}

func repo(key, head string, files map[string]string) RepoIn {
	return RepoIn{Key: key, Label: key, WebURL: "http://gl.local/g/" + key, Head: head, Files: files}
}

func find(es []Edge, from string, rel Relation, to string) *Edge {
	for i := range es {
		if es[i].From == from && es[i].Relation == rel && es[i].To == to {
			return &es[i]
		}
	}
	return nil
}

func TestDeriveDependsOnExactIdentityOnly(t *testing.T) {
	in := BuildIn{Build: 1, Now: t0, Repos: []RepoIn{
		repo("c:1", "h1", map[string]string{"go.mod": goMod("git.local/pay/payments-api", "git.local/lib/shared")}),
		repo("c:2", "h2", map[string]string{"go.mod": goMod("git.local/lib/shared")}),
		repo("c:3", "h3", map[string]string{"go.mod": goMod("git.local/pay/payment-api", "git.local/pay/payments-api/v2")}), // near-miss names
		repo("c:4", "h4", map[string]string{"package.json": `{"name":"foo","dependencies":{"foo-client":"1","bar":"2"}}`}),
		repo("c:5", "h5", map[string]string{"package.json": `{"name":"foo-client"}`}),
		repo("c:6", "h6", map[string]string{"package.json": `{"name":"bar","dependencies":{"foo":"1"}}`}),
		repo("c:7", "h7", map[string]string{"package.json": `{"name":"bar"}`}), // duplicate identity "bar"
	}}
	_, es := Derive(in)
	if e := find(es, "repo:c:1", RelDependsOn, "repo:c:2"); e == nil || e.Status != StatusInferred || e.Confidence != 0.9 || e.Evidence[0].Path != "go.mod" || e.CommitSHA != "h1" {
		t.Fatalf("exact module identity: %+v", e)
	}
	if find(es, "repo:c:3", RelDependsOn, "repo:c:1") != nil || find(es, "repo:c:1", RelDependsOn, "repo:c:3") != nil {
		t.Fatal("payment-api vs payments-api must not produce an edge")
	}
	if e := find(es, "repo:c:4", RelDependsOn, "repo:c:5"); e == nil {
		t.Fatal("foo -> foo-client is a declared exact dependency")
	}
	if find(es, "repo:c:5", RelDependsOn, "repo:c:4") != nil {
		t.Fatal("foo-client does not depend on foo")
	}
	if find(es, "repo:c:4", RelDependsOn, "repo:c:6") != nil || find(es, "repo:c:4", RelDependsOn, "repo:c:7") != nil {
		t.Fatal("an identity declared by two repos is ambiguous: no edge")
	}
	if e := find(es, "repo:c:6", RelDependsOn, "repo:c:4"); e == nil {
		t.Fatal("bar -> foo (unique identity)")
	}
	// Maven.
	pom := func(g, a string, deps ...string) string {
		s := `<project><groupId>` + g + `</groupId><artifactId>` + a + `</artifactId><dependencies>`
		for _, d := range deps {
			g2, a2, _ := strings.Cut(d, ":")
			s += `<dependency><groupId>` + g2 + `</groupId><artifactId>` + a2 + `</artifactId></dependency>`
		}
		return s + `</dependencies></project>`
	}
	_, es = Derive(BuildIn{Build: 1, Now: t0, Repos: []RepoIn{
		repo("m:1", "a", map[string]string{"pom.xml": pom("com.x", "svc", "com.x:core")}),
		repo("m:2", "b", map[string]string{"pom.xml": pom("com.x", "core")}),
	}})
	if find(es, "repo:m:1", RelDependsOn, "repo:m:2") == nil {
		t.Fatal("maven groupId:artifactId")
	}
}

func TestDeriveConfigAndInferredEdges(t *testing.T) {
	in := BuildIn{Build: 3, Now: t0,
		Projects: []ProjectIn{{ID: "prj_1", Name: "Demo", RepoKeys: []string{"c:1"}, JiraProjects: []string{"ORQ"}, PrimaryRepo: "c:1", Commands: []string{"go test"}}},
		Repos: []RepoIn{repo("c:1", "h1", map[string]string{
			"openapi.yaml":       "openapi: 3.0.0",
			".gitlab/CODEOWNERS": "# comment\n/docs/ @docs\n* @team/payments @alice\n",
		})},
		Docs:  []DocIn{{Key: "conf:1001", Label: "Architecture", SourceVersionID: "srv_1", Links: []string{"http://gl.local/g/c:1/", "http://gl.local/g/other"}}},
		Tasks: []TaskIn{{Key: "run_1", Label: "DEMO-1", RepoKey: "c:1"}},
	}
	nodes, es := Derive(in)
	kinds := map[NodeKind]int{}
	for _, n := range nodes {
		kinds[n.Kind]++
	}
	if kinds[KindProject] != 1 || kinds[KindRepo] != 1 || kinds[KindJiraProject] != 1 || kinds[KindTest] != 1 || kinds[KindTask] != 1 || kinds[KindOwner] != 2 || kinds[KindAPI] != 1 || kinds[KindDoc] != 1 {
		t.Fatalf("nodes %v", kinds)
	}
	verified := []struct {
		from, to string
		rel      Relation
	}{{"project:prj_1", "repo:c:1", RelMapsTo}, {"project:prj_1", "jira_project:ORQ", RelMapsTo}, {"task:run_1", "repo:c:1", RelImplements}, {"repo:c:1", "test:c:1:go test", RelTestedBy}}
	for _, v := range verified {
		if e := find(es, v.from, v.rel, v.to); e == nil || e.Status != StatusVerified || e.Confidence != 1 {
			t.Errorf("config edge %v: %+v", v, e)
		}
	}
	for _, v := range []struct {
		from, to string
		rel      Relation
		conf     float64
	}{{"repo:c:1", "api:c:1:openapi.yaml", RelExposesAPI, 0.8}, {"repo:c:1", "owner:@team/payments", RelOwnedBy, 0.7}, {"repo:c:1", "owner:@alice", RelOwnedBy, 0.7}, {"doc:conf:1001", "repo:c:1", RelDocuments, 0.6}} {
		if e := find(es, v.from, v.rel, v.to); e == nil || e.Status != StatusInferred || e.Confidence != v.conf || len(e.Evidence) == 0 {
			t.Errorf("inferred edge %v: %+v", v, e)
		}
	}
	if find(es, "doc:conf:1001", RelDocuments, "repo:other") != nil {
		t.Fatal("only an exact web_url match documents a repo")
	}
	// determinism
	n2, e2 := Derive(in)
	if fmt.Sprint(nodes) != fmt.Sprint(n2) || len(es) != len(e2) {
		t.Fatal("not deterministic")
	}
	for i := range es {
		if es[i].Key() != e2[i].Key() {
			t.Fatal("order")
		}
	}
	// an unreadable repo produces no inferred edges
	in.Repos[0].Missing = true
	_, es = Derive(in)
	if find(es, "repo:c:1", RelOwnedBy, "owner:@alice") != nil {
		t.Fatal("missing repo")
	}
}

func TestManifestParsers(t *testing.T) {
	if m := ParseGoMod("go.mod", "module a/b // c\nrequire x/y v1\nrequire (\n x/z v2 // indirect\n)\n"); m.Identities[0] != "a/b" || len(m.Deps) != 2 || m.Deps[1].Identity != "x/z" {
		t.Fatalf("%+v", m)
	}
	if m := ParsePackageJSON("package.json", `not json`); len(m.Identities) != 0 {
		t.Fatal("bad json")
	}
	if got := ParseCodeowners("* @a\n* @b @c\n"); len(got) != 2 || got[0] != "@b" {
		t.Fatalf("last * rule wins: %v", got)
	}
	if got := ParseCodeowners("/x @a\n"); len(got) != 0 {
		t.Fatal("only the * rule")
	}
	if m := ParsePom("pom.xml", `<project><parent><groupId>g</groupId></parent><artifactId>a</artifactId></project>`); m.Identities[0] != "g:a" {
		t.Fatalf("%+v", m)
	}
}

func TestMergeFreshness(t *testing.T) {
	mk := func(from, to string, st Status, sha string) Edge {
		return Edge{From: from, To: to, Relation: RelDependsOn, Status: st, SourceID: from, CommitSHA: sha, CreatedBy: "system"}
	}
	prev := []Edge{
		mk("repo:a", "repo:b", StatusInferred, "old"),  // not re-observed, HEAD changed -> stale
		mk("repo:c", "repo:d", StatusInferred, "same"), // not re-observed, HEAD unchanged -> stays
		mk("repo:e", "repo:f", StatusVerified, "old"),  // verified never auto-removed or staled
		mk("repo:g", "repo:h", StatusStale, "old"),     // stale for long -> pruned
		mk("repo:i", "repo:j", StatusInferred, "old"),  // source failed to read -> untouched
		mk("repo:k", "repo:l", StatusRejected, "old"),  // rejected, re-observed -> stays rejected
	}
	prev[3].StaleSince = t0.AddDate(0, 0, -40)
	fresh := []Edge{mk("repo:k", "repo:l", StatusInferred, "new"), mk("repo:m", "repo:n", StatusInferred, "new")}
	heads := map[string]string{"repo:a": "new", "repo:c": "same", "repo:e": "new", "repo:g": "new", "repo:i": "new", "repo:k": "new"}
	merged, removed := Merge(prev, fresh, heads, map[string]bool{"repo:i": true}, t0, 30)
	get := func(from string) *Edge {
		for i := range merged {
			if merged[i].From == from {
				return &merged[i]
			}
		}
		return nil
	}
	if get("repo:a").Status != StatusStale || get("repo:a").StaleSince.IsZero() {
		t.Fatalf("a: %+v", get("repo:a"))
	}
	if get("repo:c").Status != StatusInferred || get("repo:e").Status != StatusVerified || get("repo:i").Status != StatusInferred {
		t.Fatal("c/e/i must be unchanged")
	}
	if get("repo:g") != nil || len(removed) != 1 || removed[0] != "repo:g|depends_on|repo:h" {
		t.Fatalf("g must be pruned: %v", removed)
	}
	if get("repo:k").Status != StatusRejected {
		t.Fatal("a rejected edge is never resurrected by a build")
	}
	if get("repo:m") == nil {
		t.Fatal("new edge")
	}
	// A stale edge inside retention stays; a user edge is never touched.
	prev2 := []Edge{{From: "x", To: "y", Relation: RelRelated, Status: StatusVerified, CreatedBy: "user"}}
	if m, r := Merge(prev2, nil, nil, nil, t0, 1); len(m) != 1 || len(r) != 0 {
		t.Fatal("user edge")
	}
}

func TestNeighborhoodBoundedAndFiltered(t *testing.T) {
	var es []Edge
	for i := 0; i < 300; i++ {
		es = append(es, Edge{From: "hub", To: fmt.Sprintf("n%03d", i), Relation: RelDependsOn, Status: StatusInferred})
	}
	es = append(es, Edge{From: "n000", To: "far", Relation: RelDependsOn, Status: StatusVerified},
		Edge{From: "hub", To: "hidden", Relation: RelRelated, Status: StatusRejected},
		Edge{From: "hub", To: "stale", Relation: RelRelated, Status: StatusStale})
	sg := Neighborhood(es, "hub", 1, Filter{}, 50)
	if !sg.Truncated || len(sg.Edges) != 50 || len(sg.Nodes) != 51 {
		t.Fatalf("truncated=%v edges=%d nodes=%d", sg.Truncated, len(sg.Edges), len(sg.Nodes))
	}
	if sg2 := Neighborhood(es, "hub", 1, Filter{}, 50); fmt.Sprint(sg.Edges) != fmt.Sprint(sg2.Edges) {
		t.Fatal("not deterministic")
	}
	sg = Neighborhood(es, "n000", 2, Filter{}, 1000)
	hasFar := false
	for _, n := range sg.Nodes {
		hasFar = hasFar || n == "far"
	}
	if !hasFar || sg.Depth != 2 {
		t.Fatalf("depth 2 reaches far: %v", sg.Nodes)
	}
	if sg = Neighborhood(es, "n000", 1, Filter{}, 1000); len(sg.Edges) != 2 || sg.Truncated {
		t.Fatalf("depth 1: %d", len(sg.Edges))
	}
	if sg = Neighborhood(es, "hub", 5, Filter{Statuses: []Status{StatusRejected}}, 10); len(sg.Edges) != 1 || sg.Depth != 2 {
		t.Fatalf("rejected filter, depth clamp: %+v", sg)
	}
	if sg = Neighborhood(es, "hub", 1, Filter{Relations: []Relation{RelRelated}, Statuses: []Status{StatusStale, StatusRejected}}, 10); len(sg.Edges) != 2 {
		t.Fatalf("relation filter: %d", len(sg.Edges))
	}
	if sg = Neighborhood(es, "hub", 1, Filter{}, 9999); len(sg.Edges) != MaxNeighborhoodEdges || !sg.Truncated {
		t.Fatal("limit capped at 200")
	}
}

func TestValidateUserEdge(t *testing.T) {
	if ValidateUserEdge("a", "b", RelRelated) != nil || ValidateUserEdge("a", "a", RelRelated) == nil || ValidateUserEdge("a", "b", "bogus") == nil || ValidateUserEdge("", "b", RelRelated) == nil {
		t.Fatal("user edge validation")
	}
	if len(NodeKinds) != 10 || len(Relations) != 9 {
		t.Fatal("enum sizes")
	}
}
