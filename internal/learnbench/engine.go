package learnbench

// Packet is what a task receives: the facts it contains and its size.
type Packet struct {
	Facts []string
	Bytes int
}

// Outcome is what an engine reports for one task.
type Outcome struct {
	Reworks           int
	Missing           []string
	FirstPassApproved bool
}

// Engine runs one task against a packet. A real engine (Sprint 6+, user-run) would call models; the harness never does.
type Engine interface {
	Name() string
	RunTask(t Task, p Packet) Outcome
}

// ClaimLevel says what a result may be used for.
const ClaimLevelHarnessOnly = "harness_only"

// FakeEngine reworks once per needed fact that is missing from the packet. It is deterministic and spends nothing.
type FakeEngine struct{}

func (FakeEngine) Name() string { return "fake" }
func (FakeEngine) RunTask(t Task, p Packet) Outcome {
	have := map[string]bool{}
	for _, f := range p.Facts {
		have[f] = true
	}
	var missing []string
	for _, n := range t.Needs {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	return Outcome{Reworks: len(missing), Missing: missing, FirstPassApproved: len(missing) == 0}
}
