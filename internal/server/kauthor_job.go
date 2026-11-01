package server

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"orqen/internal/codexhost"

	"orqen/internal/kauthor"
)

var terminalAuthorStates = []string{kauthor.Applied, kauthor.Failed, kauthor.Cancelled}

// failAuthorSession ends a session with a code and, for a real failure, leaves the run's knowledge to the
// deterministic author when learn_on_merge is on (Y12). A cancelled session never falls back by itself.
func (a *App) failAuthorSession(ctx context.Context, lease Lease, kas KnowledgeAuthorSessionDoc, code string) error {
	state := kauthor.Failed
	if code == kauthor.CodeCancelled {
		state = kauthor.Cancelled
	}
	if err := a.fenced(ctx, lease, func(sc context.Context) error {
		_, e := a.kasSet(sc, kas.OrgID, kas.ID, 0, nil, bson.M{"state": state, "code": code})
		return e
	}); err != nil {
		return err
	}
	if state == kauthor.Cancelled {
		return nil
	}
	run, err := a.run(ctx, kas.OrgID, kas.RunID)
	if err != nil {
		return nil // an origin run that is gone has nothing to fall back to
	}
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return err
	}
	if !pv.Bool("knowledge.learn_on_merge") {
		return nil
	}
	cur, err := a.authorSession(ctx, kas.OrgID, kas.ID)
	if err != nil {
		return err
	}
	_, err = a.fallbackAuthor(ctx, cur)
	return err
}

// finishAuthorPass completes the org's author job, or schedules it again at once when a session is still waiting.
func (a *App) finishAuthorPass(ctx context.Context, lease Lease, org string, delay time.Duration) error {
	return a.fenced(ctx, lease, func(sc context.Context) error {
		n, err := a.DB.Coll("knowledge_author_sessions").CountDocuments(sc, bson.M{"org_id": org, "state": bson.M{"$nin": terminalAuthorStates}})
		if err != nil {
			return err
		}
		if n == 0 {
			return a.completeJob(sc, lease)
		}
		return a.leaseWrite(sc, lease, bson.M{"state": "queued", "lease_owner": nil, "lease_expires_at": nil, "next_at": time.Now().UTC().Add(delay), "updated_at": time.Now().UTC()})
	})
}

// kauthorMaxSteps bounds one pass of the job; the next pass continues.
const kauthorMaxSteps = 40

// runKnowledgeAuthorRoles advances the oldest unfinished session of the org (one job per org serialises them).
func (a *App) runKnowledgeAuthorRoles(ctx context.Context, job JobDoc, lease Lease) error {
	org := job.OrgID
	list, err := findMany[KnowledgeAuthorSessionDoc](ctx, a.DB, "knowledge_author_sessions", bson.M{"org_id": org, "state": bson.M{"$nin": terminalAuthorStates}},
		bson.D{{Key: "created_at", Value: 1}, {Key: "_id", Value: 1}}, 1)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return a.finishAuthorPass(ctx, lease, org, 0)
	}
	delay, err := a.driveAuthorSession(ctx, lease, list[0])
	if err != nil {
		return err
	}
	return a.finishAuthorPass(ctx, lease, org, delay)
}

// driveAuthorSession runs steps of one session until it must wait; it returns how long the job should wait.
func (a *App) driveAuthorSession(ctx context.Context, lease Lease, kas KnowledgeAuthorSessionDoc) (time.Duration, error) {
	fail := func(code string) (time.Duration, error) { return 0, a.failAuthorSession(ctx, lease, kas, code) }
	origin, err := a.run(ctx, kas.OrgID, kas.RunID)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return fail(kauthor.CodeOriginMissing)
	}
	if err != nil {
		return 0, err
	}
	pv, err := a.pinnedPolicy(ctx, origin)
	if err != nil {
		return 0, err
	}
	ver, err := a.profileVersionOf(ctx, origin)
	if err != nil {
		return fail(kauthor.CodeCatherdUnavailable)
	}
	r := &kasRunner{a: a, lease: lease, kas: kas, origin: origin, pv: pv, ver: ver}
	defer r.close()
	poll := time.Duration(pv.Int("roles.poll_interval_secs")) * time.Second
	hostFailed := func(err error) (time.Duration, error) {
		cur, e := a.authorSession(ctx, kas.OrgID, kas.ID)
		if e != nil {
			return 0, e
		}
		r.kas = cur
		n := cur.HostFailures + 1
		if n >= kauthor.MaxHostFailures {
			return fail(kauthor.CodeHostUnavailable)
		}
		if _, e = r.set(ctx, nil, bson.M{"host_failures": n}); e != nil {
			return 0, e
		}
		return kauthorHostBackoff, nil
	}
	needHost := func() error {
		if r.hostUp() {
			return nil
		}
		return r.openHost(ctx)
	}
	for i := 0; i < kauthorMaxSteps; i++ {
		cur, err := a.authorSession(ctx, kas.OrgID, kas.ID)
		if err != nil {
			return 0, err
		}
		r.kas = cur
		if kauthor.Terminal(cur.State) {
			return 0, nil
		}
		paused, err := a.orgPaused(ctx, kas.OrgID)
		if err != nil {
			return 0, err
		}
		f, err := r.facts(ctx, paused)
		if err != nil {
			return 0, err
		}
		step := kauthor.Next(f)
		var e error
		changed := true
		switch step.Kind {
		case kauthor.Noop:
			return 0, nil
		case kauthor.Pause, kauthor.Wait:
			return poll, nil
		case kauthor.Fail:
			if step.Code == kauthor.CodeTimeout {
				r.cancelRoles(ctx)
			}
			return fail(step.Code)
		case kauthor.Cancel:
			r.cancelRoles(ctx)
			return fail(kauthor.CodeCancelled)
		case kauthor.StartHost:
			if e = needHost(); e == nil && cur.State == kauthor.Queued {
				changed, e = r.set(ctx, []string{kauthor.Queued}, bson.M{"state": kauthor.Starting})
			}
		case kauthor.StartCatherdRun:
			if e = needHost(); e == nil {
				e = r.ensureCatherdRun(ctx)
			}
		case kauthor.DispatchWriter:
			if e = needHost(); e == nil {
				changed, e = r.dispatchRole(ctx, true)
			}
		case kauthor.DispatchKVerify:
			if e = needHost(); e == nil {
				changed, e = r.dispatchRole(ctx, false)
			}
		case kauthor.Reconcile:
			if e = needHost(); e == nil {
				writer := cur.State == kauthor.WriterDispatching
				changed, e = r.reconcile(ctx, writer)
			}
		case kauthor.CollectWriter, kauthor.CollectKVerify:
			writer := step.Kind == kauthor.CollectWriter
			role := r.role(writer)
			env, ee := a.catherdEnv(kas.OrgID)
			if ee != nil {
				return 0, ee
			}
			view, ee := a.Catherd.RunsShow(ctx, env, cur.Catherd.RunID)
			if ee != nil {
				return poll, nil
			}
			rec, ok := view.RecordFor(role.DispatchID)
			if !ok {
				return poll, nil
			}
			if e = needHost(); e == nil {
				changed, e = r.collectRole(ctx, writer, rec)
			}
		case kauthor.RetryWriter:
			changed, e = r.retryWriter(ctx)
		case kauthor.Apply:
			e = a.applyAuthorSession(ctx, lease, r)
		default:
			return fail(kauthor.CodeRoleFailed)
		}
		if e != nil {
			var te *codexhost.ToolError
			var rf *runFailureError
			switch {
			case errors.As(e, &te):
				return fail(kauthor.CodeRoleFailed)
			case errors.As(e, &rf):
				return fail(kauthor.CodeCatherdUnavailable)
			case isHostErr(e):
				return hostFailed(e)
			}
			return 0, e
		}
		_ = changed
	}
	return 0, nil
}

// isHostErr reports whether an error came from the host or catherd side (as opposed to our own storage).
func isHostErr(err error) bool {
	var hf *hostFault
	var jf *JobFailure
	return errors.Is(err, errOutcomeUnknown) || errors.As(err, &hf) || errors.As(err, &jf)
}

// applyAuthorSession turns the two stored replies into the proposal and ends the session as applied.
func (a *App) applyAuthorSession(ctx context.Context, lease Lease, r *kasRunner) error {
	writer, err := r.readArtifact(ctx, r.kas.Writer.ReplyArtifactID)
	if err != nil {
		return err
	}
	kverify, err := r.readArtifact(ctx, r.kas.KVerifier.ReplyArtifactID)
	if err != nil {
		return err
	}
	prop, err := a.applyAuthorReplies(ctx, r.kas, writer, kverify)
	if err != nil {
		return err
	}
	return a.fenced(ctx, lease, func(sc context.Context) error {
		_, e := a.kasSet(sc, r.kas.OrgID, r.kas.ID, 0, []string{kauthor.KVerifyDone}, bson.M{"state": kauthor.Applied, "proposal_id": prop.ID, "code": ""})
		if errors.Is(e, mongo.ErrNoDocuments) {
			return nil
		}
		return e
	})
}
