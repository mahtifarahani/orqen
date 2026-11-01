package sysprobe

import (
	"context"
	"encoding/json"
	"strings"

	"orqen/internal/semver"
)

func runEnv(in Inputs) []string {
	return []string{"PATH=" + in.ChildPath, "HOME=" + in.UserHome, "LANG=C", "NO_COLOR=1"}
}

type probed struct {
	found   bool
	path    string
	version semver.Version
	hasVer  bool
	raw     string
}

func probeBinary(ctx context.Context, d Deps, in Inputs, name string, args ...string) probed {
	path := d.LookPath(in.ChildPath, name)
	if path == "" {
		return probed{}
	}
	out, exit, err := d.Runner.Run(ctx, append([]string{path}, args...), runEnv(in))
	if err != nil || exit != 0 {
		return probed{found: true, path: path, raw: strings.TrimSpace(out)}
	}
	v, ok := semver.Find(out)
	return probed{found: true, path: path, version: v, hasVer: ok, raw: strings.TrimSpace(out)}
}

type binarySpec struct {
	id, name string
	args     []string
	min      semver.Version
	required bool
	blocks   []string
	fix      []string
	fixCode  string
}

func (s binarySpec) check(p probed) Check {
	c := Check{ID: s.id, Group: "binaries", Required: s.required, Blocks: s.blocks, FixCommand: s.fix}
	bad := Warn
	if s.required {
		bad = Fail
	}
	switch {
	case !p.found:
		c.Status, c.FixCode, c.Detail = bad, s.fixCode+"_missing", s.name+" was not found on the PATH Orqen gives its tools"
	case !p.hasVer:
		c.Status, c.FixCode, c.Detail = bad, s.fixCode+"_unreadable", s.name+" did not report a version"
	case !p.version.AtLeast(s.min):
		c.Status, c.FixCode, c.Detail = bad, s.fixCode+"_too_old", s.name+" "+p.version.String()+" is older than "+s.min.String()
	default:
		c.Status, c.Detail = Pass, s.name+" "+p.version.String()
	}
	return c
}

func catherdCheck(_ context.Context, _ Deps, in Inputs) []Check {
	return []Check{timed(func() Check {
		c := Check{ID: "bin.catherd", Group: "binaries", Required: true, Blocks: []string{"profile_publish", "automation"}}
		switch {
		case in.CatherdBin == "" || in.CatherdVersion == "":
			c.Status, c.FixCode, c.Detail = Fail, "catherd_missing", "catherd was not found or did not report a version"
			c.FixCommand = []string{"bun add -g catherd-cli", "export ORQEN_CATHERD_BIN=/path/to/catherd"}
		case !in.CatherdSupported:
			c.Status, c.FixCode, c.Detail = Fail, "catherd_version_unsupported", "catherd "+in.CatherdVersion+" is outside the supported window 1.5.x"
		default:
			c.Status, c.Detail = Pass, "catherd "+in.CatherdVersion+" at "+in.CatherdBin
		}
		return c
	})}
}
func binaryChecks(ctx context.Context, d Deps, in Inputs) []Check {
	var out []Check
	specs := []binarySpec{
		{"bin.bun", "bun", []string{"--version"}, semver.Version{Major: 1, Minor: 4}, true, []string{"profile_publish"}, []string{"brew install oven-sh/bun/bun"}, "bun"},
		{"bin.codex", "codex", []string{"--version"}, semver.Version{Major: 0, Minor: 159, Patch: 2}, false, []string{"coding_runs"}, []string{"npm install -g @openai/codex"}, "codex"},
		{"bin.claude", "claude", []string{"--version"}, semver.Version{Major: 2, Minor: 1, Patch: 282}, in.NeedsClaude, []string{"claude_code_roles"}, []string{"npm install -g @anthropic-ai/claude-code"}, "claude"},
		{"bin.git", "git", []string{"--version"}, semver.Version{Major: 2, Minor: 30}, false, []string{"managed_workspaces"}, []string{"xcode-select --install"}, "git"},
		{"bin.node", "node", []string{"--version"}, semver.Version{Major: 22}, false, []string{"mcp_connectors"}, []string{"brew install node"}, "node"},
	}
	for _, s := range specs {
		out = append(out, timed(func() Check {
			p := probeBinary(ctx, d, in, s.name, s.args...)
			c := s.check(p)
			if c.Status != Pass {
				return c
			}
			switch s.name {
			case "codex":
				if _, exit, err := d.Runner.Run(ctx, []string{p.path, "login", "status"}, runEnv(in)); err != nil || exit != 0 {
					c.Status, c.FixCode, c.Detail = Warn, "codex_logged_out", c.Detail+"; not logged in"
					c.FixCommand = []string{"codex login"}
				}
			case "claude":
				raw, exit, err := d.Runner.Run(ctx, []string{p.path, "auth", "status", "--json"}, runEnv(in))
				var st struct {
					LoggedIn bool `json:"loggedIn"`
				}
				if err != nil || exit != 0 || json.Unmarshal([]byte(raw), &st) != nil || !st.LoggedIn {
					c.Status, c.FixCode, c.Detail = Warn, "claude_logged_out", c.Detail+"; not logged in"
					c.FixCommand = []string{"claude auth login"}
				}
			}
			return c
		}))
	}
	out = append(out, opencodeLogin(ctx, d, in))
	return out
}

// opencodeLogin only reads `opencode auth list --format json`: the exit code and how many providers are listed.
func opencodeLogin(ctx context.Context, d Deps, in Inputs) Check {
	return timed(func() Check {
		c := Check{ID: "bin.opencode_login", Group: "binaries", Blocks: []string{"opencode_roles"}}
		path := d.LookPath(in.ChildPath, "opencode")
		if path == "" {
			c.Status, c.Detail = Skipped, "opencode was not found on the PATH Orqen gives its tools"
			return c
		}
		raw, exit, err := d.Runner.Run(ctx, []string{path, "auth", "list", "--format", "json"}, runEnv(in))
		n, parsed := countProviders(raw)
		switch {
		case err != nil || exit != 0:
			c.Status, c.FixCode, c.Detail = Warn, "opencode_logged_out", "opencode could not list its logins"
			c.FixCommand = []string{"opencode auth login"}
		case parsed && n == 0:
			c.Status, c.FixCode, c.Detail = Warn, "opencode_logged_out", "opencode has no provider login"
			c.FixCommand = []string{"opencode auth login"}
		case parsed:
			c.Status, c.Detail = Pass, "opencode lists "+itoa(n)+" provider login(s)"
		default:
			c.Status, c.Detail = Pass, "opencode answered"
		}
		return c
	})
}

// countProviders counts the entries of a JSON array or object without keeping any content.
func countProviders(raw string) (int, bool) {
	var arr []json.RawMessage
	if json.Unmarshal([]byte(raw), &arr) == nil {
		return len(arr), true
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &obj) == nil {
		return len(obj), true
	}
	return 0, false
}
