package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"orqen/internal/backup"
)

func init() {
	for _, r := range []string{
		"POST /api/v1/orgs/{orgId}/system/backups",
		"POST /api/v1/orgs/{orgId}/system/backups/{backupName}/verify",
		"POST /api/v1/orgs/{orgId}/system/backups/prune",
	} {
		idemRequired[r] = true
	}
}

// Backup settings limits (X31).
const (
	backupSettingsID      = "backup"
	backupIntervalDefault = 24
	backupKeepLastDefault = 7
	backupKeepDaysDefault = 30
)

// BackupSettingsDoc is the singleton _id:"backup" (collection backup_settings). Backups are system-wide, not policy keys.
type BackupSettingsDoc struct {
	ID                string    `bson:"_id"`
	Enabled           bool      `bson:"enabled"`
	IntervalHours     int       `bson:"interval_hours"`
	KeepLast          int       `bson:"keep_last"`
	KeepDays          int       `bson:"keep_days"`
	Dir               string    `bson:"dir"`
	OnArtifactPattern string    `bson:"on_artifact_pattern"`
	Revision          int64     `bson:"revision"`
	UpdatedAt         time.Time `bson:"updated_at"`
}

// BackupFailure is where a backup stopped. It never holds a value, only a code and a location.
type BackupFailure struct {
	Code     string `bson:"code"`
	Location string `bson:"location,omitempty"`
}

// BackupRunDoc is one backup attempt (collection backup_runs, excluded from backups).
type BackupRunDoc struct {
	ID         string         `bson:"_id"`
	Name       string         `bson:"name"`
	State      string         `bson:"state"` // running | succeeded | failed
	Trigger    string         `bson:"trigger"`
	Bytes      int64          `bson:"bytes"`
	Failure    *BackupFailure `bson:"failure"`
	StartedAt  time.Time      `bson:"started_at"`
	FinishedAt *time.Time     `bson:"finished_at"`
}

func (d BackupRunDoc) DTO() bson.M {
	var fin, fail any
	if d.FinishedAt != nil {
		fin = timestamp(*d.FinishedAt)
	}
	if d.Failure != nil {
		fail = bson.M{"code": d.Failure.Code, "location": optStr(d.Failure.Location)}
	}
	return bson.M{"id": d.ID, "name": d.Name, "state": d.State, "trigger": d.Trigger, "bytes": d.Bytes, "failure": fail, "started_at": timestamp(d.StartedAt), "finished_at": fin}
}

func (a *App) backupClock() time.Time {
	if a.backupNow != nil {
		return a.backupNow().UTC()
	}
	return time.Now().UTC()
}

func (a *App) defaultBackupDir() string { return filepath.Join(a.Config.DataDir, "backups") }

func (a *App) backupSettings(ctx context.Context) (BackupSettingsDoc, error) {
	d, err := findOne[BackupSettingsDoc](ctx, a.DB, "backup_settings", bson.M{"_id": backupSettingsID})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return BackupSettingsDoc{ID: backupSettingsID, IntervalHours: backupIntervalDefault, KeepLast: backupKeepLastDefault, KeepDays: backupKeepDaysDefault, OnArtifactPattern: backup.PatternFail}, nil
	}
	return d, err
}

func (d BackupSettingsDoc) effectiveDir(def string) string {
	if d.Dir == "" {
		return def
	}
	return d.Dir
}

func (a *App) backupSettingsDTO(d BackupSettingsDoc) bson.M {
	return bson.M{"enabled": d.Enabled, "interval_hours": d.IntervalHours, "keep_last": d.KeepLast, "keep_days": d.KeepDays, "dir": d.effectiveDir(a.defaultBackupDir()),
		"default_dir": a.defaultBackupDir(), "on_artifact_pattern": d.OnArtifactPattern, "revision": d.Revision}
}

// resolveExisting evaluates symlinks of the longest existing ancestor and appends the rest, so a directory that does not
// exist yet can still be compared with the protected ones.
func resolveExisting(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

func insideOrEqual(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// validBackupDir enforces X31: absolute, symlinks resolved, outside the data directories that must never be backed up
// into and outside the personal state of the tools Orqen drives. No user-specific path is hardcoded.
func validBackupDir(dataDir, home, dir string) (string, bool) {
	if dir == "" || !filepath.IsAbs(dir) {
		return "", false
	}
	real := resolveExisting(dir)
	data := resolveExisting(dataDir)
	for _, sub := range []string{"artifacts", "workspaces", "wiki", "codegraph", "scans", "catherd"} {
		if insideOrEqual(filepath.Join(data, sub), real) {
			return "", false
		}
	}
	if home != "" {
		h := resolveExisting(home)
		for _, sub := range []string{".config", ".local/share/catherd", ".claude", ".codex", ".openclaw", "Library/LaunchAgents"} {
			if insideOrEqual(filepath.Join(h, filepath.FromSlash(sub)), real) {
				return "", false
			}
		}
	}
	return real, true
}

func (a *App) backupRoutes() []Route {
	const p = "/api/v1/orgs/{orgId}/system"
	return []Route{
		{"GET", p + "/backups/settings", a.getBackupSettings}, {"PUT", p + "/backups/settings", a.putBackupSettings},
		{"GET", p + "/backups", a.listBackups}, {"POST", p + "/backups", a.startBackup},
		{"GET", p + "/backups/prune-preview", a.previewBackupPrune}, {"POST", p + "/backups/prune", a.pruneBackupsRoute},
		{"GET", p + "/backups/{backupName}", a.getBackup}, {"POST", p + "/backups/{backupName}/verify", a.verifyBackup},
		{"GET", p + "/restore/last", a.getLastRestore}, {"GET", p + "/support-bundle", a.getSupportBundle},
	}
}

func (a *App) getBackupSettings(w http.ResponseWriter, r *http.Request) error {
	d, err := a.backupSettings(r.Context())
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, 200, a.backupSettingsDTO(d))
	return nil
}

func (a *App) putBackupSettings(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	var req struct {
		Enabled           bool   `json:"enabled"`
		IntervalHours     int    `json:"interval_hours"`
		KeepLast          int    `json:"keep_last"`
		KeepDays          int    `json:"keep_days"`
		Dir               string `json:"dir"`
		OnArtifactPattern string `json:"on_artifact_pattern"`
	}
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	var bad []FieldError
	if req.IntervalHours < 1 || req.IntervalHours > 168 {
		bad = append(bad, FieldError{Field: "interval_hours", Message: "must be 1 to 168"})
	}
	if req.KeepLast < 1 || req.KeepLast > 100 {
		bad = append(bad, FieldError{Field: "keep_last", Message: "must be 1 to 100"})
	}
	if req.KeepDays < 0 || req.KeepDays > 3650 {
		bad = append(bad, FieldError{Field: "keep_days", Message: "must be 0 to 3650"})
	}
	if req.OnArtifactPattern != backup.PatternFail && req.OnArtifactPattern != backup.PatternExcludeArtifact {
		bad = append(bad, FieldError{Field: "on_artifact_pattern", Message: "must be fail or exclude_artifact"})
	}
	if len(bad) > 0 {
		return validationErrors(bad)
	}
	dir := ""
	if req.Dir != "" {
		home, _ := os.UserHomeDir()
		real, ok := validBackupDir(a.Config.DataDir, home, req.Dir)
		if !ok {
			return problem("backup_dir_invalid")
		}
		dir = real
	}
	var out BackupSettingsDoc
	err = a.DB.Txn(r.Context(), func(sc context.Context) error {
		cur, e := a.backupSettings(sc)
		if e != nil {
			return e
		}
		if cur.Revision != expected {
			c := problem("revision_conflict")
			c.Current = a.backupSettingsDTO(cur)
			return c
		}
		out = BackupSettingsDoc{ID: backupSettingsID, Enabled: req.Enabled, IntervalHours: req.IntervalHours, KeepLast: req.KeepLast, KeepDays: req.KeepDays, Dir: dir,
			OnArtifactPattern: req.OnArtifactPattern, Revision: cur.Revision + 1, UpdatedAt: time.Now().UTC()}
		if cur.Revision == 0 {
			_, e = a.DB.Coll("backup_settings").InsertOne(sc, out)
			if mongo.IsDuplicateKeyError(e) {
				c := problem("revision_conflict")
				return c
			}
			return e
		}
		res, e := a.DB.Coll("backup_settings").ReplaceOne(sc, bson.M{"_id": backupSettingsID, "revision": cur.Revision}, out)
		if e != nil {
			return e
		}
		if res.MatchedCount == 0 {
			return problem("revision_conflict")
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.Committed()
	w.Header().Set("ETag", etag(out.Revision))
	writeJSON(w, 200, a.backupSettingsDTO(out))
	return nil
}

// backupEntries lists the valid and invalid backup directories of dir, newest first.
func (a *App) backupEntries(dir string) ([]backup.ListEntry, error) {
	es, err := backup.List(backup.OSFS{}, dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return es, err
}

func manifestBytes(m *backup.Manifest) int64 {
	n := m.Artifacts.Bytes
	for _, c := range m.Collections {
		n += c.Bytes
	}
	return n
}

func (a *App) lastBackupSuccess(ctx context.Context) (*time.Time, error) {
	rows, err := findMany[BackupRunDoc](ctx, a.DB, "backup_runs", bson.M{"state": "succeeded"}, bson.D{{Key: "started_at", Value: -1}}, 1)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	t := rows[0].StartedAt
	if rows[0].FinishedAt != nil {
		t = *rows[0].FinishedAt
	}
	return &t, nil
}

func (a *App) listBackups(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	s, err := a.backupSettings(ctx)
	if err != nil {
		return err
	}
	dir := s.effectiveDir(a.defaultBackupDir())
	es, err := a.backupEntries(dir)
	if err != nil {
		return err
	}
	entries := []bson.M{}
	for _, e := range es {
		row := bson.M{"name": e.Name, "created_at": nil, "bytes": 0, "orgs": []backup.Org{}, "valid": e.Valid}
		if e.Manifest != nil {
			row["created_at"], row["bytes"], row["orgs"] = timestamp(e.Manifest.CreatedAt), manifestBytes(e.Manifest), nonNilOrgs(e.Manifest.Orgs)
		}
		entries = append(entries, row)
	}
	runs, err := findMany[BackupRunDoc](ctx, a.DB, "backup_runs", bson.M{}, bson.D{{Key: "started_at", Value: -1}}, 20)
	if err != nil {
		return err
	}
	dtos := []bson.M{}
	for _, d := range runs {
		dtos = append(dtos, d.DTO())
	}
	var next any
	if s.Enabled {
		last, e := a.lastBackupSuccess(ctx)
		if e != nil {
			return e
		}
		t := a.backupClock()
		if last != nil {
			t = last.Add(time.Duration(s.IntervalHours) * time.Hour)
		}
		next = timestamp(t)
	}
	writeJSON(w, 200, bson.M{"dir": dir, "entries": entries, "runs": dtos, "next_due_at": next})
	return nil
}

func nonNilOrgs(o []backup.Org) []backup.Org {
	if o == nil {
		return []backup.Org{}
	}
	return o
}

func (a *App) startBackup(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org := r.PathValue("orgId")
	route := "POST /api/v1/orgs/{orgId}/system/backups"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org}, nil), func(ctx context.Context) (Output, error) {
		return a.startSystemOperation(ctx, org, "backup_run", Subject{"system", "backup"}, "backup:system", 3, bson.M{"trigger": "manual", "op_org": org})
	})
}

// startSystemOperation queues a system job (empty org id) whose operation belongs to the requesting organization, so the
// owner can follow it through the ordinary operations route.
func (a *App) startSystemOperation(ctx context.Context, org, kind string, subject Subject, dedupe string, maxAttempts int, payload bson.M) (Output, error) {
	now := time.Now().UTC()
	opID := newID("op")
	job := JobDoc{ID: newID("job"), OrgID: "", Kind: kind, DedupeKey: dedupe, Payload: payload, State: "queued", Active: true, MaxAttempts: maxAttempts, NextAt: now, OperationID: &opID, CreatedAt: now, UpdatedAt: now}
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
	return Output{202, op.DTO(), map[string]string{"location": "/api/v1/orgs/" + org + "/operations/" + opID}}, nil
}

func (a *App) openBackup(ctx context.Context, name string) (*backup.Backup, string, error) {
	if !backup.NamePattern.MatchString(name) {
		return nil, "", problem("not_found")
	}
	s, err := a.backupSettings(ctx)
	if err != nil {
		return nil, "", err
	}
	dir := s.effectiveDir(a.defaultBackupDir())
	b, err := backup.Open(backup.OSFS{}, filepath.Join(dir, name))
	if err != nil {
		var ie *backup.IntegrityError
		if errors.As(err, &ie) {
			return nil, dir, problem("restore_integrity_failed")
		}
		return nil, dir, problem("not_found")
	}
	return b, dir, nil
}

func (a *App) getBackup(w http.ResponseWriter, r *http.Request) error {
	b, _, err := a.openBackup(r.Context(), r.PathValue("backupName"))
	if err != nil {
		return err
	}
	raw, err := json.Marshal(b.Manifest)
	if err != nil {
		return err
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		return err
	}
	out["name"], out["bytes"] = b.Name, manifestBytes(&b.Manifest)
	writeJSON(w, 200, out)
	return nil
}

func (a *App) verifyBackup(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org, name := r.PathValue("orgId"), r.PathValue("backupName")
	route := "POST /api/v1/orgs/{orgId}/system/backups/{backupName}/verify"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "backupName": name}, nil), func(ctx context.Context) (Output, error) {
		b, _, e := a.openBackup(ctx, name)
		if e != nil {
			return Output{}, e
		}
		p, e := a.previewBackup(ctx, b)
		if e != nil {
			return Output{}, e
		}
		return Output{200, p, nil}, nil
	})
}

func (a *App) prunePlan(ctx context.Context) (backup.PrunePlan, BackupSettingsDoc, string, error) {
	s, err := a.backupSettings(ctx)
	if err != nil {
		return backup.PrunePlan{}, s, "", err
	}
	dir := s.effectiveDir(a.defaultBackupDir())
	es, err := a.backupEntries(dir)
	if err != nil {
		return backup.PrunePlan{}, s, dir, err
	}
	var cands []backup.Candidate
	for _, e := range es {
		c := backup.Candidate{Name: e.Name, Valid: e.Valid}
		if e.Manifest != nil {
			c.CreatedAt = e.Manifest.CreatedAt
		}
		cands = append(cands, c)
	}
	return backup.PlanPrune(cands, s.KeepLast, s.KeepDays, a.backupClock()), s, dir, nil
}

func (a *App) previewBackupPrune(w http.ResponseWriter, r *http.Request) error {
	plan, _, _, err := a.prunePlan(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, 200, bson.M{"delete": plan.Delete, "keep": plan.Keep})
	return nil
}

// applyPrune deletes only the planned directories, each of which matches the backup name pattern and lies directly in dir.
func (a *App) applyPrune(ctx context.Context) ([]string, error) {
	plan, _, dir, err := a.prunePlan(ctx)
	if err != nil {
		return nil, err
	}
	deleted := []string{}
	for _, name := range plan.Delete {
		p := filepath.Join(dir, name)
		if !backup.NamePattern.MatchString(name) || filepath.Dir(p) != filepath.Clean(dir) {
			continue
		}
		if err = os.RemoveAll(p); err != nil {
			return deleted, err
		}
		deleted = append(deleted, name)
	}
	sort.Strings(deleted)
	if len(deleted) > 0 {
		err = a.DB.Txn(ctx, func(sc context.Context) error {
			return a.AppendEvent(sc, "", "system.backup_pruned", Subject{"system", "backup"}, bson.M{"count": len(deleted)}, nil, nil, true)
		})
		if err == nil {
			a.Committed()
		}
	}
	return deleted, err
}

func (a *App) pruneBackupsRoute(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	route := "POST /api/v1/orgs/{orgId}/system/backups/prune"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org}, req), func(ctx context.Context) (Output, error) {
		if !req.Confirm {
			return Output{}, problem("confirm_required")
		}
		deleted, e := a.applyPrune(ctx)
		if e != nil {
			return Output{}, e
		}
		return Output{200, bson.M{"deleted": deleted}, nil}, nil
	})
}

func (a *App) getLastRestore(w http.ResponseWriter, r *http.Request) error {
	rows, err := findMany[bson.M](r.Context(), a.DB, "restore_reports", bson.M{}, bson.D{{Key: "created_at", Value: -1}}, 1)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return problem("not_found")
	}
	writeJSON(w, 200, restoreReportDTO(rows[0]))
	return nil
}

// restoreReportDTO renders a restore_reports document (written by `orqen restore`) without its internal id.
func restoreReportDTO(d bson.M) bson.M {
	out := bson.M{}
	for k, v := range d {
		if k == "_id" {
			out["id"] = v
			continue
		}
		out[k] = v
	}
	return out
}

// sweepBackupDue enqueues one scheduled backup when backups are enabled and the last success is older than the interval.
// A failed attempt is not retried for an hour, so a broken backup directory does not enqueue a job on every sweep.
func (a *App) sweepBackupDue(ctx context.Context) error {
	s, err := a.backupSettings(ctx)
	if err != nil || !s.Enabled {
		return err
	}
	last, err := a.lastBackupSuccess(ctx)
	if err != nil {
		return err
	}
	now := a.backupClock()
	if !backup.Due(s.Enabled, last, s.IntervalHours, now) {
		return nil
	}
	failed, err := findMany[BackupRunDoc](ctx, a.DB, "backup_runs", bson.M{"state": "failed"}, bson.D{{Key: "started_at", Value: -1}}, 1)
	if err != nil {
		return err
	}
	if len(failed) > 0 && failed[0].FinishedAt != nil && now.Sub(*failed[0].FinishedAt) < time.Hour && (last == nil || failed[0].FinishedAt.After(*last)) {
		return nil
	}
	err = a.txnRetryJob(ctx, func(sc context.Context) error {
		return a.ensureJobTx(sc, "", "backup_run", "backup:system", 3, bson.M{"trigger": "schedule"})
	})
	if err == nil {
		a.Committed()
	}
	return err
}
