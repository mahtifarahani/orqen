package server

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"orqen/internal/runstate"
	"strings"
	"time"
	"unicode/utf8"
)

type InboxEvidence struct {
	SourceID string `bson:"source_id" json:"source_id"`
	Kind     string `bson:"kind" json:"kind"`
	Label    string `bson:"label" json:"label"`
	Excerpt  string `bson:"excerpt" json:"excerpt"`
	URL      string `bson:"url" json:"url"`
}
type InboxOption struct {
	ID           string `bson:"id" json:"id"`
	RequiresText bool   `bson:"requires_text" json:"requires_text"`
	EffectCode   string `bson:"effect_code" json:"effect_code"`
}
type InboxAnswer struct {
	N      int       `bson:"n" json:"n"`
	Choice string    `bson:"choice" json:"choice"`
	Text   *string   `bson:"text" json:"text"`
	At     time.Time `bson:"at" json:"at"`
}

// InboxItemDoc is a question or approval waiting for the user. At most one is open per run.
type InboxItemDoc struct {
	ID              string          `bson:"_id"`
	OrgID           string          `bson:"org_id"`
	RunID           string          `bson:"run_id"`
	ProjectID       string          `bson:"project_id"`
	TrackedIssueID  string          `bson:"tracked_issue_id"`
	IssueKey        string          `bson:"issue_key"`
	Kind            string          `bson:"kind"` // question | approval
	Reason          string          `bson:"reason"`
	QuestionCode    string          `bson:"question_code"`
	Params          bson.M          `bson:"params"`
	Stage           string          `bson:"stage"`
	Priority        string          `bson:"priority"`
	Evidence        []InboxEvidence `bson:"evidence"`
	Options         []InboxOption   `bson:"options"`
	Answers         []InboxAnswer   `bson:"answers"`
	Status          string          `bson:"status"` // open | resolved | superseded | cancelled
	Resolution      string          `bson:"resolution"`
	SupersededByRun *string         `bson:"superseded_by_run"`
	SortKey         string          `bson:"sort_key"`
	Revision        int64           `bson:"revision"`
	CreatedAt       time.Time       `bson:"created_at"`
	UpdatedAt       time.Time       `bson:"updated_at"`
}

func inboxRank(priority string) int {
	switch priority {
	case "high":
		return 0
	case "normal":
		return 1
	}
	return 2
}

func (d InboxItemDoc) DTO() bson.M {
	ev, opts, ans := d.Evidence, d.Options, d.Answers
	if ev == nil {
		ev = []InboxEvidence{}
	}
	if opts == nil {
		opts = []InboxOption{}
	}
	if ans == nil {
		ans = []InboxAnswer{}
	}
	params := d.Params
	if params == nil {
		params = bson.M{}
	}
	return bson.M{"id": d.ID, "run_id": d.RunID, "project_id": d.ProjectID, "task_id": d.TrackedIssueID, "issue_key": d.IssueKey, "kind": d.Kind, "reason": d.Reason, "question_code": d.QuestionCode, "params": params, "stage": d.Stage,
		"priority": d.Priority, "evidence": ev, "options": opts, "answers": ans, "status": d.Status, "resolution": d.Resolution, "superseded_by_run": d.SupersededByRun, "created_at": timestamp(d.CreatedAt), "updated_at": timestamp(d.UpdatedAt), "revision": d.Revision}
}

type inboxSpec struct {
	Kind, Reason, QuestionCode string
	Params                     bson.M
	Evidence                   []InboxEvidence
	Options                    []InboxOption
}

// openInbox inserts the run's single open Inbox item. The caller moves the run to needs_input in the same transaction.
func (a *App) openInbox(sc context.Context, run RunDoc, spec inboxSpec) (InboxItemDoc, error) {
	now := time.Now().UTC()
	priority := run.Priority
	if spec.Reason == "credential_expired" {
		priority = "high"
	}
	if priority == "" {
		priority = "normal"
	}
	if spec.Kind == "" {
		spec.Kind = "question"
	}
	id := newID("inb")
	it := InboxItemDoc{ID: id, OrgID: run.OrgID, RunID: run.ID, ProjectID: run.ProjectID, TrackedIssueID: run.TrackedIssueID, IssueKey: run.IssueKey, Kind: spec.Kind, Reason: spec.Reason, QuestionCode: spec.QuestionCode, Params: spec.Params,
		Stage: run.Checkpoint.Stage, Priority: priority, Evidence: spec.Evidence, Options: spec.Options, Answers: []InboxAnswer{}, Status: "open", SortKey: string(rune('0'+inboxRank(priority))) + ":" + id, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := a.DB.Coll("inbox_items").InsertOne(sc, it); err != nil {
		return InboxItemDoc{}, err
	}
	if err := a.AppendEvent(sc, run.OrgID, "inbox.opened", Subject{"inbox", id}, bson.M{"run_id": run.ID, "reason": spec.Reason}, &it.Revision, nil, true); err != nil {
		return InboxItemDoc{}, err
	}
	return it, nil
}

// optionsFor lists the choices of a question. Each has an effect code the UI explains.
func optionsFor(reason, code string) []InboxOption {
	cancel := InboxOption{ID: "cancel", EffectCode: "cancel_run"}
	switch reason {
	case "missing_acceptance":
		return []InboxOption{{ID: "provide_acceptance", RequiresText: true, EffectCode: "add_acceptance"}, cancel}
	case "ambiguous_behavior":
		if code == "already_addressed_confirm" {
			return []InboxOption{{ID: "close", EffectCode: "close_run"}, {ID: "continue", EffectCode: "continue_anyway"}}
		}
		return []InboxOption{{ID: "clarify", RequiresText: true, EffectCode: "add_clarification"}, cancel}
	case "protected_change":
		return []InboxOption{{ID: "proceed_without_protected", EffectCode: "proceed_without_protected"}, cancel}
	case "policy_conflict":
		return []InboxOption{{ID: "raise_limit", EffectCode: "raise_context_limit"}, cancel}
	case "plan_approval":
		return []InboxOption{{ID: "approve", EffectCode: "approve_plan"}, {ID: "request_changes", RequiresText: true, EffectCode: "request_plan_changes"}, cancel}
	}
	return resolveOptions()
}

type AnswerInboxRequest struct {
	Choice string  `json:"choice"`
	Text   *string `json:"text"`
}
type ResolveInboxRequest struct {
	Action string `json:"action"`
}

func (a *App) inboxItem(ctx context.Context, org, id string) (InboxItemDoc, error) {
	return findOne[InboxItemDoc](ctx, a.DB, "inbox_items", bson.M{"_id": id, "org_id": org})
}

func (a *App) listInbox(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	limit, after, err := pageQueryKey(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	filter := bson.M{"org_id": org, "status": "open"}
	if v := q.Get("status"); v != "" {
		if !contains([]string{"open", "resolved", "superseded", "cancelled"}, v) {
			return validation("status", "must be open, resolved, superseded or cancelled")
		}
		filter["status"] = v
	}
	if v := q.Get("project_id"); v != "" {
		if !validID("prj", v) {
			return validation("project_id", "is not a project id")
		}
		filter["project_id"] = v
	}
	if v := q.Get("priority"); v != "" {
		if !contains([]string{"high", "normal", "low"}, v) {
			return validation("priority", "must be high, normal or low")
		}
		filter["priority"] = v
	}
	if v := q.Get("reason"); v != "" {
		filter["reason"] = v
	}
	return listAs(w, r, a, filter, limit, after)
}
func listAs(w http.ResponseWriter, r *http.Request, a *App, filter bson.M, limit int64, after string) error {
	docs, next, err := findPageBy(r.Context(), a.DB, "inbox_items", filter, "sort_key", func(d InboxItemDoc) string { return d.SortKey }, limit, after)
	if err != nil {
		return err
	}
	items := []bson.M{}
	for _, d := range docs {
		items = append(items, d.DTO())
	}
	writeJSON(w, 200, bson.M{"items": items, "next_cursor": next})
	return nil
}
func (a *App) getInboxItem(w http.ResponseWriter, r *http.Request) error {
	it, err := a.inboxItem(r.Context(), r.PathValue("orgId"), r.PathValue("inbId"))
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(it.Revision))
	writeJSON(w, 200, it.DTO())
	return nil
}

// resolveOnlyReasons need no answer text: the user fixes something elsewhere and resumes.
var resolveOnlyReasons = []string{"credential_expired", "project_mapping", "dependency_unavailable", "unsupported_toolchain", "policy_conflict"}

func validAnswerText(text *string, required bool) *AppError {
	if text == nil {
		if required {
			return answerInvalid("text", "this choice needs a text")
		}
		return nil
	}
	t := strings.TrimSpace(*text)
	if required && t == "" {
		return answerInvalid("text", "this choice needs a text")
	}
	if utf8.RuneCountInString(t) > 4000 || !utf8.ValidString(t) {
		return answerInvalid("text", "must be at most 4000 characters")
	}
	for _, c := range t {
		if c < 0x20 && c != '\n' && c != '\t' && c != '\r' || c == 0x7f {
			return answerInvalid("text", "must not contain control characters")
		}
	}
	return nil
}
func answerInvalid(field, msg string) *AppError {
	e := problem("answer_invalid")
	e.Errors = []FieldError{{field, msg}}
	return e
}

// answerEffect is what a choice does to the run.
type answerEffect struct {
	Cancel       bool
	CancelReason string
	Stage        string // the stage to resume at
	Source       *RunSource
	Set          bson.M
	Event        string
}

// effectOf maps (reason, choice) to the run change. text is the trimmed answer text.
func (a *App) effectOf(ctx context.Context, it InboxItemDoc, run RunDoc, choice, text string) (answerEffect, error) {
	src := func(kind, code, label string) *RunSource {
		t := text
		return &RunSource{Kind: kind, Code: code, Label: label, Ref: it.ID, Text: &t, InboxID: &it.ID}
	}
	if it.QuestionCode == "architect_plan_approval" {
		switch choice {
		case "approve":
			return answerEffect{Stage: runstate.StageArchitectGate, Set: bson.M{"plan.approved": true, "plan.approved_at": time.Now().UTC()}}, nil
		case "request_changes":
			return answerEffect{Stage: runstate.StageArchitect, Source: src("user_answer", "plan_changes", "plan changes"), Set: bson.M{"plan_rounds": run.PlanRounds + 1, "plan.approved": false}}, nil
		}
	}
	switch choice {
	case "retry_dispatch":
		if _, err := a.DB.Coll("role_executions").UpdateMany(ctx, bson.M{"org_id": run.OrgID, "run_id": run.ID, "state": RexAmbiguous}, bson.M{"$set": bson.M{"state": RexDispatching, "retry_confirmed": true, "updated_at": time.Now().UTC()}}); err != nil {
			return answerEffect{}, err
		}
		// A role that failed is replaced by a fresh dispatch.
		if _, err := a.DB.Coll("role_executions").UpdateMany(ctx, bson.M{"org_id": run.OrgID, "run_id": run.ID, "state": RexCollected, "climbed": bson.M{"$ne": true}, "catherd.status": bson.M{"$ne": "ok"}}, bson.M{"$set": bson.M{"climbed": true}}); err != nil {
			return answerEffect{}, err
		}
		return answerEffect{Stage: run.Checkpoint.Stage, Set: bson.M{"host_faults": 0}}, nil
	case "retry_review":
		if _, err := a.DB.Coll("role_executions").UpdateMany(ctx, bson.M{"org_id": run.OrgID, "run_id": run.ID, "role": "reviewer", "round": run.Round, "climbed": bson.M{"$ne": true}}, bson.M{"$set": bson.M{"climbed": true}}); err != nil {
			return answerEffect{}, err
		}
		return answerEffect{Stage: runstate.StageReview}, nil
	case "waive":
		if _, err := a.DB.Coll("review_findings").UpdateMany(ctx, bson.M{"org_id": run.OrgID, "run_id": run.ID, "status": FindingOpen, "blocking": true}, bson.M{"$set": bson.M{"status": FindingWaived, "waiver": FindingWaiver{Text: text, At: time.Now().UTC()}}}); err != nil {
			return answerEffect{}, err
		}
		return answerEffect{Stage: runstate.StageLand, Set: bson.M{"review.blocking_open": 0}, Event: "finding.waived"}, nil
	case "adopt_remote":
		return answerEffect{Stage: runstate.StageCI, Set: bson.M{"adopt_remote": true}}, nil
	case "approve_all":
		return answerEffect{}, answerInvalid("choice", "use the approval dialog on the task, which shows the exact payload")
	case "reject":
		if _, err := a.DB.Coll("external_operations").UpdateMany(ctx, bson.M{"org_id": run.OrgID, "run_id": run.ID, "state": XopAwaitingApproval}, bson.M{"$set": bson.M{"state": XopDryRun, "updated_at": time.Now().UTC()}}); err != nil {
			return answerEffect{}, err
		}
		return answerEffect{Stage: runstate.StageReview, Set: bson.M{"publish_mode": "dry_run"}}, nil
	case "retry_write":
		return answerEffect{Stage: run.Checkpoint.Stage}, nil
	case "accept":
		if run.Heads.Head == "" {
			return answerEffect{}, answerInvalid("choice", "there is nothing to accept")
		}
		return answerEffect{Stage: runstate.StageVerify, Set: bson.M{"waiver": Waiver{Head: run.Heads.Head, Text: text, At: time.Now().UTC()}}, Event: "verification.waived"}, nil
	case "another_round":
		return answerEffect{Stage: run.Checkpoint.Stage, Set: bson.M{"fix_cap_extra": run.FixCapExtra + 1}}, nil
	case "send_back":
		notes := []string{"Protected paths were changed or the checks were weakened; restore them and keep the change within your lane."}
		return answerEffect{Stage: runstate.StageImplement, Set: bson.M{"round": run.Round + 1, "fix_notes": notes}}, nil
	case "cancel", "cancel_run":
		return answerEffect{Cancel: true, CancelReason: "cancelled"}, nil
	case "close":
		return answerEffect{Cancel: true, CancelReason: "already_addressed"}, nil
	case "provide_acceptance":
		return answerEffect{Stage: runstate.StageContext, Source: src("user_answer", "missing_acceptance", "acceptance")}, nil
	case "clarify":
		return answerEffect{Stage: runstate.StageContext, Source: src("user_answer", "clarification", "clarification")}, nil
	case "continue":
		text = "Continue although the requested work may already exist; check what is missing."
		return answerEffect{Stage: runstate.StagePlan, Source: src("constraint", "already_done_ack", "continue anyway"), Set: bson.M{"triage.outcome": "execute", "triage.source": "user"}}, nil
	case "proceed_without_protected":
		paths := ""
		if raw, ok := it.Params["paths"]; ok {
			if arr, ok := raw.(bson.A); ok {
				var list []string
				for _, p := range arr {
					if s, ok := p.(string); ok {
						list = append(list, s)
					}
				}
				paths = strings.Join(list, ", ")
			}
		}
		text = "Do not modify protected paths: " + paths
		return answerEffect{Stage: runstate.StageContext, Source: src("constraint", "protected_ack", "protected paths")}, nil
	case "raise_limit":
		pv, err := a.livePolicy(ctx, run.OrgID, run.ProjectID)
		if err != nil {
			return answerEffect{}, err
		}
		return answerEffect{Stage: runstate.StageContext, Set: bson.M{"pinned.packet_cap": pv.Int("context.packet_bytes")}, Event: "run.repinned"}, nil
	case "approve":
		now := time.Now().UTC()
		return answerEffect{Stage: runstate.StageStartGate, Set: bson.M{"plan.approved": true, "plan.approved_at": now}}, nil
	case "request_changes":
		return answerEffect{Stage: runstate.StagePlan, Source: src("user_answer", "plan_changes", "plan changes"), Set: bson.M{"plan": nil}}, nil
	case "resume":
		return answerEffect{Stage: run.Checkpoint.Stage}, nil
	}
	return answerEffect{}, answerInvalid("choice", "unknown choice")
}

// applyAnswer is the transaction of PLAN-S3-GO WP46: close the item, add the source to the same run, move the run
// back to its stage and wake its single stage job. It runs inside the caller's transaction.
func (a *App) applyAnswer(ctx context.Context, org string, it InboxItemDoc, expected int64, choice string, text *string, validate bool, actor string) (InboxItemDoc, RunDoc, error) {
	if it.Status != "open" {
		stale := problem("inbox_item_stale")
		stale.Current = it.DTO()
		return it, RunDoc{}, stale
	}
	if it.Revision != expected {
		c := problem("revision_conflict")
		c.Current = it.DTO()
		return it, RunDoc{}, c
	}
	if validate {
		var opt *InboxOption
		for i := range it.Options {
			if it.Options[i].ID == choice {
				opt = &it.Options[i]
			}
		}
		if opt == nil {
			return it, RunDoc{}, answerInvalid("choice", "is not one of the offered choices")
		}
		if e := validAnswerText(text, opt.RequiresText); e != nil {
			return it, RunDoc{}, e
		}
	} else if e := validAnswerText(text, false); e != nil {
		return it, RunDoc{}, e
	}
	run, err := findOne[RunDoc](ctx, a.DB, "runs", bson.M{"_id": it.RunID, "org_id": org, "active": true, "state": runstate.NeedsInput, "open_inbox_id": it.ID})
	if errors.Is(err, mongo.ErrNoDocuments) {
		stale := problem("inbox_item_stale")
		stale.Current = it.DTO()
		return it, RunDoc{}, stale
	}
	if err != nil {
		return it, RunDoc{}, err
	}
	trimmed := ""
	if text != nil {
		trimmed = strings.TrimSpace(*text)
	}
	eff, err := a.effectOf(ctx, it, run, choice, trimmed)
	if err != nil {
		return it, RunDoc{}, err
	}
	now := time.Now().UTC()
	var textPtr *string
	if trimmed != "" {
		textPtr = &trimmed
	}
	var updated InboxItemDoc
	err = a.DB.Coll("inbox_items").FindOneAndUpdate(ctx, bson.M{"_id": it.ID, "org_id": org, "status": "open", "revision": expected},
		bson.M{"$push": bson.M{"answers": InboxAnswer{N: len(it.Answers) + 1, Choice: choice, Text: textPtr, At: now}}, "$set": bson.M{"status": "resolved", "resolution": choice, "updated_at": now}, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&updated)
	if errors.Is(err, mongo.ErrNoDocuments) {
		cur, e := a.inboxItem(ctx, org, it.ID)
		if e != nil {
			return it, RunDoc{}, e
		}
		stale := problem("inbox_item_stale")
		stale.Current = cur.DTO()
		return cur, RunDoc{}, stale
	}
	if err != nil {
		return it, RunDoc{}, err
	}
	rev := updated.Revision
	if err = a.AppendEvent(ctx, org, "inbox.answered", Subject{"inbox", it.ID}, bson.M{"run_id": run.ID, "reason": it.Reason}, &rev, nil, actor != runstate.ActorUser); err != nil {
		return it, RunDoc{}, err
	}
	if eff.Cancel {
		next, e := a.cancelRunTx(ctx, run, eff.CancelReason, "")
		return updated, next, e
	}
	set := bson.M{"open_inbox_id": nil, "state": runstate.StateOfRound(eff.Stage, run.Round), "checkpoint": RunCheckpoint{Stage: eff.Stage, Since: now}}
	for k, v := range eff.Set {
		set[k] = v
	}
	if rd, ok := set["round"].(int); ok {
		set["state"] = runstate.StateOfRound(eff.Stage, rd)
	}
	if eff.Source != nil {
		s := *eff.Source
		s.ID, s.RevisionAdded = newID("src"), run.SourcesRevision+1
		set["sources"] = append(append([]RunSource{}, run.Sources...), s)
		set["sources_revision"] = run.SourcesRevision + 1
	}
	extra := []eventSpec{{"run.answered", bson.M{"inbox_id": it.ID}}, {"run.resumed", bson.M{"reason": "answered"}}}
	if eff.Event != "" {
		extra = append(extra, eventSpec{eff.Event, bson.M{"inbox_id": it.ID, "packet_cap": set["pinned.packet_cap"]}})
	}
	next, err := a.casRun(ctx, run, run.Checkpoint.Stage, set, runEvent{Reason: "answered", Actor: runstate.ActorUser, Extra: extra})
	if errors.Is(err, errRunMoved) {
		stale := problem("inbox_item_stale")
		stale.Current = updated.DTO()
		return updated, RunDoc{}, stale
	}
	if err != nil {
		return it, RunDoc{}, err
	}
	if err = a.enqueueRunAdvance(ctx, org, run.ID); err != nil {
		return it, RunDoc{}, err
	}
	return updated, next, nil
}

func (a *App) answerOutput(ctx context.Context, item InboxItemDoc, run RunDoc) (Output, error) {
	runDTO, err := a.runDTO(ctx, run)
	if err != nil {
		return Output{}, err
	}
	return Output{200, bson.M{"item": item.DTO(), "run": runDTO}, map[string]string{"etag": etag(item.Revision)}}, nil
}

func (a *App) answerInboxItem(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req AnswerInboxRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("inbId")
	route := "POST /api/v1/orgs/{orgId}/inbox/{inbId}/answer"
	hash := requestHash("POST", route, map[string]string{"orgId": org, "inbId": id, "if_match": fmt.Sprint(expected)}, req)
	return a.idempotent(w, r, org, route, key, hash, func(ctx context.Context) (Output, error) {
		it, err := a.inboxItem(ctx, org, id)
		if err != nil {
			return Output{}, err
		}
		item, run, err := a.applyAnswer(ctx, org, it, expected, req.Choice, req.Text, true, runstate.ActorUser)
		if err != nil {
			return Output{}, err
		}
		return a.answerOutput(ctx, item, run)
	})
}
func (a *App) resolveInboxItem(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req ResolveInboxRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if req.Action != "resume" && req.Action != "cancel_run" {
		return answerInvalid("action", "must be resume or cancel_run")
	}
	org, id := r.PathValue("orgId"), r.PathValue("inbId")
	route := "POST /api/v1/orgs/{orgId}/inbox/{inbId}/resolve"
	hash := requestHash("POST", route, map[string]string{"orgId": org, "inbId": id, "if_match": fmt.Sprint(expected)}, req)
	return a.idempotent(w, r, org, route, key, hash, func(ctx context.Context) (Output, error) {
		it, err := a.inboxItem(ctx, org, id)
		if err != nil {
			return Output{}, err
		}
		if req.Action == "resume" && !contains(resolveOnlyReasons, it.Reason) {
			return Output{}, answerInvalid("action", "this question needs an answer, not a resume")
		}
		item, run, err := a.applyAnswer(ctx, org, it, expected, req.Action, nil, false, runstate.ActorUser)
		if err != nil {
			return Output{}, err
		}
		return a.answerOutput(ctx, item, run)
	})
}
