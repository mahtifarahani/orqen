package secscan

// Skip reasons of AutoScanDecision. They are an enumerated list; a sweep that skips writes nothing.
const (
	AutoSkipModeNotAuto   = "mode_not_auto"
	AutoSkipNotUsable     = "not_usable"
	AutoSkipDocker        = "docker_unavailable"
	AutoSkipModelUnset    = "model_unset"
	AutoSkipBudgetUnset   = "budget_unset"
	AutoSkipRunState      = "run_state"
	AutoSkipHold          = "hold"
	AutoSkipOrgPaused     = "org_paused"
	AutoSkipUnreviewed    = "unreviewed"
	AutoSkipReviewNotPass = "review_not_passing"
	AutoSkipHeadMismatch  = "head_mismatch"
	AutoSkipScanExists    = "scan_exists"
	AutoSkipBusy          = "busy"
)

// AutoSkipReasons lists every skip reason.
var AutoSkipReasons = []string{AutoSkipModeNotAuto, AutoSkipNotUsable, AutoSkipDocker, AutoSkipModelUnset, AutoSkipBudgetUnset,
	AutoSkipRunState, AutoSkipHold, AutoSkipOrgPaused, AutoSkipUnreviewed, AutoSkipReviewNotPass, AutoSkipHeadMismatch,
	AutoSkipScanExists, AutoSkipBusy}

// AutoScanInput holds the facts the server gathers for one candidate run.
type AutoScanInput struct {
	Mode          string // automation.security_scan: "auto" enables the sweep
	UsableReason  string // "" when the strix extension is usable and bound
	DockerOK      bool
	ModelSet      bool
	BudgetOK      bool
	RunState      string
	HoldCode      string
	OrgPaused     bool
	ReviewVerdict string
	ReviewHead    string
	ReviewedHead  string
	BlockingOpen  int
	// ScanExistsForHead is true when any non-cancelled scan exists for (run, reviewed head), whatever its completion.
	ScanExistsForHead bool
	ScansRunningInOrg int
}

// AutoScanDecision reports whether the sweep should queue a scan and, when not, why.
func AutoScanDecision(in AutoScanInput) (bool, string) {
	switch {
	case in.Mode != "auto":
		return false, AutoSkipModeNotAuto
	case in.OrgPaused:
		return false, AutoSkipOrgPaused
	case in.RunState != "awaiting_ci" && in.RunState != "ready_for_merge":
		return false, AutoSkipRunState
	case in.HoldCode != "" && in.HoldCode != "dry_run":
		return false, AutoSkipHold
	case in.ReviewedHead == "" || in.ReviewVerdict == "":
		return false, AutoSkipUnreviewed
	case in.ReviewVerdict != "approved" && in.ReviewVerdict != "approved_with_scope", in.BlockingOpen != 0:
		return false, AutoSkipReviewNotPass
	case in.ReviewHead != in.ReviewedHead:
		return false, AutoSkipHeadMismatch
	case in.UsableReason != "":
		return false, AutoSkipNotUsable
	case !in.DockerOK:
		return false, AutoSkipDocker
	case !in.ModelSet:
		return false, AutoSkipModelUnset
	case !in.BudgetOK:
		return false, AutoSkipBudgetUnset
	case in.ScanExistsForHead:
		return false, AutoSkipScanExists
	case in.ScansRunningInOrg > 0:
		return false, AutoSkipBusy
	}
	return true, ""
}
