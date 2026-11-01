// Package extensions is the pure core of the Orqen extension catalog (SPEC §5, §12): the manifest model, the embedded
// catalog, the state machine, the license gate, the Permit that every extension exec requires, and the sandboxed
// runner. It imports nothing from internal/server. Orqen never installs, downloads or updates an extension: it only
// stats, hashes and (after an explicit user action) executes an absolute path the user registered.
package extensions

import (
	"fmt"
	"regexp"
	"strings"

	"orqen/internal/semver"
)

// Kinds of catalog entries.
const (
	KindKnowledgeIndex = "knowledge_index"
	KindCodeGraph      = "code_graph"
	KindSecurityScan   = "security_scan"
	KindMCPConnector   = "mcp_connector"
	KindDesign         = "design"
)

// Integration says whether Orqen drives the extension itself or only reports what is installed.
const (
	Integrated = "integrated"
	DetectOnly = "detect_only"
)

// Transports.
const (
	TransportCLI       = "cli"
	TransportMCPStdio  = "mcp_stdio"
	TransportRemoteMCP = "remote_mcp"
)

// License gates.
const (
	GateNone               = "none"
	GateAcceptanceRequired = "acceptance_required"
)

// Manifest is one catalog entry. The SPEC §5 fields come first, the Orqen fields after.
type Manifest struct {
	ID        string `json:"id"`
	Version   string `json:"version"` // manifest version, not the tool version
	Source    string `json:"source"`  // upstream URL; display text only, never fetched
	Integrity struct {
		Mode string `json:"mode"` // user_pinned: the sha256 is recorded at Detect
	} `json:"integrity"`
	License struct {
		SPDX      string `json:"spdx"`
		Gate      string `json:"gate"`
		NoticeKey string `json:"notice_key"`
	} `json:"license"`
	Transport string `json:"transport"`
	Runtime   struct {
		Kind       string `json:"kind"`
		MinVersion string `json:"min_version"`
	} `json:"runtime"`
	ConfigSchema     map[string]any    `json:"config_schema"`
	CredentialFields []CredentialField `json:"credential_fields"`
	Capabilities     []string          `json:"capabilities"`
	ReadTools        []string          `json:"read_tools"`
	WriteTools       []string          `json:"write_tools"` // always empty in Sprint 6
	Healthcheck      struct {
		Args []string `json:"args"`
	} `json:"healthcheck"`
	SupportedHosts []string `json:"supported_hosts"`

	Kind         string          `json:"kind"`
	Integration  string          `json:"integration"`
	InstallGuide InstallGuidance `json:"install_guidance"`
	Detect       struct {
		Args []string `json:"args"`
	} `json:"detect"`
	Requires struct {
		Docker bool `json:"docker"`
	} `json:"requires"`
}

// CredentialField names a secret the extension needs; the value lives in the credential store and reaches the child
// only through the named environment variable.
type CredentialField struct {
	Field    string `json:"field"`
	Env      string `json:"env"`
	LabelKey string `json:"label_key"`
	Required bool   `json:"required"`
}

// InstallGuidance is display-only: Orqen never runs these commands.
type InstallGuidance struct {
	TextKey  string   `json:"text_key"`
	Commands []string `json:"commands"`
}

// Gated reports whether the entry needs a license acceptance before anything but stat is allowed.
func (m *Manifest) Gated() bool { return m.License.Gate == GateAcceptanceRequired }

// MinVersion parses runtime.min_version; the zero version when unset.
func (m *Manifest) MinVersion() semver.Version {
	v, _ := semver.Find(m.Runtime.MinVersion)
	return v
}

var (
	idPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,39}$`)
	envPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
)

var (
	kinds        = []string{KindKnowledgeIndex, KindCodeGraph, KindSecurityScan, KindMCPConnector, KindDesign}
	transports   = []string{TransportCLI, TransportMCPStdio, TransportRemoteMCP}
	runtimeKinds = []string{"native", "node", "python", "docker", "remote"}
)

func oneOf(v string, set []string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// Validate checks a manifest. The catalog panics at init when a built-in entry is invalid.
func (m *Manifest) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("extension manifest %q: %s", m.ID, fmt.Sprintf(format, a...))
	}
	if !idPattern.MatchString(m.ID) {
		return bad("invalid id")
	}
	if _, ok := semver.Find(m.Version); !ok {
		return bad("version is not a version")
	}
	if m.Source == "" || !strings.HasPrefix(m.Source, "https://") {
		return bad("source must be an https URL (display only)")
	}
	if m.Integrity.Mode != "user_pinned" {
		return bad("integrity.mode must be user_pinned")
	}
	if m.License.SPDX == "" || m.License.NoticeKey == "" {
		return bad("license.spdx and license.notice_key are required")
	}
	if m.License.Gate != GateNone && m.License.Gate != GateAcceptanceRequired {
		return bad("license.gate is %q", m.License.Gate)
	}
	if !oneOf(m.Transport, transports) {
		return bad("transport is %q", m.Transport)
	}
	if !oneOf(m.Runtime.Kind, runtimeKinds) {
		return bad("runtime.kind is %q", m.Runtime.Kind)
	}
	if m.Runtime.MinVersion != "" {
		if _, ok := semver.Find(m.Runtime.MinVersion); !ok {
			return bad("runtime.min_version is not a version")
		}
	}
	if !oneOf(m.Kind, kinds) {
		return bad("kind is %q", m.Kind)
	}
	if m.Integration != Integrated && m.Integration != DetectOnly {
		return bad("integration is %q", m.Integration)
	}
	if len(m.WriteTools) != 0 {
		return bad("write_tools must be empty in this version")
	}
	if len(m.Healthcheck.Args) == 0 || len(m.Detect.Args) == 0 {
		return bad("detect.args and healthcheck.args are required")
	}
	if m.InstallGuide.TextKey == "" {
		return bad("install_guidance.text_key is required")
	}
	seen := map[string]bool{}
	for _, c := range m.CredentialFields {
		if c.Field == "" || !envPattern.MatchString(c.Env) || c.LabelKey == "" || seen[c.Field] {
			return bad("invalid credential field %q", c.Field)
		}
		seen[c.Field] = true
	}
	if m.ConfigSchema != nil {
		if err := CheckSchema(m.ConfigSchema); err != nil {
			return bad("config_schema: %v", err)
		}
	}
	return nil
}
