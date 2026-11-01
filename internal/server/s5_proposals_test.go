package server

import (
	"orqen/internal/runstate"
	"strconv"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMergeProposesDeterministicKnowledgeAndPromotes(t *testing.T) {
	e := liveEnv(t, nil, nil)
	run := e.waitState("ORQ-1", runstate.AwaitingCI)
	e.gl.SetPipeline(run.Heads.Head, "success", okJobs())
	e.waitState("ORQ-1", runstate.ReadyForMerge)
	writes := len(e.gl.Writes())
	e.gl.Merge(1, 1)
	e.waitState("ORQ-1", runstate.Merged)
	waitFor(t, "a proposal", func() bool { return e.count("knowledge_proposals", bson.M{"org_id": e.org, "run_id": run.ID}) == 1 })
	if len(e.gl.Writes()) != writes {
		t.Fatal("authoring must write nothing to GitLab")
	}
	list := e.f.json("GET", e.orgPath("knowledge-proposals"), nil, nil, 200)["items"].([]any)
	items := list[0].(map[string]any)["items"].([]any)
	var task map[string]any
	for _, it := range items {
		if m := it.(map[string]any); m["type"] == "task" {
			task = m
		}
	}
	if task == nil || task["outcome"] != "awaiting_review" {
		t.Fatal(list)
	}
	// Manual policy: nothing is verified until the person promotes the draft.
	vid := task["version_id"].(string)
	v := e.f.json("GET", e.orgPath("knowledge-versions", vid), nil, nil, 200)
	if v["status"] != "draft" || v["origin"].(map[string]any)["kind"] != "deterministic" {
		t.Fatal(v)
	}
	rid := task["record_id"].(string)
	rec := e.f.json("GET", e.orgPath("knowledge", rid), nil, nil, 200)
	e.f.json("POST", e.orgPath("knowledge-versions", vid, "verify"), map[string]any{}, map[string]string{"If-Match": "\"" + itoa(int64(rec["revision"].(float64))) + "\""}, 200)
	if e.count("wiki_outbox", bson.M{"org_id": e.org, "version_id": vid}) != 1 {
		t.Fatal("promotion writes an outbox row")
	}
	// A manual proposal on the same run reuses existing records without a second open draft.
	e.f.json("POST", e.orgPath("runs", run.ID, "knowledge", "propose"), nil, nil, 201)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
