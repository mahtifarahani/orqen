package server

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"orqen/internal/catherd"
	"orqen/internal/contextpkt"
	"orqen/internal/gitdiff"
	"orqen/internal/rolebriefs"
	"orqen/internal/runstate"
	"orqen/internal/verify"
	"orqen/internal/workspace"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Waiver is a person's acceptance of a test-weakening suspicion for one head.
type Waiver struct {
	Head string    `bson:"head" json:"head"`
	Text string    `bson:"text" json:"text"`
	At   time.Time `bson:"at" json:"at"`
}

func (a *App) dockerUsable(ctx context.Context) bool {
	if a.dockerCheck != nil {
		return a.dockerCheck()
	}
	path, err := exec.LookPath("docker")
	if err != nil {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, path, "version", "--format", "{{.Server.Version}}").Run() == nil
}

// knownSecrets are the connection credentials of the organization, used only to scrub command output.
func (a *App) knownSecrets(ctx context.Context, org string) []string {
	conns, err := findMany[ConnectionDoc](ctx, a.DB, "connections", bson.M{"org_id": org, "active": true}, bson.D{{Key: "_id", Value: 1}}, 100)
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range conns {
		if c.CredentialRef != nil {
			if v, ok, e := a.Creds.Get(ctx, *c.CredentialRef); e == nil && ok && len(v) >= 4 {
				out = append(out, v)
			}
		}
	}
	return out
}

func scrubber(secrets []string) func([]byte) []byte {
	return func(b []byte) []byte {
		s := contextpkt.Scrub(string(b))
		for _, sec := range secrets {
			s = strings.ReplaceAll(s, sec, "[secret]")
		}
		return []byte(s)
	}
}

func (a *App) childPath() string {
	p := os.Getenv("PATH")
	if a.CatherdRT != nil && a.CatherdRT.ChildPath != "" {
		p = a.CatherdRT.ChildPath
	}
	return catherd.MergePath(p, "/usr/bin:/bin:/usr/sbin:/sbin")
}

func (a *App) findEvidence(ctx context.Context, run RunDoc, kind, head, fp string) (VerificationEvidenceDoc, bool, error) {
	d, err := findOne[VerificationEvidenceDoc](ctx, a.DB, "verification_evidence", bson.M{"org_id": run.OrgID, "run_id": run.ID, "kind": kind, "head_sha": head, "commands_fp": fp})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return d, false, nil
	}
	return d, err == nil, err
}

func (s *stageCtx) saveEvidence(ctx context.Context, ev VerificationEvidenceDoc) error {
	a := s.a
	return a.fenced(ctx, s.lease, func(tx context.Context) error {
		ev.CreatedAt = nowIfZero(ev.CreatedAt)
		_, e := a.DB.Coll("verification_evidence").ReplaceOne(tx, bson.M{"org_id": ev.OrgID, "run_id": ev.RunID, "kind": ev.Kind, "head_sha": ev.HeadSHA, "commands_fp": ev.CommandsFP}, ev, options.Replace().SetUpsert(true))
		return e
	})
}

func nowIfZero(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}

// resolveEnv turns a command's env references into KEY=VALUE pairs.
func resolveEnv(vars []EnvVar) []string {
	var out []string
	for _, v := range vars {
		switch v.Source.Kind {
		case "literal":
			if v.Source.Value != nil {
				out = append(out, v.Name+"="+*v.Source.Value)
			}
		case "host":
			if v.Source.Var != nil {
				if val := os.Getenv(*v.Source.Var); val != "" {
					out = append(out, v.Name+"="+val)
				}
			}
		}
	}
	return out
}

func commandsFP(cmds []CommandSpec) string {
	var parts []string
	for _, c := range cmds {
		parts = append(parts, c.Key+":"+c.Fingerprint)
	}
	return sha256Hex(strings.Join(parts, "\n"))
}

// runCommandSet runs the approved commands in a detached checkout of sha and stores their outputs.
func (s *stageCtx) runCommandSet(ctx context.Context, run RunDoc, wm *workspace.Manager, mirror, sha string, cmds []CommandSpec, pv policyValues) ([]verify.Result, []EvidenceCommand, error) {
	a := s.a
	dir, err := wm.VerifyPath(run.ID, sha)
	if err != nil {
		return nil, nil, err
	}
	if err = wm.AddDetached(ctx, mirror, dir, sha); err != nil {
		return nil, nil, err
	}
	defer func() { _ = wm.RemoveDetached(context.WithoutCancel(ctx), mirror, dir) }()
	var vc []verify.Command
	for _, c := range cmds {
		vc = append(vc, verify.Command{Key: c.Key, Argv: c.Argv, Cwd: c.Cwd, Env: resolveEnv(c.Env), Timeout: time.Duration(c.TimeoutSecs) * time.Second, NeedsNetwork: c.NeedsNetwork, NeedsDocker: c.NeedsDocker})
	}
	spec := verify.Spec{Dir: dir, Tmp: filepath.Join(filepath.Dir(dir), "tmp-"+run.ID+"-"+sha[:12]), Commands: vc, BaseEnv: []string{"PATH=" + a.childPath(), "LANG=C.UTF-8"},
		Sandbox: pv.Str("verification.sandbox"), MaxOutput: int64(pv.Int("verification.max_output_bytes")), Scrub: scrubber(a.knownSecrets(ctx, run.OrgID)),
		DockerOK: func() bool { return a.dockerUsable(ctx) }, NoSandbox: a.noSandbox}
	defer os.RemoveAll(spec.Tmp)
	results, err := verify.Run(ctx, spec)
	if err != nil {
		return nil, nil, err
	}
	var out []EvidenceCommand
	for i, r := range results {
		so, err := s.storeText(ctx, run.OrgID, r.Stdout, "text/plain", "verification_output", Subject{"run", run.ID})
		if err != nil {
			return nil, nil, err
		}
		se, err := s.storeText(ctx, run.OrgID, r.Stderr, "text/plain", "verification_output", Subject{"run", run.ID})
		if err != nil {
			return nil, nil, err
		}
		out = append(out, EvidenceCommand{Key: r.Key, Fingerprint: cmds[i].Fingerprint, Argv: r.Argv, Cwd: r.Cwd, ExitCode: r.ExitCode, DurationMs: r.DurationMs, TimedOut: r.TimedOut, Sandbox: r.Sandbox, Outcome: r.Outcome, Reason: r.Reason,
			StdoutArtifactID: so.ID, StderrArtifactID: se.ID, OutputSHA256: r.SHA256})
	}
	return results, out, nil
}

// stageVerify is the two-layer verification of H15: Orqen's own command runs first, the verifier role second.
func (s *stageCtx) stageVerify(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	proj, err := a.project(ctx, run.OrgID, run.ProjectID)
	if err != nil {
		return false, err
	}
	var cmds []CommandSpec
	for _, c := range proj.Commands {
		if c.Approved {
			cmds = append(cmds, c)
		}
	}
	head, base := run.Heads.Head, run.Heads.Base
	if head == "" || base == "" {
		return false, &runFailureError{Code: "no_commit", Message: "the run has no commit to verify"}
	}
	fp := commandsFP(cmds)
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return false, err
	}
	ev, found, err := a.findEvidence(ctx, run, "head", head, fp)
	if err != nil {
		return false, err
	}
	if !found || len(ev.Commands) == 0 {
		var stop error
		if ev, stop, err = s.commandLayer(ctx, run, proj, cmds, fp, pv, ev, found); err != nil || stop != nil {
			return false, firstErr(stop, err)
		}
	}
	switch ev.Gate.Status {
	case verify.GateFail:
		return false, s.sendBack(ctx, run, "verification_failed", ev)
	case verify.GateIncomplete:
		if len(ev.Gate.Reasons) == 1 && ev.Gate.Reasons[0] == "verifier_pending" {
			break
		}
		return false, s.stopFromError(ctx, run, stopSpec{Reason: "dependency_unavailable", QuestionCode: "verification_incomplete", Params: bson.M{"reasons": toAny(ev.Gate.Reasons)}, Options: resolveOptions()})
	}
	// the commands passed: now the verifier role, on a fresh thread
	return s.verifierLayer(ctx, run, ev)
}

func firstErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

// commandLayer produces the evidence of the commands (and the weakening check). stop is a stage stop to return.
func (s *stageCtx) commandLayer(ctx context.Context, run RunDoc, proj ProjectDoc, cmds []CommandSpec, fp string, pv policyValues, prior VerificationEvidenceDoc, havePrior bool) (VerificationEvidenceDoc, error, error) {
	a := s.a
	head, base := run.Heads.Head, run.Heads.Base
	wm, err := a.workspaceManager(run.OrgID, nil, 0)
	if err != nil {
		return prior, nil, err
	}
	ws, _, err := a.workspaceDoc(ctx, run.OrgID, run.ID)
	if err != nil {
		return prior, nil, err
	}
	mirror := filepath.Join(a.Config.DataDir, ws.MirrorRel)
	wt := filepath.Join(a.Config.DataDir, ws.PathRel)
	res, err := wm.Git.Run(ctx, wt, workspace.Creds{}, 0, "diff", "--no-color", "--no-ext-diff", "-M", base, head)
	if err != nil {
		return prior, nil, err
	}
	diffArt, err := s.storeText(ctx, run.OrgID, []byte(res.Stdout), "text/x-diff", "run_diff", Subject{"run", run.ID})
	if err != nil {
		return prior, nil, err
	}
	diff, err := gitdiff.Parse(res.Stdout)
	if err != nil {
		return prior, nil, err
	}
	var vcmds []verify.Command
	for _, c := range cmds {
		vcmds = append(vcmds, verify.Command{Key: c.Key, Argv: c.Argv})
	}
	signals := verify.Weakening(diff, nil, vcmds)
	ev := VerificationEvidenceDoc{ID: newID("vev"), OrgID: run.OrgID, RunID: run.ID, ProjectID: run.ProjectID, Round: run.Round, Kind: "head", BaseSHA: base, HeadSHA: head, CommandsFP: fp, DiffArtifactID: diffArt.ID, DiffSHA: diffArt.Sha256, Suspected: len(signals) > 0}
	if havePrior {
		ev.ID, ev.CreatedAt = prior.ID, prior.CreatedAt
	}
	for _, sg := range signals {
		ev.Signals = append(ev.Signals, EvidenceSignal{Code: sg.Code, Path: sg.Path, Detail: sg.Detail})
	}
	if len(signals) > 0 {
		if w := run.Waiver; w != nil && w.Head == head {
			ev.Waiver = &EvidenceWaiver{Text: w.Text, At: w.At}
		} else {
			ev.Gate = EvidenceGate{Status: verify.GateIncomplete, Reasons: []string{"test_weakening_suspected"}}
			if err = s.saveEvidence(ctx, ev); err != nil {
				return ev, nil, err
			}
			codes := bson.A{}
			for _, sg := range signals {
				codes = append(codes, sg.Code+":"+sg.Path)
			}
			return ev, s.stopFromError(ctx, run, stopSpec{Reason: "protected_change", QuestionCode: "test_weakening_suspected", Params: bson.M{"signals": codes},
				Options: []InboxOption{{ID: "send_back", EffectCode: "send_back"}, {ID: "accept", RequiresText: true, EffectCode: "accept_weakening"}, {ID: "cancel_run", EffectCode: "cancel_run"}}}), nil
		}
	}
	if len(cmds) == 0 {
		ev.Gate = EvidenceGate{Status: verify.GateIncomplete, Reasons: []string{"no_commands"}}
		return ev, nil, s.saveEvidence(ctx, ev)
	}
	headRes, headEv, err := s.runCommandSet(ctx, run, wm, mirror, head, cmds, pv)
	if err != nil {
		return ev, nil, err
	}
	ev.Commands = headEv
	var baseRes []verify.Result
	needBase := false
	for _, r := range headRes {
		needBase = needBase || r.Outcome == verify.OutcomeFail
	}
	if needBase {
		if baseRes, err = s.baseline(ctx, run, wm, mirror, base, fp, cmds, pv); err != nil {
			return ev, nil, err
		}
	}
	gate := verify.Classify(headRes, baseRes, pv.Bool("verification.allow_baseline_failures"))
	for i := range ev.Commands {
		if c, ok := gate.Classes[ev.Commands[i].Key]; ok && c != verify.ClassPass {
			ev.Commands[i].Outcome = c
		}
	}
	ev.Gate = EvidenceGate{Status: gate.Status, Reasons: gate.Reasons}
	if gate.Status == verify.GatePass {
		ev.Gate = EvidenceGate{Status: verify.GateIncomplete, Reasons: []string{"verifier_pending"}}
	}
	if err = a.hook(runstate.StageVerify); err != nil {
		return ev, nil, err
	}
	return ev, nil, s.saveEvidence(ctx, ev)
}

// baseline returns the results of the commands at the base commit, cached per project, base and command set.
func (s *stageCtx) baseline(ctx context.Context, run RunDoc, wm *workspace.Manager, mirror, base, fp string, cmds []CommandSpec, pv policyValues) ([]verify.Result, error) {
	a := s.a
	cached, err := findOne[VerificationEvidenceDoc](ctx, a.DB, "verification_evidence", bson.M{"org_id": run.OrgID, "project_id": run.ProjectID, "kind": "baseline", "head_sha": base, "commands_fp": fp})
	if err == nil {
		return resultsOf(cached.Commands), nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}
	res, evCmds, err := s.runCommandSet(ctx, run, wm, mirror, base, cmds, pv)
	if err != nil {
		return nil, err
	}
	ev := VerificationEvidenceDoc{ID: newID("vev"), OrgID: run.OrgID, RunID: run.ID, ProjectID: run.ProjectID, Round: run.Round, Kind: "baseline", BaseSHA: base, HeadSHA: base, CommandsFP: fp, Commands: evCmds, Gate: EvidenceGate{Status: "pass"}}
	if err = s.saveEvidence(ctx, ev); err != nil {
		return nil, err
	}
	return res, nil
}

func resultsOf(cmds []EvidenceCommand) []verify.Result {
	var out []verify.Result
	for _, c := range cmds {
		o := verify.OutcomeIncomplete
		switch {
		case c.Reason != "":
		case c.ExitCode == 0:
			o = verify.OutcomePass
		default:
			o = verify.OutcomeFail
		}
		out = append(out, verify.Result{Key: c.Key, Outcome: o, Reason: c.Reason})
	}
	return out
}

// verifierLayer dispatches the verifier role and turns its verdict into the final gate.
func (s *stageCtx) verifierLayer(ctx context.Context, run RunDoc, ev VerificationEvidenceDoc) (bool, error) {
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
	if err = a.attachContext(ctx, run, "verifier", &data); err != nil {
		return false, err
	}
	data.BaseSHA, data.HeadSHA, data.Summary = run.Heads.Base, run.Heads.Head, snap.Summary
	spec := roleSpec{Role: "verifier", Name: "verifier-M1", Stage: runstate.StageVerify, Round: run.Round, NeedRoute: true,
		Brief: func() (rolebriefs.Brief, error) { return rolebriefs.Render(rolebriefs.Verifier, data) }}
	parked, rex, err := s.roleAttempts(ctx, run, h, spec, s.climbOnce(ctx, run, h, ""))
	if err != nil || parked {
		return parked, err
	}
	reply, err := a.replyText(ctx, rex)
	if err != nil {
		return false, err
	}
	verdict, why := parseVerdict(reply)
	ev.Verifier = &EvidenceVerifier{RexID: rex.ID, Verdict: verdict, ArtifactID: rex.ReplyArtifactID}
	switch verdict {
	case "PASS":
		ev.Gate = EvidenceGate{Status: verify.GatePass}
	case "BLOCKED":
		ev.Gate = EvidenceGate{Status: verify.GateIncomplete, Reasons: []string{"verifier_blocked"}}
	default:
		ev.Verifier.Verdict = "FAIL"
		ev.Gate = EvidenceGate{Status: verify.GateFail, Reasons: []string{"verifier_fail"}}
		if why != "" {
			ev.Gate.Reasons = append(ev.Gate.Reasons, why)
		}
	}
	if err = s.saveEvidence(ctx, ev); err != nil {
		return false, err
	}
	switch verdict {
	case "PASS":
		err = a.fenced(ctx, s.lease, func(tx context.Context) error {
			set := bson.M{"heads.verified": run.Heads.Head, "verification": RunVerification{EvidenceID: ev.ID, Head: run.Heads.Head, Gate: verify.GatePass}}
			_, e := s.complete(tx, run, runstate.StageVerify, runstate.StagePublish, set, eventSpec{"verification.recorded", bson.M{"head": run.Heads.Head, "gate": verify.GatePass}})
			return e
		})
		return false, err
	case "BLOCKED":
		return false, s.stopFromError(ctx, run, stopSpec{Reason: "dependency_unavailable", QuestionCode: "verifier_blocked", Params: bson.M{"why": why}, Options: resolveOptions()})
	}
	return false, s.sendBack(ctx, run, "verifier_fail", ev)
}

// parseVerdict reads the first non-blank line of the verifier's reply.
func parseVerdict(reply string) (verdict, why string) {
	for _, l := range strings.Split(reply, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		switch {
		case l == "VERDICT: PASS" || strings.HasPrefix(l, "VERDICT: PASS "):
			return "PASS", ""
		case strings.HasPrefix(l, "VERDICT: BLOCKED: environment"):
			return "BLOCKED", strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(l, "VERDICT: BLOCKED: environment"), " —-–:"))
		case strings.HasPrefix(l, "VERDICT: FAIL"):
			return "FAIL", strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(l, "VERDICT: FAIL"), " —-–:"))
		}
		return "FAIL", "the verifier gave no verdict"
	}
	return "FAIL", "the verifier gave no verdict"
}

// sendBack starts the next fix round, or asks the user when the round cap is reached.
func (s *stageCtx) sendBack(ctx context.Context, run RunDoc, reason string, ev VerificationEvidenceDoc) error {
	a := s.a
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return err
	}
	capRounds := pv.Int("review.max_fix_rounds") + run.FixCapExtra
	if run.Round+1 > capRounds {
		opts := []InboxOption{{ID: "another_round", EffectCode: "raise_fix_cap"}, {ID: "cancel_run", EffectCode: "cancel_run"}}
		if reason == "review_blocking" {
			opts = []InboxOption{opts[0], {ID: "waive", RequiresText: true, EffectCode: "waive_findings"}, opts[1]}
		}
		return s.stopFromError(ctx, run, stopSpec{Reason: "review_disagreement", QuestionCode: "fix_rounds_exhausted", Params: bson.M{"reason": reason, "rounds": run.Round}, Options: opts})
	}
	return a.fenced(ctx, s.lease, func(tx context.Context) error {
		set := bson.M{"round": run.Round + 1, "verification": RunVerification{EvidenceID: ev.ID, Head: ev.HeadSHA, Gate: ev.Gate.Status}}
		if reason == "review_blocking" {
			delete(set, "verification")
		}
		_, e := s.complete(tx, run, run.Checkpoint.Stage, runstate.StageImplement, set, eventSpec{"verification.recorded", bson.M{"head": ev.HeadSHA, "gate": ev.Gate.Status}})
		return e
	})
}

var _ = fmt.Sprint
