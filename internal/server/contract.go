package server

import (
	"context"
	"encoding/base64"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http"
	"time"
)

type ContractFile struct {
	Path       string `bson:"path" json:"path"`
	Size       int64  `bson:"size" json:"size"`
	SHA256     string `bson:"sha256" json:"sha256"`
	ArtifactID string `bson:"artifact_id" json:"artifact_id"`
	Truncated  bool   `bson:"truncated" json:"truncated"`
}
type ContractSnapshotDoc struct {
	ID                string              `bson:"_id" json:"_id"`
	OrgID             string              `bson:"org_id" json:"org_id"`
	ProjectID         string              `bson:"project_id" json:"project_id"`
	RepoKey           string              `bson:"repo_key" json:"repo_key"`
	ConnectionID      string              `bson:"connection_id" json:"connection_id"`
	ProviderProjectID string              `bson:"provider_project_id" json:"provider_project_id"`
	RefName           string              `bson:"ref_name" json:"ref_name"`
	CommitSHA         string              `bson:"commit_sha" json:"commit_sha"`
	Files             []ContractFile      `bson:"files" json:"files"`
	Suggestions       []CommandSuggestion `bson:"suggestions" json:"suggestions"`
	CreatedAt         time.Time           `bson:"created_at" json:"created_at"`
	OperationID       string              `bson:"operation_id" json:"operation_id"`
}

func (d ContractSnapshotDoc) DTO() bson.M {
	files, suggestions := d.Files, d.Suggestions
	if files == nil {
		files = []ContractFile{}
	}
	if suggestions == nil {
		suggestions = []CommandSuggestion{}
	}
	return bson.M{"id": d.ID, "project_id": d.ProjectID, "repo_key": d.RepoKey, "connection_id": d.ConnectionID, "provider_project_id": d.ProviderProjectID, "ref_name": d.RefName, "commit_sha": d.CommitSHA, "files": files, "suggestions": suggestions, "created_at": timestamp(d.CreatedAt), "operation_id": d.OperationID}
}
func (a *App) startContractImport(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("prjId")
	route := "POST /api/v1/orgs/{orgId}/projects/{prjId}/contract/import"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "prjId": id}, nil), func(ctx context.Context) (Output, error) {
		p, err := a.project(ctx, org, id)
		if err != nil {
			return Output{}, err
		}
		if p.Mapping.primary() == nil {
			return Output{}, validation("mapping", "the project has no primary repository")
		}
		return a.startOperation(ctx, org, "contract_import", Subject{"project", id}, "contract_import:"+id, 3, bson.M{"project_id": id})
	})
}
func (a *App) contractSnapshot(ctx context.Context, org, project, id string) (ContractSnapshotDoc, error) {
	return findOne[ContractSnapshotDoc](ctx, a.DB, "repo_contract_snapshots", bson.M{"_id": id, "org_id": org, "project_id": project})
}
func (a *App) listContractSnapshots(w http.ResponseWriter, r *http.Request) error {
	org, id := r.PathValue("orgId"), r.PathValue("prjId")
	if _, err := a.project(r.Context(), org, id); err != nil {
		return err
	}
	limit, after, err := pageQuery(r)
	if err != nil {
		return err
	}
	filter := bson.M{"org_id": org, "project_id": id}
	if after != "" {
		filter["_id"] = bson.M{"$lt": after}
	}
	docs, err := findMany[ContractSnapshotDoc](r.Context(), a.DB, "repo_contract_snapshots", filter, bson.D{{Key: "_id", Value: -1}}, limit+1)
	if err != nil {
		return err
	}
	var next any
	if len(docs) > int(limit) {
		docs = docs[:limit]
		next = base64.RawURLEncoding.EncodeToString([]byte(docs[len(docs)-1].ID))
	}
	items := []bson.M{}
	for _, d := range docs {
		items = append(items, d.DTO())
	}
	writeJSON(w, 200, bson.M{"items": items, "next_cursor": next})
	return nil
}
func (a *App) getContractSnapshot(w http.ResponseWriter, r *http.Request) error {
	org, id := r.PathValue("orgId"), r.PathValue("prjId")
	if _, err := a.project(r.Context(), org, id); err != nil {
		return err
	}
	d, err := a.contractSnapshot(r.Context(), org, id, r.PathValue("rcsId"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, d.DTO())
	return nil
}

// reviewContract records that a person looked at the snapshot; it approves nothing by itself.
func (a *App) reviewContract(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	snapshot := r.PathValue("rcsId")
	return a.updateProject(w, r, expected, "contract_review", func(ctx context.Context, cur ProjectDoc) (bson.M, error) {
		if _, err := a.contractSnapshot(ctx, cur.OrgID, cur.ID, snapshot); err != nil {
			return nil, err
		}
		return bson.M{"reviewed_contract_snapshot_id": snapshot}, nil
	})
}
