package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"log/slog"
	"orqen/internal/catherd"
	"orqen/internal/runstate"
	"time"
)

// catherdFromRecord copies a catherd record into the role execution document.
func catherdFromRecord(base *RexCatherd, r catherd.RunRecord, family string) *RexCatherd {
	c := &RexCatherd{}
	if base != nil {
		*c = *base
	}
	c.RunID, c.DispatchID, c.Rung, c.Backend, c.Family, c.Attempt, c.Status = r.RunID, r.DispatchID, r.Rung, r.Backend, family, r.Attempt, r.Status
	c.Thread, c.FailoverFrom, c.ReplyStatus, c.ReplyWhy = "", "", "", ""
	if r.Thread != nil {
		c.Thread = *r.Thread
	}
	if r.FailoverFrom != nil {
		c.FailoverFrom = *r.FailoverFrom
	}
	if r.ReplyStatus != nil {
		c.ReplyStatus = *r.ReplyStatus
	}
	if r.ReplyWhy != nil {
		c.ReplyWhy = *r.ReplyWhy
	}
	c.Environment, c.Secs, c.Tokens, c.CostUSD = r.Environment, r.Secs, r.Tokens, r.CostUSD
	c.ChangedOwned, c.Violations, c.ReplyPath = nonNilStr(r.ChangedOwned), nonNilStr(r.Violations), r.ReplyPath
	return c
}

// runRoleWatch polls catherd for the records of the run's dispatched roles and wakes the run when one appears. It
// re-enqueues itself in the transaction that completes it while a role is still running.
func (a *App) runRoleWatch(ctx context.Context, job JobDoc, lease Lease) error {
	runID, _ := job.Payload["run_id"].(string)
	done := func() error {
		return a.fenced(ctx, lease, func(sc context.Context) error { return a.completeJob(sc, lease) })
	}
	run, err := a.run(ctx, job.OrgID, runID)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return done()
	}
	if err != nil {
		return err
	}
	live, err := findMany[RoleExecDoc](ctx, a.DB, "role_executions", bson.M{"org_id": run.OrgID, "run_id": run.ID, "state": bson.M{"$in": []string{RexDispatched, RexCancelRequested}}}, bson.D{{Key: "_id", Value: 1}}, 100)
	if err != nil {
		return err
	}
	if len(live) == 0 || run.CatherdRunID == "" || !run.Active {
		return done()
	}
	env, err := a.catherdEnv(run.OrgID)
	if err != nil {
		return err
	}
	view, err := a.Catherd.RunsShow(ctx, env, run.CatherdRunID)
	if err != nil {
		// catherd is unreachable: coding waits, nothing is lost; try again soon
		slog.Warn("role watch: catherd could not be read", "run_id", run.ID)
		return a.fenced(ctx, lease, func(sc context.Context) error {
			if e := a.completeJob(sc, lease); e != nil {
				return e
			}
			return a.enqueueRoleWatch(sc, run.OrgID, run.ID, 5*time.Second)
		})
	}
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return err
	}
	wall := time.Duration(pv.Int("roles.max_wall_secs")) * time.Second
	for _, r := range live {
		if r.State != RexDispatched || r.DispatchedAt == nil || time.Since(*r.DispatchedAt) < wall {
			continue
		}
		if _, ok := view.RecordFor(r.Catherd.DispatchID); !ok {
			if e := a.Catherd.RunsCancel(ctx, env, run.CatherdRunID, r.Name); e != nil {
				slog.Warn("role watch: could not cancel a role past its wall time", "run_id", run.ID, "name", r.Name)
			}
		}
	}
	families := map[string]string{}
	for _, r := range live {
		if rec, ok := view.RecordFor(r.Catherd.DispatchID); ok {
			families[r.ID] = a.familyOf(ctx, run.OrgID, rec.Rung)
		}
	}
	interval := time.Duration(pv.Int("roles.poll_interval_secs")) * time.Second
	return a.fenced(ctx, lease, func(sc context.Context) error {
		finished, remaining := 0, 0
		for _, r := range live {
			rec, ok := view.RecordFor(r.Catherd.DispatchID)
			if !ok {
				remaining++
				continue
			}
			from := []string{RexDispatched}
			state := RexFinished
			if r.State == RexCancelRequested {
				from, state = []string{RexCancelRequested}, RexCancelled
			}
			next, e := a.rexUpdate(sc, r, from, bson.M{"state": state, "catherd": catherdFromRecord(r.Catherd, rec, families[r.ID])})
			if errors.Is(e, errRunMoved) {
				continue
			}
			if e != nil {
				return e
			}
			finished++
			kind := "role.finished"
			if state == RexCancelled {
				kind = "role.cancelled"
			}
			if e = a.AppendEvent(sc, run.OrgID, kind, Subject{"run", run.ID}, bson.M{"run_id": run.ID, "rex_id": next.ID, "status": rec.Status}, nil, nil, true); e != nil {
				return e
			}
		}
		if e := a.completeJob(sc, lease); e != nil {
			return e
		}
		if finished > 0 {
			if e := a.enqueueRunAdvance(sc, run.OrgID, run.ID); e != nil {
				return e
			}
		}
		if remaining > 0 {
			return a.enqueueRoleWatch(sc, run.OrgID, run.ID, interval)
		}
		return nil
	})
}

// runRoleCancel stops the live roles of a cancelled run (H12): catherd's own cancel for each, no model involved.
func (a *App) runRoleCancel(ctx context.Context, job JobDoc, lease Lease) error {
	runID, _ := job.Payload["run_id"].(string)
	run, err := a.run(ctx, job.OrgID, runID)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return a.fenced(ctx, lease, func(sc context.Context) error { return a.completeJob(sc, lease) })
	}
	if err != nil {
		return err
	}
	live, err := findMany[RoleExecDoc](ctx, a.DB, "role_executions", bson.M{"org_id": run.OrgID, "run_id": run.ID, "state": bson.M{"$in": []string{RexDispatching, RexDispatched, RexCancelRequested}}}, bson.D{{Key: "_id", Value: 1}}, 100)
	if err != nil {
		return err
	}
	env, err := a.catherdEnv(run.OrgID)
	if err != nil {
		return err
	}
	for _, r := range live {
		if run.CatherdRunID == "" {
			break
		}
		if e := a.Catherd.RunsCancel(ctx, env, run.CatherdRunID, r.Name); e != nil {
			var ce *catherd.Error
			if errors.As(e, &ce) && ce.Code == "E_RUN_NOT_FOUND" {
				continue
			}
			return &JobFailure{Code: "role_cancel_failed", Message: "a role could not be cancelled", Retryable: true}
		}
	}
	var view catherd.RunView
	if run.CatherdRunID != "" {
		if view, err = a.Catherd.RunsShow(ctx, env, run.CatherdRunID); err != nil {
			return &JobFailure{Code: "role_cancel_failed", Message: "catherd could not be read", Retryable: true}
		}
	}
	return a.fenced(ctx, lease, func(sc context.Context) error {
		for _, r := range live {
			state := RexCancelled
			set := bson.M{"state": state}
			if r.Catherd != nil {
				if rec, ok := view.RecordFor(r.Catherd.DispatchID); ok {
					set["catherd"] = catherdFromRecord(r.Catherd, rec, "")
				}
			}
			if _, e := a.rexUpdate(sc, r, []string{RexDispatching, RexDispatched, RexCancelRequested}, set); e != nil && !errors.Is(e, errRunMoved) {
				return e
			}
			if e := a.AppendEvent(sc, run.OrgID, "role.cancelled", Subject{"run", run.ID}, bson.M{"run_id": run.ID, "rex_id": r.ID}, nil, nil, true); e != nil {
				return e
			}
		}
		if run.Cancellation != nil && run.Cancellation.State == "requested" {
			// The stage job's lease may still be live: the sweep confirms once it ends (cancel is not termination).
			leased, e := a.DB.Coll("jobs").CountDocuments(sc, bson.M{"org_id": run.OrgID, "dedupe_key": runAdvanceKey(run.ID), "lease_expires_at": bson.M{"$gt": time.Now().UTC()}})
			if e != nil {
				return e
			}
			if leased == 0 {
				if e = a.confirmCancellationTx(sc, run); e != nil {
					return e
				}
			}
		}
		return a.completeJob(sc, lease)
	})
}

// liveRoles counts the role executions of a run that may still run in catherd.
func (a *App) liveRoles(ctx context.Context, org, run string) (int64, error) {
	return a.DB.Coll("role_executions").CountDocuments(ctx, bson.M{"org_id": org, "run_id": run, "state": bson.M{"$in": []string{RexDispatching, RexDispatched, RexCancelRequested}}})
}

// requestRoleCancelTx marks the run's live roles cancel_requested and queues the cancel job.
func (a *App) requestRoleCancelTx(sc context.Context, run RunDoc) (int64, error) {
	res, err := a.DB.Coll("role_executions").UpdateMany(sc, bson.M{"org_id": run.OrgID, "run_id": run.ID, "state": bson.M{"$in": []string{RexDispatching, RexDispatched}}}, bson.M{"$set": bson.M{"state": RexCancelRequested, "updated_at": time.Now().UTC()}})
	if err != nil {
		return 0, err
	}
	live, err := a.liveRoles(sc, run.OrgID, run.ID)
	if err != nil || live == 0 {
		return live, err
	}
	if res.ModifiedCount > 0 {
		if err = a.AppendEvent(sc, run.OrgID, "role.cancel_requested", Subject{"run", run.ID}, bson.M{"run_id": run.ID}, nil, nil, true); err != nil {
			return live, err
		}
	}
	now := time.Now().UTC()
	err = a.enqueue(sc, JobDoc{ID: newID("job"), OrgID: run.OrgID, Kind: "role_cancel", DedupeKey: "rolecancel:" + run.ID, Payload: bson.M{"run_id": run.ID}, State: "queued", Active: true, MaxAttempts: 20, NextAt: now, CreatedAt: now, UpdatedAt: now})
	var dup *DuplicateActiveJob
	if errors.As(err, &dup) {
		err = nil
	}
	return live, err
}

// confirmCancellationTx sets a requested cancellation confirmed.
func (a *App) confirmCancellationTx(sc context.Context, r RunDoc) error {
	now := time.Now().UTC()
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
}

var _ = runstate.Queued
