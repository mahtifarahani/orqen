package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"orqen/internal/backup"
	"orqen/internal/policy"
)

// Failure codes of a backup beyond those of internal/backup.
const (
	codeSnapshotTooOld = "backup_snapshot_too_old"
	codeBackupFailed   = "backup_failed"
)

// credentialDocs are the included collections whose documents point at the credential store.
var credentialDocs = []string{"connections", "publisher_credentials", "catherd_secrets", "extension_states"}

// credentialRefsOf lists the credential references of one document of a credential-bearing collection. Only references
// are read, never values.
func credentialRefsOf(coll string, doc bson.M) []backup.CredentialRef {
	id, _ := doc["_id"].(string)
	org, _ := doc["org_id"].(string)
	mk := func(kind, ref string) backup.CredentialRef {
		return backup.CredentialRef{Ref: ref, Kind: kind, Owner: id, OrgID: org}
	}
	var out []backup.CredentialRef
	switch coll {
	case "connections":
		if ref, _ := doc["credential_ref"].(string); ref != "" {
			out = append(out, mk("connection", ref))
		}
	case "publisher_credentials":
		if ref, _ := doc["credential_ref"].(string); ref != "" {
			out = append(out, mk("publisher_credential", ref))
		}
	case "catherd_secrets":
		if ref, _ := doc["credential_ref"].(string); ref != "" {
			out = append(out, mk("catherd_secret", ref))
		}
	case "extension_states":
		for _, v := range asDoc(doc["credentials"]) {
			if ref, _ := asDoc(v)["credential_ref"].(string); ref != "" {
				out = append(out, mk("extension", ref))
			}
		}
	}
	return out
}

// asDoc views an embedded document as a map whichever Go type the driver decoded it into.
func asDoc(v any) map[string]any {
	switch d := v.(type) {
	case bson.M:
		return d
	case map[string]any:
		return d
	case bson.D:
		out := make(map[string]any, len(d))
		for _, e := range d {
			out[e.Key] = e.Value
		}
		return out
	}
	return nil
}

func newBackupName(now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return backup.NewName(now, hex.EncodeToString(b[:]))
}

// exported summarizes what an export wrote.
type exported struct {
	name  string
	bytes int64
}

func docID(raw bson.Raw) string {
	v, err := raw.LookupErr("_id")
	if err != nil {
		return ""
	}
	if s, ok := v.StringValueOK(); ok {
		return s
	}
	if oid, ok := v.ObjectIDOK(); ok {
		return oid.Hex()
	}
	return v.String()
}

// exportBackup writes one backup of the whole install (X27): every included collection is read from one MongoDB
// snapshot (one cluster time) in _id order, each line is scanned for secrets before it is compressed, and the artifact
// blobs referenced by the snapshotted records are copied afterwards (blobs are written before their records).
func (a *App) exportBackup(ctx context.Context, dir, mode string) (res exported, err error) {
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return res, &backup.Error{Code: backup.CodeDirUnwritable, Err: err}
	}
	now := a.backupClock()
	name := newBackupName(now)
	sess, err := a.DB.Database.Client().StartSession(options.Session().SetSnapshot(true))
	if err != nil {
		return res, err
	}
	defer sess.EndSession(ctx)
	sc := mongo.NewSessionContext(ctx, sess)

	// Known credential values come from the same snapshot, so every reference that is in the backup is checked.
	var values []string
	for _, coll := range credentialDocs {
		cur, e := a.DB.Coll(coll).Find(sc, bson.M{})
		if e != nil {
			return res, e
		}
		var docs []bson.M
		if e = cur.All(sc, &docs); e != nil {
			return res, e
		}
		for _, d := range docs {
			for _, ref := range credentialRefsOf(coll, d) {
				v, ok, ge := a.Creds.Get(ctx, ref.Ref)
				if ge != nil {
					return res, ge
				}
				if ok {
					values = append(values, v)
				}
			}
		}
	}
	w, err := backup.NewWriter(backup.OSFS{}, dir, name, mode, backup.NewScanner(values))
	if err != nil {
		return res, err
	}
	defer func() {
		if err != nil {
			w.Abort()
		}
	}()
	var orgs []backup.Org
	type blobRef struct{ org, digest, id string }
	var blobs []blobRef
	for _, coll := range backup.IncludedCollections() {
		cur, e := a.DB.Coll(coll).Find(sc, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
		if e != nil {
			return res, e
		}
		next := func() (string, []byte, bool, error) {
			if !cur.Next(sc) {
				return "", nil, false, cur.Err()
			}
			raw := cur.Current
			line, e := bson.MarshalExtJSON(raw, true, false)
			if e != nil {
				return "", nil, false, e
			}
			id := docID(raw)
			switch coll {
			case "organizations":
				var o struct {
					ID   string `bson:"_id"`
					Slug string `bson:"slug"`
					Name string `bson:"name"`
				}
				if bson.Unmarshal(raw, &o) == nil {
					orgs = append(orgs, backup.Org{ID: o.ID, Slug: o.Slug, Name: o.Name})
				}
			case "artifacts":
				var d ArtifactDoc
				if bson.Unmarshal(raw, &d) == nil {
					blobs = append(blobs, blobRef{d.OrgID, d.Sha256, d.ID})
				}
			}
			return id, line, true, nil
		}
		_, e = w.WriteCollection(coll, next)
		_ = cur.Close(sc)
		if e != nil {
			return res, e
		}
	}
	var clusterTime string
	if ts := sess.OperationTime(); ts != nil {
		clusterTime = fmt.Sprintf("%d.%d", ts.T, ts.I)
	}
	written := map[string]bool{}
	for _, b := range blobs {
		key := b.org + "/" + b.digest
		if written[key] {
			continue
		}
		p, e := artifactPath(a.artifactRoot(), b.org, b.digest)
		if e != nil {
			return res, &backup.Error{Code: backup.CodeArtifactMissing, Location: b.id, Err: e}
		}
		f, e := os.Open(p)
		if e != nil {
			return res, &backup.Error{Code: backup.CodeArtifactMissing, Location: b.id, Err: e}
		}
		ok, e := w.CopyBlob(b.org, b.digest, b.id, f)
		_ = f.Close()
		if e != nil {
			return res, e
		}
		written[key] = ok
	}
	sort.Slice(orgs, func(i, j int) bool { return orgs[i].ID < orgs[j].ID })
	m := backup.Manifest{OrqenVersion: Version, SchemaVersion: SchemaVersion, PolicyDefaultsVersion: policy.ProductDefaultsVersion, ClusterTime: clusterTime, Orgs: nonNilOrgs(orgs)}
	if _, err = w.Finalize(m, now); err != nil {
		return res, err
	}
	res.name = name
	if b, e := backup.Open(backup.OSFS{}, filepath.Join(dir, name)); e == nil {
		res.bytes = manifestBytes(&b.Manifest)
	}
	return res, nil
}

// backupFailure maps an export error to a stable code and location, never a value.
func backupFailure(err error) (code, location string) {
	var be *backup.Error
	if errors.As(err, &be) {
		return be.Code, be.Location
	}
	var ce mongo.CommandError
	if errors.As(err, &ce) && ce.Code == 246 {
		return codeSnapshotTooOld, ""
	}
	return codeBackupFailed, ""
}

// runBackup is the system job backup_run (X27 to X32). Its failures are results: they are recorded on the backup run, the
// operation and an event, and the job completes. Only a lost lease or a database failure fails the job.
func (a *App) runBackup(ctx context.Context, job JobDoc, lease Lease) error {
	s, err := a.backupSettings(ctx)
	if err != nil {
		return err
	}
	trigger, _ := job.Payload["trigger"].(string)
	if trigger == "" {
		trigger = "schedule"
	}
	opJob := job
	opJob.OrgID, _ = job.Payload["op_org"].(string)
	run := BackupRunDoc{ID: newID("bkr"), State: "running", Trigger: trigger, StartedAt: a.backupClock()}
	if err = a.fenced(ctx, lease, func(sc context.Context) error {
		if _, e := a.DB.Coll("backup_runs").InsertOne(sc, run); e != nil {
			return e
		}
		return a.transitionOperation(sc, opJob, "running", nil)
	}); err != nil {
		return err
	}
	dir := s.effectiveDir(a.defaultBackupDir())
	real, ok := validBackupDir(a.Config.DataDir, homeDir(), dir)
	if !ok {
		return a.finishBackup(ctx, job, opJob, lease, run, exported{}, &backup.Error{Code: "backup_dir_invalid"})
	}
	dir = real
	res, xerr := a.exportBackup(ctx, dir, s.OnArtifactPattern)
	return a.finishBackup(ctx, job, opJob, lease, run, res, xerr)
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func (a *App) finishBackup(ctx context.Context, job, opJob JobDoc, lease Lease, run BackupRunDoc, res exported, xerr error) error {
	fin := a.backupClock()
	err := a.fenced(ctx, lease, func(sc context.Context) error {
		set := bson.M{"finished_at": fin}
		if xerr == nil {
			set["state"], set["name"], set["bytes"] = "succeeded", res.name, res.bytes
		} else {
			code, loc := backupFailure(xerr)
			set["state"], set["failure"] = "failed", BackupFailure{Code: code, Location: loc}
		}
		if _, e := a.DB.Coll("backup_runs").UpdateOne(sc, bson.M{"_id": run.ID}, bson.M{"$set": set}); e != nil {
			return e
		}
		if xerr == nil {
			if e := a.AppendEvent(sc, "", "system.backup_succeeded", Subject{"system", "backup"}, bson.M{"name": res.name, "bytes": res.bytes}, nil, nil, true); e != nil {
				return e
			}
			if e := a.transitionOperation(sc, opJob, "succeeded", bson.M{"name": res.name, "bytes": res.bytes}); e != nil {
				return e
			}
		} else {
			code, loc := backupFailure(xerr)
			if e := a.AppendEvent(sc, "", "system.backup_failed", Subject{"system", "backup"}, bson.M{"code": code, "location": optStr(loc)}, nil, nil, true); e != nil {
				return e
			}
			if e := a.transitionOperation(sc, opJob, "failed", bson.M{"code": code, "location": optStr(loc)}); e != nil {
				return e
			}
		}
		return a.completeJob(sc, lease)
	})
	if err != nil {
		return err
	}
	if xerr == nil {
		if _, perr := a.applyPrune(ctx); perr != nil {
			slog.Warn("backup prune failed", "error", perr)
		}
	}
	return nil
}
