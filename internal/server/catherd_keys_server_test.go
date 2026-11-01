package server

import (
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/catherd"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (e *projectEnv) putSecret(kind, value string) {
	e.f.t.Helper()
	rev := "0"
	resp := e.f.request("GET", "/api/v1/orgs/"+e.org+"/catherd/secrets", nil, nil)
	var rows []map[string]any
	if err := decodeBody(resp, &rows); err != nil {
		e.f.t.Fatal(err)
	}
	for _, r := range rows {
		if r["kind"] == kind {
			rev = formatRev(r["revision"])
		}
	}
	e.f.json("PUT", "/api/v1/orgs/"+e.org+"/catherd/secrets/"+kind, map[string]string{"secret": value}, map[string]string{"If-Match": rev}, 200)
}
func formatRev(v any) string { return fmt.Sprint(int(v.(float64))) }
func toJSON(v any) string    { b, _ := json.Marshal(v); return string(b) }
func (e *projectEnv) setCheck(kind, status string) {
	e.f.t.Helper()
	_, err := e.f.db.Coll("catherd_secrets").UpdateOne(e.f.t.Context(), bson.M{"org_id": e.org, "kind": kind}, bson.M{"$set": bson.M{"last_check": SecretCheck{Status: status, CheckedAt: time.Now().UTC(), Detail: "test", OperationID: "op_x"}}})
	if err != nil {
		e.f.t.Fatal(err)
	}
}

// withGrok adds a grok model (a copy of gpt-6-sol) to the fake catalog and reloads the organization's snapshot.
func (e *projectEnv) withGrok() {
	e.f.t.Helper()
	var cat struct {
		Total  int              `json:"total"`
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(e.fake.CatalogJSON, &cat); err != nil {
		e.f.t.Fatal(err)
	}
	for _, m := range cat.Models {
		if m["id"] == "gpt-6-sol" && m["backend"] == "codex" {
			raw, _ := json.Marshal(m)
			raw = []byte(strings.ReplaceAll(strings.ReplaceAll(string(raw), "codex:gpt-6-sol", "grok:grok-5"), `"backend":"codex"`, `"backend":"grok"`))
			var g map[string]any
			_ = json.Unmarshal(raw, &g)
			g["id"], g["model"] = "grok-5", "grok-5"
			cat.Models = append(cat.Models, g)
			break
		}
	}
	cat.Total = len(cat.Models)
	e.fake.CatalogJSON, _ = json.Marshal(cat)
	(&catherdEnv{f: e.f, fake: e.fake, org: e.org}).post("/catalog/refresh", map[string]any{"mode": "list"})
}
func warningCodes(r map[string]any) []string {
	var out []string
	for _, w := range r["warnings"].([]any) {
		out = append(out, w.(map[string]any)["code"].(string))
	}
	return out
}

// Keys change readiness the way SPEC section 9 says: a missing Jev key is a fallback warning, a missing key of an
// isolated backend is a blocker.
func TestReadinessKeyEffects(t *testing.T) {
	e, s := newReadyProject(t)
	s.all()
	if r := e.ready(); r["ready"] != true || contains(warningCodes(r), "jev_key_missing") {
		t.Fatal("jev.use=off needs no key:", r)
	}
	// jev.use=auto without a key: warning, still ready.
	auto := goodProfileDoc()
	auto["jev"] = map[string]any{"use": "auto"}
	s.bind(s.profile(auto))
	r := e.ready()
	if r["ready"] != true || !contains(warningCodes(r), "jev_key_missing") {
		t.Fatal(r)
	}
	e.putSecret("jev", "orqen-test-jev")
	if r = e.ready(); r["ready"] != true || contains(warningCodes(r), "jev_key_missing") || contains(warningCodes(r), "jev_key_refused") {
		t.Fatal("a stored, unchecked key raises no warning:", r)
	}
	e.setCheck("jev", "refused")
	if r = e.ready(); r["ready"] != true || !contains(warningCodes(r), "jev_key_refused") {
		t.Fatal(r)
	}
	// An isolated grok harness without a key is a blocker, naming the backend.
	e.withGrok()
	iso := goodProfileDoc()
	iso["harness"].(map[string]any)["grok"] = map[string]any{"isolated": true}
	iso["failover"] = map[string]any{"codex:gpt-6-luna#high": "opencode:opencode-go/gpt-6-luna#high", "codex:gpt-6-sol#high": "grok:grok-5#high"}
	bound := s.bind(s.profile(iso))
	if got := blockerCodes(e.ready()); !contains(got, "backend_key_missing:grok") || bound["state"] != "draft" {
		t.Fatal(got, bound["state"])
	}
	e.putSecret("xai", "orqen-test-xai")
	r = e.ready()
	if !onlyUnsupportedBackends(r) || !contains(warningCodes(r), "backend_key_unverified:grok") {
		t.Fatal(r)
	}
	e.setCheck("xai", "refused")
	if got := blockerCodes(e.ready()); !contains(got, "backend_key_refused:grok") {
		t.Fatal(got)
	}
	e.setCheck("xai", "ok")
	if r = e.ready(); !onlyUnsupportedBackends(r) || contains(warningCodes(r), "backend_key_unverified:grok") {
		t.Fatal(r)
	}
	// Keys never leak through the readiness body.
	if b := toJSON(r); strings.Contains(b, "orqen-test") {
		t.Fatal(b)
	}
}
func TestPreflightCatherdKeys(t *testing.T) {
	e, s := newReadyProject(t)
	checks := (&catherdEnv{f: e.f, fake: e.fake, org: e.org}).preflight()
	for _, id := range []string{"catherd.jev_key", "catherd.backend_keys"} {
		if checks[id]["status"] != "skipped" {
			t.Fatal(id, checks[id])
		}
	}
	if checks["catherd.credentials_file"]["status"] != "pass" {
		t.Fatal(checks["catherd.credentials_file"])
	}
	// A published version using Jev and an isolated grok without keys.
	e.withGrok()
	doc := goodProfileDoc()
	doc["jev"] = map[string]any{"use": "auto"}
	doc["harness"].(map[string]any)["grok"] = map[string]any{"isolated": true}
	doc["failover"] = map[string]any{"codex:gpt-6-luna#high": "opencode:opencode-go/gpt-6-luna#high", "codex:gpt-6-sol#high": "grok:grok-5#high"}
	s.profile(doc)
	checks = (&catherdEnv{f: e.f, fake: e.fake, org: e.org}).preflight()
	if c := checks["catherd.jev_key"]; c["status"] != "warn" || c["fix_code"] != "jev_key_missing" || c["required"] != false {
		t.Fatal(c)
	}
	if c := checks["catherd.backend_keys"]; c["status"] != "warn" || c["fix_code"] != "backend_key_missing" {
		t.Fatal(c)
	}
	e.putSecret("jev", "orqen-test-jev")
	e.setCheck("jev", "ok")
	e.putSecret("xai", "orqen-test-xai")
	checks = (&catherdEnv{f: e.f, fake: e.fake, org: e.org}).preflight()
	if checks["catherd.jev_key"]["status"] != "pass" || checks["catherd.backend_keys"]["status"] != "pass" {
		t.Fatal(checks["catherd.jev_key"], checks["catherd.backend_keys"])
	}
	// Orqen never writes credentials.json; a planted one is reported.
	cfg := filepath.Join(catherd.NewOrgEnv(e.f.a.Config.DataDir, e.org).Home(), "config")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "credentials.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	checks = (&catherdEnv{f: e.f, fake: e.fake, org: e.org}).preflight()
	if c := checks["catherd.credentials_file"]; c["status"] != "warn" || c["fix_code"] != "unexpected_credentials_file" {
		t.Fatal(c)
	}
}

// onlyUnsupportedBackends: the codex host refuses non-codex rungs (host_backend_unsupported), which these key tests
// use on purpose; every other blocker must be gone.
func onlyUnsupportedBackends(r map[string]any) bool {
	for _, c := range blockerCodes(r) {
		if !strings.HasPrefix(c, "host_backend_unsupported:") {
			return false
		}
	}
	return true
}
