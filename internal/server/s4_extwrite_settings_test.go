package server

import (
	"fmt"
	"orqen/internal/runstate"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func (e *s4Env) xws() map[string]any {
	return e.f.json("GET", e.orgPath("external-writes"), nil, nil, 200)
}

func (e *s4Env) putXws(body PutExternalWritesRequest, rev any, status int) map[string]any {
	return e.f.json("PUT", e.orgPath("external-writes"), body, map[string]string{"If-Match": fmt.Sprint(rev)}, status)
}

func revOf(m map[string]any) int { return int(m["revision"].(float64)) }

func TestS4ExternalWriteSettingsAreDryRunByDefaultAndGuardedWhenLive(t *testing.T) {
	e := newS4Env(t, s4Opts{NoHost: true})
	cur := e.xws()
	g := cur["env_guard"].(map[string]any)
	if cur["mode"] != "dry_run" || len(cur["allowlist"].([]any)) != 0 || cur["paused"] != false || revOf(cur) != 0 || g["scope"] != "loopback_only" || g["build_channel"] != "dev" || g["kill_switch"] != false {
		t.Fatal(cur)
	}
	allow := []AllowInput{{ConnectionID: e.glID, ProviderProjectID: "1"}}
	phrase := ptr(LivePhrase)
	// no phrase, wrong phrase, empty list: refused, nothing changes
	expectCode(t, e.putXws(PutExternalWritesRequest{Mode: "live", Allowlist: allow}, 0, 422), "confirm_phrase_mismatch")
	expectCode(t, e.putXws(PutExternalWritesRequest{Mode: "live", Allowlist: allow, ConfirmPhrase: ptr("enable live writes")}, 0, 422), "confirm_phrase_mismatch")
	expectCode(t, e.putXws(PutExternalWritesRequest{Mode: "live", ConfirmPhrase: phrase}, 0, 422), "allowlist_invalid")
	// unknown connection, a Jira connection, a project that is nobody's primary repository
	expectCode(t, e.putXws(PutExternalWritesRequest{Mode: "live", ConfirmPhrase: phrase, Allowlist: []AllowInput{{ConnectionID: newID("con"), ProviderProjectID: "1"}}}, 0, 422), "allowlist_invalid")
	expectCode(t, e.putXws(PutExternalWritesRequest{Mode: "live", ConfirmPhrase: phrase, Allowlist: []AllowInput{{ConnectionID: e.jiID, ProviderProjectID: "1"}}}, 0, 422), "allowlist_invalid")
	expectCode(t, e.putXws(PutExternalWritesRequest{Mode: "live", ConfirmPhrase: phrase, Allowlist: []AllowInput{{ConnectionID: e.glID, ProviderProjectID: "2"}}}, 0, 422), "allowlist_invalid")
	if e.xws()["mode"] != "dry_run" {
		t.Fatal("a refused request changed the mode")
	}
	live := e.putXws(PutExternalWritesRequest{Mode: "live", ConfirmPhrase: phrase, Allowlist: allow}, 0, 200)
	if live["mode"] != "live" || revOf(live) != 1 || len(live["allowlist"].([]any)) != 1 || live["mode_changed_at"] == nil {
		t.Fatal(live)
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "external_writes.mode_changed", "payload.to": "live", "payload.from": "dry_run"}) != 1 || e.count("events", bson.M{"org_id": e.org, "type": "external_writes.allowlist_changed", "payload.count": 1}) != 1 {
		t.Fatal("events")
	}
	// stale revision
	expectCode(t, e.putXws(PutExternalWritesRequest{Mode: "dry_run"}, 0, 409), "revision_conflict")
	// back to dry-run needs no phrase
	back := e.putXws(PutExternalWritesRequest{Mode: "dry_run", Allowlist: allow}, 1, 200)
	if back["mode"] != "dry_run" || revOf(back) != 2 {
		t.Fatal(back)
	}
	// pause and resume
	p := e.f.json("PUT", e.orgPath("external-writes", "pause"), PutExternalWritesPauseRequest{Paused: true, Reason: ptr("incident")}, map[string]string{"If-Match": "2"}, 200)
	if p["paused"] != true || p["paused_reason"] != "incident" {
		t.Fatal(p)
	}
	e.f.json("PUT", e.orgPath("external-writes", "pause"), PutExternalWritesPauseRequest{Paused: false}, map[string]string{"If-Match": "3"}, 200)
	if e.count("events", bson.M{"org_id": e.org, "type": "external_writes.paused"}) != 1 || e.count("events", bson.M{"org_id": e.org, "type": "external_writes.resumed"}) != 1 {
		t.Fatal("pause events")
	}
}

func (e *s4Env) putWriteCred(con, secret string, rev any, status int) map[string]any {
	e.f.t.Helper()
	return e.f.json("PUT", e.orgPath("connections", con, "write-credential"), PutWriteCredentialRequest{Secret: &secret}, map[string]string{"If-Match": fmt.Sprint(rev)}, status)
}

func TestS4WriteCredentialIsSeparateWriteOnlyAndChecked(t *testing.T) {
	logs := captureLogs(t)
	e := newS4Env(t, withJQL(s4Opts{}, "ORQ-1"))
	writeTok := "glpat-WRITE-SENTINEL-" + strings.ToLower(newID("w"))[2:]
	e.gl.WriteToken = writeTok
	// the same secret as the read credential is refused
	expectCode(t, e.putWriteCred(e.glID, e.GitlabToken, 0, 422), "write_credential_same_as_read")
	expectCode(t, e.putWriteCred(e.jiID, writeTok, 0, 422), "connection_kind_unsupported")
	cred := e.putWriteCred(e.glID, writeTok, 0, 200)
	if cred["state"] != "set" || revOf(cred) != 1 || cred["last_check"] != nil {
		t.Fatal(cred)
	}
	if b := fmt.Sprint(e.f.json("GET", e.orgPath("connections", e.glID, "write-credential"), nil, nil, 200)); strings.Contains(b, writeTok) {
		t.Fatal("the secret came back")
	}
	if strings.Contains(fmt.Sprint(e.f.json("GET", e.orgPath("connections", e.glID), nil, nil, 200)), writeTok) {
		t.Fatal("the connection shows the secret")
	}
	// the check needs the allowlist for the project permission part
	e.putXws(PutExternalWritesRequest{Mode: "dry_run", Allowlist: []AllowInput{{ConnectionID: e.glID, ProviderProjectID: "1"}}}, 0, 200)
	check := func() map[string]any {
		op := e.f.json("POST", e.orgPath("connections", e.glID, "write-credential", "check"), nil, map[string]string{"Idempotency-Key": nextKey()}, 202)
		e.f.waitOperation(e.org, op["id"].(string))
		return e.f.json("GET", e.orgPath("connections", e.glID, "write-credential"), nil, nil, 200)["last_check"].(map[string]any)
	}
	lc := check()
	if lc["status"] != "ok" || lc["username"] != "orqen-bot" || len(lc["projects"].([]any)) != 1 || lc["projects"].([]any)[0].(map[string]any)["access_level"] != float64(40) {
		t.Fatal(lc)
	}
	e.gl.WriteAccess = 20
	if lc = check(); lc["status"] != "insufficient" {
		t.Fatal(lc)
	}
	e.gl.WriteAccess = 40
	e.gl.Scopes = []string{"read_api"}
	if lc = check(); lc["status"] != "insufficient" {
		t.Fatal("a token without the api scope is insufficient", lc)
	}
	e.gl.Scopes = []string{"api"}
	e.replaceWriteCred(e.glID, "glpat-another-SENTINEL-token")
	if lc = check(); lc["status"] != "refused" {
		t.Fatal(lc)
	}
	// the secret reaches neither the database, the data directory (mirrors, worktrees), the logs nor a host process
	e.replaceWriteCred(e.glID, writeTok)
	e.poll()
	e.waitStage("ORQ-1", runstate.StageVerify)
	e.envMu.Lock()
	for _, env := range e.spawnEnvs {
		if strings.Contains(strings.Join(env, "\n"), writeTok) || strings.Contains(strings.Join(env, "\n"), "WRITE-SENTINEL") {
			t.Fatal("the write credential reached the host environment")
		}
	}
	n := len(e.spawnEnvs)
	e.envMu.Unlock()
	if n == 0 {
		t.Fatal("no host process was started")
	}
	e.leakScan([]string{writeTok, "another-SENTINEL"}, logs.String())
	if e.gl.Requests() == nil {
		t.Fatal("fake")
	}
	// removing it
	cur := e.f.json("GET", e.orgPath("connections", e.glID, "write-credential"), nil, nil, 200)
	gone := e.f.json("DELETE", e.orgPath("connections", e.glID, "write-credential"), nil, map[string]string{"If-Match": fmt.Sprint(revOf(cur))}, 200)
	if gone["state"] != "none" {
		t.Fatal(gone)
	}
}

// replaceWriteCred sets the credential at the latest revision (a check job may bump it concurrently).
func (e *s4Env) replaceWriteCred(con, secret string) {
	e.f.t.Helper()
	for attempt := 0; ; attempt++ {
		cur := e.f.json("GET", e.orgPath("connections", con, "write-credential"), nil, nil, 200)
		resp := e.f.request("PUT", e.orgPath("connections", con, "write-credential"), PutWriteCredentialRequest{Secret: &secret}, map[string]string{"If-Match": fmt.Sprint(revOf(cur))})
		if resp.StatusCode == 409 && attempt < 5 {
			resp.Body.Close()
			continue
		}
		readResponse(e.f.t, resp, 200)
		return
	}
}
