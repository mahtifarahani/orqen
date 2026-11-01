package workspace

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"orqen/internal/testfakes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type env struct {
	t       *testing.T
	data    string
	m       *Manager
	repos   *testfakes.GitRepos
	remote  string // file:// URL of the bare repo
	argvLog *argvRecorder
}
type argvRecorder struct {
	mu   sync.Mutex
	rows [][]string
}

func (r *argvRecorder) add(a []string) { r.mu.Lock(); r.rows = append(r.rows, a); r.mu.Unlock() }
func (r *argvRecorder) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, row := range r.rows {
		b.WriteString(strings.Join(row, " ") + "\n")
	}
	return b.String()
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if !testfakes.GitAvailable() {
		t.Skip("git is not available")
	}
	data := t.TempDir()
	root := filepath.Join(data, "workspaces", "org_x")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := &argvRecorder{}
	if _, err := EnsureAskPass(data); err != nil {
		t.Fatal(err)
	}
	g := &Git{DataDir: data, Ceiling: filepath.Join(data, "workspaces"), AllowFile: true, AllowHTTP: true, Hook: rec.add}
	repos, err := testfakes.NewGitRepos(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, data: data, m: &Manager{Root: root, Git: g}, repos: repos, remote: "file://" + filepath.Join(repos.Dir, "group-1", "proj-1.git"), argvLog: rec}
}
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func (e *env) prepare(run string) (string, Worktree) {
	e.t.Helper()
	ctx := context.Background()
	mirror, err := e.m.EnsureMirror(ctx, "con_1", "1", e.remote)
	if err != nil {
		e.t.Fatal(err)
	}
	if err = e.m.Fetch(ctx, mirror, "main", Creds{}); err != nil {
		e.t.Fatal(err)
	}
	wt, err := e.m.AddWorktree(ctx, mirror, run, "orqen/orq-1/"+strings.ToLower(run), "main")
	if err != nil {
		e.t.Fatal(err)
	}
	return mirror, wt
}

func TestWorktreeLifecycleAndRecovery(t *testing.T) {
	e := newEnv(t)
	mirror, wt := e.prepare("run0001")
	if wt.BaseSHA != e.repos.SHA || wt.Branch != "orqen/orq-1/run0001" || wt.BaseRef != "refs/remotes/origin/main" {
		t.Fatalf("%+v", wt)
	}
	if b, err := os.ReadFile(filepath.Join(wt.Path, "package.json")); err != nil || !strings.Contains(string(b), "vitest") {
		t.Fatal(err)
	}
	if got := gitOut(t, wt.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != wt.Branch {
		t.Fatal(got)
	}
	st, err := e.m.Inspect(context.Background(), wt.Path, wt.BaseSHA)
	if err != nil || !st.Clean || st.HeadSHA != wt.BaseSHA || st.Ahead != 0 {
		t.Fatal(st, err)
	}
	// A crash after the add: the second prepare reuses the registered worktree.
	again, err := e.m.AddWorktree(context.Background(), mirror, "run0001", wt.Branch, "main")
	if err != nil || again.Path != wt.Path || again.BaseSHA != wt.BaseSHA {
		t.Fatal(again, err)
	}
	if n := strings.Count(gitOut(t, mirror, "worktree", "list", "--porcelain"), "worktree "); n != 2 {
		t.Fatal("worktrees:", n)
	}
	// A leftover directory that git does not know (a crash inside the add) is cleared and re-added.
	_, wt2 := e.prepare("run0002")
	if err = os.RemoveAll(filepath.Join(mirror, "worktrees", "run0002")); err != nil {
		t.Fatal(err)
	}
	fixed, err := e.m.AddWorktree(context.Background(), mirror, "run0002", wt2.Branch, "main")
	if err != nil || fixed.Path != wt2.Path {
		t.Fatal(fixed, err)
	}
	if _, err = os.Stat(filepath.Join(fixed.Path, "package.json")); err != nil {
		t.Fatal(err)
	}
	// Removing a clean worktree deletes the directory and the branch.
	if err = e.m.Remove(context.Background(), mirror, wt.Path, wt.Branch, wt.BaseSHA, true); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatal("worktree still there")
	}
	if out := gitOut(t, mirror, "branch", "--list", wt.Branch); out != "" {
		t.Fatal("branch still there:", out)
	}
	// Missing target branch.
	if _, err = e.m.AddWorktree(context.Background(), mirror, "run0003", "orqen/orq-1/run0003", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestPersonalCheckoutIsNeverTouched(t *testing.T) {
	e := newEnv(t)
	personal := filepath.Join(t.TempDir(), "mine")
	gitOut(t, filepath.Dir(personal), "clone", "-q", e.remote, personal)
	if err := os.WriteFile(filepath.Join(personal, "scratch.txt"), []byte("my own work"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap := func() [5]string {
		cfg, _ := os.ReadFile(filepath.Join(personal, ".git", "config"))
		pkg, _ := os.ReadFile(filepath.Join(personal, "package.json"))
		return [5]string{gitOut(t, personal, "rev-parse", "HEAD"), gitOut(t, personal, "status", "--porcelain"), string(cfg), sha256Sum(string(pkg)), gitOut(t, personal, "branch", "-a")}
	}
	before := snap()
	mirror, wt := e.prepare("run0001")
	if err := os.WriteFile(filepath.Join(wt.Path, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Inspect(context.Background(), wt.Path, wt.BaseSHA); err != nil {
		t.Fatal(err)
	}
	var sink strings.Builder
	if err := e.m.Snapshot(context.Background(), wt.Path, wt.BaseSHA, &sinkWriter{&sink}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Remove(context.Background(), mirror, wt.Path, wt.Branch, wt.BaseSHA, false); err != nil {
		t.Fatal(err)
	}
	if after := snap(); after != before {
		t.Fatalf("the personal checkout changed:\n%v\n%v", before, after)
	}
	// Nothing git ran was aimed at it.
	if strings.Contains(e.argvLog.joined(), personal) {
		t.Fatal("a git command named the personal checkout")
	}
	// The sandbox refuses paths outside the root, and file remotes unless the test flag is set.
	for _, p := range []string{personal, filepath.Join(e.m.Root, "..", "..", "mine"), "/"} {
		if err := e.m.inside(p); !errors.Is(err, ErrUnsafePath) {
			t.Fatal(p, err)
		}
	}
	strict := &Manager{Root: e.m.Root, Git: &Git{DataDir: e.data, Ceiling: e.m.Git.Ceiling}}
	if _, err := strict.CheckURL("file://" + personal); !errors.Is(err, ErrBadURL) {
		t.Fatal(err)
	}
}

type sinkWriter struct{ b *strings.Builder }

func (s *sinkWriter) Write(p []byte) (int, error) { return s.b.Write(p) }

func TestAskPassFlowAgainstSmartHTTP(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(e.repos.Handler())
	defer srv.Close()
	url := srv.URL + "/gitlab/group-1/proj-1.git"
	host := strings.TrimPrefix(srv.URL, "http://")
	ctx := context.Background()
	mirror, err := e.m.EnsureMirror(ctx, "con_1", "1", url)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "glpat-test"
	if err = e.m.Fetch(ctx, mirror, "main", Creds{Host: host, Username: "oauth2", Password: "wrong-token-value"}); !errors.Is(err, ErrAuth) {
		t.Fatal("a wrong token must be ErrAuth:", err)
	} else if strings.Contains(err.Error(), "wrong-token-value") {
		t.Fatal("the secret is in the error:", err)
	}
	if err = e.m.Fetch(ctx, mirror, "main", Creds{Host: host, Username: "oauth2", Password: secret}); err != nil {
		t.Fatal(err)
	}
	// No credential in the mirror's config, a URL without userinfo, no credential section.
	cfg, _ := os.ReadFile(filepath.Join(mirror, "config"))
	if strings.Contains(string(cfg), secret) || strings.Contains(string(cfg), "@") || strings.Contains(string(cfg), "[credential") || !strings.Contains(string(cfg), url) {
		t.Fatal(string(cfg))
	}
	// No secret in any argv; the keychain helper is switched off on every command.
	argv := e.argvLog.joined()
	if strings.Contains(argv, secret) || strings.Contains(argv, "wrong-token") || strings.Contains(argv, "oauth2") {
		t.Fatal("secret in argv:\n", argv)
	}
	for _, row := range e.argvLog.rows {
		if !strings.Contains(strings.Join(row, " "), "credential.helper=") || !strings.Contains(strings.Join(row, " "), "protocol.allow=never") || !strings.Contains(strings.Join(row, " "), "core.hooksPath=/dev/null") {
			t.Fatal("fixed flags missing from", row)
		}
	}
	// Nothing was written to any file under the data directory or the git home with the secret.
	_ = filepath.WalkDir(e.data, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), secret) {
				t.Errorf("secret written to %s", p)
			}
		}
		return nil
	})
	// The helper is fixed, executable only by us, and holds no secret.
	info, err := os.Stat(AskPassPath(e.data))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal(info, err)
	}
	// URLs with credentials are refused before git sees them.
	if _, err = e.m.EnsureMirror(ctx, "con_1", "2", "http://oauth2:"+secret+"@127.0.0.1:1/x.git"); !errors.Is(err, ErrBadURL) {
		t.Fatal(err)
	}
}

func TestNetworkFailureIsClassified(t *testing.T) {
	e := newEnv(t)
	mirror, err := e.m.EnsureMirror(context.Background(), "con_1", "1", "http://127.0.0.1:1/gitlab/group-1/proj-1.git")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.m.Fetch(context.Background(), mirror, "main", Creds{Host: "127.0.0.1:1", Username: "oauth2", Password: "x"}); !errors.Is(err, ErrNetwork) {
		t.Fatal(err)
	}
}

func TestDirtyWorktreeIsPreservedUntilDiscard(t *testing.T) {
	e := newEnv(t)
	mirror, wt := e.prepare("run0001")
	if err := os.WriteFile(filepath.Join(wt.Path, "package.json"), []byte(`{"changed":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "notes.txt"), []byte("untracked work"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := e.m.Inspect(context.Background(), wt.Path, wt.BaseSHA)
	if err != nil || st.Clean || st.Modified != 1 || st.Untracked != 1 {
		t.Fatal(st, err)
	}
	if err = e.m.Remove(context.Background(), mirror, wt.Path, wt.Branch, wt.BaseSHA, true); !errors.Is(err, ErrDirty) {
		t.Fatal("a dirty worktree must refuse:", err)
	}
	if _, err = os.Stat(filepath.Join(wt.Path, "notes.txt")); err != nil {
		t.Fatal("the refused removal deleted work")
	}
	var buf pipeBuf
	if err = e.m.Snapshot(context.Background(), wt.Path, wt.BaseSHA, &buf); err != nil {
		t.Fatal(err)
	}
	files := untar(t, buf.data)
	if !strings.Contains(files["changes.diff"], `{"changed":true}`) || files["untracked/notes.txt"] != "untracked work" {
		t.Fatalf("%v", files)
	}
	// A committed change makes HEAD differ from the base and also blocks the clean removal.
	gitOut(t, wt.Path, "add", "-A")
	gitOut(t, wt.Path, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "work")
	st, _ = e.m.Inspect(context.Background(), wt.Path, wt.BaseSHA)
	if !st.Clean || st.Ahead != 1 || st.HeadSHA == wt.BaseSHA {
		t.Fatal(st)
	}
	if err = e.m.Remove(context.Background(), mirror, wt.Path, wt.Branch, wt.BaseSHA, true); !errors.Is(err, ErrDirty) {
		t.Fatal("commits ahead of the base must be preserved:", err)
	}
	if err = e.m.Remove(context.Background(), mirror, wt.Path, wt.Branch, wt.BaseSHA, false); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatal("forced removal failed")
	}
}

type pipeBuf struct{ data []byte }

func (p *pipeBuf) Write(b []byte) (int, error) { p.data = append(p.data, b...); return len(b), nil }
func untar(t *testing.T, data []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}

func TestPathSafetyAndDisk(t *testing.T) {
	e := newEnv(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(e.m.Root, "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.m.Root, "runs", "evil")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.RunPath("evil"); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("a symlink out of the root must be refused:", err)
	}
	if err := os.Symlink(outside, filepath.Join(e.m.Root, "mirrors")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.MirrorPath("con_1", "1"); !errors.Is(err, ErrUnsafePath) {
		t.Fatal(err)
	}
	for _, bad := range []string{"../x", "a/b", "", "a b"} {
		if _, err := e.m.RunPath(bad); !errors.Is(err, ErrUnsafePath) {
			t.Fatal(bad, err)
		}
	}
	// Disk space is an injected statfs.
	low := &Manager{Root: e.m.Root, Git: e.m.Git, MinFreeBytes: 2048 << 20, Statfs: func(string) (uint64, error) { return 1 << 20, nil }}
	if err := low.CheckDisk(); !errors.Is(err, ErrDiskLow) {
		t.Fatal(err)
	}
	ok := &Manager{Root: e.m.Root, Git: e.m.Git, MinFreeBytes: 2048 << 20, Statfs: func(string) (uint64, error) { return 8 << 30, nil }}
	if err := ok.CheckDisk(); err != nil {
		t.Fatal(err)
	}
}

func TestGitVersionAndClassification(t *testing.T) {
	g := &Git{DataDir: t.TempDir()}
	if v, err := g.Version(context.Background()); err != nil || !strings.Contains(v, "git version") {
		t.Fatal(v, err)
	}
	if _, err := (&Git{Bin: "/nonexistent/git", DataDir: t.TempDir()}).Version(context.Background()); !errors.Is(err, ErrGitMissing) {
		t.Fatal(err)
	}
	cases := map[string]error{
		"fatal: Authentication failed for 'https://x/y.git/'":                                  ErrAuth,
		"fatal: unable to access 'https://x/': The requested URL returned error: 403":          ErrAuth,
		"remote: HTTP Basic: Access denied. fatal: Authentication failed":                      ErrAuth,
		"fatal: unable to access 'https://x/': Could not resolve host: x":                      ErrNetwork,
		"fatal: unable to access 'https://x/': Failed to connect to x port 443 after 3 ms":     ErrNetwork,
		"fatal: unable to access 'https://x/': Connection timed out after 5001 milliseconds":   ErrNetwork,
		"fatal: unable to access 'https://x/': The requested URL returned error: 502":          ErrNetwork,
		"remote: The project you were looking for could not be found or Repository not found.": ErrNotFound,
		"fatal: unable to access 'https://x/': The requested URL returned error: 404":          ErrNotFound,
		"fatal: couldn't find remote ref refs/heads/nope":                                      ErrNotFound,
		"fatal: something strange": ErrGit,
	}
	for stderr, want := range cases {
		if err := classify(stderr, errors.New("exit 128")); !errors.Is(err, want) {
			t.Errorf("%q -> %v, want %v", stderr, err, want)
		}
	}
}
