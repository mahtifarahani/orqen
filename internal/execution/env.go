package execution

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"orqen/internal/catherd"
)

// HostConfig describes the app-server of one organization.
type HostConfig struct {
	CodexBin   string
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
	for _, d := range []string{HostHome(dataDir, orgID), CodexHome(dataDir, orgID)} {
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

// HostEnvNames lists every variable BuildHostEnv sets (the secrets come on top, by their own names).
var HostEnvNames = []string{"HOME", "CODEX_HOME", "CATHERD_HOME", "CATHERD_CLAUDE_AGENTS_DIR", "CATHERD_ORCHESTRATION_HOST", "PATH", "LANG", "TMPDIR",
	"GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL", "GIT_SSH_COMMAND", "GIT_TERMINAL_PROMPT"}

// BuildHostEnv is the complete environment of the app-server and everything under it. Nothing is inherited:
// no SSH agent, no CODEX_* or CLAUDE_* of Orqen's own process, no proxy and no provider credential.
func BuildHostEnv(rt *catherd.Runtime, org *catherd.OrgEnv, dataDir string, sec catherd.SecretEnv) ([]string, error) {
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
		"CODEX_HOME=" + CodexHome(dataDir, orgID),
		"CATHERD_HOME=" + org.Home(),
		"CATHERD_CLAUDE_AGENTS_DIR=" + org.Agents(),
		"CATHERD_ORCHESTRATION_HOST=codex",
		"PATH=" + rt.ChildPath,
		"LANG=" + lang,
		"TMPDIR=" + org.Tmp(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + GitConfigPath(dataDir, orgID),
		"GIT_SSH_COMMAND=false",
		"GIT_TERMINAL_PROMPT=0",
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

func tomlString(s string) string {
	b, _ := json.Marshal(s) // a JSON string is a valid TOML basic string
	return string(b)
}

// HostArgs are the app-server flags: stdio, strict config, no approvals, a read-only sandbox for the orchestrator
// thread, and the catherd MCP server with its own explicit environment.
func HostArgs(catherdBin string, org *catherd.OrgEnv, cfg HostConfig) []string {
	orgID := filepath.Base(org.Root())
	dataDir := cfg.DataDir
	fixed := map[string]string{
		"CATHERD_HOME":               org.Home(),
		"CATHERD_CLAUDE_AGENTS_DIR":  org.Agents(),
		"CATHERD_ORCHESTRATION_HOST": "codex",
		"CATHERD_NO_SYNC":            "1",
		"CATHERD_LOG":                "warn",
	}
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
