package knowledge

import (
	"sort"
	"strings"
)

// Resolved is what a Resolver knows about an evidence reference.
type Resolved struct {
	Kind      string
	OrgID     string
	Authority string
	Text      string
	Fields    map[string]string
	Latest    bool
}

// Resolver resolves evidence refs. An unknown or out-of-scope ref returns an error. The server's resolver filters by
// org_id; the gate checks OrgID again.
type Resolver interface {
	Resolve(ref Evidence) (Resolved, error)
}

// Gate reason codes.
const (
	ReasonNoEvidence       = "claim_without_evidence"
	ReasonRefInvalid       = "evidence_ref_invalid"
	ReasonBusinessNoSource = "business_claim_without_canonical_source"
	ReasonTemplateMismatch = "template_param_mismatch"
	ReasonExcerptNotFound  = "excerpt_not_found"
	ReasonEvidenceStale    = "evidence_stale"
)

type ClaimVerdict struct {
	ClaimID string   `json:"claim_id"`
	Verdict Verdict  `json:"verdict"`
	Reasons []string `json:"reasons"`
}

type GateResult struct {
	Claims []ClaimVerdict `json:"claims"`
	Pass   bool           `json:"pass"`
}

// Failing returns the claim ids that are not supported.
func (g GateResult) Failing() []string {
	var out []string
	for _, c := range g.Claims {
		if c.Verdict != VerdictSupported {
			out = append(out, c.ClaimID)
		}
	}
	return out
}

func (g GateResult) Verdict(id string) (ClaimVerdict, bool) {
	for _, c := range g.Claims {
		if c.ClaimID == id {
			return c, true
		}
	}
	return ClaimVerdict{}, false
}

func isAttestation(e Evidence) bool { return e.RefKind == RefUserAttestation }

// CheckClaims runs the deterministic claim gate (K9) per claim, in order. It never contacts anything.
func CheckClaims(t Type, cs []Claim, r Resolver, org string) GateResult {
	res := GateResult{Claims: []ClaimVerdict{}, Pass: true}
	for _, c := range cs {
		cv := ClaimVerdict{ClaimID: c.ID, Reasons: []string{}}
		add := func(code string) {
			for _, x := range cv.Reasons {
				if x == code {
					return
				}
			}
			cv.Reasons = append(cv.Reasons, code)
		}
		if len(c.Evidence) == 0 {
			add(ReasonNoEvidence)
		}
		type got struct {
			ev Evidence
			rs Resolved
		}
		var resolved []got
		for _, e := range c.Evidence {
			rs, err := r.Resolve(e)
			if err != nil || rs.OrgID != org {
				add(ReasonRefInvalid)
				continue
			}
			resolved = append(resolved, got{e, rs})
		}
		if len(c.Evidence) > 0 && !has(cv.Reasons, ReasonRefInvalid) {
			if t == TypeBusiness {
				ok := false
				for _, g := range resolved {
					if isAttestation(g.ev) || (g.ev.RefKind == RefSourceVersion && g.rs.Authority == AuthorityCanonicalExternal) {
						ok = true
					}
				}
				if !ok {
					add(ReasonBusinessNoSource)
				}
			}
			if c.Template != nil {
				// A param is checked against every evidence that knows its key: some evidence must carry the same
				// value. A key no evidence knows is descriptive only, but at least one param must be verified.
				verified := 0
				for k, v := range c.Template.Params {
					known, equal := false, false
					for _, g := range resolved {
						if mv, ok := g.rs.Fields[k]; ok {
							known = true
							equal = equal || mv == v
						}
					}
					if known && !equal {
						add(ReasonTemplateMismatch)
					}
					if known && equal {
						verified++
					}
				}
				if len(c.Template.Params) > 0 && verified == 0 {
					add(ReasonTemplateMismatch)
				}
			} else {
				good, bad := false, false
				for _, g := range resolved {
					if isAttestation(g.ev) {
						good = true
						continue
					}
					if g.ev.Excerpt == "" {
						continue
					}
					n := NormText(g.ev.Excerpt)
					if n != "" && strings.Contains(NormText(g.rs.Text), n) &&
						(g.ev.ExcerptSHA == "" || g.ev.ExcerptSHA == SHA(n)) {
						good = true
					} else {
						bad = true
					}
				}
				if bad || !good {
					add(ReasonExcerptNotFound)
				}
			}
			for _, g := range resolved {
				if g.ev.RefKind == RefSourceVersion && !g.rs.Latest {
					add(ReasonEvidenceStale)
				}
			}
		}
		sort.Strings(cv.Reasons)
		cv.Verdict = VerdictSupported
		if len(cv.Reasons) > 0 {
			cv.Verdict = VerdictUnsupported
			res.Pass = false
		}
		res.Claims = append(res.Claims, cv)
	}
	if len(cs) == 0 {
		res.Pass = false
	}
	return res
}

func has(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Downgrade merges a role verdict into a gate verdict: a role may only downgrade (never upgrade), so the result is
// the worse of the two.
func Downgrade(det, role Verdict) Verdict {
	rank := map[Verdict]int{VerdictSupported: 0, VerdictUnchecked: 1, VerdictUnsupported: 2, VerdictContradicted: 3}
	if rank[role] > rank[det] {
		return role
	}
	return det
}
