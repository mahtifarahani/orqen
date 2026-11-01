package orggraph

import "sort"

// Filter selects edges for a neighborhood. Empty Relations means all; empty Statuses means verified and inferred.
type Filter struct {
	Relations []Relation
	Statuses  []Status
}

func (f Filter) match(e Edge) bool {
	st := f.Statuses
	if len(st) == 0 {
		st = []Status{StatusVerified, StatusInferred}
	}
	okS := false
	for _, s := range st {
		okS = okS || s == e.Status
	}
	if !okS {
		return false
	}
	if len(f.Relations) == 0 {
		return true
	}
	for _, r := range f.Relations {
		if r == e.Relation {
			return true
		}
	}
	return false
}

type Subgraph struct {
	Nodes     []string // node ids, start first, then by discovery
	Edges     []Edge
	Truncated bool
	Depth     int
}

// MaxNeighborhoodEdges is the hard limit of a neighborhood.
const MaxNeighborhoodEdges = 200

// Neighborhood walks edges in both directions from start, breadth first, up to depth (1..2) hops and limit edges
// (at most 200). Output is deterministic; Truncated says the limit cut the result.
func Neighborhood(edges []Edge, start string, depth int, f Filter, limit int) Subgraph {
	if depth < 1 {
		depth = 1
	}
	if depth > 2 {
		depth = 2
	}
	if limit <= 0 || limit > MaxNeighborhoodEdges {
		limit = MaxNeighborhoodEdges
	}
	adj := map[string][]Edge{}
	for _, e := range edges {
		if f.match(e) {
			adj[e.From] = append(adj[e.From], e)
			adj[e.To] = append(adj[e.To], e)
		}
	}
	for k := range adj {
		sort.Slice(adj[k], func(i, j int) bool { return adj[k][i].Key() < adj[k][j].Key() })
	}
	out := Subgraph{Nodes: []string{start}, Depth: depth}
	seenN := map[string]bool{start: true}
	seenE := map[string]bool{}
	frontier := []string{start}
	for d := 0; d < depth && len(frontier) > 0; d++ {
		var next []string
		for _, n := range frontier {
			for _, e := range adj[n] {
				if seenE[e.Key()] {
					continue
				}
				if len(out.Edges) >= limit {
					out.Truncated = true
					return out
				}
				seenE[e.Key()] = true
				out.Edges = append(out.Edges, e)
				other := e.To
				if other == n {
					other = e.From
				}
				if !seenN[other] {
					seenN[other] = true
					out.Nodes = append(out.Nodes, other)
					next = append(next, other)
				}
			}
		}
		frontier = next
	}
	return out
}
