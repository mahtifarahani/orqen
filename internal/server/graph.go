package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"orqen/internal/contextpkt"
	"orqen/internal/orggraph"
	"orqen/internal/providers"
)

type GraphNodeDoc struct {
	ID         string `bson:"_id"`
	OrgID      string `bson:"org_id"`
	Kind       string `bson:"kind"`
	NaturalKey string `bson:"natural_key"`
	Label      string `bson:"label"`
}

type GraphEdgeDoc struct {
	ID            string              `bson:"_id"`
	OrgID         string              `bson:"org_id"`
	From          string              `bson:"from"`
	To            string              `bson:"to"`
	Relation      string              `bson:"relation"`
	Status        string              `bson:"status"`
	Confidence    float64             `bson:"confidence"`
	Evidence      []orggraph.Evidence `bson:"evidence"`
	CommitSHA     string              `bson:"commit_sha"`
	SourceID      string              `bson:"source_id"`
	ObservedAt    time.Time           `bson:"observed_at"`
	StaleSince    time.Time           `bson:"stale_since"`
	LastSeenBuild int                 `bson:"last_seen_build"`
	CreatedBy     string              `bson:"created_by"`
	Revision      int64               `bson:"revision"`
}

func (d GraphEdgeDoc) edge() orggraph.Edge {
	return orggraph.Edge{From: d.From, To: d.To, Relation: orggraph.Relation(d.Relation), Status: orggraph.Status(d.Status), Confidence: d.Confidence, Evidence: d.Evidence,
		CommitSHA: d.CommitSHA, SourceID: d.SourceID, ObservedAt: d.ObservedAt, StaleSince: d.StaleSince, LastSeenBuild: d.LastSeenBuild, CreatedBy: d.CreatedBy}
}

func edgeDTO(id string, e orggraph.Edge, revision int64) bson.M {
	ev := []bson.M{}
	for _, x := range e.Evidence {
		ev = append(ev, bson.M{"kind": x.Kind, "path": x.Path, "commit_sha": x.CommitSHA, "excerpt": x.Excerpt, "source_version_id": optStr(x.SourceVersionID)})
	}
	return bson.M{"id": id, "from": e.From, "to": e.To, "relation": string(e.Relation), "status": string(e.Status), "confidence": e.Confidence, "evidence": ev,
		"commit_sha": optStr(e.CommitSHA), "created_by": e.CreatedBy, "observed_at": timestamp(e.ObservedAt), "revision": revision}
}

func (d GraphEdgeDoc) DTO() bson.M { return edgeDTO(d.ID, d.edge(), d.Revision) }
func (d GraphNodeDoc) DTO() bson.M {
	return bson.M{"id": d.NaturalKey, "kind": d.Kind, "label": d.Label}
}

// RepoFiles reads the manifests of one repository at its default branch head (injectable for tests).
type repoReader func(ctx context.Context, conn ConnectionDoc, pid, branch string) (head string, files map[string]string, err error)

const maxManifestBytes = 256 << 10

func (a *App) defaultRepoReader(ctx context.Context, conn ConnectionDoc, pid, branch string) (string, map[string]string, error) {
	host, err := a.codeHost(ctx, conn)
	if err != nil {
		return "", nil, err
	}
	head, err := host.ResolveCommit(ctx, pid, branch)
	if err != nil {
		return "", nil, err
	}
	files := map[string]string{}
	paths := append(append(append([]string{}, orggraph.ManifestFiles...), orggraph.OpenAPIPaths...), orggraph.CodeownersPaths...)
	for _, p := range paths {
		// ReadFileRaw answers (bytes, truncated, err): a missing file is a not_found error, an oversize file is skipped.
		b, truncated, e := host.ReadFileRaw(ctx, pid, head, p, maxManifestBytes)
		if pe, ok := providers.AsError(e); ok && pe.Kind == providers.KindNotFound {
			continue
		}
		if e != nil {
			return "", nil, e
		}
		if !truncated {
			files[p] = string(b)
		}
	}
	return head, files, nil
}

// buildGraph derives the org graph from stored configuration, repository manifests and Confluence links, merges it
// with the stored edges (freshness, rejected and verified edges kept) and upserts the result.
func (a *App) buildGraph(ctx context.Context, org string, read repoReader) (int, error) {
	projects, err := findMany[ProjectDoc](ctx, a.DB, "projects", bson.M{"org_id": org, "active": true}, bson.D{{Key: "_id", Value: 1}}, 500)
	if err != nil {
		return 0, err
	}
	prevN, _ := a.DB.Coll("graph_edges").CountDocuments(ctx, bson.M{"org_id": org})
	_ = prevN
	build := 1
	if m, e := findOne[bson.M](ctx, a.DB, "graph_meta", bson.M{"_id": org}); e == nil {
		if n, ok := m["build"].(int32); ok {
			build = int(n) + 1
		} else if n, ok := m["build"].(int64); ok {
			build = int(n) + 1
		}
	}
	in := orggraph.BuildIn{Build: build, Now: time.Now().UTC()}
	seenRepo := map[string]bool{}
	heads := map[string]string{}
	failed := map[string]bool{}
	for _, p := range projects {
		pi := orggraph.ProjectIn{ID: p.ID, Name: p.Name}
		for _, r := range p.Mapping.Repos {
			key := r.ConnectionID + ":" + r.ProviderProjectID
			pi.RepoKeys = append(pi.RepoKeys, key)
			if r.Role == "primary" {
				pi.PrimaryRepo = key
			}
			if seenRepo[key] {
				continue
			}
			seenRepo[key] = true
			ri := orggraph.RepoIn{Key: key, Label: r.PathSnapshot}
			if conn, e := a.connection(ctx, org, r.ConnectionID); e != nil {
				ri.Missing = true
			} else if head, files, e := read(ctx, conn, r.ProviderProjectID, r.TargetBranch); e != nil {
				ri.Missing = true
			} else {
				ri.Head, ri.Files = head, files
				heads[orggraph.NodeID(orggraph.KindRepo, key)] = head
			}
			if ri.Missing {
				failed[orggraph.NodeID(orggraph.KindRepo, key)] = true
			}
			in.Repos = append(in.Repos, ri)
		}
		if j := p.Mapping.Jira; j != nil && j.ProjectKeySnapshot != "" {
			pi.JiraProjects = append(pi.JiraProjects, j.ProjectKeySnapshot)
		}
		for _, c := range p.Commands {
			if c.Approved {
				pi.Commands = append(pi.Commands, c.Key)
			}
		}
		in.Projects = append(in.Projects, pi)
	}
	// Confluence documents: the latest source versions that carry links.
	docs, err := findMany[SourceVersionDoc](ctx, a.DB, "source_versions", bson.M{"org_id": org, "kind": "confluence_page", "latest": true}, bson.D{{Key: "_id", Value: 1}}, 500)
	if err != nil {
		return 0, err
	}
	for _, d := range docs {
		in.Docs = append(in.Docs, orggraph.DocIn{Key: d.ConnectionID + ":" + d.ExternalID, Label: d.Title, SourceVersionID: d.ID, Links: d.Links})
		heads[orggraph.NodeID(orggraph.KindDoc, d.ConnectionID+":"+d.ExternalID)] = d.ID
	}
	nodes, fresh := orggraph.Derive(in)
	prevDocs, err := findMany[GraphEdgeDoc](ctx, a.DB, "graph_edges", bson.M{"org_id": org}, bson.D{{Key: "_id", Value: 1}}, 100000)
	if err != nil {
		return 0, err
	}
	prev := make([]orggraph.Edge, 0, len(prevDocs))
	ids := map[string]GraphEdgeDoc{}
	for _, d := range prevDocs {
		prev = append(prev, d.edge())
		ids[d.edge().Key()] = d
	}
	merged, removed := orggraph.Merge(prev, fresh, heads, failed, in.Now, a.graphRetentionDays(ctx, org))
	err = a.DB.Txn(ctx, func(sc context.Context) error {
		for _, n := range nodes {
			_, e := a.DB.Coll("graph_nodes").UpdateOne(sc, bson.M{"org_id": org, "kind": string(n.Kind), "natural_key": n.ID()},
				bson.M{"$set": bson.M{"label": n.Label}, "$setOnInsert": bson.M{"_id": newID("gno")}}, upsertOpt())
			if e != nil {
				return e
			}
		}
		for _, k := range removed {
			if d, ok := ids[k]; ok {
				if _, e := a.DB.Coll("graph_edges").DeleteOne(sc, bson.M{"_id": d.ID}); e != nil {
					return e
				}
			}
		}
		for _, e := range merged {
			doc := GraphEdgeDoc{ID: newID("ged"), OrgID: org, From: e.From, To: e.To, Relation: string(e.Relation), Status: string(e.Status), Confidence: e.Confidence, Evidence: e.Evidence,
				CommitSHA: e.CommitSHA, SourceID: e.SourceID, ObservedAt: e.ObservedAt, StaleSince: e.StaleSince, LastSeenBuild: e.LastSeenBuild, CreatedBy: e.CreatedBy, Revision: 1}
			if old, ok := ids[e.Key()]; ok {
				doc.ID, doc.Revision = old.ID, old.Revision
			}
			if _, e2 := a.DB.Coll("graph_edges").ReplaceOne(sc, bson.M{"_id": doc.ID}, doc, upsertReplace()); e2 != nil {
				return e2
			}
		}
		_, e := a.DB.Coll("graph_meta").UpdateOne(sc, bson.M{"_id": org}, bson.M{"$set": bson.M{"build": build, "built_at": in.Now}}, upsertOpt())
		return e
	})
	if err == nil {
		a.Committed()
	}
	return len(merged), err
}

func (a *App) runGraphBuild(ctx context.Context, job JobDoc, lease Lease) error {
	if _, err := a.buildGraph(ctx, job.OrgID, a.defaultRepoReader); err != nil {
		return err
	}
	return a.fenced(ctx, lease, func(sc context.Context) error { return a.completeJob(sc, lease) })
}

func (a *App) startGraphRebuild(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	if err := a.txnRetryJob(r.Context(), func(sc context.Context) error {
		return a.ensureJobTx(sc, org, "graph_build", "graph:"+org, 5, bson.M{})
	}); err != nil {
		return err
	}
	a.Committed()
	writeJSON(w, 202, bson.M{"queued": true})
	return nil
}

func (a *App) listGraphNodes(w http.ResponseWriter, r *http.Request) error {
	f := bson.M{"org_id": r.PathValue("orgId")}
	if k := r.URL.Query().Get("kind"); k != "" {
		f["kind"] = k
	}
	docs, err := findMany[GraphNodeDoc](r.Context(), a.DB, "graph_nodes", f, bson.D{{Key: "natural_key", Value: 1}}, 1000)
	if err != nil {
		return err
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	items := []bson.M{}
	for _, d := range docs {
		if q == "" || strings.Contains(strings.ToLower(d.Label), q) || strings.Contains(strings.ToLower(d.NaturalKey), q) {
			items = append(items, d.DTO())
		}
	}
	writeJSON(w, 200, bson.M{"items": items, "next_cursor": nil})
	return nil
}

func (a *App) graphNeighborhood(w http.ResponseWriter, r *http.Request) error {
	org, q := r.PathValue("orgId"), r.URL.Query()
	start := q.Get("node")
	if start == "" {
		return validation("node", "is required")
	}
	if n, _ := a.DB.Coll("graph_nodes").CountDocuments(r.Context(), bson.M{"org_id": org, "natural_key": start}); n == 0 {
		return problem("not_found")
	}
	depth, err := queryInt(r, "depth", 1)
	if err != nil {
		return err
	}
	limit, err := queryInt(r, "limit", 100)
	if err != nil {
		return err
	}
	f := orggraph.Filter{}
	for _, rel := range q["relation"] {
		f.Relations = append(f.Relations, orggraph.Relation(rel))
	}
	for _, s := range q["status"] {
		f.Statuses = append(f.Statuses, orggraph.Status(s))
	}
	docs, err := findMany[GraphEdgeDoc](r.Context(), a.DB, "graph_edges", bson.M{"org_id": org}, bson.D{{Key: "_id", Value: 1}}, 100000)
	if err != nil {
		return err
	}
	edges := make([]orggraph.Edge, 0, len(docs))
	byKey := map[string]GraphEdgeDoc{}
	for _, d := range docs {
		edges = append(edges, d.edge())
		byKey[d.edge().Key()] = d
	}
	sub := orggraph.Neighborhood(edges, start, int(depth), f, int(limit))
	nodeDocs, err := findMany[GraphNodeDoc](r.Context(), a.DB, "graph_nodes", bson.M{"org_id": org, "natural_key": bson.M{"$in": sub.Nodes}}, bson.D{{Key: "natural_key", Value: 1}}, 1000)
	if err != nil {
		return err
	}
	nodes := []bson.M{}
	for _, d := range nodeDocs {
		nodes = append(nodes, d.DTO())
	}
	out := []bson.M{}
	for _, e := range sub.Edges {
		d := byKey[e.Key()]
		out = append(out, d.DTO())
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["id"].(string) < out[j]["id"].(string) })
	writeJSON(w, 200, bson.M{"start": start, "depth": sub.Depth, "truncated": sub.Truncated, "nodes": nodes, "edges": out})
	return nil
}

type CreateGraphEdgeRequest struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
	Note     string `json:"note"`
}

func (a *App) createGraphEdge(w http.ResponseWriter, r *http.Request) error {
	var req CreateGraphEdgeRequest
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	if err := orggraph.ValidateUserEdge(req.From, req.To, orggraph.Relation(req.Relation)); err != nil {
		return &AppError{Code: "graph_edge_invalid", Status: 422, Detail: err.Error(), Errors: []FieldError{{"relation", err.Error()}}}
	}
	for _, id := range []string{req.From, req.To} {
		if n, _ := a.DB.Coll("graph_nodes").CountDocuments(r.Context(), bson.M{"org_id": org, "natural_key": id}); n == 0 {
			return &AppError{Code: "graph_edge_invalid", Status: 422, Detail: "unknown node " + id, Errors: []FieldError{{"from", "both nodes must exist in this organization"}}}
		}
	}
	now := time.Now().UTC()
	doc := GraphEdgeDoc{ID: newID("ged"), OrgID: org, From: req.From, To: req.To, Relation: req.Relation, Status: "verified", Confidence: 1,
		Evidence: []orggraph.Evidence{{Kind: "user", Excerpt: strings.TrimSpace(req.Note)}}, ObservedAt: now, CreatedBy: "user", Revision: 1}
	if _, err := a.DB.Coll("graph_edges").InsertOne(r.Context(), doc); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return &AppError{Code: "graph_edge_invalid", Status: 409, Detail: "the edge already exists"}
		}
		return err
	}
	a.Committed()
	w.Header().Set("ETag", etag(doc.Revision))
	writeJSON(w, 201, doc.DTO())
	return nil
}

func (a *App) setEdgeStatus(status string) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		expected, err := ifMatch(r)
		if err != nil {
			return err
		}
		org, id := r.PathValue("orgId"), r.PathValue("gedId")
		set := bson.M{"status": status}
		if status == "verified" {
			set["evidence"] = nil
		}
		cur, err := findOne[GraphEdgeDoc](r.Context(), a.DB, "graph_edges", bson.M{"_id": id, "org_id": org})
		if err != nil {
			return err
		}
		if status == "verified" {
			set["evidence"] = append(cur.Evidence, orggraph.Evidence{Kind: "user", Excerpt: "confirmed by a person"})
			set["confidence"] = 1.0
		} else {
			delete(set, "evidence")
		}
		doc, err := revisionUpdate[GraphEdgeDoc](r.Context(), a.DB, "graph_edges", bson.M{"_id": id, "org_id": org}, expected, set)
		if err != nil {
			return err
		}
		a.Committed()
		w.Header().Set("ETag", etag(doc.Revision))
		writeJSON(w, 200, doc.DTO())
		return nil
	}
}

func upsertReplace() *options.ReplaceOptionsBuilder { return options.Replace().SetUpsert(true) }

// graphNeighbors is the graph_neighbors code_context item (K33): depth 1 around the primary repo, at most 10 edges
// and 1 KiB, each edge tagged with its status. It returns nil when the graph has nothing for the repo.
func (a *App) graphNeighbors(ctx context.Context, run RunDoc, p *RepoMapping) *contextpkt.Item {
	if p == nil {
		return nil
	}
	start := orggraph.NodeID(orggraph.KindRepo, p.ConnectionID+":"+p.ProviderProjectID)
	docs, err := findMany[GraphEdgeDoc](ctx, a.DB, "graph_edges", bson.M{"org_id": run.OrgID, "status": bson.M{"$ne": "rejected"}, "$or": []bson.M{{"from": start}, {"to": start}}}, bson.D{{Key: "_id", Value: 1}}, 10)
	if err != nil || len(docs) == 0 {
		return nil
	}
	var lines []string
	for _, d := range docs {
		lines = append(lines, fmt.Sprintf("%s -[%s]-> %s (%s)", d.From, d.Relation, d.To, d.Status))
	}
	text := strings.Join(lines, "\n")
	if len(text) > 1024 {
		text = text[:1024]
	}
	return &contextpkt.Item{SourceID: "graph_neighbors", Kind: "graph_neighbors", Label: "graph neighbors", Ref: start, Text: text, OrgID: run.OrgID}
}
