package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"orqen/internal/codegraph"
	"orqen/internal/extensions"
	"orqen/internal/secscan"
)

// The automatic sweeps (Sprint 7). They only enqueue, never execute: at most one enqueue per org per sweep, at most
// 200 runs read, and the shared validation of the manual routes. The pure decision tables live in
// codegraph.AutoIndexDecision and secscan.AutoScanDecision; a skipped candidate writes nothing. They fail closed:
// an error while gathering facts skips that run (ids only in the log) and never aborts the whole sweep.

const autoSweepRuns = 200

// sweepCodeIndexAuto queues a code index for the head of an active run when codegraph.auto_index is on_run.
func (a *App) sweepCodeIndexAuto(ctx context.Context) error {
	runs, err := findMany[RunDoc](ctx, a.DB, "runs", bson.M{"active": true, "cancellation": nil, "repo_head_sha": bson.M{"$nin": []any{nil, ""}},
		"state": bson.M{"$in": []string{"planned", "queued", "coding", "fixing", "reviewing"}}}, bson.D{{Key: "_id", Value: 1}}, autoSweepRuns)
	if err != nil {
		return err
	}
	done := map[string]bool{}
	for _, r := range runs {
		if done[r.OrgID] || r.RepoHeadSHA == nil {
			continue
		}
		pv, e := a.pinnedPolicy(ctx, r)
		if e != nil {
			slog.Warn("auto index: policy unreadable", "org", r.OrgID, "run", r.ID)
			continue
		}
		in := codegraph.AutoIndexInput{Mode: pv.Str("codegraph.auto_index"), RunState: r.State, HeadSHA: *r.RepoHeadSHA}
		if r.Hold != nil {
			in.HoldCode = r.Hold.Code
		}
		if in.Mode != "on_run" { // cheap exit before any other read
			continue
		}
		if in.OrgPaused, e = a.orgPaused(ctx, r.OrgID); e != nil {
			return e
		}
		_, why, e := a.gitnexusUsable(ctx, r.OrgID, r.ProjectID)
		if e != nil {
			slog.Warn("auto index: usability unreadable", "org", r.OrgID, "run", r.ID)
			continue
		}
		in.UsableReason = why
		proj, e := a.project(ctx, r.OrgID, r.ProjectID)
		if e != nil {
			continue
		}
		primary := proj.Mapping.primary()
		if primary == nil {
			continue
		}
		rk := repoKey(r.OrgID, primary.ConnectionID, primary.ProviderProjectID)
		if cur, e := findOne[CodeIndexDoc](ctx, a.DB, "code_indexes", bson.M{"org_id": r.OrgID, "repo_key": rk, "commit_sha": in.HeadSHA}); e == nil {
			in.IndexState = cur.State
		} else if !errors.Is(e, mongo.ErrNoDocuments) {
			return e
		}
		n, e := a.DB.Coll("jobs").CountDocuments(ctx, bson.M{"org_id": r.OrgID, "kind": "code_index", "active": true})
		if e != nil {
			return e
		}
		in.ActiveIndexJobs = int(n)
		if ok, _ := codegraph.AutoIndexDecision(in); !ok {
			continue
		}
		org, prj, sha := r.OrgID, r.ProjectID, in.HeadSHA
		if err = a.txnRetryJob(ctx, func(sc context.Context) error {
			cix, e := a.upsertCodeIndexT(sc, org, prj, rk, sha, "auto")
			if e != nil {
				return e
			}
			if e = a.ensureJobTx(sc, org, "code_index", "code_index:"+cix.ID, 2, bson.M{"cix_id": cix.ID, "trigger": "auto"}); e != nil {
				return e
			}
			return a.AppendEvent(sc, org, "code_index.auto_queued", Subject{"code_index", cix.ID}, bson.M{"cix_id": cix.ID, "commit_sha": sha}, nil, nil, true)
		}); err != nil && !mongo.IsDuplicateKeyError(err) { // a concurrent manual start won the unique index: nothing to do
			return err
		}
		done[org] = true
	}
	a.Committed()
	return nil
}

// sweepSecurityAuto queues a scan of the reviewed head of a run whose review passed when automation.security_scan is auto.
func (a *App) sweepSecurityAuto(ctx context.Context) error {
	runs, err := findMany[RunDoc](ctx, a.DB, "runs", bson.M{"active": true, "cancellation": nil, "heads.reviewed": bson.M{"$nin": []any{nil, ""}},
		"state": bson.M{"$in": []string{"awaiting_ci", "ready_for_merge"}}}, bson.D{{Key: "_id", Value: 1}}, autoSweepRuns)
	if err != nil {
		return err
	}
	done := map[string]bool{}
	for _, r := range runs {
		if done[r.OrgID] {
			continue
		}
		pv, e := a.pinnedPolicy(ctx, r)
		if e != nil {
			slog.Warn("auto scan: policy unreadable", "org", r.OrgID, "run", r.ID)
			continue
		}
		in := secscan.AutoScanInput{Mode: pv.Str("automation.security_scan"), RunState: r.State, ReviewedHead: r.Heads.Reviewed}
		if in.Mode != "auto" { // cheap exit before any other read
			continue
		}
		if r.Hold != nil {
			in.HoldCode = r.Hold.Code
		}
		if r.Review != nil {
			in.ReviewVerdict, in.ReviewHead, in.BlockingOpen = r.Review.Verdict, r.Review.Head, r.Review.BlockingOpen
		}
		if in.OrgPaused, e = a.orgPaused(ctx, r.OrgID); e != nil {
			return e
		}
		_, why, e := a.strixUsable(ctx, r.OrgID)
		if e != nil {
			slog.Warn("auto scan: usability unreadable", "org", r.OrgID, "run", r.ID)
			continue
		}
		in.UsableReason = why
		m, _ := extensions.Get("strix")
		in.DockerOK = !m.Requires.Docker || a.dockerUsable(ctx)
		_, perr := a.scanPolicy(ctx, r.OrgID, r.ProjectID)
		var ae *AppError
		switch {
		case perr == nil:
			in.ModelSet, in.BudgetOK = true, true
		case errors.As(perr, &ae) && ae.Code == "security_budget_unset":
			in.ModelSet = true
		case errors.As(perr, &ae):
		default:
			slog.Warn("auto scan: policy unreadable", "org", r.OrgID, "run", r.ID)
			continue
		}
		n, e := a.DB.Coll("security_scans").CountDocuments(ctx, bson.M{"org_id": r.OrgID, "run_id": r.ID, "head_sha": r.Heads.Reviewed, "state": bson.M{"$ne": "cancelled"}})
		if e != nil {
			return e
		}
		in.ScanExistsForHead = n > 0
		n, e = a.DB.Coll("security_scans").CountDocuments(ctx, bson.M{"org_id": r.OrgID, "state": bson.M{"$in": []string{"queued", "running"}}})
		if e != nil {
			return e
		}
		in.ScansRunningInOrg = int(n)
		if ok, _ := secscan.AutoScanDecision(in); !ok {
			continue
		}
		run, org := r, r.OrgID
		err = a.txnRetryJob(ctx, func(sc context.Context) error {
			_, _, e := a.queueScan(sc, org, run, "auto", "local_clone")
			return e
		})
		switch {
		case err == nil:
			done[org] = true
		case errors.Is(err, errScanAlreadyQueued):
			done[org] = true
		case errors.As(err, &ae):
			slog.Warn("auto scan: not queued", "org", org, "run", r.ID, "code", ae.Code)
		default:
			return err
		}
	}
	a.Committed()
	return nil
}

// sweepSourceSync queues a source_sync job for every active project with page references whose last sync is older than
// docs.sync_interval_secs (live project policy; 0 = never automatically). The job is deduped per project, and an
// organization with automation paused is skipped (Y18).
func (a *App) sweepSourceSync(ctx context.Context) error {
	projects, err := findMany[ProjectDoc](ctx, a.DB, "projects", bson.M{"active": true, "mapping.confluence.page_id": bson.M{"$nin": []any{nil, ""}}}, bson.D{{Key: "_id", Value: 1}}, autoSweepRuns)
	if err != nil {
		return err
	}
	paused := map[string]bool{}
	for _, p := range projects {
		pv, e := a.livePolicy(ctx, p.OrgID, p.ID)
		if e != nil {
			slog.Warn("source sync sweep: policy unreadable", "org", p.OrgID, "project", p.ID)
			continue
		}
		interval := time.Duration(pv.Int("docs.sync_interval_secs")) * time.Second
		if interval <= 0 {
			continue
		}
		if _, ok := paused[p.OrgID]; !ok {
			if paused[p.OrgID], e = a.orgPaused(ctx, p.OrgID); e != nil {
				return e
			}
		}
		if paused[p.OrgID] {
			continue
		}
		if st, e := findOne[SourceSyncStateDoc](ctx, a.DB, "source_sync_states", bson.M{"_id": p.ID, "org_id": p.OrgID}); e == nil {
			if time.Since(st.LastSyncAt) < interval {
				continue
			}
		} else if !errors.Is(e, mongo.ErrNoDocuments) {
			return e
		}
		org, prj := p.OrgID, p.ID
		if err = a.txnRetryJob(ctx, func(sc context.Context) error {
			return a.ensureJobTx(sc, org, "source_sync", "source_sync:"+prj, 3, bson.M{"project_id": prj})
		}); err != nil {
			return err
		}
	}
	return nil
}
