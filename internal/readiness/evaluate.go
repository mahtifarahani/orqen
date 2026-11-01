// Package readiness decides whether a project may run unattended. It is pure: internal/server fills Inputs
// from its documents. Inputs deliberately carry no setup-wizard state; skipping a wizard step never changes the result.
package readiness

import (
	"sort"
	"strings"
)

const (
	StateDraft          = "draft"
	StateReady          = "ready"
	StateEnabled        = "enabled"
	StateEnabledBlocked = "enabled_blocked"
)

type Issue struct {
	Code   string
	Detail string
	Tab    string
}

// ConnReasonCredentialMissing is set on a connection whose credential reference no longer resolves in this install's
// credential store (after a restore on another machine). It keeps the connection from being unattended-ready.
const ConnReasonCredentialMissing = "credential_missing"

type Connection struct {
	Ready   bool
	Reasons []string
}
type CatalogRow struct {
	Present  bool
	Gone     bool
	Archived bool
}
type MappingCheck struct {
	Hash   string
	Failed []string
}
type Command struct {
	Key      string
	Kind     string
	Approved bool
}
type Profile struct {
	Published       bool
	ReviewerEnabled bool
	VerifierEnabled bool
	// Independence is independent, same_model, unknown or reviewer_disabled.
	Independence string
	BudgetChosen bool
	InCatherd    bool
	UsedBackends []string
	// BoundHost is the orchestration host the bound version was validated and built for.
	BoundHost string
	// JevAuto: the version routes with Jev (jev.use=auto).
	JevAuto bool
	// IsolatedBackends are the keyed backends (grok, cursor, antigravity) the version runs on an isolated harness.
	IsolatedBackends []string
}

// Secret is a stored key's state without its value: whether it is set and its last check (ok, refused, unchecked,
// unverifiable; empty when never checked).
type Secret struct {
	Set    bool
	Status string
}
type DoctorCheck struct{ ID, State string }
type Doctor struct{ Checks []DoctorCheck }

type Inputs struct {
	Enabled bool

	HasPrimaryRepo bool
	TargetBranch   string
	RepoConnection Connection
	Catalog        CatalogRow

	HasJira        bool
	JiraConnection Connection
	JiraHasBoard   bool
	JiraHasJQL     bool

	MappingHash string
	Check       *MappingCheck

	ConfluenceRefs int
	// ConfluenceUnsynced counts page references with no stored source version; DocsStale counts verified knowledge versions whose evidence changed (K41).
	ConfluenceUnsynced int
	DocsStale          int
	// ConfluenceVerified counts page references whose last sync succeeded; ConfluenceSyncFailed counts those whose last
	// sync failed (S7, K41). KnowledgeIndex is "" or "degraded": the wiki extension is usable but its vault is degraded
	// or behind. None of them is ever a blocker.
	ConfluenceVerified   int
	ConfluenceSyncFailed int
	KnowledgeIndex       string
	Commands             []Command

	LatestContract   string
	ReviewedContract string

	Profile *Profile

	CatherdAvailable bool
	CatherdSupported bool
	// Doctor is the report of the effective host.
	Doctor *Doctor

	// EffectiveHost is the organization's orchestration host; HostAdapterAvailable says whether Orqen can execute on it.
	EffectiveHost        string
	HostAdapterAvailable bool

	// Host is the probe state of the Codex host (nil: never probed). It only matters for the codex host.
	Host *HostFacts
	// Publishing: the write mode, whether this project's repository is allowlisted and its write credential.
	PublishLive       bool
	Allowlisted       bool
	WriteCredential   string // none | set | ok | refused | insufficient | unchecked
	ReadHasWriteScope bool

	// Jev and BackendKeys (by backend) are the organization's stored keys.
	Jev         Secret
	BackendKeys map[string]Secret
}

// HostFacts are the results of the host probe.
type HostFacts struct {
	Probed        bool
	Fake          bool
	SpikeAttested bool
	// Reasons are specific blocker codes the probe found (host_codex_missing, host_codex_too_old, host_login_missing,
	// host_catherd_mcp_missing).
	Reasons []string
}

type Result struct {
	Ready    bool
	State    string
	Blockers []Issue
	Warnings []Issue
}

func (r Result) HasBlocker(code string) bool {
	for _, b := range r.Blockers {
		if b.Code == code {
			return true
		}
	}
	return false
}

func Evaluate(in Inputs) Result {
	var blockers, warnings []Issue
	block := func(code, tab, detail string) { blockers = append(blockers, Issue{code, detail, tab}) }
	warn := func(code, tab, detail string) { warnings = append(warnings, Issue{code, detail, tab}) }

	if !in.HasPrimaryRepo {
		block("primary_repo_missing", "mapping", "the project has no primary repository")
	} else {
		if !in.RepoConnection.Ready {
			block("repo_connection_not_ready", "mapping", strings.Join(in.RepoConnection.Reasons, ","))
		}
		switch {
		case !in.Catalog.Present || in.Catalog.Gone:
			block("repo_not_in_catalog", "mapping", "the repository is not in the refreshed catalog")
		case in.Catalog.Archived:
			block("repo_archived", "mapping", "the repository is archived")
		}
		if strings.TrimSpace(in.TargetBranch) == "" {
			block("target_branch_missing", "mapping", "the primary repository has no target branch")
		}
	}
	if !in.HasJira {
		block("jira_mapping_missing", "mapping", "no Jira project is mapped")
	} else {
		if !in.JiraConnection.Ready {
			block("jira_connection_not_ready", "mapping", strings.Join(in.JiraConnection.Reasons, ","))
		}
		if !in.JiraHasBoard && !in.JiraHasJQL {
			block("jira_filter_missing", "mapping", "choose a board or write a JQL filter")
		}
	}
	switch {
	case in.Check == nil:
		block("mapping_not_checked", "mapping", "the mapping has not been checked")
	case in.Check.Hash != in.MappingHash:
		block("mapping_check_stale", "mapping", "the mapping changed since it was checked")
	case len(in.Check.Failed) > 0:
		block("mapping_check_failed", "mapping", strings.Join(in.Check.Failed, ","))
	}
	if in.ConfluenceRefs > in.ConfluenceVerified {
		warn("confluence_refs_unverified", "mapping", "Confluence references are not verified")
	}
	if in.ConfluenceSyncFailed > 0 {
		warn("confluence_ref_unreachable", "mapping", "a Confluence page could not be fetched")
	}
	if in.KnowledgeIndex == "degraded" {
		warn("knowledge_index_fallback", "mapping", "the knowledge index is degraded and searches use the database")
	}
	if in.ConfluenceUnsynced > 0 {
		warn("docs_not_synced", "mapping", "Confluence pages have not been synced yet")
	}
	if in.DocsStale > 0 {
		warn("docs_evidence_stale", "mapping", "verified knowledge cites a document that changed")
	}
	hasTest := false
	var unapproved []string
	for _, c := range in.Commands {
		if c.Kind == "test" && c.Approved {
			hasTest = true
		}
		if !c.Approved {
			unapproved = append(unapproved, c.Key)
		}
	}
	if !hasTest {
		block("test_command_missing", "commands", "no approved test command")
	}
	if len(unapproved) > 0 {
		block("command_not_approved", "commands", strings.Join(unapproved, ","))
	}
	switch {
	case in.LatestContract == "":
		block("contract_not_imported", "contract", "the repository contract has not been imported")
	case in.ReviewedContract != in.LatestContract:
		block("contract_not_reviewed", "contract", "the latest repository contract has not been reviewed")
	}
	if in.Profile == nil {
		block("profile_not_bound", "profile", "no model profile version is bound")
	} else {
		p := in.Profile
		if !p.Published {
			block("profile_version_not_published", "profile", "the bound version is not published")
		}
		if !p.ReviewerEnabled || !p.VerifierEnabled {
			block("profile_not_automation_ready", "profile", "reviewer_or_verifier_disabled")
		}
		if p.Independence != "independent" {
			block("profile_not_automation_ready", "profile", p.Independence)
		}
		if !p.BudgetChosen {
			block("profile_not_automation_ready", "profile", "budget_not_chosen")
		}
		if !p.InCatherd {
			block("bound_profile_missing_in_catherd", "profile", "the published profile is missing from catherd")
		}
		if p.BoundHost != in.EffectiveHost {
			block("profile_host_mismatch", "profile", p.BoundHost+" -> "+in.EffectiveHost)
		}
		// A missing Jev key only drops routing to lane kind and difficulty (SPEC section 9): a warning. An isolated
		// backend without a key cannot run at all: a blocker.
		if p.JevAuto {
			switch {
			case !in.Jev.Set:
				warn("jev_key_missing", "profile", "no Jev key is set")
			case in.Jev.Status == "refused":
				warn("jev_key_refused", "profile", "Jev refused the stored key")
			}
		}
		isolated := append([]string{}, p.IsolatedBackends...)
		sort.Strings(isolated)
		for _, b := range isolated {
			switch k := in.BackendKeys[b]; {
			case !k.Set:
				block("backend_key_missing:"+b, "profile", b)
			case k.Status == "refused":
				block("backend_key_refused:"+b, "profile", b)
			case k.Status != "ok":
				warn("backend_key_unverified:"+b, "profile", b)
			}
		}
	}
	switch {
	case in.EffectiveHost != "codex":
		if !in.HostAdapterAvailable {
			block("host_adapter_unavailable", "profile", in.EffectiveHost)
		}
	case in.Host == nil || !in.Host.Probed:
		block("host_not_probed", "profile", "probe the Codex host")
	case len(in.Host.Reasons) > 0:
		for _, r := range in.Host.Reasons {
			block(r, "profile", "")
		}
	case !in.Host.Fake && !in.Host.SpikeAttested:
		block("host_spike_unverified", "profile", "run the real-host spike and attest it")
	}
	if in.EffectiveHost == "codex" && in.Profile != nil {
		for _, b := range in.Profile.UsedBackends {
			if b != "codex" {
				block("host_backend_unsupported:"+b, "profile", b)
			}
		}
	}
	if !in.PublishLive {
		warn("publishing_dry_run", "profile", "writes to GitLab are simulated")
	} else if in.Allowlisted {
		switch in.WriteCredential {
		case "none", "":
			warn("write_credential_missing", "mapping", "")
		case "set", "unchecked":
			warn("write_credential_unchecked", "mapping", "")
		}
	}
	if in.ReadHasWriteScope {
		warn("read_credential_has_write_scope", "mapping", "the read credential could write; use a read-only token")
	}
	if !in.CatherdAvailable || !in.CatherdSupported {
		block("catherd_unavailable", "profile", "catherd is missing or its version is unsupported")
	}
	if in.Doctor == nil {
		block("catherd_doctor_missing", "profile", "catherd doctor has not been run")
	} else {
		used := map[string]bool{}
		if in.Profile != nil {
			for _, b := range in.Profile.UsedBackends {
				used[b] = true
			}
		}
		var failing []string
		anyWarn := false
		for _, c := range in.Doctor.Checks {
			if c.State == "warn" {
				anyWarn = true
			}
			if c.State != "fail" {
				continue
			}
			if c.ID == "config" || strings.HasPrefix(c.ID, "profile") || used[strings.TrimPrefix(c.ID, "backend:")] && strings.HasPrefix(c.ID, "backend:") {
				failing = append(failing, c.ID)
			}
		}
		sort.Strings(failing)
		if len(failing) > 0 {
			block("catherd_doctor_failing", "profile", strings.Join(failing, ","))
		}
		if anyWarn {
			warn("doctor_warnings", "profile", "catherd doctor reported warnings")
		}
	}
	state := StateDraft
	switch {
	case len(blockers) == 0 && in.Enabled:
		state = StateEnabled
	case len(blockers) == 0:
		state = StateReady
	case in.Enabled:
		state = StateEnabledBlocked
	}
	return Result{Ready: len(blockers) == 0, State: state, Blockers: blockers, Warnings: warnings}
}
