// Package runstate defines the run states, the internal checkpoint stages and the transition table. It is pure.
package runstate

// The 19 states of SPEC section 7.
const (
	Discovered       = "discovered"
	GatheringContext = "gathering_context"
	Triaging         = "triaging"
	Planned          = "planned"
	Queued           = "queued"
	Coding           = "coding"
	Verifying        = "verifying"
	PublishingDraft  = "publishing_draft"
	Reviewing        = "reviewing"
	AwaitingCI       = "awaiting_ci"
	ReadyForMerge    = "ready_for_merge"
	Merged           = "merged"
	NeedsInput       = "needs_input"
	WaitingDep       = "waiting_dependency"
	WaitingNetwork   = "waiting_network"
	Fixing           = "fixing"
	Failed           = "failed"
	Cancelled        = "cancelled"
	Superseded       = "superseded"
)

// States lists every state in a stable order.
var States = []string{Discovered, GatheringContext, Triaging, Planned, Queued, Coding, Verifying, PublishingDraft, Reviewing, AwaitingCI, ReadyForMerge, Merged, NeedsInput, WaitingDep, WaitingNetwork, Fixing, Failed, Cancelled, Superseded}

// Internal checkpoint stages (a run resumes at its stage after a question or an outage).
const (
	StageContext   = "context"
	StageTriage    = "triage"
	StagePlan      = "plan"
	StagePlanGate  = "plan_gate"
	StageStartGate = "start_gate"
	StageWorkspace = "workspace"
	StageQueued    = "queued"
	// Sprint 4: from the coding slot to the merge request being ready.
	StageHostSession   = "host_session"
	StageArchitect     = "architect"
	StageArchitectGate = "architect_gate"
	StageImplement     = "implement"
	StageVerify        = "verify"
	StagePublish       = "publish"
	StageReview        = "review"
	StageComment       = "comment"
	StageLand          = "land"
	StageCI            = "ci"
)

// Stages lists the stages in execution order.
var Stages = []string{StageContext, StageTriage, StagePlan, StagePlanGate, StageStartGate, StageWorkspace, StageQueued,
	StageHostSession, StageArchitect, StageArchitectGate, StageImplement, StageVerify, StagePublish, StageReview, StageComment, StageLand, StageCI}

// Hold codes: a hold leaves the state unchanged and blocks automatic advance.
const (
	HoldAutomationPaused   = "automation_paused"
	HoldLeftFilter         = "left_filter"
	HoldCodingStart        = "coding_start"
	HoldProjectNotReady    = "project_not_ready"
	HoldProjectNotEnabled  = "project_not_enabled"
	HoldStale              = "stale"
	HoldWaitingNetworkNote = "waiting_network"
	// Sprint 4.
	HoldAwaitingCodingSlot   = "awaiting_coding_slot"
	HoldDryRun               = "dry_run"
	HoldExternalWritesPaused = "external_writes_paused"
	// Sprint 6: a run that was in flight when its install was restored from a backup. Only the user's Resume clears it.
	HoldRestored = "restored"
)

// Holds lists the holds (waiting_network is a state, not a hold).
var Holds = []string{HoldAutomationPaused, HoldLeftFilter, HoldCodingStart, HoldProjectNotReady, HoldProjectNotEnabled, HoldStale,
	HoldAwaitingCodingSlot, HoldDryRun, HoldExternalWritesPaused, HoldRestored}

// NeverAutoClear lists the holds the periodic sweep must never clear: only an explicit user action does.
var NeverAutoClear = []string{HoldRestored}

// AutoClearable reports whether the sweep may clear a hold when its cause is gone.
func AutoClearable(code string) bool {
	for _, h := range NeverAutoClear {
		if h == code {
			return false
		}
	}
	return true
}

// Actors of a transition.
const (
	ActorSystem = "system"
	ActorUser   = "user"
)

// StageIndex is the position of a stage in Stages, or -1.
func StageIndex(stage string) int {
	for i, s := range Stages {
		if s == stage {
			return i
		}
	}
	return -1
}

// StateOf maps a checkpoint stage to the run state shown while the run sits at that stage (fix round 0).
func StateOf(stage string) string { return StateOfRound(stage, 0) }

// StateOfRound maps a stage and fix round to the run state: implement is fixing from round 1 on.
func StateOfRound(stage string, round int) string {
	switch stage {
	case StageHostSession, StageArchitect, StageArchitectGate:
		return Coding
	case StageImplement:
		if round > 0 {
			return Fixing
		}
		return Coding
	case StageVerify:
		return Verifying
	case StagePublish:
		return PublishingDraft
	case StageReview, StageComment, StageLand:
		return Reviewing
	case StageCI:
		return AwaitingCI
	}
	switch stage {
	case StageContext:
		return GatheringContext
	case StageTriage:
		return Triaging
	case StagePlan, StagePlanGate, StageStartGate, StageWorkspace:
		return Planned
	case StageQueued:
		return Queued
	}
	return ""
}

// PreCoding reports whether a run in this state has not started coding yet (it may be superseded cleanly).
func PreCoding(state string) bool {
	switch state {
	case Discovered, GatheringContext, Triaging, Planned, Queued, NeedsInput, WaitingDep, WaitingNetwork:
		return true
	}
	return false
}

// Terminal reports whether the run is over. failed and cancelled can be retried (see Allowed).
func Terminal(state string) bool {
	switch state {
	case Merged, Failed, Cancelled, Superseded:
		return true
	}
	return false
}

// Active is the opposite of Terminal for the unique-active-run index.
func Active(state string) bool { return !Terminal(state) }

// CodingPhase reports the states a run holds a coding slot in (plus awaiting_ci, which is released).
func CodingPhase(state string) bool {
	switch state {
	case Coding, Verifying, PublishingDraft, Reviewing, Fixing, AwaitingCI:
		return true
	}
	return false
}

// SlotHolding reports the states in which a run occupies a coding slot.
func SlotHolding(state string) bool {
	switch state {
	case Coding, Verifying, PublishingDraft, Reviewing, Fixing:
		return true
	}
	return false
}

// AtOrAfterCoding reports whether a stage belongs to the coding phase (host_session and later).
func AtOrAfterCoding(stage string) bool {
	i := StageIndex(stage)
	return i >= StageIndex(StageHostSession)
}

// PreCodingRun reports whether a run may still be superseded cleanly: its state is pre-coding and its stage has not
// reached the coding phase (a run in needs_input or waiting_network keeps its stage).
func PreCodingRun(state, stage string) bool { return PreCoding(state) && !AtOrAfterCoding(stage) }
