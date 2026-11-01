package knowledge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Reply contracts of the roles author (K40). The roles are not dispatched yet (X37); these pure parsers and the
// verdict merge are the prerequisites, tested against fixtures.
const (
	WriterMarker    = "ORQEN-KNOWLEDGE v1"
	KVerifyMarker   = "ORQEN-KVERIFY v1"
	FlagUnknownRef  = "unknown_ref"
	FlagNoEvidence  = "no_evidence"
	maxWriterRecs   = 20
	maxWriterClaims = 40
)

// ErrReplyUnparseable means the reply has no valid block for the contract.
var ErrReplyUnparseable = errors.New("knowledge: reply has no valid block")

func clipRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

func decodeAfter(reply, marker string, out any) error {
	i := strings.LastIndex(reply, marker)
	if i < 0 {
		return ErrReplyUnparseable
	}
	if err := json.NewDecoder(bytes.NewReader([]byte(reply[i+len(marker):]))).Decode(out); err != nil {
		return ErrReplyUnparseable
	}
	return nil
}

// WriterRecord is one record proposed by the writer role.
type WriterRecord struct {
	Type      Type
	ScopeKind string
	Key       string
	Title     string
	Body      string
	Claims    []Claim
}

type writerReply struct {
	Records []struct {
		Type      string `json:"type"`
		ScopeKind string `json:"scope_kind"`
		Key       string `json:"key"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		Claims    []struct {
			Text     string `json:"text"`
			Fact     *Fact  `json:"fact"`
			Critical bool   `json:"critical"`
			Evidence []struct {
				RefID   string `json:"ref_id"`
				Excerpt string `json:"excerpt"`
			} `json:"evidence"`
		} `json:"claims"`
	} `json:"records"`
}

var writerTypes = map[Type]bool{TypeBusiness: true, TypeProject: true, TypeSprint: true, TypeTask: true, TypeExperience: true}

// ParseWriterReply decodes the writer's ORQEN-KNOWLEDGE v1 block. bundleRefs maps every ref_id of the evidence bundle
// to its reference kind. A claim that cites an id outside the bundle (or cites nothing) is marked unsupported with
// the flag unknown_ref (or no_evidence) instead of being dropped, so the user sees why. Texts are clipped to the
// limits and keys are validated.
func ParseWriterReply(reply string, bundleRefs map[string]string) ([]WriterRecord, error) {
	var r writerReply
	if err := decodeAfter(reply, WriterMarker, &r); err != nil {
		return nil, err
	}
	if len(r.Records) == 0 || len(r.Records) > maxWriterRecs {
		return nil, ErrReplyUnparseable
	}
	var out []WriterRecord
	for _, rec := range r.Records {
		t := Type(rec.Type)
		if !writerTypes[t] || !ValidKey(rec.Key) || strings.TrimSpace(rec.Title) == "" || len(rec.Claims) == 0 || len(rec.Claims) > maxWriterClaims {
			return nil, fmt.Errorf("%w: record %q", ErrReplyUnparseable, rec.Key)
		}
		d := WriterRecord{Type: t, ScopeKind: rec.ScopeKind, Key: rec.Key, Title: clipRunes(rec.Title, MaxTitle), Body: clipRunes(rec.Body, MaxBody)}
		for i, c := range rec.Claims {
			cl := Claim{ID: fmt.Sprintf("c%d", i+1), Text: clipRunes(strings.TrimSpace(c.Text), MaxClaimText), Fact: c.Fact, Critical: c.Critical, Evidence: []Evidence{}}
			if cl.Text == "" {
				return nil, fmt.Errorf("%w: empty claim text in %q", ErrReplyUnparseable, rec.Key)
			}
			bad := false
			for _, e := range c.Evidence {
				kind, ok := bundleRefs[e.RefID]
				if !ok {
					bad = true
					continue
				}
				cl.Evidence = append(cl.Evidence, Evidence{RefKind: kind, RefID: e.RefID, Excerpt: clipRunes(e.Excerpt, MaxExcerpt)})
			}
			switch {
			case bad:
				cl.Verdict, cl.VerdictBy, cl.Flags = VerdictUnsupported, ByDeterministic, []string{FlagUnknownRef}
			case len(cl.Evidence) == 0:
				cl.Verdict, cl.VerdictBy, cl.Flags = VerdictUnsupported, ByDeterministic, []string{FlagNoEvidence}
			}
			d.Claims = append(d.Claims, cl)
		}
		out = append(out, d)
	}
	return out, nil
}

// KVerdict is the verifier role's verdict on one claim.
type KVerdict struct {
	ClaimID string
	Verdict Verdict
	Reason  string
}

// ParseKVerifyReply decodes the ORQEN-KVERIFY v1 block. Verdicts other than supported, unsupported and contradicted
// are refused (the role cannot answer "unchecked" or invent a value).
func ParseKVerifyReply(reply string) ([]KVerdict, error) {
	var r struct {
		Claims []struct {
			ClaimID string `json:"claim_id"`
			Verdict string `json:"verdict"`
			Reason  string `json:"reason"`
		} `json:"claims"`
	}
	if err := decodeAfter(reply, KVerifyMarker, &r); err != nil {
		return nil, err
	}
	if len(r.Claims) == 0 {
		return nil, ErrReplyUnparseable
	}
	seen := map[string]bool{}
	var out []KVerdict
	for _, c := range r.Claims {
		v := Verdict(c.Verdict)
		if c.ClaimID == "" || seen[c.ClaimID] || (v != VerdictSupported && v != VerdictUnsupported && v != VerdictContradicted) {
			return nil, ErrReplyUnparseable
		}
		seen[c.ClaimID] = true
		out = append(out, KVerdict{ClaimID: c.ClaimID, Verdict: v, Reason: clipRunes(c.Reason, 500)})
	}
	return out, nil
}

func verdictRank(v Verdict) int {
	switch v {
	case VerdictSupported:
		return 3
	case VerdictUnchecked:
		return 2
	case VerdictUnsupported:
		return 1
	}
	return 0 // contradicted (and anything unknown) is the lowest
}

// MergeVerdicts applies the verifier role's verdicts to the deterministic ones. The verifier can only downgrade:
// the result for each claim is the lower of the two, and a claim the verifier did not mention keeps its verdict. A
// claim the verifier names but the draft does not contain is ignored.
func MergeVerdicts(det []ClaimVerdict, role []KVerdict) []ClaimVerdict {
	byID := map[string]KVerdict{}
	for _, k := range role {
		byID[k.ClaimID] = k
	}
	out := make([]ClaimVerdict, len(det))
	for i, d := range det {
		out[i] = ClaimVerdict{ClaimID: d.ClaimID, Verdict: d.Verdict, Reasons: append([]string{}, d.Reasons...)}
		k, ok := byID[d.ClaimID]
		if ok && verdictRank(k.Verdict) < verdictRank(d.Verdict) {
			out[i].Verdict = k.Verdict
			out[i].Reasons = append(out[i].Reasons, "verifier_role")
		}
	}
	return out
}
