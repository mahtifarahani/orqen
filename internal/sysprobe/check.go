// Package sysprobe runs detect-only system checks. It reads versions, dials hosts and writes probe files inside
// Orqen's own data directory; it never installs, loads, logs in or changes settings.
package sysprobe

import (
	"context"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	Pass    = "pass"
	Warn    = "warn"
	Fail    = "fail"
	Skipped = "skipped"
)

type Check struct {
	ID, Group  string
	Status     string
	Detail     string
	FixCode    string
	FixCommand []string // displayed for the user to run; never executed
	Required   bool
	Blocks     []string
	DurationMS int64
}

// Deps are the seams tests replace.
type Deps struct {
	Runner   Runner
	FS       FS
	Net      Network
	Getenv   func(string) string
	Keychain func() (status, detail string)
	Now      func() time.Time
	LookPath func(path, name string) string
}

// Inputs describe what to check; internal/server fills them from its configuration and documents.
type Inputs struct {
	Mongo            MongoState
	CredentialStore  string // keychain or memory
	CatherdBin       string
	CatherdVersion   string
	CatherdSupported bool
	NeedsClaude      bool // a bound profile has claude-code rungs
	DataDir          string
	Artifacts        string
	CatherdHome      string
	Workspaces       string
	Connections      []ConnTarget
	LaunchdOptIn     bool
	LaunchdPlist     string // absolute path of the user's plist, shown only
	UID              int
	UserHome         string
	ChildPath        string
	Keys             KeyInputs
}
type MongoState struct {
	OK      bool
	SetName string
	Members int
	Err     string
}

// Executed lists every subprocess argument vector the checks may run. A test asserts it holds no installing or
// state-changing verb.
var Executed = map[string][][]string{
	"bin.bun":    {{"bun", "--version"}},
	"bin.codex":  {{"codex", "--version"}, {"codex", "login", "status"}},
	"bin.claude": {{"claude", "--version"}, {"claude", "auth", "status", "--json"}},
	"bin.git":    {{"git", "--version"}},
	"bin.node":   {{"node", "--version"}},
	"docker":     {{"docker", "version", "--format", "{{.Server.Version}}"}, {"docker", "context", "show"}},
	"launchd":    {{"launchctl", "print"}},
	// Lists provider logins (read-only); only the exit code and the number of entries are used, never the output.
	"bin.opencode_login": {{"opencode", "auth", "list", "--format", "json"}},
}

func timed(f func() Check) Check {
	start := time.Now()
	c := f()
	c.DurationMS = time.Since(start).Milliseconds()
	if c.Blocks == nil {
		c.Blocks = []string{}
	}
	if c.FixCommand == nil {
		c.FixCommand = []string{}
	}
	return c
}

// Run executes every check, four at a time, and returns them sorted by group and id.
func Run(ctx context.Context, d Deps, in Inputs) []Check {
	if d.Runner == nil {
		d.Runner = ExecRunner{Dir: os.TempDir()}
	}
	if d.FS == nil {
		d.FS = RealFS{}
	}
	if d.Net == nil {
		d.Net = RealNetwork{}
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.LookPath == nil {
		d.LookPath = lookPath
	}
	if d.Getenv == nil {
		d.Getenv = func(string) string { return "" }
	}
	if d.Keychain == nil {
		d.Keychain = DefaultKeychain
	}
	groups := []func(context.Context, Deps, Inputs) []Check{
		func(_ context.Context, _ Deps, in Inputs) []Check { return []Check{mongoCheck(in)} },
		dockerChecks, keychainCheck, catherdCheck, catherdKeyChecks, binaryChecks, fsChecks, networkChecks, envCheck, launchdCheck,
	}
	var mu sync.Mutex
	var all []Check
	var g errgroup.Group
	g.SetLimit(4)
	for _, f := range groups {
		g.Go(func() error {
			cs := f(ctx, d, in)
			mu.Lock()
			all = append(all, cs...)
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	sort.Slice(all, func(i, j int) bool {
		if all[i].Group != all[j].Group {
			return all[i].Group < all[j].Group
		}
		return all[i].ID < all[j].ID
	})
	return all
}
func mongoCheck(in Inputs) Check {
	return timed(func() Check {
		c := Check{ID: "mongo.replica_set", Group: "database", Required: true, Blocks: []string{"everything"}}
		switch {
		case !in.Mongo.OK:
			c.Status, c.Detail, c.FixCode = Fail, "MongoDB is not reachable as a replica set: "+in.Mongo.Err, "mongo_not_replica_set"
			c.FixCommand = []string{"scripts/dev-mongo.sh start"}
		default:
			c.Status = Pass
			c.Detail = "replica set " + in.Mongo.SetName
			if in.Mongo.Members > 0 {
				c.Detail += ", members: " + itoa(in.Mongo.Members)
			}
		}
		return c
	})
}

func keychainCheck(_ context.Context, d Deps, in Inputs) []Check {
	return []Check{timed(func() Check {
		c := Check{ID: "keychain.access", Group: "credentials", Blocks: []string{"credential_persistence"}}
		if in.CredentialStore == "memory" {
			c.Status, c.FixCode, c.Detail = Warn, "credential_store_memory", "credentials are kept in memory and are lost when Orqen restarts"
			c.FixCommand = []string{"unset ORQEN_CREDENTIAL_STORE"}
			return c
		}
		c.Required = true
		status, detail := d.Keychain()
		c.Status, c.Detail = status, detail
		if status == Fail {
			c.FixCode = "credential_store_unavailable"
			c.FixCommand = []string{"xcode-select --install", "export ORQEN_CREDENTIAL_STORE=memory"}
		}
		return c
	})}
}
func envCheck(_ context.Context, d Deps, in Inputs) []Check {
	return []Check{timed(func() Check {
		c := Check{ID: "env.proxy", Group: "network", Status: Pass}
		var parts []string
		for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
			if v := d.Getenv(k); v != "" {
				parts = append(parts, k+"="+redactProxy(v))
			}
		}
		c.Detail = "no proxy variables set"
		if len(parts) > 0 {
			c.Detail = strings.Join(parts, ", ")
		}
		return c
	})}
}
