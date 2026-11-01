package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"orqen/internal/catherd"
	"orqen/internal/codexhost"
	"orqen/internal/execution"
	"orqen/internal/rolebriefs"
	"orqen/internal/runstate"
	"path/filepath"
	"strings"
	"time"
)

// roleSpec describes one role execution a stage needs.
type roleSpec struct {
	Role, Name, Lane, Stage string
	Round                   int
	Rung                    string // explicit rung; empty with a lane lets catherd route
	NeedRoute               bool   // ask catherd for the role's rung first (no lane)
	Thread                  string // resume this thread (a fix round)
	Brief                   func() (rolebriefs.Brief, error)
	Next                    string
}

const maxReplyBytes = 256 << 10

// hostFailure handles a directive the host did not carry out: retry once, then ask the user.
func (s *stageCtx) hostFailure(ctx context.Context, run RunDoc, hf *hostFault) error {
	a := s.a
	var faults int
	err := a.fenced(ctx, s.lease, func(tx context.Context) error {
		var cur RunDoc
		if e := a.DB.Coll("runs").FindOneAndUpdate(tx, bson.M{"_id": run.ID, "org_id": run.OrgID}, bson.M{"$inc": bson.M{"host_faults": 1}}, afterUpdate()).Decode(&cur); e != nil {
			return e
		}
		faults = cur.HostFaults
		return nil
	})
	if err != nil {
		return err
	}
	if faults < 2 {
		return &JobFailure{Code: "host_deviation", Message: hf.Error(), Retryable: true}
	}
	code := "host_misbehaving"
	if hf.Tool == "dispatch" {
		code = "role_dispatch_uncertain"
	}
	return s.stopFromError(ctx, run, stopSpec{Reason: "dependency_unavailable", QuestionCode: code, Params: bson.M{"tool": hf.Tool}, Options: []InboxOption{{ID: "retry_dispatch", EffectCode: "retry_dispatch"}, {ID: "cancel_run", EffectCode: "cancel_run"}}})
}

func (a *App) rexOf(ctx context.Context, org, run, name string, round int) (RoleExecDoc, bool, error) {
	d, err := findOne[RoleExecDoc](ctx, a.DB, "role_executions", bson.M{"org_id": org, "run_id": run, "name": name, "round": round})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return RoleExecDoc{}, false, nil
	}
	return d, err == nil, err
}

// storeText stores bytes as an artifact: the blob first, the record inside a fenced transaction.
func (s *stageCtx) storeText(ctx context.Context, org string, data []byte, media, kind string, subject Subject) (ArtifactDoc, error) {
	a := s.a
	blob, err := storeBlob(filepath.Join(a.Config.DataDir, "artifacts"), org, bytes.NewReader(data), a.Config.ArtifactMaxBytes)
	if err != nil {
		return ArtifactDoc{}, err
	}
	var art ArtifactDoc
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		var e error
		art, e = a.recordArtifact(tx, org, blob, media, kind, subject)
		return e
	})
	return art, err
}

func (a *App) enqueueRoleWatch(sc context.Context, org, runID string, delay time.Duration) error {
	now := time.Now().UTC()
	err := a.enqueue(sc, JobDoc{ID: newID("job"), OrgID: org, Kind: "role_watch", DedupeKey: "rolewatch:" + runID, Payload: bson.M{"run_id": runID}, State: "queued", Active: true, MaxAttempts: 100000, NextAt: now.Add(delay), CreatedAt: now, UpdatedAt: now})
	var dup *DuplicateActiveJob
	if errors.As(err, &dup) {
		return nil
	}
	return err
}

// rexUpsert returns the role execution of (name, round), creating it (and storing its brief) when absent.
func (s *stageCtx) rexUpsert(ctx context.Context, run RunDoc, spec roleSpec) (RoleExecDoc, error) {
	a := s.a
	rex, found, err := a.rexOf(ctx, run.OrgID, run.ID, spec.Name, spec.Round)
	if err != nil || found {
		return rex, err
	}
	brief, err := spec.Brief()
	if err != nil {
		return rex, err
	}
	art, err := s.storeText(ctx, run.OrgID, []byte(brief.Text), "text/markdown", "role_brief", Subject{"run", run.ID})
	if err != nil {
		return rex, err
	}
	now := time.Now().UTC()
	rex = RoleExecDoc{ID: newID("rex"), OrgID: run.OrgID, RunID: run.ID, Attempt: run.Attempt, Stage: spec.Stage, Round: spec.Round, Role: spec.Role, Name: spec.Name, Lane: spec.Lane, BriefArtifactID: art.ID, BriefSHA: brief.SHA256, RolebriefVer: brief.Version,
		RequestedRung: spec.Rung, State: RexPlanned, Context: a.rexContext(ctx, run, spec.Role), Skills: a.rexSkills(ctx, run, spec.Role, spec.Round), CreatedAt: now, UpdatedAt: now, Revision: 1}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		_, e := a.DB.Coll("role_executions").InsertOne(tx, rex)
		return e
	})
	if mongo.IsDuplicateKeyError(err) {
		rex, _, err = a.rexOf(ctx, run.OrgID, run.ID, spec.Name, spec.Round)
	}
	return rex, err
}

func (a *App) rexUpdate(sc context.Context, rex RoleExecDoc, from []string, set bson.M) (RoleExecDoc, error) {
	set["updated_at"] = time.Now().UTC()
	var out RoleExecDoc
	err := a.DB.Coll("role_executions").FindOneAndUpdate(sc, bson.M{"_id": rex.ID, "org_id": rex.OrgID, "state": bson.M{"$in": from}}, bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return rex, errRunMoved
	}
	return out, err
}

// claimedDispatches lists the dispatch ids other role executions of the run own.
func (a *App) claimedDispatches(ctx context.Context, run RunDoc, except string) (map[string]bool, error) {
	rexes, err := findMany[RoleExecDoc](ctx, a.DB, "role_executions", bson.M{"org_id": run.OrgID, "run_id": run.ID}, bson.D{{Key: "_id", Value: 1}}, 500)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range rexes {
		if r.ID != except && r.Catherd != nil && r.Catherd.DispatchID != "" {
			out[r.Catherd.DispatchID] = true
		}
	}
	return out, nil
}

func (a *App) familyOf(ctx context.Context, org, rung string) string {
	cat, err := a.currentCatalog(ctx, org)
	if err != nil || cat == nil {
		return ""
	}
	fam, _ := cat.Family(rung)
	return fam
}

// roleStep advances one role execution as far as it can without waiting. parked is true while the role runs in
// catherd (role_watch wakes the run when its record appears); when it returns false the role is collected.
func (s *stageCtx) roleStep(ctx context.Context, run RunDoc, h *hostRun, spec roleSpec) (parked bool, rex RoleExecDoc, err error) {
	a := s.a
	if rex, err = s.rexUpsert(ctx, run, spec); err != nil {
		return false, rex, err
	}
	switch rex.State {
	case RexPlanned, RexDispatching:
		return s.dispatchRole(ctx, run, h, spec, rex)
	case RexDispatched:
		return true, rex, nil
	case RexFinished:
		rex, err = s.collectRole(ctx, run, h, rex)
		return false, rex, err
	case RexCollected:
		return false, rex, nil
	case RexAmbiguous, RexCancelRequested, RexCancelled:
		return true, rex, nil
	}
	_ = a
	return true, rex, nil
}

func (s *stageCtx) dispatchRole(ctx context.Context, run RunDoc, h *hostRun, spec roleSpec, rex RoleExecDoc) (bool, RoleExecDoc, error) {
	a := s.a
	if rex.State == RexDispatching {
		// A previous pass sent the directive and never recorded its outcome: look before dispatching again.
		view, err := a.Catherd.RunsShow(ctx, h.env, run.CatherdRunID)
		if err != nil {
			return false, rex, catherdJobError(err)
		}
		claimed, err := a.claimedDispatches(ctx, run, rex.ID)
		if err != nil {
			return false, rex, err
		}
		since := time.Now().UTC()
		if rex.DispatchingAt != nil {
			since = *rex.DispatchingAt
		}
		if id, ok := execution.ReconcileDispatch(view, rex.Name, since, claimed); ok {
			var d catherd.Dispatch
			for _, x := range view.Dispatches {
				if x.DispatchID == id {
					d = x
				}
			}
			err = a.fenced(ctx, s.lease, func(tx context.Context) error {
				var e error
				if rex, e = a.rexUpdate(tx, rex, []string{RexDispatching}, bson.M{"state": RexDispatched, "dispatched_at": time.Now().UTC(), "catherd": &RexCatherd{RunID: run.CatherdRunID, DispatchID: id, Rung: d.Rung}}); e != nil {
					return e
				}
				if e = a.AppendEvent(tx, run.OrgID, "role.reconciled", Subject{"run", run.ID}, bson.M{"run_id": run.ID, "rex_id": rex.ID, "adopted": true}, nil, nil, true); e != nil {
					return e
				}
				return a.enqueueRoleWatch(tx, run.OrgID, run.ID, 0)
			})
			return true, rex, err
		}
		var turns []codexhost.Turn
		if th, e := h.client.ResumeThread(ctx, h.thread); e == nil {
			turns = th.Turns
			if turns == nil {
				turns = []codexhost.Turn{}
			}
		}
		directive := ""
		if rex.Host != nil {
			directive = rex.Host.DirectiveID
		}
		safe, known := execution.DispatchSafeToRetry(turns, directive)
		if (!known || !safe) && !rex.RetryConfirmed {
			err = a.fenced(ctx, s.lease, func(tx context.Context) error {
				var e error
				rex, e = a.rexUpdate(tx, rex, []string{RexDispatching}, bson.M{"state": RexAmbiguous})
				return e
			})
			if err != nil {
				return false, rex, err
			}
			return true, rex, s.stopFromError(ctx, run, stopSpec{Reason: "dependency_unavailable", QuestionCode: "role_dispatch_uncertain", Params: bson.M{"name": rex.Name},
				Options: []InboxOption{{ID: "retry_dispatch", EffectCode: "retry_dispatch"}, {ID: "cancel_run", EffectCode: "cancel_run"}}})
		}
	}
	if rex.State == RexPlanned {
		if paused, e := a.orgPaused(ctx, run.OrgID); e != nil {
			return false, rex, e
		} else if paused {
			return true, rex, s.hold(ctx, run, runstate.HoldAutomationPaused, "automation is paused for the organization")
		}
	}
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return false, rex, err
	}
	n, err := a.DB.Coll("role_executions").CountDocuments(ctx, bson.M{"org_id": run.OrgID, "run_id": run.ID, "state": bson.M{"$nin": []string{RexPlanned, RexFailed}}, "_id": bson.M{"$ne": rex.ID}})
	if err != nil {
		return false, rex, err
	}
	if int(n) >= pv.Int("roles.max_dispatches_per_run") {
		return true, rex, s.stopFromError(ctx, run, stopSpec{Reason: "budget_exceeded", QuestionCode: "dispatch_cap", Params: bson.M{"cap": pv.Int("roles.max_dispatches_per_run")}, Options: []InboxOption{{ID: "another_round", EffectCode: "raise_dispatch_cap"}, {ID: "cancel_run", EffectCode: "cancel_run"}}})
	}
	rung := rex.RequestedRung
	if spec.Lane == "" && rung == "" {
		if spec.Rung != "" {
			rung = spec.Rung
		} else if spec.NeedRoute {
			out, e := h.call(ctx, "route", map[string]any{"run": run.CatherdRunID, "role": spec.Role})
			if e != nil {
				return false, rex, s.roleToolError(ctx, run, rex, e, false)
			}
			if rung = strOf(out, "rung"); rung == "" {
				return false, rex, &hostFault{Tool: "route", Err: errors.New("no rung in the answer")}
			}
		}
		if rung != "" {
			if err = a.fenced(ctx, s.lease, func(tx context.Context) error {
				var e error
				rex, e = a.rexUpdate(tx, rex, []string{RexPlanned, RexDispatching}, bson.M{"requested_rung": rung})
				return e
			}); err != nil {
				return false, rex, err
			}
		}
	}
	brief, err := readBrief(ctx, a, rex)
	if err != nil {
		return false, rex, err
	}
	args := map[string]any{"run": run.CatherdRunID, "role": spec.Role, "name": spec.Name, "brief": brief}
	if spec.Lane != "" {
		args["lane"] = spec.Lane
	}
	if rung != "" && spec.Lane == "" {
		args["rung"] = rung
	}
	if spec.Thread != "" {
		args["thread"] = spec.Thread
	}
	if spec.Next != "" {
		args["next"] = spec.Next
	}
	d := codexhost.NewDirective("dispatch", args)
	now := time.Now().UTC()
	if err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		var e error
		rex, e = a.rexUpdate(tx, rex, []string{RexPlanned, RexDispatching}, bson.M{"state": RexDispatching, "dispatching_at": now, "tries": rex.Tries + 1, "host": &RexHost{SessionID: h.session.ID, DirectiveID: d.ID}})
		return e
	}); err != nil {
		return false, rex, err
	}
	out, err := h.callWith(ctx, d)
	if err != nil {
		return false, rex, s.roleToolError(ctx, run, rex, err, true)
	}
	if err = a.hook(spec.Stage); err != nil {
		return false, rex, err
	}
	adm, _ := out["dispatched"].(map[string]any)
	id := strOf(adm, "dispatchId")
	if id == "" {
		return false, rex, &hostFault{Tool: "dispatch", Err: errors.New("no dispatch id in the answer")}
	}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		var e error
		rex, e = a.rexUpdate(tx, rex, []string{RexDispatching}, bson.M{"state": RexDispatched, "dispatched_at": time.Now().UTC(), "catherd": &RexCatherd{RunID: run.CatherdRunID, DispatchID: id, Rung: strOf(adm, "rung")}})
		if e != nil {
			return e
		}
		if e = a.AppendEvent(tx, run.OrgID, "role.dispatched", Subject{"run", run.ID}, bson.M{"run_id": run.ID, "rex_id": rex.ID, "role": rex.Role, "name": rex.Name, "rung": strOf(adm, "rung")}, nil, nil, true); e != nil {
			return e
		}
		return a.enqueueRoleWatch(tx, run.OrgID, run.ID, 0)
	})
	return true, rex, err
}

func readBrief(ctx context.Context, a *App, rex RoleExecDoc) (string, error) {
	art, err := findOne[ArtifactDoc](ctx, a.DB, "artifacts", bson.M{"_id": rex.BriefArtifactID, "org_id": rex.OrgID})
	if err != nil {
		return "", err
	}
	data, err := readBlob(filepath.Join(a.Config.DataDir, "artifacts"), rex.OrgID, art.Sha256)
	return string(data), err
}

func catherdJobError(err error) error {
	var ce *catherd.Error
	if errors.As(err, &ce) {
		return &JobFailure{Code: "catherd_unavailable", Message: "catherd could not be read", Retryable: true}
	}
	return err
}

// roleToolError maps a failed directive to the run outcome (H11). dispatching says the rex was marked dispatching.
func (s *stageCtx) roleToolError(ctx context.Context, run RunDoc, rex RoleExecDoc, err error, dispatching bool) error {
	a := s.a
	var te *codexhost.ToolError
	if !errors.As(err, &te) {
		return err // hostFault, outcome unknown, job failures: the callers' policies apply
	}
	revert := func() error {
		if !dispatching {
			return nil
		}
		return a.fenced(ctx, s.lease, func(tx context.Context) error {
			_, e := a.rexUpdate(tx, rex, []string{RexDispatching}, bson.M{"state": RexPlanned})
			return e
		})
	}
	if e := revert(); e != nil {
		return e
	}
	stop := func(reason, code string, opts []InboxOption) error {
		return s.stopFromError(ctx, run, stopSpec{Reason: reason, QuestionCode: code, Params: bson.M{"catherd_code": te.Code, "fix": te.Fix, "name": rex.Name}, Options: opts})
	}
	retry := []InboxOption{{ID: "resume", EffectCode: "resume"}, {ID: "cancel_run", EffectCode: "cancel_run"}}
	switch {
	case te.Code == "E_RUN_BUDGET":
		return stop("budget_exceeded", "run_budget", retry)
	case te.Code == "E_ADMIT_RUNG" || strings.HasPrefix(te.Code, "E_BACKEND_"):
		return stop("model_unavailable", "rung_unavailable", retry)
	case te.Code == "E_ADMIT_DUPLICATE":
		// A live role of that name exists: the previous attempt probably worked. Reconcile on the next pass.
		_ = a.fenced(ctx, s.lease, func(tx context.Context) error {
			_, e := a.rexUpdate(tx, rex, []string{RexPlanned}, bson.M{"state": RexDispatching, "dispatching_at": time.Now().UTC().Add(-time.Hour)})
			return e
		})
		return &JobFailure{Code: "admit_duplicate", Message: "a role of that name is already live", Retryable: true}
	}
	return stop("dependency_unavailable", "catherd_error", retry)
}

// collectRole reads the finished role's result, marks it read in catherd and stores its reply as an artifact.
func (s *stageCtx) collectRole(ctx context.Context, run RunDoc, h *hostRun, rex RoleExecDoc) (RoleExecDoc, error) {
	a := s.a
	out, err := h.call(ctx, "result", map[string]any{"run": run.CatherdRunID, "name": rex.Name})
	if err != nil {
		return rex, s.roleToolError(ctx, run, rex, err, false)
	}
	var hints []string
	if raw, ok := out["hints"].([]any); ok {
		for _, x := range raw {
			if str, ok := x.(string); ok {
				hints = append(hints, str)
			}
		}
	}
	var artID string
	if rex.Catherd != nil && rex.Catherd.ReplyPath != "" && run.CatherdRunDir != "" {
		data, e := execution.ReadReply(run.CatherdRunDir, rex.Catherd.ReplyPath, maxReplyBytes)
		if e == nil && len(data) > 0 {
			art, e2 := s.storeText(ctx, run.OrgID, data, "text/markdown", "role_reply", Subject{"run", run.ID})
			if e2 != nil {
				return rex, e2
			}
			artID = art.ID
		}
	}
	tokens := h.hostTokens()
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		set := bson.M{"state": RexCollected, "reply_artifact_id": artID, "result_hints": hints}
		if tokens != nil {
			set["host_tokens"] = *tokens
		}
		var e error
		if rex, e = a.rexUpdate(tx, rex, []string{RexFinished}, set); e != nil {
			return e
		}
		return a.AppendEvent(tx, run.OrgID, "role.collected", Subject{"run", run.ID}, bson.M{"run_id": run.ID, "rex_id": rex.ID}, nil, nil, true)
	})
	return rex, err
}

func (h *hostRun) hostTokens() *int64 {
	if u, ok := h.client.Usage(h.thread); ok {
		t := u.Total
		return &t
	}
	return nil
}

// replyText returns the stored reply of a collected role.
func (a *App) replyText(ctx context.Context, rex RoleExecDoc) (string, error) {
	if rex.ReplyArtifactID == "" {
		return "", nil
	}
	art, err := findOne[ArtifactDoc](ctx, a.DB, "artifacts", bson.M{"_id": rex.ReplyArtifactID, "org_id": rex.OrgID})
	if err != nil {
		return "", err
	}
	data, err := readBlob(filepath.Join(a.Config.DataDir, "artifacts"), rex.OrgID, art.Sha256)
	return string(data), err
}

var _ = fmt.Sprint
var _ = runstate.StageImplement

// rexSkills is the skill manifest of a role execution (nil when the run pinned none for the role).
func (a *App) rexSkills(ctx context.Context, run RunDoc, role string, round int) bson.M {
	_, man, err := a.skillsFor(ctx, run, role, round)
	if err != nil {
		return nil
	}
	return man
}
