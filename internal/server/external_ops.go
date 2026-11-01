package server

import (
	"context"
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"orqen/internal/extwrite"
	"orqen/internal/runstate"
	"path/filepath"
	"time"
)

// External operation states.
const (
	XopPlanned          = "planned"
	XopDryRun           = "dry_run"
	XopAwaitingApproval = "awaiting_approval"
	XopExecuting        = "executing"
	XopSucceeded        = "succeeded"
	XopFailed           = "failed"
	XopUnknown          = "outcome_unknown"
	XopReconciled       = "reconciled"
	XopSuperseded       = "superseded"
	XopCancelled        = "cancelled"
	XopRefused          = "refused"
)

// XopTarget names where an operation would write.
type XopTarget struct {
	Host        string `bson:"host" json:"host"`
	ProjectPath string `bson:"project_path" json:"project_path"`
	Branch      string `bson:"branch" json:"branch"`
	MRIID       int    `bson:"mr_iid" json:"mr_iid"`
}

// XopDecision is the gate's decision at a time.
type XopDecision struct {
	Outcome          string    `bson:"outcome" json:"outcome"`
	Reasons          []string  `bson:"reasons" json:"reasons"`
	DecidedAt        time.Time `bson:"decided_at" json:"decided_at"`
	SettingsRevision int64     `bson:"settings_revision" json:"settings_revision"`
	PolicyVersionID  string    `bson:"policy_version_id" json:"policy_version_id"`
	EnvScope         string    `bson:"env_scope" json:"env_scope"`
}

// XopApproval is a person's approval of one exact intent hash.
type XopApproval struct {
	IntentHash string    `bson:"intent_hash" json:"intent_hash"`
	At         time.Time `bson:"at" json:"at"`
	InboxID    string    `bson:"inbox_id" json:"inbox_id"`
	Source     string    `bson:"source" json:"source"` // dialog | inbox
}

// XopRemote is what the remote reported.
type XopRemote struct {
	MRIID        int    `bson:"mr_iid" json:"mr_iid"`
	NoteID       int    `bson:"note_id" json:"note_id"`
	DiscussionID string `bson:"discussion_id" json:"discussion_id"`
	PushedSHA    string `bson:"pushed_sha" json:"pushed_sha"`
	WebURL       string `bson:"web_url" json:"web_url"`
}

// XopError is why an operation failed.
type XopError struct {
	Code    string `bson:"code" json:"code"`
	Message string `bson:"message" json:"message"`
}

// ExternalOpDoc is one write Orqen wants to make (collection external_operations).
type ExternalOpDoc struct {
	ID                string       `bson:"_id"`
	OrgID             string       `bson:"org_id"`
	RunID             string       `bson:"run_id"`
	ProjectID         string       `bson:"project_id"`
	ConnectionID      string       `bson:"connection_id"`
	ProviderProjectID string       `bson:"provider_project_id"`
	Action            string       `bson:"action"`
	IntentKey         string       `bson:"intent_key"`
	IntentHash        string       `bson:"intent_hash"`
	Marker            string       `bson:"marker"`
	PayloadArtifactID string       `bson:"payload_artifact_id"`
	Payload           bson.M       `bson:"payload"`
	Head              string       `bson:"head"`
	Round             int          `bson:"round"`
	Target            XopTarget    `bson:"target"`
	TargetURL         string       `bson:"target_url"`
	Decision          *XopDecision `bson:"decision"`
	Approval          *XopApproval `bson:"approval"`
	State             string       `bson:"state"`
	Attempts          int          `bson:"attempts"`
	Remote            *XopRemote   `bson:"remote"`
	Error             *XopError    `bson:"error"`
	CreatedAt         time.Time    `bson:"created_at"`
	UpdatedAt         time.Time    `bson:"updated_at"`
	Revision          int64        `bson:"revision"`
}

// DTO is ExternalOperationDto.
func (d ExternalOpDoc) DTO() bson.M {
	var dec, appr, remote, errv any
	if x := d.Decision; x != nil {
		dec = bson.M{"outcome": x.Outcome, "reasons": nonNilStr(x.Reasons), "decided_at": timestamp(x.DecidedAt), "settings_revision": x.SettingsRevision, "policy_version_id": optStr(x.PolicyVersionID), "env_scope": x.EnvScope}
	}
	if x := d.Approval; x != nil {
		appr = bson.M{"intent_hash": x.IntentHash, "at": timestamp(x.At), "inbox_id": optStr(x.InboxID), "source": x.Source}
	}
	if x := d.Remote; x != nil {
		remote = bson.M{"mr_iid": x.MRIID, "note_id": x.NoteID, "discussion_id": optStr(x.DiscussionID), "pushed_sha": optStr(x.PushedSHA), "web_url": optStr(x.WebURL)}
	}
	if x := d.Error; x != nil {
		errv = bson.M{"code": x.Code, "message": x.Message}
	}
	return bson.M{"id": d.ID, "run_id": d.RunID, "action": d.Action, "intent_key": d.IntentKey, "intent_hash": d.IntentHash, "head": d.Head, "round": d.Round,
		"target":   bson.M{"host": d.Target.Host, "project_path": d.Target.ProjectPath, "branch": d.Target.Branch, "mr_iid": d.Target.MRIID},
		"decision": dec, "approval": appr, "state": d.State, "attempts": d.Attempts, "remote": remote, "error": errv, "created_at": timestamp(d.CreatedAt), "updated_at": timestamp(d.UpdatedAt)}
}

func (a *App) listRunExternalOperations(w http.ResponseWriter, r *http.Request) error {
	org, id := r.PathValue("orgId"), r.PathValue("runId")
	if _, err := a.run(r.Context(), org, id); err != nil {
		return err
	}
	docs, err := findMany[ExternalOpDoc](r.Context(), a.DB, "external_operations", bson.M{"org_id": org, "run_id": id}, bson.D{{Key: "created_at", Value: 1}, {Key: "_id", Value: 1}}, 500)
	if err != nil {
		return err
	}
	out := []bson.M{}
	for _, d := range docs {
		out = append(out, d.DTO())
	}
	writeJSON(w, 200, bson.M{"items": out, "next_cursor": nil})
	return nil
}

func (a *App) getExternalOperation(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	d, err := findOne[ExternalOpDoc](r.Context(), a.DB, "external_operations", bson.M{"_id": r.PathValue("xopId"), "org_id": org})
	if err != nil {
		return err
	}
	dto := d.DTO()
	var payload any = bson.M{}
	if d.PayloadArtifactID != "" {
		if art, e := findOne[ArtifactDoc](r.Context(), a.DB, "artifacts", bson.M{"_id": d.PayloadArtifactID, "org_id": org}); e == nil {
			if data, e2 := readBlob(filepath.Join(a.Config.DataDir, "artifacts"), org, art.Sha256); e2 == nil {
				var m any
				if json.Unmarshal(data, &m) == nil {
					payload = m
				}
			}
		}
	}
	dto["payload"] = payload
	dto["preview"] = previewOf(d, payload)
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, 200, dto)
	return nil
}

// previewOf is the rendered form of a payload: what a person reads before approving.
func previewOf(d ExternalOpDoc, payload any) bson.M {
	m, _ := payload.(map[string]any)
	str := func(k string) string { s, _ := m[k].(string); return s }
	commits := []any{}
	if c, ok := m["commits"].([]any); ok {
		commits = c
	}
	return bson.M{"title": str("title"), "body": firstNonEmptyStr(str("description"), str("body")), "diffstat": str("diffstat"), "commits": commits, "refspec": str("refspec"), "url": d.TargetURL}
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ApproveOpInput names an operation and the exact intent hash the person reviewed.
type ApproveOpInput struct {
	ID         string `json:"id"`
	IntentHash string `json:"intent_hash"`
}

// ApproveOperationsRequest is the body of POST /runs/{runId}/external-operations/approve.
type ApproveOperationsRequest struct {
	Operations []ApproveOpInput `json:"operations"`
}

// gateFor builds the gate with live inputs: settings, kill switch, write credential, policy and approvals.
func (a *App) gateFor() *extwrite.Gate {
	scope := a.guard().Scope
	return &extwrite.Gate{Scope: scope, Dialer: &extwrite.Dialer{Scope: scope},
		Credential: func(ctx context.Context, con string) (string, error) {
			return "", errors.New("credential is read per operation")
		},
		Load: nil}
}

// gateInputs loads what Decide needs for one operation.
func (a *App) gateInput(ctx context.Context, op ExternalOpDoc) (extwrite.DecideInput, error) {
	st, err := a.externalWriteSettings(ctx, op.OrgID)
	if err != nil {
		return extwrite.DecideInput{}, err
	}
	in := extwrite.DecideInput{Scope: a.guard().Scope, KillSwitch: a.guard().KillSwitch, HasCredential: a.hasWriteCredential(ctx, op.OrgID, op.ConnectionID)}
	in.Settings = extwrite.Settings{Mode: st.Mode, Paused: st.Paused}
	for _, e := range st.Allowlist {
		in.Settings.Allowlist = append(in.Settings.Allowlist, extwrite.AllowEntry{ConnectionID: e.ConnectionID, ProviderProjectID: e.ProviderProjectID})
	}
	run, err := a.run(ctx, op.OrgID, op.RunID)
	if err != nil {
		return in, err
	}
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return in, err
	}
	key := "automation.publish_draft_mr"
	if op.Action == string(extwrite.MRNote) || op.Action == string(extwrite.MRDiscussion) {
		key = "automation.publish_comments"
	}
	in.PolicyMode = pv.Str(key)
	cur, err := findOne[ExternalOpDoc](ctx, a.DB, "external_operations", bson.M{"_id": op.ID, "org_id": op.OrgID})
	if err == nil && cur.Approval != nil {
		in.Approval = &extwrite.Approval{IntentHash: cur.Approval.IntentHash}
	}
	return in, nil
}

func opView(op ExternalOpDoc) extwrite.OpView {
	return extwrite.OpView{OrgID: op.OrgID, RunID: op.RunID, Action: extwrite.Action(op.Action), ConnectionID: op.ConnectionID, ProviderProjectID: op.ProviderProjectID, TargetURL: op.TargetURL, IntentHash: op.IntentHash}
}

// decideOp runs the gate for an operation with a live loader (the same loader Execute uses right before the write).
func (a *App) decideOp(ctx context.Context, op ExternalOpDoc) (extwrite.Decision, *extwrite.Gate, error) {
	g := a.gateFor()
	g.Load = func(c context.Context, _ extwrite.OpView) (extwrite.DecideInput, error) { return a.gateInput(c, op) }
	g.Credential = func(c context.Context, con string) (string, error) { return a.writeSecret(c, op.OrgID, con) }
	d, err := g.Decide(ctx, opView(op))
	return d, g, err
}

func (a *App) approveOperations(w http.ResponseWriter, r *http.Request) error {
	var req ApproveOperationsRequest
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if len(req.Operations) == 0 || len(req.Operations) > 50 {
		return validation("operations", "must list 1 to 50 operations")
	}
	return a.runAction(w, r, "external-operations/approve", req, func(ctx context.Context, run RunDoc) (RunDoc, error) {
		if !run.Active || runstate.Terminal(run.State) {
			return run, problem("run_state_conflict")
		}
		st, err := a.externalWriteSettings(ctx, run.OrgID)
		if err != nil {
			return run, err
		}
		if st.Mode != extwrite.ModeLive {
			return run, problem("external_writes_not_live")
		}
		if st.Paused || a.guard().KillSwitch {
			return run, problem("external_writes_paused")
		}
		now := time.Now().UTC()
		hasComment, hasPublish := false, false
		for _, in := range req.Operations {
			op, err := findOne[ExternalOpDoc](ctx, a.DB, "external_operations", bson.M{"_id": in.ID, "org_id": run.OrgID, "run_id": run.ID})
			if errors.Is(err, mongo.ErrNoDocuments) {
				return run, problem("not_found")
			}
			if err != nil {
				return run, err
			}
			if (op.State != XopAwaitingApproval && op.State != XopDryRun) || op.Head != run.Heads.Head || op.IntentHash != in.IntentHash {
				e := problem("intent_changed")
				e.Current = op.DTO()
				return run, e
			}
			if err = a.checkApprovable(ctx, st, op); err != nil {
				return run, err
			}
			set := bson.M{"approval": XopApproval{IntentHash: in.IntentHash, At: now, Source: "dialog"}, "updated_at": now}
			if _, err = a.DB.Coll("external_operations").UpdateOne(ctx, bson.M{"_id": op.ID, "org_id": run.OrgID}, bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}}); err != nil {
				return run, err
			}
			if err = a.AppendEvent(ctx, run.OrgID, "extwrite.approved", Subject{"run", run.ID}, bson.M{"op_id": op.ID, "action": op.Action}, nil, nil, false); err != nil {
				return run, err
			}
			if op.Action == string(extwrite.MRNote) || op.Action == string(extwrite.MRDiscussion) {
				hasComment = true
			} else {
				hasPublish = true
			}
		}
		stage := runstate.StagePublish
		if hasComment && !hasPublish {
			stage = runstate.StageComment
		}
		if run.OpenInboxID != nil {
			if err = a.closeInbox(ctx, run.OrgID, *run.OpenInboxID, "resolved", nil); err != nil {
				return run, err
			}
		}
		set := bson.M{"hold": nil, "open_inbox_id": nil, "publish_mode": "live", "checkpoint": RunCheckpoint{Stage: stage, Since: now}, "state": runstate.StateOfRound(stage, run.Round)}
		next, err := a.casRun(ctx, run, "", set, runEvent{Reason: "publish_approved", Actor: runstate.ActorUser, Extra: []eventSpec{{"run.resumed", bson.M{"reason": "publish_approved"}}}})
		if err != nil {
			return run, err
		}
		return next, a.enqueueRunAdvance(ctx, run.OrgID, run.ID)
	})
}

// checkApprovable names the first reason an approval cannot lead to a write.
func (a *App) checkApprovable(ctx context.Context, st ExternalWriteSettingsDoc, op ExternalOpDoc) error {
	listed := false
	for _, e := range st.Allowlist {
		listed = listed || e.ConnectionID == op.ConnectionID && e.ProviderProjectID == op.ProviderProjectID
	}
	if !listed {
		return problem("write_target_not_allowlisted")
	}
	if err := extwrite.CheckURL(a.guard().Scope, op.TargetURL, op.Action == string(extwrite.GitPush)); err != nil {
		return problem("env_guard_refused")
	}
	if !a.hasWriteCredential(ctx, op.OrgID, op.ConnectionID) {
		return problem("write_credential_missing")
	}
	return nil
}
