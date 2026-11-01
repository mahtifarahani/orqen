package server

import (
	"encoding/json"
	"fmt"
	"orqen/internal/catherd"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
)

var opKey atomic.Int64

func nextKey() string { return fmt.Sprintf("k-%d", opKey.Add(1)) }

func goodProfileDoc() map[string]any {
	doc := catherd.BlankDoc()
	roles := doc["roles"].(map[string]any)
	roles["architect"].(map[string]any)["rungs"] = []any{"codex:gpt-6.1-sol#high"}
	roles["verifier"].(map[string]any)["rungs"] = []any{"codex:gpt-6.1-sol#low"}
	roles["reviewer"].(map[string]any)["rungs"] = []any{"codex:gpt-6.1-sol#high"}
	doc["failover"] = map[string]any{}
	doc["budget"] = map[string]any{"usd": json.Number("5")}
	doc["objective"] = "speed"
	doc["jev"] = map[string]any{"use": "off"}
	return doc
}
func clone(m map[string]any) map[string]any { return catherd.Clone(m).(map[string]any) }

type profileEnv struct {
	*catherdEnv
}

func newProfileEnv(t *testing.T) *profileEnv {
	e := newCatherdEnv(t, "1.5.0")
	e.post("/catalog/refresh", map[string]any{"mode": "list"})
	return &profileEnv{e}
}
func (e *profileEnv) base() string { return "/api/v1/orgs/" + e.org + "/model-profiles" }
func (e *profileEnv) create(slug string, doc map[string]any) string {
	id := e.f.json("POST", e.base(), map[string]any{"slug": slug, "name": "Profile " + slug}, nil, 201)["id"].(string)
	if doc != nil {
		e.putDraft(id, doc, 200)
	}
	return id
}
func (e *profileEnv) rev(id string) string {
	return fmt.Sprint(int(e.f.json("GET", e.base()+"/"+id, nil, nil, 200)["revision"].(float64)))
}
func (e *profileEnv) putDraft(id string, doc map[string]any, status int) map[string]any {
	return e.f.json("PUT", e.base()+"/"+id+"/draft", map[string]any{"doc": doc, "meta": map[string]any{"budget_uncapped_ack": false}}, map[string]string{"If-Match": e.rev(id)}, status)
}
func (e *profileEnv) op(method, path string, body any, headers map[string]string, status int) map[string]any {
	if headers == nil {
		headers = map[string]string{}
	}
	headers["Idempotency-Key"] = nextKey()
	res := e.f.json(method, path, body, headers, status)
	if status != 202 {
		return res
	}
	return e.f.waitOperation(e.org, res["id"].(string))
}
func (e *profileEnv) validate(id string) map[string]any {
	return e.op("POST", e.base()+"/"+id+"/validate", nil, nil, 202)
}
func (e *profileEnv) publish(id string) map[string]any {
	return e.op("POST", e.base()+"/"+id+"/publish", nil, map[string]string{"If-Match": e.rev(id)}, 202)
}
func (e *profileEnv) home() string {
	return catherd.NewOrgEnv(e.f.a.Config.DataDir, e.org).Home()
}
func (e *profileEnv) version(id string) map[string]any {
	return e.f.json("GET", "/api/v1/orgs/"+e.org+"/model-profile-versions/"+id, nil, nil, 200)
}
func issueCodes(list any) []string {
	var out []string
	for _, x := range list.([]any) {
		out = append(out, x.(map[string]any)["code"].(string))
	}
	sort.Strings(out)
	return out
}
func TestModelProfilesDraftValidatePublish(t *testing.T) {
	e := newProfileEnv(t)
	id := e.create("main", goodProfileDoc())
	p := e.f.json("GET", e.base()+"/"+id, nil, nil, 200)
	if p["source"].(map[string]any)["kind"] != "blank" || p["last_validation"] != nil || p["draft_hash"] == "" {
		t.Fatal(p)
	}
	// Publishing needs a fresh, valid validation.
	expectCode(t, e.f.json("POST", e.base()+"/"+id+"/publish", nil, map[string]string{"If-Match": e.rev(id), "Idempotency-Key": nextKey()}, 409), "profile_not_validated")
	op := e.validate(id)
	if op["status"] != "succeeded" || op["result"].(map[string]any)["valid"] != true {
		t.Fatal(op)
	}
	p = e.f.json("GET", e.base()+"/"+id, nil, nil, 200)
	lv := p["last_validation"].(map[string]any)
	if lv["valid"] != true || lv["draft_hash"] != p["draft_hash"] || lv["independence"].(map[string]any)["status"] != "independent" || lv["catherd_version"] != "1.5.0" {
		t.Fatal(lv)
	}
	if names := e.fake.Names(e.home()); len(names) != 0 {
		t.Fatal("staging profiles must be removed:", names)
	}
	pub := e.publish(id)
	if pub["status"] != "succeeded" {
		t.Fatal(pub)
	}
	res := pub["result"].(map[string]any)
	if res["catherd_name"] != "o-main-v1" {
		t.Fatal(res)
	}
	if names := e.fake.Names(e.home()); len(names) != 1 || names[0] != "o-main-v1" {
		t.Fatal(names)
	}
	v1 := e.version(res["version_id"].(string))
	if v1["state"] != "published" || v1["number"] != float64(1) || v1["resolved"] == nil || v1["host"] != "codex" || v1["source"].(map[string]any)["kind"] != "draft" || v1["validation"].(map[string]any)["valid"] != true {
		t.Fatal(v1)
	}
	// The catherd profile holds exactly the draft's document (plus its name).
	stored := e.fake.Doc(e.home(), "o-main-v1")
	delete(stored, "name")
	want := goodProfileDoc()
	if canonical(stored) != canonical(want) {
		t.Fatalf("catherd stores\n%s\nwanted\n%s", canonical(stored), canonical(want))
	}
	if p = e.f.json("GET", e.base()+"/"+id, nil, nil, 200); p["latest_version_id"] != res["version_id"] {
		t.Fatal(p)
	}
	if st := e.f.json("GET", e.path("/status"), nil, nil, 200); !contains(anyStrings(st["inventory"].(map[string]any)["names"]), "o-main-v1") {
		t.Fatal(st["inventory"])
	}
	// Editing the draft makes the validation stale; v2 leaves v1 untouched.
	v1Before, _ := json.Marshal(v1)
	changed := clone(goodProfileDoc())
	changed["budget"] = map[string]any{"usd": 9}
	changed["notify"] = []any{"finish"}
	e.putDraft(id, changed, 200)
	expectCode(t, e.f.json("POST", e.base()+"/"+id+"/publish", nil, map[string]string{"If-Match": e.rev(id), "Idempotency-Key": nextKey()}, 409), "profile_not_validated")
	diff := e.f.json("GET", e.base()+"/"+id+"/diff", nil, nil, 200)
	paths := map[string]string{}
	for _, c := range diff["changes"].([]any) {
		m := c.(map[string]any)
		paths[m["path_display"].(string)] = m["kind"].(string)
	}
	if diff["from"] != "latest" || diff["to"] != "draft" || paths["budget.usd"] != "changed" || paths["notify"] != "changed" || len(paths) != 2 {
		t.Fatal(diff)
	}
	e.validate(id)
	pub2 := e.publish(id)
	if pub2["status"] != "succeeded" || pub2["result"].(map[string]any)["catherd_name"] != "o-main-v2" {
		t.Fatal(pub2)
	}
	v1After, _ := json.Marshal(e.version(res["version_id"].(string)))
	if string(v1Before) != string(v1After) {
		t.Fatal("version 1 changed")
	}
	// Rollback publishes the old document as version 3 and resets the draft to it.
	rb := e.op("POST", e.base()+"/"+id+"/rollback", map[string]any{"version_id": res["version_id"]}, map[string]string{"If-Match": e.rev(id)}, 202)
	if rb["status"] != "succeeded" || rb["result"].(map[string]any)["catherd_name"] != "o-main-v3" {
		t.Fatal(rb)
	}
	v3 := e.version(rb["result"].(map[string]any)["version_id"].(string))
	if v3["source"].(map[string]any)["kind"] != "rollback" || v3["source"].(map[string]any)["from_version_id"] != res["version_id"] || canonical(v3["doc"]) != canonical(v1["doc"]) || v3["doc_hash"] != v1["doc_hash"] {
		t.Fatal(v3)
	}
	if p = e.f.json("GET", e.base()+"/"+id, nil, nil, 200); canonical(p["draft"].(map[string]any)["doc"]) != canonical(v1["doc"]) {
		t.Fatal("the draft must become the rolled-back document")
	}
	versions := e.f.json("GET", e.base()+"/"+id+"/versions?limit=2", nil, nil, 200)
	items := versions["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["number"] != float64(3) || versions["next_cursor"] == nil {
		t.Fatal(versions)
	}
	expectCode(t, e.f.json("POST", e.base()+"/"+id+"/rollback", map[string]any{"version_id": newID("mpv")}, map[string]string{"If-Match": e.rev(id), "Idempotency-Key": nextKey()}, 422), "version_not_published")
	// The publish never activated or bound anything in catherd.
	for _, c := range e.fake.Calls() {
		if c.Op == "use" || c.Home == "personal" {
			t.Fatal(c)
		}
	}
}
func canonical(v any) string { return canonicalJSON(v) }
func TestModelProfilesIssues(t *testing.T) {
	e := newProfileEnv(t)
	issues := func(id string) ([]string, map[string]any) {
		r := e.f.json("GET", e.base()+"/"+id+"/draft/issues", nil, nil, 200)
		return issueCodes(r["issues"]), r["independence"].(map[string]any)
	}
	doc := goodProfileDoc()
	doc["surprise"] = true
	doc["roles"].(map[string]any)["writer"].(map[string]any)["rungs"] = []any{"codex:gpt-6-sool#high"}
	id := e.create("bad", doc)
	codes, _ := issues(id)
	if !contains(codes, "unsupported_field") || !contains(codes, "rung_not_in_catalog") {
		t.Fatal(codes)
	}
	op := e.validate(id)
	if op["result"].(map[string]any)["valid"] != false {
		t.Fatal(op)
	}
	lv := e.f.json("GET", e.base()+"/"+id, nil, nil, 200)["last_validation"].(map[string]any)
	if !contains(issueCodes(lv["orqen_issues"]), "unsupported_field") {
		t.Fatal(lv)
	}
	// An unsettable draft never reaches catherd, and it cannot be published.
	if n := len(e.fake.CallsOf("create")); n != 0 {
		t.Fatal("catherd was called for an unsettable draft", n)
	}
	expectCode(t, e.f.json("POST", e.base()+"/"+id+"/publish", nil, map[string]string{"If-Match": e.rev(id), "Idempotency-Key": nextKey()}, 409), "profile_not_validated")
	// No substitution: the wrong model name is an error, not a different model.
	doc = goodProfileDoc()
	doc["roles"].(map[string]any)["worker"].(map[string]any)["rungs"] = []any{"codex:gpt-6-sool#high"}
	doc["roles"].(map[string]any)["worker"].(map[string]any)["defaultRung"] = "codex:gpt-6-sool#high"
	id2 := e.create("typo", doc)
	if codes, _ = issues(id2); !contains(codes, "rung_not_in_catalog") {
		t.Fatal(codes)
	}
	// Same model through another backend name is not independent.
	doc = goodProfileDoc()
	doc["roles"].(map[string]any)["reviewer"].(map[string]any)["rungs"] = []any{"codex:gpt-6-sol#high"}
	id3 := e.create("same", doc)
	codes, ind := issues(id3)
	if !contains(codes, "reviewer_not_independent") || ind["status"] != "same_model" || ind["shared"].([]any)[0] != "gpt-6-sol" {
		t.Fatal(codes, ind)
	}
	if op = e.validate(id3); op["result"].(map[string]any)["valid"] != false {
		t.Fatal("same_model must block publishing", op)
	}
	// A reviewer rung missing from the catalog makes independence unknown, which is a warning here.
	doc = goodProfileDoc()
	doc["roles"].(map[string]any)["reviewer"].(map[string]any)["rungs"] = []any{"codex:gpt-6.1-sol#medium", "codex:gpt-6-nope#high"}
	id4 := e.create("unk", doc)
	codes, ind = issues(id4)
	if ind["status"] != "unknown" || !contains(codes, "independence_unknown") || !contains(codes, "rung_not_in_catalog") {
		t.Fatal(codes, ind)
	}
	// Defaults: roles need explicit rungs, and the budget choice is a warning.
	id5 := e.create("blank", nil)
	codes, _ = issues(id5)
	if !contains(codes, "rungs_required") || !contains(codes, "budget_not_chosen") {
		t.Fatal(codes)
	}
	big := map[string]any{"x": strings.Repeat("a", 70<<10)}
	expectCode(t, e.putDraft(id5, big, 422), "validation_failed")
	e.f.json("PUT", e.base()+"/"+id5+"/draft", map[string]any{"doc": goodProfileDoc()}, nil, 428)
}
func TestModelProfilesCrashRecovery(t *testing.T) {
	e := newProfileEnv(t)
	id := e.create("main", goodProfileDoc())
	e.validate(id)
	e.fake.CrashAfterSets(3)
	pub := e.publish(id)
	if pub["status"] != "succeeded" {
		t.Fatal(pub)
	}
	if names := e.fake.Names(e.home()); len(names) != 1 || names[0] != "o-main-v1" {
		t.Fatal("exactly one catherd profile must remain:", names)
	}
	job, err := findOne[JobDoc](t.Context(), e.f.db, "jobs", map[string]any{"kind": "profile_publish"})
	if err != nil || job.Attempts != 2 {
		t.Fatal(job.Attempts, err)
	}
	stored := e.fake.Doc(e.home(), "o-main-v1")
	delete(stored, "name")
	if canonical(stored) != canonical(goodProfileDoc()) {
		t.Fatal("the rebuilt profile differs")
	}
}
func TestModelProfilesFinalizeAfterCatherdFinished(t *testing.T) {
	e := newProfileEnv(t)
	var crashed atomic.Bool
	e.f.a.profileHook = func(stage string) error {
		if stage == "built" && crashed.CompareAndSwap(false, true) {
			return fmt.Errorf("injected crash before the finalize")
		}
		return nil
	}
	id := e.create("main", goodProfileDoc())
	e.validate(id)
	creates := len(e.fake.CallsOf("create"))
	pub := e.publish(id)
	if pub["status"] != "succeeded" || !crashed.Load() {
		t.Fatal(pub)
	}
	if got := len(e.fake.CallsOf("create")) - creates; got != 1 {
		t.Fatalf("the retry must reuse the finished catherd profile, create was called %d times", got)
	}
	if names := e.fake.Names(e.home()); len(names) != 1 {
		t.Fatal(names)
	}
}
func TestModelProfilesOrphanSweep(t *testing.T) {
	e := newProfileEnv(t)
	e.fake.Put(e.home(), "s-leftover", map[string]any{"schema": json.Number("1")})
	e.fake.Put(e.home(), "o-ghost-v1", map[string]any{"schema": json.Number("1")})
	id := e.create("main", goodProfileDoc())
	e.validate(id)
	e.publish(id)
	if names := e.fake.Names(e.home()); len(names) != 1 || names[0] != "o-main-v1" {
		t.Fatal("orphans must be swept:", names)
	}
}
func TestModelProfilesFailuresAreResults(t *testing.T) {
	e := newProfileEnv(t)
	id := e.create("main", goodProfileDoc())
	e.validate(id)
	e.fake.FailNext("validate", &catherd.Error{Code: "E_BOOM", Message: "catherd exploded"})
	pub := e.publish(id)
	if pub["status"] != "failed" || pub["problem"].(map[string]any)["code"] != "catherd_failed" {
		t.Fatal(pub)
	}
	versions := e.f.json("GET", e.base()+"/"+id+"/versions", nil, nil, 200)["items"].([]any)
	if len(versions) != 1 || versions[0].(map[string]any)["state"] != "failed" || versions[0].(map[string]any)["failure"].(map[string]any)["code"] != "catherd_failed" {
		t.Fatal(versions)
	}
	if names := e.fake.Names(e.home()); len(names) != 0 {
		t.Fatal("a failed publish must leave no o-* profile:", names)
	}
	// The failed number is consumed; the next publish is version 2.
	pub = e.publish(id)
	if pub["status"] != "succeeded" || pub["result"].(map[string]any)["catherd_name"] != "o-main-v2" {
		t.Fatal(pub)
	}
}
func TestModelProfilesUnsupportedCatherd(t *testing.T) {
	e := newCatherdEnv(t, "1.6.0")
	id := e.f.json("POST", "/api/v1/orgs/"+e.org+"/model-profiles", map[string]any{"slug": "x", "name": "X"}, nil, 201)["id"].(string)
	base := "/api/v1/orgs/" + e.org + "/model-profiles/" + id
	expectCode(t, e.f.json("POST", base+"/validate", nil, map[string]string{"Idempotency-Key": nextKey()}, 503), "catherd_version_unsupported")
	expectCode(t, e.f.json("POST", base+"/publish", nil, map[string]string{"If-Match": "1", "Idempotency-Key": nextKey()}, 503), "catherd_version_unsupported")
}
func TestModelProfilesOrganizationsAreIsolated(t *testing.T) {
	e := newProfileEnv(t)
	other := e.f.org("beta")
	otherBase := "/api/v1/orgs/" + other + "/model-profiles"
	idA := e.create("main", goodProfileDoc())
	idB := e.f.json("POST", otherBase, map[string]any{"slug": "main", "name": "Beta main"}, nil, 201)["id"].(string)
	e.validate(idA)
	pubA := e.publish(idA)
	e.f.json("GET", otherBase+"/"+idA, nil, nil, 404)
	e.f.json("GET", e.base()+"/"+idB, nil, nil, 404)
	e.f.json("GET", "/api/v1/orgs/"+other+"/model-profile-versions/"+pubA["result"].(map[string]any)["version_id"].(string), nil, nil, 404)
	e.f.json("PATCH", otherBase+"/"+idA, map[string]any{"name": "x"}, map[string]string{"If-Match": "1"}, 404)
	if names := e.fake.Names(catherd.NewOrgEnv(e.f.a.Config.DataDir, other).Home()); len(names) != 0 {
		t.Fatal("organization B's home must stay empty:", names)
	}
	// Same slug in two organizations: distinct profiles, distinct homes.
	expectCode(t, e.f.json("POST", e.base(), map[string]any{"slug": "main", "name": "dup"}, nil, 422), "validation_failed")
	list := e.f.json("GET", otherBase, nil, nil, 200)["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != idB {
		t.Fatal(list)
	}
}
func TestModelProfilesCrudAndClone(t *testing.T) {
	e := newProfileEnv(t)
	id := e.create("main", goodProfileDoc())
	expectCode(t, e.f.json("POST", e.base(), map[string]any{"slug": "Bad Slug", "name": "x"}, nil, 422), "validation_failed")
	patched := e.f.json("PATCH", e.base()+"/"+id, map[string]any{"name": " Renamed ", "description": "d"}, map[string]string{"If-Match": e.rev(id)}, 200)
	if patched["name"] != "Renamed" || patched["description"] != "d" {
		t.Fatal(patched)
	}
	expectCode(t, e.f.json("PATCH", e.base()+"/"+id, map[string]any{"name": "stale"}, map[string]string{"If-Match": "1"}, 409), "revision_conflict")
	e.validate(id)
	pub := e.publish(id)
	vid := pub["result"].(map[string]any)["version_id"].(string)
	clone := e.f.json("POST", e.base(), map[string]any{"slug": "copy", "name": "Copy", "from": map[string]any{"kind": "version", "version_id": vid}}, nil, 201)
	if clone["source"].(map[string]any)["kind"] != "clone" || canonical(clone["draft"].(map[string]any)["doc"]) != canonical(e.version(vid)["doc"]) {
		t.Fatal(clone)
	}
	expectCode(t, e.f.json("POST", e.base(), map[string]any{"slug": "c2", "name": "x", "from": map[string]any{"kind": "version", "version_id": newID("mpv")}}, nil, 422), "version_not_published")
	// A profile bound to an active project cannot be archived.
	prj := newID("prj")
	if _, err := e.f.db.Coll("projects").InsertOne(t.Context(), map[string]any{"_id": prj, "org_id": e.org, "active": true, "profile_binding": map[string]any{"profile_id": id}}); err != nil {
		t.Fatal(err)
	}
	expectCode(t, e.f.json("DELETE", e.base()+"/"+id, nil, map[string]string{"If-Match": e.rev(id)}, 409), "profile_in_use")
	if _, err := e.f.db.Coll("projects").UpdateOne(t.Context(), map[string]any{"_id": prj}, map[string]any{"$set": map[string]any{"active": false}}); err != nil {
		t.Fatal(err)
	}
	e.f.json("DELETE", e.base()+"/"+id, nil, map[string]string{"If-Match": e.rev(id)}, 204)
	e.f.json("GET", e.base()+"/"+id, nil, nil, 404)
	// The archived profile's catherd names stay reserved, so its slug cannot be reused.
	expectCode(t, e.f.json("POST", e.base(), map[string]any{"slug": "main", "name": "again"}, nil, 422), "validation_failed")
}
func TestModelProfilesConcurrentPublishesNumberWithoutGaps(t *testing.T) {
	e := newProfileEnv(t)
	id := e.create("main", goodProfileDoc())
	e.validate(id)
	// Ten publishes race on the same revision: one wins, the others get a conflict or an in-flight refusal.
	type result struct{ status int }
	results := make(chan int, 10)
	rev := e.rev(id)
	for i := 0; i < 10; i++ {
		go func() {
			resp := e.f.request("POST", e.base()+"/"+id+"/publish", nil, map[string]string{"If-Match": rev, "Idempotency-Key": nextKey()})
			resp.Body.Close()
			results <- resp.StatusCode
		}()
	}
	won := 0
	for i := 0; i < 10; i++ {
		switch s := <-results; s {
		case 202:
			won++
		case 409:
		default:
			t.Fatal("status", s)
		}
	}
	if won != 1 {
		t.Fatal("exactly one publish may win the revision, won:", won)
	}
	// Sequential publishes then number 2..11 without gaps.
	numbers := []int{}
	for i := 0; i < 10; i++ {
		for {
			e.f.waitOperationDone(e.org)
			resp := e.f.request("POST", e.base()+"/"+id+"/publish", nil, map[string]string{"If-Match": e.rev(id), "Idempotency-Key": nextKey()})
			var op map[string]any
			if err := decodeBody(resp, &op); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode == 202 {
				done := e.f.waitOperation(e.org, op["id"].(string))
				numbers = append(numbers, int(e.version(done["result"].(map[string]any)["version_id"].(string))["number"].(float64)))
				break
			}
			if resp.StatusCode == 409 {
				continue
			}
			t.Fatal(resp.StatusCode, op)
		}
	}
	for i, n := range numbers {
		if n != i+2 {
			t.Fatal("numbers must follow without gaps:", numbers)
		}
	}
}
func TestModelProfilesImportPersonalIsReadOnly(t *testing.T) {
	e := newProfileEnv(t)
	personal := goodProfileDoc()
	personal["name"] = "mine"
	personal["schema"] = json.Number("1")
	e.fake.Personal["mine"] = personal
	op := e.op("POST", e.base()+"/import-personal", map[string]any{"personal_name": "mine", "slug": "imp", "name": "Imported"}, nil, 202)
	if op["status"] != "succeeded" {
		t.Fatal(op)
	}
	id := op["result"].(map[string]any)["profile_id"].(string)
	p := e.f.json("GET", e.base()+"/"+id, nil, nil, 200)
	want := clone(personal)
	delete(want, "name")
	if canonical(p["draft"].(map[string]any)["doc"]) != canonical(want) || p["source"].(map[string]any)["kind"] != "import_personal" || p["source"].(map[string]any)["ref"] != "mine" || p["last_validation"] != nil {
		t.Fatal(p)
	}
	for _, c := range e.fake.Calls() {
		if c.Home == "personal" && c.Op != "list" && c.Op != "show_raw" {
			t.Fatal("write against the personal state:", c)
		}
	}
	missing := e.op("POST", e.base()+"/import-personal", map[string]any{"personal_name": "nope", "slug": "imp2", "name": "X"}, nil, 202)
	if missing["status"] != "failed" || missing["problem"].(map[string]any)["code"] != "personal_profile_not_found" {
		t.Fatal(missing)
	}
	taken := e.op("POST", e.base()+"/import-personal", map[string]any{"personal_name": "mine", "slug": "imp", "name": "Again"}, nil, 202)
	if taken["status"] != "failed" || taken["problem"].(map[string]any)["code"] != "slug_taken" {
		t.Fatal(taken)
	}
	expectCode(t, e.f.json("POST", e.base()+"/import-personal", map[string]any{"personal_name": "Bad Name", "slug": "Bad", "name": ""}, map[string]string{"Idempotency-Key": nextKey()}, 422), "validation_failed")
	// An imported draft is unvalidated: it cannot be published until validated.
	expectCode(t, e.f.json("POST", e.base()+"/"+id+"/publish", nil, map[string]string{"If-Match": e.rev(id), "Idempotency-Key": nextKey()}, 409), "profile_not_validated")
}
