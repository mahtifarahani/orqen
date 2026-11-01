package server

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/contextpkt"
)

var confluencePageID = regexp.MustCompile(`/pages/(\d+)|[?&]pageId=(\d+)`)

func samePageHost(a, b string) bool {
	ua, e1 := url.Parse(a)
	ub, e2 := url.Parse(b)
	return e1 == nil && e2 == nil && ua.Host != "" && strings.EqualFold(ua.Host, ub.Host)
}

func pageItem(sv SourceVersionDoc, text, ref string) contextpkt.Item {
	return contextpkt.Item{SourceID: "ref:" + ref, Kind: "confluence", Label: sv.Title, Ref: ref, Text: text, External: true, OrgID: sv.OrgID, ConnectionID: sv.ConnectionID}
}

// mappedDocItems lists the stored latest versions of the pages in the project mapping (no provider call).
func (a *App) mappedDocItems(ctx context.Context, org string, proj ProjectDoc) []contextpkt.Item {
	var out []contextpkt.Item
	for _, ref := range proj.Mapping.Confluence {
		if ref.PageID == nil {
			continue
		}
		sv, err := findOne[SourceVersionDoc](ctx, a.DB, "source_versions", bson.M{"org_id": org, "kind": "confluence_page", "connection_id": ref.ConnectionID, "external_id": *ref.PageID, "latest": true})
		if err != nil {
			continue
		}
		text, _ := a.artifactText(ctx, org, sv.ArtifactID, maxSourceText)
		out = append(out, pageItem(sv, text, "page:"+sv.ExternalID))
	}
	return out
}

// linkedPages resolves the Confluence links of the issue: a link on the host of a mapped connection is fetched (GET)
// and stored as a source version; a link on any other host is out of scope and is never requested.
func (a *App) linkedPages(ctx context.Context, org string, proj ProjectDoc, links []string) map[string]contextpkt.Item {
	out := map[string]contextpkt.Item{}
	for _, u := range links {
		it := contextpkt.Item{SourceID: "ref:" + u, Kind: "confluence", Label: u, Ref: u, Excluded: contextpkt.ReasonOutOfScope}
		for _, ref := range proj.Mapping.Confluence {
			conn, err := a.connection(ctx, org, ref.ConnectionID)
			if err != nil || !samePageHost(conn.BaseURL, u) {
				continue
			}
			m := confluencePageID.FindStringSubmatch(u)
			id := ""
			if m != nil {
				id = m[1] + m[2]
			}
			if id == "" {
				it.Excluded = contextpkt.ReasonAdapterUnavailable
				break
			}
			src, err := a.docsSource(ctx, conn)
			if err != nil {
				it.Excluded = contextpkt.ReasonAdapterUnavailable
				break
			}
			page, err := src.GetPage(ctx, id)
			if err != nil {
				it.Excluded = contextpkt.ReasonAdapterUnavailable
				break
			}
			pid := proj.ID
			sv, _, err := a.ingestSource(ctx, pageSource(org, conn.ID, &pid, page))
			if err != nil {
				it.Excluded = contextpkt.ReasonAdapterUnavailable
				break
			}
			text, _ := a.artifactText(ctx, org, sv.ArtifactID, maxSourceText)
			it = pageItem(sv, text, u)
			break
		}
		out[u] = it
	}
	return out
}
