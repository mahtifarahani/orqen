package server

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"orqen/internal/catherd"
	"orqen/internal/codexhost"
	"orqen/internal/extwrite"
	"orqen/internal/runstate"
	"strings"
	"time"
)

func openFindings(ctx context.Context, a *App, run RunDoc, round int) ([]ReviewFindingDoc, error) {
	return findMany[ReviewFindingDoc](ctx, a.DB, "review_findings", bson.M{"org_id": run.OrgID, "run_id": run.ID, "round": round, "status": FindingOpen}, bson.D{{Key: "finding_key", Value: 1}}, 200)
}

// commentSpecs builds the summary note and one discussion per finding (H22): inline only where the line is part of
// the diff of this head, otherwise a general discussion that quotes file:line.
func (s *stageCtx) commentSpecs(ctx context.Context, run RunDoc, pe publishEnv, mr MergeRequestDoc, findings []ReviewFindingDoc) []opSpec {
	h12 := first12(run.Heads.Head)
	verdict := ""
	if run.Review != nil {
		verdict = run.Review.Verdict
	}
	blocking := 0
	for _, f := range findings {
		if f.Blocking {
			blocking++
		}
	}
	target := XopTarget{Host: hostOfURL(pe.repoURL), ProjectPath: pe.repoPath, Branch: pe.branch, MRIID: mr.IID}
	sumKey := fmt.Sprintf("note:%s:%d:summary", run.ID, run.Round)
	summary := fmt.Sprintf("Independent review of %s (round %d): **%s**, %d finding(s), %d blocking.\n\n%s", h12, run.Round+1, verdict, len(findings), blocking, extwrite.Marker(run.ID, sumKey))
	specs := []opSpec{{Action: extwrite.MRNote, IntentKey: sumKey, Target: target, TargetURL: pe.conn.BaseURL, Payload: map[string]any{"action": "mr_note", "project_id": pe.primary.ProviderProjectID, "body": summary, "head": run.Heads.Head, "round": run.Round}}}
	base, start := run.Heads.Base, run.Heads.Base
	for _, f := range findings {
		key := fmt.Sprintf("discussion:%s:%d:%s", run.ID, run.Round, f.FindingKey)
		body := fmt.Sprintf("[%s] %s\n\n%s", f.Severity, f.Title, f.Body)
		payload := map[string]any{"action": "mr_discussion", "project_id": pe.primary.ProviderProjectID, "head": run.Heads.Head, "round": run.Round, "finding": f.FindingKey}
		if f.InlineEligible {
			payload["position"] = map[string]any{"position_type": "text", "base_sha": base, "start_sha": start, "head_sha": run.Heads.Head, "new_path": f.File, "new_line": f.Line}
		} else if f.File != "" {
			body = fmt.Sprintf("`%s:%d` [%s] %s\n\n%s", f.File, f.Line, f.Severity, f.Title, f.Body)
		}
		payload["body"] = body + "\n\n" + extwrite.Marker(run.ID, key)
		specs = append(specs, opSpec{Action: extwrite.MRDiscussion, IntentKey: key, Target: target, TargetURL: pe.conn.BaseURL, Payload: payload})
	}
	return specs
}

// stageComment posts (or simulates) the review comments, then either starts a fix round or lands the milestone.
func (s *stageCtx) stageComment(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	pe, err := s.publishEnv(ctx, run)
	if err != nil {
		return false, err
	}
	findings, err := openFindings(ctx, a, run, run.Round)
	if err != nil {
		return false, err
	}
	mr, _, err := a.mrOfRun(ctx, run.OrgID, run.ID)
	if err != nil {
		return false, err
	}
	specs := s.commentSpecs(ctx, run, pe, mr, findings)
	allDry, stop, err := s.runOps(ctx, run, pe, specs)
	_ = allDry
	if err != nil || stop {
		return false, err
	}
	for _, f := range findings {
		key := fmt.Sprintf("discussion:%s:%d:%s", run.ID, run.Round, f.FindingKey)
		if op, e := findOne[ExternalOpDoc](ctx, a.DB, "external_operations", bson.M{"org_id": run.OrgID, "intent_key": key}); e == nil && f.Publication == nil {
			rid := ""
			if op.Remote != nil {
				rid = op.Remote.DiscussionID
			}
			if _, e = a.DB.Coll("review_findings").UpdateOne(ctx, bson.M{"_id": f.ID, "org_id": run.OrgID}, bson.M{"$set": bson.M{"publication": FindingPublication{OpID: op.ID, RemoteID: rid}}}); e != nil {
				return false, e
			}
		}
	}
	blocking := 0
	for _, f := range findings {
		if f.Blocking {
			blocking++
		}
	}
	if blocking > 0 || run.Review != nil && run.Review.Verdict == "changes_requested" {
		return false, s.sendBack(ctx, run, "review_blocking", VerificationEvidenceDoc{ID: run.evidenceID(), HeadSHA: run.Heads.Head, Gate: EvidenceGate{Status: "fail"}})
	}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		_, e := s.complete(tx, run, runstate.StageComment, runstate.StageLand, nil)
		return e
	})
	return false, err
}

func (r RunDoc) evidenceID() string {
	if r.Verification != nil {
		return r.Verification.EvidenceID
	}
	return ""
}

// stageLand records the landed milestone in catherd's ledger (a directed call); catherd refuses without a reviewer
// record and a verifier verdict.
func (s *stageCtx) stageLand(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	h, err := s.openHost(ctx, run)
	if err != nil {
		return false, err
	}
	defer h.close()
	view, err := a.Catherd.RunsShow(ctx, h.env, run.CatherdRunID)
	if err != nil {
		return false, catherdJobError(err)
	}
	landed := false
	for _, m := range view.Landed {
		landed = landed || m == "M1"
	}
	if !landed {
		evidence := fmt.Sprintf("Orqen verification at %s: gate pass; independent review: %s", first12(run.Heads.Head), nonEmpty(reviewVerdict(run), "approved"))
		_, err = h.call(ctx, "land", map[string]any{"run": run.CatherdRunID, "milestone": "M1", "what": "ticket " + run.IssueKey, "commit": run.Heads.Head, "evidence": evidence, "next": "awaiting user merge"})
		var te *codexhost.ToolError
		if errors.As(err, &te) && te.Code == "E_LAND_GATE" {
			return false, s.stopFromError(ctx, run, stopSpec{Reason: "review_disagreement", QuestionCode: "catherd_land_refused", Params: bson.M{"fix": te.Fix, "message": te.Message},
				Options: []InboxOption{{ID: "retry_dispatch", EffectCode: "retry_land"}, {ID: "cancel_run", EffectCode: "cancel_run"}}})
		}
		if err != nil {
			return false, s.roleToolError(ctx, run, RoleExecDoc{}, err, false)
		}
	}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		_, e := s.complete(tx, run, runstate.StageLand, runstate.StageCI, nil, eventSpec{"run.landed", bson.M{"head": run.Heads.Head}})
		return e
	})
	return false, err
}

func reviewVerdict(run RunDoc) string {
	if run.Review != nil {
		return run.Review.Verdict
	}
	return ""
}

// stageCI waits for the merge request. In dry-run there is nothing to wait for: the run holds as a simulation.
func (s *stageCtx) stageCI(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	mr, found, err := a.mrOfRun(ctx, run.OrgID, run.ID)
	if err != nil {
		return false, err
	}
	if !found || mr.Mode != "live" {
		return true, s.hold(ctx, run, runstate.HoldDryRun, "the merge request is simulated; approve and publish to write it")
	}
	return s.watchMR(ctx, run)
}

var _ = mongo.ErrNoDocuments
var _ = catherd.HostCodex
var _ = strings.TrimSpace
var _ = time.Now
