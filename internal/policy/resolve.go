package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"orqen/internal/canon"
)

// Version is a published layer: an immutable set of overrides with its identity.
type Version struct {
	ID        string
	Number    int64
	Overrides map[string]any
}
type Source struct {
	Layer     string
	VersionID string
	Number    int64
}
type Entry struct {
	Key     string
	Value   any
	Source  Source
	Default any
	Locked  bool
}
type Effective struct {
	Entries []Entry
	Hash    string
}

// Resolve applies product defaults, then the org version, then the project version, per key.
// Locked keys always keep the product value; org-only keys ignore the project layer.
func Resolve(org, project *Version) Effective {
	out := Effective{Entries: make([]Entry, 0, len(Registry))}
	values := map[string]any{}
	for _, k := range Registry {
		e := Entry{Key: k.Key, Value: k.Default, Default: k.Default, Locked: k.Locked, Source: Source{Layer: LayerProduct, Number: ProductDefaultsVersion}}
		if !k.Locked {
			if org != nil {
				if v, ok := org.Overrides[k.Key]; ok {
					e.Value, e.Source = v, Source{LayerOrg, org.ID, org.Number}
				}
			}
			if k.ProjectOverridable && project != nil {
				if v, ok := project.Overrides[k.Key]; ok {
					e.Value, e.Source = v, Source{LayerProject, project.ID, project.Number}
				}
			}
		}
		values[k.Key] = e.Value
		out.Entries = append(out.Entries, e)
	}
	sum := sha256.Sum256([]byte(canon.JSON(values)))
	out.Hash = hex.EncodeToString(sum[:])
	return out
}
