package testfakes

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// GitRepos serves bare repositories over git's smart HTTP protocol (git-http-backend) below /gitlab/, behind Basic
// authentication as oauth2:<Token>. NewGitRepos creates group-1/proj-1.git with one commit holding the fake GitLab
// files on branches main and develop.
type GitRepos struct {
	Dir   string
	Token string
	mu    sync.RWMutex
	// requests records every request path and query served (authenticated or not).
	reqs []string
	// PushToken is the only token that may push (receive-pack); the read Token gets 403. Empty: nobody may push.
	PushToken string
	// OnPush runs after a push changed a branch (project path such as group-1/proj-1, branch, new sha).
	OnPush func(project, branch, sha string)
	pushes []Push
	// SHA is the commit id of main in group-1/proj-1.git.
	SHA string
}

// GitAvailable reports whether git and git-http-backend can be used.
func GitAvailable() bool {
	_, err := backendPath()
	return err == nil
}
func backendPath() (string, error) {
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		return "", err
	}
	p := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	if _, err = os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

// cleanGitEnv isolates the helper git processes from the user's configuration.
func cleanGitEnv(home string, extra ...string) []string {
	return append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LANG=C", "LC_ALL=C",
		"GIT_AUTHOR_NAME=Orqen Fake", "GIT_AUTHOR_EMAIL=fake@example.com", "GIT_COMMITTER_NAME=Orqen Fake", "GIT_COMMITTER_EMAIL=fake@example.com",
		"GIT_AUTHOR_DATE=2026-01-02T03:04:05Z", "GIT_COMMITTER_DATE=2026-01-02T03:04:05Z"}, extra...)
}
func runGit(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// NewGitRepos creates the repositories below dir.
func NewGitRepos(dir string) (*GitRepos, error) {
	if _, err := backendPath(); err != nil {
		return nil, fmt.Errorf("git-http-backend unavailable: %w", err)
	}
	g := &GitRepos{Dir: dir, Token: "glpat-test"}
	home, err := os.MkdirTemp("", "orqen-fake-git-home-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	env := cleanGitEnv(home)
	work, err := os.MkdirTemp("", "orqen-fake-git-work-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	if _, err = runGit(work, env, "init", "-q", "-b", "main"); err != nil {
		return nil, err
	}
	files := map[string]string{}
	for k, v := range NewGitLab().Files {
		files[k] = v
	}
	// Approved commands of the manual walk: the check passes once a worker wrote src/feature.txt.
	files["scripts/check.sh"] = "#!/bin/sh\ngrep -q orqen-feature src/feature.txt\n"
	files["scripts/lint.sh"] = "#!/bin/sh\nexit 0\n"
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := filepath.Join(work, n)
		if err = os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err = os.WriteFile(p, []byte(files[n]), 0o644); err != nil {
			return nil, err
		}
	}
	if _, err = runGit(work, env, "add", "-A"); err != nil {
		return nil, err
	}
	if _, err = runGit(work, env, "commit", "-q", "-m", "initial"); err != nil {
		return nil, err
	}
	if g.SHA, err = runGit(work, env, "rev-parse", "HEAD"); err != nil {
		return nil, err
	}
	if _, err = runGit(work, env, "branch", "develop"); err != nil {
		return nil, err
	}
	dest := filepath.Join(dir, "group-1", "proj-1.git")
	if err = os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, err
	}
	if _, err = runGit(work, env, "clone", "-q", "--bare", work, dest); err != nil {
		return nil, err
	}
	if _, err = runGit(dest, env, "config", "http.receivepack", "true"); err != nil {
		return nil, err
	}
	return g, nil
}

// SetToken changes the accepted token while the handler is serving.
func (g *GitRepos) SetToken(t string) { g.mu.Lock(); g.Token = t; g.mu.Unlock() }

// Requests returns the path and query of every request served so far.
func (g *GitRepos) Requests() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]string{}, g.reqs...)
}

// ReceivePackRequests counts the requests that touched git-receive-pack (a push attempt, even a refused one).
func (g *GitRepos) ReceivePackRequests() int {
	n := 0
	for _, r := range g.Requests() {
		if strings.Contains(r, "git-receive-pack") {
			n++
		}
	}
	return n
}

// Handler serves /gitlab/<group>/<project>.git/...
func (g *GitRepos) Handler() http.Handler {
	backend, _ := backendPath()
	cgiHandler := &cgi.Handler{Path: backend, Root: "/gitlab", Env: []string{"GIT_PROJECT_ROOT=" + g.Dir, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.reqs = append(g.reqs, r.URL.Path+"?"+r.URL.RawQuery)
		g.mu.Unlock()
		user, pass, ok := r.BasicAuth()
		g.mu.RLock()
		token, pushToken := g.Token, g.PushToken
		g.mu.RUnlock()
		receive := strings.HasSuffix(r.URL.Path, "/git-receive-pack") || r.URL.Query().Get("service") == "git-receive-pack"
		if receive && ok && user == "oauth2" && (pushToken == "" || subtle.ConstantTimeCompare([]byte(pass), []byte(pushToken)) != 1) {
			http.Error(w, "forbidden: this token may not push", http.StatusForbidden)
			return
		}
		pushOK := receive && ok && user == "oauth2" && pushToken != "" && subtle.ConstantTimeCompare([]byte(pass), []byte(pushToken)) == 1
		pushTokenOK := pushToken != "" && subtle.ConstantTimeCompare([]byte(pass), []byte(pushToken)) == 1
		if !ok || user != "oauth2" || !pushOK && !pushTokenOK && subtle.ConstantTimeCompare([]byte(pass), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if backend == "" {
			http.Error(w, "git-http-backend unavailable", http.StatusInternalServerError)
			return
		}
		if receive && r.Method == http.MethodPost {
			project := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/gitlab/"), ".git/git-receive-pack")
			before := g.heads(project)
			cgiHandler.ServeHTTP(w, r)
			g.afterPush(project, before, "write")
			return
		}
		cgiHandler.ServeHTTP(w, r)
	})
}

// Push is one accepted push.
type Push struct{ Project, Branch, SHA, TokenKind string }

// Pushes lists the accepted pushes.
func (g *GitRepos) Pushes() []Push {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]Push{}, g.pushes...)
}

// PushCount is the number of accepted pushes.
func (g *GitRepos) PushCount() int { return len(g.Pushes()) }

func (g *GitRepos) bare(project string) string {
	return filepath.Join(g.Dir, filepath.FromSlash(project)+".git")
}

func (g *GitRepos) heads(project string) map[string]string {
	out := map[string]string{}
	env := cleanGitEnv(os.TempDir())
	text, err := runGit(g.bare(project), env, "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads")
	if err != nil {
		return out
	}
	for _, l := range strings.Split(text, "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			out[f[0]] = f[1]
		}
	}
	return out
}

func (g *GitRepos) afterPush(project string, before map[string]string, kind string) {
	for branch, sha := range g.heads(project) {
		if before[branch] == sha {
			continue
		}
		g.mu.Lock()
		g.pushes = append(g.pushes, Push{project, branch, sha, kind})
		hook := g.OnPush
		g.mu.Unlock()
		if hook != nil {
			hook(project, branch, sha)
		}
	}
}

// Head returns the sha of a branch of a project, or "".
func (g *GitRepos) Head(project, branch string) string { return g.heads(project)[branch] }

// PushAsOther makes a foreign commit on top of a branch, as if another person pushed it.
func (g *GitRepos) PushAsOther(project, branch string) (string, error) {
	work, err := os.MkdirTemp("", "orqen-fake-other-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	env := cleanGitEnv(work)
	before := g.heads(project)
	if _, err = runGit(work, env, "clone", "-q", "--branch", branch, g.bare(project), "clone"); err != nil {
		return "", err
	}
	dir := filepath.Join(work, "clone")
	if err = os.WriteFile(filepath.Join(dir, "other-"+fmt.Sprint(len(before))+".txt"), []byte("someone else\n"), 0o644); err != nil {
		return "", err
	}
	if _, err = runGit(dir, env, "add", "-A"); err != nil {
		return "", err
	}
	if _, err = runGit(dir, env, "commit", "-q", "-m", "foreign commit"); err != nil {
		return "", err
	}
	if _, err = runGit(dir, env, "push", "-q", "origin", branch); err != nil {
		return "", err
	}
	g.afterPush(project, before, "other")
	return g.Head(project, branch), nil
}

// PushRewritten replaces a branch with a history that does not contain its old head (a force push by someone else).
func (g *GitRepos) PushRewritten(project, branch string) (string, error) {
	work, err := os.MkdirTemp("", "orqen-fake-rewrite-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	env := cleanGitEnv(work)
	before := g.heads(project)
	if _, err = runGit(work, env, "clone", "-q", "--branch", g.defaultOf(project), g.bare(project), "clone"); err != nil {
		return "", err
	}
	dir := filepath.Join(work, "clone")
	if _, err = runGit(dir, env, "checkout", "-q", "-B", branch); err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(dir, "rewritten.txt"), []byte("rewritten\n"), 0o644); err != nil {
		return "", err
	}
	if _, err = runGit(dir, env, "add", "-A"); err != nil {
		return "", err
	}
	if _, err = runGit(dir, env, "commit", "-q", "-m", "rewritten history"); err != nil {
		return "", err
	}
	if _, err = runGit(dir, env, "push", "-q", "-f", "origin", branch); err != nil {
		return "", err
	}
	g.afterPush(project, before, "other")
	return g.Head(project, branch), nil
}

func (g *GitRepos) defaultOf(project string) string { return "main" }
