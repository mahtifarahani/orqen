package secscan

import "testing"

func goodScan() AutoScanInput {
	return AutoScanInput{Mode: "auto", DockerOK: true, ModelSet: true, BudgetOK: true, RunState: "awaiting_ci",
		ReviewVerdict: "approved", ReviewHead: "h1", ReviewedHead: "h1"}
}

func TestAutoScanDecision(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(*AutoScanInput)
		ok     bool
		reason string
	}{
		{"go", func(*AutoScanInput) {}, true, ""},
		{"scope verdict", func(i *AutoScanInput) { i.ReviewVerdict = "approved_with_scope"; i.RunState = "ready_for_merge" }, true, ""},
		{"manual", func(i *AutoScanInput) { i.Mode = "manual" }, false, AutoSkipModeNotAuto},
		{"off", func(i *AutoScanInput) { i.Mode = "off" }, false, AutoSkipModeNotAuto},
		{"unusable", func(i *AutoScanInput) { i.UsableReason = "not_tested" }, false, AutoSkipNotUsable},
		{"docker", func(i *AutoScanInput) { i.DockerOK = false }, false, AutoSkipDocker},
		{"model", func(i *AutoScanInput) { i.ModelSet = false }, false, AutoSkipModelUnset},
		{"budget", func(i *AutoScanInput) { i.BudgetOK = false }, false, AutoSkipBudgetUnset},
		{"state", func(i *AutoScanInput) { i.RunState = "coding" }, false, AutoSkipRunState},
		{"restored", func(i *AutoScanInput) { i.HoldCode = "restored" }, false, AutoSkipHold},
		{"automation paused", func(i *AutoScanInput) { i.HoldCode = "automation_paused" }, false, AutoSkipHold},
		{"slot hold", func(i *AutoScanInput) { i.HoldCode = "awaiting_coding_slot" }, false, AutoSkipHold},
		{"dry run", func(i *AutoScanInput) { i.HoldCode = "dry_run" }, true, ""},
		{"org paused", func(i *AutoScanInput) { i.OrgPaused = true }, false, AutoSkipOrgPaused},
		{"unreviewed", func(i *AutoScanInput) { i.ReviewedHead = "" }, false, AutoSkipUnreviewed},
		{"no verdict", func(i *AutoScanInput) { i.ReviewVerdict = "" }, false, AutoSkipUnreviewed},
		{"changes requested", func(i *AutoScanInput) { i.ReviewVerdict = "changes_requested" }, false, AutoSkipReviewNotPass},
		{"blocking open", func(i *AutoScanInput) { i.BlockingOpen = 1 }, false, AutoSkipReviewNotPass},
		{"head mismatch", func(i *AutoScanInput) { i.ReviewHead = "h0" }, false, AutoSkipHeadMismatch},
		{"scan exists (incomplete too)", func(i *AutoScanInput) { i.ScanExistsForHead = true }, false, AutoSkipScanExists},
		{"busy", func(i *AutoScanInput) { i.ScansRunningInOrg = 1 }, false, AutoSkipBusy},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		in := goodScan()
		c.mut(&in)
		ok, reason := AutoScanDecision(in)
		if ok != c.ok || reason != c.reason {
			t.Errorf("%s: got (%v, %q), want (%v, %q)", c.name, ok, reason, c.ok, c.reason)
		}
		seen[reason] = true
	}
	for _, r := range AutoSkipReasons {
		if !seen[r] {
			t.Errorf("skip reason %q is not reachable in the table", r)
		}
	}
}
