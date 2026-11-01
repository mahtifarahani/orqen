package secscan

// Completion is how a scan ended. Only Complete can pass a gate.
type Completion string

const (
	Complete      Completion = "complete"
	StoppedBudget Completion = "stopped_budget"
	StoppedTurns  Completion = "stopped_turns"
	Timeout       Completion = "timeout"
	Failed        Completion = "failed"
	Incomplete    Completion = "incomplete"
	InvalidOutput Completion = "invalid_output"
)

// Completions lists every value (the i18n catalog has one key per entry).
var Completions = []Completion{Complete, StoppedBudget, StoppedTurns, Timeout, Failed, Incomplete, InvalidOutput}

// Classify decides how a scan ended (X22). report is nil when no report was written; parseErr is set when one was
// written but did not parse. tree is the target the scan was given.
//
// complete only when the process exited 0, the report parses, its status is "completed" and its scope names the given
// tree. A budget or turn stop is a stop even when the process exited 0. A missing report after exit 0 is incomplete.
func Classify(exit int, timedOut bool, report *Report, parseErr error, tree string) Completion {
	switch {
	case timedOut:
		return Timeout
	case parseErr != nil:
		return InvalidOutput
	case report == nil:
		if exit != 0 {
			return Failed
		}
		return Incomplete
	}
	switch report.Status {
	case StatusBudgetExceeded:
		return StoppedBudget
	case StatusMaxTurns:
		return StoppedTurns
	case StatusError:
		return Failed
	case StatusCompleted:
		if exit != 0 {
			return Failed
		}
		if report.Scope.Target != tree {
			return Incomplete
		}
		return Complete
	}
	return InvalidOutput
}

// Passes reports whether a completion may satisfy a gate.
func (c Completion) Passes() bool { return c == Complete }
