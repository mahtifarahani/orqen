package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"orqen/internal/catherd"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

type secretEnvT struct {
	*catherdEnv
	servers map[string]*httptest.Server
	seen    map[string][]string // kind -> auth values the verifier servers received
	mu      *sync.Mutex
	rec     *recordingTransport
}

// recordingTransport keeps every response (headers and body) the API produced.
type recordingTransport struct {
	mu   sync.Mutex
	logs [][]byte
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var h bytes.Buffer
	_ = resp.Header.Write(&h)
	r.mu.Lock()
	r.logs = append(r.logs, append(h.Bytes(), body...))
	r.mu.Unlock()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// verifier answers 200 for a key starting with "good", 401 for "bad" and 500 otherwise.
func (e *secretEnvT) verifier(kind string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization") + r.Header.Get("x-api-key") + r.Header.Get("x-goog-api-key")
		if r.URL.RawQuery != "" && kind != "artificial_analysis" || strings.Contains(r.URL.String(), "orqen-") {
			http.Error(w, "key in URL", 500)
			return
		}
		e.mu.Lock()
		e.seen[kind] = append(e.seen[kind], auth)
		e.mu.Unlock()
		switch {
		case strings.Contains(auth, "good"):
			w.WriteHeader(200)
		case strings.Contains(auth, "bad"):
			w.WriteHeader(401)
		default:
			w.WriteHeader(500)
		}
	}))
	return srv
}
func newSecretEnv(t *testing.T) *secretEnvT {
	t.Helper()
	fake := catherd.NewFake()
	e := &secretEnvT{servers: map[string]*httptest.Server{}, seen: map[string][]string{}, mu: &sync.Mutex{}, rec: &recordingTransport{}}
	bases := map[string]string{}
	for _, k := range secretKinds {
		if k.verifiable() {
			e.servers[k.Kind] = e.verifier(k.Kind)
			bases[k.Kind] = e.servers[k.Kind].URL
			t.Cleanup(e.servers[k.Kind].Close)
		}
	}
	f := newTestAppWith(t, true, func(a *App) { a.SetCatherd(fake, fakeRuntime("1.5.0")); a.secretBase = bases })
	f.client.Transport = e.rec
	f.bootstrap()
	e.catherdEnv = &catherdEnv{f: f, fake: fake, org: f.org("alpha")}
	return e
}
func (e *secretEnvT) list() map[string]map[string]any {
	out := map[string]map[string]any{}
	req := e.f.request("GET", e.path("/secrets"), nil, nil)
	defer req.Body.Close()
	var rows []map[string]any
	if err := json.NewDecoder(req.Body).Decode(&rows); err != nil {
		e.f.t.Fatal(err)
	}
	for _, r := range rows {
		out[r["kind"].(string)] = r
	}
	return out
}
func (e *secretEnvT) put(kind, value string, ifMatch string, status int) map[string]any {
	return e.f.json("PUT", e.path("/secrets/"+kind), map[string]string{"secret": value}, map[string]string{"If-Match": ifMatch}, status)
}
func (e *secretEnvT) set(kind, value string) {
	e.f.t.Helper()
	rev := fmt.Sprint(int(e.list()[kind]["revision"].(float64)))
	e.put(kind, value, rev, 200)
}
func (e *secretEnvT) check(kind string) map[string]any {
	op := e.f.json("POST", e.path("/secrets/"+kind+"/check"), nil, map[string]string{"Idempotency-Key": nextKey()}, 202)
	return e.f.waitOperation(e.org, op["id"].(string))
}
func (s *MemoryStore) refs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for k := range s.values {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestCatherdSecretsPutReplaceRemove(t *testing.T) {
	e := newSecretEnv(t)
	l := e.list()
	if len(l) != 5 {
		t.Fatal(l)
	}
	for _, k := range []string{"jev", "artificial_analysis", "gemini", "cursor", "xai"} {
		if l[k]["state"] != "none" || l[k]["revision"] != float64(0) || l[k]["last_check"] != nil {
			t.Fatal(k, l[k])
		}
	}
	if l["jev"]["env_name"] != "TYPESAFE_API_KEY" || l["cursor"]["verifiable"] != false || l["jev"]["verifiable"] != true || l["xai"]["required_reason"] != "isolated_harness" || l["jev"]["required_reason"] != nil {
		t.Fatal(l["jev"], l["cursor"])
	}
	// First PUT needs If-Match "0"; the DTO carries no secret, hash or reference.
	body := e.put("jev", "good-first", "\"0\"", 200)
	if body["state"] != "set" || body["revision"] != float64(1) || body["updated_at"] == nil {
		t.Fatal(body)
	}
	if b, _ := json.Marshal(body); strings.Contains(string(b), "good-first") || strings.Contains(string(b), "cred_") {
		t.Fatal("the response must not carry the secret or its reference:", string(b))
	}
	oldRefs := e.f.creds.refs()
	if len(oldRefs) != 1 {
		t.Fatal(oldRefs)
	}
	// A second first-PUT and a stale revision are conflicts that carry the current state.
	conf := e.f.json("PUT", e.path("/secrets/jev"), map[string]string{"secret": "good-x"}, map[string]string{"If-Match": "0"}, 409)
	expectCode(t, conf, "revision_conflict")
	if conf["current"].(map[string]any)["revision"] != float64(1) {
		t.Fatal(conf)
	}
	expectCode(t, e.f.json("PUT", e.path("/secrets/jev"), map[string]string{"secret": "good-x"}, map[string]string{"If-Match": "5"}, 409), "revision_conflict")
	if got := e.f.creds.refs(); len(got) != 1 || got[0] != oldRefs[0] {
		t.Fatal("a rejected PUT must leave the stored key and add no orphan:", got)
	}
	// Replace: the old Keychain item is deleted.
	e.put("jev", "good-second", "1", 200)
	newRefs := e.f.creds.refs()
	if len(newRefs) != 1 || newRefs[0] == oldRefs[0] {
		t.Fatal("the old reference must be deleted:", oldRefs, newRefs)
	}
	// Remove.
	expectCode(t, e.f.json("DELETE", e.path("/secrets/jev"), nil, map[string]string{"If-Match": "1"}, 409), "revision_conflict")
	rm := e.f.json("DELETE", e.path("/secrets/jev"), nil, map[string]string{"If-Match": "2"}, 200)
	if rm["state"] != "none" || rm["revision"] != float64(3) || len(e.f.creds.refs()) != 0 {
		t.Fatal(rm, e.f.creds.refs())
	}
	// Removing something never stored is a no-op; the precondition is still required.
	e.f.json("DELETE", e.path("/secrets/xai"), nil, map[string]string{"If-Match": "0"}, 200)
	expectCode(t, e.f.json("DELETE", e.path("/secrets/xai"), nil, nil, 428), "precondition_required")
	// Validation never echoes the value.
	for _, bad := range []string{"", "two\nlines", " leading", "trailing ", "tab\there", strings.Repeat("k", 2049)} {
		res := e.f.json("PUT", e.path("/secrets/gemini"), map[string]string{"secret": bad}, map[string]string{"If-Match": "0"}, 422)
		expectCode(t, res, "secret_invalid")
		if bad != "" {
			if b, _ := json.Marshal(res); strings.Contains(string(b), "leading") || strings.Contains(string(b), "two") {
				t.Fatal(string(b))
			}
		}
	}
	expectCode(t, e.f.json("PUT", e.path("/secrets/nope"), map[string]string{"secret": "x"}, map[string]string{"If-Match": "0"}, 404), "not_found")
	expectCode(t, e.f.json("POST", e.path("/secrets/nope/check"), nil, map[string]string{"Idempotency-Key": nextKey()}, 404), "not_found")
	// Events carry the kind only.
	ev := e.f.json("GET", "/api/v1/orgs/"+e.org+"/events?limit=200", nil, nil, 200)
	kinds := map[string]bool{}
	for _, x := range ev["items"].([]any) {
		m := x.(map[string]any)
		kinds[m["type"].(string)] = true
		if strings.HasPrefix(m["type"].(string), "catherd_secret.") {
			if p, _ := json.Marshal(m["payload"]); !strings.Contains(string(p), "jev") || strings.Contains(string(p), "good") || strings.Contains(string(p), "cred_") {
				t.Fatal(string(p))
			}
		}
	}
	if !kinds["catherd_secret.set"] || !kinds["catherd_secret.removed"] {
		t.Fatal(kinds)
	}
}
func TestCatherdSecretsCheck(t *testing.T) {
	e := newSecretEnv(t)
	// Nothing set: refused up front.
	e.f.json("POST", e.path("/secrets/jev/check"), nil, map[string]string{"Idempotency-Key": nextKey()}, 422)
	cases := []struct{ kind, value, want string }{
		{"jev", "good-jev", "ok"}, {"jev", "bad-jev", "refused"}, {"jev", "odd-jev", "unchecked"},
		{"artificial_analysis", "good-aa", "ok"}, {"artificial_analysis", "bad-aa", "refused"}, {"artificial_analysis", "odd-aa", "unchecked"},
		{"gemini", "good-g", "ok"}, {"xai", "bad-x", "refused"}, {"cursor", "any-cursor", "unverifiable"},
	}
	for _, c := range cases {
		e.set(c.kind, c.value)
		if st := e.list()[c.kind]; st["last_check"] != nil {
			t.Fatal("a new key must not inherit an old check:", st)
		}
		op := e.check(c.kind)
		if op["status"] != "succeeded" || op["result"].(map[string]any)["status"] != c.want {
			t.Fatal(c, op)
		}
		lc := e.list()[c.kind]["last_check"].(map[string]any)
		if lc["status"] != c.want || lc["operation_id"] != op["id"] || len(lc["detail"].(string)) > 300 || strings.Contains(lc["detail"].(string), c.value) {
			t.Fatal(c, lc)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.seen["cursor"]) != 0 || len(e.seen["jev"]) != 3 || e.seen["jev"][0] != "Bearer good-jev" || e.seen["artificial_analysis"][0] != "good-aa" || e.seen["gemini"][0] != "good-g" || e.seen["xai"][0] != "Bearer bad-x" {
		t.Fatal(e.seen)
	}
	// A check never runs a model and never calls catherd.
	if n := len(e.fake.Calls()); n != 0 {
		t.Fatal("the check must not call catherd:", e.fake.Calls())
	}
}
func TestCatherdSecretsInjectionMatrix(t *testing.T) {
	e := newSecretEnv(t)
	e.post("/catalog/refresh", map[string]any{"mode": "list"})
	// Without keys nothing is injected anywhere.
	e.post("/doctor", nil)
	for _, c := range e.fake.Calls() {
		if c.Env != "" {
			t.Fatal("no key is set, nothing may be injected:", c)
		}
	}
	vals := map[string]string{"jev": "v-jev", "artificial_analysis": "v-aa", "gemini": "v-gem", "cursor": "v-cur", "xai": "v-xai"}
	for k, v := range vals {
		e.set(k, v)
	}
	envs := func(op string) []string {
		cs := e.fake.CallsOf(op)
		return cs[len(cs)-1].EnvNames()
	}
	hash := func(v string) string { return catherd.SecretHash(v) }
	e.post("/doctor", nil)
	if got := strings.Join(envs("doctor"), ","); got != "CURSOR_API_KEY,GEMINI_API_KEY,TYPESAFE_API_KEY,XAI_API_KEY" {
		t.Fatal(got)
	}
	if c := e.fake.CallsOf("doctor"); c[len(c)-1].Host != "codex" || !strings.Contains(c[len(c)-1].Env, "TYPESAFE_API_KEY="+hash("v-jev")) {
		t.Fatal(c[len(c)-1])
	}
	e.post("/catalog/refresh", map[string]any{"mode": "sync"})
	if got := strings.Join(envs("catalog_sync"), ","); got != "ARTIFICIAL_ANALYSIS_API_KEY" {
		t.Fatal(got)
	}
	e.post("/catalog/refresh", map[string]any{"mode": "listings"})
	if got := strings.Join(envs("catalog_refresh"), ","); got != "CURSOR_API_KEY,GEMINI_API_KEY,XAI_API_KEY" {
		t.Fatal(got)
	}
	e.post("/catalog/refresh", map[string]any{"mode": "list"})
	// list/show/catalog list/remove and every personal call get nothing.
	pe := &profileEnv{e.catherdEnv}
	id := pe.create("main", goodProfileDoc())
	pe.validate(id)
	e.f.json("GET", "/api/v1/orgs/"+e.org+"/catherd/personal-profiles", nil, nil, 200)
	for _, c := range e.fake.Calls() {
		switch c.Op {
		case "list", "show_raw", "show_resolved", "catalog_list", "remove", "version":
			if c.Env != "" {
				t.Fatal("this call must not receive secrets:", c)
			}
		case "create", "set", "validate":
			if c.Env != "" {
				t.Fatal("no harness is isolated in this document:", c)
			}
		}
	}
	// grok isolated: only XAI_API_KEY goes to create/set/validate, for staging and for publish.
	iso := goodProfileDoc()
	iso["harness"].(map[string]any)["grok"] = map[string]any{"isolated": true}
	id2 := pe.create("iso", iso)
	before := len(e.fake.Calls())
	if op := pe.validate(id2); op["status"] != "succeeded" || op["result"].(map[string]any)["valid"] != true {
		t.Fatal(op, e.f.json("GET", pe.base()+"/"+id2, nil, nil, 200)["last_validation"])
	}
	if op := pe.publish(id2); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	n := 0
	for _, c := range e.fake.Calls()[before:] {
		switch c.Op {
		case "create", "set", "validate":
			n++
			if c.Env != "XAI_API_KEY="+hash("v-xai") {
				t.Fatal("only the isolated backend's key may be injected:", c)
			}
		default:
			if c.Env != "" {
				t.Fatal(c)
			}
		}
	}
	if n < 6 {
		t.Fatal("expected staging and publish calls, saw", n)
	}
}

// sentinels returns one distinct sentinel per kind.
func sentinels() map[string]string {
	out := map[string]string{}
	for _, k := range secretKinds {
		out[k.Kind] = "orqen-SENTINEL-" + newID("s")[2:]
	}
	return out
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// TestSecretLeak puts a sentinel into every kind, runs every job that touches a secret and proves the sentinels
// exist only in the credential store.
func TestSecretLeak(t *testing.T) {
	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	e := newSecretEnv(t)
	sent := sentinels()
	for k, v := range sent {
		e.set(k, v)
	}
	e.post("/catalog/refresh", map[string]any{"mode": "list"})
	e.post("/doctor", nil)
	e.post("/catalog/refresh", map[string]any{"mode": "sync"})
	e.post("/catalog/refresh", map[string]any{"mode": "listings"})
	pe := &profileEnv{e.catherdEnv}
	iso := goodProfileDoc()
	for _, b := range []string{"grok", "cursor", "antigravity"} {
		iso["harness"].(map[string]any)[b] = map[string]any{"isolated": true}
	}
	id := pe.create("iso", iso)
	if op := pe.validate(id); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	if op := pe.publish(id); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	for k := range sent {
		e.check(k)
	}
	// Read everything the API can show.
	for _, p := range []string{"/status", "/catalog", "/secrets", "/personal-profiles"} {
		e.f.request("GET", e.path(p), nil, nil).Body.Close()
	}
	e.f.json("GET", "/api/v1/orgs/"+e.org+"/events?limit=500", nil, nil, 200)
	e.f.json("GET", "/api/v1/orgs/"+e.org+"/health", nil, nil, 200)
	e.f.json("GET", "/api/v1/orgs/"+e.org+"/model-profiles/"+id, nil, nil, 200)
	e.f.json("GET", "/api/v1/orgs/"+e.org+"/model-profiles/"+id+"/versions", nil, nil, 200)

	found := func(where string, data []byte) {
		for k, s := range sent {
			if bytes.Contains(data, []byte(s)) {
				t.Errorf("the %s sentinel leaked into %s", k, where)
			}
		}
	}
	// 1. every API response (headers and body) seen by the client.
	e.rec.mu.Lock()
	for i, l := range e.rec.logs {
		found(fmt.Sprintf("API response #%d", i), l)
	}
	nresp := len(e.rec.logs)
	e.rec.mu.Unlock()
	if nresp < 20 {
		t.Fatal("recorded only", nresp, "responses")
	}
	// 2. every document of every Mongo collection (raw bson).
	ctx := context.Background()
	names, err := e.f.db.Database.ListCollectionNames(ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	docs := 0
	for _, name := range names {
		cur, err := e.f.db.Coll(name).Find(ctx, bson.M{})
		if err != nil {
			t.Fatal(err)
		}
		for cur.Next(ctx) {
			docs++
			found("collection "+name, cur.Current)
		}
		_ = cur.Close(ctx)
	}
	if docs < 20 {
		t.Fatal("dumped only", docs, "documents")
	}
	// 3. every file under the data directory, and no credentials.json anywhere.
	files := 0
	_ = filepath.WalkDir(e.f.a.Config.DataDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		files++
		if d.Name() == "credentials.json" {
			t.Errorf("catherd's credentials.json must never be written: %s", p)
		}
		b, _ := os.ReadFile(p)
		found("file "+p, b)
		found("file name "+p, []byte(p))
		return nil
	})
	if files < 2 {
		t.Fatal("scanned only", files, "files")
	}
	// 4. logs, 5. fake-recorded calls (argv-level data), 6. the verifier servers got the values only as headers.
	found("slog output", []byte(logs.String()))
	found("recorded catherd calls", []byte(fmt.Sprintf("%+v", e.fake.Calls())))
	e.mu.Lock()
	for kind, urls := range e.seen {
		if len(urls) == 0 {
			t.Error("the verifier for", kind, "was not called")
		}
	}
	e.mu.Unlock()
	// 7. positive control: the values are in the credential store and nowhere else.
	stored := 0
	e.f.creds.mu.RLock()
	for _, v := range e.f.creds.values {
		for _, s := range sent {
			if v == s {
				stored++
			}
		}
	}
	e.f.creds.mu.RUnlock()
	if stored != 5 {
		t.Fatal("expected all five sentinels in the credential store, found", stored)
	}
	for _, c := range e.fake.Calls() {
		if strings.Contains(c.Env, "SENTINEL") {
			t.Fatal(c)
		}
	}
}
