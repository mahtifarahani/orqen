package extensions

// Reasons an extension cannot be used (the i18n catalog has one key per entry).
const (
	ReasonNotRegistered     = "not_registered"
	ReasonLicenseRequired   = "license_acceptance_required"
	ReasonLicenseRevoked    = "license_revoked"
	ReasonNotDetected       = "not_detected"
	ReasonNotTested         = "not_tested"
	ReasonDisabled          = "disabled"
	ReasonBinaryChanged     = "binary_changed"
	ReasonCredentialMissing = "credential_missing"
	ReasonConfigInvalid     = "config_invalid"
	ReasonProjectNotBound   = "project_not_bound"
)

// Reasons lists every Usable reason.
var Reasons = []string{ReasonNotRegistered, ReasonLicenseRequired, ReasonLicenseRevoked, ReasonNotDetected, ReasonNotTested,
	ReasonDisabled, ReasonBinaryChanged, ReasonCredentialMissing, ReasonConfigInvalid, ReasonProjectNotBound}

// Snapshot is the stored state of one extension in one org, reduced to what Usable needs.
type Snapshot struct {
	Status      Status
	Path        string
	Version     string // detected tool version
	SHA256      string // detected binary digest
	License     *Acceptance
	Config      map[string]any
	Credentials map[string]bool // credential field -> set

	// ProjectScoped asks for a project binding; ProjectBound says whether the project has one that is enabled.
	ProjectScoped bool
	ProjectBound  bool
}

// Permit is the capability to execute one extension binary. Its fields are unexported, so only Usable can mint it;
// codegraph.NewCLI, secscan.NewCLI and the wiki CLI factory require one.
type Permit struct {
	ext     string
	path    string
	version string
	sha256  string
}

// Extension is the catalog id the permit was issued for.
func (p Permit) Extension() string { return p.ext }

// Path is the absolute binary path.
func (p Permit) Path() string { return p.path }

// Version is the tool version recorded at Detect.
func (p Permit) Version() string { return p.version }

// SHA256 is the digest recorded at Detect.
func (p Permit) SHA256() string { return p.sha256 }

// Valid reports whether the permit was minted (the zero Permit is never valid).
func (p Permit) Valid() bool { return p.ext != "" && p.path != "" }

// Usable is the only way to mint a Permit. It returns the first reason the extension cannot be used.
func Usable(m *Manifest, s Snapshot) (Permit, string) {
	switch s.Status {
	case StatusAbsent, "":
		return Permit{}, ReasonNotRegistered
	}
	if m.Gated() {
		if s.License == nil || s.Status == StatusLicenseRequired {
			return Permit{}, ReasonLicenseRequired
		}
		if s.License.RevokedAt != nil {
			return Permit{}, ReasonLicenseRevoked
		}
	}
	switch s.Status {
	case StatusRegistered, StatusDetectFailed, StatusLicenseRequired:
		return Permit{}, ReasonNotDetected
	case StatusChanged:
		return Permit{}, ReasonBinaryChanged
	case StatusDetected, StatusTestFailed:
		return Permit{}, ReasonNotTested
	case StatusTested, StatusDisabled:
		return Permit{}, ReasonDisabled
	case StatusEnabled:
	default:
		return Permit{}, ReasonNotRegistered
	}
	if m.Integration == DetectOnly {
		return Permit{}, ReasonDisabled
	}
	for _, c := range m.CredentialFields {
		if c.Required && !s.Credentials[c.Field] {
			return Permit{}, ReasonCredentialMissing
		}
	}
	if len(ValidateConfig(m.ConfigSchema, s.Config)) > 0 {
		return Permit{}, ReasonConfigInvalid
	}
	if s.ProjectScoped && !s.ProjectBound {
		return Permit{}, ReasonProjectNotBound
	}
	if s.Path == "" || s.SHA256 == "" {
		return Permit{}, ReasonNotDetected
	}
	return Permit{ext: m.ID, path: s.Path, version: s.Version, sha256: s.SHA256}, ""
}

// Probe mints a Permit for the Detect, Test and Health steps only: they run the extension's own version and
// healthcheck verbs before the entry is enabled. It requires a registered path and, for a gated entry, an accepted and
// not revoked license; it never returns a permit for an entry that has no license yet (only stat is allowed then).
// The permit it returns is for the exact path and digest the caller hashed just before the exec.
func Probe(m *Manifest, s Snapshot, sha string) (Permit, string) {
	switch s.Status {
	case StatusAbsent, "":
		return Permit{}, ReasonNotRegistered
	}
	if m.Gated() {
		if s.License == nil || s.Status == StatusLicenseRequired {
			return Permit{}, ReasonLicenseRequired
		}
		if s.License.RevokedAt != nil {
			return Permit{}, ReasonLicenseRevoked
		}
	}
	if s.Path == "" || sha == "" {
		return Permit{}, ReasonNotRegistered
	}
	return Permit{ext: m.ID, path: s.Path, version: s.Version, sha256: sha}, ""
}
