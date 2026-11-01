package orggraph

import "time"

// Merge combines the stored edges with a fresh derivation (K32 freshness). heads maps a source node id (a repo or a
// doc) to its current head; failed lists source nodes that could not be read this build (their edges are left alone).
//
//   - a fresh edge replaces an inferred or stale one (status inferred again);
//   - a rejected edge stays rejected and a verified edge stays verified (only its observation is refreshed);
//   - an inferred edge that was not re-observed while its source head changed becomes stale;
//   - a stale edge older than retention is removed; verified and rejected edges are never auto-removed.
//
// It returns the merged edges and the keys of the removed ones.
func Merge(prev, fresh []Edge, heads map[string]string, failed map[string]bool, now time.Time, retentionDays int) (merged []Edge, removed []string) {
	freshByKey := map[string]Edge{}
	for _, e := range fresh {
		freshByKey[e.Key()] = e
	}
	seen := map[string]bool{}
	for _, p := range prev {
		seen[p.Key()] = true
		f, observed := freshByKey[p.Key()]
		switch {
		case observed && p.Status == StatusRejected:
			merged = append(merged, p)
		case observed && p.Status == StatusVerified:
			p.ObservedAt, p.LastSeenBuild, p.CommitSHA = f.ObservedAt, f.LastSeenBuild, f.CommitSHA
			merged = append(merged, p)
		case observed:
			f.CreatedBy = p.CreatedBy
			merged = append(merged, f)
		case p.Status == StatusVerified || p.Status == StatusRejected || p.CreatedBy == "user":
			merged = append(merged, p)
		case p.SourceID == "" || failed[p.SourceID]:
			merged = append(merged, p)
		default:
			if head, ok := heads[p.SourceID]; ok && head != p.CommitSHA && p.Status == StatusInferred {
				p.Status, p.StaleSince = StatusStale, now
			}
			if p.Status == StatusStale && retentionDays > 0 && !p.StaleSince.IsZero() && now.Sub(p.StaleSince) > time.Duration(retentionDays)*24*time.Hour {
				removed = append(removed, p.Key())
				continue
			}
			merged = append(merged, p)
		}
	}
	for _, f := range fresh {
		if !seen[f.Key()] {
			merged = append(merged, f)
		}
	}
	sortEdges(merged)
	return merged, removed
}
