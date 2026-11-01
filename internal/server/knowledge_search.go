package server

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type KnowledgeSearchResult struct {
	Mode     string         `json:"mode"`
	Degraded *string        `json:"degraded"`
	Items    []KnowledgeHit `json:"items"`
}

// searchVerified prefers the Wiki index only when it is configured, healthy and fully caught up; every hit is
// re-resolved against MongoDB (same org, still verified), so a foreign or stale hit is dropped (K22c).
func (a *App) searchVerified(ctx context.Context, org, q, typ, project string, limit int) (any, error) {
	st, err := a.wikiStatusDTO(ctx, org)
	if err != nil {
		return nil, err
	}
	reason := "wiki_not_configured"
	idx := a.indexFor(ctx, org)
	if idx != nil {
		reason = "wiki_degraded"
		if st["pending"].(int64) > 0 {
			reason = "wiki_catching_up"
		}
	}
	if idx != nil && st["mode"] == "wiki" {
		hits, err := idx.Search(ctx, a.vaultFor(org), q, limit)
		if err == nil {
			items := []KnowledgeHit{}
			for _, h := range hits {
				v, e := a.knowledgeVersion(ctx, org, h.ID)
				if e != nil || v.Status != "verified" || (typ != "" && v.Type != typ) || (project != "" && v.ProjectID != project) {
					if e != nil {
						_ = a.AppendEvent(ctx, org, "knowledge.scope_violation", Subject{"organization", org}, bson.M{"hit": h.ID}, nil, nil, true)
						a.Committed()
					}
					continue
				}
				items = append(items, KnowledgeHit{VersionID: v.ID, RecordID: v.RecordID, Title: v.Title, Type: v.Type, ScopeKey: v.ScopeKey, Score: h.Score, Snippet: h.Snippet})
			}
			return KnowledgeSearchResult{Mode: "wiki", Items: items}, nil
		}
		reason = "wiki_error"
	}
	hits, err := a.mongoSearch(ctx, org, q, typ, project, limit)
	if err != nil {
		return nil, err
	}
	return KnowledgeSearchResult{Mode: "fallback", Degraded: &reason, Items: hits}, nil
}
