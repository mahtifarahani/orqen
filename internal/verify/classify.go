package verify

// Per-command classification against the baseline (H16).
const (
	ClassPass            = "pass"
	ClassNewFailure      = "new_failure"
	ClassBaselineFailure = "baseline_failure"
	ClassIncomplete      = "incomplete"
)

// Gate statuses.
const (
	GatePass       = "pass"
	GateFail       = "fail"
	GateIncomplete = "incomplete"
)

// Gate is the verdict of the command layer.
type Gate struct {
	Status  string
	Classes map[string]string // command key -> class
	Reasons []string
	Notes   []string
}

// Classify compares the head results with the baseline results (same commands at the base commit).
func Classify(head, baseline []Result, allowBaseline bool) Gate {
	g := Gate{Classes: map[string]string{}}
	base := map[string]Result{}
	for _, b := range baseline {
		base[b.Key] = b
	}
	fail, incomplete := false, false
	if len(head) == 0 {
		g.Status, g.Reasons = GateIncomplete, []string{"no_commands"}
		return g
	}
	for _, h := range head {
		switch h.Outcome {
		case OutcomePass:
			g.Classes[h.Key] = ClassPass
		case OutcomeFail:
			b, ok := base[h.Key]
			switch {
			case !ok || b.Outcome == OutcomeIncomplete:
				g.Classes[h.Key] = ClassIncomplete
				incomplete = true
				g.Reasons = append(g.Reasons, "baseline_unknown:"+h.Key)
			case b.Outcome == OutcomePass:
				g.Classes[h.Key] = ClassNewFailure
				fail = true
				g.Reasons = append(g.Reasons, "new_failure:"+h.Key)
			default:
				g.Classes[h.Key] = ClassBaselineFailure
				if allowBaseline {
					g.Notes = append(g.Notes, "baseline_failure_allowed:"+h.Key)
				} else {
					fail = true
					g.Reasons = append(g.Reasons, "baseline_failure:"+h.Key)
				}
			}
		default:
			g.Classes[h.Key] = ClassIncomplete
			incomplete = true
			g.Reasons = append(g.Reasons, "incomplete:"+h.Key+":"+h.Reason)
		}
	}
	switch {
	case fail:
		g.Status = GateFail
	case incomplete:
		g.Status = GateIncomplete
	default:
		g.Status = GatePass
	}
	return g
}
