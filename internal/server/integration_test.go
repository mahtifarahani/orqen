package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"orqen/api"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type testApp struct {
	t         *testing.T
	a         *App
	db        *DB
	client    *http.Client
	url, csrf string
	stop      context.CancelFunc
	done      chan error
	creds     *MemoryStore
	setup     func(*App)
}

func newTestApp(t *testing.T, runner bool) *testApp { return newTestAppWith(t, runner, nil) }

// newTestAppWith runs setup on the App before it starts serving (for example to install a fake catherd).
func newTestAppWith(t *testing.T, runner bool, setup func(*App)) *testApp {
	t.Helper()
	if testing.Short() {
		t.Skip("MongoDB integration test")
	}
	c := DefaultConfig()
	c.Port = 0
	c.DataDir = t.TempDir()
	c.DBName = "orqen_go_test_" + strings.ToLower(newID("db"))
	c.CredentialStore = "memory"
	c.LeaseTTL = 1000 * time.Millisecond
	c.SchedTick, c.SchedSweep = 100*time.Millisecond, 500*time.Millisecond
	if uri := os.Getenv("ORQEN_TEST_MONGO_URI"); uri != "" {
		c.MongoURI = uri
	}
	// Creating the indexes of a fresh database takes long when many test packages hit the one dev replica set at once.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := OpenDB(ctx, c)
	if err != nil {
		t.Fatalf("start the dev replica set with scripts/dev-mongo.sh start: %v", err)
	}
	jar, _ := cookiejar.New(nil)
	f := &testApp{t: t, db: db, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}, creds: NewMemoryStore(), setup: setup}
	f.start(c, runner)
	t.Cleanup(func() {
		f.stop()
		select {
		case err := <-f.done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(12 * time.Second):
			t.Error("server did not stop")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = db.Database.Drop(ctx)
		_ = db.Client.Disconnect(ctx)
	})
	return f
}
func (f *testApp) start(c Config, runner bool) {
	f.t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(c.Port)))
	if err != nil {
		f.t.Fatal(err)
	}
	c.Port = l.Addr().(*net.TCPAddr).Port
	f.url = fmt.Sprintf("http://127.0.0.1:%d", c.Port)
	f.a = NewApp(c, f.db, f.creds)
	if f.setup != nil {
		f.setup(f.a)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.stop = cancel
	f.done = make(chan error, 1)
	go func() { f.done <- f.a.Run(ctx, l, runner) }()
	for i := 0; i < 100; i++ {
		resp, err := f.client.Get(f.url + "/api/v1/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatal("server did not start")
}
func (f *testApp) cookieValue() string {
	req, _ := http.NewRequest("GET", f.url, nil)
	for _, c := range f.client.Jar.Cookies(req.URL) {
		if c.Name == f.a.Config.CookieName() {
			return c.Value
		}
	}
	return ""
}
func (f *testApp) request(method, path string, body any, headers map[string]string) *http.Response {
	f.t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, f.url+path, reader)
	if err != nil {
		f.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", f.url)
	if f.csrf != "" {
		req.Header.Set("X-CSRF-Token", f.csrf)
	}
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp
}
func readResponse(t *testing.T, resp *http.Response, status int) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("status %d, wanted %d: %s", resp.StatusCode, status, b)
	}
	var m map[string]any
	if len(b) > 0 {
		if err = json.Unmarshal(b, &m); err != nil {
			t.Fatalf("JSON: %v, %s", err, b)
		}
	}
	return m
}
func (f *testApp) json(method, path string, body any, headers map[string]string, status int) map[string]any {
	f.t.Helper()
	resp := f.request(method, path, body, headers)
	if resp.StatusCode != status {
		f.t.Logf("%s %s answered %d", method, path, resp.StatusCode)
	}
	return readResponse(f.t, resp, status)
}
func (f *testApp) bootstrap() {
	f.t.Helper()
	raw, err := f.db.IssueToken(context.Background(), "bootstrap")
	if err != nil {
		f.t.Fatal(err)
	}
	m := f.json("POST", "/api/v1/auth/bootstrap", bson.M{"token": raw}, nil, 200)
	f.csrf = m["csrf_token"].(string)
}
func (f *testApp) org(slug string) string {
	return f.json("POST", "/api/v1/orgs", CreateOrgRequest{slug, slug, "en", "UTC"}, nil, 201)["id"].(string)
}
func (f *testApp) connection(org, name, base string) string {
	return f.json("POST", "/api/v1/orgs/"+org+"/connections", CreateConnectionRequest{Name: name, Provider: "jira", Deployment: "server", BaseURL: base, AuthMethod: "personal_access_token"}, nil, 201)["id"].(string)
}
func (f *testApp) startTest(org, id, key string) map[string]any {
	return f.json("POST", connectionPath(org, id)+"/test", nil, map[string]string{"Idempotency-Key": key}, 202)
}
func (f *testApp) waitOperation(org, id string) map[string]any {
	f.t.Helper()
	for i := 0; i < 300; i++ {
		op := f.json("GET", "/api/v1/orgs/"+org+"/operations/"+id, nil, nil, 200)
		if op["status"] == "succeeded" || op["status"] == "failed" {
			return op
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatal("operation did not finish")
	return nil
}
func expectCode(t *testing.T, m map[string]any, code string) {
	t.Helper()
	if m["code"] != code {
		t.Fatal(m)
	}
}

func TestAuthIntegration(t *testing.T) {
	f := newTestApp(t, false)
	status := f.json("GET", "/api/v1/auth/status", nil, nil, 200)
	if status["bootstrapped"] != false || status["authenticated"] != false {
		t.Fatal(status)
	}
	expectCode(t, f.json("GET", "/api/v1/orgs", nil, nil, 401), "unauthenticated")
	expectCode(t, f.json("POST", "/api/v1/auth/bootstrap", bson.M{"token": "bad"}, nil, 401), "invalid_token")
	f.bootstrap()
	status = f.json("GET", "/api/v1/auth/status", nil, nil, 200)
	if status["bootstrapped"] != true || status["authenticated"] != true || status["csrf_token"] != f.csrf {
		t.Fatal(status)
	}
	expectCode(t, f.json("POST", "/api/v1/auth/bootstrap", bson.M{"token": "bad"}, nil, 409), "already_bootstrapped")
	expectCode(t, f.json("POST", "/api/v1/orgs", CreateOrgRequest{"abc", "A", "en", "UTC"}, map[string]string{"X-CSRF-Token": "wrong"}, 403), "csrf_failed")
	expectCode(t, f.json("POST", "/api/v1/orgs", CreateOrgRequest{"abc", "A", "en", "UTC"}, map[string]string{"Origin": "http://evil.invalid"}, 403), "origin_rejected")
	expectCode(t, f.json("GET", "/api/v1/health", nil, map[string]string{"Host": "evil.example:4280"}, 421), "host_rejected")
	raw, err := f.db.IssueToken(context.Background(), "login")
	if err != nil {
		t.Fatal(err)
	}
	f.json("POST", "/api/v1/auth/logout", nil, nil, 204)
	if f.cookieValue() != "" {
		t.Fatal("cookie not cleared")
	}
	expectCode(t, f.json("GET", "/api/v1/orgs", nil, nil, 401), "unauthenticated")
	m := f.json("POST", "/api/v1/auth/redeem", bson.M{"token": raw}, nil, 200)
	f.csrf = m["csrf_token"].(string)
	expectCode(t, f.json("POST", "/api/v1/auth/redeem", bson.M{"token": raw}, nil, 401), "invalid_token")
}
func TestDocsHealthAndUIIntegration(t *testing.T) {
	f := newTestApp(t, false)
	health := f.json("GET", "/api/v1/health", nil, nil, 200)
	if health["status"] != "ok" || health["schema_version"] != float64(SchemaVersion) {
		t.Fatal(health)
	}
	for _, p := range []string{"/api/docs", "/api/docs/", "/api/docs/swagger-initializer.js", "/api/docs/swagger-ui-bundle.js", "/api/docs/swagger-ui.css"} {
		resp := f.request("GET", p, nil, nil)
		if resp.StatusCode != 200 {
			t.Fatal(p, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp := f.request("GET", "/api/v1/openapi.json", nil, nil)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(b, api.OpenAPI) {
		t.Fatal("served contract drift")
	}
	expectCode(t, f.json("GET", "/api/unknown", nil, nil, 404), "not_found")
	resp = f.request("GET", "/orgs/anything/connections", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("UI CSP missing")
	}
}
func TestOrgRevisionPaginationIntegration(t *testing.T) {
	f := newTestApp(t, false)
	f.bootstrap()
	body := CreateOrgRequest{"alpha", "نام", "fa", "Asia/Tehran"}
	resp := f.request("POST", "/api/v1/orgs", body, nil)
	if resp.Header.Get("ETag") != "\"1\"" || resp.Header.Get("Location") == "" {
		t.Fatal(resp.Header)
	}
	org := readResponse(t, resp, 201)
	id := org["id"].(string)
	path := "/api/v1/orgs/" + id
	expectCode(t, f.json("PATCH", path, bson.M{"name": "New"}, nil, 428), "precondition_required")
	updated := f.json("PATCH", path, bson.M{"name": " New "}, map[string]string{"If-Match": "1"}, 200)
	if updated["name"] != "New" || updated["revision"] != float64(2) {
		t.Fatal(updated)
	}
	conflict := f.json("PATCH", path, bson.M{"name": "stale"}, map[string]string{"If-Match": "1"}, 409)
	expectCode(t, conflict, "revision_conflict")
	if conflict["current"].(map[string]any)["revision"] != float64(2) {
		t.Fatal(conflict)
	}
	expectCode(t, f.json("PATCH", path, bson.M{"slug": "new"}, map[string]string{"If-Match": "2"}, 422), "validation_failed")
	for i := 0; i < 7; i++ {
		f.org(fmt.Sprintf("org-%d", i))
	}
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 10; i++ {
		page := f.json("GET", "/api/v1/orgs?limit=3"+cursor, nil, nil, 200)
		for _, v := range page["items"].([]any) {
			id := v.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatal("duplicate page")
			}
			seen[id] = true
		}
		if page["next_cursor"] == nil {
			break
		}
		cursor = "&cursor=" + page["next_cursor"].(string)
	}
	if len(seen) != 8 {
		t.Fatal(seen)
	}
	for _, path := range []string{"/api/v1/orgs/nope", "/api/v1/orgs/" + newID("org")} {
		f.json("GET", path, nil, nil, 404)
	}
	for _, q := range []string{"limit=0", "limit=201", "limit=nope", "cursor=!!"} {
		expectCode(t, f.json("GET", "/api/v1/orgs?"+q, nil, nil, 422), "validation_failed")
	}
}
func TestIdempotencyIntegration(t *testing.T) {
	f := newTestApp(t, false)
	f.bootstrap()
	body := CreateOrgRequest{"alpha", "Alpha", "en", "UTC"}
	header := map[string]string{"Idempotency-Key": "same"}
	first := f.json("POST", "/api/v1/orgs", body, header, 201)
	resp := f.request("POST", "/api/v1/orgs", body, header)
	if resp.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal(resp.Header)
	}
	second := readResponse(t, resp, 201)
	if !reflect.DeepEqual(first, second) {
		t.Fatal(first, second)
	}
	body.Name = "Different"
	expectCode(t, f.json("POST", "/api/v1/orgs", body, header, 422), "idempotency_key_reused")
	bad := CreateOrgRequest{"INVALID", "Bad", "en", "UTC"}
	header = map[string]string{"Idempotency-Key": "failure"}
	f.json("POST", "/api/v1/orgs", bad, header, 422)
	resp = f.request("POST", "/api/v1/orgs", bad, header)
	if resp.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("failed response not replayed")
	}
	readResponse(t, resp, 422)
	body = CreateOrgRequest{"concurrent", "Concurrent", "en", "UTC"}
	var wg sync.WaitGroup
	statuses := make(chan int, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.request("POST", "/api/v1/orgs", body, map[string]string{"Idempotency-Key": "concurrent"})
			statuses <- r.StatusCode
			r.Body.Close()
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != 201 && status != 409 {
			t.Fatal(status)
		}
	}
	n, err := f.db.Coll("organizations").CountDocuments(context.Background(), bson.M{"slug": "concurrent"})
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	body = CreateOrgRequest{"stale", "Stale", "en", "UTC"}
	route := "POST /api/v1/orgs"
	rec, _, err := f.a.claim(context.Background(), "global", route, "stale", requestHash("POST", route, nil, body))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.db.Coll("idempotency_records").UpdateOne(context.Background(), bson.M{"_id": rec.ID}, bson.M{"$set": bson.M{"locked_until": time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	f.json("POST", "/api/v1/orgs", body, map[string]string{"Idempotency-Key": "stale"}, 201)
}
func TestCredentialsAndScopeIntegration(t *testing.T) {
	f := newTestApp(t, false)
	f.bootstrap()
	org := f.org("first")
	other := f.org("second")
	id := f.connection(org, "Jira", "http://127.0.0.1:1/jira")
	otherID := f.connection(other, "Jira", "http://127.0.0.1:1/jira")
	path := connectionPath(org, id)
	foreign := connectionPath(other, id)
	for _, p := range []string{foreign, connectionPath(org, otherID)} {
		f.json("GET", p, nil, nil, 404)
	}
	f.json("PUT", foreign+"/credential", bson.M{"secret": "x"}, map[string]string{"If-Match": "1"}, 404)
	put := f.json("PUT", path+"/credential", bson.M{"secret": "top-secret"}, map[string]string{"If-Match": "1"}, 200)
	b, _ := json.Marshal(put)
	if bytes.Contains(b, []byte("top-secret")) || bytes.Contains(b, []byte("credential_ref")) {
		t.Fatal(string(b))
	}
	doc, err := f.a.connection(context.Background(), org, id)
	if err != nil || doc.CredentialRef == nil {
		t.Fatal(doc, err)
	}
	old := *doc.CredentialRef
	f.json("PUT", path+"/credential", bson.M{"secret": "new-secret"}, map[string]string{"If-Match": "1"}, 409)
	f.creds.mu.RLock()
	if len(f.creds.values) != 1 {
		t.Fatal("orphan secret after conflict")
	}
	f.creds.mu.RUnlock()
	f.json("PUT", path+"/credential", bson.M{"secret": "replaced"}, map[string]string{"If-Match": "2"}, 200)
	if _, ok, _ := f.creds.Get(context.Background(), old); ok {
		t.Fatal("old credential retained")
	}
	f.json("DELETE", path+"/credential", nil, map[string]string{"If-Match": "3"}, 200)
	f.json("PATCH", path, bson.M{"base_url": "https://new.example/jira"}, map[string]string{"If-Match": "4"}, 200)
	f.json("DELETE", path, nil, map[string]string{"If-Match": "5"}, 204)
	f.json("GET", path, nil, nil, 404)
	list := f.json("GET", "/api/v1/orgs/"+org+"/connections", nil, nil, 200)
	if len(list["items"].([]any)) != 0 {
		t.Fatal(list)
	}
	f.connection(org, "Jira", "http://127.0.0.1:1")
}
func TestConnectionOperationAndArtifactIntegration(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jira" {
			t.Errorf("exact context path lost: %s", r.URL.Path)
		}
		w.WriteHeader(401)
	}))
	defer up.Close()
	f := newTestApp(t, true)
	f.bootstrap()
	org := f.org("alpha")
	id := f.connection(org, "Jira", up.URL+"/jira")
	op := f.startTest(org, id, "test")
	done := f.waitOperation(org, op["id"].(string))
	if done["status"] != "succeeded" {
		t.Fatal(done)
	}
	result := done["result"].(map[string]any)
	if result["overall"] != "reachable" {
		t.Fatal(result)
	}
	artifact := result["artifact_id"].(string)
	path := "/api/v1/orgs/" + org + "/artifacts/" + artifact
	meta := f.json("GET", path, nil, nil, 200)
	report := f.json("GET", path+"/content", nil, nil, 200)
	if meta["sha256"] == nil || report["base_url"] != up.URL+"/jira" {
		t.Fatal(meta, report)
	}
	stages := report["stages"].([]any)
	if len(stages) != 8 {
		t.Fatal(stages)
	}
	for _, x := range stages {
		stage := x.(map[string]any)
		if contains([]string{"auth", "identity", "capabilities", "sample_access"}, stage["stage"].(string)) && (stage["status"] != "skipped" || stage["code"] != "no_credential") {
			t.Fatal(stage)
		}
	}
	conn := f.json("GET", connectionPath(org, id), nil, nil, 200)
	if conn["revision"] != float64(1) || conn["status"] != "reachable" {
		t.Fatal(conn)
	}
	resp := f.request("POST", connectionPath(org, id)+"/test", nil, map[string]string{"Idempotency-Key": "test"})
	if resp.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal(resp.Header)
	}
	readResponse(t, resp, 202)
	other := f.org("other")
	f.json("GET", "/api/v1/orgs/"+other+"/operations/"+op["id"].(string), nil, nil, 404)
	f.json("GET", "/api/v1/orgs/"+other+"/artifacts/"+artifact, nil, nil, 404)
	f.json("GET", "/api/v1/orgs/"+other+"/artifacts/"+artifact+"/content", nil, nil, 404)
	events := f.json("GET", "/api/v1/orgs/"+org+"/events", nil, nil, 200)
	items := events["items"].([]any)
	seqs := []int{}
	staged := []string{}
	for _, x := range items {
		ev := x.(map[string]any)
		seqs = append(seqs, int(ev["seq"].(float64)))
		if ev["type"] == "connection_test.stage" {
			staged = append(staged, ev["payload"].(map[string]any)["stage"].(string))
		}
	}
	if !sort.IntsAreSorted(seqs) || !reflect.DeepEqual(staged, []string{"network", "tls", "http", "credential", "auth", "identity", "capabilities", "sample_access"}) {
		t.Fatal(seqs, staged)
	}
}
func TestDuplicateStartIntegration(t *testing.T) {
	f := newTestApp(t, false)
	f.bootstrap()
	org := f.org("alpha")
	id := f.connection(org, "Jira", "http://127.0.0.1:1")
	first := f.startTest(org, id, "one")
	second := f.startTest(org, id, "two")
	if first["id"] != second["id"] || second["deduplicated"] != true {
		t.Fatal(first, second)
	}
	n, _ := f.db.Coll("jobs").CountDocuments(context.Background(), bson.M{"org_id": org})
	if n != 1 {
		t.Fatal(n)
	}
	f.json("POST", connectionPath(org, id)+"/test", nil, nil, 422)
}
func TestEventTransactionsIntegration(t *testing.T) {
	f := newTestApp(t, false)
	org := newID("org")
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- f.db.Txn(context.Background(), func(ctx context.Context) error {
				return f.a.AppendEvent(ctx, org, "test", Subject{"test", fmt.Sprint(i)}, bson.M{"token": "secret", "credential_ref": "keep"}, nil, nil, false)
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	events, err := f.a.fetchEvents(context.Background(), org, 0, 100)
	if err != nil || len(events) != 20 {
		t.Fatal(len(events), err)
	}
	for i, e := range events {
		if e.Seq != int64(i+1) || e.Payload["token"] != "[REDACTED]" || e.Payload["credential_ref"] != "keep" {
			t.Fatal(e)
		}
	}
	sentinel := errors.New("abort")
	err = f.db.Txn(context.Background(), func(ctx context.Context) error {
		if e := f.a.AppendEvent(ctx, org, "abort", Subject{}, bson.M{}, nil, nil, false); e != nil {
			return e
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	err = f.db.Txn(context.Background(), func(ctx context.Context) error {
		return f.a.AppendEvent(ctx, org, "last", Subject{}, bson.M{}, nil, nil, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err = f.a.fetchEvents(context.Background(), org, 20, 10)
	if err != nil || len(events) != 1 || events[0].Seq != 21 {
		t.Fatal(events, err)
	}
}
func TestSSEIntegration(t *testing.T) {
	f := newTestApp(t, false)
	f.bootstrap()
	org := f.org("alpha")
	id := f.connection(org, "Jira", "http://127.0.0.1:1")
	f.startTest(org, id, "one")
	events, err := f.a.fetchEvents(context.Background(), org, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	after := events[0].Seq
	resp := f.request("GET", fmt.Sprintf("/api/v1/orgs/%s/events/stream?after=0", org), nil, map[string]string{"Last-Event-ID": fmt.Sprint(after)})
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(resp.Header)
	}
	scan := bufio.NewScanner(resp.Body)
	seqs := []int64{}
	for scan.Scan() {
		line := scan.Text()
		if strings.HasPrefix(line, "data: ") {
			var event EventDoc
			var m map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &m); err != nil {
				t.Fatal(err)
			}
			event.Seq = int64(m["seq"].(float64))
			if event.Seq <= after {
				t.Fatal("replayed old event")
			}
			seqs = append(seqs, event.Seq)
			if len(seqs) == len(events)-1 {
				break
			}
		}
	}
	if len(seqs) != len(events)-1 {
		t.Fatal(seqs, scan.Err())
	}
}
func TestSchemaAndRustDataCompatibilityIntegration(t *testing.T) {
	f := newTestApp(t, false)
	ctx := context.Background()
	_, err := f.db.Coll("schema_meta").UpdateOne(ctx, bson.M{"_id": "schema"}, bson.M{"$set": bson.M{"version": 999}})
	if err != nil {
		t.Fatal(err)
	}
	var newer *SchemaTooNew
	if err = f.db.Migrate(ctx); !errors.As(err, &newer) {
		t.Fatal(err)
	}
	_, _ = f.db.Coll("schema_meta").UpdateOne(ctx, bson.M{"_id": "schema"}, bson.M{"$set": bson.M{"version": 1}})
	_, err = f.db.Coll("counters").UpdateOne(ctx, bson.M{"_id": "events"}, bson.M{"$set": bson.M{"seq": int64(37)}})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	org := newID("org")
	err = f.db.Txn(ctx, func(sc context.Context) error {
		return f.a.AppendEvent(sc, org, "test", Subject{}, bson.M{}, nil, nil, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := f.a.fetchEvents(ctx, org, 0, 10)
	if err != nil || len(events) != 1 || events[0].Seq != 38 {
		t.Fatal("Rust counter was reset", events, err)
	}
	raw := "rust-issued-session"
	_, err = f.db.Coll("sessions").InsertOne(ctx, bson.M{"_id": newID("sess"), "token_hash": sha256Hex(raw), "csrf_token": "rust-csrf", "created_at": bson.NewDateTimeFromTime(time.Now()), "last_seen_at": bson.NewDateTimeFromTime(time.Now()), "expires_at": bson.NewDateTimeFromTime(time.Now().Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.a.loadSession(ctx, raw)
	if err != nil || session == nil || session.CsrfToken != "rust-csrf" {
		t.Fatal(session, err)
	}
}
func TestRestartIntegration(t *testing.T) {
	f := newTestApp(t, true)
	f.bootstrap()
	org := f.org("alpha")
	id := f.connection(org, "Jira", "http://127.0.0.1:1")
	f.json("PUT", connectionPath(org, id)+"/credential", bson.M{"secret": "retained"}, map[string]string{"If-Match": "1"}, 200)
	op := f.startTest(org, id, "restart")
	finished := f.waitOperation(org, op["id"].(string))
	art := finished["result"].(map[string]any)["artifact_id"].(string)
	before := f.json("GET", "/api/v1/orgs/"+org+"/artifacts/"+art+"/content", nil, nil, 200)
	raw := f.cookieValue()
	c := f.a.Config
	f.stop()
	if err := <-f.done; err != nil {
		t.Fatal(err)
	}
	f.start(c, true)
	req, _ := http.NewRequest("GET", f.url, nil)
	f.client.Jar.SetCookies(req.URL, []*http.Cookie{{Name: f.a.Config.CookieName(), Value: raw, Path: "/"}})
	status := f.json("GET", "/api/v1/auth/status", nil, nil, 200)
	if status["authenticated"] != true {
		t.Fatal(status)
	}
	after := f.json("GET", "/api/v1/orgs/"+org+"/artifacts/"+art+"/content", nil, nil, 200)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("artifact changed")
	}
	conn, err := f.a.connection(context.Background(), org, id)
	if err != nil || conn.CredentialRef == nil {
		t.Fatal(conn, err)
	}
	v, ok, err := f.creds.Get(context.Background(), *conn.CredentialRef)
	if err != nil || !ok || v != "retained" {
		t.Fatal(ok, err)
	}
	f.json("POST", connectionPath(org, id)+"/test", nil, map[string]string{"Idempotency-Key": "restart"}, 202)
}
func TestBootstrapInvalidationIntegration(t *testing.T) {
	f := newTestApp(t, false)
	old, err := f.db.IssueToken(context.Background(), "bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	c := f.a.Config
	f.stop()
	if err = <-f.done; err != nil {
		t.Fatal(err)
	}
	f.start(c, false)
	expectCode(t, f.json("POST", "/api/v1/auth/bootstrap", bson.M{"token": old}, nil, 401), "invalid_token")
	b, err := os.ReadFile(filepath.Join(c.DataDir, "bootstrap-url.txt"))
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(c.DataDir, "bootstrap-url.txt"))
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	_, raw, _ := strings.Cut(strings.TrimSpace(string(b)), "#token=")
	f.json("POST", "/api/v1/auth/bootstrap", bson.M{"token": raw}, nil, 200)
}
