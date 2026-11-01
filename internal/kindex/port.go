// Package kindex is the KnowledgeIndex port (SPEC section 16): the Wiki projection of verified knowledge. The Wiki is
// derived and rebuildable; MongoDB stays canonical. The CLI adapter speaks an ASSUMED contract (v1): the OpenClaw
// Wiki CLI is not available offline, so every output is validated against schema/v1 and the deterministic Fake is
// the executable spec.
package kindex

import "context"

// Vault names one organization's vault. Dir must live inside <data>/wiki/<OrgID>/.
type Vault struct {
	OrgID string
	Dir   string
}

type Health struct {
	Version string `json:"version"`
	Schema  string `json:"schema"`
}
type Doc struct {
	ID     string `json:"id"`
	Hash   string `json:"content_hash,omitempty"`
	Reason string `json:"reason,omitempty"`
}
type IngestResult struct {
	Accepted []Doc `json:"accepted"`
	Rejected []Doc `json:"rejected"`
}
type CompileResult struct {
	Pages int    `json:"pages"`
	Hash  string `json:"hash"`
}
type Issue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
type LintResult struct {
	Errors   []Issue `json:"errors"`
	Warnings []Issue `json:"warnings"`
}
type Hit struct {
	ID      string  `json:"id"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
}
type Slice struct {
	ID          string `json:"id"`
	ContentHash string `json:"content_hash"`
	Text        string `json:"text"`
}

// Index is the port. All methods take a context; typed errors are *Error.
type Index interface {
	Health(ctx context.Context, v Vault) (Health, error)
	// Ingest syncs the Markdown projection files of dir into the vault; documents absent from dir are removed.
	Ingest(ctx context.Context, v Vault, dir string) (IngestResult, error)
	RegisterSynthesis(ctx context.Context, v Vault, file string) error
	Compile(ctx context.Context, v Vault) (CompileResult, error)
	Lint(ctx context.Context, v Vault) (LintResult, error)
	Search(ctx context.Context, v Vault, q string, limit int) ([]Hit, error)
	Get(ctx context.Context, v Vault, id string, maxBytes int) (Slice, error)
}
