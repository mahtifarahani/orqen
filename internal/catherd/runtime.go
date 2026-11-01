package catherd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"orqen/internal/semver"
)

type RuntimeConfig struct {
	Bin, Path    string // ORQEN_CATHERD_BIN, ORQEN_CATHERD_PATH
	PersonalHome string // ORQEN_PERSONAL_CATHERD_HOME (tests)
	DataDir      string
	UserHome     string
	Getenv       func(string) string
	// LoginShellPath overrides the login-shell PATH probe (tests).
	LoginShellPath func(context.Context) string
}

// Runtime is how catherd is found and the environment it runs in.
type Runtime struct {
	Bin          string
	ChildPath    string
	PathSource   string
	UserHome     string
	DataDir      string
	PersonalHome string
	Getenv       func(string) string
	mu           sync.RWMutex
	version      string
}

var knownDirs = func(home string) []string {
	return []string{filepath.Join(home, ".bun", "bin"), filepath.Join(home, ".local", "bin"), filepath.Join(home, ".opencode", "bin"), "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"}
}

var loginMarker = regexp.MustCompile(`(?s)__ORQEN_PATH__(.*)$`)

func loginShellPath(ctx context.Context, getenv func(string) string) string {
	shell := getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, shell, "-ilc", `printf "__ORQEN_PATH__%s" "$PATH"`).Output()
	if err != nil {
		return ""
	}
	m := loginMarker.FindStringSubmatch(string(out))
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// MergePath joins PATH lists in order without duplicates.
func MergePath(parts ...string) string {
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		for _, d := range strings.Split(p, string(os.PathListSeparator)) {
			if d != "" && !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	return strings.Join(out, string(os.PathListSeparator))
}
func NewRuntime(ctx context.Context, cfg RuntimeConfig) *Runtime {
	if cfg.Getenv == nil {
		cfg.Getenv = os.Getenv
	}
	if cfg.UserHome == "" {
		cfg.UserHome, _ = os.UserHomeDir()
	}
	rt := &Runtime{UserHome: cfg.UserHome, DataDir: cfg.DataDir, PersonalHome: cfg.PersonalHome, Getenv: cfg.Getenv}
	if cfg.Path != "" {
		rt.ChildPath, rt.PathSource = cfg.Path, "explicit"
	} else {
		login := ""
		if cfg.LoginShellPath != nil {
			login = cfg.LoginShellPath(ctx)
		} else {
			login = loginShellPath(ctx, cfg.Getenv)
		}
		rt.ChildPath = MergePath(login, cfg.Getenv("PATH"), strings.Join(knownDirs(cfg.UserHome), string(os.PathListSeparator)))
		rt.PathSource = "process+known"
		if login != "" {
			rt.PathSource = "login_shell+process+known"
		}
	}
	if cfg.Bin != "" {
		rt.Bin = cfg.Bin
	} else {
		for _, d := range strings.Split(rt.ChildPath, string(os.PathListSeparator)) {
			p := filepath.Join(d, "catherd")
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
				rt.Bin = p
				break
			}
		}
	}
	return rt
}
func (r *Runtime) Version() string { r.mu.RLock(); defer r.mu.RUnlock(); return r.version }
func (r *Runtime) SetVersion(v string) {
	r.mu.Lock()
	r.version = v
	r.mu.Unlock()
}
func (r *Runtime) Available() bool { return r.Bin != "" && r.Version() != "" }

// Supported reports the 1.5.x window (D61).
func (r *Runtime) Supported() bool {
	v, ok := semver.Find(r.Version())
	return ok && v.InWindow(semver.Version{Major: SupportedMajor, Minor: SupportedMinor}, semver.Version{Major: SupportedMajor, Minor: SupportedMinor + 1})
}
