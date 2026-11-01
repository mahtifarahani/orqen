package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
)

type policyEnv struct {
	f    *testApp
	org  string
	base string
}

func newPolicyEnv(t *testing.T) *policyEnv {
	f := newTestApp(t, false)
	f.bootstrap()
	org := f.org("alpha")
	return &policyEnv{f: f, org: org, base: "/api/v1/orgs/" + org}
}

var policyKeys int

func (e *policyEnv) publish(base string, revision int, status int) map[string]any {
	policyKeys++
	return e.f.json("POST", base+"/publish", nil, map[string]string{"If-Match": fmt.Sprint(revision), "Idempotency-Key": fmt.Sprintf("pub-%d", policyKeys)}, status)
}
func (e *policyEnv) put(base string, revision int, overrides map[string]any, status int) map[string]any {
	return e.f.json("PUT", base+"/draft", map[string]any{"overrides": overrides}, map[string]string{"If-Match": fmt.Sprint(revision)}, status)
}
func (e *policyEnv) effective(base, key string) map[string]any {
	for _, x := range e.f.json("GET", base+"/effective", nil, nil, 200)["items"].([]any) {
		if x.(map[string]any)["key"] == key {
			return x.(map[string]any)
		}
	}
	e.f.t.Fatal("no such key", key)
	return nil
}
func TestPolicySchemaAndProductDefaults(t *testing.T) {
	e := newPolicyEnv(t)
	resp := e.f.request("GET", e.base+"/policy/schema", nil, nil)
	var schema []map[string]any
	if err := decodeBody(resp, &schema); err != nil || len(schema) != 77 {
		t.Fatal(len(schema), err)
	}
	locked := 0
	for _, k := range schema {
		if k["locked"] == true {
			locked++
		}
		if k["type"] == "enum" && len(k["enum"].([]any)) == 0 || k["type"] == "int" && k["min"] == nil {
			t.Fatal(k)
		}
	}
	if locked != 1 {
		t.Fatal(locked)
	}
	got := e.effective(e.base+"/policy", "automation.intake")
	src := got["source"].(map[string]any)
	if got["value"] != "manual" || src["layer"] != "product" || src["number"] != float64(5) || src["version_id"] != nil {
		t.Fatal(got)
	}
	draft := e.f.json("GET", e.base+"/policy/draft", nil, nil, 200)
	if draft["revision"] != float64(0) || len(draft["overrides"].(map[string]any)) != 0 || draft["differs_from_published"] != false {
		t.Fatal(draft)
	}
}
func TestPolicyLayersAndSources(t *testing.T) {
	e := newPolicyEnv(t)
	prjID := e.addProject(t)
	prj := e.base + "/projects/" + prjID
	pol := e.base + "/policy"
	d := e.put(pol, 0, map[string]any{"automation.intake": "auto", "intake.poll_interval_secs": 60, "concurrency.coding_runs": 3}, 200)
	if d["revision"] != float64(1) || d["differs_from_published"] != true {
		t.Fatal(d)
	}
	// The effective policy ignores unpublished drafts.
	if got := e.effective(pol, "automation.intake"); got["value"] != "manual" {
		t.Fatal(got)
	}
	v1 := e.publish(pol, 1, 201)
	if v1["number"] != float64(1) || v1["kind"] != "org" || v1["scope_id"] != e.org || v1["overrides"].(map[string]any)["intake.poll_interval_secs"] != float64(60) {
		t.Fatal(v1)
	}
	got := e.effective(pol, "automation.intake")
	if got["value"] != "auto" || got["source"].(map[string]any)["layer"] != "org" || got["source"].(map[string]any)["version_id"] != v1["id"] || got["source"].(map[string]any)["number"] != float64(1) {
		t.Fatal(got)
	}
	if e.f.json("GET", pol+"/draft", nil, nil, 200)["differs_from_published"] != false {
		t.Fatal("a freshly published draft must not differ")
	}
	// The project layer wins where it may, and shows where each value comes from.
	pp := prj + "/policy"
	e.put(pp, 0, map[string]any{"automation.intake": "manual", "review.max_fix_rounds": 4}, 200)
	pv := e.publish(pp, 1, 201)
	if pv["kind"] != "project" || pv["scope_id"] != prjID {
		t.Fatal(pv)
	}
	got = e.effective(pp, "automation.intake")
	if got["value"] != "manual" || got["source"].(map[string]any)["layer"] != "project" || got["source"].(map[string]any)["version_id"] != pv["id"] {
		t.Fatal(got)
	}
	if got = e.effective(pp, "intake.poll_interval_secs"); got["value"] != float64(60) || got["source"].(map[string]any)["layer"] != "org" {
		t.Fatal(got)
	}
	if got = e.effective(pp, "concurrency.coding_runs"); got["value"] != float64(3) || got["source"].(map[string]any)["layer"] != "org" {
		t.Fatal(got)
	}
	if got = e.effective(pol, "review.max_fix_rounds"); got["value"] != float64(2) || got["source"].(map[string]any)["layer"] != "product" {
		t.Fatal("the org view does not include project overrides", got)
	}
	// Locked and org-only keys are refused at the layers that may not set them.
	expectCode(t, e.put(pol, 1, map[string]any{"review.require_different_model": false}, 422), "validation_failed")
	bad := e.put(pp, 1, map[string]any{"review.require_different_model": false, "concurrency.coding_runs": 2, "nope": 1}, 422)
	msgs := map[string]string{}
	for _, x := range bad["errors"].([]any) {
		m := x.(map[string]any)
		msgs[m["field"].(string)] = m["message"].(string)
	}
	if msgs["overrides.review.require_different_model"] != "locked" || msgs["overrides.concurrency.coding_runs"] != "org only" || msgs["overrides.nope"] != "unknown setting" {
		t.Fatal(msgs)
	}
	expectCode(t, e.put(pol, 1, map[string]any{"intake.poll_interval_secs": 5}, 422), "validation_failed")
	expectCode(t, e.put(pol, 1, map[string]any{"intake.poll_interval_secs": 1.5}, 422), "validation_failed")
}
func (e *policyEnv) addProject(t *testing.T) string {
	t.Helper()
	// A minimal project document is enough for the policy routes.
	id := newID("prj")
	if _, err := e.f.db.Coll("projects").InsertOne(t.Context(), ProjectDoc{ID: id, OrgID: e.org, Name: "p", NameKey: "p", Active: true, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	return id
}
func TestPolicyVersionsAreImmutable(t *testing.T) {
	e := newPolicyEnv(t)
	pol := e.base + "/policy"
	e.put(pol, 0, map[string]any{"automation.intake": "auto"}, 200)
	v1 := e.publish(pol, 1, 201)
	before, _ := json.Marshal(e.f.json("GET", pol+"/versions/"+v1["id"].(string), nil, nil, 200))
	e.put(pol, 2, map[string]any{"automation.intake": "manual", "ci.require_pipeline": false}, 200)
	v2 := e.publish(pol, 3, 201)
	if v2["number"] != float64(2) || v2["id"] == v1["id"] {
		t.Fatal(v2)
	}
	after, _ := json.Marshal(e.f.json("GET", pol+"/versions/"+v1["id"].(string), nil, nil, 200))
	if string(before) != string(after) {
		t.Fatal("version 1 changed", string(before), string(after))
	}
	if got := e.effective(pol, "automation.intake"); got["value"] != "manual" || got["source"].(map[string]any)["number"] != float64(2) {
		t.Fatal(got)
	}
	list := e.f.json("GET", pol+"/versions?limit=1", nil, nil, 200)
	items := list["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["number"] != float64(2) || list["next_cursor"] == nil {
		t.Fatal(list)
	}
	next := e.f.json("GET", pol+"/versions?limit=1&cursor="+list["next_cursor"].(string), nil, nil, 200)
	if next["items"].([]any)[0].(map[string]any)["number"] != float64(1) || next["next_cursor"] != nil {
		t.Fatal(next)
	}
	e.f.json("GET", pol+"/versions/"+newID("pol"), nil, nil, 404)
	e.f.json("GET", pol+"/versions/nope", nil, nil, 404)
}
func TestPolicyRevisionsAndUseDefaults(t *testing.T) {
	e := newPolicyEnv(t)
	pol := e.base + "/policy"
	// "Use product defaults" publishes version 1 with no overrides from a synthesized draft.
	v := e.publish(pol, 0, 201)
	if v["number"] != float64(1) || len(v["overrides"].(map[string]any)) != 0 {
		t.Fatal(v)
	}
	draft := e.f.json("GET", pol+"/draft", nil, nil, 200)
	if draft["revision"] != float64(1) || draft["base_version_id"] != v["id"] {
		t.Fatal(draft)
	}
	conflict := e.put(pol, 0, map[string]any{}, 409)
	expectCode(t, conflict, "revision_conflict")
	if conflict["current"].(map[string]any)["revision"] != float64(1) {
		t.Fatal(conflict)
	}
	expectCode(t, e.put(pol, 7, map[string]any{}, 409), "revision_conflict")
	expectCode(t, e.publish(pol, 9, 409), "revision_conflict")
	e.f.json("PUT", pol+"/draft", map[string]any{"overrides": map[string]any{}}, nil, 428)
	e.f.json("POST", pol+"/publish", nil, map[string]string{"If-Match": "1"}, 422)
	// A replay with the same key returns the stored result and creates no new version.
	first := e.f.json("POST", pol+"/publish", nil, map[string]string{"If-Match": "1", "Idempotency-Key": "same"}, 201)
	second := e.f.json("POST", pol+"/publish", nil, map[string]string{"If-Match": "1", "Idempotency-Key": "same"}, 201)
	if !reflect.DeepEqual(first, second) || first["number"] != float64(2) {
		t.Fatal(first, second)
	}
}
func TestPolicyIsolationBetweenOrganizations(t *testing.T) {
	e := newPolicyEnv(t)
	other := e.f.org("beta")
	pol := e.base + "/policy"
	e.put(pol, 0, map[string]any{"automation.intake": "auto"}, 200)
	v := e.publish(pol, 1, 201)
	otherPol := "/api/v1/orgs/" + other + "/policy"
	if got := e.effective(otherPol, "automation.intake"); got["value"] != "manual" || got["source"].(map[string]any)["layer"] != "product" {
		t.Fatal("another organization sees our policy", got)
	}
	e.f.json("GET", otherPol+"/versions/"+v["id"].(string), nil, nil, 404)
	if list := e.f.json("GET", otherPol+"/versions", nil, nil, 200); len(list["items"].([]any)) != 0 {
		t.Fatal(list)
	}
	// A project of another organization has no policy endpoints here.
	prj := e.addProject(t)
	e.f.json("GET", otherPol[:len(otherPol)-len("/policy")]+"/projects/"+prj+"/policy/draft", nil, nil, 404)
	// Drafts are separate documents per organization.
	if d := e.f.json("GET", otherPol+"/draft", nil, nil, 200); d["revision"] != float64(0) {
		t.Fatal(d)
	}
}
func TestPolicyConcurrentPublishesNumberWithoutGaps(t *testing.T) {
	e := newPolicyEnv(t)
	pol := e.base + "/policy"
	e.put(pol, 0, map[string]any{"automation.intake": "auto"}, 200)
	const n = 10
	var wg sync.WaitGroup
	numbers := make(chan int, n)
	var mu sync.Mutex
	conflicts := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for attempt := 0; attempt < 200; attempt++ {
				draft := e.f.json("GET", pol+"/draft", nil, nil, 200)
				resp := e.f.request("POST", pol+"/publish", nil, map[string]string{"If-Match": fmt.Sprint(int(draft["revision"].(float64))), "Idempotency-Key": fmt.Sprintf("race-%d-%d", i, attempt)})
				if resp.StatusCode == 409 {
					resp.Body.Close()
					mu.Lock()
					conflicts++
					mu.Unlock()
					continue
				}
				var v map[string]any
				if err := decodeBody(resp, &v); err != nil || resp.StatusCode != 201 {
					t.Error(resp.StatusCode, err)
					return
				}
				numbers <- int(v["number"].(float64))
				return
			}
			t.Error("publish never succeeded")
		}(i)
	}
	wg.Wait()
	close(numbers)
	got := []int{}
	for x := range numbers {
		got = append(got, x)
	}
	sort.Ints(got)
	for i, x := range got {
		if x != i+1 {
			t.Fatal("numbers must be 1..10 with no gaps", got)
		}
	}
	if len(got) != n {
		t.Fatal(got)
	}
	t.Logf("%d conflicts retried", conflicts)
}
