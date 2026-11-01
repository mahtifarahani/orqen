package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"time"
	"unicode/utf8"
)

// AutomationStateDoc is the organization-level pause switch (id "aut_" + the organization's ULID).
type AutomationStateDoc struct {
	ID        string     `bson:"_id"`
	OrgID     string     `bson:"org_id"`
	Paused    bool       `bson:"paused"`
	PausedAt  *time.Time `bson:"paused_at"`
	Reason    *string    `bson:"reason"`
	Revision  int64      `bson:"revision"`
	UpdatedAt time.Time  `bson:"updated_at"`
}

func automationStateID(org string) string { return "aut_" + org[4:] }

func (d AutomationStateDoc) DTO() bson.M {
	return bson.M{"paused": d.Paused, "paused_at": optionalTime(d.PausedAt), "reason": d.Reason, "revision": d.Revision}
}

type PutAutomationRequest struct {
	Paused bool    `json:"paused"`
	Reason *string `json:"reason"`
}

// automationState returns the stored state or a synthesized revision-0 default.
func (a *App) automationState(ctx context.Context, org string) (AutomationStateDoc, error) {
	d, err := findOne[AutomationStateDoc](ctx, a.DB, "automation_states", bson.M{"_id": automationStateID(org), "org_id": org})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return AutomationStateDoc{ID: automationStateID(org), OrgID: org}, nil
	}
	return d, err
}
func (a *App) orgPaused(ctx context.Context, org string) (bool, error) {
	d, err := a.automationState(ctx, org)
	return d.Paused, err
}
func (a *App) getAutomationState(w http.ResponseWriter, r *http.Request) error {
	d, err := a.automationState(r.Context(), r.PathValue("orgId"))
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, 200, d.DTO())
	return nil
}
func (a *App) putAutomationState(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	var req PutAutomationRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	var reason *string
	if req.Reason != nil {
		s := *req.Reason
		if utf8.RuneCountInString(s) > 500 {
			return validation("reason", "must be at most 500 characters")
		}
		if s != "" {
			reason = &s
		}
	}
	org := r.PathValue("orgId")
	var doc AutomationStateDoc
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		now := time.Now().UTC()
		var paused *time.Time
		if req.Paused {
			paused = &now
		}
		set := bson.M{"paused": req.Paused, "paused_at": paused, "reason": reason, "updated_at": now}
		if expected == 0 {
			doc = AutomationStateDoc{ID: automationStateID(org), OrgID: org, Paused: req.Paused, PausedAt: paused, Reason: reason, Revision: 1, UpdatedAt: now}
			if _, e := a.DB.Coll("automation_states").InsertOne(ctx, doc); e != nil {
				if mongo.IsDuplicateKeyError(e) {
					return errAutomationExists
				}
				return e
			}
		} else {
			e := a.DB.Coll("automation_states").FindOneAndUpdate(ctx, bson.M{"_id": automationStateID(org), "org_id": org, "revision": expected}, bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&doc)
			if errors.Is(e, mongo.ErrNoDocuments) {
				return a.automationConflict(ctx, org)
			}
			if e != nil {
				return e
			}
		}
		kind := "automation.resumed"
		if req.Paused {
			kind = "automation.paused"
		}
		return a.AppendEvent(ctx, org, kind, Subject{"automation", automationStateID(org)}, bson.M{"paused": req.Paused}, &doc.Revision, nil, false)
	})
	if errors.Is(err, errAutomationExists) {
		return a.automationConflict(r.Context(), org)
	}
	if err != nil {
		return err
	}
	a.Committed()
	w.Header().Set("ETag", etag(doc.Revision))
	writeJSON(w, 200, doc.DTO())
	return nil
}

var errAutomationExists = errors.New("automation state exists")

func (a *App) automationConflict(ctx context.Context, org string) error {
	cur, err := a.automationState(ctx, org)
	if err != nil {
		return err
	}
	c := problem("revision_conflict")
	c.Current = cur.DTO()
	return c
}
