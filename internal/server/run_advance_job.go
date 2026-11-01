package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"log/slog"
	"orqen/internal/providers"
	"orqen/internal/runstate"
	"orqen/internal/workspace"
	"time"
)

// maxStagesPerJob bounds how long one job holds a run before it re-enqueues itself.
const maxStagesPerJob = 8

// stageCtx carries what every stage needs.
type stageCtx struct {
	a     *App
	lease Lease
	job   JobDoc
}

// hook is the test seam called after a stage's external work and before its commit.
func (a *App) hook(stage string) error {
	if a.runHook == nil {
		return nil
	}
	return a.runHook(stage)
}

// runAdvance runs the stages of one run. One job per run (dedupe run:<id>) means at most one worker; the job runs up
// to maxStagesPerJob stages and then re-enqueues itself in the same fenced transaction that completes it.
func (a *App) runAdvance(ctx context.Context, job JobDoc, lease Lease) error {
	runID, _ := job.Payload["run_id"].(string)
	if !validID("run", runID) {
		return &JobFailure{Code: "bad_payload", Message: "missing run_id"}
	}
	done := func() error {
		return a.fenced(ctx, lease, func(sc context.Context) error { return a.completeJob(sc, lease) })
	}
	sc := &stageCtx{a: a, lease: lease, job: job}
	for i := 0; i < maxStagesPerJob; i++ {
		run, err := a.run(ctx, job.OrgID, runID)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return done()
		}
		if err != nil {
			return err
		}
		parked, err := sc.advanceOnce(ctx, run)
		if err != nil {
			if errors.Is(err, errRunMoved) {
				return done()
			}
			return sc.stageFailure(ctx, run, err)
		}
		if parked {
			return done()
		}
	}
	return a.fenced(ctx, lease, func(tx context.Context) error {
		if err := a.completeJob(tx, lease); err != nil {
			return err
		}
		return a.enqueueRunAdvance(tx, job.OrgID, runID)
	})
}

// advanceOnce executes the run's current stage. parked means the run waits for something else (a user, the
// scheduler, Sprint 4) and the job is finished.
func (s *stageCtx) advanceOnce(ctx context.Context, run RunDoc) (parked bool, err error) {
	a := s.a
	switch {
	case !run.Active || runstate.Terminal(run.State):
		return true, nil
	case run.Cancellation != nil || run.Hold != nil:
		return true, nil
	case runstate.SlotHolding(run.State) && run.Slot == nil:
		return true, nil // the dispatcher gives the run a coding slot first
	case run.State == runstate.NeedsInput || run.State == runstate.Queued || run.State == runstate.WaitingDep:
		return true, nil
	}
	// In the coding phase a pause only stops new dispatches (dispatchRole holds); live roles are still collected.
	if paused, e := a.orgPaused(ctx, run.OrgID); e != nil {
		return false, e
	} else if paused && !runstate.AtOrAfterCoding(run.Checkpoint.Stage) {
		return true, s.hold(ctx, run, runstate.HoldAutomationPaused, "automation is paused for the organization")
	}
	if run.Eligibility == "left_filter" {
		return true, nil
	}
	if run.State == runstate.WaitingNetwork {
		// The outage is over: restore the state of the saved stage and carry on.
		err = a.fenced(ctx, s.lease, func(tx context.Context) error {
			_, e := a.casRun(tx, run, run.Checkpoint.Stage, bson.M{"state": runstate.StateOf(run.Checkpoint.Stage)}, runEvent{Reason: "network_restored", Actor: runstate.ActorSystem, Extra: []eventSpec{{"run.resumed", bson.M{"reason": "network_restored"}}}})
			return e
		})
		if err != nil {
			return false, err
		}
		if run, err = a.run(ctx, run.OrgID, run.ID); err != nil {
			return false, err
		}
	}
	switch run.Checkpoint.Stage {
	case runstate.StageContext:
		return s.stageContext(ctx, run)
	case runstate.StageTriage:
		return s.stageTriage(ctx, run)
	case runstate.StagePlan:
		return s.stagePlan(ctx, run)
	case runstate.StagePlanGate:
		return s.stagePlanGate(ctx, run)
	case runstate.StageStartGate:
		return s.stageStartGate(ctx, run)
	case runstate.StageWorkspace:
		return s.stageWorkspace(ctx, run)
	case runstate.StageHostSession:
		return s.stageHostSession(ctx, run)
	case runstate.StageArchitect:
		return s.stageArchitect(ctx, run)
	case runstate.StageArchitectGate:
		return s.stageArchitectGate(ctx, run)
	case runstate.StageImplement:
		return s.stageImplement(ctx, run)
	case runstate.StageVerify:
		return s.stageVerify(ctx, run)
	case runstate.StagePublish:
		return s.stagePublish(ctx, run)
	case runstate.StageReview:
		return s.stageReview(ctx, run)
	case runstate.StageComment:
		return s.stageComment(ctx, run)
	case runstate.StageLand:
		return s.stageLand(ctx, run)
	case runstate.StageCI:
		return s.stageCI(ctx, run)
	}
	return true, nil
}

// hold sets a hold: the state stays, the run stops advancing, and the scheduler or the user releases it.
func (s *stageCtx) hold(ctx context.Context, run RunDoc, code, detail string) error {
	return s.a.fenced(ctx, s.lease, func(tx context.Context) error {
		_, err := s.a.casRun(tx, run, run.Checkpoint.Stage, bson.M{"hold": RunHold{Code: code, Detail: detail, Since: time.Now().UTC()}},
			runEvent{Reason: code, Actor: runstate.ActorSystem, Extra: []eventSpec{{"run.held", bson.M{"code": code}}}})
		return err
	})
}

// complete finishes a stage: the run moves to the next stage (and its state), with the stage-completed event.
func (s *stageCtx) complete(tx context.Context, run RunDoc, stage, next string, set bson.M, extra ...eventSpec) (RunDoc, error) {
	if set == nil {
		set = bson.M{}
	}
	set["checkpoint"] = RunCheckpoint{Stage: next, Since: time.Now().UTC()}
	set["host_faults"] = 0
	round := run.Round
	if v, ok := set["round"].(int); ok {
		round = v
	}
	if st := runstate.StateOfRound(next, round); st != "" && st != run.State {
		set["state"] = st
	}
	events := append([]eventSpec{{"run.stage_completed", bson.M{"stage": stage}}}, extra...)
	return s.a.casRun(tx, run, stage, set, runEvent{Reason: stage + "_completed", Actor: runstate.ActorSystem, Extra: events})
}

// stageFailure turns a stage error into the right run outcome and the job's retry policy.
func (s *stageCtx) stageFailure(ctx context.Context, run RunDoc, err error) error {
	a := s.a
	if errors.Is(err, errLeaseLost) || ctx.Err() != nil {
		return err // the lease is gone or the process is stopping: someone else continues
	}
	var stopped *stageStop
	if errors.As(err, &stopped) {
		return a.fenced(ctx, s.lease, func(tx context.Context) error {
			cur, e := a.run(tx, run.OrgID, run.ID)
			if e != nil {
				return e
			}
			if e = a.stopRun(tx, cur, stopped.Spec); errors.Is(e, errRunMoved) {
				return nil
			} else if e != nil {
				return e
			}
			return a.completeJob(tx, s.lease)
		})
	}
	var hf *hostFault
	if errors.As(err, &hf) {
		return s.hostFailure(ctx, run, hf)
	}
	var jf *JobFailure
	if errors.As(err, &jf) {
		return err // host, catherd or database trouble: the job retries with backoff
	}
	if pe, ok := providers.AsError(err); ok {
		return s.providerFailure(ctx, run, pe, err)
	}
	if handled, e := s.workspaceFailure(ctx, run, err); handled {
		return e
	}
	if code, ok := runFailureCode(err); ok {
		return s.failRun(ctx, run, code, err.Error())
	}
	if isTransient(err) {
		return err
	}
	slog.Error("run stage failed", "run_id", run.ID, "stage", run.Checkpoint.Stage, "error", err)
	return s.failRun(ctx, run, "internal", "internal error")
}

func (s *stageCtx) providerFailure(ctx context.Context, run RunDoc, pe *providers.Error, err error) error {
	a := s.a
	switch {
	case pe.Kind == providers.KindCredentialExpired || pe.Kind == providers.KindPermissionDenied:
		return s.stopFromError(ctx, run, stopSpec{Reason: "credential_expired", QuestionCode: "credential_expired", Options: resolveOptions()})
	case pe.Kind == providers.KindNotFound:
		return s.stopFromError(ctx, run, stopSpec{Reason: "project_mapping", QuestionCode: "provider_not_found", Options: resolveOptions()})
	case pe.Retryable():
		if cid := run.ConnectionID; cid != "" {
			a.noteRateLimit(cid, pe)
		}
		if e := s.markWaitingNetwork(ctx, run); e != nil {
			return e
		}
		return err // the job retries with backoff; waiting_network is restored on the next attempt
	}
	return s.failRun(ctx, run, "provider_"+string(pe.Kind), pe.Message)
}

// stopFromError opens the Inbox question for an error and completes the job.
func (s *stageCtx) stopFromError(ctx context.Context, run RunDoc, spec stopSpec) error {
	return s.stageFailure(ctx, run, &stageStop{Spec: spec})
}
func (s *stageCtx) failRun(ctx context.Context, run RunDoc, code, message string) error {
	a := s.a
	return a.fenced(ctx, s.lease, func(tx context.Context) error {
		cur, e := a.run(tx, run.OrgID, run.ID)
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		if _, e = a.casRun(tx, cur, "", bson.M{"state": runstate.Failed, "active": false, "failure": RunFailure{Code: code, Message: message}, "terminal": RunTerminal{Reason: "failed", At: now}},
			runEvent{Reason: code, Actor: runstate.ActorSystem, Extra: []eventSpec{{"run.failed", bson.M{"code": code}}}}); errors.Is(e, errRunMoved) {
			return a.completeJob(tx, s.lease)
		} else if e != nil {
			return e
		}
		if _, e = a.endAttempt(tx, cur, "failed:"+code); e != nil {
			return e
		}
		return a.completeJob(tx, s.lease)
	})
}

// endAttempt closes the run's open attempt.
func (a *App) endAttempt(sc context.Context, run RunDoc, outcome string) (int64, error) {
	now := time.Now().UTC()
	res, err := a.DB.Coll("run_attempts").UpdateOne(sc, bson.M{"org_id": run.OrgID, "run_id": run.ID, "number": run.Attempt, "ended_at": nil}, bson.M{"$set": bson.M{"ended_at": now, "outcome": outcome}})
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

// noteRateLimit starts the connection cooldown after a 429.
func (a *App) noteRateLimit(conID string, pe *providers.Error) {
	if pe.Kind != providers.KindRateLimited {
		return
	}
	wait := pe.RetryAfter
	if wait <= 0 {
		wait = 5 * time.Second
	}
	a.cooldown.Store(conID, time.Now().Add(wait))
}

// stageStop is returned by a stage to park the run in needs_input.
type stageStop struct{ Spec stopSpec }

func (e *stageStop) Error() string { return "run needs input: " + e.Spec.Reason }

// runFailureError makes a stage fail its run with a code (invalid output, bad configuration).
type runFailureError struct{ Code, Message string }

func (e *runFailureError) Error() string { return e.Message }
func runFailureCode(err error) (string, bool) {
	var f *runFailureError
	if errors.As(err, &f) {
		return f.Code, true
	}
	return "", false
}

// isTransient reports database and cancellation errors that a retry can fix.
func isTransient(err error) bool {
	return mongo.IsNetworkError(err) || mongo.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// workspaceFailure maps workspace manager errors to the run outcome.
func (s *stageCtx) workspaceFailure(ctx context.Context, run RunDoc, err error) (bool, error) {
	switch {
	case errors.Is(err, workspace.ErrAuth):
		return true, s.stopFromError(ctx, run, stopSpec{Reason: "credential_expired", QuestionCode: "credential_expired", Options: resolveOptions()})
	case errors.Is(err, workspace.ErrNotFound):
		return true, s.stopFromError(ctx, run, stopSpec{Reason: "project_mapping", QuestionCode: "target_branch_missing", Options: resolveOptions()})
	case errors.Is(err, workspace.ErrDiskLow):
		return true, s.stopFromError(ctx, run, stopSpec{Reason: "dependency_unavailable", QuestionCode: "disk_low", Options: resolveOptions()})
	case errors.Is(err, workspace.ErrGitMissing):
		return true, s.stopFromError(ctx, run, stopSpec{Reason: "dependency_unavailable", QuestionCode: "git_unavailable", Options: resolveOptions()})
	case errors.Is(err, workspace.ErrNetwork):
		if e := s.markWaitingNetwork(ctx, run); e != nil {
			return true, e
		}
		return true, &JobFailure{Code: "git_network", Message: "the repository could not be reached", Retryable: true}
	case errors.Is(err, workspace.ErrUnsafePath), errors.Is(err, workspace.ErrBadURL), errors.Is(err, workspace.ErrDirty), errors.Is(err, workspace.ErrGit):
		return true, s.failRun(ctx, run, "workspace_failed", "the workspace could not be prepared")
	}
	return false, nil
}

// markWaitingNetwork moves the run to waiting_network, keeping its stage.
func (s *stageCtx) markWaitingNetwork(ctx context.Context, run RunDoc) error {
	a := s.a
	return a.fenced(ctx, s.lease, func(tx context.Context) error {
		cur, e := a.run(tx, run.OrgID, run.ID)
		if e != nil || cur.State == runstate.WaitingNetwork {
			return e
		}
		if _, e = a.casRun(tx, cur, cur.Checkpoint.Stage, bson.M{"state": runstate.WaitingNetwork}, runEvent{Reason: "network", Actor: runstate.ActorSystem}); errors.Is(e, errRunMoved) {
			return nil
		} else {
			return e
		}
	})
}
