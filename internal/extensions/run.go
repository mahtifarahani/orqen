package extensions

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"orqen/internal/verify"
)

// This is the only file in internal/{extensions,codegraph,secscan} that starts a process (the census test checks it).

// Spec describes one execution. Env is the complete environment; nil is treated as empty, never as "inherit".
type Spec struct {
	Bin         string // must equal the permit's path
	Args        []string
	Env         []string
	Dir         string
	Timeout     time.Duration
	StdoutCap   int64
	Sandbox     string // sandbox profile text; empty = none
	SandboxMode string // enforce | best_effort (default enforce when a profile is given)
}

// Result of an execution. Err is set when the process could not start or the sandbox was required but missing.
type Result struct {
	Exit       int
	Stdout     []byte
	StderrTail string
	TimedOut   bool
	Overflow   bool // stdout exceeded StdoutCap (the tail was dropped)
	Err        error
}

// Errors.
var (
	ErrSandboxUnavailable = errors.New("extensions: the sandbox is enforced but sandbox-exec is not usable")
	ErrBinMismatch        = errors.New("extensions: spec binary differs from the permit")
	ErrNotAbsolute        = errors.New("extensions: binary path must be absolute")
)

const stderrTailMax = 2048

// Exec runs one binary with an explicit environment in its own process group. It needs a valid Permit.
func Exec(ctx context.Context, p Permit, spec Spec) Result {
	if !p.Valid() {
		return Result{Exit: -1, Err: errors.New("extensions: no permit")}
	}
	if !filepath.IsAbs(spec.Bin) {
		return Result{Exit: -1, Err: ErrNotAbsolute}
	}
	if spec.Bin != p.path {
		return Result{Exit: -1, Err: ErrBinMismatch}
	}
	argv := append([]string{spec.Bin}, spec.Args...)
	if spec.Sandbox != "" {
		if spec.SandboxMode == SandboxBestEffort && !verify.SandboxAvailable() {
			// run unsandboxed, as asked
		} else {
			if !verify.SandboxAvailable() {
				return Result{Exit: -1, Err: ErrSandboxUnavailable}
			}
			argv = append([]string{verify.SandboxExec, "-p", spec.Sandbox}, argv...)
		}
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	max := spec.StdoutCap
	if max <= 0 {
		max = 4 << 20
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Env = append([]string{}, spec.Env...) // never nil: nil would inherit the parent environment
	cmd.Dir = spec.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var stdout bytes.Buffer
	lim := &capWriter{w: &stdout, max: max}
	tail := &tailWriter{max: stderrTailMax}
	cmd.Stdout = lim
	cmd.Stderr = tail
	runErr := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), StderrTail: tail.String(), Overflow: lim.over}
	if cctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.Exit = -1
		return res
	}
	if ctx.Err() != nil {
		res.Exit = -1
		res.Err = ctx.Err()
		return res
	}
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			res.Exit = ee.ExitCode()
			return res
		}
		res.Exit = -1
		res.Err = runErr
	}
	return res
}

type capWriter struct {
	w    *bytes.Buffer
	max  int64
	over bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	if room := c.max - int64(c.w.Len()); int64(len(p)) > room {
		c.over = true
		if room > 0 {
			c.w.Write(p[:room])
		}
		return len(p), nil
	}
	return c.w.Write(p)
}

type tailWriter struct {
	buf []byte
	max int
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailWriter) String() string { return string(t.buf) }
