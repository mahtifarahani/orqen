// Package testfakes serves deterministic GitLab and Jira REST fakes under /gitlab and /jira.
// It is used by tests and by the development-only cmd/orqen-fakes.
package testfakes

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

type tracker struct {
	mu          sync.Mutex
	log         []string
	inflight    int
	maxInflight int
}

func (t *tracker) enter(r *http.Request) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.log = append(t.log, r.URL.EscapedPath())
	t.inflight++
	t.maxInflight = max(t.maxInflight, t.inflight)
}
func (t *tracker) leave() { t.mu.Lock(); t.inflight--; t.mu.Unlock() }

// Requests returns the escaped paths served so far.
func (t *tracker) Requests() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string{}, t.log...)
}

// MaxInflight is the highest number of simultaneous requests seen.
func (t *tracker) MaxInflight() int { t.mu.Lock(); defer t.mu.Unlock(); return t.maxInflight }
func (t *tracker) Reset()           { t.mu.Lock(); t.log, t.maxInflight = nil, 0; t.mu.Unlock() }
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func intParam(r *http.Request, key string, fallback int) int {
	if n, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil {
		return n
	}
	return fallback
}
func segments(escaped, prefix string) ([]string, bool) {
	rest, ok := strings.CutPrefix(escaped, prefix)
	if !ok {
		return nil, false
	}
	return strings.Split(strings.Trim(rest, "/"), "/"), true
}
