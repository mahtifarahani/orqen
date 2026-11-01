// Package mrready decides whether a merge request is ready for a person to merge. It is pure: the server collects the
// facts and passes them in.
package mrready

import "time"

// Job is one pipeline job.
type Job struct {
	Name         string
	Status       string
	AllowFailure bool
}

// Pipeline is the newest pipeline Orqen read for the merge request.
type Pipeline struct {
	SHA    string
	Status string
	Jobs   []Job
}

// Inputs are the facts of one evaluation.
type Inputs struct {
	CurrentHead, VerifiedHead, ReviewedHead string
	Pipeline                                *Pipeline
	RequirePipeline                         bool
	NoPipelineSince                         *time.Time
	Grace                                   time.Duration
	Now                                     time.Time
	BlockingOpen                            int
	DiscussionsResolved, HasConflicts       bool
	Opened                                  bool
	VerifyGate, ReviewVerdict               string
	Independence                            string
	Stale, Held                             bool
	Mode                                    string
	// Security is nil unless the security gate is not off (Sprint 6); a nil value adds no check, so the checklist of
	// earlier sprints is unchanged.
	Security *SecurityFacts
}

// Security gate modes.
const (
	GateRequired = "required"
	GateAdvisory = "advisory"
)

// SecurityFacts are the facts of the newest security scan of the run. Completion is the scan's completion value
// (internal/secscan); only "complete" can pass.
type SecurityFacts struct {
	Gate         string // required | advisory
	ScanHead     string // the head the newest scan covered; empty when there is none
	Completion   string
	Running      bool
	BlockingOpen int
}

// Check codes, in the order they are evaluated.
const (
	RemoteMR         = "remote_mr"
	HeadMatch        = "head_match"
	PipelineHead     = "pipeline_head"
	PipelineSuccess  = "pipeline_success"
	RequiredJobs     = "required_jobs"
	BlockingFindings = "blocking_findings"
	Discussions      = "discussions"
	Conflicts        = "conflicts"
	MROpen           = "mr_open"
	Verification     = "verification"
	Review           = "review"
	Independence     = "independence"
	RunState         = "run_state"
	SecurityScan     = "security_scan"
)

// Check is one line of the checklist.
type Check struct {
	Code   string
	Status string // pass | fail | pending | stale
	Detail string
}

// Result is the evaluation.
type Result struct {
	Ready  bool
	Checks []Check
}

// Evaluate applies the readiness formula. A check is only "pass" when the fact positively holds: a missing or
// unknown fact never passes.
func Evaluate(in Inputs) Result {
	var cs []Check
	add := func(code string, ok bool, failStatus, detail string) {
		st := "pass"
		if !ok {
			st = failStatus
		} else {
			detail = firstNonEmpty(detail, "")
		}
		cs = append(cs, Check{code, st, detail})
	}
	add(RemoteMR, in.Mode == "live", "fail", map[bool]string{true: "", false: "simulated"}[in.Mode == "live"])
	headsOK := in.CurrentHead != "" && in.CurrentHead == in.VerifiedHead && in.CurrentHead == in.ReviewedHead
	detail := ""
	if !headsOK {
		detail = "the heads differ: current " + short(in.CurrentHead) + ", verified " + short(in.VerifiedHead) + ", reviewed " + short(in.ReviewedHead)
	}
	add(HeadMatch, headsOK, "stale", detail)

	// the pipeline
	pipeHead, pipeOK, jobsOK := false, false, false
	pipeDetail, headDetail, jobDetail := "", "", ""
	st1, st2, st3 := "fail", "fail", "fail"
	switch p := in.Pipeline; {
	case p == nil:
		if !in.RequirePipeline && in.NoPipelineSince != nil && in.Now.Sub(*in.NoPipelineSince) >= in.Grace {
			pipeHead, pipeOK, jobsOK = true, true, true
			headDetail, pipeDetail, jobDetail = "no_ci_policy", "no_ci_policy", "no_ci_policy"
		} else if !in.RequirePipeline && in.NoPipelineSince != nil {
			st1, st2, st3 = "pending", "pending", "pending"
			headDetail = "waiting for a pipeline within the grace period"
		} else {
			headDetail = "no_pipeline"
			if in.RequirePipeline {
				st1, st2, st3 = "fail", "fail", "fail"
			}
		}
	default:
		pipeHead = p.SHA != "" && p.SHA == in.CurrentHead
		if !pipeHead {
			st1 = "stale"
			headDetail = "the newest pipeline is for " + short(p.SHA) + ", the head is " + short(in.CurrentHead) + " (merged-results pipelines are not supported)"
		}
		pipeOK = p.Status == "success"
		if !pipeOK {
			st2 = "pending"
			if p.Status == "failed" || p.Status == "canceled" || p.Status == "skipped" {
				st2 = "fail"
			}
			pipeDetail = "pipeline " + p.Status
		}
		jobsOK = true
		for _, j := range p.Jobs {
			if !j.AllowFailure && j.Status != "success" {
				jobsOK = false
				jobDetail = j.Name + " is " + j.Status
				st3 = "pending"
				if j.Status == "failed" || j.Status == "canceled" {
					st3 = "fail"
				}
			}
		}
	}
	add(PipelineHead, pipeHead, st1, headDetail)
	add(PipelineSuccess, pipeOK, st2, pipeDetail)
	add(RequiredJobs, jobsOK && (pipeOK || in.Pipeline == nil), st3, jobDetail)
	add(BlockingFindings, in.BlockingOpen == 0, "fail", map[bool]string{true: "", false: "open blocking findings"}[in.BlockingOpen == 0])
	add(Discussions, in.DiscussionsResolved, "fail", "")
	add(Conflicts, !in.HasConflicts, "fail", "")
	add(MROpen, in.Opened, "fail", "")
	add(Verification, in.VerifyGate == "pass", "fail", "gate "+in.VerifyGate)
	add(Review, in.ReviewVerdict == "approved" || in.ReviewVerdict == "approved_with_scope", "fail", "verdict "+in.ReviewVerdict)
	add(Independence, in.Independence == "independent", "fail", "reviewer independence: "+in.Independence)
	add(RunState, !in.Stale && !in.Held, "fail", "")
	if sf := in.Security; sf != nil && sf.Gate == GateRequired {
		cs = append(cs, securityCheck(in.CurrentHead, sf))
	}
	ready := true
	for _, c := range cs {
		ready = ready && c.Status == "pass"
	}
	return Result{Ready: ready, Checks: cs}
}

func short(s string) string {
	if s == "" {
		return "none"
	}
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// securityCheck applies the required gate: pass only for a complete scan of the current head with no blocking finding
// left. A scan of another head is stale, a running scan is pending, anything else fails with the completion as detail.
func securityCheck(current string, sf *SecurityFacts) Check {
	switch {
	case sf.Running:
		return Check{SecurityScan, "pending", "a scan is running"}
	case sf.ScanHead == "" && sf.Completion == "":
		return Check{SecurityScan, "fail", "no scan"}
	case sf.ScanHead != current:
		return Check{SecurityScan, "stale", "the newest scan is for " + short(sf.ScanHead) + ", the head is " + short(current)}
	case sf.Completion != "complete":
		return Check{SecurityScan, "fail", sf.Completion}
	case sf.BlockingOpen > 0:
		return Check{SecurityScan, "fail", "open blocking findings"}
	}
	return Check{SecurityScan, "pass", ""}
}
