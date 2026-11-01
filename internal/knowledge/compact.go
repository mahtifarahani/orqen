package knowledge

import (
	"sort"
	"strconv"
	"strings"
)

// predicateFamily strips the per-run suffix: "fix_after_failure:abc" and "fix_after_failure@x" share a family.
func predicateFamily(p string) string {
	if i := strings.IndexAny(p, ":@"); i > 0 {
		return p[:i]
	}
	return p
}

// CompactionGroups groups verified experience versions of one scope that share fact.subject and predicate family;
// groups smaller than threshold are dropped. Output order is deterministic.
func CompactionGroups(vs []Version, threshold int) [][]Version {
	groups := map[string][]Version{}
	for _, v := range vs {
		if v.Type != TypeExperience || v.Status != StatusVerified {
			continue
		}
		for _, c := range v.Claims {
			if c.Fact == nil {
				continue
			}
			k := v.ScopeKey + "|" + normFact(c.Fact.Subject) + "|" + predicateFamily(normFact(c.Fact.Predicate))
			groups[k] = append(groups[k], v)
			break
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out [][]Version
	for _, k := range keys {
		g := groups[k]
		sort.Slice(g, func(i, j int) bool { return g[i].ID < g[j].ID })
		if len(g) >= threshold {
			out = append(out, g)
		}
	}
	return out
}

type Draft struct {
	Title, Body    string
	Claims         []Claim
	SupersedesMany []string
}

func evSet(es []Evidence) string {
	ks := make([]string, 0, len(es))
	for _, e := range es {
		ks = append(ks, e.RefKind+"/"+e.RefID+"/"+e.Locator)
	}
	sort.Strings(ks)
	return strings.Join(ks, ";")
}

// Consolidate merges versions into one draft: claims with the same fact and the same evidence set are merged, the
// evidence of claims with the same fact is otherwise kept per claim. The second result lists the merged version ids.
func Consolidate(vs []Version) (Draft, []string) {
	sorted := append([]Version{}, vs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	d := Draft{Claims: []Claim{}}
	seen := map[string]bool{}
	var ids []string
	for _, v := range sorted {
		ids = append(ids, v.ID)
		for _, c := range v.Claims {
			key := normFact(c.Text) + "|" + evSet(c.Evidence)
			if c.Fact != nil {
				key = normFact(c.Fact.Subject) + "|" + normFact(c.Fact.Predicate) + "|" + normFact(c.Fact.Value) + "|" + evSet(c.Evidence)
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			c.ID = "cl_" + strconv.Itoa(len(d.Claims)+1)
			c.Verdict, c.VerdictBy = "", ""
			d.Claims = append(d.Claims, c)
		}
	}
	if len(sorted) > 0 {
		d.Title = "Consolidated: " + sorted[0].Title
	}
	d.Body = "Consolidated from " + strconv.Itoa(len(ids)) + " experience versions."
	d.SupersedesMany = ids
	return d, ids
}
