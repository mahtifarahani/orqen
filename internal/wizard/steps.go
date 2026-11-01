// Package wizard defines the first-run setup steps and their completion criteria. It is pure. The wizard is a
// guide: its state never feeds the automation readiness gate (internal/readiness has no wizard input).
package wizard

import "time"

const (
	Todo    = "todo"
	Done    = "done"
	Skipped = "skipped"
)

// Steps in the fixed order of the wizard.
var Steps = []string{"organization", "connections", "projects", "profile", "policy", "system", "review"}

func ValidStep(key string) bool {
	for _, s := range Steps {
		if s == key {
			return true
		}
	}
	return false
}

// Consequences are the i18n codes (wizard.consequence.*) shown before a step is skipped.
func Consequences(step string) []string {
	switch step {
	case "connections":
		return []string{"discovery_unavailable", "automation_blocked"}
	case "projects":
		return []string{"automation_blocked"}
	case "profile":
		return []string{"automation_blocked", "no_runs", "jev_fallback_rules"}
	case "policy":
		return []string{"defaults_apply"}
	case "system":
		return []string{"issues_undetected"}
	}
	return []string{}
}

// ProjectInput is a project's open readiness blocker codes.
type ProjectInput struct{ Blockers []string }

// Inputs are filled by internal/server from its documents.
type Inputs struct {
	GitLabReady bool // an active GitLab connection is unattended-ready
	JiraReady   bool
	Projects    []ProjectInput
	// PreflightAt is the time of the last preflight report, nil when there is none.
	PreflightAt             *time.Time
	PreflightRequiredFailed bool
	OrgPolicyPublished      bool
	Now                     time.Time
}
type Missing struct{ Code, Detail string }

func has(list []string, code string) bool {
	for _, c := range list {
		if c == code {
			return true
		}
	}
	return false
}

var projectBlockers = []string{"primary_repo_missing", "target_branch_missing", "jira_mapping_missing", "jira_filter_missing", "mapping_not_checked", "mapping_check_stale", "mapping_check_failed", "contract_not_imported", "contract_not_reviewed", "test_command_missing", "command_not_approved"}
var profileBlockers = []string{"profile_not_bound", "profile_version_not_published", "profile_not_automation_ready"}

func configured(p ProjectInput) bool {
	for _, b := range projectBlockers {
		if has(p.Blockers, b) {
			return false
		}
	}
	return true
}

// Evaluate returns what is still missing for a step; an empty list means the criteria hold.
func Evaluate(step string, in Inputs) []Missing {
	var out []Missing
	switch step {
	case "connections":
		if !in.GitLabReady {
			out = append(out, Missing{"no_ready_gitlab", "no verified GitLab connection"})
		}
		if !in.JiraReady {
			out = append(out, Missing{"no_ready_jira", "no verified Jira connection"})
		}
	case "projects":
		ok := false
		for _, p := range in.Projects {
			ok = ok || configured(p)
		}
		if !ok {
			out = append(out, Missing{"no_configured_project", "no project is fully configured"})
		}
	case "profile":
		ok := false
		for _, p := range in.Projects {
			ready := configured(p)
			for _, b := range profileBlockers {
				ready = ready && !has(p.Blockers, b)
			}
			ok = ok || ready
		}
		if !ok {
			out = append(out, Missing{"no_ready_profile_bound", "no automation-ready profile version is bound to a configured project"})
		}
	case "policy":
		if !in.OrgPolicyPublished {
			out = append(out, Missing{"no_org_policy_version", "the organization has no published policy"})
		}
	case "system":
		switch {
		case in.PreflightAt == nil || in.Now.Sub(*in.PreflightAt) > 24*time.Hour:
			out = append(out, Missing{"no_recent_preflight", "no system check from the last 24 hours"})
		case in.PreflightRequiredFailed:
			out = append(out, Missing{"required_check_failed", "a required system check failed"})
		}
	}
	return out
}
