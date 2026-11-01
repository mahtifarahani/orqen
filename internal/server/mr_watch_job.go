package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"orqen/internal/mrready"
	"orqen/internal/providers"
	"orqen/internal/runstate"
	"orqen/internal/workspace"
	"strings"
	"time"
)

func mrWatchKey(run string) string { return "mr:" + run }

func (a *App) enqueueMRWatch(sc context.Context, org, runID string, delay time.Duration) error {
	now := time.Now().UTC()
	err := a.enqueue(sc, JobDoc{ID: newID("job"), OrgID: org, Kind: "mr_watch", DedupeKey: mrWatchKey(runID), Payload: bson.M{"run_id": runID}, State: "queued", Active: true, MaxAttempts: 100000, NextAt: now.Add(delay), CreatedAt: now, UpdatedAt: now})
	var dup *DuplicateActiveJob
	if errors.As(err, &dup) {
		return nil
	}
	return err
}

// watchMR is the ci stage of a live run: it makes sure the watcher job exists and parks the run.
func (s *stageCtx) watchMR(ctx context.Context, run RunDoc) (bool, error) {
	if run.AdoptRemote {
		if err := s.adoptRemote(ctx, run); err != nil {
			return false, err
		}
		return true, nil
	}
	err := s.a.fenced(ctx, s.lease, func(tx context.Context) error { return s.a.enqueueMRWatch(tx, run.OrgID, run.ID, 0) })
	return true, err
}

func (a *App) runMRWatch(ctx context.Context, job JobDoc, lease Lease) error {
	runID, _ := job.Payload["run_id"].(string)
	return a.mrWatchOnce(ctx, job, lease, runID, true)
}

// runMRRefresh evaluates once, on demand.
func (a *App) runMRRefresh(ctx context.Context, job JobDoc, lease Lease) error {
	runID, _ := job.Payload["run_id"].(string)
	return a.mrWatchOnce(ctx, job, lease, runID, false)
}

func (a *App) mrWatchOnce(ctx context.Context, job JobDoc, lease Lease, runID string, repeat bool) error {
	done := func(next bool, delay time.Duration) error {
		return a.fenced(ctx, lease, func(sc context.Context) error {
			if e := a.completeJob(sc, lease); e != nil {
				return e
			}
			if job.OperationID != nil {
				if e := a.transitionOperation(sc, job, "succeeded", bson.M{}); e != nil {
					return e
				}
			}
			if next {
				return a.enqueueMRWatch(sc, job.OrgID, runID, delay)
			}
			return nil
		})
	}
	run, err := a.run(ctx, job.OrgID, runID)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return done(false, 0)
	}
	if err != nil {
		return err
	}
	if !run.Active || (run.State != runstate.AwaitingCI && run.State != runstate.ReadyForMerge) || run.Checkpoint.Stage != runstate.StageCI || run.Cancellation != nil {
		return done(false, 0)
	}
	mr, found, err := a.mrOfRun(ctx, run.OrgID, run.ID)
	if err != nil {
		return err
	}
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return err
	}
	interval := time.Duration(pv.Int("ci.poll_interval_secs")) * time.Second
	if a.ciPoll > 0 {
		interval = a.ciPoll
	}
	if !found || mr.Mode != "live" || mr.IID == 0 {
		return done(false, 0)
	}
	conn, err := a.connection(ctx, run.OrgID, mr.ConnectionID)
	if err != nil {
		return err
	}
	host, err := a.codeHost(ctx, conn)
	if err != nil {
		return err
	}
	rm, err := host.GetMR(ctx, mr.ProviderProjectID, mr.IID)
	if err != nil {
		a.noteRateLimitErr(conn.ID, err)
		if pe, ok := providers.AsError(err); ok && pe.Retryable() {
			return done(repeat, interval)
		}
		return err
	}
	// somebody else pushed to the branch?
	if rm.State == "opened" && rm.SHA != "" && rm.SHA != mr.LastPushedSHA {
		return a.foreignHead(ctx, lease, job, run, mr, conn, rm.SHA, interval, repeat)
	}
	switch rm.State {
	case "merged":
		return a.fenced(ctx, lease, func(sc context.Context) error {
			now := time.Now().UTC()
			cur, e := a.run(sc, run.OrgID, run.ID)
			if e != nil {
				return e
			}
			if _, e = a.casRun(sc, cur, runstate.StageCI, bson.M{"state": runstate.Merged, "active": false, "terminal": RunTerminal{Reason: "merged", At: now}, "hold": nil},
				runEvent{Reason: "merged", Actor: runstate.ActorSystem, Extra: []eventSpec{{"mr.merged", bson.M{"merge_commit_sha": rm.MergeCommitSHA}}}}); e != nil && !errors.Is(e, errRunMoved) {
				return e
			}
			if _, e = a.DB.Coll("merge_requests").UpdateOne(sc, bson.M{"_id": mr.ID, "org_id": mr.OrgID}, bson.M{"$set": bson.M{"state": "merged", "merged": MRMerged{At: now, MergeCommitSHA: rm.MergeCommitSHA}, "updated_at": now}, "$inc": bson.M{"revision": int64(1)}}); e != nil {
				return e
			}
			if _, e = a.endAttempt(sc, cur, "merged"); e != nil {
				return e
			}
			// Learning on merge: the author job reads the pinned learn_on_merge policy itself.
			if e = a.ensureJobTx(sc, run.OrgID, "knowledge_author", "kauthor:"+run.ID, 5, bson.M{"run_id": run.ID, "trigger": "merge"}); e != nil && !errors.Is(e, errJobRace) {
				return e
			}
			return a.completeJob(sc, lease)
		})
	case "closed":
		return a.fenced(ctx, lease, func(sc context.Context) error {
			cur, e := a.run(sc, run.OrgID, run.ID)
			if e != nil {
				return e
			}
			if _, e = a.casRun(sc, cur, runstate.StageCI, bson.M{"state": runstate.Cancelled, "active": false, "terminal": RunTerminal{Reason: "mr_closed_by_user", At: time.Now().UTC()}, "hold": nil},
				runEvent{Reason: "mr_closed_by_user", Actor: runstate.ActorSystem, Extra: []eventSpec{{"mr.closed", bson.M{}}}}); e != nil && !errors.Is(e, errRunMoved) {
				return e
			}
			if _, e = a.DB.Coll("merge_requests").UpdateOne(sc, bson.M{"_id": mr.ID, "org_id": mr.OrgID}, bson.M{"$set": bson.M{"state": "closed", "updated_at": time.Now().UTC()}, "$inc": bson.M{"revision": int64(1)}}); e != nil {
				return e
			}
			if _, e = a.endAttempt(sc, cur, "cancelled:mr_closed_by_user"); e != nil {
				return e
			}
			return a.completeJob(sc, lease)
		})
	}
	// the pipeline of the head
	var pipe *MRPipeline
	pipes, err := host.ListMRPipelines(ctx, mr.ProviderProjectID, mr.IID)
	if err != nil {
		a.noteRateLimitErr(conn.ID, err)
		return err
	}
	var newest *providers.PipelineRef
	for i := range pipes {
		if newest == nil || pipes[i].ID > newest.ID {
			newest = &pipes[i]
		}
	}
	if newest != nil {
		ref, e := host.GetPipeline(ctx, mr.ProviderProjectID, newest.ID)
		if e != nil {
			return e
		}
		jobs, e := host.ListPipelineJobs(ctx, mr.ProviderProjectID, newest.ID)
		if e != nil {
			return e
		}
		pipe = &MRPipeline{ID: ref.ID, SHA: ref.SHA, Status: ref.Status, Source: ref.Source, FetchedAt: time.Now().UTC()}
		for _, j := range jobs {
			pipe.Jobs = append(pipe.Jobs, PipelineJobDoc{Name: j.Name, Status: j.Status, AllowFailure: j.AllowFailure})
		}
	}
	unresolved, err := host.ListMRDiscussionsUnresolved(ctx, mr.ProviderProjectID, mr.IID)
	if err != nil {
		return err
	}
	blocking, err := a.DB.Coll("review_findings").CountDocuments(ctx, bson.M{"org_id": run.OrgID, "run_id": run.ID, "status": FindingOpen, "blocking": true})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	noPipe := mr.NoPipelineSince
	if pipe == nil && noPipe == nil {
		noPipe = &now
	}
	if pipe != nil {
		noPipe = nil
	}
	in := mrready.Inputs{CurrentHead: mr.CurrentHead, VerifiedHead: run.Heads.Verified, ReviewedHead: run.Heads.Reviewed, RequirePipeline: pv.Bool("ci.require_pipeline"), NoPipelineSince: noPipe,
		Grace: time.Duration(pv.Int("ci.no_pipeline_grace_secs")) * time.Second, Now: now, BlockingOpen: int(blocking), DiscussionsResolved: unresolved == 0 && rm.BlockingDiscussionsResolved, HasConflicts: rm.HasConflicts, Opened: rm.State == "opened",
		VerifyGate: gateAt(run, mr.CurrentHead), ReviewVerdict: verdictAt(run, mr.CurrentHead), Independence: independenceOf(run), Stale: run.Stale, Held: run.Hold != nil, Mode: mr.Mode}
	if pipe != nil {
		p := &mrready.Pipeline{SHA: pipe.SHA, Status: pipe.Status}
		for _, j := range pipe.Jobs {
			p.Jobs = append(p.Jobs, mrready.Job{Name: j.Name, Status: j.Status, AllowFailure: j.AllowFailure})
		}
		in.Pipeline = p
	}
	in.Security = a.securityFacts(ctx, run, pv)
	res := mrready.Evaluate(in)
	checks := make([]ReadinessCheck, 0, len(res.Checks))
	var failed []string
	for _, c := range res.Checks {
		checks = append(checks, ReadinessCheck{Code: c.Code, Status: c.Status, Detail: c.Detail})
		if c.Status != "pass" {
			failed = append(failed, c.Code)
		}
	}
	waited := now.Sub(run.Checkpoint.Since)
	maxWait := time.Duration(pv.Int("ci.max_wait_secs")) * time.Second
	if a.ciMaxWait > 0 {
		maxWait = a.ciMaxWait
	}
	timeout := !res.Ready && waited > maxWait
	err = a.fenced(ctx, lease, func(sc context.Context) error {
		set := bson.M{"discussions_resolved": in.DiscussionsResolved, "has_conflicts": rm.HasConflicts, "readiness": MRReadiness{Ready: res.Ready, Checks: checks, EvaluatedAt: now, Head: mr.CurrentHead}, "pipeline": pipe, "no_pipeline_since": noPipe, "updated_at": now}
		if _, e := a.DB.Coll("merge_requests").UpdateOne(sc, bson.M{"_id": mr.ID, "org_id": mr.OrgID}, bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}}); e != nil {
			return e
		}
		if pipe != nil && (mr.Pipeline == nil || mr.Pipeline.Status != pipe.Status || mr.Pipeline.ID != pipe.ID) {
			if e := a.AppendEvent(sc, run.OrgID, "mr.pipeline_updated", Subject{"run", run.ID}, bson.M{"run_id": run.ID, "status": pipe.Status}, nil, nil, true); e != nil {
				return e
			}
		}
		cur, e := a.run(sc, run.OrgID, run.ID)
		if e != nil {
			return e
		}
		switch {
		case res.Ready && cur.State == runstate.AwaitingCI:
			if _, e = a.casRun(sc, cur, runstate.StageCI, bson.M{"state": runstate.ReadyForMerge}, runEvent{Reason: "mr_ready", Actor: runstate.ActorSystem, Extra: []eventSpec{{"mr.ready", bson.M{"head": mr.CurrentHead}}}}); e != nil && !errors.Is(e, errRunMoved) {
				return e
			}
		case !res.Ready && cur.State == runstate.ReadyForMerge:
			if _, e = a.casRun(sc, cur, runstate.StageCI, bson.M{"state": runstate.AwaitingCI}, runEvent{Reason: "readiness_lost", Actor: runstate.ActorSystem, Extra: []eventSpec{{"mr.readiness_lost", bson.M{"codes": toAny(failed)}}}}); e != nil && !errors.Is(e, errRunMoved) {
				return e
			}
		case timeout && cur.State == runstate.AwaitingCI && cur.OpenInboxID == nil:
			if e = a.stopRun(sc, cur, stopSpec{Reason: "dependency_unavailable", QuestionCode: "ci_timeout", Params: bson.M{"waited_secs": int(waited.Seconds())}, Options: resolveOptions(), Set: bson.M{"checkpoint.since": now}}); e != nil && !errors.Is(e, errRunMoved) {
				return e
			}
		}
		if e = a.completeJob(sc, lease); e != nil {
			return e
		}
		if job.OperationID != nil {
			if e = a.transitionOperation(sc, job, "succeeded", bson.M{"ready": res.Ready}); e != nil {
				return e
			}
		}
		if repeat && !timeout {
			return a.enqueueMRWatch(sc, run.OrgID, run.ID, interval)
		}
		return nil
	})
	return err
}

func gateAt(run RunDoc, head string) string {
	if run.Verification != nil && run.Verification.Head == head && run.Heads.Verified == head {
		return run.Verification.Gate
	}
	return "stale"
}
func verdictAt(run RunDoc, head string) string {
	if run.Review != nil && run.Review.Head == head && run.Heads.Reviewed == head {
		return run.Review.Verdict
	}
	return ""
}
func independenceOf(run RunDoc) string {
	if run.Review != nil {
		return run.Review.Independence.Status
	}
	return "unknown"
}

// foreignHead handles a remote branch that moved without Orqen: the readiness is lost at once, then the run adopts
// the new head when it descends from the last pushed one (policy review.on_foreign_push=reverify), else asks.
func (a *App) foreignHead(ctx context.Context, lease Lease, job JobDoc, run RunDoc, mr MergeRequestDoc, conn ConnectionDoc, remote string, interval time.Duration, repeat bool) error {
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return err
	}
	wm, err := a.workspaceManager(run.OrgID, &conn, 0)
	if err != nil {
		return err
	}
	wt, err := a.worktreePath(ctx, run)
	if err != nil {
		return err
	}
	auth, err := a.providerAuth(ctx, conn)
	if err != nil {
		return err
	}
	ws, _, _ := a.workspaceDoc(ctx, run.OrgID, run.ID)
	host, _ := a.codeHost(ctx, conn)
	gp, err := host.GetProject(ctx, mr.ProviderProjectID)
	if err != nil {
		return err
	}
	creds := workspace.Creds{Host: hostOfURL(gp.HTTPURLToRepo), Username: "oauth2", Password: auth.Secret}
	ref := "refs/remotes/orqen-remote/" + mr.SourceBranch
	if _, err = wm.Git.Run(ctx, wt, creds, 0, "fetch", "--no-tags", gp.HTTPURLToRepo, "+refs/heads/"+mr.SourceBranch+":"+ref); err != nil {
		return err
	}
	descends := true
	if _, e := wm.Git.Run(ctx, wt, workspace.Creds{}, 0, "merge-base", "--is-ancestor", mr.LastPushedSHA, remote); e != nil {
		descends = false
	}
	st, err := wm.Inspect(ctx, wt, ws.BaseSHA)
	if err != nil {
		return err
	}
	adopt := descends && st.Clean && pv.Str("review.on_foreign_push") == "reverify"
	return a.fenced(ctx, lease, func(sc context.Context) error {
		now := time.Now().UTC()
		cur, e := a.run(sc, run.OrgID, run.ID)
		if e != nil {
			return e
		}
		if e = a.AppendEvent(sc, run.OrgID, "mr.head_changed", Subject{"run", run.ID}, bson.M{"run_id": run.ID, "from": mr.LastPushedSHA, "to": remote, "foreign": true}, nil, nil, true); e != nil {
			return e
		}
		stale := bson.M{"verified_head": "", "reviewed_head": "", "current_head": remote, "readiness": MRReadiness{Ready: false, Checks: []ReadinessCheck{{Code: "head_match", Status: "stale", Detail: "someone else pushed " + short(remote)}}, EvaluatedAt: now, Head: remote}, "updated_at": now}
		if _, e = a.DB.Coll("merge_requests").UpdateOne(sc, bson.M{"_id": mr.ID, "org_id": mr.OrgID}, bson.M{"$set": stale, "$inc": bson.M{"revision": int64(1)}}); e != nil {
			return e
		}
		if adopt {
			// the worktree is moved to the remote head outside this transaction by adoptRemote; mark it requested
			if _, e = a.casRun(sc, cur, runstate.StageCI, bson.M{"adopt_remote": true, "adopt_sha": remote}, runEvent{Reason: "foreign_push", Actor: runstate.ActorSystem}); e != nil && !errors.Is(e, errRunMoved) {
				return e
			}
			if e = a.completeJob(sc, lease); e != nil {
				return e
			}
			return a.enqueueRunAdvance(sc, run.OrgID, run.ID)
		}
		if cur.State == runstate.ReadyForMerge {
			if cur, e = a.casRun(sc, cur, runstate.StageCI, bson.M{"state": runstate.AwaitingCI}, runEvent{Reason: "readiness_lost", Actor: runstate.ActorSystem, Extra: []eventSpec{{"mr.readiness_lost", bson.M{"codes": bson.A{"head_match"}}}}}); e != nil && !errors.Is(e, errRunMoved) {
				return e
			}
		}
		if cur.OpenInboxID == nil {
			if e = a.stopRun(sc, cur, stopSpec{Reason: "policy_conflict", QuestionCode: "branch_rewritten", Params: bson.M{"branch": mr.SourceBranch, "remote": short(remote)},
				Options: []InboxOption{{ID: "adopt_remote", EffectCode: "adopt_remote"}, {ID: "cancel_run", EffectCode: "cancel_run"}}, Set: bson.M{"adopt_sha": remote}}); e != nil && !errors.Is(e, errRunMoved) {
				return e
			}
		}
		return a.completeJob(sc, lease)
	})
}

// adoptRemote moves the worktree to the foreign head and sends the run back to verification with a new round.
func (s *stageCtx) adoptRemote(ctx context.Context, run RunDoc) error {
	a := s.a
	mr, found, err := a.mrOfRun(ctx, run.OrgID, run.ID)
	if err != nil || !found {
		return err
	}
	remote := run.AdoptSHA
	wt, err := a.worktreePath(ctx, run)
	if err != nil {
		return err
	}
	wm, err := a.workspaceManager(run.OrgID, nil, 0)
	if err != nil {
		return err
	}
	if _, err = wm.Git.Run(ctx, wt, workspace.Creds{}, 0, "reset", "--hard", remote); err != nil {
		return err
	}
	_ = strings.TrimSpace
	return a.fenced(ctx, s.lease, func(tx context.Context) error {
		now := time.Now().UTC()
		cur, e := a.run(tx, run.OrgID, run.ID)
		if e != nil {
			return e
		}
		set := bson.M{"adopt_remote": false, "adopt_sha": "", "heads.head": remote, "heads.pushed": remote, "heads.verified": "", "heads.reviewed": "", "round": run.Round + 1, "state": runstate.Verifying, "checkpoint": RunCheckpoint{Stage: runstate.StageVerify, Since: now}, "hold": nil, "open_inbox_id": nil, "review": nil, "verification": nil}
		if _, e = a.casRun(tx, cur, runstate.StageCI, set, runEvent{Reason: "foreign_push_adopted", Actor: runstate.ActorSystem, Extra: []eventSpec{{"mr.head_adopted", bson.M{"head": remote}}}}); e != nil {
			return e
		}
		if _, e = a.DB.Coll("merge_requests").UpdateOne(tx, bson.M{"_id": mr.ID, "org_id": mr.OrgID}, bson.M{"$set": bson.M{"last_pushed_sha": remote, "current_head": remote, "updated_at": now}, "$inc": bson.M{"revision": int64(1)}}); e != nil {
			return e
		}
		return a.enqueueRunAdvance(tx, run.OrgID, run.ID)
	})
}

// ---- API

func (a *App) listMergeRequests(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	limit, after, err := pageQueryKey(r)
	if err != nil {
		return err
	}
	filter := bson.M{"org_id": org, "active": true}
	q := r.URL.Query()
	if v := q.Get("project_id"); v != "" {
		if !validID("prj", v) {
			return validation("project_id", "is not a project id")
		}
		filter["project_id"] = v
	}
	if v := q.Get("mode"); v != "" {
		if v != "dry_run" && v != "live" {
			return validation("mode", "must be dry_run or live")
		}
		filter["mode"] = v
	}
	if v := q.Get("ready"); v != "" {
		filter["readiness.ready"] = v == "true"
	}
	docs, next, err := findPageBy(r.Context(), a.DB, "merge_requests", filter, "_id", func(d MergeRequestDoc) string { return d.ID }, limit, after)
	if err != nil {
		return err
	}
	items := []bson.M{}
	for _, d := range docs {
		items = append(items, d.DTO())
	}
	writeJSON(w, 200, bson.M{"items": items, "next_cursor": next})
	return nil
}

// withReadiness shows the checklist of a merge request nobody watches (a simulated one): remote_mr fails there.
func (a *App) withReadiness(ctx context.Context, d MergeRequestDoc) MergeRequestDoc {
	if d.Readiness != nil {
		return d
	}
	run, err := a.run(ctx, d.OrgID, d.RunID)
	if err != nil {
		return d
	}
	in := mrready.Inputs{CurrentHead: d.CurrentHead, VerifiedHead: run.Heads.Verified, ReviewedHead: run.Heads.Reviewed, RequirePipeline: true, DiscussionsResolved: true, Opened: d.State == "opened",
		VerifyGate: gateAt(run, d.CurrentHead), ReviewVerdict: verdictAt(run, d.CurrentHead), Independence: independenceOf(run), Stale: run.Stale, Held: run.Hold != nil, Mode: d.Mode, Now: time.Now()}
	if pvv, perr := a.pinnedPolicy(ctx, run); perr == nil {
		in.Security = a.securityFacts(ctx, run, pvv)
	}
	res := mrready.Evaluate(in)
	rd := &MRReadiness{Ready: res.Ready, EvaluatedAt: time.Now().UTC(), Head: d.CurrentHead}
	for _, c := range res.Checks {
		rd.Checks = append(rd.Checks, ReadinessCheck{Code: c.Code, Status: c.Status, Detail: c.Detail})
	}
	d.Readiness = rd
	return d
}

func (a *App) getMergeRequest(w http.ResponseWriter, r *http.Request) error {
	d, err := findOne[MergeRequestDoc](r.Context(), a.DB, "merge_requests", bson.M{"_id": r.PathValue("mrqId"), "org_id": r.PathValue("orgId")})
	if err != nil {
		return err
	}
	d = a.withReadiness(r.Context(), d)
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, 200, d.DTO())
	return nil
}

func (a *App) refreshMergeRequest(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("mrqId")
	route := "POST /api/v1/orgs/{orgId}/merge-requests/{mrqId}/refresh"
	hash := requestHash("POST", route, map[string]string{"orgId": org, "mrqId": id}, nil)
	return a.idempotent(w, r, org, route, key, hash, func(ctx context.Context) (Output, error) {
		d, err := findOne[MergeRequestDoc](ctx, a.DB, "merge_requests", bson.M{"_id": id, "org_id": org})
		if err != nil {
			return Output{}, err
		}
		return a.startOperation(ctx, org, "mr_refresh", Subject{"merge_request", d.ID}, "mrrefresh:"+d.ID, 1, bson.M{"run_id": d.RunID})
	})
}
