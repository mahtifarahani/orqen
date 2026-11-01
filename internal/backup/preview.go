package backup

import "sort"

// CredentialRef is a credential a backup refers to. Only the reference and where it is used are stored, never a value.
type CredentialRef struct {
	Ref     string `json:"ref"`
	Kind    string `json:"kind"`  // connection, publisher_credential, catherd_secret, extension
	Owner   string `json:"owner"` // id of the owning document
	OrgID   string `json:"org_id"`
	Present bool   `json:"present"` // in THIS install's credential store
}

// ProfileRef is a published model profile version a restore would need to republish.
type ProfileRef struct {
	OrgID   string `json:"org_id"`
	Name    string `json:"name"`
	Present bool   `json:"present"` // visible in the target catherd home
}

// PreviewInput is everything Preview needs; the caller gathers it read-only.
type PreviewInput struct {
	Name            string
	Manifest        Manifest
	Verify          []FileResult
	BuildSchema     int
	Target          Target
	Credentials     []CredentialRef
	NonTerminalRuns int
	Profiles        []ProfileRef
}

// PreviewDTO is the dry-run result (X35). It is read-only: nothing is written anywhere.
type PreviewDTO struct {
	Name               string           `json:"name"`
	Integrity          []FileResult     `json:"integrity"`
	IntegrityOK        bool             `json:"integrity_ok"`
	FirstFailedFile    string           `json:"first_failed_file,omitempty"`
	SchemaVersion      int              `json:"schema_version"`
	SchemaCompatible   bool             `json:"schema_compatible"`
	TargetEmpty        bool             `json:"target_empty"`
	TargetNonEmpty     []string         `json:"target_non_empty"`
	Counts             map[string]int64 `json:"counts"`
	Orgs               []Org            `json:"orgs"`
	MissingCredentials []CredentialRef  `json:"missing_credentials"`
	HeldRuns           int              `json:"held_runs"`
	RepublishProfiles  []ProfileRef     `json:"republish_profiles"`
	CanRestore         bool             `json:"can_restore"`
	Notes              []string         `json:"notes"`
}

// Note codes of the preview.
const (
	NoteTargetNotEmpty = "target_not_empty"
	NoteSchemaTooNew   = "schema_too_new"
	NoteIntegrity      = "integrity_failed"
)

// Preview builds the dry-run result from verified inputs. It is pure; CLI --dry-run and the API verify route call it
// with different inputs but the same logic.
func Preview(in PreviewInput) PreviewDTO {
	p := PreviewDTO{
		Name: in.Name, Integrity: in.Verify, IntegrityOK: AllOK(in.Verify), SchemaVersion: in.Manifest.SchemaVersion,
		SchemaCompatible: in.Manifest.SchemaVersion <= in.BuildSchema, Counts: map[string]int64{}, Orgs: in.Manifest.Orgs,
		TargetNonEmpty: in.Target.NonEmpty(), MissingCredentials: []CredentialRef{}, RepublishProfiles: []ProfileRef{},
		HeldRuns: in.NonTerminalRuns, Notes: []string{},
	}
	if f, bad := FirstFailure(in.Verify); bad {
		p.FirstFailedFile = f
		p.Notes = append(p.Notes, NoteIntegrity)
	}
	p.TargetEmpty = len(p.TargetNonEmpty) == 0
	for _, c := range in.Manifest.Collections {
		p.Counts[c.Name] = c.Docs
	}
	for _, c := range in.Credentials {
		if !c.Present {
			p.MissingCredentials = append(p.MissingCredentials, c)
		}
	}
	sort.Slice(p.MissingCredentials, func(i, j int) bool {
		a, b := p.MissingCredentials[i], p.MissingCredentials[j]
		if a.OrgID != b.OrgID {
			return a.OrgID < b.OrgID
		}
		return a.Ref < b.Ref
	})
	for _, pr := range in.Profiles {
		if !pr.Present {
			p.RepublishProfiles = append(p.RepublishProfiles, pr)
		}
	}
	if !p.SchemaCompatible {
		p.Notes = append(p.Notes, NoteSchemaTooNew)
	}
	if !p.TargetEmpty {
		p.Notes = append(p.Notes, NoteTargetNotEmpty)
	}
	p.CanRestore = p.IntegrityOK && p.SchemaCompatible && p.TargetEmpty
	return p
}
