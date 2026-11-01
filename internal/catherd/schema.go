// Package catherd drives the catherd command line. It must not import internal/server.
package catherd

import (
	"regexp"
	"strings"
)

// The 1.5.0 profile vocabulary, mirrored from catherd's profile.ts.
var (
	Roles        = []string{"architect", "verifier", "worker", "reviewer", "ui-reviewer", "artist", "writer", "researcher"}
	Access       = []string{"read-only", "workspace-write", "full"}
	BillingKeys  = []string{"codex", "claude", "claude-code", "opencode-go", "opencode", "cursor", "grok", "antigravity"}
	BillingModes = []string{"chatgpt-plan", "claude-plan", "subscription", "metered"}
	HarnessKeys  = []string{"codex", "claude-code", "opencode", "cursor", "grok", "antigravity"}
	Notify       = []string{"milestone", "finish", "blocked"}
	Objectives   = []string{"cost", "speed"}
	JevUses      = []string{"auto", "off"}
	RungBackends = append(append([]string{}, HarnessKeys...), "claude")
	// TopKeys are the keys a profile patch may set, in the order Orqen applies them (roles last).
	TopKeys = []string{"objective", "jev", "billing", "harness", "budget", "timeouts", "preflight", "lock", "notify", "failover", "roles"}
)

const (
	// HostCodex and HostClaudeCode are the orchestration hosts Orqen offers; `auto` is never used.
	HostCodex, HostClaudeCode = "codex", "claude-code"
	// SupportedMajor and SupportedMinor name the supported window: >= 1.5.0, < 1.6.0.
	SupportedMajor, SupportedMinor = 1, 5
)

var (
	modelPattern  = regexp.MustCompile(`^[A-Za-z0-9][^\s#]*$`)
	effortPattern = regexp.MustCompile(`^[A-Za-z0-9][\w-]*$`)
	NamePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

// Hosts lists the valid orchestration hosts.
var Hosts = []string{HostCodex, HostClaudeCode}

// ValidHost reports whether h is an orchestration host Orqen passes to catherd.
func ValidHost(h string) bool { return has(Hosts, h) }

type Rung struct{ Backend, Model, Effort string }

func (r Rung) String() string { return r.Backend + ":" + r.Model + "#" + r.Effort }
func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ParseRung accepts <backend>:<model>#<effort> exactly as catherd does (first colon, last hash).
func ParseRung(s string) (Rung, bool) {
	colon, hash := strings.Index(s, ":"), strings.LastIndex(s, "#")
	if colon < 1 || hash <= colon+1 {
		return Rung{}, false
	}
	r := Rung{Backend: s[:colon], Model: s[colon+1 : hash], Effort: s[hash+1:]}
	if !modelPattern.MatchString(r.Model) || !effortPattern.MatchString(r.Effort) || !has(RungBackends, r.Backend) {
		return Rung{}, false
	}
	return r, true
}

// Enum returns the vocabulary the API publishes for the profile editor.
func Enum(name string) []string {
	switch name {
	case "roles":
		return Roles
	case "access":
		return Access
	case "billing_keys":
		return BillingKeys
	case "billing_modes":
		return BillingModes
	case "harness_keys":
		return HarnessKeys
	case "notify":
		return Notify
	}
	return nil
}
