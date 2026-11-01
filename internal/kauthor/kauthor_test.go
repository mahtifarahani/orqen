package kauthor

import "testing"

func base(state string) Facts {
	return Facts{State: state, HostUp: true, CatherdRunReady: true, WriterEnabled: true, VerifierEnabled: true}
}

func TestNextMatrix(t *testing.T) {
	mut := func(state string, f func(*Facts)) Facts { x := base(state); f(&x); return x }
	cases := []struct {
		name string
		in   Facts
		want Step
	}{
		{"queued starts host", mut(Queued, func(f *Facts) { f.HostUp = false }), Step{Kind: StartHost}},
		{"host fails 5 times", mut(Queued, func(f *Facts) { f.HostUp = false; f.HostFailures = 5 }), Step{Fail, CodeHostUnavailable}},
		{"catherd run next", mut(Starting, func(f *Facts) { f.CatherdRunReady = false }), Step{Kind: StartCatherdRun}},
		{"dispatch writer", base(Starting), Step{Kind: DispatchWriter}},
		{"writer disabled", mut(Queued, func(f *Facts) { f.WriterEnabled = false }), Step{Fail, CodeWriterDisabled}},
		{"verifier disabled", mut(Queued, func(f *Facts) { f.VerifierEnabled = false }), Step{Fail, CodeVerifierDisabled}},
		{"intent recorded reconciles", mut(WriterDispatching, func(f *Facts) { f.Dispatch = DispatchIntent }), Step{Kind: Reconcile}},
		{"uncertain fails, never redispatch", mut(WriterDispatching, func(f *Facts) { f.Dispatch = DispatchUncertain }), Step{Fail, CodeDispatchUncertain}},
		{"uncertain kverify fails", mut(KVerifyDispatch, func(f *Facts) { f.Dispatch = DispatchUncertain }), Step{Fail, CodeDispatchUncertain}},
		{"known dispatch waits", mut(WriterDispatching, func(f *Facts) { f.Dispatch = DispatchKnown }), Step{Kind: Wait}},
		{"reconciled absent sends again", base(WriterDispatching), Step{Kind: DispatchWriter}},
		{"running waits", base(WriterRunning), Step{Kind: Wait}},
		{"running collects", mut(WriterRunning, func(f *Facts) { f.RecordPresent = true }), Step{Kind: CollectWriter}},
		{"running times out", mut(WriterRunning, func(f *Facts) { f.WallExceeded = true }), Step{Fail, CodeTimeout}},
		{"record beats wall", mut(WriterRunning, func(f *Facts) { f.WallExceeded = true; f.RecordPresent = true }), Step{Kind: CollectWriter}},
		{"role failed", mut(KVerifyRunning, func(f *Facts) { f.RoleFailed = true }), Step{Fail, CodeRoleFailed}},
		{"kverify only after parse", mut(WriterDone, func(f *Facts) { f.ParseOK = true; f.Tries = 1 }), Step{Kind: DispatchKVerify}},
		{"unparseable first retries", mut(WriterDone, func(f *Facts) { f.Tries = 1 }), Step{Kind: RetryWriter}},
		{"unparseable twice fails", mut(WriterDone, func(f *Facts) { f.Tries = 2 }), Step{Fail, CodeReplyUnparseable}},
		{"kverify collects", mut(KVerifyRunning, func(f *Facts) { f.RecordPresent = true }), Step{Kind: CollectKVerify}},
		{"kverify done applies", mut(KVerifyDone, func(f *Facts) { f.ParseOK = true }), Step{Kind: Apply}},
		{"kverify unparseable fails", base(KVerifyDone), Step{Fail, CodeReplyUnparseable}},
		{"terminal", base(Applied), Step{Kind: Noop}},
	}
	for _, c := range cases {
		if got := Next(c.in); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func TestCancelFromEveryNonTerminalState(t *testing.T) {
	for _, s := range States {
		f := base(s)
		f.CancelRequested = true
		got := Next(f)
		if Terminal(s) {
			if got.Kind != Noop {
				t.Errorf("%s: terminal must stay put, got %+v", s, got)
			}
			continue
		}
		if got.Kind != Cancel {
			t.Errorf("%s: want cancel, got %+v", s, got)
		}
	}
	if Next(base(CancelRequested)).Kind != Cancel {
		t.Error("cancel_requested state cancels")
	}
}

func TestPausedNeverDispatches(t *testing.T) {
	for _, s := range States {
		f := base(s)
		f.OrgPaused = true
		f.ParseOK = true
		got := Next(f)
		switch {
		case Terminal(s):
			if got.Kind != Noop {
				t.Errorf("%s: %+v", s, got)
			}
		case s == CancelRequested:
			if got.Kind != Cancel {
				t.Errorf("%s: %+v", s, got)
			}
		default:
			if got.Kind != Pause {
				t.Errorf("%s: paused must Pause, got %+v", s, got)
			}
		}
	}
}

func TestKVerifyNeverBeforeWriterParsed(t *testing.T) {
	for _, s := range States {
		for _, parse := range []bool{false, true} {
			for tries := 0; tries <= 3; tries++ {
				f := base(s)
				f.ParseOK = parse
				f.Tries = tries
				if Next(f).Kind == DispatchKVerify && !(s == WriterDone && parse) && s != KVerifyDispatch {
					t.Errorf("kverify dispatched from %s parse=%v", s, parse)
				}
			}
		}
	}
}
