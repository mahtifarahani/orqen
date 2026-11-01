package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"os"
	"testing"
	"time"
)

// TestWalkSetup builds the fixtures of the Sprint 4 manual verification walk on a RUNNING orqen (dev build, memory
// credential store) through its public API, then writes the ids to ORQEN_WALK_OUT. It skips unless ORQEN_WALK_URL is
// set, so it never runs in `make check`. It talks only to the URLs it is given (loopback fakes in the walk).
//
//	ORQEN_WALK_URL        http://127.0.0.1:<port> of the orqen under test
//	ORQEN_WALK_TOKEN      the bootstrap token printed by `orqen serve` / `orqen login-link`
//	ORQEN_WALK_FAKES      http://127.0.0.1:<port> of cmd/orqen-fakes
//	ORQEN_WALK_WRITE      the fake write token printed by cmd/orqen-fakes
//	ORQEN_WALK_OUT        file for the ids (JSON)
//	ORQEN_WALK_LOGIN=1    ORQEN_WALK_TOKEN is a login token (`orqen login-link`), not the bootstrap token
//	ORQEN_WALK_LIVE=1     also switch external writes to live for the fake GitLab project 1 (needs ORQEN_WALK_WRITE)
//	ORQEN_WALK_SLUG       slug of the organization to create (default walk)
func TestWalkSetup(t *testing.T) {
	base := os.Getenv("ORQEN_WALK_URL")
	if base == "" {
		t.Skip("ORQEN_WALK_URL is not set")
	}
	jar, _ := cookiejar.New(nil)
	f := &testApp{t: t, url: base, client: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
	route := "/api/v1/auth/bootstrap" // the first claim; a later run redeems a login token instead (ORQEN_WALK_LOGIN=1)
	if os.Getenv("ORQEN_WALK_LOGIN") == "1" {
		route = "/api/v1/auth/redeem"
	}
	m := f.json("POST", route, map[string]string{"token": os.Getenv("ORQEN_WALK_TOKEN")}, nil, 200)
	f.csrf = m["csrf_token"].(string)
	fakes := os.Getenv("ORQEN_WALK_FAKES")
	slug := os.Getenv("ORQEN_WALK_SLUG")
	if slug == "" {
		slug = "walk"
	}
	org := f.org(slug)
	e := &projectEnv{f: f, org: org}
	gl := gitlabSpec(fakes)
	gl.Secret = "glpat-test"
	e.glID = f.providerConn(org, gl)
	e.jiID = f.providerConn(org, connSpec{Name: "Jira", Provider: "jira", Deployment: "cloud", Base: fakes + "/jira", Method: "api_token", Username: "bot@example.com", Secret: "jira-token"})
	for name, op := range map[string]map[string]any{"catalog refresh": f.refreshCatalog(org, e.glID), "gitlab test": f.runTest(org, e.glID), "jira test": f.runTest(org, e.jiID)} {
		if op["status"] != "succeeded" {
			t.Fatalf("%s: %v", name, op)
		}
	}
	added := e.add("walk-add", "1")
	created, _ := added["created"].([]any)
	if len(created) == 0 {
		t.Fatalf("project 1 was not added: %v", added)
	}
	e.pid = created[0].(map[string]any)["id"].(string)
	jql := "key = ORQ-1"
	// Sprint 5: a Confluence connection (the fake's page 1001) mapped to the project, and ORQ-1 linking that page, so a run
	// stores a source version and the packet carries a design reference. Project 2 gives the graph its exact dependency
	// (the fakes seed project 2's package.json to depend on project 1's package).
	e.cfID = f.providerConn(org, connSpec{Name: "Confluence", Provider: "confluence", Deployment: "server", Base: fakes + "/confluence", Method: "personal_access_token", Secret: "confluence-token"})
	f.json("PUT", e.path(e.pid, "mapping"), MappingInput{Repos: []RepoMappingInput{{Role: "primary", ConnectionID: e.glID, ProviderProjectID: "1", TargetBranch: "main"}},
		Jira: &JiraMappingInput{ConnectionID: e.jiID, ProjectID: "10000", JQL: &jql}, Confluence: []ConfluenceRefInput{{ConnectionID: e.cfID, PageID: ptr("1001")}}}, map[string]string{"If-Match": e.rev(e.pid)}, 200)
	desc := "Users need to export the invoice list.\n\nDesign: " + fakes + "/confluence/pages/viewpage.action?pageId=1001\n\nh2. Acceptance Criteria\n* The export button downloads a CSV with all visible columns.\n* The file name contains the current date."
	body, _ := json.Marshal(map[string]string{"description": desc})
	if rq, err := http.NewRequest("PUT", fakes+"/_fakes/jira/issues/ORQ-1", bytes.NewReader(body)); err == nil {
		if rs, err := http.DefaultClient.Do(rq); err != nil || rs.StatusCode >= 300 {
			t.Fatalf("setting the ORQ-1 description on the fake: %v", err)
		} else {
			_ = rs.Body.Close()
		}
	}
	added2 := e.add("walk-add-2", "2")
	if c2, _ := added2["created"].([]any); len(c2) > 0 {
		p2 := c2[0].(map[string]any)["id"].(string)
		e.mapping(p2, RepoMappingInput{Role: "primary", ConnectionID: e.glID, ProviderProjectID: "2", TargetBranch: "main"}, nil)
		e.pid2 = p2
	}
	e.check(e.pid)

	// the approved commands of the fake repository
	snap := e.importContract(e.pid)
	cmds := []CommandInput{
		{Key: "test", Kind: "test", Argv: []string{"/bin/sh", "scripts/check.sh"}, Approved: ptr(true)},
		{Key: "lint", Kind: "lint", Argv: []string{"/bin/sh", "scripts/lint.sh"}, Approved: ptr(true)},
	}
	f.json("PUT", e.path(e.pid, "commands"), PutCommandsRequest{Commands: cmds}, map[string]string{"If-Match": e.rev(e.pid)}, 200)
	f.json("POST", e.path(e.pid, "contract", "snapshots", snap["id"].(string), "review"), nil, map[string]string{"If-Match": e.rev(e.pid)}, 200)

	// catherd doctor, a published profile with the architect, reviewer and verifier on codex rungs, bound to the project
	pe := &profileEnv{&catherdEnv{f: f, org: org}}
	pe.post("/catalog/refresh", map[string]any{"mode": "list"})
	pe.post("/doctor", nil)
	id := pe.create("walk", goodProfileDoc())
	if op := pe.validate(id); op["result"].(map[string]any)["valid"] != true {
		t.Fatal(op)
	}
	pub := pe.publish(id)
	if pub["status"] != "succeeded" {
		t.Fatal(pub)
	}
	version := pub["result"].(map[string]any)["version_id"].(string)
	f.json("PUT", e.path(e.pid, "profile-binding"), BindingRequest{VersionID: &version}, map[string]string{"If-Match": e.rev(e.pid)}, 200)

	// automatic plan approval and start (the intake itself is already automatic); one fix round more than the default is not needed
	overrides := map[string]any{"automation.intake": "auto", "automation.plan_approval": "auto", "automation.coding_start": "auto"}
	if os.Getenv("ORQEN_WALK_FAKEHOST") != "" {
		for k, v := range walkPolicyS6() {
			overrides[k] = v
		}
	}
	if walkS7() {
		for k, v := range walkPolicyS7() {
			overrides[k] = v
		}
	}
	e.publishPolicy("/api/v1/orgs/"+org+"/policy", overrides)

	// Sprint 6 fixtures (extension wrappers, security policy, poisoned skill) go in before automation is enabled: a run pins
	// the policy version of the moment automation was enabled. Skipped without ORQEN_WALK_FAKEHOST.
	ids := map[string]string{"org": org, "project": e.pid, "gitlab": e.glID, "jira": e.jiID, "confluence": e.cfID, "project2": e.pid2}
	e.walkSetupS6(ids)

	// probe the (fake) host, set the fake write credential on the GitLab connection (Publishing stays in dry-run), enable
	op := f.json("POST", "/api/v1/orgs/"+org+"/host/probe", nil, map[string]string{"Idempotency-Key": nextKey()}, 202)
	if done := f.waitOperation(org, op["id"].(string)); done["status"] != "succeeded" {
		t.Fatal(done)
	}
	if w := os.Getenv("ORQEN_WALK_WRITE"); w != "" {
		cred := f.json("GET", connectionPath(org, e.glID)+"/write-credential", nil, nil, 200)
		f.json("PUT", connectionPath(org, e.glID)+"/write-credential", map[string]string{"secret": w}, map[string]string{"If-Match": jsonNum(cred["revision"])}, 200)
	}
	e.enable()
	// Optional (ORQEN_WALK_LIVE=1): live writes against the FAKE GitLab only (allowlist: provider project 1 on the fake
	// connection), so a run opens a merge request that `POST /_fakes/gitlab/merge` can merge and Orqen then proposes
	// knowledge from. It needs ORQEN_WALK_WRITE (the fake write token). Jira writes stay off by default settings.
	if os.Getenv("ORQEN_WALK_LIVE") == "1" && os.Getenv("ORQEN_WALK_WRITE") != "" {
		cur := f.json("GET", "/api/v1/orgs/"+org+"/external-writes", nil, nil, 200)
		f.json("PUT", "/api/v1/orgs/"+org+"/external-writes", PutExternalWritesRequest{Mode: "live", ConfirmPhrase: ptr(LivePhrase), Allowlist: []AllowInput{{ConnectionID: e.glID, ProviderProjectID: "1"}}},
			map[string]string{"If-Match": jsonNum(cur["revision"])}, 200)
	}

	ids["profile"], ids["version"] = id, version
	out, _ := json.Marshal(ids)
	if p := os.Getenv("ORQEN_WALK_OUT"); p != "" {
		if err := os.WriteFile(p, out, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("walk fixtures: %s", out)
}

func jsonNum(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
