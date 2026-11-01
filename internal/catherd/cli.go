package catherd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const (
	stdoutLimit = 16 << 20
	stderrLimit = 64 << 10
)

type limitWriter struct {
	buf      bytes.Buffer
	max      int
	exceeded bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.max {
		w.exceeded = true
		p2 := p[:max(w.max-w.buf.Len(), 0)]
		w.buf.Write(p2)
		return len(p), nil
	}
	return w.buf.Write(p)
}

// CLI runs the catherd executable. It never starts `catherd mcp` or `catherd init`.
type CLI struct {
	RT *Runtime
	// Neutral is the directory for commands that read no state.
	Neutral string
}

type result struct {
	stdout []byte
	stderr string
	exit   int
}

// run executes catherd. sec holds the secrets already present in vars: they are replaced with [REDACTED] in
// stdout and stderr before anything is parsed, stored or wrapped into an error. Neither cmd.Env nor the argv is
// ever formatted into an error or log.
func (c *CLI) run(ctx context.Context, env ReadEnv, vars []string, sec SecretEnv, timeout time.Duration, args ...string) (result, error) {
	if c.RT == nil || c.RT.Bin == "" {
		return result{}, &Error{Code: CodeUnavailable, Message: "catherd was not found"}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.RT.Bin, args...)
	cmd.Dir = env.WorkDir()
	cmd.Env = vars
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	out, errw := &limitWriter{max: stdoutLimit}, &limitWriter{max: stderrLimit}
	cmd.Stdout, cmd.Stderr = out, errw
	err := cmd.Run()
	res := result{stdout: sec.scrubBytes(out.buf.Bytes()), stderr: sec.scrubString(errw.buf.String())}
	if out.exceeded {
		return res, &Error{Code: CodeBadOutput, Message: "catherd printed more than 16 MiB"}
	}
	if err == nil {
		return res, nil
	}
	var ee *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return res, &Error{Code: CodeTimeout, Message: "catherd did not finish in time"}
	case ctx.Err() != nil:
		return res, ctx.Err()
	case errors.As(err, &ee):
		res.exit = ee.ExitCode()
		return res, nil
	}
	return res, &Error{Code: CodeUnavailable, Message: "catherd could not be started: " + ScrubText(sec.scrubString(err.Error()))}
}

// fail converts a non-zero exit into an Error carrying catherd's code, message and fix.
func fail(r result) error {
	code, msg, fix := parseStderr(r.stderr)
	if code == "" {
		code = "orqen_exit"
		line, _, _ := strings.Cut(strings.TrimSpace(r.stderr), "\n")
		msg = line
	}
	if r.exit == 2 {
		code = CodeRejected
	}
	return &Error{Code: code, Message: ScrubText(msg), Fix: ScrubText(fix), Exit: r.exit}
}
func (c *CLI) ok(ctx context.Context, env ReadEnv, vars []string, sec SecretEnv, timeout time.Duration, args ...string) (result, error) {
	r, err := c.run(ctx, env, vars, sec, timeout, args...)
	if err != nil {
		return r, err
	}
	if r.exit != 0 {
		return r, fail(r)
	}
	return r, nil
}
func (c *CLI) neutral() ReadEnv { return NeutralEnv{Dir: c.Neutral} }
func (c *CLI) Version(ctx context.Context) (string, error) {
	env := c.neutral()
	r, err := c.ok(ctx, env, env.Vars(c.RT), nil, 10*time.Second, "--version")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(r.stdout)), nil
}
func badOutput(err error) error {
	return &Error{Code: CodeBadOutput, Message: "catherd printed unexpected output: " + ScrubText(err.Error())}
}
func (c *CLI) ListProfiles(ctx context.Context, env ReadEnv) ([]ProfileRow, error) {
	r, err := c.ok(ctx, env, env.Vars(c.RT), nil, 30*time.Second, "profile", "list", "--json")
	if err != nil {
		return nil, err
	}
	var rows []ProfileRow
	if err = json.Unmarshal(r.stdout, &rows); err != nil {
		return nil, badOutput(err)
	}
	return rows, nil
}
func checkHost(host string) error {
	if !ValidHost(host) {
		return &Error{Code: CodeRejected, Message: "unknown orchestration host", Exit: 2}
	}
	return nil
}
func (c *CLI) show(ctx context.Context, env ReadEnv, host, name string, raw bool) (map[string]any, error) {
	if err := checkHost(host); err != nil {
		return nil, err
	}
	args := []string{"profile", "show", name, "--json", "--host", host}
	if raw {
		args = append(args, "--raw")
	}
	r, err := c.ok(ctx, env, env.Vars(c.RT), nil, 30*time.Second, args...)
	if err != nil {
		return nil, err
	}
	m, err := Decode(r.stdout)
	if err != nil {
		return nil, badOutput(err)
	}
	return m, nil
}
func (c *CLI) ShowRaw(ctx context.Context, env ReadEnv, host, name string) (map[string]any, error) {
	return c.show(ctx, env, host, name, true)
}
func (c *CLI) ShowResolved(ctx context.Context, env *OrgEnv, host, name string) (map[string]any, error) {
	return c.show(ctx, env, host, name, false)
}
func (c *CLI) Create(ctx context.Context, env *OrgEnv, host, name string, sec SecretEnv) error {
	if err := checkHost(host); err != nil {
		return err
	}
	_, err := c.ok(ctx, env, env.VarsWith(c.RT, sec), sec, 30*time.Second, "profile", "new", name, "--host", host)
	return err
}
func (c *CLI) SetKey(ctx context.Context, env *OrgEnv, host, name, key string, value any, sec SecretEnv) (SetOutcome, error) {
	if err := checkHost(host); err != nil {
		return SetOutcome{}, err
	}
	b, err := json.Marshal(value)
	if err != nil {
		return SetOutcome{}, err
	}
	r, err := c.run(ctx, env, env.VarsWith(c.RT, sec), sec, 30*time.Second, "profile", "set", "--profile", name, "--host", host, key, string(b))
	if err != nil {
		return SetOutcome{}, err
	}
	switch {
	case r.exit == 0:
		return SetOutcome{Saved: true}, nil
	case r.exit == 1:
		code, msg, fix := parseStderr(r.stderr)
		if code == "E_CONFIG_INVALID" && strings.HasPrefix(msg, refusedPrefix) {
			return SetOutcome{Refused: refusalIssues(msg, fix)}, nil
		}
	}
	return SetOutcome{}, fail(r)
}
func (c *CLI) Validate(ctx context.Context, env *OrgEnv, host, name string, sec SecretEnv) (Validation, error) {
	if err := checkHost(host); err != nil {
		return Validation{}, err
	}
	r, err := c.run(ctx, env, env.VarsWith(c.RT, sec), sec, 30*time.Second, "profile", "validate", name, "--host", host, "--json")
	if err != nil {
		return Validation{}, err
	}
	if r.exit != 0 && r.exit != 1 {
		return Validation{}, fail(r)
	}
	var raw struct {
		Valid  bool `json:"valid"`
		Errors []struct {
			Path, Message, Fix string
		} `json:"errors"`
		Warnings []struct {
			Path, Message, Fix string
		} `json:"warnings"`
	}
	if err = json.Unmarshal(r.stdout, &raw); err != nil {
		if r.exit == 1 {
			return Validation{}, fail(r)
		}
		return Validation{}, badOutput(err)
	}
	conv := func(code, severity string, in []struct{ Path, Message, Fix string }) []Issue {
		out := []Issue{}
		for _, x := range in {
			out = append(out, Issue{Path: SplitPath(x.Path), PathDisplay: x.Path, Code: code, Message: ScrubText(x.Message), Fix: ScrubText(x.Fix), Origin: "catherd", Severity: severity})
		}
		return out
	}
	return Validation{Valid: raw.Valid, Errors: conv("catherd_invalid", "error", raw.Errors), Warnings: conv("catherd_warning", "warning", raw.Warnings)}, nil
}
func (c *CLI) Remove(ctx context.Context, env *OrgEnv, name string) error {
	_, err := c.ok(ctx, env, env.Vars(c.RT), nil, 30*time.Second, "profile", "rm", name)
	return err
}
func (c *CLI) CatalogList(ctx context.Context, env *OrgEnv) ([]byte, error) {
	r, err := c.ok(ctx, env, env.Vars(c.RT), nil, 30*time.Second, "catalog", "list", "--json")
	return r.stdout, err
}
func (c *CLI) CatalogRefresh(ctx context.Context, env *OrgEnv, sec SecretEnv) (json.RawMessage, error) {
	r, err := c.ok(ctx, env, env.VarsWith(c.RT, sec), sec, 120*time.Second, "catalog", "refresh", "--json")
	return rawJSON(r.stdout, err)
}
func (c *CLI) CatalogSync(ctx context.Context, env *OrgEnv, force bool, sec SecretEnv) (json.RawMessage, error) {
	args := []string{"catalog", "sync", "--json"}
	if force {
		args = append(args, "--force")
	}
	r, err := c.ok(ctx, env, env.SyncVarsWith(c.RT, sec), sec, 180*time.Second, args...)
	return rawJSON(r.stdout, err)
}
func rawJSON(b []byte, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return json.RawMessage(`{}`), nil
	}
	return json.RawMessage(b), nil
}
func (c *CLI) Doctor(ctx context.Context, env *OrgEnv, host string, sec SecretEnv) (json.RawMessage, int, error) {
	if err := checkHost(host); err != nil {
		return nil, 0, err
	}
	r, err := c.run(ctx, env, env.VarsWith(c.RT, sec), sec, 180*time.Second, "doctor", "--host", host, "--json")
	if err != nil {
		return nil, 0, err
	}
	if r.exit != 0 && r.exit != 3 {
		return nil, r.exit, fail(r)
	}
	if !json.Valid(r.stdout) {
		return nil, r.exit, badOutput(errors.New("not JSON"))
	}
	return json.RawMessage(r.stdout), r.exit, nil
}

// dirEnv runs a command of the org namespace in another directory (the repo a profile is bound to).
type dirEnv struct {
	*OrgEnv
	dir string
}

func (e dirEnv) WorkDir() string { return e.dir }

// RunsShow reads the run's records and dispatches. It starts no model and changes nothing.
func (c *CLI) RunsShow(ctx context.Context, env *OrgEnv, runID string) (RunView, error) {
	r, err := c.ok(ctx, env, env.Vars(c.RT), nil, 60*time.Second, "runs", "show", runID, "--json", "--debug")
	if err != nil {
		return RunView{}, err
	}
	v, err := ParseRunView(r.stdout)
	if err != nil {
		return RunView{}, badOutput(err)
	}
	return v, nil
}

// RunsCancel cancels one live role.
func (c *CLI) RunsCancel(ctx context.Context, env *OrgEnv, runID, name string) error {
	_, err := c.ok(ctx, env, env.Vars(c.RT), nil, 60*time.Second, "runs", "cancel", runID, name)
	return err
}

// ProfileUseRepo binds the profile to the repo containing repoDir, for the given host.
func (c *CLI) ProfileUseRepo(ctx context.Context, env *OrgEnv, host, name, repoDir string) error {
	if err := checkHost(host); err != nil {
		return err
	}
	_, err := c.ok(ctx, dirEnv{env, repoDir}, env.Vars(c.RT), nil, 30*time.Second, "profile", "use", name, "--repo", "--host", host)
	return err
}

var _ Port = (*CLI)(nil)

// scrubString replaces every injected value (raw and JSON-escaped) with [REDACTED].
func (s SecretEnv) scrubString(in string) string {
	for _, v := range s {
		if v == "" {
			continue
		}
		in = strings.ReplaceAll(in, v, "[REDACTED]")
		if b, err := json.Marshal(v); err == nil && len(b) > 2 {
			in = strings.ReplaceAll(in, string(b[1:len(b)-1]), "[REDACTED]")
		}
	}
	return in
}
func (s SecretEnv) scrubBytes(in []byte) []byte {
	if len(s) == 0 {
		return in
	}
	return []byte(s.scrubString(string(in)))
}
