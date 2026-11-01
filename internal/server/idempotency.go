package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"orqen/internal/canon"
	"sort"
	"strings"
	"time"
)

type Output struct {
	Status  int
	Body    any
	Headers map[string]string
}
type DuplicateActiveJob struct{ Org, DedupeKey string }

func (e *DuplicateActiveJob) Error() string { return "an active job with the same dedupe key exists" }
func idempotencyKey(r *http.Request, required bool) (string, error) {
	vs, ok := r.Header["Idempotency-Key"]
	if !ok {
		if required {
			return "", validation("Idempotency-Key", "header is required")
		}
		return "", nil
	}
	if len(vs) != 1 {
		return "", validation("Idempotency-Key", "must be 1-255 visible ASCII characters")
	}
	v := vs[0]
	if len(v) < 1 || len(v) > 255 {
		return "", validation("Idempotency-Key", "must be 1-255 visible ASCII characters")
	}
	for _, b := range []byte(v) {
		if b < 0x21 || b > 0x7e {
			return "", validation("Idempotency-Key", "must be 1-255 visible ASCII characters")
		}
	}
	return v, nil
}
func canonicalJSON(v any) string { return canon.JSON(v) }
func requestHash(method, route string, params map[string]string, body any) string {
	fields := []string{}
	for k, v := range params {
		fields = append(fields, k+"="+v)
	}
	sort.Strings(fields)
	return sha256Hex(method + "\n" + route + "\n" + strings.Join(fields, "&") + "\n" + canonicalJSON(body))
}
func (a *App) claim(ctx context.Context, scope, route, key, hash string) (IdemRecord, *Output, error) {
	now := time.Now().UTC()
	rec := IdemRecord{ID: newID("idem"), Scope: scope, Route: route, Key: key, RequestHash: hash, State: "in_progress", ClaimToken: newID("claim"), LockedUntil: now.Add(30 * time.Second), CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	_, err := a.DB.Coll("idempotency_records").InsertOne(ctx, rec)
	if err == nil {
		return rec, nil, nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return rec, nil, err
	}
	rec, err = findOne[IdemRecord](ctx, a.DB, "idempotency_records", bson.M{"scope": scope, "route": route, "key": key})
	if err != nil {
		return rec, nil, err
	}
	if rec.RequestHash != hash {
		return rec, nil, problem("idempotency_key_reused")
	}
	if rec.State == "completed" {
		headers := map[string]string{}
		if rec.ResponseHeaders != nil {
			for k, v := range *rec.ResponseHeaders {
				if s, ok := v.(string); ok {
					headers[k] = s
				}
			}
		}
		status := 200
		if rec.ResponseStatus != nil {
			status = *rec.ResponseStatus
		}
		var body any
		if rec.ResponseBody != nil {
			body = *rec.ResponseBody
		}
		out := Output{status, body, headers}
		return rec, &out, nil
	}
	if rec.LockedUntil.After(now) {
		return rec, nil, problem("idempotency_in_progress")
	}
	token := newID("claim")
	res, err := a.DB.Coll("idempotency_records").UpdateOne(ctx, bson.M{"_id": rec.ID, "state": "in_progress", "locked_until": bson.M{"$lte": now}}, bson.M{"$set": bson.M{"claim_token": token, "locked_until": now.Add(30 * time.Second)}})
	if err != nil {
		return rec, nil, err
	}
	if res.MatchedCount == 0 {
		return rec, nil, problem("idempotency_in_progress")
	}
	rec.ClaimToken = token
	return rec, nil, nil
}
func (a *App) completeIdem(ctx context.Context, rec IdemRecord, out Output) error {
	body, err := toMap(out.Body)
	if err != nil {
		return err
	}
	headers := bson.M{}
	for k, v := range out.Headers {
		if k == "etag" || k == "location" {
			headers[k] = v
		}
	}
	res, err := a.DB.Coll("idempotency_records").UpdateOne(ctx, bson.M{"_id": rec.ID, "claim_token": rec.ClaimToken, "state": "in_progress"}, bson.M{"$set": bson.M{"state": "completed", "response_status": out.Status, "response_body": body, "response_headers": headers}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return problem("idempotency_in_progress")
	}
	return nil
}
func outputResponse(w http.ResponseWriter, out Output, replay bool) {
	for k, v := range out.Headers {
		w.Header().Set(k, v)
	}
	if replay {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	if out.Status >= 400 {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(out.Status)
		_ = json.NewEncoder(w).Encode(out.Body)
	} else {
		writeJSON(w, out.Status, out.Body)
	}
}
func (a *App) dedupeOutput(ctx context.Context, duplicate *DuplicateActiveJob) (Output, error) {
	job, err := findOne[JobDoc](ctx, a.DB, "jobs", bson.M{"org_id": duplicate.Org, "dedupe_key": duplicate.DedupeKey, "active": true})
	if err != nil || job.OperationID == nil {
		return Output{}, problem("idempotency_in_progress")
	}
	// A system job (empty org id) has its operation in the requesting organization, so the operation is found by id.
	opFilter := bson.M{"_id": *job.OperationID, "org_id": duplicate.Org}
	if duplicate.Org == "" {
		opFilter = bson.M{"_id": *job.OperationID}
	}
	op, err := findOne[OperationDoc](ctx, a.DB, "operations", opFilter)
	if err != nil {
		return Output{}, err
	}
	if duplicate.Org == "" {
		duplicate = &DuplicateActiveJob{op.OrgID, duplicate.DedupeKey}
	}
	body := op.DTO()
	body["deduplicated"] = true
	return Output{202, body, map[string]string{"location": fmt.Sprintf("/api/v1/orgs/%s/operations/%s", duplicate.Org, op.ID)}}, nil
}
func (a *App) idempotent(w http.ResponseWriter, r *http.Request, scope, route, key, hash string, f func(context.Context) (Output, error)) error {
	var rec IdemRecord
	if key != "" {
		var replay *Output
		var err error
		rec, replay, err = a.claim(r.Context(), scope, route, key, hash)
		if err != nil {
			return err
		}
		if replay != nil {
			outputResponse(w, *replay, true)
			return nil
		}
	}
	var out Output
	err := a.DB.Txn(r.Context(), func(ctx context.Context) error {
		var e error
		out, e = f(ctx)
		if e != nil {
			return e
		}
		if key != "" {
			return a.completeIdem(ctx, rec, out)
		}
		return nil
	})
	var duplicate *DuplicateActiveJob
	if errors.As(err, &duplicate) {
		out, err = a.dedupeOutput(r.Context(), duplicate)
		if err == nil && key != "" {
			err = a.completeIdem(r.Context(), rec, out)
		}
	}
	if err != nil {
		if key != "" {
			e := asAppError(err)
			if e.Status >= 400 && e.Status < 500 && e.Code != "idempotency_in_progress" {
				_ = a.completeIdem(r.Context(), rec, Output{e.Status, errorBody(err, requestID(r)), nil})
			} else {
				_, _ = a.DB.Coll("idempotency_records").DeleteOne(r.Context(), bson.M{"_id": rec.ID, "claim_token": rec.ClaimToken, "state": "in_progress"})
			}
		}
		return err
	}
	a.Committed()
	outputResponse(w, out, false)
	return nil
}
