package server

import (
	"context"
	"time"

	"orqen/internal/extensions"
	"orqen/internal/kindex"
)

// Policy values that were registry entries without a reader before Sprint 7 (Y16). They read the organization's live
// policy; a failed read falls back to the registry default, never to a looser setting.

// extSandboxMode is policy extensions.sandbox (the test seam noSandbox pretends sandbox-exec is missing).
func (a *App) extSandboxMode(ctx context.Context, org string) string {
	if a.noSandbox {
		return extensions.SandboxBestEffort
	}
	if pv, err := a.livePolicy(ctx, org, ""); err == nil && pv.Str("extensions.sandbox") == "best_effort" {
		return extensions.SandboxBestEffort
	}
	return extensions.SandboxEnforce
}

// wikiSandboxMode is policy knowledge.wiki.sandbox.
func (a *App) wikiSandboxMode(ctx context.Context, org string) string {
	if a.noSandbox {
		return kindex.SandboxBestEffort
	}
	if pv, err := a.livePolicy(ctx, org, ""); err == nil && pv.Str("knowledge.wiki.sandbox") == "best_effort" {
		return kindex.SandboxBestEffort
	}
	return kindex.SandboxEnforce
}

// livePolicyInt reads an integer key of the organization's live policy, or def when it cannot be read.
func (a *App) livePolicyInt(ctx context.Context, org, key string, def int) int {
	pv, err := a.livePolicy(ctx, org, "")
	if err != nil {
		return def
	}
	if n := pv.Int(key); n > 0 {
		return n
	}
	return def
}

// outboxRetention is policy retention.outbox_days.
func (a *App) outboxRetention(ctx context.Context, org string) time.Duration {
	return time.Duration(a.livePolicyInt(ctx, org, "retention.outbox_days", 30)) * 24 * time.Hour
}

// graphRetentionDays is policy graph.inferred_retention_days.
func (a *App) graphRetentionDays(ctx context.Context, org string) int {
	return a.livePolicyInt(ctx, org, "graph.inferred_retention_days", 30)
}

// scanTimeout is policy security.timeout_secs.
func (a *App) scanTimeout(ctx context.Context, org string) time.Duration {
	return time.Duration(a.livePolicyInt(ctx, org, "security.timeout_secs", 3600)) * time.Second
}

// compactionThreshold is policy knowledge.compaction.threshold.
func (a *App) compactionThreshold(ctx context.Context, org string) int {
	return a.livePolicyInt(ctx, org, "knowledge.compaction.threshold", 5)
}
