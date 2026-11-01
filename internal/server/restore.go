package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"orqen/internal/backup"
	"orqen/internal/runstate"
)

// Restore refusal codes beyond those of internal/backup.
const (
	CodeRestoreIntegrity  = "restore_integrity_failed"
	CodeRestoreSchemaNew  = "restore_schema_too_new"
	CodeRestoreFailed     = "restore_failed"
	restoreLockID         = "restore"
	runnerBeatID          = "runner"
	runnerBeatFresh       = 60 * time.Second
	runnerBeatPeriod      = 10 * time.Second
	restoredIncompleteMsg = "restored_incomplete"
)

// RestoreError is a refusal or a failure of a restore. It never carries a value.
type RestoreError struct {
	Code     string   `json:"code"`
	File     string   `json:"file,omitempty"`
	NonEmpty []string `json:"non_empty,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

func (e *RestoreError) Error() string {
	s := e.Code
	if e.File != "" {
		s += " (" + e.File + ")"
	}
	if len(e.NonEmpty) > 0 {
		s += fmt.Sprintf(" %v", e.NonEmpty)
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

// RestoreLockedError is returned by Run while a restore is unfinished (X33).
type RestoreLockedError struct{ State string }

func (e *RestoreLockedError) Error() string {
	return "a restore is " + e.State + "; drop the database and the data directory, then retry the restore (`orqen restore --from <backup>`)"
}

// RestoreResult is what RestoreFromDir returns: the dry-run preview and, for a real restore, the report document.
type RestoreResult struct {
	Preview backup.PreviewDTO `json:"preview"`
	Report  bson.M            `json:"report,omitempty"`
}

// beatRunner records that a runner is alive, so a restore can refuse while a server is running.
func (a *App) beatRunner(ctx context.Context) {
	_, _ = a.DB.Coll("restore_locks").UpdateOne(ctx, bson.M{"_id": runnerBeatID}, bson.M{"$set": bson.M{"beat_at": time.Now().UTC(), "pid": os.Getpid()}}, options.UpdateOne().SetUpsert(true))
}

func (a *App) runnerBeatLoop() {
	a.beatRunner(a.ctx)
	t := time.NewTicker(runnerBeatPeriod)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
			a.beatRunner(a.ctx)
		}
	}
}

// restoreLockState returns the state of the restore lock ("" when there is none).
func restoreLockState(ctx context.Context, db *DB) (string, error) {
	var d struct {
		State string `bson:"state"`
	}
	err := db.Coll("restore_locks").FindOne(ctx, bson.M{"_id": restoreLockID}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil
	}
	return d.State, err
}

// checkRestoreLock makes the runner and the scheduler refuse to start while a restore is in progress or failed.
func checkRestoreLock(ctx context.Context, db *DB) error {
	st, err := restoreLockState(ctx, db)
	if err != nil {
		return err
	}
	if st == backup.LockInProgress || st == backup.LockFailed {
		return &RestoreLockedError{State: st}
	}
	return nil
}

func (a *App) runnerFresh(ctx context.Context) (bool, error) {
	var d struct {
		BeatAt time.Time `bson:"beat_at"`
	}
	err := a.DB.Coll("restore_locks").FindOne(ctx, bson.M{"_id": runnerBeatID}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	return err == nil && time.Since(d.BeatAt) < runnerBeatFresh, err
}

// RestoreFromDir restores a backup into an empty install (X33). With dryRun it only returns the preview of X35 and
// writes nothing. The refusals (integrity, schema, a running server, a non-empty target) are decided before the first
// write. The API never calls this; only `orqen restore` does.
func (a *App) RestoreFromDir(ctx context.Context, dir string, dryRun bool) (RestoreResult, error) {
	var res RestoreResult
	b, err := backup.Open(backup.OSFS{}, dir)
	if err != nil {
		var ie *backup.IntegrityError
		if errors.As(err, &ie) {
			return res, &RestoreError{Code: CodeRestoreIntegrity, File: ie.File}
		}
		return res, &RestoreError{Code: CodeRestoreIntegrity, Detail: err.Error()}
	}
	target, err := targetOf(ctx, a.DB, a.artifactRoot())
	if err != nil {
		return res, err
	}
	res.Preview, err = buildPreview(ctx, b, target, a.Creds, a.profileProbe(ctx))
	if err != nil {
		return res, err
	}
	if dryRun {
		return res, nil
	}
	if !res.Preview.IntegrityOK {
		return res, &RestoreError{Code: CodeRestoreIntegrity, File: res.Preview.FirstFailedFile}
	}
	if !res.Preview.SchemaCompatible {
		return res, &RestoreError{Code: CodeRestoreSchemaNew}
	}
	if fresh, e := a.runnerFresh(ctx); e != nil {
		return res, e
	} else if fresh {
		return res, &RestoreError{Code: backup.CodeServerRunning}
	}
	if st, e := restoreLockState(ctx, a.DB); e != nil {
		return res, e
	} else if st != "" {
		return res, &RestoreError{Code: backup.CodeTargetNotEmpty, NonEmpty: []string{"restore_locks"}}
	}
	if !res.Preview.TargetEmpty {
		return res, &RestoreError{Code: backup.CodeTargetNotEmpty, NonEmpty: res.Preview.TargetNonEmpty}
	}
	report, err := a.applyRestore(ctx, b, res.Preview)
	res.Report = report
	return res, err
}

func (a *App) applyRestore(ctx context.Context, b *backup.Backup, pv backup.PreviewDTO) (bson.M, error) {
	now := time.Now().UTC()
	if _, err := a.DB.Coll("restore_locks").InsertOne(ctx, bson.M{"_id": restoreLockID, "state": backup.LockInProgress, "backup_name": b.Name, "started_at": now}); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, &RestoreError{Code: backup.CodeTargetNotEmpty, NonEmpty: []string{"restore_locks"}}
		}
		return nil, err
	}
	fail := func(err error) (bson.M, error) {
		_, _ = a.DB.Coll("restore_locks").UpdateOne(ctx, bson.M{"_id": restoreLockID}, bson.M{"$set": bson.M{"state": backup.LockFailed, "failed_at": time.Now().UTC(), "code": CodeRestoreFailed}})
		var re *RestoreError
		if errors.As(err, &re) {
			return nil, err
		}
		return nil, &RestoreError{Code: CodeRestoreFailed, Detail: err.Error() + "; drop the database and data dir, then retry"}
	}
	// 3. automation is paused for every organization before anything else exists
	for _, o := range b.Manifest.Orgs {
		if !validID("org", o.ID) {
			return fail(fmt.Errorf("invalid organization id in the backup"))
		}
		t := now
		reason := "restored"
		doc := AutomationStateDoc{ID: automationStateID(o.ID), OrgID: o.ID, Paused: true, PausedAt: &t, Reason: &reason, Revision: 1, UpdatedAt: now}
		if _, err := a.DB.Coll("automation_states").InsertOne(ctx, doc); err != nil {
			return fail(err)
		}
	}
	// 4. collections in the restore order, batches of 500, ordered
	counts := map[string]int64{}
	for _, name := range backup.IncludedCollections() {
		n, err := a.restoreCollection(ctx, b, name, now)
		if err != nil {
			return fail(err)
		}
		counts[name] = n
	}
	// 5. artifacts, each re-hashed on the way in
	blobs, err := b.Blobs()
	if err != nil {
		return fail(err)
	}
	for _, kv := range blobs {
		if err = a.restoreBlob(b, kv[0], kv[1]); err != nil {
			return fail(err)
		}
	}
	// 6. marks (X34)
	held, err := a.postRestoreMarks(ctx, b, now)
	if err != nil {
		return fail(err)
	}
	// 7. post_restore_check per organization; the next server start runs them
	for _, o := range b.Manifest.Orgs {
		org := o.ID
		if err = a.txnRetryJob(ctx, func(sc context.Context) error {
			return a.ensureJobTx(sc, org, "post_restore_check", "postrestore:"+org, 5, bson.M{"backup_name": b.Name})
		}); err != nil {
			return fail(err)
		}
	}
	// 8. report and lock
	missing := []bson.M{}
	for _, c := range pv.MissingCredentials {
		missing = append(missing, bson.M{"org_id": c.OrgID, "kind": c.Kind, "owner": c.Owner, "ref": c.Ref})
	}
	republish := []bson.M{}
	for _, p := range pv.RepublishProfiles {
		republish = append(republish, bson.M{"org_id": p.OrgID, "name": p.Name})
	}
	orgIDs := []string{}
	for _, o := range b.Manifest.Orgs {
		orgIDs = append(orgIDs, o.ID)
	}
	report := bson.M{"_id": newID("rpt"), "backup_name": b.Name, "created_at": time.Now().UTC(), "orgs": orgIDs, "counts": counts, "blobs": len(blobs), "held_runs": held,
		"missing_credentials": missing, "republish_profiles": republish, "post_check": bson.M{}}
	if _, err = a.DB.Coll("restore_reports").InsertOne(ctx, report); err != nil {
		return fail(err)
	}
	if _, err = a.DB.Coll("restore_locks").UpdateOne(ctx, bson.M{"_id": restoreLockID}, bson.M{"$set": bson.M{"state": backup.LockDone, "finished_at": time.Now().UTC()}}); err != nil {
		return fail(err)
	}
	return report, nil
}

// restoreCollection inserts one collection of the backup. counters is replaced by id (the events counter already
// exists); automation_states keeps the paused state the restore wrote first.
func (a *App) restoreCollection(ctx context.Context, b *backup.Backup, name string, now time.Time) (int64, error) {
	var total int64
	coll := a.DB.Coll(name)
	var batch []any
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, err := coll.InsertMany(ctx, batch, options.InsertMany().SetOrdered(true))
		total += int64(len(batch))
		batch = batch[:0]
		return err
	}
	entryDocs := int64(0)
	for _, c := range b.Manifest.Collections {
		if c.Name == name {
			entryDocs = c.Docs
		}
	}
	if entryDocs == 0 {
		return 0, nil
	}
	err := b.ReadCollection(name, func(line []byte) error {
		switch {
		case backup.ReplaceOnRestore[name]:
			var d bson.D
			if err := bson.UnmarshalExtJSON(line, true, &d); err != nil {
				return &backup.IntegrityError{File: backup.CollectionFile(name)}
			}
			id := d[0].Value
			for _, e := range d {
				if e.Key == "_id" {
					id = e.Value
				}
			}
			_, err := coll.ReplaceOne(ctx, bson.M{"_id": id}, d, options.Replace().SetUpsert(true))
			total++
			return err
		case name == "automation_states":
			var d bson.M
			if err := bson.UnmarshalExtJSON(line, true, &d); err != nil {
				return &backup.IntegrityError{File: backup.CollectionFile(name)}
			}
			d["paused"], d["paused_at"], d["reason"], d["updated_at"] = true, now, "restored", now
			_, err := coll.ReplaceOne(ctx, bson.M{"_id": d["_id"]}, d, options.Replace().SetUpsert(true))
			total++
			return err
		}
		var d bson.D
		if err := bson.UnmarshalExtJSON(line, true, &d); err != nil {
			return &backup.IntegrityError{File: backup.CollectionFile(name)}
		}
		batch = append(batch, d)
		if len(batch) >= backup.BatchSize {
			return flush()
		}
		return nil
	})
	if err != nil {
		return total, err
	}
	return total, flush()
}

func (a *App) restoreBlob(b *backup.Backup, org, digest string) error {
	if !validID("org", org) {
		return fmt.Errorf("invalid organization id in the backup")
	}
	r, err := b.OpenBlob(org, digest)
	if err != nil {
		return err
	}
	defer r.Close()
	blob, err := storeBlob(a.artifactRoot(), org, io.LimitReader(r, backup.MaxBlob+1), backup.MaxBlob)
	if err != nil {
		return err
	}
	if blob.SHA256 != digest {
		return &backup.IntegrityError{File: backup.BlobPath(org, digest)}
	}
	return nil
}

// postRestoreMarks writes X34: a hold "restored" on every non-terminal run (only the user's Resume action clears it),
// non-terminal operations failed with restored_incomplete, a Wiki rebuild per organization and the restored event.
func (a *App) postRestoreMarks(ctx context.Context, b *backup.Backup, now time.Time) (int64, error) {
	res, err := a.DB.Coll("runs").UpdateMany(ctx, bson.M{"active": true}, bson.M{"$set": bson.M{"hold": RunHold{Code: runstate.HoldRestored, Detail: b.Name, Since: now}}})
	if err != nil {
		return 0, err
	}
	if _, err = a.DB.Coll("operations").UpdateMany(ctx, bson.M{"status": bson.M{"$in": []string{"pending", "running"}}},
		bson.M{"$set": bson.M{"status": "failed", "problem": bson.M{"code": restoredIncompleteMsg}, "updated_at": now}, "$inc": bson.M{"revision": int64(1)}}); err != nil {
		return 0, err
	}
	if err = a.markAuthorSessionsRestored(ctx, now); err != nil {
		return 0, err
	}
	for _, o := range b.Manifest.Orgs {
		if err = a.setWiki(ctx, o.ID, bson.M{"rebuild_requested": true}); err != nil {
			return 0, err
		}
		if err = a.AppendEvent(ctx, o.ID, "system.restored", Subject{"organization", o.ID}, bson.M{"backup_name": b.Name}, nil, nil, true); err != nil {
			return 0, err
		}
	}
	return res.ModifiedCount, nil
}

// markAuthorSessionsRestored fails every roles author session that was mid-flight: its catherd run lived outside the
// backup, so it cannot resume (S7). The person can retry it.
func (a *App) markAuthorSessionsRestored(ctx context.Context, now time.Time) error {
	_, err := a.DB.Coll("knowledge_author_sessions").UpdateMany(ctx, bson.M{"state": bson.M{"$nin": terminalAuthorStates}},
		bson.M{"$set": bson.M{"state": "failed", "code": "restored_incomplete", "updated_at": now}, "$inc": bson.M{"revision": int64(1)}})
	return err
}
