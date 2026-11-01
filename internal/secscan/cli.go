package secscan

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"orqen/internal/extensions"
)

// AllowedEnv is the complete list of variables a scan may receive besides the fixed ones. BuildEnv panics on any
// other name (the catherd SecretEnv pattern).
var AllowedEnv = []string{"STRIX_LLM", "LLM_API_KEY", "LLM_API_BASE", "DOCKER_HOST"}

// CLI is the adapter for the Strix CLI (assumed contract v1). It exists only for an extensions.Permit and runs two argv
// vectors (no shell, no PATH lookup) with an explicit environment inside <data>/scans/<org>. The network is not
// restricted: the model provider needs it, and the UI says so.
type CLI struct {
	permit  extensions.Permit
	orgDir  string
	Mode    string   // extensions.SandboxEnforce (default) | SandboxBestEffort
	Path    string   // PATH of the child
	Extra   []string // extra writable paths (the Docker socket)
	Timeout time.Duration
	MaxOut  int64
}

// NewCLI binds the adapter to a permit and the org directory <data>/scans/<org>.
func NewCLI(p extensions.Permit, orgDir string) *CLI {
	return &CLI{permit: p, orgDir: orgDir, Mode: extensions.SandboxEnforce, Path: "/usr/bin:/bin", Timeout: time.Hour, MaxOut: 4 << 20}
}

var _ Scanner = (*CLI)(nil)

// OrgDir is the root of everything the adapter may write for an org.
func OrgDir(dataDir, orgID string) string { return filepath.Join(dataDir, "scans", orgID) }

func (c *CLI) fixedEnv() []string {
	return []string{
		"HOME=" + filepath.Join(c.orgDir, "home"),
		"TMPDIR=" + filepath.Join(c.orgDir, "tmp"),
		"PATH=" + c.Path,
		"LANG=C.UTF-8",
		"NO_COLOR=1",
	}
}

func (c *CLI) prepare(verb string, paths ...string) error {
	for _, d := range []string{"home", "tmp"} {
		if err := os.MkdirAll(filepath.Join(c.orgDir, d), 0o700); err != nil {
			return errf(KindUnavailable, verb, "cannot create %s: %v", d, err)
		}
	}
	for _, p := range paths {
		if !within(c.orgDir, p) {
			return errf(KindBadRequest, verb, "%q is outside the org directory", p)
		}
	}
	return nil
}

// within reports whether p (which may not exist yet) resolves to a path inside root.
func within(root, p string) bool {
	rr, err := filepath.EvalSymlinks(root)
	if err != nil || !filepath.IsAbs(p) {
		return false
	}
	rest, cur := "", filepath.Clean(p)
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			full := filepath.Join(r, rest)
			return full == rr || strings.HasPrefix(full, rr+string(filepath.Separator))
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return false
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func (c *CLI) exec(ctx context.Context, args, env []string, timeout time.Duration) extensions.Result {
	return extensions.Exec(ctx, c.permit, extensions.Spec{
		Bin: c.permit.Path(), Args: args, Env: env, Dir: c.orgDir, Timeout: timeout, StdoutCap: c.MaxOut,
		Sandbox: extensions.WriteUnder(c.orgDir, c.Extra...), SandboxMode: c.Mode,
	})
}

func (c *CLI) fail(verb string, res extensions.Result) error {
	switch {
	case res.Err == extensions.ErrSandboxUnavailable:
		return errf(KindSandbox, verb, "the sandbox is enforced but not usable")
	case res.Err != nil:
		return errf(KindUnavailable, verb, "cannot start: %v", res.Err)
	}
	return nil
}

// Version runs `--version` and keeps the first line of its output.
func (c *CLI) Version(ctx context.Context) (Health, error) {
	if err := c.prepare("version"); err != nil {
		return Health{}, err
	}
	res := c.exec(ctx, []string{"--version"}, c.fixedEnv(), 20*time.Second)
	if err := c.fail("version", res); err != nil {
		return Health{}, err
	}
	if res.TimedOut || res.Exit != 0 {
		return Health{}, errf(KindFailed, "version", "exit status %d", res.Exit)
	}
	line := strings.TrimSpace(strings.SplitN(string(res.Stdout), "\n", 2)[0])
	if line == "" || len(line) > 200 {
		return Health{}, errf(KindFailed, "version", "unreadable version")
	}
	return Health{Version: line}, nil
}

func (c *CLI) argv(req Request) []string {
	args := []string{"--target", req.Tree, "--non-interactive", "--output-dir", req.ReportDir, "--max-turns", strconv.Itoa(req.MaxTurns)}
	if req.BudgetUSD > 0 {
		args = append(args, "--budget-usd", strconv.FormatFloat(req.BudgetUSD, 'f', -1, 64))
	}
	return args
}

func (c *CLI) scanEnv(req Request) []string {
	vals := map[string]string{}
	if req.Model != "" {
		vals["STRIX_LLM"] = req.Model
	}
	if req.LLMKey != "" {
		vals["LLM_API_KEY"] = req.LLMKey
	}
	if req.APIBase != "" {
		vals["LLM_API_BASE"] = req.APIBase
	}
	if req.DockerHost != "" {
		vals["DOCKER_HOST"] = req.DockerHost
	}
	return extensions.BuildEnv(c.fixedEnv(), AllowedEnv, vals)
}

const maxReport = 4 << 20

// Scan runs one scan of req.Tree. The tree must already exist (a disposable copy) and both paths must be inside the org
// directory; a URL, a relative path or an endpoint that is not an http(s) URL is refused before anything starts.
func (c *CLI) Scan(ctx context.Context, req Request) (RawResult, error) {
	var out RawResult
	if !filepath.IsAbs(req.Tree) || !filepath.IsAbs(req.ReportDir) {
		return out, errf(KindBadRequest, "scan", "tree and report directory must be absolute paths")
	}
	if req.MaxTurns < 1 || req.MaxTurns > 1000 || req.BudgetUSD < 0 {
		return out, errf(KindBadRequest, "scan", "max turns must be 1..1000 and the budget not negative")
	}
	if req.APIBase != "" {
		u, err := url.Parse(req.APIBase)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return out, errf(KindBadRequest, "scan", "the model endpoint must be an http(s) URL")
		}
	}
	if err := c.prepare("scan", req.Tree, req.ReportDir); err != nil {
		return out, err
	}
	if err := os.MkdirAll(req.ReportDir, 0o700); err != nil {
		return out, errf(KindUnavailable, "scan", "cannot create the report directory")
	}
	res := c.exec(ctx, c.argv(req), c.scanEnv(req), c.Timeout)
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if err := c.fail("scan", res); err != nil {
		return out, err
	}
	known := []string{req.LLMKey}
	out = RawResult{Exit: res.Exit, TimedOut: res.TimedOut, Stdout: Redact(string(res.Stdout), known), StderrTail: Redact(res.StderrTail, known)}
	if p := filepath.Join(req.ReportDir, "report.json"); within(c.orgDir, p) {
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() && fi.Size() <= maxReport {
			if f, err := os.Open(p); err == nil {
				out.Report, _ = io.ReadAll(io.LimitReader(f, maxReport))
				f.Close()
			}
		}
	}
	return out, nil
}
