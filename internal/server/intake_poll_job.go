package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"log/slog"
	"orqen/internal/intake"
	"orqen/internal/providers"
	"orqen/internal/runstate"
	"time"
)

// pollFields is the minimal projection of a poll; everything else is read by the ingest job.
var pollFields = []string{"summary", "status", "labels", "assignee", "issuetype", "priority", "updated"}

const pollPageSize = 50

func (a *App) intakeState(ctx context.Context, org, prj string) (IntakeStateDoc, error) {
	d, err := findOne[IntakeStateDoc](ctx, a.DB, "intake_states", bson.M{"_id": intakeStateID(prj), "org_id": org})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return IntakeStateDoc{ID: intakeStateID(prj), OrgID: org, ProjectID: prj}, nil
	}
	return d, err
}

// saveIntake applies a $set to the project's intake state, creating it when missing.
func (a *App) saveIntake(ctx context.Context, org, prj string, set bson.M) error {
	_, err := a.DB.Coll("intake_states").UpdateOne(ctx, bson.M{"_id": intakeStateID(prj), "org_id": org}, bson.M{"$set": set, "$setOnInsert": bson.M{"project_id": prj}}, options.UpdateOne().SetUpsert(true))
	return err
}

// noteProviderFailure records a provider failure of a poll or ingest: it starts the connection's rate-limit
// cooldown, and sets the intake status (rate limited, waiting for the network, blocked until the credential changes).
func (a *App) noteProviderFailure(ctx context.Context, proj ProjectDoc, conn ConnectionDoc, err error) {
	pe, ok := providers.AsError(err)
	if !ok || ctx.Err() != nil {
		return
	}
	now := time.Now().UTC()
	set := bson.M{"last_error": IntakeError{Code: string(pe.Kind), Detail: pe.Message, At: now}}
	switch pe.Kind {
	case providers.KindRateLimited:
		set["last_error"] = IntakeError{Code: "rate_limited", Detail: pe.Message, At: now}
		wait := pe.RetryAfter
		if wait <= 0 {
			wait = 5 * time.Second
		}
		a.cooldown.Store(conn.ID, now.Add(wait))
	case providers.KindNetwork:
		set["waiting_network"] = true
	case providers.KindCredentialExpired, providers.KindPermissionDenied:
		set["blocked"] = IntakeBlocked{Code: "credential_expired", ConnectionID: conn.ID, MaterialHash: conn.MaterialHash()}
	}
	if e := a.saveIntake(ctx, proj.OrgID, proj.ID, set); e != nil {
		slog.Warn("could not record the intake failure", "error", e)
	}
}

// runIntakePoll lists the eligible issues with minimal fields and enqueues an ingest for new or changed ones.
func (a *App) runIntakePoll(ctx context.Context, job JobDoc, lease Lease) error {
	prjID, _ := job.Payload["project_id"].(string)
	if !validID("prj", prjID) {
		return &JobFailure{Code: "bad_payload", Message: "missing project_id"}
	}
	finish := func(status string, payload bson.M) error {
		return a.fenced(ctx, lease, func(sc context.Context) error {
			if err := a.transitionOperation(sc, job, status, payload); err != nil {
				return err
			}
			return a.completeJob(sc, lease)
		})
	}
	if err := a.fenced(ctx, lease, func(sc context.Context) error { return a.transitionOperation(sc, job, "running", nil) }); err != nil {
		return err
	}
	proj, err := a.project(ctx, job.OrgID, prjID)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return finish("failed", bson.M{"code": "project_archived", "message": "project no longer exists"})
	}
	if err != nil {
		return err
	}
	jm := proj.Mapping.Jira
	if jm == nil {
		return finish("failed", bson.M{"code": "jira_mapping_missing", "message": "the project has no Jira mapping"})
	}
	if paused, e := a.orgPaused(ctx, job.OrgID); e != nil {
		return e
	} else if paused {
		return finish("succeeded", bson.M{"skipped": "paused"})
	}
	pv, err := a.livePolicy(ctx, job.OrgID, prjID)
	if err != nil {
		return err
	}
	state, err := a.intakeState(ctx, job.OrgID, prjID)
	if err != nil {
		return err
	}
	conn, err := a.connection(ctx, job.OrgID, jm.ConnectionID)
	if err != nil {
		return finish("failed", bson.M{"code": "connection_unavailable", "message": "the Jira connection no longer exists"})
	}
	src, err := a.issueSource(ctx, conn)
	if err != nil {
		a.noteProviderFailure(ctx, proj, conn, err)
		return err
	}
	start := time.Now().UTC()
	interval := time.Duration(pv.Int("intake.poll_interval_secs")) * time.Second
	if e := a.saveIntake(ctx, job.OrgID, prjID, bson.M{"last_poll_started_at": start}); e != nil {
		return e
	}
	fail := func(err error) error {
		a.noteProviderFailure(ctx, proj, conn, err)
		if pe, ok := providers.AsError(err); ok && !pe.Retryable() {
			// A permanent failure must not retry in a loop; the next scheduled poll tries again (or waits for the credential).
			_ = a.saveIntake(ctx, job.OrgID, prjID, bson.M{"next_poll_at": start.Add(interval)})
		}
		return err
	}
	in := intake.JQLInput{ProjectID: jm.ProjectID, UserJQL: jm.JQL, Label: pv.Str("intake.label"), AssignedToMe: pv.Bool("intake.require_assigned_to_me"), RequireOpenSprint: pv.Bool("intake.require_open_sprint")}
	in.IncludeUnresolvedDone = pv.Bool("intake.include_unresolved_done")
	var sprints []string
	if jm.Board != nil && jm.JQL == nil && in.RequireOpenSprint {
		board, berr := src.GetBoard(ctx, jm.Board.ID)
		if berr != nil {
			return fail(berr)
		}
		if board.Type == "scrum" {
			in.BoardScrum = true
			list, serr := src.ActiveSprints(ctx, jm.Board.ID)
			if pe, ok := providers.AsError(serr); ok && pe.Kind == providers.KindUnsupported {
				list, serr = nil, nil
			}
			if serr != nil {
				return fail(serr)
			}
			for _, s := range list {
				sprints = append(sprints, s.ID)
			}
			in.SprintIDs = sprints
		}
	}
	minutes, full := intake.Window(state.LastSuccessStartedAt, start, state.PollsSinceFull, pv.Int("intake.reconcile_every_polls"))
	// A user-requested refresh must also find older issues newly included by a changed filter.
	if manual, _ := job.Payload["manual"].(bool); manual {
		minutes, full = 0, true
	}
	in.WindowMinutes = minutes
	jql := intake.ComposeJQL(in)
	maxIssues := pv.Int("intake.max_issues_per_poll")
	var heads []providers.IssueHead
	complete := false
	for page := ""; len(heads) < maxIssues; {
		p, serr := src.SearchIssues(ctx, jql, pollFields, page, min(pollPageSize, maxIssues-len(heads)))
		if serr != nil {
			return fail(serr)
		}
		heads = append(heads, p.Items...)
		if p.Next == "" {
			complete = true
			break
		}
		page = p.Next
	}
	ids := make([]string, 0, len(heads))
	for _, h := range heads {
		ids = append(ids, h.ID)
	}
	tracked := map[string]TrackedIssueDoc{}
	if len(ids) > 0 {
		docs, e := findMany[TrackedIssueDoc](ctx, a.DB, "tracked_issues", bson.M{"org_id": job.OrgID, "connection_id": jm.ConnectionID, "issue_id": bson.M{"$in": ids}}, bson.D{{Key: "_id", Value: 1}}, int64(len(ids)))
		if e != nil {
			return e
		}
		for _, d := range docs {
			tracked[d.IssueID] = d
		}
	}
	pollID := newID("poll")
	counts := IntakeCounts{Seen: len(heads)}
	err = a.fenced(ctx, lease, func(sc context.Context) error {
		now := time.Now().UTC()
		counts.Changed, counts.LeftFilter = 0, 0
		var reentered []TrackedIssueDoc
		for _, h := range heads {
			ti, known := tracked[h.ID]
			if known && !ti.InFilter && ti.ProjectID == prjID {
				reentered = append(reentered, ti)
			}
			if known && ti.JiraUpdated == h.Updated && !ti.Inaccessible && (ti.ProjectID == prjID || contains(ti.AmbiguousProject, prjID)) {
				continue
			}
			counts.Changed++
			err := a.enqueue(sc, JobDoc{ID: newID("job"), OrgID: job.OrgID, Kind: "issue_ingest", DedupeKey: "issue_ingest:" + jm.ConnectionID + ":" + h.ID, State: "queued", Active: true, MaxAttempts: 50, NextAt: now, CreatedAt: now, UpdatedAt: now,
				Payload: bson.M{"project_id": prjID, "connection_id": jm.ConnectionID, "issue_id": h.ID, "key": h.Key, "poll_id": pollID}})
			var dup *DuplicateActiveJob
			if err != nil && !errors.As(err, &dup) {
				return err
			}
		}
		if len(ids) > 0 {
			if _, err := a.DB.Coll("tracked_issues").UpdateMany(sc, bson.M{"org_id": job.OrgID, "connection_id": jm.ConnectionID, "issue_id": bson.M{"$in": ids}, "project_id": prjID}, bson.M{"$set": bson.M{"last_seen_poll": pollID, "in_filter": true}}); err != nil {
				return err
			}
		}
		for _, ti := range reentered {
			if err := a.reenterFilter(sc, ti); err != nil {
				return err
			}
		}
		if full && complete {
			left, err := findMany[TrackedIssueDoc](sc, a.DB, "tracked_issues", bson.M{"org_id": job.OrgID, "project_id": prjID, "in_filter": true, "last_seen_poll": bson.M{"$ne": pollID}}, bson.D{{Key: "_id", Value: 1}}, 1000)
			if err != nil {
				return err
			}
			for _, ti := range left {
				if err := a.leftFilter(sc, ti); err != nil {
					return err
				}
				counts.LeftFilter++
			}
		}
		since := state.PollsSinceFull + 1
		if full && complete {
			since = 0
		}
		set := bson.M{"next_poll_at": start.Add(interval), "last_poll_started_at": start, "polls_since_full": since, "last_error": nil, "waiting_network": false, "blocked": nil, "last_counts": counts, "active_sprints": nonNilStrings(sprints)}
		if complete {
			set["last_success_started_at"] = start
		}
		if _, err := a.DB.Coll("intake_states").UpdateOne(sc, bson.M{"_id": intakeStateID(prjID), "org_id": job.OrgID}, bson.M{"$set": set, "$setOnInsert": bson.M{"project_id": prjID}}, options.UpdateOne().SetUpsert(true)); err != nil {
			return err
		}
		mode := "incremental"
		if full {
			mode = "full"
		}
		if err := a.AppendEvent(sc, job.OrgID, "intake.polled", Subject{"project", prjID}, bson.M{"project_id": prjID, "mode": mode, "seen": counts.Seen, "changed": counts.Changed, "new_runs": 0, "left_filter": counts.LeftFilter}, nil, job.OperationID, true); err != nil {
			return err
		}
		if err := a.transitionOperation(sc, job, "succeeded", bson.M{"seen": counts.Seen, "changed": counts.Changed, "left_filter": counts.LeftFilter, "mode": mode}); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
	var dup *DuplicateActiveJob
	if errors.As(err, &dup) {
		return &JobFailure{Code: "poll_conflict", Message: "concurrent enqueue, retrying", Retryable: true, RetryAfter: time.Second}
	}
	return err
}

// leftFilter marks a tracked issue as no longer matching the intake filter. The active run keeps its state but holds.
func (a *App) leftFilter(sc context.Context, ti TrackedIssueDoc) error {
	if _, err := a.DB.Coll("tracked_issues").UpdateOne(sc, bson.M{"_id": ti.ID, "org_id": ti.OrgID}, bson.M{"$set": bson.M{"in_filter": false}}); err != nil {
		return err
	}
	if err := a.AppendEvent(sc, ti.OrgID, "intake.issue_left_filter", Subject{"issue", ti.ID}, bson.M{"task_id": ti.ID}, nil, nil, true); err != nil {
		return err
	}
	if ti.ActiveRunID == nil {
		return nil
	}
	run, err := a.run(sc, ti.OrgID, *ti.ActiveRunID)
	if errors.Is(err, mongo.ErrNoDocuments) || err == nil && !run.Active {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = a.casRun(sc, run, "", bson.M{"eligibility": "left_filter", "hold": RunHold{Code: runstate.HoldLeftFilter, Detail: "the issue no longer matches the intake filter", Since: time.Now().UTC()}},
		runEvent{Reason: "left_filter", Actor: runstate.ActorSystem, Extra: []eventSpec{{"run.held", bson.M{"code": runstate.HoldLeftFilter}}}})
	if errors.Is(err, errRunMoved) {
		return nil
	}
	return err
}

// reenterFilter makes the run eligible again and wakes its stage job.
func (a *App) reenterFilter(sc context.Context, ti TrackedIssueDoc) error {
	if ti.ActiveRunID == nil {
		return nil
	}
	run, err := a.run(sc, ti.OrgID, *ti.ActiveRunID)
	if errors.Is(err, mongo.ErrNoDocuments) || err == nil && (!run.Active || run.Eligibility != "left_filter") {
		return nil
	}
	if err != nil {
		return err
	}
	set := bson.M{"eligibility": "eligible"}
	if run.Hold != nil && run.Hold.Code == runstate.HoldLeftFilter {
		set["hold"] = nil
	}
	if _, err = a.casRun(sc, run, "", set, runEvent{Reason: "reentered_filter", Actor: runstate.ActorSystem, Extra: []eventSpec{{"run.resumed", bson.M{"reason": "reentered_filter"}}}}); errors.Is(err, errRunMoved) {
		return nil
	} else if err != nil {
		return err
	}
	return a.enqueueRunAdvance(sc, ti.OrgID, run.ID)
}
