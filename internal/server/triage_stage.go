package server

import (
	"context"
	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/runstate"
	"orqen/internal/sysprobe"
	"orqen/internal/triage"
	"strings"
	"time"
	"unicode/utf8"
)

// answerSources returns the user's answers that still apply to the snapshot. A carried missing_acceptance answer
// stands in for acceptance only while Jira's acceptance is still empty.
func answerSources(run RunDoc, snap IssueSnapshotDoc) []RunSource {
	var out []RunSource
	for _, s := range run.Sources {
		if !answerKind(s.Kind) || s.Text == nil && s.Code == "" {
			continue
		}
		if s.Carried && s.Code == "missing_acceptance" && snap.Acceptance.Source != "none" {
			continue
		}
		out = append(out, s)
	}
	return out
}
func hasAnswerCode(srcs []RunSource, code string) bool {
	for _, s := range srcs {
		if s.Code == code {
			return true
		}
	}
	return false
}

func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// evidenceFor lists what the user needs to see to answer: the issue text the decision looked at.
func evidenceFor(run RunDoc, snap IssueSnapshotDoc, ti TrackedIssueDoc) []InboxEvidence {
	url := ti.URL
	ev := []InboxEvidence{{SourceID: run.SnapshotID, Kind: "jira_summary", Label: run.IssueKey, Excerpt: excerpt(snap.Summary, 300), URL: url}}
	if strings.TrimSpace(snap.Description) != "" {
		ev = append(ev, InboxEvidence{SourceID: run.SnapshotID, Kind: "jira_description", Label: "description", Excerpt: excerpt(snap.Description, 300), URL: url})
	}
	if snap.Acceptance.Source != "none" && snap.Acceptance.Text != "" {
		ev = append(ev, InboxEvidence{SourceID: run.SnapshotID, Kind: "jira_acceptance", Label: "acceptance", Excerpt: excerpt(snap.Acceptance.Text, 300), URL: url})
	}
	if p := snap.Parent; p != nil {
		ev = append(ev, InboxEvidence{SourceID: run.SnapshotID, Kind: "jira_parent", Label: p.Key, Excerpt: excerpt(p.Summary+" — "+p.Description, 300), URL: url})
	}
	return ev
}

// stageTriage applies the hard guards, then (if configured and a key is set) one bounded Jev decision, then the
// fixed rules. The outcome is committed in one transaction with the run's next stage or its Inbox question.
func (s *stageCtx) stageTriage(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	proj, err := a.project(ctx, run.OrgID, run.ProjectID)
	if err != nil {
		return false, err
	}
	snap, err := findOne[IssueSnapshotDoc](ctx, a.DB, "issue_snapshots", bson.M{"_id": run.SnapshotID, "org_id": run.OrgID})
	if err != nil {
		return false, err
	}
	ti, err := a.tracked(ctx, run.OrgID, run.TrackedIssueID)
	if err != nil {
		return false, err
	}
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return false, err
	}
	res, err := a.readinessResults(ctx, run.OrgID, []ProjectDoc{proj})
	if err != nil {
		return false, err
	}
	var readinessCodes []string
	for _, b := range res[0].Blockers {
		readinessCodes = append(readinessCodes, b.Code)
	}
	sys, err := a.systemState(ctx, run.OrgID)
	if err != nil {
		return false, err
	}
	dockerFailed := false
	if p := sys.LastPreflight; p != nil {
		for _, c := range p.Checks {
			if c.ID == "docker.engine" && c.Status != sysprobe.Pass {
				dockerFailed = true
			}
		}
	}
	answers := answerSources(run, snap)
	var answerText, allAnswers []string
	for _, ans := range answers {
		if ans.Text != nil {
			allAnswers = append(allAnswers, *ans.Text)
			if ans.Code == "missing_acceptance" {
				answerText = append(answerText, *ans.Text)
			}
		}
	}
	gi := triage.GuardInput{StatusCategory: snap.Status.Category, Resolution: snap.Resolution, Inaccessible: ti.Inaccessible, ProjectsClaim: len(ti.AmbiguousProject), ReadinessBlockers: readinessCodes,
		AcceptanceText: snap.Acceptance.Text, AcceptanceSource: snap.Acceptance.Source, AnswerAcceptance: strings.Join(answerText, "\n"), AckProtected: hasAnswerCode(answers, "protected_ack"),
		TextForPaths: append([]string{snap.Summary, snap.Description, snap.Acceptance.Text}, allAnswers...)}
	for _, c := range proj.Commands {
		if c.Approved && c.Kind == "test" {
			gi.HasApprovedTest = true
			gi.NeedsDocker = gi.NeedsDocker || c.NeedsDocker
		}
	}
	for _, l := range snap.Links {
		if l.Type == "Blocks" && l.Direction == "inward" {
			gi.Blockers = append(gi.Blockers, triage.Blocker{Key: l.Key, StatusCategory: l.StatusCategory})
		}
	}
	gi.DockerFailed = dockerFailed
	var prot []triage.ProtectedPattern
	for _, p := range proj.ProtectedPaths {
		prot = append(prot, triage.ProtectedPattern{Pattern: p.Pattern, Mode: p.Mode})
	}
	gi.Protected = prot
	findings := triage.Guards(gi)
	areaClass := map[string]string{}
	for _, area := range triage.Areas {
		areaClass[area] = pv.Str("risk.area." + area)
	}
	acceptanceText := snap.Acceptance.Text
	if snap.Acceptance.Source == "none" {
		acceptanceText = strings.Join(answerText, "\n")
	}
	accSource, accItems := snap.Acceptance.Source, len(snap.Acceptance.Items)
	if accSource == "none" && strings.TrimSpace(acceptanceText) != "" {
		accSource, accItems = "answer", len(strings.Split(strings.TrimSpace(acceptanceText), "\n")) // the user's own acceptance
	}
	in := triage.DecideInput{Findings: findings, AcceptanceSource: accSource, AcceptanceItems: accItems, HasApprovedTest: gi.HasApprovedTest,
		UserClarified: hasAnswerCode(answers, "clarification"), IgnoreAlreadyDone: hasAnswerCode(answers, "already_done_ack"),
		Risk: triage.RiskInput{Text: []string{snap.Summary, acceptanceText, snap.Description}, AreaClass: areaClass, ReviewMatches: triage.ProtectedMatches(gi.TextForPaths, prot, "review"), AcceptanceItems: len(snap.Acceptance.Items), LinkCount: len(snap.Links)}}
	var decisionID *string
	if len(findings) == 0 && pv.Str("decision.jev") == "auto" {
		key, ok, kerr := a.jevKey(ctx, run.OrgID)
		if kerr != nil {
			return false, kerr
		}
		if ok {
			state := triage.JevState(s.jevStateInput(snap, answers, proj, gi.HasApprovedTest))
			resp, doc, cerr := s.triageCall(ctx, run, state, key)
			if cerr != nil {
				return false, cerr
			}
			if doc != nil {
				decisionID = &doc.ID
			}
			if resp != nil {
				j := triage.ReadJev(*resp)
				in.Jev = &j
			}
		}
	}
	d := triage.Decide(in)
	if err = a.hook(runstate.StageTriage); err != nil {
		return false, err
	}
	rt := RunTriage{Outcome: d.Outcome, Reasons: d.Reasons, Risk: d.Risk, RiskReasons: d.RiskReasons, Source: d.Source, DecisionID: decisionID, SourcesRevision: run.SourcesRevision, DecidedAt: time.Now().UTC()}
	switch d.Outcome {
	case triage.OutcomeExecute:
		err = a.fenced(ctx, s.lease, func(tx context.Context) error {
			_, e := s.complete(tx, run, runstate.StageTriage, runstate.StagePlan, bson.M{"triage": rt})
			return e
		})
		return false, err
	case triage.OutcomeWaitingDependency:
		err = a.fenced(ctx, s.lease, func(tx context.Context) error {
			_, e := a.casRun(tx, run, runstate.StageTriage, bson.M{"state": runstate.WaitingDep, "triage": rt, "dep_checked_at": time.Now().UTC()},
				runEvent{Reason: "blocked_by", Actor: runstate.ActorSystem, Extra: []eventSpec{{"run.stage_completed", bson.M{"stage": runstate.StageTriage}}}})
			return e
		})
		return true, err
	case triage.OutcomeAlreadyAddressed:
		err = a.fenced(ctx, s.lease, func(tx context.Context) error {
			now := time.Now().UTC()
			if _, e := a.casRun(tx, run, runstate.StageTriage, bson.M{"state": runstate.Cancelled, "active": false, "triage": rt, "open_inbox_id": nil, "terminal": RunTerminal{Reason: "already_addressed", At: now}},
				runEvent{Reason: "already_addressed", Actor: runstate.ActorSystem}); e != nil {
				return e
			}
			_, e := a.endAttempt(tx, run, "cancelled:already_addressed")
			return e
		})
		return true, err
	}
	spec := stopSpec{Reason: d.Reason, QuestionCode: d.QuestionCode, Params: bson.M(d.Params), Evidence: evidenceFor(run, snap, ti), Options: optionsFor(d.Reason, d.QuestionCode), Set: bson.M{"triage": rt}}
	if spec.Params == nil {
		spec.Params = bson.M{}
	}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		cur, e := a.run(tx, run.OrgID, run.ID)
		if e != nil {
			return e
		}
		return a.stopRun(tx, cur, spec)
	})
	return true, err
}

func (s *stageCtx) jevStateInput(snap IssueSnapshotDoc, answers []RunSource, proj ProjectDoc, hasTest bool) triage.StateInput {
	in := triage.StateInput{Title: snap.Summary, IssueType: snap.IssueType, Acceptance: snap.Acceptance.Text, Description: snap.Description, HasTestCommand: hasTest}
	if p := snap.Parent; p != nil {
		in.Parent = &triage.ParentInfo{Summary: p.Summary, Description: p.Description}
	}
	for _, l := range snap.Links {
		in.Links = append(in.Links, triage.LinkInfo{Type: l.Type, Summary: l.Summary})
	}
	for _, c := range snap.Comments {
		in.Comments = append(in.Comments, c.Body)
	}
	for _, a := range answers {
		if a.Text != nil {
			in.Answers = append(in.Answers, *a.Text)
		}
	}
	if p := proj.Mapping.primary(); p != nil && p.PackagePath != nil {
		in.PackagePath = *p.PackagePath
	}
	return in
}
