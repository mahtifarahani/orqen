package extwrite

import (
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"orqen/internal/testfakes"
	"orqen/internal/workspace"
)

type rig struct {
	t        *testing.T
	gl       *testfakes.GitLab
	repos    *testfakes.GitRepos
	glSrv    *httptest.Server
	repoSrv  *httptest.Server
	git      *workspace.Git
	gate     *Gate
	argv     [][]string
	mu       sync.Mutex
	repoURL  string
	apiBase  string
	worktree string
}

const writeToken = "glpat-WRITE-test-token-12345"

func newRig(t *testing.T) *rig {
	if !testfakes.GitAvailable() {
		t.Skip("git smart HTTP is not available")
	}
	r := &rig{t: t, gl: testfakes.NewGitLab()}
	repos, err := testfakes.NewGitRepos(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r.repos = repos
	repos.PushToken = writeToken
	r.gl.WriteToken = writeToken
	r.gl.AutoPipeline = true
	r.gl.LinkRepos(repos)
	r.glSrv = httptest.NewServer(r.gl.Handler())
	r.repoSrv = httptest.NewServer(repos.Handler())
	t.Cleanup(r.glSrv.Close)
	t.Cleanup(r.repoSrv.Close)
	r.apiBase = r.glSrv.URL + "/gitlab"
	r.repoURL = r.repoSrv.URL + "/gitlab/group-1/proj-1.git"
	data := t.TempDir()
	if _, err = workspace.EnsureAskPass(data); err != nil {
		t.Fatal(err)
	}
	r.git = &workspace.Git{DataDir: data, Ceiling: data, AllowHTTP: true, Hook: func(a []string) { r.mu.Lock(); r.argv = append(r.argv, a); r.mu.Unlock() }}
	r.worktree = filepath.Join(t.TempDir(), "wt")
	if _, err = r.git.Run(context.Background(), filepath.Dir(r.worktree), workspace.Creds{Host: strings.TrimPrefix(r.repoSrv.URL, "http://"), Username: "oauth2", Password: repos.Token}, 0, "clone", "-q", r.repoURL, r.worktree); err != nil {
		t.Fatal(err)
	}
	r.gate = &Gate{Scope: ScopeLoopbackOnly, Credential: func(context.Context, string) (string, error) { return writeToken, nil }, Dialer: &Dialer{Scope: ScopeLoopbackOnly}}
	r.setDecision(Settings{Mode: ModeLive, Allowlist: []AllowEntry{{"con_1", "1"}}}, PolicyAuto)
	return r
}

func (r *rig) setDecision(s Settings, policy string) {
	r.gate.Load = func(context.Context, OpView) (DecideInput, error) {
		return DecideInput{Settings: s, HasCredential: true, PolicyMode: policy}, nil
	}
}

func (r *rig) commit(file, content string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.worktree, file), []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
	for _, a := range [][]string{{"add", "-A"}, {"-c", "user.name=Orqen", "-c", "user.email=orqen@localhost", "commit", "-q", "-m", "change"}} {
		if _, err := r.git.Run(context.Background(), r.worktree, workspace.Creds{}, 0, a...); err != nil {
			r.t.Fatal(err)
		}
	}
	res, err := r.git.Run(context.Background(), r.worktree, workspace.Creds{}, 0, "rev-parse", "HEAD")
	if err != nil {
		r.t.Fatal(err)
	}
	return strings.TrimSpace(res.Stdout)
}

func (r *rig) op(a Action) OpView {
	return OpView{Action: a, ConnectionID: "con_1", ProviderProjectID: "1", TargetURL: r.apiBase, IntentHash: "h-" + string(a)}
}

func (r *rig) run(in Exec) (Result, error) {
	r.t.Helper()
	op := r.op(in.Action)
	if in.Action == GitPush {
		op.TargetURL = r.repoURL
	}
	d, err := r.gate.Decide(context.Background(), op)
	if err != nil || d.Outcome() != OutcomeExecute {
		r.t.Fatalf("decision %s %v %v", d.Outcome(), d.Reasons(), err)
	}
	in.BaseURL, in.ProjectID, in.RepoURL, in.Dir, in.Git = r.apiBase, "1", r.repoURL, r.worktree, r.git
	var res Result
	err = r.gate.Execute(context.Background(), d, op, func(p *Permit) error {
		var e error
		res, e = Run(context.Background(), p, in)
		return e
	})
	return res, err
}

func TestPushLeaseDetectsAForeignPush(t *testing.T) {
	r := newRig(t)
	branch := "orqen/orq-1/abc"
	first := r.commit("a.txt", "one\n")
	res, err := r.run(Exec{Action: GitPush, Branch: branch, SHA: first, LeaseSHA: ""})
	if err != nil || res.PushedSHA != first {
		t.Fatal(res, err)
	}
	if r.repos.Head("group-1/proj-1", branch) != first || r.repos.PushCount() != 1 {
		t.Fatal("the push did not arrive")
	}
	if w := r.gl.Writes(); len(w) != 1 || w[0].Method != "PUSH" || w[0].TokenKind != "write" {
		t.Fatalf("%+v", w)
	}
	// a second push with the right lease works, a foreign commit breaks the next one
	second := r.commit("b.txt", "two\n")
	if _, err = r.run(Exec{Action: GitPush, Branch: branch, SHA: second, LeaseSHA: first}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.repos.PushAsOther("group-1/proj-1", branch); err != nil {
		t.Fatal(err)
	}
	third := r.commit("c.txt", "three\n")
	if _, err = r.run(Exec{Action: GitPush, Branch: branch, SHA: third, LeaseSHA: second}); !errors.Is(err, ErrForeignPush) {
		t.Fatalf("want a foreign push, got %v", err)
	}
	// reconcile: the head we wanted is not there
	if sha := r.repos.Head("group-1/proj-1", branch); sha == third {
		t.Fatal("the rejected push must not land")
	}
}

func TestPushArgvHoldsNoSecretAndConfigIsUntouched(t *testing.T) {
	r := newRig(t)
	cfg := filepath.Join(r.worktree, ".git", "config")
	before, _ := os.ReadFile(cfg)
	sha := r.commit("a.txt", "one\n")
	if _, err := r.run(Exec{Action: GitPush, Branch: "orqen/x/y", SHA: sha}); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pushed := false
	for _, a := range r.argv {
		line := strings.Join(a, " ")
		if strings.Contains(line, "WRITE-test-token") || strings.Contains(line, "oauth2:") {
			t.Fatalf("a secret on the command line: %s", line)
		}
		if strings.Contains(line, " push ") && strings.Contains(line, "--force-with-lease=refs/heads/orqen/x/y:") && strings.Contains(line, sha+":refs/heads/orqen/x/y") {
			pushed = true
		}
	}
	if !pushed {
		t.Fatal("no push argv recorded")
	}
	after, _ := os.ReadFile(cfg)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal(".git/config changed")
	}
}

func TestMRCreateTimeoutIsReconciledWithoutADuplicate(t *testing.T) {
	r := newRig(t)
	old := WriteTimeout
	WriteTimeout = 400 * time.Millisecond
	defer func() { WriteTimeout = old }()
	branch := "orqen/orq-1/abc"
	sha := r.commit("a.txt", "one\n")
	if _, err := r.run(Exec{Action: GitPush, Branch: branch, SHA: sha}); err != nil {
		t.Fatal(err)
	}
	marker := Marker("run_1", "mr_create:run_1")
	mr := MRParams{Title: "Draft: ORQ-1: x", SourceBranch: branch, TargetBranch: "main", Description: "body\n" + marker}
	r.gl.HangAfterWrite("/projects/1/merge_requests", 1)
	if _, err := r.run(Exec{Action: MRCreate, MR: mr, Marker: marker}); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("want unknown, got %v", err)
	}
	res, err := r.run(Exec{Action: MRCreate, MR: mr, Marker: marker, Reconcile: true})
	if err != nil || !res.Reconciled || res.MRIID != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if n := len(r.gl.MRs(1)); n != 1 {
		t.Fatalf("%d merge requests", n)
	}
	if !r.gl.MRs(1)[0].Draft {
		t.Fatal("the merge request must be a draft")
	}
}

func TestNotesDiscussionsAndUpdateUseTheWriteToken(t *testing.T) {
	r := newRig(t)
	branch := "orqen/orq-1/abc"
	sha := r.commit("a.txt", "one\n")
	_, _ = r.run(Exec{Action: GitPush, Branch: branch, SHA: sha})
	marker := Marker("run_1", "mr_create:run_1")
	res, err := r.run(Exec{Action: MRCreate, Marker: marker, MR: MRParams{Title: "Draft: x", SourceBranch: branch, TargetBranch: "main", Description: marker}})
	if err != nil {
		t.Fatal(err)
	}
	nm := Marker("run_1", "note:1")
	if _, err = r.run(Exec{Action: MRNote, MRIID: res.MRIID, Body: "summary " + nm, Marker: nm}); err != nil {
		t.Fatal(err)
	}
	if again, e := r.run(Exec{Action: MRNote, MRIID: res.MRIID, Body: "summary " + nm, Marker: nm, Reconcile: true}); e != nil || !again.Reconciled {
		t.Fatalf("a repost must adopt the existing note: %+v %v", again, e)
	}
	base := r.gl.MRs(1)[0].BaseSHA
	pos := &Position{PositionType: "text", BaseSHA: base, StartSHA: base, HeadSHA: sha, NewPath: "a.txt", NewLine: 1}
	dm := Marker("run_1", "discussion:1:F1")
	if _, err = r.run(Exec{Action: MRDiscussion, MRIID: res.MRIID, Body: "inline " + dm, Marker: dm, Position: pos}); err != nil {
		t.Fatal(err)
	}
	bad := *pos
	bad.NewLine = 99
	if _, err = r.run(Exec{Action: MRDiscussion, MRIID: res.MRIID, Body: "x", Marker: "y", Position: &bad}); err == nil {
		t.Fatal("a position outside the diff must be refused by GitLab")
	}
	if _, err = r.run(Exec{Action: MRUpdate, MRIID: res.MRIID, Body: "new body " + marker}); err != nil {
		t.Fatal(err)
	}
	if got := r.gl.MRs(1)[0]; got.Description != "new body "+marker || got.State != "opened" || len(got.Notes) != 1 || len(got.Discussions) != 1 {
		t.Fatalf("%+v", got)
	}
	for _, w := range r.gl.Writes() {
		if w.TokenKind != "write" {
			t.Fatalf("a write with token kind %s: %+v", w.TokenKind, w)
		}
	}
}

func TestEnvGuardRefusesAPublicHostWithZeroDialsAndNoGitProcess(t *testing.T) {
	r := newRig(t)
	dialer := &Dialer{Scope: ScopeLoopbackOnly, Lookup: func(context.Context, string) ([]net.IP, error) {
		t.Fatal("no name may be resolved")
		return nil, nil
	}}
	_ = dialer
	r.mu.Lock()
	r.argv = nil
	r.mu.Unlock()
	op := OpView{Action: MRCreate, ConnectionID: "con_1", ProviderProjectID: "1", TargetURL: "https://gitlab.example.invalid/api/v4", IntentHash: "h"}
	d, err := r.gate.Decide(context.Background(), op)
	if err != nil || d.Outcome() != OutcomeRefusedEnv {
		t.Fatalf("%s %v", d.Outcome(), err)
	}
	ran := false
	err = r.gate.Execute(context.Background(), d, op, func(*Permit) error { ran = true; return nil })
	var np *NotPermittedError
	if !errors.As(err, &np) || ran {
		t.Fatal("a refused decision must never run")
	}
	push := OpView{Action: GitPush, ConnectionID: "con_1", ProviderProjectID: "1", TargetURL: "https://gitlab.example.invalid/g/p.git", IntentHash: "h2"}
	d2, _ := r.gate.Decide(context.Background(), push)
	if d2.Outcome() != OutcomeRefusedEnv {
		t.Fatal(d2.Outcome())
	}
	if r.gate.Dialer.Dials() != 0 {
		t.Fatalf("%d dials", r.gate.Dialer.Dials())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.argv) != 0 {
		t.Fatalf("a git process was started: %v", r.argv)
	}
}
