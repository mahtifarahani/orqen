package backup

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func iter(docs ...string) DocIter {
	i := 0
	return func() (string, []byte, bool, error) {
		if i >= len(docs) {
			return "", nil, false, nil
		}
		d := docs[i]
		i++
		return fmt.Sprintf("doc%d", i), []byte(d), true, nil
	}
}

func digestOf(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

type fixtureBackup struct {
	dir, name string
}

func makeBackup(t *testing.T, root string, name string, sc *Scanner, blobs map[string]string) fixtureBackup {
	t.Helper()
	w, err := NewWriter(OSFS{}, root, name, PatternFail, sc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.WriteCollection("organizations", iter(`{"_id":"org_1","slug":"a"}`, `{"_id":"org_2","slug":"b"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = w.WriteCollection("runs", iter(`{"_id":"run_1","state":"coding"}`)); err != nil {
		t.Fatal(err)
	}
	for org, body := range blobs {
		if _, err = w.CopyBlob(org, digestOf(body), "art_"+org, strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := w.Finalize(Manifest{OrqenVersion: "test", SchemaVersion: 2, PolicyDefaultsVersion: 5, ClusterTime: "1", Orgs: []Org{{ID: "org_1", Slug: "a", Name: "A"}}}, t0)
	if err != nil || got != name {
		t.Fatalf("finalize: %q %v", got, err)
	}
	return fixtureBackup{dir: filepath.Join(root, name), name: name}
}

func TestWriterReaderRoundTripAndDeterminism(t *testing.T) {
	blobs := map[string]string{"org_1": "patch one\n", "org_2": "patch two\n"}
	a := makeBackup(t, t.TempDir(), NewName(t0, "deadbeef"), nil, blobs)
	b := makeBackup(t, t.TempDir(), NewName(t0, "deadbeef"), nil, blobs)
	ba, err := Open(OSFS{}, a.dir)
	if err != nil {
		t.Fatal(err)
	}
	bb, _ := Open(OSFS{}, b.dir)
	if len(ba.Manifest.Collections) != 2 || ba.Manifest.Artifacts.Count != 2 || ba.Manifest.Format != FormatName {
		t.Fatalf("manifest: %+v", ba.Manifest)
	}
	for i, c := range ba.Manifest.Collections {
		if c.SHA256 != bb.Manifest.Collections[i].SHA256 {
			t.Errorf("%s: same input must give the same file hash", c.Name)
		}
	}
	if ba.Manifest.Artifacts.ListSHA256 != bb.Manifest.Artifacts.ListSHA256 {
		t.Error("blob list digest differs")
	}
	if rs := ba.VerifyAll(); !AllOK(rs) {
		t.Fatalf("verify: %+v", rs)
	}
	var lines []string
	if err = ba.ReadCollection("organizations", func(l []byte) error { lines = append(lines, string(l)); return nil }); err != nil || len(lines) != 2 || lines[0] != `{"_id":"org_1","slug":"a"}` {
		t.Fatalf("read: %v %v", lines, err)
	}
	keys, _ := ba.Blobs()
	if len(keys) != 2 {
		t.Fatalf("blobs: %v", keys)
	}
	rc, err := ba.OpenBlob("org_1", digestOf("patch one\n"))
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(rc); string(body) != "patch one\n" {
		t.Fatalf("blob: %q", body)
	}
	rc.Close()
	// directory modes
	if fi, _ := os.Stat(a.dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("backup dir mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(a.dir, ManifestFile)); fi.Mode().Perm() != 0o600 {
		t.Errorf("manifest mode %v", fi.Mode().Perm())
	}
}

func flipByte(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 0xff
	if err = os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFlippedByteIsAnIntegrityFailure(t *testing.T) {
	blobs := map[string]string{"org_1": "patch one\n"}
	for name, rel := range map[string]string{
		"collection": CollectionFile("runs"),
		"blob":       BlobPath("org_1", digestOf("patch one\n")),
		"manifest":   ManifestFile,
	} {
		fb := makeBackup(t, t.TempDir(), NewName(t0, "00000001"), nil, blobs)
		flipByte(t, filepath.Join(fb.dir, filepath.FromSlash(rel)))
		b, err := Open(OSFS{}, fb.dir)
		if name == "manifest" {
			var ie *IntegrityError
			if !errors.As(err, &ie) || ie.File != ManifestFile {
				t.Errorf("manifest flip: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		f, bad := FirstFailure(b.VerifyAll())
		if !bad || f != rel {
			t.Errorf("%s: first failure %q (%v), want %q", name, f, bad, rel)
		}
		if name == "collection" {
			var ie *IntegrityError
			if err = b.ReadCollection("runs", func([]byte) error { t.Fatal("no line may be delivered"); return nil }); !errors.As(err, &ie) || ie.File != rel {
				t.Errorf("read of a flipped collection: %v", err)
			}
		}
	}
	// a missing sum file
	fb := makeBackup(t, t.TempDir(), NewName(t0, "00000002"), nil, blobs)
	_ = os.Remove(filepath.Join(fb.dir, ManifestSum))
	if _, err := Open(OSFS{}, fb.dir); err == nil {
		t.Error("a backup without MANIFEST.sha256 must not open")
	}
}

func TestPartialDirsAreIgnoredByList(t *testing.T) {
	root := t.TempDir()
	good := makeBackup(t, root, NewName(t0, "aaaaaaaa"), nil, nil)
	_ = os.MkdirAll(filepath.Join(root, PartialPrefix+NewName(t0.Add(time.Hour), "bbbbbbbb")), 0o700)
	_ = os.MkdirAll(filepath.Join(root, "some-other-dir"), 0o700)
	_ = os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o600)
	broken := NewName(t0.Add(2*time.Hour), "cccccccc")
	_ = os.MkdirAll(filepath.Join(root, broken), 0o700)
	list, err := List(OSFS{}, root)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if list[0].Name != broken || list[0].Valid || list[1].Name != good.name || !list[1].Valid {
		t.Fatalf("order/validity: %+v", list)
	}
	// an abandoned writer leaves nothing under a final name
	w, _ := NewWriter(OSFS{}, root, NewName(t0, "dddddddd"), "", nil)
	w.Abort()
	if _, err = os.Stat(filepath.Join(root, PartialPrefix+NewName(t0, "dddddddd"))); err == nil {
		t.Fatal("abort must remove the partial directory")
	}
	if _, err = NewWriter(OSFS{}, root, "not-a-backup-name", "", nil); err == nil {
		t.Fatal("bad names are refused")
	}
}

func TestScannerFindsEveryFormOfAKnownValueAndEveryPattern(t *testing.T) {
	secret := "test-not-a-secret-8fK2xQ9"
	sc := NewScanner([]string{secret, "short", secret})
	if sc.KnownChecked() != 1 {
		t.Fatalf("checked %d, want 1 (short and duplicate values are not counted)", sc.KnownChecked())
	}
	forms := map[string]string{
		"raw":       "token " + secret + " end",
		"urlenc":    "q=" + url.QueryEscape(secret+" x/y"),
		"b64":       base64.StdEncoding.EncodeToString([]byte(secret)),
		"b64url":    base64.URLEncoding.EncodeToString([]byte(secret)),
		"b64raw":    base64.RawStdEncoding.EncodeToString([]byte(secret)),
		"b64 off 1": base64.StdEncoding.EncodeToString([]byte("a" + secret + "zzzz")),
		"b64 off 2": base64.StdEncoding.EncodeToString([]byte("ab" + secret + "zzzzz")),
		"b64 off 0": base64.StdEncoding.EncodeToString([]byte(secret + "yz")),
		"b64url o1": base64.URLEncoding.EncodeToString([]byte("a" + secret)),
	}
	for name, text := range forms {
		hits := sc.Scan([]byte("prefix " + text + " suffix"))
		if len(hits) == 0 || hits[0].Kind != HitKnownValue {
			t.Errorf("%s: not found", name)
		}
	}
	if len(sc.Scan([]byte("nothing to see; test-not-a-secret-other"))) != 0 {
		t.Error("a different value is not a hit")
	}
	for code, text := range map[string]string{
		"gitlab_token":   "glpat-AAAAAAAAAAAAAAAAAAAA",
		"github_token":   "ghp_" + strings.Repeat("a", 36),
		"api_key_sk":     "sk-" + strings.Repeat("a", 32),
		"aws_access_key": "AKIAABCDEFGHIJKLMNOP",
		"private_key":    "-----BEGIN RSA PRIVATE KEY-----",
		"slack_token":    "xoxb-1234567890-abcdef",
		"bearer_header":  "Authorization: Bearer " + strings.Repeat("a", 24),
	} {
		hits := NewScanner(nil).Scan([]byte("x " + text + " y"))
		if len(hits) != 1 || hits[0].Code != code || hits[0].Kind != HitPattern {
			t.Errorf("%s: %+v", code, hits)
		}
	}
	if len(PatternCodes()) != 7 {
		t.Error("seven patterns")
	}
	e := &SecretError{Location: "connections/con_1", Hit: Hit{Kind: HitKnownValue, Code: HitKnownValue}}
	if strings.Contains(e.Error(), secret) {
		t.Error("errors never carry a value")
	}
}

func TestSecretInADocumentFailsTheCollectionWithoutTheValue(t *testing.T) {
	secret := "test-not-a-secret-ABCDEF12"
	root := t.TempDir()
	w, _ := NewWriter(OSFS{}, root, NewName(t0, "11111111"), "", NewScanner([]string{secret}))
	_, err := w.WriteCollection("connections", iter(`{"_id":"con_1"}`, `{"_id":"con_2","note":"`+secret+`"}`))
	var be *Error
	if !errors.As(err, &be) || be.Code != CodeSecretDetected || be.Location != "connections/doc2" {
		t.Fatalf("err: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("the error holds the value")
	}
	w.Abort()
	if ents, _ := os.ReadDir(root); len(ents) != 0 {
		t.Fatalf("the failed backup left %d entries", len(ents))
	}
	// a pattern in a document fails too
	w, _ = NewWriter(OSFS{}, root, NewName(t0, "22222222"), "exclude_artifact", NewScanner(nil))
	if _, err = w.WriteCollection("projects", iter(`{"_id":"p","k":"glpat-AAAAAAAAAAAAAAAAAAAA"}`)); !errors.As(err, &be) || be.Code != CodeSecretDetected {
		t.Fatalf("pattern in a document: %v", err)
	}
	w.Abort()
	if _, err = (&Writer{scanner: NewScanner(nil)}).WriteCollection("sessions", iter()); err == nil {
		t.Fatal("excluded collections cannot be written")
	}
}

func TestBlobPolicies(t *testing.T) {
	secret := "test-not-a-secret-ZZZZ9999"
	pattern := "log: glpat-AAAAAAAAAAAAAAAAAAAA\n"
	root := t.TempDir()
	// known value: always fails, even in exclude mode
	w, _ := NewWriter(OSFS{}, root, NewName(t0, "33333333"), PatternExcludeArtifact, NewScanner([]string{secret}))
	body := "contains " + secret
	var be *Error
	if _, err := w.CopyBlob("org_1", digestOf(body), "art_1", strings.NewReader(body)); !errors.As(err, &be) || be.Code != CodeSecretDetected || be.Location != "art_1" {
		t.Fatalf("known value in a blob: %v", err)
	}
	// pattern in exclude mode: left out and recorded
	wrote, err := w.CopyBlob("org_1", digestOf(pattern), "art_2", strings.NewReader(pattern))
	if err != nil || wrote {
		t.Fatalf("pattern exclude: %v %v", wrote, err)
	}
	if wrote, err = w.CopyBlob("org_1", digestOf("fine"), "art_3", strings.NewReader("fine")); err != nil || !wrote {
		t.Fatalf("clean blob: %v %v", wrote, err)
	}
	name, err := w.Finalize(Manifest{SchemaVersion: 2}, t0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Open(OSFS{}, filepath.Join(root, name))
	ex := b.Manifest.SecretScan.ExcludedArtifacts
	if len(ex) != 1 || ex[0].ArtifactID != "art_2" || ex[0].PatternCode != "gitlab_token" || b.Manifest.Artifacts.Count != 1 || b.Manifest.SecretScan.KnownValuesChecked != 1 {
		t.Fatalf("manifest scan section: %+v", b.Manifest.SecretScan)
	}
	if b.Manifest.SecretScan.PatternsVersion != PatternsVersion || len(b.Manifest.Excluded.Collections) == 0 || len(b.Manifest.Excluded.DataDirs) == 0 {
		t.Fatalf("manifest excluded: %+v", b.Manifest.Excluded)
	}
	// pattern in fail mode
	w2, _ := NewWriter(OSFS{}, root, NewName(t0, "44444444"), PatternFail, NewScanner(nil))
	if _, err = w2.CopyBlob("org_1", digestOf(pattern), "art_9", strings.NewReader(pattern)); !errors.As(err, &be) || be.Code != CodeSecretDetected {
		t.Fatalf("pattern in fail mode: %v", err)
	}
	// a blob that does not match its digest or is missing
	if _, err = w2.CopyBlob("org_1", digestOf("x"), "art_8", strings.NewReader("y")); !errors.As(err, &be) || be.Code != CodeArtifactMissing {
		t.Fatalf("digest mismatch: %v", err)
	}
	w2.Abort()
}

func TestBackupFilesNeverHoldThePlainSecret(t *testing.T) {
	secret := "test-not-a-secret-QQQQ7777"
	root := t.TempDir()
	fb := makeBackup(t, root, NewName(t0, "55555555"), NewScanner([]string{secret}), map[string]string{"org_1": "clean"})
	var all bytes.Buffer
	_ = filepath.Walk(fb.dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			raw, _ := os.ReadFile(p)
			if strings.HasSuffix(p, ".gz") {
				zr, err := gzip.NewReader(bytes.NewReader(raw))
				if err == nil {
					raw, _ = io.ReadAll(zr)
				}
			}
			all.Write(raw)
		}
		return nil
	})
	if bytes.Contains(all.Bytes(), []byte(secret)) {
		t.Fatal("secret in the backup")
	}
}

func TestPlanPrune(t *testing.T) {
	now := t0.AddDate(0, 0, 100)
	mk := func(day int, hex string, valid bool) Candidate {
		ts := t0.AddDate(0, 0, day)
		return Candidate{Name: NewName(ts, hex), CreatedAt: ts, Valid: valid}
	}
	es := []Candidate{mk(0, "00000001", true), mk(10, "00000002", true), mk(60, "00000003", true), mk(90, "00000004", true), mk(95, "00000005", true),
		mk(50, "00000006", false), {Name: "photos", CreatedAt: t0, Valid: true}, {Name: PartialPrefix + NewName(t0, "00000007"), Valid: true}}
	p := PlanPrune(es, 2, 30, now)
	if len(p.Keep) != 2 || p.Keep[0] != es[4].Name || p.Keep[1] != es[3].Name {
		t.Fatalf("keep: %v", p.Keep)
	}
	// 60, 10 and 0 days are beyond keep_last and older than 30 days
	if len(p.Delete) != 3 {
		t.Fatalf("delete: %v", p.Delete)
	}
	if len(p.Ignored) != 3 {
		t.Fatalf("foreign names and invalid manifests are ignored: %v", p.Ignored)
	}
	for _, d := range p.Delete {
		if d == es[5].Name || d == "photos" {
			t.Fatalf("deleted a foreign or invalid dir: %s", d)
		}
	}
	// keep_last survives whatever the age
	old := PlanPrune(es[:3], 3, 1, now)
	if len(old.Delete) != 0 || len(old.Keep) != 3 {
		t.Fatalf("keep_last: %+v", old)
	}
	// keep_days 0: count decides
	if c := PlanPrune(es[:5], 1, 0, now); len(c.Keep) != 1 || len(c.Delete) != 4 {
		t.Fatalf("no age rule: %+v", c)
	}
	// younger than keep_days are kept
	if y := PlanPrune(es[:5], 1, 20, now); len(y.Keep) != 2 {
		t.Fatalf("recent backups are kept: %+v", y)
	}
	if PlanPrune(nil, 3, 3, now).Delete == nil {
		t.Fatal("empty slices, not nil")
	}
	last := t0
	if !Due(true, nil, 24, now) || Due(false, nil, 24, now) || Due(true, &last, 24*200, now) || !Due(true, &last, 24, now) {
		t.Fatal("Due")
	}
}

func TestPreview(t *testing.T) {
	fb := makeBackup(t, t.TempDir(), NewName(t0, "66666666"), nil, nil)
	b, _ := Open(OSFS{}, fb.dir)
	in := PreviewInput{Name: fb.name, Manifest: b.Manifest, Verify: b.VerifyAll(), BuildSchema: 2,
		Target:          Target{Counts: map[string]int64{"organizations": 0, "counters": 1}, EventsOnly: true},
		Credentials:     []CredentialRef{{Ref: "cred_b", Kind: "connection", OrgID: "org_1"}, {Ref: "cred_a", Kind: "extension", OrgID: "org_1", Present: true}, {Ref: "cred_a2", Kind: "catherd_secret", OrgID: "org_1"}},
		NonTerminalRuns: 3, Profiles: []ProfileRef{{OrgID: "org_1", Name: "o-x-v1"}, {OrgID: "org_1", Name: "o-y-v1", Present: true}}}
	p := Preview(in)
	if !p.CanRestore || !p.TargetEmpty || !p.IntegrityOK || p.HeldRuns != 3 || len(p.MissingCredentials) != 2 || p.MissingCredentials[0].Ref != "cred_a2" || len(p.RepublishProfiles) != 1 {
		t.Fatalf("preview: %+v", p)
	}
	if p.Counts["organizations"] != 2 || p.Counts["runs"] != 1 || len(p.Orgs) != 1 {
		t.Fatalf("counts: %+v", p.Counts)
	}
	in.Target = Target{Counts: map[string]int64{"runs": 4, "events": 0}, Blobs: 2}
	p = Preview(in)
	if p.CanRestore || p.TargetEmpty || strings.Join(p.TargetNonEmpty, ",") != "artifacts_dir,runs" || p.Notes[0] != NoteTargetNotEmpty {
		t.Fatalf("non-empty target: %+v", p)
	}
	in.BuildSchema = 1
	if p = Preview(in); p.SchemaCompatible || p.CanRestore {
		t.Fatal("a newer schema is not restorable")
	}
	flipByte(t, filepath.Join(fb.dir, filepath.FromSlash(CollectionFile("runs"))))
	b2, _ := Open(OSFS{}, fb.dir)
	in.BuildSchema, in.Verify = 2, b2.VerifyAll()
	if p = Preview(in); p.IntegrityOK || p.FirstFailedFile != CollectionFile("runs") || p.CanRestore {
		t.Fatalf("integrity: %+v", p)
	}
}

func TestTargetEmptyRule(t *testing.T) {
	if !(Target{Counts: map[string]int64{"counters": 1}, EventsOnly: true}).Empty() {
		t.Error("the pristine events counter is empty")
	}
	if (Target{Counts: map[string]int64{"counters": 1}}).Empty() {
		t.Error("a used counters collection is not empty")
	}
	if (Target{Counts: map[string]int64{"sessions": 5}}).Empty() != true {
		t.Error("excluded collections do not count")
	}
	if (Target{Counts: map[string]int64{}, Blobs: 1}).Empty() {
		t.Error("blobs make the target non-empty")
	}
	if !ReplaceOnRestore["counters"] || BatchSize != 500 {
		t.Error("restore constants")
	}
}
