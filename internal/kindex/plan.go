package kindex

import "sort"

// Entry is one outbox entry as the planner sees it.
type Entry struct {
	Seq         int64
	Op          string // upsert | delete
	VersionID   string
	RecordID    string
	ContentHash string
}

const (
	OpUpsert = "upsert"
	OpDelete = "delete"
)

// Coalesce keeps the last operation per version (last write wins) in ascending order of that last entry's seq.
// upsert, delete, upsert becomes one upsert; upsert, delete becomes one delete.
func Coalesce(es []Entry) []Entry {
	last := map[string]Entry{}
	for _, e := range es {
		if prev, ok := last[e.VersionID]; !ok || e.Seq >= prev.Seq {
			last[e.VersionID] = e
		}
	}
	out := make([]Entry, 0, len(last))
	for _, e := range last {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Batches splits entries into batches of at most n.
func Batches(es []Entry, n int) [][]Entry {
	if n <= 0 {
		n = 200
	}
	var out [][]Entry
	for len(es) > n {
		out = append(out, es[:n])
		es = es[n:]
	}
	if len(es) > 0 {
		out = append(out, es)
	}
	return out
}

// MaxSeq is the highest seq of the entries (0 for none).
func MaxSeq(es []Entry) int64 {
	var m int64
	for _, e := range es {
		if e.Seq > m {
			m = e.Seq
		}
	}
	return m
}
