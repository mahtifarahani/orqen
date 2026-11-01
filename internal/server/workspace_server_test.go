package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func (e *intakeEnv) wsOf(key string) WorkspaceDoc {
	r := e.runState(key)
	d, err := findOne[WorkspaceDoc](context.Background(), e.f.db, "workspaces", bson.M{"org_id": e.org, "run_id": r.ID})
	if err != nil {
		e.f.t.Fatal(key, err)
	}
	return d
}
func (e *intakeEnv) abs(rel string) string { return filepath.Join(e.f.a.Config.DataDir, rel) }
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}
func autoOpts() intakeOpts {
	return intakeOpts{PlanApproval: "auto", CodingStart: "auto", Git: true}
}

func TestWorkspaceReachesQueuedWithAReadyWorktree(t *testing.T) {
	logs := captureLogs(t)
	o := autoOpts()
	o.JQL = ptr("key = ORQ-1")
	e := newIntakeEnv(t, o)
	e.poll()
	run := e.waitState("ORQ-1", "queued")
	if run.Checkpoint.Stage != "queued" || run.QueuedAt == nil || run.WorkspaceID == nil || run.Hold != nil {
		t.Fatalf("%+v", run)
	}
	ws := e.wsOf("ORQ-1")
	suffix := strings.ToLower(run.ID[len(run.ID)-10:])
	if ws.State != "ready" || ws.Branch != "orqen/orq-1/"+suffix || ws.BaseSHA != e.repo.SHA || ws.HeadSHA != ws.BaseSHA || ws.BaseRef != "refs/remotes/origin/main" || !ws.Active || ws.Dirty {
		t.Fatalf("%+v", ws)
	}
	wt := e.abs(ws.PathRel)
	if b, err := os.ReadFile(filepath.Join(wt, "package.json")); err != nil || !strings.Contains(string(b), "vitest") {
		t.Fatal("the worktree is not populated", err)
	}
	mirror := e.abs(ws.MirrorRel)
	if gitIn(t, mirror, "rev-parse", "refs/heads/"+ws.Branch) != e.repo.SHA || gitIn(t, wt, "rev-parse", "--abbrev-ref", "HEAD") != ws.Branch {
		t.Fatal("branch")
	}
	if !strings.HasPrefix(ws.PathRel, "workspaces/"+e.org+"/runs/") || !strings.HasPrefix(ws.MirrorRel, "workspaces/"+e.org+"/mirrors/"+e.glID+"/") {
		t.Fatal(ws.PathRel, ws.MirrorRel)
	}
	dto := e.f.json("GET", e.orgPath("runs", run.ID), nil, nil, 200)
	if d := dto["workspace"].(map[string]any); d["state"] != "ready" || d["branch"] != ws.Branch || d["base_sha"] != e.repo.SHA || dto["queue_position"] != float64(1) {
		t.Fatal(dto)
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "run.workspace_ready"}) != 1 {
		t.Fatal("event")
	}
	// A repository that is only in the catalog never gets a mirror (project 1 is the only mapped repository).
	entries, err := os.ReadDir(filepath.Join(e.f.a.Config.DataDir, "workspaces", e.org, "mirrors", e.glID))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, ent := range entries {
		names = append(names, ent.Name())
	}
	if strings.Join(names, ",") != "1.git,1.git.lock" {
		t.Fatal(names)
	}
	// Secrets: the sentinel tokens appear nowhere in Mongo, artifacts, mirrors, worktrees or logs.
	docs, files := e.leakScan([]string{e.GitlabToken, e.JiraToken}, logs.String())
	if docs < 30 || files < 20 {
		t.Fatal("scanned too little", docs, files)
	}
	cfg, _ := os.ReadFile(filepath.Join(mirror, "config"))
	if strings.Contains(string(cfg), "@") || strings.Contains(string(cfg), "[credential") {
		t.Fatal(string(cfg))
	}
	stored := 0
	e.f.creds.mu.RLock()
	for _, v := range e.f.creds.values {
		if v == e.GitlabToken || v == e.JiraToken {
			stored++
		}
	}
	e.f.creds.mu.RUnlock()
	if stored != 2 {
		t.Fatal("the tokens must be in the credential store:", stored)
	}
}

func TestWorkspaceCrashAfterWorktreeAddReusesIt(t *testing.T) {
	var crashed atomic.Bool
	o := autoOpts()
	o.JQL = ptr("key = ORQ-1")
	o.Setup = func(a *App) {
		a.runHook = func(stage string) error {
			if stage == "workspace" && crashed.CompareAndSwap(false, true) {
				return errLeaseLost
			}
			return nil
		}
	}
	e := newIntakeEnv(t, o)
	e.poll()
	e.waitState("ORQ-1", "queued")
	if !crashed.Load() {
		t.Fatal("no crash was injected")
	}
	ws := e.wsOf("ORQ-1")
	if n := e.count("workspaces", bson.M{"org_id": e.org}); n != 1 {
		t.Fatal("workspace documents:", n)
	}
	if n := strings.Count(gitIn(t, e.abs(ws.MirrorRel), "worktree", "list", "--porcelain"), "worktree "); n != 2 {
		t.Fatal("worktrees:", n)
	}
	if e.count("events", bson.M{"org_id": e.org, "type": "run.workspace_ready"}) != 1 {
		t.Fatal("ready event")
	}
}

func TestCancelledRunsWorkspaceIsRemovedWhenCleanAndPreservedWhenDirty(t *testing.T) {
	e := newIntakeEnv(t, autoOpts())
	e.poll()
	r1 := e.waitState("ORQ-1", "queued")
	r3 := e.waitState("ORQ-3", "queued")
	w1, w3 := e.wsOf("ORQ-1"), e.wsOf("ORQ-3")
	// A queued run is never cleaned up while it is active.
	e.f.a.Committed()
	if e.count("workspaces", bson.M{"org_id": e.org, "state": "ready"}) != 2 {
		t.Fatal("active workspaces must stay")
	}
	if pos := e.f.json("GET", e.orgPath("runs", r3.ID), nil, nil, 200)["queue_position"]; pos == nil {
		t.Fatal("queued runs show a position")
	}
	if err := os.WriteFile(filepath.Join(e.abs(w3.PathRel), "work-in-progress.txt"), []byte("do not lose me"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.act(r1, "cancel", nil, 200)
	e.act(r3, "cancel", nil, 200)
	waitFor(t, "cleanup", func() bool { return e.wsOf("ORQ-1").State == "removed" && e.wsOf("ORQ-3").State == "preserved" })
	if _, err := os.Stat(e.abs(w1.PathRel)); !os.IsNotExist(err) {
		t.Fatal("the clean worktree must be gone")
	}
	if out := gitIn(t, e.abs(w1.MirrorRel), "branch", "--list", w1.Branch); out != "" {
		t.Fatal("the branch must be gone:", out)
	}
	if b, err := os.ReadFile(filepath.Join(e.abs(w3.PathRel), "work-in-progress.txt")); err != nil || string(b) != "do not lose me" {
		t.Fatal("the dirty worktree must be preserved")
	}
	kept := e.wsOf("ORQ-3")
	if !kept.Dirty || !kept.Active || kept.RemovedAt != nil {
		t.Fatalf("%+v", kept)
	}
	// Running cleanup again does not remove it either.
	if err := e.f.db.Txn(context.Background(), func(sc context.Context) error { return e.f.a.enqueueCleanup(sc, e.org) }); err != nil {
		t.Fatal(err)
	}
	e.f.a.Committed()
	time.Sleep(1500 * time.Millisecond)
	if e.wsOf("ORQ-3").State != "preserved" {
		t.Fatal("a preserved workspace is never auto-deleted")
	}
	// Discard: the snapshot artifact is stored first, then the worktree goes.
	notTerminal := e.f.json("POST", e.orgPath("workspaces", kept.ID, "discard"), nil, map[string]string{"If-Match": fmt.Sprint(kept.Revision), "Idempotency-Key": nextKey()}, 202)
	op := e.f.waitOperation(e.org, notTerminal["id"].(string))
	if op["status"] != "succeeded" {
		t.Fatal(op)
	}
	gone := e.wsOf("ORQ-3")
	if gone.State != "removed" || gone.Active || gone.SnapshotArtifactID == nil || gone.RemovedAt == nil {
		t.Fatalf("%+v", gone)
	}
	if _, err := os.Stat(e.abs(w3.PathRel)); !os.IsNotExist(err) {
		t.Fatal("the discarded worktree must be gone")
	}
	gz, err := gzip.NewReader(bytes.NewReader([]byte(e.artifactText(*gone.SnapshotArtifactID))))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		if h.Name == "untracked/work-in-progress.txt" && string(b) == "do not lose me" {
			found = true
		}
	}
	if !found {
		t.Fatal("the snapshot must contain the untracked file")
	}
	// A second discard (state removed) and a discard of a live workspace are refused.
	expectCode(t, e.f.json("POST", e.orgPath("workspaces", kept.ID, "discard"), nil, map[string]string{"If-Match": fmt.Sprint(gone.Revision), "Idempotency-Key": nextKey()}, 409), "run_state_conflict")
}

func TestDiscardRefusesALiveRunsWorkspace(t *testing.T) {
	o := autoOpts()
	o.JQL = ptr("key = ORQ-1")
	e := newIntakeEnv(t, o)
	e.poll()
	e.waitState("ORQ-1", "queued")
	ws := e.wsOf("ORQ-1")
	expectCode(t, e.f.json("POST", e.orgPath("workspaces", ws.ID, "discard"), nil, map[string]string{"If-Match": fmt.Sprint(ws.Revision), "Idempotency-Key": nextKey()}, 409), "run_state_conflict")
	expectCode(t, e.f.json("POST", e.orgPath("workspaces", ws.ID, "discard"), nil, map[string]string{"If-Match": "99", "Idempotency-Key": nextKey()}, 409), "revision_conflict")
	e.f.json("POST", e.orgPath("workspaces", newID("wsp"), "discard"), nil, map[string]string{"If-Match": "1", "Idempotency-Key": nextKey()}, 404)
}

func TestWorkspaceFetchWaitsForTheNetworkThenRecovers(t *testing.T) {
	o := autoOpts()
	o.JQL = ptr("key = ORQ-1")
	e := newIntakeEnv(t, o)
	e.repS.stop() // the repository host drops while the API still answers
	e.poll()
	waitFor(t, "waiting_network", func() bool { return e.runState("ORQ-1").State == "waiting_network" })
	if r := e.runState("ORQ-1"); r.Checkpoint.Stage != "workspace" || !r.Active {
		t.Fatalf("%+v", r.Checkpoint)
	}
	e.repS.start(t)
	e.f.a.Committed()
	// Resume pulls the retry forward instead of waiting for the backoff.
	e.act(e.runState("ORQ-1"), "resume", nil, 200)
	e.waitState("ORQ-1", "queued")
	if e.count("workspaces", bson.M{"org_id": e.org}) != 1 {
		t.Fatal("one workspace")
	}
}

func TestWorkspaceCloneHostMustBeAllowed(t *testing.T) {
	o := autoOpts()
	o.JQL = ptr("key = ORQ-1")
	o.GitHostNotAllowed = true
	e := newIntakeEnv(t, o)
	e.poll()
	e.waitState("ORQ-1", "needs_input")
	it := e.openItem("ORQ-1")
	if it.Reason != "project_mapping" || it.QuestionCode != "clone_host_not_allowed" || it.Params["host"] == nil {
		t.Fatalf("%+v", it)
	}
	if e.count("workspaces", bson.M{"org_id": e.org}) != 0 {
		t.Fatal("no workspace for a refused host")
	}
	if _, err := os.Stat(filepath.Join(e.f.a.Config.DataDir, "workspaces", e.org, "mirrors")); !os.IsNotExist(err) {
		t.Fatal("no mirror may be created for a refused host")
	}
}

func TestWorkspaceLowDiskStopsTheRun(t *testing.T) {
	o := autoOpts()
	o.JQL = ptr("key = ORQ-1")
	o.Setup = func(a *App) { a.statfs = func(string) (uint64, error) { return 1 << 20, nil } }
	e := newIntakeEnv(t, o)
	e.poll()
	e.waitState("ORQ-1", "needs_input")
	if it := e.openItem("ORQ-1"); it.Reason != "dependency_unavailable" || it.QuestionCode != "disk_low" {
		t.Fatalf("%+v", it)
	}
}

func TestWorkspaceAuthFailureAsksForTheCredential(t *testing.T) {
	o := autoOpts()
	o.JQL = ptr("key = ORQ-1")
	e := newIntakeEnv(t, o)
	e.repo.SetToken("rotated-on-the-git-host") // the git host stops accepting the connection's token
	e.poll()
	e.waitState("ORQ-1", "needs_input")
	if it := e.openItem("ORQ-1"); it.Reason != "credential_expired" {
		t.Fatalf("%+v", it)
	}
}

func TestRetainedWorkspaceSelection(t *testing.T) {
	now := time.Now()
	var in []WorkspaceDoc
	for i := 0; i < 5; i++ {
		in = append(in, WorkspaceDoc{ID: fmt.Sprint(i), CreatedAt: now.Add(time.Duration(i) * time.Minute)})
	}
	got := retainedToRemove(in, 3)
	if len(got) != 2 || got[0].ID != "0" || got[1].ID != "1" {
		t.Fatal(got)
	}
	if retainedToRemove(in, 5) != nil || retainedToRemove(nil, 1) != nil {
		t.Fatal("nothing to remove")
	}
}
