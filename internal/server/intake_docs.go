package server

import (
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/intake"
	"time"
)

// TrackedIssueDoc is one Jira issue Orqen has seen, unique per (org, connection, issue id). The first project that
// sees an issue claims it.
type TrackedIssueDoc struct {
	ID                string    `bson:"_id"`
	OrgID             string    `bson:"org_id"`
	ProjectID         string    `bson:"project_id"`
	ConnectionID      string    `bson:"connection_id"`
	IssueID           string    `bson:"issue_id"`
	Key               string    `bson:"key"`
	URL               string    `bson:"url"`
	SummarySnapshot   string    `bson:"summary_snapshot"`
	StatusName        string    `bson:"status_name"`
	StatusCategory    string    `bson:"status_category"`
	Priority          string    `bson:"priority"`
	JiraUpdated       string    `bson:"jira_updated"`
	InFilter          bool      `bson:"in_filter"`
	LastSeenPoll      string    `bson:"last_seen_poll"`
	Candidate         bool      `bson:"candidate"`
	Inaccessible      bool      `bson:"inaccessible"`
	AmbiguousProject  []string  `bson:"ambiguous_project"`
	LatestSnapshotID  string    `bson:"latest_snapshot_id"`
	LatestRevision    int       `bson:"latest_revision"`
	NextRevision      int       `bson:"next_revision"`
	LatestMaterialFP  string    `bson:"latest_material_fp"`
	LatestContentHash string    `bson:"latest_content_hash"`
	ActiveRunID       *string   `bson:"active_run_id"`
	LatestRunID       *string   `bson:"latest_run_id"`
	RunState          string    `bson:"run_state"`
	SortKey           string    `bson:"sort_key"`
	LastActivityAt    time.Time `bson:"last_activity_at"`
	CreatedAt         time.Time `bson:"created_at"`
}

// taskSortKey orders newest activity first under an ascending index: (9e15 - ms) zero padded, then the id.
func taskSortKey(activity time.Time, id string) string {
	return fmt.Sprintf("%016d:%s", int64(9e15)-activity.UnixMilli(), id)
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// SummaryDTO is TaskSummaryDto; run is the issue's latest run, if any.
func (d TrackedIssueDoc) SummaryDTO(run *RunDoc, queuePos *int) bson.M {
	var r any
	if run != nil {
		r = run.SummaryDTO(queuePos)
	}
	return bson.M{"id": d.ID, "project_id": d.ProjectID, "connection_id": d.ConnectionID, "issue_id": d.IssueID, "issue_key": d.Key, "issue_url": d.URL, "summary": d.SummarySnapshot, "priority": d.Priority, "status_name": d.StatusName,
		"in_filter": d.InFilter, "candidate": d.Candidate, "inaccessible": d.Inaccessible, "ambiguous_project": nonNilStrings(d.AmbiguousProject), "last_seen_at": timestamp(d.LastActivityAt), "run": r, "security": nil}
}

// SnapshotContent is the stored issue content (PLAN-S3-GO S7).
type SnapshotComment struct {
	ID            string `bson:"id" json:"id"`
	AuthorDisplay string `bson:"author_display" json:"author_display"`
	Created       string `bson:"created" json:"created"`
	Body          string `bson:"body" json:"body"`
	Truncated     bool   `bson:"truncated" json:"truncated"`
}
type SnapshotLink struct {
	Type           string `bson:"type" json:"type"`
	Direction      string `bson:"direction" json:"direction"`
	ID             string `bson:"id" json:"id"`
	Key            string `bson:"key" json:"key"`
	Summary        string `bson:"summary" json:"summary"`
	StatusCategory string `bson:"status_category" json:"status_category"`
}
type SnapshotAttachment struct {
	ID       string `bson:"id" json:"id"`
	Filename string `bson:"filename" json:"filename"`
	Size     int64  `bson:"size" json:"size"`
	Mime     string `bson:"mime" json:"mime"`
}
type SnapshotParent struct {
	ID          string `bson:"id" json:"id"`
	Key         string `bson:"key" json:"key"`
	Summary     string `bson:"summary" json:"summary"`
	Description string `bson:"description" json:"description"`
}
type SnapshotStatus struct {
	Name     string `bson:"name" json:"name"`
	Category string `bson:"category" json:"category"`
}
type IssueSnapshotDoc struct {
	ID                   string               `bson:"_id"`
	OrgID                string               `bson:"org_id"`
	TrackedIssueID       string               `bson:"tracked_issue_id"`
	ConnectionID         string               `bson:"connection_id"`
	IssueID              string               `bson:"issue_id"`
	Revision             int                  `bson:"revision"`
	FetchedAt            time.Time            `bson:"fetched_at"`
	JiraUpdated          string               `bson:"jira_updated"`
	Summary              string               `bson:"summary"`
	Description          string               `bson:"description"`
	DescriptionTruncated bool                 `bson:"description_truncated"`
	Acceptance           intake.Acceptance    `bson:"acceptance"`
	IssueType            string               `bson:"issue_type"`
	Status               SnapshotStatus       `bson:"status"`
	Priority             string               `bson:"priority"`
	Resolution           string               `bson:"resolution"`
	Labels               []string             `bson:"labels"`
	Components           []string             `bson:"components"`
	Parent               *SnapshotParent      `bson:"parent"`
	Links                []SnapshotLink       `bson:"links"`
	Comments             []SnapshotComment    `bson:"comments"`
	Attachments          []SnapshotAttachment `bson:"attachments"`
	Refs                 intake.Refs          `bson:"refs"`
	MaterialFP           string               `bson:"material_fp"`
	ContentHash          string               `bson:"content_hash"`
}

func (d IssueSnapshotDoc) DTO() bson.M {
	parent := any(nil)
	if d.Parent != nil {
		parent = d.Parent
	}
	links, comments, atts := d.Links, d.Comments, d.Attachments
	if links == nil {
		links = []SnapshotLink{}
	}
	if comments == nil {
		comments = []SnapshotComment{}
	}
	if atts == nil {
		atts = []SnapshotAttachment{}
	}
	items := d.Acceptance.Items
	if items == nil {
		items = []string{}
	}
	conf, fig := d.Refs.Confluence, d.Refs.Figma
	if conf == nil {
		conf = []string{}
	}
	if fig == nil {
		fig = []string{}
	}
	return bson.M{"id": d.ID, "revision": d.Revision, "fetched_at": timestamp(d.FetchedAt), "jira_updated": d.JiraUpdated, "summary": d.Summary, "description": d.Description, "description_truncated": d.DescriptionTruncated,
		"acceptance": bson.M{"text": d.Acceptance.Text, "source": d.Acceptance.Source, "items": items}, "issue_type": d.IssueType, "status": d.Status, "priority": d.Priority,
		"labels": nonNilStrings(d.Labels), "components": nonNilStrings(d.Components), "parent": parent, "links": links, "comments": comments, "attachments": atts,
		"refs": bson.M{"confluence": conf, "figma": fig}, "material_fingerprint": d.MaterialFP}
}

// IntakeStateDoc is the polling state of one project (id "ins_" + the project's ULID).
type IntakeError struct {
	Code   string    `bson:"code" json:"code"`
	Detail string    `bson:"detail" json:"detail"`
	At     time.Time `bson:"at" json:"at"`
}
type IntakeBlocked struct {
	Code         string `bson:"code" json:"code"`
	ConnectionID string `bson:"connection_id" json:"connection_id"`
	MaterialHash string `bson:"material_hash" json:"material_hash"`
}
type IntakeCounts struct {
	Seen       int `bson:"seen" json:"seen"`
	Changed    int `bson:"changed" json:"changed"`
	NewRuns    int `bson:"new_runs" json:"new_runs"`
	LeftFilter int `bson:"left_filter" json:"left_filter"`
}
type IntakeStateDoc struct {
	ID                   string         `bson:"_id"`
	OrgID                string         `bson:"org_id"`
	ProjectID            string         `bson:"project_id"`
	NextPollAt           time.Time      `bson:"next_poll_at"`
	LastPollStartedAt    *time.Time     `bson:"last_poll_started_at"`
	LastSuccessStartedAt *time.Time     `bson:"last_success_started_at"`
	PollsSinceFull       int            `bson:"polls_since_full"`
	LastError            *IntakeError   `bson:"last_error"`
	WaitingNetwork       bool           `bson:"waiting_network"`
	Blocked              *IntakeBlocked `bson:"blocked"`
	LastCounts           *IntakeCounts  `bson:"last_counts"`
	ActiveSprints        []string       `bson:"active_sprints"`
}

func intakeStateID(project string) string { return "ins_" + project[4:] }

// DTO is IntakeStateDto. enabled, paused and nextMode come from the project, organization and policy.
func (d IntakeStateDoc) DTO(enabled, paused bool, nextMode string) bson.M {
	var lastErr, blocked, counts any
	if d.LastError != nil {
		lastErr = bson.M{"code": d.LastError.Code, "detail": d.LastError.Detail, "at": timestamp(d.LastError.At)}
	}
	if d.Blocked != nil {
		blocked = bson.M{"code": d.Blocked.Code, "connection_id": d.Blocked.ConnectionID}
	}
	if d.LastCounts != nil {
		counts = d.LastCounts
	}
	var next any
	if !d.NextPollAt.IsZero() {
		next = timestamp(d.NextPollAt)
	}
	var success any
	if d.LastSuccessStartedAt != nil {
		success = timestamp(*d.LastSuccessStartedAt)
	}
	return bson.M{"project_id": d.ProjectID, "enabled": enabled, "paused": paused, "next_mode": nextMode, "last_poll_started_at": optionalTime(d.LastPollStartedAt), "last_success_at": success, "next_poll_at": next,
		"last_error": lastErr, "waiting_network": d.WaitingNetwork, "blocked": blocked, "last_counts": counts, "active_sprints": nonNilStrings(d.ActiveSprints)}
}

type RunAttemptDoc struct {
	ID        string     `bson:"_id"`
	OrgID     string     `bson:"org_id"`
	RunID     string     `bson:"run_id"`
	Number    int        `bson:"number"`
	Reason    string     `bson:"reason"` // initial | retry
	FromStage string     `bson:"from_stage"`
	StartedAt time.Time  `bson:"started_at"`
	EndedAt   *time.Time `bson:"ended_at"`
	Outcome   string     `bson:"outcome"`
}

func (d RunAttemptDoc) DTO() bson.M {
	return bson.M{"number": d.Number, "reason": d.Reason, "from_stage": d.FromStage, "started_at": timestamp(d.StartedAt), "ended_at": optionalTime(d.EndedAt), "outcome": d.Outcome}
}
