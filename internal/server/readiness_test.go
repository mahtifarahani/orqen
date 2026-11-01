package server

import (
	"fmt"
	"sort"
	"testing"
)

func (e *projectEnv) profiles() *profileEnv {
	return &profileEnv{&catherdEnv{f: e.f, fake: e.fake, org: e.org}}
}
func (e *projectEnv) ready() map[string]any {
	return e.f.json("GET", e.path(e.pid, "readiness"), nil, nil, 200)
}
func sortedCodes(r map[string]any) []string {
	c := blockerCodes(r)
	sort.Strings(c)
	return c
}

// setUp walks a project through every requirement except those named in skip, using only the public API.
type setup struct {
	e        *projectEnv
	pid      string
	version  string // published profile version bound to the project
	snapshot string
}

func newReadyProject(t *testing.T) (*projectEnv, *setup) {
	e := newProjectEnvWith(t, true)
	e.f.runTest(e.org, e.glID)
	e.f.runTest(e.org, e.jiID)
	pid := e.add("a", "1")["created"].([]any)[0].(map[string]any)["id"].(string)
	e.pid = pid
	return e, &setup{e: e, pid: pid}
}
func (s *setup) mapJira() {
	s.e.mapping(s.pid, RepoMappingInput{Role: "primary", ConnectionID: s.e.glID, ProviderProjectID: "1", TargetBranch: "main"}, &JiraMappingInput{ConnectionID: s.e.jiID, ProjectID: "10000", Board: &JiraBoardInput{ID: "1"}})
	s.e.check(s.pid)
}
func (s *setup) contract() {
	snap := s.e.importContract(s.pid)
	s.snapshot = snap["id"].(string)
	cmd := CommandInput{Key: "test", Kind: "test", Argv: []string{"pnpm", "run", "test"}, Approved: ptr(true)}
	s.e.f.json("PUT", s.e.path(s.pid, "commands"), PutCommandsRequest{Commands: []CommandInput{cmd}}, map[string]string{"If-Match": s.e.rev(s.pid)}, 200)
	s.e.f.json("POST", s.e.path(s.pid, "contract", "snapshots", s.snapshot, "review"), nil, map[string]string{"If-Match": s.e.rev(s.pid)}, 200)
}
func (s *setup) doctor() {
	s.e.profiles().catherdEnv.post("/doctor", nil)
}
func (s *setup) profile(doc map[string]any) string {
	pe := s.e.profiles()
	id := pe.create(fmt.Sprintf("p%d", len(s.e.fake.CallsOf("create"))), doc)
	if op := pe.validate(id); op["result"].(map[string]any)["valid"] != true {
		s.e.f.t.Fatal(op)
	}
	pub := pe.publish(id)
	if pub["status"] != "succeeded" {
		s.e.f.t.Fatal(pub)
	}
	return pub["result"].(map[string]any)["version_id"].(string)
}
func (s *setup) bind(version string) map[string]any {
	return s.e.f.json("PUT", s.e.path(s.pid, "profile-binding"), BindingRequest{VersionID: &version}, map[string]string{"If-Match": s.e.rev(s.pid)}, 200)
}
func (s *setup) all() {
	s.mapJira()
	s.contract()
	s.doctor()
	s.version = s.profile(goodProfileDoc())
	s.bind(s.version)
}
func TestReadinessFreshProjectAndStepByStep(t *testing.T) {
	e, s := newReadyProject(t)
	want := []string{"catherd_doctor_missing", "contract_not_imported", "jira_mapping_missing", "mapping_not_checked", "profile_not_bound", "test_command_missing"}
	if got := sortedCodes(e.ready()); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	s.mapJira()
	s.contract()
	s.doctor()
	if got := sortedCodes(e.ready()); fmt.Sprint(got) != "[profile_not_bound]" {
		t.Fatal(got)
	}
	s.version = s.profile(goodProfileDoc())
	bound := s.bind(s.version)
	if bound["profile_binding"].(map[string]any)["version_id"] != s.version || bound["state"] != "ready" {
		t.Fatal(bound)
	}
	r := e.ready()
	if r["ready"] != true || r["state"] != "ready" || len(r["blockers"].([]any)) != 0 {
		t.Fatal(r)
	}
	if len(r["warnings"].([]any)) == 0 {
		t.Fatal("the fake doctor reports a warning that must surface")
	}
	// Enabling stores the policy snapshot and the profile version it ran on.
	rev := e.rev(s.pid)
	enabled := e.f.json("POST", e.path(s.pid, "automation", "enable"), nil, map[string]string{"If-Match": rev, "Idempotency-Key": nextKey()}, 200)
	if enabled["automation_enabled"] != true || enabled["state"] != "enabled" || enabled["enabled_profile_version_id"] != s.version || enabled["enabled_policy_version_id"] == nil || enabled["enabled_at"] == nil {
		t.Fatal(enabled)
	}
	pol := e.f.json("GET", "/api/v1/orgs/"+e.org+"/policy/versions", nil, nil, 200)
	if len(pol["items"].([]any)) != 0 {
		t.Fatal("a resolved snapshot is not an org version")
	}
	snap, err := findOne[PolicyVersionDoc](t.Context(), e.f.db, "policy_versions", map[string]any{"_id": enabled["enabled_policy_version_id"]})
	if err != nil || snap.Kind != "resolved" || snap.ScopeID != s.pid || snap.Resolved["automation.intake"] == nil || len(snap.Hash) != 64 {
		t.Fatal(snap, err)
	}
	// Disabling clears the flag and the stored ids.
	off := e.f.json("POST", e.path(s.pid, "automation", "disable"), nil, map[string]string{"If-Match": e.rev(s.pid)}, 200)
	if off["automation_enabled"] != false || off["enabled_policy_version_id"] != nil || off["state"] != "ready" {
		t.Fatal(off)
	}
	// A bound profile cannot be archived while the project is active.
	pid := e.f.json("GET", e.path(s.pid), nil, nil, 200)["profile_binding"].(map[string]any)["profile_id"].(string)
	expectCode(t, e.f.json("DELETE", "/api/v1/orgs/"+e.org+"/model-profiles/"+pid, nil, map[string]string{"If-Match": e.profiles().rev(pid)}, 409), "profile_in_use")
}
func TestReadinessNeverStaleGreen(t *testing.T) {
	e, s := newReadyProject(t)
	s.all()
	e.f.json("POST", e.path(s.pid, "automation", "enable"), nil, map[string]string{"If-Match": e.rev(s.pid), "Idempotency-Key": nextKey()}, 200)
	// Changing the target branch makes the stored check stale.
	e.mapping(s.pid, RepoMappingInput{Role: "primary", ConnectionID: e.glID, ProviderProjectID: "1", TargetBranch: "develop"}, &JiraMappingInput{ConnectionID: e.jiID, ProjectID: "10000", Board: &JiraBoardInput{ID: "1"}})
	r := e.ready()
	if r["state"] != "enabled_blocked" || !contains(blockerCodes(r), "mapping_check_stale") {
		t.Fatal(r)
	}
	if p := e.f.json("GET", e.path(s.pid), nil, nil, 200); p["state"] != "enabled_blocked" || p["blocker_count"] == float64(0) {
		t.Fatal(p)
	}
	list := e.f.walk(e.path())
	if list[0]["state"] != "enabled_blocked" {
		t.Fatal(list[0])
	}
	// Checking again clears it.
	e.check(s.pid)
	if r = e.ready(); r["state"] != "enabled" {
		t.Fatal(r)
	}
}
func TestReadinessProfileRules(t *testing.T) {
	e, s := newReadyProject(t)
	s.all()
	pe := e.profiles()
	// Version 2 of the same profile with the reviewer disabled: bound, it blocks.
	pid := e.f.json("GET", e.path(s.pid), nil, nil, 200)["profile_binding"].(map[string]any)["profile_id"].(string)
	doc := goodProfileDoc()
	doc["roles"].(map[string]any)["reviewer"].(map[string]any)["enabled"] = false
	pe.putDraft(pid, doc, 200)
	pe.validate(pid)
	v2 := pe.publish(pid)["result"].(map[string]any)["version_id"].(string)
	// Binding stays on v1 after v2 is published.
	if p := e.f.json("GET", e.path(s.pid), nil, nil, 200); p["profile_binding"].(map[string]any)["version_id"] != s.version || p["state"] != "ready" {
		t.Fatal(p)
	}
	s.bind(v2)
	r := e.ready()
	found := false
	for _, b := range r["blockers"].([]any) {
		m := b.(map[string]any)
		found = found || m["code"] == "profile_not_automation_ready" && m["detail"] == "reviewer_or_verifier_disabled"
	}
	if !found {
		t.Fatal(r)
	}
	// Same-model reviewers and unknown models cannot be published, so the rule is exercised via a catalog change.
	s.bind(s.version)
	if r = e.ready(); r["ready"] != true {
		t.Fatal(r)
	}
	// No budget choice blocks.
	nb := goodProfileDoc()
	nb["budget"] = map[string]any{}
	pe.putDraft(pid, nb, 200)
	pe.validate(pid)
	v3 := pe.publish(pid)["result"].(map[string]any)["version_id"].(string)
	s.bind(v3)
	r = e.ready()
	found = false
	for _, b := range r["blockers"].([]any) {
		found = found || b.(map[string]any)["detail"] == "budget_not_chosen"
	}
	if !found {
		t.Fatal(r)
	}
	// An unpublished or foreign version cannot be bound.
	expectCode(t, e.f.json("PUT", e.path(s.pid, "profile-binding"), BindingRequest{VersionID: ptr(newID("mpv"))}, map[string]string{"If-Match": e.rev(s.pid)}, 422), "version_not_published")
	cleared := e.f.json("PUT", e.path(s.pid, "profile-binding"), BindingRequest{}, map[string]string{"If-Match": e.rev(s.pid)}, 200)
	if cleared["profile_binding"] != nil || !contains(blockerCodes(e.ready()), "profile_not_bound") {
		t.Fatal(cleared)
	}
	// Doctor failures that concern the profile's backends block; unrelated ones do not.
	s.bind(s.version)
	e.fake.DoctorJSON = []byte(`{"ready":false,"version":"1.5.0","checks":[{"id":"backend:cursor","label":"cursor","state":"fail","word":"fail","detail":"","fix":""}]}`)
	e.fake.DoctorExit = 3
	s.doctor()
	if r = e.ready(); r["ready"] != true {
		t.Fatal("a failing backend the profile does not use must not block", r)
	}
	e.fake.DoctorJSON = []byte(`{"ready":false,"version":"1.5.0","checks":[{"id":"backend:codex","label":"codex","state":"fail","word":"fail","detail":"","fix":""}]}`)
	s.doctor()
	if r = e.ready(); !contains(blockerCodes(r), "catherd_doctor_failing") {
		t.Fatal(r)
	}
}
func TestReadinessEnableRefusedWithBlockers(t *testing.T) {
	e, s := newReadyProject(t)
	body := func(key string) map[string]any {
		return e.f.json("POST", e.path(s.pid, "automation", "enable"), nil, map[string]string{"If-Match": e.rev(s.pid), "Idempotency-Key": key}, 409)
	}
	first := body("same-key")
	expectCode(t, first, "project_not_ready")
	fields := map[string]bool{}
	for _, x := range first["errors"].([]any) {
		fields[x.(map[string]any)["field"].(string)] = true
	}
	if !fields["jira_mapping_missing"] || !fields["profile_not_bound"] || len(fields) < 5 {
		t.Fatal(first)
	}
	// The same key replays the stored 409 even after the project became ready.
	s.all()
	resp := e.f.request("POST", e.path(s.pid, "automation", "enable"), nil, map[string]string{"If-Match": "1", "Idempotency-Key": "same-key"})
	if resp.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal(resp.Header)
	}
	readResponse(t, resp, 409)
	// A stale If-Match is a conflict, not a readiness error.
	expectCode(t, e.f.json("POST", e.path(s.pid, "automation", "enable"), nil, map[string]string{"If-Match": "1", "Idempotency-Key": nextKey()}, 409), "revision_conflict")
	e.f.json("POST", e.path(s.pid, "automation", "enable"), nil, map[string]string{"Idempotency-Key": nextKey()}, 428)
	// Enabled projects cannot be archived; disabling first allows it.
	e.f.json("POST", e.path(s.pid, "automation", "enable"), nil, map[string]string{"If-Match": e.rev(s.pid), "Idempotency-Key": nextKey()}, 200)
	expectCode(t, e.f.json("DELETE", e.path(s.pid), nil, map[string]string{"If-Match": e.rev(s.pid)}, 409), "automation_enabled")
}
func TestReadinessConnectionCredentialRemoved(t *testing.T) {
	e, s := newReadyProject(t)
	s.all()
	if e.ready()["ready"] != true {
		t.Fatal("setup")
	}
	conn := e.f.json("GET", connectionPath(e.org, e.jiID), nil, nil, 200)
	e.f.json("DELETE", connectionPath(e.org, e.jiID)+"/credential", nil, map[string]string{"If-Match": fmt.Sprint(int(conn["revision"].(float64)))}, 200)
	r := e.ready()
	if !contains(blockerCodes(r), "jira_connection_not_ready") || r["ready"] != false {
		t.Fatal(r)
	}
}
func TestReadinessUnselectedCatalogProjectHasNoReadiness(t *testing.T) {
	e, _ := newReadyProject(t)
	rows := e.f.walk(e.f.catalogPath(e.org, e.glID) + "?selection=unselected")
	if len(rows) == 0 || rows[0]["project_id"] != nil {
		t.Fatal("unselected rows carry no project id")
	}
	e.f.json("GET", e.path(newID("prj"), "readiness"), nil, nil, 404)
	var kinds = map[string]bool{}
	cur, err := e.f.db.Coll("jobs").Find(t.Context(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var jobs []JobDoc
	_ = cur.All(t.Context(), &jobs)
	for _, j := range jobs {
		kinds[j.Kind] = true
	}
	for k := range kinds {
		if k != "catalog_refresh" && k != "connection_test" && k != "source_sync" { // source_sync: scheduled sweep (WP119), not discovery
			t.Fatal("discovery started a job of kind", k)
		}
	}
}
