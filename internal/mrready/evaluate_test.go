package mrready

import (
	"testing"
	"time"
)

const h = "1111111111111111111111111111111111111111"

func green() Inputs {
	return Inputs{CurrentHead: h, VerifiedHead: h, ReviewedHead: h, Pipeline: &Pipeline{SHA: h, Status: "success", Jobs: []Job{{"build", "success", false}, {"lint", "failed", true}}},
		RequirePipeline: true, DiscussionsResolved: true, Opened: true, VerifyGate: "pass", ReviewVerdict: "approved", Independence: "independent", Mode: "live", Now: time.Now()}
}

func status(r Result, code string) string {
	for _, c := range r.Checks {
		if c.Code == code {
			return c.Status
		}
	}
	return "missing"
}

func TestGreenIsReadyAndHasEveryCheck(t *testing.T) {
	r := Evaluate(green())
	if !r.Ready || len(r.Checks) != 13 {
		t.Fatalf("%+v", r)
	}
	want := []string{RemoteMR, HeadMatch, PipelineHead, PipelineSuccess, RequiredJobs, BlockingFindings, Discussions, Conflicts, MROpen, Verification, Review, Independence, RunState}
	for i, c := range r.Checks {
		if c.Code != want[i] {
			t.Fatalf("check %d is %s", i, c.Code)
		}
	}
}

func TestEveryConditionBlocksReadiness(t *testing.T) {
	later := time.Now().Add(-time.Hour)
	_ = later
	cases := []struct {
		name   string
		mut    func(*Inputs)
		code   string
		status string
	}{
		{"dry run", func(i *Inputs) { i.Mode = "dry_run" }, RemoteMR, "fail"},
		{"new head, old verification", func(i *Inputs) { i.CurrentHead = "2" + h[1:] }, HeadMatch, "stale"},
		{"verified elsewhere", func(i *Inputs) { i.VerifiedHead = "" }, HeadMatch, "stale"},
		{"reviewed elsewhere", func(i *Inputs) { i.ReviewedHead = "3" + h[1:] }, HeadMatch, "stale"},
		{"pipeline of an older sha", func(i *Inputs) { i.Pipeline.SHA = "4" + h[1:] }, PipelineHead, "stale"},
		{"pipeline of an unrelated sha", func(i *Inputs) { i.Pipeline.SHA = "deadbeef" }, PipelineHead, "stale"},
		{"pipeline running", func(i *Inputs) { i.Pipeline.Status = "running" }, PipelineSuccess, "pending"},
		{"pipeline pending", func(i *Inputs) { i.Pipeline.Status = "pending" }, PipelineSuccess, "pending"},
		{"pipeline skipped", func(i *Inputs) { i.Pipeline.Status = "skipped" }, PipelineSuccess, "fail"},
		{"pipeline canceled", func(i *Inputs) { i.Pipeline.Status = "canceled" }, PipelineSuccess, "fail"},
		{"pipeline failed", func(i *Inputs) { i.Pipeline.Status = "failed" }, PipelineSuccess, "fail"},
		{"pipeline status unknown", func(i *Inputs) { i.Pipeline.Status = "mystery" }, PipelineSuccess, "pending"},
		{"manual job", func(i *Inputs) { i.Pipeline.Jobs = append(i.Pipeline.Jobs, Job{"deploy", "manual", false}) }, RequiredJobs, "pending"},
		{"skipped job", func(i *Inputs) { i.Pipeline.Jobs = append(i.Pipeline.Jobs, Job{"x", "skipped", false}) }, RequiredJobs, "pending"},
		{"failed required job", func(i *Inputs) { i.Pipeline.Jobs[0].Status = "failed" }, RequiredJobs, "fail"},
		{"no pipeline, required", func(i *Inputs) { i.Pipeline = nil }, PipelineHead, "fail"},
		{"open blocking finding", func(i *Inputs) { i.BlockingOpen = 1 }, BlockingFindings, "fail"},
		{"unresolved discussions", func(i *Inputs) { i.DiscussionsResolved = false }, Discussions, "fail"},
		{"conflicts", func(i *Inputs) { i.HasConflicts = true }, Conflicts, "fail"},
		{"closed", func(i *Inputs) { i.Opened = false }, MROpen, "fail"},
		{"verification incomplete", func(i *Inputs) { i.VerifyGate = "incomplete" }, Verification, "fail"},
		{"changes requested", func(i *Inputs) { i.ReviewVerdict = "changes_requested" }, Review, "fail"},
		{"no review", func(i *Inputs) { i.ReviewVerdict = "" }, Review, "fail"},
		{"unknown independence", func(i *Inputs) { i.Independence = "unknown" }, Independence, "fail"},
		{"same model", func(i *Inputs) { i.Independence = "same_model" }, Independence, "fail"},
		{"stale run", func(i *Inputs) { i.Stale = true }, RunState, "fail"},
		{"held run", func(i *Inputs) { i.Held = true }, RunState, "fail"},
	}
	for _, c := range cases {
		in := green()
		c.mut(&in)
		r := Evaluate(in)
		if r.Ready || status(r, c.code) != c.status {
			t.Errorf("%s: ready=%v %s=%s, want %s", c.name, r.Ready, c.code, status(r, c.code), c.status)
		}
	}
	// approved with scope counts as approved
	in := green()
	in.ReviewVerdict = "approved_with_scope"
	if !Evaluate(in).Ready {
		t.Fatal("approved_with_scope is a pass")
	}
}

func TestNoCIPolicy(t *testing.T) {
	now := time.Now()
	since := now.Add(-10 * time.Minute)
	noCI := func(mut func(*Inputs)) Inputs {
		in := green()
		in.Pipeline, in.RequirePipeline, in.NoPipelineSince, in.Grace, in.Now = nil, false, &since, 5*time.Minute, now
		if mut != nil {
			mut(&in)
		}
		return in
	}
	if r := Evaluate(noCI(nil)); !r.Ready || r.Checks[2].Detail != "no_ci_policy" {
		t.Fatalf("grace elapsed and no pipeline: %+v", r)
	}
	within := now.Add(-time.Minute)
	if r := Evaluate(noCI(func(i *Inputs) { i.NoPipelineSince = &within })); r.Ready || status(r, PipelineHead) != "pending" {
		t.Fatalf("still within the grace period: %+v", r)
	}
	if r := Evaluate(noCI(func(i *Inputs) { i.Pipeline = &Pipeline{SHA: h, Status: "failed"} })); r.Ready || status(r, PipelineSuccess) != "fail" {
		t.Fatal("a pipeline that exists must still pass")
	}
	if r := Evaluate(noCI(func(i *Inputs) { i.NoPipelineSince = nil })); r.Ready {
		t.Fatal("without the first-seen time the grace cannot elapse")
	}
}

func TestSecurityGateAddsACheckOnlyWhenRequired(t *testing.T) {
	base := Evaluate(green())
	for _, sf := range []*SecurityFacts{nil, {Gate: GateAdvisory, ScanHead: h, Completion: "failed"}, {Gate: "off"}} {
		in := green()
		in.Security = sf
		r := Evaluate(in)
		if len(r.Checks) != len(base.Checks) || r.Ready != base.Ready {
			t.Fatalf("%+v must not change the checklist: %d checks", sf, len(r.Checks))
		}
		for i := range r.Checks {
			if r.Checks[i] != base.Checks[i] {
				t.Fatalf("check %d differs", i)
			}
		}
	}
	other := "5" + h[1:]
	cases := []struct {
		name   string
		sf     SecurityFacts
		status string
		ready  bool
	}{
		{"complete and clean", SecurityFacts{Gate: GateRequired, ScanHead: h, Completion: "complete"}, "pass", true},
		{"blocking finding open", SecurityFacts{Gate: GateRequired, ScanHead: h, Completion: "complete", BlockingOpen: 1}, "fail", false},
		{"stopped on budget", SecurityFacts{Gate: GateRequired, ScanHead: h, Completion: "stopped_budget"}, "fail", false},
		{"stopped on turns", SecurityFacts{Gate: GateRequired, ScanHead: h, Completion: "stopped_turns"}, "fail", false},
		{"incomplete", SecurityFacts{Gate: GateRequired, ScanHead: h, Completion: "incomplete"}, "fail", false},
		{"invalid output", SecurityFacts{Gate: GateRequired, ScanHead: h, Completion: "invalid_output"}, "fail", false},
		{"scan of an older head", SecurityFacts{Gate: GateRequired, ScanHead: other, Completion: "complete"}, "stale", false},
		{"running", SecurityFacts{Gate: GateRequired, ScanHead: other, Completion: "complete", Running: true}, "pending", false},
		{"never scanned", SecurityFacts{Gate: GateRequired}, "fail", false},
	}
	for _, c := range cases {
		in := green()
		sf := c.sf
		in.Security = &sf
		r := Evaluate(in)
		if len(r.Checks) != 14 || r.Checks[13].Code != SecurityScan {
			t.Fatalf("%s: the security check goes last: %+v", c.name, r.Checks)
		}
		if got := status(r, SecurityScan); got != c.status || r.Ready != c.ready {
			t.Errorf("%s: %s ready=%v, want %s ready=%v", c.name, got, r.Ready, c.status, c.ready)
		}
		if c.status == "fail" && c.sf.Completion != "" && r.Checks[13].Detail == "" {
			t.Errorf("%s: the completion is the detail", c.name)
		}
	}
}
