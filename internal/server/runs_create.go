package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"orqen/internal/runstate"
	"strings"
	"time"
)

func runAdvanceKey(runID string) string { return "run:" + runID }

// enqueueRunAdvance adds the run's stage job unless one is already active, so a resubmit, a resume and the
// scheduler never create a second worker for a run. It must run inside the caller's transaction.
func (a *App) enqueueRunAdvance(sc context.Context, org, runID string) error {
	now := time.Now().UTC()
	n, err := a.DB.Coll("jobs").CountDocuments(sc, bson.M{"org_id": org, "dedupe_key": runAdvanceKey(runID), "active": true})
	if err != nil || n > 0 {
		return err
	}
	err = a.enqueue(sc, JobDoc{ID: newID("job"), OrgID: org, Kind: "run_advance", DedupeKey: runAdvanceKey(runID), Payload: bson.M{"run_id": runID}, State: "queued", Active: true, MaxAttempts: 1000, NextAt: now, CreatedAt: now, UpdatedAt: now})
	var dup *DuplicateActiveJob
	if errors.As(err, &dup) {
		return nil
	}
	return err
}

// cancelRunJob retires the run's active stage job; a running handler loses its lease and stops.
func (a *App) cancelRunJob(sc context.Context, org, runID string) error {
	_, err := a.DB.Coll("jobs").UpdateMany(sc, bson.M{"org_id": org, "dedupe_key": runAdvanceKey(runID), "active": true}, bson.M{"$set": bson.M{"state": "cancelled", "active": false, "updated_at": time.Now().UTC()}})
	return err
}

// priorityOf maps Jira's business priority to the Inbox priority.
func priorityOf(jira string) string {
	switch strings.ToLower(jira) {
	case "highest", "high", "blocker", "critical":
		return "high"
	case "medium", "normal":
		return "normal"
	}
	return "low"
}

// pinnedPolicyVersion returns the policy version a new run pins: the project's enabled version, or a fresh snapshot
// of the effective policy (a manual Evaluate on a project that is not enabled).
func (a *App) pinnedPolicyVersion(sc context.Context, p ProjectDoc) (string, error) {
	if p.EnabledPolicyVersionID != nil {
		return *p.EnabledPolicyVersionID, nil
	}
	return a.snapshotResolved(sc, p.OrgID, p.ID)
}

type newRun struct {
	ID        string
	Project   ProjectDoc
	Tracked   TrackedIssueDoc
	Snapshot  IssueSnapshotDoc
	IntentKey string
	Intent    string
	Origin    string
	Carry     []RunSource
	Supersede *RunDoc
}

func answerKind(kind string) bool { return kind == "user_answer" || kind == "constraint" }

// createRun inserts a run at discovered/context with its first attempt and stage job. A duplicate intent or a second
// active run for the issue is a duplicate-key error for the caller to treat as "already exists".
func (a *App) createRun(sc context.Context, in newRun) (RunDoc, error) {
	now := time.Now().UTC()
	pol, err := a.pinnedPolicyVersion(sc, in.Project)
	if err != nil {
		return RunDoc{}, err
	}
	host, err := a.orgHostE(sc, in.Project.OrgID)
	if err != nil {
		return RunDoc{}, err
	}
	if in.ID == "" {
		in.ID = newID("run")
	}
	run := RunDoc{ID: in.ID, OrgID: in.Project.OrgID, ProjectID: in.Project.ID, ConnectionID: in.Tracked.ConnectionID, IssueID: in.Tracked.IssueID, IssueKey: in.Tracked.Key, TrackedIssueID: in.Tracked.ID,
		IntentKey: in.IntentKey, MaterialFP: in.Snapshot.MaterialFP, PolicyIntent: in.Intent, Origin: in.Origin, State: runstate.Discovered, Active: true, Attempt: 1, Priority: priorityOf(in.Snapshot.Priority),
		Checkpoint: RunCheckpoint{Stage: runstate.StageContext, Since: now}, Eligibility: "eligible", SnapshotID: in.Snapshot.ID, SnapshotRev: in.Snapshot.Revision, SourcesRevision: 1,
		Pinned: RunPinned{PolicyVersionID: pol, Host: host}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if b := in.Project.ProfileBinding; b != nil {
		v := b.VersionID
		run.Pinned.ProfileVersionID = &v
	}
	pv, err := a.pinnedPolicy(sc, run)
	if err != nil {
		return RunDoc{}, err
	}
	run.Pinned.PacketCap = pv.Int("context.packet_bytes")
	if run.Pinned.Skills, run.Pinned.SkillsFP, err = a.pinSkills(sc, run.OrgID, run.ProjectID, pv.Int("skills.max_bytes_per_role")); err != nil {
		return RunDoc{}, err
	}
	run.Sources = []RunSource{{ID: newID("src"), Kind: "jira_issue", Label: in.Tracked.Key, Ref: in.Snapshot.ID, ContentHash: in.Snapshot.ContentHash, RevisionAdded: 1}}
	for _, c := range in.Carry {
		c.ID, c.Carried = newID("src"), true
		run.Sources = append(run.Sources, c)
	}
	if in.Supersede != nil {
		id := in.Supersede.ID
		run.Supersedes = &id
	}
	if _, err = a.DB.Coll("runs").InsertOne(sc, run); err != nil {
		return RunDoc{}, err
	}
	attempt := RunAttemptDoc{ID: newID("rat"), OrgID: run.OrgID, RunID: run.ID, Number: 1, Reason: "initial", FromStage: runstate.StageContext, StartedAt: now}
	if _, err = a.DB.Coll("run_attempts").InsertOne(sc, attempt); err != nil {
		return RunDoc{}, err
	}
	activity := now
	if _, err = a.DB.Coll("tracked_issues").UpdateOne(sc, bson.M{"_id": in.Tracked.ID, "org_id": run.OrgID}, bson.M{"$set": bson.M{"active_run_id": run.ID, "latest_run_id": run.ID, "run_state": run.State, "candidate": false, "last_activity_at": activity, "sort_key": taskSortKey(activity, in.Tracked.ID)}}); err != nil {
		return RunDoc{}, err
	}
	if err = a.AppendEvent(sc, run.OrgID, "run.created", Subject{"run", run.ID}, bson.M{"issue_key": run.IssueKey, "origin": run.Origin, "project_id": run.ProjectID, "supersedes": run.Supersedes}, &run.Revision, nil, run.Origin != "manual_evaluate"); err != nil {
		return RunDoc{}, err
	}
	if err = a.enqueueRunAdvance(sc, run.OrgID, run.ID); err != nil {
		return RunDoc{}, err
	}
	return run, nil
}

// supersedeRun retires a pre-coding run whose intent changed: its open Inbox item is superseded, its stage job is
// cancelled and its workspace (if any, it is clean) goes to cleanup. Answers are carried to the new run by the caller.
func (a *App) supersedeRun(sc context.Context, old RunDoc, by string) (RunDoc, error) {
	now := time.Now().UTC()
	next, err := a.casRun(sc, old, "", bson.M{"state": runstate.Superseded, "active": false, "superseded_by": by, "open_inbox_id": nil, "hold": nil, "terminal": RunTerminal{Reason: "superseded", At: now}},
		runEvent{Reason: "material_change", Actor: runstate.ActorSystem, Extra: []eventSpec{{"run.superseded", bson.M{"by": by}}}})
	if err != nil {
		return RunDoc{}, err
	}
	if old.OpenInboxID != nil {
		if err = a.closeInbox(sc, old.OrgID, *old.OpenInboxID, "superseded", &by); err != nil {
			return RunDoc{}, err
		}
	}
	if err = a.cancelRunJob(sc, old.OrgID, old.ID); err != nil {
		return RunDoc{}, err
	}
	if old.WorkspaceID != nil {
		if err = a.enqueueCleanup(sc, old.OrgID); err != nil {
			return RunDoc{}, err
		}
	}
	return next, nil
}

// closeInbox moves an open Inbox item to a non-answered status (superseded or cancelled) and records the event.
func (a *App) closeInbox(sc context.Context, org, id, status string, supersededBy *string) error {
	set := bson.M{"status": status, "updated_at": time.Now().UTC(), "resolution": status}
	if supersededBy != nil {
		set["superseded_by_run"] = *supersededBy
	}
	var it InboxItemDoc
	err := a.DB.Coll("inbox_items").FindOneAndUpdate(sc, bson.M{"_id": id, "org_id": org, "status": "open"}, bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&it)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	if err != nil {
		return err
	}
	return a.AppendEvent(sc, org, "inbox."+status, Subject{"inbox", it.ID}, bson.M{"run_id": it.RunID, "reason": it.Reason}, &it.Revision, nil, true)
}
