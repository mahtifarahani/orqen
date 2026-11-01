package server

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func testJob(org, dedupe string, now time.Time) JobDoc {
	return JobDoc{ID: newID("job"), OrgID: org, Kind: "connection_test", DedupeKey: dedupe, Payload: bson.M{}, State: "queued", Active: true, MaxAttempts: 3, NextAt: now, CreatedAt: now, UpdatedAt: now}
}
func TestLeaseFencingIntegration(t *testing.T) {
	f := newTestApp(t, false)
	ctx := context.Background()
	now := time.Now().UTC()
	org := newID("org")
	job := testJob(org, "key", now)
	if err := f.db.Txn(ctx, func(sc context.Context) error { return f.a.enqueue(sc, job) }); err != nil {
		t.Fatal(err)
	}
	first, l1, err := f.a.acquire(ctx, "first", []string{"connection_test"}, now)
	if err != nil || first.Fence != 1 {
		t.Fatal(first, err)
	}
	second, l2, err := f.a.acquire(ctx, "second", []string{"connection_test"}, now.Add(time.Second))
	if err != nil || second.Fence != 2 || second.Attempts != 2 {
		t.Fatal(second, err)
	}
	if err = f.a.heartbeat(ctx, l1, now); !errors.Is(err, errLeaseLost) {
		t.Fatal(err)
	}
	if err = f.a.fenced(ctx, l1, func(sc context.Context) error {
		_, e := f.db.Coll("effects").InsertOne(sc, bson.M{"owner": "stale"})
		return e
	}); !errors.Is(err, errLeaseLost) {
		t.Fatal(err)
	}
	count, _ := f.db.Coll("effects").CountDocuments(ctx, bson.M{})
	if count != 0 {
		t.Fatal("stale effect committed")
	}
	if err = f.a.fenced(ctx, l2, func(sc context.Context) error {
		if _, e := f.db.Coll("effects").InsertOne(sc, bson.M{"owner": "new"}); e != nil {
			return e
		}
		return f.a.completeJob(sc, l2)
	}); err != nil {
		t.Fatal(err)
	}
	if err = f.a.completeJob(ctx, l1); !errors.Is(err, errLeaseLost) {
		t.Fatal(err)
	}
	count, _ = f.db.Coll("effects").CountDocuments(ctx, bson.M{})
	if count != 1 {
		t.Fatal(count)
	}
	indexes, err := f.db.Coll("jobs").Indexes().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var docs []bson.M
	if err = indexes.All(ctx, &docs); err != nil {
		t.Fatal(err)
	}
	for _, ix := range docs {
		if _, ok := ix["expireAfterSeconds"]; ok {
			t.Fatal("jobs have a TTL index")
		}
	}
}
func TestRetryAndDedupeIntegration(t *testing.T) {
	f := newTestApp(t, false)
	ctx := context.Background()
	now := time.Now().UTC()
	org := newID("org")
	job := testJob(org, "key", now.Add(time.Hour))
	if err := f.db.Txn(ctx, func(sc context.Context) error { return f.a.enqueue(sc, job) }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.a.acquire(ctx, "worker", []string{"connection_test"}, now); !errors.Is(err, mongo.ErrNoDocuments) {
		t.Fatal(err)
	}
	job2 := testJob(org, "key", now)
	err := f.db.Txn(ctx, func(sc context.Context) error { return f.a.enqueue(sc, job2) })
	var duplicate *DuplicateActiveJob
	if !errors.As(err, &duplicate) {
		t.Fatal(err)
	}
	other := testJob(newID("org"), "key", now)
	if err = f.db.Txn(ctx, func(sc context.Context) error { return f.a.enqueue(sc, other) }); err != nil {
		t.Fatal(err)
	}
	acquired, l, err := f.a.acquire(ctx, "worker", []string{"connection_test"}, now)
	if err != nil {
		t.Fatal(err)
	}
	var state string
	err = f.db.Txn(ctx, func(sc context.Context) error {
		var e error
		state, e = f.a.failJob(sc, acquired, l, JobFailure{Code: "network", Message: "network unavailable", Retryable: true, RetryAfter: time.Minute}, now)
		return e
	})
	if err != nil || state != "queued" {
		t.Fatal(state, err)
	}
	if _, _, err = f.a.acquire(ctx, "early", []string{"connection_test"}, now.Add(59*time.Second)); !errors.Is(err, mongo.ErrNoDocuments) {
		t.Fatal(err)
	}
	acquired, l, err = f.a.acquire(ctx, "later", []string{"connection_test"}, now.Add(61*time.Second))
	if err != nil || acquired.Attempts != 2 {
		t.Fatal(acquired, err)
	}
	err = f.db.Txn(ctx, func(sc context.Context) error {
		var e error
		state, e = f.a.failJob(sc, acquired, l, JobFailure{Code: "permanent", Message: "stop"}, now)
		return e
	})
	if err != nil || state != "failed" {
		t.Fatal(state, err)
	}
	stored, err := findOne[JobDoc](ctx, f.db, "jobs", bson.M{"_id": acquired.ID})
	if err != nil || stored.Active {
		t.Fatal(stored, err)
	}
	replacement := testJob(acquired.OrgID, "key", now)
	if err = f.db.Txn(ctx, func(sc context.Context) error { return f.a.enqueue(sc, replacement) }); err != nil {
		t.Fatal(err)
	}
}
func TestMaxAttemptsIntegration(t *testing.T) {
	f := newTestApp(t, false)
	ctx := context.Background()
	job := testJob(newID("org"), "max", time.Now().UTC())
	job.MaxAttempts = 1
	job.Attempts = 1
	job.OperationID = ptr(newID("op"))
	op := OperationDoc{ID: *job.OperationID, OrgID: job.OrgID, Kind: job.Kind, Subject: Subject{"connection", newID("con")}, Status: "pending", Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	_, err := f.db.Coll("operations").InsertOne(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.Txn(ctx, func(sc context.Context) error { return f.a.enqueue(sc, job) }); err != nil {
		t.Fatal(err)
	}
	acquired, lease, err := f.a.acquire(ctx, "worker", []string{"connection_test"}, time.Now().UTC())
	if err != nil || acquired.Attempts != 2 {
		t.Fatal(acquired, err)
	}
	f.a.runJob(acquired, lease)
	stored, err := findOne[JobDoc](ctx, f.db, "jobs", bson.M{"_id": job.ID})
	if err != nil || stored.State != "dead" || stored.Active {
		t.Fatal(stored, err)
	}
	op, err = findOne[OperationDoc](ctx, f.db, "operations", bson.M{"_id": op.ID})
	if err != nil || op.Status != "failed" || (*op.Problem)["code"] != "max_attempts_exceeded" {
		t.Fatal(op, err)
	}
}
func TestTwoRunnersIntegration(t *testing.T) {
	f := newTestApp(t, false)
	f.bootstrap()
	org := f.org("alpha")
	for i := 0; i < 12; i++ {
		id := f.connection(org, fmt.Sprintf("connection-%d", i), "http://127.0.0.1:1")
		f.startTest(org, id, fmt.Sprint(i))
	}
	second := NewApp(f.a.Config, f.db, f.creds)
	defer second.Stop()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); f.a.runWorkers() }()
	go func() { defer wg.Done(); second.runWorkers() }()
	t.Cleanup(func() { f.a.Stop(); second.Stop(); wg.Wait() })
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		n, _ := f.db.Coll("jobs").CountDocuments(context.Background(), bson.M{"state": "succeeded"})
		if n == 12 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	n, err := f.db.Coll("jobs").CountDocuments(context.Background(), bson.M{"state": "succeeded"})
	if err != nil || n != 12 {
		t.Fatal(n, err)
	}
	events, err := f.a.fetchEvents(context.Background(), org, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, e := range events {
		if e.Type == "connection.tested" {
			counts[e.Subject.ID]++
		}
	}
	if len(counts) != 12 {
		t.Fatal(counts)
	}
	for _, n := range counts {
		if n != 1 {
			t.Fatal("duplicate result", counts)
		}
	}
}
func TestCrashRecoveryIntegration(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
			w.WriteHeader(200)
		}
	}))
	defer up.Close()
	f := newTestApp(t, true)
	f.bootstrap()
	org := f.org("alpha")
	id := f.connection(org, "Jira", up.URL)
	op := f.startTest(org, id, "crash")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("job did not reach upstream")
	}
	f.a.Stop()
	f.stop()
	if err := <-f.done; err != nil {
		t.Fatal(err)
	}
	f.start(f.a.Config, true)
	finished := f.waitOperation(org, op["id"].(string))
	if finished["status"] != "succeeded" {
		t.Fatal(finished)
	}
	events, err := f.a.fetchEvents(context.Background(), org, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, ev := range events {
		if ev.Type == "connection.tested" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("duplicate final result", count)
	}
	job, err := findOne[JobDoc](context.Background(), f.db, "jobs", bson.M{"org_id": org})
	if err != nil || job.Attempts < 2 || job.Fence < 2 {
		t.Fatal(job, err)
	}
}
func TestArtifactOrgIsolationIntegration(t *testing.T) {
	f := newTestApp(t, false)
	ctx := context.Background()
	first, other := newID("org"), newID("org")
	var ids []string
	for _, org := range []string{first, first, other} {
		blob, err := storeBlob(f.a.Config.DataDir, org, strings.NewReader("same"), 10)
		if err != nil {
			t.Fatal(err)
		}
		var doc ArtifactDoc
		err = f.db.Txn(ctx, func(sc context.Context) error {
			var e error
			doc, e = f.a.recordArtifact(sc, org, blob, "text/plain", "test", Subject{})
			return e
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, doc.ID)
	}
	if ids[0] != ids[1] || ids[0] == ids[2] {
		t.Fatal(ids)
	}
}
