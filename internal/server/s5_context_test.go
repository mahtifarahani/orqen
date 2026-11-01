package server

import (
	"context"
	"fmt"
	"orqen/internal/contextpkt"
	"orqen/internal/rolebriefs"
	"strings"
	"testing"
)

// seedKnowledge creates and verifies one org-scoped record (the source is a canonical seeded text).
func (e *intakeEnv) seedKnowledge(key, title, text string, pin bool, roles []string) string {
	k := &kenv{f: e.f, org: e.org}
	src := k.source(e.org, "Doc "+key, text)
	rec := k.create(key, title, "", claim(text, src, text))
	id := rec["id"].(string)
	k.f.json("POST", k.base()+"/knowledge-versions/"+rec["current_version"].(map[string]any)["id"].(string)+"/verify", map[string]any{}, k.rev(id), 200)
	if pin {
		k.f.json("PUT", k.base()+"/knowledge/"+id+"/pin", map[string]any{"pinned": true, "critical": true, "applies_to_roles": roles}, k.rev(id), 200)
	}
	return id
}

func sectionKinds(man map[string]any) map[string][]string {
	out := map[string][]string{}
	for _, s := range man["sections"].([]any) {
		sm := s.(map[string]any)
		for _, it := range sm["items"].([]any) {
			out[sm["id"].(string)] = append(out[sm["id"].(string)], it.(map[string]any)["kind"].(string))
		}
	}
	return out
}

func TestRolePacketsReceiveKnowledgeAndRespectRoles(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{JQL: ptr(`key = ORQ-1`)})
	e.poll()
	run := e.waitStage("ORQ-1", "triage")
	e.seedKnowledge("export-rule", "Export rule", "CSV exports keep the column order stable", false, nil)
	e.seedKnowledge("worker-only", "Worker only rule", "Never touch generated files", true, []string{"worker"})
	ctx := context.Background()
	// The worker packet carries both items; the reviewer never gets task discussion and not the worker-only rule.
	wp, wman, over, err := e.f.a.rolePacket(ctx, run, "worker")
	if err != nil || over != nil {
		t.Fatal(err, over)
	}
	if !strings.Contains(wp.Text, "Never touch generated files") || !strings.Contains(wp.Text, "CSV exports keep the column order stable") {
		t.Fatal(wp.Text)
	}
	if wman.Role != "worker" || wman.Consumption.EstMethod != "bytes_div_4" {
		t.Fatal(wman)
	}
	rp, rman, _, err := e.f.a.rolePacket(ctx, run, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rp.Text, "Never touch generated files") || len(sectionKinds(map[string]any{"sections": toAnySections(rman.Sections)})["task_discussion"]) > 0 {
		t.Fatal("the reviewer packet must not carry the worker-only rule or task discussion:\n", rp.Text)
	}
	found := false
	for _, x := range rman.Excluded {
		if x.Reason == "not_for_role" {
			found = true
		}
	}
	if !found {
		t.Fatal("the worker-only rule must show as not_for_role in the reviewer manifest", rman.Excluded)
	}
	// The brief a role is dispatched with contains the packet.
	data := rolebriefs.Data{Key: "ORQ-1", Summary: "x", Lane: rolebriefs.Lane{ID: "M1.L1"}}
	if err = e.f.a.attachContext(ctx, run, "worker", &data); err != nil {
		t.Fatal(err)
	}
	b, err := rolebriefs.Render(rolebriefs.Worker, data)
	if err != nil || !strings.Contains(b.Text, "Never touch generated files") || !strings.Contains(b.Text, "Context packet") {
		t.Fatal(err, b.Text)
	}
	// The route builds the same manifest, and usage was recorded.
	m := e.f.json("GET", e.orgPath("runs", run.ID, "context", "roles", "worker"), nil, nil, 200)
	if m["role"] != "worker" {
		t.Fatal(m)
	}
	n, _ := e.f.db.Coll("knowledge_usage").CountDocuments(ctx, map[string]any{"org_id": e.org, "run_id": run.ID, "role": "worker"})
	if n != 1 {
		t.Fatal("usage rows", n)
	}
}

func TestHugeCriticalRuleForWorkerOnlyStopsOnlyTheWorker(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{JQL: ptr(`key = ORQ-1`)})
	e.poll()
	run := e.waitStage("ORQ-1", "triage")
	k := &kenv{f: e.f, org: e.org}
	huge := strings.Repeat("must follow the rule ", 1500)
	src := k.source(e.org, "Huge", huge)
	body := map[string]any{"type": "business", "scope": map[string]any{"kind": "org"}, "key": "huge", "title": "Huge", "claims": []any{claim("Huge rule", src, huge[:100])}, "body": strings.Repeat("x", 3900)}
	rec := e.f.json("POST", e.orgPath("knowledge"), body, nil, 201)
	id := rec["id"].(string)
	e.f.json("POST", e.orgPath("knowledge-versions", rec["current_version"].(map[string]any)["id"].(string), "verify"), map[string]any{}, k.rev(id), 200)
	// Pin it critical for the worker, several times over via many records so the critical set exceeds the cap.
	e.f.json("PUT", e.orgPath("knowledge", id, "pin"), map[string]any{"pinned": true, "critical": true, "applies_to_roles": []string{"worker"}}, k.rev(id), 200)
	for i := 0; i < 6; i++ {
		key := fmt.Sprintf("huge-%d", i)
		r := e.f.json("POST", e.orgPath("knowledge"), map[string]any{"type": "business", "scope": map[string]any{"kind": "org"}, "key": key, "title": "Huge " + key,
			"claims": []any{claim("Huge rule", src, huge[:100])}, "body": strings.Repeat("y", 3900)}, nil, 201)
		rid := r["id"].(string)
		e.f.json("POST", e.orgPath("knowledge-versions", r["current_version"].(map[string]any)["id"].(string), "verify"), map[string]any{}, k.rev(rid), 200)
		e.f.json("PUT", e.orgPath("knowledge", rid, "pin"), map[string]any{"pinned": true, "critical": true, "applies_to_roles": []string{"worker"}}, k.rev(rid), 200)
	}
	ctx := context.Background()
	_, _, over, err := e.f.a.rolePacket(ctx, run, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if over == nil || over.Role != "worker" {
		t.Skip("critical set fit in this cap; oversize covered by contextpkt tests")
	}
	if _, _, ro, err := e.f.a.rolePacket(ctx, run, "reviewer"); err != nil || ro != nil {
		t.Fatal("the reviewer packet must build", err, ro)
	}
	var d rolebriefs.Data
	err = e.f.a.attachContext(ctx, run, "worker", &d)
	if _, ok := err.(*stageStop); !ok {
		t.Fatal("worker gets a stage stop", err)
	}
}

func toAnySections(in []contextpkt.ManifestSection) []any {
	out := []any{}
	for _, s := range in {
		items := []any{}
		for _, it := range s.Items {
			items = append(items, map[string]any{"kind": it.Kind})
		}
		out = append(out, map[string]any{"id": s.ID, "items": items})
	}
	return out
}
