package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http/httptest"
	"orqen/internal/catherd"
	"orqen/internal/testfakes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (e *catherdEnv) preflight() map[string]map[string]any {
	testKeyCounter++
	op := e.f.json("POST", "/api/v1/orgs/"+e.org+"/system/preflight", nil, map[string]string{"Idempotency-Key": "pf-" + strings.Repeat("x", testKeyCounter%7) + string(rune('a'+testKeyCounter%26)) + string(rune('0'+testKeyCounter%10)) + string(rune('A'+testKeyCounter/26%26))}, 202)
	e.f.waitOperation(e.org, op["id"].(string))
	rep := e.f.json("GET", "/api/v1/orgs/"+e.org+"/system/preflight", nil, nil, 200)
	out := map[string]map[string]any{}
	for _, c := range rep["checks"].([]any) {
		m := c.(map[string]any)
		out[m["id"].(string)] = m
	}
	return out
}
func TestPreflightReportsAndRecheckIsANewOperation(t *testing.T) {
	fake := catherd.NewFake()
	rt := catherd.NewRuntime(t.Context(), catherd.RuntimeConfig{Path: "/nowhere", Getenv: func(string) string { return "" }})
	gl := testfakes.NewGitLab()
	up := httptest.NewServer(gl.Handler())
	defer up.Close()
	f := newTestAppWith(t, true, func(a *App) { a.SetCatherd(fake, rt) })
	f.bootstrap()
	org := f.org("alpha")
	e := &catherdEnv{f: f, fake: fake, org: org}
	empty := f.json("GET", "/api/v1/orgs/"+org+"/system/preflight", nil, nil, 200)
	if empty["checked_at"] != nil || len(empty["checks"].([]any)) != 0 {
		t.Fatal(empty)
	}
	conn := f.providerConn(org, gitlabSpec(up.URL))
	checks := e.preflight()
	if c := checks["bin.catherd"]; c["status"] != "fail" || c["fix_code"] != "catherd_missing" || c["required"] != true {
		t.Fatal(c)
	}
	if c := checks["net."+conn]; c["status"] != "pass" {
		t.Fatal(c)
	}
	if c := checks["mongo.replica_set"]; c["status"] != "pass" || !strings.Contains(c["detail"].(string), "replica set") {
		t.Fatal(c)
	}
	if c := checks["keychain.access"]; c["status"] != "warn" || c["fix_code"] != "credential_store_memory" {
		t.Fatal(c)
	}
	for _, id := range []string{"fs.data_dir", "fs.artifacts", "fs.catherd_home", "fs.workspaces"} {
		if checks[id]["status"] != "pass" {
			t.Fatal(checks[id])
		}
	}
	first := f.json("GET", "/api/v1/orgs/"+org+"/system/preflight", nil, nil, 200)
	// GET never runs probes: the report is unchanged until a new operation completes.
	time.Sleep(50 * time.Millisecond)
	if again := f.json("GET", "/api/v1/orgs/"+org+"/system/preflight", nil, nil, 200); again["operation_id"] != first["operation_id"] || again["checked_at"] != first["checked_at"] {
		t.Fatal("GET changed the report")
	}
	e.preflight()
	second := f.json("GET", "/api/v1/orgs/"+org+"/system/preflight", nil, nil, 200)
	if second["operation_id"] == first["operation_id"] {
		t.Fatal("re-check must be a new operation")
	}
	art := f.json("GET", "/api/v1/orgs/"+org+"/artifacts/"+second["artifact_id"].(string)+"/content", nil, nil, 200)
	if len(art["checks"].([]any)) != len(second["checks"].([]any)) {
		t.Fatal(art)
	}
	// Another organization has its own (empty) report.
	other := f.org("beta")
	if r := f.json("GET", "/api/v1/orgs/"+other+"/system/preflight", nil, nil, 200); r["checked_at"] != nil {
		t.Fatal(r)
	}
}
func TestPreflightExpiredCAFails(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "old ca"}, NotBefore: time.Now().Add(-72 * time.Hour), NotAfter: time.Now().Add(-time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	e := newCatherdEnv(t, "1.5.0")
	id := e.f.json("POST", "/api/v1/orgs/"+e.org+"/connections", CreateConnectionRequest{Name: "Wiki", Provider: "gitlab", Deployment: "self_managed", BaseURL: "https://127.0.0.1:9", AuthMethod: "personal_access_token", CustomCAPEM: &ca}, nil, 201)["id"].(string)
	checks := e.preflight()
	if c := checks["net."+id]; c["status"] != "fail" || c["fix_code"] != "ca_expired" {
		t.Fatal(c)
	}
	if c := checks["bin.catherd"]; c["status"] != "pass" {
		t.Fatal(c)
	}
}
func TestLaunchdPlanIsTextOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	e := newCatherdEnv(t, "1.5.0")
	base := "/api/v1/orgs/" + e.org + "/system/launchd"
	resp := e.f.request("GET", base, nil, nil)
	if resp.Header.Get("ETag") != "\"0\"" {
		t.Fatal(resp.Header)
	}
	plan := readResponse(t, resp, 200)
	if plan["opted_in"] != false || plan["plist_path"] != "~/Library/LaunchAgents/dev.orqen.server.plist" || !strings.Contains(plan["plist_xml"].(string), "dev.orqen.server") || !strings.Contains(plan["plist_xml"].(string), "<string>serve</string>") {
		t.Fatal(plan)
	}
	if !strings.Contains(strings.Join(anyStrings(plan["install_commands"]), "\n"), "launchctl bootstrap") {
		t.Fatal(plan)
	}
	patched := e.f.json("PATCH", base, map[string]any{"opted_in": true}, map[string]string{"If-Match": "0"}, 200)
	if patched["opted_in"] != true || patched["revision"] != float64(1) {
		t.Fatal(patched)
	}
	expectCode(t, e.f.json("PATCH", base, map[string]any{"opted_in": false}, map[string]string{"If-Match": "0"}, 409), "revision_conflict")
	e.f.json("PATCH", base, map[string]any{"opted_in": false}, map[string]string{"If-Match": "1"}, 200)
	// Opting in with the preflight document already created by a report works from revision 0 too.
	checks := e.preflight()
	if checks["launchd.start_at_login"]["status"] != "skipped" {
		t.Fatal(checks["launchd.start_at_login"])
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "LaunchAgents")); !os.IsNotExist(err) {
		t.Fatal("Orqen created ~/Library/LaunchAgents", err)
	}
	e.f.json("PATCH", base, map[string]any{}, map[string]string{"If-Match": "2"}, 422)
}
func anyStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
func TestKeychainProbeUsesTheCredentialStore(t *testing.T) {
	e := newCatherdEnv(t, "1.5.0")
	res := e.f.json("POST", "/api/v1/orgs/"+e.org+"/system/keychain-probe", nil, map[string]string{"Idempotency-Key": "kc"}, 200)
	if res["status"] != "pass" {
		t.Fatal(res)
	}
	e.f.creds.mu.RLock()
	n := len(e.f.creds.values)
	e.f.creds.mu.RUnlock()
	if n != 0 {
		t.Fatal("the probe credential was left behind")
	}
	e.f.json("POST", "/api/v1/orgs/"+e.org+"/system/keychain-probe", nil, nil, 422)
}

// Opt-in: exercises the real OS credential store, which may show a prompt.
func TestKeychainProbe(t *testing.T) {
	if os.Getenv("ORQEN_TEST_KEYCHAIN") != "1" {
		t.Skip("set ORQEN_TEST_KEYCHAIN=1 to exercise the OS credential store")
	}
	f := newTestAppWith(t, false, func(a *App) { a.Creds = KeychainStore{} })
	f.bootstrap()
	org := f.org("alpha")
	res := f.json("POST", "/api/v1/orgs/"+org+"/system/keychain-probe", nil, map[string]string{"Idempotency-Key": "kc"}, 200)
	if res["status"] != "pass" {
		t.Fatal(res)
	}
}
