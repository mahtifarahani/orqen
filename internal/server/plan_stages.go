package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/runstate"
	"orqen/internal/triage"
	"path/filepath"
	"strings"
	"time"
)

// planBrief is the deterministic plan of Sprint 3. Sprint 4's PlanPort (the architect) replaces it.
type planBrief struct {
	Goal            string        `json:"goal"`
	Acceptance      []string      `json:"acceptance"`
	TestContract    []planCommand `json:"test_contract"`
	Risk            string        `json:"risk"`
	RiskReasons     []string      `json:"risk_reasons"`
	ProtectedMatch  []string      `json:"protected_matches"`
	Constraints     []string      `json:"constraints"`
	OutOfScope      []string      `json:"out_of_scope"`
	LikelyFiles     []string      `json:"likely_files"`
	Architect       string        `json:"architect"`
	TriageSource    string        `json:"triage_source"`
	SourcesRevision int           `json:"sources_revision"`
}
type planCommand struct {
	Key  string   `json:"key"`
	Kind string   `json:"kind"`
	Argv []string `json:"argv"`
}

// stagePlan stores the plan brief artifact and moves to the plan gate.
func (s *stageCtx) stagePlan(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	proj, err := a.project(ctx, run.OrgID, run.ProjectID)
	if err != nil {
		return false, err
	}
	snap, err := findOne[IssueSnapshotDoc](ctx, a.DB, "issue_snapshots", bson.M{"_id": run.SnapshotID, "org_id": run.OrgID})
	if err != nil {
		return false, err
	}
	brief := planBrief{Goal: snap.Summary, Acceptance: snap.Acceptance.Items, Architect: "sprint4", LikelyFiles: []string{}, TestContract: []planCommand{}, ProtectedMatch: []string{}, Constraints: []string{}, OutOfScope: []string{}, SourcesRevision: run.SourcesRevision}
	if run.Triage != nil {
		brief.Risk, brief.RiskReasons, brief.TriageSource = run.Triage.Risk, nonNilStrings(run.Triage.RiskReasons), run.Triage.Source
	}
	if len(brief.Acceptance) == 0 && snap.Acceptance.Text != "" {
		brief.Acceptance = []string{snap.Acceptance.Text}
	}
	for _, c := range proj.Commands {
		if c.Approved {
			brief.TestContract = append(brief.TestContract, planCommand{c.Key, c.Kind, c.Argv})
		}
	}
	var prot []triage.ProtectedPattern
	for _, p := range proj.ProtectedPaths {
		prot = append(prot, triage.ProtectedPattern{Pattern: p.Pattern, Mode: p.Mode})
	}
	texts := []string{snap.Summary, snap.Description, snap.Acceptance.Text}
	for _, ans := range answerSources(run, snap) {
		if ans.Text != nil {
			brief.Constraints = append(brief.Constraints, *ans.Text)
			texts = append(texts, *ans.Text)
		}
	}
	brief.ProtectedMatch = append(triage.ProtectedMatches(texts, prot, "deny"), triage.ProtectedMatches(texts, prot, "review")...)
	for _, l := range snap.Links {
		brief.OutOfScope = append(brief.OutOfScope, fmt.Sprintf("%s %s: %s", l.Type, l.Key, l.Summary))
	}
	data, err := json.Marshal(brief)
	if err != nil {
		return false, err
	}
	blob, err := storeBlob(filepath.Join(a.Config.DataDir, "artifacts"), run.OrgID, bytes.NewReader(data), a.Config.ArtifactMaxBytes)
	if err != nil {
		return false, err
	}
	if err = a.hook(runstate.StagePlan); err != nil {
		return false, err
	}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		art, e := a.recordArtifact(tx, run.OrgID, blob, "application/json", "plan_brief", Subject{"run", run.ID})
		if e != nil {
			return e
		}
		_, e = s.complete(tx, run, runstate.StagePlan, runstate.StagePlanGate, bson.M{"plan": RunPlan{ArtifactID: art.ID, Hash: blob.SHA256}})
		return e
	})
	return false, err
}

// stagePlanGate asks for approval when the pinned policy is manual or the risk is sensitive (always manual).
func (s *stageCtx) stagePlanGate(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return false, err
	}
	sensitive := run.Triage != nil && run.Triage.Risk == triage.RiskSensitive
	needsApproval := pv.Str("automation.plan_approval") == "manual" || sensitive
	if run.Plan != nil && run.Plan.Approved {
		needsApproval = false
	}
	// With the architect enabled the plan that matters is the architect's: approval moves to the architect gate.
	if enabled, e := a.architectEnabled(ctx, run); e == nil && enabled {
		err = a.fenced(ctx, s.lease, func(tx context.Context) error {
			_, e := s.complete(tx, run, runstate.StagePlanGate, runstate.StageStartGate, nil)
			return e
		})
		return false, err
	}
	if !needsApproval {
		err = a.fenced(ctx, s.lease, func(tx context.Context) error {
			now := time.Now().UTC()
			_, e := s.complete(tx, run, runstate.StagePlanGate, runstate.StageStartGate, bson.M{"plan.approved": true, "plan.approved_at": now})
			return e
		})
		return false, err
	}
	params := bson.M{"risk": ""}
	if run.Triage != nil {
		params["risk"] = run.Triage.Risk
	}
	params["forced"] = sensitive && pv.Str("automation.plan_approval") != "manual"
	var ev []InboxEvidence
	if run.Plan != nil {
		ev = append(ev, InboxEvidence{SourceID: run.Plan.ArtifactID, Kind: "plan_brief", Label: "plan", Excerpt: run.IssueKey, URL: ""})
	}
	return true, &stageStop{Spec: stopSpec{Kind: "approval", Reason: "plan_approval", QuestionCode: "plan_approval", Params: params, Evidence: ev, Options: optionsFor("plan_approval", "")}}
}

// stageStartGate holds for the user's Start (coding_start=manual), then requires an enabled, ready project.
func (s *stageCtx) stageStartGate(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return false, err
	}
	if pv.Str("automation.coding_start") == "manual" && !run.StartApproved {
		return true, s.hold(ctx, run, runstate.HoldCodingStart, "waiting for Start")
	}
	code, blockers, err := a.gateBlockers(ctx, run)
	if err != nil {
		return false, err
	}
	if code != "" {
		return true, s.hold(ctx, run, code, strings.Join(blockers, ", "))
	}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		_, e := s.complete(tx, run, runstate.StageStartGate, runstate.StageWorkspace, nil)
		return e
	})
	return false, err
}
