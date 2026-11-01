package server

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"orqen/internal/catherd"
	"strings"
	"time"
)

type RollbackRequest struct {
	VersionID string `json:"version_id"`
}

// beginPublish creates the next version in state publishing and queues the job, all in the caller's transaction.
// prepare returns the document to publish and any extra profile fields to set (rollback resets the draft).
func (a *App) beginPublish(ctx context.Context, org, id string, expected int64, prepare func(ModelProfileDoc) (bson.M, ProfileMeta, VersionSource, bson.M, error)) (Output, error) {
	prof, err := a.profile(ctx, org, id)
	if err != nil {
		return Output{}, err
	}
	doc, meta, src, extra, err := prepare(prof)
	if err != nil {
		return Output{}, err
	}
	if err = a.sweepAbandoned(ctx, org, id); err != nil {
		return Output{}, err
	}
	set := bson.M{"updated_at": time.Now().UTC()}
	for k, v := range extra {
		set[k] = v
	}
	// next_number is incremented in the same transaction as the version insert (PLAN-S2-GO A.4).
	var updated ModelProfileDoc
	err = a.DB.Coll("model_profiles").FindOneAndUpdate(ctx, bson.M{"_id": id, "org_id": org, "active": true, "revision": expected}, bson.M{"$set": set, "$inc": bson.M{"next_number": int64(1), "revision": int64(1)}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&updated)
	if errors.Is(err, mongo.ErrNoDocuments) {
		conflict := problem("revision_conflict")
		conflict.Current = prof.DTO()
		return Output{}, conflict
	}
	if err != nil {
		return Output{}, err
	}
	host, err := a.orgHostE(ctx, org)
	if err != nil {
		return Output{}, err
	}
	number := updated.NextNumber - 1
	out, err := a.startOperation(ctx, org, "profile_publish", Subject{"model_profile", id}, "profile_publish:"+id, 5, bson.M{"profile_id": id})
	if err != nil {
		return Output{}, err
	}
	opID := out.Body.(bson.M)["id"].(string)
	v := ModelProfileVersionDoc{ID: newID("mpv"), OrgID: org, ProfileID: id, Number: number, State: "publishing", Doc: doc, Meta: meta, DocHash: docHash(plain(doc)), CatherdName: fmt.Sprintf("o-%s-v%d", prof.Slug, number), CatherdVersion: a.CatherdRT.Version(), Host: host, Source: src, OperationID: opID, CreatedAt: time.Now().UTC()}
	if _, err = a.DB.Coll("model_profile_versions").InsertOne(ctx, v); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return Output{}, problem("revision_conflict") // another publish is in flight for this profile
		}
		return Output{}, err
	}
	if err = a.AppendEvent(ctx, org, "model_profile_version.publishing", Subject{"model_profile", id}, bson.M{"version_id": v.ID, "number": number, "catherd_name": v.CatherdName}, &updated.Revision, &opID, false); err != nil {
		return Output{}, err
	}
	return out, nil
}
func (a *App) publishProfile(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("mpfId")
	route := "POST /api/v1/orgs/{orgId}/model-profiles/{mpfId}/publish"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "mpfId": id, "if_match": fmt.Sprint(expected)}, nil), func(ctx context.Context) (Output, error) {
		if err := a.catherdGate(); err != nil {
			return Output{}, err
		}
		return a.beginPublish(ctx, org, id, expected, func(p ModelProfileDoc) (bson.M, ProfileMeta, VersionSource, bson.M, error) {
			lv := p.LastValidation
			if lv == nil || !lv.Valid || lv.DraftHash != profileHash(p.Draft.Doc, p.Draft.Meta) || lv.CatherdVersion != a.CatherdRT.Version() || lv.hostOrDefault() != a.orgHost(ctx, org) {
				return nil, ProfileMeta{}, VersionSource{}, nil, problem("profile_not_validated")
			}
			return p.Draft.Doc, p.Draft.Meta, VersionSource{Kind: "draft"}, nil, nil
		})
	})
}
func (a *App) rollbackProfile(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req RollbackRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("mpfId")
	route := "POST /api/v1/orgs/{orgId}/model-profiles/{mpfId}/rollback"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "mpfId": id, "if_match": fmt.Sprint(expected)}, req), func(ctx context.Context) (Output, error) {
		if err := a.catherdGate(); err != nil {
			return Output{}, err
		}
		return a.beginPublish(ctx, org, id, expected, func(p ModelProfileDoc) (bson.M, ProfileMeta, VersionSource, bson.M, error) {
			target, err := findOne[ModelProfileVersionDoc](ctx, a.DB, "model_profile_versions", bson.M{"_id": req.VersionID, "org_id": org, "profile_id": id})
			if errors.Is(err, mongo.ErrNoDocuments) || err == nil && target.State != "published" {
				return nil, ProfileMeta{}, VersionSource{}, nil, &AppError{Code: "version_not_published", Status: 422, Detail: "the profile version is not published", Errors: []FieldError{{"version_id", "must be a published version of this profile"}}}
			}
			if err != nil {
				return nil, ProfileMeta{}, VersionSource{}, nil, err
			}
			// The job validates through catherd, so the draft precondition is skipped; the draft becomes this document.
			return target.Doc, target.Meta, VersionSource{Kind: "rollback", FromVersionID: &target.ID}, bson.M{"draft": ProfileDraft{Doc: target.Doc, Meta: target.Meta, UpdatedAt: time.Now().UTC()}}, nil
		})
	})
}

// finishPublishFailed records a catherd outcome that is a result, not a job failure: the version fails and the job succeeds.
func (a *App) finishPublishFailed(ctx context.Context, job JobDoc, lease Lease, v ModelProfileVersionDoc, code, message string, issues []catherd.Issue) error {
	return a.fenced(ctx, lease, func(sc context.Context) error {
		f := VersionFailure{Code: code, Message: message, Issues: issuesOrEmpty(issues)}
		if _, err := a.DB.Coll("model_profile_versions").UpdateOne(sc, bson.M{"_id": v.ID, "state": "publishing"}, bson.M{"$set": bson.M{"state": "failed", "failure": f}}); err != nil {
			return err
		}
		if err := a.transitionOperation(sc, job, "failed", bson.M{"code": code, "message": message}); err != nil {
			return err
		}
		if err := a.AppendEvent(sc, job.OrgID, "model_profile_version.failed", Subject{"model_profile", v.ProfileID}, bson.M{"version_id": v.ID, "code": code}, nil, job.OperationID, true); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
}

// runProfilePublish builds the version as its own immutable catherd profile (o-<slug>-v<n>) in the organization's home.
// Invariant: a published version always has a catherd profile whose raw content hash equals doc_hash at publish time;
// o-* profiles exist only for published or publishing versions (the orphan sweep enforces it). Orqen never makes a
// profile active or bound in catherd.
func (a *App) runProfilePublish(ctx context.Context, job JobDoc, lease Lease) error {
	if job.OperationID == nil {
		return &JobFailure{Code: "no_operation", Message: "job has no operation"}
	}
	id, ok := job.Payload["profile_id"].(string)
	if !ok || !validID("mpf", id) {
		return &JobFailure{Code: "bad_payload", Message: "missing profile_id"}
	}
	if err := a.fenced(ctx, lease, func(sc context.Context) error { return a.transitionOperation(sc, job, "running", nil) }); err != nil {
		return err
	}
	v, err := findOne[ModelProfileVersionDoc](ctx, a.DB, "model_profile_versions", bson.M{"org_id": job.OrgID, "profile_id": id, "operation_id": *job.OperationID})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return &JobFailure{Code: "version_missing", Message: "the version to publish no longer exists"}
	}
	if err != nil {
		return err
	}
	if v.State != "publishing" {
		return a.fenced(ctx, lease, func(sc context.Context) error { return a.completeJob(sc, lease) })
	}
	fail := func(code, msg string, issues []catherd.Issue) error {
		return a.finishPublishFailed(ctx, job, lease, v, code, msg, issues)
	}
	if err = a.catherdReady(); err != nil {
		return fail("catherd_unavailable", "catherd is not available", nil)
	}
	env, err := a.catherdEnv(job.OrgID)
	if err != nil {
		return err
	}
	lock := a.orgLock(job.OrgID)
	lock.Lock()
	defer lock.Unlock()
	// Catherd outcomes are results (the version fails); only Mongo and file system errors fail the job.
	result := func(err error) error {
		var ce *catherd.Error
		if errors.As(err, &ce) {
			code := "catherd_failed"
			if ce.Code == catherd.CodeUnavailable {
				code = "catherd_unavailable"
			}
			return fail(code, strings.TrimSpace(ce.Code+": "+ce.Message), nil)
		}
		return err
	}
	rows, err := a.Catherd.ListProfiles(ctx, env)
	if err != nil {
		return result(err)
	}
	if err = a.sweepOrphans(ctx, env, job.OrgID, rows); err != nil {
		return result(err)
	}
	doc := plain(v.Doc)
	host := v.Host
	if host == "" {
		host = catherd.HostCodex
	}
	sec, err := a.secretEnvFor(ctx, job.OrgID, isolationEnvsFor(doc))
	if err != nil {
		return err
	}
	exists := false
	for _, r := range rows {
		exists = exists || r.Name == v.CatherdName
	}
	built := false
	if exists {
		raw, err := a.Catherd.ShowRaw(ctx, env, host, v.CatherdName)
		if err != nil {
			return result(err)
		}
		delete(raw, "name")
		if docHash(raw) == v.DocHash {
			built = true // a previous attempt finished the catherd side; finalize without recreating
		} else if err = a.Catherd.Remove(ctx, env, v.CatherdName); err != nil {
			return result(err)
		}
	}
	if !built {
		refused, errs, _, serr := a.stageDoc(ctx, env, job.OrgID, host, v.CatherdName, doc)
		if serr != nil {
			a.removeBestEffort(env, v.CatherdName)
			return result(serr)
		}
		if len(refused) > 0 {
			a.removeBestEffort(env, v.CatherdName)
			return fail("catherd_refused", "catherd refused the profile", refused)
		}
		if len(errs) > 0 {
			a.removeBestEffort(env, v.CatherdName)
			return fail("catherd_invalid", "catherd reports the profile invalid", errs)
		}
		raw, err := a.Catherd.ShowRaw(ctx, env, host, v.CatherdName)
		if err != nil {
			return result(err)
		}
		delete(raw, "name")
		if docHash(raw) != v.DocHash {
			a.removeBestEffort(env, v.CatherdName)
			return fail("readback_mismatch", "catherd stored a different document than was sent", nil)
		}
	}
	if a.profileHook != nil {
		if err = a.profileHook("built"); err != nil {
			return err
		}
	}
	val, err := a.Catherd.Validate(ctx, env, host, v.CatherdName, sec)
	if err != nil {
		return result(err)
	}
	resolved, err := a.Catherd.ShowResolved(ctx, env, host, v.CatherdName)
	if err != nil {
		return result(err)
	}
	inv, err := a.Catherd.ListProfiles(ctx, env)
	if err != nil {
		return result(err)
	}
	return a.fenced(ctx, lease, func(sc context.Context) error {
		now := time.Now().UTC()
		cat, err := a.currentCatalog(sc, job.OrgID)
		if err != nil {
			return err
		}
		an := catherd.Analyze(doc, cat, catherd.Meta{BudgetUncappedAck: v.Meta.BudgetUncappedAck})
		rec := ValidationRecord{Host: host, DraftHash: v.DocHash, CatherdVersion: a.CatherdRT.Version(), Valid: val.Valid && len(an.Errors) == 0, Errors: val.Errors, Warnings: val.Warnings, OrqenIssues: append(append([]catherd.Issue{}, an.Errors...), an.Warnings...), Independence: an.Independence, CheckedAt: now, OperationID: *job.OperationID}
		res, err := a.DB.Coll("model_profile_versions").UpdateOne(sc, bson.M{"_id": v.ID, "state": "publishing"}, bson.M{"$set": bson.M{"state": "published", "resolved": stored(resolved), "validation": rec, "published_at": now, "republish_required": false}})
		if err != nil {
			return err
		}
		if res.MatchedCount == 0 {
			return errLeaseLost
		}
		if _, err = a.DB.Coll("model_profiles").UpdateOne(sc, bson.M{"_id": id, "org_id": job.OrgID}, bson.M{"$set": bson.M{"latest_version_id": v.ID}}); err != nil {
			return err
		}
		names := []string{}
		for _, r := range inv {
			names = append(names, r.Name)
		}
		if _, err = a.DB.Coll("catherd_states").UpdateOne(sc, bson.M{"_id": catherdStateID(job.OrgID)}, bson.M{"$set": bson.M{"org_id": job.OrgID, "inventory": CatherdInventory{Names: names, CheckedAt: now}}}, upsertOpts); err != nil {
			return err
		}
		if err = a.transitionOperation(sc, job, "succeeded", bson.M{"version_id": v.ID, "catherd_name": v.CatherdName}); err != nil {
			return err
		}
		if err = a.AppendEvent(sc, job.OrgID, "model_profile_version.published", Subject{"model_profile", id}, bson.M{"version_id": v.ID, "number": v.Number, "catherd_name": v.CatherdName}, nil, job.OperationID, true); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
}

// sweepOrphans removes staging profiles (s-*) and o-* profiles that no published or publishing version owns.
func (a *App) sweepOrphans(ctx context.Context, env *catherd.OrgEnv, org string, rows []catherd.ProfileRow) error {
	for _, r := range rows {
		switch {
		case strings.HasPrefix(r.Name, "s-"):
			if err := a.Catherd.Remove(ctx, env, r.Name); err != nil {
				return err
			}
		case strings.HasPrefix(r.Name, "o-"):
			n, err := a.DB.Coll("model_profile_versions").CountDocuments(ctx, bson.M{"org_id": org, "catherd_name": r.Name, "state": bson.M{"$in": []string{"published", "publishing"}}})
			if err != nil {
				return err
			}
			if n == 0 {
				if err = a.Catherd.Remove(ctx, env, r.Name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
