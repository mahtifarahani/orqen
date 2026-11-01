package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"log/slog"
	"net/http"
	"orqen/internal/catherd"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

func (a *App) startProfileValidation(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("mpfId")
	route := "POST /api/v1/orgs/{orgId}/model-profiles/{mpfId}/validate"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "mpfId": id}, nil), func(ctx context.Context) (Output, error) {
		if err := a.catherdGate(); err != nil {
			return Output{}, err
		}
		if _, err := a.profile(ctx, org, id); err != nil {
			return Output{}, err
		}
		return a.startOperation(ctx, org, "profile_validate", Subject{"model_profile", id}, "profile_validate:"+id, 3, bson.M{"profile_id": id})
	})
}

// stageDoc applies the patch from catherd's blank profile to the draft into a throwaway staging profile and asks
// catherd to validate it. Refusals and errors are results, not failures; only infrastructure errors are returned.
func (a *App) stageDoc(ctx context.Context, env *catherd.OrgEnv, org, host, name string, doc map[string]any) (refused, errs, warns []catherd.Issue, err error) {
	// Only the isolation keys of backends this document isolates are handed to catherd (K3).
	sec, err := a.secretEnvFor(ctx, org, isolationEnvsFor(doc))
	if err != nil {
		return
	}
	if err = a.Catherd.Create(ctx, env, host, name, sec); err != nil {
		return
	}
	base, err := a.Catherd.ShowRaw(ctx, env, host, name)
	if err != nil {
		return
	}
	delete(base, "name")
	patch := catherd.PatchBetween(base, doc)
	if bad := catherd.ValidatePatch(patch); len(bad) > 0 {
		return bad, nil, nil, nil
	}
	for _, k := range catherd.TopKeys {
		v, ok := patch[k]
		if !ok {
			continue
		}
		out, e := a.Catherd.SetKey(ctx, env, host, name, k, v, sec)
		if e != nil {
			return nil, nil, nil, e
		}
		if !out.Saved {
			return out.Refused, nil, nil, nil
		}
	}
	val, err := a.Catherd.Validate(ctx, env, host, name, sec)
	if err != nil {
		return
	}
	return nil, val.Errors, val.Warnings, nil
}
func (a *App) removeBestEffort(env *catherd.OrgEnv, name string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(a.ctx), 30*time.Second)
	defer cancel()
	if err := a.Catherd.Remove(ctx, env, name); err != nil {
		slog.Warn("could not remove a catherd profile", "name", name, "error", err)
	}
}

// docForCatherd is the draft as catherd stores it: schema 1 and no name.
func docForCatherd(draft bson.M) map[string]any {
	d := plain(draft)
	delete(d, "name")
	return d
}
func (a *App) runProfileValidate(ctx context.Context, job JobDoc, lease Lease) error {
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
	if err := a.catherdReady(); err != nil {
		return err
	}
	if !a.CatherdRT.Supported() {
		return &JobFailure{Code: "catherd_version_unsupported", Message: "catherd " + a.CatherdRT.Version() + " is outside the supported window"}
	}
	prof, err := a.profile(ctx, job.OrgID, id)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return &JobFailure{Code: "profile_archived", Message: "profile no longer exists"}
	}
	if err != nil {
		return err
	}
	hash := profileHash(prof.Draft.Doc, prof.Draft.Meta)
	doc := docForCatherd(prof.Draft.Doc)
	cat, err := a.currentCatalog(ctx, job.OrgID)
	if err != nil {
		return err
	}
	an := catherd.Analyze(doc, cat, catherd.Meta{BudgetUncappedAck: prof.Draft.Meta.BudgetUncappedAck})
	host, err := a.orgHostE(ctx, job.OrgID)
	if err != nil {
		return err
	}
	rec := ValidationRecord{Host: host, DraftHash: hash, CatherdVersion: a.CatherdRT.Version(), Independence: an.Independence, CheckedAt: time.Now().UTC(), OperationID: *job.OperationID, Errors: []catherd.Issue{}, Warnings: []catherd.Issue{}, OrqenIssues: append(append([]catherd.Issue{}, an.Errors...), an.Warnings...)}
	unsettable := false
	for _, e := range an.Errors {
		unsettable = unsettable || e.Code == "unsupported_field" || e.Code == "unknown_value" || e.Code == "bad_rung" || e.Code == "bad_value"
	}
	if !unsettable {
		env, err := a.catherdEnv(job.OrgID)
		if err != nil {
			return err
		}
		lock := a.orgLock(job.OrgID)
		lock.Lock()
		name := "s-" + strings.ToLower(ulid.Make().String())
		refused, errs, warns, serr := a.stageDoc(ctx, env, job.OrgID, host, name, doc)
		a.removeBestEffort(env, name)
		lock.Unlock()
		if serr != nil {
			return catherdFailure(serr)
		}
		rec.Errors = append(rec.Errors, refused...)
		rec.Errors = append(rec.Errors, errs...)
		rec.Warnings = append(rec.Warnings, warns...)
	}
	rec.Valid = len(an.Errors) == 0 && len(rec.Errors) == 0
	return a.fenced(ctx, lease, func(sc context.Context) error {
		if _, err := a.DB.Coll("model_profiles").UpdateOne(sc, profFilter(job.OrgID, id), bson.M{"$set": bson.M{"last_validation": rec}}); err != nil {
			return err
		}
		if err := a.transitionOperation(sc, job, "succeeded", bson.M{"valid": rec.Valid, "draft_hash": hash, "errors": len(an.Errors) + len(rec.Errors)}); err != nil {
			return err
		}
		if err := a.AppendEvent(sc, job.OrgID, "model_profile.validated", Subject{"model_profile", id}, bson.M{"valid": rec.Valid, "draft_hash": hash}, nil, job.OperationID, true); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
}
