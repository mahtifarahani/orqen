package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"log/slog"
	"orqen/internal/catherd"
	"path/filepath"
	"time"
)

func (a *App) storeJSONBlob(org string, data []byte) (Blob, error) {
	return storeBlob(filepath.Join(a.Config.DataDir, "artifacts"), org, bytes.NewReader(data), a.Config.ArtifactMaxBytes)
}

// snapshotCatalog lists the catalog and stores it as a content-addressed artifact blob.
func (a *App) snapshotCatalog(ctx context.Context, org string, env *catherd.OrgEnv) (Blob, int, error) {
	raw, err := a.Catherd.CatalogList(ctx, env)
	if err != nil {
		return Blob{}, 0, err
	}
	cat, err := catherd.ParseCatalog(raw)
	if err != nil {
		return Blob{}, 0, &catherd.Error{Code: catherd.CodeBadOutput, Message: "the catalog was not valid JSON"}
	}
	blob, err := a.storeJSONBlob(org, raw)
	return blob, len(cat.Models), err
}

// runCatherdDoctor runs `catherd doctor` in the organization's own home. Doctor writes locks and logs and rewrites
// model listings there, so it only runs on request; afterwards the catalog snapshot and profile inventory are refreshed.
func (a *App) runCatherdDoctor(ctx context.Context, job JobDoc, lease Lease) error {
	if job.OperationID == nil {
		return &JobFailure{Code: "no_operation", Message: "job has no operation"}
	}
	if err := a.fenced(ctx, lease, func(sc context.Context) error { return a.transitionOperation(sc, job, "running", nil) }); err != nil {
		return err
	}
	if err := a.catherdReady(); err != nil {
		return err
	}
	env, err := a.catherdEnv(job.OrgID)
	if err != nil {
		return err
	}
	lock := a.orgLock(job.OrgID)
	lock.Lock()
	defer lock.Unlock()
	host, _ := job.Payload["host"].(string)
	if !catherd.ValidHost(host) {
		if host, err = a.orgHostE(ctx, job.OrgID); err != nil {
			return err
		}
	}
	sec, err := a.secretEnvFor(ctx, job.OrgID, append([]string{"TYPESAFE_API_KEY"}, isolationEnvs()...))
	if err != nil {
		return err
	}
	raw, _, err := a.Catherd.Doctor(ctx, env, host, sec)
	if err != nil {
		return catherdFailure(err)
	}
	var report struct {
		Ready   bool          `json:"ready"`
		Version string        `json:"version"`
		Checks  []DoctorCheck `json:"checks"`
	}
	if err = json.Unmarshal(raw, &report); err != nil {
		return &JobFailure{Code: "catherd_failed", Message: "the doctor report was not understood"}
	}
	for i := range report.Checks {
		report.Checks[i].Detail, report.Checks[i].Fix = catherd.ScrubText(report.Checks[i].Detail), catherd.ScrubText(report.Checks[i].Fix)
	}
	reportBlob, err := a.storeJSONBlob(job.OrgID, raw)
	if err != nil {
		return err
	}
	catalogBlob, models, catalogErr := a.snapshotCatalog(ctx, job.OrgID, env)
	if catalogErr != nil {
		slog.Warn("catalog snapshot after doctor failed", "error", catalogErr)
	}
	rows, listErr := a.Catherd.ListProfiles(ctx, env)
	if listErr != nil {
		slog.Warn("profile inventory after doctor failed", "error", listErr)
	}
	return a.fenced(ctx, lease, func(sc context.Context) error {
		now := time.Now().UTC()
		art, err := a.recordArtifact(sc, job.OrgID, reportBlob, "application/json", "catherd_doctor_report", Subject{"operation", *job.OperationID})
		if err != nil {
			return err
		}
		version := report.Version
		if version == "" {
			version = a.CatherdRT.Version()
		}
		set := bson.M{"org_id": job.OrgID, "version": version, "doctors." + host: DoctorSummary{OperationID: *job.OperationID, CheckedAt: now, Ready: report.Ready, Checks: report.Checks, ArtifactID: art.ID, Host: host}}
		if listErr == nil {
			names := []string{}
			for _, r := range rows {
				names = append(names, r.Name)
			}
			set["inventory"] = CatherdInventory{Names: names, CheckedAt: now}
		}
		if catalogErr == nil {
			cart, err := a.recordArtifact(sc, job.OrgID, catalogBlob, "application/json", "catherd_catalog_snapshot", Subject{"operation", *job.OperationID})
			if err != nil {
				return err
			}
			set["catalog"] = CatherdCatalogState{ArtifactID: cart.ID, RefreshedAt: now, ModelCount: models, Mode: "doctor", OperationID: *job.OperationID}
		}
		if _, err = a.DB.Coll("catherd_states").UpdateOne(sc, bson.M{"_id": catherdStateID(job.OrgID)}, bson.M{"$set": set}, upsertOpts); err != nil {
			return err
		}
		if err = a.transitionOperation(sc, job, "succeeded", bson.M{"ready": report.Ready, "artifact_id": art.ID, "host": host}); err != nil {
			return err
		}
		if err = a.AppendEvent(sc, job.OrgID, "catherd.doctor_completed", Subject{"organization", job.OrgID}, bson.M{"ready": report.Ready, "checks": len(report.Checks), "host": host}, nil, job.OperationID, true); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
}

// runCatherdCatalog refreshes (listings), syncs public sources (sync) or just re-lists the catalog, then snapshots it.
func (a *App) runCatherdCatalog(ctx context.Context, job JobDoc, lease Lease) error {
	if job.OperationID == nil {
		return &JobFailure{Code: "no_operation", Message: "job has no operation"}
	}
	mode, _ := job.Payload["mode"].(string)
	if err := a.fenced(ctx, lease, func(sc context.Context) error { return a.transitionOperation(sc, job, "running", nil) }); err != nil {
		return err
	}
	if err := a.catherdReady(); err != nil {
		return err
	}
	env, err := a.catherdEnv(job.OrgID)
	if err != nil {
		return err
	}
	lock := a.orgLock(job.OrgID)
	lock.Lock()
	defer lock.Unlock()
	var sec catherd.SecretEnv
	switch mode {
	case "listings":
		if sec, err = a.secretEnvFor(ctx, job.OrgID, isolationEnvs()); err == nil {
			_, err = a.Catherd.CatalogRefresh(ctx, env, sec)
		}
	case "sync":
		if sec, err = a.secretEnvFor(ctx, job.OrgID, []string{"ARTIFICIAL_ANALYSIS_API_KEY"}); err == nil {
			_, err = a.Catherd.CatalogSync(ctx, env, false, sec)
		}
	}
	var jf *JobFailure
	if errors.As(err, &jf) {
		return jf
	}
	if err != nil {
		return catherdFailure(err)
	}
	blob, models, err := a.snapshotCatalog(ctx, job.OrgID, env)
	if err != nil {
		return catherdFailure(err)
	}
	return a.fenced(ctx, lease, func(sc context.Context) error {
		now := time.Now().UTC()
		art, err := a.recordArtifact(sc, job.OrgID, blob, "application/json", "catherd_catalog_snapshot", Subject{"operation", *job.OperationID})
		if err != nil {
			return err
		}
		set := bson.M{"org_id": job.OrgID, "catalog": CatherdCatalogState{ArtifactID: art.ID, RefreshedAt: now, ModelCount: models, Mode: mode, OperationID: *job.OperationID}}
		if _, err = a.DB.Coll("catherd_states").UpdateOne(sc, bson.M{"_id": catherdStateID(job.OrgID)}, bson.M{"$set": set}, upsertOpts); err != nil {
			return err
		}
		if err = a.transitionOperation(sc, job, "succeeded", bson.M{"mode": mode, "models": models, "artifact_id": art.ID}); err != nil {
			return err
		}
		if err = a.AppendEvent(sc, job.OrgID, "catherd.catalog_refreshed", Subject{"organization", job.OrgID}, bson.M{"mode": mode, "models": models}, nil, job.OperationID, true); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
}
