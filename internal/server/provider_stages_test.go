package server

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http"
	"net/http/httptest"
	"orqen/internal/testfakes"
	"strings"
	"sync"
	"testing"
	"time"
)

func gitlabSpec(base string) connSpec {
	return connSpec{Name: "GitLab", Provider: "gitlab", Deployment: "self_managed", Base: base + "/gitlab", Method: "personal_access_token", Secret: "glpat-test"}
}
func reasons(conn map[string]any) []string {
	out := []string{}
	for _, r := range conn["readiness"].(map[string]any)["reasons"].([]any) {
		out = append(out, r.(string))
	}
	return out
}
func TestConnectionTestGitLabVerified(t *testing.T) {
	fake := testfakes.NewGitLab()
	up := httptest.NewServer(fake.Handler())
	defer up.Close()
	f := newTestApp(t, true)
	f.bootstrap()
	org := f.org("alpha")
	id := f.providerConn(org, gitlabSpec(up.URL))
	op := f.runTest(org, id)
	if op["status"] != "succeeded" || op["result"].(map[string]any)["overall"] != "verified" {
		t.Fatal(op)
	}
	stages := f.stages(org, op)
	for _, name := range []string{"network", "http", "credential", "auth", "identity", "capabilities", "sample_access"} {
		requireStage(t, stages, name, "passed", "")
	}
	requireStage(t, stages, "tls", "skipped", "")
	conn := f.json("GET", connectionPath(org, id), nil, nil, 200)
	if conn["status"] != "verified" || conn["readiness"].(map[string]any)["unattended_ready"] != true {
		t.Fatal(conn)
	}
	if conn["identity"].(map[string]any)["username"] != "orqen-bot" || conn["capabilities"].(map[string]any)["server_version"] != "17.0.0" {
		t.Fatal(conn)
	}
	if conn["revision"] != float64(2) {
		t.Fatal("system snapshots must not bump the revision", conn["revision"])
	}
	for _, r := range fake.Requests() {
		if r != "/gitlab" && !strings.HasPrefix(r, "/gitlab/") {
			t.Fatal("request outside the configured prefix", r)
		}
	}
	art := op["result"].(map[string]any)["artifact_id"].(string)
	var all []byte
	for _, p := range []string{connectionPath(org, id), "/api/v1/orgs/" + org + "/artifacts/" + art + "/content", "/api/v1/orgs/" + org + "/events"} {
		b, _ := json.Marshal(f.json("GET", p, nil, nil, 200))
		all = append(all, b...)
	}
	if strings.Contains(string(all), "glpat-test") {
		t.Fatal("secret leaked")
	}
	f.json("PATCH", connectionPath(org, id), map[string]any{"auth_username": "someone"}, map[string]string{"If-Match": "2"}, 200)
	conn = f.json("GET", connectionPath(org, id), nil, nil, 200)
	if !contains(reasons(conn), "test_stale") || conn["readiness"].(map[string]any)["unattended_ready"] != false {
		t.Fatal(conn)
	}
}
func TestConnectionTestGitLabFailures(t *testing.T) {
	fake := testfakes.NewGitLab()
	up := httptest.NewServer(fake.Handler())
	defer up.Close()
	f := newTestApp(t, true)
	f.bootstrap()
	org := f.org("alpha")
	bad := gitlabSpec(up.URL)
	bad.Name, bad.Secret = "Bad", "wrong"
	id := f.providerConn(org, bad)
	op := f.runTest(org, id)
	stages := f.stages(org, op)
	requireStage(t, stages, "auth", "failed", "credential_rejected")
	requireStage(t, stages, "identity", "skipped", "previous_stage_failed")
	conn := f.json("GET", connectionPath(org, id), nil, nil, 200)
	if conn["status"] != "degraded" || !contains(reasons(conn), "auth_failed") {
		t.Fatal(conn)
	}
	fake.TreeForbidden = true
	id = f.providerConn(org, gitlabSpec(up.URL))
	stages = f.stages(org, f.runTest(org, id))
	requireStage(t, stages, "capabilities", "passed", "")
	requireStage(t, stages, "sample_access", "failed", "repo_read_denied")
	conn = f.json("GET", connectionPath(org, id), nil, nil, 200)
	if conn["status"] != "degraded" || conn["capabilities"].(map[string]any)["features"].(map[string]any)["read_repository_files"] != "denied" || !contains(reasons(conn), "sample_access_failed") {
		t.Fatal(conn)
	}
}
func TestConnectionTestGitLabRedirect(t *testing.T) {
	var outside int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { outside++ }))
	defer target.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/gitlab/api/") {
			http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
			return
		}
		w.WriteHeader(401)
	}))
	defer up.Close()
	f := newTestApp(t, true)
	f.bootstrap()
	org := f.org("alpha")
	id := f.providerConn(org, gitlabSpec(up.URL))
	stages := f.stages(org, f.runTest(org, id))
	requireStage(t, stages, "auth", "failed", "unexpected_redirect")
	if outside != 0 {
		t.Fatal("the redirect was followed")
	}
}
func TestConnectionTestJira(t *testing.T) {
	fake := testfakes.NewJira()
	fake.Cloud = true
	up := httptest.NewServer(fake.Handler())
	defer up.Close()
	f := newTestApp(t, true)
	f.bootstrap()
	org := f.org("alpha")
	noUser := f.providerConn(org, connSpec{Name: "Cloud", Provider: "jira", Deployment: "cloud", Base: up.URL + "/jira", Method: "api_token", Secret: fake.Token})
	stages := f.stages(org, f.runTest(org, noUser))
	requireStage(t, stages, "auth", "failed", "username_required")
	withUser := f.providerConn(org, connSpec{Name: "Cloud2", Provider: "jira", Deployment: "cloud", Base: up.URL + "/jira", Method: "api_token", Username: fake.User, Secret: fake.Token})
	op := f.runTest(org, withUser)
	if op["result"].(map[string]any)["overall"] != "verified" {
		t.Fatal(f.stages(org, op))
	}
	fake.Cloud = false
	fake.AgileMissing = true
	dc := f.providerConn(org, connSpec{Name: "DC", Provider: "jira", Deployment: "data_center", Base: up.URL + "/jira", Method: "personal_access_token", Secret: fake.Token})
	op = f.runTest(org, dc)
	if op["result"].(map[string]any)["overall"] != "verified" {
		t.Fatal(f.stages(org, op))
	}
	conn := f.json("GET", connectionPath(org, dc), nil, nil, 200)
	if conn["capabilities"].(map[string]any)["features"].(map[string]any)["boards"] != "unsupported" || conn["readiness"].(map[string]any)["unattended_ready"] != true {
		t.Fatal(conn)
	}
}
func TestConnectionTestConfluenceStaysNotImplemented(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer up.Close()
	f := newTestApp(t, true)
	f.bootstrap()
	org := f.org("alpha")
	id := f.providerConn(org, connSpec{Name: "Wiki", Provider: "confluence", Deployment: "cloud", Base: up.URL + "/wiki", Method: "api_token", Secret: "x"})
	op := f.runTest(org, id)
	if op["result"].(map[string]any)["overall"] != "reachable" {
		t.Fatal(op)
	}
	stages := f.stages(org, op)
	requireStage(t, stages, "auth", "not_implemented", "provider_adapter_unavailable")
	conn := f.json("GET", connectionPath(org, id), nil, nil, 200)
	if !contains(reasons(conn), "provider_adapter_unavailable") {
		t.Fatal(conn)
	}
}
func TestProviderConcurrencyGate(t *testing.T) {
	fake := testfakes.NewGitLab()
	fake.Delay = 40 * time.Millisecond
	up := httptest.NewServer(fake.Handler())
	defer up.Close()
	f := newTestApp(t, false)
	f.bootstrap()
	org := f.org("alpha")
	spec := gitlabSpec(up.URL)
	id := f.providerConn(org, spec)
	f.json("PATCH", connectionPath(org, id), map[string]any{"max_concurrency": 1}, map[string]string{"If-Match": "2"}, 200)
	conn, err := f.a.connection(context.Background(), org, id)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			host, err := f.a.codeHost(context.Background(), conn)
			if err == nil {
				_, err = host.GetProject(context.Background(), "1")
			}
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if fake.MaxInflight() != 1 {
		t.Fatal("max in flight", fake.MaxInflight())
	}
}
func TestUnknownJobKindFails(t *testing.T) {
	f := newTestApp(t, true)
	f.bootstrap()
	org := f.org("alpha")
	f.a.Handle("test_kind", func(context.Context, JobDoc, Lease) error {
		return &JobFailure{Code: "boom", Message: "boom"}
	})
	opID := newID("op")
	now := time.Now().UTC()
	job := testJob(org, "x", now)
	job.Kind, job.OperationID = "test_kind", &opID
	if _, err := f.db.Coll("operations").InsertOne(context.Background(), OperationDoc{ID: opID, OrgID: org, Kind: "test_kind", Subject: Subject{"x", "y"}, Status: "pending", JobID: &job.ID, CreatedAt: now, UpdatedAt: now, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Txn(context.Background(), func(sc context.Context) error { return f.a.enqueue(sc, job) }); err != nil {
		t.Fatal(err)
	}
	f.a.Committed()
	op := f.waitOperation(org, opID)
	if op["status"] != "failed" || op["problem"].(map[string]any)["code"] != "boom" {
		t.Fatal(op)
	}
}
func TestJobWithoutHandlerFailsPermanently(t *testing.T) {
	f := newTestApp(t, false)
	org := newID("org")
	opID := newID("op")
	now := time.Now().UTC()
	job := testJob(org, "ghost", now)
	job.Kind, job.OperationID = "ghost", &opID
	if _, err := f.db.Coll("operations").InsertOne(context.Background(), OperationDoc{ID: opID, OrgID: org, Kind: "ghost", Subject: Subject{"x", "y"}, Status: "pending", JobID: &job.ID, CreatedAt: now, UpdatedAt: now, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Txn(context.Background(), func(sc context.Context) error { return f.a.enqueue(sc, job) }); err != nil {
		t.Fatal(err)
	}
	got, lease, err := f.a.acquire(context.Background(), "w", []string{"ghost"}, now)
	if err != nil {
		t.Fatal(err)
	}
	f.a.runJob(got, lease)
	op, err := findOne[OperationDoc](context.Background(), f.db, "operations", bson.M{"_id": opID})
	if err != nil || op.Status != "failed" || (*op.Problem)["code"] != "unknown_kind" {
		t.Fatal(op, err)
	}
	if kinds := f.a.handlerKinds(); !contains(kinds, "connection_test") || contains(kinds, "ghost") {
		t.Fatal(kinds)
	}
}
