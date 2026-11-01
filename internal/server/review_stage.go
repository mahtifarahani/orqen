package server

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"orqen/internal/catherd"
	"orqen/internal/gitdiff"
	"orqen/internal/review"
	"orqen/internal/rolebriefs"
	"orqen/internal/runstate"
	"orqen/internal/workspace"
	"strings"
	"time"
)

func reviewerName(run RunDoc) string { return fmt.Sprintf("reviewer-M1-r%d", run.Round+1) }

// headDiff reads the diff of the run's head from its evidence artifact.
func (a *App) headDiff(ctx context.Context, run RunDoc) (gitdiff.Diff, error) {
	ev, err := findOne[VerificationEvidenceDoc](ctx, a.DB, "verification_evidence", bson.M{"org_id": run.OrgID, "run_id": run.ID, "kind": "head", "head_sha": run.Heads.Head, "diff_artifact_id": bson.M{"$ne": ""}})
	if err != nil {
		return gitdiff.Diff{}, err
	}
	art, err := findOne[ArtifactDoc](ctx, a.DB, "artifacts", bson.M{"_id": ev.DiffArtifactID, "org_id": run.OrgID})
	if err != nil {
		return gitdiff.Diff{}, err
	}
	data, err := readBlobAt(a, run.OrgID, art.Sha256)
	if err != nil {
		return gitdiff.Diff{}, err
	}
	return gitdiff.Parse(string(data))
}

// workerRecords lists the catherd data of every worker execution of the run (failover stand-ins included).
func (a *App) workerRecords(ctx context.Context, run RunDoc) []catherd.RunRecord {
	rexes, err := findMany[RoleExecDoc](ctx, a.DB, "role_executions", bson.M{"org_id": run.OrgID, "run_id": run.ID, "role": "worker", "catherd": bson.M{"$ne": nil}}, bson.D{{Key: "_id", Value: 1}}, 100)
	if err != nil {
		return nil
	}
	var out []catherd.RunRecord
	for _, r := range rexes {
		rec := catherd.RunRecord{Rung: r.Catherd.Rung}
		if r.Catherd.Thread != "" {
			t := r.Catherd.Thread
			rec.Thread = &t
		}
		out = append(out, rec)
	}
	return out
}

func recordOf(rex RoleExecDoc) catherd.RunRecord {
	rec := catherd.RunRecord{Rung: rex.Catherd.Rung}
	if rex.Catherd.Thread != "" {
		t := rex.Catherd.Thread
		rec.Thread = &t
	}
	return rec
}

// stageReview runs a fresh independent reviewer on the head, records its findings and its independence.
func (s *stageCtx) stageReview(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	h, err := s.openHost(ctx, run)
	if err != nil {
		return false, err
	}
	defer h.close()
	data, _, snap, err := a.briefData(ctx, run)
	if err != nil {
		return false, err
	}
	if err = a.attachContext(ctx, run, "reviewer", &data); err != nil {
		return false, err
	}
	diff, err := a.headDiff(ctx, run)
	if err != nil {
		return false, err
	}
	data.BaseSHA, data.HeadSHA, data.Summary, data.Files = run.Heads.Base, run.Heads.Head, snap.Summary, diff.Paths()
	if run.Verification != nil {
		if ev, e := findOne[VerificationEvidenceDoc](ctx, a.DB, "verification_evidence", bson.M{"_id": run.Verification.EvidenceID, "org_id": run.OrgID}); e == nil {
			for _, c := range ev.Commands {
				data.Evidence = append(data.Evidence, rolebriefs.Evidence{Key: c.Key, Outcome: c.Outcome, Exit: c.ExitCode, OutputSHA12: first12(c.OutputSHA256)})
			}
		}
	}
	spec := roleSpec{Role: "reviewer", Name: reviewerName(run), Stage: runstate.StageReview, Round: run.Round, NeedRoute: true,
		Brief: func() (rolebriefs.Brief, error) { return rolebriefs.Render(rolebriefs.Reviewer, data) }}
	parked, rex, err := s.roleAttempts(ctx, run, h, spec, s.climbOnce(ctx, run, h, ""))
	if err != nil || parked {
		return parked, err
	}
	reply, err := a.replyText(ctx, rex)
	if err != nil {
		return false, err
	}
	res, perr := review.Parse(reply)
	if perr != nil {
		if strings.Contains(rex.Name, "-c") {
			return false, s.stopFromError(ctx, run, stopSpec{Reason: "review_disagreement", QuestionCode: "review_unparseable", Params: bson.M{"name": rex.Name},
				Options: []InboxOption{{ID: "retry_review", EffectCode: "retry_review"}, {ID: "cancel_run", EffectCode: "cancel_run"}}})
		}
		return false, s.markClimbed(ctx, rex) // one fresh retry
	}
	wm, err := a.workspaceManager(run.OrgID, nil, 0)
	if err != nil {
		return false, err
	}
	wt, err := a.worktreePath(ctx, run)
	if err != nil {
		return false, err
	}
	review.Validate(&res, review.Context{Diff: diff, LineCount: func(p string) int {
		out, e := wm.Git.Run(ctx, wt, workspace.Creds{}, 0, "show", run.Heads.Head+":"+p)
		if e != nil {
			return -1
		}
		return strings.Count(out.Stdout, "\n")
	}})
	cat, _ := a.currentCatalog(ctx, run.OrgID)
	workers := a.workerRecords(ctx, run)
	ind := catherd.ActualIndependence(workers, []catherd.RunRecord{recordOf(rex)}, cat)
	var coderFam []string
	for _, w := range workers {
		if cat != nil {
			if f, ok := cat.Family(w.Rung); ok {
				coderFam = append(coderFam, f)
			}
		}
	}
	if ind.Status == "same_model" {
		return false, s.stopFromError(ctx, run, stopSpec{Reason: "model_unavailable", QuestionCode: "reviewer_not_independent", Params: bson.M{"shared": toAny(ind.Shared)},
			Options: []InboxOption{{ID: "retry_review", EffectCode: "retry_review"}, {ID: "cancel_run", EffectCode: "cancel_run"}}})
	}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		now := time.Now().UTC()
		if _, e := a.DB.Coll("review_findings").UpdateMany(tx, bson.M{"org_id": run.OrgID, "run_id": run.ID, "status": FindingOpen, "round": bson.M{"$lt": run.Round}}, bson.M{"$set": bson.M{"status": FindingSuperseded}}); e != nil {
			return e
		}
		for _, f := range res.Findings {
			doc := ReviewFindingDoc{ID: newID("fnd"), OrgID: run.OrgID, RunID: run.ID, Round: run.Round, ReviewRexID: rex.ID, HeadSHA: run.Heads.Head, FindingKey: f.ID, Severity: f.Severity, Blocking: f.Blocking, File: f.File, Line: f.Line, Side: f.Side,
				LocationValid: f.LocationValid, InlineEligible: f.InlineEligible, Title: f.Title, Body: f.Body, AcceptanceRef: f.AcceptanceRef, Status: FindingOpen, CreatedAt: now, Revision: 1}
			if _, e := a.DB.Coll("review_findings").UpdateOne(tx, bson.M{"org_id": run.OrgID, "run_id": run.ID, "round": run.Round, "finding_key": f.ID}, bson.M{"$setOnInsert": doc}, upsertOpt()); e != nil {
				return e
			}
		}
		rv := RunReview{Round: run.Round, Verdict: res.Effective(), Head: run.Heads.Head, BlockingOpen: res.Blocking(), Independence: ind, CoderFamilies: coderFam}
		if cat != nil {
			rv.ReviewerFamily, _ = cat.Family(rex.Catherd.Rung)
		}
		set := bson.M{"review": rv, "heads.reviewed": run.Heads.Head}
		if run.MergeRequestID != "" {
			// the merge request document shows the reviewed head too
			if _, e := a.DB.Coll("merge_requests").UpdateOne(tx, bson.M{"_id": run.MergeRequestID, "org_id": run.OrgID}, bson.M{"$set": bson.M{"reviewed_head": run.Heads.Head}}); e != nil {
				return e
			}
		}
		_, e := s.complete(tx, run, runstate.StageReview, runstate.StageComment, set, eventSpec{"review.recorded", bson.M{"round": run.Round, "verdict": res.Effective(), "blocking": res.Blocking()}})
		return e
	})
	return false, err
}

func first12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func upsertOpt() *options.UpdateOneOptionsBuilder { return options.UpdateOne().SetUpsert(true) }
