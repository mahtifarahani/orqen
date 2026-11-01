// Package triage holds the pure triage logic: hard guards, risk classification and the final decision. It must not
// import internal/server. External issue text is only ever read here, never executed or applied.
package triage

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
)

// Outcomes of triage.
const (
	OutcomeExecute           = "execute"
	OutcomeNeedsInput        = "needs_input"
	OutcomeWaitingDependency = "waiting_dependency"
	OutcomeAlreadyAddressed  = "already_addressed"
)

// Reasons are the Inbox reasons triage can raise (SPEC section 8).
const (
	ReasonProjectMapping       = "project_mapping"
	ReasonUnsupportedToolchain = "unsupported_toolchain"
	ReasonMissingAcceptance    = "missing_acceptance"
	ReasonProtectedChange      = "protected_change"
	ReasonAmbiguousBehavior    = "ambiguous_behavior"
)

// MinAcceptanceRunes is the shortest acceptance text that counts.
const MinAcceptanceRunes = 20

// Finding is one thing triage found. The first guard finding decides; all are collected for the record.
type Finding struct {
	Order        int
	Outcome      string
	Reason       string
	QuestionCode string
	Params       map[string]any
	Detail       string
}

// Blocker is an inward "Blocks" link: another issue that blocks this one.
type Blocker struct {
	Key            string
	StatusCategory string
}
type ProtectedPattern struct{ Pattern, Mode string }

// GuardInput is the deterministic evidence.
type GuardInput struct {
	StatusCategory string
	Resolution     string
	Inaccessible   bool
	ProjectsClaim  int // how many projects matched this issue (2+ is ambiguous)
	// ReadinessBlockers are the blocker codes of the project's readiness.
	ReadinessBlockers []string
	HasApprovedTest   bool
	NeedsDocker       bool // the approved test command needs Docker
	DockerFailed      bool // the last preflight reported docker.engine not passing
	Blockers          []Blocker
	AcceptanceText    string // from the Jira field or section
	AcceptanceSource  string // field | section | none
	AnswerAcceptance  string // text of a missing_acceptance answer, if any
	TextForPaths      []string
	Protected         []ProtectedPattern
	// AckProtected is set once the user chose to proceed without the protected paths (the plan carries a constraint).
	AckProtected bool
}

var mappingBlocker = regexp.MustCompile(`^(primary_repo_missing|repo_.*|jira_.*|target_branch_missing|mapping_.*)$`)

// IsMappingBlocker says whether a readiness blocker code concerns intake mapping.
func IsMappingBlocker(code string) bool {
	code, _, _ = strings.Cut(code, ":")
	return mappingBlocker.MatchString(code)
}

// Guards runs the ordered hard guards and returns every finding in order.
func Guards(in GuardInput) []Finding {
	var out []Finding
	add := func(f Finding) { f.Order = len(out) + 1; out = append(out, f) }
	if in.StatusCategory == "done" || in.Resolution != "" {
		add(Finding{Outcome: OutcomeAlreadyAddressed, Reason: "already_addressed", QuestionCode: "already_addressed", Detail: "the issue is done or resolved"})
	}
	if in.Inaccessible {
		add(Finding{Outcome: OutcomeNeedsInput, Reason: ReasonProjectMapping, QuestionCode: "issue_inaccessible"})
	}
	if in.ProjectsClaim > 1 {
		add(Finding{Outcome: OutcomeNeedsInput, Reason: ReasonProjectMapping, QuestionCode: "issue_in_two_projects"})
	}
	var mapping []string
	for _, c := range in.ReadinessBlockers {
		if IsMappingBlocker(c) {
			mapping = append(mapping, c)
		}
	}
	if len(mapping) > 0 {
		add(Finding{Outcome: OutcomeNeedsInput, Reason: ReasonProjectMapping, QuestionCode: "mapping_incomplete", Params: map[string]any{"blockers": mapping}})
	}
	switch {
	case !in.HasApprovedTest:
		add(Finding{Outcome: OutcomeNeedsInput, Reason: ReasonUnsupportedToolchain, QuestionCode: "no_test_command"})
	case in.NeedsDocker && in.DockerFailed:
		add(Finding{Outcome: OutcomeNeedsInput, Reason: ReasonUnsupportedToolchain, QuestionCode: "docker_unavailable"})
	}
	var open []string
	for _, b := range in.Blockers {
		if b.StatusCategory != "done" {
			open = append(open, b.Key)
		}
	}
	if len(open) > 0 {
		add(Finding{Outcome: OutcomeWaitingDependency, Reason: "waiting_dependency", QuestionCode: "blocked_by", Params: map[string]any{"issues": open}})
	}
	text := strings.TrimSpace(in.AcceptanceText)
	if in.AcceptanceSource == "none" || in.AcceptanceSource == "" {
		text = ""
	}
	if text == "" {
		text = strings.TrimSpace(in.AnswerAcceptance)
	}
	if utf8.RuneCountInString(text) < MinAcceptanceRunes {
		add(Finding{Outcome: OutcomeNeedsInput, Reason: ReasonMissingAcceptance, QuestionCode: "missing_acceptance"})
	}
	if hits := ProtectedMatches(in.TextForPaths, in.Protected, "deny"); len(hits) > 0 && !in.AckProtected {
		add(Finding{Outcome: OutcomeNeedsInput, Reason: ReasonProtectedChange, QuestionCode: "protected_change", Params: map[string]any{"paths": hits}})
	}
	return out
}

var pathToken = regexp.MustCompile(`[\w.@-]+(?:/[\w.@-]+)+`)
var rootFileToken = regexp.MustCompile(`(?:^|[\s"'\x60(\[])(\.gitlab-ci\.yml|\.github|Dockerfile|docker-compose\.ya?ml|Makefile|\.env(?:\.[\w-]+)?|package\.json|go\.mod|pnpm-lock\.yaml)(?:$|[\s"'\x60)\].,;:])`)

// PathTokens finds path-like tokens and well-known root files in free text.
func PathTokens(texts []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimRight(p, ".,;:")
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, t := range texts {
		for _, m := range pathToken.FindAllString(t, -1) {
			add(m)
		}
		for _, m := range rootFileToken.FindAllStringSubmatch(t, -1) {
			add(m[1])
		}
	}
	return out
}

// ProtectedMatches returns the path tokens of the texts that match a protected pattern of the given mode.
func ProtectedMatches(texts []string, patterns []ProtectedPattern, mode string) []string {
	var hits []string
	seen := map[string]bool{}
	for _, tok := range PathTokens(texts) {
		for _, p := range patterns {
			if p.Mode != mode || seen[tok] {
				continue
			}
			if ok, _ := doublestar.Match(p.Pattern, tok); ok {
				hits = append(hits, tok)
				seen[tok] = true
			}
		}
	}
	return hits
}
