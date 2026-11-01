package server

import (
	"bytes"
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"orqen/internal/canon"
	"orqen/internal/decision"
	"path/filepath"
	"time"
)

// DecisionAnswer is the stored form of one Jev answer.
type DecisionAnswer struct {
	Type          string             `bson:"type"`
	Choice        string             `bson:"choice"`
	Probabilities map[string]float64 `bson:"probabilities"`
	Confidence    float64            `bson:"confidence"`
	Score         float64            `bson:"score"`
	Noul          float64            `bson:"noul"`
}

// DecisionDoc records one bounded decision call. The state is not stored here, only its artifact and hash.
type DecisionDoc struct {
	ID              string                    `bson:"_id"`
	OrgID           string                    `bson:"org_id"`
	RunID           string                    `bson:"run_id"`
	Purpose         string                    `bson:"purpose"`
	SetID           string                    `bson:"set_id"`
	Model           string                    `bson:"model"`
	RequestKey      string                    `bson:"request_key"`
	StateHash       string                    `bson:"state_hash"`
	InputArtifactID string                    `bson:"input_artifact_id"`
	PolicyVersionID string                    `bson:"policy_version_id"`
	EvidenceIDs     []string                  `bson:"evidence_ids"`
	Answers         map[string]DecisionAnswer `bson:"answers"`
	Status          string                    `bson:"status"` // ok | unsure | error
	Why             string                    `bson:"why"`
	LatencyMS       int64                     `bson:"latency_ms"`
	Attempts        int                       `bson:"attempts"`
	RequestID       string                    `bson:"request_id"`
	CreatedAt       time.Time                 `bson:"created_at"`
}

func (d DecisionDoc) response() decision.Response {
	r := decision.Response{Model: d.Model, RequestID: d.RequestID, LatencyMS: d.LatencyMS, Attempts: d.Attempts, Answers: map[string]decision.Answer{}}
	for id, a := range d.Answers {
		r.Answers[id] = decision.Answer{Type: a.Type, Choice: a.Choice, Probabilities: a.Probabilities, Confidence: a.Confidence, Score: a.Score, Noul: a.Noul}
	}
	return r
}

// cachedDecision returns a successful earlier answer to the same request, so a crash between the answer and the
// stage commit never pays for Jev twice.
func (a *App) cachedDecision(ctx context.Context, org, run, requestKey string) (DecisionDoc, bool, error) {
	d, err := findOne[DecisionDoc](ctx, a.DB, "decisions", bson.M{"org_id": org, "run_id": run, "request_key": requestKey, "status": "ok"})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return DecisionDoc{}, false, nil
	}
	return d, err == nil, err
}

// triageCall asks Jev (or reuses the cached answer) and records the decision. A classified Jev failure is recorded
// and returned as (doc, nil response, error kind); the caller then uses the fixed rules.
func (s *stageCtx) triageCall(ctx context.Context, run RunDoc, state map[string]any, key string) (*decision.Response, *DecisionDoc, error) {
	a := s.a
	req, err := decision.NewTriageRequest(state)
	if err != nil {
		return nil, nil, err
	}
	reqKey := decision.RequestKey(req.Model, req.State, req.Questions)
	if cached, ok, e := a.cachedDecision(ctx, run.OrgID, run.ID, reqKey); e != nil {
		return nil, nil, e
	} else if ok {
		r := cached.response()
		return &r, &cached, nil
	}
	stateJSON := []byte(canon.JSON(state))
	blob, err := storeBlob(filepath.Join(a.Config.DataDir, "artifacts"), run.OrgID, bytes.NewReader(stateJSON), a.Config.ArtifactMaxBytes)
	if err != nil {
		return nil, nil, err
	}
	doc := DecisionDoc{ID: newID("dec"), OrgID: run.OrgID, RunID: run.ID, Purpose: "triage", SetID: req.SetID, Model: req.Model, RequestKey: reqKey, StateHash: blob.SHA256, PolicyVersionID: run.Pinned.PolicyVersionID, EvidenceIDs: []string{run.SnapshotID}, CreatedAt: time.Now().UTC()}
	resp, askErr := a.Decision.Ask(ctx, req, key)
	switch {
	case askErr == nil:
		doc.Status, doc.Answers = "ok", map[string]DecisionAnswer{}
		doc.RequestID, doc.LatencyMS, doc.Attempts = resp.RequestID, resp.LatencyMS, resp.Attempts
		for id, ans := range resp.Answers {
			doc.Answers[id] = DecisionAnswer{Type: ans.Type, Choice: ans.Choice, Probabilities: ans.Probabilities, Confidence: ans.Confidence, Score: ans.Score, Noul: ans.Noul}
		}
	case errors.Is(askErr, decision.ErrNoKey), errors.Is(askErr, decision.ErrTimeout), errors.Is(askErr, decision.ErrRefused), errors.Is(askErr, decision.ErrInvalid), errors.Is(askErr, decision.ErrUnavailable):
		doc.Status, doc.Why = "error", decisionWhy(askErr)
	default:
		return nil, nil, askErr // cancellation: the stage is abandoned
	}
	err = a.fenced(ctx, s.lease, func(sc context.Context) error {
		art, e := a.recordArtifact(sc, run.OrgID, blob, "application/json", "decision_input", Subject{"run", run.ID})
		if e != nil {
			return e
		}
		doc.InputArtifactID = art.ID
		raw, e := bson.Marshal(doc)
		if e != nil {
			return e
		}
		var fields bson.M
		if e = bson.Unmarshal(raw, &fields); e != nil {
			return e
		}
		delete(fields, "_id")
		if _, e = a.DB.Coll("decisions").UpdateOne(sc, bson.M{"org_id": run.OrgID, "run_id": run.ID, "request_key": reqKey}, bson.M{"$set": fields, "$setOnInsert": bson.M{"_id": doc.ID}}, options.UpdateOne().SetUpsert(true)); e != nil {
			return e
		}
		return a.AppendEvent(sc, run.OrgID, "decision.recorded", Subject{"run", run.ID}, bson.M{"run_id": run.ID, "status": doc.Status, "source": "jev"}, nil, nil, true)
	})
	if err != nil {
		return nil, nil, err
	}
	if doc.Status != "ok" {
		return nil, &doc, nil
	}
	return &resp, &doc, nil
}

func decisionWhy(err error) string {
	switch {
	case errors.Is(err, decision.ErrNoKey):
		return "no_key"
	case errors.Is(err, decision.ErrTimeout):
		return "timeout"
	case errors.Is(err, decision.ErrRefused):
		return "key_refused"
	case errors.Is(err, decision.ErrInvalid):
		return "invalid_answer"
	}
	return "unavailable"
}

// jevKey reads the organization's Jev key at call time. It returns ok=false when none is set.
func (a *App) jevKey(ctx context.Context, org string) (string, bool, error) {
	d, err := findOne[CatherdSecretDoc](ctx, a.DB, "catherd_secrets", bson.M{"org_id": org, "kind": "jev", "active": true})
	if errors.Is(err, mongo.ErrNoDocuments) || err == nil && (!d.isSet() || d.CredentialRef == nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	v, ok, err := a.Creds.Get(ctx, *d.CredentialRef)
	if err != nil || !ok {
		return "", false, nil
	}
	return v, true, nil
}
