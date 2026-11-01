package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"orqen/internal/providers"
	"strings"
	"time"
)

const catalogProjectLimit = 100_000

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// runCatalogRefresh pages through the provider's projects and upserts them. A retry restarts from the first page
// with the same refresh id, which is idempotent; rows not seen in the refresh are marked gone at the end.
func (a *App) runCatalogRefresh(ctx context.Context, job JobDoc, lease Lease) error {
	if job.OperationID == nil {
		return &JobFailure{Code: "no_operation", Message: "job has no operation"}
	}
	id, ok := job.Payload["connection_id"].(string)
	if !ok || !validID("con", id) {
		return &JobFailure{Code: "bad_payload", Message: "missing connection_id"}
	}
	refresh := *job.OperationID
	if err := a.fenced(ctx, lease, func(sc context.Context) error { return a.transitionOperation(sc, job, "running", nil) }); err != nil {
		return err
	}
	conn, err := a.connection(ctx, job.OrgID, id)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return &JobFailure{Code: "connection_archived", Message: "connection no longer exists"}
	}
	if err != nil {
		return err
	}
	if conn.Provider != "gitlab" {
		return &JobFailure{Code: "provider_mismatch", Message: "catalog refresh needs a GitLab connection"}
	}
	host, err := a.codeHost(ctx, conn)
	if err != nil {
		if _, ok := providers.AsError(err); !ok {
			return &JobFailure{Code: "provider_unavailable", Message: err.Error()}
		}
		return err
	}
	seen, cursor := int64(0), ""
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		page, err := host.ListProjectsPage(ctx, cursor)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		models := make([]mongo.WriteModel, 0, len(page.Items))
		for _, p := range page.Items {
			set := bson.M{"name": p.Name, "path_with_namespace": p.PathWithNamespace, "default_branch": optionalString(p.DefaultBranch), "web_url": p.WebURL, "archived": p.Archived, "visibility": optionalString(p.Visibility), "last_activity_at": optionalString(p.LastActivityAt), "sort_key": strings.ToLower(p.PathWithNamespace) + "\x00" + p.ID, "search_text": strings.ToLower(p.Name + " " + p.PathWithNamespace), "last_seen_refresh": refresh, "gone": false, "last_seen_at": now}
			models = append(models, mongo.NewUpdateOneModel().SetFilter(bson.M{"org_id": job.OrgID, "connection_id": id, "provider_project_id": p.ID}).SetUpdate(bson.M{"$set": set, "$setOnInsert": bson.M{"_id": newID("cpj"), "first_seen_at": now, "first_seen_refresh": refresh}}).SetUpsert(true))
		}
		seen += int64(len(page.Items))
		if seen > catalogProjectLimit {
			return &JobFailure{Code: "too_many_projects", Message: "the provider lists more than 100000 projects"}
		}
		if err = a.fenced(ctx, lease, func(sc context.Context) error {
			if len(models) > 0 {
				if _, e := a.DB.Coll("catalog_projects").BulkWrite(sc, models); e != nil {
					return e
				}
			}
			return a.AppendEvent(sc, job.OrgID, "catalog_refresh.progress", Subject{"connection", id}, bson.M{"connection_id": id, "seen": seen}, nil, job.OperationID, true)
		}); err != nil {
			return err
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	return a.fenced(ctx, lease, func(sc context.Context) error {
		res, err := a.DB.Coll("catalog_projects").UpdateMany(sc, bson.M{"org_id": job.OrgID, "connection_id": id, "gone": false, "last_seen_refresh": bson.M{"$ne": refresh}}, bson.M{"$set": bson.M{"gone": true}})
		if err != nil {
			return err
		}
		added, err := a.DB.Coll("catalog_projects").CountDocuments(sc, bson.M{"org_id": job.OrgID, "connection_id": id, "first_seen_refresh": refresh})
		if err != nil {
			return err
		}
		if _, err = a.DB.Coll("connections").UpdateOne(sc, connFilter(job.OrgID, id), bson.M{"$set": bson.M{"catalog_summary": CatalogSummary{RefreshedAt: time.Now().UTC(), ProjectCount: seen, OperationID: refresh}}}); err != nil {
			return err
		}
		result := bson.M{"seen": seen, "added": added, "gone": res.ModifiedCount}
		if err = a.transitionOperation(sc, job, "succeeded", result); err != nil {
			return err
		}
		result["connection_id"] = id
		if err = a.AppendEvent(sc, job.OrgID, "catalog.refreshed", Subject{"connection", id}, result, nil, job.OperationID, true); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
}
