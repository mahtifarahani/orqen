package server

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"log/slog"
	"math"
	"math/rand/v2"
	"orqen/internal/providers"
	"os"
	"sync"
	"time"
)

type Lease struct {
	JobID, OrgID, Owner string
	Fence               int64
}

var errLeaseLost = errors.New("lease lost")

type JobFailure struct {
	Code, Message string
	Retryable     bool
	RetryAfter    time.Duration
}

func (e *JobFailure) Error() string { return e.Message }
func afterUpdate() *options.FindOneAndUpdateOptionsBuilder {
	return options.FindOneAndUpdate().SetReturnDocument(options.After)
}
func leaseFilter(l Lease) bson.M {
	return bson.M{"_id": l.JobID, "org_id": l.OrgID, "fence": l.Fence, "state": "leased"}
}
func (a *App) enqueue(ctx context.Context, job JobDoc) error {
	n, err := a.DB.Coll("jobs").CountDocuments(ctx, bson.M{"org_id": job.OrgID, "dedupe_key": job.DedupeKey, "active": true})
	if err != nil {
		return err
	}
	if n > 0 {
		return &DuplicateActiveJob{job.OrgID, job.DedupeKey}
	}
	_, err = a.DB.Coll("jobs").InsertOne(ctx, job)
	if mongo.IsDuplicateKeyError(err) {
		return &DuplicateActiveJob{job.OrgID, job.DedupeKey}
	}
	return err
}
func (a *App) acquire(ctx context.Context, owner string, kinds []string, now time.Time) (JobDoc, Lease, error) {
	var job JobDoc
	filter := bson.M{"kind": bson.M{"$in": kinds}, "$or": []bson.M{{"state": "queued", "next_at": bson.M{"$lte": now}}, {"state": "leased", "lease_expires_at": bson.M{"$lte": now}}}}
	update := bson.M{"$set": bson.M{"state": "leased", "lease_owner": owner, "lease_expires_at": now.Add(a.Config.LeaseTTL), "updated_at": now}, "$inc": bson.M{"fence": int64(1), "attempts": 1}}
	err := a.DB.Coll("jobs").FindOneAndUpdate(ctx, filter, update, afterUpdate().SetSort(bson.D{{Key: "next_at", Value: 1}})).Decode(&job)
	return job, Lease{JobID: job.ID, OrgID: job.OrgID, Owner: owner, Fence: job.Fence}, err
}
func (a *App) leaseWrite(ctx context.Context, l Lease, set bson.M) error {
	res, err := a.DB.Coll("jobs").UpdateOne(ctx, leaseFilter(l), bson.M{"$set": set})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return errLeaseLost
	}
	return nil
}
func (a *App) heartbeat(ctx context.Context, l Lease, now time.Time) error {
	return a.leaseWrite(ctx, l, bson.M{"lease_expires_at": now.Add(a.Config.LeaseTTL)})
}

// Every job-caused transaction touches the fenced job before domain writes or events.
func (a *App) fenced(ctx context.Context, l Lease, f func(context.Context) error) error {
	err := a.DB.Txn(ctx, func(sc context.Context) error {
		if err := a.leaseWrite(sc, l, bson.M{"updated_at": time.Now().UTC()}); err != nil {
			return err
		}
		return f(sc)
	})
	if err == nil {
		a.Committed()
	}
	return err
}
func (a *App) completeJob(ctx context.Context, l Lease) error {
	return a.leaseWrite(ctx, l, bson.M{"state": "succeeded", "active": false, "lease_owner": nil, "lease_expires_at": nil, "updated_at": time.Now().UTC()})
}
func backoffWith(attempts int, jitter float64) time.Duration {
	return time.Duration(math.Min(math.Pow(2, float64(min(max(attempts, 0), 30))), 300) * jitter * float64(time.Second))
}
func (a *App) failJob(ctx context.Context, job JobDoc, l Lease, failure JobFailure, now time.Time) (string, error) {
	state := "failed"
	set := bson.M{"lease_owner": nil, "lease_expires_at": nil, "updated_at": now, "last_error": bson.M{"code": failure.Code, "message": failure.Message, "retryable": failure.Retryable}}
	if failure.Retryable && job.Attempts < job.MaxAttempts {
		state = "queued"
		delay := max(backoffWith(job.Attempts, 0.8+rand.Float64()*0.4), failure.RetryAfter)
		set["next_at"] = now.Add(delay)
	} else {
		if failure.Retryable || failure.Code == "max_attempts_exceeded" {
			state = "dead"
		}
		set["active"] = false
	}
	set["state"] = state
	return state, a.leaseWrite(ctx, l, set)
}
func (a *App) recordFailure(job JobDoc, l Lease, err error) {
	if errors.Is(err, errLeaseLost) || errors.Is(err, context.Canceled) || a.ctx.Err() != nil {
		return
	}
	failure := JobFailure{Code: "internal", Message: "internal error", Retryable: true}
	var jf *JobFailure
	if pe, ok := providers.AsError(err); ok {
		failure = JobFailure{Code: "provider_" + string(pe.Kind), Message: pe.Message, Retryable: pe.Retryable(), RetryAfter: pe.RetryAfter}
	} else if errors.As(err, &jf) {
		failure = *jf
	} else {
		slog.Error("job step failed", "job_id", job.ID, "error", err)
	}
	e := a.DB.Txn(a.ctx, func(ctx context.Context) error {
		outcome, err := a.failJob(ctx, job, l, failure, time.Now().UTC())
		if err != nil {
			return err
		}
		kind := "job.failed"
		if outcome == "queued" {
			kind = "job.retry_scheduled"
		} else {
			if outcome == "dead" {
				kind = "job.dead"
			}
			if err = a.transitionOperation(ctx, job, "failed", bson.M{"code": failure.Code, "message": failure.Message}); err != nil {
				return err
			}
		}
		return a.AppendEvent(ctx, job.OrgID, kind, Subject{"job", job.ID}, bson.M{"kind": job.Kind, "code": failure.Code, "attempts": job.Attempts}, nil, nil, true)
	})
	if e == nil {
		a.Committed()
	} else if !errors.Is(e, errLeaseLost) {
		slog.Error("failed to record job failure", "job_id", job.ID, "error", e)
	}
}
func (a *App) runWorkers() {
	var wg sync.WaitGroup
	host, _ := os.Hostname()
	owner := fmt.Sprintf("w-%s-%d-%s", host, os.Getpid(), newID("worker"))
	slots := make(chan struct{}, max(a.Config.WorkerConcurrency, 1))
	defer wg.Wait()
	for {
		a.runnerBeat.Store(time.Now().UnixNano())
		select {
		case slots <- struct{}{}:
		case <-a.ctx.Done():
			return
		}
		job, lease, err := a.acquire(a.ctx, owner, a.handlerKinds(), time.Now().UTC())
		if err == nil {
			wg.Add(1)
			go func() { defer wg.Done(); defer func() { <-slots }(); a.runJob(job, lease) }()
			continue
		}
		<-slots
		if !errors.Is(err, mongo.ErrNoDocuments) && a.ctx.Err() == nil {
			slog.Warn("failed to acquire a job", "error", err)
		}
		select {
		case <-a.ctx.Done():
			return
		case <-a.jobWake:
		case <-time.After(500 * time.Millisecond):
		}
	}
}
func (a *App) runJob(job JobDoc, lease Lease) {
	if job.Attempts > job.MaxAttempts {
		a.recordFailure(job, lease, &JobFailure{Code: "max_attempts_exceeded", Message: "job exceeded its maximum attempts"})
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(max(a.Config.LeaseTTL/3, time.Millisecond))
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := a.heartbeat(ctx, lease, now.UTC()); errors.Is(err, errLeaseLost) {
					cancel()
					return
				} else if err != nil && ctx.Err() == nil {
					slog.Warn("heartbeat failed", "job_id", job.ID, "error", err)
				}
			}
		}
	}()
	var result error
	func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("job handler panicked", "job_id", job.ID, "panic", p)
				result = &JobFailure{Code: "internal_panic", Message: "handler panicked", Retryable: true}
			}
		}()
		h := a.handler(job.Kind)
		if h == nil {
			result = &JobFailure{Code: "unknown_kind", Message: "no handler for job kind " + job.Kind}
			return
		}
		result = h(ctx, job, lease)
	}()
	cancel()
	<-heartbeatDone
	if result != nil {
		a.recordFailure(job, lease, result)
	}
}
