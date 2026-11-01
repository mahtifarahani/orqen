package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bmatcuk/doublestar/v4"
	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/catherd"
	"orqen/internal/execution"
	"orqen/internal/rolebriefs"
	"orqen/internal/runstate"
	"orqen/internal/triage"
	"strings"
	"time"
)

// pinnedCommands lists the approved commands of the run's project as brief data.
func (a *App) briefData(ctx context.Context, run RunDoc) (rolebriefs.Data, ProjectDoc, IssueSnapshotDoc, error) {
	proj, err := a.project(ctx, run.OrgID, run.ProjectID)
	if err != nil {
		return rolebriefs.Data{}, proj, IssueSnapshotDoc{}, err
	}
	snap, err := findOne[IssueSnapshotDoc](ctx, a.DB, "issue_snapshots", bson.M{"_id": run.SnapshotID, "org_id": run.OrgID})
	if err != nil {
		return rolebriefs.Data{}, proj, snap, err
	}
	d := rolebriefs.Data{Key: run.IssueKey, Summary: snap.Summary, Acceptance: acceptanceItems(snap)}
	for _, c := range proj.Commands {
		if c.Approved {
			d.Commands = append(d.Commands, rolebriefs.Command{Key: c.Key, Display: strings.Join(c.Argv, " ")})
		}
	}
	for _, p := range proj.ProtectedPaths {
		if p.Mode == "deny" {
			d.DenyPaths = append(d.DenyPaths, p.Pattern)
		}
	}
	for _, ans := range answerSources(run, snap) {
		if ans.Text != nil {
			d.CriticalRules = append(d.CriticalRules, *ans.Text)
		}
	}
	return d, proj, snap, nil
}

func acceptanceItems(snap IssueSnapshotDoc) []string {
	items := append([]string{}, snap.Acceptance.Items...)
	if len(items) == 0 && snap.Acceptance.Text != "" {
		items = []string{snap.Acceptance.Text}
	}
	return items
}

// stageHostSession binds the pinned profile to the worktree, starts (or resumes) the Codex thread, starts the
// catherd run once and checks that catherd resolves the pinned profile.
func (s *stageCtx) stageHostSession(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	h, err := s.openHost(ctx, run)
	if err != nil {
		return false, err
	}
	defer h.close()
	wt, err := a.worktreePath(ctx, run)
	if err != nil {
		return false, err
	}
	ver, err := a.profileVersionOf(ctx, run)
	if err != nil {
		return false, err
	}
	data, _, snap, err := a.briefData(ctx, run)
	if err != nil {
		return false, err
	}
	if err = a.Catherd.ProfileUseRepo(ctx, h.env, catherd.HostCodex, ver.CatherdName, wt); err != nil {
		return false, catherdJobError(err)
	}
	if run.CatherdRunID == "" {
		acc := data.Acceptance
		if len(acc) == 0 {
			acc = []string{"see the ticket"}
		}
		out, e := h.call(ctx, "run_start", map[string]any{"repo": wt, "title": run.IssueKey + ": " + snap.Summary, "a_lines": acc})
		if e != nil {
			return false, s.roleToolError(ctx, run, RoleExecDoc{}, e, false)
		}
		id, dir := strOf(out, "run"), strOf(out, "dir")
		if id == "" || dir == "" {
			return false, &hostFault{Tool: "run_start", Err: errors.New("no run id or folder in the answer")}
		}
		real, e := h.validateRunDir(dir)
		if e != nil {
			return false, e
		}
		if err = a.fenced(ctx, s.lease, func(tx context.Context) error {
			next, e := a.casRun(tx, run, runstate.StageHostSession, bson.M{"catherd_run_id": id, "catherd_run_dir": real}, runEvent{Reason: "catherd_run_started", Actor: runstate.ActorSystem})
			if e != nil {
				return e
			}
			run = next
			_, e = a.DB.Coll("host_sessions").UpdateOne(tx, bson.M{"_id": h.session.ID, "org_id": run.OrgID}, bson.M{"$set": bson.M{"catherd_run_id": id, "catherd_run_dir_rel": strings.TrimPrefix(real, h.env.Home())}})
			return e
		}); err != nil {
			return false, err
		}
		h.run = run
	}
	out, err := h.call(ctx, "profile_get", map[string]any{"repo": wt})
	if err != nil {
		return false, s.roleToolError(ctx, run, RoleExecDoc{}, err, false)
	}
	if strOf(out, "here") != ver.CatherdName {
		return false, &runFailureError{Code: "profile_pin_mismatch", Message: "catherd resolved a different model profile than the one pinned"}
	}
	enabled, err := a.architectEnabled(ctx, run)
	if err != nil {
		return false, err
	}
	next := runstate.StageArchitect
	set := bson.M{}
	if !enabled {
		next = runstate.StageImplement
		lanes, e := s.writeSingleLane(ctx, run, h, snap, data)
		if e != nil {
			return false, e
		}
		set["lanes"] = lanes
	}
	if err = a.hook(runstate.StageHostSession); err != nil {
		return false, err
	}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		_, e := s.complete(tx, run, runstate.StageHostSession, next, set)
		return e
	})
	return false, err
}

// writeSingleLane is the architect-disabled path: Orqen writes plan.md and the one lane M1.L1 from the plan brief.
func (s *stageCtx) writeSingleLane(ctx context.Context, run RunDoc, h *hostRun, snap IssueSnapshotDoc, data rolebriefs.Data) ([]RunLane, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Plan: %s\n\nGoal: %s\n\n## M1\n- M1.L1: implement the ticket (owns the whole repository)\n", run.IssueKey, snap.Summary)
	for _, c := range data.Commands {
		fmt.Fprintf(&b, "\nFull check: %s\n", c.Display)
		break
	}
	lane := fmt.Sprintf("# M1.L1: %s\nOwns: ./\nKind: repo_code\nDifficulty: build\n\nImplement the ticket as its acceptance criteria say.\n", snap.Summary)
	for path, content := range map[string]string{"plan.md": b.String(), "lanes/M1.L1.md": lane} {
		if _, err := h.call(ctx, "write_run_file", map[string]any{"run": run.CatherdRunID, "path": path, "content": content}); err != nil {
			return nil, s.roleToolError(ctx, run, RoleExecDoc{}, err, false)
		}
	}
	return []RunLane{{ID: "M1.L1", Title: "M1.L1: " + snap.Summary, Owns: []string{"./"}, After: []string{}}}, nil
}

func (s *stageCtx) architectName(run RunDoc) string {
	if run.PlanRounds == 0 {
		return "architect-M1"
	}
	return fmt.Sprintf("architect-M1-p%d", run.PlanRounds)
}

// stageArchitect has the architect role write plan.md and the lanes, then validates them.
func (s *stageCtx) stageArchitect(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	h, err := s.openHost(ctx, run)
	if err != nil {
		return false, err
	}
	defer h.close()
	data, proj, _, err := a.briefData(ctx, run)
	if err != nil {
		return false, err
	}
	if err = a.attachContext(ctx, run, "architect", &data); err != nil {
		return false, err
	}
	spec := roleSpec{Role: "architect", Name: s.architectName(run), Stage: runstate.StageArchitect, Round: run.PlanRounds, NeedRoute: true,
		Brief: func() (rolebriefs.Brief, error) { return rolebriefs.Render(rolebriefs.Architect, data) }}
	parked, rex, err := s.roleStep(ctx, run, h, spec)
	if err != nil || parked {
		return parked, err
	}
	if rex.Catherd == nil || rex.Catherd.Status != "ok" {
		return false, s.roleOutcomeStop(ctx, run, rex)
	}
	plan, err := execution.ParsePlan(run.CatherdRunDir)
	if err != nil {
		var pe *execution.PlanError
		if errors.As(err, &pe) {
			return false, s.stopFromError(ctx, run, stopSpec{Reason: "ambiguous_behavior", QuestionCode: "plan_invalid", Params: bson.M{"plan_error": pe.Code}, Options: []InboxOption{{ID: "retry_dispatch", EffectCode: "replan"}, {ID: "cancel_run", EffectCode: "cancel_run"}}})
		}
		return false, err
	}
	var deny []triage.ProtectedPattern
	for _, p := range proj.ProtectedPaths {
		deny = append(deny, triage.ProtectedPattern{Pattern: p.Pattern, Mode: p.Mode})
	}
	var lanes []RunLane
	var hits []string
	for _, l := range plan.Lanes {
		lanes = append(lanes, RunLane{ID: l.ID, Title: l.Title, Owns: nonNilStr(l.Owns), After: nonNilStr(l.After)})
		hits = append(hits, ownsHits(l.Owns, deny)...)
	}
	if len(hits) > 0 {
		return false, s.stopFromError(ctx, run, stopSpec{Reason: "protected_change", QuestionCode: "lane_owns_protected", Params: bson.M{"paths": toAny(hits)}, Options: optionsFor("protected_change", "")})
	}
	laneJSON, _ := json.Marshal(lanes)
	planArt, err := s.storeText(ctx, run.OrgID, []byte(plan.Text), "text/markdown", "architect_plan", Subject{"run", run.ID})
	if err != nil {
		return false, err
	}
	lanesArt, err := s.storeText(ctx, run.OrgID, laneJSON, "application/json", "architect_lanes", Subject{"run", run.ID})
	if err != nil {
		return false, err
	}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		set := bson.M{"lanes": lanes, "plan.artifact_id": planArt.ID, "plan.hash": planArt.Sha256}
		_, e := s.complete(tx, run, runstate.StageArchitect, runstate.StageArchitectGate, set, eventSpec{"run.plan_ready", bson.M{"plan_artifact_id": planArt.ID, "lanes_artifact_id": lanesArt.ID}})
		return e
	})
	return false, err
}

// readLane reads a lane file of the catherd run folder (read-only, capped).
func readLane(runDir, id string) (string, error) {
	b, err := execution.ReadRunFile(runDir, "lanes/"+id+".md", 64<<10)
	return string(b), err
}

// ownsHits lists the owned paths of a lane that a deny pattern covers: the path itself, or any file below an
// owned directory.
func ownsHits(owns []string, deny []triage.ProtectedPattern) []string {
	var hits []string
	for _, o := range owns {
		o = strings.TrimPrefix(strings.TrimSpace(o), "./")
		for _, p := range deny {
			if p.Mode != "deny" {
				continue
			}
			probe := strings.TrimSuffix(o, "/")
			ok, _ := doublestar.Match(p.Pattern, probe)
			if !ok {
				ok, _ = doublestar.Match(p.Pattern, probe+"/x")
			}
			if !ok {
				// an owned directory above the pattern's fixed prefix (owning "." or "infra" covers "infra/secret/**")
				prefix, _, _ := strings.Cut(p.Pattern, "*")
				ok = probe == "." || probe == "" || strings.HasPrefix(prefix, probe+"/")
			}
			if ok {
				hits = append(hits, o+" ("+p.Pattern+")")
				break
			}
		}
	}
	return hits
}

func toAny(in []string) bson.A {
	out := bson.A{}
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

// roleOutcomeStop turns a role that did not end ok into the Inbox question of H11.
func (s *stageCtx) roleOutcomeStop(ctx context.Context, run RunDoc, rex RoleExecDoc) error {
	status := ""
	if rex.Catherd != nil {
		status = rex.Catherd.Status
	}
	code, reason := "role_failed", "model_unavailable"
	if status == "limit" {
		code = "usage_limit"
	}
	return s.stopFromError(ctx, run, stopSpec{Reason: reason, QuestionCode: code, Params: bson.M{"name": rex.Name, "status": status},
		Options: []InboxOption{{ID: "retry_dispatch", EffectCode: "retry_dispatch"}, {ID: "cancel_run", EffectCode: "cancel_run"}}})
}

// stageArchitectGate asks for approval of the architect's plan when the policy is manual or the risk sensitive.
func (s *stageCtx) stageArchitectGate(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return false, err
	}
	sensitive := run.Triage != nil && run.Triage.Risk == triage.RiskSensitive
	need := pv.Str("automation.plan_approval") == "manual" || sensitive
	if run.Plan != nil && run.Plan.Approved {
		need = false
	}
	if need {
		var ev []InboxEvidence
		if run.Plan != nil {
			ev = append(ev, InboxEvidence{SourceID: run.Plan.ArtifactID, Kind: "architect_plan", Label: "plan", Excerpt: run.IssueKey})
		}
		return true, &stageStop{Spec: stopSpec{Kind: "approval", Reason: "plan_approval", QuestionCode: "architect_plan_approval", Params: bson.M{"risk": riskOf(run)}, Evidence: ev, Options: optionsFor("plan_approval", "")}}
	}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		_, e := s.complete(tx, run, runstate.StageArchitectGate, runstate.StageImplement, bson.M{"plan.approved": true, "plan.approved_at": time.Now().UTC()})
		return e
	})
	return false, err
}

func riskOf(run RunDoc) string {
	if run.Triage != nil {
		return run.Triage.Risk
	}
	return ""
}
