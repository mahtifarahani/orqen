package server

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"time"
)

// startOperation enqueues a job and its operation inside the caller's transaction and returns the 202 output.
// A duplicate active job surfaces as *DuplicateActiveJob, which idempotent() turns into a deduplicated response.
func (a *App) startOperation(ctx context.Context, org, kind string, subject Subject, dedupe string, maxAttempts int, payload bson.M) (Output, error) {
	now := time.Now().UTC()
	opID := newID("op")
	job := JobDoc{ID: newID("job"), OrgID: org, Kind: kind, DedupeKey: dedupe, Payload: payload, State: "queued", Active: true, MaxAttempts: maxAttempts, NextAt: now, OperationID: &opID, CreatedAt: now, UpdatedAt: now}
	if err := a.enqueue(ctx, job); err != nil {
		return Output{}, err
	}
	op := OperationDoc{ID: opID, OrgID: org, Kind: kind, Subject: subject, Status: "pending", JobID: &job.ID, CreatedAt: now, UpdatedAt: now, Revision: 1}
	if _, err := a.DB.Coll("operations").InsertOne(ctx, op); err != nil {
		return Output{}, err
	}
	if err := a.AppendEvent(ctx, org, "operation.created", Subject{"operation", opID}, bson.M{"kind": op.Kind, "subject": op.Subject}, &op.Revision, &opID, false); err != nil {
		return Output{}, err
	}
	return Output{202, op.DTO(), map[string]string{"location": fmt.Sprintf("/api/v1/orgs/%s/operations/%s", org, opID)}}, nil
}
