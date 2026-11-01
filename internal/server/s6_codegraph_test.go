package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"orqen/internal/codegraph"
	"orqen/internal/extensions"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type cgEnv struct {
	*extEnv
	repo, prj string
	fake      *codegraph.Fake
}

// newCGEnv enables the (scripted) gitnexus extension for a project whose primary repository is a local git repo.
func newCGEnv(t *testing.T, setup ...func(*App)) *cgEnv {
	e := &cgEnv{extEnv: newExtEnv(t, setup...)}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	bin := filepath.Join(dir, "gitnexus")
	writeScript(t, bin, "#!/bin/sh\necho 'gitnexus 1.4.0'\n")
	d := e.register("gitnexus", bin)
	sha := d["license_notices"].([]any)[0].(map[string]any)["sha256"].(string)
	e.json("PUT", e.path("gitnexus", "/license"), map[string]any{"basis": "noncommercial_use", "note": "evaluation", "confirm_phrase": "gitnexus", "notice_sha256": sha}, e.rev("gitnexus"), 200)
	e.run("gitnexus", "detect", 202)
	e.run("gitnexus", "test", 202)
	e.json("POST", e.path("gitnexus", "/enable"), nil, e.rev("gitnexus"), 200)
	now := time.Now().UTC()
	prj := ProjectDoc{ID: newID("prj"), OrgID: e.org, Name: "cg", NameKey: "cg", Active: true, Revision: 1, CreatedAt: now, UpdatedAt: now,
		Mapping: Mapping{Repos: []RepoMapping{{Role: "primary", ConnectionID: "con_00000000000000000000000000", ProviderProjectID: "7", TargetBranch: "main"}}}}
	if _, err := e.db.Coll("projects").InsertOne(context.Background(), prj); err != nil {
		t.Fatal(err)
	}
	e.prj = prj.ID
	e.json("PUT", "/api/v1/orgs/"+e.org+"/projects/"+prj.ID+"/extensions/gitnexus", map[string]any{"enabled": true}, map[string]string{"If-Match": "0"}, 200)
	e.repo = filepath.Join(dir, "repo")
	if err := os.MkdirAll(e.repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, e.repo, "init", "-q", "-b", "main")
	gitIn(t, e.repo, "config", "user.email", "t@example.test")
	gitIn(t, e.repo, "config", "user.name", "t")
	e.fake = &codegraph.Fake{}
	e.a.CodeGraph = e.fake
	return e
}

func (e *cgEnv) commit(t *testing.T, file, text string) string {
	if err := os.WriteFile(filepath.Join(e.repo, file), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, e.repo, "add", "-A")
	gitIn(t, e.repo, "commit", "-q", "-m", "c "+file)
	return gitIn(t, e.repo, "rev-parse", "HEAD")
}

func (e *cgEnv) key() string {
	return repoKey(e.org, "con_00000000000000000000000000", "7")
}

func (e *cgEnv) build(t *testing.T, sha string, fence func(func(context.Context) error) error) (CodeIndexDoc, error) {
	ctx := context.Background()
	d, err := e.a.upsertCodeIndex(ctx, e.org, e.prj, e.key(), sha)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := extensions.Get("gitnexus")
	p, why, err := e.a.extUsable(ctx, e.org, m, e.prj)
	if err != nil || why != "" {
		t.Fatal(err, why)
	}
	mgr, _ := e.a.workspaceManager(e.org, nil, 0)
	if fence == nil {
		fence = func(f func(context.Context) error) error { return e.a.DB.Txn(ctx, f) }
	}
	err = e.a.buildCodeIndex(ctx, e.org, d, e.repo, mgr.Git, e.fake, p, fence)
	cur, _ := e.a.codeIndex(ctx, e.org, d.ID)
	return cur, err
}

func (e *cgEnv) run0(owns ...string) RunDoc {
	return RunDoc{ID: newID("run"), OrgID: e.org, ProjectID: e.prj, Lanes: []RunLane{{ID: "M1.L1", Owns: owns}}}
}

func TestCodeIndexIsAttributedToItsOwnCommit(t *testing.T) {
	e := newCGEnv(t)
	ctx := context.Background()
	a := e.commit(t, "b.js", "// symbol: B\n")
	a = e.commit(t, "a.js", "// imports: b.js\n// symbol: A\n")
	d, err := e.build(t, a, nil)
	if err != nil || d.State != "ready" || d.Files != 2 {
		t.Fatal(err, d)
	}
	if _, err := os.Stat(filepath.Join(e.a.Config.DataDir, "codegraph", e.org, "trees", d.ID)); err == nil {
		t.Fatal("the disposable tree must be removed")
	}
	pv := policyValues{}
	it := e.a.codeImpactItem(ctx, e.run0("b.js"), "worker", a, pv)
	if it == nil || it.Excluded != "" || !strings.Contains(it.Text, "commit="+a[:12]) || !strings.Contains(it.Text, "a.js") {
		t.Fatalf("%+v", it)
	}
	if e.a.codeImpactItem(ctx, e.run0("b.js"), "verifier", a, pv) != nil {
		t.Fatal("the verifier gets no code impact")
	}
	b := e.commit(t, "c.js", "// imports: a.js\n")
	st := e.a.codeImpactItem(ctx, e.run0("b.js"), "worker", b, pv)
	if st == nil || st.Excluded != "index_stale" || st.Text != "" {
		t.Fatalf("%+v", st)
	}
	if none := e.a.codeImpactItem(ctx, e.run0("b.js"), "worker", b, pv); none.Excluded == "" {
		t.Fatal("stale must stay excluded")
	}
	// the HTTP surface
	got := e.json("GET", "/api/v1/orgs/"+e.org+"/code-indexes/"+d.ID+"/impact?target=b.js", nil, nil, 200)
	if got["commit_sha"] != a || len(got["items"].([]any)) != 1 {
		t.Fatal(got)
	}
	if s := e.json("GET", "/api/v1/orgs/"+e.org+"/graph/status", nil, nil, 200)["code_graph"].(map[string]any); s["available"] != true || s["ready_indexes"].(float64) != 1 {
		t.Fatal(s)
	}
	// revoking the license stops queries and the packet item, and marks the index
	e.json("DELETE", e.path("gitnexus", "/license"), nil, e.rev("gitnexus"), 200)
	expectCode(t, e.json("GET", "/api/v1/orgs/"+e.org+"/code-indexes/"+d.ID+"/impact?target=b.js", nil, nil, 409), "index_not_ready")
	if rv := e.a.codeImpactItem(ctx, e.run0("b.js"), "worker", a, pv); rv == nil || rv.Excluded != "license_revoked" {
		t.Fatalf("%+v", rv)
	}
	if cur, _ := e.a.codeIndex(ctx, e.org, d.ID); cur.State != "license_revoked" {
		t.Fatal(cur.State)
	}
}

func TestCodeIndexLostLeaseWritesNothingAfterAnalyze(t *testing.T) {
	e := newCGEnv(t)
	ctx := context.Background()
	sha := e.commit(t, "a.js", "// imports: b.js\n")
	calls := 0
	lost := errors.New("lease lost")
	d, err := e.build(t, sha, func(f func(context.Context) error) error {
		calls++
		if calls == 2 { // the write that would mark the index ready
			return lost
		}
		return e.a.DB.Txn(ctx, f)
	})
	if !errors.Is(err, lost) || d.State != "indexing" || d.FinishedAt != nil {
		t.Fatal(err, d)
	}
	// the index content is on disk only
	var all []string
	for _, n := range []string{"code_indexes", "extension_states", "events"} {
		c, _ := e.db.Coll(n).Find(ctx, bson.M{})
		var docs []bson.M
		_ = c.All(ctx, &docs)
		all = append(all, fmt.Sprint(docs))
	}
	if strings.Contains(strings.Join(all, ""), "\"imports\"") {
		t.Fatal("index content leaked into MongoDB")
	}
	if _, err := os.Stat(filepath.Join(d.IndexDir, "index.json")); err != nil {
		t.Fatal("the derived index must be on disk", err)
	}
}

func hashTree(t *testing.T, root string) string {
	var lines []string
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() && fi.Name() == ".git" {
			if fi != nil && fi.IsDir() {
				return filepath.SkipDir
			}
			return err
		}
		if fi.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			s := sha256.Sum256(b)
			rel, _ := filepath.Rel(root, p)
			lines = append(lines, rel+" "+hex.EncodeToString(s[:]))
		}
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func TestCodeIndexLeavesTheRepositoryUntouched(t *testing.T) {
	e := newCGEnv(t)
	sha := e.commit(t, "a.js", "// imports: b.js\n")
	if err := e.a.sweepExtensionHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	refs := gitIn(t, e.repo, "for-each-ref")
	files := hashTree(t, e.repo)
	if d, err := e.build(t, sha, nil); err != nil || d.State != "ready" {
		t.Fatal(err, d)
	}
	if gitIn(t, e.repo, "for-each-ref") != refs || hashTree(t, e.repo) != files {
		t.Fatal("indexing changed the repository")
	}
	if gitIn(t, e.repo, "status", "--porcelain") != "" {
		t.Fatal("the worktree must stay clean")
	}
	// a failing analyze marks the index failed, never ready
	e.fake.FailVerb = "analyze"
	sha2 := e.commit(t, "b.js", "x\n")
	if d, err := e.build(t, sha2, nil); err == nil || d.State == "ready" {
		t.Fatal(err, d)
	}
	// delete removes the validated directory
	cur, _ := e.a.codeIndex(context.Background(), e.org, mustFirstReady(t, e))
	e.json("DELETE", "/api/v1/orgs/"+e.org+"/code-indexes/"+cur.ID, nil, map[string]string{"If-Match": fmt.Sprint(cur.Revision)}, 200)
	if _, err := os.Stat(cur.IndexDir); err == nil {
		t.Fatal("the index directory must be removed")
	}
}

func mustFirstReady(t *testing.T, e *cgEnv) string {
	l, err := findMany[CodeIndexDoc](context.Background(), e.a.DB, "code_indexes", bson.M{"org_id": e.org, "state": "ready"}, bson.D{{Key: "_id", Value: 1}}, 5)
	if err != nil || len(l) == 0 {
		t.Fatal(err, l)
	}
	return l[0].ID
}
