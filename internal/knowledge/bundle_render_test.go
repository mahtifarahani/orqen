package knowledge

import (
	"strings"
	"testing"
)

func testBundle() Bundle {
	return Bundle{RunID: "run_1", IssueKey: "ORQ-1", Head: "abc", Merged: true,
		MR:           &BundleMR{RefID: "mr_1", IID: 7, MergeCommit: "def"},
		Acceptance:   []BundleAcceptance{{Idx: 0, Text: "it works"}},
		Verification: []BundleVerification{{RefID: "ve_2", Command: "go test", Round: 1, Outcome: "pass", Head: "abc", Files: []string{"b.go", "a.go"}}, {RefID: "ve_1", Command: "lint", Outcome: "fail"}},
		Review:       &BundleReview{RefID: "run_1", Verdict: "approved", Head: "abc"},
		Findings:     []BundleFinding{{RefID: "rf_1", Severity: "minor", Title: "nit", Round: 1, Status: "fixed"}}}
}

func TestRenderBundleIsDeterministicAndCitable(t *testing.T) {
	b := testBundle()
	a1, a2 := RenderBundle(b), RenderBundle(b)
	if a1 != a2 || len(a1) > MaxBundleRender {
		t.Fatal("render must be deterministic and bounded")
	}
	refs := BundleRefs(b)
	if len(refs) != 5 {
		t.Fatalf("refs: %v", refs)
	}
	for id := range refs {
		if !strings.Contains(a1, "ref_id="+id+" ") {
			t.Errorf("ref %s missing in render", id)
		}
	}
	// bounded even for a huge bundle
	for i := 0; i < 2000; i++ {
		b.Findings = append(b.Findings, BundleFinding{RefID: "rf_x", Title: strings.Repeat("t", 190)})
	}
	if len(RenderBundle(b)) > MaxBundleRender {
		t.Fatal("render exceeds the cap")
	}
}

func TestSplitVerdictsRoundTrip(t *testing.T) {
	recs := []WriterRecord{{Key: "alpha", Type: TypeProject, Title: "t", Claims: []Claim{{ID: "c1", Text: "x"}, {ID: "c2", Text: "y"}}},
		{Key: "beta", Type: TypeTask, Title: "u", Claims: []Claim{{ID: "c1", Text: "z"}}}}
	txt := RenderDraftForVerify(recs)
	for _, id := range []string{"alpha/c1", "alpha/c2", "beta/c1"} {
		if !strings.Contains(txt, "claim_id="+id+":") {
			t.Errorf("claim id %s missing", id)
		}
	}
	got := SplitVerdicts([]KVerdict{{ClaimID: "alpha/c2", Verdict: VerdictContradicted, Reason: "r"}, {ClaimID: "beta/c1", Verdict: VerdictSupported}, {ClaimID: "bogus"}, {ClaimID: "/c1"}})
	if len(got) != 2 || got["alpha"][0].ClaimID != "c2" || got["alpha"][0].Verdict != VerdictContradicted || got["beta"][0].ClaimID != "c1" {
		t.Fatalf("split: %+v", got)
	}
}

func TestNeverAutoRolesOrigin(t *testing.T) {
	v := Version{Claims: []Claim{{ID: "cl_1", Fact: &Fact{}}}, Origin: Origin{Kind: "roles"}}
	g := GateResult{Claims: []ClaimVerdict{{ClaimID: "cl_1", Verdict: VerdictSupported}}}
	if !has(NeverAuto(v, Record{}, g, nil, false), NARolesAuthored) {
		t.Fatal("roles origin must always carry roles_authored_needs_review")
	}
	v.Origin = Origin{Kind: "proposal"}
	if has(NeverAuto(v, Record{}, g, nil, false), NARolesAuthored) {
		t.Fatal("deterministic origin is unchanged")
	}
}
