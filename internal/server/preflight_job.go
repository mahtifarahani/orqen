package server

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/sysprobe"
	"time"
)

// runPreflight runs the detect-only checks outside any transaction, stores the report as an artifact and keeps the
// latest report on the organization's system state.
func (a *App) runPreflight(ctx context.Context, job JobDoc, lease Lease) error {
	if job.OperationID == nil {
		return &JobFailure{Code: "no_operation", Message: "job has no operation"}
	}
	if err := a.fenced(ctx, lease, func(sc context.Context) error { return a.transitionOperation(sc, job, "running", nil) }); err != nil {
		return err
	}
	in, err := a.preflightInputs(ctx, job.OrgID)
	if err != nil {
		return err
	}
	checks := toPreflightChecks(sysprobe.Run(ctx, a.SysDeps, in))
	if err = ctx.Err(); err != nil {
		return err
	}
	now := time.Now().UTC()
	data, err := json.MarshalIndent(bson.M{"checked_at": timestamp(now), "checks": checks}, "", "  ")
	if err != nil {
		return err
	}
	blob, err := a.storeJSONBlob(job.OrgID, data)
	if err != nil {
		return err
	}
	failures := 0
	for _, c := range checks {
		if c.Status == sysprobe.Fail {
			failures++
		}
	}
	return a.fenced(ctx, lease, func(sc context.Context) error {
		art, err := a.recordArtifact(sc, job.OrgID, blob, "application/json", "system_preflight_report", Subject{"operation", *job.OperationID})
		if err != nil {
			return err
		}
		rec := PreflightRecord{OperationID: *job.OperationID, CheckedAt: now, Checks: checks, ArtifactID: art.ID}
		if _, err = a.DB.Coll("system_states").UpdateOne(sc, bson.M{"_id": systemStateID(job.OrgID)}, bson.M{"$set": bson.M{"org_id": job.OrgID, "last_preflight": rec}, "$setOnInsert": bson.M{"revision": int64(0), "launchd_opted_in": false, "updated_at": now}}, upsertOpts); err != nil {
			return err
		}
		if err = a.transitionOperation(sc, job, "succeeded", bson.M{"artifact_id": art.ID, "checks": len(checks), "failures": failures}); err != nil {
			return err
		}
		if err = a.AppendEvent(sc, job.OrgID, "system.preflight_completed", Subject{"organization", job.OrgID}, bson.M{"checks": len(checks), "failures": failures}, nil, job.OperationID, true); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
}
