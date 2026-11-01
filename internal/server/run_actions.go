package server

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"orqen/internal/runstate"
	"strings"
	"time"
	"unicode/utf8"
)

type CancelRunRequest struct {
	Reason *string `json:"reason"`
}

// runAction runs f in an idempotent transaction on a run guarded by If-Match, and answers with the new RunDto.
func (a *App) runAction(w http.ResponseWriter, r *http.Request, name string, body any, f func(ctx context.Context, run RunDoc) (RunDoc, error)) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("runId")
	route := "POST /api/v1/orgs/{orgId}/runs/{runId}/" + name
	hash := requestHash("POST", route, map[string]string{"orgId": org, "runId": id, "if_match": fmt.Sprint(expected)}, body)
	return a.idempotent(w, r, org, route, key, hash, func(ctx context.Context) (Output, error) {
		run, err := a.run(ctx, org, id)
		if err != nil {
			return Output{}, err
		}
		if run.Revision != expected {
			c := problem("revision_conflict")
			c.Current = run.SummaryDTO(nil)
			return Output{}, c
		}
		next, err := f(ctx, run)
		if errors.Is(err, errRunMoved) {
			return Output{}, problem("run_state_conflict")
		}
		if err != nil {
			return Output{}, err
		}
		dto, err := a.runDTO(ctx, next)
		if err != nil {
			return Output{}, err
		}
		return Output{200, dto, map[string]string{"etag": etag(next.Revision)}}, nil
	})
}

// gateBlockers evaluates the start gate for a run: the project must be enabled and its readiness Ready.
func (a *App) gateBlockers(ctx context.Context, run RunDoc) (code string, blockers []string, err error) {
	proj, err := a.project(ctx, run.OrgID, run.ProjectID)
	if err != nil {
		return "", nil, err
	}
	if !proj.AutomationEnabled {
		return runstate.HoldProjectNotEnabled, []string{"project_not_enabled"}, nil
	}
	res, err := a.readinessResults(ctx, run.OrgID, []ProjectDoc{proj})
	if err != nil {
		return "", nil, err
	}
	if !res[0].Ready {
		for _, b := range res[0].Blockers {
			blockers = append(blockers, b.Code)
		}
		return runstate.HoldProjectNotReady, blockers, nil
	}
	return "", nil, nil
}

func (a *App) startRun(w http.ResponseWriter, r *http.Request) error {
	return a.runAction(w, r, "start", nil, func(ctx context.Context, run RunDoc) (RunDoc, error) {
		if paused, e := a.orgPaused(ctx, run.OrgID); e != nil {
			return run, e
		} else if paused {
			return run, problem("automation_paused")
		}
		if !run.Active || run.State != runstate.Planned || run.Checkpoint.Stage != runstate.StageStartGate || run.Hold == nil ||
			!contains([]string{runstate.HoldCodingStart, runstate.HoldProjectNotReady, runstate.HoldProjectNotEnabled}, run.Hold.Code) {
			return run, problem("run_state_conflict")
		}
		code, blockers, err := a.gateBlockers(ctx, run)
		if err != nil {
			return run, err
		}
		if code != "" {
			e := problem("run_not_startable")
			for _, b := range blockers {
				e.Errors = append(e.Errors, FieldError{b, code})
			}
			return run, e
		}
		next, err := a.casRun(ctx, run, runstate.StageStartGate, bson.M{"hold": nil, "start_approved": true}, runEvent{Reason: "started", Actor: runstate.ActorUser, Extra: []eventSpec{{"run.resumed", bson.M{"reason": "started"}}}})
		if err != nil {
			return run, err
		}
		return next, a.enqueueRunAdvance(ctx, run.OrgID, run.ID)
	})
}

func (a *App) resumeRun(w http.ResponseWriter, r *http.Request) error {
	return a.runAction(w, r, "resume", nil, func(ctx context.Context, run RunDoc) (RunDoc, error) {
		if !run.Active {
			return run, problem("run_state_conflict")
		}
		set := bson.M{}
		switch {
		case run.State == runstate.WaitingNetwork && run.Hold == nil:
			// Pull the retry forward; the state is restored by the stage job.
			if _, err := a.DB.Coll("jobs").UpdateMany(ctx, bson.M{"org_id": run.OrgID, "dedupe_key": runAdvanceKey(run.ID), "active": true, "state": "queued"}, bson.M{"$set": bson.M{"next_at": time.Now().UTC()}}); err != nil {
				return run, err
			}
			if err := a.enqueueRunAdvance(ctx, run.OrgID, run.ID); err != nil {
				return run, err
			}
			return run, nil
		case run.Hold == nil:
			return run, problem("run_state_conflict")
		case run.Hold.Code == runstate.HoldProjectNotReady || run.Hold.Code == runstate.HoldProjectNotEnabled:
			set["hold"] = nil
		case run.Hold.Code == runstate.HoldAutomationPaused:
			if paused, e := a.orgPaused(ctx, run.OrgID); e != nil {
				return run, e
			} else if paused {
				return run, problem("automation_paused")
			}
			set["hold"] = nil
		case run.Hold.Code == runstate.HoldRestored:
			// Only this explicit user action clears the hold a restore sets; the sweep never does (X34).
			if paused, e := a.orgPaused(ctx, run.OrgID); e != nil {
				return run, e
			} else if paused {
				return run, problem("automation_paused")
			}
			set["hold"] = nil
		case run.Hold.Code == runstate.HoldLeftFilter:
			if run.Eligibility != "eligible" {
				return run, problem("run_state_conflict")
			}
			set["hold"] = nil
		default:
			return run, problem("run_state_conflict")
		}
		next, err := a.casRun(ctx, run, "", set, runEvent{Reason: "resumed", Actor: runstate.ActorUser, Extra: []eventSpec{{"run.resumed", bson.M{"reason": "user"}}}})
		if err != nil {
			return run, err
		}
		return next, a.enqueueRunAdvance(ctx, run.OrgID, run.ID)
	})
}

// cancelRunTx cancels a run in the caller's transaction (PLAN-S3-GO S29): the run ends, its open Inbox item is
// cancelled, its stage job is retired (a running handler loses its lease and stops) and cleanup is scheduled.
func (a *App) cancelRunTx(ctx context.Context, run RunDoc, terminalReason, detail string) (RunDoc, error) {
	now := time.Now().UTC()
	leased, err := a.DB.Coll("jobs").CountDocuments(ctx, bson.M{"org_id": run.OrgID, "dedupe_key": runAdvanceKey(run.ID), "active": true, "state": "leased", "lease_expires_at": bson.M{"$gt": now}})
	if err != nil {
		return run, err
	}
	liveRoles, err := a.liveRoles(ctx, run.OrgID, run.ID)
	if err != nil {
		return run, err
	}
	canc := RunCancellation{State: "requested", RequestedAt: now}
	if leased == 0 && liveRoles == 0 {
		canc.State, canc.ConfirmedAt = "confirmed", &now
	}
	reason := terminalReason
	if reason == "" {
		reason = "cancelled"
	}
	extra := []eventSpec{{"run.cancel_requested", bson.M{"reason": reason}}}
	if canc.State == "confirmed" {
		extra = append(extra, eventSpec{"run.cancel_confirmed", bson.M{}})
	}
	next, err := a.casRun(ctx, run, "", bson.M{"state": runstate.Cancelled, "active": false, "cancellation": canc, "terminal": RunTerminal{Reason: reason, At: now}, "open_inbox_id": nil, "hold": nil},
		runEvent{Reason: reason, Actor: runstate.ActorUser, Extra: extra})
	if err != nil {
		return run, err
	}
	if run.OpenInboxID != nil {
		if err = a.closeInbox(ctx, run.OrgID, *run.OpenInboxID, "cancelled", nil); err != nil {
			return run, err
		}
	}
	if err = a.cancelRunJob(ctx, run.OrgID, run.ID); err != nil {
		return run, err
	}
	if _, err = a.requestRoleCancelTx(ctx, run); err != nil {
		return run, err
	}
	if _, err = a.endAttempt(ctx, run, "cancelled:"+reason); err != nil {
		return run, err
	}
	if err = a.enqueueCleanup(ctx, run.OrgID); err != nil {
		return run, err
	}
	_ = detail
	return next, nil
}

func (a *App) cancelRun(w http.ResponseWriter, r *http.Request) error {
	var req CancelRunRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &req, true); err != nil {
			return err
		}
	}
	if req.Reason != nil && utf8.RuneCountInString(*req.Reason) > 500 {
		return validation("reason", "must be at most 500 characters")
	}
	return a.runAction(w, r, "cancel", req, func(ctx context.Context, run RunDoc) (RunDoc, error) {
		if !run.Active || runstate.Terminal(run.State) {
			return run, problem("run_state_conflict")
		}
		detail := ""
		if req.Reason != nil {
			detail = strings.TrimSpace(*req.Reason)
		}
		return a.cancelRunTx(ctx, run, "cancelled", detail)
	})
}

func (a *App) retryRun(w http.ResponseWriter, r *http.Request) error {
	return a.runAction(w, r, "retry", nil, func(ctx context.Context, run RunDoc) (RunDoc, error) {
		if run.Active || (run.State != runstate.Failed && run.State != runstate.Cancelled) {
			return run, problem("run_state_conflict")
		}
		n, err := a.DB.Coll("runs").CountDocuments(ctx, bson.M{"org_id": run.OrgID, "connection_id": run.ConnectionID, "issue_id": run.IssueID, "active": true})
		if err != nil {
			return run, err
		}
		if n > 0 {
			return run, problem("run_state_conflict")
		}
		if paused, e := a.orgPaused(ctx, run.OrgID); e != nil {
			return run, e
		} else if paused {
			return run, problem("automation_paused")
		}
		pv, err := a.livePolicy(ctx, run.OrgID, run.ProjectID)
		if err != nil {
			return run, err
		}
		now := time.Now().UTC()
		attempt := run.Attempt + 1
		var next RunDoc
		err = a.DB.Coll("runs").FindOneAndUpdate(ctx, bson.M{"_id": run.ID, "org_id": run.OrgID, "revision": run.Revision, "active": false}, bson.M{"$set": bson.M{
			"state": runstate.GatheringContext, "active": true, "attempt": attempt, "hold": nil, "failure": nil, "terminal": nil, "cancellation": nil, "open_inbox_id": nil, "stale": false, "eligibility": "eligible", "start_approved": false,
			"checkpoint": RunCheckpoint{Stage: runstate.StageContext, Since: now}, "pinned.packet_cap": pv.Int("context.packet_bytes"), "context": nil, "triage": nil, "plan": nil, "updated_at": now}, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&next)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return run, problem("run_state_conflict")
		}
		if err != nil {
			return run, err
		}
		if _, err = a.DB.Coll("run_attempts").InsertOne(ctx, RunAttemptDoc{ID: newID("rat"), OrgID: run.OrgID, RunID: run.ID, Number: attempt, Reason: "retry", FromStage: run.Checkpoint.Stage, StartedAt: now}); err != nil {
			return run, err
		}
		if _, err = a.DB.Coll("tracked_issues").UpdateOne(ctx, bson.M{"_id": run.TrackedIssueID, "org_id": run.OrgID}, bson.M{"$set": bson.M{"active_run_id": run.ID, "latest_run_id": run.ID, "run_state": next.State}}); err != nil {
			return run, err
		}
		rev := next.Revision
		for _, ev := range []eventSpec{{"run.state_changed", bson.M{"from": run.State, "to": next.State, "stage": runstate.StageContext, "reason": "retry", "actor": runstate.ActorUser}}, {"run.retried", bson.M{"attempt": attempt}}} {
			if err = a.AppendEvent(ctx, run.OrgID, ev.Type, Subject{"run", run.ID}, ev.Payload, &rev, nil, false); err != nil {
				return run, err
			}
		}
		return next, a.enqueueRunAdvance(ctx, run.OrgID, run.ID)
	})
}
