// Package hostfake is the fake Codex host used by Sprint 4 tests and by cmd/orqen-fakehost: an in-memory (or
// file-backed) catherd run engine plus a fake Codex App Server that executes Orqen's directives against it.
package hostfake

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// LaneSpec is one lane of the architect's plan.
type LaneSpec struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Owns  []string `json:"owns"`
	After []string `json:"after"`
}

// FindingSpec is one reviewer finding.
type FindingSpec struct {
	ID            string `json:"id"`
	Severity      string `json:"severity"`
	File          string `json:"file"`
	Line          int    `json:"line"`
	Side          string `json:"side"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	AcceptanceRef string `json:"acceptance_ref"`
}

// ReviewSpec is what one reviewer dispatch answers.
type ReviewSpec struct {
	Verdict  string        `json:"verdict"` // approved | changes_requested | approved_with_scope
	Scope    string        `json:"scope"`
	Findings []FindingSpec `json:"findings"`
}

// ErrSpec is an injected catherd tool error.
type ErrSpec struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fix     string `json:"fix"`
}

// Scenario is pure data (so the fake binary can reload it from a file) plus a few Go-only hooks.
type Scenario struct {
	// RecordDelayMs is how long after admission a role's record appears.
	RecordDelayMs int `json:"record_delay_ms"`
	// Rungs maps a role to the rung routed for it. Defaults keep worker and reviewer in different families.
	Rungs map[string]string `json:"rungs"`
	// WorkerLadder is the worker's ladder; climb moves up it.
	WorkerLadder []string   `json:"worker_ladder"`
	Lanes        []LaneSpec `json:"lanes"`
	Goal         string     `json:"goal"`
	// WorkerEdits[i] is applied by the i-th dispatch of the same worker name (the last entry repeats).
	WorkerEdits []map[string]string `json:"worker_edits"`
	// NoChange makes workers edit nothing.
	NoChange bool `json:"no_change"`
	// LieRounds lists worker dispatch indexes whose reply claims success while the edit breaks the check.
	LieRounds []int `json:"lie_rounds"`
	// VerifierVerdicts[i] is the verdict of the i-th verifier dispatch: PASS, FAIL or BLOCKED.
	VerifierVerdicts []string `json:"verifier_verdicts"`
	// Reviews[i] is the answer of the i-th reviewer dispatch (the last entry repeats).
	Reviews []ReviewSpec `json:"reviews"`
	// MalformedReviews makes the first N reviewer replies unparseable.
	MalformedReviews int `json:"malformed_reviews"`
	// RoleStatuses maps a role name to the status of its n-th dispatch ("" = ok): failed, limit, timeout.
	RoleStatuses map[string][]string `json:"role_statuses"`
	// FailoverTo maps a role to the rung its record shows instead of the routed one (a failover stand-in).
	FailoverTo map[string]string `json:"failover_to"`
	// Budget is the number of dispatches after which catherd refuses with E_RUN_BUDGET (0 = none).
	Budget int `json:"budget"`
	// AdmitErrors maps a tool to a sequence of errors returned by its successive calls ("" code = no error).
	AdmitErrors map[string][]ErrSpec `json:"admit_errors"`
	// Inject maps a tool to a sequence of app-server behaviours for its successive directives: "" (none),
	// no_tool_call, wrong_args, extra_command, hang, hang_after_admit, crash_after_admit, tool_error:<CODE>.
	Inject map[string][]string `json:"inject"`
	// UnreadableHistory makes thread/resume fail.
	UnreadableHistory bool `json:"unreadable_history"`
	// WriterReply and KVerifyReply answer the detached knowledge author's roles (S7). Empty = a valid default built from
	// the brief ("<n>" in a list means the n-th dispatch of that role; the last entry repeats).
	WriterReply  []string `json:"writer_reply"`
	KVerifyReply []string `json:"kverify_reply"`
	// WorkerPushAttempt makes every worker run `git push origin` in its worktree with PushEnv.
	WorkerPushAttempt bool `json:"worker_push_attempt"`

	// WorkerFn replaces the default worker behaviour.
	WorkerFn func(index int, worktree string) (reply string, edits map[string]string) `json:"-"`
	// ReviewerFn replaces the scripted reviews.
	ReviewerFn func(index int) string `json:"-"`
	// PushEnv is the environment a WorkerPushAttempt uses (set by the test: the real host environment).
	PushEnv func() []string `json:"-"`
}

// DefaultScenario is one lane, a worker that makes the check pass, a passing verifier and an approving reviewer.
func DefaultScenario() *Scenario {
	s := &Scenario{}
	s.norm()
	return s
}

func (s *Scenario) norm() {
	if s.Rungs == nil {
		s.Rungs = map[string]string{}
	}
	for role, rung := range map[string]string{"architect": "codex:gpt-6.1-sol#high", "worker": "codex:gpt-6-sol#medium", "verifier": "codex:gpt-6-luna#medium", "reviewer": "codex:gpt-6.1-sol#high", "writer": "codex:gpt-6-sol#medium"} {
		if s.Rungs[role] == "" {
			s.Rungs[role] = rung
		}
	}
	if len(s.WorkerLadder) == 0 {
		s.WorkerLadder = []string{s.Rungs["worker"], "codex:gpt-6-sol#high"}
	}
	if len(s.Lanes) == 0 {
		s.Lanes = []LaneSpec{{ID: "M1.L1", Title: "Implement the change", Owns: []string{"src/"}}}
	}
	if s.Goal == "" {
		s.Goal = "Implement the requested change."
	}
	if len(s.WorkerEdits) == 0 {
		s.WorkerEdits = []map[string]string{{"src/feature.txt": "orqen-feature\n"}}
	}
}

// DeleteFile is the edit content that removes a file.
const DeleteFile = "<<delete>>"

// workerEdits are the edits of the i-th dispatch of a worker name. Past the scripted list the last entry repeats and
// a marker file makes every later round a real change (an identical rewrite would be an empty commit).
func (s *Scenario) workerEdits(i int) map[string]string {
	if i < len(s.WorkerEdits) {
		return s.WorkerEdits[i]
	}
	out := map[string]string{}
	for k, v := range s.WorkerEdits[len(s.WorkerEdits)-1] {
		out[k] = v
	}
	out[fmt.Sprintf("src/fix-%d.txt", i)] = fmt.Sprintf("fix round %d\n", i)
	return out
}

func (s *Scenario) review(i int) ReviewSpec {
	if len(s.Reviews) == 0 {
		return ReviewSpec{Verdict: "approved"}
	}
	if i >= len(s.Reviews) {
		i = len(s.Reviews) - 1
	}
	return s.Reviews[i]
}

func (s *Scenario) verdict(i int) string {
	if i < len(s.VerifierVerdicts) {
		return strings.ToUpper(s.VerifierVerdicts[i])
	}
	return "PASS"
}

func (s *Scenario) lies(i int) bool {
	for _, n := range s.LieRounds {
		if n == i {
			return true
		}
	}
	return false
}

// seq returns the n-th entry of a per-tool sequence, or "" past its end.
func seqAt[T any](m map[string][]T, key string, n int) (T, bool) {
	var zero T
	list := m[key]
	if n < len(list) {
		return list[n], true
	}
	return zero, false
}

// LoadScenario reads a scenario file (the fake binary reloads it on every call).
func LoadScenario(path string) (*Scenario, error) {
	s := &Scenario{}
	if b, err := os.ReadFile(path); err == nil {
		if err = json.Unmarshal(b, s); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	s.norm()
	return s, nil
}
