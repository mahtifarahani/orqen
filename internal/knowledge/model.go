// Package knowledge is the pure core of versioned knowledge (Sprint 5): the model, the status transitions, the claim
// gate, the never-auto rules, promotion, conflict detection, diff, injection lint, the deterministic author and
// compaction. It must not import internal/server and has no I/O.
package knowledge

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Type string

const (
	TypeBusiness   Type = "business"
	TypeProject    Type = "project"
	TypeSprint     Type = "sprint"
	TypeTask       Type = "task"
	TypeExperience Type = "experience"
)

type Status string

const (
	StatusDraft      Status = "draft"
	StatusVerified   Status = "verified"
	StatusConflicted Status = "conflicted"
	StatusSuperseded Status = "superseded"
	StatusArchived   Status = "archived"
)

type Verdict string

const (
	VerdictSupported    Verdict = "supported"
	VerdictUnsupported  Verdict = "unsupported"
	VerdictContradicted Verdict = "contradicted"
	VerdictUnchecked    Verdict = "unchecked"
)

// Verdict authors.
const (
	ByDeterministic = "deterministic"
	ByVerifierRole  = "verifier_role"
	ByUser          = "user"
)

// Evidence reference kinds (K8).
const (
	RefSourceVersion        = "source_version"
	RefArtifact             = "artifact"
	RefVerificationEvidence = "verification_evidence"
	RefReviewFinding        = "review_finding"
	RefMergeRequest         = "merge_request"
	RefRun                  = "run"
	RefUserAttestation      = "user_attestation"
)

// Source authorities.
const (
	AuthorityCanonicalExternal = "canonical_external"
	AuthorityOrqenRecord       = "orqen_record"
	AuthorityUser              = "user"
)

const (
	MaxClaimText   = 500
	MaxExcerpt     = 300
	MaxAttestation = 1000
	MaxTitle       = 200
	MaxBody        = 4000
)

type Evidence struct {
	RefKind    string `json:"ref_kind" bson:"ref_kind"`
	RefID      string `json:"ref_id" bson:"ref_id"`
	Locator    string `json:"locator,omitempty" bson:"locator,omitempty"`
	Excerpt    string `json:"excerpt,omitempty" bson:"excerpt,omitempty"`
	ExcerptSHA string `json:"excerpt_sha,omitempty" bson:"excerpt_sha,omitempty"`
}

type Template struct {
	Code   string            `json:"code" bson:"code"`
	Params map[string]string `json:"params,omitempty" bson:"params,omitempty"`
}

type Fact struct {
	Subject   string `json:"subject" bson:"subject"`
	Predicate string `json:"predicate" bson:"predicate"`
	Value     string `json:"value" bson:"value"`
}

type Claim struct {
	ID        string     `json:"id" bson:"id"`
	Text      string     `json:"text" bson:"text"`
	Template  *Template  `json:"template,omitempty" bson:"template,omitempty"`
	Fact      *Fact      `json:"fact,omitempty" bson:"fact,omitempty"`
	Critical  bool       `json:"critical,omitempty" bson:"critical,omitempty"`
	Evidence  []Evidence `json:"evidence" bson:"evidence"`
	Verdict   Verdict    `json:"verdict,omitempty" bson:"verdict,omitempty"`
	VerdictBy string     `json:"verdict_by,omitempty" bson:"verdict_by,omitempty"`
	Flags     []string   `json:"flags,omitempty" bson:"flags,omitempty"`
}

type Origin struct {
	Kind       string `json:"kind" bson:"kind"` // manual | proposal | rollback | resolution
	ProposalID string `json:"proposal_id,omitempty" bson:"proposal_id,omitempty"`
	Actor      string `json:"actor,omitempty" bson:"actor,omitempty"`
}

type Version struct {
	ID             string
	OrgID          string
	RecordID       string
	Number         int
	Type           Type
	ScopeKey       string
	ProjectID      string
	Title, Body    string
	Claims         []Claim
	ValidFrom      *time.Time
	ValidUntil     *time.Time
	Status         Status
	Supersedes     string
	SupersededBy   string
	RollbackOf     string
	SupersedesMany []string
	Origin         Origin
	EvidenceStale  bool
	InjectionFlags []string
	VerifiedAt     *time.Time
}

type Record struct {
	ID            string
	OrgID         string
	ScopeKey      string
	Key           string
	Type          Type
	Pinned        bool
	Critical      bool
	OpenConflicts int
}

var keyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,99}$`)

func ValidKey(k string) bool { return keyRe.MatchString(k) }

// Scope is a parsed scope_key: org | project:<prj> | sprint:<prj>:<sprint> | task:<run>.
type Scope struct{ Kind, Project, Sprint, Run string }

func ParseScope(key string) (Scope, error) {
	parts := strings.Split(key, ":")
	switch {
	case key == "org":
		return Scope{Kind: "org"}, nil
	case parts[0] == "project" && len(parts) == 2 && parts[1] != "":
		return Scope{Kind: "project", Project: parts[1]}, nil
	case parts[0] == "sprint" && len(parts) == 3 && parts[1] != "" && parts[2] != "":
		return Scope{Kind: "sprint", Project: parts[1], Sprint: parts[2]}, nil
	case parts[0] == "task" && len(parts) == 2 && parts[1] != "":
		return Scope{Kind: "task", Run: parts[1]}, nil
	}
	return Scope{}, fmt.Errorf("invalid scope key %q", key)
}

func (s Scope) Key() string {
	switch s.Kind {
	case "project":
		return "project:" + s.Project
	case "sprint":
		return "sprint:" + s.Project + ":" + s.Sprint
	case "task":
		return "task:" + s.Run
	}
	return "org"
}

func (s Scope) rank() int {
	return map[string]int{"org": 0, "project": 1, "sprint": 2, "task": 3}[s.Kind]
}

// Related reports whether two scopes are equal or in an ancestor/descendant relation of the chain
// org > project > sprint > task. Task scopes carry no project, so the projects of the versions decide.
func Related(a Scope, aProject string, b Scope, bProject string) bool {
	if a.Kind == "org" || b.Kind == "org" {
		return true
	}
	if a.Kind == b.Kind {
		return a.Key() == b.Key()
	}
	pa, pb := aProject, bProject
	if a.Project != "" {
		pa = a.Project
	}
	if b.Project != "" {
		pb = b.Project
	}
	return pa != "" && pa == pb
}

// ValidateTypeScope enforces the K4 table (a mismatch is 422 knowledge_scope_invalid).
func ValidateTypeScope(t Type, scopeKind string) error {
	ok := map[Type][]string{
		TypeBusiness:   {"org", "project"},
		TypeProject:    {"project"},
		TypeSprint:     {"sprint"},
		TypeTask:       {"task"},
		TypeExperience: {"project", "org"},
	}[t]
	for _, k := range ok {
		if k == scopeKind {
			return nil
		}
	}
	return fmt.Errorf("type %q cannot live in scope %q", t, scopeKind)
}

func ValidType(t Type) bool {
	switch t {
	case TypeBusiness, TypeProject, TypeSprint, TypeTask, TypeExperience:
		return true
	}
	return false
}
