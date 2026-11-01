package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/knowledge"
	"orqen/internal/learnbench"
)

// compactOrg proposes consolidated drafts for groups of verified experience versions (manual promotion only).
// A proposal is a draft version of a new experience record; nothing is superseded until a person verifies it.
func (a *App) compactOrg(ctx context.Context, org string, threshold int) (int, error) {
	docs, err := findMany[KnowledgeVersionDoc](ctx, a.DB, "knowledge_versions", bson.M{"org_id": org, "status": "verified", "type": "experience"}, bson.D{{Key: "_id", Value: 1}}, 5000)
	if err != nil {
		return 0, err
	}
	vs := make([]knowledge.Version, 0, len(docs))
	for _, d := range docs {
		vs = append(vs, d.domain())
	}
	made := 0
	for _, g := range knowledge.CompactionGroups(vs, threshold) {
		draft, ids := knowledge.Consolidate(g)
		key := "compaction-" + knowledge.SHA(strings.Join(ids, ","))[:12]
		sc, err := knowledge.ParseScope(g[0].ScopeKey)
		if err != nil {
			continue
		}
		in := KnowledgeScopeInput{Kind: sc.Kind, ProjectID: sc.Project, SprintID: sc.Sprint, RunID: sc.Run}
		rec, v, err := a.createKnowledgeRecord(ctx, org, "system", CreateKnowledgeRecordRequest{Type: "experience", Scope: in, Key: key, Title: draft.Title, Body: draft.Body, Claims: claimInputs(draft.Claims)},
			knowledge.Origin{Kind: "compaction", Actor: "system"})
		if err != nil {
			continue // exists already, or evidence no longer resolves: skipped, never forced
		}
		_, _ = a.DB.Coll("knowledge_versions").UpdateOne(ctx, bson.M{"_id": v.ID}, bson.M{"$set": bson.M{"supersedes_many": ids}})
		_ = rec
		made++
	}
	return made, nil
}

func (a *App) runKnowledgeCompaction(ctx context.Context, job JobDoc, lease Lease) error {
	if _, err := a.compactOrg(ctx, job.OrgID, a.compactionThreshold(ctx, job.OrgID)); err != nil {
		return err
	}
	return a.fenced(ctx, lease, func(sc context.Context) error { return a.completeJob(sc, lease) })
}

type KnowledgeBenchmarkDoc struct {
	ID        string            `bson:"_id"`
	OrgID     string            `bson:"org_id"`
	Result    learnbench.Result `bson:"result"`
	CreatedAt time.Time         `bson:"created_at"`
}

func (d KnowledgeBenchmarkDoc) DTO() bson.M {
	return bson.M{"id": d.ID, "created_at": timestamp(d.CreatedAt), "result": d.Result}
}

// runBenchmark runs the fixture suite with the deterministic fake engine. It measures the harness only (claim level
// harness_only); no model is called.
func (a *App) runBenchmark(ctx context.Context, org string) (KnowledgeBenchmarkDoc, error) {
	suite, err := learnbench.Load("fixture-v1")
	if err != nil {
		return KnowledgeBenchmarkDoc{}, err
	}
	d := KnowledgeBenchmarkDoc{ID: newID("kbr"), OrgID: org, Result: learnbench.RunBoth(suite, learnbench.FakeEngine{}), CreatedAt: time.Now().UTC()}
	_, err = a.DB.Coll("knowledge_benchmark_runs").InsertOne(ctx, d)
	return d, err
}

func (a *App) runKnowledgeBenchmark(ctx context.Context, job JobDoc, lease Lease) error {
	if _, err := a.runBenchmark(ctx, job.OrgID); err != nil {
		return err
	}
	return a.fenced(ctx, lease, func(sc context.Context) error { return a.completeJob(sc, lease) })
}

func (a *App) startKnowledgeOp(kind string) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		org := r.PathValue("orgId")
		if err := a.txnRetryJob(r.Context(), func(sc context.Context) error { return a.ensureJobTx(sc, org, kind, kind+":"+org, 3, bson.M{}) }); err != nil {
			return err
		}
		a.Committed()
		writeJSON(w, 202, bson.M{"queued": true})
		return nil
	}
}

func (a *App) listBenchmarks(w http.ResponseWriter, r *http.Request) error {
	docs, err := findMany[KnowledgeBenchmarkDoc](r.Context(), a.DB, "knowledge_benchmark_runs", bson.M{"org_id": r.PathValue("orgId")}, bson.D{{Key: "_id", Value: -1}}, 20)
	if err != nil {
		return err
	}
	items := []bson.M{}
	for _, d := range docs {
		items = append(items, d.DTO())
	}
	writeJSON(w, 200, bson.M{"items": items, "next_cursor": nil})
	return nil
}

// retentionPreview counts what a retention sweep would remove; it removes nothing.
func (a *App) retentionPreview(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	ctx := r.Context()
	cut := time.Now().UTC().AddDate(0, 0, -90)
	count := func(coll string, f bson.M) int64 {
		f["org_id"] = org
		n, _ := a.DB.Coll(coll).CountDocuments(ctx, f)
		return n
	}
	writeJSON(w, 200, bson.M{
		"old_source_versions": count("source_versions", bson.M{"latest": false, "fetched_at": bson.M{"$lt": cut}}),
		"applied_outbox":      count("wiki_outbox", bson.M{"state": "applied"}),
		"archived_versions":   count("knowledge_versions", bson.M{"status": "archived"}),
		"stale_graph_edges":   count("graph_edges", bson.M{"status": "stale"}),
		"cutoff":              timestamp(cut),
	})
	return nil
}

func (a *App) knowledgeMetrics(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	ctx := r.Context()
	count := func(coll string, f bson.M) int64 {
		f["org_id"] = org
		n, _ := a.DB.Coll(coll).CountDocuments(ctx, f)
		return n
	}
	byStatus := bson.M{}
	for _, s := range []string{"draft", "verified", "conflicted", "superseded", "archived"} {
		byStatus[s] = count("knowledge_versions", bson.M{"status": s})
	}
	wiki, err := a.wikiStatusDTO(ctx, org)
	if err != nil {
		return err
	}
	writeJSON(w, 200, bson.M{
		"versions_by_status": byStatus,
		"records":            count("knowledge_records", bson.M{"active": true}),
		"stale_evidence":     count("knowledge_versions", bson.M{"status": "verified", "evidence_stale": true}),
		"open_conflicts":     count("knowledge_conflicts", bson.M{"state": "open"}),
		"proposals_waiting":  count("knowledge_proposals", bson.M{"state": "awaiting_review"}),
		"packets_served":     count("knowledge_usage", bson.M{}),
		"wiki_mode":          wiki["mode"],
		"graph_edges":        count("graph_edges", bson.M{}),
	})
	return nil
}
