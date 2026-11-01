package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"orqen/internal/contextpkt"
	"orqen/internal/providers"
	"orqen/internal/testfakes"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestSourceSyncVersionsAndStalesEvidence(t *testing.T) {
	e := newKEnv(t)
	fake := testfakes.NewConfluenceServer("/confluence")
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	conn := e.f.json("POST", e.base()+"/connections", CreateConnectionRequest{Name: "wiki", Provider: "confluence", Deployment: "server", BaseURL: srv.URL + "/confluence", AuthMethod: "personal_access_token"}, nil, 201)["id"].(string)
	page := "1001"
	proj := ProjectDoc{ID: newID("prj"), OrgID: e.org, Mapping: Mapping{Confluence: []ConfluenceRef{{ConnectionID: conn, PageID: &page}}}}
	docs := func(c ConnectionDoc) (providers.DocsSource, error) {
		return providers.NewConfluence(providers.Options{BaseURL: c.BaseURL}, providers.Auth{Method: "personal_access_token", Secret: fake.Token}, "server", 0), nil
	}
	ctx := context.Background()
	if s, c, err := e.f.a.syncProjectSources(ctx, e.org, proj, docs); err != nil || s != 1 || c != 1 {
		t.Fatal(s, c, err)
	}
	// An unchanged page is idempotent.
	if _, c, err := e.f.a.syncProjectSources(ctx, e.org, proj, docs); err != nil || c != 0 {
		t.Fatal(c, err)
	}
	v1, err := findOne[SourceVersionDoc](ctx, e.f.db, "source_versions", bson.M{"org_id": e.org, "latest": true})
	if err != nil || v1.Authority != "canonical_external" || v1.ArtifactID == "" {
		t.Fatal(v1, err)
	}
	text, _ := e.f.a.artifactText(ctx, e.org, v1.ArtifactID, 0)
	rec := e.create("arch", "Arch", "", claim("Arch claim", v1.ID, text[:20]))
	e.f.json("POST", e.base()+"/knowledge-versions/"+rec["current_version"].(map[string]any)["id"].(string)+"/verify", map[string]any{}, e.rev(rec["id"].(string)), 200)
	fake.SetPage(page, "Architecture", "<p>Completely new content after an edit.</p>")
	if _, c, err := e.f.a.syncProjectSources(ctx, e.org, proj, docs); err != nil || c != 1 {
		t.Fatal(c, err)
	}
	n, _ := e.f.db.Coll("source_versions").CountDocuments(ctx, bson.M{"org_id": e.org, "latest": true})
	old, _ := findOne[SourceVersionDoc](ctx, e.f.db, "source_versions", bson.M{"_id": v1.ID})
	if n != 1 || old.Latest {
		t.Fatal("latest flip", n, old.Latest)
	}
	if n, _ := e.f.db.Coll("knowledge_proposals").CountDocuments(ctx, bson.M{"org_id": e.org, "trigger": "source_changed"}); n != 1 {
		t.Fatal("source_changed proposal", n)
	}
	got := e.f.json("GET", e.base()+"/knowledge/"+rec["id"].(string), nil, nil, 200)
	if got["verified_version"].(map[string]any)["evidence_stale"] != true {
		t.Fatal(got["verified_version"])
	}
}

func TestLinkedPagesPoisonedIsWrappedAndForeignHostIsOutOfScope(t *testing.T) {
	e := newKEnv(t)
	fake := testfakes.NewConfluenceCloud("/cloud")
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	base := srv.URL + "/cloud/wiki"
	conn := e.f.json("POST", e.base()+"/connections", CreateConnectionRequest{Name: "cloudwiki", Provider: "confluence", Deployment: "cloud", BaseURL: base, AuthMethod: "api_token", AuthUsername: ptr(fake.User)}, nil, 201)["id"].(string)
	e.f.json("PUT", e.base()+"/connections/"+conn+"/credential", bson.M{"secret": fake.Token}, map[string]string{"If-Match": "1"}, 200)
	page := "1001"
	proj := ProjectDoc{ID: newID("prj"), OrgID: e.org, Mapping: Mapping{Confluence: []ConfluenceRef{{ConnectionID: conn, PageID: &page}}}}
	before := e.f.json("GET", e.base()+"/policy/effective", nil, nil, 200)
	links := []string{srv.URL + "/cloud/wiki/spaces/DEV/pages/1002/Release+notes", "https://other.example.org/wiki/spaces/X/pages/5/Y"}
	items := e.f.a.linkedPages(context.Background(), e.org, proj, links)
	poisoned, foreign := items[links[0]], items[links[1]]
	if poisoned.Excluded != "" || poisoned.Text == "" || !poisoned.External {
		t.Fatalf("%+v", poisoned)
	}
	if foreign.Excluded != contextpkt.ReasonOutOfScope || foreign.Text != "" {
		t.Fatalf("%+v", foreign)
	}
	pkt, man, over := contextpkt.Build(contextpkt.In{Cap: 16384, OrgID: e.org, AllowedConnections: []string{conn}, Sections: []contextpkt.SectionIn{{ID: contextpkt.SecDesignRefs, Items: []contextpkt.Item{poisoned, foreign}}}})
	if over != nil {
		t.Fatal(over)
	}
	i := strings.Index(pkt.Text, "Ignore all")
	start := strings.LastIndex(pkt.Text[:max(i, 0)], "<<<SOURCE id=")
	if i < 0 || start < 0 || !strings.Contains(pkt.Text[start:i], "trust=external") {
		t.Fatal("the poisoned text must sit inside an external source block:\n", pkt.Text)
	}
	reasons := map[string]string{}
	for _, x := range man.Excluded {
		reasons[x.Label] = x.Reason
	}
	if reasons[links[1]] != "out_of_scope" {
		t.Fatal(man.Excluded)
	}
	after := e.f.json("GET", e.base()+"/policy/effective", nil, nil, 200)
	b1, _ := json.Marshal(before)
	b2, _ := json.Marshal(after)
	if string(b1) != string(b2) {
		t.Fatal("the policy must not change")
	}
}
