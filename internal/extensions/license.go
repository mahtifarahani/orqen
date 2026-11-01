package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// License acceptance bases (X9).
const (
	BasisNoncommercial = "noncommercial_use"
	BasisCommercial    = "commercial_license_obtained"
	BasisOther         = "other_compatible"
)

// Bases lists the accepted bases.
var Bases = []string{BasisNoncommercial, BasisCommercial, BasisOther}

// LicenseError carries a stable code the routes map to a problem.
type LicenseError struct{ Code, Detail string }

func (e *LicenseError) Error() string { return e.Code + ": " + e.Detail }

// AcceptInput is what the user submits to accept a gated license.
type AcceptInput struct {
	Basis         string
	Note          string
	ConfirmPhrase string
	NoticeSHA256  string // sha256 of the exact notice text the client showed
}

// Acceptance is the stored record. Revoking keeps the record and sets RevokedAt.
type Acceptance struct {
	LicenseID    string     `json:"license_id" bson:"license_id"`
	NoticeSHA256 string     `json:"notice_sha256" bson:"notice_sha256"`
	Basis        string     `json:"basis" bson:"basis"`
	Note         string     `json:"note" bson:"note"`
	AcceptedAt   time.Time  `json:"accepted_at" bson:"accepted_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty" bson:"revoked_at,omitempty"`
}

// NoticeSHA is the digest of a notice text; the client sends it and the server checks it against both locale texts.
func NoticeSHA(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// Accept validates an acceptance for a manifest. knownNotices are the digests of the notice in every shipped locale.
func Accept(m *Manifest, in AcceptInput, knownNotices []string, now time.Time) (Acceptance, error) {
	if !m.Gated() {
		return Acceptance{}, &LicenseError{"license_not_required", m.ID + " has no license gate"}
	}
	if in.ConfirmPhrase != m.ID {
		return Acceptance{}, &LicenseError{"confirm_phrase_mismatch", "type the extension id to confirm"}
	}
	known := false
	for _, k := range knownNotices {
		if k == in.NoticeSHA256 {
			known = true
		}
	}
	if !known {
		return Acceptance{}, &LicenseError{"license_notice_mismatch", "the notice shown is not the current notice"}
	}
	basisOK := false
	for _, b := range Bases {
		if b == in.Basis {
			basisOK = true
		}
	}
	note := strings.TrimSpace(in.Note)
	if !basisOK || note == "" || len([]rune(note)) > 2000 {
		return Acceptance{}, &LicenseError{"license_input_invalid", "basis must be one of the listed values and the note 1..2000 characters"}
	}
	return Acceptance{LicenseID: m.License.SPDX, NoticeSHA256: in.NoticeSHA256, Basis: in.Basis, Note: note, AcceptedAt: now.UTC()}, nil
}
