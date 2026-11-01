package server

import (
	"orqen/internal/knowledge"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// resolveRun resolves the Orqen-record evidence kinds (verification evidence, review findings, merge requests, runs).
// Every lookup filters by org_id, so a ref of another organization does not resolve.
func (r kresolver) resolveRun(e knowledge.Evidence) (knowledge.Resolved, error) {
	res := knowledge.Resolved{Kind: e.RefKind, OrgID: r.org, Authority: knowledge.AuthorityOrqenRecord, Latest: true, Fields: map[string]string{}}
	switch e.RefKind {
	case knowledge.RefVerificationEvidence:
		id, cmd, _ := strings.Cut(e.RefID, "#")
		d, err := findOne[VerificationEvidenceDoc](r.ctx, r.a.DB, "verification_evidence", bson.M{"_id": id, "org_id": r.org})
		if err != nil {
			return knowledge.Resolved{}, err
		}
		res.Fields["head"], res.Fields["round"] = d.HeadSHA, strconv.Itoa(d.Round)
		for _, c := range d.Commands {
			if cmd == "" || c.Key == cmd {
				res.Fields["command"], res.Fields["outcome"] = c.Key, c.Outcome
				res.Text = strings.Join(c.Argv, " ")
				break
			}
		}
		if cmd != "" && res.Fields["command"] == "" {
			return knowledge.Resolved{}, mongo.ErrNoDocuments
		}
	case knowledge.RefReviewFinding:
		d, err := findOne[ReviewFindingDoc](r.ctx, r.a.DB, "review_findings", bson.M{"_id": e.RefID, "org_id": r.org})
		if err != nil {
			return knowledge.Resolved{}, err
		}
		res.Fields["severity"], res.Fields["round"], res.Fields["status"] = d.Severity, strconv.Itoa(d.Round), d.Status
		res.Fields["title_sha8"] = knowledge.SHA(d.Title)[:8]
		res.Text = d.Title + "\n" + d.Body
	case knowledge.RefMergeRequest:
		d, err := findOne[MergeRequestDoc](r.ctx, r.a.DB, "merge_requests", bson.M{"_id": e.RefID, "org_id": r.org})
		if err != nil {
			return knowledge.Resolved{}, err
		}
		res.Fields["iid"] = strconv.Itoa(d.IID)
		if d.Merged != nil {
			res.Fields["merge_commit"] = d.Merged.MergeCommitSHA
		}
		res.Text = d.Title
	case knowledge.RefRun:
		d, err := findOne[RunDoc](r.ctx, r.a.DB, "runs", bson.M{"_id": e.RefID, "org_id": r.org})
		if err != nil {
			return knowledge.Resolved{}, err
		}
		res.Fields["head"] = d.Heads.Head
		if d.Review != nil {
			res.Fields["verdict"] = d.Review.Verdict
		}
		res.Text = d.IssueKey
	default:
		return knowledge.Resolved{}, mongo.ErrNoDocuments
	}
	return res, nil
}
