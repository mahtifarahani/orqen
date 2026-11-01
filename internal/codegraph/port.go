// Package codegraph is the code-graph port (SPEC section 11): the derived code index of a repository at one commit.
// The CLI adapter speaks an ASSUMED GitNexus contract (v1): GitNexus is not installed here, so every output is
// validated against schema/v1 and the deterministic Fake is the executable spec. Nothing of GitNexus is bundled or
// fetched, and the index stays on disk; MongoDB only holds its status.
package codegraph

import "context"

// SchemaVersion labels the contract: assumed, not verified against a real GitNexus.
const SchemaVersion = "assumed-v1"

// Target kinds of an impact query.
const (
	KindFile   = "file"
	KindSymbol = "symbol"
)

// Relations an impact entry can carry.
const (
	RelImports    = "imports"
	RelImportedBy = "imported_by"
)

// Health is the `--version --json` answer.
type Health struct {
	Version string `json:"version"`
	Schema  string `json:"schema"`
}

// AnalyzeResult is the answer of an index run.
type AnalyzeResult struct {
	Files   int `json:"files"`
	Symbols int `json:"symbols"`
	Bytes   int `json:"bytes,omitempty"`
}

// Impacted is one file (or symbol) affected by a change to the target.
type Impacted struct {
	Path     string `json:"path"`
	Symbol   string `json:"symbol,omitempty"`
	Relation string `json:"relation"`
	Depth    int    `json:"depth"`
}

// ImpactResult is the answer of an impact query.
type ImpactResult struct {
	Target    string     `json:"target"`
	Items     []Impacted `json:"items"`
	Truncated bool       `json:"truncated"`
}

// ContextResult is the answer of a context query for a file.
type ContextResult struct {
	Target     string   `json:"target"`
	Imports    []string `json:"imports"`
	ImportedBy []string `json:"imported_by"`
	Symbols    []string `json:"symbols"`
}

// Index is the port. tree and out are absolute paths inside the org's codegraph directory; typed errors are *Error.
type Index interface {
	Version(ctx context.Context) (Health, error)
	// Analyze indexes the disposable tree into the index directory out.
	Analyze(ctx context.Context, tree, out string) (AnalyzeResult, error)
	Impact(ctx context.Context, index, target, kind string, depth, limit int) (ImpactResult, error)
	Context(ctx context.Context, index, target string, limit int) (ContextResult, error)
}
