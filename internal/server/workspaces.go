package server

import (
	"go.mongodb.org/mongo-driver/v2/bson"
	"strconv"
	"time"
)

// WorkspaceDoc is the managed worktree of one run (PLAN-S3-GO S27).
type WorkspaceDoc struct {
	ID                 string     `bson:"_id"`
	OrgID              string     `bson:"org_id"`
	RunID              string     `bson:"run_id"`
	ProjectID          string     `bson:"project_id"`
	ConnectionID       string     `bson:"connection_id"`
	ProviderProjectID  string     `bson:"provider_project_id"`
	MirrorRel          string     `bson:"mirror_rel"`
	PathRel            string     `bson:"path_rel"`
	Branch             string     `bson:"branch"`
	BaseRef            string     `bson:"base_ref"`
	BaseSHA            string     `bson:"base_sha"`
	HeadSHA            string     `bson:"head_sha"`
	State              string     `bson:"state"` // preparing | ready | retained | preserved | removed | failed
	Dirty              bool       `bson:"dirty"`
	SnapshotArtifactID *string    `bson:"snapshot_artifact_id"`
	Active             bool       `bson:"active"`
	Revision           int64      `bson:"revision"`
	CreatedAt          time.Time  `bson:"created_at"`
	RemovedAt          *time.Time `bson:"removed_at"`
}

// DTO is WorkspaceDto. path is relative to the data directory.
func (d WorkspaceDoc) DTO() bson.M {
	return bson.M{"id": d.ID, "state": d.State, "path": d.PathRel, "branch": d.Branch, "base_ref": d.BaseRef, "base_sha": d.BaseSHA, "head_sha": d.HeadSHA, "dirty": d.Dirty, "created_at": timestamp(d.CreatedAt),
		"removed_at": optionalTime(d.RemovedAt), "snapshot_artifact_id": d.SnapshotArtifactID, "revision": d.Revision}
}

func formatInt(n int64) string { return strconv.FormatInt(n, 10) }
