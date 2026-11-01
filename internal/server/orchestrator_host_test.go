package server

import (
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"testing"
)

// setHost publishes an org policy version with orchestrator.host.
func (e *projectEnv) setHost(host string) map[string]any {
	e.f.t.Helper()
	base := "/api/v1/orgs/" + e.org + "/policy"
	rev := fmt.Sprint(int(e.f.json("GET", base+"/draft", nil, nil, 200)["revision"].(float64)))
	e.f.json("PUT", base+"/draft", map[string]any{"overrides": map[string]any{hostPolicyKey: host}}, map[string]string{"If-Match": rev}, 200)
	next := fmt.Sprint(int(e.f.json("GET", base+"/draft", nil, nil, 200)["revision"].(float64)))
	return e.f.json("POST", base+"/publish", nil, map[string]string{"If-Match": next, "Idempotency-Key": nextKey()}, 201)
}
func (e *projectEnv) hosts() map[string]map[string]any {
	resp := e.f.request("GET", "/api/v1/orgs/"+e.org+"/catherd/hosts", nil, nil)
	var rows []map[string]any
	if err := decodeBody(resp, &rows); err != nil {
		e.f.t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, r := range rows {
		out[r["host"].(string)] = r
	}
	return out
}

func TestOrchestratorHost(t *testing.T) {
	e, s := newReadyProject(t)
	s.all()
	pe := e.profiles()
	profileID := e.f.json("GET", e.path(s.pid), nil, nil, 200)["profile_binding"].(map[string]any)["profile_id"].(string)
	if r := e.ready(); r["ready"] != true {
		t.Fatal(r)
	}
	h := e.hosts()
	if len(h) != 2 || h["codex"]["effective"] != true || h["claude-code"]["effective"] != false || h["codex"]["orqen_host_adapter"] != "available" || h["claude-code"]["orqen_host_adapter"] != "unavailable" || h["codex"]["last_doctor"] == nil || h["claude-code"]["last_doctor"] != nil {
		t.Fatal(h)
	}
	v1Raw, _ := json.Marshal(pe.version(s.version))
	if pe.version(s.version)["host"] != "codex" {
		t.Fatal("v1 is built for codex")
	}
	// The host is org-only.
	prjBase := e.path(s.pid, "policy")
	e.f.json("PUT", prjBase+"/draft", map[string]any{"overrides": map[string]any{hostPolicyKey: "claude-code"}}, map[string]string{"If-Match": "0"}, 422)
	e.f.json("PUT", "/api/v1/orgs/"+e.org+"/policy/draft", map[string]any{"overrides": map[string]any{hostPolicyKey: "auto"}}, map[string]string{"If-Match": "0"}, 422)

	// Publish claude-code: both blockers show on the bound codex v1 (and the new host has no doctor yet).
	pub := e.setHost("claude-code")
	if pub["number"] != float64(1) {
		t.Fatal(pub)
	}
	r := e.ready()
	got := blockerCodes(r)
	for _, want := range []string{"profile_host_mismatch", "host_adapter_unavailable", "catherd_doctor_missing"} {
		if !contains(got, want) {
			t.Fatalf("missing %s in %v", want, got)
		}
	}
	if st := e.f.json("GET", "/api/v1/orgs/"+e.org+"/catherd/status", nil, nil, 200); st["host"] != "claude-code" || st["last_doctor"] != nil {
		t.Fatal("no stale ready from another host's doctor:", st)
	}
	// Publishing a stale validation (made for codex) is refused: it must be revalidated under the new host.
	pe.putDraft(profileID, goodProfileDoc(), 200)
	expectCode(t, e.f.json("POST", pe.base()+"/"+profileID+"/publish", nil, map[string]string{"If-Match": pe.rev(profileID), "Idempotency-Key": nextKey()}, 409), "profile_not_validated")

	// Doctor for claude-code is stored beside the codex one and leaves it untouched.
	stateBefore, _ := findOne[CatherdStateDoc](t.Context(), e.f.db, "catherd_states", bson.M{"_id": catherdStateID(e.org)})
	codexOp := stateBefore.Doctors["codex"].OperationID
	op := pe.op("POST", pe.catherdEnv.path("/doctor"), map[string]any{"host": "claude-code"}, nil, 202)
	if op["status"] != "succeeded" || op["result"].(map[string]any)["host"] != "claude-code" {
		t.Fatal(op)
	}
	stateAfter, _ := findOne[CatherdStateDoc](t.Context(), e.f.db, "catherd_states", bson.M{"_id": catherdStateID(e.org)})
	if stateAfter.Doctors["codex"].OperationID != codexOp || stateAfter.Doctors["claude-code"].Host != "claude-code" || stateAfter.Doctors["claude-code"].OperationID != op["id"] || stateAfter.LastDoctor != nil {
		t.Fatal(stateAfter.Doctors, stateAfter.LastDoctor)
	}
	if c := e.fake.CallsOf("doctor"); c[len(c)-1].Host != "claude-code" {
		t.Fatal(c[len(c)-1])
	}
	e.f.json("POST", pe.catherdEnv.path("/doctor"), map[string]any{"host": "auto"}, map[string]string{"Idempotency-Key": nextKey()}, 422)

	// Republish for the current host = rollback to the same version: v2, host claude-code, v1 byte-identical.
	pubv := pe.op("POST", pe.base()+"/"+profileID+"/rollback", map[string]any{"version_id": s.version}, map[string]string{"If-Match": pe.rev(profileID)}, 202)
	if pubv["status"] != "succeeded" {
		t.Fatal(pubv)
	}
	v2 := pe.version(pubv["result"].(map[string]any)["version_id"].(string))
	if v2["host"] != "claude-code" || v2["number"] != float64(2) || v2["validation"].(map[string]any)["host"] != "claude-code" || v2["doc_hash"] != pe.version(s.version)["doc_hash"] {
		t.Fatal(v2)
	}
	if after, _ := json.Marshal(pe.version(s.version)); string(after) != string(v1Raw) {
		t.Fatal("v1 must stay byte-identical")
	}
	sawHost := map[string]bool{}
	for _, c := range e.fake.Calls() {
		if c.Host == "claude-code" {
			sawHost[c.Op] = true
		}
	}
	for _, op := range []string{"create", "set", "validate", "show_raw", "show_resolved", "doctor"} {
		if !sawHost[op] {
			t.Fatalf("no %s call ran with --host claude-code: %v", op, sawHost)
		}
	}
	// Binding v2 clears the mismatch; the missing adapter remains an honest blocker.
	s.bind(v2["id"].(string))
	got = blockerCodes(e.ready())
	if contains(got, "profile_host_mismatch") || contains(got, "catherd_doctor_missing") || !contains(got, "host_adapter_unavailable") {
		t.Fatal(got)
	}
	if h = e.hosts(); h["claude-code"]["effective"] != true || h["claude-code"]["last_doctor"] == nil || h["codex"]["last_doctor"] == nil {
		t.Fatal(h)
	}
	// Legacy last_doctor is read as the codex report.
	if _, err := e.f.db.Coll("catherd_states").UpdateOne(t.Context(), bson.M{"_id": catherdStateID(e.org)}, bson.M{"$rename": bson.M{"doctors.codex": "last_doctor"}}); err != nil {
		t.Fatal(err)
	}
	if h = e.hosts(); h["codex"]["last_doctor"] == nil || h["codex"]["last_doctor"].(map[string]any)["host"] != "codex" || h["claude-code"]["last_doctor"] == nil {
		t.Fatal("legacy last_doctor must read as codex:", h)
	}
	// Back to codex: v2 (claude-code) mismatches again until republished; the codex doctor is current.
	e.setHost("codex")
	got = blockerCodes(e.ready())
	if !contains(got, "profile_host_mismatch") || contains(got, "host_adapter_unavailable") || contains(got, "catherd_doctor_missing") {
		t.Fatal(got)
	}
	if len(e.fake.CallsOf("doctor")) != 2 {
		t.Fatal("doctors:", len(e.fake.CallsOf("doctor")))
	}
}
