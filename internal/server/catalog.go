package server

import (
	"context"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type CatalogProjectDoc struct {
	ID                string    `bson:"_id" json:"_id"`
	OrgID             string    `bson:"org_id" json:"org_id"`
	ConnectionID      string    `bson:"connection_id" json:"connection_id"`
	ProviderProjectID string    `bson:"provider_project_id" json:"provider_project_id"`
	Name              string    `bson:"name" json:"name"`
	PathWithNamespace string    `bson:"path_with_namespace" json:"path_with_namespace"`
	DefaultBranch     *string   `bson:"default_branch" json:"default_branch"`
	WebURL            string    `bson:"web_url" json:"web_url"`
	Archived          bool      `bson:"archived" json:"archived"`
	Visibility        *string   `bson:"visibility" json:"visibility"`
	LastActivityAt    *string   `bson:"last_activity_at" json:"last_activity_at"`
	SortKey           string    `bson:"sort_key" json:"sort_key"`
	SearchText        string    `bson:"search_text" json:"search_text"`
	LastSeenRefresh   string    `bson:"last_seen_refresh" json:"last_seen_refresh"`
	FirstSeenRefresh  string    `bson:"first_seen_refresh" json:"first_seen_refresh"`
	Gone              bool      `bson:"gone" json:"gone"`
	FirstSeenAt       time.Time `bson:"first_seen_at" json:"first_seen_at"`
	LastSeenAt        time.Time `bson:"last_seen_at" json:"last_seen_at"`
}

// DTO leaves project_id empty; the list handler fills it from the org's projects.
func (d CatalogProjectDoc) DTO() bson.M {
	return bson.M{"id": d.ID, "connection_id": d.ConnectionID, "provider_project_id": d.ProviderProjectID, "name": d.Name, "path_with_namespace": d.PathWithNamespace, "default_branch": d.DefaultBranch, "web_url": d.WebURL, "archived": d.Archived, "visibility": d.Visibility, "last_activity_at": d.LastActivityAt, "gone": d.Gone, "last_seen_at": timestamp(d.LastSeenAt), "project_id": nil}
}

type CatalogSummaryItem struct {
	ConnectionID, Name string
	ProjectCount       int64
	RefreshedAt        *time.Time
	RunningOperationID *string
}

func (d CatalogSummaryItem) DTO() bson.M {
	return bson.M{"connection_id": d.ConnectionID, "name": d.Name, "project_count": d.ProjectCount, "refreshed_at": optionalTime(d.RefreshedAt), "running_operation_id": d.RunningOperationID}
}

// repoKey is the canonical identity of a repository: org, connection and provider project id.
func repoKey(org, connection, providerProjectID string) string {
	return org + ":" + connection + ":" + providerProjectID
}
func (a *App) refreshCatalog(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("conId")
	route := "POST /api/v1/orgs/{orgId}/connections/{conId}/catalog/refresh"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "conId": id}, nil), func(ctx context.Context) (Output, error) {
		conn, err := a.connection(ctx, org, id)
		if err != nil {
			return Output{}, err
		}
		if conn.Provider != "gitlab" {
			return Output{}, providerMismatch()
		}
		return a.startOperation(ctx, org, "catalog_refresh", Subject{"connection", id}, "catalog_refresh:"+id, 5, bson.M{"connection_id": id})
	})
}
func boolQuery(r *http.Request, key string) (bool, error) {
	raw, ok := r.URL.Query()[key]
	if !ok {
		return false, nil
	}
	if len(raw) != 1 {
		return false, validation(key, "duplicate parameter")
	}
	v, err := strconv.ParseBool(raw[0])
	if err != nil {
		return false, validation(key, "must be true or false")
	}
	return v, nil
}
func (a *App) listCatalogProjects(w http.ResponseWriter, r *http.Request) error {
	org, conID := r.PathValue("orgId"), r.PathValue("conId")
	if _, err := a.connection(r.Context(), org, conID); err != nil {
		return err
	}
	limit, after, err := pageQueryKey(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	selection := q.Get("selection")
	if selection == "" {
		selection = "all"
	}
	if !contains([]string{"all", "selected", "unselected"}, selection) {
		return validation("selection", "must be all, selected or unselected")
	}
	includeGone, err := boolQuery(r, "include_gone")
	if err != nil {
		return err
	}
	includeArchived, err := boolQuery(r, "include_archived")
	if err != nil {
		return err
	}
	filter := bson.M{"org_id": org, "connection_id": conID}
	if !includeGone {
		filter["gone"] = false
	}
	if !includeArchived {
		filter["archived"] = false
	}
	if search := strings.ToLower(strings.TrimSpace(q.Get("search"))); search != "" {
		if len(search) > 200 {
			return validation("search", "must be at most 200 characters")
		}
		filter["search_text"] = bson.Regex{Pattern: regexp.QuoteMeta(search)}
	}
	selected, err := a.selectedProviderIDs(r.Context(), org, conID)
	if err != nil {
		return err
	}
	switch selection {
	case "selected":
		filter["provider_project_id"] = bson.M{"$in": selected}
	case "unselected":
		filter["provider_project_id"] = bson.M{"$nin": selected}
	}
	docs, next, err := findPageBy(r.Context(), a.DB, "catalog_projects", filter, "sort_key", func(d CatalogProjectDoc) string { return d.SortKey }, limit, after)
	if err != nil {
		return err
	}
	projects := map[string]string{}
	if len(docs) > 0 {
		keys := make([]string, 0, len(docs))
		for _, d := range docs {
			keys = append(keys, repoKey(org, conID, d.ProviderProjectID))
		}
		found, err := findMany[struct {
			ID  string `bson:"_id"`
			Key string `bson:"primary_repo_key"`
		}](r.Context(), a.DB, "projects", bson.M{"org_id": org, "active": true, "primary_repo_key": bson.M{"$in": keys}}, bson.D{{Key: "_id", Value: 1}}, int64(len(keys)))
		if err != nil {
			return err
		}
		for _, p := range found {
			projects[p.Key] = p.ID
		}
	}
	items := []bson.M{}
	for _, d := range docs {
		dto := d.DTO()
		if id, ok := projects[repoKey(org, conID, d.ProviderProjectID)]; ok {
			dto["project_id"] = id
		}
		items = append(items, dto)
	}
	writeJSON(w, 200, bson.M{"items": items, "next_cursor": next})
	return nil
}

// selectedProviderIDs lists the provider project ids this connection already has active automation projects for.
func (a *App) selectedProviderIDs(ctx context.Context, org, conID string) ([]string, error) {
	prefix := repoKey(org, conID, "")
	rows, err := findMany[struct {
		Key string `bson:"primary_repo_key"`
	}](ctx, a.DB, "projects", bson.M{"org_id": org, "active": true, "primary_repo_key": bson.Regex{Pattern: "^" + regexp.QuoteMeta(prefix)}}, bson.D{{Key: "_id", Value: 1}}, 100000)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, strings.TrimPrefix(row.Key, prefix))
	}
	return out, nil
}
func (a *App) catalogSummary(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	conns, err := findMany[ConnectionDoc](r.Context(), a.DB, "connections", bson.M{"org_id": org, "active": true, "provider": "gitlab"}, bson.D{{Key: "_id", Value: 1}}, 200)
	if err != nil {
		return err
	}
	items := []bson.M{}
	for _, c := range conns {
		n, err := a.DB.Coll("catalog_projects").CountDocuments(r.Context(), bson.M{"org_id": org, "connection_id": c.ID, "gone": false})
		if err != nil {
			return err
		}
		item := CatalogSummaryItem{ConnectionID: c.ID, Name: c.Name, ProjectCount: n}
		if c.CatalogSummary != nil {
			item.RefreshedAt = &c.CatalogSummary.RefreshedAt
		}
		jobs, err := findMany[JobDoc](r.Context(), a.DB, "jobs", bson.M{"org_id": org, "dedupe_key": "catalog_refresh:" + c.ID, "active": true}, bson.D{{Key: "_id", Value: 1}}, 1)
		if err != nil {
			return err
		}
		if len(jobs) == 1 {
			item.RunningOperationID = jobs[0].OperationID
		}
		items = append(items, item.DTO())
	}
	writeJSON(w, 200, items)
	return nil
}
