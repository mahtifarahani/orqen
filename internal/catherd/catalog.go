package catherd

import (
	"encoding/json"
	"strings"
)

type TreatLike struct {
	Like   string `json:"like"`
	Source string `json:"source"`
}
type CatalogRung struct {
	Rung      string                     `json:"rung"`
	Enabled   bool                       `json:"enabled"`
	Scores    map[string]json.RawMessage `json:"scores"`
	TreatLike *TreatLike                 `json:"treatLike"`
	Cost      *struct {
		Tier  *float64 `json:"tier"`
		Value *float64 `json:"value"`
		Mode  string   `json:"mode"`
	} `json:"cost"`
}
type CatalogModel struct {
	ID           string          `json:"id"`
	Name         *string         `json:"name"`
	Backend      string          `json:"backend"`
	Model        string          `json:"model"`
	Billing      string          `json:"billing"`
	Efforts      []string        `json:"efforts"`
	Context      *int64          `json:"context"`
	Roles        []string        `json:"roles"`
	Listed       *bool           `json:"listed"`
	Price        json.RawMessage `json:"price"`
	Rungs        []CatalogRung   `json:"rungs"`
	Capabilities json.RawMessage `json:"capabilities"`
}
type Catalog struct {
	Total  int            `json:"total"`
	Models []CatalogModel `json:"models"`
}

func ParseCatalog(b []byte) (Catalog, error) {
	var c Catalog
	err := json.Unmarshal(b, &c)
	return c, err
}

// Lookup finds the model and rung for a rung string like codex:gpt-6-sol#high.
func (c Catalog) Lookup(rung string) (CatalogModel, CatalogRung, bool) {
	r, ok := ParseRung(rung)
	if !ok {
		return CatalogModel{}, CatalogRung{}, false
	}
	for _, m := range c.Models {
		if m.Backend != r.Backend || m.Model != r.Model {
			continue
		}
		for _, cr := range m.Rungs {
			if cr.Rung == rung {
				return m, cr, true
			}
		}
	}
	return CatalogModel{}, CatalogRung{}, false
}

// Family maps a rung to its catalog family id, shared across backends.
func (c Catalog) Family(rung string) (string, bool) {
	m, _, ok := c.Lookup(rung)
	return m.ID, ok
}

const (
	Available        = "available"
	UnknownListing   = "unknown_listing"
	NotOffered       = "not_offered"
	NoScore          = "no_score"
	BackendMissing   = "backend_missing"
	RoleIncompatible = "role_incompatible"
)

// Availability implements D57. backends maps a backend to its doctor state ("fail", "skip:missing", ...).
// role may be empty. Everything except available and unknown_listing is shown disabled with its reason.
func Availability(m CatalogModel, r CatalogRung, role string, backends map[string]string) string {
	switch {
	case role != "" && !has(m.Roles, role):
		return RoleIncompatible
	case strings.HasPrefix(backends[m.Backend], "fail") || backends[m.Backend] == "skip:missing":
		return BackendMissing
	case m.Listed != nil && !*m.Listed:
		return NotOffered
	case !r.Enabled && r.TreatLike == nil:
		return NoScore
	case m.Listed == nil:
		return UnknownListing
	}
	return Available
}

// InferredOnly is true when every score of the rung is inferred (reference-project shows such a rung as unscored).
func (r CatalogRung) InferredOnly() bool {
	if len(r.Scores) == 0 {
		return false
	}
	for _, raw := range r.Scores {
		var s struct {
			Confidence string `json:"confidence"`
		}
		if json.Unmarshal(raw, &s) != nil || s.Confidence != "inferred" {
			return false
		}
	}
	return true
}
