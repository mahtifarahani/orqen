package catherd

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Meta is Orqen-only draft metadata; it is never sent to catherd.
type Meta struct {
	BudgetUncappedAck bool `bson:"budget_uncapped_ack" json:"budget_uncapped_ack"`
}

// Analysis is what Orqen can say about a draft without calling catherd.
type Analysis struct {
	Errors       []Issue
	Warnings     []Issue
	Independence Independence
}

func warning(code string, path []string, msg string) Issue {
	i := NewIssue("orqen", code, path, msg)
	i.Severity = "warning"
	return i
}

// patchable removes the keys a patch cannot carry: the profile name is Orqen's, schema is fixed.
func patchable(doc map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range doc {
		if k != "name" && k != "schema" {
			out[k] = v
		}
	}
	return out
}

// Analyze applies D58, D59, D56 and D60 to a draft. cat may be nil when no catalog snapshot exists.
func Analyze(doc map[string]any, cat *Catalog, meta Meta) Analysis {
	var a Analysis
	if !isOne(doc["schema"]) {
		a.Errors = append(a.Errors, NewIssue("orqen", "unsupported_field", []string{"schema"}, "the profile schema must be 1"))
	}
	a.Errors = append(a.Errors, ValidatePatch(patchable(doc))...)
	if cat == nil {
		a.Warnings = append(a.Warnings, warning("catalog_missing", []string{"roles"}, "no model catalog has been loaded; rungs cannot be checked"))
	}
	roles := rolesOf(doc)
	if rm, ok := roles["worker"].(map[string]any); ok && rm["enabled"] == false {
		a.Errors = append(a.Errors, NewIssue("orqen", "worker_disabled", []string{"roles", "worker", "enabled"}, "the worker role cannot be disabled"))
	}
	checkRung := func(path []string, rung string) {
		r, ok := ParseRung(rung)
		if !ok {
			return // ValidatePatch reports bad grammar
		}
		if r.Backend == "claude" {
			a.Errors = append(a.Errors, NewIssue("orqen", "claude_native_unsupported", path, "native claude rungs are not supported: their agents are never read by a Claude Code session started by Orqen; use claude-code:"))
			return
		}
		if cat != nil {
			if _, _, found := cat.Lookup(rung); !found {
				a.Errors = append(a.Errors, NewIssue("orqen", "rung_not_in_catalog", path, fmt.Sprintf("%s is not in the model catalog", rung)))
			}
		}
	}
	names := make([]string, 0, len(roles))
	for r := range roles {
		names = append(names, r)
	}
	sort.Strings(names)
	for _, role := range Roles {
		if !roleEnabled(doc, role) {
			continue
		}
		rungs := rungsOf(doc, role)
		if len(rungs) == 0 {
			a.Errors = append(a.Errors, NewIssue("orqen", "rungs_required", []string{"roles", role, "rungs"}, "an enabled role needs explicit rungs"))
		}
		for i, r := range rungs {
			checkRung([]string{"roles", role, "rungs", fmt.Sprint(i)}, r)
		}
		if rm, ok := roles[role].(map[string]any); ok {
			if d, ok := rm["defaultRung"].(string); ok && d != "" {
				found := false
				for _, r := range rungs {
					found = found || r == d
				}
				if !found {
					a.Errors = append(a.Errors, NewIssue("orqen", "default_rung_not_in_rungs", []string{"roles", role, "defaultRung"}, "the default rung must be one of the role's rungs"))
				}
			}
		}
	}
	if fo, ok := doc["failover"].(map[string]any); ok {
		keys := make([]string, 0, len(fo))
		for k := range fo {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s, ok := fo[k].(string); ok {
				checkRung([]string{"failover", k}, s)
			}
		}
	}
	a.Independence = ComputeIndependence(doc, cat)
	switch a.Independence.Status {
	case "same_model":
		a.Errors = append(a.Errors, NewIssue("orqen", "reviewer_not_independent", []string{"roles", "reviewer", "rungs"}, "the reviewer shares a model family with the coders: "+join(a.Independence.Shared)))
	case "unknown":
		a.Warnings = append(a.Warnings, warning("independence_unknown", []string{"roles", "reviewer", "rungs"}, "reviewer independence cannot be established: "+join(a.Independence.Unmatched)))
	}
	if b, _ := doc["budget"].(map[string]any); len(b) == 0 && !meta.BudgetUncappedAck {
		a.Warnings = append(a.Warnings, warning("budget_not_chosen", []string{"budget"}, "set a budget or explicitly choose no cap"))
	}
	return a
}
func isOne(v any) bool {
	switch x := v.(type) {
	case json.Number:
		return x.String() == "1"
	case float64:
		return x == 1
	case int:
		return x == 1
	case int32:
		return x == 1
	case int64:
		return x == 1
	}
	return false
}
func join(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
