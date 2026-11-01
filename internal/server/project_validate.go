package server

import (
	"fmt"
	"github.com/bmatcuk/doublestar/v4"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type EnvSource struct {
	Kind  string  `bson:"kind" json:"kind"`
	Value *string `bson:"value" json:"value"`
	Var   *string `bson:"var" json:"var"`
}
type EnvVar struct {
	Name   string    `bson:"name" json:"name"`
	Source EnvSource `bson:"source" json:"source"`
}
type CommandOrigin struct {
	Kind       string  `bson:"kind" json:"kind"`
	SnapshotID *string `bson:"snapshot_id" json:"snapshot_id"`
	File       *string `bson:"file" json:"file"`
}
type CommandSpec struct {
	Key          string        `bson:"key" json:"key"`
	Kind         string        `bson:"kind" json:"kind"`
	Argv         []string      `bson:"argv" json:"argv"`
	Cwd          string        `bson:"cwd" json:"cwd"`
	Env          []EnvVar      `bson:"env" json:"env"`
	TimeoutSecs  int           `bson:"timeout_secs" json:"timeout_secs"`
	NeedsNetwork bool          `bson:"needs_network" json:"needs_network"`
	NeedsDocker  bool          `bson:"needs_docker" json:"needs_docker"`
	Origin       CommandOrigin `bson:"origin" json:"origin"`
	Approved     bool          `bson:"approved" json:"approved"`
	ApprovedAt   *time.Time    `bson:"approved_at" json:"approved_at"`
	Fingerprint  string        `bson:"fingerprint" json:"fingerprint"`
}
type ProtectedPath struct {
	Pattern string  `bson:"pattern" json:"pattern"`
	Mode    string  `bson:"mode" json:"mode"`
	Note    *string `bson:"note" json:"note"`
}

type EnvSourceInput struct {
	Kind  string  `json:"kind"`
	Value *string `json:"value"`
	Var   *string `json:"var"`
}
type EnvVarInput struct {
	Name   string         `json:"name"`
	Source EnvSourceInput `json:"source"`
}
type CommandOriginInput struct {
	Kind       string  `json:"kind"`
	SnapshotID *string `json:"snapshot_id"`
	File       *string `json:"file"`
}
type CommandInput struct {
	Key          string              `json:"key"`
	Kind         string              `json:"kind"`
	Argv         []string            `json:"argv"`
	Cwd          *string             `json:"cwd"`
	Env          []EnvVarInput       `json:"env"`
	TimeoutSecs  *int                `json:"timeout_secs"`
	NeedsNetwork bool                `json:"needs_network"`
	NeedsDocker  bool                `json:"needs_docker"`
	Origin       *CommandOriginInput `json:"origin"`
	Approved     *bool               `json:"approved"`
}
type PutCommandsRequest struct {
	Commands []CommandInput `json:"commands"`
}
type ProtectedPathInput struct {
	Pattern string  `json:"pattern"`
	Mode    string  `json:"mode"`
	Note    *string `json:"note"`
}
type PutProtectedPathsRequest struct {
	Rules []ProtectedPathInput `json:"rules"`
}

var (
	commandKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	envNamePattern    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	branchChars       = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)
)

// validBranchName accepts the git ref-name subset Orqen supports.
func validBranchName(s string) bool {
	return branchChars.MatchString(s) && !strings.HasPrefix(s, "/") && !strings.HasSuffix(s, "/") && !strings.Contains(s, "..") && !strings.Contains(s, "//") && !strings.HasSuffix(s, ".lock")
}

// normalizeRelPath cleans a repository-relative directory. "" and "." normalize to "".
func normalizeRelPath(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", true
	}
	if len(s) > 255 || strings.HasPrefix(s, "/") || strings.ContainsAny(s, "\\\x00") || !utf8.ValidString(s) {
		return "", false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return "", false
		}
	}
	c := path.Clean(s)
	if c == "." {
		return "", true
	}
	return c, true
}
func commandFingerprint(c CommandSpec) string {
	env := c.Env
	if env == nil {
		env = []EnvVar{}
	}
	return sha256Hex(canonicalJSON(map[string]any{"argv": c.Argv, "cwd": c.Cwd, "env": env, "timeout_secs": c.TimeoutSecs, "needs_network": c.NeedsNetwork, "needs_docker": c.NeedsDocker}))
}

const maxCommands = 50

// validateCommands checks D45 and applies the approval rule: approval survives only if the fingerprint is unchanged
// or the request asks for it explicitly. defaultCwd is the primary repository's package path.
func validateCommands(in []CommandInput, existing []CommandSpec, defaultCwd string, now time.Time) ([]CommandSpec, []FieldError) {
	var es []FieldError
	add := func(field, msg string) { es = append(es, FieldError{field, msg}) }
	if len(in) > maxCommands {
		add("commands", fmt.Sprintf("at most %d commands", maxCommands))
		return nil, es
	}
	old := map[string]CommandSpec{}
	for _, c := range existing {
		old[c.Key] = c
	}
	out := make([]CommandSpec, 0, len(in))
	seen := map[string]bool{}
	for i, c := range in {
		f := func(name string) string { return fmt.Sprintf("commands[%d].%s", i, name) }
		if !commandKeyPattern.MatchString(c.Key) {
			add(f("key"), "must match [a-z][a-z0-9-]{0,31}")
		} else if seen[c.Key] {
			add(f("key"), "already used")
		}
		seen[c.Key] = true
		if !contains([]string{"setup", "lint", "typecheck", "test", "build", "custom"}, c.Kind) {
			add(f("kind"), "must be setup, lint, typecheck, test, build or custom")
		}
		if len(c.Argv) < 1 || len(c.Argv) > 64 {
			add(f("argv"), "must have 1 to 64 items")
		}
		for j, a := range c.Argv {
			if a == "" || len(a) > 4096 || strings.ContainsRune(a, 0) || !utf8.ValidString(a) {
				add(fmt.Sprintf("commands[%d].argv[%d]", i, j), "must be 1 to 4096 bytes of text without NUL")
			}
		}
		cwd := defaultCwd
		if c.Cwd != nil {
			var ok bool
			if cwd, ok = normalizeRelPath(*c.Cwd); !ok {
				add(f("cwd"), "must be a relative directory without ..")
			}
		}
		if cwd == "" {
			cwd = "."
		}
		timeout := 600
		if c.TimeoutSecs != nil {
			timeout = *c.TimeoutSecs
		}
		if timeout < 1 || timeout > 7200 {
			add(f("timeout_secs"), "must be between 1 and 7200")
		}
		env := []EnvVar{}
		names := map[string]bool{}
		for j, e := range c.Env {
			ef := fmt.Sprintf("commands[%d].env[%d]", i, j)
			if !envNamePattern.MatchString(e.Name) || len(e.Name) > 128 {
				add(ef+".name", "must match [A-Z_][A-Z0-9_]*")
			} else if names[e.Name] {
				add(ef+".name", "already used")
			}
			names[e.Name] = true
			switch e.Source.Kind {
			case "literal":
				if e.Source.Value == nil || len(*e.Source.Value) > 1024 || strings.ContainsRune(*e.Source.Value, 0) {
					add(ef+".source.value", "must be at most 1024 bytes")
				}
				if sensitiveKey(e.Name) {
					add(ef+".source", "a secret-named variable must come from the host environment, not a literal")
				}
			case "host":
				if e.Source.Var == nil || !envNamePattern.MatchString(*e.Source.Var) {
					add(ef+".source.var", "must be a host variable name")
				}
			default:
				add(ef+".source.kind", "must be literal or host")
			}
			env = append(env, EnvVar{Name: e.Name, Source: EnvSource{Kind: e.Source.Kind, Value: e.Source.Value, Var: e.Source.Var}})
		}
		origin := CommandOrigin{Kind: "user"}
		if c.Origin != nil {
			switch c.Origin.Kind {
			case "user":
			case "contract":
				if c.Origin.SnapshotID == nil || !validID("rcs", *c.Origin.SnapshotID) || c.Origin.File == nil || len(*c.Origin.File) > 255 {
					add(f("origin"), "a contract origin needs snapshot_id and file")
				} else {
					origin = CommandOrigin{Kind: "contract", SnapshotID: c.Origin.SnapshotID, File: c.Origin.File}
				}
			default:
				add(f("origin.kind"), "must be user or contract")
			}
		}
		spec := CommandSpec{Key: c.Key, Kind: c.Kind, Argv: append([]string{}, c.Argv...), Cwd: cwd, Env: env, TimeoutSecs: timeout, NeedsNetwork: c.NeedsNetwork, NeedsDocker: c.NeedsDocker, Origin: origin}
		spec.Fingerprint = commandFingerprint(spec)
		prev, existed := old[c.Key]
		switch {
		case c.Approved != nil && *c.Approved:
			spec.Approved, spec.ApprovedAt = true, ptr(now)
			if existed && prev.Approved && prev.Fingerprint == spec.Fingerprint {
				spec.ApprovedAt = prev.ApprovedAt
			}
		case c.Approved == nil && existed && prev.Approved && prev.Fingerprint == spec.Fingerprint:
			spec.Approved, spec.ApprovedAt = true, prev.ApprovedAt
		}
		out = append(out, spec)
	}
	if len(es) > 0 {
		return nil, es
	}
	return out, nil
}

const maxProtectedPaths = 200

func validateProtectedPaths(in []ProtectedPathInput) ([]ProtectedPath, []FieldError) {
	var es []FieldError
	if len(in) > maxProtectedPaths {
		return nil, []FieldError{{"rules", fmt.Sprintf("at most %d rules", maxProtectedPaths)}}
	}
	out := make([]ProtectedPath, 0, len(in))
	for i, r := range in {
		f := func(name string) string { return fmt.Sprintf("rules[%d].%s", i, name) }
		p := strings.TrimSpace(r.Pattern)
		switch {
		case p == "" || len(p) > 512 || !utf8.ValidString(p) || strings.ContainsRune(p, 0):
			es = append(es, FieldError{f("pattern"), "must be 1 to 512 characters"})
		case strings.HasPrefix(p, "/"):
			es = append(es, FieldError{f("pattern"), "must be relative to the repository root"})
		case !doublestar.ValidatePattern(p):
			es = append(es, FieldError{f("pattern"), "is not a valid glob"})
		default:
			for _, seg := range strings.Split(p, "/") {
				if seg == ".." {
					es = append(es, FieldError{f("pattern"), "must not contain .."})
					break
				}
			}
		}
		if !contains([]string{"deny", "review"}, r.Mode) {
			es = append(es, FieldError{f("mode"), "must be deny or review"})
		}
		if r.Note != nil && utf8.RuneCountInString(*r.Note) > 500 {
			es = append(es, FieldError{f("note"), "must be at most 500 characters"})
		}
		out = append(out, ProtectedPath{Pattern: p, Mode: r.Mode, Note: r.Note})
	}
	if len(es) > 0 {
		return nil, es
	}
	return out, nil
}
