package server

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http"
	"net/http/httptest"
	"net/url"
	"orqen/internal/testfakes"
	"strings"
	"sync/atomic"
	"testing"
)

func (f *testApp) catalogPath(org, id string) string {
	return "/api/v1/orgs/" + org + "/connections/" + id + "/catalog-projects"
}

// refreshCatalog starts a refresh and waits for the operation.
func (f *testApp) refreshCatalog(org, id string) map[string]any {
	f.t.Helper()
	testKeyCounter++
	op := f.json("POST", connectionPath(org, id)+"/catalog/refresh", nil, map[string]string{"Idempotency-Key": fmt.Sprintf("refresh-%d", testKeyCounter)}, 202)
	return f.waitOperation(org, op["id"].(string))
}

// walk follows next_cursor and returns every item.
func (f *testApp) walk(path string) []map[string]any {
	f.t.Helper()
	items := []map[string]any{}
	cursor := ""
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for i := 0; i < 50; i++ {
		page := f.json("GET", path+sep+"limit=50"+cursor, nil, nil, 200)
		for _, x := range page["items"].([]any) {
			items = append(items, x.(map[string]any))
		}
		if page["next_cursor"] == nil {
			return items
		}
		cursor = "&cursor=" + url.QueryEscape(page["next_cursor"].(string))
	}
	f.t.Fatal("pagination did not end")
	return nil
}
func TestCatalogRefreshAndPaging(t *testing.T) {
	fake := testfakes.NewGitLab()
	up := httptest.NewServer(fake.Handler())
	defer up.Close()
	f := newTestApp(t, true)
	f.bootstrap()
	org := f.org("alpha")
	id := f.providerConn(org, gitlabSpec(up.URL))
	op := f.refreshCatalog(org, id)
	if op["status"] != "succeeded" {
		t.Fatal(op)
	}
	res := op["result"].(map[string]any)
	if res["seen"] != float64(250) || res["added"] != float64(250) || res["gone"] != float64(0) {
		t.Fatal(res)
	}
	all := f.walk(f.catalogPath(org, id) + "?include_archived=true")
	seen := map[string]bool{}
	for _, p := range all {
		if seen[p["id"].(string)] {
			t.Fatal("duplicate", p["id"])
		}
		seen[p["id"].(string)] = true
	}
	if len(all) != 250 {
		t.Fatal(len(all))
	}
	if shown := f.walk(f.catalogPath(org, id)); len(shown) != 249 {
		t.Fatal("archived projects must be hidden by default", len(shown))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1]["path_with_namespace"].(string) > all[i]["path_with_namespace"].(string) {
			t.Fatal("not sorted by path")
		}
	}
	hit := f.walk(f.catalogPath(org, id) + "?search=" + url.QueryEscape("GROUP-1/proj-12"))
	if len(hit) != 1 || hit[0]["name"] != "proj-12" || hit[0]["web_url"] == "" {
		t.Fatal(hit)
	}
	if none := f.walk(f.catalogPath(org, id) + "?search=" + url.QueryEscape("(.*)")); len(none) != 0 {
		t.Fatal("search must be a literal match", len(none))
	}
	fake.Drop(9)
	op = f.refreshCatalog(org, id)
	res = op["result"].(map[string]any)
	if res["seen"] != float64(249) || res["added"] != float64(0) || res["gone"] != float64(1) {
		t.Fatal(res)
	}
	if visible := f.walk(f.catalogPath(org, id) + "?include_archived=true"); len(visible) != 249 {
		t.Fatal(len(visible))
	}
	withGone := f.walk(f.catalogPath(org, id) + "?include_archived=true&include_gone=true")
	gone := 0
	for _, p := range withGone {
		if p["gone"] == true {
			gone++
			if p["name"] != "proj-9" {
				t.Fatal(p)
			}
		}
	}
	if len(withGone) != 250 || gone != 1 {
		t.Fatal(len(withGone), gone)
	}
	for _, q := range []string{"selection=bad", "include_gone=maybe", "limit=0"} {
		expectCode(t, f.json("GET", f.catalogPath(org, id)+"?"+q, nil, nil, 422), "validation_failed")
	}
	// Discovery never creates projects or any job except the refresh.
	if n, _ := f.db.Coll("projects").CountDocuments(context.Background(), bson.M{}); n != 0 {
		t.Fatal("projects were created", n)
	}
	if n, _ := f.db.Coll("jobs").CountDocuments(context.Background(), bson.M{"kind": bson.M{"$ne": "catalog_refresh"}}); n != 0 {
		t.Fatal("unexpected jobs", n)
	}
}
func TestCatalogSummaryAndSelection(t *testing.T) {
	fake := testfakes.NewGitLab()
	up := httptest.NewServer(fake.Handler())
	defer up.Close()
	f := newTestApp(t, true)
	f.bootstrap()
	org := f.org("alpha")
	id := f.providerConn(org, gitlabSpec(up.URL))
	f.refreshCatalog(org, id)
	resp := f.request("GET", "/api/v1/orgs/"+org+"/catalog/summary", nil, nil)
	defer resp.Body.Close()
	var rows []map[string]any
	if err := decodeBody(resp, &rows); err != nil || len(rows) != 1 || rows[0]["project_count"] != float64(250) || rows[0]["refreshed_at"] == nil || rows[0]["running_operation_id"] != nil {
		t.Fatal(rows, err)
	}
	prj := newID("prj")
	if _, err := f.db.Coll("projects").InsertOne(context.Background(), bson.M{"_id": prj, "org_id": org, "active": true, "primary_repo_key": repoKey(org, id, "5")}); err != nil {
		t.Fatal(err)
	}
	selected := f.walk(f.catalogPath(org, id) + "?selection=selected")
	if len(selected) != 1 || selected[0]["provider_project_id"] != "5" || selected[0]["project_id"] != prj {
		t.Fatal(selected)
	}
	if unselected := f.walk(f.catalogPath(org, id) + "?selection=unselected&include_archived=true"); len(unselected) != 249 {
		t.Fatal(len(unselected))
	}
	all := f.walk(f.catalogPath(org, id))
	marked := 0
	for _, p := range all {
		if p["project_id"] != nil {
			marked++
		}
	}
	if marked != 1 {
		t.Fatal("project_id annotation", marked)
	}
	// An inactive project no longer counts as selected.
	if _, err := f.db.Coll("projects").UpdateOne(context.Background(), bson.M{"_id": prj}, bson.M{"$set": bson.M{"active": false}}); err != nil {
		t.Fatal(err)
	}
	if selected = f.walk(f.catalogPath(org, id) + "?selection=selected"); len(selected) != 0 {
		t.Fatal(selected)
	}
	// Another organization sees none of it.
	other := f.org("beta")
	f.json("GET", f.catalogPath(other, id), nil, nil, 404)
	f.json("POST", connectionPath(other, id)+"/catalog/refresh", nil, map[string]string{"Idempotency-Key": "x"}, 404)
	resp = f.request("GET", "/api/v1/orgs/"+other+"/catalog/summary", nil, nil)
	rows = nil
	if err := decodeBody(resp, &rows); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}
func TestCatalogOffsetMode(t *testing.T) {
	fake := testfakes.NewGitLab()
	fake.Offset = true
	up := httptest.NewServer(fake.Handler())
	defer up.Close()
	f := newTestApp(t, true)
	f.bootstrap()
	org := f.org("alpha")
	id := f.providerConn(org, gitlabSpec(up.URL))
	op := f.refreshCatalog(org, id)
	if op["status"] != "succeeded" || op["result"].(map[string]any)["seen"] != float64(250) {
		t.Fatal(op)
	}
}
func TestCatalogRefreshDeduplicates(t *testing.T) {
	f := newTestApp(t, false)
	f.bootstrap()
	org := f.org("alpha")
	id := f.providerConn(org, gitlabSpec("http://127.0.0.1:1"))
	first := f.json("POST", connectionPath(org, id)+"/catalog/refresh", nil, map[string]string{"Idempotency-Key": "one"}, 202)
	second := f.json("POST", connectionPath(org, id)+"/catalog/refresh", nil, map[string]string{"Idempotency-Key": "two"}, 202)
	if first["id"] != second["id"] || second["deduplicated"] != true {
		t.Fatal(first, second)
	}
	f.json("POST", connectionPath(org, id)+"/catalog/refresh", nil, nil, 422)
	jira := f.providerConn(org, connSpec{Name: "Jira", Provider: "jira", Deployment: "server", Base: "http://127.0.0.1:1", Method: "personal_access_token"})
	expectCode(t, f.json("POST", connectionPath(org, jira)+"/catalog/refresh", nil, map[string]string{"Idempotency-Key": "three"}, 422), "provider_mismatch")
}
func TestCatalogRefreshRetriesWithoutDuplicates(t *testing.T) {
	fake := testfakes.NewGitLab()
	var lists atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gitlab/api/v4/projects" && lists.Add(1) == 3 {
			w.WriteHeader(503)
			return
		}
		fake.Handler().ServeHTTP(w, r)
	}))
	defer up.Close()
	f := newTestApp(t, true)
	f.bootstrap()
	org := f.org("alpha")
	id := f.providerConn(org, gitlabSpec(up.URL))
	op := f.refreshCatalog(org, id)
	if op["status"] != "succeeded" || op["result"].(map[string]any)["added"] != float64(250) {
		t.Fatal(op)
	}
	n, _ := f.db.Coll("catalog_projects").CountDocuments(context.Background(), bson.M{"org_id": org})
	var distinct []string
	if err := f.db.Coll("catalog_projects").Distinct(context.Background(), "provider_project_id", bson.M{"org_id": org}).Decode(&distinct); err != nil {
		t.Fatal(err)
	}
	if n != 250 || len(distinct) != 250 {
		t.Fatal(n, len(distinct))
	}
	job, err := findOne[JobDoc](context.Background(), f.db, "jobs", bson.M{"org_id": org})
	if err != nil || job.Attempts != 2 {
		t.Fatal(job, err)
	}
}
func TestCatalogRefreshRejectsForeignLink(t *testing.T) {
	fake := testfakes.NewGitLab()
	fake.ForeignLink = true
	up := httptest.NewServer(fake.Handler())
	defer up.Close()
	f := newTestApp(t, true)
	f.bootstrap()
	org := f.org("alpha")
	id := f.providerConn(org, gitlabSpec(up.URL))
	op := f.refreshCatalog(org, id)
	if op["status"] != "failed" || op["problem"].(map[string]any)["code"] != "provider_invalid_output" {
		t.Fatal(op)
	}
}
func TestCatalogRefreshWithoutCredentialFails(t *testing.T) {
	f := newTestApp(t, true)
	f.bootstrap()
	org := f.org("alpha")
	spec := gitlabSpec("http://127.0.0.1:1")
	spec.Secret = ""
	id := f.providerConn(org, spec)
	op := f.refreshCatalog(org, id)
	if op["status"] != "failed" || op["problem"].(map[string]any)["code"] != "provider_credential_expired" {
		t.Fatal(op)
	}
}
func TestLookups(t *testing.T) {
	gl := testfakes.NewGitLab()
	glUp := httptest.NewServer(gl.Handler())
	defer glUp.Close()
	jf := testfakes.NewJira()
	jf.Cloud = true
	jUp := httptest.NewServer(jf.Handler())
	defer jUp.Close()
	f := newTestApp(t, false)
	f.bootstrap()
	org := f.org("alpha")
	g := f.providerConn(org, gitlabSpec(glUp.URL))
	j := f.providerConn(org, connSpec{Name: "Jira", Provider: "jira", Deployment: "cloud", Base: jUp.URL + "/jira", Method: "api_token", Username: jf.User, Secret: jf.Token})
	projects := f.json("GET", connectionPath(org, j)+"/lookup/jira-projects?limit=2", nil, nil, 200)
	if len(projects["items"].([]any)) != 2 || projects["next_start_at"] != float64(2) {
		t.Fatal(projects)
	}
	projects = f.json("GET", connectionPath(org, j)+"/lookup/jira-projects?start_at=2&limit=2", nil, nil, 200)
	if len(projects["items"].([]any)) != 1 || projects["next_start_at"] != nil {
		t.Fatal(projects)
	}
	projects = f.json("GET", connectionPath(org, j)+"/lookup/jira-projects?query=web", nil, nil, 200)
	if len(projects["items"].([]any)) != 1 || projects["items"].([]any)[0].(map[string]any)["key"] != "WEB" {
		t.Fatal(projects)
	}
	boards := f.json("GET", connectionPath(org, j)+"/lookup/jira-boards?project_id=10000", nil, nil, 200)
	if len(boards["items"].([]any)) != 2 {
		t.Fatal(boards)
	}
	branches := f.json("GET", connectionPath(org, g)+"/lookup/gitlab-branches?provider_project_id=7&search=dev", nil, nil, 200)
	items := branches["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["name"] != "develop" {
		t.Fatal(branches)
	}
	jf.AgileMissing = true
	expectCode(t, f.json("GET", connectionPath(org, j)+"/lookup/jira-boards?project_id=10000", nil, nil, 422), "capability_unsupported")
	expectCode(t, f.json("GET", connectionPath(org, g)+"/lookup/jira-projects", nil, nil, 422), "provider_mismatch")
	expectCode(t, f.json("GET", connectionPath(org, g)+"/lookup/gitlab-branches", nil, nil, 422), "validation_failed")
	gl.Token = "rotated"
	expectCode(t, f.json("GET", connectionPath(org, g)+"/lookup/gitlab-branches?provider_project_id=7", nil, nil, 502), "provider_credential_expired")
	noCred := gitlabSpec(glUp.URL)
	noCred.Name, noCred.Secret = "Bare", ""
	bare := f.providerConn(org, noCred)
	expectCode(t, f.json("GET", connectionPath(org, bare)+"/lookup/gitlab-branches?provider_project_id=7", nil, nil, 422), "credential_required")
	other := f.org("beta")
	f.json("GET", connectionPath(other, g)+"/lookup/gitlab-branches?provider_project_id=7", nil, nil, 404)
}
