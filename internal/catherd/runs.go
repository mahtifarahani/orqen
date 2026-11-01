package catherd

import (
	"encoding/json"
	"fmt"
	"time"
)

// RunStatuses are the statuses catherd records for a finished role.
var RunStatuses = []string{"ok", "failed", "limit", "cli-too-old", "timeout", "cancelled"}

// Tokens is the usage catherd counted for one role run.
type Tokens struct {
	Input  int64 `json:"input"`
	Cached int64 `json:"cached"`
	Output int64 `json:"output"`
}

// RunRecord mirrors catherd's RunRecordSchema (domain/record.ts). The required fields are checked strictly;
// fields Orqen does not read are kept in Extra.
type RunRecord struct {
	Schema       int      `json:"schema"`
	RunID        string   `json:"runId"`
	DispatchID   string   `json:"dispatchId"`
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	Lane         *string  `json:"lane"`
	Backend      string   `json:"backend"`
	Rung         string   `json:"rung"`
	Attempt      int      `json:"attempt"`
	FailoverFrom *string  `json:"failoverFrom"`
	Thread       *string  `json:"thread"`
	Status       string   `json:"status"`
	StartedAt    string   `json:"startedAt"`
	EndedAt      string   `json:"endedAt"`
	Secs         float64  `json:"secs"`
	ExitCode     *int     `json:"exitCode"`
	Signal       *string  `json:"signal"`
	CLIVersion   *string  `json:"cliVersion"`
	Tokens       Tokens   `json:"tokens"`
	CostUSD      *float64 `json:"costUsd"`
	ChangedOwned []string `json:"changedOwned"`
	Violations   []string `json:"violations"`
	GitUnavail   bool     `json:"gitUnavailable,omitempty"`
	ReplyStatus  *string  `json:"replyStatus"`
	ReplyWhy     *string  `json:"replyWhy"`
	Environment  string   `json:"environment,omitempty"`
	ThreadHeavy  bool     `json:"threadHeavy"`
	Access       string   `json:"access"`
	Isolated     bool     `json:"isolated"`
	Error        *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	ReplyPath string `json:"replyPath"`
	Host      string `json:"host,omitempty"`

	// Extra holds every field Orqen does not model.
	Extra map[string]json.RawMessage `json:"-"`
}

var recordKnown = map[string]bool{}

func init() {
	var probe map[string]json.RawMessage
	b, _ := json.Marshal(RunRecord{})
	_ = json.Unmarshal(b, &probe)
	for k := range probe {
		recordKnown[k] = true
	}
	for _, k := range []string{"schema", "images", "network", "sessionId"} {
		recordKnown[k] = true
	}
}

// requiredRecordKeys must be present (null allowed only where catherd's schema allows it).
var requiredRecordKeys = []string{"schema", "runId", "dispatchId", "name", "role", "backend", "rung", "attempt", "status", "startedAt", "endedAt", "secs", "tokens", "changedOwned", "violations", "replyPath"}

// ParseRunRecord decodes one record and checks the fields Orqen relies on.
func ParseRunRecord(raw json.RawMessage) (RunRecord, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return RunRecord{}, fmt.Errorf("run record: %w", err)
	}
	for _, k := range requiredRecordKeys {
		if v, ok := m[k]; !ok || string(v) == "null" {
			return RunRecord{}, fmt.Errorf("run record: missing %s", k)
		}
	}
	var schema int
	if json.Unmarshal(m["schema"], &schema) != nil || schema != 1 {
		return RunRecord{}, fmt.Errorf("run record: unsupported schema %s", m["schema"])
	}
	var r RunRecord
	if err := json.Unmarshal(raw, &r); err != nil {
		return RunRecord{}, fmt.Errorf("run record: %w", err)
	}
	if !has(RunStatuses, r.Status) {
		return RunRecord{}, fmt.Errorf("run record: unknown status %q", r.Status)
	}
	if r.Attempt < 1 || r.RunID == "" || r.DispatchID == "" || r.Name == "" {
		return RunRecord{}, fmt.Errorf("run record: invalid identity")
	}
	r.Extra = map[string]json.RawMessage{}
	for k, v := range m {
		if !recordKnown[k] {
			r.Extra[k] = v
		}
	}
	return r, nil
}

// Dispatch is one role dispatch from `runs show --debug`: admission data and its record, once finished.
type Dispatch struct {
	Name       string     `json:"name"`
	DispatchID string     `json:"dispatchId"`
	Rung       string     `json:"rung"`
	AdmittedAt string     `json:"admittedAt"`
	Record     *RunRecord `json:"-"`
	ExitReason string     `json:"-"`
}

// AdmittedTime parses AdmittedAt.
func (d Dispatch) AdmittedTime() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, d.AdmittedAt)
	return t
}

// RunView is what `catherd runs show <id> --json --debug` reports about a run's roles.
type RunView struct {
	Records    []RunRecord
	Dispatches []Dispatch
	// Landed lists the milestones the run's ledger holds.
	Landed []string
}

// RecordFor returns the record of a dispatch, if finished.
func (v RunView) RecordFor(dispatchID string) (RunRecord, bool) {
	for _, r := range v.Records {
		if r.DispatchID == dispatchID {
			return r, true
		}
	}
	return RunRecord{}, false
}

// ParseRunView decodes the JSON of `runs show --json --debug`. A dispatch whose record is not in `records` yet but
// that carries one under "record" is completed from it.
func ParseRunView(b []byte) (RunView, error) {
	var top struct {
		Summary struct {
			Milestones []string `json:"milestones"`
		} `json:"summary"`
		Records    []json.RawMessage `json:"records"`
		Dispatches []struct {
			Name       string          `json:"name"`
			DispatchID string          `json:"dispatchId"`
			Rung       string          `json:"rung"`
			AdmittedAt string          `json:"admittedAt"`
			Record     json.RawMessage `json:"record"`
			Exit       *struct {
				Reason string `json:"reason"`
			} `json:"exit"`
		} `json:"dispatches"`
	}
	if err := json.Unmarshal(b, &top); err != nil {
		return RunView{}, err
	}
	v := RunView{Landed: top.Summary.Milestones}
	for _, raw := range top.Records {
		r, err := ParseRunRecord(raw)
		if err != nil {
			return RunView{}, err
		}
		v.Records = append(v.Records, r)
	}
	for _, d := range top.Dispatches {
		x := Dispatch{Name: d.Name, DispatchID: d.DispatchID, Rung: d.Rung, AdmittedAt: d.AdmittedAt}
		if d.Exit != nil {
			x.ExitReason = d.Exit.Reason
		}
		if r, ok := v.RecordFor(d.DispatchID); ok {
			cp := r
			x.Record = &cp
		} else if len(d.Record) > 0 && string(d.Record) != "null" {
			r, err := ParseRunRecord(d.Record)
			if err != nil {
				return RunView{}, err
			}
			v.Records = append(v.Records, r)
			x.Record = &r
		}
		v.Dispatches = append(v.Dispatches, x)
	}
	return v, nil
}
