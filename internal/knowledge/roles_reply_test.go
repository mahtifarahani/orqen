package knowledge

import (
	"strings"
	"testing"
)

const writerOK = `thinking...
ORQEN-KNOWLEDGE v1
{"records":[{"type":"project","scope_kind":"project","key":"build-tool","title":"Build tool","body":"We use make.","claims":[
 {"text":"The build uses make.","evidence":[{"ref_id":"vev_1#check","excerpt":"make check"}]},
 {"text":"The cache lives in /tmp.","evidence":[{"ref_id":"ghost","excerpt":"x"}]},
 {"text":"No evidence at all.","evidence":[]}]}]}
STATUS: done`

func TestParseWriterReply(t *testing.T) {
	recs, err := ParseWriterReply(writerOK, map[string]string{"vev_1#check": RefVerificationEvidence})
	if err != nil || len(recs) != 1 {
		t.Fatalf("%v %v", recs, err)
	}
	cs := recs[0].Claims
	if len(cs) != 3 || cs[0].Verdict != "" || len(cs[0].Evidence) != 1 || cs[0].Evidence[0].RefKind != RefVerificationEvidence {
		t.Fatalf("known ref: %+v", cs[0])
	}
	if cs[1].Verdict != VerdictUnsupported || cs[1].Flags[0] != FlagUnknownRef {
		t.Fatalf("unknown ref ids make the claim unsupported: %+v", cs[1])
	}
	if cs[2].Verdict != VerdictUnsupported || cs[2].Flags[0] != FlagNoEvidence {
		t.Fatalf("no evidence: %+v", cs[2])
	}
	for _, bad := range []string{"no marker", WriterMarker + " {nope", WriterMarker + `{"records":[]}`,
		WriterMarker + `{"records":[{"type":"zzz","key":"ok-key","title":"t","claims":[{"text":"a"}]}]}`,
		WriterMarker + `{"records":[{"type":"task","key":"Bad Key","title":"t","claims":[{"text":"a"}]}]}`} {
		if _, err := ParseWriterReply(bad, nil); err == nil {
			t.Errorf("%q must not parse", bad)
		}
	}
	long := strings.Repeat("a", 5000)
	recs, err = ParseWriterReply(WriterMarker+`{"records":[{"type":"task","key":"k1","title":"`+long+`","body":"`+long+`","claims":[{"text":"`+long+`"}]}]}`, nil)
	if err != nil || len([]rune(recs[0].Title)) != MaxTitle || len([]rune(recs[0].Body)) != MaxBody || len([]rune(recs[0].Claims[0].Text)) != MaxClaimText {
		t.Fatalf("clipping: %v", err)
	}
}

func TestParseKVerifyReply(t *testing.T) {
	vs, err := ParseKVerifyReply(`x ORQEN-KVERIFY v1 {"claims":[{"claim_id":"c1","verdict":"supported","reason":"ok"},{"claim_id":"c2","verdict":"contradicted","reason":"no"}]}`)
	if err != nil || len(vs) != 2 || vs[1].Verdict != VerdictContradicted {
		t.Fatalf("%v %v", vs, err)
	}
	for _, bad := range []string{"", KVerifyMarker + `{"claims":[]}`, KVerifyMarker + `{"claims":[{"claim_id":"c1","verdict":"unchecked"}]}`,
		KVerifyMarker + `{"claims":[{"claim_id":"c1","verdict":"supported"},{"claim_id":"c1","verdict":"supported"}]}`} {
		if _, err := ParseKVerifyReply(bad); err == nil {
			t.Errorf("%q must not parse", bad)
		}
	}
}

func TestMergeVerdictsOnlyDowngrades(t *testing.T) {
	det := []ClaimVerdict{
		{ClaimID: "c1", Verdict: VerdictSupported}, {ClaimID: "c2", Verdict: VerdictUnsupported},
		{ClaimID: "c3", Verdict: VerdictContradicted}, {ClaimID: "c4", Verdict: VerdictSupported}, {ClaimID: "c5", Verdict: VerdictUnchecked},
	}
	role := []KVerdict{
		{"c1", VerdictContradicted, "r"}, {"c2", VerdictSupported, "r"}, {"c3", VerdictSupported, "r"},
		{"zz", VerdictContradicted, "r"}, {"c5", VerdictSupported, "r"},
	}
	got := MergeVerdicts(det, role)
	want := map[string]Verdict{"c1": VerdictContradicted, "c2": VerdictUnsupported, "c3": VerdictContradicted, "c4": VerdictSupported, "c5": VerdictUnchecked}
	if len(got) != len(det) {
		t.Fatalf("claims not mentioned by the draft are ignored: %v", got)
	}
	for _, g := range got {
		if g.Verdict != want[g.ClaimID] {
			t.Errorf("%s: %s, want %s", g.ClaimID, g.Verdict, want[g.ClaimID])
		}
	}
	if len(det[0].Reasons) != 0 {
		t.Fatal("input is not mutated")
	}
}
