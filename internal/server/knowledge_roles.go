package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"orqen/internal/kauthor"
	"orqen/internal/knowledge"
)

// The roles knowledge author (Sprint 7, K40). A detached session turns the evidence bundle of a merged run into draft
// records through the writer and verifier catherd roles. The session has its own collection, job and catherd run and
// never writes the origin run. Everything role-written is a draft: knowledge.NeverAuto carries roles_authored_needs_review.

// KauthorDirective is the last host call of a session (same shape as the run's host session).
type KauthorDirective struct {
	ID       string `bson:"id"`
	Tool     string `bson:"tool"`
	ArgsHash string `bson:"args_hash"`
	State    string `bson:"state"`
	TurnID   string `bson:"turn_id"`
}
type KauthorHost struct {
	ThreadID      string            `bson:"thread_id"`
	LastDirective *KauthorDirective `bson:"last_directive"`
}
type KauthorCatherd struct {
	RunID   string `bson:"run_id"`
	RunDir  string `bson:"run_dir"`
	RepoRel string `bson:"repo_rel"`
}

// KauthorRole is one dispatched role of a session.
type KauthorRole struct {
	Name            string     `bson:"name"`
	Rung            string     `bson:"rung"`
	Family          string     `bson:"family"`
	DispatchID      string     `bson:"dispatch_id"`
	DirectiveID     string     `bson:"directive_id"`
	DispatchingAt   *time.Time `bson:"dispatching_at"`
	DispatchedAt    *time.Time `bson:"dispatched_at"`
	Status          string     `bson:"status"`
	ReplyArtifactID string     `bson:"reply_artifact_id"`
	Tries           int        `bson:"tries"`
}

// KnowledgeAuthorSessionDoc is one roles authoring session (collection knowledge_author_sessions, ids kas_).
type KnowledgeAuthorSessionDoc struct {
	ID                 string         `bson:"_id"`
	OrgID              string         `bson:"org_id"`
	RunID              string         `bson:"run_id"`
	ProjectID          string         `bson:"project_id"`
	DedupeKey          string         `bson:"dedupe_key"`
	Attempt            int            `bson:"attempt"`
	Trigger            string         `bson:"trigger"` // merge | retry
	State              string         `bson:"state"`
	Code               string         `bson:"code"`
	HostFailures       int            `bson:"host_failures"`
	Host               KauthorHost    `bson:"host"`
	Catherd            KauthorCatherd `bson:"catherd"`
	Writer             KauthorRole    `bson:"writer"`
	KVerifier          KauthorRole    `bson:"kverifier"`
	BundleArtifactID   string         `bson:"bundle_artifact_id"`
	ProposalID         string         `bson:"proposal_id"`
	FallbackProposalID string         `bson:"fallback_proposal_id"`
	CreatedAt          time.Time      `bson:"created_at"`
	UpdatedAt          time.Time      `bson:"updated_at"`
	Revision           int64          `bson:"revision"`
}

func (r KauthorRole) dto() bson.M {
	return bson.M{"name": optStr(r.Name), "rung": optStr(r.Rung), "status": optStr(r.Status)}
}

// DTO is KnowledgeAuthorSessionDto: ids and codes only, never a path, a brief or a reply.
func (d KnowledgeAuthorSessionDoc) DTO() bson.M {
	return bson.M{"id": d.ID, "run_id": d.RunID, "attempt": d.Attempt, "trigger": d.Trigger, "state": d.State, "code": optStr(d.Code),
		"writer": d.Writer.dto(), "kverifier": d.KVerifier.dto(), "proposal_id": optStr(d.ProposalID), "fallback_proposal_id": optStr(d.FallbackProposalID),
		"created_at": timestamp(d.CreatedAt), "updated_at": timestamp(d.UpdatedAt), "revision": d.Revision}
}

const kauthorJobKind = "knowledge_author_roles"

func kauthorJobKey(org string) string { return "kauthor-roles:" + org }

func init() {
	idemRequired["POST /api/v1/orgs/{orgId}/knowledge-author-sessions/{kasId}/retry"] = true
	idemRequired["POST /api/v1/orgs/{orgId}/knowledge-author-sessions/{kasId}/fallback"] = true
}

func (a *App) kauthorRoutes() []Route {
	const p = "/api/v1/orgs/{orgId}/knowledge-author-sessions"
	return []Route{
		{"GET", p, a.listAuthorSessions}, {"GET", p + "/{kasId}", a.getAuthorSession},
		{"POST", p + "/{kasId}/cancel", a.cancelAuthorSession}, {"POST", p + "/{kasId}/retry", a.retryAuthorSession}, {"POST", p + "/{kasId}/fallback", a.fallbackAuthorSession},
	}
}

func (a *App) authorSession(ctx context.Context, org, id string) (KnowledgeAuthorSessionDoc, error) {
	return findOne[KnowledgeAuthorSessionDoc](ctx, a.DB, "knowledge_author_sessions", bson.M{"_id": id, "org_id": org})
}

// createAuthorSessionTx inserts the session (once per dedupe key) and makes sure the org's single author job exists.
// It must run inside a transaction; the caller re-runs it once on errJobRace.
func (a *App) createAuthorSessionTx(sc context.Context, run RunDoc, trigger string, attempt int, dedupe string) (KnowledgeAuthorSessionDoc, error) {
	cur, err := findOne[KnowledgeAuthorSessionDoc](sc, a.DB, "knowledge_author_sessions", bson.M{"org_id": run.OrgID, "dedupe_key": dedupe})
	if err == nil {
		return cur, a.ensureJobTx(sc, run.OrgID, kauthorJobKind, kauthorJobKey(run.OrgID), 1000, bson.M{})
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return cur, err
	}
	now := time.Now().UTC()
	d := KnowledgeAuthorSessionDoc{ID: newID("kas"), OrgID: run.OrgID, RunID: run.ID, ProjectID: run.ProjectID, DedupeKey: dedupe, Attempt: attempt, Trigger: trigger,
		State: kauthor.Queued, CreatedAt: now, UpdatedAt: now, Revision: 1}
	if _, err = a.DB.Coll("knowledge_author_sessions").InsertOne(sc, d); err != nil {
		return d, err
	}
	if err = a.ensureJobTx(sc, run.OrgID, kauthorJobKind, kauthorJobKey(run.OrgID), 1000, bson.M{}); err != nil {
		return d, err
	}
	return d, a.AppendEvent(sc, run.OrgID, "knowledge.author_session", Subject{"knowledge_author_session", d.ID}, bson.M{"kas_id": d.ID, "run_id": d.RunID, "state": d.State, "code": ""}, nil, nil, true)
}

// kasSet applies set to the session when its state is one of from (empty = any) and, when expect > 0, its revision matches.
func (a *App) kasSet(sc context.Context, org, id string, expect int64, from []string, set bson.M) (KnowledgeAuthorSessionDoc, error) {
	f := bson.M{"_id": id, "org_id": org}
	if expect > 0 {
		f["revision"] = expect
	}
	if len(from) > 0 {
		f["state"] = bson.M{"$in": from}
	}
	set["updated_at"] = time.Now().UTC()
	var out KnowledgeAuthorSessionDoc
	err := a.DB.Coll("knowledge_author_sessions").FindOneAndUpdate(sc, f, bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&out)
	if err != nil {
		return out, err
	}
	if _, ok := set["state"]; ok {
		err = a.AppendEvent(sc, org, "knowledge.author_session", Subject{"knowledge_author_session", id}, bson.M{"kas_id": id, "run_id": out.RunID, "state": out.State, "code": out.Code}, nil, nil, true)
	}
	return out, err
}

func (a *App) listAuthorSessions(w http.ResponseWriter, r *http.Request) error {
	f := bson.M{"org_id": r.PathValue("orgId")}
	if v := r.URL.Query().Get("run_id"); v != "" {
		if !validID("run", v) {
			return validation("run_id", "is not a run id")
		}
		f["run_id"] = v
	}
	list, err := findMany[KnowledgeAuthorSessionDoc](r.Context(), a.DB, "knowledge_author_sessions", f, bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}, 100)
	if err != nil {
		return err
	}
	items := []bson.M{}
	for _, d := range list {
		items = append(items, d.DTO())
	}
	writeJSON(w, 200, bson.M{"items": items})
	return nil
}

func (a *App) getAuthorSession(w http.ResponseWriter, r *http.Request) error {
	d, err := a.authorSession(r.Context(), r.PathValue("orgId"), r.PathValue("kasId"))
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, 200, d.DTO())
	return nil
}

// cancelAuthorSession asks a non-terminal session to stop. A session that never started a catherd run is cancelled at
// once; otherwise the author job cancels the running role and finishes the cancellation.
func (a *App) cancelAuthorSession(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("kasId")
	var out KnowledgeAuthorSessionDoc
	err = a.DB.Txn(r.Context(), func(sc context.Context) error {
		cur, e := a.authorSession(sc, org, id)
		if e != nil {
			return e
		}
		if kauthor.Terminal(cur.State) {
			return problem("kas_terminal")
		}
		if cur.Revision != expected {
			return problem("revision_conflict")
		}
		set := bson.M{"state": kauthor.CancelRequested}
		if cur.State == kauthor.Queued || cur.State == kauthor.Starting {
			set["state"], set["code"] = kauthor.Cancelled, kauthor.CodeCancelled
		}
		out, e = a.kasSet(sc, org, id, expected, nil, set)
		if errors.Is(e, mongo.ErrNoDocuments) {
			return problem("revision_conflict")
		}
		if e == nil && set["state"] == kauthor.CancelRequested {
			e = a.ensureJobTx(sc, org, kauthorJobKind, kauthorJobKey(org), 1000, bson.M{})
		}
		return e
	})
	if errors.Is(err, errJobRace) {
		return a.cancelAuthorSession(w, r)
	}
	if err != nil {
		return err
	}
	a.Committed()
	w.Header().Set("ETag", etag(out.Revision))
	writeJSON(w, 200, out.DTO())
	return nil
}

func (a *App) retryAuthorSession(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("kasId")
	route := "POST /api/v1/orgs/{orgId}/knowledge-author-sessions/{kasId}/retry"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "kasId": id}, nil), func(ctx context.Context) (Output, error) {
		cur, err := a.authorSession(ctx, org, id)
		if err != nil {
			return Output{}, err
		}
		if cur.State != kauthor.Failed {
			return Output{}, problem("kas_not_failed")
		}
		run, err := a.run(ctx, org, cur.RunID)
		if err != nil {
			return Output{}, err
		}
		next, err := a.createAuthorSessionTx(ctx, run, "retry", cur.Attempt+1, fmt.Sprintf("kas:retry:%s:%d", run.ID, cur.Attempt+1))
		if err != nil {
			return Output{}, err
		}
		return Output{201, next.DTO(), map[string]string{"etag": etag(next.Revision)}}, nil
	})
}

func (a *App) fallbackAuthorSession(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("kasId")
	route := "POST /api/v1/orgs/{orgId}/knowledge-author-sessions/{kasId}/fallback"
	// authorRun opens its own transactions, so the fallback cannot run inside idempotent()'s one; it is exactly-once by
	// its own dedupe key (merge:<run>), and the idempotency record only replays the answer.
	return a.idempotentNoTxn(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "kasId": id}, nil), func(ctx context.Context) (Output, error) {
		kas, err := a.authorSession(ctx, org, id)
		if err != nil {
			return Output{}, err
		}
		if !kauthor.Terminal(kas.State) {
			return Output{}, problem("kas_active")
		}
		prop, err := a.fallbackAuthor(ctx, kas)
		if err != nil {
			return Output{}, err
		}
		return Output{200, prop.DTO(), nil}, nil
	})
}

// idempotentNoTxn is idempotent() for work that manages its own transactions.
func (a *App) idempotentNoTxn(w http.ResponseWriter, r *http.Request, scope, route, key, hash string, f func(context.Context) (Output, error)) error {
	rec, replay, err := a.claim(r.Context(), scope, route, key, hash)
	if err != nil {
		return err
	}
	if replay != nil {
		outputResponse(w, *replay, true)
		return nil
	}
	out, err := f(r.Context())
	if err == nil {
		err = a.completeIdem(r.Context(), rec, out)
	}
	if err != nil {
		e := asAppError(err)
		if e.Status >= 400 && e.Status < 500 && e.Code != "idempotency_in_progress" {
			_ = a.completeIdem(r.Context(), rec, Output{e.Status, errorBody(err, requestID(r)), nil})
		} else {
			_, _ = a.DB.Coll("idempotency_records").DeleteOne(r.Context(), bson.M{"_id": rec.ID, "claim_token": rec.ClaimToken, "state": "in_progress"})
		}
		return err
	}
	a.Committed()
	outputResponse(w, out, false)
	return nil
}

// fallbackAuthor leaves the run's knowledge to the deterministic author, at most once per run (dedupe merge:<run>).
func (a *App) fallbackAuthor(ctx context.Context, kas KnowledgeAuthorSessionDoc) (KnowledgeProposalDoc, error) {
	if kas.FallbackProposalID != "" {
		if p, err := findOne[KnowledgeProposalDoc](ctx, a.DB, "knowledge_proposals", bson.M{"_id": kas.FallbackProposalID, "org_id": kas.OrgID}); err == nil {
			return p, nil
		}
	}
	run, err := a.run(ctx, kas.OrgID, kas.RunID)
	if err != nil {
		return KnowledgeProposalDoc{}, err
	}
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return KnowledgeProposalDoc{}, err
	}
	trigger := "merge"
	if !pv.Bool("knowledge.learn_on_merge") {
		trigger = "manual" // the person asked for the draft; "merge" would produce nothing
	}
	existing, err := findOne[KnowledgeProposalDoc](ctx, a.DB, "knowledge_proposals", bson.M{"org_id": kas.OrgID, "dedupe_key": "merge:" + kas.RunID})
	prop := existing
	switch {
	case err == nil && trigger == "merge":
	case err != nil && !errors.Is(err, mongo.ErrNoDocuments):
		return prop, err
	default:
		if prop, err = a.authorRun(ctx, kas.OrgID, kas.RunID, trigger); err != nil {
			return prop, err
		}
	}
	if _, err = a.DB.Coll("knowledge_author_sessions").UpdateOne(ctx, bson.M{"_id": kas.ID, "org_id": kas.OrgID}, bson.M{"$set": bson.M{"fallback_proposal_id": prop.ID, "updated_at": time.Now().UTC()}, "$inc": bson.M{"revision": int64(1)}}); err != nil {
		return prop, err
	}
	a.Committed()
	return prop, nil
}

func verdictRank(v knowledge.Verdict) int {
	switch v {
	case knowledge.VerdictSupported:
		return 3
	case knowledge.VerdictUnchecked:
		return 2
	case knowledge.VerdictUnsupported:
		return 1
	}
	return 0
}

// applyAuthorReplies turns the writer's and the verifier's replies into a proposal of draft records (Y11, Y13).
// Nothing is verified here: role-written drafts always wait for a person. The verifier's verdicts and the writer's
// own unsupported marks (unknown or missing refs) are stored on each version and can only lower a gate verdict.
func (a *App) applyAuthorReplies(ctx context.Context, kas KnowledgeAuthorSessionDoc, writerReply, kverifyReply string) (KnowledgeProposalDoc, error) {
	org := kas.OrgID
	dedupe := "roles:" + kas.ID
	if p, err := findOne[KnowledgeProposalDoc](ctx, a.DB, "knowledge_proposals", bson.M{"org_id": org, "dedupe_key": dedupe}); err == nil {
		return p, nil
	}
	run, err := a.run(ctx, org, kas.RunID)
	if err != nil {
		return KnowledgeProposalDoc{}, err
	}
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return KnowledgeProposalDoc{}, err
	}
	bundle, err := a.buildBundle(ctx, run)
	if err != nil {
		return KnowledgeProposalDoc{}, err
	}
	recs, err := knowledge.ParseWriterReply(writerReply, knowledge.BundleRefs(bundle))
	if err != nil {
		return KnowledgeProposalDoc{}, err
	}
	verdicts, err := knowledge.ParseKVerifyReply(kverifyReply)
	if err != nil {
		return KnowledgeProposalDoc{}, err
	}
	byKey := knowledge.SplitVerdicts(verdicts)
	prop := KnowledgeProposalDoc{ID: newID("kpr"), OrgID: org, DedupeKey: dedupe, RunID: run.ID, BundleArtifactID: kas.BundleArtifactID, Trigger: "merge_roles", State: "empty", Items: []KnowledgeProposalItem{}, CreatedAt: time.Now().UTC()}
	origin := knowledge.Origin{Kind: "roles", ProposalID: prop.ID, Actor: "writer"}
	for _, d := range recs {
		mode := pv.Str("knowledge.promotion." + string(d.Type))
		if mode == "off" {
			continue
		}
		item := KnowledgeProposalItem{Key: d.Key, Type: string(d.Type), Outcome: "draft"}
		scope := KnowledgeScopeInput{Kind: d.ScopeKind}
		switch d.ScopeKind {
		case "task":
			scope.RunID = run.ID
		case "project":
			scope.ProjectID = run.ProjectID
		}
		req := CreateKnowledgeRecordRequest{Type: string(d.Type), Scope: scope, Key: d.Key, Title: d.Title, Body: d.Body, Claims: claimInputs(d.Claims)}
		rec, v, e := a.createKnowledgeRecord(ctx, org, "system", req, origin)
		var ae *AppError
		if errors.As(e, &ae) && ae.Code == "knowledge_key_taken" {
			_, sk, e2 := a.scopeFrom(ctx, org, d.Type, scope)
			if e2 != nil {
				item.Outcome, item.Reasons = "skipped", []string{errCode(e2)}
				prop.Items = append(prop.Items, item)
				continue
			}
			existing, e2 := findOne[KnowledgeRecordDoc](ctx, a.DB, "knowledge_records", bson.M{"org_id": org, "scope_key": sk, "key": d.Key})
			if e2 != nil {
				return prop, e2
			}
			nv, e3 := a.createKnowledgeVersion(ctx, org, existing.ID, existing.Revision, CreateKnowledgeVersionRequest{Title: d.Title, Body: d.Body, Claims: req.Claims}, origin)
			if e3 != nil {
				item.RecordID, item.Outcome = existing.ID, "exists"
				prop.Items = append(prop.Items, item)
				continue
			}
			rec, v, e = existing, nv, nil
		}
		if e != nil {
			item.Outcome, item.Reasons = "skipped", []string{errCode(e)}
			prop.Items = append(prop.Items, item)
			continue
		}
		if err = a.storeRoleVerdicts(ctx, org, v, d, byKey[d.Key]); err != nil {
			return prop, err
		}
		item.RecordID, item.VersionID, item.Outcome = rec.ID, v.ID, "awaiting_review"
		item.Reasons = []string{knowledge.NARolesAuthored}
		if mode == "auto" {
			// the policy asks for auto promotion: the never-auto rules refuse it and the reasons say why
			if out, ve := a.verifyVersion(ctx, org, v.ID, verifyOpts{Actor: knowledge.ActorPolicy, User: "system"}); ve == nil && out.Outcome != "verified" && len(out.Never) > 0 {
				item.Reasons = out.Never
			} else if ve != nil && errCode(ve) == "knowledge_conflict_open" {
				item.Outcome = "conflicted"
			}
		}
		prop.Items = append(prop.Items, item)
	}
	if len(prop.Items) > 0 {
		prop.State = "awaiting_review"
	}
	if _, err = a.DB.Coll("knowledge_proposals").InsertOne(ctx, prop); mongo.IsDuplicateKeyError(err) {
		err = nil
	}
	return prop, err
}

// storeRoleVerdicts maps the writer's own marks and the verifier's verdicts (claim "c<n>") to the stored claim ids and
// keeps the lower verdict per claim.
func (a *App) storeRoleVerdicts(ctx context.Context, org string, v KnowledgeVersionDoc, d knowledge.WriterRecord, kv []knowledge.KVerdict) error {
	byID := map[string]RoleVerdict{}
	put := func(id string, verdict knowledge.Verdict, reason string) {
		if cur, ok := byID[id]; !ok || verdictRank(verdict) < verdictRank(knowledge.Verdict(cur.Verdict)) {
			byID[id] = RoleVerdict{ClaimID: id, Verdict: string(verdict), Reason: reason}
		}
	}
	for i, c := range d.Claims {
		if i >= len(v.Claims) {
			break
		}
		if c.Verdict == knowledge.VerdictUnsupported && len(c.Flags) > 0 {
			put(v.Claims[i].ID, knowledge.VerdictUnsupported, c.Flags[0])
		}
	}
	for _, k := range kv {
		var n int
		if _, err := fmt.Sscanf(k.ClaimID, "c%d", &n); err != nil || n < 1 || n > len(v.Claims) {
			continue
		}
		put(v.Claims[n-1].ID, k.Verdict, k.Reason)
	}
	if len(byID) == 0 {
		return nil
	}
	list := make([]RoleVerdict, 0, len(byID))
	for _, c := range v.Claims {
		if rv, ok := byID[c.ID]; ok {
			list = append(list, rv)
		}
	}
	_, err := a.DB.Coll("knowledge_versions").UpdateOne(ctx, bson.M{"_id": v.ID, "org_id": org}, bson.M{"$set": bson.M{"role_verdicts": list}})
	return err
}
