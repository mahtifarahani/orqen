package server

import (
	"fmt"
	"testing"
)

func (e *s4Env) probe() map[string]any {
	e.sup.Stop(e.org) // a fresh app-server per probe
	res := e.f.json("POST", e.orgPath("host", "probe"), nil, map[string]string{"Idempotency-Key": nextKey()}, 202)
	e.f.waitOperation(e.org, res["id"].(string))
	return e.f.json("GET", e.orgPath("host"), nil, nil, 200)
}

func TestS4HostProbe(t *testing.T) {
	e := newS4Env(t, s4Opts{NoHost: true})
	h := e.f.json("GET", e.orgPath("host"), nil, nil, 200)
	if h["probe"] != nil || h["available"] != false {
		t.Fatal(h)
	}
	// not probed: enabling is blocked and the blocker is named
	if codes := blockerCodes(e.readyReport()); !contains(codes, "host_not_probed") {
		t.Fatal(codes)
	}
	h = e.probe()
	p := h["probe"].(map[string]any)
	if p["status"] != "ok" || p["fake"] != true || h["available"] != true || p["login"] != "ok" {
		t.Fatal(h)
	}
	if codes := blockerCodes(e.readyReport()); contains(codes, "host_not_probed") {
		t.Fatal(codes)
	}
	// a real-looking host without the user's attestation is not usable
	e.app.UserAgent = "codex_cli_rs/0.160.0 (test)"
	h = e.probe()
	if h["probe"].(map[string]any)["fake"] != false || h["available"] != false {
		t.Fatal(h)
	}
	if codes := blockerCodes(e.readyReport()); !contains(codes, "host_spike_unverified") {
		t.Fatal(codes)
	}
	// attestation needs If-Match and then makes it available
	e.f.json("POST", e.orgPath("host", "spike-attest"), map[string]any{"codex_version": "0.160.0", "catherd_version": "1.5.0"}, map[string]string{"If-Match": "99"}, 409)
	rev := fmt.Sprint(int(h["revision"].(float64)))
	h = e.f.json("POST", e.orgPath("host", "spike-attest"), map[string]any{"codex_version": "0.160.0", "catherd_version": "1.5.0", "note": "ran it"}, map[string]string{"If-Match": rev}, 200)
	if h["available"] != true || h["spike"] == nil {
		t.Fatal(h)
	}
}

func TestS4HostProbeFailures(t *testing.T) {
	e := newS4Env(t, s4Opts{NoHost: true})
	e.app.NotLoggedIn = true
	h := e.probe()
	if !contains(anyStrings(h["probe"].(map[string]any)["reasons"]), "host_login_missing") || h["available"] != false {
		t.Fatal(h)
	}
	if codes := blockerCodes(e.readyReport()); !contains(codes, "host_login_missing") {
		t.Fatal(codes)
	}
	e.app.NotLoggedIn = false
	e.app.HideTools = []string{"land"}
	h = e.probe()
	if !contains(anyStrings(h["probe"].(map[string]any)["reasons"]), "host_catherd_mcp_missing") {
		t.Fatal(h)
	}
	e.app.HideTools = nil
	e.app.UserAgent = "orqen_fake_codex/0.100.0 (test)"
	h = e.probe()
	if !contains(anyStrings(h["probe"].(map[string]any)["reasons"]), "host_codex_too_old") {
		t.Fatal(h)
	}
	// spike attestation is refused without a good probe
	e.f.json("POST", e.orgPath("host", "spike-attest"), map[string]any{"codex_version": "0.160.0", "catherd_version": "1.5.0"}, map[string]string{"If-Match": "1"}, 409)
}

func TestS4PublishingDryRunWarningByDefault(t *testing.T) {
	e := newS4Env(t, s4Opts{})
	if w := warningCodes(e.readyReport()); !contains(w, "publishing_dry_run") {
		t.Fatal(w)
	}
}

func (e *s4Env) readyReport() map[string]any {
	return e.f.json("GET", e.projectEnv.path(e.pid, "readiness"), nil, nil, 200)
}
