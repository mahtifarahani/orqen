package extensions

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
)

//go:embed catalog/*.json
var catalogFS embed.FS

var catalog = loadCatalog()

func loadCatalog() map[string]*Manifest {
	entries, err := catalogFS.ReadDir("catalog")
	if err != nil {
		panic(err)
	}
	out := map[string]*Manifest{}
	for _, e := range entries {
		raw, err := catalogFS.ReadFile("catalog/" + e.Name())
		if err != nil {
			panic(err)
		}
		var m Manifest
		if err = json.Unmarshal(raw, &m); err != nil {
			panic(fmt.Sprintf("extensions: %s: %v", e.Name(), err))
		}
		if err = m.Validate(); err != nil {
			panic(err)
		}
		if m.ID+".json" != e.Name() {
			panic("extensions: file name does not match id: " + e.Name())
		}
		out[m.ID] = &m
	}
	return out
}

// Get returns a catalog entry; the pointer must be treated as read-only.
func Get(id string) (*Manifest, bool) {
	m, ok := catalog[id]
	return m, ok
}

// All lists the catalog ordered by kind then id.
func All() []*Manifest {
	out := make([]*Manifest, 0, len(catalog))
	for _, m := range catalog {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out
}
