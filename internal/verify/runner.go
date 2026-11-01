package verify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Sandbox modes.
const (
	ModeEnforce    = "enforce"
	ModeBestEffort = "best_effort"
)

// Command is one approved command, resolved for execution.
type Command struct {
	Key          string
	Argv         []string
	Cwd          string // relative to the checkout ("" or ".")
	Env          []string
	Timeout      time.Duration
	NeedsNetwork bool
	NeedsDocker  bool
}

// Spec describes one verification run.
type Spec struct {
	Dir      string // the detached checkout
	Tmp      string // a private writable directory (created if missing)
	Commands []Command
	BaseEnv  []string
	Sandbox  string
	// MaxOutput caps each of stdout and stderr, in bytes.
	MaxOutput int64
	Scrub     func([]byte) []byte
	// DockerOK answers whether Docker is usable (nil means not checked: unavailable).
	DockerOK func() bool
	// NoSandbox pretends sandbox-exec is missing (tests).
	NoSandbox bool
}

// Command outcomes.
const (
	OutcomePass       = "pass"
	OutcomeFail       = "fail"
	OutcomeIncomplete = "incomplete"
)

// Result is what one command did.
type Result struct {
	Key        string
	Argv       []string
	Cwd        string
	ExitCode   int
	DurationMs int64
	TimedOut   bool
	Sandbox    string // enforced | best_effort | none
	Outcome    string // pass | fail | incomplete
	Reason     string // why incomplete: timeout, spawn_error, sandbox_unavailable, docker_unavailable, bad_cwd
	Stdout     []byte
	Stderr     []byte
	SHA256     string
}

type capped struct {
	buf bytes.Buffer
	max int64
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - int64(c.buf.Len()); room > 0 {
		c.buf.Write(p[:min(int64(len(p)), room)])
	}
	return len(p), nil
}

func sum(stdout, stderr []byte) string {
	h := sha256.New()
	h.Write(stdout)
	h.Write([]byte{0})
	h.Write(stderr)
	return hex.EncodeToString(h.Sum(nil))
}

// Run executes the commands one after another. A failed command never stops the others: every result is reported.
func Run(ctx context.Context, spec Spec) ([]Result, error) {
	if spec.MaxOutput <= 0 {
		spec.MaxOutput = 4 << 20
	}
	dir := realPath(spec.Dir)
	if err := os.MkdirAll(spec.Tmp, 0o700); err != nil {
		return nil, err
	}
	tmp := realPath(spec.Tmp)
	sandboxed := !spec.NoSandbox && SandboxAvailable()
	var out []Result
	for _, c := range spec.Commands {
		out = append(out, runOne(ctx, spec, c, dir, tmp, sandboxed))
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
	}
	return out, nil
}

func runOne(ctx context.Context, spec Spec, c Command, dir, tmp string, sandboxed bool) Result {
	r := Result{Key: c.Key, Argv: c.Argv, Cwd: c.Cwd, ExitCode: -1, Sandbox: "none"}
	incomplete := func(reason string) Result { r.Outcome, r.Reason = OutcomeIncomplete, reason; return r }
	if len(c.Argv) == 0 {
		return incomplete("spawn_error")
	}
	cwd := dir
	if c.Cwd != "" && c.Cwd != "." {
		cwd = filepath.Join(dir, filepath.Clean(c.Cwd))
		real, err := filepath.EvalSymlinks(cwd)
		if err != nil || real != dir && !strings.HasPrefix(real, dir+string(filepath.Separator)) {
			return incomplete("bad_cwd")
		}
		cwd = real
	}
	if c.NeedsDocker && (spec.DockerOK == nil || !spec.DockerOK()) {
		return incomplete("docker_unavailable")
	}
	argv := c.Argv
	switch {
	case sandboxed:
		profile := SandboxProfile(dir, tmp, c.NeedsNetwork)
		argv = append([]string{SandboxExec, "-p", profile}, c.Argv...)
		r.Sandbox = "enforced"
	case spec.Sandbox == ModeBestEffort:
		r.Sandbox = "best_effort"
	default:
		return incomplete("sandbox_unavailable")
	}
	home := filepath.Join(tmp, "home")
	_ = os.MkdirAll(home, 0o700)
	env := append([]string{}, spec.BaseEnv...)
	env = append(env, "HOME="+home, "TMPDIR="+tmp)
	env = append(env, c.Env...)
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = cwd, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, stderr := &capped{max: spec.MaxOutput}, &capped{max: spec.MaxOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return incomplete("spawn_error")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var werr error
	select {
	case werr = <-done:
	case <-cctx.Done():
		r.TimedOut = cctx.Err() == context.DeadlineExceeded
		pgid := cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case werr = <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			werr = <-done
		}
		_ = syscall.Kill(-pgid, syscall.SIGKILL) // anything the command left behind
	}
	r.DurationMs = time.Since(start).Milliseconds()
	r.Stdout, r.Stderr = stdout.buf.Bytes(), stderr.buf.Bytes()
	if spec.Scrub != nil {
		r.Stdout, r.Stderr = spec.Scrub(r.Stdout), spec.Scrub(r.Stderr)
	}
	r.SHA256 = sum(r.Stdout, r.Stderr)
	var ee *exec.ExitError
	switch {
	case r.TimedOut:
		return incomplete("timeout")
	case ctx.Err() != nil:
		return incomplete("cancelled")
	case werr == nil:
		r.ExitCode, r.Outcome = 0, OutcomePass
	case errors.As(werr, &ee):
		r.ExitCode, r.Outcome = ee.ExitCode(), OutcomeFail
	default:
		return incomplete("spawn_error")
	}
	return r
}
