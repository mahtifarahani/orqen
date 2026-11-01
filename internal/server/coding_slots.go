package server

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"log/slog"
	"orqen/internal/runstate"
	"time"
)

func slotDocID(n int) string { return fmt.Sprintf("slot:%d", n) }

func (a *App) slotCount() int {
	if a.Config.CodingSlots < 1 {
		return 1
	}
	return a.Config.CodingSlots
}

func (a *App) slotIDs() []string {
	ids := make([]string, 0, a.slotCount())
	for i := 1; i <= a.slotCount(); i++ {
		ids = append(ids, slotDocID(i))
	}
	return ids
}

// ensureSlots creates the slot documents (idempotent).
func (a *App) ensureSlots(ctx context.Context) error {
	for _, id := range a.slotIDs() {
		if _, err := a.DB.Coll("coding_slots").UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$setOnInsert": bson.M{"holder": nil}}, options.UpdateOne().SetUpsert(true)); err != nil {
			return err
		}
	}
	return nil
}

var slotStates = []string{runstate.Coding, runstate.Verifying, runstate.PublishingDraft, runstate.Reviewing, runstate.Fixing}

// releaseSlotTx frees the slot a run holds, in the caller's transaction. It is called from casRun, so every
// transition out of the slot states releases the slot in the same transaction.
func (a *App) releaseSlotTx(sc context.Context, run RunDoc) error {
	if run.Slot == nil {
		return nil
	}
	if _, err := a.DB.Coll("coding_slots").UpdateOne(sc, bson.M{"_id": run.Slot.ID, "holder.run_id": run.ID}, bson.M{"$set": bson.M{"holder": nil}}); err != nil {
		return err
	}
	if _, err := a.DB.Coll("runs").UpdateOne(sc, bson.M{"_id": run.ID, "org_id": run.OrgID}, bson.M{"$set": bson.M{"slot": nil}}); err != nil {
		return err
	}
	return a.AppendEvent(sc, run.OrgID, "coding_slot.released", Subject{"run", run.ID}, bson.M{"run_id": run.ID, "slot": run.Slot.ID}, nil, nil, true)
}

// hostAvailable is the host adapter readiness (H14): a probe that succeeded within 24 hours, and either the fake
// host or the user's spike attestation.
func (a *App) hostAvailable(ctx context.Context, org string) bool {
	d, err := findOne[HostStateDoc](ctx, a.DB, "host_states", bson.M{"_id": hostStateID(org), "org_id": org})
	if err != nil || d.LastProbe == nil || d.LastProbe.Status != "ok" {
		return false
	}
	if time.Since(d.LastProbe.CheckedAt) > 24*time.Hour {
		return false
	}
	return d.LastProbe.Fake || d.Spike != nil
}

// dispatchCodingSlots hands free coding slots to waiting runs, oldest first across organizations: queued runs start
// coding, and runs that resumed into a coding state (after a question or an adopted push) get their slot back.
func (a *App) dispatchCodingSlots(ctx context.Context) error {
	if err := a.ensureSlots(ctx); err != nil {
		return err
	}
	filter := bson.M{"active": true, "cancellation": nil, "stale": false, "slot": nil, "eligibility": bson.M{"$ne": "left_filter"},
		"$and": []bson.M{
			{"$or": []bson.M{{"state": runstate.Queued}, {"state": bson.M{"$in": slotStates}}}},
			{"$or": []bson.M{{"hold": nil}, {"hold.code": runstate.HoldAwaitingCodingSlot}}},
		}}
	runs, err := findMany[RunDoc](ctx, a.DB, "runs", filter, bson.D{{Key: "queued_at", Value: 1}, {Key: "_id", Value: 1}}, 200)
	if err != nil {
		return err
	}
	paused := map[string]bool{}
	avail := map[string]bool{}
	for _, r := range runs {
		if _, ok := paused[r.OrgID]; !ok {
			p, e := a.orgPaused(ctx, r.OrgID)
			if e != nil {
				return e
			}
			paused[r.OrgID] = p
			avail[r.OrgID] = a.hostAvailable(ctx, r.OrgID)
		}
		if paused[r.OrgID] || !avail[r.OrgID] {
			continue
		}
		if r.State == runstate.Queued {
			code, blockers, e := a.gateBlockers(ctx, r)
			if e != nil {
				return e
			}
			if code != "" {
				if e = a.setHoldTx(ctx, r, code, fmt.Sprint(blockers)); e != nil {
					return e
				}
				continue
			}
		}
		if err = a.tryAcquireSlot(ctx, r); err != nil {
			if errors.Is(err, errRunMoved) {
				continue
			}
			return err
		}
	}
	a.Committed()
	return nil
}

func (a *App) setHoldTx(ctx context.Context, r RunDoc, code, detail string) error {
	err := a.DB.Txn(ctx, func(sc context.Context) error {
		cur, e := a.run(sc, r.OrgID, r.ID)
		if e != nil || !cur.Active || cur.Hold != nil && cur.Hold.Code == code {
			return e
		}
		if cur.Hold != nil && cur.Hold.Code != runstate.HoldAwaitingCodingSlot {
			return nil
		}
		_, e = a.casRun(sc, cur, "", bson.M{"hold": RunHold{Code: code, Detail: detail, Since: time.Now().UTC()}}, runEvent{Reason: code, Actor: runstate.ActorSystem, Extra: []eventSpec{{"run.held", bson.M{"code": code}}}})
		return e
	})
	if errors.Is(err, errRunMoved) {
		return nil
	}
	return err
}

func (a *App) tryAcquireSlot(ctx context.Context, r RunDoc) error {
	return a.DB.Txn(ctx, func(sc context.Context) error {
		cur, err := a.run(sc, r.OrgID, r.ID)
		if err != nil {
			return err
		}
		if !cur.Active || cur.Slot != nil || cur.Cancellation != nil || cur.Stale || cur.Hold != nil && cur.Hold.Code != runstate.HoldAwaitingCodingSlot {
			return errRunMoved
		}
		if cur.State != runstate.Queued && !runstate.SlotHolding(cur.State) {
			return errRunMoved
		}
		pv, err := a.livePolicy(sc, cur.OrgID, cur.ProjectID)
		if err != nil {
			return err
		}
		held, err := a.DB.Coll("runs").CountDocuments(sc, bson.M{"org_id": cur.OrgID, "active": true, "slot": bson.M{"$ne": nil}})
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		var slot SlotDoc
		if int(held) < pv.Int("concurrency.coding_runs") {
			err = a.DB.Coll("coding_slots").FindOneAndUpdate(sc, bson.M{"_id": bson.M{"$in": a.slotIDs()}, "holder": nil}, bson.M{"$set": bson.M{"holder": SlotHolder{OrgID: cur.OrgID, RunID: cur.ID, AcquiredAt: now}}}, afterUpdate().SetSort(bson.D{{Key: "_id", Value: 1}})).Decode(&slot)
		} else {
			err = mongo.ErrNoDocuments
		}
		if errors.Is(err, mongo.ErrNoDocuments) {
			if cur.Hold != nil {
				return nil // already waiting
			}
			if int(held) < pv.Int("concurrency.coding_runs") {
				// The slot documents were not all visible (just created) or a concurrent claim lost: try again next tick.
				if n, e := a.DB.Coll("coding_slots").CountDocuments(sc, bson.M{"holder": bson.M{"$ne": nil}}); e != nil || int(n) < a.slotCount() {
					return e
				}
			}
			_, e := a.casRun(sc, cur, "", bson.M{"hold": RunHold{Code: runstate.HoldAwaitingCodingSlot, Detail: "all coding slots are busy", Since: now}},
				runEvent{Reason: runstate.HoldAwaitingCodingSlot, Actor: runstate.ActorSystem, Extra: []eventSpec{{"run.held", bson.M{"code": runstate.HoldAwaitingCodingSlot}}}})
			return e
		}
		if err != nil {
			return err
		}
		set := bson.M{"slot": RunSlot{ID: slot.ID, AcquiredAt: now}, "hold": nil}
		stage := ""
		if cur.State == runstate.Queued {
			set["state"] = runstate.Coding
			set["checkpoint"] = RunCheckpoint{Stage: runstate.StageHostSession, Since: now}
			stage = runstate.StageQueued
		}
		if _, err = a.casRun(sc, cur, stage, set, runEvent{Reason: "coding_slot_acquired", Actor: runstate.ActorSystem, Extra: []eventSpec{{"coding_slot.acquired", bson.M{"run_id": cur.ID, "slot": slot.ID}}}}); err != nil {
			return err
		}
		return a.enqueueRunAdvance(sc, cur.OrgID, cur.ID)
	})
}

// sweepSlots repairs slot bookkeeping: a slot whose holder is not in a slot state is freed, and a run that claims a
// slot nobody holds loses the claim.
func (a *App) sweepSlots(ctx context.Context) error {
	slots, err := findMany[SlotDoc](ctx, a.DB, "coding_slots", bson.M{"holder": bson.M{"$ne": nil}}, bson.D{{Key: "_id", Value: 1}}, 100)
	if err != nil {
		return err
	}
	for _, s := range slots {
		run, e := a.run(ctx, s.Holder.OrgID, s.Holder.RunID)
		ok := e == nil && run.Active && runstate.SlotHolding(run.State) && run.Slot != nil && run.Slot.ID == s.ID
		if e != nil && !errors.Is(e, mongo.ErrNoDocuments) {
			return e
		}
		if ok {
			continue
		}
		slog.Info("freeing a stale coding slot", "slot", s.ID, "run_id", s.Holder.RunID)
		if _, e = a.DB.Coll("coding_slots").UpdateOne(ctx, bson.M{"_id": s.ID, "holder.run_id": s.Holder.RunID}, bson.M{"$set": bson.M{"holder": nil}}); e != nil {
			return e
		}
		if e == nil && run.Slot != nil {
			_, _ = a.DB.Coll("runs").UpdateOne(ctx, bson.M{"_id": run.ID, "slot.id": s.ID}, bson.M{"$set": bson.M{"slot": nil}})
		}
	}
	claimed, err := findMany[RunDoc](ctx, a.DB, "runs", bson.M{"slot": bson.M{"$ne": nil}}, bson.D{{Key: "_id", Value: 1}}, 200)
	if err != nil {
		return err
	}
	for _, r := range claimed {
		s, e := findOne[SlotDoc](ctx, a.DB, "coding_slots", bson.M{"_id": r.Slot.ID})
		if e != nil && !errors.Is(e, mongo.ErrNoDocuments) {
			return e
		}
		if e != nil || s.Holder == nil || s.Holder.RunID != r.ID {
			if _, e = a.DB.Coll("runs").UpdateOne(ctx, bson.M{"_id": r.ID, "slot.id": r.Slot.ID}, bson.M{"$set": bson.M{"slot": nil}}); e != nil {
				return e
			}
		}
	}
	a.Committed()
	return nil
}

// slotQueue returns the coding-slot waiting position of every run waiting for one (1 is next).
func (a *App) slotQueue(ctx context.Context) (map[string]int, error) {
	runs, err := findMany[RunDoc](ctx, a.DB, "runs", bson.M{"active": true, "cancellation": nil, "slot": nil, "hold.code": runstate.HoldAwaitingCodingSlot}, bson.D{{Key: "queued_at", Value: 1}, {Key: "_id", Value: 1}}, 500)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for i, r := range runs {
		out[r.ID] = i + 1
	}
	return out, nil
}
