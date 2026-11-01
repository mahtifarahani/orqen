package catherd

import (
	"encoding/json"
	"os"
	"testing"
)

// The vectors were recorded from the real catherd 1.5.0 in a temporary home: `profile set` of one top-level key
// on a fresh profile. Orqen's own merge must produce the same stored document.
func TestMergePatchMatchesRealCatherd(t *testing.T) {
	b, err := os.ReadFile("testdata/merge-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			ProfileKey string          `json:"profile_key"`
			Patch      json.RawMessage `json:"patch"`
			Before     json.RawMessage `json:"before"`
			After      json.RawMessage `json:"after"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(b, &file); err != nil || len(file.Cases) < 10 {
		t.Fatal(err, len(file.Cases))
	}
	for _, c := range file.Cases {
		patch, _ := Decode(c.Patch)
		before, _ := Decode(c.Before)
		after, _ := Decode(c.After)
		got := MergePatch(before, patch).(map[string]any)
		if !same(got, after) {
			g, _ := json.Marshal(got)
			t.Errorf("%s: merge differs\n got %s\nwant %s", c.ProfileKey, g, c.After)
		}
		// And the patch Orqen derives between the two documents applies back to the same result.
		derived := PatchBetween(before, after)
		if !same(MergePatch(before, derived), after) {
			t.Errorf("%s: derived patch does not reproduce the document", c.ProfileKey)
		}
		if issues := ValidatePatch(derived); len(issues) != 0 {
			t.Errorf("%s: derived patch rejected by the schema mirror: %+v", c.ProfileKey, issues)
		}
	}
}

func goodDoc(t *testing.T) map[string]any {
	t.Helper()
	doc, _ := Decode(defaultProfileJSON)
	delete(doc, "name")
	roles := doc["roles"].(map[string]any)
	roles["architect"].(map[string]any)["rungs"] = []any{"codex:gpt-6.1-sol#high"}
	roles["verifier"].(map[string]any)["rungs"] = []any{"codex:gpt-6.1-sol#low"}
	roles["reviewer"].(map[string]any)["rungs"] = []any{"claude-code:claude-opus-5-5#high"}
	doc["failover"] = map[string]any{"codex:gpt-6-luna#high": "opencode:opencode-go/gpt-6-luna#high"}
	doc["budget"] = map[string]any{"usd": json.Number("5")}
	return doc
}
func cat(t *testing.T) *Catalog {
	t.Helper()
	c, err := ParseCatalog(fakeCatalogJSON)
	if err != nil {
		t.Fatal(err)
	}
	return &c
}
func codes(in []Issue) map[string]bool {
	out := map[string]bool{}
	for _, i := range in {
		out[i.Code] = true
	}
	return out
}
func TestAnalyzeAcceptsAGoodDraft(t *testing.T) {
	a := Analyze(goodDoc(t), cat(t), Meta{})
	if len(a.Errors) != 0 || len(a.Warnings) != 0 || a.Independence.Status != "independent" {
		t.Fatalf("%+v", a)
	}
}
func TestAnalyzeIssues(t *testing.T) {
	cases := map[string]struct {
		mutate func(map[string]any)
		code   string
		warn   bool
	}{
		"unsupported field":  {func(d map[string]any) { d["extra"] = 1 }, "unsupported_field", false},
		"nested unsupported": {func(d map[string]any) { d["roles"].(map[string]any)["worker"].(map[string]any)["surprise"] = true }, "unsupported_field", false},
		"unknown enum":       {func(d map[string]any) { d["objective"] = "fast" }, "unknown_value", false},
		"bad rung": {func(d map[string]any) {
			d["roles"].(map[string]any)["writer"].(map[string]any)["rungs"] = []any{"nonsense"}
		}, "bad_rung", false},
		"not in catalog": {func(d map[string]any) {
			d["roles"].(map[string]any)["writer"].(map[string]any)["rungs"] = []any{"codex:gpt-6-sool#high"}
		}, "rung_not_in_catalog", false},
		"rungs required": {func(d map[string]any) { delete(d["roles"].(map[string]any)["writer"].(map[string]any), "rungs") }, "rungs_required", false},
		"default not listed": {func(d map[string]any) {
			d["roles"].(map[string]any)["worker"].(map[string]any)["defaultRung"] = "codex:gpt-6-sol#low"
		}, "default_rung_not_in_rungs", false},
		"native claude": {func(d map[string]any) {
			d["roles"].(map[string]any)["researcher"].(map[string]any)["rungs"] = []any{"claude:claude-opus-5-5#high"}
		}, "claude_native_unsupported", false},
		"worker disabled": {func(d map[string]any) { d["roles"].(map[string]any)["worker"].(map[string]any)["enabled"] = false }, "worker_disabled", false},
		"schema":          {func(d map[string]any) { d["schema"] = json.Number("2") }, "unsupported_field", false},
		"same model": {func(d map[string]any) {
			d["roles"].(map[string]any)["reviewer"].(map[string]any)["rungs"] = []any{"opencode:opencode-go/gpt-6-luna#high"}
		}, "reviewer_not_independent", false},
		"unknown independence": {func(d map[string]any) {
			d["roles"].(map[string]any)["reviewer"].(map[string]any)["rungs"] = []any{"codex:gpt-6.1-sol#low"}
			d["failover"] = map[string]any{"codex:gpt-6-sol#medium": "opencode:opencode-go/kimi-k3#max"}
		}, "independence_unknown", true},
		"no budget": {func(d map[string]any) { d["budget"] = map[string]any{} }, "budget_not_chosen", true},
	}
	for name, c := range cases {
		d := goodDoc(t)
		c.mutate(d)
		a := Analyze(d, cat(t), Meta{})
		list := a.Errors
		if c.warn {
			list = a.Warnings
		}
		if !codes(list)[c.code] {
			t.Errorf("%s: want %s in %+v", name, c.code, list)
		}
		for _, i := range list {
			if i.Origin != "orqen" || i.PathDisplay == "" || (c.warn && i.Severity != "warning") || (!c.warn && i.Severity != "error") {
				t.Errorf("%s: %+v", name, i)
			}
		}
	}
	d := goodDoc(t)
	d["budget"] = map[string]any{}
	if a := Analyze(d, cat(t), Meta{BudgetUncappedAck: true}); codes(a.Warnings)["budget_not_chosen"] {
		t.Fatal("an explicit no-cap choice satisfies the budget rule")
	}
	if a := Analyze(goodDoc(t), nil, Meta{}); !codes(a.Warnings)["catalog_missing"] || a.Independence.Status != "unknown" {
		t.Fatalf("%+v", a)
	}
}
func TestIndependence(t *testing.T) {
	d := goodDoc(t)
	c := cat(t)
	if got := ComputeIndependence(d, c); got.Status != "independent" {
		t.Fatal(got)
	}
	// The same model behind a different backend name is still the same model.
	d["roles"].(map[string]any)["reviewer"].(map[string]any)["rungs"] = []any{"opencode:opencode-go/gpt-6-luna#high"}
	if got := ComputeIndependence(d, c); got.Status != "same_model" || len(got.Shared) != 1 || got.Shared[0] != "gpt-6-luna" {
		t.Fatal(got)
	}
	// A failover stand-in counts as a coder.
	d = goodDoc(t)
	d["failover"] = map[string]any{"codex:gpt-6-sol#high": "claude-code:claude-opus-5-5#high"}
	if got := ComputeIndependence(d, c); got.Status != "same_model" || got.Shared[0] != "claude-opus-5-5" {
		t.Fatal(got)
	}
	d = goodDoc(t)
	d["roles"].(map[string]any)["reviewer"].(map[string]any)["rungs"] = []any{"codex:gpt-6-sool#high"}
	if got := ComputeIndependence(d, c); got.Status != "unknown" || got.Unmatched[0] != "codex:gpt-6-sool#high" {
		t.Fatal(got)
	}
	d["roles"].(map[string]any)["reviewer"].(map[string]any)["enabled"] = false
	if got := ComputeIndependence(d, c); got.Status != "reviewer_disabled" {
		t.Fatal(got)
	}
}
func TestDiff(t *testing.T) {
	a := goodDoc(t)
	b := Clone(a).(map[string]any)
	b["name"] = "ignored"
	b["budget"] = map[string]any{"usd": json.Number("9"), "tokens": json.Number("100")}
	b["objective"] = "speed"
	delete(b["failover"].(map[string]any), "codex:gpt-6-luna#high")
	b["roles"].(map[string]any)["reviewer"].(map[string]any)["rungs"] = []any{"codex:gpt-6-luna#high"}
	got := Diff(a, b)
	want := map[string]string{"budget.tokens": "added", "budget.usd": "changed", "failover.codex:gpt-6-luna#high": "removed", "objective": "changed", "roles.reviewer.rungs": "changed"}
	// failover keeps its (now empty) object, which flattens to a leaf marker.
	gotKinds := map[string]string{}
	for _, c := range got {
		gotKinds[c.PathDisplay] = c.Kind
	}
	for p, k := range want {
		if p == "failover.codex:gpt-6-luna#high" {
			if gotKinds["failover"] == "" {
				t.Errorf("failover change missing: %v", gotKinds)
			}
			continue
		}
		if gotKinds[p] != k {
			t.Errorf("%s: %q want %q (%v)", p, gotKinds[p], k, gotKinds)
		}
	}
	if len(Diff(a, a)) != 0 {
		t.Fatal("identical documents")
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].PathDisplay > got[i].PathDisplay {
			t.Fatal("not sorted")
		}
	}
	// A rung key containing dots stays one path segment.
	x := map[string]any{"failover": map[string]any{"opencode:opencode-go/gpt-5.6-luna#high": "codex:gpt-5.6-luna#high"}}
	d := Diff(map[string]any{}, x)
	if len(d) != 1 || len(d[0].Path) != 2 || d[0].Path[1] != "opencode:opencode-go/gpt-5.6-luna#high" {
		t.Fatalf("%+v", d)
	}
}
