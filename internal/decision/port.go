// Package decision is the bounded decision adapter: it asks Jev typed questions about a task and returns typed
// answers. It never writes policy, acceptance or approvals; callers only read its answers. It must not import
// internal/server.
package decision

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
)

// Errors a caller classifies. Their messages never carry the key or the request state.
var (
	ErrNoKey       = errors.New("decision: no key")
	ErrTimeout     = errors.New("decision: timeout")
	ErrRefused     = errors.New("decision: key refused")
	ErrInvalid     = errors.New("decision: invalid answer")
	ErrUnavailable = errors.New("decision: unavailable")
)

// Question is one typed question. Exactly one of Choice, Score or Noul is used, matching Type.
type Question struct {
	Type         string
	Instructions string
	Choice       map[string]string // choice: option -> meaning
	Score        []string          // score: ordered level meanings
	Noul         *[2]string        // noul: [meaning of true, meaning of false]
}

// MarshalJSON renders the wire shape {type, instructions, criteria}.
func (q Question) MarshalJSON() ([]byte, error) {
	out := map[string]any{"type": q.Type, "instructions": q.Instructions}
	switch q.Type {
	case "choice":
		out["criteria"] = q.Choice
	case "score":
		out["criteria"] = q.Score
	case "noul":
		if q.Noul != nil {
			out["criteria"] = map[string]string{"true": q.Noul[0], "false": q.Noul[1]}
		}
	}
	return json.Marshal(out)
}

// Request is one Jev call. State must already be scrubbed and bounded by the caller.
type Request struct {
	SetID     string
	Model     string
	State     map[string]any
	Questions map[string]Question
}

// Answer is one validated answer. Score is the expected level for a score question.
type Answer struct {
	Type          string
	Choice        string
	Probabilities map[string]float64
	Confidence    float64
	Score         float64
	Noul          float64
}
type Response struct {
	Model        string
	RequestID    string
	Answers      map[string]Answer
	LatencyMS    int64
	Attempts     int
	InputTokens  int
	OutputTokens int
}

// Port asks Jev. key is the API key, read by the caller at call time; implementations never store or log it.
type Port interface {
	Ask(ctx context.Context, r Request, key string) (Response, error)
}

// Fake answers from a function and counts calls.
type Fake struct {
	Fn    func(Request) (Response, error)
	Calls atomic.Int64
}

func (f *Fake) Ask(_ context.Context, r Request, key string) (Response, error) {
	f.Calls.Add(1)
	if key == "" {
		return Response{}, ErrNoKey
	}
	if f.Fn == nil {
		return Response{}, ErrUnavailable
	}
	return f.Fn(r)
}
