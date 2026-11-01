package kindex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"orqen/internal/verify"
)

// CLI is the adapter for the OpenClaw Wiki CLI (assumed contract v1). The binary comes from ORQEN_WIKI_BIN and must be
// an absolute path; there is no PATH lookup. Only the seven verbs below are ever started.
type CLI struct {
	Bin       string // absolute path; empty = not configured
	DataDir   string
	ChildPath string // PATH of the child
	Sandbox   string // enforce | best_effort
	Timeout   time.Duration
	MaxStdout int64
}

// verbArgs is the complete argv allowlist (after `<bin> wiki`).
var verbArgs = map[string][]string{
	"version":   {"version"},
	"ingest":    {"ingest"},
	"synthesis": {"synthesis", "register"},
	"compile":   {"compile"},
	"lint":      {"lint"},
	"search":    {"search"},
	"get":       {"get"},
}

var _ Index = (*CLI)(nil)

func (c *CLI) configured(verb string) error {
	if c.Bin == "" || !filepath.IsAbs(c.Bin) {
		return errf(KindNotConfigured, verb, "ORQEN_WIKI_BIN is empty or not an absolute path")
	}
	return nil
}

// run starts one verb and validates its JSON output against the schema before decoding it into out.
func (c *CLI) run(ctx context.Context, v Vault, verb string, extra []string, out any) error {
	if err := c.configured(verb); err != nil {
		return err
	}
	root, vault, err := resolveVault(c.DataDir, v)
	if err != nil {
		return err
	}
	args := append([]string{"wiki"}, verbArgs[verb]...)
	args = append(args, "--vault", vault, "--json")
	args = append(args, extra...)
	argv := append([]string{c.Bin}, args...)
	switch c.Sandbox {
	case SandboxBestEffort:
	default:
		if !SandboxAvailable() {
			return errf(KindUnavailable, verb, "sandbox is enforced but %s is not usable", verify.SandboxExec)
		}
		tmp, _ := filepath.EvalSymlinks(filepath.Join(root, "tmp"))
		argv = append([]string{verify.SandboxExec, "-p", sandboxProfile(root, tmp)}, argv...)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	max := c.MaxStdout
	if max <= 0 {
		max = 4 << 20
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Env = Env(c.DataDir, v.OrgID, c.childPath())
	cmd.Dir = root
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = nil
	var stdout bytes.Buffer
	lim := &limited{w: &stdout, max: max}
	cmd.Stdout = lim
	cmd.Stderr = io.Discard
	runErr := cmd.Run()
	if cctx.Err() == context.DeadlineExceeded {
		return errf(KindTimeout, verb, "no result within %s", timeout)
	}
	if cctx.Err() != nil {
		return cctx.Err()
	}
	if lim.over {
		return errf(KindInvalidOutput, verb, "output exceeds %d bytes", max)
	}
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			return errf(KindFailed, verb, "exit status %d", ee.ExitCode())
		}
		return errf(KindUnavailable, verb, "cannot start: %v", runErr)
	}
	if verr := Validate(schemaFor(verb), stdout.Bytes()); verr != nil {
		return errf(KindInvalidOutput, verb, "%v", verr)
	}
	if out != nil {
		if err = json.Unmarshal(stdout.Bytes(), out); err != nil {
			return errf(KindInvalidOutput, verb, "%v", err)
		}
	}
	return nil
}

func schemaFor(verb string) string { return verb }

func (c *CLI) childPath() string {
	if c.ChildPath != "" {
		return c.ChildPath
	}
	return "/usr/bin:/bin"
}

type limited struct {
	w    *bytes.Buffer
	max  int64
	over bool
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - int64(l.w.Len()); int64(len(p)) > room {
		l.over = true
		if room > 0 {
			l.w.Write(p[:room])
		}
		return len(p), nil
	}
	return l.w.Write(p)
}

func (c *CLI) inside(verb string, v Vault, path string) error {
	if !InsideOrg(c.DataDir, v.OrgID, path) {
		return errf(KindBadVault, verb, "%q is outside the org directory", path)
	}
	return nil
}

func (c *CLI) Health(ctx context.Context, v Vault) (Health, error) {
	var h Health
	err := c.run(ctx, v, "version", nil, &h)
	return h, err
}

func (c *CLI) Ingest(ctx context.Context, v Vault, dir string) (IngestResult, error) {
	var r IngestResult
	if err := c.configured("ingest"); err != nil {
		return r, err
	}
	if _, _, err := resolveVault(c.DataDir, v); err != nil {
		return r, err
	}
	if err := c.inside("ingest", v, dir); err != nil {
		return r, err
	}
	err := c.run(ctx, v, "ingest", []string{"--input", dir}, &r)
	return r, err
}

func (c *CLI) RegisterSynthesis(ctx context.Context, v Vault, file string) error {
	if err := c.configured("synthesis"); err != nil {
		return err
	}
	if _, _, err := resolveVault(c.DataDir, v); err != nil {
		return err
	}
	if err := c.inside("synthesis", v, file); err != nil {
		return err
	}
	var r struct {
		Registered bool `json:"registered"`
	}
	if err := c.run(ctx, v, "synthesis", []string{"--input", file}, &r); err != nil {
		return err
	}
	if !r.Registered {
		return errf(KindFailed, "synthesis", "not registered")
	}
	return nil
}

func (c *CLI) Compile(ctx context.Context, v Vault) (CompileResult, error) {
	var r CompileResult
	err := c.run(ctx, v, "compile", nil, &r)
	return r, err
}

func (c *CLI) Lint(ctx context.Context, v Vault) (LintResult, error) {
	var r LintResult
	err := c.run(ctx, v, "lint", nil, &r)
	return r, err
}

func (c *CLI) Search(ctx context.Context, v Vault, q string, limit int) ([]Hit, error) {
	var r struct {
		Hits []Hit `json:"hits"`
	}
	err := c.run(ctx, v, "search", []string{"--query", q, "--limit", strconv.Itoa(limit)}, &r)
	return r.Hits, err
}

func (c *CLI) Get(ctx context.Context, v Vault, id string, maxBytes int) (Slice, error) {
	var r Slice
	err := c.run(ctx, v, "get", []string{"--id", id, "--max-bytes", strconv.Itoa(maxBytes)}, &r)
	return r, err
}
