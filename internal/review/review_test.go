package review

import (
	"strings"
	"testing"

	"orqen/internal/gitdiff"
)

func reply(json string) string {
	return "Looks mostly fine.\n\n" + Marker + "\n" + json + "\n\nSTATUS: complete — reviewed"
}

func TestParse(t *testing.T) {
	r, err := Parse(reply(`{"verdict":"changes_requested","scope":"","findings":[{"id":"F1","severity":"major","file":"src/a.go","line":3,"side":"new","title":"Bug","body":"fix it","acceptance_ref":"A1"},{"id":"F2","severity":"nit","file":"","line":0,"title":"style","body":""}]}`))
	if err != nil || r.Verdict != "changes_requested" || len(r.Findings) != 2 || !r.Findings[0].Blocking || r.Findings[1].Blocking || r.Findings[1].Side != "new" || r.Blocking() != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	for _, bad := range []string{"no marker at all", reply(`{"verdict":"maybe"}`), reply(`{`), reply(`{"verdict":"approved","findings":[{"id":"","severity":"major","title":"x"}]}`),
		reply(`{"verdict":"approved","findings":[{"id":"A","severity":"huge","title":"x"}]}`), reply(`{"verdict":"approved","findings":[{"id":"A","severity":"nit","title":"x"},{"id":"A","severity":"nit","title":"y"}]}`), reply(`{"verdict":"approved","findings":[{"id":"A","severity":"nit","title":" "}]}`)} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q must not parse", bad)
		}
	}
	// the last marker wins, text after the JSON is ignored
	r, err = Parse(Marker + "\n{\"verdict\":\"approved\"}\n" + reply(`{"verdict":"approved_with_scope","scope":"docs not read"}`))
	if err != nil || r.Verdict != "approved_with_scope" || r.Scope != "docs not read" {
		t.Fatalf("%+v %v", r, err)
	}
	long, _ := Parse(reply(`{"verdict":"approved","findings":[{"id":"A","severity":"nit","title":"` + strings.Repeat("t", 500) + `","body":"` + strings.Repeat("b", 5000) + `"}]}`))
	if len(long.Findings[0].Title) != 200 || len(long.Findings[0].Body) != 4000 {
		t.Fatal("title and body caps")
	}
}

func TestEffectiveVerdict(t *testing.T) {
	r, _ := Parse(reply(`{"verdict":"approved","findings":[{"id":"A","severity":"blocker","title":"x"}]}`))
	if r.Effective() != VerdictChangesRequested {
		t.Fatal("an approval with a blocker is a request for changes")
	}
	r, _ = Parse(reply(`{"verdict":"approved","findings":[{"id":"A","severity":"minor","title":"x"}]}`))
	if r.Effective() != VerdictApproved {
		t.Fatal("minor findings do not block")
	}
}

func TestValidateLocations(t *testing.T) {
	d, _ := gitdiff.Parse("diff --git a/src/a.go b/src/a.go\n--- a/src/a.go\n+++ b/src/a.go\n@@ -1,2 +1,3 @@\n line1\n+added\n line3\n")
	lines := map[string]int{"src/a.go": 3, "src/b.go": 10}
	r := Result{Findings: []Finding{
		{ID: "in-diff", File: "src/a.go", Line: 2, Side: "new"},
		{ID: "context", File: "src/a.go", Line: 1, Side: "new"},
		{ID: "outside-hunk", File: "src/b.go", Line: 5, Side: "new"},
		{ID: "past-end", File: "src/a.go", Line: 40, Side: "new"},
		{ID: "no-file", File: "src/missing.go", Line: 1, Side: "new"},
		{ID: "no-line", File: "src/a.go", Line: 0, Side: "new"},
		{ID: "general"},
	}}
	Validate(&r, Context{Diff: d, LineCount: func(p string) int {
		if n, ok := lines[p]; ok {
			return n
		}
		return -1
	}})
	want := map[string][2]bool{"in-diff": {true, true}, "context": {true, true}, "outside-hunk": {true, false}, "past-end": {false, false}, "no-file": {false, false}, "no-line": {false, false}, "general": {false, false}}
	for _, f := range r.Findings {
		if w := want[f.ID]; f.LocationValid != w[0] || f.InlineEligible != w[1] {
			t.Errorf("%s: valid=%v inline=%v, want %v", f.ID, f.LocationValid, f.InlineEligible, w)
		}
	}
}
