package server

import (
	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/catherd"
	"time"
)

// Role execution states (H10).
const (
	RexPlanned         = "planned"
	RexDispatching     = "dispatching"
	RexDispatched      = "dispatched"
	RexFinished        = "finished"
	RexCollected       = "collected"
	RexCancelRequested = "cancel_requested"
	RexCancelled       = "cancelled"
	RexFailed          = "failed"
	RexAmbiguous       = "ambiguous"
)

// rexLive reports whether a role execution may still be running in catherd.
func rexLive(state string) bool {
	switch state {
	case RexDispatching, RexDispatched, RexCancelRequested:
		return true
	}
	return false
}

// rexTerminal reports whether nothing more will happen to a role execution.
func rexTerminal(state string) bool {
	switch state {
	case RexCollected, RexCancelled, RexFailed:
		return true
	}
	return false
}

// RexCatherd is what catherd's record says about a finished role.
type RexCatherd struct {
	RunID        string         `bson:"run_id" json:"run_id"`
	DispatchID   string         `bson:"dispatch_id" json:"dispatch_id"`
	Thread       string         `bson:"thread" json:"thread"`
	Rung         string         `bson:"rung" json:"rung"`
	Backend      string         `bson:"backend" json:"backend"`
	Family       string         `bson:"family" json:"family"`
	FailoverFrom string         `bson:"failover_from" json:"failover_from"`
	Attempt      int            `bson:"attempt" json:"attempt"`
	Status       string         `bson:"status" json:"status"`
	ReplyStatus  string         `bson:"reply_status" json:"reply_status"`
	ReplyWhy     string         `bson:"reply_why" json:"reply_why"`
	Environment  string         `bson:"environment" json:"environment"`
	Secs         float64        `bson:"secs" json:"secs"`
	Tokens       catherd.Tokens `bson:"tokens" json:"tokens"`
	CostUSD      *float64       `bson:"cost_usd" json:"cost_usd"`
	ChangedOwned []string       `bson:"changed_owned" json:"changed_owned"`
	Violations   []string       `bson:"violations" json:"violations"`
	ExitReason   string         `bson:"exit_reason" json:"exit_reason"`
	ReplyPath    string         `bson:"reply_path" json:"reply_path"`
}

// RexHost links a role execution to the directive turn that dispatched it.
type RexHost struct {
	SessionID   string `bson:"session_id" json:"session_id"`
	TurnID      string `bson:"turn_id" json:"turn_id"`
	DirectiveID string `bson:"directive_id" json:"directive_id"`
}

// RoleExecDoc is one dispatch of one role of a run (collection role_executions).
type RoleExecDoc struct {
	ID              string      `bson:"_id"`
	OrgID           string      `bson:"org_id"`
	RunID           string      `bson:"run_id"`
	Attempt         int         `bson:"attempt"`
	Stage           string      `bson:"stage"`
	Round           int         `bson:"round"`
	Role            string      `bson:"role"`
	Name            string      `bson:"name"`
	Lane            string      `bson:"lane"`
	BriefArtifactID string      `bson:"brief_artifact_id"`
	BriefSHA        string      `bson:"brief_sha"`
	RolebriefVer    string      `bson:"rolebrief_version"`
	RequestedRung   string      `bson:"requested_rung"`
	State           string      `bson:"state"`
	Tries           int         `bson:"tries"`
	RetryConfirmed  bool        `bson:"retry_confirmed"`
	Climbed         bool        `bson:"climbed"`
	DispatchingAt   *time.Time  `bson:"dispatching_at"`
	DispatchedAt    *time.Time  `bson:"dispatched_at"`
	Catherd         *RexCatherd `bson:"catherd"`
	Host            *RexHost    `bson:"host"`
	HostTokens      *int64      `bson:"host_tokens"`
	Context         bson.M      `bson:"context,omitempty"` // packet summary (bytes, est tokens, knowledge versions)
	Skills          bson.M      `bson:"skills,omitempty"`  // skill manifest of the brief (included, excluded, bytes)
	ReplyArtifactID string      `bson:"reply_artifact_id"`
	ResultHints     []string    `bson:"result_hints"`
	CreatedAt       time.Time   `bson:"created_at"`
	UpdatedAt       time.Time   `bson:"updated_at"`
	Revision        int64       `bson:"revision"`
}

func nonNilStr(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// DTO is RoleExecutionDto.
func (d RoleExecDoc) DTO() bson.M {
	var cat, host, tokens, dispatching any
	if c := d.Catherd; c != nil {
		cat = bson.M{"run_id": c.RunID, "dispatch_id": c.DispatchID, "thread": c.Thread, "rung": c.Rung, "backend": c.Backend, "family": c.Family, "failover_from": c.FailoverFrom, "attempt": c.Attempt, "status": c.Status,
			"reply_status": c.ReplyStatus, "reply_why": c.ReplyWhy, "environment": c.Environment, "secs": c.Secs, "tokens": bson.M{"input": c.Tokens.Input, "cached": c.Tokens.Cached, "output": c.Tokens.Output}, "cost_usd": c.CostUSD,
			"changed_owned": nonNilStr(c.ChangedOwned), "violations": nonNilStr(c.Violations), "exit_reason": c.ExitReason}
	}
	if h := d.Host; h != nil {
		host = bson.M{"session_id": h.SessionID, "turn_id": h.TurnID, "directive_id": h.DirectiveID}
	}
	if d.HostTokens != nil {
		tokens = *d.HostTokens
	}
	if d.DispatchingAt != nil {
		dispatching = timestamp(*d.DispatchingAt)
	}
	var reply any
	if d.ReplyArtifactID != "" {
		reply = d.ReplyArtifactID
	}
	return bson.M{"id": d.ID, "run_id": d.RunID, "attempt": d.Attempt, "stage": d.Stage, "round": d.Round, "role": d.Role, "name": d.Name, "lane": d.Lane, "brief_artifact_id": d.BriefArtifactID, "brief_sha256": d.BriefSHA, "rolebrief_version": d.RolebriefVer,
		"requested_rung": d.RequestedRung, "state": d.State, "dispatching_at": dispatching, "catherd": cat, "host": host, "host_tokens": tokens, "reply_artifact_id": reply, "result_hints": nonNilStr(d.ResultHints),
		"created_at": timestamp(d.CreatedAt), "updated_at": timestamp(d.UpdatedAt)}
}

// Host session states.
const (
	HostStarting = "starting"
	HostActive   = "active"
	HostLost     = "lost"
	HostClosed   = "closed"
)

// LastDirective is the latest directive sent on a thread.
type LastDirective struct {
	ID       string `bson:"id" json:"id"`
	Tool     string `bson:"tool" json:"tool"`
	ArgsHash string `bson:"args_hash" json:"args_hash"`
	State    string `bson:"state" json:"state"` // sent | completed | unknown
	TurnID   string `bson:"turn_id" json:"turn_id"`
}

// HostSessionDoc is the Codex thread of one run (collection host_sessions).
type HostSessionDoc struct {
	ID               string         `bson:"_id"`
	OrgID            string         `bson:"org_id"`
	RunID            string         `bson:"run_id"`
	ThreadID         string         `bson:"thread_id"`
	CodexVersion     string         `bson:"codex_version"`
	Model            string         `bson:"model"`
	Effort           string         `bson:"effort"`
	State            string         `bson:"state"`
	Live             bool           `bson:"live"`
	RunnerOwner      string         `bson:"runner_owner"`
	LastDirective    *LastDirective `bson:"last_directive"`
	CatherdRunID     string         `bson:"catherd_run_id"`
	CatherdRunDirRel string         `bson:"catherd_run_dir_rel"`
	HostTokens       *int64         `bson:"host_tokens"`
	StartedAt        time.Time      `bson:"started_at"`
	UpdatedAt        time.Time      `bson:"updated_at"`
}

// DTO is HostSessionDto (shown on the roles tab).
func (d HostSessionDoc) DTO() bson.M {
	var tokens any
	if d.HostTokens != nil {
		tokens = *d.HostTokens
	}
	return bson.M{"id": d.ID, "thread_id": d.ThreadID, "codex_version": d.CodexVersion, "model": d.Model, "effort": d.Effort, "state": d.State, "catherd_run_id": d.CatherdRunID, "host_tokens": tokens, "started_at": timestamp(d.StartedAt)}
}

// SlotHolder identifies the run that holds a coding slot.
type SlotHolder struct {
	OrgID      string    `bson:"org_id" json:"org_id"`
	RunID      string    `bson:"run_id" json:"run_id"`
	AcquiredAt time.Time `bson:"acquired_at" json:"acquired_at"`
}

// SlotDoc is one global coding slot (collection coding_slots, _id "slot:<n>").
type SlotDoc struct {
	ID     string      `bson:"_id"`
	Holder *SlotHolder `bson:"holder"`
}

// RunSlot is the slot a run holds.
type RunSlot struct {
	ID         string    `bson:"id" json:"id"`
	AcquiredAt time.Time `bson:"acquired_at" json:"acquired_at"`
}

// RunLane is one lane of the architect's plan.
type RunLane struct {
	ID    string   `bson:"id" json:"id"`
	Title string   `bson:"title" json:"title"`
	Owns  []string `bson:"owns" json:"owns"`
	After []string `bson:"after" json:"after"`
}

// RunHeads are the commits a run talks about.
type RunHeads struct {
	Base     string `bson:"base" json:"base"`
	Head     string `bson:"head" json:"head"`
	Verified string `bson:"verified" json:"verified"`
	Reviewed string `bson:"reviewed" json:"reviewed"`
	Pushed   string `bson:"pushed" json:"pushed"`
	Remote   string `bson:"remote" json:"remote"`
}

// RunVerification summarizes the latest verification evidence.
type RunVerification struct {
	EvidenceID string `bson:"evidence_id" json:"evidence_id"`
	Head       string `bson:"head" json:"head"`
	Gate       string `bson:"gate" json:"gate"`
}

// RunReview summarizes the latest review.
type RunReview struct {
	Round          int                  `bson:"round" json:"round"`
	Verdict        string               `bson:"verdict" json:"verdict"`
	Head           string               `bson:"head" json:"head"`
	BlockingOpen   int                  `bson:"blocking_open" json:"blocking_open"`
	Independence   catherd.Independence `bson:"independence" json:"independence"`
	CoderFamilies  []string             `bson:"coder_families" json:"coder_families"`
	ReviewerFamily string               `bson:"reviewer_family" json:"reviewer_family"`
}

// HostStateDoc is the organization's Codex host probe result (collection host_states).
type HostStateDoc struct {
	ID        string     `bson:"_id"`
	OrgID     string     `bson:"org_id"`
	LastProbe *HostProbe `bson:"last_probe"`
	Spike     *HostSpike `bson:"spike_attested"`
	Revision  int64      `bson:"revision"`
}

// HostProbe is one probe of the app-server.
type HostProbe struct {
	Status       string    `bson:"status" json:"status"` // ok | failed
	CodexVersion string    `bson:"codex_version" json:"codex_version"`
	Login        string    `bson:"login" json:"login"`
	CatherdTools []string  `bson:"catherd_tools" json:"catherd_tools"`
	Fake         bool      `bson:"fake" json:"fake"`
	CheckedAt    time.Time `bson:"checked_at" json:"checked_at"`
	Reasons      []string  `bson:"reasons" json:"reasons"`
}

// HostSpike is the user's attestation that the real spike passed.
type HostSpike struct {
	At             time.Time `bson:"at" json:"at"`
	CodexVersion   string    `bson:"codex_version" json:"codex_version"`
	CatherdVersion string    `bson:"catherd_version" json:"catherd_version"`
	Note           string    `bson:"note" json:"note"`
}

func hostStateID(org string) string { return "hst_" + org[4:] }
