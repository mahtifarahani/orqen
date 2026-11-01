package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"orqen/internal/catherd"
	"orqen/internal/testfakes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stoppable serves a handler on a fixed loopback address that can be stopped and started again (a VPN drop).
type stoppable struct {
	h    http.Handler
	addr string
	srv  *httptest.Server
}

func (s *stoppable) start(t *testing.T) {
	t.Helper()
	var l net.Listener
	var err error
	for i := 0; i < 50; i++ {
		if l, err = net.Listen("tcp", firstNonEmpty(s.addr, "127.0.0.1:0")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	s.srv = httptest.NewUnstartedServer(s.h)
	_ = s.srv.Listener.Close()
	s.srv.Listener = l
	s.srv.Start()
	s.addr = l.Addr().String()
}
func (s *stoppable) stop() {
	if s.srv != nil {
		s.srv.CloseClientConnections()
		s.srv.Close()
		s.srv = nil
	}
}
func (s *stoppable) url() string { return "http://" + s.addr }
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type intakeOpts struct {
	Intake       string // automation.intake (default auto)
	PlanApproval string // default manual
	CodingStart  string // default manual
	JQL          *string
	Scheduler    bool // keep the 100 ms scheduler (default: off, tests call Poll now)
	NoEnable     bool
	Extra        map[string]any // more org policy overrides
	Setup        func(*App)
	NoCatherd    bool
	// Git serves real repositories over smart HTTP on a second loopback address (stoppable), advertised by the fake
	// GitLab as http_url_to_repo, and gives GitLab and Jira unique sentinel tokens.
	Git bool
	// GitHostNotAllowed leaves the repository host out of the connection's allowed hosts.
	GitHostNotAllowed bool
	// NoHost leaves the Codex host unprobed (Sprint 4).
	NoHost bool
	// ProfileDoc replaces the model profile document (default goodProfileDoc).
	ProfileDoc func() map[string]any
}

// intakeEnv is a ready project (mapped, contract reviewed, profile bound, doctor run) on a seeded fake Jira.
type intakeEnv struct {
	*projectEnv
	s    *setup
	jfS  *stoppable
	glS  *stoppable
	repS *stoppable
	repo *testfakes.GitRepos
	opts intakeOpts
	// Sentinels are the unique tokens of the fakes (Git mode).
	GitlabToken, JiraToken string
}

func newIntakeEnv(t *testing.T, o intakeOpts) *intakeEnv {
	t.Helper()
	if o.Intake == "" {
		o.Intake = "auto"
	}
	if o.PlanApproval == "" {
		o.PlanApproval = "manual"
	}
	if o.CodingStart == "" {
		o.CodingStart = "manual"
	}
	e := &projectEnv{gl: testfakes.NewGitLab(), jf: testfakes.NewJira()}
	e.jf.Cloud = true
	e.jf.SeedIssues()
	ie := &intakeEnv{projectEnv: e, opts: o, GitlabToken: "glpat-test", JiraToken: e.jf.Token}
	if o.Git {
		repos, err := testfakes.NewGitRepos(t.TempDir())
		if err != nil {
			t.Skip("git smart HTTP is not available: ", err)
		}
		ie.repo = repos
		ie.GitlabToken = "glpat-SENTINEL-" + strings.ToLower(newID("t"))[2:]
		ie.JiraToken = "jira-SENTINEL-" + strings.ToLower(newID("t"))[2:]
		e.gl.Token = ie.GitlabToken
		repos.SetToken(ie.GitlabToken)
		e.jf.SetToken(ie.JiraToken)
		ie.repS = &stoppable{h: repos.Handler()}
		ie.repS.start(t)
		e.gl.PublicBase = ie.repS.url()
		if o.GitHostNotAllowed {
			e.gl.PublicBase = "http://localhost:" + strings.Split(ie.repS.addr, ":")[1] // another host name than the connection's
		}
	}
	ie.glS = &stoppable{h: e.gl.Handler()}
	ie.jfS = &stoppable{h: e.jf.Handler()}
	ie.glS.start(t)
	ie.jfS.start(t)
	t.Cleanup(func() {
		ie.glS.stop()
		ie.jfS.stop()
		if ie.repS != nil {
			ie.repS.stop()
		}
	})
	e.fake = catherd.NewFake()
	e.f = newTestAppWith(t, true, func(a *App) {
		a.SetCatherd(e.fake, fakeRuntime("1.5.0"))
		if !o.Scheduler {
			a.schedTick = time.Hour
		}
		if o.Setup != nil {
			o.Setup(a)
		}
	})
	e.f.bootstrap()
	e.org = e.f.org("alpha")
	markHostProbed(e.f, e.org) // enabling automation needs a probed host; NoHost clears it again below
	gl := gitlabSpec(ie.glS.url())
	gl.Secret = ie.GitlabToken
	e.glID = e.f.providerConn(e.org, gl)
	e.jiID = e.f.providerConn(e.org, connSpec{Name: "Jira", Provider: "jira", Deployment: "cloud", Base: ie.jfS.url() + "/jira", Method: "api_token", Username: e.jf.User, Secret: ie.JiraToken})
	e.f.refreshCatalog(e.org, e.glID)
	e.f.runTest(e.org, e.glID)
	e.f.runTest(e.org, e.jiID)
	e.pid = e.add("a", "1")["created"].([]any)[0].(map[string]any)["id"].(string)
	ie.s = &setup{e: e, pid: e.pid}
	jira := &JiraMappingInput{ConnectionID: e.jiID, ProjectID: "10000", Board: &JiraBoardInput{ID: "1"}}
	if o.JQL != nil {
		jira = &JiraMappingInput{ConnectionID: e.jiID, ProjectID: "10000", JQL: o.JQL}
	}
	e.mapping(e.pid, RepoMappingInput{Role: "primary", ConnectionID: e.glID, ProviderProjectID: "1", TargetBranch: "main"}, jira)
	e.check(e.pid)
	ie.s.contract()
	ie.s.doctor()
	profileDoc := goodProfileDoc()
	// Sprint 3 fixtures have no Codex host: the architect stays off unless a test brings its own profile.
	profileDoc["roles"].(map[string]any)["architect"].(map[string]any)["enabled"] = false
	if o.ProfileDoc != nil {
		profileDoc = o.ProfileDoc()
	}
	ie.s.version = ie.s.profile(profileDoc)
	ie.s.bind(ie.s.version)
	overrides := map[string]any{"automation.intake": o.Intake, "automation.plan_approval": o.PlanApproval, "automation.coding_start": o.CodingStart}
	for k, v := range o.Extra {
		overrides[k] = v
	}
	e.publishPolicy("/api/v1/orgs/"+e.org+"/policy", overrides)
	if !o.NoEnable {
		e.enable()
	}
	if o.NoHost {
		_, _ = e.f.db.Coll("host_states").UpdateOne(context.Background(), bson.M{"_id": hostStateID(e.org)}, bson.M{"$set": bson.M{"last_probe": nil}})
	}
	return ie
}

// publishPolicy replaces the draft at base (".../policy" or ".../projects/{id}/policy") and publishes it.
func (e *projectEnv) publishPolicy(base string, overrides map[string]any) {
	e.f.t.Helper()
	draft := e.f.json("GET", base+"/draft", nil, nil, 200)
	rev := int(draft["revision"].(float64))
	e.f.json("PUT", base+"/draft", map[string]any{"overrides": overrides}, map[string]string{"If-Match": fmt.Sprint(rev)}, 200)
	e.f.json("POST", base+"/publish", nil, map[string]string{"If-Match": fmt.Sprint(rev + 1), "Idempotency-Key": nextKey()}, 201)
}
func (e *projectEnv) enable() {
	e.f.t.Helper()
	e.f.json("POST", e.path(e.pid, "automation", "enable"), nil, map[string]string{"If-Match": e.rev(e.pid), "Idempotency-Key": nextKey()}, 200)
}

// poll runs "Poll now" and waits for the operation.
func (e *projectEnv) poll() map[string]any {
	e.f.t.Helper()
	// With the scheduler on, its own poll job can be active (it has no operation to answer with): ask again.
	for attempt := 0; ; attempt++ {
		resp := e.f.request("POST", e.path(e.pid, "intake", "poll"), nil, map[string]string{"Idempotency-Key": nextKey()})
		if resp.StatusCode == 409 && attempt < 40 {
			resp.Body.Close()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		op := readResponse(e.f.t, resp, 202)
		return e.f.waitOperation(e.org, op["id"].(string))
	}
}
func (e *projectEnv) orgPath(parts ...string) string {
	p := "/api/v1/orgs/" + e.org
	for _, s := range parts {
		p += "/" + s
	}
	return p
}

// waitFor retries cond for up to 15 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + what)
}
func (e *projectEnv) count(coll string, filter bson.M) int {
	n, err := e.f.db.Coll(coll).CountDocuments(context.Background(), filter)
	if err != nil {
		e.f.t.Fatal(err)
	}
	return int(n)
}

// trackedByKey returns the tracked issue (DB document) for an issue key.
func (e *projectEnv) trackedByKey(key string) (TrackedIssueDoc, bool) {
	d, err := findOne[TrackedIssueDoc](context.Background(), e.f.db, "tracked_issues", bson.M{"org_id": e.org, "key": key})
	return d, err == nil
}
func (e *projectEnv) runOf(key string) (RunDoc, bool) {
	runs, err := findMany[RunDoc](context.Background(), e.f.db, "runs", bson.M{"org_id": e.org, "issue_key": key}, bson.D{{Key: "_id", Value: -1}}, 1)
	if err != nil || len(runs) == 0 {
		return RunDoc{}, false
	}
	return runs[0], true
}
func (e *projectEnv) requestsMatching(sub string) int {
	n := 0
	for _, r := range e.jf.Requests() {
		if strings.Contains(r, sub) {
			n++
		}
	}
	return n
}

// eligibleKeys are the seeded issues the default intake filter returns.
var eligibleKeys = []string{"ORQ-1", "ORQ-2", "ORQ-3", "ORQ-4", "ORQ-6", "ORQ-7"}

type testfakesIssue = testfakes.FakeIssue
type failure = testfakes.Failure

func decodeArray(t *testing.T, resp *http.Response) []map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var out []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// leakScan fails the test if any sentinel appears in a Mongo document, a file under the data directory (mirrors and
// worktrees included), a file name or the captured log output. It returns how many documents and files it read.
func (e *projectEnv) leakScan(sentinels []string, logs string, extraDirs ...string) (docs, files int) {
	e.f.t.Helper()
	found := func(where string, data []byte) {
		for _, s := range sentinels {
			if bytes.Contains(data, []byte(s)) {
				e.f.t.Errorf("sentinel leaked into %s", where)
			}
		}
	}
	ctx := context.Background()
	names, err := e.f.db.Database.ListCollectionNames(ctx, bson.M{})
	if err != nil {
		e.f.t.Fatal(err)
	}
	for _, name := range names {
		cur, err := e.f.db.Coll(name).Find(ctx, bson.M{})
		if err != nil {
			e.f.t.Fatal(err)
		}
		for cur.Next(ctx) {
			docs++
			found("collection "+name, cur.Current)
		}
		_ = cur.Close(ctx)
	}
	dirs := append([]string{e.f.a.Config.DataDir}, extraDirs...)
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			files++
			b, _ := os.ReadFile(p)
			found("file "+p, b)
			found("file name "+p, []byte(p))
			return nil
		})
	}
	found("slog output", []byte(logs))
	return docs, files
}

// captureLogs sends slog output to a buffer for the test's duration.
func captureLogs(t *testing.T) *lockedBuffer {
	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return logs
}

// markHostProbed records a passing probe of the fake Codex host so readiness does not wait on it.
func markHostProbed(f *testApp, org string) {
	_, _ = f.db.Coll("host_states").UpdateOne(context.Background(), bson.M{"_id": hostStateID(org)}, bson.M{"$set": bson.M{"org_id": org, "last_probe": HostProbe{Status: "ok", CodexVersion: "0.160.0", Login: "ok", Fake: true, CheckedAt: time.Now().UTC()}}, "$setOnInsert": bson.M{"revision": int64(1)}}, options.UpdateOne().SetUpsert(true))
}
