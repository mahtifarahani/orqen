package execution

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"orqen/internal/catherd"
)

// HostConfig describes the app-server of one organization.
type HostConfig struct {
	// Host is the orchestration host: catherd.HostCodex (default) or catherd.HostClaudeCode.
	Host       string
	CodexBin   string
	ClaudeBin  string
	CatherdBin string
	DataDir    string
	OrgID      string
	Org        *catherd.OrgEnv
	RT         *catherd.Runtime
	// Secrets are the K3 matrix secrets for host processes (TYPESAFE_API_KEY and the isolation keys of the
	// backends the pinned profile uses). Never a GitLab or Jira credential.
	Secrets catherd.SecretEnv
	Model   string
	Effort  string
}

// Paths of the isolated host tree.
func HostHome(dataDir, orgID string) string { return filepath.Join(dataDir, "host-home", orgID) }
func CodexHome(dataDir, orgID string) string {
	return filepath.Join(dataDir, "codex", orgID, "home")
}
func ClaudeConfigDir(dataDir, orgID string) string {
	return filepath.Join(dataDir, "claude", orgID, "config")
}
func GitConfigPath(dataDir, orgID string) string {
	return filepath.Join(HostHome(dataDir, orgID), "gitconfig")
}

// HostLockPath is the flock file guarding one organization's host.
func HostLockPath(dataDir, orgID string) string {
	return filepath.Join(CodexHome(dataDir, orgID), "orqen-host.lock")
}

const hostGitConfig = `# Written by Orqen. Any push a role attempts from the host tree fails: there is no credential helper and every
# remote URL a push could use is rewritten to a scheme git cannot run.
[credential]
	helper =
[url "orqen-blocked://"]
	pushInsteadOf = https://
	pushInsteadOf = http://
	pushInsteadOf = ssh://
	pushInsteadOf = git@
[core]
	hooksPath = /dev/null
`

// EnsureHostTree creates the isolated directories (0700) and the host gitconfig.
func EnsureHostTree(dataDir, orgID string) error {
	for _, d := range []string{HostHome(dataDir, orgID), CodexHome(dataDir, orgID), ClaudeConfigDir(dataDir, orgID)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	p := GitConfigPath(dataDir, orgID)
	if cur, err := os.ReadFile(p); err == nil && string(cur) == hostGitConfig {
		return nil
	}
	return os.WriteFile(p, []byte(hostGitConfig), 0o600)
}

// EnsureClaudeKeychain lets Claude Code find its login on macOS. It keeps the login in the user's keychain, and the
// keychain is found through HOME, which Orqen points at the isolated host home. A symlink to the user's keychain
// directory gives the Claude host the same login keychain and nothing else of the real home. Each organization still
// has its own login: Claude names the keychain item after its CLAUDE_CONFIG_DIR. Only the Claude host gets this link.
func EnsureClaudeKeychain(dataDir, orgID string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	real, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	src := filepath.Join(real, "Library", "Keychains")
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		return nil
	}
	lib := filepath.Join(HostHome(dataDir, orgID), "Library")
	if err := os.MkdirAll(lib, 0o700); err != nil {
		return err
	}
	dst := filepath.Join(lib, "Keychains")
	if cur, err := os.Readlink(dst); err == nil && cur == src {
		return nil
	}
	_ = os.Remove(dst)
	return os.Symlink(src, dst)
}

// HostEnvNames lists every variable BuildHostEnv sets (the secrets come on top, by their own names).
var HostEnvNames = []string{"HOME", "CODEX_HOME", "CATHERD_HOME", "CATHERD_CLAUDE_AGENTS_DIR", "CATHERD_ORCHESTRATION_HOST", "PATH", "LANG", "TMPDIR",
	"GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL", "GIT_SSH_COMMAND", "GIT_TERMINAL_PROMPT"}

// ClaudeHostEnvNames is HostEnvNames for the Claude Code host: CLAUDE_CONFIG_DIR instead of CODEX_HOME.
var ClaudeHostEnvNames = []string{"HOME", "CLAUDE_CONFIG_DIR", "USER", "LOGNAME", "CATHERD_HOME", "CATHERD_CLAUDE_AGENTS_DIR", "CATHERD_ORCHESTRATION_HOST", "PATH", "LANG", "TMPDIR",
	"GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL", "GIT_SSH_COMMAND", "GIT_TERMINAL_PROMPT"}

// BuildHostEnv is the complete environment of the app-server and everything under it. Nothing is inherited:
// no SSH agent, no CODEX_* or CLAUDE_* of Orqen's own process, no proxy and no provider credential.
func BuildHostEnv(rt *catherd.Runtime, org *catherd.OrgEnv, dataDir string, sec catherd.SecretEnv) ([]string, error) {
	return BuildHostEnvFor(catherd.HostCodex, rt, org, dataDir, sec)
}

// BuildHostEnvFor is BuildHostEnv for one orchestration host. The Codex host gets CODEX_HOME, the Claude Code host
// gets CLAUDE_CONFIG_DIR: each has its own isolated login per organization.
func BuildHostEnvFor(host string, rt *catherd.Runtime, org *catherd.OrgEnv, dataDir string, sec catherd.SecretEnv) ([]string, error) {
	orgID := filepath.Base(org.Root())
	if err := EnsureHostTree(dataDir, orgID); err != nil {
		return nil, err
	}
	if err := org.Ensure(); err != nil {
		return nil, err
	}
	lang := rt.Getenv("LANG")
	if lang == "" {
		lang = "en_US.UTF-8"
	}
	env := []string{
		"HOME=" + HostHome(dataDir, orgID),
		"CATHERD_HOME=" + org.Home(),
		"CATHERD_CLAUDE_AGENTS_DIR=" + org.Agents(),
		"CATHERD_ORCHESTRATION_HOST=" + hostName(host),
		"PATH=" + rt.ChildPath,
		"LANG=" + lang,
		"TMPDIR=" + org.Tmp(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + GitConfigPath(dataDir, orgID),
		"GIT_SSH_COMMAND=false",
		"GIT_TERMINAL_PROMPT=0",
	}
	if host == catherd.HostClaudeCode {
		env = append(env, "CLAUDE_CONFIG_DIR="+ClaudeConfigDir(dataDir, orgID))
		// The keychain is looked up under the account name; without USER and LOGNAME Claude does not see its login.
		if u := currentUser(); u != "" {
			env = append(env, "USER="+u, "LOGNAME="+u)
		}
	} else {
		env = append(env, "CODEX_HOME="+CodexHome(dataDir, orgID))
	}
	names := make([]string, 0, len(sec))
	for k := range sec {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		ok := false
		for _, a := range catherd.AllowedSecretEnv {
			ok = ok || a == k
		}
		if !ok {
			return nil, fmt.Errorf("execution: %s is not a secret the host may receive", k)
		}
		if sec[k] != "" {
			env = append(env, k+"="+sec[k])
		}
	}
	return env, nil
}

// currentUser is the account Orqen runs as.
func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}

// hostName is the value catherd takes in CATHERD_ORCHESTRATION_HOST.
func hostName(host string) string {
	if host == catherd.HostClaudeCode {
		return catherd.HostClaudeCode
	}
	return catherd.HostCodex
}

// CatherdMCPEnv is the explicit environment of the catherd MCP server a host starts (CATHERD_NO_SYNC and a quiet log
// are fixed: a host session must never trigger a catalog sync).
func CatherdMCPEnv(host string, org *catherd.OrgEnv) map[string]string {
	return map[string]string{
		"CATHERD_HOME":               org.Home(),
		"CATHERD_CLAUDE_AGENTS_DIR":  org.Agents(),
		"CATHERD_ORCHESTRATION_HOST": hostName(host),
		"CATHERD_NO_SYNC":            "1",
		"CATHERD_LOG":                "warn",
	}
}

func tomlString(s string) string {
	b, _ := json.Marshal(s) // a JSON string is a valid TOML basic string
	return string(b)
}

// HostArgs are the app-server flags: stdio, strict config, no approvals, a read-only sandbox for the orchestrator
// thread, and the catherd MCP server with its own explicit environment.
func HostArgs(catherdBin string, org *catherd.OrgEnv, cfg HostConfig) []string {
	orgID := filepath.Base(org.Root())
	dataDir := cfg.DataDir
	fixed := CatherdMCPEnv(catherd.HostCodex, org)
	keys := make([]string, 0, len(fixed))
	for k := range fixed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+tomlString(fixed[k]))
	}
	forward := []string{"PATH", "HOME", "TMPDIR", "LANG"}
	secretNames := make([]string, 0, len(cfg.Secrets))
	for k := range cfg.Secrets {
		secretNames = append(secretNames, k)
	}
	sort.Strings(secretNames)
	forward = append(forward, secretNames...)
	quoted := make([]string, len(forward))
	for i, n := range forward {
		quoted[i] = tomlString(n)
	}
	args := []string{"app-server", "--listen", "stdio://", "--strict-config",
		"-c", "shell_environment_policy.inherit=all",
		"-c", "shell_environment_policy.set.GIT_CONFIG_GLOBAL=" + tomlString(GitConfigPath(dataDir, orgID)),
		"-c", `approval_policy="never"`,
		"-c", `sandbox_mode="read-only"`,
		"-c", "mcp_servers.catherd.command=" + tomlString(catherdBin),
		"-c", `mcp_servers.catherd.args=["mcp"]`,
		"-c", "mcp_servers.catherd.env={" + strings.Join(pairs, ", ") + "}",
		"-c", "mcp_servers.catherd.env_vars=[" + strings.Join(quoted, ", ") + "]",
		"-c", "analytics.enabled=false",
	}
	return args
}
