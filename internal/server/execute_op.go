package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/extwrite"
	"time"
)

func payloadStr(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func intOf(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// execFor builds the extwrite call of a stored operation from its exact payload.
func execFor(op ExternalOpDoc, pe publishEnv, reconcile, lookOnly bool) extwrite.Exec {
	in := extwrite.Exec{Action: extwrite.Action(op.Action), BaseURL: pe.conn.BaseURL, ProjectID: op.ProviderProjectID, RepoURL: pe.repoURL, Dir: pe.wt, Git: pe.wm.Git, Marker: op.Marker, Reconcile: reconcile, LookOnly: lookOnly}
	p := map[string]any(op.Payload)
	switch extwrite.Action(op.Action) {
	case extwrite.GitPush:
		in.Branch, in.SHA, in.LeaseSHA = payloadStr(p, "branch"), payloadStr(p, "sha"), payloadStr(p, "lease_sha")
	case extwrite.MRCreate:
		in.MR = extwrite.MRParams{Title: payloadStr(p, "title"), SourceBranch: payloadStr(p, "source_branch"), TargetBranch: payloadStr(p, "target_branch"), Description: payloadStr(p, "description")}
	case extwrite.MRUpdate:
		in.MRIID, in.Body = op.Target.MRIID, payloadStr(p, "description")
	case extwrite.MRNote, extwrite.MRDiscussion:
		in.MRIID, in.Body = op.Target.MRIID, payloadStr(p, "body")
		if pos := anyMap(p["position"]); pos != nil {
			in.Position = &extwrite.Position{PositionType: "text", BaseSHA: payloadStr(pos, "base_sha"), StartSHA: payloadStr(pos, "start_sha"), HeadSHA: payloadStr(pos, "head_sha"), NewPath: payloadStr(pos, "new_path"), OldPath: payloadStr(pos, "old_path"), NewLine: intOf(pos["new_line"])}
		}
	}
	return in
}

// reconcileDelays are the waits between look-only attempts after an unknown outcome (2 s, 8 s, 30 s).
func (a *App) reconcileSteps() []time.Duration {
	if a.reconcileDelays != nil {
		return a.reconcileDelays
	}
	return []time.Duration{2 * time.Second, 8 * time.Second, 30 * time.Second}
}

// executeOp performs one decided operation. stop is true when the stage must not continue (a question was opened,
// or the decision changed and the stage has to plan again).
func (s *stageCtx) executeOp(ctx context.Context, run RunDoc, pe publishEnv, op ExternalOpDoc) (bool, error) {
	a := s.a
	d, gate, err := a.decideOp(ctx, op)
	if err != nil {
		return false, err
	}
	if d.Outcome() != extwrite.OutcomeExecute {
		return true, s.backOff(ctx, op, d)
	}
	// A note planned while the merge request was still simulated has no iid yet: take it from the merge request now.
	if op.Target.MRIID == 0 && (op.Action == string(extwrite.MRNote) || op.Action == string(extwrite.MRDiscussion) || op.Action == string(extwrite.MRUpdate)) && run.MergeRequestID != "" {
		if mr, e := findOne[MergeRequestDoc](ctx, a.DB, "merge_requests", bson.M{"_id": run.MergeRequestID, "org_id": run.OrgID}); e == nil && mr.IID > 0 {
			op.Target.MRIID = mr.IID
			if _, e = a.DB.Coll("external_operations").UpdateOne(ctx, bson.M{"_id": op.ID, "org_id": op.OrgID}, bson.M{"$set": bson.M{"target.mr_iid": mr.IID}}); e != nil {
				return false, e
			}
		}
	}
	reconcile := op.Attempts > 0
	event := "extwrite.executing"
	if op, err = s.setOp(ctx, op, bson.M{"state": XopExecuting, "attempts": op.Attempts + 1}, eventSpec{event, bson.M{}}); err != nil {
		return false, err
	}
	if err = a.hook("pre_execute"); err != nil {
		return false, err
	}
	var res extwrite.Result
	run1 := func(lookOnly bool) error {
		return gate.Execute(ctx, d, opView(op), func(p *extwrite.Permit) error {
			var e error
			res, e = extwrite.Run(ctx, p, execFor(op, pe, reconcile, lookOnly))
			return e
		})
	}
	err = run1(false)
	if errors.Is(err, extwrite.ErrOutcomeUnknown) {
		if op, err = s.setOp(ctx, op, bson.M{"state": XopUnknown}, eventSpec{"extwrite.outcome_unknown", bson.M{}}); err != nil {
			return false, err
		}
		found := false
		for _, wait := range a.reconcileSteps() {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return false, ctx.Err()
			}
			if e := run1(true); e == nil {
				found = true
				break
			} else if !errors.Is(e, extwrite.ErrNotFoundRemote) && !errors.Is(e, extwrite.ErrOutcomeUnknown) {
				err = e
				break
			}
		}
		if !found && errors.Is(err, extwrite.ErrOutcomeUnknown) {
			return true, s.stopFromError(ctx, run, stopSpec{Reason: "dependency_unavailable", QuestionCode: "external_write_uncertain", Params: bson.M{"op_id": op.ID, "action": op.Action},
				Options: []InboxOption{{ID: "retry_write", EffectCode: "retry_write"}, {ID: "cancel_run", EffectCode: "cancel_run"}}})
		}
		if found {
			err = nil
			res.Reconciled = true
		}
	}
	var np *extwrite.NotPermittedError
	var se *extwrite.StatusError
	switch {
	case err == nil:
		return false, s.applySuccess(ctx, run, pe, op, res)
	case errors.As(err, &np):
		return true, s.backOff(ctx, op, np.Decision)
	case errors.Is(err, extwrite.ErrForeignPush):
		if _, e := s.setOp(ctx, op, bson.M{"state": XopFailed, "error": &XopError{Code: "foreign_push", Message: "someone else pushed the branch"}}, eventSpec{"extwrite.failed", bson.M{"code": "foreign_push"}}); e != nil {
			return false, e
		}
		return true, s.foreignPush(ctx, run, pe, op)
	case errors.As(err, &se) && (se.Status == 401 || se.Status == 403):
		if _, e := s.setOp(ctx, op, bson.M{"state": XopFailed, "error": &XopError{Code: "credential_rejected", Message: "the write credential was rejected"}}, eventSpec{"extwrite.failed", bson.M{"code": "credential_rejected"}}); e != nil {
			return false, e
		}
		return true, s.stopFromError(ctx, run, stopSpec{Reason: "credential_expired", QuestionCode: "write_credential_expired", Params: bson.M{"op_id": op.ID}, Options: resolveOptions()})
	case errors.As(err, &se):
		code := "write_rejected"
		if _, e := s.setOp(ctx, op, bson.M{"state": XopFailed, "error": &XopError{Code: code, Message: "the host refused the write"}}, eventSpec{"extwrite.failed", bson.M{"code": code, "status": se.Status}}); e != nil {
			return false, e
		}
		return true, s.stopFromError(ctx, run, stopSpec{Reason: "dependency_unavailable", QuestionCode: "external_write_failed", Params: bson.M{"op_id": op.ID, "status": se.Status}, Options: resolveOptions()})
	}
	return false, err
}

// backOff stores the state a changed decision implies; the stage plans again on its next pass.
func (s *stageCtx) backOff(ctx context.Context, op ExternalOpDoc, d extwrite.Decision) error {
	state := XopDryRun
	switch d.Outcome() {
	case extwrite.OutcomeAwaitingApproval:
		state = XopAwaitingApproval
	case extwrite.OutcomeRefused, extwrite.OutcomeRefusedEnv:
		state = XopRefused
	}
	_, err := s.setOp(ctx, op, bson.M{"state": state, "decision": &XopDecision{Outcome: string(d.Outcome()), Reasons: d.Reasons(), DecidedAt: time.Now().UTC(), EnvScope: string(d.Scope())}}, eventSpec{"extwrite.decided", bson.M{"outcome": string(d.Outcome()), "reasons": toAny(d.Reasons())}})
	return err
}

// applySuccess records the remote result and what it means for the run and its merge request.
func (s *stageCtx) applySuccess(ctx context.Context, run RunDoc, pe publishEnv, op ExternalOpDoc, res extwrite.Result) error {
	a := s.a
	state, kind := XopSucceeded, "extwrite.succeeded"
	if res.Reconciled {
		state, kind = XopReconciled, "extwrite.reconciled"
	}
	remote := &XopRemote{MRIID: res.MRIID, NoteID: res.NoteID, DiscussionID: res.DiscussionID, PushedSHA: res.PushedSHA, WebURL: res.WebURL}
	if err := a.hook("extwrite"); err != nil {
		return err
	}
	if _, err := s.setOp(ctx, op, bson.M{"state": state, "remote": remote, "error": nil}, eventSpec{kind, bson.M{}}); err != nil {
		return err
	}
	head := run.Heads.Head
	switch extwrite.Action(op.Action) {
	case extwrite.GitPush:
		if _, err := a.DB.Coll("runs").UpdateOne(ctx, bson.M{"_id": run.ID, "org_id": run.OrgID}, bson.M{"$set": bson.M{"heads.pushed": res.PushedSHA}}); err != nil {
			return err
		}
		if mr, ok, _ := a.mrOfRun(ctx, run.OrgID, run.ID); ok && mr.Mode == "live" {
			_, err := s.ensureMR(ctx, run, pe, bson.M{"last_pushed_sha": res.PushedSHA, "current_head": head, "verified_head": run.Heads.Verified})
			return err
		}
	case extwrite.MRCreate:
		_, err := s.ensureMR(ctx, run, pe, bson.M{"mode": "live", "iid": res.MRIID, "web_url": res.WebURL, "last_pushed_sha": head, "current_head": head, "verified_head": run.Heads.Verified})
		return err
	}
	return nil
}

// foreignPush is replaced by the full policy in the merge request watcher (WP65): it asks the user.
func (s *stageCtx) foreignPush(ctx context.Context, run RunDoc, pe publishEnv, op ExternalOpDoc) error {
	return s.stopFromError(ctx, run, stopSpec{Reason: "policy_conflict", QuestionCode: "branch_rewritten", Params: bson.M{"branch": pe.branch},
		Options: []InboxOption{{ID: "adopt_remote", EffectCode: "adopt_remote"}, {ID: "cancel_run", EffectCode: "cancel_run"}}})
}
