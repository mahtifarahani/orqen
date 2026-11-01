package server

import (
	"bytes"
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"log/slog"
	"net/http"
	"orqen/internal/workspace"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// enqueueCleanup schedules workspace cleanup for the organization unless one is already pending.
func (a *App) enqueueCleanup(sc context.Context, org string) error {
	now := time.Now().UTC()
	err := a.enqueue(sc, JobDoc{ID: newID("job"), OrgID: org, Kind: "workspace_cleanup", DedupeKey: "workspace_cleanup:" + org, Payload: bson.M{}, State: "queued", Active: true, MaxAttempts: 5, NextAt: now, CreatedAt: now, UpdatedAt: now})
	var dup *DuplicateActiveJob
	if errors.As(err, &dup) {
		// The active job may already have scanned: ask it to scan once more before it completes.
		_, e := a.DB.Coll("jobs").UpdateOne(sc, bson.M{"org_id": org, "dedupe_key": "workspace_cleanup:" + org, "active": true}, bson.M{"$set": bson.M{"rerun": true}})
		return e
	}
	return err
}

// mirrorOf is the absolute mirror path of a workspace.
func (a *App) absPath(rel string) string { return filepath.Join(a.Config.DataDir, rel) }

// retainedToRemove returns the clean retained workspaces beyond max, oldest first.
func retainedToRemove(retained []WorkspaceDoc, max int) []WorkspaceDoc {
	sort.Slice(retained, func(i, j int) bool { return retained[i].CreatedAt.Before(retained[j].CreatedAt) })
	if len(retained) <= max {
		return nil
	}
	return retained[:len(retained)-max]
}

func (a *App) setWorkspace(ctx context.Context, ws WorkspaceDoc, set bson.M, event string) error {
	return a.DB.Txn(ctx, func(sc context.Context) error {
		var next WorkspaceDoc
		set["updated_at"] = time.Now().UTC()
		err := a.DB.Coll("workspaces").FindOneAndUpdate(sc, bson.M{"_id": ws.ID, "org_id": ws.OrgID}, bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&next)
		if err != nil {
			return err
		}
		if event == "" {
			return nil
		}
		return a.AppendEvent(sc, ws.OrgID, event, Subject{"run", ws.RunID}, bson.M{"run_id": ws.RunID, "workspace_id": ws.ID}, nil, nil, true)
	})
}

// runWorkspaceCleanup removes the clean workspaces of finished runs and preserves everything else. It never removes
// a workspace with changes, untracked files or commits, and never touches a queued, planned or active run's.
func (a *App) runWorkspaceCleanup(ctx context.Context, job JobDoc, lease Lease) error {
	for {
		if err := a.fenced(ctx, lease, func(sc context.Context) error {
			_, e := a.DB.Coll("jobs").UpdateOne(sc, bson.M{"_id": job.ID}, bson.M{"$set": bson.M{"rerun": false}})
			return e
		}); err != nil {
			return err
		}
		if err := a.cleanupPass(ctx, job); err != nil {
			return err
		}
		again := false
		if err := a.fenced(ctx, lease, func(sc context.Context) error {
			cur, e := findOne[JobDoc](sc, a.DB, "jobs", bson.M{"_id": job.ID})
			if e != nil {
				return e
			}
			if cur.Rerun { // a request arrived while this pass ran and was folded into it
				again = true
				return nil
			}
			return a.completeJob(sc, lease)
		}); err != nil {
			return err
		}
		if !again {
			return nil
		}
	}
}

func (a *App) cleanupPass(ctx context.Context, job JobDoc) error {
	org := job.OrgID
	spaces, err := findMany[WorkspaceDoc](ctx, a.DB, "workspaces", bson.M{"org_id": org, "state": bson.M{"$in": []string{"preparing", "ready", "retained", "preserved"}}}, bson.D{{Key: "created_at", Value: 1}}, 1000)
	if err != nil {
		return err
	}
	pol, err := a.livePolicy(ctx, org, "")
	if err != nil {
		return err
	}
	m, err := a.workspaceManager(org, nil, pol.Int("workspace.min_free_mb"))
	if err != nil {
		return err
	}
	var retained []WorkspaceDoc
	for _, ws := range spaces {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		run, e := a.run(ctx, org, ws.RunID)
		if errors.Is(e, mongo.ErrNoDocuments) {
			continue
		}
		if e != nil {
			return e
		}
		if run.Active {
			continue
		}
		path, mirror := a.absPath(ws.PathRel), a.absPath(ws.MirrorRel)
		if ws.PathRel == "" { // never got past preparing: nothing on disk to protect
			if e = a.setWorkspace(ctx, ws, bson.M{"state": "removed", "active": false, "removed_at": time.Now().UTC()}, "workspace.removed"); e != nil {
				return e
			}
			continue
		}
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			if e = a.setWorkspace(ctx, ws, bson.M{"state": "removed", "active": false, "removed_at": time.Now().UTC()}, "workspace.removed"); e != nil {
				return e
			}
			continue
		}
		st, e := m.Inspect(ctx, path, ws.BaseSHA)
		if e != nil {
			slog.Warn("workspace cleanup could not inspect", "workspace_id", ws.ID)
			continue
		}
		if !st.Clean || st.HeadSHA != ws.BaseSHA {
			if ws.State != "preserved" {
				if e = a.setWorkspace(ctx, ws, bson.M{"state": "preserved", "dirty": true, "head_sha": st.HeadSHA}, "workspace.preserved"); e != nil {
					return e
				}
			}
			continue
		}
		if ws.State == "retained" {
			retained = append(retained, ws)
			continue
		}
		if e = m.Remove(ctx, mirror, path, ws.Branch, ws.BaseSHA, true); e != nil {
			if errors.Is(e, workspace.ErrDirty) {
				continue
			}
			slog.Warn("workspace cleanup could not remove", "workspace_id", ws.ID, "error", e)
			continue
		}
		if e = a.setWorkspace(ctx, ws, bson.M{"state": "removed", "active": false, "removed_at": time.Now().UTC()}, "workspace.removed"); e != nil {
			return e
		}
	}
	for _, ws := range retainedToRemove(retained, pol.Int("workspace.max_retained")) {
		if e := m.Remove(ctx, a.absPath(ws.MirrorRel), a.absPath(ws.PathRel), ws.Branch, ws.BaseSHA, true); e != nil && !errors.Is(e, workspace.ErrDirty) {
			continue
		}
		if e := a.setWorkspace(ctx, ws, bson.M{"state": "removed", "active": false, "removed_at": time.Now().UTC()}, "workspace.removed"); e != nil {
			return e
		}
	}
	return nil
}

// runWorkspaceDiscard stores a snapshot of the workspace, then removes it by force.
func (a *App) runWorkspaceDiscard(ctx context.Context, job JobDoc, lease Lease) error {
	id, _ := job.Payload["workspace_id"].(string)
	if !validID("wsp", id) {
		return &JobFailure{Code: "bad_payload", Message: "missing workspace_id"}
	}
	if err := a.fenced(ctx, lease, func(sc context.Context) error { return a.transitionOperation(sc, job, "running", nil) }); err != nil {
		return err
	}
	ws, err := findOne[WorkspaceDoc](ctx, a.DB, "workspaces", bson.M{"_id": id, "org_id": job.OrgID})
	if err != nil {
		return err
	}
	pol, err := a.livePolicy(ctx, job.OrgID, "")
	if err != nil {
		return err
	}
	m, err := a.workspaceManager(job.OrgID, nil, pol.Int("workspace.min_free_mb"))
	if err != nil {
		return err
	}
	path, mirror := a.absPath(ws.PathRel), a.absPath(ws.MirrorRel)
	var blob *Blob
	if _, statErr := os.Stat(path); statErr == nil {
		var buf bytes.Buffer
		if err = m.Snapshot(ctx, path, ws.BaseSHA, &buf); err != nil {
			return a.discardFailed(ctx, lease, job, "snapshot_failed")
		}
		b, e := storeBlob(filepath.Join(a.Config.DataDir, "artifacts"), job.OrgID, &buf, a.Config.ArtifactMaxBytes)
		if e != nil {
			return e
		}
		blob = &b
		if err = m.Remove(ctx, mirror, path, ws.Branch, ws.BaseSHA, false); err != nil {
			return a.discardFailed(ctx, lease, job, "remove_failed")
		}
	}
	return a.fenced(ctx, lease, func(sc context.Context) error {
		set := bson.M{"state": "removed", "active": false, "removed_at": time.Now().UTC(), "updated_at": time.Now().UTC()}
		if blob != nil {
			art, e := a.recordArtifact(sc, job.OrgID, *blob, "application/gzip", "workspace_snapshot", Subject{"run", ws.RunID})
			if e != nil {
				return e
			}
			set["snapshot_artifact_id"] = art.ID
		}
		if _, e := a.DB.Coll("workspaces").UpdateOne(sc, bson.M{"_id": ws.ID, "org_id": ws.OrgID}, bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}}); e != nil {
			return e
		}
		if e := a.AppendEvent(sc, job.OrgID, "workspace.removed", Subject{"run", ws.RunID}, bson.M{"run_id": ws.RunID, "workspace_id": ws.ID, "discarded": true}, nil, job.OperationID, true); e != nil {
			return e
		}
		if e := a.transitionOperation(sc, job, "succeeded", bson.M{"workspace_id": ws.ID}); e != nil {
			return e
		}
		return a.completeJob(sc, lease)
	})
}
func (a *App) discardFailed(ctx context.Context, lease Lease, job JobDoc, code string) error {
	return a.fenced(ctx, lease, func(sc context.Context) error {
		if err := a.transitionOperation(sc, job, "failed", bson.M{"code": code, "message": "the workspace could not be discarded"}); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
}

func (a *App) discardWorkspace(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("wspId")
	route := "POST /api/v1/orgs/{orgId}/workspaces/{wspId}/discard"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "wspId": id, "if_match": formatInt(expected)}, nil), func(ctx context.Context) (Output, error) {
		ws, err := findOne[WorkspaceDoc](ctx, a.DB, "workspaces", bson.M{"_id": id, "org_id": org})
		if err != nil {
			return Output{}, err
		}
		if ws.Revision != expected {
			c := problem("revision_conflict")
			c.Current = ws.DTO()
			return Output{}, c
		}
		run, err := a.run(ctx, org, ws.RunID)
		if err != nil {
			return Output{}, err
		}
		if run.Active || ws.State == "removed" {
			return Output{}, problem("run_state_conflict")
		}
		return a.startOperation(ctx, org, "workspace_discard", Subject{"workspace", id}, "workspace_discard:"+id, 3, bson.M{"workspace_id": id})
	})
}
