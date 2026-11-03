package codexhost

import (
	"context"
	"time"
)

// Host is what Orqen needs from an orchestration host: a thread it can send directives to, one validated catherd call
// per directive, and a read-only probe. *Client (the Codex App Server) and the Claude Code host both implement it, so
// the run engine never knows which host it drives.
type Host interface {
	// StartThread opens a new orchestrator thread.
	StartThread(ctx context.Context, p ThreadParams) (Thread, error)
	// ResumeThread reopens a thread with its history. An *RPCError means the thread is gone.
	ResumeThread(ctx context.Context, id string) (Thread, error)
	// SetEffort sets the reasoning effort used for later turns of a thread.
	SetEffort(thread, effort string)
	// Usage returns the cumulative token usage last reported for the thread.
	Usage(thread string) (TokenUsage, bool)
	// RunDirective sends one directive as a turn and returns the one validated catherd call.
	RunDirective(ctx context.Context, threadID string, d Directive, timeout time.Duration) (ToolCall, error)
	// Probe reads the host's login state, models and MCP servers. It creates no thread and no turn.
	Probe(ctx context.Context) (ProbeResult, error)
	// Done is closed when the host connection ended.
	Done() <-chan struct{}
	// Close ends the host connection.
	Close() error
}

var _ Host = (*Client)(nil)
