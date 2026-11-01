package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"orqen/internal/knowledge"
)

// KnowledgeProposalDoc records one authoring pass over a finished run. Nothing here uses a model: the drafts are
// deterministic templates over stored Orqen evidence (K14).
type KnowledgeProposalDoc struct {
	ID               string                  `bson:"_id"`
	OrgID            string                  `bson:"org_id"`
	DedupeKey        string                  `bson:"dedupe_key"`
	RunID            string                  `bson:"run_id"`
	BundleArtifactID string                  `bson:"bundle_artifact_id"`
	Trigger          string                  `bson:"trigger"` // merge | manual | source_changed
	State            string                  `bson:"state"`   // empty | awaiting_review | applied
	Items            []KnowledgeProposalItem `bson:"items"`
	CreatedAt        time.Time               `bson:"created_at"`
}
type KnowledgeProposalItem struct {
	RecordID  string   `bson:"record_id"`
	VersionID string   `bson:"version_id"`
	Key       string   `bson:"key"`
	Type      string   `bson:"type"`
	Outcome   string   `bson:"outcome"` // draft | verified | awaiting_review | conflicted | exists | skipped
	Reasons   []string `bson:"reasons"`
}

func (d KnowledgeProposalDoc) DTO() bson.M {
	items := []bson.M{}
	for _, i := range d.Items {
		items = append(items, bson.M{"record_id": i.RecordID, "version_id": i.VersionID, "key": i.Key, "type": i.Type, "outcome": i.Outcome, "reasons": nonNilStrings(i.Reasons)})
	}
	return bson.M{"id": d.ID, "bundle_artifact_id": optStr(d.BundleArtifactID), "run_id": d.RunID, "trigger": d.Trigger, "state": d.State, "items": items, "created_at": timestamp(d.CreatedAt)}
}

// buildBundle collects the evidence of a run from stored records only.
func (a *App) buildBundle(ctx context.Context, run RunDoc) (knowledge.Bundle, error) {
	b := knowledge.Bundle{RunID: run.ID, ProjectID: run.ProjectID, IssueKey: run.IssueKey, Head: run.Heads.Head}
	snap, err := findOne[IssueSnapshotDoc](ctx, a.DB, "issue_snapshots", bson.M{"_id": run.SnapshotID, "org_id": run.OrgID})
	if err == nil {
		for i, t := range acceptanceItems(snap) {
			b.Acceptance = append(b.Acceptance, knowledge.BundleAcceptance{Idx: i + 1, Text: t})
		}
	}
	if mr, e := findOne[MergeRequestDoc](ctx, a.DB, "merge_requests", bson.M{"org_id": run.OrgID, "run_id": run.ID}); e == nil {
		b.MR = &knowledge.BundleMR{RefID: mr.ID, IID: mr.IID}
		if mr.Merged != nil && mr.State == "merged" {
			b.Merged, b.MR.MergeCommit = true, mr.Merged.MergeCommitSHA
		}
	}
	evs, err := findMany[VerificationEvidenceDoc](ctx, a.DB, "verification_evidence", bson.M{"org_id": run.OrgID, "run_id": run.ID, "kind": "head"}, bson.D{{Key: "round", Value: 1}}, 100)
	if err != nil {
		return b, err
	}
	for _, ev := range evs {
		for _, c := range ev.Commands {
			o := "fail"
			if c.Outcome == "pass" {
				o = "pass"
			}
			b.Verification = append(b.Verification, knowledge.BundleVerification{RefID: ev.ID + "#" + c.Key, Command: c.Key, Round: ev.Round, Outcome: o, Head: ev.HeadSHA})
		}
	}
	if run.Review != nil {
		b.Review = &knowledge.BundleReview{RefID: run.ID, Verdict: run.Review.Verdict, Head: run.Review.Head}
	}
	fs, err := findMany[ReviewFindingDoc](ctx, a.DB, "review_findings", bson.M{"org_id": run.OrgID, "run_id": run.ID}, bson.D{{Key: "_id", Value: 1}}, 200)
	if err != nil {
		return b, err
	}
	for _, f := range fs {
		b.Findings = append(b.Findings, knowledge.BundleFinding{RefID: f.ID, Severity: f.Severity, Title: f.Title, Round: f.Round, Status: f.Status})
	}
	return b, nil
}

func claimInputs(cs []knowledge.Claim) []KnowledgeClaimInput {
	out := make([]KnowledgeClaimInput, 0, len(cs))
	for _, c := range cs {
		in := KnowledgeClaimInput{Text: c.Text, Template: c.Template, Fact: c.Fact, Critical: c.Critical}
		for _, e := range c.Evidence {
			in.Evidence = append(in.Evidence, KnowledgeEvidence{RefKind: e.RefKind, RefID: e.RefID, Locator: e.Locator, Excerpt: e.Excerpt})
		}
		out = append(out, in)
	}
	return out
}

// authorRun drafts, gates and (per the type policy) promotes the deterministic knowledge of one run.
func (a *App) authorRun(ctx context.Context, org, runID, trigger string) (KnowledgeProposalDoc, error) {
	run, err := a.run(ctx, org, runID)
	if err != nil {
		return KnowledgeProposalDoc{}, err
	}
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return KnowledgeProposalDoc{}, err
	}
	id := newID("kpr")
	dk := id
	if trigger == "merge" {
		dk = "merge:" + runID
	}
	prop := KnowledgeProposalDoc{ID: id, DedupeKey: dk, OrgID: org, RunID: runID, Trigger: trigger, State: "empty", Items: []KnowledgeProposalItem{}, CreatedAt: time.Now().UTC()}
	if trigger == "merge" && !pv.Bool("knowledge.learn_on_merge") {
		return prop, nil
	}
	bundle, err := a.buildBundle(ctx, run)
	if err != nil {
		return prop, err
	}
	if raw, e := json.Marshal(bundle); e == nil {
		if blob, e := storeBlob(a.artifactRoot(), org, bytes.NewReader(raw), 1<<20); e == nil {
			if art, e := a.recordArtifact(ctx, org, blob, "application/json", "knowledge_bundle", Subject{"run", runID}); e == nil {
				prop.BundleArtifactID = art.ID
			}
		}
	}
	for _, d := range knowledge.DraftFromBundle(bundle) {
		mode := pv.Str("knowledge.promotion." + string(d.Type))
		if mode == "off" {
			continue
		}
		item := KnowledgeProposalItem{Key: d.Key, Type: string(d.Type), Outcome: "draft"}
		scope := KnowledgeScopeInput{Kind: d.ScopeKind}
		switch d.ScopeKind {
		case "task":
			scope.RunID = runID
		case "project":
			scope.ProjectID = run.ProjectID
		}
		origin := knowledge.Origin{Kind: "deterministic", Actor: "system"}
		rec, v, e := a.createKnowledgeRecord(ctx, org, "system", CreateKnowledgeRecordRequest{Type: string(d.Type), Scope: scope, Key: d.Key, Title: d.Title, Body: d.Body, Claims: claimInputs(d.Claims)}, origin)
		var ae *AppError
		if errors.As(e, &ae) && ae.Code == "knowledge_key_taken" {
			// The record exists: add a draft version when none is open.
			existing, e2 := findOne[KnowledgeRecordDoc](ctx, a.DB, "knowledge_records", bson.M{"org_id": org, "scope_key": d.ScopeKey, "key": d.Key})
			if e2 != nil {
				return prop, e2
			}
			nv, e3 := a.createKnowledgeVersion(ctx, org, existing.ID, existing.Revision, CreateKnowledgeVersionRequest{Title: d.Title, Body: d.Body, Claims: claimInputs(d.Claims)}, origin)
			if e3 != nil {
				item.RecordID, item.Outcome = existing.ID, "exists"
				prop.Items = append(prop.Items, item)
				continue
			}
			rec, v, e = existing, nv, nil
		}
		if e != nil {
			item.Outcome, item.Reasons = "skipped", []string{errCode(e)}
			prop.Items = append(prop.Items, item)
			continue
		}
		item.RecordID, item.VersionID = rec.ID, v.ID
		item.Outcome = "awaiting_review"
		if mode == "auto" {
			out, ve := a.verifyVersion(ctx, org, v.ID, verifyOpts{Actor: knowledge.ActorPolicy, User: "system"})
			switch {
			case ve == nil && out.Outcome == "verified":
				item.Outcome = "verified"
			case ve == nil:
				item.Outcome, item.Reasons = "awaiting_review", out.Never
			case errCode(ve) == "knowledge_conflict_open":
				item.Outcome = "conflicted"
			default:
				item.Outcome, item.Reasons = "awaiting_review", []string{errCode(ve)}
			}
		}
		prop.Items = append(prop.Items, item)
	}
	prop.State = "applied"
	for _, i := range prop.Items {
		if i.Outcome != "verified" {
			prop.State = "awaiting_review"
		}
	}
	if len(prop.Items) == 0 {
		prop.State = "empty"
	}
	_, err = a.DB.Coll("knowledge_proposals").InsertOne(ctx, prop)
	if mongo.IsDuplicateKeyError(err) {
		err = nil
	}
	return prop, err
}

func errCode(err error) string {
	var ae *AppError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return "error"
}

func (a *App) runKnowledgeAuthor(ctx context.Context, job JobDoc, lease Lease) error {
	id, _ := job.Payload["run_id"].(string)
	trigger, _ := job.Payload["trigger"].(string)
	if !validID("run", id) {
		return &JobFailure{Code: "bad_payload", Message: "missing run_id"}
	}
	if trigger == "merge" {
		// knowledge.author = roles hands the merged run to a detached author session instead of the deterministic author (S7).
		run, err := a.run(ctx, job.OrgID, id)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return &JobFailure{Code: "run_missing", Message: "run no longer exists"}
		}
		if err != nil {
			return err
		}
		pv, err := a.pinnedPolicy(ctx, run)
		if err != nil {
			return err
		}
		if pv.Bool("knowledge.learn_on_merge") && pv.Str("knowledge.author") == "roles" {
			create := func(sc context.Context) error {
				if _, e := a.createAuthorSessionTx(sc, run, "merge", 1, "kas:merge:"+run.ID); e != nil {
					return e
				}
				return a.leaseWrite(sc, lease, bson.M{"state": "succeeded", "active": false, "lease_owner": nil, "lease_expires_at": nil, "updated_at": time.Now().UTC()})
			}
			err = a.fenced(ctx, lease, create)
			if errors.Is(err, errJobRace) {
				err = a.fenced(ctx, lease, create)
			}
			return err
		}
	}
	if _, err := a.authorRun(ctx, job.OrgID, id, trigger); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return &JobFailure{Code: "run_missing", Message: "run no longer exists"}
		}
		return err
	}
	return a.fenced(ctx, lease, func(sc context.Context) error { return a.completeJob(sc, lease) })
}

func (a *App) proposeFromRun(w http.ResponseWriter, r *http.Request) error {
	org, id := r.PathValue("orgId"), r.PathValue("runId")
	if _, err := a.run(r.Context(), org, id); err != nil {
		return err
	}
	prop, err := a.authorRun(r.Context(), org, id, "manual")
	if err != nil {
		return err
	}
	writeJSON(w, 201, prop.DTO())
	return nil
}

func (a *App) listProposals(w http.ResponseWriter, r *http.Request) error {
	limit, after, err := pageQuery(r)
	if err != nil {
		return err
	}
	f := bson.M{"org_id": r.PathValue("orgId")}
	if v := strings.TrimSpace(r.URL.Query().Get("run_id")); v != "" {
		f["run_id"] = v
	}
	page, err := listPage[KnowledgeProposalDoc](r.Context(), a.DB, "knowledge_proposals", f, limit, after)
	if err != nil {
		return err
	}
	writeJSON(w, 200, page)
	return nil
}

// proposeSourceChanged records a review proposal for each verified version that cites a source version that changed.
func (a *App) proposeSourceChanged(ctx context.Context, org, oldID, newSrcID string) error {
	vs, err := findMany[KnowledgeVersionDoc](ctx, a.DB, "knowledge_versions", bson.M{"org_id": org, "status": "verified", "claims.evidence.ref_id": oldID}, bson.D{{Key: "_id", Value: 1}}, 200)
	if err != nil || len(vs) == 0 {
		return err
	}
	prop := KnowledgeProposalDoc{ID: newID("kpr"), OrgID: org, DedupeKey: "source:" + newSrcID, Trigger: "source_changed", State: "awaiting_review", Items: []KnowledgeProposalItem{}, CreatedAt: time.Now().UTC()}
	for _, v := range vs {
		prop.Items = append(prop.Items, KnowledgeProposalItem{RecordID: v.RecordID, VersionID: v.ID, Key: v.Title, Type: v.Type, Outcome: "awaiting_review", Reasons: []string{"source_changed"}})
	}
	_, err = a.DB.Coll("knowledge_proposals").InsertOne(ctx, prop)
	if mongo.IsDuplicateKeyError(err) {
		return nil
	}
	return err
}
