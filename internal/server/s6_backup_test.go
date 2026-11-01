package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"orqen/internal/backup"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func (e *scanEnv) backupSettings(t *testing.T, dir string, enabled bool, interval int, keepLast int, pattern string) {
	t.Helper()
	cur := e.json("GET", "/api/v1/orgs/"+e.org+"/system/backups/settings", nil, nil, 200)
	e.json("PUT", "/api/v1/orgs/"+e.org+"/system/backups/settings", map[string]any{"enabled": enabled, "interval_hours": interval, "keep_last": keepLast, "keep_days": 0, "dir": dir, "on_artifact_pattern": pattern},
		map[string]string{"If-Match": fmt.Sprint(int64(cur["revision"].(float64)))}, 200)
}

func (e *scanEnv) startBackup(t *testing.T) map[string]any {
	t.Helper()
	e.n++
	op := e.json("POST", "/api/v1/orgs/"+e.org+"/system/backups", nil, map[string]string{"Idempotency-Key": fmt.Sprintf("bk-%d", e.n)}, 202)
	return e.waitOperation(e.org, op["id"].(string))
}

func (e *scanEnv) addArtifact(t *testing.T, org, text string) string {
	t.Helper()
	b, err := storeBlob(e.a.artifactRoot(), org, strings.NewReader(text), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	id := newID("art")
	if _, err = e.db.Coll("artifacts").InsertOne(context.Background(), ArtifactDoc{ID: id, OrgID: org, Sha256: b.SHA256, Size: b.Size, MediaType: "text/plain", Kind: "evidence", Source: Subject{"run", e.rn.ID}, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *scanEnv) addOrg2(t *testing.T) string {
	t.Helper()
	now := time.Now().UTC()
	org := newID("org")
	if _, err := e.db.Coll("organizations").InsertOne(context.Background(), OrganizationDoc{ID: org, Slug: "second", Name: "Second", Locale: "en", Timezone: "UTC", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	r2 := RunDoc{ID: newID("run"), OrgID: org, ProjectID: newID("prj"), IssueID: "iss-1", IssueKey: e.rn.IssueKey, IntentKey: "x", State: "discovered", Active: true, Attempt: 1, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := e.db.Coll("runs").InsertOne(context.Background(), r2); err != nil {
		t.Fatal(err)
	}
	return org
}

func TestBackupCapturesTheInstallWithoutAnySecret(t *testing.T) {
	e := newScanEnv(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "backups")
	org2 := e.addOrg2(t)
	e.addArtifact(t, e.org, "evidence one")
	e.addArtifact(t, org2, "evidence two")
	if _, err := e.db.Coll("knowledge_records").InsertOne(ctx, bson.M{"_id": newID("kno"), "org_id": e.org, "title": "k"}); err != nil {
		t.Fatal(err)
	}
	e.scan(t, "findings") // a real scan: findings, artifacts and counts exist
	e.json("GET", "/api/v1/orgs/"+e.org+"/skills", nil, nil, 200)
	e.backupSettings(t, dir, false, 24, 7, "fail")
	op := e.startBackup(t)
	if op["status"] != "succeeded" {
		t.Fatal(op)
	}
	name := op["result"].(map[string]any)["name"].(string)
	b, err := backup.Open(backup.OSFS{}, filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	moving := map[string]bool{"events": true, "operations": true, "counters": true}
	have := map[string]bool{}
	for _, c := range b.Manifest.Collections {
		have[c.Name] = true
		if moving[c.Name] {
			continue
		}
		n, _ := e.db.Coll(c.Name).CountDocuments(ctx, bson.M{})
		if n != c.Docs {
			t.Fatalf("%s: manifest %d, database %d", c.Name, c.Docs, n)
		}
	}
	for _, c := range backup.IncludedCollections() {
		if !have[c] {
			t.Fatal("included collection missing from the backup:", c)
		}
	}
	for _, c := range backup.ExcludedCollections() {
		if have[c] {
			t.Fatal("excluded collection present:", c)
		}
		if _, err = os.Stat(filepath.Join(dir, name, backup.CollectionFile(c))); err == nil {
			t.Fatal("excluded collection file present:", c)
		}
	}
	if b.Manifest.Artifacts.Count < 3 || len(b.Manifest.Orgs) != 2 || b.Manifest.SecretScan.KnownValuesChecked < 1 {
		t.Fatalf("artifacts %+v orgs %d known %d", b.Manifest.Artifacts, len(b.Manifest.Orgs), b.Manifest.SecretScan.KnownValuesChecked)
	}
	// no stored credential value is anywhere in the decompressed bytes of the backup
	_ = filepath.Walk(filepath.Join(dir, name), func(p string, fi os.FileInfo, err error) error {
		if err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		raw, _ := os.ReadFile(p)
		if strings.HasSuffix(p, ".gz") {
			zr, e := gzip.NewReader(bytes.NewReader(raw))
			if e != nil {
				t.Fatal(p, e)
			}
			raw, _ = io.ReadAll(zr)
		}
		if bytes.Contains(raw, []byte(scanSecret)) {
			t.Fatal("a credential value is in", p)
		}
		return nil
	})
	// verify: every file ok, and this install is not empty
	e.n++
	pv := e.json("POST", "/api/v1/orgs/"+e.org+"/system/backups/"+name+"/verify", nil, map[string]string{"Idempotency-Key": fmt.Sprintf("v-%d", e.n)}, 200)
	if pv["integrity_ok"] != true || pv["target_empty"] != false || pv["can_restore"] != false || pv["schema_compatible"] != true {
		t.Fatal(pv)
	}
	list := e.json("GET", "/api/v1/orgs/"+e.org+"/system/backups", nil, nil, 200)
	if es := list["entries"].([]any); len(es) != 1 || es[0].(map[string]any)["valid"] != true {
		t.Fatal(list)
	}
	// the dry-run changed nothing and the support bundle holds neither documents nor secrets
	resp := e.request("GET", "/api/v1/orgs/"+e.org+"/system/support-bundle", nil, nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") || bytes.Contains(raw, []byte(scanSecret)) || !bytes.Contains(raw, []byte(`"versions"`)) {
		t.Fatal(resp.StatusCode, string(raw[:min(len(raw), 300)]))
	}
}

func TestBackupRefusesToWriteASecret(t *testing.T) {
	e := newScanEnv(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "backups")
	e.backupSettings(t, dir, false, 24, 7, "fail")
	leak := e.addArtifact(t, e.org, "config: key="+scanSecret)
	op := e.startBackup(t)
	p, _ := op["problem"].(map[string]any)
	if op["status"] != "failed" || p["code"] != "backup_secret_detected" || p["location"] != leak {
		t.Fatal(op)
	}
	if strings.Contains(fmt.Sprint(op), scanSecret) {
		t.Fatal("the value must not appear in the operation")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Fatal("a failed backup must leave no directory behind:", ents)
	}
	list := e.json("GET", "/api/v1/orgs/"+e.org+"/system/backups", nil, nil, 200)
	if len(list["entries"].([]any)) != 0 || list["runs"].([]any)[0].(map[string]any)["state"] != "failed" {
		t.Fatal(list)
	}
	// a pattern-only hit fails by default and is excluded (and recorded) in exclude_artifact mode
	if _, err := e.db.Coll("artifacts").DeleteMany(ctx, bson.M{"_id": leak}); err != nil {
		t.Fatal(err)
	}
	pat := e.addArtifact(t, e.org, "token glpat-"+strings.Repeat("a1b2", 6))
	if op = e.startBackup(t); op["status"] != "failed" || op["problem"].(map[string]any)["location"] != pat {
		t.Fatal(op)
	}
	e.backupSettings(t, dir, false, 24, 7, "exclude_artifact")
	if op = e.startBackup(t); op["status"] != "succeeded" {
		t.Fatal(op)
	}
	b, err := backup.Open(backup.OSFS{}, filepath.Join(dir, op["result"].(map[string]any)["name"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	if ex := b.Manifest.SecretScan.ExcludedArtifacts; len(ex) != 1 || ex[0].ArtifactID != pat || ex[0].PatternCode != "gitlab_token" {
		t.Fatalf("%+v", b.Manifest.SecretScan)
	}
	// a known credential value fails the backup even in exclude_artifact mode
	leak2 := e.addArtifact(t, e.org, "again "+scanSecret)
	if op = e.startBackup(t); op["status"] != "failed" || op["problem"].(map[string]any)["code"] != "backup_secret_detected" || op["problem"].(map[string]any)["location"] != leak2 {
		t.Fatal(op)
	}
	// a directory outside the allowed places is refused
	cur := e.json("GET", "/api/v1/orgs/"+e.org+"/system/backups/settings", nil, nil, 200)
	for _, bad := range []string{"relative/dir", filepath.Join(e.a.Config.DataDir, "artifacts", "x"), filepath.Join(homeDir(), ".claude", "backups")} {
		expectCode(t, e.json("PUT", "/api/v1/orgs/"+e.org+"/system/backups/settings", map[string]any{"enabled": false, "interval_hours": 24, "keep_last": 7, "keep_days": 0, "dir": bad, "on_artifact_pattern": "fail"},
			map[string]string{"If-Match": fmt.Sprint(int64(cur["revision"].(float64)))}, 422), "backup_dir_invalid")
	}
}

func TestBackupScheduleAndPruneStayInTheirLane(t *testing.T) {
	e := newScanEnv(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "backups")
	e.backupSettings(t, dir, true, 1, 2, "fail")
	var mu sync.Mutex
	clock := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }
	e.a.backupNow = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	jobs := func() int64 {
		n, _ := e.db.Coll("jobs").CountDocuments(ctx, bson.M{"kind": "backup_run"})
		return n
	}
	gate := make(chan struct{})
	e.a.Handle("backup_run", func(c context.Context, j JobDoc, l Lease) error { <-gate; return e.a.runBackup(c, j, l) })
	if err := e.a.sweepBackupDue(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.a.sweepBackupDue(ctx); err != nil {
		t.Fatal(err)
	}
	if jobs() != 1 {
		t.Fatal("two sweeps in one window must enqueue one job, got", jobs())
	}
	close(gate)
	done := func(n int64) {
		t.Helper()
		for i := 0; i < 500; i++ {
			c, _ := e.db.Coll("backup_runs").CountDocuments(ctx, bson.M{"state": "succeeded"})
			if c == n {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("backup did not finish", n)
	}
	done(1)
	e.waitOperationDone("")
	if err := e.a.sweepBackupDue(ctx); err != nil || jobs() != 1 {
		t.Fatal("not due yet", err, jobs())
	}
	advance(30 * time.Minute)
	if err := e.a.sweepBackupDue(ctx); err != nil || jobs() != 1 {
		t.Fatal("still inside the interval", err, jobs())
	}
	for i := 2; i <= 4; i++ {
		advance(61 * time.Minute)
		if err := e.a.sweepBackupDue(ctx); err != nil {
			t.Fatal(err)
		}
		done(int64(i))
		e.waitOperationDone("")
	}
	if jobs() != 4 {
		t.Fatal("one job per due window, got", jobs())
	}
	// the runner pruned to keep_last = 2; foreign and unvalid directories are never touched
	names := func() []string {
		es, _ := backup.List(backup.OSFS{}, dir)
		var out []string
		for _, x := range es {
			out = append(out, x.Name)
		}
		return out
	}
	// Prune runs after the job's transaction commits, so wait for it (a prune that never happens still fails).
	waitFor(t, "keep_last 2 after the prune", func() bool { return len(names()) == 2 })
	if got := names(); len(got) != 2 {
		t.Fatal("keep_last 2 expected, got", got)
	}
	foreign := []string{"notes", "orqen-backup-foo", "orqen-backup-20200101T000000Z-deadbeef"}
	for _, f := range foreign {
		if err := os.MkdirAll(filepath.Join(dir, f), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "notes", "keep.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.json("POST", "/api/v1/orgs/"+e.org+"/system/backups/prune", map[string]bool{"confirm": false}, map[string]string{"Idempotency-Key": "pr-1"}, 422)
	prev := e.json("GET", "/api/v1/orgs/"+e.org+"/system/backups/prune-preview", nil, nil, 200)
	if len(prev["delete"].([]any)) != 0 || len(prev["keep"].([]any)) != 2 {
		t.Fatal(prev)
	}
	e.backupSettings(t, dir, false, 24, 1, "fail")
	prev = e.json("GET", "/api/v1/orgs/"+e.org+"/system/backups/prune-preview", nil, nil, 200)
	if len(prev["delete"].([]any)) != 1 || len(prev["keep"].([]any)) != 1 {
		t.Fatal(prev)
	}
	res := e.json("POST", "/api/v1/orgs/"+e.org+"/system/backups/prune", map[string]bool{"confirm": true}, map[string]string{"Idempotency-Key": "pr-2"}, 200)
	if len(res["deleted"].([]any)) != 1 {
		t.Fatal(res)
	}
	for _, f := range foreign {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatal("prune touched", f)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "notes", "keep.txt")); string(b) != "mine" {
		t.Fatal("prune changed a foreign file")
	}
	if len(names()) != 2 { // the valid survivor plus the invalid look-alike that prune ignores
		t.Fatal(names())
	}
}
