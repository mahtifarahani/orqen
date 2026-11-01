package codegraph

import (
	"context"
	"embed"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"orqen/internal/extensions"
)

//go:embed schema/v1/*.json
var schemaFS embed.FS

// SchemaFile returns the embedded output schema of a verb (version, analyze, impact, context).
func SchemaFile(name string) ([]byte, error) { return schemaFS.ReadFile("schema/v1/" + name + ".json") }

// SchemaNames are the verbs with an output schema.
var SchemaNames = []string{"version", "analyze", "impact", "context"}

// CLI is the adapter for the GitNexus CLI (assumed contract v1). It exists only for an extensions.Permit, runs the
// four argv vectors below (no shell, no PATH lookup) with an explicit environment inside <data>/codegraph/<org>, and
// validates every output against schema/v1.
type CLI struct {
	permit         extensions.Permit
	orgDir         string
	Mode           string // extensions.SandboxEnforce (default) | SandboxBestEffort
	Path           string // PATH of the child
	AnalyzeTimeout time.Duration
	QueryTimeout   time.Duration
	MaxOut         int64
}

// NewCLI binds the adapter to a permit and the org directory <data>/codegraph/<org>.
func NewCLI(p extensions.Permit, orgDir string) *CLI {
	return &CLI{permit: p, orgDir: orgDir, Mode: extensions.SandboxEnforce, Path: "/usr/bin:/bin",
		AnalyzeTimeout: 900 * time.Second, QueryTimeout: 20 * time.Second, MaxOut: 4 << 20}
}

var _ Index = (*CLI)(nil)

// OrgDir is the root of everything the adapter may read or write.
func OrgDir(dataDir, orgID string) string { return filepath.Join(dataDir, "codegraph", orgID) }

func (c *CLI) env() []string {
	return []string{
		"HOME=" + filepath.Join(c.orgDir, "home"),
		"TMPDIR=" + filepath.Join(c.orgDir, "tmp"),
		"PATH=" + c.Path,
		"LANG=C.UTF-8",
		"NO_COLOR=1",
		"GITNEXUS_NO_TELEMETRY=1",
	}
}

// within reports whether p (which may not exist yet) resolves to a path inside root.
func within(root, p string) bool {
	rr, err := filepath.EvalSymlinks(root)
	if err != nil || !filepath.IsAbs(p) {
		return false
	}
	p = filepath.Clean(p)
	rest := ""
	cur := p
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

func (c *CLI) prepare(verb string, paths ...string) error {
	for _, d := range []string{"home", "tmp"} {
		if err := os.MkdirAll(filepath.Join(c.orgDir, d), 0o700); err != nil {
			return errf(KindUnavailable, verb, "cannot create %s: %v", d, err)
		}
	}
	for _, p := range paths {
		if !within(c.orgDir, p) {
			return errf(KindBadPath, verb, "%q is outside the org directory", p)
		}
	}
	return nil
}

var (
	filePattern   = regexp.MustCompile(`^[^\x00-\x1f-][^\x00-\x1f]{0,499}$`)
	symbolPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.$:<>-]{0,199}$`)
)

// ValidTarget checks a query target: a relative file path without .. or a symbol name, never something that could
// read as a flag.
func ValidTarget(kind, target string) bool {
	switch kind {
	case KindFile:
		return filePattern.MatchString(target) && !filepath.IsAbs(target) && !strings.Contains(target, "..")
	case KindSymbol:
		return symbolPattern.MatchString(target)
	}
	return false
}

func (c *CLI) run(ctx context.Context, verb string, args []string, timeout time.Duration, schema string, out any) error {
	profile := extensions.DenyNetworkWriteUnder(c.orgDir)
	res := extensions.Exec(ctx, c.permit, extensions.Spec{
		Bin: c.permit.Path(), Args: args, Env: c.env(), Dir: c.orgDir, Timeout: timeout, StdoutCap: c.MaxOut,
		Sandbox: profile, SandboxMode: c.Mode,
	})
	switch {
	case res.Err == extensions.ErrSandboxUnavailable:
		return errf(KindSandbox, verb, "the sandbox is enforced but not usable")
	case res.Err != nil:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errf(KindUnavailable, verb, "cannot start: %v", res.Err)
	case res.TimedOut:
		return errf(KindTimeout, verb, "no result within %s", timeout)
	case res.Overflow:
		return errf(KindInvalidOutput, verb, "output exceeds %d bytes", c.MaxOut)
	case res.Exit != 0:
		return errf(KindFailed, verb, "exit status %d", res.Exit)
	}
	sb, err := SchemaFile(schema)
	if err != nil {
		return errf(KindInvalidOutput, verb, "%v", err)
	}
	if verr := extensions.ValidateJSON(sb, res.Stdout); verr != nil {
		return errf(KindInvalidOutput, verb, "%v", verr)
	}
	if out != nil {
		if err = json.Unmarshal(res.Stdout, out); err != nil {
			return errf(KindInvalidOutput, verb, "%v", err)
		}
	}
	return nil
}

func (c *CLI) Version(ctx context.Context) (Health, error) {
	var h Health
	if err := c.prepare("version"); err != nil {
		return h, err
	}
	err := c.run(ctx, "version", []string{"--version"}, c.QueryTimeout, "version", &h)
	return h, err
}

func (c *CLI) Analyze(ctx context.Context, tree, out string) (AnalyzeResult, error) {
	var r AnalyzeResult
	if err := c.prepare("analyze", tree, out); err != nil {
		return r, err
	}
	err := c.run(ctx, "analyze", []string{"analyze", "--index-only", "--path", tree, "--output", out, "--json"}, c.AnalyzeTimeout, "analyze", &r)
	return r, err
}

func (c *CLI) Impact(ctx context.Context, index, target, kind string, depth, limit int) (ImpactResult, error) {
	var r ImpactResult
	if err := c.prepare("impact", index); err != nil {
		return r, err
	}
	if !ValidTarget(kind, target) {
		return r, errf(KindBadPath, "impact", "invalid %s target", kind)
	}
	if depth < 1 || depth > 2 || limit < 1 || limit > 200 {
		return r, errf(KindBadPath, "impact", "depth must be 1..2 and limit 1..200")
	}
	err := c.run(ctx, "impact", []string{"query", "impact", "--index", index, "--target", target, "--depth", strconv.Itoa(depth), "--limit", strconv.Itoa(limit), "--json"}, c.QueryTimeout, "impact", &r)
	return r, err
}

func (c *CLI) Context(ctx context.Context, index, target string, limit int) (ContextResult, error) {
	var r ContextResult
	if err := c.prepare("context", index); err != nil {
		return r, err
	}
	if !ValidTarget(KindFile, target) || limit < 1 || limit > 200 {
		return r, errf(KindBadPath, "context", "invalid target or limit")
	}
	err := c.run(ctx, "context", []string{"query", "context", "--index", index, "--target", target, "--limit", strconv.Itoa(limit), "--json"}, c.QueryTimeout, "context", &r)
	return r, err
}
