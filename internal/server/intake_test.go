package server

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net"
	"orqen/internal/runstate"
	"strings"
	"testing"
	"time"
)

func (e *projectEnv) tasks(query string) []map[string]any {
	e.f.t.Helper()
	return e.f.walk(e.orgPath("tasks") + query)
}
func (e *projectEnv) waitRuns(n int) {
	e.f.t.Helper()
	waitFor(e.f.t, fmt.Sprintf("%d runs", n), func() bool { return e.count("runs", bson.M{"org_id": e.org}) >= n })
}
func (e *projectEnv) intakeDoc() IntakeStateDoc {
	d, _ := findOne[IntakeStateDoc](context.Background(), e.f.db, "intake_states", bson.M{"project_id": e.pid})
	return d
}
func (e *projectEnv) dueNow() {
	_, err := e.f.db.Coll("intake_states").UpdateOne(context.Background(), bson.M{"project_id": e.pid}, bson.M{"$set": bson.M{"next_poll_at": time.Now().Add(-time.Minute)}})
	if err != nil {
		e.f.t.Fatal(err)
	}
}

// maxActive is the highest number of active jobs sharing one dedupe key seen in the sample.
func (e *projectEnv) activeJobs(kind string) map[string]int {
	jobs, _ := findMany[JobDoc](context.Background(), e.f.db, "jobs", bson.M{"org_id": e.org, "kind": kind, "active": true}, bson.D{{Key: "_id", Value: 1}}, 1000)
	out := map[string]int{}
	for _, j := range jobs {
		out[j.DedupeKey]++
	}
	return out
}

func TestIntakeDuplicatePollAndUnchangedIssues(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{})
	// Two concurrent Poll now calls share one operation.
	type res struct{ id string }
	ch := make(chan string, 2)
	for i := 0; i < 2; i++ {
		go func() {
			op := e.f.json("POST", e.path(e.pid, "intake", "poll"), nil, map[string]string{"Idempotency-Key": nextKey()}, 202)
			ch <- op["id"].(string)
		}()
	}
	a, b := <-ch, <-ch
	if a != b {
		// The second call may arrive after the first job finished; then it is a legitimate second poll.
		e.f.waitOperation(e.org, b)
	}
	e.f.waitOperation(e.org, a)
	e.waitRuns(6)
	for k, n := range e.activeJobs("intake_poll") {
		if n > 1 {
			t.Fatal("two active polls", k)
		}
	}
	e.poll()
	e.poll()
	for _, key := range eligibleKeys {
		if n := e.count("runs", bson.M{"org_id": e.org, "issue_key": key}); n != 1 {
			t.Fatalf("%s has %d runs", key, n)
		}
	}
	if n := e.count("runs", bson.M{"org_id": e.org}); n != 6 {
		t.Fatal(n)
	}
	// Unchanged issues cost only the minimal search: no further issue reads after the first ingest.
	reads := e.requestsMatching("/rest/api/2/issue/")
	e.poll()
	e.poll()
	if after := e.requestsMatching("/rest/api/2/issue/"); after != reads {
		t.Fatalf("unchanged issues were read again: %d -> %d", reads, after)
	}
	items := e.tasks("")
	if len(items) != 6 {
		t.Fatal(len(items))
	}
	for _, it := range items {
		run := it["run"].(map[string]any)
		if !contains(runstateAll(), run["state"].(string)) || it["in_filter"] != true {
			t.Fatal(it)
		}
	}
	if got := e.tasks("?q=orq-1"); len(got) != 1 || got[0]["issue_key"] != "ORQ-1" {
		t.Fatal(got)
	}
	if got := e.tasks("?project_id=" + e.pid); len(got) != 6 {
		t.Fatal(len(got))
	}
	if got := e.tasks("?state=candidate"); len(got) != 0 {
		t.Fatal(got)
	}
	expectCode(t, e.f.json("GET", e.orgPath("tasks")+"?state=nope", nil, nil, 422), "validation_failed")
}

func TestIntakeTwoRunnersOneRunPerIssue(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{Scheduler: true, NoEnable: true})
	c := e.f.a.Config
	second := NewApp(c, e.f.db, e.f.creds)
	second.SetCatherd(e.fake, fakeRuntime("1.5.0"))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- second.Run(ctx, l, true) }()
	t.Cleanup(func() { cancel(); <-done })
	e.enable()
	e.waitRuns(6)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, kind := range []string{"intake_poll", "issue_ingest", "run_advance"} {
			for k, n := range e.activeJobs(kind) {
				if n > 1 {
					t.Fatal("two active jobs for", k)
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, key := range eligibleKeys {
		if n := e.count("runs", bson.M{"org_id": e.org, "issue_key": key}); n != 1 {
			t.Fatalf("%s has %d runs", key, n)
		}
	}
	if n := e.count("run_attempts", bson.M{"org_id": e.org}); n != 6 {
		t.Fatal(n)
	}
}

func TestIntakeRefreshKeepsTheRun(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{})
	e.poll()
	e.waitRuns(6)
	waitFor(t, "ORQ-1 snapshot", func() bool { d, ok := e.trackedByKey("ORQ-1"); return ok && d.LatestRevision == 1 })
	first, _ := e.runOf("ORQ-1")
	e.jf.SetIssue("ORQ-1", func(i *testfakesIssue) {
		i.StatusName, i.StatusCategory, i.Priority = "In Progress", "indeterminate", "Low"
	})
	e.poll()
	waitFor(t, "second snapshot", func() bool { d, _ := e.trackedByKey("ORQ-1"); return d.LatestRevision == 2 })
	d, _ := e.trackedByKey("ORQ-1")
	if n := e.count("issue_snapshots", bson.M{"org_id": e.org, "tracked_issue_id": d.ID}); n != 2 {
		t.Fatal("snapshots", n)
	}
	again, _ := e.runOf("ORQ-1")
	if again.ID != first.ID || again.IntentKey != first.IntentKey || e.count("runs", bson.M{"org_id": e.org, "issue_key": "ORQ-1"}) != 1 || !again.Active {
		t.Fatal("a refresh must not create a run", again.ID, first.ID)
	}
	task := e.f.json("GET", e.orgPath("tasks", d.ID), nil, nil, 200)
	snap := task["snapshot"].(map[string]any)
	if snap["revision"] != float64(2) || snap["status"].(map[string]any)["category"] != "indeterminate" || snap["acceptance"].(map[string]any)["source"] != "section" {
		t.Fatal(snap)
	}
	if len(task["runs"].([]any)) != 1 {
		t.Fatal(task["runs"])
	}
	// The injection comment is stored as data, never applied.
	comments := snap["comments"].([]any)
	if len(comments) != 2 || !strings.Contains(comments[1].(map[string]any)["body"].(string), "ignore previous instructions") {
		t.Fatal(comments)
	}
	if eff := e.f.json("GET", e.orgPath("policy", "effective"), nil, nil, 200); policyValueOf(eff, "automation.plan_approval") != "manual" {
		t.Fatal("policy changed")
	}
}
func policyValueOf(eff map[string]any, key string) any {
	for _, x := range eff["items"].([]any) {
		if m := x.(map[string]any); m["key"] == key {
			return m["value"]
		}
	}
	return nil
}

func TestIntakeSupersedeOnMaterialChange(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{})
	e.poll()
	e.waitRuns(6)
	old, _ := e.runOf("ORQ-3")
	e.jf.SetIssue("ORQ-3", func(i *testfakesIssue) {
		i.Description = "Add a footer row.\n\nh2. Acceptance Criteria\n* The footer shows the sum of the visible invoice totals.\n* It is hidden on print."
	})
	e.poll()
	waitFor(t, "ORQ-3 superseded", func() bool { r, _ := e.runOf("ORQ-3"); return r.ID != old.ID })
	cur, _ := e.runOf("ORQ-3")
	was := e.f.json("GET", e.orgPath("runs", old.ID), nil, nil, 200)
	if was["state"] != "superseded" || was["active"] != false || was["superseded_by"] != cur.ID || cur.Supersedes == nil || *cur.Supersedes != old.ID || cur.Origin != "supersede" || !cur.Active {
		t.Fatal(was, cur)
	}
	if e.count("runs", bson.M{"org_id": e.org, "issue_key": "ORQ-3", "active": true}) != 1 {
		t.Fatal("exactly one active run per issue")
	}
}

func TestIntakeRateLimitedThenSucceeds(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{})
	e.jf.Fail["/rest/api/3/search"] = &failure{Status: 429, RetryAfter: "2", Count: 1}
	op := e.f.json("POST", e.path(e.pid, "intake", "poll"), nil, map[string]string{"Idempotency-Key": nextKey()}, 202)
	var job JobDoc
	waitFor(t, "the failed attempt", func() bool {
		j, err := findOne[JobDoc](context.Background(), e.f.db, "jobs", bson.M{"org_id": e.org, "kind": "intake_poll", "attempts": bson.M{"$gte": 1}, "state": "queued"})
		job = j
		return err == nil && j.LastError != nil
	})
	if wait := job.NextAt.Sub(job.UpdatedAt); wait < 1900*time.Millisecond {
		t.Fatal("the retry ignored Retry-After:", wait)
	}
	if st := e.intakeDoc(); st.LastError == nil || st.LastError.Code != "rate_limited" {
		t.Fatal(st.LastError)
	}
	done := e.f.waitOperation(e.org, op["id"].(string))
	if done["status"] != "succeeded" {
		t.Fatal(done)
	}
	if st := e.intakeDoc(); st.LastError != nil || st.LastSuccessStartedAt == nil {
		t.Fatal(st)
	}
}

func TestIntakeCredentialRejectedBlocksUntilReplaced(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{Scheduler: true, NoEnable: true})
	e.jf.SetToken("rotated")
	e.enable()
	waitFor(t, "blocked", func() bool { return e.intakeDoc().Blocked != nil })
	st := e.intakeDoc()
	if st.Blocked.Code != "credential_expired" || st.LastError == nil {
		t.Fatal(st)
	}
	e.jf.Reset()
	e.dueNow()
	time.Sleep(900 * time.Millisecond)
	if n := len(e.jf.Requests()); n != 0 {
		t.Fatal("a blocked project polled again:", n)
	}
	e.f.json("PUT", "/api/v1/orgs/"+e.org+"/connections/"+e.jiID+"/credential", map[string]any{"secret": "rotated"}, map[string]string{"If-Match": e.connRev()}, 200)
	waitFor(t, "recovery", func() bool { st := e.intakeDoc(); return st.Blocked == nil && st.LastSuccessStartedAt != nil })
	e.waitRuns(6)
}
func (e *projectEnv) connRev() string {
	c := e.f.json("GET", "/api/v1/orgs/"+e.org+"/connections/"+e.jiID, nil, nil, 200)
	return fmt.Sprint(int(c["revision"].(float64)))
}

func TestIntakeNetworkDropWaitsAndRecovers(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{})
	e.poll()
	e.waitRuns(6)
	first := e.intakeDoc().LastSuccessStartedAt
	if first == nil {
		t.Fatal("no cursor")
	}
	e.jfS.stop()
	op := e.f.json("POST", e.path(e.pid, "intake", "poll"), nil, map[string]string{"Idempotency-Key": nextKey()}, 202)
	waitFor(t, "waiting for the network", func() bool { return e.intakeDoc().WaitingNetwork })
	if st := e.intakeDoc(); st.LastSuccessStartedAt == nil || !st.LastSuccessStartedAt.Equal(*first) {
		t.Fatal("the cursor must stay put during an outage")
	}
	e.jfS.start(t)
	done := e.f.waitOperation(e.org, op["id"].(string))
	if done["status"] != "succeeded" {
		t.Fatal(done)
	}
	if st := e.intakeDoc(); st.WaitingNetwork || st.LastError != nil || !st.LastSuccessStartedAt.After(*first) {
		t.Fatal(st)
	}
}

func TestIntakeLeftFilterHoldsTheRunAndReentryReleases(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{})
	e.poll()
	e.waitRuns(6)
	waitFor(t, "ORQ-1 ingested", func() bool { d, ok := e.trackedByKey("ORQ-1"); return ok && d.LatestRevision == 1 })
	e.jf.SetIssue("ORQ-1", func(i *testfakesIssue) { i.Labels = nil })
	e.setSinceFull(100)
	e.poll()
	waitFor(t, "left filter", func() bool { d, _ := e.trackedByKey("ORQ-1"); return !d.InFilter })
	r, _ := e.runOf("ORQ-1")
	if r.Eligibility != "left_filter" || r.Hold == nil || r.Hold.Code != "left_filter" || !r.Active {
		t.Fatal(r.Eligibility, r.Hold, r.State)
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "intake.issue_left_filter"}) != 1 {
		t.Fatal("event missing")
	}
	e.jf.SetIssue("ORQ-1", func(i *testfakesIssue) { i.Labels = []string{"ai-ready"} })
	e.poll()
	waitFor(t, "re-entry", func() bool { r, _ := e.runOf("ORQ-1"); return r.Eligibility == "eligible" && r.Hold == nil })
	if d, _ := e.trackedByKey("ORQ-1"); !d.InFilter {
		t.Fatal("in_filter")
	}
	if e.count("runs", bson.M{"org_id": e.org, "issue_key": "ORQ-1"}) != 1 {
		t.Fatal("re-entry must not create a run")
	}
}
func (e *projectEnv) setSinceFull(n int) {
	if _, err := e.f.db.Coll("intake_states").UpdateOne(context.Background(), bson.M{"project_id": e.pid}, bson.M{"$set": bson.M{"polls_since_full": n}}); err != nil {
		e.f.t.Fatal(err)
	}
}

func TestIntakeManualModeCandidatesAndEvaluate(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{Intake: "manual"})
	e.poll()
	waitFor(t, "candidates", func() bool { return len(e.tasks("?state=candidate")) == 6 })
	if e.count("runs", bson.M{"org_id": e.org}) != 0 {
		t.Fatal("manual intake created a run")
	}
	cand := e.tasks("?state=candidate&q=ORQ-1")
	id := cand[0]["id"].(string)
	first := e.f.request("POST", e.orgPath("tasks", id, "evaluate"), nil, map[string]string{"Idempotency-Key": nextKey()})
	if first.StatusCode != 201 || first.Header.Get("Location") == "" {
		t.Fatal(first.StatusCode)
	}
	run := readResponse(t, first, 201)
	if run["state"] != "discovered" || run["origin"] != "manual_evaluate" || run["pinned"].(map[string]any)["packet_cap"] != float64(16384) {
		t.Fatal(run)
	}
	second := e.f.json("POST", e.orgPath("tasks", id, "evaluate"), nil, map[string]string{"Idempotency-Key": nextKey()}, 200)
	if second["id"] != run["id"] || second["deduplicated"] != true {
		t.Fatal(second)
	}
	if e.count("runs", bson.M{"org_id": e.org}) != 1 {
		t.Fatal("exactly one run")
	}
	if got := e.tasks("?state=candidate"); len(got) != 5 {
		t.Fatal("the evaluated issue stays a candidate", len(got))
	}
	expectCode(t, e.f.json("POST", e.orgPath("tasks", "iss_00000000000000000000000000", "evaluate"), nil, map[string]string{"Idempotency-Key": nextKey()}, 404), "not_found")
}

func TestIntakeTwoOrganizationsSameIssueKeys(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{})
	other := e.f.org("beta")
	glB := e.f.providerConn(other, gitlabSpec(e.glS.url()))
	jiB := e.f.providerConn(other, connSpec{Name: "Jira", Provider: "jira", Deployment: "cloud", Base: e.jfS.url() + "/jira", Method: "api_token", Username: e.jf.User, Secret: e.jf.Token})
	pk := repoKey(other, glB, "2")
	now := time.Now().UTC()
	prj := ProjectDoc{ID: newID("prj"), OrgID: other, Name: "b", NameKey: "b", PrimaryRepoKey: &pk, Active: true, AutomationEnabled: true, Revision: 1, CreatedAt: now, UpdatedAt: now,
		Mapping: Mapping{Repos: []RepoMapping{{Role: "primary", ConnectionID: glB, ProviderProjectID: "2", TargetBranch: "main"}}, Jira: &JiraMapping{ConnectionID: jiB, ProjectID: "10000", ProjectKeySnapshot: "ORQ", Board: &JiraBoardRef{ID: "1"}}}}
	if _, err := e.f.db.Coll("projects").InsertOne(context.Background(), prj); err != nil {
		t.Fatal(err)
	}
	e.publishPolicy("/api/v1/orgs/"+other+"/policy", map[string]any{"automation.intake": "auto"})
	e.poll()
	op := e.f.json("POST", "/api/v1/orgs/"+other+"/projects/"+prj.ID+"/intake/poll", nil, map[string]string{"Idempotency-Key": nextKey()}, 202)
	e.f.waitOperation(other, op["id"].(string))
	waitFor(t, "both organizations", func() bool {
		return e.count("runs", bson.M{"org_id": e.org}) == 6 && e.count("runs", bson.M{"org_id": other}) == 6
	})
	a, _ := findOne[RunDoc](context.Background(), e.f.db, "runs", bson.M{"org_id": e.org, "issue_key": "ORQ-1"})
	b, _ := findOne[RunDoc](context.Background(), e.f.db, "runs", bson.M{"org_id": other, "issue_key": "ORQ-1"})
	if a.ID == b.ID || a.IntentKey == b.IntentKey || a.TrackedIssueID == b.TrackedIssueID || a.ConnectionID == b.ConnectionID {
		t.Fatal("the two organizations must not share anything")
	}
	e.f.json("GET", "/api/v1/orgs/"+other+"/tasks/"+a.TrackedIssueID, nil, nil, 404)
	e.f.json("GET", "/api/v1/orgs/"+other+"/runs/"+a.ID, nil, nil, 404)
}

func TestIntakeIssueInTwoProjectsIsAmbiguous(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{})
	pk := repoKey(e.org, e.glID, "2")
	now := time.Now().UTC()
	prj := ProjectDoc{ID: newID("prj"), OrgID: e.org, Name: "b", NameKey: "b", PrimaryRepoKey: &pk, Active: true, AutomationEnabled: true, Revision: 1, CreatedAt: now, UpdatedAt: now,
		Mapping: Mapping{Repos: []RepoMapping{{Role: "primary", ConnectionID: e.glID, ProviderProjectID: "2", TargetBranch: "main"}}, Jira: &JiraMapping{ConnectionID: e.jiID, ProjectID: "10000", ProjectKeySnapshot: "ORQ", Board: &JiraBoardRef{ID: "1"}}}}
	if _, err := e.f.db.Coll("projects").InsertOne(context.Background(), prj); err != nil {
		t.Fatal(err)
	}
	e.poll()
	e.waitRuns(6)
	op := e.f.json("POST", e.path(prj.ID, "intake", "poll"), nil, map[string]string{"Idempotency-Key": nextKey()}, 202)
	e.f.waitOperation(e.org, op["id"].(string))
	waitFor(t, "ambiguity", func() bool { d, _ := e.trackedByKey("ORQ-1"); return len(d.AmbiguousProject) == 2 })
	d, _ := e.trackedByKey("ORQ-1")
	if d.ProjectID != e.pid || !contains(d.AmbiguousProject, e.pid) || !contains(d.AmbiguousProject, prj.ID) {
		t.Fatal(d.ProjectID, d.AmbiguousProject)
	}
	if e.count("runs", bson.M{"org_id": e.org}) != 6 || e.count("runs", bson.M{"org_id": e.org, "project_id": prj.ID}) != 0 {
		t.Fatal("the second project must not create a run")
	}
	task := e.f.json("GET", e.orgPath("tasks", d.ID), nil, nil, 200)
	if len(task["ambiguous_project"].([]any)) != 2 {
		t.Fatal(task)
	}
}

func TestIntakePauseStopsPollingAndEvaluate(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{Scheduler: true, NoEnable: true, Intake: "manual"})
	st := e.f.json("GET", e.orgPath("automation"), nil, nil, 200)
	if st["paused"] != false || st["revision"] != float64(0) {
		t.Fatal(st)
	}
	paused := e.f.json("PUT", e.orgPath("automation"), PutAutomationRequest{Paused: true, Reason: ptr("maintenance")}, map[string]string{"If-Match": "0"}, 200)
	if paused["paused"] != true || paused["revision"] != float64(1) || paused["reason"] != "maintenance" {
		t.Fatal(paused)
	}
	expectCode(t, e.f.json("PUT", e.orgPath("automation"), PutAutomationRequest{Paused: false}, map[string]string{"If-Match": "0"}, 409), "revision_conflict")
	e.enable()
	e.jf.Reset()
	for i := 0; i < 3; i++ {
		e.dueNow()
		time.Sleep(300 * time.Millisecond)
	}
	if n := len(e.jf.Requests()); n != 0 {
		t.Fatal("a paused organization polled:", n)
	}
	expectCode(t, e.f.json("POST", e.path(e.pid, "intake", "poll"), nil, map[string]string{"Idempotency-Key": nextKey()}, 409), "automation_paused")
	// Unpausing lets the scheduler poll again.
	e.f.json("PUT", e.orgPath("automation"), PutAutomationRequest{Paused: false}, map[string]string{"If-Match": "1"}, 200)
	waitFor(t, "polling after the pause", func() bool { return len(e.tasks("?state=candidate")) == 6 })
	id := e.tasks("?q=ORQ-2")[0]["id"].(string)
	e.f.json("PUT", e.orgPath("automation"), PutAutomationRequest{Paused: true}, map[string]string{"If-Match": "2"}, 200)
	expectCode(t, e.f.json("POST", e.orgPath("tasks", id, "evaluate"), nil, map[string]string{"Idempotency-Key": nextKey()}, 409), "automation_paused")
}

func TestIntakeRunPinsPolicyAtCreation(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{PlanApproval: "auto", CodingStart: "auto"})
	e.poll()
	e.waitRuns(6)
	r, _ := e.runOf("ORQ-1")
	run := e.f.json("GET", e.orgPath("runs", r.ID), nil, nil, 200)
	gates := run["gates"].(map[string]any)
	if gates["plan_approval"] != "auto" || gates["coding_start"] != "auto" || run["pinned"].(map[string]any)["host"] != "codex" || run["pinned"].(map[string]any)["profile_version_id"] != e.s.version {
		t.Fatal(run["gates"], run["pinned"])
	}
	e.publishPolicy(e.orgPath("policy"), map[string]any{"automation.intake": "auto", "automation.plan_approval": "manual", "automation.coding_start": "manual"})
	if eff := e.f.json("GET", e.orgPath("policy", "effective"), nil, nil, 200); policyValueOf(eff, "automation.plan_approval") != "manual" {
		t.Fatal("the new policy is not effective")
	}
	again := e.f.json("GET", e.orgPath("runs", r.ID), nil, nil, 200)
	if again["gates"].(map[string]any)["plan_approval"] != "auto" || again["gates"].(map[string]any)["coding_start"] != "auto" {
		t.Fatal("a policy published after run creation changed the run:", again["gates"])
	}
	resp := e.f.request("GET", e.orgPath("runs", r.ID), nil, nil)
	body := readResponse(t, resp, 200)
	if resp.Header.Get("ETag") != fmt.Sprintf(`"%d"`, int(body["revision"].(float64))) {
		t.Fatal(resp.Header.Get("ETag"), body["revision"])
	}
}

func TestIntakeStateAndSummaryEndpoints(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{})
	fresh := e.f.json("GET", e.path(e.pid, "intake"), nil, nil, 200)
	if fresh["enabled"] != true || fresh["paused"] != false || fresh["next_mode"] != "auto" || fresh["last_success_at"] != nil || fresh["waiting_network"] != false {
		t.Fatal(fresh)
	}
	e.poll()
	e.waitRuns(6)
	st := e.f.json("GET", e.path(e.pid, "intake"), nil, nil, 200)
	if st["last_success_at"] == nil || st["next_poll_at"] == nil || st["last_counts"].(map[string]any)["seen"] != float64(6) || fmt.Sprint(st["active_sprints"]) != "[11]" {
		t.Fatal(st)
	}
	sum := e.f.json("GET", e.orgPath("tasks", "summary"), nil, nil, 200)
	total := 0.0
	for _, n := range sum["runs_by_state"].(map[string]any) {
		total += n.(float64)
	}
	if total != 6 || sum["candidates"] != float64(0) || len(sum["intake"].([]any)) != 1 {
		t.Fatal(sum)
	}
	expectCode(t, e.f.json("GET", e.path(e.pid, "intake")+"x", nil, nil, 404), "not_found")
	// A project without a Jira mapping cannot be polled.
	other := e.add("b", "2")["created"].([]any)[0].(map[string]any)["id"].(string)
	expectCode(t, e.f.json("POST", e.path(other, "intake", "poll"), nil, map[string]string{"Idempotency-Key": nextKey()}, 422), "jira_mapping_missing")
}

func runstateAll() []string { return runstate.States }
