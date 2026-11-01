package server

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"orqen/internal/policy"
	"orqen/internal/runstate"
	"time"
)

type RunCheckpoint struct {
	Stage string    `bson:"stage" json:"stage"`
	Since time.Time `bson:"since" json:"since"`
}
type RunHold struct {
	Code   string    `bson:"code" json:"code"`
	Detail string    `bson:"detail" json:"detail"`
	Since  time.Time `bson:"since" json:"since"`
}

// RunSource is one input of a run: the issue snapshot, a user answer or a constraint. Carried sources were copied
// from a superseded run.
type RunSource struct {
	ID            string  `bson:"id" json:"id"`
	Kind          string  `bson:"kind" json:"kind"`
	Label         string  `bson:"label" json:"label"`
	Ref           string  `bson:"ref" json:"ref"`
	ContentHash   string  `bson:"content_hash" json:"content_hash"`
	Text          *string `bson:"text" json:"text"`
	InboxID       *string `bson:"inbox_id" json:"inbox_id"`
	RevisionAdded int     `bson:"revision_added" json:"revision_added"`
	Carried       bool    `bson:"carried" json:"carried"`
	// Code names a structured answer ("missing_acceptance", "constraint", ...) so stages can read it without parsing text.
	Code string `bson:"code" json:"code"`
}
type RunPinned struct {
	PolicyVersionID  string  `bson:"policy_version_id" json:"policy_version_id"`
	ProfileVersionID *string `bson:"profile_version_id" json:"profile_version_id"`
	Host             string  `bson:"host" json:"host"`
	PacketCap        int     `bson:"packet_cap" json:"packet_cap"`
	// Skills are the approved skills pinned per role at run creation (Sprint 6); nil on older runs.
	Skills   map[string]PinnedSkillSet `bson:"skills,omitempty" json:"-"`
	SkillsFP string                    `bson:"skills_fp,omitempty" json:"-"`
}
type RunContext struct {
	ArtifactID         string    `bson:"artifact_id" json:"artifact_id"`
	ManifestArtifactID string    `bson:"manifest_artifact_id" json:"manifest_artifact_id"`
	Hash               string    `bson:"hash" json:"hash"`
	Bytes              int       `bson:"bytes" json:"bytes"`
	Cap                int       `bson:"cap" json:"cap"`
	SourcesRevision    int       `bson:"sources_revision" json:"sources_revision"`
	BuiltAt            time.Time `bson:"built_at" json:"built_at"`
}
type RunTriage struct {
	Outcome         string    `bson:"outcome" json:"outcome"`
	Reasons         []string  `bson:"reasons" json:"reasons"`
	Risk            string    `bson:"risk" json:"risk"`
	RiskReasons     []string  `bson:"risk_reasons" json:"risk_reasons"`
	Source          string    `bson:"source" json:"source"`
	DecisionID      *string   `bson:"decision_id" json:"decision_id"`
	SourcesRevision int       `bson:"sources_revision" json:"sources_revision"`
	DecidedAt       time.Time `bson:"decided_at" json:"decided_at"`
}
type RunPlan struct {
	ArtifactID string     `bson:"artifact_id" json:"artifact_id"`
	Hash       string     `bson:"hash" json:"hash"`
	Approved   bool       `bson:"approved" json:"approved"`
	ApprovedAt *time.Time `bson:"approved_at" json:"approved_at"`
}
type RunCancellation struct {
	State       string     `bson:"state" json:"state"`
	RequestedAt time.Time  `bson:"requested_at" json:"requested_at"`
	ConfirmedAt *time.Time `bson:"confirmed_at" json:"confirmed_at"`
}
type RunTerminal struct {
	Reason string    `bson:"reason" json:"reason"`
	At     time.Time `bson:"at" json:"at"`
}
type RunFailure struct {
	Code    string `bson:"code" json:"code"`
	Message string `bson:"message" json:"message"`
}

type RunDoc struct {
	ID              string           `bson:"_id"`
	OrgID           string           `bson:"org_id"`
	ProjectID       string           `bson:"project_id"`
	ConnectionID    string           `bson:"connection_id"`
	IssueID         string           `bson:"issue_id"`
	IssueKey        string           `bson:"issue_key"`
	TrackedIssueID  string           `bson:"tracked_issue_id"`
	IntentKey       string           `bson:"intent_key"`
	MaterialFP      string           `bson:"material_fp"`
	PolicyIntent    string           `bson:"policy_intent"`
	Origin          string           `bson:"origin"` // auto_intake | manual_evaluate | supersede
	State           string           `bson:"state"`
	Active          bool             `bson:"active"`
	Attempt         int              `bson:"attempt"`
	Priority        string           `bson:"priority"`
	Checkpoint      RunCheckpoint    `bson:"checkpoint"`
	Hold            *RunHold         `bson:"hold"`
	Eligibility     string           `bson:"eligibility"` // eligible | left_filter
	Stale           bool             `bson:"stale"`
	OpenInboxID     *string          `bson:"open_inbox_id"`
	SnapshotID      string           `bson:"snapshot_id"`
	SnapshotRev     int              `bson:"snapshot_rev"`
	Sources         []RunSource      `bson:"sources"`
	SourcesRevision int              `bson:"sources_revision"`
	Pinned          RunPinned        `bson:"pinned"`
	Context         *RunContext      `bson:"context"`
	Triage          *RunTriage       `bson:"triage"`
	Plan            *RunPlan         `bson:"plan"`
	RepoHeadSHA     *string          `bson:"repo_head_sha"`
	WorkspaceID     *string          `bson:"workspace_id"`
	QueuedAt        *time.Time       `bson:"queued_at"`
	DepCheckedAt    *time.Time       `bson:"dep_checked_at"`
	StartApproved   bool             `bson:"start_approved"`
	Cancellation    *RunCancellation `bson:"cancellation"`
	Terminal        *RunTerminal     `bson:"terminal"`
	Failure         *RunFailure      `bson:"failure"`
	// Sprint 4.
	CatherdRunID   string           `bson:"catherd_run_id"`
	CatherdRunDir  string           `bson:"catherd_run_dir"`
	HostSessionID  string           `bson:"host_session_id"`
	Lanes          []RunLane        `bson:"lanes"`
	Heads          RunHeads         `bson:"heads"`
	Round          int              `bson:"round"`
	FixCapExtra    int              `bson:"fix_cap_extra"`
	HostFaults     int              `bson:"host_faults"`
	FixNotes       []string         `bson:"fix_notes"`
	Waiver         *Waiver          `bson:"waiver"`
	AdoptRemote    bool             `bson:"adopt_remote"`
	AdoptSHA       string           `bson:"adopt_sha"`
	PlanRounds     int              `bson:"plan_rounds"`
	Verification   *RunVerification `bson:"verification"`
	Review         *RunReview       `bson:"review"`
	MergeRequestID string           `bson:"merge_request_id"`
	PublishMode    string           `bson:"publish_mode"`
	Slot           *RunSlot         `bson:"slot"`
	Supersedes     *string          `bson:"supersedes"`
	SupersededBy   *string          `bson:"superseded_by"`
	Revision       int64            `bson:"revision"`
	CreatedAt      time.Time        `bson:"created_at"`
	UpdatedAt      time.Time        `bson:"updated_at"`
}

func runFilter(org, id string) bson.M { return bson.M{"_id": id, "org_id": org} }

func (a *App) run(ctx context.Context, org, id string) (RunDoc, error) {
	return findOne[RunDoc](ctx, a.DB, "runs", runFilter(org, id))
}

func (d RunDoc) holdDTO() any {
	if d.Hold == nil {
		return nil
	}
	return bson.M{"code": d.Hold.Code, "detail": d.Hold.Detail}
}

// SummaryDTO is RunSummaryDto.
func (d RunDoc) SummaryDTO(queuePos *int) bson.M {
	var risk any
	if d.Triage != nil && d.Triage.Risk != "" {
		risk = d.Triage.Risk
	}
	var pos any
	if queuePos != nil {
		pos = *queuePos
	}
	elig := d.Eligibility
	if elig == "" {
		elig = "eligible"
	}
	return bson.M{"id": d.ID, "state": d.State, "stage": d.Checkpoint.Stage, "hold": d.holdDTO(), "eligibility": elig, "stale": d.Stale, "open_inbox_id": d.OpenInboxID, "risk": risk, "queue_position": pos, "attempt": d.Attempt,
		"updated_at": timestamp(d.UpdatedAt), "revision": d.Revision}
}

func sourcesDTO(in []RunSource) []bson.M {
	out := []bson.M{}
	for _, s := range in {
		out = append(out, bson.M{"id": s.ID, "kind": s.Kind, "label": s.Label, "ref": s.Ref, "content_hash": s.ContentHash, "text": s.Text, "inbox_id": s.InboxID, "revision_added": s.RevisionAdded, "carried": s.Carried, "code": s.Code})
	}
	return out
}

// DTO is RunDto: the run, its gates (from the pinned policy), workspace and attempts are attached by the handler.
func (d RunDoc) DTO() bson.M {
	return d.FullDTO(nil, nil, nil, nil)
}

// FullDTO builds RunDto. gates and workspace may be nil (zero value DTO for the contract test).
func (d RunDoc) FullDTO(gates bson.M, workspace bson.M, attempts []bson.M, queuePos *int) bson.M {
	s := d.SummaryDTO(queuePos)
	var ctxDTO, triage, plan, cancel, terminal, failure any
	if c := d.Context; c != nil {
		ctxDTO = bson.M{"artifact_id": c.ArtifactID, "manifest_artifact_id": c.ManifestArtifactID, "hash": c.Hash, "bytes": c.Bytes, "cap": c.Cap, "sources_revision": c.SourcesRevision, "built_at": timestamp(c.BuiltAt)}
	}
	if t := d.Triage; t != nil {
		triage = bson.M{"outcome": t.Outcome, "reasons": nonNilStrings(t.Reasons), "risk": t.Risk, "risk_reasons": nonNilStrings(t.RiskReasons), "source": t.Source, "decision_id": t.DecisionID, "sources_revision": t.SourcesRevision, "decided_at": timestamp(t.DecidedAt)}
	}
	if p := d.Plan; p != nil {
		plan = bson.M{"artifact_id": p.ArtifactID, "hash": p.Hash, "approved": p.Approved, "approved_at": optionalTime(p.ApprovedAt)}
	}
	if c := d.Cancellation; c != nil {
		cancel = bson.M{"state": c.State, "requested_at": timestamp(c.RequestedAt), "confirmed_at": optionalTime(c.ConfirmedAt)}
	}
	if t := d.Terminal; t != nil {
		terminal = bson.M{"reason": t.Reason, "at": timestamp(t.At)}
	}
	if f := d.Failure; f != nil {
		failure = bson.M{"code": f.Code, "message": f.Message}
	}
	if gates == nil {
		gates = bson.M{"plan_approval": "manual", "coding_start": "manual"}
	}
	if workspace == nil {
		s["workspace"] = nil
	} else {
		s["workspace"] = workspace
	}
	if attempts == nil {
		attempts = []bson.M{}
	}
	var queued any
	if d.QueuedAt != nil {
		queued = timestamp(*d.QueuedAt)
	}
	pinned := bson.M{"policy_version_id": d.Pinned.PolicyVersionID, "profile_version_id": d.Pinned.ProfileVersionID, "host": d.Pinned.Host, "packet_cap": d.Pinned.PacketCap}
	s["project_id"], s["connection_id"], s["issue_id"], s["issue_key"], s["tracked_issue_id"] = d.ProjectID, d.ConnectionID, d.IssueID, d.IssueKey, d.TrackedIssueID
	s["origin"], s["active"], s["priority"], s["snapshot_id"], s["snapshot_revision"] = d.Origin, d.Active, d.Priority, d.SnapshotID, d.SnapshotRev
	s["sources"], s["sources_revision"], s["pinned"], s["context"], s["triage"], s["plan"] = sourcesDTO(d.Sources), d.SourcesRevision, pinned, ctxDTO, triage, plan
	s["repo_head_sha"], s["workspace_id"], s["queued_at"], s["cancellation"], s["terminal"], s["failure"] = d.RepoHeadSHA, d.WorkspaceID, queued, cancel, terminal, failure
	s["supersedes"], s["superseded_by"], s["intent_key"], s["material_fingerprint"] = d.Supersedes, d.SupersededBy, d.IntentKey, d.MaterialFP
	s["gates"], s["attempts"], s["created_at"] = gates, attempts, timestamp(d.CreatedAt)
	s["catherd_run_id"], s["host_session_id"], s["merge_request_id"], s["publish_mode"] = optStr(d.CatherdRunID), optStr(d.HostSessionID), optStr(d.MergeRequestID), optStr(d.PublishMode)
	s["round"], s["fix_cap_extra"] = d.Round, d.FixCapExtra
	s["heads"] = bson.M{"base": d.Heads.Base, "head": d.Heads.Head, "verified": d.Heads.Verified, "reviewed": d.Heads.Reviewed, "pushed": d.Heads.Pushed, "remote": d.Heads.Remote}
	lanes := []bson.M{}
	for _, l := range d.Lanes {
		lanes = append(lanes, bson.M{"id": l.ID, "title": l.Title, "owns": nonNilStr(l.Owns), "after": nonNilStr(l.After)})
	}
	s["lanes"] = lanes
	s["verification"], s["review"], s["slot"], s["slot_queue_position"] = nil, nil, nil, nil
	if v := d.Verification; v != nil {
		s["verification"] = bson.M{"evidence_id": v.EvidenceID, "head": v.Head, "gate": v.Gate}
	}
	if r := d.Review; r != nil {
		s["review"] = bson.M{"round": r.Round, "verdict": r.Verdict, "head": r.Head, "blocking_open": r.BlockingOpen, "independence": bson.M{"status": r.Independence.Status, "reason": r.Independence.Reason, "coder_families": nonNilStr(r.CoderFamilies), "reviewer_family": r.ReviewerFamily, "shared": nonNilStr(r.Independence.Shared)}}
	}
	if sl := d.Slot; sl != nil {
		s["slot"] = bson.M{"id": sl.ID, "acquired_at": timestamp(sl.AcquiredAt)}
	}
	return s
}

// pinnedValues reads settings from the policy version a run pinned, falling back to the registry default.
type policyValues map[string]any

func (v policyValues) get(key string) any {
	if x, ok := v[key]; ok && x != nil {
		return x
	}
	if k, ok := policy.Lookup(key); ok {
		return k.Default
	}
	return nil
}
func (v policyValues) Str(key string) string { s, _ := v.get(key).(string); return s }
func (v policyValues) Bool(key string) bool  { b, _ := v.get(key).(bool); return b }
func (v policyValues) Int(key string) int {
	switch n := v.get(key).(type) {
	case int64:
		return int(n)
	case int32:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

// pinnedPolicy returns the settings of the policy version pinned at run creation.
func (a *App) pinnedPolicy(ctx context.Context, run RunDoc) (policyValues, error) {
	v, err := findOne[PolicyVersionDoc](ctx, a.DB, "policy_versions", bson.M{"_id": run.Pinned.PolicyVersionID, "org_id": run.OrgID})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return policyValues{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := policyValues{}
	for k, raw := range v.Resolved {
		if m := anyMap(raw); m != nil {
			out[k] = m["value"]
		}
	}
	return out, nil
}

// livePolicy resolves the published org and project policy now (intake is not a run setting).
func (a *App) livePolicy(ctx context.Context, org, project string) (policyValues, error) {
	eff, _, _, err := a.effectivePolicy(ctx, org, project)
	if err != nil {
		return nil, err
	}
	out := policyValues{}
	for _, e := range eff.Entries {
		out[e.Key] = e.Value
	}
	return out, nil
}

func (a *App) getRun(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	run, err := a.run(r.Context(), org, r.PathValue("runId"))
	if err != nil {
		return err
	}
	dto, err := a.runDTO(r.Context(), run)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(run.Revision))
	writeJSON(w, 200, dto)
	return nil
}

// runDTO assembles RunDto with the live parts: gates, workspace, attempts and queue position.
func (a *App) runDTO(ctx context.Context, run RunDoc) (bson.M, error) {
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return nil, err
	}
	gates := bson.M{"plan_approval": pv.Str("automation.plan_approval"), "coding_start": pv.Str("automation.coding_start")}
	var ws bson.M
	if run.WorkspaceID != nil {
		w, e := findOne[WorkspaceDoc](ctx, a.DB, "workspaces", bson.M{"_id": *run.WorkspaceID, "org_id": run.OrgID})
		if e == nil {
			ws = w.DTO()
		} else if !errors.Is(e, mongo.ErrNoDocuments) {
			return nil, e
		}
	}
	atts, err := findMany[RunAttemptDoc](ctx, a.DB, "run_attempts", bson.M{"org_id": run.OrgID, "run_id": run.ID}, bson.D{{Key: "number", Value: 1}}, 100)
	if err != nil {
		return nil, err
	}
	attDTO := []bson.M{}
	for _, x := range atts {
		attDTO = append(attDTO, x.DTO())
	}
	var pos *int
	if run.State == runstate.Queued && run.Active {
		m, e := a.queuePositions(ctx, run.OrgID)
		if e != nil {
			return nil, e
		}
		if p, ok := m[run.ID]; ok {
			pos = &p
		}
	}
	dto := run.FullDTO(gates, ws, attDTO, pos)
	if run.Slot == nil && run.Hold != nil && run.Hold.Code == runstate.HoldAwaitingCodingSlot {
		if q, e := a.slotQueue(ctx); e == nil {
			if p, ok := q[run.ID]; ok {
				dto["slot_queue_position"] = p
			}
		}
	}
	return dto, nil
}

// queuePositions numbers the org's queued runs by queued_at (1 is next). Sprint 4's dispatcher enforces the slot.
func (a *App) queuePositions(ctx context.Context, org string) (map[string]int, error) {
	runs, err := findMany[RunDoc](ctx, a.DB, "runs", bson.M{"org_id": org, "active": true, "state": runstate.Queued}, bson.D{{Key: "queued_at", Value: 1}, {Key: "_id", Value: 1}}, 500)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for i, r := range runs {
		out[r.ID] = i + 1
	}
	return out, nil
}

// runEvent describes the events a transition emits besides run.state_changed.
type runEvent struct {
	Reason string
	Actor  string // runstate.ActorSystem | ActorUser
	Extra  []eventSpec
}
type eventSpec struct {
	Type    string
	Payload bson.M
}

var errRunMoved = errors.New("the run moved")

// casRun is the only way a stage changes a run: a compare-and-swap on the revision and the checkpoint stage. A run
// that moved on (another worker, a cancel, a supersede) answers errRunMoved and the caller abandons its result.
// It must run inside a transaction (fenced). set may change state, checkpoint and active.
func (a *App) casRun(sc context.Context, r RunDoc, stage string, set bson.M, ev runEvent) (RunDoc, error) {
	if set == nil {
		set = bson.M{}
	}
	now := time.Now().UTC()
	set["updated_at"] = now
	to := r.State
	if s, ok := set["state"].(string); ok {
		to = s
		if !runstate.Allowed(r.State, to, ev.Actor) {
			return RunDoc{}, fmt.Errorf("run transition %s -> %s by %q is not allowed", r.State, to, ev.Actor)
		}
	}
	filter := bson.M{"_id": r.ID, "org_id": r.OrgID, "revision": r.Revision, "active": true}
	if stage != "" {
		filter["checkpoint.stage"] = stage
	}
	var next RunDoc
	err := a.DB.Coll("runs").FindOneAndUpdate(sc, filter, bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&next)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return RunDoc{}, errRunMoved
	}
	if err != nil {
		return RunDoc{}, err
	}
	rev := next.Revision
	if to != r.State || r.Checkpoint.Stage != next.Checkpoint.Stage {
		if err = a.AppendEvent(sc, r.OrgID, "run.state_changed", Subject{"run", r.ID}, bson.M{"from": r.State, "to": to, "stage": next.Checkpoint.Stage, "reason": ev.Reason, "actor": ev.Actor}, &rev, nil, ev.Actor != runstate.ActorUser); err != nil {
			return RunDoc{}, err
		}
	}
	for _, x := range ev.Extra {
		if err = a.AppendEvent(sc, r.OrgID, x.Type, Subject{"run", r.ID}, x.Payload, &rev, nil, ev.Actor != runstate.ActorUser); err != nil {
			return RunDoc{}, err
		}
	}
	if to != r.State {
		if _, err = a.DB.Coll("tracked_issues").UpdateOne(sc, bson.M{"_id": r.TrackedIssueID, "org_id": r.OrgID, "latest_run_id": r.ID}, bson.M{"$set": bson.M{"run_state": to}}); err != nil {
			return RunDoc{}, err
		}
	}
	if next.Slot != nil && (!next.Active || !runstate.SlotHolding(next.State)) {
		if err = a.releaseSlotTx(sc, next); err != nil {
			return RunDoc{}, err
		}
		next.Slot = nil
	}
	if v, ok := set["active"].(bool); ok && !v && r.Active && (r.WorkspaceID != nil || next.WorkspaceID != nil) {
		if err = a.enqueueCleanup(sc, r.OrgID); err != nil {
			return RunDoc{}, err
		}
	}
	if v, ok := set["active"].(bool); ok && !v && r.Active {
		if _, err = a.DB.Coll("tracked_issues").UpdateOne(sc, bson.M{"_id": r.TrackedIssueID, "org_id": r.OrgID, "active_run_id": r.ID}, bson.M{"$set": bson.M{"active_run_id": nil}}); err != nil {
			return RunDoc{}, err
		}
	}
	return next, nil
}

// anyMap reads a nested BSON document whatever type the driver decoded it into.
func anyMap(raw any) map[string]any {
	switch m := raw.(type) {
	case bson.M:
		return m
	case map[string]any:
		return m
	case bson.D:
		out := make(map[string]any, len(m))
		for _, e := range m {
			out[e.Key] = e.Value
		}
		return out
	}
	return nil
}

func optStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
