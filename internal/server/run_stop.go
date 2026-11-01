package server

import (
	"context"
	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/runstate"
	"time"
)

// stopSpec describes why a run waits for the user.
type stopSpec struct {
	Reason, QuestionCode string
	Params               bson.M
	Evidence             []InboxEvidence
	Options              []InboxOption
	Kind                 string
	// Set holds more run fields to change in the same transaction (for example the triage summary).
	Set bson.M
}

// resolveOptions are the choices of a question that only needs the user to fix something elsewhere and press resume.
func resolveOptions() []InboxOption {
	return []InboxOption{{ID: "resume", EffectCode: "resume"}, {ID: "cancel_run", EffectCode: "cancel_run"}}
}

// stopRun moves a run to needs_input at its current stage and opens its one Inbox item, in the caller's transaction.
func (a *App) stopRun(sc context.Context, run RunDoc, spec stopSpec) error {
	it, err := a.openInbox(sc, run, inboxSpec{Kind: spec.Kind, Reason: spec.Reason, QuestionCode: spec.QuestionCode, Params: spec.Params, Evidence: spec.Evidence, Options: spec.Options})
	if err != nil {
		return err
	}
	set := bson.M{"state": runstate.NeedsInput, "open_inbox_id": it.ID, "checkpoint.since": time.Now().UTC()}
	for k, v := range spec.Set {
		set[k] = v
	}
	_, err = a.casRun(sc, run, run.Checkpoint.Stage, set,
		runEvent{Reason: spec.Reason, Actor: runstate.ActorSystem, Extra: []eventSpec{{"run.needs_input", bson.M{"inbox_id": it.ID, "reason": spec.Reason}}}})
	return err
}
