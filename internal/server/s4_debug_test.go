package server

import (
	"context"
	"go.mongodb.org/mongo-driver/v2/bson"
	"testing"
)

func debugRun(t *testing.T, e *s4Env, key string) {
	r, _ := e.runOf(key)
	if r.ID == "" {
		return
	}
	t.Logf("run state=%s stage=%s hold=%+v faults=%d slot=%v failure=%+v", r.State, r.Checkpoint.Stage, r.Hold, r.HostFaults, r.Slot, r.Failure)
	for _, x := range e.roles(key) {
		t.Logf("rex %s %s tries=%d", x.Name, x.State, x.Tries)
	}
	evs, _ := findMany[VerificationEvidenceDoc](context.Background(), e.f.db, "verification_evidence", map[string]any{"org_id": e.org, "run_id": r.ID}, bson.D{{Key: "created_at", Value: 1}}, 20)
	for _, ev := range evs {
		t.Logf("evidence %s round=%d head=%s gate=%+v verifier=%+v", ev.Kind, ev.Round, ev.HeadSHA[:7], ev.Gate, ev.Verifier)
		for _, c := range ev.Commands {
			t.Logf("   cmd %s outcome=%s exit=%d reason=%s sandbox=%s", c.Key, c.Outcome, c.ExitCode, c.Reason, c.Sandbox)
		}
	}
	slots, serr := findMany[SlotDoc](context.Background(), e.f.db, "coding_slots", map[string]any{}, bson.D{{Key: "_id", Value: 1}}, 10)
	t.Logf("slots: %d err=%v", len(slots), serr)
	for _, sl := range slots {
		t.Logf("slot %s holder=%+v", sl.ID, sl.Holder)
	}
	jobs, _ := findMany[JobDoc](context.Background(), e.f.db, "jobs", map[string]any{"org_id": e.org, "kind": map[string]any{"$in": []string{"run_advance", "role_watch"}}}, nil, 20)
	for _, j := range jobs {
		t.Logf("job %s %s active=%v attempts=%d err=%+v", j.Kind, j.State, j.Active, j.Attempts, j.LastError)
	}
}
