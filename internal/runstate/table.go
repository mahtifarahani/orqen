package runstate

// Transition is one row of the table in PLAN-S3-GO section 2.4. From "" is run creation; "*" matches any
// non-terminal state; "<=queued" matches the pre-coding states.
type Transition struct {
	From, To string
	Actor    string // ActorSystem, ActorUser or "" for either
	Note     string
}

// Table is the complete list of allowed transitions.
var Table = []Transition{
	{"", Discovered, "", "ingest or evaluate"},
	{Discovered, GatheringContext, ActorSystem, "run_advance"},
	{GatheringContext, Triaging, ActorSystem, "packet built"},
	{GatheringContext, NeedsInput, ActorSystem, "packet oversize"},
	{Triaging, Planned, ActorSystem, "outcome execute"},
	{Triaging, NeedsInput, ActorSystem, "guard or decision stop"},
	{Triaging, WaitingDep, ActorSystem, "blocked by an unresolved issue"},
	{WaitingDep, Triaging, ActorSystem, "blocker done"},
	{Triaging, Cancelled, ActorSystem, "already addressed"},
	{Planned, Planned, ActorSystem, "next stage"},
	{Planned, NeedsInput, ActorSystem, "plan approval"},
	{Planned, Queued, ActorSystem, "worktree ready"},
	{"*", WaitingNetwork, ActorSystem, "retryable network failure"},
	{WaitingNetwork, "<=queued", ActorSystem, "saved stage restored"},
	{"*", NeedsInput, ActorSystem, "credential expired and other stops"},
	{NeedsInput, "<=queued", ActorUser, "answer or resolve"},
	{"*", Cancelled, ActorUser, "cancel"},
	{"<=queued", Superseded, ActorSystem, "material change"},
	{"*", Failed, ActorSystem, "invalid output, bad configuration, internal"},
	{Failed, GatheringContext, ActorUser, "retry"},
	{Cancelled, GatheringContext, ActorUser, "retry"},
	{Queued, Coding, ActorSystem, "Sprint 4 dispatch"},
	// Sprint 4.
	{Coding, Coding, ActorSystem, "stage change"},
	{Reviewing, Reviewing, ActorSystem, "stage change"},
	{AwaitingCI, AwaitingCI, ActorSystem, "stage change"},
	{Coding, Verifying, ActorSystem, "committed"},
	{Verifying, Fixing, ActorSystem, "fix round"},
	{Verifying, PublishingDraft, ActorSystem, "verification passed"},
	{PublishingDraft, Reviewing, ActorSystem, "published or simulated"},
	{Reviewing, Fixing, ActorSystem, "blocking findings"},
	{Reviewing, AwaitingCI, ActorSystem, "landed"},
	{Fixing, Verifying, ActorSystem, "fix committed"},
	{AwaitingCI, ReadyForMerge, ActorSystem, "readiness holds"},
	{ReadyForMerge, AwaitingCI, ActorSystem, "readiness lost"},
	{AwaitingCI, Verifying, ActorSystem, "adopted foreign push"},
	{ReadyForMerge, Verifying, ActorSystem, "adopted foreign push"},
	{AwaitingCI, Merged, ActorSystem, "merge request merged"},
	{ReadyForMerge, Merged, ActorSystem, "merge request merged"},
	{AwaitingCI, Cancelled, ActorSystem, "merge request closed"},
	{ReadyForMerge, Cancelled, ActorSystem, "merge request closed"},
	{AwaitingCI, PublishingDraft, ActorUser, "approve and publish"},
	{AwaitingCI, Reviewing, ActorUser, "approve and post comments"},
	{NeedsInput, "coding_phase", ActorUser, "answer or resolve"},
	{WaitingNetwork, "coding_phase", ActorSystem, "saved stage restored"},
}

func matches(pattern, state string) bool {
	switch pattern {
	case "*":
		return !Terminal(state) && state != ""
	case "<=queued":
		return PreCoding(state)
	case "coding_phase":
		return CodingPhase(state)
	}
	return pattern == state
}

// Allowed reports whether actor may move a run from one state to another. A same-state move is a stage change
// inside planned, coding, reviewing and awaiting_ci only.
func Allowed(from, to, actor string) bool {
	if from == to {
		switch from {
		case Planned, Coding, Reviewing, AwaitingCI:
			return actor == ActorSystem
		}
		return false
	}
	for _, t := range Table {
		if matches(t.From, from) && matches(t.To, to) && (t.Actor == "" || t.Actor == actor) {
			return true
		}
	}
	return false
}
