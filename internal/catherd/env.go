package catherd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// SecretEnv is the secrets injected into exactly one catherd call, by environment variable name.
// Values are never logged, formatted or stored by this package; the CLI scrubs them from catherd's output.
type SecretEnv map[string]string

// AllowedSecretEnv is the complete list of variables Orqen may hand to catherd.
var AllowedSecretEnv = []string{"TYPESAFE_API_KEY", "ARTIFICIAL_ANALYSIS_API_KEY", "GEMINI_API_KEY", "CURSOR_API_KEY", "XAI_API_KEY"}

func (s SecretEnv) lines() []string {
	names := make([]string, 0, len(s))
	for k := range s {
		allowed := false
		for _, a := range AllowedSecretEnv {
			allowed = allowed || a == k
		}
		if !allowed {
			panic("catherd: " + k + " is not an injectable secret")
		}
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, k := range names {
		if s[k] != "" {
			out = append(out, k+"="+s[k])
		}
	}
	return out
}

// ReadEnv is the environment of a read-only catherd call. Only *OrgEnv can write.
type ReadEnv interface {
	Vars(*Runtime) []string
	WorkDir() string
	// Home identifies the state directory (for logs and fakes).
	Home() string
}

var orgIDPattern = regexp.MustCompile(`^org_[0-9A-HJKMNP-TV-Z]{26}$`)

func baseVars(rt *Runtime, tmp string) []string {
	v := []string{"PATH=" + rt.ChildPath, "HOME=" + rt.UserHome, "TMPDIR=" + tmp}
	for _, k := range []string{"LANG", "LC_ALL", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
		if x := rt.Getenv(k); x != "" {
			v = append(v, k+"="+x)
		}
	}
	return append(v, "NO_COLOR=1", "CATHERD_LOG=warn")
}

// OrgEnv is one organization's private catherd namespace under <data>/catherd/<org>.
type OrgEnv struct{ root string }

// NewOrgEnv panics unless orgID is a valid organization id and the result stays under <data>/catherd.
func NewOrgEnv(dataDir, orgID string) *OrgEnv {
	if !orgIDPattern.MatchString(orgID) {
		panic(fmt.Sprintf("catherd: invalid organization id %q", orgID))
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		panic(err)
	}
	base := filepath.Join(abs, "catherd")
	root := filepath.Join(base, orgID)
	if rel, err := filepath.Rel(base, root); err != nil || rel != orgID {
		panic("catherd: namespace escapes the data directory")
	}
	return &OrgEnv{root: root}
}
func (e *OrgEnv) Root() string    { return e.root }
func (e *OrgEnv) Home() string    { return filepath.Join(e.root, "home") }
func (e *OrgEnv) Agents() string  { return filepath.Join(e.root, "claude-agents") }
func (e *OrgEnv) WorkDir() string { return filepath.Join(e.root, "work") }
func (e *OrgEnv) Tmp() string     { return filepath.Join(e.root, "tmp") }

// Ensure creates the directories with mode 0700.
func (e *OrgEnv) Ensure() error {
	for _, d := range []string{e.root, e.Home(), e.Agents(), e.WorkDir(), e.Tmp()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Vars is the exact child environment. Nothing is inherited, so CATHERD_ROLE, CODEX_*, CLAUDE_CODE_* and a parent
// CATHERD_CONFIG_DIR never reach catherd.
func (e *OrgEnv) Vars(rt *Runtime) []string {
	return append(e.SyncVars(rt), "CATHERD_NO_SYNC=1")
}

// VarsWith is Vars plus the given secrets (the only way a secret reaches a catherd process).
func (e *OrgEnv) VarsWith(rt *Runtime, s SecretEnv) []string { return append(e.Vars(rt), s.lines()...) }

// SyncVarsWith is SyncVars plus the given secrets.
func (e *OrgEnv) SyncVarsWith(rt *Runtime, s SecretEnv) []string {
	return append(e.SyncVars(rt), s.lines()...)
}

// SyncVars omits CATHERD_NO_SYNC; only `catalog sync` uses it.
func (e *OrgEnv) SyncVars(rt *Runtime) []string {
	return append(baseVars(rt, e.Tmp()), "CATHERD_HOME="+e.Home(), "CATHERD_CLAUDE_AGENTS_DIR="+e.Agents())
}

// PersonalEnv reads the user's own catherd state; it satisfies ReadEnv only, so no write method accepts it.
type PersonalEnv struct{ root string }

func NewPersonalEnv(dataDir string) *PersonalEnv {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		panic(err)
	}
	return &PersonalEnv{root: filepath.Join(abs, "catherd", "_personal")}
}
func (e *PersonalEnv) WorkDir() string { return filepath.Join(e.root, "work") }
func (e *PersonalEnv) Home() string    { return "personal" }
func (e *PersonalEnv) Ensure() error {
	for _, d := range []string{e.root, e.WorkDir(), filepath.Join(e.root, "tmp")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}
func (e *PersonalEnv) Vars(rt *Runtime) []string {
	v := baseVars(rt, filepath.Join(e.root, "tmp"))
	if rt.PersonalHome != "" {
		v = append(v, "CATHERD_HOME="+rt.PersonalHome)
	} else {
		for _, k := range []string{"CATHERD_HOME", "CATHERD_CONFIG_DIR", "CATHERD_DATA_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
			if x := rt.Getenv(k); x != "" {
				v = append(v, k+"="+x)
			}
		}
	}
	return append(v, "CATHERD_NO_SYNC=1")
}

// NeutralEnv runs commands that read no state (--version).
type NeutralEnv struct{ Dir string }

func (e NeutralEnv) WorkDir() string { return e.Dir }
func (e NeutralEnv) Home() string    { return "" }
func (e NeutralEnv) Vars(rt *Runtime) []string {
	return append(baseVars(rt, e.Dir), "CATHERD_NO_SYNC=1")
}
