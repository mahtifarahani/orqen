package server

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http/httptest"
	"orqen/internal/catherd"
	"orqen/internal/testfakes"
	"strings"
	"testing"
	"time"
)

type projectEnv struct {
	f    *testApp
	org  string
	gl   *testfakes.GitLab
	jf   *testfakes.Jira
	glID string
	jiID string
	fake *catherd.Fake
	pid  string
	cfID string
	pid2 string
}

// newProjectEnv creates an org with a verified-shaped GitLab connection whose catalog is refreshed, and a Jira connection.
func newProjectEnv(t *testing.T) *projectEnv { return newProjectEnvWith(t, false) }

// newProjectEnvWith optionally installs a fake catherd, as a ready installation has.
func newProjectEnvWith(t *testing.T, withCatherd bool) *projectEnv {
	t.Helper()
	e := &projectEnv{gl: testfakes.NewGitLab(), jf: testfakes.NewJira()}
	glUp := httptest.NewServer(e.gl.Handler())
	jfUp := httptest.NewServer(e.jf.Handler())
	t.Cleanup(func() { glUp.Close(); jfUp.Close() })
	e.jf.Cloud = true
	if withCatherd {
		e.fake = catherd.NewFake()
		e.f = newTestAppWith(t, true, func(a *App) { a.SetCatherd(e.fake, fakeRuntime("1.5.0")) })
	} else {
		e.f = newTestApp(t, true)
	}
	e.f.bootstrap()
	e.org = e.f.org("alpha")
	markHostProbed(e.f, e.org)
	e.glID = e.f.providerConn(e.org, gitlabSpec(glUp.URL))
	e.jiID = e.f.providerConn(e.org, connSpec{Name: "Jira", Provider: "jira", Deployment: "cloud", Base: jfUp.URL + "/jira", Method: "api_token", Username: e.jf.User, Secret: e.jf.Token})
	e.f.refreshCatalog(e.org, e.glID)
	return e
}
func (e *projectEnv) path(parts ...string) string {
	p := "/api/v1/orgs/" + e.org + "/projects"
	for _, s := range parts {
		p += "/" + s
	}
	return p
}
func (e *projectEnv) add(key string, pids ...string) map[string]any {
	return e.f.json("POST", e.path("from-catalog"), FromCatalogRequest{ConnectionID: e.glID, ProviderProjectIDs: pids}, map[string]string{"Idempotency-Key": key}, 200)
}

// rev returns the current revision header value for If-Match.
func (e *projectEnv) rev(id string) string {
	p := e.f.json("GET", e.path(id), nil, nil, 200)
	return fmt.Sprint(int(p["revision"].(float64)))
}
func (e *projectEnv) mapping(id string, repo RepoMappingInput, jira *JiraMappingInput) map[string]any {
	return e.f.json("PUT", e.path(id, "mapping"), MappingInput{Repos: []RepoMappingInput{repo}, Jira: jira}, map[string]string{"If-Match": e.rev(id)}, 200)
}
func (e *projectEnv) check(id string) (map[string]any, map[string]map[string]any) {
	testKeyCounter++
	op := e.f.json("POST", e.path(id, "mapping", "check"), nil, map[string]string{"Idempotency-Key": fmt.Sprintf("check-%d", testKeyCounter)}, 202)
	done := e.f.waitOperation(e.org, op["id"].(string))
	p := e.f.json("GET", e.path(id), nil, nil, 200)
	results := map[string]map[string]any{}
	for _, r := range p["mapping_check"].(map[string]any)["results"].([]any) {
		m := r.(map[string]any)
		results[m["check"].(string)] = m
	}
	return done, results
}
func blockerCodes(readiness map[string]any) []string {
	out := []string{}
	for _, b := range readiness["blockers"].([]any) {
		out = append(out, b.(map[string]any)["code"].(string))
	}
	return out
}
func TestProjectsFromCatalog(t *testing.T) {
	e := newProjectEnv(t)
	first := e.add("one", "1", "2", "3")
	created := first["created"].([]any)
	if len(created) != 3 || len(first["existing"].([]any)) != 0 || len(first["missing"].([]any)) != 0 {
		t.Fatal(first)
	}
	if created[0].(map[string]any)["name"] != "proj-1" || created[0].(map[string]any)["state"] != "draft" || created[0].(map[string]any)["primary_repo"].(map[string]any)["path"] != "group-1/proj-1" {
		t.Fatal(created[0])
	}
	second := e.add("two", "2", "3", "999")
	if len(second["created"].([]any)) != 0 || len(second["existing"].([]any)) != 2 || len(second["missing"].([]any)) != 1 || second["missing"].([]any)[0] != "999" {
		t.Fatal(second)
	}
	// Replaying the same key returns the stored result without creating anything.
	replay := e.f.json("POST", e.path("from-catalog"), FromCatalogRequest{ConnectionID: e.glID, ProviderProjectIDs: []string{"1", "2", "3"}}, map[string]string{"Idempotency-Key": "one"}, 200)
	if len(replay["created"].([]any)) != 3 {
		t.Fatal(replay)
	}
	if n, _ := e.f.db.Coll("projects").CountDocuments(context.Background(), bson.M{"org_id": e.org}); n != 3 {
		t.Fatal(n)
	}
	list := e.f.walk(e.path())
	if len(list) != 3 {
		t.Fatal(list)
	}
	byName := e.f.walk(e.path() + "?search=PROJ-2")
	if len(byName) != 1 || byName[0]["name"] != "proj-2" {
		t.Fatal(byName)
	}
	// The catalog marks the selected rows.
	marked := e.f.walk(e.f.catalogPath(e.org, e.glID) + "?selection=selected")
	if len(marked) != 3 || marked[0]["project_id"] == nil {
		t.Fatal(marked)
	}
	// Validation.
	expectCode(t, e.f.json("POST", e.path("from-catalog"), FromCatalogRequest{ConnectionID: e.glID}, map[string]string{"Idempotency-Key": "empty"}, 422), "validation_failed")
	expectCode(t, e.f.json("POST", e.path("from-catalog"), FromCatalogRequest{ConnectionID: e.jiID, ProviderProjectIDs: []string{"1"}}, map[string]string{"Idempotency-Key": "jira"}, 422), "provider_mismatch")
	// A name already taken is made unique.
	e.f.json("PATCH", e.path(created[0].(map[string]any)["id"].(string)), PatchProjectRequest{Name: ptr("proj-4")}, map[string]string{"If-Match": "1"}, 200)
	third := e.add("three", "4")
	if third["created"].([]any)[0].(map[string]any)["name"] != "proj-4 (2)" {
		t.Fatal(third)
	}
}
func TestProjectsRevisionsArchiveAndIsolation(t *testing.T) {
	e := newProjectEnv(t)
	id := e.add("one", "1")["created"].([]any)[0].(map[string]any)["id"].(string)
	resp := e.f.request("GET", e.path(id), nil, nil)
	if resp.Header.Get("ETag") != "\"1\"" {
		t.Fatal(resp.Header)
	}
	readResponse(t, resp, 200)
	expectCode(t, e.f.json("PATCH", e.path(id), PatchProjectRequest{Name: ptr("x")}, nil, 428), "precondition_required")
	patched := e.f.json("PATCH", e.path(id), PatchProjectRequest{Name: ptr(" Renamed "), Description: ptr("about")}, map[string]string{"If-Match": "1"}, 200)
	if patched["name"] != "Renamed" || patched["description"] != "about" || patched["revision"] != float64(2) {
		t.Fatal(patched)
	}
	conflict := e.f.json("PATCH", e.path(id), PatchProjectRequest{Name: ptr("stale")}, map[string]string{"If-Match": "1"}, 409)
	expectCode(t, conflict, "revision_conflict")
	if conflict["current"].(map[string]any)["revision"] != float64(2) {
		t.Fatal(conflict)
	}
	expectCode(t, e.f.json("PATCH", e.path(id), PatchProjectRequest{}, map[string]string{"If-Match": "2"}, 422), "validation_failed")
	// Another organization cannot see it, and the same provider project is independent there.
	other := e.f.org("beta")
	for _, p := range []string{"/api/v1/orgs/" + other + "/projects/" + id, "/api/v1/orgs/" + other + "/projects/" + id + "/readiness"} {
		e.f.json("GET", p, nil, nil, 404)
	}
	e.f.json("PATCH", "/api/v1/orgs/"+other+"/projects/"+id, PatchProjectRequest{Name: ptr("x")}, map[string]string{"If-Match": "2"}, 404)
	e.f.json("GET", "/api/v1/orgs/"+other+"/projects/not-an-id", nil, nil, 404)
	// An enabled project cannot be archived.
	if _, err := e.f.db.Coll("projects").UpdateOne(context.Background(), bson.M{"_id": id}, bson.M{"$set": bson.M{"automation_enabled": true}}); err != nil {
		t.Fatal(err)
	}
	expectCode(t, e.f.json("DELETE", e.path(id), nil, map[string]string{"If-Match": "2"}, 409), "automation_enabled")
	if _, err := e.f.db.Coll("projects").UpdateOne(context.Background(), bson.M{"_id": id}, bson.M{"$set": bson.M{"automation_enabled": false}}); err != nil {
		t.Fatal(err)
	}
	e.f.json("DELETE", e.path(id), nil, map[string]string{"If-Match": "2"}, 204)
	e.f.json("GET", e.path(id), nil, nil, 404)
	// The repository can be selected again after archiving.
	again := e.add("again", "1")
	if len(again["created"].([]any)) != 1 || len(again["existing"].([]any)) != 0 {
		t.Fatal(again)
	}
}
func TestProjectsSameRepoInTwoOrganizations(t *testing.T) {
	e := newProjectEnv(t)
	e.add("a", "5")
	otherOrg := e.f.org("beta")
	conn := e.f.providerConn(otherOrg, gitlabSpec(e.f.url)) // the base URL is irrelevant: only the catalog row is needed
	now := bson.M{"org_id": otherOrg, "connection_id": conn, "provider_project_id": "5", "name": "proj-5", "path_with_namespace": "group-1/proj-5", "web_url": "x", "archived": false, "gone": false, "sort_key": "k", "search_text": "proj-5", "_id": newID("cpj")}
	if _, err := e.f.db.Coll("catalog_projects").InsertOne(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	res := e.f.json("POST", "/api/v1/orgs/"+otherOrg+"/projects/from-catalog", FromCatalogRequest{ConnectionID: conn, ProviderProjectIDs: []string{"5"}}, map[string]string{"Idempotency-Key": "b"}, 200)
	if len(res["created"].([]any)) != 1 || len(res["existing"].([]any)) != 0 {
		t.Fatal(res)
	}
	if n, _ := e.f.db.Coll("projects").CountDocuments(context.Background(), bson.M{}); n != 2 {
		t.Fatal(n)
	}
}
func TestProjectMappingValidation(t *testing.T) {
	e := newProjectEnv(t)
	a := e.add("a", "1")["created"].([]any)[0].(map[string]any)["id"].(string)
	b := e.add("b", "2")["created"].([]any)[0].(map[string]any)["id"].(string)
	repo := func(pid string) RepoMappingInput {
		return RepoMappingInput{Role: "primary", ConnectionID: e.glID, ProviderProjectID: pid, TargetBranch: "main"}
	}
	put := func(id string, m MappingInput, status int) map[string]any {
		return e.f.json("PUT", e.path(id, "mapping"), m, map[string]string{"If-Match": e.rev(id)}, status)
	}
	fieldOf := func(m map[string]any) string { return m["errors"].([]any)[0].(map[string]any)["field"].(string) }
	if f := fieldOf(put(a, MappingInput{Repos: []RepoMappingInput{{Role: "primary", ConnectionID: e.glID, ProviderProjectID: "1", TargetBranch: "bad branch"}}}, 422)); f != "mapping.repos[0].target_branch" {
		t.Fatal(f)
	}
	two := MappingInput{Repos: []RepoMappingInput{repo("1"), repo("3")}}
	if f := fieldOf(put(a, two, 422)); f != "mapping.repos" {
		t.Fatal(f)
	}
	rel := repo("3")
	rel.Role = "related"
	dup := MappingInput{Repos: []RepoMappingInput{repo("1"), rel, rel}}
	if f := fieldOf(put(a, dup, 422)); f != "mapping.repos[2].provider_project_id" {
		t.Fatal(f)
	}
	pkg := repo("1")
	pkg.PackagePath = ptr("../x")
	if f := fieldOf(put(a, MappingInput{Repos: []RepoMappingInput{pkg}}, 422)); f != "mapping.repos[0].package_path" {
		t.Fatal(f)
	}
	wrong := repo("1")
	wrong.ConnectionID = e.jiID
	expectCode(t, put(a, MappingInput{Repos: []RepoMappingInput{wrong}}, 422), "provider_mismatch")
	unknown := repo("1")
	unknown.ConnectionID = newID("con")
	if f := fieldOf(put(a, MappingInput{Repos: []RepoMappingInput{unknown}}, 422)); f != "mapping.repos[0].connection_id" {
		t.Fatal(f)
	}
	long := &JiraMappingInput{ConnectionID: e.jiID, ProjectID: "10000", JQL: ptr(strings.Repeat("a", 4001))}
	if f := fieldOf(put(a, MappingInput{Repos: []RepoMappingInput{repo("1")}, Jira: long}, 422)); f != "mapping.jira.jql" {
		t.Fatal(f)
	}
	badJira := &JiraMappingInput{ConnectionID: e.glID, ProjectID: "10000"}
	expectCode(t, put(a, MappingInput{Repos: []RepoMappingInput{repo("1")}, Jira: badJira}, 422), "provider_mismatch")
	// The same primary repository cannot back two projects.
	expectCode(t, put(b, MappingInput{Repos: []RepoMappingInput{repo("1")}}, 422), "repo_already_mapped")
	ok := put(a, MappingInput{Repos: []RepoMappingInput{pkgOK(repo("1"), "web/"), rel}, Jira: &JiraMappingInput{ConnectionID: e.jiID, ProjectID: "10000", ProjectKey: ptr("ORQ"), Board: &JiraBoardInput{ID: "1", Name: ptr("ORQ board")}, JQL: ptr(" project = ORQ ")}}, 200)
	m := ok["mapping"].(map[string]any)
	repos := m["repos"].([]any)
	if len(repos) != 2 || repos[0].(map[string]any)["package_path"] != "web" || repos[0].(map[string]any)["path_snapshot"] != "group-1/proj-1" || m["jira"].(map[string]any)["jql"] != "project = ORQ" || m["jira"].(map[string]any)["project_key_snapshot"] != "ORQ" {
		t.Fatal(m)
	}
	// Dropping the primary repository frees its key.
	put(a, MappingInput{Repos: []RepoMappingInput{rel}}, 200)
	put(b, MappingInput{Repos: []RepoMappingInput{repo("1")}}, 200)
}
func pkgOK(r RepoMappingInput, p string) RepoMappingInput { r.PackagePath = &p; return r }
func TestProjectCommandsAndProtectedPaths(t *testing.T) {
	e := newProjectEnv(t)
	id := e.add("a", "1")["created"].([]any)[0].(map[string]any)["id"].(string)
	put := func(body any, kind string, status int) map[string]any {
		return e.f.json("PUT", e.path(id, kind), body, map[string]string{"If-Match": e.rev(id)}, status)
	}
	shell := "pnpm test && rm -rf /"
	res := put(PutCommandsRequest{Commands: []CommandInput{{Key: "test", Kind: "test", Argv: []string{shell}}}}, "commands", 200)
	cmd := res["commands"].([]any)[0].(map[string]any)
	if argv := cmd["argv"].([]any); len(argv) != 1 || argv[0] != shell || cmd["approved"] != false || cmd["cwd"] != "." {
		t.Fatal(cmd)
	}
	env := EnvVarInput{Name: "NPM_TOKEN", Source: EnvSourceInput{Kind: "literal", Value: ptr("secret")}}
	bad := put(PutCommandsRequest{Commands: []CommandInput{{Key: "test", Kind: "test", Argv: []string{"x"}, Env: []EnvVarInput{env}}}}, "commands", 422)
	if bad["errors"].([]any)[0].(map[string]any)["field"] != "commands[0].env[0].source" {
		t.Fatal(bad)
	}
	expectCode(t, put(PutCommandsRequest{Commands: []CommandInput{{Key: "test", Kind: "test", Argv: []string{"x"}, Cwd: ptr("../x")}}}, "commands", 422), "validation_failed")
	approved := put(PutCommandsRequest{Commands: []CommandInput{{Key: "test", Kind: "test", Argv: []string{"pnpm", "test"}, Approved: ptr(true)}}}, "commands", 200)
	if approved["commands"].([]any)[0].(map[string]any)["approved"] != true {
		t.Fatal(approved)
	}
	kept := put(PutCommandsRequest{Commands: []CommandInput{{Key: "test", Kind: "test", Argv: []string{"pnpm", "test"}}}}, "commands", 200)
	if kept["commands"].([]any)[0].(map[string]any)["approved"] != true {
		t.Fatal("unchanged approved command lost its approval")
	}
	changed := put(PutCommandsRequest{Commands: []CommandInput{{Key: "test", Kind: "test", Argv: []string{"pnpm", "test", "-u"}}}}, "commands", 200)
	if changed["commands"].([]any)[0].(map[string]any)["approved"] != false {
		t.Fatal("changed command kept approval")
	}
	expectCode(t, put(PutProtectedPathsRequest{Rules: []ProtectedPathInput{{Pattern: "[", Mode: "deny"}}}, "protected-paths", 422), "validation_failed")
	ok := put(PutProtectedPathsRequest{Rules: []ProtectedPathInput{{Pattern: ".github/**", Mode: "deny"}, {Pattern: "**/*.pem", Mode: "review", Note: ptr("keys")}}}, "protected-paths", 200)
	if len(ok["protected_paths"].([]any)) != 2 {
		t.Fatal(ok)
	}
	cleared := put(PutProtectedPathsRequest{Rules: []ProtectedPathInput{}}, "protected-paths", 200)
	if len(cleared["protected_paths"].([]any)) != 0 {
		t.Fatal(cleared)
	}
}
func TestMappingCheck(t *testing.T) {
	e := newProjectEnv(t)
	e.gl.Files["web/package.json"] = "{}"
	id := e.add("a", "1")["created"].([]any)[0].(map[string]any)["id"].(string)
	repo := RepoMappingInput{Role: "primary", ConnectionID: e.glID, ProviderProjectID: "1", TargetBranch: "main", PackagePath: ptr("web")}
	jira := &JiraMappingInput{ConnectionID: e.jiID, ProjectID: "10000", Board: &JiraBoardInput{ID: "1"}, JQL: ptr("project = ORQ")}
	e.mapping(id, repo, jira)
	readiness := e.f.json("GET", e.path(id, "readiness"), nil, nil, 200)
	if !contains(blockerCodes(readiness), "mapping_not_checked") {
		t.Fatal(readiness)
	}
	op, results := e.check(id)
	if op["status"] != "succeeded" {
		t.Fatal(op)
	}
	for _, name := range []string{"repo_access:1", "target_branch:1", "package_path:1", "jira_project", "jira_board", "jql"} {
		if results[name]["status"] != "passed" {
			t.Fatal(name, results)
		}
	}
	p := e.f.json("GET", e.path(id), nil, nil, 200)
	if p["mapping"].(map[string]any)["jira"].(map[string]any)["project_key_snapshot"] != "ORQ" {
		t.Fatal("snapshot not refreshed", p["mapping"])
	}
	readiness = e.f.json("GET", e.path(id, "readiness"), nil, nil, 200)
	codes := blockerCodes(readiness)
	if contains(codes, "mapping_not_checked") || contains(codes, "mapping_check_stale") || contains(codes, "mapping_check_failed") || contains(codes, "jira_mapping_missing") || contains(codes, "primary_repo_missing") {
		t.Fatal(codes)
	}
	// A changed mapping makes the check stale.
	repo.TargetBranch = "develop"
	e.mapping(id, repo, jira)
	readiness = e.f.json("GET", e.path(id, "readiness"), nil, nil, 200)
	if !contains(blockerCodes(readiness), "mapping_check_stale") {
		t.Fatal(readiness)
	}
	// Failures are reported per check.
	repo.TargetBranch, repo.PackagePath = "nope", ptr("missing-dir")
	jira.Board, jira.JQL = &JiraBoardInput{ID: "3"}, ptr("BROKEN")
	e.mapping(id, repo, jira)
	_, results = e.check(id)
	for name, code := range map[string]string{"target_branch:1": "branch_not_found", "package_path:1": "package_path_not_found", "jira_board": "board_project_mismatch", "jql": "jql_invalid"} {
		if results[name]["status"] != "failed" || results[name]["code"] != code {
			t.Fatal(name, results[name])
		}
	}
	readiness = e.f.json("GET", e.path(id, "readiness"), nil, nil, 200)
	if !contains(blockerCodes(readiness), "mapping_check_failed") {
		t.Fatal(readiness)
	}
	// Archived catalog rows block, and a connection that lost its credential does too.
	jira.Board, jira.JQL = nil, nil
	repo.TargetBranch, repo.PackagePath = "main", nil
	e.mapping(id, repo, jira)
	e.f.json("DELETE", connectionPath(e.org, e.jiID)+"/credential", nil, map[string]string{"If-Match": "2"}, 200)
	readiness = e.f.json("GET", e.path(id, "readiness"), nil, nil, 200)
	if !contains(blockerCodes(readiness), "jira_connection_not_ready") || !contains(blockerCodes(readiness), "jira_filter_missing") {
		t.Fatal(readiness)
	}
	_, results = e.check(id)
	if results["jira_project"]["status"] != "failed" || results["jira_project"]["code"] != "credential_missing" {
		t.Fatal(results)
	}
}
func TestMappingCheckDeduplicates(t *testing.T) {
	f := newTestApp(t, false)
	f.bootstrap()
	org := f.org("alpha")
	now := time.Now().UTC()
	doc := ProjectDoc{ID: newID("prj"), OrgID: org, Name: "p", NameKey: "p", Active: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := f.db.Coll("projects").InsertOne(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/orgs/" + org + "/projects/" + doc.ID + "/mapping/check"
	first := f.json("POST", path, nil, map[string]string{"Idempotency-Key": "one"}, 202)
	second := f.json("POST", path, nil, map[string]string{"Idempotency-Key": "two"}, 202)
	if first["id"] != second["id"] || second["deduplicated"] != true {
		t.Fatal(first, second)
	}
	f.json("POST", path, nil, nil, 422)
}
func TestProjectReadinessOfFreshProject(t *testing.T) {
	e := newProjectEnv(t)
	id := e.add("a", "7")["created"].([]any)[0].(map[string]any)["id"].(string)
	readiness := e.f.json("GET", e.path(id, "readiness"), nil, nil, 200)
	codes := blockerCodes(readiness)
	for _, want := range []string{"jira_mapping_missing", "mapping_not_checked", "test_command_missing", "contract_not_imported", "profile_not_bound", "catherd_unavailable", "catherd_doctor_missing", "repo_connection_not_ready", "repo_archived"} {
		if !contains(codes, want) {
			t.Errorf("missing blocker %s in %v", want, codes)
		}
	}
	if readiness["ready"] != false || readiness["state"] != "draft" {
		t.Fatal(readiness)
	}
	p := e.f.json("GET", e.path(id), nil, nil, 200)
	if int(p["blocker_count"].(float64)) != len(codes) {
		t.Fatal(p["blocker_count"], len(codes))
	}
}
