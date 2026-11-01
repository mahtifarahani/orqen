package server

import (
	"bytes"
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"orqen/internal/providers"
	"path"
	"path/filepath"
	"time"
)

const contractFileMax = 256 << 10

func contractMedia(name string) string {
	if path.Base(name) == "package.json" {
		return "application/json"
	}
	return "text/plain; charset=utf-8"
}

// runContractImport reads a fixed set of well-known files from the primary repository at the target branch head.
// Files are stored as artifacts and parsed for deterministic command suggestions; nothing is approved.
func (a *App) runContractImport(ctx context.Context, job JobDoc, lease Lease) error {
	if job.OperationID == nil {
		return &JobFailure{Code: "no_operation", Message: "job has no operation"}
	}
	id, ok := job.Payload["project_id"].(string)
	if !ok || !validID("prj", id) {
		return &JobFailure{Code: "bad_payload", Message: "missing project_id"}
	}
	if err := a.fenced(ctx, lease, func(sc context.Context) error { return a.transitionOperation(sc, job, "running", nil) }); err != nil {
		return err
	}
	proj, err := a.project(ctx, job.OrgID, id)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return &JobFailure{Code: "project_archived", Message: "project no longer exists"}
	}
	if err != nil {
		return err
	}
	repo := proj.Mapping.primary()
	if repo == nil {
		return &JobFailure{Code: "primary_repo_missing", Message: "the project has no primary repository"}
	}
	conn, err := a.connection(ctx, job.OrgID, repo.ConnectionID)
	if err != nil {
		return &JobFailure{Code: "connection_unavailable", Message: "the repository connection no longer exists"}
	}
	host, err := a.codeHost(ctx, conn)
	if err != nil {
		if _, ok := providers.AsError(err); !ok {
			return &JobFailure{Code: "provider_unavailable", Message: err.Error()}
		}
		return err
	}
	pkg := ""
	if repo.PackagePath != nil {
		pkg = *repo.PackagePath
	}
	sha, err := host.ResolveCommit(ctx, repo.ProviderProjectID, repo.TargetBranch)
	if err != nil {
		return err
	}
	dirs := []string{""}
	if pkg != "" {
		dirs = append(dirs, pkg)
	}
	listings := map[string]map[string]bool{}
	for _, dir := range dirs {
		entries, err := host.ListTree(ctx, repo.ProviderProjectID, sha, dir)
		if err != nil {
			if e, ok := providers.AsError(err); ok && e.Kind == providers.KindNotFound && dir != "" {
				continue
			}
			return err
		}
		names := map[string]bool{}
		for _, e := range entries {
			if e.Type == "blob" {
				names[e.Name] = true
			}
		}
		listings[dir] = names
	}
	type fetched struct {
		path      string
		truncated bool
		blob      Blob
	}
	var got []fetched
	contents := map[string]string{}
	artifacts := filepath.Join(a.Config.DataDir, "artifacts")
	for _, dir := range dirs {
		for _, name := range contractReadable {
			if !listings[dir][name] {
				continue
			}
			p := path.Join(dir, name)
			data, truncated, err := host.ReadFileRaw(ctx, repo.ProviderProjectID, sha, p, contractFileMax)
			if err != nil {
				return err
			}
			blob, err := storeBlob(artifacts, job.OrgID, bytes.NewReader(data), a.Config.ArtifactMaxBytes)
			if err != nil {
				return err
			}
			got = append(got, fetched{p, truncated, blob})
			contents[p] = string(data)
		}
	}
	suggestions := suggestCommands(contents, listings, pkg)
	snapshot := ContractSnapshotDoc{ID: newID("rcs"), OrgID: job.OrgID, ProjectID: id, RepoKey: repoKey(job.OrgID, repo.ConnectionID, repo.ProviderProjectID), ConnectionID: repo.ConnectionID, ProviderProjectID: repo.ProviderProjectID, RefName: repo.TargetBranch, CommitSHA: sha, Files: []ContractFile{}, Suggestions: suggestions, CreatedAt: time.Now().UTC(), OperationID: *job.OperationID}
	return a.fenced(ctx, lease, func(sc context.Context) error {
		for _, f := range got {
			art, err := a.recordArtifact(sc, job.OrgID, f.blob, contractMedia(f.path), "repo_contract_file", Subject{"project", id})
			if err != nil {
				return err
			}
			snapshot.Files = append(snapshot.Files, ContractFile{Path: f.path, Size: f.blob.Size, SHA256: f.blob.SHA256, ArtifactID: art.ID, Truncated: f.truncated})
		}
		if _, err := a.DB.Coll("repo_contract_snapshots").InsertOne(sc, snapshot); err != nil {
			return err
		}
		if _, err := a.DB.Coll("projects").UpdateOne(sc, prjFilter(job.OrgID, id), bson.M{"$set": bson.M{"latest_contract_snapshot_id": snapshot.ID}}); err != nil {
			return err
		}
		result := bson.M{"snapshot_id": snapshot.ID, "commit_sha": sha, "files": len(snapshot.Files), "suggestions": len(suggestions)}
		if err := a.transitionOperation(sc, job, "succeeded", result); err != nil {
			return err
		}
		if err := a.AppendEvent(sc, job.OrgID, "project.contract_imported", Subject{"project", id}, bson.M{"snapshot_id": snapshot.ID, "commit_sha": sha, "files": len(snapshot.Files), "suggestions": len(suggestions)}, nil, job.OperationID, true); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
}
