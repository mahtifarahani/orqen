// Package workspace manages the bare mirrors and per-run git worktrees under the data directory. Git runs with an
// explicit environment and fixed flags so the user's global configuration, credential helpers (the macOS keychain
// helper in particular) and personal checkouts are never involved. It must not import internal/server.
package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"orqen/internal/semver"
)

// Errors a caller classifies.
var (
	ErrAuth       = errors.New("workspace: authentication failed")
	ErrNetwork    = errors.New("workspace: network unavailable")
	ErrNotFound   = errors.New("workspace: repository or branch not found")
	ErrDiskLow    = errors.New("workspace: not enough free disk space")
	ErrUnsafePath = errors.New("workspace: path outside the workspace root")
	ErrGitMissing = errors.New("workspace: git is missing or older than 2.30")
	ErrGit        = errors.New("workspace: git failed")
	ErrDirty      = errors.New("workspace: the worktree has changes")
	ErrBadURL     = errors.New("workspace: the clone URL is not acceptable")
)

// Creds are the git credentials of one process. They reach git only through its environment.
type Creds struct{ Host, Username, Password string }

// Git runs git processes.
type Git struct {
	Bin string // default "git"
	// DataDir holds git-home, bin/orqen-git-askpass and git-ca.
	DataDir string
	// Ceiling is GIT_CEILING_DIRECTORIES (<data>/workspaces).
	Ceiling string
	Proxy   string
	// CAPEM is a custom CA bundle; it is written under DataDir/git-ca and appended to the system roots.
	CAPEM string
	// AllowHTTP permits plain http remotes (a loopback base URL); AllowFile permits file remotes (tests).
	AllowHTTP, AllowFile bool
	// Hook sees every argv (never the environment) before it runs. Tests assert no secret is in it.
	Hook func(argv []string)
	// FetchTimeout and Timeout default to 10 minutes and 60 seconds.
	FetchTimeout, Timeout time.Duration
}

const askpassScript = `#!/bin/sh
# Prints the credential from the environment of the one git process that asks, and only for its own host.
case "$1" in
  *"$ORQEN_GIT_HOST"*) ;;
  *) exit 1 ;;
esac
case "$1" in
  Username*) printf '%s' "$ORQEN_GIT_USERNAME" ;;
  *) printf '%s' "$ORQEN_GIT_PASSWORD" ;;
esac
`

// AskPassPath is the fixed helper path under dir; EnsureAskPass writes it (mode 0700, no secret inside).
func AskPassPath(dir string) string { return filepath.Join(dir, "bin", "orqen-git-askpass") }
func EnsureAskPass(dir string) (string, error) {
	p := AskPassPath(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	if cur, err := os.ReadFile(p); err == nil && string(cur) == askpassScript {
		return p, os.Chmod(p, 0o700)
	}
	return p, os.WriteFile(p, []byte(askpassScript), 0o700)
}

func (g *Git) bin() string {
	if g.Bin != "" {
		return g.Bin
	}
	return "git"
}

// Version returns the git version, or ErrGitMissing when git is absent or older than 2.30.
func (g *Git) Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, g.bin(), "--version").Output()
	if err != nil {
		return "", ErrGitMissing
	}
	v, ok := semver.Find(string(out))
	if !ok || v.Major < 2 || v.Major == 2 && v.Minor < 30 {
		return string(bytes.TrimSpace(out)), ErrGitMissing
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *Git) flags() []string {
	f := []string{"-c", "credential.helper=", "-c", "http.followRedirects=false", "-c", "core.hooksPath=/dev/null", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always"}
	if g.AllowHTTP {
		f = append(f, "-c", "protocol.http.allow=always")
	}
	if g.AllowFile {
		f = append(f, "-c", "protocol.file.allow=always")
	}
	return f
}

func (g *Git) env(c Creds) ([]string, error) {
	home := filepath.Join(g.DataDir, "git-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_CEILING_DIRECTORIES=" + g.Ceiling, "LANG=C", "LC_ALL=C"}
	if g.Proxy != "" {
		env = append(env, "HTTPS_PROXY="+g.Proxy, "HTTP_PROXY="+g.Proxy)
	}
	if g.CAPEM != "" {
		p, err := g.caFile()
		if err != nil {
			return nil, err
		}
		env = append(env, "GIT_SSL_CAINFO="+p)
	}
	if c.Host != "" {
		env = append(env, "GIT_ASKPASS="+AskPassPath(g.DataDir), "ORQEN_GIT_HOST="+c.Host, "ORQEN_GIT_USERNAME="+c.Username, "ORQEN_GIT_PASSWORD="+c.Password)
	}
	return env, nil
}

// caFile writes the custom CA together with the system roots, content-addressed.
func (g *Git) caFile() (string, error) {
	dir := filepath.Join(g.DataDir, "git-ca")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	bundle := g.CAPEM + "\n"
	if sys, err := os.ReadFile("/etc/ssl/cert.pem"); err == nil {
		bundle += string(sys)
	}
	p := filepath.Join(dir, shaHex(bundle)[:16]+".pem")
	if _, err := os.Stat(p); err != nil {
		if err = os.WriteFile(p, []byte(bundle), 0o600); err != nil {
			return "", err
		}
	}
	return p, nil
}

var (
	authRe     = regexp.MustCompile(`(?i)authentication failed|could not read (username|password)|returned error: 40[13]|HTTP/[0-9.]+ 40[13]`)
	notFoundRe = regexp.MustCompile(`(?i)repository not found|returned error: 404|couldn't find remote ref|does not appear to be a git repository|not a valid object name|invalid reference`)
	networkRe  = regexp.MustCompile(`(?i)could not resolve host|failed to connect|connection timed out|connection refused|connection reset|operation timed out|network is unreachable|early eof|unable to access|returned error: 5\d\d|ssl|tls|proxy`)
)

// Result is a finished git command.
type Result struct {
	Stdout string
	Stderr string
}

// Run executes git with the fixed flags in dir. Secrets in creds are scrubbed from the returned error text.
func (g *Git) Run(ctx context.Context, dir string, c Creds, timeout time.Duration, args ...string) (Result, error) {
	if timeout <= 0 {
		timeout = g.Timeout
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
	}
	env, err := g.env(c)
	if err != nil {
		return Result{}, err
	}
	argv := append(g.flags(), args...)
	if g.Hook != nil {
		g.Hook(append([]string{g.bin()}, argv...))
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, g.bin(), argv...)
	cmd.Dir, cmd.Env = dir, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr limitBuffer
	stdout.max, stderr.max = 16<<20, 64<<10
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: g.scrub(stderr.String(), c)}
	if runErr == nil {
		return res, nil
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if cctx.Err() != nil {
		return res, fmt.Errorf("%w: timeout", ErrNetwork)
	}
	if errors.Is(runErr, exec.ErrNotFound) {
		return res, ErrGitMissing
	}
	return res, classify(res.Stderr, runErr)
}

// classify maps git's (LANG=C) stderr to a sentinel; the message is kept short and scrubbed.
func classify(stderr string, runErr error) error {
	msg := firstLines(stderr, 2)
	switch {
	case authRe.MatchString(stderr):
		return fmt.Errorf("%w: %s", ErrAuth, msg)
	case notFoundRe.MatchString(stderr):
		return fmt.Errorf("%w: %s", ErrNotFound, msg)
	case networkRe.MatchString(stderr):
		return fmt.Errorf("%w: %s", ErrNetwork, msg)
	}
	return fmt.Errorf("%w: %s (%v)", ErrGit, msg, runErr)
}
func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.TrimSpace(strings.Join(lines, " "))
}

var urlUserinfo = regexp.MustCompile(`(https?://)[^/@\s]+@`)

func (g *Git) scrub(s string, c Creds) string {
	s = urlUserinfo.ReplaceAllString(s, "${1}[redacted]@")
	for _, secret := range []string{c.Password, c.Username} {
		if len(secret) >= 4 {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}

type limitBuffer struct {
	buf bytes.Buffer
	max int
}

func (l *limitBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
func (l *limitBuffer) String() string { return l.buf.String() }

func shaHex(s string) string { return sha256Sum(s) }
