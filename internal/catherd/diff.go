package catherd

import (
	"sort"
	"strings"
)

type Change struct {
	Path        []string
	PathDisplay string
	Kind        string // added, removed or changed
	Before      any
	After       any
}

// flatten maps every leaf to its path. Objects recurse; arrays, scalars and empty objects are leaves.
func flatten(v any, path []string, out map[string]leaf) {
	if m, ok := v.(map[string]any); ok && (len(m) > 0 || len(path) == 0) {
		for k, c := range m {
			flatten(c, append(append([]string{}, path...), k), out)
		}
		return
	}
	out[strings.Join(path, "\x00")] = leaf{path, v}
}

type leaf struct {
	path  []string
	value any
}

// Diff lists the leaves that differ between two profile documents (the name is ignored).
func Diff(a, b map[string]any) []Change {
	fa, fb := map[string]leaf{}, map[string]leaf{}
	flatten(patchableName(a), nil, fa)
	flatten(patchableName(b), nil, fb)
	var out []Change
	for k, l := range fa {
		if m, ok := fb[k]; !ok {
			out = append(out, Change{Path: l.path, PathDisplay: strings.Join(l.path, "."), Kind: "removed", Before: l.value})
		} else if !same(l.value, m.value) {
			out = append(out, Change{Path: l.path, PathDisplay: strings.Join(l.path, "."), Kind: "changed", Before: l.value, After: m.value})
		}
	}
	for k, l := range fb {
		if _, ok := fa[k]; !ok {
			out = append(out, Change{Path: l.path, PathDisplay: strings.Join(l.path, "."), Kind: "added", After: l.value})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PathDisplay < out[j].PathDisplay })
	return out
}
func patchableName(doc map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range doc {
		if k != "name" {
			out[k] = v
		}
	}
	return out
}
