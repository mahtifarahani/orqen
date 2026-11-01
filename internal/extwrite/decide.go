package extwrite

import "net/url"

// Action is a kind of external write.
type Action string

const (
	GitPush        Action = "git_push"
	MRCreate       Action = "mr_create"
	MRUpdate       Action = "mr_update"
	MRNote         Action = "mr_note"
	MRDiscussion   Action = "mr_discussion"
	JiraComment    Action = "jira_comment"
	JiraTransition Action = "jira_transition"
)

// Supported reports whether Sprint 4 can execute the action. Jira writes are reserved and always refused.
func (a Action) Supported() bool {
	switch a {
	case GitPush, MRCreate, MRUpdate, MRNote, MRDiscussion:
		return true
	}
	return false
}

// Outcome is the result of a decision.
type Outcome string

const (
	OutcomeExecute          Outcome = "execute"
	OutcomeDryRun           Outcome = "dry_run"
	OutcomeAwaitingApproval Outcome = "awaiting_approval"
	OutcomeRefused          Outcome = "refused"
	OutcomeRefusedEnv       Outcome = "refused_env"
)

// Reason codes.
const (
	ReasonKillSwitch     = "kill_switch"
	ReasonPaused         = "paused"
	ReasonActionRefused  = "action_not_supported"
	ReasonEnvGuard       = "env_guard"
	ReasonModeDryRun     = "mode_dry_run"
	ReasonNotAllowlisted = "not_allowlisted"
	ReasonNoCredential   = "write_credential_missing"
	ReasonPolicyOff      = "policy_off"
	ReasonNeedsApproval  = "needs_approval"
	ReasonApproved       = "approved"
	ReasonPolicyAuto     = "policy_auto"
	ModeLive             = "live"
	ModeDryRun           = "dry_run"
	PolicyManual         = "manual"
	PolicyAuto           = "auto"
	PolicyOff            = "off"
	reasonBadTarget      = "bad_target"
)

// OpView is what the gate needs to know about one operation.
type OpView struct {
	// OrgID, RunID and PolicyKey let the gate's loader find the settings, the run's pinned policy and an approval.
	OrgID, RunID, PolicyKey string
	Action                  Action
	ConnectionID            string
	ProviderProjectID       string
	// TargetURL is the URL the write would contact (repository URL for a push, API base for an MR call).
	TargetURL  string
	IntentHash string
}

// AllowEntry is one allowlisted (connection, project).
type AllowEntry struct{ ConnectionID, ProviderProjectID string }

// Settings are the organization's external write settings.
type Settings struct {
	Mode      string
	Allowlist []AllowEntry
	Paused    bool
}

// Approval is a user's approval of one exact intent hash.
type Approval struct{ IntentHash string }

// DecideInput is everything Decide looks at.
type DecideInput struct {
	Op            OpView
	Settings      Settings
	Scope         Scope
	KillSwitch    bool
	HasCredential bool
	// PolicyMode is the action policy: manual (needs an approval), auto, or off.
	PolicyMode string
	Approval   *Approval
}

// Decision can only be built by Decide: its fields are unexported, so a caller cannot forge an execute outcome.
type Decision struct {
	outcome Outcome
	reasons []string
	action  Action
	hash    string
	scope   Scope
}

func (d Decision) Outcome() Outcome   { return d.outcome }
func (d Decision) Reasons() []string  { return append([]string(nil), d.reasons...) }
func (d Decision) Action() Action     { return d.action }
func (d Decision) IntentHash() string { return d.hash }
func (d Decision) Scope() Scope       { return d.scope }

func isGit(a Action) bool { return a == GitPush }

// Decide checks, in order, and the first match wins:
// kill switch or pause, unsupported action, environment guard, mode, allowlist, credential, approval.
func Decide(in DecideInput) Decision {
	d := Decision{action: in.Op.Action, hash: in.Op.IntentHash, scope: in.Scope}
	out := func(o Outcome, reasons ...string) Decision { d.outcome, d.reasons = o, reasons; return d }
	switch {
	case in.KillSwitch:
		return out(OutcomeDryRun, ReasonKillSwitch)
	case in.Settings.Paused:
		return out(OutcomeDryRun, ReasonPaused)
	case !in.Op.Action.Supported():
		return out(OutcomeRefused, ReasonActionRefused)
	}
	if in.Scope != ScopeAnyHost {
		if err := CheckURL(ScopeLoopbackOnly, in.Op.TargetURL, isGit(in.Op.Action)); err != nil {
			return out(OutcomeRefusedEnv, ReasonEnvGuard)
		}
	} else if u, err := url.Parse(in.Op.TargetURL); err != nil || u.Host == "" {
		return out(OutcomeRefused, reasonBadTarget)
	}
	if in.Settings.Mode != ModeLive {
		return out(OutcomeDryRun, ReasonModeDryRun)
	}
	listed := false
	for _, e := range in.Settings.Allowlist {
		if e.ConnectionID == in.Op.ConnectionID && e.ProviderProjectID == in.Op.ProviderProjectID {
			listed = true
		}
	}
	if !listed {
		return out(OutcomeDryRun, ReasonNotAllowlisted)
	}
	if !in.HasCredential {
		return out(OutcomeDryRun, ReasonNoCredential)
	}
	switch in.PolicyMode {
	case PolicyOff:
		return out(OutcomeDryRun, ReasonPolicyOff)
	case PolicyAuto:
		return out(OutcomeExecute, ReasonPolicyAuto)
	}
	if in.Approval == nil || in.Approval.IntentHash == "" || in.Approval.IntentHash != in.Op.IntentHash {
		return out(OutcomeAwaitingApproval, ReasonNeedsApproval)
	}
	return out(OutcomeExecute, ReasonApproved)
}
