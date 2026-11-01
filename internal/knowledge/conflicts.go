package knowledge

import "strings"

// Conflict kinds (K15).
const (
	KindValueMismatch = "value_mismatch"
	KindNegation      = "negation"
	KindSourceChanged = "source_changed"
)

// Pair is one conflicting pair of claims. B is the existing (or newer-source) side.
type Pair struct {
	AVersion string `json:"a_version"`
	AClaim   string `json:"a_claim"`
	BVersion string `json:"b_version"`
	BClaim   string `json:"b_claim"`
	Kind     string `json:"kind"`
}

func normFact(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

func overlaps(a, b Version) bool {
	if a.ValidUntil != nil && b.ValidFrom != nil && !a.ValidUntil.After(*b.ValidFrom) {
		return false
	}
	if b.ValidUntil != nil && a.ValidFrom != nil && !b.ValidUntil.After(*a.ValidFrom) {
		return false
	}
	return true
}

func scopeOf(v Version) Scope {
	s, err := ParseScope(v.ScopeKey)
	if err != nil {
		return Scope{Kind: "org"}
	}
	return s
}

// DetectConflicts compares the facts of a candidate with the existing verified versions. Versions of the same record
// are skipped (the candidate supersedes them). Facts compare after normalization; scopes must be equal or in an
// ancestor/descendant relation; validity must overlap.
func DetectConflicts(cand Version, existing []Version) []Pair {
	var out []Pair
	cs := scopeOf(cand)
	for _, ex := range existing {
		if ex.RecordID == cand.RecordID || ex.ID == cand.ID || ex.Status != StatusVerified {
			continue
		}
		if !Related(cs, cand.ProjectID, scopeOf(ex), ex.ProjectID) || !overlaps(cand, ex) {
			continue
		}
		for _, ac := range cand.Claims {
			if ac.Fact == nil {
				continue
			}
			for _, bc := range ex.Claims {
				if bc.Fact == nil || normFact(ac.Fact.Subject) != normFact(bc.Fact.Subject) {
					continue
				}
				ap, bp := normFact(ac.Fact.Predicate), normFact(bc.Fact.Predicate)
				av, bv := normFact(ac.Fact.Value), normFact(bc.Fact.Value)
				switch {
				case ap == bp && av != bv:
					out = append(out, Pair{cand.ID, ac.ID, ex.ID, bc.ID, KindValueMismatch})
				case ap == "not:"+bp && av == bv, bp == "not:"+ap && av == bv:
					out = append(out, Pair{cand.ID, ac.ID, ex.ID, bc.ID, KindNegation})
				}
			}
		}
	}
	return out
}

// DetectSourceChanged reports claims that cite a source version that is no longer the latest for its source.
func DetectSourceChanged(v Version, r Resolver) []Pair {
	var out []Pair
	for _, c := range v.Claims {
		for _, e := range c.Evidence {
			if e.RefKind != RefSourceVersion {
				continue
			}
			if rs, err := r.Resolve(e); err == nil && !rs.Latest {
				out = append(out, Pair{v.ID, c.ID, e.RefID, "", KindSourceChanged})
			}
		}
	}
	return out
}

// Fingerprint identifies an open conflict: sha(sorted version ids, kinds).
func Fingerprint(ps []Pair) string {
	set := map[string]bool{}
	for _, p := range ps {
		set["v:"+p.AVersion] = true
		set["v:"+p.BVersion] = true
		set["k:"+p.Kind] = true
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return SHA(strings.Join(keys, "|"))
}
