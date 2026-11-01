package server

import (
	"context"
	"fmt"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestCompactionBenchmarkRetentionAndMetrics(t *testing.T) {
	e := newKEnv(t)
	ctx := context.Background()
	src := e.source(e.org, "Runbook", "Restart the worker after a failed build. Restart the worker after a failed build again.")
	for i := 0; i < 5; i++ {
		body := map[string]any{"type": "experience", "scope": map[string]any{"kind": "org"}, "key": fmt.Sprintf("exp-%d", i), "title": fmt.Sprintf("Experience %d", i),
			"claims": []any{map[string]any{"text": fmt.Sprintf("Restart fixes build %d", i), "critical": false,
				"fact":     map[string]any{"subject": "build worker", "predicate": "fix_after_failure:" + fmt.Sprint(i), "value": "restart"},
				"evidence": []any{map[string]any{"ref_kind": "source_version", "ref_id": src, "excerpt": "Restart the worker after a failed build"}}}}}
		rec := e.f.json("POST", e.base()+"/knowledge", body, nil, 201)
		id := rec["id"].(string)
		e.f.json("POST", e.base()+"/knowledge-versions/"+rec["current_version"].(map[string]any)["id"].(string)+"/verify", map[string]any{}, e.rev(id), 200)
	}
	n, err := e.f.a.compactOrg(ctx, e.org, 5)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	// A second pass proposes nothing new (same key), and nothing is verified automatically.
	if n, _ = e.f.a.compactOrg(ctx, e.org, 5); n != 0 {
		t.Fatal("second pass", n)
	}
	draft, err := findOne[KnowledgeVersionDoc](ctx, e.f.db, "knowledge_versions", bson.M{"org_id": e.org, "origin.kind": "compaction"})
	if err != nil || draft.Status != "draft" || len(draft.SupersedesMany) != 5 {
		t.Fatal(draft.Status, len(draft.SupersedesMany), err)
	}
	rec, _ := e.f.a.knowledgeRecord(ctx, e.org, draft.RecordID)
	e.f.json("POST", e.base()+"/knowledge-versions/"+draft.ID+"/verify", map[string]any{}, map[string]string{"If-Match": fmt.Sprintf("\"%d\"", rec.Revision)}, 200)
	if c, _ := e.f.db.Coll("knowledge_versions").CountDocuments(ctx, bson.M{"org_id": e.org, "status": "superseded"}); c != 5 {
		t.Fatal("superseded", c)
	}
	b, err := e.f.a.runBenchmark(ctx, e.org)
	if err != nil || b.Result.ClaimLevel != "harness_only" {
		t.Fatal(b.Result.ClaimLevel, err)
	}
	if l := e.f.json("GET", e.base()+"/knowledge-benchmarks", nil, nil, 200)["items"].([]any); len(l) != 1 {
		t.Fatal(l)
	}
	p := e.f.json("GET", e.base()+"/knowledge-retention/preview", nil, nil, 200)
	if p["archived_versions"].(float64) != 0 {
		t.Fatal(p)
	}
	m := e.f.json("GET", e.base()+"/knowledge-metrics", nil, nil, 200)
	if m["versions_by_status"].(map[string]any)["verified"].(float64) != 1 || m["wiki_mode"] != "fallback" {
		t.Fatal(m)
	}
}
