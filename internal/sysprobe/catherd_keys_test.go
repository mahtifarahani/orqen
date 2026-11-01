package sysprobe

import (
	"context"
	"testing"
)

func TestCatherdKeyChecks(t *testing.T) {
	run := func(mut func(*Inputs)) map[string]Check {
		d, _ := happy()
		in := baseInputs()
		mut(&in)
		return byID(Run(context.Background(), d, in))
	}
	got := run(func(in *Inputs) {})
	for _, id := range []string{"catherd.jev_key", "catherd.backend_keys"} {
		if got[id].Status != Skipped {
			t.Errorf("%s: %+v", id, got[id])
		}
	}
	if got["catherd.credentials_file"].Status != Pass || got["bin.opencode_login"].Status != Skipped {
		t.Fatal(got["catherd.credentials_file"], got["bin.opencode_login"])
	}
	for _, c := range got {
		if c.ID == "catherd.jev_key" && (c.Required || len(c.Blocks) == 0) {
			t.Fatal(c)
		}
	}
	// Jev: warn when missing or refused while a published version uses it; pass when accepted; never a failure.
	cases := []struct {
		name string
		k    KeyInputs
		want string
		fix  string
	}{
		{"missing", KeyInputs{JevInUse: true}, Warn, "jev_key_missing"},
		{"refused", KeyInputs{JevInUse: true, Jev: SecretState{Set: true, Status: "refused"}}, Warn, "jev_key_refused"},
		{"ok", KeyInputs{JevInUse: true, Jev: SecretState{Set: true, Status: "ok"}}, Pass, ""},
		{"ok unused", KeyInputs{Jev: SecretState{Set: true, Status: "ok"}}, Pass, ""},
		{"unchecked", KeyInputs{JevInUse: true, Jev: SecretState{Set: true}}, Pass, ""},
		{"missing unused", KeyInputs{}, Skipped, ""},
	}
	for _, c := range cases {
		got := run(func(in *Inputs) { in.Keys = c.k })["catherd.jev_key"]
		if got.Status != c.want || got.FixCode != c.fix {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	// Backend keys: warn per isolated backend without a key.
	got = run(func(in *Inputs) {
		in.Keys = KeyInputs{IsolatedBackends: []string{"grok", "cursor"}, Backends: map[string]SecretState{"cursor": {Set: true}}}
	})
	if c := got["catherd.backend_keys"]; c.Status != Warn || c.FixCode != "backend_key_missing" || c.Detail != "no key for the isolated backend: grok" {
		t.Fatal(c)
	}
	got = run(func(in *Inputs) {
		in.Keys = KeyInputs{IsolatedBackends: []string{"grok"}, Backends: map[string]SecretState{"grok": {Set: true}}}
	})
	if got["catherd.backend_keys"].Status != Pass {
		t.Fatal(got["catherd.backend_keys"])
	}
}
func TestCredentialsFileWarns(t *testing.T) {
	d, _ := happy()
	d.FS.(*fakeFS).existing["/org/home/config/credentials.json"] = true
	in := baseInputs()
	in.Keys.OrgHome = "/org/home"
	c := byID(Run(context.Background(), d, in))["catherd.credentials_file"]
	if c.Status != Warn || c.FixCode != "unexpected_credentials_file" || c.Required {
		t.Fatal(c)
	}
}
func TestOpencodeLoginReadsOnly(t *testing.T) {
	d, r := happy()
	d.LookPath = lookup("bun", "opencode")
	r.out["opencode auth list --format json"] = `[{"provider":"opencode-go"},{"provider":"x"}]`
	c := byID(Run(context.Background(), d, baseInputs()))["bin.opencode_login"]
	if c.Status != Pass || c.Detail != "opencode lists 2 provider login(s)" || c.Required {
		t.Fatal(c)
	}
	r.out["opencode auth list --format json"] = `[]`
	if c = byID(Run(context.Background(), d, baseInputs()))["bin.opencode_login"]; c.Status != Warn || c.FixCode != "opencode_logged_out" {
		t.Fatal(c)
	}
	r.exit["opencode auth list --format json"] = 1
	if c = byID(Run(context.Background(), d, baseInputs()))["bin.opencode_login"]; c.Status != Warn {
		t.Fatal(c)
	}
	// The output never reaches the report.
	r.out["opencode auth list --format json"] = `{"secret-provider":"sk-live-123"}`
	r.exit["opencode auth list --format json"] = 0
	if c = byID(Run(context.Background(), d, baseInputs()))["bin.opencode_login"]; c.Detail != "opencode lists 1 provider login(s)" {
		t.Fatal(c)
	}
}
