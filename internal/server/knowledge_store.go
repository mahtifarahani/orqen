package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"orqen/internal/knowledge"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxKnowledgeText = 256 << 10

// kresolver resolves evidence refs inside one org. A ref of another org does not resolve (filter by org_id).
type kresolver struct {
	a   *App
	ctx context.Context
	org string
}

func (a *App) resolverFor(ctx context.Context, org string) kresolver { return kresolver{a, ctx, org} }

func (r kresolver) Resolve(e knowledge.Evidence) (knowledge.Resolved, error) {
	switch e.RefKind {
	case knowledge.RefSourceVersion:
		d, err := findOne[SourceVersionDoc](r.ctx, r.a.DB, "source_versions", bson.M{"_id": e.RefID, "org_id": r.org})
		if err != nil {
			return knowledge.Resolved{}, err
		}
		text := ""
		if !d.LinkOnly && d.ArtifactID != "" {
			text, _ = r.a.artifactText(r.ctx, r.org, d.ArtifactID, maxKnowledgeText)
		}
		return knowledge.Resolved{Kind: e.RefKind, OrgID: d.OrgID, Authority: d.Authority, Text: text, Latest: d.Latest}, nil
	case knowledge.RefArtifact:
		d, err := findOne[ArtifactDoc](r.ctx, r.a.DB, "artifacts", bson.M{"_id": e.RefID, "org_id": r.org})
		if err != nil {
			return knowledge.Resolved{}, err
		}
		text := ""
		if strings.HasPrefix(d.MediaType, "text/") || d.MediaType == "application/json" {
			text, _ = r.a.artifactText(r.ctx, r.org, d.ID, maxKnowledgeText)
		}
		return knowledge.Resolved{Kind: e.RefKind, OrgID: d.OrgID, Authority: knowledge.AuthorityOrqenRecord, Text: text, Latest: true}, nil
	}
	return r.resolveRun(e)
}

// evidenceLabel is a short human label of an evidence reference.
func (a *App) evidenceLabel(ctx context.Context, org string, e knowledge.Evidence) string {
	if e.RefKind == knowledge.RefSourceVersion {
		if d, err := findOne[SourceVersionDoc](ctx, a.DB, "source_versions", bson.M{"_id": e.RefID, "org_id": org}); err == nil {
			return d.Title
		}
	}
	return e.RefID
}

// ---- input handling ----

type KnowledgeClaimInput struct {
	Text     string              `json:"text"`
	Template *knowledge.Template `json:"template"`
	Fact     *knowledge.Fact     `json:"fact"`
	Critical bool                `json:"critical"`
	Evidence []KnowledgeEvidence `json:"evidence"`
}
type KnowledgeEvidence struct {
	RefKind string `json:"ref_kind"`
	RefID   string `json:"ref_id"`
	Locator string `json:"locator"`
	Excerpt string `json:"excerpt"`
}

var evidenceKinds = []string{knowledge.RefSourceVersion, knowledge.RefArtifact, knowledge.RefVerificationEvidence, knowledge.RefReviewFinding, knowledge.RefMergeRequest, knowledge.RefRun}

// normalizeClaims validates input and builds stored claims: ids cl_<n>, excerpt hashes, injection flags, verdict unchecked.
func (a *App) normalizeClaims(ctx context.Context, org string, in []KnowledgeClaimInput) ([]knowledge.Claim, error) {
	var es []FieldError
	if len(in) == 0 || len(in) > 50 {
		es = append(es, FieldError{"claims", "must have 1 to 50 claims"})
	}
	res := a.resolverFor(ctx, org)
	out := make([]knowledge.Claim, 0, len(in))
	for i, c := range in {
		f := func(n string) string { return "claims[" + strconv.Itoa(i) + "]." + n }
		text := strings.TrimSpace(c.Text)
		if text == "" || len(text) > knowledge.MaxClaimText {
			es = append(es, FieldError{f("text"), "must be 1 to 500 characters"})
		}
		if c.Fact != nil && (strings.TrimSpace(c.Fact.Subject) == "" || strings.TrimSpace(c.Fact.Predicate) == "") {
			es = append(es, FieldError{f("fact"), "needs a subject and a predicate"})
		}
		cl := knowledge.Claim{ID: "cl_" + strconv.Itoa(i+1), Text: text, Template: c.Template, Fact: c.Fact, Critical: c.Critical, Verdict: knowledge.VerdictUnchecked, Evidence: []knowledge.Evidence{}}
		for j, e := range c.Evidence {
			if !contains(evidenceKinds, e.RefKind) || e.RefID == "" || len(e.Excerpt) > knowledge.MaxExcerpt {
				es = append(es, FieldError{f("evidence[" + strconv.Itoa(j) + "]"), "has an unknown kind, no ref_id or an excerpt over 300 characters"})
				continue
			}
			ev := knowledge.Evidence{RefKind: e.RefKind, RefID: e.RefID, Locator: e.Locator, Excerpt: e.Excerpt}
			if e.Excerpt != "" {
				ev.ExcerptSHA = knowledge.SHA(knowledge.NormText(e.Excerpt))
			}
			if _, err := res.Resolve(ev); err != nil {
				es = append(es, FieldError{f("evidence[" + strconv.Itoa(j) + "]"), "evidence_ref_invalid: the reference does not exist in this organization"})
				continue
			}
			cl.Evidence = append(cl.Evidence, ev)
		}
		out = append(out, cl)
	}
	if len(es) > 0 {
		e := validationErrors(es).(*AppError)
		for _, fe := range es {
			if strings.Contains(fe.Message, "evidence_ref_invalid") {
				return nil, &AppError{Code: "evidence_ref_invalid", Status: 422, Detail: "an evidence reference does not resolve in this organization", Errors: es}
			}
		}
		return nil, e
	}
	return out, nil
}

func validateKnowledgeText(title, body string) []FieldError {
	var es []FieldError
	if t := strings.TrimSpace(title); t == "" || len([]rune(t)) > knowledge.MaxTitle {
		es = append(es, FieldError{"title", "must be 1 to 200 characters"})
	}
	if len(body) > knowledge.MaxBody {
		es = append(es, FieldError{"body", "must be at most 4000 bytes"})
	}
	return es
}

// ---- records and versions ----

type KnowledgeScopeInput struct {
	Kind      string `json:"kind"`
	ProjectID string `json:"project_id"`
	SprintID  string `json:"sprint_id"`
	RunID     string `json:"run_id"`
}

func (a *App) scopeFrom(ctx context.Context, org string, t knowledge.Type, in KnowledgeScopeInput) (KnowledgeScope, string, error) {
	sc := knowledge.Scope{Kind: in.Kind, Project: in.ProjectID, Sprint: in.SprintID, Run: in.RunID}
	key := sc.Key()
	if _, err := knowledge.ParseScope(key); err != nil || in.Kind != sc.Kind || (in.Kind != "org" && in.Kind != "project" && in.Kind != "sprint" && in.Kind != "task") {
		return KnowledgeScope{}, "", &AppError{Code: "knowledge_scope_invalid", Status: 422, Detail: "the scope is not valid", Errors: []FieldError{{"scope", "is not a valid scope"}}}
	}
	if !knowledge.ValidType(t) || knowledge.ValidateTypeScope(t, in.Kind) != nil {
		return KnowledgeScope{}, "", &AppError{Code: "knowledge_scope_invalid", Status: 422, Detail: "this type cannot live in this scope", Errors: []FieldError{{"scope", "the type does not fit the scope"}}}
	}
	if in.ProjectID != "" {
		if !validID("prj", in.ProjectID) {
			return KnowledgeScope{}, "", validation("scope.project_id", "is not a project id")
		}
		if _, err := a.project(ctx, org, in.ProjectID); err != nil {
			return KnowledgeScope{}, "", validation("scope.project_id", "unknown project")
		}
	}
	if in.Kind == "task" {
		if !validID("run", in.RunID) {
			return KnowledgeScope{}, "", validation("scope.run_id", "is not a run id")
		}
		run, err := findOne[RunDoc](ctx, a.DB, "runs", bson.M{"_id": in.RunID, "org_id": org})
		if err != nil {
			return KnowledgeScope{}, "", validation("scope.run_id", "unknown run")
		}
		return KnowledgeScope{Kind: "task", RunID: in.RunID, ProjectID: run.ProjectID}, key, nil
	}
	return KnowledgeScope{Kind: in.Kind, ProjectID: in.ProjectID, SprintID: in.SprintID, RunID: in.RunID}, key, nil
}

func (a *App) newVersionDoc(org string, rec KnowledgeRecordDoc, number int, title, body string, claims []knowledge.Claim, from, until *time.Time, origin knowledge.Origin) KnowledgeVersionDoc {
	cs, codes := knowledge.FlagClaims(claims, title, body)
	v := KnowledgeVersionDoc{ID: newID("kvr"), OrgID: org, RecordID: rec.ID, Number: number, Type: rec.Type, ScopeKey: rec.ScopeKey, ProjectID: rec.Scope.ProjectID,
		Title: strings.TrimSpace(title), Body: strings.TrimSpace(body), Claims: cs, ValidFrom: from, ValidUntil: until, Status: "draft", Origin: origin,
		InjectionFlags: codes, SupersedesMany: []string{}, CreatedAt: time.Now().UTC()}
	v.ContentHash = knowledge.ContentHash(v.domain())
	return v
}

type CreateKnowledgeRecordRequest struct {
	Type       string                `json:"type"`
	Scope      KnowledgeScopeInput   `json:"scope"`
	Key        string                `json:"key"`
	Title      string                `json:"title"`
	Body       string                `json:"body"`
	Claims     []KnowledgeClaimInput `json:"claims"`
	ValidFrom  *time.Time            `json:"valid_from"`
	ValidUntil *time.Time            `json:"valid_until"`
}

func (a *App) createKnowledgeRecord(ctx context.Context, org, actor string, req CreateKnowledgeRecordRequest, origin knowledge.Origin) (KnowledgeRecordDoc, KnowledgeVersionDoc, error) {
	if err := validationErrors(validateKnowledgeText(req.Title, req.Body)); err != nil {
		return KnowledgeRecordDoc{}, KnowledgeVersionDoc{}, err
	}
	if !knowledge.ValidKey(req.Key) {
		return KnowledgeRecordDoc{}, KnowledgeVersionDoc{}, validation("key", "must match ^[a-z0-9][a-z0-9._-]{1,99}$")
	}
	scope, scopeKey, err := a.scopeFrom(ctx, org, knowledge.Type(req.Type), req.Scope)
	if err != nil {
		return KnowledgeRecordDoc{}, KnowledgeVersionDoc{}, err
	}
	claims, err := a.normalizeClaims(ctx, org, req.Claims)
	if err != nil {
		return KnowledgeRecordDoc{}, KnowledgeVersionDoc{}, err
	}
	now := time.Now().UTC()
	rec := KnowledgeRecordDoc{ID: newID("kno"), OrgID: org, ScopeKey: scopeKey, Scope: scope, Type: req.Type, Key: req.Key, Title: strings.TrimSpace(req.Title),
		Status: "draft", AppliesToRoles: []string{}, NextNumber: 2, Active: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
	v := a.newVersionDoc(org, rec, 1, req.Title, req.Body, claims, req.ValidFrom, req.ValidUntil, origin)
	rec.CurrentVersionID = v.ID
	if n, _ := a.DB.Coll("knowledge_records").CountDocuments(ctx, bson.M{"org_id": org, "scope_key": scopeKey, "key": req.Key}); n > 0 {
		return rec, v, problem("knowledge_key_taken")
	}
	err = a.DB.Txn(ctx, func(sc context.Context) error {
		if _, e := a.DB.Coll("knowledge_records").InsertOne(sc, rec); e != nil {
			return e
		}
		if _, e := a.DB.Coll("knowledge_versions").InsertOne(sc, v); e != nil {
			return e
		}
		return a.AppendEvent(sc, org, "knowledge.version_created", Subject{"knowledge_record", rec.ID}, bson.M{"record_id": rec.ID, "version_id": v.ID, "status": "draft", "origin": origin.Kind}, &rec.Revision, nil, false)
	})
	if mongo.IsDuplicateKeyError(err) {
		return rec, v, problem("knowledge_key_taken")
	}
	if err == nil {
		a.Committed()
	}
	return rec, v, err
}

func (a *App) knowledgeRecord(ctx context.Context, org, id string) (KnowledgeRecordDoc, error) {
	return findOne[KnowledgeRecordDoc](ctx, a.DB, "knowledge_records", bson.M{"_id": id, "org_id": org})
}
func (a *App) knowledgeVersion(ctx context.Context, org, id string) (KnowledgeVersionDoc, error) {
	return findOne[KnowledgeVersionDoc](ctx, a.DB, "knowledge_versions", bson.M{"_id": id, "org_id": org})
}

type CreateKnowledgeVersionRequest struct {
	BaseVersionID string                `json:"base_version_id"`
	Title         string                `json:"title"`
	Body          string                `json:"body"`
	Claims        []KnowledgeClaimInput `json:"claims"`
	ValidFrom     *time.Time            `json:"valid_from"`
	ValidUntil    *time.Time            `json:"valid_until"`
}

func (a *App) createKnowledgeVersion(ctx context.Context, org, recID string, ifMatch int64, req CreateKnowledgeVersionRequest, origin knowledge.Origin) (KnowledgeVersionDoc, error) {
	if err := validationErrors(validateKnowledgeText(req.Title, req.Body)); err != nil {
		return KnowledgeVersionDoc{}, err
	}
	claims, err := a.normalizeClaims(ctx, org, req.Claims)
	if err != nil {
		return KnowledgeVersionDoc{}, err
	}
	var out KnowledgeVersionDoc
	err = a.DB.Txn(ctx, func(sc context.Context) error {
		rec, e := a.knowledgeRecord(sc, org, recID)
		if e != nil {
			return e
		}
		if !rec.Active {
			return problem("not_found")
		}
		if req.BaseVersionID != "" {
			if _, e = a.knowledgeVersion(sc, org, req.BaseVersionID); e != nil {
				return validation("base_version_id", "unknown version")
			}
		}
		if n, _ := a.DB.Coll("knowledge_versions").CountDocuments(sc, bson.M{"org_id": org, "record_id": recID, "status": "draft"}); n > 0 {
			d, _ := findOne[KnowledgeVersionDoc](sc, a.DB, "knowledge_versions", bson.M{"org_id": org, "record_id": recID, "status": "draft"})
			c := problem("knowledge_draft_exists")
			c.Current = a.versionDTO(sc, org, d, nil)
			return c
		}
		out = a.newVersionDoc(org, rec, rec.NextNumber, req.Title, req.Body, claims, req.ValidFrom, req.ValidUntil, origin)
		if rec.VerifiedVersionID != nil {
			out.Supersedes = *rec.VerifiedVersionID
		}
		if _, e = a.DB.Coll("knowledge_versions").InsertOne(sc, out); e != nil {
			return e
		}
		upd, e := revisionUpdate[recDTO](sc, a.DB, "knowledge_records", bson.M{"_id": recID, "org_id": org}, ifMatch, bson.M{"current_version_id": out.ID, "title": out.Title})
		if e != nil {
			return e
		}
		_ = upd
		if _, e = a.DB.Coll("knowledge_records").UpdateOne(sc, bson.M{"_id": recID}, bson.M{"$inc": bson.M{"next_number": 1}}); e != nil {
			return e
		}
		rev := upd.Rev
		return a.AppendEvent(sc, org, "knowledge.version_created", Subject{"knowledge_record", recID}, bson.M{"record_id": recID, "version_id": out.ID, "status": "draft", "origin": origin.Kind}, &rev, nil, false)
	})
	if mongo.IsDuplicateKeyError(err) {
		return out, problem("knowledge_draft_exists")
	}
	if err == nil {
		a.Committed()
	}
	return out, err
}

// recDTO adapts revisionUpdate (it needs a DTO method) for callers that only need the new revision.
type recDTO struct {
	Rev int64 `bson:"revision"`
}

func (r recDTO) DTO() bson.M { return bson.M{"revision": r.Rev} }

// ---- verify (the one path for user verification, policy auto promotion, conflict resolution and rollback) ----

type verifyOpts struct {
	Actor           knowledge.Actor
	User            string
	IfMatch         int64
	Note            string
	Attest          map[string]string
	PolicyVersionID string
	SkipConflicts   bool
	ConflictID      string
}

type verifyOut struct {
	Outcome  string // verified | conflicted | awaiting_review
	Gate     knowledge.GateResult
	Never    []string
	Conflict *KnowledgeConflictDoc
}

func (a *App) verifiedInScope(ctx context.Context, org string, v KnowledgeVersionDoc) []knowledge.Version {
	filter := bson.M{"org_id": org, "status": "verified", "record_id": bson.M{"$ne": v.RecordID}}
	if v.ProjectID != "" {
		filter["$or"] = []bson.M{{"scope_key": "org"}, {"project_id": v.ProjectID}}
	} else {
		filter["scope_key"] = "org"
		if v.ScopeKey != "org" {
			filter["scope_key"] = bson.M{"$in": []string{"org", v.ScopeKey}}
		}
	}
	docs, _ := findMany[KnowledgeVersionDoc](ctx, a.DB, "knowledge_versions", filter, bson.D{{Key: "_id", Value: 1}}, 500)
	out := make([]knowledge.Version, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.domain())
	}
	return out
}

func claimsUnsupported(ids []string) error {
	es := make([]FieldError, 0, len(ids))
	for _, id := range ids {
		es = append(es, FieldError{"claims." + id, "has no supporting evidence; add an attestation or evidence"})
	}
	return &AppError{Code: "knowledge_claims_unsupported", Status: 422, Detail: "some claims are not supported", Errors: es}
}

func (a *App) verifyVersion(ctx context.Context, org, kvr string, o verifyOpts) (verifyOut, error) {
	var out verifyOut
	v, err := a.knowledgeVersion(ctx, org, kvr)
	if err != nil {
		return out, err
	}
	rec, err := a.knowledgeRecord(ctx, org, v.RecordID)
	if err != nil || !rec.Active {
		return out, problem("not_found")
	}
	if v.Status == "conflicted" && !o.SkipConflicts {
		return out, a.conflictOpenError(ctx, org, v.ID)
	}
	if v.Status != "draft" && !(o.SkipConflicts && v.Status == "conflicted") {
		return out, problem("knowledge_version_not_draft")
	}
	dv := v.domain()
	out.Gate = knowledge.CheckClaims(dv.Type, v.Claims, a.resolverFor(ctx, org), org)
	if len(v.RoleVerdicts) > 0 {
		out.Gate.Claims = knowledge.MergeVerdicts(out.Gate.Claims, v.roleKVerdicts())
		out.Gate.Pass = len(out.Gate.Failing()) == 0 && len(out.Gate.Claims) > 0
	}
	attested := map[string]bool{}
	for id, t := range o.Attest {
		if strings.TrimSpace(t) != "" && len(t) <= knowledge.MaxAttestation {
			attested[id] = true
		}
	}
	if o.Actor == knowledge.ActorUser {
		if blockers := knowledge.UserVerifyBlockers(out.Gate, attested); len(blockers) > 0 {
			return out, claimsUnsupported(blockers)
		}
	}
	var pairs []knowledge.Pair
	if !o.SkipConflicts {
		pairs = knowledge.DetectConflicts(dv, a.verifiedInScope(ctx, org, v))
		if n, _ := a.DB.Coll("knowledge_conflicts").CountDocuments(ctx, bson.M{"org_id": org, "state": "open", "version_ids": v.ID}); n > 0 && len(pairs) == 0 {
			return out, a.conflictOpenError(ctx, org, v.ID)
		}
	}
	if len(pairs) == 0 && o.Actor == knowledge.ActorPolicy {
		userVerified := false
		if rec.VerifiedVersionID != nil {
			if cur, e := a.knowledgeVersion(ctx, org, *rec.VerifiedVersionID); e == nil && cur.Promotion != nil && cur.Promotion.Mode == "user" {
				userVerified = true
			}
		}
		krec := knowledge.Record{ID: rec.ID, Type: knowledge.Type(rec.Type), Pinned: rec.Pinned, Critical: rec.Critical}
		out.Never = knowledge.NeverAuto(dv, krec, out.Gate, nil, userVerified)
		if knowledge.Decide(string(knowledge.PolicyAuto), out.Never) != knowledge.AutoVerify {
			out.Outcome = "awaiting_review"
			_ = a.AppendEvent(ctx, org, "knowledge.auto_blocked", Subject{"knowledge_record", rec.ID}, bson.M{"record_id": rec.ID, "version_id": v.ID, "codes": out.Never}, nil, nil, true)
			a.Committed()
			return out, nil
		}
	}
	expected := o.IfMatch
	if expected == 0 {
		expected = rec.Revision
	}
	now := time.Now().UTC()
	err = a.txnRetryJob(ctx, func(sc context.Context) error {
		out.Conflict = nil
		out.Outcome = ""
		cur, e := findOne[KnowledgeRecordDoc](sc, a.DB, "knowledge_records", bson.M{"_id": rec.ID, "org_id": org})
		if e != nil {
			return e
		}
		if cur.Revision != expected {
			c := problem("revision_conflict")
			c.Current = cur.DTO()
			return c
		}
		if len(pairs) > 0 {
			fp := knowledge.Fingerprint(pairs)
			var existing KnowledgeConflictDoc
			e = a.DB.Coll("knowledge_conflicts").FindOne(sc, bson.M{"org_id": org, "fingerprint": fp, "state": "open"}).Decode(&existing)
			if errors.Is(e, mongo.ErrNoDocuments) {
				recIDs := []string{rec.ID}
				vids := []string{v.ID}
				for _, p := range pairs {
					vids = appendUniq(vids, p.BVersion)
					if ev, e2 := a.knowledgeVersion(sc, org, p.BVersion); e2 == nil {
						recIDs = appendUniq(recIDs, ev.RecordID)
					}
				}
				existing = KnowledgeConflictDoc{ID: newID("kcf"), OrgID: org, Fingerprint: fp, VersionIDs: vids, RecordIDs: recIDs, Pairs: pairs, State: "open", Revision: 1, CreatedAt: now, UpdatedAt: now}
				if _, e = a.DB.Coll("knowledge_conflicts").InsertOne(sc, existing); e != nil {
					return e
				}
				if e = a.AppendEvent(sc, org, "knowledge.conflict_opened", Subject{"knowledge_conflict", existing.ID}, bson.M{"conflict_id": existing.ID, "record_id": rec.ID, "version_id": v.ID}, nil, nil, o.Actor == knowledge.ActorPolicy); e != nil {
					return e
				}
			} else if e != nil {
				return e
			}
			if _, e = a.DB.Coll("knowledge_versions").UpdateOne(sc, bson.M{"_id": v.ID, "org_id": org, "status": "draft"}, bson.M{"$set": bson.M{"status": "conflicted"}}); e != nil {
				return e
			}
			set := bson.M{"updated_at": now}
			if cur.VerifiedVersionID == nil {
				set["status"] = "conflicted"
			}
			if _, e = a.DB.Coll("knowledge_records").UpdateOne(sc, bson.M{"_id": rec.ID, "revision": cur.Revision}, bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}}); e != nil {
				return e
			}
			out.Outcome, out.Conflict = "conflicted", &existing
			return nil
		}
		// Supersede the current verified version first (the partial unique index allows one verified).
		var oldID string
		if cur.VerifiedVersionID != nil {
			oldID = *cur.VerifiedVersionID
			r, e2 := a.DB.Coll("knowledge_versions").UpdateOne(sc, bson.M{"_id": oldID, "org_id": org, "status": "verified"}, bson.M{"$set": bson.M{"status": "superseded", "superseded_by": v.ID}})
			if e2 != nil {
				return e2
			}
			if r.MatchedCount != 1 {
				return problem("revision_conflict")
			}
		}
		claims := make([]knowledge.Claim, len(v.Claims))
		var atts []KnowledgeAttestation
		for i, c := range v.Claims {
			cv, _ := out.Gate.Verdict(c.ID)
			switch {
			case cv.Verdict == knowledge.VerdictSupported:
				c.Verdict, c.VerdictBy = knowledge.VerdictSupported, knowledge.ByDeterministic
			case attested[c.ID]:
				c.Verdict, c.VerdictBy = knowledge.VerdictSupported, knowledge.ByUser
				atts = append(atts, KnowledgeAttestation{ClaimID: c.ID, Text: strings.TrimSpace(o.Attest[c.ID]), By: o.User, At: now})
			default:
				c.Verdict, c.VerdictBy = knowledge.VerdictUnsupported, knowledge.ByDeterministic
			}
			claims[i] = c
		}
		mode := "user"
		if o.Actor == knowledge.ActorPolicy {
			mode = "auto"
		}
		prom := KnowledgePromotion{Mode: mode, DecidedBy: o.User, PolicyVersionID: o.PolicyVersionID, At: now, Note: o.Note, Attestations: atts}
		r, e := a.DB.Coll("knowledge_versions").UpdateOne(sc, bson.M{"_id": v.ID, "org_id": org, "status": bson.M{"$in": []string{"draft", "conflicted"}}},
			bson.M{"$set": bson.M{"status": "verified", "verified_at": now, "promotion": prom, "claims": claims, "supersedes": oldID}})
		if e != nil {
			return e
		}
		if r.MatchedCount != 1 {
			return problem("knowledge_version_not_draft")
		}
		if _, e = a.DB.Coll("knowledge_records").UpdateOne(sc, bson.M{"_id": rec.ID, "revision": cur.Revision}, bson.M{"$set": bson.M{"verified_version_id": v.ID, "current_version_id": v.ID, "status": "verified", "title": v.Title, "updated_at": now}, "$inc": bson.M{"revision": int64(1)}}); e != nil {
			return e
		}
		if o.ConflictID != "" {
			if _, e = a.DB.Coll("knowledge_conflicts").UpdateOne(sc, bson.M{"_id": o.ConflictID, "org_id": org, "state": "open"}, bson.M{"$set": bson.M{"state": "resolved", "resolution": bson.M{"choice": "accept_new", "note": o.Note, "by": o.User, "at": now, "result_version_id": v.ID}, "updated_at": now}, "$inc": bson.M{"revision": int64(1)}}); e != nil {
				return e
			}
		}
		if e = a.outboxTx(sc, org, "upsert", v.ID, rec.ID, v.ContentHash); e != nil {
			return e
		}
		for _, sid := range v.SupersedesMany {
			r, e2 := a.DB.Coll("knowledge_versions").UpdateOne(sc, bson.M{"_id": sid, "org_id": org, "status": "verified"}, bson.M{"$set": bson.M{"status": "superseded", "superseded_by": v.ID}})
			if e2 != nil {
				return e2
			}
			if r.MatchedCount == 1 {
				if e2 = a.outboxTx(sc, org, "delete", sid, "", ""); e2 != nil {
					return e2
				}
			}
		}
		if oldID != "" {
			if e = a.outboxTx(sc, org, "delete", oldID, rec.ID, ""); e != nil {
				return e
			}
			if e = a.AppendEvent(sc, org, "knowledge.superseded", Subject{"knowledge_record", rec.ID}, bson.M{"record_id": rec.ID, "old": oldID, "new": v.ID}, nil, nil, o.Actor == knowledge.ActorPolicy); e != nil {
				return e
			}
		}
		rev := cur.Revision + 1
		if e = a.AppendEvent(sc, org, "knowledge.verified", Subject{"knowledge_record", rec.ID}, bson.M{"record_id": rec.ID, "version_id": v.ID, "mode": mode}, &rev, nil, o.Actor == knowledge.ActorPolicy); e != nil {
			return e
		}
		out.Outcome = "verified"
		return a.ensureJobTx(sc, org, "wiki_maintain", "wiki:"+org, 1000, bson.M{})
	})
	if err != nil {
		return out, err
	}
	a.Committed()
	if out.Outcome == "conflicted" {
		c := problem("knowledge_conflict_open")
		c.Current = a.conflictDTO(*out.Conflict)
		return out, c
	}
	return out, nil
}

func appendUniq(xs []string, x string) []string {
	if x == "" || contains(xs, x) {
		return xs
	}
	return append(xs, x)
}

func (a *App) conflictOpenError(ctx context.Context, org, versionID string) error {
	c := problem("knowledge_conflict_open")
	if d, err := findOne[KnowledgeConflictDoc](ctx, a.DB, "knowledge_conflicts", bson.M{"org_id": org, "state": "open", "version_ids": versionID}); err == nil {
		c.Current = a.conflictDTO(d)
	}
	return c
}

// outboxTx records a Wiki projection change in the caller's transaction (the job is the outbox consumer).
func (a *App) outboxTx(sc context.Context, org, op, versionID, recordID, hash string) error {
	seq, err := a.nextCounter(sc, "wob:"+org)
	if err != nil {
		return err
	}
	_, err = a.DB.Coll("wiki_outbox").InsertOne(sc, bson.M{"_id": newID("wob"), "org_id": org, "seq": seq, "op": op, "version_id": versionID, "record_id": recordID,
		"content_hash": hash, "state": "pending", "attempts": 0, "created_at": time.Now().UTC()})
	return err
}

// ---- reject, archive, pin, rollback ----

func (a *App) rejectVersion(ctx context.Context, org, kvr string, ifMatch int64, reason, user string) error {
	return a.DB.Txn(ctx, func(sc context.Context) error {
		v, err := a.knowledgeVersion(sc, org, kvr)
		if err != nil {
			return err
		}
		if v.Status != "draft" && v.Status != "conflicted" {
			return problem("knowledge_version_not_draft")
		}
		if err = knowledge.Transition(knowledge.Status(v.Status), knowledge.StatusArchived, knowledge.ActorUser); err != nil {
			return problem("knowledge_version_not_draft")
		}
		rec, err := revisionUpdate[recDTO](sc, a.DB, "knowledge_records", bson.M{"_id": v.RecordID, "org_id": org}, ifMatch, bson.M{})
		if err != nil {
			return err
		}
		if _, err = a.DB.Coll("knowledge_versions").UpdateOne(sc, bson.M{"_id": kvr}, bson.M{"$set": bson.M{"status": "archived", "archive_reason": "rejected", "archive_note": reason}}); err != nil {
			return err
		}
		now := time.Now().UTC()
		if _, err = a.DB.Coll("knowledge_conflicts").UpdateMany(sc, bson.M{"org_id": org, "state": "open", "version_ids": kvr}, bson.M{"$set": bson.M{"state": "resolved", "resolution": bson.M{"choice": "keep_existing", "note": reason, "by": user, "at": now, "result_version_id": ""}, "updated_at": now}, "$inc": bson.M{"revision": int64(1)}}); err != nil {
			return err
		}
		r2, _ := a.knowledgeRecord(sc, org, v.RecordID)
		if r2.VerifiedVersionID == nil {
			if _, err = a.DB.Coll("knowledge_records").UpdateOne(sc, bson.M{"_id": v.RecordID}, bson.M{"$set": bson.M{"status": "draft"}}); err != nil {
				return err
			}
		} else if r2.Status == "conflicted" {
			_, _ = a.DB.Coll("knowledge_records").UpdateOne(sc, bson.M{"_id": v.RecordID}, bson.M{"$set": bson.M{"status": "verified"}})
		}
		rev := rec.Rev
		return a.AppendEvent(sc, org, "knowledge.rejected", Subject{"knowledge_record", v.RecordID}, bson.M{"record_id": v.RecordID, "version_id": kvr}, &rev, nil, false)
	})
}

func (a *App) archiveRecord(ctx context.Context, org, recID string, ifMatch int64, reason string) (KnowledgeRecordDoc, error) {
	var out KnowledgeRecordDoc
	err := a.txnRetryJob(ctx, func(sc context.Context) error {
		cur, err := a.knowledgeRecord(sc, org, recID)
		if err != nil {
			return err
		}
		if !cur.Active {
			return problem("not_found")
		}
		out, err = revisionUpdate[KnowledgeRecordDoc](sc, a.DB, "knowledge_records", bson.M{"_id": recID, "org_id": org}, ifMatch, bson.M{"active": false, "status": "archived", "archive_reason": reason, "pinned": false, "critical": false})
		if err != nil {
			return err
		}
		if _, err = a.DB.Coll("knowledge_versions").UpdateMany(sc, bson.M{"org_id": org, "record_id": recID, "status": bson.M{"$in": []string{"draft", "conflicted", "verified"}}}, bson.M{"$set": bson.M{"status": "archived", "archive_reason": "record_archived"}}); err != nil {
			return err
		}
		if cur.VerifiedVersionID != nil {
			if err = a.outboxTx(sc, org, "delete", *cur.VerifiedVersionID, recID, ""); err != nil {
				return err
			}
			if err = a.ensureJobTx(sc, org, "wiki_maintain", "wiki:"+org, 1000, bson.M{}); err != nil {
				return err
			}
		}
		_, _ = a.DB.Coll("knowledge_conflicts").UpdateMany(sc, bson.M{"org_id": org, "state": "open", "record_ids": recID}, bson.M{"$set": bson.M{"state": "resolved", "resolution": bson.M{"choice": "archive_both", "note": "record archived", "by": "system", "at": time.Now().UTC(), "result_version_id": ""}}})
		rev := out.Revision
		return a.AppendEvent(sc, org, "knowledge.archived", Subject{"knowledge_record", recID}, bson.M{"record_id": recID}, &rev, nil, false)
	})
	if err == nil {
		a.Committed()
	}
	return out, err
}

type PinKnowledgeRequest struct {
	Pinned         bool     `json:"pinned"`
	Critical       bool     `json:"critical"`
	AppliesToRoles []string `json:"applies_to_roles"`
}

var packetRoles = []string{"architect", "worker", "verifier", "reviewer", "writer"}

func (a *App) pinRecord(ctx context.Context, org, recID string, ifMatch int64, req PinKnowledgeRequest) (KnowledgeRecordDoc, error) {
	var out KnowledgeRecordDoc
	err := a.DB.Txn(ctx, func(sc context.Context) error {
		cur, err := a.knowledgeRecord(sc, org, recID)
		if err != nil || !cur.Active {
			return problem("not_found")
		}
		if (req.Pinned || req.Critical) && cur.VerifiedVersionID == nil {
			return problem("knowledge_not_verified")
		}
		if req.Critical && cur.Type != "business" && cur.Type != "project" {
			return problem("knowledge_pin_type_invalid")
		}
		for _, r := range req.AppliesToRoles {
			if !contains(packetRoles, r) {
				return validation("applies_to_roles", "unknown role "+r)
			}
		}
		pinned := req.Pinned || req.Critical
		out, err = revisionUpdate[KnowledgeRecordDoc](sc, a.DB, "knowledge_records", bson.M{"_id": recID, "org_id": org}, ifMatch, bson.M{"pinned": pinned, "critical": req.Critical, "applies_to_roles": nonNilStrings(req.AppliesToRoles)})
		if err != nil {
			return err
		}
		kind := "knowledge.pinned"
		if !pinned {
			kind = "knowledge.unpinned"
		}
		rev := out.Revision
		return a.AppendEvent(sc, org, kind, Subject{"knowledge_record", recID}, bson.M{"record_id": recID, "critical": req.Critical}, &rev, nil, false)
	})
	if err == nil {
		a.Committed()
	}
	return out, err
}

// rollbackRecord creates version N+1 as a copy of an earlier verified version and verifies it through the normal gate.
// When the gate (current evidence) does not pass, the new version stays a draft and the gate is returned.
func (a *App) rollbackRecord(ctx context.Context, org, recID string, ifMatch int64, toID, note, user string) (verifyOut, KnowledgeVersionDoc, error) {
	var nv KnowledgeVersionDoc
	var out verifyOut
	err := a.DB.Txn(ctx, func(sc context.Context) error {
		rec, err := a.knowledgeRecord(sc, org, recID)
		if err != nil || !rec.Active {
			return problem("not_found")
		}
		target, err := a.knowledgeVersion(sc, org, toID)
		if err != nil || target.RecordID != recID || target.VerifiedAt == nil || (rec.VerifiedVersionID != nil && *rec.VerifiedVersionID == toID) {
			return problem("rollback_target_invalid")
		}
		if n, _ := a.DB.Coll("knowledge_versions").CountDocuments(sc, bson.M{"org_id": org, "record_id": recID, "status": bson.M{"$in": []string{"draft", "conflicted"}}}); n > 0 {
			return problem("knowledge_draft_exists")
		}
		claims := make([]knowledge.Claim, len(target.Claims))
		for i, c := range target.Claims {
			c.Verdict, c.VerdictBy = knowledge.VerdictUnchecked, ""
			claims[i] = c
		}
		nv = a.newVersionDoc(org, rec, rec.NextNumber, target.Title, target.Body, claims, target.ValidFrom, target.ValidUntil, knowledge.Origin{Kind: "rollback", Actor: user})
		nv.RollbackOf = toID
		if rec.VerifiedVersionID != nil {
			nv.Supersedes = *rec.VerifiedVersionID
		}
		if _, err = a.DB.Coll("knowledge_versions").InsertOne(sc, nv); err != nil {
			return err
		}
		upd, err := revisionUpdate[recDTO](sc, a.DB, "knowledge_records", bson.M{"_id": recID, "org_id": org}, ifMatch, bson.M{"current_version_id": nv.ID})
		if err != nil {
			return err
		}
		if _, err = a.DB.Coll("knowledge_records").UpdateOne(sc, bson.M{"_id": recID}, bson.M{"$inc": bson.M{"next_number": 1}}); err != nil {
			return err
		}
		rev := upd.Rev
		return a.AppendEvent(sc, org, "knowledge.version_created", Subject{"knowledge_record", recID}, bson.M{"record_id": recID, "version_id": nv.ID, "status": "draft", "origin": "rollback"}, &rev, nil, false)
	})
	if err != nil {
		return out, nv, err
	}
	a.Committed()
	out, err = a.verifyVersion(ctx, org, nv.ID, verifyOpts{Actor: knowledge.ActorUser, User: user, Note: note})
	var ae *AppError
	if errors.As(err, &ae) && (ae.Code == "knowledge_claims_unsupported" || ae.Code == "knowledge_conflict_open") {
		if out.Outcome == "" {
			out.Outcome = "draft"
		}
		return out, nv, nil
	}
	if err == nil {
		_ = a.AppendEvent(ctx, org, "knowledge.rolled_back", Subject{"knowledge_record", recID}, bson.M{"record_id": recID, "to": toID, "new": nv.ID}, nil, nil, false)
		a.Committed()
	}
	return out, nv, err
}

// ---- conflicts ----

type ResolveKnowledgeConflictRequest struct {
	Choice       string             `json:"choice"`
	Note         string             `json:"note"`
	Attestations []AttestationInput `json:"attestations"`
}
type AttestationInput struct {
	ClaimID string `json:"claim_id"`
	Text    string `json:"text"`
}

func attestMap(in []AttestationInput) map[string]string {
	m := map[string]string{}
	for _, x := range in {
		m[x.ClaimID] = x.Text
	}
	return m
}

func (a *App) resolveConflict(ctx context.Context, org, id string, ifMatch int64, req ResolveKnowledgeConflictRequest, user string) (KnowledgeConflictDoc, error) {
	c, err := findOne[KnowledgeConflictDoc](ctx, a.DB, "knowledge_conflicts", bson.M{"_id": id, "org_id": org})
	if err != nil {
		return c, err
	}
	if c.State != "open" {
		return c, problem("conflict_not_open")
	}
	if c.Revision != ifMatch {
		e := problem("revision_conflict")
		e.Current = a.conflictDTO(c)
		return c, e
	}
	if !contains([]string{"keep_existing", "accept_new", "archive_both"}, req.Choice) {
		return c, validation("choice", "must be keep_existing, accept_new or archive_both")
	}
	if req.Choice == "accept_new" && strings.TrimSpace(req.Note) == "" {
		return c, validation("note", "is required to accept the new version")
	}
	if len(req.Note) > 2000 {
		return c, validation("note", "must be at most 2000 characters")
	}
	// The candidate is the conflicted version of the pair; the others are existing verified versions.
	var cand KnowledgeVersionDoc
	var others []KnowledgeVersionDoc
	for _, vid := range c.VersionIDs {
		v, e := a.knowledgeVersion(ctx, org, vid)
		if e != nil {
			continue
		}
		if v.Status == "conflicted" || v.Status == "draft" {
			cand = v
		} else {
			others = append(others, v)
		}
	}
	if cand.ID == "" {
		return c, problem("conflict_not_open")
	}
	now := time.Now().UTC()
	resolve := func(sc context.Context, result string) error {
		_, e := a.DB.Coll("knowledge_conflicts").UpdateOne(sc, bson.M{"_id": id, "org_id": org, "state": "open"}, bson.M{"$set": bson.M{"state": "resolved", "resolution": bson.M{"choice": req.Choice, "note": req.Note, "by": user, "at": now, "result_version_id": result}, "updated_at": now}, "$inc": bson.M{"revision": int64(1)}})
		return e
	}
	switch req.Choice {
	case "keep_existing":
		err = a.DB.Txn(ctx, func(sc context.Context) error {
			if _, e := a.DB.Coll("knowledge_versions").UpdateOne(sc, bson.M{"_id": cand.ID}, bson.M{"$set": bson.M{"status": "archived", "archive_reason": "conflict_keep_existing"}}); e != nil {
				return e
			}
			a.settleRecordStatus(sc, cand.RecordID)
			if e := resolve(sc, ""); e != nil {
				return e
			}
			return a.AppendEvent(sc, org, "knowledge.conflict_resolved", Subject{"knowledge_conflict", id}, bson.M{"conflict_id": id, "choice": req.Choice}, nil, nil, false)
		})
	case "archive_both":
		err = a.txnRetryJob(ctx, func(sc context.Context) error {
			for _, v := range append([]KnowledgeVersionDoc{cand}, others...) {
				if _, e := a.DB.Coll("knowledge_versions").UpdateOne(sc, bson.M{"_id": v.ID}, bson.M{"$set": bson.M{"status": "archived", "archive_reason": "conflict_archive_both"}}); e != nil {
					return e
				}
				if v.Status == "verified" {
					if e := a.outboxTx(sc, org, "delete", v.ID, v.RecordID, ""); e != nil {
						return e
					}
				}
				_, _ = a.DB.Coll("knowledge_records").UpdateOne(sc, bson.M{"_id": v.RecordID}, bson.M{"$set": bson.M{"verified_version_id": nil, "status": "archived", "active": false, "archive_reason": "conflict_archive_both"}, "$inc": bson.M{"revision": int64(1)}})
			}
			if e := resolve(sc, ""); e != nil {
				return e
			}
			if e := a.ensureJobTx(sc, org, "wiki_maintain", "wiki:"+org, 1000, bson.M{}); e != nil {
				return e
			}
			return a.AppendEvent(sc, org, "knowledge.conflict_resolved", Subject{"knowledge_conflict", id}, bson.M{"conflict_id": id, "choice": req.Choice}, nil, nil, false)
		})
	default: // accept_new
		_, err = a.verifyVersion(ctx, org, cand.ID, verifyOpts{Actor: knowledge.ActorUser, User: user, Note: req.Note, Attest: attestMap(req.Attestations), SkipConflicts: true, ConflictID: id})
		if err == nil {
			_ = a.AppendEvent(ctx, org, "knowledge.conflict_resolved", Subject{"knowledge_conflict", id}, bson.M{"conflict_id": id, "choice": req.Choice}, nil, nil, false)
		}
	}
	if err != nil {
		return c, err
	}
	a.Committed()
	return findOne[KnowledgeConflictDoc](ctx, a.DB, "knowledge_conflicts", bson.M{"_id": id, "org_id": org})
}

// settleRecordStatus restores a record's status after a candidate version left the conflicted state.
func (a *App) settleRecordStatus(sc context.Context, recID string) {
	r, err := findOne[KnowledgeRecordDoc](sc, a.DB, "knowledge_records", bson.M{"_id": recID})
	if err != nil {
		return
	}
	st := "draft"
	if r.VerifiedVersionID != nil {
		st = "verified"
	}
	_, _ = a.DB.Coll("knowledge_records").UpdateOne(sc, bson.M{"_id": recID}, bson.M{"$set": bson.M{"status": st}, "$inc": bson.M{"revision": int64(1)}})
}

// ---- search (the Mongo fallback; the Wiki index adds its own searcher in knowledge_search.go) ----

type KnowledgeHit struct {
	VersionID string  `json:"version_id"`
	RecordID  string  `json:"record_id"`
	Title     string  `json:"title"`
	Type      string  `json:"type"`
	ScopeKey  string  `json:"scope_key"`
	Score     float64 `json:"score"`
	Snippet   string  `json:"snippet"`
}

func (a *App) mongoSearch(ctx context.Context, org, q, typ, project string, limit int) ([]KnowledgeHit, error) {
	filter := bson.M{"org_id": org, "status": "verified", "$text": bson.M{"$search": q}}
	if typ != "" {
		filter["type"] = typ
	}
	if project != "" {
		filter["project_id"] = project
	}
	cur, err := a.DB.Coll("knowledge_versions").Find(ctx, filter, options.Find().SetProjection(bson.M{"score": bson.M{"$meta": "textScore"}}).SetSort(bson.D{{Key: "score", Value: bson.M{"$meta": "textScore"}}, {Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	var docs []struct {
		KnowledgeVersionDoc `bson:",inline"`
		Score               float64 `bson:"score"`
	}
	if err = cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	hits := []KnowledgeHit{}
	for _, d := range docs {
		snip := d.Title
		if len(d.Claims) > 0 {
			snip += ": " + d.Claims[0].Text
		}
		if len(snip) > 200 {
			snip = snip[:200]
		}
		hits = append(hits, KnowledgeHit{VersionID: d.ID, RecordID: d.RecordID, Title: d.Title, Type: d.Type, ScopeKey: d.ScopeKey, Score: d.Score, Snippet: snip})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	return hits, nil
}
