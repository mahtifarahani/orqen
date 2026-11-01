package wizard

import (
	"reflect"
	"testing"
	"time"
)

func codes(m []Missing) []string {
	out := []string{}
	for _, x := range m {
		out = append(out, x.Code)
	}
	return out
}
func TestStepsAndConsequences(t *testing.T) {
	if len(Steps) != 7 || Steps[0] != "organization" || Steps[6] != "review" || !ValidStep("policy") || ValidStep("nope") {
		t.Fatal(Steps)
	}
	if len(Consequences("organization")) != 0 || len(Consequences("review")) != 0 || !reflect.DeepEqual(Consequences("profile"), []string{"automation_blocked", "no_runs", "jev_fallback_rules"}) || !reflect.DeepEqual(Consequences("policy"), []string{"defaults_apply"}) {
		t.Fatal("consequences")
	}
	for _, s := range Steps {
		if Consequences(s) == nil {
			t.Fatal("nil consequences for", s)
		}
	}
}
func TestCriteria(t *testing.T) {
	now := time.Now()
	recent := now.Add(-time.Hour)
	old := now.Add(-25 * time.Hour)
	cases := []struct {
		step string
		in   Inputs
		want []string
	}{
		{"organization", Inputs{}, []string{}},
		{"review", Inputs{}, []string{}},
		{"connections", Inputs{}, []string{"no_ready_gitlab", "no_ready_jira"}},
		{"connections", Inputs{GitLabReady: true}, []string{"no_ready_jira"}},
		{"connections", Inputs{GitLabReady: true, JiraReady: true}, []string{}},
		{"projects", Inputs{}, []string{"no_configured_project"}},
		{"projects", Inputs{Projects: []ProjectInput{{Blockers: []string{"mapping_not_checked"}}}}, []string{"no_configured_project"}},
		{"projects", Inputs{Projects: []ProjectInput{{Blockers: []string{"profile_not_bound", "catherd_doctor_missing", "repo_connection_not_ready"}}}}, []string{}},
		{"profile", Inputs{Projects: []ProjectInput{{Blockers: []string{"profile_not_bound"}}}}, []string{"no_ready_profile_bound"}},
		{"profile", Inputs{Projects: []ProjectInput{{Blockers: []string{"profile_not_automation_ready"}}}}, []string{"no_ready_profile_bound"}},
		{"profile", Inputs{Projects: []ProjectInput{{Blockers: []string{"mapping_not_checked"}}}}, []string{"no_ready_profile_bound"}},
		{"profile", Inputs{Projects: []ProjectInput{{Blockers: []string{"catherd_doctor_missing"}}}}, []string{}},
		{"policy", Inputs{}, []string{"no_org_policy_version"}},
		{"policy", Inputs{OrgPolicyPublished: true}, []string{}},
		{"system", Inputs{Now: now}, []string{"no_recent_preflight"}},
		{"system", Inputs{Now: now, PreflightAt: &old}, []string{"no_recent_preflight"}},
		{"system", Inputs{Now: now, PreflightAt: &recent, PreflightRequiredFailed: true}, []string{"required_check_failed"}},
		{"system", Inputs{Now: now, PreflightAt: &recent}, []string{}},
	}
	for i, c := range cases {
		if got := codes(Evaluate(c.step, c.in)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%d %s: %v want %v", i, c.step, got, c.want)
		}
	}
}
