package readiness

import (
	"reflect"
	"strings"
	"testing"
)

func ready() Inputs {
	return Inputs{
		HasPrimaryRepo: true, TargetBranch: "main", RepoConnection: Connection{Ready: true}, Catalog: CatalogRow{Present: true},
		HasJira: true, JiraConnection: Connection{Ready: true}, JiraHasBoard: true,
		MappingHash: "h", Check: &MappingCheck{Hash: "h"},
		Commands:       []Command{{Key: "test", Kind: "test", Approved: true}},
		LatestContract: "rcs_1", ReviewedContract: "rcs_1",
		Profile:          &Profile{Published: true, ReviewerEnabled: true, VerifierEnabled: true, Independence: "independent", BudgetChosen: true, InCatherd: true, UsedBackends: []string{"codex"}, BoundHost: "codex"},
		CatherdAvailable: true, CatherdSupported: true, Doctor: &Doctor{Checks: []DoctorCheck{{"config", "ok"}, {"backend:codex", "ok"}}},
		EffectiveHost: "codex", HostAdapterAvailable: true, Host: &HostFacts{Probed: true, Fake: true}, PublishLive: true,
	}
}
func codes(r Result) []string {
	out := []string{}
	for _, b := range r.Blockers {
		out = append(out, b.Code)
	}
	return out
}
func TestReady(t *testing.T) {
	r := Evaluate(ready())
	if !r.Ready || r.State != StateReady || len(r.Blockers) != 0 {
		t.Fatal(r)
	}
	in := ready()
	in.Enabled = true
	if Evaluate(in).State != StateEnabled {
		t.Fatal("enabled")
	}
}
func TestBlockers(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Inputs)
		code   string
	}{
		{"no primary", func(i *Inputs) { i.HasPrimaryRepo = false }, "primary_repo_missing"},
		{"repo connection", func(i *Inputs) { i.RepoConnection = Connection{Reasons: []string{"not_tested"}} }, "repo_connection_not_ready"},
		{"gone", func(i *Inputs) { i.Catalog.Gone = true }, "repo_not_in_catalog"},
		{"absent", func(i *Inputs) { i.Catalog = CatalogRow{} }, "repo_not_in_catalog"},
		{"archived", func(i *Inputs) { i.Catalog.Archived = true }, "repo_archived"},
		{"branch", func(i *Inputs) { i.TargetBranch = " " }, "target_branch_missing"},
		{"jira", func(i *Inputs) { i.HasJira = false }, "jira_mapping_missing"},
		{"jira conn", func(i *Inputs) { i.JiraConnection.Ready = false }, "jira_connection_not_ready"},
		{"filter", func(i *Inputs) { i.JiraHasBoard = false }, "jira_filter_missing"},
		{"unchecked", func(i *Inputs) { i.Check = nil }, "mapping_not_checked"},
		{"stale", func(i *Inputs) { i.Check.Hash = "old" }, "mapping_check_stale"},
		{"failed", func(i *Inputs) { i.Check.Failed = []string{"jql"} }, "mapping_check_failed"},
		{"no test", func(i *Inputs) { i.Commands = nil }, "test_command_missing"},
		{"unapproved test", func(i *Inputs) { i.Commands[0].Approved = false }, "test_command_missing"},
		{"unapproved", func(i *Inputs) { i.Commands = append(i.Commands, Command{Key: "lint", Kind: "lint"}) }, "command_not_approved"},
		{"no contract", func(i *Inputs) { i.LatestContract = "" }, "contract_not_imported"},
		{"unreviewed", func(i *Inputs) { i.ReviewedContract = "" }, "contract_not_reviewed"},
		{"unbound", func(i *Inputs) { i.Profile = nil }, "profile_not_bound"},
		{"unpublished", func(i *Inputs) { i.Profile.Published = false }, "profile_version_not_published"},
		{"reviewer off", func(i *Inputs) { i.Profile.ReviewerEnabled = false }, "profile_not_automation_ready"},
		{"same model", func(i *Inputs) { i.Profile.Independence = "same_model" }, "profile_not_automation_ready"},
		{"unknown independence", func(i *Inputs) { i.Profile.Independence = "unknown" }, "profile_not_automation_ready"},
		{"no budget", func(i *Inputs) { i.Profile.BudgetChosen = false }, "profile_not_automation_ready"},
		{"missing in catherd", func(i *Inputs) { i.Profile.InCatherd = false }, "bound_profile_missing_in_catherd"},
		{"catherd", func(i *Inputs) { i.CatherdAvailable = false }, "catherd_unavailable"},
		{"unsupported", func(i *Inputs) { i.CatherdSupported = false }, "catherd_unavailable"},
		{"no doctor", func(i *Inputs) { i.Doctor = nil }, "catherd_doctor_missing"},
		{"host mismatch", func(i *Inputs) { i.EffectiveHost = "claude-code"; i.HostAdapterAvailable = true }, "profile_host_mismatch"},
		{"no adapter (other host)", func(i *Inputs) {
			i.EffectiveHost = "claude-code"
			i.Profile.BoundHost = "claude-code"
			i.HostAdapterAvailable = false
		}, "host_adapter_unavailable"},
		{"not probed", func(i *Inputs) { i.Host = nil }, "host_not_probed"},
		{"codex missing", func(i *Inputs) { i.Host = &HostFacts{Probed: true, Reasons: []string{"host_codex_missing"}} }, "host_codex_missing"},
		{"codex too old", func(i *Inputs) { i.Host = &HostFacts{Probed: true, Reasons: []string{"host_codex_too_old"}} }, "host_codex_too_old"},
		{"login missing", func(i *Inputs) { i.Host = &HostFacts{Probed: true, Reasons: []string{"host_login_missing"}} }, "host_login_missing"},
		{"catherd mcp missing", func(i *Inputs) { i.Host = &HostFacts{Probed: true, Reasons: []string{"host_catherd_mcp_missing"}} }, "host_catherd_mcp_missing"},
		{"real host without the spike", func(i *Inputs) { i.Host = &HostFacts{Probed: true} }, "host_spike_unverified"},
		{"non-codex backend", func(i *Inputs) { i.Profile.UsedBackends = append(i.Profile.UsedBackends, "opencode") }, "host_backend_unsupported:opencode"},
		{"isolated key missing", func(i *Inputs) { i.Profile.IsolatedBackends = []string{"grok"} }, "backend_key_missing:grok"},
		{"isolated key refused", func(i *Inputs) {
			i.Profile.IsolatedBackends = []string{"cursor"}
			i.BackendKeys = map[string]Secret{"cursor": {Set: true, Status: "refused"}}
		}, "backend_key_refused:cursor"},
		{"doctor config", func(i *Inputs) { i.Doctor.Checks = []DoctorCheck{{"config", "fail"}} }, "catherd_doctor_failing"},
		{"doctor used backend", func(i *Inputs) { i.Doctor.Checks = []DoctorCheck{{"backend:codex", "fail"}} }, "catherd_doctor_failing"},
	}
	for _, c := range cases {
		in := ready()
		in.Check = &MappingCheck{Hash: "h"}
		in.Commands = []Command{{Key: "test", Kind: "test", Approved: true}}
		p := *in.Profile
		in.Profile = &p
		d := *in.Doctor
		in.Doctor = &d
		c.mutate(&in)
		r := Evaluate(in)
		if r.Ready || !r.HasBlocker(c.code) || r.State != StateDraft {
			t.Errorf("%s: %v", c.name, codes(r))
		}
		in.Enabled = true
		if Evaluate(in).State != StateEnabledBlocked {
			t.Errorf("%s: enabled project with a blocker must be enabled_blocked", c.name)
		}
	}
}
func TestUnusedBackendFailureAndWarnings(t *testing.T) {
	in := ready()
	in.Doctor.Checks = []DoctorCheck{{"backend:opencode", "fail"}, {"jev", "warn"}}
	in.ConfluenceRefs, in.ConfluenceVerified = 1, 0
	r := Evaluate(in)
	if !r.Ready || len(r.Warnings) != 2 {
		t.Fatal(codes(r), r.Warnings)
	}
}
func TestInputsCarryNoWizardState(t *testing.T) {
	var check func(t reflect.Type)
	check = func(typ reflect.Type) {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if strings.Contains(strings.ToLower(f.Name), "wizard") || strings.Contains(strings.ToLower(f.Name), "setup") {
				t.Fatal("readiness inputs must not depend on the setup wizard:", f.Name)
			}
			if f.Type.Kind() == reflect.Struct {
				check(f.Type)
			}
			if f.Type.Kind() == reflect.Pointer && f.Type.Elem().Kind() == reflect.Struct {
				check(f.Type.Elem())
			}
		}
	}
	check(reflect.TypeOf(Inputs{}))
}

func warnCodes(r Result) []string {
	out := []string{}
	for _, w := range r.Warnings {
		out = append(out, w.Code)
	}
	return out
}
func TestKeyEffects(t *testing.T) {
	// Jev: a warning only, and only when the bound version routes with Jev.
	in := ready()
	in.Profile.JevAuto = true
	r := Evaluate(in)
	if !r.Ready || !reflect.DeepEqual(warnCodes(r), []string{"jev_key_missing"}) {
		t.Fatal(r)
	}
	in.Jev = Secret{Set: true, Status: "refused"}
	if r = Evaluate(in); !r.Ready || !reflect.DeepEqual(warnCodes(r), []string{"jev_key_refused"}) {
		t.Fatal(r)
	}
	in.Jev = Secret{Set: true, Status: "ok"}
	if r = Evaluate(in); len(r.Warnings) != 0 {
		t.Fatal(r)
	}
	in = ready()
	in.Profile.JevAuto = false
	if r = Evaluate(in); len(r.Warnings) != 0 {
		t.Fatal("jev.use=off needs no key:", r)
	}
	// A backend key: missing and refused block; unverifiable and unchecked warn.
	in = ready()
	in.Profile.IsolatedBackends = []string{"grok", "antigravity"}
	in.BackendKeys = map[string]Secret{"grok": {Set: true, Status: "unverifiable"}}
	r = Evaluate(in)
	if r.Ready || !reflect.DeepEqual(codes(r), []string{"backend_key_missing:antigravity"}) || !reflect.DeepEqual(warnCodes(r), []string{"backend_key_unverified:grok"}) {
		t.Fatal(codes(r), warnCodes(r))
	}
	in.BackendKeys["antigravity"] = Secret{Set: true, Status: "ok"}
	in.BackendKeys["grok"] = Secret{Set: true, Status: "ok"}
	if r = Evaluate(in); !r.Ready || len(r.Warnings) != 0 {
		t.Fatal(r)
	}
}

func TestPublishingWarnings(t *testing.T) {
	in := ready()
	in.PublishLive = false
	r := Evaluate(in)
	if !r.Ready || !hasWarning(r, "publishing_dry_run") {
		t.Fatalf("dry-run must never block: %v", codes(r))
	}
	in.PublishLive, in.Allowlisted, in.WriteCredential = true, true, "none"
	if r = Evaluate(in); !r.Ready || hasWarning(r, "publishing_dry_run") || !hasWarning(r, "write_credential_missing") {
		t.Fatalf("%v", codes(r))
	}
	in.WriteCredential = "set"
	if r = Evaluate(in); !hasWarning(r, "write_credential_unchecked") {
		t.Fatal(codes(r))
	}
	in.WriteCredential = "ok"
	if r = Evaluate(in); hasWarning(r, "write_credential_missing") || hasWarning(r, "write_credential_unchecked") {
		t.Fatal(codes(r))
	}
	in.PublishLive, in.WriteCredential = true, "none"
	in.Allowlisted = false
	if r = Evaluate(in); hasWarning(r, "write_credential_missing") {
		t.Fatal("only an allowlisted project warns about the credential")
	}
	in.ReadHasWriteScope = true
	if r = Evaluate(in); !hasWarning(r, "read_credential_has_write_scope") || !r.Ready {
		t.Fatal(codes(r))
	}
}

func hasWarning(r Result, code string) bool {
	for _, w := range r.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestCredentialMissingConnectionBlocksUnattendedUse(t *testing.T) {
	if ConnReasonCredentialMissing != "credential_missing" {
		t.Fatal("reason code")
	}
	in := ready()
	in.RepoConnection = Connection{Ready: false, Reasons: []string{ConnReasonCredentialMissing}}
	r := Evaluate(in)
	found := false
	for _, b := range r.Blockers {
		if b.Code == "repo_connection_not_ready" && b.Detail == "credential_missing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("blockers: %+v", r.Blockers)
	}
}

func TestDocsWarningsNeverBlock(t *testing.T) {
	has := func(r Result, code string) bool {
		for _, w := range r.Warnings {
			if w.Code == code {
				return true
			}
		}
		return false
	}
	in := ready()
	in.ConfluenceRefs, in.ConfluenceVerified = 2, 1
	r := Evaluate(in)
	if !r.Ready || !has(r, "confluence_refs_unverified") {
		t.Fatal("unverified ref warns, never blocks", codes(r), r.Warnings)
	}
	in.ConfluenceVerified = 2
	if r = Evaluate(in); has(r, "confluence_refs_unverified") || !r.Ready {
		t.Fatal("cleared once verified == refs", r.Warnings)
	}
	in.ConfluenceVerified, in.ConfluenceSyncFailed = 1, 1
	if r = Evaluate(in); !has(r, "confluence_ref_unreachable") || !r.Ready {
		t.Fatal("failed ref warns", r.Warnings)
	}
	in = ready()
	in.KnowledgeIndex = "degraded"
	if r = Evaluate(in); !has(r, "knowledge_index_fallback") || !r.Ready {
		t.Fatal("degraded index warns", r.Warnings)
	}
	in.KnowledgeIndex = ""
	if r = Evaluate(in); has(r, "knowledge_index_fallback") {
		t.Fatal("no warning when not degraded")
	}
}
