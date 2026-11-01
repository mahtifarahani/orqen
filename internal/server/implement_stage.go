package server

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/rolebriefs"
	"orqen/internal/runstate"
	"orqen/internal/triage"
	"orqen/internal/workspace"
	"path/filepath"
	"strings"
	"time"
)

func attemptName(base string, attempt int) string {
	if attempt == 0 {
		return base
	}
	return fmt.Sprintf("%s-c%d", base, attempt)
}

// roleFailure classifies a collected role: limit stops the run, failed is a role that did not do its work.
func roleFailure(rex RoleExecDoc) (failed bool, limit bool, reason string) {
	if rex.Catherd == nil {
		return false, false, ""
	}
	switch rex.Catherd.Status {
	case "ok":
		if rex.Role == "verifier" || rex.Role == "reviewer" {
			return false, false, "" // their verdict is in the reply text, not in the STATUS word
		}
		switch rex.Catherd.ReplyStatus {
		case "refused":
			return true, false, "refused"
		case "blocked":
			return true, false, "blocked"
		}
		return false, false, ""
	case "limit":
		return false, true, ""
	}
	return true, false, "blocked"
}

// roleAttempts runs a role and, when it failed, lets onFailure decide (climb and try once more, or stop).
func (s *stageCtx) roleAttempts(ctx context.Context, run RunDoc, h *hostRun, spec roleSpec, onFailure func(attempt int, rex RoleExecDoc, reason string) error) (bool, RoleExecDoc, error) {
	for a := 0; ; a++ {
		sp := spec
		sp.Name = attemptName(spec.Name, a)
		if a > 0 {
			sp.Thread = ""
		}
		parked, rex, err := s.roleStep(ctx, run, h, sp)
		if err != nil || parked {
			return parked, rex, err
		}
		if rex.Climbed {
			continue
		}
		failed, limit, reason := roleFailure(rex)
		if limit {
			return false, rex, s.roleOutcomeStop(ctx, run, rex)
		}
		if failed {
			if err = onFailure(a, rex, reason); err != nil {
				return false, rex, err
			}
			continue
		}
		return false, rex, nil
	}
}

// markClimbed records that a role execution was replaced by a fresh dispatch.
func (s *stageCtx) markClimbed(ctx context.Context, rex RoleExecDoc) error {
	a := s.a
	return a.fenced(ctx, s.lease, func(tx context.Context) error {
		_, e := a.DB.Coll("role_executions").UpdateOne(tx, bson.M{"_id": rex.ID, "org_id": rex.OrgID}, bson.M{"$set": bson.M{"climbed": true, "updated_at": time.Now().UTC()}})
		return e
	})
}

// climbOnce moves a lane up its ladder with a directed climb, once; a second failure or the top asks the user.
func (s *stageCtx) climbOnce(ctx context.Context, run RunDoc, h *hostRun, lane string) func(int, RoleExecDoc, string) error {
	return func(attempt int, rex RoleExecDoc, reason string) error {
		if attempt >= 1 {
			return s.roleOutcomeStop(ctx, run, rex)
		}
		if lane != "" {
			out, err := h.call(ctx, "climb", map[string]any{"run": run.CatherdRunID, "lane": lane, "reason": reason})
			if err != nil {
				return s.roleToolError(ctx, run, rex, err, false)
			}
			if top, _ := out["top"].(bool); top {
				return s.roleOutcomeStop(ctx, run, rex)
			}
		}
		return s.markClimbed(ctx, rex)
	}
}

// stageImplement runs the lane workers in order, commits their work (Orqen commits, not the workers) and moves on.
func (s *stageCtx) stageImplement(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	h, err := s.openHost(ctx, run)
	if err != nil {
		return false, err
	}
	defer h.close()
	data, proj, snap, err := a.briefData(ctx, run)
	if err != nil {
		return false, err
	}
	if err = a.attachContext(ctx, run, "worker", &data); err != nil {
		return false, err
	}
	_ = proj
	lanes := run.Lanes
	if run.Round > 0 && len(lanes) > 0 {
		lanes = lanes[len(lanes)-1:] // a fix round goes to the last lane's worker, on its own thread
	}
	var last RoleExecDoc
	var lastLane RunLane
	for _, lane := range lanes {
		lane := lane
		spec := roleSpec{Role: "worker", Name: "worker-" + lane.ID, Lane: lane.ID, Stage: runstate.StageImplement, Round: run.Round,
			Brief: func() (rolebriefs.Brief, error) {
				d := data
				d.Lane = rolebriefs.Lane{ID: lane.ID, Title: lane.Title, Owns: lane.Owns, Body: s.laneBody(run, lane)}
				d.Round = run.Round
				d.Weakening = run.FixNotes
				if run.Round > 0 {
					d.Failures, d.Findings = a.fixContext(ctx, run)
				}
				d.Summary = snap.Summary
				return rolebriefs.Render(rolebriefs.Worker, d)
			}}
		if run.Round > 0 {
			spec.Thread = a.previousThread(ctx, run, "worker-"+lane.ID)
		}
		parked, rex, err := s.roleAttempts(ctx, run, h, spec, s.climbOnce(ctx, run, h, lane.ID))
		if err != nil || parked {
			return parked, err
		}
		last, lastLane = rex, lane
	}
	return s.commitWork(ctx, run, h, last, lastLane, snap)
}

func (s *stageCtx) laneBody(run RunDoc, lane RunLane) string {
	data, err := readLane(run.CatherdRunDir, lane.ID)
	if err != nil {
		return lane.Title
	}
	return data
}

// previousThread returns the catherd thread of the latest execution of a worker name.
func (a *App) previousThread(ctx context.Context, run RunDoc, base string) string {
	rexes, err := findMany[RoleExecDoc](ctx, a.DB, "role_executions", bson.M{"org_id": run.OrgID, "run_id": run.ID, "role": "worker", "name": bson.M{"$regex": "^" + strings.ReplaceAll(base, ".", `\.`) + `(-c\d+)?$`}, "state": RexCollected}, bson.D{{Key: "created_at", Value: -1}}, 1)
	if err != nil || len(rexes) == 0 || rexes[0].Catherd == nil {
		return ""
	}
	return rexes[0].Catherd.Thread
}

// commitWork is H18: inspect the worktree, refuse protected paths, commit locally with the neutral identity.
func (s *stageCtx) commitWork(ctx context.Context, run RunDoc, h *hostRun, last RoleExecDoc, lastLane RunLane, snap IssueSnapshotDoc) (bool, error) {
	a := s.a
	wt, err := a.worktreePath(ctx, run)
	if err != nil {
		return false, err
	}
	ws, _, err := a.workspaceDoc(ctx, run.OrgID, run.ID)
	if err != nil {
		return false, err
	}
	wm, err := a.workspaceManager(run.OrgID, nil, 0)
	if err != nil {
		return false, err
	}
	git := func(args ...string) (string, error) {
		res, e := wm.Git.Run(ctx, wt, workspace.Creds{}, 0, args...)
		return res.Stdout, e
	}
	prev := run.Heads.Head
	if prev == "" {
		prev = ws.BaseSHA
	}
	status, err := git("status", "--porcelain=v1", "-z")
	if err != nil {
		return false, err
	}
	var paths []string
	for _, entry := range strings.Split(status, "\x00") {
		if len(entry) > 3 {
			paths = append(paths, strings.TrimSpace(entry[3:]))
		}
	}
	headOut, err := git("rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	head := strings.TrimSpace(headOut)
	workerCommitted := false
	if len(paths) == 0 {
		if head == prev {
			// nothing changed: the worker refused in effect (H11)
			if strings.Contains(last.Name, "-c") {
				return false, s.roleOutcomeStop(ctx, run, last)
			}
			if err = s.climbOnce(ctx, run, h, lastLane.ID)(0, last, "unchanged"); err != nil {
				return false, err
			}
			return false, nil
		}
		// HEAD moved with a clean tree: our own commit before a crash, or a commit the worker made
		msg, _ := git("log", "-1", "--format=%B")
		workerCommitted = !strings.Contains(msg, "Orqen-Run: "+run.ID)
	} else {
		var deny []triage.ProtectedPattern
		proj, e := a.project(ctx, run.OrgID, run.ProjectID)
		if e != nil {
			return false, e
		}
		for _, p := range proj.ProtectedPaths {
			deny = append(deny, triage.ProtectedPattern{Pattern: p.Pattern, Mode: p.Mode})
		}
		if hits := triage.ProtectedMatches(paths, deny, "deny"); len(hits) > 0 {
			return false, s.stopFromError(ctx, run, stopSpec{Reason: "protected_change", QuestionCode: "protected_paths_changed", Params: bson.M{"paths": toAny(hits)},
				Options: []InboxOption{{ID: "send_back", EffectCode: "send_back"}, {ID: "cancel_run", EffectCode: "cancel_run"}}})
		}
		pv, e := a.pinnedPolicy(ctx, run)
		if e != nil {
			return false, e
		}
		if _, err = git("add", "-A"); err != nil {
			return false, err
		}
		summary := strings.Join(strings.Fields(snap.Summary), " ")
		if len(summary) > 200 {
			summary = summary[:200]
		}
		msg := fmt.Sprintf("%s: %s\n\nOrqen-Run: %s", run.IssueKey, summary, run.ID)
		name, email := pv.Str("publish.commit_author_name"), pv.Str("publish.commit_author_email")
		if _, err = git("-c", "user.name="+name, "-c", "user.email="+email, "-c", "commit.gpgsign=false", "commit", "-q", "--author="+name+" <"+email+">", "-m", msg); err != nil {
			return false, err
		}
		headOut, err = git("rev-parse", "HEAD")
		if err != nil {
			return false, err
		}
		head = strings.TrimSpace(headOut)
	}
	if err = a.hook(runstate.StageImplement); err != nil {
		return false, err
	}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		extra := []eventSpec{{"run.committed", bson.M{"head": head, "round": run.Round}}}
		if workerCommitted {
			extra = append(extra, eventSpec{"run.warning", bson.M{"code": "worker_committed"}})
		}
		set := bson.M{"heads.base": ws.BaseSHA, "heads.head": head, "fix_notes": []string{}}
		_, e := s.complete(tx, run, runstate.StageImplement, runstate.StageVerify, set, extra...)
		return e
	})
	return false, err
}

const tailBytes = 2048

func tail(b []byte) string {
	if len(b) > tailBytes {
		b = b[len(b)-tailBytes:]
	}
	return string(b)
}

// fixContext lists what the next fix round must address: failing commands of the latest evidence (the last 2 KiB of
// their output) and the verifier's complaint.
func (a *App) fixContext(ctx context.Context, run RunDoc) ([]rolebriefs.Failure, []rolebriefs.Finding) {
	var failures []rolebriefs.Failure
	var findings []rolebriefs.Finding
	if fs, e := findMany[ReviewFindingDoc](ctx, a.DB, "review_findings", bson.M{"org_id": run.OrgID, "run_id": run.ID, "status": FindingOpen, "blocking": true}, bson.D{{Key: "round", Value: 1}, {Key: "finding_key", Value: 1}}, 100); e == nil {
		for _, f := range fs {
			findings = append(findings, rolebriefs.Finding{Severity: f.Severity, File: f.File, Line: f.Line, Title: f.Title, Body: f.Body})
		}
	}
	if run.Verification == nil {
		return nil, findings
	}
	ev, err := findOne[VerificationEvidenceDoc](ctx, a.DB, "verification_evidence", bson.M{"_id": run.Verification.EvidenceID, "org_id": run.OrgID})
	if err != nil {
		return nil, findings
	}
	read := func(id string) []byte {
		if id == "" {
			return nil
		}
		art, e := findOne[ArtifactDoc](ctx, a.DB, "artifacts", bson.M{"_id": id, "org_id": run.OrgID})
		if e != nil {
			return nil
		}
		b, _ := readBlob(filepath.Join(a.Config.DataDir, "artifacts"), run.OrgID, art.Sha256)
		return b
	}
	for _, c := range ev.Commands {
		if c.Outcome == "pass" {
			continue
		}
		out := append(read(c.StderrArtifactID), read(c.StdoutArtifactID)...)
		failures = append(failures, rolebriefs.Failure{Key: c.Key, Outcome: c.Outcome, Tail: tail(out)})
	}
	if ev.Verifier != nil && ev.Verifier.Verdict == "FAIL" {
		failures = append(failures, rolebriefs.Failure{Key: "verifier", Outcome: "fail", Tail: tail(read(ev.Verifier.ArtifactID))})
	}
	return failures, findings
}
