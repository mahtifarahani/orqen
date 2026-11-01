package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type kenv struct {
	f   *testApp
	org string
}

func newKEnv(t *testing.T) *kenv {
	f := newTestApp(t, false)
	f.bootstrap()
	return &kenv{f, f.org("acme")}
}

// source seeds a canonical external source version whose text is in a real artifact blob.
func (e *kenv) source(org, title, text string) string {
	e.f.t.Helper()
	blob, err := storeBlob(e.f.a.artifactRoot(), org, strings.NewReader(text), 1<<20)
	if err != nil {
		e.f.t.Fatal(err)
	}
	art, err := e.f.a.recordArtifact(context.Background(), org, blob, "text/plain", "source", Subject{"test", "seed"})
	if err != nil {
		e.f.t.Fatal(err)
	}
	sv := SourceVersionDoc{ID: newID("srv"), OrgID: org, Kind: "confluence_page", ExternalID: title, Title: title, ArtifactID: art.ID, Authority: "canonical_external", FetchedAt: time.Now().UTC(), Latest: true, ContentHash: blob.SHA256}
	if _, err = e.f.db.Coll("source_versions").InsertOne(context.Background(), sv); err != nil {
		e.f.t.Fatal(err)
	}
	return sv.ID
}

func claim(text, src, excerpt string) map[string]any {
	return map[string]any{"text": text, "critical": false, "evidence": []any{map[string]any{"ref_kind": "source_version", "ref_id": src, "excerpt": excerpt}}}
}

func (e *kenv) base() string { return "/api/v1/orgs/" + e.org }
func (e *kenv) rev(rec string) map[string]string {
	m := e.f.json("GET", e.base()+"/knowledge/"+rec, nil, nil, 200)
	return map[string]string{"If-Match": fmt.Sprintf("\"%d\"", int64(m["revision"].(float64)))}
}

func (e *kenv) create(key, title, body string, claims ...any) map[string]any {
	return e.f.json("POST", e.base()+"/knowledge", map[string]any{"type": "business", "scope": map[string]any{"kind": "org"}, "key": key, "title": title, "body": body, "claims": claims}, nil, 201)
}

func TestKnowledgeLifecycleRollback(t *testing.T) {
	e := newKEnv(t)
	src := e.source(e.org, "Refund policy", "Refunds are issued within 14 days. Shipping is free above 500.")
	rec := e.create("refund-policy", "Refunds", "Refund rules", claim("Refunds take 14 days", src, "Refunds are issued within 14 days"))
	id := rec["id"].(string)
	v1 := rec["current_version"].(map[string]any)["id"].(string)
	if rec["status"] != "draft" || rec["current_version"].(map[string]any)["status"] != "draft" {
		t.Fatal(rec)
	}
	// A second draft cannot start while one is open.
	expectCode(t, e.f.json("POST", e.base()+"/knowledge/"+id+"/versions", map[string]any{"title": "x", "claims": []any{claim("a", src, "Refunds are issued")}}, e.rev(id), 409), "knowledge_draft_exists")
	e.f.json("POST", e.base()+"/knowledge-versions/"+v1+"/verify", map[string]any{}, e.rev(id), 200)
	got := e.f.json("GET", e.base()+"/knowledge/"+id, nil, nil, 200)
	if got["status"] != "verified" || got["verified_version_id"] != v1 {
		t.Fatal(got)
	}
	// Version 2 supersedes version 1.
	v2doc := e.f.json("POST", e.base()+"/knowledge/"+id+"/versions", map[string]any{"title": "Refunds v2", "body": "new", "claims": []any{claim("Shipping is free above 500", src, "Shipping is free above 500")}}, e.rev(id), 201)
	v2 := v2doc["id"].(string)
	e.f.json("POST", e.base()+"/knowledge-versions/"+v2+"/verify", map[string]any{}, e.rev(id), 200)
	old := e.f.json("GET", e.base()+"/knowledge-versions/"+v1, nil, nil, 200)
	if old["status"] != "superseded" {
		t.Fatal(old)
	}
	// Rollback copies version 1 as version 3 and verifies it through the gate again.
	rb := e.f.json("POST", e.base()+"/knowledge/"+id+"/rollback", map[string]any{"to_version_id": v1, "note": "wrong"}, e.rev(id), 200)
	nv := rb["version"].(map[string]any)
	if nv["status"] != "verified" || nv["rollback_of"] != v1 || nv["number"].(float64) != 3 {
		t.Fatal(rb)
	}
	versions := e.f.json("GET", e.base()+"/knowledge/"+id+"/versions", nil, nil, 200)["items"].([]any)
	if len(versions) != 3 {
		t.Fatal(versions)
	}
	// The status change wrote Wiki outbox rows and one wiki_maintain job per org (never an external write).
	n, _ := e.f.db.Coll("wiki_outbox").CountDocuments(context.Background(), bson.M{"org_id": e.org})
	if n < 3 {
		t.Fatal("outbox", n)
	}
	jobs, _ := e.f.db.Coll("jobs").CountDocuments(context.Background(), bson.M{"org_id": e.org, "kind": "wiki_maintain"})
	if jobs != 1 {
		t.Fatal("jobs", jobs)
	}
}

func TestKnowledgeOrgIsolation(t *testing.T) {
	e := newKEnv(t)
	other := e.f.org("globex")
	src := e.source(e.org, "Doc", "Alpha is the code name.")
	rec := e.create("codename", "Codename", "", claim("Alpha is the code name", src, "Alpha is the code name"))
	id := rec["id"].(string)
	e.f.json("GET", "/api/v1/orgs/"+other+"/knowledge/"+id, nil, nil, 404)
	// An evidence ref of another org does not resolve.
	resp := e.f.json("POST", "/api/v1/orgs/"+other+"/knowledge", map[string]any{"type": "business", "scope": map[string]any{"kind": "org"}, "key": "codename", "title": "x", "claims": []any{claim("Alpha", src, "Alpha")}}, nil, 422)
	expectCode(t, resp, "evidence_ref_invalid")
	other2src := e.source(other, "Doc", "Alpha is the code name.")
	e.f.json("POST", "/api/v1/orgs/"+other+"/knowledge", map[string]any{"type": "business", "scope": map[string]any{"kind": "org"}, "key": "codename", "title": "x", "claims": []any{claim("Alpha", other2src, "Alpha is the code name")}}, nil, 201)
	list := e.f.json("GET", e.base()+"/knowledge", nil, nil, 200)["items"].([]any)
	if len(list) != 1 {
		t.Fatal(list)
	}
}

func TestKnowledgeConflictAndClaimGate(t *testing.T) {
	e := newKEnv(t)
	src := e.source(e.org, "Policy", "Refund window is 14 days. Refund window is 30 days.")
	fact := func(obj string) map[string]any {
		return map[string]any{"text": "Refund window is " + obj, "critical": false, "fact": map[string]any{"subject": "refund window", "predicate": "is", "value": obj},
			"evidence": []any{map[string]any{"ref_kind": "source_version", "ref_id": src, "excerpt": "Refund window is " + obj}}}
	}
	a := e.create("refund-a", "Refund A", "", fact("14 days"))
	aid := a["id"].(string)
	e.f.json("POST", e.base()+"/knowledge-versions/"+a["current_version"].(map[string]any)["id"].(string)+"/verify", map[string]any{}, e.rev(aid), 200)
	b := e.create("refund-b", "Refund B", "", fact("30 days"))
	bid, bv := b["id"].(string), b["current_version"].(map[string]any)["id"].(string)
	// The conflicting draft becomes conflicted (the transaction commits) and the call answers 409.
	expectCode(t, e.f.json("POST", e.base()+"/knowledge-versions/"+bv+"/verify", map[string]any{}, e.rev(bid), 409), "knowledge_conflict_open")
	if v := e.f.json("GET", e.base()+"/knowledge-versions/"+bv, nil, nil, 200); v["status"] != "conflicted" {
		t.Fatal(v)
	}
	cs := e.f.json("GET", e.base()+"/knowledge-conflicts?state=open", nil, nil, 200)["items"].([]any)
	if len(cs) != 1 {
		t.Fatal(cs)
	}
	c := cs[0].(map[string]any)
	cid := c["id"].(string)
	hdr := map[string]string{"If-Match": fmt.Sprintf("\"%d\"", int64(c["revision"].(float64)))}
	e.f.json("POST", e.base()+"/knowledge-conflicts/"+cid+"/resolve", map[string]any{"choice": "accept_new"}, hdr, 422)
	res := e.f.json("POST", e.base()+"/knowledge-conflicts/"+cid+"/resolve", map[string]any{"choice": "accept_new", "note": "the 30 day window is current"}, hdr, 200)
	if res["state"] != "resolved" {
		t.Fatal(res)
	}
	if v := e.f.json("GET", e.base()+"/knowledge-versions/"+bv, nil, nil, 200); v["status"] != "verified" {
		t.Fatal(v)
	}
	// A claim without any evidence cannot be verified.
	z := e.f.json("POST", e.base()+"/knowledge", map[string]any{"type": "business", "scope": map[string]any{"kind": "org"}, "key": "no-proof", "title": "x", "claims": []any{map[string]any{"text": "Unproven", "critical": false, "evidence": []any{}}}}, nil, 201)
	zid := z["id"].(string)
	m := e.f.json("POST", e.base()+"/knowledge-versions/"+z["current_version"].(map[string]any)["id"].(string)+"/verify", map[string]any{}, e.rev(zid), 422)
	expectCode(t, m, "knowledge_claims_unsupported")
}
