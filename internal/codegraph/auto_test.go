package codegraph

import "testing"

func goodIndex() AutoIndexInput {
	return AutoIndexInput{Mode: "on_run", RunState: "planned", HeadSHA: "abc"}
}

func TestAutoIndexDecision(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(*AutoIndexInput)
		ok     bool
		reason string
	}{
		{"go", func(*AutoIndexInput) {}, true, ""},
		{"off", func(i *AutoIndexInput) { i.Mode = "off" }, false, AutoSkipModeOff},
		{"manual", func(i *AutoIndexInput) { i.Mode = "manual" }, false, AutoSkipModeOff},
		{"unusable", func(i *AutoIndexInput) { i.UsableReason = "license_revoked" }, false, AutoSkipNotUsable},
		{"state", func(i *AutoIndexInput) { i.RunState = "merged" }, false, AutoSkipRunState},
		{"restored", func(i *AutoIndexInput) { i.HoldCode = "restored" }, false, AutoSkipHold},
		{"paused hold", func(i *AutoIndexInput) { i.HoldCode = "automation_paused" }, false, AutoSkipHold},
		{"slot hold ok", func(i *AutoIndexInput) { i.HoldCode = "awaiting_coding_slot" }, true, ""},
		{"dry run hold ok", func(i *AutoIndexInput) { i.HoldCode = "dry_run" }, true, ""},
		{"org paused", func(i *AutoIndexInput) { i.OrgPaused = true }, false, AutoSkipOrgPaused},
		{"no head", func(i *AutoIndexInput) { i.HeadSHA = "" }, false, AutoSkipNoHead},
		{"ready", func(i *AutoIndexInput) { i.IndexState = "ready" }, false, AutoSkipIndexExists},
		{"queued", func(i *AutoIndexInput) { i.IndexState = "queued" }, false, AutoSkipIndexExists},
		{"indexing", func(i *AutoIndexInput) { i.IndexState = "indexing" }, false, AutoSkipIndexExists},
		{"failed", func(i *AutoIndexInput) { i.IndexState = "failed" }, false, AutoSkipIndexFailed},
		{"revoked", func(i *AutoIndexInput) { i.IndexState = "license_revoked" }, false, AutoSkipIndexFailed},
		{"removed retries", func(i *AutoIndexInput) { i.IndexState = "removed" }, true, ""},
		{"busy", func(i *AutoIndexInput) { i.ActiveIndexJobs = 1 }, false, AutoSkipBusy},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		in := goodIndex()
		c.mut(&in)
		ok, reason := AutoIndexDecision(in)
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
	for _, s := range []string{"planned", "queued", "coding", "fixing", "reviewing"} {
		in := goodIndex()
		in.RunState = s
		if ok, _ := AutoIndexDecision(in); !ok {
			t.Errorf("state %s should index", s)
		}
	}
}
