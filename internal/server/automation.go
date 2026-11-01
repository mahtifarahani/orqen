package server

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"orqen/internal/policy"
	"time"
)

type BindingRequest struct {
	VersionID *string `json:"version_id"`
}

func (a *App) putProfileBinding(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req BindingRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	// A published version never reverts, so it is checked with a plain read before the transaction: a snapshot
	// read inside it can lag the majority commit point under load and reject a version that was just published.
	var v ModelProfileVersionDoc
	if req.VersionID != nil {
		v, err = findOne[ModelProfileVersionDoc](r.Context(), a.DB, "model_profile_versions", bson.M{"_id": *req.VersionID, "org_id": org})
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			return err
		}
		if err != nil || v.State != "published" {
			return &AppError{Code: "version_not_published", Status: 422, Detail: "the profile version is not published", Errors: []FieldError{{"version_id", "must be a published version of this organization"}}}
		}
	}
	return a.updateProject(w, r, expected, "profile_binding", func(ctx context.Context, cur ProjectDoc) (bson.M, error) {
		if req.VersionID == nil {
			return bson.M{"profile_binding": nil}, nil
		}
		return bson.M{"profile_binding": ProfileBinding{ProfileID: v.ProfileID, VersionID: v.ID, BoundAt: time.Now().UTC()}}, nil
	})
}

// snapshotResolved stores the effective policy at enable time as an immutable resolved version.
func (a *App) snapshotResolved(ctx context.Context, org, project string) (string, error) {
	eff, orgV, prjV, err := a.effectivePolicy(ctx, org, project)
	if err != nil {
		return "", err
	}
	var counter struct {
		Seq int64 `bson:"seq"`
	}
	if err = a.DB.Coll("counters").FindOneAndUpdate(ctx, bson.M{"_id": "polres:" + project}, bson.M{"$inc": bson.M{"seq": int64(1)}}, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&counter); err != nil {
		return "", err
	}
	resolved := bson.M{}
	for _, e := range eff.Entries {
		src := bson.M{"layer": e.Source.Layer, "number": e.Source.Number}
		if e.Source.VersionID != "" {
			src["version_id"] = e.Source.VersionID
		}
		resolved[e.Key] = bson.M{"value": e.Value, "source": src}
	}
	refs := bson.M{"org_version_id": nil, "project_version_id": nil}
	if orgV != nil {
		refs["org_version_id"] = orgV.ID
	}
	if prjV != nil {
		refs["project_version_id"] = prjV.ID
	}
	v := PolicyVersionDoc{ID: newID("pol"), OrgID: org, Kind: "resolved", ScopeID: project, Number: counter.Seq, SchemaVersion: policy.SchemaVersion, DefaultsVersion: policy.ProductDefaultsVersion, Overrides: bson.M{}, Resolved: resolved, Hash: eff.Hash, CreatedAt: time.Now().UTC(), SourceRefs: refs}
	_, err = a.DB.Coll("policy_versions").InsertOne(ctx, v)
	return v.ID, err
}
func (a *App) enableAutomation(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("prjId")
	route := "POST /api/v1/orgs/{orgId}/projects/{prjId}/automation/enable"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "prjId": id, "if_match": fmt.Sprint(expected)}, nil), func(ctx context.Context) (Output, error) {
		cur, err := a.project(ctx, org, id)
		if err != nil {
			return Output{}, err
		}
		if cur.Revision != expected {
			conflict := problem("revision_conflict")
			conflict.Current = cur.DTO()
			return Output{}, conflict
		}
		res, err := a.readinessResults(ctx, org, []ProjectDoc{cur})
		if err != nil {
			return Output{}, err
		}
		if !res[0].Ready {
			e := problem("project_not_ready")
			for _, b := range res[0].Blockers {
				e.Errors = append(e.Errors, FieldError{b.Code, b.Detail})
			}
			return Output{}, e
		}
		pol, err := a.snapshotResolved(ctx, org, id)
		if err != nil {
			return Output{}, err
		}
		now := time.Now().UTC()
		doc, err := revisionUpdate[ProjectDoc](ctx, a.DB, "projects", prjFilter(org, id), expected, bson.M{"automation_enabled": true, "enabled_at": now, "enabled_policy_version_id": pol, "enabled_profile_version_id": cur.ProfileBinding.VersionID})
		if err != nil {
			return Output{}, err
		}
		if err = a.AppendEvent(ctx, org, "project.automation_enabled", Subject{"project", id}, bson.M{"policy_version_id": pol, "profile_version_id": cur.ProfileBinding.VersionID}, &doc.Revision, nil, false); err != nil {
			return Output{}, err
		}
		dto := doc.DTO()
		dto["state"], dto["blocker_count"] = "enabled", 0
		return Output{200, dto, map[string]string{"etag": etag(doc.Revision)}}, nil
	})
}
func (a *App) disableAutomation(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("prjId")
	var doc ProjectDoc
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		var e error
		if doc, e = revisionUpdate[ProjectDoc](ctx, a.DB, "projects", prjFilter(org, id), expected, bson.M{"automation_enabled": false, "enabled_at": nil, "enabled_policy_version_id": nil, "enabled_profile_version_id": nil}); e != nil {
			return e
		}
		return a.AppendEvent(ctx, org, "project.automation_disabled", Subject{"project", id}, bson.M{}, &doc.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.Committed()
	return a.projectResponse(r.Context(), w, 200, doc)
}
