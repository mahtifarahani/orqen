package server

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Finding statuses.
const (
	FindingOpen       = "open"
	FindingFixed      = "fixed"
	FindingWaived     = "waived"
	FindingSuperseded = "superseded"
)

// FindingWaiver is a person's reason for accepting a blocking finding.
type FindingWaiver struct {
	Text string    `bson:"text" json:"text"`
	At   time.Time `bson:"at" json:"at"`
}

// FindingPublication links a finding to the operation that posted it.
type FindingPublication struct {
	OpID     string `bson:"op_id" json:"op_id"`
	RemoteID string `bson:"remote_id" json:"remote_id"`
}

// ReviewFindingDoc is one finding of one review (collection review_findings).
type ReviewFindingDoc struct {
	ID             string              `bson:"_id"`
	OrgID          string              `bson:"org_id"`
	RunID          string              `bson:"run_id"`
	Round          int                 `bson:"round"`
	ReviewRexID    string              `bson:"review_rex_id"`
	HeadSHA        string              `bson:"head_sha"`
	FindingKey     string              `bson:"finding_key"`
	Severity       string              `bson:"severity"`
	Blocking       bool                `bson:"blocking"`
	File           string              `bson:"file"`
	Line           int                 `bson:"line"`
	Side           string              `bson:"side"`
	LocationValid  bool                `bson:"location_valid"`
	InlineEligible bool                `bson:"inline_eligible"`
	Title          string              `bson:"title"`
	Body           string              `bson:"body"`
	AcceptanceRef  string              `bson:"acceptance_ref"`
	Status         string              `bson:"status"`
	Waiver         *FindingWaiver      `bson:"waiver"`
	Publication    *FindingPublication `bson:"publication"`
	CreatedAt      time.Time           `bson:"created_at"`
	Revision       int64               `bson:"revision"`
}

// DTO is ReviewFindingDto.
func (d ReviewFindingDoc) DTO() bson.M {
	var waiver, pub any
	if d.Waiver != nil {
		waiver = bson.M{"text": d.Waiver.Text, "at": timestamp(d.Waiver.At)}
	}
	if d.Publication != nil {
		pub = bson.M{"op_id": d.Publication.OpID, "remote_id": optStr(d.Publication.RemoteID)}
	}
	return bson.M{"id": d.ID, "run_id": d.RunID, "round": d.Round, "head_sha": d.HeadSHA, "finding_key": d.FindingKey, "severity": d.Severity, "blocking": d.Blocking, "file": d.File, "line": d.Line, "side": d.Side,
		"location_valid": d.LocationValid, "inline_eligible": d.InlineEligible, "title": d.Title, "body": d.Body, "acceptance_ref": d.AcceptanceRef, "status": d.Status, "waiver": waiver, "publication": pub,
		"created_at": timestamp(d.CreatedAt), "revision": d.Revision}
}

func (a *App) listRunFindings(w http.ResponseWriter, r *http.Request) error {
	org, id := r.PathValue("orgId"), r.PathValue("runId")
	if _, err := a.run(r.Context(), org, id); err != nil {
		return err
	}
	filter := bson.M{"org_id": org, "run_id": id}
	if v := r.URL.Query().Get("round"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return validation("round", "must be a non-negative integer")
		}
		filter["round"] = n
	}
	docs, err := findMany[ReviewFindingDoc](r.Context(), a.DB, "review_findings", filter, bson.D{{Key: "round", Value: 1}, {Key: "_id", Value: 1}}, 500)
	if err != nil {
		return err
	}
	out := []bson.M{}
	for _, d := range docs {
		out = append(out, d.DTO())
	}
	writeJSON(w, 200, bson.M{"items": out, "next_cursor": nil})
	return nil
}

// WaiveFindingRequest is the body of POST /runs/{runId}/findings/{fndId}/waive.
type WaiveFindingRequest struct {
	Reason string `json:"reason"`
}

func (a *App) waiveFinding(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req WaiveFindingRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || utf8.RuneCountInString(reason) > 2000 {
		return validation("reason", "must be 1 to 2000 characters")
	}
	org, run, id := r.PathValue("orgId"), r.PathValue("runId"), r.PathValue("fndId")
	route := "POST /api/v1/orgs/{orgId}/runs/{runId}/findings/{fndId}/waive"
	hash := requestHash("POST", route, map[string]string{"orgId": org, "runId": run, "fndId": id, "if_match": fmt.Sprint(expected)}, req)
	return a.idempotent(w, r, org, route, key, hash, func(ctx context.Context) (Output, error) {
		f, err := findOne[ReviewFindingDoc](ctx, a.DB, "review_findings", bson.M{"_id": id, "org_id": org, "run_id": run})
		if err != nil {
			return Output{}, err
		}
		if f.Revision != expected {
			c := problem("revision_conflict")
			c.Current = f.DTO()
			return Output{}, c
		}
		if f.Status != FindingOpen || !f.Blocking {
			return Output{}, problem("finding_not_waivable")
		}
		var next ReviewFindingDoc
		err = a.DB.Coll("review_findings").FindOneAndUpdate(ctx, bson.M{"_id": id, "org_id": org, "revision": expected, "status": FindingOpen}, bson.M{"$set": bson.M{"status": FindingWaived, "waiver": FindingWaiver{Text: reason, At: time.Now().UTC()}}, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&next)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return Output{}, problem("finding_not_waivable")
		}
		if err != nil {
			return Output{}, err
		}
		if err = a.AppendEvent(ctx, org, "finding.waived", Subject{"run", run}, bson.M{"run_id": run, "finding_id": id}, nil, nil, false); err != nil {
			return Output{}, err
		}
		return Output{200, next.DTO(), map[string]string{"etag": etag(next.Revision)}}, nil
	})
}
