package server

import (
	"encoding/json"
	"fmt"
	"orqen/internal/wizard"
	"testing"
)

func wizardPath(org string) string { return "/api/v1/orgs/" + org + "/setup" }
func stepOf(w map[string]any, key string) map[string]any {
	for _, s := range w["steps"].([]any) {
		if m := s.(map[string]any); m["key"] == key {
			return m
		}
	}
	return nil
}
func readinessBytes(e *projectEnv, pid string) string {
	r := e.f.json("GET", e.path(pid, "readiness"), nil, nil, 200)
	delete(r, "evaluated_at")
	b, _ := json.Marshal(r)
	return string(b)
}
func TestWizardSynthesizedAndMutations(t *testing.T) {
	e, _ := newReadyProject(t)
	base := wizardPath(e.org)
	resp := e.f.request("GET", base, nil, nil)
	if resp.Header.Get("ETag") != "\"0\"" {
		t.Fatal(resp.Header)
	}
	w := readResponse(t, resp, 200)
	if w["revision"] != float64(0) || w["current_step"] != "organization" || w["dismissed"] != false || w["completed_at"] != nil || len(w["steps"].([]any)) != 7 || w["pending_count"] != float64(7) {
		t.Fatal(w)
	}
	for i, s := range w["steps"].([]any) {
		m := s.(map[string]any)
		if m["key"] != wizard.Steps[i] || m["status"] != "todo" || m["attention"] != false {
			t.Fatal(m)
		}
	}
	if c := stepOf(w, "profile")["consequences"].([]any); len(c) != 3 || c[2] != "jev_fallback_rules" {
		t.Fatal(c)
	}
	// Completing a step whose criteria do not hold is refused with the missing items.
	step := func(key, action string, rev any, body any, status int) map[string]any {
		return e.f.json("POST", base+"/steps/"+key+"/"+action, body, map[string]string{"If-Match": fmt.Sprint(rev)}, status)
	}
	refused := step("projects", "complete", 0, nil, 409)
	expectCode(t, refused, "wizard_step_incomplete")
	if refused["errors"].([]any)[0].(map[string]any)["field"] != "no_configured_project" {
		t.Fatal(refused)
	}
	// The connections criteria hold (both connections are verified), so completing creates the document.
	w = step("connections", "complete", 0, nil, 200)
	if w["revision"] != float64(1) || stepOf(w, "connections")["status"] != "done" || stepOf(w, "connections")["updated_at"] == nil || w["pending_count"] != float64(6) {
		t.Fatal(w)
	}
	// Stale and missing preconditions.
	expectCode(t, step("policy", "skip", 0, nil, 409), "revision_conflict")
	conflict := step("policy", "skip", 0, nil, 409)
	if conflict["current"].(map[string]any)["revision"] != float64(1) {
		t.Fatal(conflict)
	}
	e.f.json("POST", base+"/steps/policy/skip", nil, nil, 428)
	step("nope", "complete", 1, nil, 404)
	step("nope", "skip", 1, nil, 404)
	// Skip keeps the reason; reopen puts the step back to todo.
	w = step("policy", "skip", 1, map[string]any{"reason": "  later  "}, 200)
	if s := stepOf(w, "policy"); s["status"] != "skipped" || s["skip_reason"] != "later" {
		t.Fatal(s)
	}
	expectCode(t, step("policy", "skip", 2, map[string]any{"reason": string(make([]byte, 0)) + fmt.Sprintf("%0501d", 0)}, 422), "validation_failed")
	w = step("policy", "reopen", 2, nil, 200)
	if stepOf(w, "policy")["status"] != "todo" {
		t.Fatal(w)
	}
	// Resume position and hiding.
	w = e.f.json("PATCH", base, map[string]any{"current_step": "projects", "dismissed": true}, map[string]string{"If-Match": "3"}, 200)
	if w["current_step"] != "projects" || w["dismissed"] != true || w["revision"] != float64(4) {
		t.Fatal(w)
	}
	expectCode(t, e.f.json("PATCH", base, map[string]any{"current_step": "x"}, map[string]string{"If-Match": "4"}, 422), "validation_failed")
	expectCode(t, e.f.json("PATCH", base, map[string]any{}, map[string]string{"If-Match": "4"}, 422), "validation_failed")
	// Finishing is allowed with pending steps and keeps listing them.
	w = e.f.json("POST", base+"/finish", nil, map[string]string{"If-Match": "4"}, 200)
	if w["completed_at"] == nil || w["pending_count"] != float64(6) {
		t.Fatal(w)
	}
	events := e.f.json("GET", "/api/v1/orgs/"+e.org+"/events?limit=500", nil, nil, 200)
	seen := map[string]bool{}
	for _, x := range events["items"].([]any) {
		seen[x.(map[string]any)["type"].(string)] = true
	}
	for _, k := range []string{"setup.step_completed", "setup.step_skipped", "setup.step_reopened", "setup.finished"} {
		if !seen[k] {
			t.Errorf("missing event %s", k)
		}
	}
	// Another organization has its own wizard.
	other := e.f.org("beta")
	if ow := e.f.json("GET", wizardPath(other), nil, nil, 200); ow["revision"] != float64(0) || stepOf(ow, "connections")["status"] != "todo" {
		t.Fatal(ow)
	}
	e.f.json("GET", "/api/v1/orgs/org_01ARZ3NDEKTSV4RRFFQ69G5FAV/setup", nil, nil, 404)
}
func TestWizardSkipNeverChangesReadiness(t *testing.T) {
	e, s := newReadyProject(t)
	before := readinessBytes(e, s.pid)
	proj := e.f.json("GET", e.path(s.pid), nil, nil, 200)
	rev := 0
	for _, key := range wizard.Steps {
		w := e.f.json("POST", wizardPath(e.org)+"/steps/"+key+"/skip", nil, map[string]string{"If-Match": fmt.Sprint(rev)}, 200)
		rev = int(w["revision"].(float64))
	}
	if after := readinessBytes(e, s.pid); after != before {
		t.Fatalf("skipping changed readiness\n%s\n%s", before, after)
	}
	if again := e.f.json("GET", e.path(s.pid), nil, nil, 200); again["revision"] != proj["revision"] || again["automation_enabled"] != false {
		t.Fatal("skipping touched the project")
	}
	w := e.f.json("GET", wizardPath(e.org), nil, nil, 200)
	for _, st := range w["steps"].([]any) {
		if st.(map[string]any)["status"] != "skipped" {
			t.Fatal(st)
		}
	}
	if n, _ := e.f.db.Coll("model_profiles").CountDocuments(t.Context(), map[string]any{}); n != 0 {
		t.Fatal("skipping created profiles")
	}
	if n, _ := e.f.db.Coll("policy_versions").CountDocuments(t.Context(), map[string]any{}); n != 0 {
		t.Fatal("skipping published policy")
	}
}
func TestWizardCompletesWhenCriteriaHoldAndShowsAttention(t *testing.T) {
	e, s := newReadyProject(t)
	base := wizardPath(e.org)
	rev := 0
	do := func(key string) map[string]any {
		w := e.f.json("POST", base+"/steps/"+key+"/complete", nil, map[string]string{"If-Match": fmt.Sprint(rev)}, 200)
		rev = int(w["revision"].(float64))
		return w
	}
	do("organization")
	do("connections")
	s.all()
	do("projects")
	do("profile")
	// system needs a recent report; policy needs a published org version.
	e.f.json("POST", base+"/steps/system/complete", nil, map[string]string{"If-Match": fmt.Sprint(rev), "Idempotency-Key": "x"}, 409)
	pe := e.profiles()
	pe.catherdEnv.post("/doctor", nil)
	testKeyCounter++
	op := e.f.json("POST", "/api/v1/orgs/"+e.org+"/system/preflight", nil, map[string]string{"Idempotency-Key": fmt.Sprintf("pf%d", testKeyCounter)}, 202)
	e.f.waitOperation(e.org, op["id"].(string))
	report := e.f.json("GET", "/api/v1/orgs/"+e.org+"/system/preflight", nil, nil, 200)
	required := false
	for _, c := range report["checks"].([]any) {
		m := c.(map[string]any)
		required = required || m["required"] == true && m["status"] == "fail"
	}
	if !required {
		do("system")
	} else if w := e.f.json("GET", base, nil, nil, 200); len(stepOf(w, "system")["missing"].([]any)) != 1 {
		t.Fatal("a failed required check must show as missing", stepOf(w, "system"))
	}
	e.f.json("POST", "/api/v1/orgs/"+e.org+"/policy/publish", nil, map[string]string{"If-Match": "0", "Idempotency-Key": "pol"}, 201)
	w := do("policy")
	if stepOf(w, "policy")["status"] != "done" || stepOf(w, "profile")["attention"] != false {
		t.Fatal(w)
	}
	// A verified connection that loses its credential makes a done step show attention.
	conn := e.f.json("GET", connectionPath(e.org, e.jiID), nil, nil, 200)
	e.f.json("DELETE", connectionPath(e.org, e.jiID)+"/credential", nil, map[string]string{"If-Match": fmt.Sprint(int(conn["revision"].(float64)))}, 200)
	w = e.f.json("GET", base, nil, nil, 200)
	c := stepOf(w, "connections")
	if c["status"] != "done" || c["attention"] != true || len(c["missing"].([]any)) != 1 || c["missing"].([]any)[0].(map[string]any)["code"] != "no_ready_jira" {
		t.Fatal(c)
	}
	if w["pending_count"].(float64) < 1 {
		t.Fatal(w["pending_count"])
	}
}
