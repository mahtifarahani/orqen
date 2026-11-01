package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"orqen/internal/canon"
	"orqen/internal/policy"
	"strconv"
	"time"
)

type PolicyScope struct {
	Kind string `bson:"kind" json:"kind"`
	ID   string `bson:"id" json:"id"`
}
type PolicyDraftDoc struct {
	ID            string      `bson:"_id" json:"_id"`
	OrgID         string      `bson:"org_id" json:"org_id"`
	Scope         PolicyScope `bson:"scope" json:"scope"`
	Overrides     bson.M      `bson:"overrides" json:"overrides"`
	BaseVersionID *string     `bson:"base_version_id" json:"base_version_id"`
	NextNumber    int64       `bson:"next_number" json:"next_number"`
	Revision      int64       `bson:"revision" json:"revision"`
	CreatedAt     time.Time   `bson:"created_at" json:"created_at"`
	UpdatedAt     time.Time   `bson:"updated_at" json:"updated_at"`
}
type PolicyVersionDoc struct {
	ID              string    `bson:"_id" json:"_id"`
	OrgID           string    `bson:"org_id" json:"org_id"`
	Kind            string    `bson:"kind" json:"kind"`
	ScopeID         string    `bson:"scope_id" json:"scope_id"`
	Number          int64     `bson:"number" json:"number"`
	SchemaVersion   int       `bson:"schema_version" json:"schema_version"`
	DefaultsVersion int       `bson:"defaults_version" json:"defaults_version"`
	Overrides       bson.M    `bson:"overrides" json:"overrides"`
	Resolved        bson.M    `bson:"resolved" json:"resolved"`
	Hash            string    `bson:"hash" json:"hash"`
	CreatedAt       time.Time `bson:"created_at" json:"created_at"`
	SourceRefs      bson.M    `bson:"source_refs" json:"source_refs"`
}

func nonNil(m bson.M) bson.M {
	if m == nil {
		return bson.M{}
	}
	return m
}

// DTO leaves differs_from_published false; the handler computes it against the latest published version.
func (d PolicyDraftDoc) DTO() bson.M {
	return bson.M{"overrides": nonNil(d.Overrides), "revision": d.Revision, "base_version_id": d.BaseVersionID, "differs_from_published": false}
}
func (d PolicyVersionDoc) DTO() bson.M {
	var resolved, refs any
	if d.Resolved != nil {
		resolved = d.Resolved
	}
	if d.SourceRefs != nil {
		refs = d.SourceRefs
	}
	return bson.M{"id": d.ID, "kind": d.Kind, "scope_id": d.ScopeID, "number": d.Number, "schema_version": d.SchemaVersion, "defaults_version": d.DefaultsVersion, "overrides": nonNil(d.Overrides), "resolved": resolved, "hash": d.Hash, "created_at": timestamp(d.CreatedAt), "source_refs": refs}
}

type PutPolicyDraftRequest struct {
	Overrides map[string]any `json:"overrides"`
}

func draftID(org string, s PolicyScope) string { return org + ":" + s.Kind + ":" + s.ID }
func (a *App) latestPolicyVersion(ctx context.Context, org string, s PolicyScope) (*PolicyVersionDoc, error) {
	docs, err := findMany[PolicyVersionDoc](ctx, a.DB, "policy_versions", bson.M{"org_id": org, "kind": s.Kind, "scope_id": s.ID}, bson.D{{Key: "number", Value: -1}}, 1)
	if err != nil || len(docs) == 0 {
		return nil, err
	}
	return &docs[0], nil
}
func (a *App) policyDraft(ctx context.Context, org string, s PolicyScope) (PolicyDraftDoc, error) {
	d, err := findOne[PolicyDraftDoc](ctx, a.DB, "policy_drafts", bson.M{"_id": draftID(org, s), "org_id": org})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return PolicyDraftDoc{ID: draftID(org, s), OrgID: org, Scope: s, Overrides: bson.M{}, NextNumber: 1}, nil
	}
	return d, err
}
func (a *App) draftResponse(ctx context.Context, w http.ResponseWriter, status int, d PolicyDraftDoc) error {
	latest, err := a.latestPolicyVersion(ctx, d.OrgID, d.Scope)
	if err != nil {
		return err
	}
	dto := d.DTO()
	published := bson.M{}
	if latest != nil {
		published = nonNil(latest.Overrides)
	}
	dto["differs_from_published"] = canon.JSON(nonNil(d.Overrides)) != canon.JSON(published)
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, status, dto)
	return nil
}

type scopedEndpoint func(w http.ResponseWriter, r *http.Request, s PolicyScope) error

// scoped resolves the policy layer from the route: the organization, or an existing project.
func (a *App) scoped(project bool, f scopedEndpoint) endpoint {
	return func(w http.ResponseWriter, r *http.Request) error {
		org := r.PathValue("orgId")
		if !project {
			return f(w, r, PolicyScope{Kind: "org", ID: org})
		}
		id := r.PathValue("prjId")
		if _, err := a.project(r.Context(), org, id); err != nil {
			return err
		}
		return f(w, r, PolicyScope{Kind: "project", ID: id})
	}
}
func layerOf(s PolicyScope) string {
	if s.Kind == "project" {
		return policy.LayerProject
	}
	return policy.LayerOrg
}
func fieldErrors(es []policy.FieldError) error {
	out := make([]FieldError, len(es))
	for i, e := range es {
		out[i] = FieldError{e.Field, e.Message}
	}
	return validationErrors(out)
}
func (a *App) getPolicySchema(w http.ResponseWriter, r *http.Request) error {
	items := []bson.M{}
	for _, k := range policy.Registry {
		item := bson.M{"key": k.Key, "type": k.Type, "default": k.Default, "locked": k.Locked, "project_overridable": k.ProjectOverridable}
		switch k.Type {
		case "enum":
			item["enum"] = k.Enum
		case "int":
			item["min"], item["max"] = k.Min, k.Max
		}
		items = append(items, item)
	}
	writeJSON(w, 200, items)
	return nil
}
func (a *App) getPolicyDraft(w http.ResponseWriter, r *http.Request, s PolicyScope) error {
	d, err := a.policyDraft(r.Context(), r.PathValue("orgId"), s)
	if err != nil {
		return err
	}
	return a.draftResponse(r.Context(), w, 200, d)
}
func (a *App) putPolicyDraft(w http.ResponseWriter, r *http.Request, s PolicyScope) error {
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	var req PutPolicyDraftRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if req.Overrides == nil {
		return validation("overrides", "is required")
	}
	overrides, es := policy.ValidateOverrides(layerOf(s), req.Overrides)
	if err = fieldErrors(es); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	var doc PolicyDraftDoc
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		now := time.Now().UTC()
		if expected == 0 {
			doc = PolicyDraftDoc{ID: draftID(org, s), OrgID: org, Scope: s, Overrides: bson.M(overrides), NextNumber: 1, Revision: 1, CreatedAt: now, UpdatedAt: now}
			if _, e := a.DB.Coll("policy_drafts").InsertOne(ctx, doc); e != nil {
				if mongo.IsDuplicateKeyError(e) {
					return errDraftExists // the transaction is aborted; the current draft is read outside it
				}
				return e
			}
		} else {
			e := a.DB.Coll("policy_drafts").FindOneAndUpdate(ctx, bson.M{"_id": draftID(org, s), "org_id": org, "revision": expected}, bson.M{"$set": bson.M{"overrides": bson.M(overrides), "updated_at": now}, "$inc": bson.M{"revision": int64(1)}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&doc)
			if errors.Is(e, mongo.ErrNoDocuments) {
				return a.draftConflict(ctx, org, s)
			}
			if e != nil {
				return e
			}
		}
		return a.AppendEvent(ctx, org, "policy.draft_updated", Subject{"policy", draftID(org, s)}, bson.M{"scope_kind": s.Kind, "scope_id": s.ID}, &doc.Revision, nil, false)
	})
	if errors.Is(err, errDraftExists) {
		return a.draftConflict(r.Context(), org, s)
	}
	if err != nil {
		return err
	}
	a.Committed()
	return a.draftResponse(r.Context(), w, 200, doc)
}

var errDraftExists = errors.New("policy draft exists")

func (a *App) draftConflict(ctx context.Context, org string, s PolicyScope) error {
	current, err := a.policyDraft(ctx, org, s)
	if err != nil {
		return err
	}
	conflict := problem("revision_conflict")
	conflict.Current = current.DTO()
	return conflict
}
func (a *App) publishPolicy(w http.ResponseWriter, r *http.Request, s PolicyScope) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	org := r.PathValue("orgId")
	route := "POST /api/v1/orgs/{orgId}/policy/publish"
	params := map[string]string{"orgId": org, "if_match": strconv.FormatInt(expected, 10)}
	if s.Kind == "project" {
		route = "POST /api/v1/orgs/{orgId}/projects/{prjId}/policy/publish"
		params["prjId"] = s.ID
	}
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, params, nil), func(ctx context.Context) (Output, error) {
		id := newID("pol")
		now := time.Now().UTC()
		var draft PolicyDraftDoc
		if expected == 0 {
			draft = PolicyDraftDoc{ID: draftID(org, s), OrgID: org, Scope: s, Overrides: bson.M{}, BaseVersionID: &id, NextNumber: 2, Revision: 1, CreatedAt: now, UpdatedAt: now}
			if _, err := a.DB.Coll("policy_drafts").InsertOne(ctx, draft); err != nil {
				if mongo.IsDuplicateKeyError(err) {
					return Output{}, problem("revision_conflict") // aborted transaction: no current document in the body
				}
				return Output{}, err
			}
		} else {
			// The counter lives on the draft: number = next_number - 1 after the increment, with no max+1 race.
			err := a.DB.Coll("policy_drafts").FindOneAndUpdate(ctx, bson.M{"_id": draftID(org, s), "org_id": org, "revision": expected}, bson.M{"$inc": bson.M{"next_number": int64(1), "revision": int64(1)}, "$set": bson.M{"base_version_id": id, "updated_at": now}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&draft)
			if errors.Is(err, mongo.ErrNoDocuments) {
				return Output{}, a.draftConflict(ctx, org, s)
			}
			if err != nil {
				return Output{}, err
			}
		}
		overrides, es := policy.ValidateOverrides(layerOf(s), draft.Overrides)
		if err := fieldErrors(es); err != nil {
			return Output{}, err
		}
		v := PolicyVersionDoc{ID: id, OrgID: org, Kind: s.Kind, ScopeID: s.ID, Number: draft.NextNumber - 1, SchemaVersion: policy.SchemaVersion, DefaultsVersion: policy.ProductDefaultsVersion, Overrides: bson.M(overrides), Hash: sha256Hex(canon.JSON(overrides)), CreatedAt: now}
		if _, err := a.DB.Coll("policy_versions").InsertOne(ctx, v); err != nil {
			return Output{}, err
		}
		if err := a.AppendEvent(ctx, org, "policy.published", Subject{"policy", id}, bson.M{"scope_kind": s.Kind, "scope_id": s.ID, "number": v.Number, "version_id": id}, &draft.Revision, nil, false); err != nil {
			return Output{}, err
		}
		return Output{201, v.DTO(), map[string]string{"etag": etag(draft.Revision)}}, nil
	})
}
func (a *App) listPolicyVersions(w http.ResponseWriter, r *http.Request, s PolicyScope) error {
	limit, after, err := pageQuery(r)
	if err != nil {
		return err
	}
	filter := bson.M{"org_id": r.PathValue("orgId"), "kind": s.Kind, "scope_id": s.ID}
	if after != "" {
		n, err := strconv.ParseInt(after, 10, 64)
		if err != nil {
			return validation("cursor", "invalid cursor")
		}
		filter["number"] = bson.M{"$lt": n}
	}
	docs, err := findMany[PolicyVersionDoc](r.Context(), a.DB, "policy_versions", filter, bson.D{{Key: "number", Value: -1}}, limit+1)
	if err != nil {
		return err
	}
	var next any
	if len(docs) > int(limit) {
		docs = docs[:limit]
		next = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprint(docs[len(docs)-1].Number)))
	}
	items := []bson.M{}
	for _, d := range docs {
		items = append(items, d.DTO())
	}
	writeJSON(w, 200, bson.M{"items": items, "next_cursor": next})
	return nil
}
func (a *App) getPolicyVersion(w http.ResponseWriter, r *http.Request, s PolicyScope) error {
	id := r.PathValue("polId")
	if !validID("pol", id) {
		return problem("not_found")
	}
	d, err := findOne[PolicyVersionDoc](r.Context(), a.DB, "policy_versions", bson.M{"_id": id, "org_id": r.PathValue("orgId"), "kind": s.Kind, "scope_id": s.ID})
	if err != nil {
		return err
	}
	writeJSON(w, 200, d.DTO())
	return nil
}
func toLayer(v *PolicyVersionDoc) *policy.Version {
	if v == nil {
		return nil
	}
	return &policy.Version{ID: v.ID, Number: v.Number, Overrides: v.Overrides}
}

// effectivePolicy resolves from published versions only; unpublished drafts never apply.
func (a *App) effectivePolicy(ctx context.Context, org string, project string) (policy.Effective, *PolicyVersionDoc, *PolicyVersionDoc, error) {
	orgV, err := a.latestPolicyVersion(ctx, org, PolicyScope{Kind: "org", ID: org})
	if err != nil {
		return policy.Effective{}, nil, nil, err
	}
	var prjV *PolicyVersionDoc
	if project != "" {
		if prjV, err = a.latestPolicyVersion(ctx, org, PolicyScope{Kind: "project", ID: project}); err != nil {
			return policy.Effective{}, nil, nil, err
		}
	}
	return policy.Resolve(toLayer(orgV), toLayer(prjV)), orgV, prjV, nil
}
func effectiveDTO(e policy.Effective) bson.M {
	items := []bson.M{}
	for _, x := range e.Entries {
		src := bson.M{"layer": x.Source.Layer, "version_id": nil, "number": x.Source.Number}
		if x.Source.VersionID != "" {
			src["version_id"] = x.Source.VersionID
		}
		items = append(items, bson.M{"key": x.Key, "value": x.Value, "source": src, "default": x.Default, "locked": x.Locked})
	}
	return bson.M{"items": items, "hash": e.Hash}
}
func (a *App) getPolicyEffective(w http.ResponseWriter, r *http.Request, s PolicyScope) error {
	project := ""
	if s.Kind == "project" {
		project = s.ID
	}
	e, _, _, err := a.effectivePolicy(r.Context(), r.PathValue("orgId"), project)
	if err != nil {
		return err
	}
	writeJSON(w, 200, effectiveDTO(e))
	return nil
}
