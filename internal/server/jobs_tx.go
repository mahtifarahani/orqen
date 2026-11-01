package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"time"
)

// errJobRace reports that another transaction inserted the same active job first. A duplicate-key error aborts the
// surrounding transaction, so the caller re-runs its whole transaction once (it then sees the job).
var errJobRace = errors.New("job insert raced")

// ensureJobTx makes sure one active job with this dedupe key exists, without an operation (a system job).
func (a *App) ensureJobTx(sc context.Context, org, kind, dedupe string, maxAttempts int, payload bson.M) error {
	n, err := a.DB.Coll("jobs").CountDocuments(sc, bson.M{"org_id": org, "dedupe_key": dedupe, "active": true})
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	now := time.Now().UTC()
	if payload == nil {
		payload = bson.M{}
	}
	_, err = a.DB.Coll("jobs").InsertOne(sc, JobDoc{ID: newID("job"), OrgID: org, Kind: kind, DedupeKey: dedupe, Payload: payload, State: "queued", Active: true, MaxAttempts: maxAttempts, NextAt: now, CreatedAt: now, UpdatedAt: now})
	if mongo.IsDuplicateKeyError(err) {
		return errJobRace
	}
	return err
}

// txnRetryJob runs a transaction and re-runs it once when ensureJobTx lost a race.
func (a *App) txnRetryJob(ctx context.Context, f func(context.Context) error) error {
	err := a.DB.Txn(ctx, f)
	if errors.Is(err, errJobRace) {
		err = a.DB.Txn(ctx, f)
	}
	return err
}

// nextCounter increments a named counter and returns the new value.
func (a *App) nextCounter(sc context.Context, id string) (int64, error) {
	var c struct {
		Seq int64 `bson:"seq"`
	}
	err := a.DB.Coll("counters").FindOneAndUpdate(sc, bson.M{"_id": id}, bson.M{"$inc": bson.M{"seq": int64(1)}}, afterUpdate().SetUpsert(true)).Decode(&c)
	return c.Seq, err
}
