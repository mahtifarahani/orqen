package policy

import (
	"sort"
	"strings"
	"testing"
)

func TestRegistry(t *testing.T) {
	keys := make([]string, len(Registry))
	for i, k := range Registry {
		keys[i] = k.Key
		if v, msg := normalize(k, k.Default); msg != "" || v != k.Default {
			t.Errorf("default of %s does not validate: %v %s", k.Key, v, msg)
		}
	}
	if !sort.StringsAreSorted(keys) || len(keys) != 78 {
		t.Fatal(keys)
	}
	locked, orgOnly := 0, 0
	for _, k := range Registry {
		if k.Locked {
			locked++
		}
		if !k.Locked && !k.ProjectOverridable {
			orgOnly++
		}
	}
	if locked != 1 || orgOnly != 24 {
		t.Fatal(locked, orgOnly)
	}
}
func TestValidateOverrides(t *testing.T) {
	out, es := ValidateOverrides(LayerOrg, map[string]any{"automation.intake": "auto", "intake.poll_interval_secs": float64(60), "intake.label": "go", "intake.require_assigned_to_me": false, "concurrency.coding_runs": float64(2)})
	if len(es) != 0 || out["intake.poll_interval_secs"] != int64(60) || out["automation.intake"] != "auto" {
		t.Fatal(out, es)
	}
	bad := map[string]string{
		"nope.key":                       "unknown setting",
		"review.require_different_model": "locked",
		"automation.jira_write":          "must be one of",
		"intake.poll_interval_secs":      "between 30 and 3600",
		"review.max_fix_rounds":          "between 0 and 5",
		"intake.label":                   "0 to 255",
		"ci.require_pipeline":            "true or false",
	}
	in := map[string]any{"nope.key": 1, "review.require_different_model": false, "automation.jira_write": "yes", "intake.poll_interval_secs": float64(5), "review.max_fix_rounds": float64(2.5), "intake.label": strings.Repeat("x", 256), "ci.require_pipeline": "no"}
	_, es = ValidateOverrides(LayerOrg, in)
	got := map[string]string{}
	for _, e := range es {
		got[strings.TrimPrefix(e.Field, "overrides.")] = e.Message
	}
	if len(got) != len(bad) {
		t.Fatal(got)
	}
	for k, want := range bad {
		if k == "review.max_fix_rounds" {
			want = "whole number"
		}
		if !strings.Contains(got[k], want) {
			t.Errorf("%s: %q does not contain %q", k, got[k], want)
		}
	}
	// A project may not set org-only keys.
	_, es = ValidateOverrides(LayerProject, map[string]any{"concurrency.coding_runs": float64(2)})
	if len(es) != 1 || es[0].Message != "org only" {
		t.Fatal(es)
	}
	if _, es = ValidateOverrides(LayerProject, map[string]any{"review.max_fix_rounds": int64(3)}); len(es) != 0 {
		t.Fatal(es)
	}
	for _, layer := range []string{LayerOrg, LayerProject} {
		out, es := ValidateOverrides(layer, map[string]any{"intake.label": ""})
		if len(es) != 0 || out["intake.label"] != "" {
			t.Fatal("empty label disables the label filter", out, es)
		}
	}
}
func find(e Effective, key string) Entry {
	for _, x := range e.Entries {
		if x.Key == key {
			return x
		}
	}
	return Entry{}
}
func TestResolveSources(t *testing.T) {
	def := Resolve(nil, nil)
	if e := find(def, "automation.intake"); e.Value != "manual" || e.Source.Layer != LayerProduct || e.Source.Number != ProductDefaultsVersion {
		t.Fatal(e)
	}
	org := &Version{ID: "pol_org", Number: 3, Overrides: map[string]any{"automation.intake": "auto", "concurrency.coding_runs": int64(2)}}
	prj := &Version{ID: "pol_prj", Number: 1, Overrides: map[string]any{"automation.intake": "manual", "review.max_fix_rounds": int64(4), "concurrency.coding_runs": int64(4), "review.require_different_model": false}}
	eff := Resolve(org, prj)
	if e := find(eff, "automation.intake"); e.Value != "manual" || e.Source != (Source{LayerProject, "pol_prj", 1}) {
		t.Fatal("project wins", e)
	}
	if e := find(eff, "concurrency.coding_runs"); e.Value != int64(2) || e.Source != (Source{LayerOrg, "pol_org", 3}) {
		t.Fatal("org-only key ignores the project layer", e)
	}
	if e := find(eff, "review.require_different_model"); e.Value != true || !e.Locked || e.Source.Layer != LayerProduct {
		t.Fatal("a locked key keeps the product value", e)
	}
	if e := find(eff, "review.max_fix_rounds"); e.Value != int64(4) {
		t.Fatal(e)
	}
	if e := find(Resolve(org, nil), "automation.intake"); e.Value != "auto" || e.Source.Layer != LayerOrg {
		t.Fatal(e)
	}
	if Resolve(org, prj).Hash != eff.Hash || Resolve(nil, nil).Hash == eff.Hash || len(eff.Hash) != 64 {
		t.Fatal("hash")
	}
	if find(Resolve(org, nil), "intake.label").Source.Layer != LayerProduct {
		t.Fatal("untouched keys stay on product defaults")
	}
}
