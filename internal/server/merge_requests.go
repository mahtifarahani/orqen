package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"time"
)

// PipelineJobDoc is one job of the pipeline of the current head.
type PipelineJobDoc struct {
	Name         string `bson:"name" json:"name"`
	Status       string `bson:"status" json:"status"`
	AllowFailure bool   `bson:"allow_failure" json:"allow_failure"`
}

// MRPipeline is the pipeline Orqen last read for a merge request.
type MRPipeline struct {
	ID        int              `bson:"id" json:"id"`
	SHA       string           `bson:"sha" json:"sha"`
	Status    string           `bson:"status" json:"status"`
	Source    string           `bson:"source" json:"source"`
	Jobs      []PipelineJobDoc `bson:"jobs" json:"jobs"`
	FetchedAt time.Time        `bson:"fetched_at" json:"fetched_at"`
}

// ReadinessCheck is one line of the readiness checklist.
type ReadinessCheck struct {
	Code   string `bson:"code" json:"code"`
	Status string `bson:"status" json:"status"` // pass | fail | pending | stale
	Detail string `bson:"detail" json:"detail"`
}

// MRReadiness is the evaluated readiness of a merge request.
type MRReadiness struct {
	Ready       bool             `bson:"ready" json:"ready"`
	Checks      []ReadinessCheck `bson:"checks" json:"checks"`
	EvaluatedAt time.Time        `bson:"evaluated_at" json:"evaluated_at"`
	Head        string           `bson:"head" json:"head"`
}

// MRMerged is how a merge request ended.
type MRMerged struct {
	At             time.Time `bson:"at" json:"at"`
	MergeCommitSHA string    `bson:"merge_commit_sha" json:"merge_commit_sha"`
}

// MergeRequestDoc is the merge request of a run: real (live) or simulated (dry_run).
type MergeRequestDoc struct {
	ID                  string       `bson:"_id"`
	OrgID               string       `bson:"org_id"`
	RunID               string       `bson:"run_id"`
	ProjectID           string       `bson:"project_id"`
	ConnectionID        string       `bson:"connection_id"`
	ProviderProjectID   string       `bson:"provider_project_id"`
	Mode                string       `bson:"mode"` // dry_run | live
	IID                 int          `bson:"iid"`
	WebURL              string       `bson:"web_url"`
	Title               string       `bson:"title"`
	SourceBranch        string       `bson:"source_branch"`
	TargetBranch        string       `bson:"target_branch"`
	Draft               bool         `bson:"draft"`
	State               string       `bson:"state"` // opened | merged | closed
	CurrentHead         string       `bson:"current_head"`
	LastPushedSHA       string       `bson:"last_pushed_sha"`
	VerifiedHead        string       `bson:"verified_head"`
	ReviewedHead        string       `bson:"reviewed_head"`
	Pipeline            *MRPipeline  `bson:"pipeline"`
	DiscussionsResolved bool         `bson:"discussions_resolved"`
	HasConflicts        bool         `bson:"has_conflicts"`
	Readiness           *MRReadiness `bson:"readiness"`
	Merged              *MRMerged    `bson:"merged"`
	NoPipelineSince     *time.Time   `bson:"no_pipeline_since"`
	Active              bool         `bson:"active"`
	Revision            int64        `bson:"revision"`
	CreatedAt           time.Time    `bson:"created_at"`
	UpdatedAt           time.Time    `bson:"updated_at"`
}

// DTO is MergeRequestDto.
func (d MergeRequestDoc) DTO() bson.M {
	var pipe, ready, merged any
	if p := d.Pipeline; p != nil {
		jobs := []bson.M{}
		for _, j := range p.Jobs {
			jobs = append(jobs, bson.M{"name": j.Name, "status": j.Status, "allow_failure": j.AllowFailure})
		}
		pipe = bson.M{"id": p.ID, "sha": p.SHA, "status": p.Status, "source": p.Source, "jobs": jobs, "fetched_at": timestamp(p.FetchedAt)}
	}
	if r := d.Readiness; r != nil {
		checks := []bson.M{}
		for _, c := range r.Checks {
			checks = append(checks, bson.M{"code": c.Code, "status": c.Status, "detail": c.Detail})
		}
		ready = bson.M{"ready": r.Ready, "checks": checks, "evaluated_at": timestamp(r.EvaluatedAt), "head": r.Head}
	}
	if m := d.Merged; m != nil {
		merged = bson.M{"at": timestamp(m.At), "merge_commit_sha": optStr(m.MergeCommitSHA)}
	}
	var iid any
	if d.IID > 0 {
		iid = d.IID
	}
	return bson.M{"id": d.ID, "run_id": d.RunID, "project_id": d.ProjectID, "mode": d.Mode, "iid": iid, "web_url": optStr(d.WebURL), "title": d.Title, "source_branch": d.SourceBranch, "target_branch": d.TargetBranch, "draft": d.Draft, "state": d.State,
		"current_head": d.CurrentHead, "last_pushed_sha": d.LastPushedSHA, "verified_head": d.VerifiedHead, "reviewed_head": d.ReviewedHead, "pipeline": pipe, "discussions_resolved": d.DiscussionsResolved, "has_conflicts": d.HasConflicts,
		"readiness": ready, "merged": merged, "created_at": timestamp(d.CreatedAt), "updated_at": timestamp(d.UpdatedAt), "revision": d.Revision}
}

func (a *App) mrOfRun(ctx context.Context, org, run string) (MergeRequestDoc, bool, error) {
	d, err := findOne[MergeRequestDoc](ctx, a.DB, "merge_requests", bson.M{"org_id": org, "run_id": run, "active": true})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return MergeRequestDoc{}, false, nil
	}
	return d, err == nil, err
}
