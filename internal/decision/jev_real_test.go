package decision

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestJevReal makes one real inference. It never runs unless the user sets ORQEN_TEST_JEV=1 and TYPESAFE_API_KEY:
//
//	read -rs TYPESAFE_API_KEY && export TYPESAFE_API_KEY && ORQEN_TEST_JEV=1 go test ./internal/decision -run TestJevReal -count=1 -v
//
// It asserts a typed valid Response for a fixed synthetic state, or a classified error, and prints neither the key
// nor the state.
func TestJevReal(t *testing.T) {
	if os.Getenv("ORQEN_TEST_JEV") != "1" {
		t.Skip("set ORQEN_TEST_JEV=1 (and TYPESAFE_API_KEY) to make one real Jev call")
	}
	key := os.Getenv("TYPESAFE_API_KEY")
	if key == "" {
		t.Skip("TYPESAFE_API_KEY is not set")
	}
	r, err := NewTriageRequest(map[string]any{"title": "Add a CSV export button", "issue_type": "Task", "acceptance": "The export button downloads a CSV with all visible columns.", "description": "Users need to export the table.", "repo": map[string]any{"package_path": "", "has_test_command": true}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	resp, err := NewJev(Options{}).Ask(ctx, r, key)
	switch {
	case err == nil:
		if len(resp.Answers) != len(r.Questions) || resp.Model == "" {
			t.Fatal("incomplete response")
		}
		t.Logf("ok: model %s, attempts %d, latency %d ms", resp.Model, resp.Attempts, resp.LatencyMS)
	case errors.Is(err, ErrRefused), errors.Is(err, ErrTimeout), errors.Is(err, ErrUnavailable), errors.Is(err, ErrInvalid):
		t.Logf("classified error: %v", err)
	default:
		t.Fatal(err)
	}
}
