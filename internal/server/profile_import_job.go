package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"orqen/internal/catherd"
	"time"
)

type ImportPersonalRequest struct {
	PersonalName string `json:"personal_name"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
}

func (a *App) startImportPersonal(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	var req ImportPersonalRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	route := "POST /api/v1/orgs/{orgId}/model-profiles/import-personal"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org}, req), func(ctx context.Context) (Output, error) {
		es := checkProfileFields(req.Slug, req.Name, nil, true)
		if !catherd.NamePattern.MatchString(req.PersonalName) {
			es = append(es, FieldError{"personal_name", "is not a catherd profile name"})
		}
		if err := validationErrors(es); err != nil {
			return Output{}, err
		}
		return a.startOperation(ctx, org, "profile_import_personal", Subject{"organization", org}, "profile_import_personal", 2, bson.M{"personal_name": req.PersonalName, "slug": req.Slug, "name": req.Name})
	})
}

// runProfileImport copies one personal catherd profile into a new draft. It only reads the personal state
// (list and show take a ReadEnv); no write method accepts the personal environment.
func (a *App) runProfileImport(ctx context.Context, job JobDoc, lease Lease) error {
	if job.OperationID == nil {
		return &JobFailure{Code: "no_operation", Message: "job has no operation"}
	}
	name, _ := job.Payload["personal_name"].(string)
	slug, _ := job.Payload["slug"].(string)
	title, _ := job.Payload["name"].(string)
	if err := a.fenced(ctx, lease, func(sc context.Context) error { return a.transitionOperation(sc, job, "running", nil) }); err != nil {
		return err
	}
	if err := a.catherdReady(); err != nil {
		return err
	}
	failOp := func(code, msg string) error {
		return a.fenced(ctx, lease, func(sc context.Context) error {
			if err := a.transitionOperation(sc, job, "failed", bson.M{"code": code, "message": msg}); err != nil {
				return err
			}
			return a.completeJob(sc, lease)
		})
	}
	personal := catherd.NewPersonalEnv(a.Config.DataDir)
	if err := personal.Ensure(); err != nil {
		return err
	}
	rows, err := a.Catherd.ListProfiles(ctx, personal)
	if err != nil {
		return catherdFailure(err)
	}
	found := false
	for _, r := range rows {
		found = found || r.Name == name
	}
	if !found {
		return failOp("personal_profile_not_found", "the personal catherd profile does not exist")
	}
	host, err := a.orgHostE(ctx, job.OrgID)
	if err != nil {
		return err
	}
	raw, err := a.Catherd.ShowRaw(ctx, personal, host, name)
	if err != nil {
		return catherdFailure(err)
	}
	delete(raw, "name")
	err = a.fenced(ctx, lease, func(sc context.Context) error {
		now := time.Now().UTC()
		d := ModelProfileDoc{ID: newID("mpf"), OrgID: job.OrgID, Slug: slug, Name: title, Draft: ProfileDraft{Doc: stored(raw), UpdatedAt: now}, NextNumber: 1, Source: ProfileSource{Kind: "import_personal", Ref: &name}, Active: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
		used, err := a.DB.Coll("model_profile_versions").CountDocuments(sc, bson.M{"org_id": job.OrgID, "catherd_name": bson.Regex{Pattern: "^o-" + slug + "-v[0-9]+$"}})
		if err != nil {
			return err
		}
		if used > 0 {
			return errSlugTaken
		}
		if _, err = a.DB.Coll("model_profiles").InsertOne(sc, d); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return errSlugTaken
			}
			return err
		}
		if err = a.transitionOperation(sc, job, "succeeded", bson.M{"profile_id": d.ID}); err != nil {
			return err
		}
		if err = a.AppendEvent(sc, job.OrgID, "model_profile.imported", Subject{"model_profile", d.ID}, bson.M{"slug": slug, "personal_name": name}, &d.Revision, job.OperationID, true); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
	if errors.Is(err, errSlugTaken) {
		return failOp("slug_taken", "a profile with this slug already exists or was used before")
	}
	return err
}

var errSlugTaken = errors.New("slug taken")
