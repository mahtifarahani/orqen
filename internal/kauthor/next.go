package kauthor

// StepKind is what the server must do next.
type StepKind string

const (
	Noop            StepKind = "noop" // terminal: nothing to do
	StartHost       StepKind = "start_host"
	StartCatherdRun StepKind = "start_catherd_run"
	DispatchWriter  StepKind = "dispatch_writer"
	Reconcile       StepKind = "reconcile"
	Wait            StepKind = "wait"
	CollectWriter   StepKind = "collect_writer"
	RetryWriter     StepKind = "retry_writer"
	DispatchKVerify StepKind = "dispatch_kverify"
	CollectKVerify  StepKind = "collect_kverify"
	Apply           StepKind = "apply"
	Fail            StepKind = "fail"
	Cancel          StepKind = "cancel"
	Pause           StepKind = "pause"
)

// Step is the decision. Code is set for Fail.
type Step struct {
	Kind StepKind
	Code string
}

// Dispatch is what is known about the dispatch of the role of the current phase.
type Dispatch int

const (
	// DispatchNone: nothing was sent (or a reconcile found it absent and safe to repeat).
	DispatchNone Dispatch = iota
	// DispatchIntent: the intent was recorded but the result of the call is unknown (a crash): reconcile by name.
	DispatchIntent
	// DispatchKnown: the dispatch id is recorded.
	DispatchKnown
	// DispatchUncertain: reconcile found nothing and a repeat is not known to be safe.
	DispatchUncertain
)

// Facts is everything Next may look at.
type Facts struct {
	State           string
	HostUp          bool
	CatherdRunReady bool
	WriterEnabled   bool
	VerifierEnabled bool
	OrgPaused       bool
	CancelRequested bool
	Dispatch        Dispatch
	RecordPresent   bool // catherd shows the role record as finished
	RoleFailed      bool // catherd shows the role as failed
	WallExceeded    bool
	ParseOK         bool // the stored reply of the phase parses
	Tries           int  // writer dispatches so far
	HostFailures    int
}

func fail(code string) Step { return Step{Kind: Fail, Code: code} }

// Next returns the next step. It never dispatches the knowledge verifier before the writer reply parses, never
// dispatches a second time after an uncertain dispatch, and retries the writer at most once.
func Next(f Facts) Step {
	if Terminal(f.State) {
		return Step{Kind: Noop}
	}
	if f.CancelRequested || f.State == CancelRequested {
		return Step{Kind: Cancel}
	}
	if f.OrgPaused {
		return Step{Kind: Pause}
	}
	switch f.State {
	case Queued, Starting:
		if !f.WriterEnabled {
			return fail(CodeWriterDisabled)
		}
		if !f.VerifierEnabled {
			return fail(CodeVerifierDisabled)
		}
		if !f.HostUp {
			if f.HostFailures >= MaxHostFailures {
				return fail(CodeHostUnavailable)
			}
			return Step{Kind: StartHost}
		}
		if !f.CatherdRunReady {
			return Step{Kind: StartCatherdRun}
		}
		return Step{Kind: DispatchWriter}
	case WriterDispatching:
		return dispatching(f, DispatchWriter)
	case WriterRunning:
		return running(f, CollectWriter)
	case WriterDone:
		if f.ParseOK {
			return Step{Kind: DispatchKVerify}
		}
		if f.Tries < MaxWriterTries {
			return Step{Kind: RetryWriter}
		}
		return fail(CodeReplyUnparseable)
	case KVerifyDispatch:
		return dispatching(f, DispatchKVerify)
	case KVerifyRunning:
		return running(f, CollectKVerify)
	case KVerifyDone:
		if f.ParseOK {
			return Step{Kind: Apply}
		}
		return fail(CodeReplyUnparseable)
	}
	return fail(CodeRoleFailed)
}

func dispatching(f Facts, send StepKind) Step {
	switch f.Dispatch {
	case DispatchIntent:
		return Step{Kind: Reconcile}
	case DispatchUncertain:
		return fail(CodeDispatchUncertain)
	case DispatchKnown:
		return Step{Kind: Wait}
	}
	if !f.HostUp {
		if f.HostFailures >= MaxHostFailures {
			return fail(CodeHostUnavailable)
		}
		return Step{Kind: StartHost}
	}
	return Step{Kind: send}
}

func running(f Facts, collect StepKind) Step {
	switch {
	case f.RoleFailed:
		return fail(CodeRoleFailed)
	case f.RecordPresent:
		return Step{Kind: collect}
	case f.WallExceeded:
		return fail(CodeTimeout)
	}
	return Step{Kind: Wait}
}
