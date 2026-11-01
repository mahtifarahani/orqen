package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/url"
	"orqen/internal/extwrite"
	"orqen/internal/runstate"
	"orqen/internal/workspace"
	"strings"
	"time"
)

// publishEnv is what every operation of a run needs to be built and executed.
type publishEnv struct {
	proj     ProjectDoc
	conn     ConnectionDoc
	primary  RepoMapping
	repoURL  string
	repoPath string
	branch   string
	wt       string
	summary  string
	snap     IssueSnapshotDoc
	wm       *workspace.Manager
}

// opSpec is an operation before it is stored.
type opSpec struct {
	Action    extwrite.Action
	IntentKey string
	Payload   map[string]any
	Target    XopTarget
	TargetURL string
	MRIID     int
}

func (s *stageCtx) publishEnv(ctx context.Context, run RunDoc) (publishEnv, error) {
	a := s.a
	var pe publishEnv
	var err error
	if pe.proj, err = a.project(ctx, run.OrgID, run.ProjectID); err != nil {
		return pe, err
	}
	p := pe.proj.Mapping.primary()
	if p == nil {
		return pe, &stageStop{Spec: stopSpec{Reason: "project_mapping", QuestionCode: "primary_repo_missing", Options: resolveOptions()}}
	}
	pe.primary = *p
	if pe.conn, err = a.connection(ctx, run.OrgID, p.ConnectionID); err != nil {
		return pe, err
	}
	host, err := a.codeHost(ctx, pe.conn)
	if err != nil {
		return pe, err
	}
	gp, err := host.GetProject(ctx, p.ProviderProjectID)
	if err != nil {
		a.noteRateLimitErr(pe.conn.ID, err)
		return pe, err
	}
	pe.repoURL, pe.repoPath = gp.HTTPURLToRepo, gp.PathWithNamespace
	ws, _, err := a.workspaceDoc(ctx, run.OrgID, run.ID)
	if err != nil {
		return pe, err
	}
	pe.branch = ws.Branch
	if pe.wt, err = a.worktreePath(ctx, run); err != nil {
		return pe, err
	}
	if pe.snap, err = findOne[IssueSnapshotDoc](ctx, a.DB, "issue_snapshots", bson.M{"_id": run.SnapshotID, "org_id": run.OrgID}); err != nil {
		return pe, err
	}
	pe.summary = strings.Join(strings.Fields(pe.snap.Summary), " ")
	if len(pe.summary) > 200 {
		pe.summary = pe.summary[:200]
	}
	pe.wm, err = a.workspaceManager(run.OrgID, &pe.conn, 0)
	return pe, err
}

func (pe publishEnv) git(ctx context.Context, args ...string) (string, error) {
	res, err := pe.wm.Git.Run(ctx, pe.wt, workspace.Creds{}, 0, args...)
	return res.Stdout, err
}

func hostOfURL(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return ""
}

// mrDescription is the merge request description: ticket, plan goal, evidence table and the marker. It holds no
// local path and is scrubbed.
func (s *stageCtx) mrDescription(ctx context.Context, run RunDoc, pe publishEnv, marker string) (string, error) {
	a := s.a
	var b strings.Builder
	jiraLink := ""
	if j := pe.proj.Mapping.Jira; j != nil {
		if jc, err := a.connection(ctx, run.OrgID, j.ConnectionID); err == nil {
			jiraLink = strings.TrimRight(jc.BaseURL, "/") + "/browse/" + run.IssueKey
		}
	}
	fmt.Fprintf(&b, "## %s\n\n", run.IssueKey)
	if jiraLink != "" {
		fmt.Fprintf(&b, "Ticket: [%s](%s)\n\n", run.IssueKey, jiraLink)
	}
	goal := pe.summary
	if run.Plan != nil && run.Plan.ArtifactID != "" {
		if art, err := findOne[ArtifactDoc](ctx, a.DB, "artifacts", bson.M{"_id": run.Plan.ArtifactID, "org_id": run.OrgID}); err == nil {
			if data, e := readBlobAt(a, run.OrgID, art.Sha256); e == nil {
				for _, l := range strings.Split(string(data), "\n") {
					if g, ok := strings.CutPrefix(strings.TrimSpace(l), "Goal:"); ok && strings.TrimSpace(g) != "" {
						goal = strings.TrimSpace(g)
					}
				}
			}
		}
	}
	fmt.Fprintf(&b, "%s\n\n### Verification (run by Orqen)\n\n| Command | Outcome | Exit | Duration | Output | Verified head |\n|---|---|---|---|---|---|\n", goal)
	if run.Verification != nil {
		if ev, err := findOne[VerificationEvidenceDoc](ctx, a.DB, "verification_evidence", bson.M{"_id": run.Verification.EvidenceID, "org_id": run.OrgID}); err == nil {
			for _, c := range ev.Commands {
				sha := c.OutputSHA256
				if len(sha) > 12 {
					sha = sha[:12]
				}
				fmt.Fprintf(&b, "| %s | %s | %d | %d ms | %s | %s |\n", c.Key, c.Outcome, c.ExitCode, c.DurationMs, sha, short(run.Heads.Head))
			}
		}
	}
	fmt.Fprintf(&b, "\nReview: an independent reviewer is run by Orqen; findings arrive as comments. Merging is yours.\n\n%s\n", marker)
	return string(scrubber(a.knownSecrets(ctx, run.OrgID))([]byte(b.String()))), nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func readBlobAt(a *App, org, digest string) ([]byte, error) {
	return readBlob(a.Config.DataDir+"/artifacts", org, digest)
}

func (s *stageCtx) buildSpecs(ctx context.Context, run RunDoc, pe publishEnv, mr MergeRequestDoc, haveMR bool) ([]opSpec, error) {
	head := run.Heads.Head
	lease := run.Heads.Pushed
	log, err := pe.git(ctx, "log", "--format=%H%x09%s", run.Heads.Base+".."+head)
	if err != nil {
		return nil, err
	}
	commits := []any{}
	for _, l := range strings.Split(strings.TrimSpace(log), "\n") {
		if sha, subj, ok := strings.Cut(l, "\t"); ok {
			commits = append(commits, map[string]any{"sha": sha, "subject": subj})
		}
	}
	stat, _ := pe.git(ctx, "diff", "--stat", run.Heads.Base, head)
	target := XopTarget{Host: hostOfURL(pe.repoURL), ProjectPath: pe.repoPath, Branch: pe.branch}
	push := opSpec{Action: extwrite.GitPush, IntentKey: "push:" + pe.branch + ":" + head, Target: target, TargetURL: pe.repoURL, Payload: map[string]any{
		"action": "git_push", "host": target.Host, "project_path": pe.repoPath, "branch": pe.branch, "sha": head, "lease_sha": lease, "refspec": head + ":refs/heads/" + pe.branch, "commits": commits, "diffstat": strings.TrimSpace(stat)}}
	specs := []opSpec{push}
	if lease == head {
		specs = nil // the head is already on the remote (an adopted foreign push): only the description is updated
	}
	if haveMR && mr.Mode == "live" && mr.IID > 0 {
		key := "mr_update:" + run.ID + ":" + head
		desc, err := s.mrDescription(ctx, run, pe, extwrite.Marker(run.ID, "mr_create:"+run.ID))
		if err != nil {
			return nil, err
		}
		t := target
		t.MRIID = mr.IID
		specs = append(specs, opSpec{Action: extwrite.MRUpdate, IntentKey: key, Target: t, TargetURL: pe.conn.BaseURL, MRIID: mr.IID, Payload: map[string]any{"action": "mr_update", "project_id": pe.primary.ProviderProjectID, "iid": mr.IID, "description": desc}})
	} else {
		marker := extwrite.Marker(run.ID, "mr_create:"+run.ID)
		desc, err := s.mrDescription(ctx, run, pe, marker)
		if err != nil {
			return nil, err
		}
		specs = append(specs, opSpec{Action: extwrite.MRCreate, IntentKey: "mr_create:" + run.ID, Target: target, TargetURL: pe.conn.BaseURL, Payload: map[string]any{
			"action": "mr_create", "project_id": pe.primary.ProviderProjectID, "title": "Draft: " + run.IssueKey + ": " + pe.summary, "source_branch": pe.branch, "target_branch": pe.primary.TargetBranch,
			"description": desc, "remove_source_branch": false, "draft": true}})
	}
	return specs, nil
}

// upsertOp stores an operation with its exact payload (an artifact first), keyed by its intent key.
func (s *stageCtx) upsertOp(ctx context.Context, run RunDoc, pe publishEnv, sp opSpec) (ExternalOpDoc, error) {
	a := s.a
	cur, err := findOne[ExternalOpDoc](ctx, a.DB, "external_operations", bson.M{"org_id": run.OrgID, "intent_key": sp.IntentKey})
	if err == nil {
		return cur, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return cur, err
	}
	data, _ := json.Marshal(sp.Payload)
	art, err := s.storeText(ctx, run.OrgID, data, "application/json", "extwrite_payload", Subject{"run", run.ID})
	if err != nil {
		return cur, err
	}
	now := time.Now().UTC()
	op := ExternalOpDoc{ID: newID("xop"), OrgID: run.OrgID, RunID: run.ID, ProjectID: run.ProjectID, ConnectionID: pe.conn.ID, ProviderProjectID: pe.primary.ProviderProjectID, Action: string(sp.Action), IntentKey: sp.IntentKey,
		IntentHash: extwrite.IntentHash(sp.Payload), Marker: extwrite.Marker(run.ID, sp.IntentKey), PayloadArtifactID: art.ID, Payload: bson.M(sp.Payload), Head: run.Heads.Head, Round: run.Round, Target: sp.Target, TargetURL: sp.TargetURL,
		State: XopPlanned, CreatedAt: now, UpdatedAt: now, Revision: 1}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		_, e := a.DB.Coll("external_operations").InsertOne(tx, op)
		return e
	})
	if mongo.IsDuplicateKeyError(err) {
		return findOne[ExternalOpDoc](ctx, a.DB, "external_operations", bson.M{"org_id": run.OrgID, "intent_key": sp.IntentKey})
	}
	return op, err
}

func (s *stageCtx) setOp(ctx context.Context, op ExternalOpDoc, set bson.M, events ...eventSpec) (ExternalOpDoc, error) {
	a := s.a
	set["updated_at"] = time.Now().UTC()
	var out ExternalOpDoc
	err := a.fenced(ctx, s.lease, func(tx context.Context) error {
		if e := a.DB.Coll("external_operations").FindOneAndUpdate(tx, bson.M{"_id": op.ID, "org_id": op.OrgID}, bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&out); e != nil {
			return e
		}
		for _, ev := range events {
			ev.Payload["op_id"], ev.Payload["action"] = op.ID, op.Action
			if e := a.AppendEvent(tx, op.OrgID, ev.Type, Subject{"run", op.RunID}, ev.Payload, nil, nil, true); e != nil {
				return e
			}
		}
		return nil
	})
	return out, err
}

// ensureMR creates or updates the run's merge request document.
func (s *stageCtx) ensureMR(ctx context.Context, run RunDoc, pe publishEnv, set bson.M) (MergeRequestDoc, error) {
	a := s.a
	now := time.Now().UTC()
	var out MergeRequestDoc
	err := a.fenced(ctx, s.lease, func(tx context.Context) error {
		set["updated_at"] = now
		e := a.DB.Coll("merge_requests").FindOneAndUpdate(tx, bson.M{"org_id": run.OrgID, "run_id": run.ID, "active": true}, bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&out)
		if errors.Is(e, mongo.ErrNoDocuments) {
			out = MergeRequestDoc{ID: newID("mrq"), OrgID: run.OrgID, RunID: run.ID, ProjectID: run.ProjectID, ConnectionID: pe.conn.ID, ProviderProjectID: pe.primary.ProviderProjectID, Mode: "dry_run", Title: "Draft: " + run.IssueKey + ": " + pe.summary,
				SourceBranch: pe.branch, TargetBranch: pe.primary.TargetBranch, Draft: true, State: "opened", CurrentHead: run.Heads.Head, DiscussionsResolved: true, Active: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
			applyMRSet(&out, set)
			if _, e = a.DB.Coll("merge_requests").InsertOne(tx, out); e != nil {
				return e
			}
			if e = a.AppendEvent(tx, run.OrgID, "mr.created", Subject{"run", run.ID}, bson.M{"run_id": run.ID, "mode": out.Mode, "iid": out.IID}, nil, nil, true); e != nil {
				return e
			}
			_, e = a.DB.Coll("runs").UpdateOne(tx, bson.M{"_id": run.ID, "org_id": run.OrgID}, bson.M{"$set": bson.M{"merge_request_id": out.ID}})
			return e
		}
		return e
	})
	return out, err
}

func applyMRSet(d *MergeRequestDoc, set bson.M) {
	for k, v := range set {
		switch k {
		case "mode":
			d.Mode, _ = v.(string)
		case "iid":
			d.IID, _ = v.(int)
		case "web_url":
			d.WebURL, _ = v.(string)
		case "current_head":
			d.CurrentHead, _ = v.(string)
		case "last_pushed_sha":
			d.LastPushedSHA, _ = v.(string)
		case "verified_head":
			d.VerifiedHead, _ = v.(string)
		}
	}
}

// stagePublish plans the push and the merge request call, decides all of them through the write gate and either
// simulates (dry-run), asks for approval, or executes.
func (s *stageCtx) stagePublish(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	if run.Heads.Head == "" || run.Heads.Verified != run.Heads.Head {
		return false, &runFailureError{Code: "unverified_head", Message: "only a verified head is published"}
	}
	pe, err := s.publishEnv(ctx, run)
	if err != nil {
		return false, err
	}
	// operations planned for an older head can never run now
	if _, err = a.DB.Coll("external_operations").UpdateMany(ctx, bson.M{"org_id": run.OrgID, "run_id": run.ID, "head": bson.M{"$ne": run.Heads.Head}, "state": bson.M{"$in": []string{XopPlanned, XopDryRun, XopAwaitingApproval}}}, bson.M{"$set": bson.M{"state": XopSuperseded, "updated_at": time.Now().UTC()}}); err != nil {
		return false, err
	}
	mr, haveMR, err := a.mrOfRun(ctx, run.OrgID, run.ID)
	if err != nil {
		return false, err
	}
	specs, err := s.buildSpecs(ctx, run, pe, mr, haveMR)
	if err != nil {
		return false, err
	}
	allDry, stop, err := s.runOps(ctx, run, pe, specs)
	if err != nil || stop {
		return false, err
	}
	if allDry {
		return s.simulate(ctx, run, pe)
	}
	return s.finishPublish(ctx, run, pe)
}

// runOps stores, decides and (when the gate says so) executes a set of operations. It returns allDry when every
// operation was only simulated, and stop when the stage must end its pass (a question was opened).
func (s *stageCtx) runOps(ctx context.Context, run RunDoc, pe publishEnv, specs []opSpec) (allDry, stop bool, err error) {
	a := s.a
	var ops []ExternalOpDoc
	for _, sp := range specs {
		op, e := s.upsertOp(ctx, run, pe, sp)
		if e != nil {
			return false, false, e
		}
		if op.State == XopFailed && op.Error != nil && op.Error.Code == "credential_rejected" {
			if op, e = s.setOp(ctx, op, bson.M{"state": XopPlanned, "error": nil}); e != nil {
				return false, false, e
			}
		}
		ops = append(ops, op)
	}
	// decide every operation first, so a person approves the whole set at once
	decisions := make([]extwrite.Outcome, len(ops))
	for i := range ops {
		if ops[i].State == XopSucceeded || ops[i].State == XopReconciled {
			decisions[i] = extwrite.OutcomeExecute
			continue
		}
		d, _, e := a.decideOp(ctx, ops[i])
		if e != nil {
			return false, false, e
		}
		decisions[i] = d.Outcome()
		dec := &XopDecision{Outcome: string(d.Outcome()), Reasons: d.Reasons(), DecidedAt: time.Now().UTC(), EnvScope: string(d.Scope())}
		set := bson.M{"decision": dec}
		switch d.Outcome() {
		case extwrite.OutcomeDryRun:
			set["state"] = XopDryRun
		case extwrite.OutcomeAwaitingApproval:
			set["state"] = XopAwaitingApproval
		case extwrite.OutcomeRefused, extwrite.OutcomeRefusedEnv:
			set["state"] = XopRefused
		}
		if ops[i].State == XopExecuting || ops[i].State == XopUnknown {
			delete(set, "state") // a recovery: the executor looks at what happened first
		}
		if ops[i], e = s.setOp(ctx, ops[i], set, eventSpec{"extwrite.decided", bson.M{"outcome": string(d.Outcome()), "reasons": toAny(d.Reasons())}}); e != nil {
			return false, false, e
		}
	}
	for i, d := range decisions {
		if d == extwrite.OutcomeRefused || d == extwrite.OutcomeRefusedEnv {
			return false, true, s.stopFromError(ctx, run, stopSpec{Reason: "policy_conflict", QuestionCode: "env_guard_refused", Params: bson.M{"action": ops[i].Action, "host": ops[i].Target.Host}, Options: []InboxOption{{ID: "cancel_run", EffectCode: "cancel_run"}, {ID: "reject", EffectCode: "hold_dry_run"}}})
		}
	}
	var pending []bson.A
	for i, d := range decisions {
		if d == extwrite.OutcomeAwaitingApproval {
			pending = append(pending, bson.A{ops[i].ID, ops[i].IntentHash})
		}
	}
	if len(pending) > 0 {
		list := bson.A{}
		for _, p := range pending {
			list = append(list, bson.M{"id": p[0], "intent_hash": p[1]})
		}
		return false, true, &stageStop{Spec: stopSpec{Kind: "approval", Reason: "publish_approval", QuestionCode: "publish_approval", Params: bson.M{"operations": list}, Options: []InboxOption{{ID: "approve_all", EffectCode: "use_approval_dialog"}, {ID: "reject", EffectCode: "hold_dry_run"}, {ID: "cancel_run", EffectCode: "cancel_run"}}}}
	}
	allDry = true
	for _, d := range decisions {
		allDry = allDry && d == extwrite.OutcomeDryRun
	}
	if allDry {
		return true, false, nil
	}
	for i := range ops {
		if ops[i].State == XopSucceeded || ops[i].State == XopReconciled {
			continue
		}
		halt, e := s.executeOp(ctx, run, pe, ops[i])
		if e != nil || halt {
			return false, true, e
		}
	}
	return false, false, nil
}

// simulate is the dry-run result: the merge request exists only in Orqen, the run goes on to the review.
func (s *stageCtx) simulate(ctx context.Context, run RunDoc, pe publishEnv) (bool, error) {
	a := s.a
	if _, err := s.ensureMR(ctx, run, pe, bson.M{"mode": "dry_run", "current_head": run.Heads.Head, "verified_head": run.Heads.Verified}); err != nil {
		return false, err
	}
	err := a.fenced(ctx, s.lease, func(tx context.Context) error {
		_, e := s.complete(tx, run, runstate.StagePublish, runstate.StageReview, bson.M{"publish_mode": "dry_run"})
		return e
	})
	return false, err
}

func (s *stageCtx) finishPublish(ctx context.Context, run RunDoc, pe publishEnv) (bool, error) {
	a := s.a
	if err := a.hook(runstate.StagePublish); err != nil {
		return false, err
	}
	err := a.fenced(ctx, s.lease, func(tx context.Context) error {
		_, e := s.complete(tx, run, runstate.StagePublish, runstate.StageReview, bson.M{"publish_mode": "live", "heads.pushed": run.Heads.Head})
		return e
	})
	return false, err
}
