package runstate

import "testing"

func TestStateOf(t *testing.T) {
	want := map[string]string{StageContext: GatheringContext, StageTriage: Triaging, StagePlan: Planned, StagePlanGate: Planned, StageStartGate: Planned, StageWorkspace: Planned, StageQueued: Queued, "nope": ""}
	for stage, state := range want {
		if StateOf(stage) != state {
			t.Fatal(stage)
		}
	}
	if len(States) != 19 || len(Stages) != 17 || len(Holds) != 10 {
		t.Fatal(len(States), len(Stages), len(Holds))
	}
}

// expected lists every allowed (from, to, actor) triple. Everything else must be refused.
func expected() map[[3]string]bool {
	e := map[[3]string]bool{}
	allow := func(from, to string, actors ...string) {
		for _, a := range actors {
			e[[3]string{from, to, a}] = true
		}
	}
	both := []string{ActorSystem, ActorUser}
	allow("", Discovered, both...)
	allow(Discovered, GatheringContext, ActorSystem)
	allow(GatheringContext, Triaging, ActorSystem)
	allow(GatheringContext, NeedsInput, ActorSystem)
	allow(Triaging, Planned, ActorSystem)
	allow(Triaging, NeedsInput, ActorSystem)
	allow(Triaging, WaitingDep, ActorSystem)
	allow(WaitingDep, Triaging, ActorSystem)
	allow(Triaging, Cancelled, ActorSystem)
	allow(Planned, Planned, ActorSystem)
	allow(Planned, NeedsInput, ActorSystem)
	allow(Planned, Queued, ActorSystem)
	for _, from := range States {
		if Terminal(from) {
			continue
		}
		if from != WaitingNetwork {
			allow(from, WaitingNetwork, ActorSystem)
		}
		if from != NeedsInput {
			allow(from, NeedsInput, ActorSystem)
		}
		allow(from, Cancelled, ActorUser)
		allow(from, Failed, ActorSystem)
		if PreCoding(from) {
			allow(from, Superseded, ActorSystem)
		}
	}
	for _, to := range []string{Discovered, GatheringContext, Triaging, Planned, Queued, NeedsInput, WaitingDep, WaitingNetwork} {
		allow(WaitingNetwork, to, ActorSystem)
		allow(NeedsInput, to, ActorUser)
	}
	allow(Failed, GatheringContext, ActorUser)
	allow(Cancelled, GatheringContext, ActorUser)
	allow(Queued, Coding, ActorSystem)
	// Sprint 4.
	for _, same := range []string{Coding, Reviewing, AwaitingCI} {
		allow(same, same, ActorSystem)
	}
	allow(Coding, Verifying, ActorSystem)
	allow(Verifying, Fixing, ActorSystem)
	allow(Verifying, PublishingDraft, ActorSystem)
	allow(PublishingDraft, Reviewing, ActorSystem)
	allow(Reviewing, Fixing, ActorSystem)
	allow(Reviewing, AwaitingCI, ActorSystem)
	allow(Fixing, Verifying, ActorSystem)
	allow(AwaitingCI, ReadyForMerge, ActorSystem)
	allow(ReadyForMerge, AwaitingCI, ActorSystem)
	for _, from := range []string{AwaitingCI, ReadyForMerge} {
		allow(from, Verifying, ActorSystem)
		allow(from, Merged, ActorSystem)
		allow(from, Cancelled, ActorSystem)
	}
	allow(AwaitingCI, PublishingDraft, ActorUser)
	allow(AwaitingCI, Reviewing, ActorUser)
	for _, to := range []string{Coding, Verifying, PublishingDraft, Reviewing, Fixing, AwaitingCI} {
		allow(NeedsInput, to, ActorUser)
		allow(WaitingNetwork, to, ActorSystem)
	}
	return e
}
func TestTransitionTableIsExact(t *testing.T) {
	want := expected()
	from := append([]string{""}, States...)
	for _, f := range from {
		for _, to := range States {
			for _, actor := range []string{ActorSystem, ActorUser} {
				// waiting_network and needs_input restore to the pre-coding states only; the explicit rows above
				// add the same-state exceptions, which the plan does not list, so compare through the helper.
				got := Allowed(f, to, actor)
				w := want[[3]string{f, to, actor}]
				if f == to && f != Planned && f != Coding && f != Reviewing && f != AwaitingCI {
					w = false
				}
				if got != w {
					t.Errorf("Allowed(%q, %q, %s) = %v, want %v", f, to, actor, got, w)
				}
			}
		}
	}
}
func TestTerminalStatesOnlyRetry(t *testing.T) {
	for _, from := range []string{Merged, Failed, Cancelled, Superseded} {
		for _, to := range States {
			for _, actor := range []string{ActorSystem, ActorUser} {
				ok := Allowed(from, to, actor)
				retry := (from == Failed || from == Cancelled) && to == GatheringContext && actor == ActorUser
				if ok != retry {
					t.Errorf("terminal %s -> %s (%s): %v", from, to, actor, ok)
				}
			}
		}
	}
	if !Terminal(Superseded) || Terminal(NeedsInput) || !PreCoding(Queued) || PreCoding(Coding) {
		t.Fatal("classification")
	}
}

func TestStateOfRound(t *testing.T) {
	cases := []struct {
		stage string
		round int
		want  string
	}{
		{StageHostSession, 0, Coding}, {StageArchitect, 0, Coding}, {StageArchitectGate, 0, Coding}, {StageImplement, 0, Coding}, {StageImplement, 1, Fixing}, {StageImplement, 2, Fixing},
		{StageVerify, 0, Verifying}, {StageVerify, 2, Verifying}, {StagePublish, 1, PublishingDraft}, {StageReview, 0, Reviewing}, {StageComment, 1, Reviewing}, {StageLand, 0, Reviewing}, {StageCI, 0, AwaitingCI},
		{StageQueued, 0, Queued}, {StageContext, 3, GatheringContext},
	}
	for _, c := range cases {
		if got := StateOfRound(c.stage, c.round); got != c.want {
			t.Errorf("StateOfRound(%s, %d) = %q, want %q", c.stage, c.round, got, c.want)
		}
	}
	if StateOf(StageImplement) != Coding || StateOfRound("nope", 1) != "" {
		t.Fatal("StateOf")
	}
	for _, st := range Stages {
		if StateOfRound(st, 0) == "" {
			t.Errorf("stage %s has no state", st)
		}
	}
}

func TestSlotHoldingAndCodingPhase(t *testing.T) {
	slots := map[string]bool{Coding: true, Verifying: true, PublishingDraft: true, Reviewing: true, Fixing: true}
	for _, s := range States {
		if SlotHolding(s) != slots[s] {
			t.Errorf("SlotHolding(%s)", s)
		}
		if CodingPhase(s) != (slots[s] || s == AwaitingCI) {
			t.Errorf("CodingPhase(%s)", s)
		}
	}
	if AtOrAfterCoding(StageQueued) || !AtOrAfterCoding(StageHostSession) || !AtOrAfterCoding(StageCI) || AtOrAfterCoding("nope") {
		t.Fatal("AtOrAfterCoding")
	}
}

func TestSupersedeOnlyBeforeCoding(t *testing.T) {
	for _, st := range []string{Queued, Planned, NeedsInput, WaitingNetwork} {
		if !PreCodingRun(st, StageQueued) || !PreCodingRun(st, StageContext) {
			t.Errorf("%s at an early stage is pre-coding", st)
		}
		if PreCodingRun(st, StageImplement) || PreCodingRun(st, StageCI) {
			t.Errorf("%s at a coding stage is not pre-coding", st)
		}
	}
	if PreCodingRun(Coding, StageQueued) {
		t.Fatal("coding is never pre-coding")
	}
}

func TestRestoredHoldIsNeverAutoCleared(t *testing.T) {
	if HoldRestored != "restored" || AutoClearable(HoldRestored) {
		t.Fatal("the restored hold is cleared only by the user's Resume")
	}
	for _, h := range Holds {
		if h != HoldRestored && !AutoClearable(h) {
			t.Errorf("%s must stay auto-clearable", h)
		}
	}
	found := false
	for _, h := range Holds {
		found = found || h == HoldRestored
	}
	if !found {
		t.Fatal("restored is not in Holds")
	}
}
