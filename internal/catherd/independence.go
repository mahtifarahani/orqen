package catherd

import "sort"

// Independence says whether the reviewers run on different models than the coders (D56).
type Independence struct {
	Status    string   `bson:"status" json:"status"` // independent, same_model, unknown or reviewer_disabled
	Shared    []string `bson:"shared" json:"shared"`
	Unmatched []string `bson:"unmatched" json:"unmatched"`
	// Reason explains an unknown status of ActualIndependence (same_thread, no_worker, no_reviewer).
	Reason string `bson:"reason,omitempty" json:"reason,omitempty"`
}

func rolesOf(doc map[string]any) map[string]any {
	r, _ := doc["roles"].(map[string]any)
	return r
}

// RungsOf lists a role's explicit rungs.
func RungsOf(doc map[string]any, role string) []string { return rungsOf(doc, role) }

// RoleEnabled reports whether a role is enabled (the default).
func RoleEnabled(doc map[string]any, role string) bool { return roleEnabled(doc, role) }
func rungsOf(doc map[string]any, role string) []string {
	rm, _ := rolesOf(doc)[role].(map[string]any)
	list, _ := rm["rungs"].([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
func roleEnabled(doc map[string]any, role string) bool {
	rm, ok := rolesOf(doc)[role].(map[string]any)
	if !ok {
		return true
	}
	e, present := rm["enabled"].(bool)
	return !present || e
}

// withStandIns adds each rung's failover stand-in.
func withStandIns(doc map[string]any, rungs []string) []string {
	fo, _ := doc["failover"].(map[string]any)
	out := append([]string{}, rungs...)
	for _, r := range rungs {
		if s, ok := fo[r].(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ComputeIndependence maps every worker and reviewer rung (and failover stand-in) to a catalog family.
// An unmatched rung gives unknown; a shared family gives same_model.
func ComputeIndependence(doc map[string]any, cat *Catalog) Independence {
	res := Independence{Shared: []string{}, Unmatched: []string{}}
	if !roleEnabled(doc, "reviewer") {
		res.Status = "reviewer_disabled"
		return res
	}
	families := func(rungs []string) map[string]bool {
		out := map[string]bool{}
		for _, r := range rungs {
			var fam string
			ok := false
			if cat != nil {
				fam, ok = cat.Family(r)
			}
			if !ok {
				res.Unmatched = append(res.Unmatched, r)
				continue
			}
			out[fam] = true
		}
		return out
	}
	coders := families(withStandIns(doc, rungsOf(doc, "worker")))
	reviewers := families(withStandIns(doc, rungsOf(doc, "reviewer")))
	for f := range reviewers {
		if coders[f] {
			res.Shared = append(res.Shared, f)
		}
	}
	sort.Strings(res.Shared)
	res.Unmatched = dedupe(res.Unmatched)
	switch {
	case len(res.Shared) > 0:
		res.Status = "same_model"
	case len(res.Unmatched) > 0 || len(reviewers) == 0:
		res.Status = "unknown"
	default:
		res.Status = "independent"
	}
	return res
}
func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// ActualIndependence judges a run from what actually ran: the rung of every worker record (failover stand-ins
// included, because a record shows the rung that ran) against the rung of every reviewer record. An unmatched rung
// gives unknown; a shared family gives same_model; a reviewer on a thread a worker used gives unknown with the
// reason same_thread.
func ActualIndependence(workers, reviewers []RunRecord, cat *Catalog) Independence {
	res := Independence{Shared: []string{}, Unmatched: []string{}}
	if len(workers) == 0 {
		res.Status, res.Reason = "unknown", "no_worker"
		return res
	}
	if len(reviewers) == 0 {
		res.Status, res.Reason = "unknown", "no_reviewer"
		return res
	}
	families := func(recs []RunRecord) map[string]bool {
		out := map[string]bool{}
		for _, r := range recs {
			var fam string
			ok := false
			if cat != nil {
				fam, ok = cat.Family(r.Rung)
			}
			if !ok {
				res.Unmatched = append(res.Unmatched, r.Rung)
				continue
			}
			out[fam] = true
		}
		return out
	}
	coders := families(workers)
	judges := families(reviewers)
	for f := range judges {
		if coders[f] {
			res.Shared = append(res.Shared, f)
		}
	}
	sort.Strings(res.Shared)
	res.Unmatched = dedupe(res.Unmatched)
	threads := map[string]bool{}
	for _, w := range workers {
		if w.Thread != nil {
			threads[*w.Thread] = true
		}
	}
	sameThread := false
	for _, r := range reviewers {
		if r.Thread != nil && threads[*r.Thread] {
			sameThread = true
		}
	}
	switch {
	case len(res.Shared) > 0:
		res.Status = "same_model"
	case len(res.Unmatched) > 0 || len(judges) == 0:
		res.Status = "unknown"
	case sameThread:
		res.Status, res.Reason = "unknown", "same_thread"
	default:
		res.Status = "independent"
	}
	return res
}
