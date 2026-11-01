package sysprobe

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
)

// SecretState is what Orqen knows about one stored key without reading it: whether it is set and the last check result
// (ok, refused, unchecked, unverifiable or empty when never checked). The value is never an input here.
type SecretState struct {
	Set    bool
	Status string
}

// KeyInputs describe catherd's keys for one organization.
type KeyInputs struct {
	Jev SecretState
	// JevInUse: some published profile version has jev.use=auto.
	JevInUse bool
	// Backends holds the key state of every isolated-capable backend (grok, cursor, antigravity).
	Backends map[string]SecretState
	// IsolatedBackends are the backends a published version runs on an isolated harness.
	IsolatedBackends []string
	// OrgHome is the organization's catherd home; Orqen never writes credentials.json there.
	OrgHome string
}

func catherdKeyChecks(_ context.Context, d Deps, in Inputs) []Check {
	k := in.Keys
	jev := timed(func() Check {
		c := Check{ID: "catherd.jev_key", Group: "catherd", Blocks: []string{"jev_routing"}}
		switch {
		case k.Jev.Set && k.Jev.Status == "ok":
			c.Status, c.Detail = Pass, "the Jev key is set and Jev accepted it"
		case k.JevInUse && !k.Jev.Set:
			c.Status, c.FixCode, c.Detail = Warn, "jev_key_missing", "a published profile uses Jev routing but no Jev key is set; routing falls back to lane kind and difficulty"
		case k.JevInUse && k.Jev.Status == "refused":
			c.Status, c.FixCode, c.Detail = Warn, "jev_key_refused", "Jev refused the stored key; routing falls back to lane kind and difficulty"
		case k.JevInUse:
			c.Status, c.Detail = Pass, "the Jev key is set and has not been verified yet"
		default:
			c.Status, c.Detail = Skipped, "no published profile uses Jev routing"
		}
		return c
	})
	backends := timed(func() Check {
		c := Check{ID: "catherd.backend_keys", Group: "catherd", Blocks: []string{"isolated_harness"}}
		var missing []string
		for _, b := range k.IsolatedBackends {
			if !k.Backends[b].Set {
				missing = append(missing, b)
			}
		}
		sort.Strings(missing)
		switch {
		case len(k.IsolatedBackends) == 0:
			c.Status, c.Detail = Skipped, "no published profile isolates a keyed backend"
		case len(missing) > 0:
			c.Status, c.FixCode, c.Detail = Warn, "backend_key_missing", "no key for the isolated backend: "+strings.Join(missing, ", ")
		default:
			c.Status, c.Detail = Pass, "every isolated backend has a key"
		}
		return c
	})
	file := timed(func() Check {
		c := Check{ID: "catherd.credentials_file", Group: "catherd"}
		for _, p := range []string{filepath.Join(k.OrgHome, "config", "credentials.json"), filepath.Join(k.OrgHome, "credentials.json")} {
			if k.OrgHome != "" && d.FS.Exists(p) {
				c.Status, c.FixCode, c.Detail = Warn, "unexpected_credentials_file", "catherd's credentials.json exists at "+p+"; Orqen never writes it and passes keys through the environment"
				return c
			}
		}
		c.Status, c.Detail = Pass, "no credentials.json in the organization's catherd home"
		return c
	})
	return []Check{jev, backends, file}
}
