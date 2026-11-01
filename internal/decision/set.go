package decision

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"sort"

	"orqen/internal/canon"
)

//go:embed triage_v1.json
var triageJSON []byte

// TriageModel is the pinned Jev model (like catherd's catalog/jev.json).
const TriageModel = "jev-1.13.0"

type setFile struct {
	Schema    int                        `json:"schema"`
	Model     string                     `json:"model"`
	Questions map[string]json.RawMessage `json:"questions"`
}
type rawQuestion struct {
	Type         string          `json:"type"`
	Instructions string          `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria"`
}

// TriageQuestions returns the embedded triage-v1 question set.
func TriageQuestions() (map[string]Question, error) {
	var f setFile
	if err := json.Unmarshal(triageJSON, &f); err != nil {
		return nil, err
	}
	out := make(map[string]Question, len(f.Questions))
	for id, raw := range f.Questions {
		var rq rawQuestion
		if err := json.Unmarshal(raw, &rq); err != nil {
			return nil, err
		}
		q := Question{Type: rq.Type, Instructions: rq.Instructions}
		switch rq.Type {
		case "choice":
			if err := json.Unmarshal(rq.Criteria, &q.Choice); err != nil {
				return nil, err
			}
		case "score":
			if err := json.Unmarshal(rq.Criteria, &q.Score); err != nil {
				return nil, err
			}
		case "noul":
			var c struct {
				True  string `json:"true"`
				False string `json:"false"`
			}
			if err := json.Unmarshal(rq.Criteria, &c); err != nil {
				return nil, err
			}
			q.Noul = &[2]string{c.True, c.False}
		}
		out[id] = q
	}
	return out, nil
}
func sha(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

// QuestionIDs lists the question ids in a stable order.
func QuestionIDs(qs map[string]Question) []string {
	ids := make([]string, 0, len(qs))
	for id := range qs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// SetID is "triage#" plus the first 8 hex digits over the model and the questions: any change names a new set.
func SetID(model string, qs map[string]Question) string {
	return "triage#" + sha(canon.JSON(map[string]any{"model": model, "questions": qs}))[:8]
}

// RequestKey identifies one request (model, scrubbed state, questions) for the decision cache.
func RequestKey(model string, state map[string]any, qs map[string]Question) string {
	return sha(canon.JSON(map[string]any{"model": model, "state": state, "questions": qs}))
}

// NewTriageRequest builds the request for a scrubbed state.
func NewTriageRequest(state map[string]any) (Request, error) {
	qs, err := TriageQuestions()
	if err != nil {
		return Request{}, err
	}
	return Request{SetID: SetID(TriageModel, qs), Model: TriageModel, State: state, Questions: qs}, nil
}
