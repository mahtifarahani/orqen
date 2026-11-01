package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"log/slog"
	"orqen/internal/intake"
	"orqen/internal/providers"
	"orqen/internal/runstate"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxDescriptionBytes = 64 << 10
	maxCommentBytes     = 8 << 10
	maxParentBytes      = 8 << 10
	maxCommentsKept     = 50
	maxCommentsRead     = 200
)

// issueFields are read for every ingest; the mapped acceptance custom field is added.
var issueFields = []string{"summary", "description", "issuetype", "status", "priority", "labels", "components", "assignee", "parent", "issuelinks", "attachment", "resolution", "updated"}

func cutBytes(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// ingestInput is everything read from Jira before the ingest transaction.
type ingestInput struct {
	Project  ProjectDoc
	Issue    providers.Issue
	Parent   *SnapshotParent
	Comments []SnapshotComment
	Snapshot IssueSnapshotDoc // ID and revision are assigned in the transaction
	PollID   string
	Fetched  time.Time
}

func snapshotParts(is providers.Issue, parent *SnapshotParent, comments []SnapshotComment, acceptanceField *string) (IssueSnapshotDoc, intake.Snapshot) {
	desc, descTrunc := cutBytes(is.Description, maxDescriptionBytes)
	acc := intake.ExtractAcceptance(desc, acceptanceField)
	doc := IssueSnapshotDoc{ConnectionID: "", IssueID: is.ID, JiraUpdated: is.Updated, Summary: is.Summary, Description: desc, DescriptionTruncated: descTrunc, Acceptance: acc, IssueType: is.IssueType,
		Status: SnapshotStatus{Name: is.StatusName, Category: is.StatusCategory}, Priority: is.Priority, Resolution: is.Resolution, Labels: nonNilStrings(is.Labels), Components: nonNilStrings(is.Components), Parent: parent, Comments: comments}
	snap := intake.Snapshot{Summary: is.Summary, Description: desc, Acceptance: acc, IssueType: is.IssueType, Components: is.Components, Labels: is.Labels, StatusName: is.StatusName, StatusCategory: is.StatusCategory,
		Priority: is.Priority, AssigneeID: is.AssigneeID, Resolution: is.Resolution, Updated: is.Updated}
	if parent != nil {
		snap.Parent = &intake.ParentRef{ID: parent.ID, Summary: parent.Summary, Description: parent.Description}
	}
	doc.Links, doc.Attachments = []SnapshotLink{}, []SnapshotAttachment{}
	for _, l := range is.Links {
		doc.Links = append(doc.Links, SnapshotLink{Type: l.Type, Direction: l.Direction, ID: l.ID, Key: l.Key, Summary: l.Summary, StatusCategory: l.StatusCategory})
		snap.Links = append(snap.Links, intake.LinkRef{Type: l.Type, Direction: l.Direction, ID: l.ID, Summary: l.Summary, StatusCategory: l.StatusCategory})
	}
	for _, x := range is.Attachments {
		doc.Attachments = append(doc.Attachments, SnapshotAttachment{ID: x.ID, Filename: x.Filename, Size: x.Size, Mime: x.Mime})
		snap.Attachments = append(snap.Attachments, intake.AttachmentRef{ID: x.ID, Filename: x.Filename, Size: x.Size, Mime: x.Mime})
	}
	for _, c := range comments {
		snap.Comments = append(snap.Comments, intake.CommentRef{ID: c.ID, Body: c.Body})
	}
	texts := []string{desc, acc.Text}
	for _, c := range comments {
		texts = append(texts, c.Body)
	}
	doc.Refs = intake.ExtractRefs(texts...)
	return doc, snap
}

// runIssueIngest reads one issue in full, stores a snapshot when its content changed and creates, keeps,
// supersedes or marks stale the issue's run. All Jira reads happen before the single fenced transaction.
func (a *App) runIssueIngest(ctx context.Context, job JobDoc, lease Lease) error {
	prjID, _ := job.Payload["project_id"].(string)
	conID, _ := job.Payload["connection_id"].(string)
	issueID, _ := job.Payload["issue_id"].(string)
	pollID, _ := job.Payload["poll_id"].(string)
	if !validID("prj", prjID) || !validID("con", conID) || issueID == "" {
		return &JobFailure{Code: "bad_payload", Message: "missing project, connection or issue"}
	}
	done := func() error {
		return a.fenced(ctx, lease, func(sc context.Context) error { return a.completeJob(sc, lease) })
	}
	proj, err := a.project(ctx, job.OrgID, prjID)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return done()
	}
	if err != nil {
		return err
	}
	jm := proj.Mapping.Jira
	if jm == nil || jm.ConnectionID != conID {
		return done()
	}
	if paused, e := a.orgPaused(ctx, job.OrgID); e != nil {
		return e
	} else if paused {
		return done() // the next poll after the pause sees the same change again
	}
	conn, err := a.connection(ctx, job.OrgID, conID)
	if err != nil {
		return err
	}
	src, err := a.issueSource(ctx, conn)
	if err != nil {
		return a.ingestFailure(ctx, proj, conn, "", err)
	}
	fields := append([]string{}, issueFields...)
	if jm.AcceptanceField != nil {
		fields = append(fields, *jm.AcceptanceField)
	}
	issue, err := src.GetIssue(ctx, issueID, fields)
	if err != nil {
		if pe, ok := providers.AsError(err); ok && (pe.Kind == providers.KindNotFound || pe.Kind == providers.KindPermissionDenied) {
			return a.markInaccessible(ctx, lease, job, proj, conn, issueID, pollID, err)
		}
		return a.ingestFailure(ctx, proj, conn, issueID, err)
	}
	var parent *SnapshotParent
	if issue.Parent != nil {
		p, perr := src.GetIssue(ctx, issue.Parent.ID, []string{"summary", "description", "status", "issuetype"})
		switch {
		case perr == nil:
			d, _ := cutBytes(p.Description, maxParentBytes)
			parent = &SnapshotParent{ID: p.ID, Key: p.Key, Summary: p.Summary, Description: d}
		default:
			if pe, ok := providers.AsError(perr); ok && (pe.Kind == providers.KindNotFound || pe.Kind == providers.KindPermissionDenied) {
				parent = &SnapshotParent{ID: issue.Parent.ID, Key: issue.Parent.Key, Summary: issue.Parent.Summary}
			} else {
				return a.ingestFailure(ctx, proj, conn, issueID, perr)
			}
		}
	}
	var all []providers.Comment
	for start := 0; len(all) < maxCommentsRead; {
		page, cerr := src.ListComments(ctx, issue.ID, start, 100)
		if cerr != nil {
			return a.ingestFailure(ctx, proj, conn, issueID, cerr)
		}
		all = append(all, page.Items...)
		if page.NextStartAt < 0 {
			break
		}
		start = page.NextStartAt
	}
	if len(all) > maxCommentsKept {
		all = all[len(all)-maxCommentsKept:]
	}
	comments := []SnapshotComment{}
	for _, c := range all {
		body, trunc := cutBytes(c.Body, maxCommentBytes)
		comments = append(comments, SnapshotComment{ID: c.ID, AuthorDisplay: c.AuthorDisplay, Created: c.Created, Body: body, Truncated: trunc})
	}
	var accField *string
	if jm.AcceptanceField != nil {
		if raw, ok := issue.Fields[*jm.AcceptanceField]; ok {
			t, _ := providers.TextOf(raw)
			accField = &t
		}
	}
	doc, snap := snapshotParts(issue, parent, comments, accField)
	pv, err := a.livePolicy(ctx, job.OrgID, prjID)
	if err != nil {
		return err
	}
	doc.OrgID, doc.ConnectionID, doc.FetchedAt = job.OrgID, conID, time.Now().UTC()
	doc.MaterialFP = intake.MaterialFP(snap, pv.Str("intake.label"))
	doc.ContentHash = intake.ContentHash(snap)
	in := ingestInput{Project: proj, Issue: issue, Snapshot: doc, PollID: pollID, Fetched: doc.FetchedAt}
	// A snapshot with the same content hash is reused (content that flipped back), never duplicated.
	existing, err := findOne[IssueSnapshotDoc](ctx, a.DB, "issue_snapshots", bson.M{"org_id": job.OrgID, "connection_id": conID, "issue_id": issue.ID, "content_hash": doc.ContentHash})
	hasExisting := err == nil
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return err
	}
	err = a.fenced(ctx, lease, func(sc context.Context) error {
		if err := a.ingestTx(sc, in, pv, existing, hasExisting, conn); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
	if mongo.IsDuplicateKeyError(err) {
		// Another writer created the same tracked issue, snapshot or run first; the next poll reconciles.
		slog.Info("ingest lost a duplicate-key race", "issue", issue.Key)
		return &JobFailure{Code: "ingest_conflict", Message: "concurrent ingest, retrying", Retryable: true, RetryAfter: time.Second}
	}
	return err
}

// ingestFailure records the failure for the intake status and returns it for the job's retry policy.
func (a *App) ingestFailure(ctx context.Context, proj ProjectDoc, conn ConnectionDoc, issueID string, err error) error {
	a.noteProviderFailure(ctx, proj, conn, err)
	return err
}

// markInaccessible flags the tracked issue; an active run goes to needs_input (project_mapping).
func (a *App) markInaccessible(ctx context.Context, lease Lease, job JobDoc, proj ProjectDoc, conn ConnectionDoc, issueID, pollID string, cause error) error {
	return a.fenced(ctx, lease, func(sc context.Context) error {
		var ti TrackedIssueDoc
		err := a.DB.Coll("tracked_issues").FindOneAndUpdate(sc, bson.M{"org_id": job.OrgID, "connection_id": conn.ID, "issue_id": issueID}, bson.M{"$set": bson.M{"inaccessible": true, "last_seen_poll": pollID}}, afterUpdate()).Decode(&ti)
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			return err
		}
		if err == nil && ti.ActiveRunID != nil {
			run, e := a.run(sc, job.OrgID, *ti.ActiveRunID)
			if e == nil && run.Active && run.State != runstate.NeedsInput && !runstate.Terminal(run.State) {
				if e = a.stopRun(sc, run, stopSpec{Reason: "project_mapping", QuestionCode: "issue_inaccessible", Options: resolveOptions()}); e != nil && !errors.Is(e, errRunMoved) {
					return e
				}
			}
		}
		return a.completeJob(sc, lease)
	})
}

// ingestTx is the transaction of PLAN-S3-GO WP43.
func (a *App) ingestTx(sc context.Context, in ingestInput, pv policyValues, existing IssueSnapshotDoc, hasExisting bool, conn ConnectionDoc) error {
	org, proj, doc, issue := in.Project.OrgID, in.Project, in.Snapshot, in.Issue
	now := time.Now().UTC()
	ti, err := findOne[TrackedIssueDoc](sc, a.DB, "tracked_issues", bson.M{"org_id": org, "connection_id": conn.ID, "issue_id": issue.ID})
	if errors.Is(err, mongo.ErrNoDocuments) {
		id := newID("iss")
		ti = TrackedIssueDoc{ID: id, OrgID: org, ProjectID: proj.ID, ConnectionID: conn.ID, IssueID: issue.ID, Key: issue.Key, URL: strings.TrimRight(conn.BaseURL, "/") + "/browse/" + issue.Key, NextRevision: 1, InFilter: true, LastSeenPoll: in.PollID,
			LastActivityAt: now, SortKey: taskSortKey(now, id), CreatedAt: now, AmbiguousProject: []string{}}
		if _, err = a.DB.Coll("tracked_issues").InsertOne(sc, ti); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if ti.ProjectID != proj.ID {
		// The first project claimed the issue; a second match makes it ambiguous and creates nothing.
		_, err = a.DB.Coll("tracked_issues").UpdateOne(sc, bson.M{"_id": ti.ID, "org_id": org}, bson.M{"$addToSet": bson.M{"ambiguous_project": bson.M{"$each": []string{ti.ProjectID, proj.ID}}}, "$set": bson.M{"last_seen_poll": in.PollID}})
		return err
	}
	volatile := bson.M{"summary_snapshot": issue.Summary, "status_name": issue.StatusName, "status_category": issue.StatusCategory, "priority": issue.Priority, "jira_updated": issue.Updated, "key": issue.Key, "in_filter": true, "inaccessible": false, "last_seen_poll": in.PollID}
	if in.PollID == "dependency" {
		delete(volatile, "in_filter") // a dependency re-read says nothing about the intake filter
		delete(volatile, "last_seen_poll")
	}
	if ti.LatestContentHash == doc.ContentHash && ti.LatestSnapshotID != "" {
		_, err = a.DB.Coll("tracked_issues").UpdateOne(sc, bson.M{"_id": ti.ID, "org_id": org}, bson.M{"$set": volatile})
		return err
	}
	materialChanged := ti.LatestMaterialFP != doc.MaterialFP
	var snap IssueSnapshotDoc
	if hasExisting {
		snap = existing
	} else {
		snap = doc
		snap.ID, snap.TrackedIssueID, snap.Revision = newID("isn"), ti.ID, ti.NextRevision
		if _, err = a.DB.Coll("issue_snapshots").InsertOne(sc, snap); err != nil {
			return err
		}
		volatile["next_revision"] = ti.NextRevision + 1
	}
	volatile["latest_snapshot_id"], volatile["latest_revision"], volatile["latest_material_fp"], volatile["latest_content_hash"] = snap.ID, snap.Revision, doc.MaterialFP, doc.ContentHash
	volatile["last_activity_at"], volatile["sort_key"] = now, taskSortKey(now, ti.ID)
	if _, err = a.DB.Coll("tracked_issues").UpdateOne(sc, bson.M{"_id": ti.ID, "org_id": org}, bson.M{"$set": volatile}); err != nil {
		return err
	}
	ti.LatestSnapshotID, ti.LatestRevision = snap.ID, snap.Revision
	if err = a.AppendEvent(sc, org, "issue.ingested", Subject{"issue", ti.ID}, bson.M{"task_id": ti.ID, "issue_key": issue.Key, "revision": snap.Revision, "material_changed": materialChanged}, nil, nil, true); err != nil {
		return err
	}
	return a.applyIntent(sc, proj, ti, snap, pv)
}

// applyIntent decides what the issue's new snapshot does to its run: nothing, supersede, stale or a new run.
func (a *App) applyIntent(sc context.Context, proj ProjectDoc, ti TrackedIssueDoc, snap IssueSnapshotDoc, pv policyValues) error {
	_, _, err := a.applyIntentRun(sc, proj, ti, snap, pv, "")
	return err
}

// applyIntentRun returns the run the intent resolves to and whether this call created it. origin is
// "manual_evaluate" for Evaluate (it creates even when intake is manual); "" lets the intake mode decide.
func (a *App) applyIntentRun(sc context.Context, proj ProjectDoc, ti TrackedIssueDoc, snap IssueSnapshotDoc, pv policyValues, origin string) (*RunDoc, bool, error) {
	org := proj.OrgID
	var repoKey, branch string
	if p := proj.Mapping.primary(); p != nil && proj.PrimaryRepoKey != nil {
		repoKey, branch = *proj.PrimaryRepoKey, p.TargetBranch
	}
	policyIntent := intake.PolicyIntent(proj.ID, repoKey, branch)
	intent := intake.IntentKey(org, ti.ConnectionID, ti.IssueID, snap.MaterialFP, policyIntent)
	active, err := findOne[RunDoc](sc, a.DB, "runs", bson.M{"org_id": org, "connection_id": ti.ConnectionID, "issue_id": ti.IssueID, "active": true})
	hasActive := err == nil
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, false, err
	}
	if hasActive && active.IntentKey == intent {
		// Same intent: the run follows a newer snapshot while it is still before planning, so triage sees the
		// issue's current status.
		if active.SnapshotID != snap.ID && (active.Checkpoint.Stage == runstate.StageContext || active.Checkpoint.Stage == runstate.StageTriage) {
			set := bson.M{"snapshot_id": snap.ID, "snapshot_rev": snap.Revision}
			if active.State == runstate.WaitingDep {
				set["state"] = runstate.Triaging
				set["checkpoint"] = RunCheckpoint{Stage: runstate.StageTriage, Since: time.Now().UTC()}
			}
			next, e := a.casRun(sc, active, "", set, runEvent{Reason: "snapshot_refreshed", Actor: runstate.ActorSystem})
			if e == nil && active.State == runstate.WaitingDep {
				if e = a.enqueueRunAdvance(sc, org, active.ID); e != nil {
					return nil, false, e
				}
			}
			if e == nil {
				active = next
			} else if !errors.Is(e, errRunMoved) {
				return nil, false, e
			}
		}
		return &active, false, nil
	}
	supersede := hasActive && runstate.PreCodingRun(active.State, active.Checkpoint.Stage)
	if hasActive && !supersede {
		// Past the pre-coding states (Sprint 4): the run keeps going; mark it stale for re-triage.
		_, err = a.casRun(sc, active, "", bson.M{"stale": true, "hold": RunHold{Code: runstate.HoldStale, Detail: "the issue changed", Since: time.Now().UTC()}}, runEvent{Reason: "stale", Actor: runstate.ActorSystem, Extra: []eventSpec{{"run.held", bson.M{"code": runstate.HoldStale}}}})
		if errors.Is(err, errRunMoved) {
			return &active, false, nil
		}
		return &active, false, err
	}
	prior, err := findOne[RunDoc](sc, a.DB, "runs", bson.M{"org_id": org, "intent_key": intent})
	if err == nil {
		return &prior, false, nil // never re-create the same intent (Retry reopens it)
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, false, err
	}
	autoCreate := pv.Str("automation.intake") == "auto" && proj.AutomationEnabled
	if origin == "" && !supersede && !autoCreate {
		_, err = a.DB.Coll("tracked_issues").UpdateOne(sc, bson.M{"_id": ti.ID, "org_id": org}, bson.M{"$set": bson.M{"candidate": true}})
		return nil, false, err
	}
	if origin == "" {
		origin = "auto_intake"
		if supersede {
			origin = "supersede"
		}
	}
	nr := newRun{ID: newID("run"), Project: proj, Tracked: ti, Snapshot: snap, IntentKey: intent, Intent: policyIntent, Origin: origin}
	if supersede {
		for _, s := range active.Sources {
			if answerKind(s.Kind) {
				nr.Carry = append(nr.Carry, s)
			}
		}
		nr.Supersede = &active
		// The old run must be inactive before the new one is inserted (unique active run per issue).
		if _, err = a.supersedeRun(sc, active, nr.ID); errors.Is(err, errRunMoved) {
			return nil, false, nil
		} else if err != nil {
			return nil, false, err
		}
	}
	run, err := a.createRun(sc, nr)
	if err != nil {
		return nil, false, err
	}
	if _, err = a.DB.Coll("intake_states").UpdateOne(sc, bson.M{"_id": intakeStateID(proj.ID), "org_id": org}, bson.M{"$inc": bson.M{"last_counts.new_runs": 1}}); err != nil {
		return nil, false, err
	}
	return &run, true, nil
}
