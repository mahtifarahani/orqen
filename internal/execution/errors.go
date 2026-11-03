// Package execution is the host adapter: it supervises one Codex app-server per organization with an isolated
// environment, and gives the server the pure helpers it needs to reconcile directed turns. It must not import
// internal/server.
package execution

import "errors"

var (
	// ErrHostBusy means another runner holds the organization's host lock.
	ErrHostBusy = errors.New("execution: the organization's orchestration host is busy")
	// ErrHostUnavailable means the host cannot be started.
	ErrHostUnavailable = errors.New("execution: the orchestration host cannot be started")
)
