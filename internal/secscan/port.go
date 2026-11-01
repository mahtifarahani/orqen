// Package secscan is the security-scan port (SPEC sections 12 and 13): a Strix-style scanner run against a
// disposable copy of the reviewed head. The CLI adapter speaks an ASSUMED contract (v1): Strix is not installed here,
// so the report is validated against schema/v1 and the deterministic Fake is the executable spec. A scan never runs in
// a coder worktree, never publishes anything, and only a complete scan can pass a gate.
package secscan

import "context"

// SchemaVersion labels the contract: assumed, not verified against a real Strix.
const SchemaVersion = "assumed-v1"

// Health is what `--version` printed (free text; the first line is kept).
type Health struct {
	Version string `json:"version"`
}

// Request is one scan. Tree and ReportDir are absolute paths inside the org's scans directory. Model, LLMKey,
// APIBase and DockerHost reach the child only as the allowlisted environment variables.
type Request struct {
	Tree       string
	ReportDir  string
	MaxTurns   int
	BudgetUSD  float64 // 0 = no budget flag (the user chose unlimited)
	Model      string
	LLMKey     string // secret: held in memory for this call only
	APIBase    string
	DockerHost string
}

// RawResult is what one scan produced, before classification. Stdout is scrubbed of the key and secret patterns.
type RawResult struct {
	Exit       int
	TimedOut   bool
	Stdout     string
	StderrTail string
	Report     []byte // contents of <ReportDir>/report.json; nil when missing
}

// Scanner is the port.
type Scanner interface {
	Version(ctx context.Context) (Health, error)
	Scan(ctx context.Context, req Request) (RawResult, error)
}
