package codegraph

// Skip reasons of AutoIndexDecision. They are an enumerated list; a sweep that skips writes nothing.
const (
	AutoSkipModeOff     = "mode_off"
	AutoSkipNotUsable   = "not_usable"
	AutoSkipRunState    = "run_state"
	AutoSkipHold        = "hold"
	AutoSkipOrgPaused   = "org_paused"
	AutoSkipNoHead      = "no_head"
	AutoSkipIndexExists = "index_exists"
	AutoSkipIndexFailed = "index_failed"
	AutoSkipBusy        = "busy"
)

// AutoSkipReasons lists every skip reason.
var AutoSkipReasons = []string{AutoSkipModeOff, AutoSkipNotUsable, AutoSkipRunState, AutoSkipHold, AutoSkipOrgPaused,
	AutoSkipNoHead, AutoSkipIndexExists, AutoSkipIndexFailed, AutoSkipBusy}

// AutoIndexInput holds the facts the server gathers for one candidate run. The decision itself is pure.
type AutoIndexInput struct {
	Mode         string // codegraph.auto_index: "on_run" enables the sweep
	UsableReason string // "" when the gitnexus extension is usable and bound for the project
	RunState     string
	HoldCode     string // "" when the run has no hold
	OrgPaused    bool
	HeadSHA      string
	// IndexState is the state of an existing index for (repo, head): "", queued, indexing, ready, failed,
	// license_revoked or removed.
	IndexState      string
	ActiveIndexJobs int
}

// autoRunStates are the run states for which an automatic index is useful (the roles read the impact item).
var autoRunStates = map[string]bool{"planned": true, "queued": true, "coding": true, "fixing": true, "reviewing": true}

// AutoIndexDecision reports whether the sweep should queue an index and, when not, why.
func AutoIndexDecision(in AutoIndexInput) (bool, string) {
	switch {
	case in.Mode != "on_run":
		return false, AutoSkipModeOff
	case in.OrgPaused:
		return false, AutoSkipOrgPaused
	case !autoRunStates[in.RunState]:
		return false, AutoSkipRunState
	case in.HoldCode != "" && in.HoldCode != "awaiting_coding_slot" && in.HoldCode != "dry_run":
		return false, AutoSkipHold
	case in.HeadSHA == "":
		return false, AutoSkipNoHead
	case in.UsableReason != "":
		return false, AutoSkipNotUsable
	}
	switch in.IndexState {
	case "failed", "license_revoked":
		return false, AutoSkipIndexFailed // the person uses "Index now"
	case "queued", "indexing", "ready":
		return false, AutoSkipIndexExists
	}
	if in.ActiveIndexJobs > 0 {
		return false, AutoSkipBusy
	}
	return true, ""
}
