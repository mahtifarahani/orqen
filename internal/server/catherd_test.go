package server

import (
	"context"
	"orqen/internal/catherd"
	"testing"
)

type catherdEnv struct {
	f    *testApp
	fake *catherd.Fake
	org  string
}

func fakeRuntime(version string) *catherd.Runtime {
	rt := catherd.NewRuntime(context.Background(), catherd.RuntimeConfig{Bin: "/fake/catherd", Path: "/fake/bin", UserHome: "/home/fake", Getenv: func(string) string { return "" }})
	if version != "" {
		rt.SetVersion(version)
	}
	return rt
}
func newCatherdEnv(t *testing.T, version string) *catherdEnv {
	t.Helper()
	fake := catherd.NewFake()
	f := newTestAppWith(t, true, func(a *App) { a.SetCatherd(fake, fakeRuntime(version)) })
	f.bootstrap()
	return &catherdEnv{f: f, fake: fake, org: f.org("alpha")}
}
func (e *catherdEnv) path(p string) string { return "/api/v1/orgs/" + e.org + "/catherd" + p }
func (e *catherdEnv) post(path string, body any) map[string]any {
	testKeyCounter++
	op := e.f.json("POST", e.path(path), body, map[string]string{"Idempotency-Key": "ch-" + string(rune('a'+testKeyCounter%26)) + string(rune('A'+testKeyCounter/26%26)) + string(rune('0'+testKeyCounter%10))}, 202)
	return e.f.waitOperation(e.org, op["id"].(string))
}
func TestCatherdDoctorStoresSummaryAndRefreshesCatalog(t *testing.T) {
	e := newCatherdEnv(t, "1.5.0")
	st := e.f.json("GET", e.path("/status"), nil, nil, 200)
	if st["available"] != true || st["supported"] != true || st["version"] != "1.5.0" || st["last_doctor"] != nil || st["catalog"] != nil {
		t.Fatal(st)
	}
	op := e.post("/doctor", nil)
	if op["status"] != "succeeded" || op["result"].(map[string]any)["ready"] != true {
		t.Fatal(op)
	}
	st = e.f.json("GET", e.path("/status"), nil, nil, 200)
	d := st["last_doctor"].(map[string]any)
	if d["ready"] != true || len(d["checks"].([]any)) != 3 || d["artifact_id"] == "" {
		t.Fatal(st)
	}
	if st["catalog"].(map[string]any)["model_count"] != float64(8) || st["catalog"].(map[string]any)["mode"] != "doctor" {
		t.Fatal("doctor must refresh the catalog snapshot", st["catalog"])
	}
	if names := st["inventory"].(map[string]any)["names"].([]any); len(names) != 1 || names[0] != "default" {
		t.Fatal(st["inventory"])
	}
	health := e.f.json("GET", "/api/v1/orgs/"+e.org+"/health", nil, nil, 200)
	if health["catherd"].(map[string]any)["last_doctor"] == nil || health["mongo"].(map[string]any)["ok"] != true || health["runner"].(map[string]any)["alive"] != true {
		t.Fatal(health)
	}
	report := e.f.json("GET", "/api/v1/orgs/"+e.org+"/artifacts/"+d["artifact_id"].(string)+"/content", nil, nil, 200)
	if report["ready"] != true {
		t.Fatal(report)
	}
	// Every call went to this organization's own home, never a shared or personal one.
	for _, c := range e.fake.Calls() {
		if c.Home == "" && c.Op != "version" || c.Home == "personal" {
			t.Fatal("call outside the organization home", c)
		}
	}
}
func TestCatherdTwoOrganizationsGetTwoHomes(t *testing.T) {
	e := newCatherdEnv(t, "1.5.0")
	other := e.f.org("beta")
	e.post("/doctor", nil)
	testKeyCounter++
	op := e.f.json("POST", "/api/v1/orgs/"+other+"/catherd/doctor", nil, map[string]string{"Idempotency-Key": "other-doctor"}, 202)
	e.f.waitOperation(other, op["id"].(string))
	homes := map[string]bool{}
	for _, c := range e.fake.CallsOf("doctor") {
		homes[c.Home] = true
	}
	if len(homes) != 2 {
		t.Fatal(homes)
	}
	st := e.f.json("GET", "/api/v1/orgs/"+other+"/catherd/status", nil, nil, 200)
	if st["last_doctor"] == nil || st["home_display"] == e.f.json("GET", e.path("/status"), nil, nil, 200)["home_display"] {
		t.Fatal(st)
	}
}
func TestCatherdCatalogAvailabilityAndFilters(t *testing.T) {
	e := newCatherdEnv(t, "1.5.0")
	empty := e.f.json("GET", e.path("/catalog"), nil, nil, 200)
	if empty["refreshed_at"] != nil || len(empty["models"].([]any)) != 0 {
		t.Fatal(empty)
	}
	op := e.post("/catalog/refresh", map[string]any{"mode": "list"})
	if op["status"] != "succeeded" {
		t.Fatal(op)
	}
	if len(e.fake.CallsOf("catalog_refresh")) != 0 || len(e.fake.CallsOf("catalog_list")) != 1 {
		t.Fatal(e.fake.Calls())
	}
	e.post("/catalog/refresh", map[string]any{"mode": "listings"})
	e.post("/catalog/refresh", map[string]any{"mode": "sync"})
	if len(e.fake.CallsOf("catalog_refresh")) != 1 || len(e.fake.CallsOf("catalog_sync")) != 1 {
		t.Fatal(e.fake.Calls())
	}
	expectCode(t, e.f.json("POST", e.path("/catalog/refresh"), map[string]any{"mode": "bad"}, map[string]string{"Idempotency-Key": "bad-mode"}, 422), "validation_failed")
	cat := e.f.json("GET", e.path("/catalog?role=worker"), nil, nil, 200)
	avail := map[string]string{}
	for _, m := range cat["models"].([]any) {
		for _, r := range m.(map[string]any)["rungs"].([]any) {
			rung := r.(map[string]any)
			avail[rung["rung"].(string)] = rung["availability"].(string)
		}
	}
	for rung, want := range map[string]string{"opencode:opencode/gpt-6-sol#high": "not_offered", "codex:gpt-6-sol#ultra": "no_score", "codex:gpt-6-luna#high": "available", "claude:claude-opus-5-5#high": "unknown_listing"} {
		if avail[rung] != want {
			t.Errorf("%s: %q want %q", rung, avail[rung], want)
		}
	}
	artist := e.f.json("GET", e.path("/catalog?role=artist&backend=claude"), nil, nil, 200)
	for _, m := range artist["models"].([]any) {
		for _, r := range m.(map[string]any)["rungs"].([]any) {
			if r.(map[string]any)["availability"] != "role_incompatible" {
				t.Fatal(r)
			}
		}
	}
	only := e.f.json("GET", e.path("/catalog?available_only=true&text=LUNA"), nil, nil, 200)
	if len(only["models"].([]any)) == 0 {
		t.Fatal(only)
	}
	for _, m := range only["models"].([]any) {
		if m.(map[string]any)["family_id"] != "gpt-6-luna" {
			t.Fatal(m)
		}
	}
	expectCode(t, e.f.json("GET", e.path("/catalog?role=cook"), nil, nil, 422), "validation_failed")
	// A failed backend in the last doctor report disables its rungs with a reason.
	e.fake.DoctorJSON = []byte(`{"ready":false,"version":"1.5.0","checks":[{"id":"backend:codex","label":"codex","state":"fail","word":"fail","detail":"","fix":""}]}`)
	e.fake.DoctorExit = 3
	e.post("/doctor", nil)
	cat = e.f.json("GET", e.path("/catalog?backend=codex"), nil, nil, 200)
	for _, m := range cat["models"].([]any) {
		for _, r := range m.(map[string]any)["rungs"].([]any) {
			if r.(map[string]any)["availability"] != "backend_missing" {
				t.Fatal(r)
			}
		}
	}
}
func TestCatherdUnavailable(t *testing.T) {
	fake := catherd.NewFake()
	f := newTestAppWith(t, true, func(a *App) {
		a.SetCatherd(fake, catherd.NewRuntime(context.Background(), catherd.RuntimeConfig{Path: "/nowhere", Getenv: func(string) string { return "" }}))
	})
	f.bootstrap()
	org := f.org("alpha")
	e := &catherdEnv{f: f, fake: fake, org: org}
	st := f.json("GET", e.path("/status"), nil, nil, 200)
	if st["available"] != false || st["version"] != nil {
		t.Fatal(st)
	}
	op := e.post("/doctor", nil)
	if op["status"] != "failed" || op["problem"].(map[string]any)["code"] != "catherd_unavailable" {
		t.Fatal(op)
	}
	if len(fake.Calls()) != 0 {
		t.Fatal("catherd must not be called when it is unavailable", fake.Calls())
	}
	if pp := f.json("GET", e.path("/personal-profiles"), nil, nil, 200); pp["available"] != false {
		t.Fatal(pp)
	}
	health := f.json("GET", "/api/v1/orgs/"+org+"/health", nil, nil, 200)
	if health["catherd"].(map[string]any)["available"] != false {
		t.Fatal(health)
	}
}
func TestCatherdUnsupportedVersionAndFailures(t *testing.T) {
	e := newCatherdEnv(t, "1.6.0")
	st := e.f.json("GET", e.path("/status"), nil, nil, 200)
	if st["available"] != true || st["supported"] != false {
		t.Fatal(st)
	}
	e = newCatherdEnv(t, "1.5.0")
	e.fake.FailNext("doctor", &catherd.Error{Code: "E_BOOM", Message: "exploded with token=abc123"})
	op := e.post("/doctor", nil)
	if op["status"] != "failed" || op["problem"].(map[string]any)["code"] != "catherd_failed" {
		t.Fatal(op)
	}
}
func TestCatherdSchemaAndPersonalProfilesAreReadOnly(t *testing.T) {
	e := newCatherdEnv(t, "1.5.0")
	e.fake.Personal["mine"] = map[string]any{"name": "mine"}
	schema := e.f.json("GET", e.path("/schema"), nil, nil, 200)
	if schema["host"] != "codex" || len(schema["roles"].([]any)) != 8 || schema["supported_range"] != ">=1.5.0 <1.6.0" {
		t.Fatal(schema)
	}
	pp := e.f.json("GET", e.path("/personal-profiles"), nil, nil, 200)
	profiles := pp["profiles"].([]any)
	if pp["available"] != true || len(profiles) != 1 || profiles[0].(map[string]any)["name"] != "mine" {
		t.Fatal(pp)
	}
	for _, c := range e.fake.Calls() {
		if c.Home == "personal" && c.Op != "list" {
			t.Fatal("personal state was touched", c)
		}
	}
	other := e.f.org("beta")
	e.f.json("GET", "/api/v1/orgs/"+other+"/catherd/status", nil, nil, 200)
	e.f.json("GET", "/api/v1/orgs/org_01ARZ3NDEKTSV4RRFFQ69G5FAV/catherd/status", nil, nil, 404)
}
