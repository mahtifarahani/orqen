package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"log/slog"
	"orqen/internal/runstate"
	"time"
)

const (
	defaultSchedulerTick  = 5 * time.Second
	defaultSchedulerSweep = 60 * time.Second
)

// runScheduler is the durable poller: every tick it starts polls that are due, and every sweep interval it repairs
// what a crash or a pause left behind. It is a goroutine of the runner process, not a job; every action it takes is an
// enqueue with a dedupe key, so several processes (or a restart) never produce a duplicate worker.
func (a *App) runScheduler() {
	tick, sweep := a.schedTick, a.schedSweep
	if tick <= 0 {
		tick = defaultSchedulerTick
	}
	if sweep <= 0 {
		sweep = defaultSchedulerSweep
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	lastSweep := time.Now()
	lastClean := time.Now()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
		}
		a.runnerBeat.Store(time.Now().UnixNano())
		if err := a.schedulePolls(a.ctx); err != nil && a.ctx.Err() == nil {
			slog.Warn("scheduler: could not start polls", "error", err)
		}
		if err := a.dispatchCodingSlots(a.ctx); err != nil && a.ctx.Err() == nil {
			slog.Warn("scheduler: could not dispatch coding slots", "error", err)
		}
		if time.Since(lastSweep) >= sweep {
			lastSweep = time.Now()
			if err := a.sweep(a.ctx); err != nil && a.ctx.Err() == nil {
				slog.Warn("scheduler: sweep failed", "error", err)
			}
		}
		if time.Since(lastClean) >= 24*time.Hour {
			lastClean = time.Now()
			a.scheduleDailyCleanup(a.ctx)
		}
	}
}

// schedulePolls enqueues an intake_poll for every enabled project whose next poll is due.
func (a *App) schedulePolls(ctx context.Context) error {
	projects, err := findMany[ProjectDoc](ctx, a.DB, "projects", bson.M{"active": true, "automation_enabled": true, "mapping.jira": bson.M{"$ne": nil}}, bson.D{{Key: "_id", Value: 1}}, 1000)
	if err != nil || len(projects) == 0 {
		return err
	}
	ids := make([]string, 0, len(projects))
	orgs := map[string]bool{}
	for _, p := range projects {
		ids = append(ids, p.ID)
		orgs[p.OrgID] = true
	}
	states, err := findMany[IntakeStateDoc](ctx, a.DB, "intake_states", bson.M{"project_id": bson.M{"$in": ids}}, bson.D{{Key: "_id", Value: 1}}, int64(len(ids)))
	if err != nil {
		return err
	}
	byProject := map[string]IntakeStateDoc{}
	for _, s := range states {
		byProject[s.ProjectID] = s
	}
	paused := map[string]bool{}
	orgIDs := []string{}
	for o := range orgs {
		orgIDs = append(orgIDs, o)
	}
	pausedDocs, err := findMany[AutomationStateDoc](ctx, a.DB, "automation_states", bson.M{"org_id": bson.M{"$in": orgIDs}, "paused": true}, bson.D{{Key: "_id", Value: 1}}, int64(len(orgIDs)))
	if err != nil {
		return err
	}
	for _, d := range pausedDocs {
		paused[d.OrgID] = true
	}
	now := time.Now().UTC()
	for _, p := range projects {
		if paused[p.OrgID] {
			continue
		}
		st, known := byProject[p.ID]
		if known && st.Blocked != nil {
			conn, e := a.connection(ctx, p.OrgID, st.Blocked.ConnectionID)
			if e == nil && conn.MaterialHash() == st.Blocked.MaterialHash {
				continue // blocked until the connection (credential, URL) changes
			}
			// The connection changed: poll right away, whatever the old schedule said.
			if e = a.saveIntake(ctx, p.OrgID, p.ID, bson.M{"blocked": nil, "last_error": nil, "next_poll_at": now}); e != nil {
				return e
			}
		} else if known && st.NextPollAt.After(now) {
			continue
		}
		job := JobDoc{ID: newID("job"), OrgID: p.OrgID, Kind: "intake_poll", DedupeKey: "intake_poll:" + p.ID, Payload: bson.M{"project_id": p.ID}, State: "queued", Active: true, MaxAttempts: 20, NextAt: now, CreatedAt: now, UpdatedAt: now}
		var dup *DuplicateActiveJob
		if err = a.enqueue(ctx, job); err != nil && !errors.As(err, &dup) {
			return err
		}
	}
	a.Committed()
	return nil
}

// sweep repairs the run pipeline: orphaned active runs get their stage job back, and holds that no longer apply are cleared.
func (a *App) sweep(ctx context.Context) error {
	runs, err := findMany[RunDoc](ctx, a.DB, "runs", bson.M{"active": true, "state": bson.M{"$nin": []string{runstate.NeedsInput, runstate.Queued}}, "hold": nil, "cancellation": nil}, bson.D{{Key: "_id", Value: 1}}, 500)
	if err != nil {
		return err
	}
	for _, r := range runs {
		n, e := a.DB.Coll("jobs").CountDocuments(ctx, bson.M{"org_id": r.OrgID, "dedupe_key": runAdvanceKey(r.ID), "active": true})
		if e != nil {
			return e
		}
		if n == 0 && r.State != runstate.WaitingDep {
			if e = a.DB.Txn(ctx, func(sc context.Context) error { return a.enqueueRunAdvance(sc, r.OrgID, r.ID) }); e != nil {
				return e
			}
		}
	}
	a.Committed()
	if err = a.sweepSlots(ctx); err != nil {
		return err
	}
	if err = a.sweepDependencies(ctx); err != nil {
		return err
	}
	if err = a.sweepHolds(ctx); err != nil {
		return err
	}
	if err = a.sweepWiki(ctx); err != nil {
		return err
	}
	if err = a.sweepExtensionHealth(ctx); err != nil {
		return err
	}
	if err = a.sweepCodeIndexAuto(ctx); err != nil {
		return err
	}
	if err = a.sweepSecurityAuto(ctx); err != nil {
		return err
	}
	if err = a.sweepSourceSync(ctx); err != nil {
		return err
	}
	if err = a.sweepBackupDue(ctx); err != nil {
		return err
	}
	if err = a.rollupUsage(ctx); err != nil {
		return err
	}
	return a.sweepCompaction(ctx)
}

// sweepDependencies re-reads the issues of runs waiting on a blocker, once per intake poll interval. When the blocker
// is done the new snapshot wakes the run (see applyIntentRun).
func (a *App) sweepDependencies(ctx context.Context) error {
	waiting, err := findMany[RunDoc](ctx, a.DB, "runs", bson.M{"active": true, "state": runstate.WaitingDep}, bson.D{{Key: "_id", Value: 1}}, 200)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, r := range waiting {
		pv, e := a.livePolicy(ctx, r.OrgID, r.ProjectID)
		if e != nil {
			return e
		}
		interval := time.Duration(pv.Int("intake.poll_interval_secs")) * time.Second
		if r.DepCheckedAt != nil && now.Sub(*r.DepCheckedAt) < interval {
			continue
		}
		e = a.DB.Txn(ctx, func(sc context.Context) error {
			if _, e := a.DB.Coll("runs").UpdateOne(sc, bson.M{"_id": r.ID, "org_id": r.OrgID}, bson.M{"$set": bson.M{"dep_checked_at": now}}); e != nil {
				return e
			}
			err := a.enqueue(sc, JobDoc{ID: newID("job"), OrgID: r.OrgID, Kind: "issue_ingest", DedupeKey: "issue_ingest:" + r.ConnectionID + ":" + r.IssueID, State: "queued", Active: true, MaxAttempts: 50, NextAt: now, CreatedAt: now, UpdatedAt: now,
				Payload: bson.M{"project_id": r.ProjectID, "connection_id": r.ConnectionID, "issue_id": r.IssueID, "key": r.IssueKey, "poll_id": "dependency"}})
			var dup *DuplicateActiveJob
			if err != nil && !errors.As(err, &dup) {
				return err
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	a.Committed()
	return nil
}

// scheduleDailyCleanup asks every organization with workspaces to clean up (the job is a no-op when nothing qualifies).
func (a *App) scheduleDailyCleanup(ctx context.Context) {
	orgs, err := a.DB.Coll("workspaces").Distinct(ctx, "org_id", bson.M{"state": bson.M{"$ne": "removed"}}).Raw()
	if err != nil {
		return
	}
	vals, err := orgs.Values()
	if err != nil {
		return
	}
	for _, v := range vals {
		if org, ok := v.StringValueOK(); ok {
			_ = a.enqueueCleanup(ctx, org)
		}
	}
}

// sweepHolds clears holds whose cause is gone, and confirms cancellations whose stage job has ended.
func (a *App) sweepHolds(ctx context.Context) error {
	held, err := findMany[RunDoc](ctx, a.DB, "runs", bson.M{"active": true, "hold.code": bson.M{"$in": []string{runstate.HoldAutomationPaused, runstate.HoldProjectNotReady, runstate.HoldProjectNotEnabled}}}, bson.D{{Key: "_id", Value: 1}}, 500)
	if err != nil {
		return err
	}
	for _, r := range held {
		switch r.Hold.Code {
		case runstate.HoldAutomationPaused:
			if paused, e := a.orgPaused(ctx, r.OrgID); e != nil || paused {
				continue
			}
			if e := a.releaseHold(ctx, r, "unpaused"); e != nil {
				return e
			}
		default:
			pv, e := a.pinnedPolicy(ctx, r)
			if e != nil {
				return e
			}
			if pv.Str("automation.coding_start") != "auto" && !r.StartApproved {
				continue // a person presses Start; the sweep never starts coding on its own
			}
			code, _, e := a.gateBlockers(ctx, r)
			if e != nil {
				return e
			}
			if code == "" {
				if e = a.releaseHold(ctx, r, "project_ready"); e != nil {
					return e
				}
			}
		}
	}
	if err = a.sweepCleanup(ctx); err != nil {
		return err
	}
	return a.confirmCancellations(ctx)
}

// sweepCleanup asks for cleanup when a finished run still has a live workspace: a cleanup that was already running
// when the run ended (and so skipped it) would otherwise never be repeated.
func (a *App) sweepCleanup(ctx context.Context) error {
	spaces, err := findMany[WorkspaceDoc](ctx, a.DB, "workspaces", bson.M{"state": bson.M{"$in": []string{"preparing", "ready", "retained"}}}, bson.D{{Key: "_id", Value: 1}}, 500)
	if err != nil {
		return err
	}
	orgs := map[string]bool{}
	for _, ws := range spaces {
		if orgs[ws.OrgID] {
			continue
		}
		if run, e := a.run(ctx, ws.OrgID, ws.RunID); e == nil && !run.Active {
			orgs[ws.OrgID] = true
			if e = a.DB.Txn(ctx, func(sc context.Context) error { return a.enqueueCleanup(sc, ws.OrgID) }); e != nil {
				return e
			}
		}
	}
	return nil
}

// confirmCancellations marks requested cancellations confirmed once no lease of the run's stage job is live.
func (a *App) confirmCancellations(ctx context.Context) error {
	pending, err := findMany[RunDoc](ctx, a.DB, "runs", bson.M{"cancellation.state": "requested"}, bson.D{{Key: "_id", Value: 1}}, 200)
	if err != nil {
		return err
	}
	for _, r := range pending {
		n, e := a.DB.Coll("jobs").CountDocuments(ctx, bson.M{"org_id": r.OrgID, "dedupe_key": runAdvanceKey(r.ID), "lease_expires_at": bson.M{"$gt": time.Now().UTC()}})
		if e != nil {
			return e
		}
		live, e := a.liveRoles(ctx, r.OrgID, r.ID)
		if e != nil {
			return e
		}
		if n > 0 || live > 0 {
			continue
		}
		now := time.Now().UTC()
		e = a.DB.Txn(ctx, func(sc context.Context) error {
			var next RunDoc
			err := a.DB.Coll("runs").FindOneAndUpdate(sc, bson.M{"_id": r.ID, "org_id": r.OrgID, "cancellation.state": "requested"}, bson.M{"$set": bson.M{"cancellation.state": "confirmed", "cancellation.confirmed_at": now, "updated_at": now}, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&next)
			if errors.Is(err, mongo.ErrNoDocuments) {
				return nil
			}
			if err != nil {
				return err
			}
			rev := next.Revision
			return a.AppendEvent(sc, r.OrgID, "run.cancel_confirmed", Subject{"run", r.ID}, bson.M{}, &rev, nil, true)
		})
		if e != nil {
			return e
		}
	}
	a.Committed()
	return nil
}

// releaseHold clears a run's hold and wakes its stage job.
func (a *App) releaseHold(ctx context.Context, r RunDoc, reason string) error {
	err := a.DB.Txn(ctx, func(sc context.Context) error {
		cur, e := a.run(sc, r.OrgID, r.ID)
		if e != nil || !cur.Active || cur.Hold == nil {
			return e
		}
		if _, e = a.casRun(sc, cur, "", bson.M{"hold": nil}, runEvent{Reason: reason, Actor: runstate.ActorSystem, Extra: []eventSpec{{"run.resumed", bson.M{"reason": reason}}}}); e != nil {
			return e
		}
		return a.enqueueRunAdvance(sc, r.OrgID, r.ID)
	})
	if errors.Is(err, errRunMoved) {
		return nil
	}
	if err == nil {
		a.Committed()
	}
	return err
}
