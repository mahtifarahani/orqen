package server

import (
	"context"
	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/knowledge"
	"path/filepath"
	"time"
)

// SourceVersionDoc is raw provenance: one fetched version of an external source (or a link-only reference). The text
// lives in a content-addressed artifact; it is data and never instructions.
type SourceVersionDoc struct {
	ID              string    `bson:"_id"`
	OrgID           string    `bson:"org_id"`
	ProjectID       *string   `bson:"project_id"`
	Kind            string    `bson:"kind"` // confluence_page | jira_issue | repo_document | run_evidence | merge_request | figma_link | user_note
	ConnectionID    string    `bson:"connection_id"`
	ExternalID      string    `bson:"external_id"`
	URL             string    `bson:"url"`
	Title           string    `bson:"title"`
	RemoteVersion   int       `bson:"remote_version"`
	RemoteUpdatedAt string    `bson:"remote_updated_at"`
	ContentHash     string    `bson:"content_hash"`
	RawHash         string    `bson:"raw_hash"`
	ArtifactID      string    `bson:"artifact_id"`
	Bytes           int       `bson:"bytes"`
	Truncated       bool      `bson:"truncated"`
	LinkOnly        bool      `bson:"link_only"`
	Authority       string    `bson:"authority"`
	SnapshotID      string    `bson:"snapshot_id,omitempty"`
	FetchedAt       time.Time `bson:"fetched_at"`
	PrevID          string    `bson:"prev_id"`
	Latest          bool      `bson:"latest"`
	Links           []string  `bson:"links,omitempty"`
}

type KnowledgeScope struct {
	Kind      string `bson:"kind" json:"kind"`
	ProjectID string `bson:"project_id" json:"project_id"`
	SprintID  string `bson:"sprint_id" json:"sprint_id"`
	RunID     string `bson:"run_id" json:"run_id"`
}

type KnowledgeRecordDoc struct {
	ID                string         `bson:"_id"`
	OrgID             string         `bson:"org_id"`
	ScopeKey          string         `bson:"scope_key"`
	Scope             KnowledgeScope `bson:"scope"`
	Type              string         `bson:"type"`
	Key               string         `bson:"key"`
	Title             string         `bson:"title"`
	CurrentVersionID  string         `bson:"current_version_id"`
	VerifiedVersionID *string        `bson:"verified_version_id"`
	Status            string         `bson:"status"` // draft | verified | conflicted | archived
	Pinned            bool           `bson:"pinned"`
	Critical          bool           `bson:"critical"`
	AppliesToRoles    []string       `bson:"applies_to_roles"`
	NextNumber        int            `bson:"next_number"`
	Active            bool           `bson:"active"`
	ArchiveReason     string         `bson:"archive_reason"`
	Revision          int64          `bson:"revision"`
	CreatedAt         time.Time      `bson:"created_at"`
	UpdatedAt         time.Time      `bson:"updated_at"`
}

type KnowledgeAttestation struct {
	ClaimID string    `bson:"claim_id" json:"claim_id"`
	Text    string    `bson:"text" json:"text"`
	By      string    `bson:"by" json:"by"`
	At      time.Time `bson:"at" json:"at"`
}

type KnowledgePromotion struct {
	Mode            string                 `bson:"mode"` // user | auto
	DecidedBy       string                 `bson:"decided_by"`
	PolicyVersionID string                 `bson:"policy_version_id"`
	At              time.Time              `bson:"at"`
	Note            string                 `bson:"note"`
	Attestations    []KnowledgeAttestation `bson:"attestations,omitempty"`
}

type KnowledgeVersionDoc struct {
	ID             string              `bson:"_id"`
	OrgID          string              `bson:"org_id"`
	RecordID       string              `bson:"record_id"`
	Number         int                 `bson:"number"`
	Type           string              `bson:"type"`
	ScopeKey       string              `bson:"scope_key"`
	ProjectID      string              `bson:"project_id"`
	Title          string              `bson:"title"`
	Body           string              `bson:"body"`
	Claims         []knowledge.Claim   `bson:"claims"`
	ValidFrom      *time.Time          `bson:"valid_from"`
	ValidUntil     *time.Time          `bson:"valid_until"`
	Status         string              `bson:"status"`
	Supersedes     string              `bson:"supersedes"`
	SupersededBy   string              `bson:"superseded_by"`
	RollbackOf     string              `bson:"rollback_of"`
	SupersedesMany []string            `bson:"supersedes_many"`
	Origin         knowledge.Origin    `bson:"origin"`
	Promotion      *KnowledgePromotion `bson:"promotion"`
	EvidenceStale  bool                `bson:"evidence_stale"`
	InjectionFlags []string            `bson:"injection_flags"`
	ContentHash    string              `bson:"content_hash"`
	VerifiedAt     *time.Time          `bson:"verified_at"`
	CreatedAt      time.Time           `bson:"created_at"`
	// RoleVerdicts are the knowledge verifier role's verdicts and the writer's own unsupported marks (S7). They can only
	// lower a gate verdict (knowledge.MergeVerdicts).
	RoleVerdicts []RoleVerdict `bson:"role_verdicts,omitempty"`
}

// RoleVerdict is one claim verdict given by a role.
type RoleVerdict struct {
	ClaimID string `bson:"claim_id" json:"claim_id"`
	Verdict string `bson:"verdict" json:"verdict"`
	Reason  string `bson:"reason" json:"reason"`
}

func (v KnowledgeVersionDoc) roleKVerdicts() []knowledge.KVerdict {
	out := make([]knowledge.KVerdict, 0, len(v.RoleVerdicts))
	for _, r := range v.RoleVerdicts {
		out = append(out, knowledge.KVerdict{ClaimID: r.ClaimID, Verdict: knowledge.Verdict(r.Verdict), Reason: r.Reason})
	}
	return out
}

type KnowledgeConflictDoc struct {
	ID          string           `bson:"_id"`
	OrgID       string           `bson:"org_id"`
	Fingerprint string           `bson:"fingerprint"`
	VersionIDs  []string         `bson:"version_ids"`
	RecordIDs   []string         `bson:"record_ids"`
	Pairs       []knowledge.Pair `bson:"pairs"`
	State       string           `bson:"state"` // open | resolved
	Resolution  *struct {
		Choice          string    `bson:"choice"`
		Note            string    `bson:"note"`
		By              string    `bson:"by"`
		At              time.Time `bson:"at"`
		ResultVersionID string    `bson:"result_version_id"`
	} `bson:"resolution"`
	Revision  int64     `bson:"revision"`
	CreatedAt time.Time `bson:"created_at"`
	UpdatedAt time.Time `bson:"updated_at"`
}

func (d KnowledgeRecordDoc) DTO() bson.M {
	return bson.M{"id": d.ID, "scope_key": d.ScopeKey, "scope": d.Scope, "type": d.Type, "key": d.Key, "title": d.Title,
		"current_version_id": d.CurrentVersionID, "verified_version_id": d.VerifiedVersionID, "status": d.Status, "pinned": d.Pinned,
		"critical": d.Critical, "applies_to_roles": nonNilStrings(d.AppliesToRoles), "active": d.Active, "archive_reason": d.ArchiveReason,
		"revision": d.Revision, "created_at": timestamp(d.CreatedAt), "updated_at": timestamp(d.UpdatedAt)}
}

// domain converts a stored version to the pure model.
func (v KnowledgeVersionDoc) domain() knowledge.Version {
	return knowledge.Version{ID: v.ID, OrgID: v.OrgID, RecordID: v.RecordID, Number: v.Number, Type: knowledge.Type(v.Type), ScopeKey: v.ScopeKey,
		ProjectID: v.ProjectID, Title: v.Title, Body: v.Body, Claims: v.Claims, ValidFrom: v.ValidFrom, ValidUntil: v.ValidUntil,
		Status: knowledge.Status(v.Status), Supersedes: v.Supersedes, SupersededBy: v.SupersededBy, RollbackOf: v.RollbackOf,
		SupersedesMany: v.SupersedesMany, Origin: v.Origin, EvidenceStale: v.EvidenceStale, InjectionFlags: v.InjectionFlags, VerifiedAt: v.VerifiedAt}
}

func (a *App) artifactRoot() string { return filepath.Join(a.Config.DataDir, "artifacts") }

// artifactText reads a text artifact of the org (capped).
func (a *App) artifactText(ctx context.Context, org, artID string, max int) (string, error) {
	art, err := findOne[ArtifactDoc](ctx, a.DB, "artifacts", bson.M{"_id": artID, "org_id": org})
	if err != nil {
		return "", err
	}
	b, err := readBlob(a.artifactRoot(), org, art.Sha256)
	if err != nil {
		return "", err
	}
	if max > 0 && len(b) > max {
		b = b[:max]
	}
	return string(b), nil
}
