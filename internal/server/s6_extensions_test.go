package server

import (
	"context"
	"fmt"
	"orqen/internal/extensions"
	"orqen/internal/kindex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestExtWikiHelperProcess re-executes the test binary as the fake `openclaw` CLI (helper-process pattern).
func TestExtWikiHelperProcess(t *testing.T) {
	if os.Getenv("EXT_WIKI_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Exit(kindex.RunFakeCLI(args, os.Stdin, os.Stdout, os.Getenv("EXT_WIKI_STATE")))
}

type extEnv struct {
	*testApp
	org string
	key int
}

func newExtEnv(t *testing.T, setup ...func(*App)) *extEnv {
	f := newTestAppWith(t, true, func(a *App) {
		a.noSandbox = true
		for _, s := range setup {
			s(a)
		}
	})
	f.bootstrap()
	return &extEnv{testApp: f, org: f.org("acme")}
}

func (e *extEnv) path(ext, tail string) string {
	return "/api/v1/orgs/" + e.org + "/extensions/" + ext + tail
}

func (e *extEnv) rev(ext string) map[string]string {
	return map[string]string{"If-Match": fmt.Sprint(int64(e.json("GET", e.path(ext, ""), nil, nil, 200)["revision"].(float64)))}
}

func (e *extEnv) register(ext, bin string) map[string]any {
	return e.json("PUT", e.path(ext, "/binary"), map[string]string{"path": bin}, e.rev(ext), 200)
}

// run starts detect, test or health and waits for the operation.
func (e *extEnv) run(ext, verb string, wantStatus int) map[string]any {
	e.key++
	op := e.json("POST", e.path(ext, "/"+verb), nil, map[string]string{"Idempotency-Key": fmt.Sprintf("k-%s-%s-%d", ext, verb, e.key)}, wantStatus)
	if wantStatus != 202 {
		return op
	}
	return e.waitOperation(e.org, op["id"].(string))
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionLicenseGateLifecycle(t *testing.T) {
	e := newExtEnv(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	log := filepath.Join(dir, "execs.log")
	bin := filepath.Join(dir, "gitnexus")
	writeScript(t, bin, "#!/bin/sh\necho run >> "+log+"\necho 'gitnexus 1.4.0'\n")
	d := e.register("gitnexus", bin)
	if d["state"] != "license_required" {
		t.Fatal(d["state"])
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("registering must not execute the binary")
	}
	expectCode(t, e.run("gitnexus", "detect", 409), "license_acceptance_required")
	notices := d["license_notices"].([]any)
	sha := notices[0].(map[string]any)["sha256"].(string)
	body := func(phrase, sha string) map[string]any {
		return map[string]any{"basis": "noncommercial_use", "note": "internal evaluation", "confirm_phrase": phrase, "notice_sha256": sha}
	}
	expectCode(t, e.json("PUT", e.path("gitnexus", "/license"), body("nope", sha), e.rev("gitnexus"), 422), "confirm_phrase_mismatch")
	expectCode(t, e.json("PUT", e.path("gitnexus", "/license"), body("gitnexus", "deadbeef"), e.rev("gitnexus"), 422), "license_notice_mismatch")
	if _, err := os.Stat(log); err == nil {
		t.Fatal("nothing may execute before acceptance")
	}
	d = e.json("PUT", e.path("gitnexus", "/license"), body("gitnexus", sha), e.rev("gitnexus"), 200)
	if d["state"] != "registered" || d["license_state"] == nil {
		t.Fatal(d)
	}
	if op := e.run("gitnexus", "detect", 202); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	if op := e.run("gitnexus", "test", 202); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	d = e.json("POST", e.path("gitnexus", "/enable"), nil, e.rev("gitnexus"), 200)
	if d["state"] != "enabled" || !d["usable"].(map[string]any)["ok"].(bool) || len(d["installs"].([]any)) != 1 {
		t.Fatal(d)
	}
	// invalid config is refused, valid config is stored
	expectCode(t, e.json("PUT", e.path("gitnexus", "/config"), map[string]any{"config": map[string]any{"depth": 9}}, e.rev("gitnexus"), 422), "extension_config_invalid")
	e.json("PUT", e.path("gitnexus", "/config"), map[string]any{"config": map[string]any{"depth": 2}}, e.rev("gitnexus"), 200)
	// project binding
	now := time.Now().UTC()
	prj := ProjectDoc{ID: newID("prj"), OrgID: e.org, Name: "p", NameKey: "p", Active: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := e.db.Coll("projects").InsertOne(context.Background(), prj); err != nil {
		t.Fatal(err)
	}
	bp := "/api/v1/orgs/" + e.org + "/projects/" + prj.ID + "/extensions/gitnexus"
	b := e.json("PUT", bp, map[string]any{"enabled": true}, map[string]string{"If-Match": "0"}, 200)
	if b["enabled"] != true || b["revision"].(float64) != 1 {
		t.Fatal(b)
	}
	m, _ := extensions.Get("gitnexus")
	if _, why, _ := e.a.extUsable(context.Background(), e.org, m, prj.ID); why != "" {
		t.Fatal("usable for the bound project:", why)
	}
	// revoke: unusable with license_revoked and detect refused
	d = e.json("DELETE", e.path("gitnexus", "/license"), nil, e.rev("gitnexus"), 200)
	if d["state"] != "disabled" || d["usable"].(map[string]any)["reason"] != "license_revoked" {
		t.Fatal(d)
	}
	expectCode(t, e.run("gitnexus", "detect", 409), "license_acceptance_required")
	if _, why, _ := e.a.extUsable(context.Background(), e.org, m, prj.ID); why != "license_revoked" {
		t.Fatal(why)
	}
	// unknown extension and unknown credential field
	e.json("GET", e.path("nope", ""), nil, nil, 404)
}

func TestExtensionWikiThroughCatalog(t *testing.T) {
	e := newExtEnv(t)
	k := &kenv{e.testApp, e.org}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	state := filepath.Join(dir, "fh", "wiki")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	bin := filepath.Join(dir, "openclaw")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'openclaw 0.2.0'; exit 0; fi\nEXT_WIKI_HELPER=1 EXT_WIKI_STATE=%s exec %s -test.run='^TestExtWikiHelperProcess$' -- \"$@\"\n", state, self)
	writeScript(t, bin, script)
	ctx := context.Background()
	src := k.source(e.org, "Doc", "Platypus facts are documented here.")
	k.verified("platypus", "Platypus facts are documented here", src)
	if r := k.search("platypus"); r["mode"] != "fallback" || r["degraded"] != "wiki_not_configured" {
		t.Fatal(r)
	}
	e.register("openclaw-wiki", bin)
	e.run("openclaw-wiki", "detect", 202)
	e.run("openclaw-wiki", "test", 202)
	e.json("POST", e.path("openclaw-wiki", "/enable"), nil, e.rev("openclaw-wiki"), 200)
	if err := e.a.maintainWiki(ctx, e.org); err != nil {
		t.Fatal(err)
	}
	if r := k.search("platypus"); r["mode"] != "wiki" || len(r["items"].([]any)) != 1 {
		t.Fatal(r)
	}
	e.json("POST", e.path("openclaw-wiki", "/disable"), nil, e.rev("openclaw-wiki"), 200)
	if r := k.search("platypus"); r["mode"] != "fallback" || len(r["items"].([]any)) != 1 {
		t.Fatal(r)
	}
	e.json("POST", e.path("openclaw-wiki", "/enable"), nil, e.rev("openclaw-wiki"), 200)
	if r := k.search("platypus"); r["mode"] != "wiki" {
		t.Fatal(r)
	}
	// the binary changes: health reports changed and search falls back
	writeScript(t, bin, script+"# rewritten\n")
	if op := e.run("openclaw-wiki", "health", 202); op["status"] != "failed" {
		t.Fatal(op)
	}
	d := e.json("GET", e.path("openclaw-wiki", ""), nil, nil, 200)
	if d["state"] != "changed" || d["health"].(map[string]any)["status"] != "changed" || d["usable"].(map[string]any)["reason"] != "binary_changed" {
		t.Fatal(d)
	}
	if r := k.search("platypus"); r["mode"] != "fallback" {
		t.Fatal(r)
	}
	// rollback to install 1 fails while the file differs, and works once the hash is equal again
	inst := d["installs"].([]any)[0].(map[string]any)["id"].(string)
	expectCode(t, e.json("POST", e.path("openclaw-wiki", "/rollback"), map[string]string{"install_id": inst}, e.rev("openclaw-wiki"), 409), "extension_binary_changed")
	writeScript(t, bin, script)
	d = e.json("POST", e.path("openclaw-wiki", "/rollback"), map[string]string{"install_id": inst}, e.rev("openclaw-wiki"), 200)
	if d["state"] != "tested" || d["health"].(map[string]any)["status"] != "ok" {
		t.Fatal(d)
	}
}

func TestExtensionCredentialNeverStored(t *testing.T) {
	e := newExtEnv(t)
	const secret = "sk-test-SENTINEL-extension-secret-0123456789"
	resp := e.request("PUT", e.path("strix", "/credentials/llm_api_key"), map[string]string{"value": secret}, nil)
	body := readResponse(t, resp, 200)
	if body["set"] != true || strings.Contains(fmt.Sprint(body), secret) {
		t.Fatal(body)
	}
	expectCode(t, e.json("PUT", e.path("strix", "/credentials/other"), map[string]string{"value": "x"}, nil, 422), "credential_field_unknown")
	d := e.json("GET", e.path("strix", ""), nil, nil, 200)
	if c := d["credentials"].([]any)[0].(map[string]any); c["field"] != "llm_api_key" || c["set"] != true {
		t.Fatal(d["credentials"])
	}
	// the value is in the credential store under a cred_ ref, and nowhere in MongoDB, events or responses
	st, err := e.a.extState(context.Background(), e.org, "strix")
	if err != nil {
		t.Fatal(err)
	}
	ref := st.Credentials["llm_api_key"].Ref
	if v, ok, _ := e.creds.Get(context.Background(), ref); !ok || v != secret || !validID("cred", ref) {
		t.Fatal("credential store", ref)
	}
	names, err := e.db.Database.ListCollectionNames(context.Background(), bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		c, err := e.db.Coll(n).Find(context.Background(), bson.M{})
		if err != nil {
			t.Fatal(err)
		}
		var docs []bson.M
		if err = c.All(context.Background(), &docs); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(fmt.Sprint(docs), secret) {
			t.Fatalf("secret found in collection %s", n)
		}
	}
	if strings.Contains(fmt.Sprint(d), secret) {
		t.Fatal("secret in detail")
	}
	// replacing deletes the old value; deleting clears it
	e.json("PUT", e.path("strix", "/credentials/llm_api_key"), map[string]string{"value": "second-value"}, nil, 200)
	if _, ok, _ := e.creds.Get(context.Background(), ref); ok {
		t.Fatal("the old secret must be deleted")
	}
	if r := e.json("DELETE", e.path("strix", "/credentials/llm_api_key"), nil, nil, 200); r["set"] != false {
		t.Fatal(r)
	}
	// enabling needs the credential
	if d := e.json("GET", e.path("strix", ""), nil, nil, 200); d["credentials"].([]any)[0].(map[string]any)["set"] != false {
		t.Fatal(d)
	}
}
