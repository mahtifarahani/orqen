package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"orqen/internal/backup"
	"orqen/internal/catherd"
	"orqen/internal/runstate"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// targetApp is an empty install (its own database and data directory) that a backup can be restored into.
func targetApp(t *testing.T) *testApp {
	t.Helper()
	return newTestAppWith(t, false, func(a *App) { a.SetCatherd(catherd.NewFake(), fakeRuntime("1.5.0")) })
}

// writeCounts counts the documents of every collection, so a refused restore can prove it wrote nothing.
func writeCounts(t *testing.T, db *DB) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	names, err := db.Database.ListCollectionNames(ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, n := range names {
		c, err := db.Coll(n).CountDocuments(ctx, bson.M{})
		if err != nil {
			t.Fatal(err)
		}
		out[n] = c
	}
	return out
}

// treeDigest hashes the names, sizes and bytes of every file below dir.
func treeDigest(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || !fi.Mode().IsRegular() || strings.Contains(p, "bootstrap") {
			return nil
		}
		raw, _ := os.ReadFile(p)
		fmt.Fprintf(h, "%s %d %x\n", p, len(raw), sha256.Sum256(raw))
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

func sameCounts(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// restoreFixture builds a source install with two organizations, a held-able run, evidence, a connection whose
// credential exists in the source store and a published profile version, and backs it up.
func restoreFixture(t *testing.T) (*scanEnv, string, string, string) {
	t.Helper()
	ctx := context.Background()
	e := newScanEnv(t)
	org2 := e.addOrg2(t)
	e.addArtifact(t, e.org, "evidence one")
	e.addArtifact(t, org2, "evidence two")
	e.scan(t, "findings")
	now := time.Now().UTC()
	ref := "cred-connection-restore"
	if err := e.creds.Put(ctx, ref, "connection-secret-value-0123"); err != nil {
		t.Fatal(err)
	}
	conID := newID("con")
	if _, err := e.db.Coll("connections").InsertOne(ctx, ConnectionDoc{ID: conID, OrgID: e.org, Name: "Source", NameKey: "source", Provider: "gitlab", Deployment: "self_managed", BaseURL: "https://git.example.test",
		AuthMethod: "personal_access_token", CredentialRef: &ref, CredentialState: "set", Status: "untested", Active: true, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Coll("model_profile_versions").InsertOne(ctx, ModelProfileVersionDoc{ID: newID("mpv"), OrgID: e.org, ProfileID: newID("mpf"), Number: 1, State: "published", CatherdName: "restore-profile", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "backups")
	e.backupSettings(t, dir, false, 24, 7, "fail")
	op := e.startBackup(t)
	if op["status"] != "succeeded" {
		t.Fatal(op)
	}
	name := op["result"].(map[string]any)["name"].(string)
	return e, filepath.Join(dir, name), org2, conID
}

func TestRestoreRebuildsTheInstallPausedAndHeld(t *testing.T) {
	src, bdir, org2, conID := restoreFixture(t)
	ctx := context.Background()
	b, err := backup.Open(backup.OSFS{}, bdir)
	if err != nil {
		t.Fatal(err)
	}
	dst := targetApp(t)
	res, err := dst.a.RestoreFromDir(ctx, bdir, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Report == nil || res.Preview.CanRestore != true || len(res.Preview.MissingCredentials) == 0 {
		t.Fatalf("%+v", res.Preview)
	}
	// every restored document equals its line in the backup (runs without the hold the restore adds)
	skip := map[string]bool{"automation_states": true, "operations": true, "events": true, "counters": true}
	for _, c := range b.Manifest.Collections {
		if skip[c.Name] {
			continue
		}
		want := map[string]string{}
		if err = b.ReadCollection(c.Name, func(line []byte) error {
			var d bson.D
			if e := bson.UnmarshalExtJSON(line, true, &d); e != nil {
				return e
			}
			if c.Name == "runs" {
				d = withoutKey(d, "hold")
			}
			out, _ := bson.MarshalExtJSON(d, true, false)
			want[fmt.Sprint(d[0].Value)] = string(out)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		cur, e := dst.db.Coll(c.Name).Find(ctx, bson.M{})
		if e != nil {
			t.Fatal(e)
		}
		got := 0
		for cur.Next(ctx) {
			var d bson.D
			if e = bson.Unmarshal(cur.Current, &d); e != nil {
				t.Fatal(e)
			}
			if c.Name == "runs" {
				d = withoutKey(d, "hold")
			}
			out, _ := bson.MarshalExtJSON(d, true, false)
			if want[fmt.Sprint(d[0].Value)] != string(out) {
				t.Fatalf("%s %v differs after the restore", c.Name, d[0].Value)
			}
			got++
		}
		if got != len(want) {
			t.Fatalf("%s: %d restored, %d in the backup", c.Name, got, len(want))
		}
	}
	// artifact bytes are equal
	blobs, _ := b.Blobs()
	for _, kv := range blobs {
		r, _ := b.OpenBlob(kv[0], kv[1])
		want, _ := io.ReadAll(r)
		r.Close()
		p, _ := artifactPath(dst.a.artifactRoot(), kv[0], kv[1])
		if got, e := os.ReadFile(p); e != nil || !bytes.Equal(got, want) {
			t.Fatal("artifact bytes differ", kv, e)
		}
	}
	// automation is paused for both organizations, every non-terminal run is held `restored`
	for _, o := range []string{src.org, org2} {
		st, e := dst.a.automationState(ctx, o)
		if e != nil || !st.Paused || st.Reason == nil || *st.Reason != "restored" {
			t.Fatalf("%s %+v %v", o, st, e)
		}
	}
	runs, _ := findMany[RunDoc](ctx, dst.a.DB, "runs", bson.M{"active": true}, bson.D{{Key: "_id", Value: 1}}, 10)
	if len(runs) < 2 {
		t.Fatal(len(runs))
	}
	for _, r := range runs {
		if r.Hold == nil || r.Hold.Code != runstate.HoldRestored {
			t.Fatalf("%+v", r.Hold)
		}
	}
	// the events counter continues past the backup
	var ctr struct {
		Seq int64 `bson:"seq"`
	}
	_ = dst.db.Coll("counters").FindOne(ctx, bson.M{"_id": "events"}).Decode(&ctr)
	var maxEv EventDoc
	_ = dst.db.Coll("events").FindOne(ctx, bson.M{}, options.FindOne().SetSort(bson.D{{Key: "seq", Value: -1}})).Decode(&maxEv)
	if ctr.Seq < 1 || maxEv.Seq != ctr.Seq {
		t.Fatalf("counter %d, last event %d", ctr.Seq, maxEv.Seq)
	}
	// the post-restore check: the destination credential store is empty, so the connection is explicitly credential_missing
	// and the published profile (the fake catherd home is empty) needs a republish
	job, lease, err := dst.a.acquire(ctx, "w-test", []string{"post_restore_check"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err = dst.a.runPostRestoreCheck(ctx, job, lease); err != nil {
		t.Fatal(err)
	}
	con, err := findOne[ConnectionDoc](ctx, dst.a.DB, "connections", bson.M{"_id": conID})
	if err != nil {
		t.Fatal(err)
	}
	reasons := con.DTO()["readiness"].(bson.M)["reasons"].([]string)
	if !con.CredentialMissing || !contains(reasons, "credential_missing") || contains(reasons, "no_api_credential") {
		t.Fatalf("%+v %v", con.CredentialState, reasons)
	}
	if n, _ := dst.db.Coll("model_profile_versions").CountDocuments(ctx, bson.M{"republish_required": true}); n != 1 {
		t.Fatal("the published profile must be republish_required, got", n)
	}
	if n, _ := dst.db.Coll("jobs").CountDocuments(ctx, bson.M{"kind": "extension_health"}); n < 1 {
		t.Fatal("extensions are re-checked by a health job")
	}
	// the user logs in, sets the credential, un-pauses automation and resumes: only Resume clears the hold
	tok, err := dst.db.IssueToken(ctx, "login")
	if err != nil {
		t.Fatal(err)
	}
	dst.csrf = dst.json("POST", "/api/v1/auth/redeem", bson.M{"token": tok}, nil, 200)["csrf_token"].(string)
	last := dst.json("GET", "/api/v1/orgs/"+src.org+"/system/restore/last", nil, nil, 200)
	pc, _ := last["post_check"].(map[string]any)[src.org].(map[string]any)
	if last["backup_name"] != filepath.Base(bdir) || last["held_runs"] == nil || pc == nil || len(pc["missing_credentials"].([]any)) == 0 || len(pc["republish_required"].([]any)) != 1 {
		t.Fatalf("%v", last)
	}
	if strings.Contains(fmt.Sprint(last), "connection-secret-value") {
		t.Fatal("the restore report must hold no credential value")
	}
	conPath := "/api/v1/orgs/" + src.org + "/connections/" + conID
	cdoc := dst.json("GET", conPath, nil, nil, 200)
	if rd := cdoc["readiness"].(map[string]any)["reasons"].([]any); len(rd) == 0 || rd[0] != "credential_missing" {
		t.Fatal(cdoc["readiness"])
	}
	dst.json("PUT", conPath+"/credential", map[string]any{"secret": "new-secret"}, map[string]string{"If-Match": fmt.Sprint(int64(cdoc["revision"].(float64)))}, 200)
	if con, _ = findOne[ConnectionDoc](ctx, dst.a.DB, "connections", bson.M{"_id": conID}); con.CredentialMissing || con.CredentialState != "set" {
		t.Fatalf("%+v", con)
	}
	if err = dst.a.sweepHolds(ctx); err != nil {
		t.Fatal(err)
	}
	runPath := "/api/v1/orgs/" + src.org + "/runs/" + src.rn.ID
	rk := 0
	resume := func(status int) map[string]any {
		cur := dst.json("GET", runPath, nil, nil, 200)
		rk++
		return dst.json("POST", runPath+"/resume", nil, map[string]string{"If-Match": fmt.Sprint(int64(cur["revision"].(float64))), "Idempotency-Key": fmt.Sprintf("rs-%d", rk)}, status)
	}
	if h := dst.json("GET", runPath, nil, nil, 200)["hold"]; h == nil || h.(map[string]any)["code"] != "restored" {
		t.Fatal("the sweep must not clear the restored hold", h)
	}
	expectCode(t, resume(409), "automation_paused")
	au := dst.json("GET", "/api/v1/orgs/"+src.org+"/automation", nil, nil, 200)
	dst.json("PUT", "/api/v1/orgs/"+src.org+"/automation", map[string]any{"paused": false}, map[string]string{"If-Match": fmt.Sprint(int64(au["revision"].(float64)))}, 200)
	if h := dst.json("GET", runPath, nil, nil, 200)["hold"]; h == nil {
		_ = dst.a.sweepHolds(ctx)
		if h = dst.json("GET", runPath, nil, nil, 200)["hold"]; h == nil {
			t.Fatal("un-pausing automation must not clear the restored hold")
		}
	}
	if r := resume(200); r["hold"] != nil {
		t.Fatal(r["hold"])
	}
}

func TestRestoreRefusesBeforeItWritesAnything(t *testing.T) {
	src, bdir, _, _ := restoreFixture(t)
	ctx := context.Background()
	// a running server (fresh heartbeat) is refused
	waitFor := func(f func() bool) {
		for i := 0; i < 200 && !f(); i++ {
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor(func() bool { fresh, _ := src.a.runnerFresh(ctx); return fresh })
	before := writeCounts(t, src.db)
	_, err := src.a.RestoreFromDir(ctx, bdir, false)
	if re, ok := err.(*RestoreError); !ok || re.Code != backup.CodeServerRunning {
		t.Fatal(err)
	}
	if !sameCounts(before, writeCounts(t, src.db)) || before["restore_locks"] != 1 { // only the runner heartbeat document
		t.Fatal("a refused restore wrote something")
	}
	// a non-empty target is refused with zero writes
	dst := targetApp(t)
	if _, err = dst.db.Coll("organizations").InsertOne(ctx, OrganizationDoc{ID: newID("org"), Slug: "x", Name: "x", Locale: "en", Timezone: "UTC", Revision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	before, tree := writeCounts(t, dst.db), treeDigest(t, dst.a.Config.DataDir)
	_, err = dst.a.RestoreFromDir(ctx, bdir, false)
	if re, ok := err.(*RestoreError); !ok || re.Code != backup.CodeTargetNotEmpty || !contains(re.NonEmpty, "organizations") {
		t.Fatal(err)
	}
	if !sameCounts(before, writeCounts(t, dst.db)) || tree != treeDigest(t, dst.a.Config.DataDir) {
		t.Fatal("a refused restore wrote something")
	}
	// a flipped byte is refused with zero writes
	flipped := filepath.Join(t.TempDir(), filepath.Base(bdir))
	if err = os.CopyFS(flipped, os.DirFS(bdir)); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(flipped, backup.CollectionFile("runs"))
	raw, _ := os.ReadFile(f)
	raw[len(raw)/2] ^= 0xff
	if err = os.WriteFile(f, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	empty := targetApp(t)
	before, tree = writeCounts(t, empty.db), treeDigest(t, empty.a.Config.DataDir)
	_, err = empty.a.RestoreFromDir(ctx, flipped, false)
	if re, ok := err.(*RestoreError); !ok || re.Code != "restore_integrity_failed" || re.File != backup.CollectionFile("runs") {
		t.Fatal(err)
	}
	if !sameCounts(before, writeCounts(t, empty.db)) || tree != treeDigest(t, empty.a.Config.DataDir) {
		t.Fatal("a refused restore wrote something")
	}
	// a failed restore leaves the lock failed, and the runner refuses to start while it is
	if err = checkRestoreLock(ctx, empty.db); err != nil {
		t.Fatal(err)
	}
	if _, e := empty.db.Coll("restore_locks").InsertOne(ctx, bson.M{"_id": restoreLockID, "state": backup.LockFailed}); e != nil {
		t.Fatal(e)
	}
	if err = checkRestoreLock(ctx, empty.db); err == nil || !strings.Contains(err.Error(), "drop the database") {
		t.Fatal(err)
	}
	if _, e := empty.db.Coll("restore_locks").UpdateOne(ctx, bson.M{"_id": restoreLockID}, bson.M{"$set": bson.M{"state": backup.LockDone}}); e != nil {
		t.Fatal(e)
	}
	if err = checkRestoreLock(ctx, empty.db); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreDryRunWritesNothingAndMatchesTheApi(t *testing.T) {
	_, bdir, _, _ := restoreFixture(t)
	ctx := context.Background()
	dst := targetApp(t)
	dst.bootstrap()
	org := dst.org("dest")
	// the API reads the same backup directory
	cur := dst.json("GET", "/api/v1/orgs/"+org+"/system/backups/settings", nil, nil, 200)
	dst.json("PUT", "/api/v1/orgs/"+org+"/system/backups/settings", map[string]any{"enabled": false, "interval_hours": 24, "keep_last": 7, "keep_days": 0, "dir": filepath.Dir(bdir), "on_artifact_pattern": "fail"},
		map[string]string{"If-Match": fmt.Sprint(int64(cur["revision"].(float64)))}, 200)
	before, tree := writeCounts(t, dst.db), treeDigest(t, dst.a.Config.DataDir)
	cli, err := dst.a.RestoreFromDir(ctx, bdir, true)
	if err != nil {
		t.Fatal(err)
	}
	api := dst.json("POST", "/api/v1/orgs/"+org+"/system/backups/"+filepath.Base(bdir)+"/verify", nil, map[string]string{"Idempotency-Key": "dry-1"}, 200)
	if !sameCounts(before, writeCounts(t, dst.db)) && !onlyIdempotencyGrew(before, writeCounts(t, dst.db)) {
		t.Fatal("a dry run wrote documents")
	}
	if tree != treeDigest(t, dst.a.Config.DataDir) {
		t.Fatal("a dry run wrote files")
	}
	a, _ := json.Marshal(cli.Preview)
	var want, got map[string]any
	_ = json.Unmarshal(a, &want)
	got = api
	wj, _ := json.Marshal(want)
	gj, _ := json.Marshal(got)
	if string(wj) != string(gj) {
		t.Fatalf("the CLI and the API preview differ:\n%s\n%s", wj, gj)
	}
	if want["can_restore"] != false || want["integrity_ok"] != true {
		t.Fatal(want)
	}
	keys := sort.StringSlice{}
	for k := range want {
		keys = append(keys, k)
	}
	if len(keys) < 10 {
		t.Fatal(keys)
	}
}

// onlyIdempotencyGrew accepts the idempotency record that the verify call itself stores.
func onlyIdempotencyGrew(before, after map[string]int64) bool {
	for k, v := range after {
		if v != before[k] && k != "idempotency_records" {
			return false
		}
	}
	return true
}

func withoutKey(d bson.D, key string) bson.D {
	out := bson.D{}
	for _, kv := range d {
		if kv.Key != key {
			out = append(out, kv)
		}
	}
	return out
}
