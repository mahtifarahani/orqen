package knowledge

import "sort"

// Never-auto codes (K10).
const (
	NAConflictOpen      = "conflict_open"
	NAConflictDetected  = "conflict_detected"
	NACriticalOrPinned  = "critical_or_pinned"
	NAClaimNoEvidence   = "claim_without_evidence"
	NAClaimUnsupported  = "claim_unsupported"
	NAClaimContradicted = "claim_contradicted"
	NAFactMissing       = "fact_missing"
	NAInjectionSuspect  = "injection_suspect"
	NAEvidenceStale     = "evidence_stale"
	NASupersedesUser    = "supersedes_user_verified"
	NABusinessNoSource  = "business_claim_without_canonical_source"
	NAPolicyNotAuto     = "policy_not_auto"
	NACompactionReview  = "compaction_requires_review"
	// NARolesAuthored: text written by the roles author never becomes verified without a person (S7).
	NARolesAuthored = "roles_authored_needs_review"
)

// AllNeverAuto lists every code (the UI needs an i18n text for each).
var AllNeverAuto = []string{NAConflictOpen, NAConflictDetected, NACriticalOrPinned, NAClaimNoEvidence, NAClaimUnsupported,
	NAClaimContradicted, NAFactMissing, NAInjectionSuspect, NAEvidenceStale, NASupersedesUser, NABusinessNoSource,
	NAPolicyNotAuto, NACompactionReview, NARolesAuthored}

// NeverAuto returns every reason that blocks automatic promotion of v. An empty result means auto promotion is not
// blocked by content; the policy decides the rest (Decide).
func NeverAuto(v Version, rec Record, g GateResult, conflicts []Pair, userVerifiedSuperseded bool) []string {
	set := map[string]bool{}
	if rec.OpenConflicts > 0 {
		set[NAConflictOpen] = true
	}
	if len(conflicts) > 0 {
		set[NAConflictDetected] = true
	}
	if rec.Pinned || rec.Critical {
		set[NACriticalOrPinned] = true
	}
	for _, c := range v.Claims {
		if c.Critical {
			set[NACriticalOrPinned] = true
		}
		if c.Template == nil && c.Fact == nil {
			set[NAFactMissing] = true
		}
		if has(c.Flags, "injection_suspect") {
			set[NAInjectionSuspect] = true
		}
		if c.Verdict == VerdictContradicted {
			set[NAClaimContradicted] = true
		}
	}
	if len(v.InjectionFlags) > 0 {
		set[NAInjectionSuspect] = true
	}
	if len(v.Claims) == 0 {
		set[NAClaimNoEvidence] = true
	}
	for _, cv := range g.Claims {
		if cv.Verdict == VerdictContradicted {
			set[NAClaimContradicted] = true
		}
		if cv.Verdict == VerdictSupported {
			continue
		}
		set[NAClaimUnsupported] = true
		for _, r := range cv.Reasons {
			switch r {
			case ReasonNoEvidence:
				set[NAClaimNoEvidence] = true
			case ReasonBusinessNoSource:
				set[NABusinessNoSource] = true
			case ReasonEvidenceStale:
				set[NAEvidenceStale] = true
			}
		}
	}
	if v.EvidenceStale {
		set[NAEvidenceStale] = true
	}
	if userVerifiedSuperseded {
		set[NASupersedesUser] = true
	}
	if len(v.SupersedesMany) > 0 {
		set[NACompactionReview] = true
	}
	if v.Origin.Kind == "roles" {
		set[NARolesAuthored] = true
	}
	out := make([]string, 0, len(set))
	for k, on := range set {
		if on {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
