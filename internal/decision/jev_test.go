package decision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const sentinel = "sk-jev-SENTINEL-1234567890"

func request(t *testing.T) Request {
	t.Helper()
	r, err := NewTriageRequest(map[string]any{"title": "Add CSV export"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// validReply answers every question: the first option of a choice (sorted), 0.1 for a noul.
func validReply(t *testing.T, r Request, mutate func(map[string]any)) []byte {
	t.Helper()
	answers := map[string]any{}
	for id, q := range r.Questions {
		switch q.Type {
		case "choice":
			choice := ""
			for opt := range q.Choice {
				if choice == "" || opt < choice {
					choice = opt
				}
			}
			answers[id] = map[string]any{"type": "choice", "choice": choice, "probabilities": map[string]float64{choice: 0.9}, "confidence": 0.8}
		case "noul":
			answers[id] = map[string]any{"type": "noul", "noul": 0.1}
		}
	}
	if mutate != nil {
		mutate(answers)
	}
	b, _ := json.Marshal(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 100, "output_tokens": 20}})
	return b
}
func noSleep(rec *[]time.Duration) func(context.Context, time.Duration) error {
	var mu sync.Mutex
	return func(_ context.Context, d time.Duration) error {
		mu.Lock()
		*rec = append(*rec, d)
		mu.Unlock()
		return nil
	}
}

func TestJevRequestShapeAndAnswers(t *testing.T) {
	r := request(t)
	var got struct {
		auth, ctype, path, retry string
		body                     map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got.auth, got.ctype, got.path, got.retry = req.Header.Get("Authorization"), req.Header.Get("Content-Type"), req.URL.Path, req.Header.Get("x-typesafe-retry-count")
		b, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(b, &got.body)
		w.Header().Set("x-typesafe-request-id", "req-42")
		_, _ = w.Write(validReply(t, r, nil))
	}))
	defer srv.Close()
	resp, err := NewJev(Options{Base: srv.URL}).Ask(context.Background(), r, sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if got.auth != "Bearer "+sentinel || got.ctype != "application/json" || got.path != "/v1/systemone" || got.retry != "" {
		t.Fatal(got)
	}
	if got.body["model"] != "jev-1.13.0" || got.body["state"].(map[string]any)["title"] != "Add CSV export" {
		t.Fatal(got.body)
	}
	qs := got.body["questions"].(map[string]any)
	if len(qs) != 10 || qs["testable"].(map[string]any)["criteria"].(map[string]any)["true"] == nil || qs["clarity"].(map[string]any)["criteria"].(map[string]any)["clear"] == nil {
		t.Fatal(qs)
	}
	if resp.RequestID != "req-42" || resp.Attempts != 1 || resp.InputTokens != 100 || resp.Answers["clarity"].Choice != "ambiguous" || resp.Answers["testable"].Noul != 0.1 || resp.Model != "jev-1.13.0" {
		t.Fatalf("%+v", resp)
	}
	if !strings.HasPrefix(r.SetID, "triage#") || len(r.SetID) != len("triage#")+8 {
		t.Fatal(r.SetID)
	}
}
func TestJevRetriesHonourRetryAfterAndCountHeader(t *testing.T) {
	r := request(t)
	var mu sync.Mutex
	var counts []string
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		n++
		counts = append(counts, req.Header.Get("x-typesafe-retry-count"))
		call := n
		mu.Unlock()
		switch call {
		case 1:
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(429)
		case 2:
			w.WriteHeader(503)
		default:
			_, _ = w.Write(validReply(t, r, nil))
		}
	}))
	defer srv.Close()
	var sleeps []time.Duration
	resp, err := NewJev(Options{Base: srv.URL, Sleep: noSleep(&sleeps)}).Ask(context.Background(), r, sentinel)
	if err != nil || resp.Attempts != 3 {
		t.Fatal(resp.Attempts, err)
	}
	if len(sleeps) != 2 || sleeps[0] != 3*time.Second || sleeps[1] < 300*time.Millisecond || sleeps[1] > 800*time.Millisecond*2 {
		t.Fatal(sleeps)
	}
	if strings.Join(counts, ",") != ",1,2" {
		t.Fatal(counts)
	}
	// Retry-After is capped at 10 seconds.
	n = 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(429)
			return
		}
		_, _ = w.Write(validReply(t, r, nil))
	}))
	defer srv2.Close()
	sleeps = nil
	if _, err = NewJev(Options{Base: srv2.URL, Sleep: noSleep(&sleeps), Deadline: time.Minute}).Ask(context.Background(), r, sentinel); err != nil || sleeps[0] != 10*time.Second {
		t.Fatal(sleeps, err)
	}
	// Exhausted retries on a 500 end as unavailable, with exactly three attempts.
	hits := 0
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { hits++; w.WriteHeader(500) }))
	defer srv3.Close()
	sleeps = nil
	_, err = NewJev(Options{Base: srv3.URL, Sleep: noSleep(&sleeps)}).Ask(context.Background(), r, sentinel)
	if !errors.Is(err, ErrUnavailable) || hits != 3 {
		t.Fatal(err, hits)
	}
}
func TestJevDeadlineAndAttemptTimeout(t *testing.T) {
	r := request(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case <-time.After(400 * time.Millisecond):
		case <-req.Context().Done():
		}
	}))
	defer srv.Close()
	start := time.Now()
	_, err := NewJev(Options{Base: srv.URL, Attempt: 50 * time.Millisecond, Deadline: 120 * time.Millisecond, Sleep: func(context.Context, time.Duration) error { return nil }}).Ask(context.Background(), r, sentinel)
	if !errors.Is(err, ErrTimeout) || time.Since(start) > time.Second {
		t.Fatal(err, time.Since(start))
	}
}
func TestJevRejectsInvalidAnswers(t *testing.T) {
	r := request(t)
	cases := map[string]func(map[string]any){
		"wrong type": func(a map[string]any) { a["clarity"] = map[string]any{"type": "noul", "noul": 0.5} },
		"unknown choice": func(a map[string]any) {
			a["scope"] = map[string]any{"type": "choice", "choice": "all_repos", "probabilities": map[string]float64{"all_repos": 1}}
		},
		"unknown option p": func(a map[string]any) {
			a["scope"] = map[string]any{"type": "choice", "choice": "single_repo", "probabilities": map[string]float64{"zzz": 1}}
		},
		"noul out of range": func(a map[string]any) { a["testable"] = map[string]any{"type": "noul", "noul": 1.5} },
		"missing answer":    func(a map[string]any) { delete(a, "already_done") },
	}
	for name, mutate := range cases {
		body := validReply(t, r, mutate)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write(body) }))
		_, err := NewJev(Options{Base: srv.URL}).Ask(context.Background(), r, sentinel)
		srv.Close()
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	defer srv.Close()
	if _, err := NewJev(Options{Base: srv.URL}).Ask(context.Background(), r, sentinel); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
func TestJevRefusedAndTheKeyNeverAppearsInErrors(t *testing.T) {
	r := request(t)
	for _, status := range []int{401, 403} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// A hostile server echoes the key and the state.
			http.Error(w, "bad key "+req.Header.Get("Authorization")+" state Add CSV export", status)
		}))
		_, err := NewJev(Options{Base: srv.URL}).Ask(context.Background(), r, sentinel)
		srv.Close()
		if !errors.Is(err, ErrRefused) || strings.Contains(err.Error(), "SENTINEL") || strings.Contains(err.Error(), "CSV") {
			t.Fatal(status, err)
		}
	}
	// A validation rejection (400) is not retried and leaks nothing.
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { hits++; http.Error(w, "state: Add CSV export", 400) }))
	defer srv.Close()
	_, err := NewJev(Options{Base: srv.URL}).Ask(context.Background(), r, sentinel)
	if !errors.Is(err, ErrInvalid) || hits != 1 || strings.Contains(err.Error(), "CSV") {
		t.Fatal(err, hits)
	}
	// A connection error does not echo the URL or key either.
	_, err = NewJev(Options{Base: "http://127.0.0.1:1", Sleep: func(context.Context, time.Duration) error { return nil }}).Ask(context.Background(), r, sentinel)
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "SENTINEL") {
		t.Fatal(err)
	}
	if _, err = NewJev(Options{}).Ask(context.Background(), r, ""); !errors.Is(err, ErrNoKey) {
		t.Fatal(err)
	}
	f := &Fake{Fn: func(Request) (Response, error) { return Response{Model: "m"}, nil }}
	if _, err = f.Ask(context.Background(), r, ""); !errors.Is(err, ErrNoKey) || f.Calls.Load() != 1 {
		t.Fatal(err)
	}
}
func TestQuestionSetIsStable(t *testing.T) {
	qs, err := TriageQuestions()
	if err != nil || len(qs) != 10 {
		t.Fatal(len(qs), err)
	}
	a := SetID(TriageModel, qs)
	if a != SetID(TriageModel, qs) || a == SetID("jev-9", qs) {
		t.Fatal(a)
	}
	s1 := map[string]any{"a": 1}
	if RequestKey("m", s1, qs) != RequestKey("m", map[string]any{"a": 1}, qs) || RequestKey("m", s1, qs) == RequestKey("m", map[string]any{"a": 2}, qs) {
		t.Fatal("request key")
	}
}
