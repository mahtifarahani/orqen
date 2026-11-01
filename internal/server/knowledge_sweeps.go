package server

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"orqen/internal/runstate"
)

// usageID is the knowledge_usage row of (run, role, round).
func usageID(run RunDoc, role string) string {
	return run.ID + ":" + role + ":" + string(rune('0'+run.Round%10))
}

// rexContext summarizes the packet a role execution received (stored on the rex; the DTO does not expose it).
func (a *App) rexContext(ctx context.Context, run RunDoc, role string) bson.M {
	var u bson.M
	if err := a.DB.Coll("knowledge_usage").FindOne(ctx, bson.M{"_id": usageID(run, role)}).Decode(&u); err != nil {
		return nil
	}
	n := 0
	if ids, ok := u["version_ids"].(bson.A); ok {
		n = len(ids)
	}
	return bson.M{"packet_bytes": u["packet_bytes"], "est_tokens": u["est_tokens"], "est_method": u["est_method"], "knowledge_versions": n}
}

// rollupUsage writes the outcome of terminal runs onto their usage rows: fix rounds, the first review verdict and
// whether the run merged. It is idempotent and driven by the scheduler sweep, so no terminal transition needs a hook.
func (a *App) rollupUsage(ctx context.Context) error {
	ids, err := a.DB.Coll("knowledge_usage").Distinct(ctx, "run_id", bson.M{"outcome": bson.M{"$exists": false}}).Raw()
	if err != nil {
		return err
	}
	vals, err := ids.Values()
	if err != nil {
		return err
	}
	for i, v := range vals {
		if i >= 200 {
			break
		}
		runID, ok := v.StringValueOK()
		if !ok {
			continue
		}
		var run RunDoc
		if err := a.DB.Coll("runs").FindOne(ctx, bson.M{"_id": runID}).Decode(&run); err != nil || !runstate.Terminal(run.State) {
			continue
		}
		verdict := ""
		var ev EventDoc
		if err := a.DB.Coll("events").FindOne(ctx, bson.M{"type": "review.recorded", "subject.id": run.ID}, options.FindOne().SetSort(bson.D{{Key: "seq", Value: 1}})).Decode(&ev); err == nil {
			verdict, _ = ev.Payload["verdict"].(string)
		}
		fix := run.Round - 1
		if fix < 0 {
			fix = 0
		}
		if _, err := a.DB.Coll("knowledge_usage").UpdateMany(ctx, bson.M{"run_id": run.ID, "outcome": bson.M{"$exists": false}},
			bson.M{"$set": bson.M{"outcome": bson.M{"fix_rounds": fix, "first_review_verdict": verdict, "merged": run.State == runstate.Merged}}}); err != nil {
			return err
		}
	}
	return nil
}

// sweepCompaction queues one compaction job per org and day for orgs that hold verified experience. Proposals are
// deduplicated by group, so repeating it is harmless; nothing is superseded until a person verifies a proposal.
func (a *App) sweepCompaction(ctx context.Context) error {
	orgs, err := a.DB.Coll("knowledge_versions").Distinct(ctx, "org_id", bson.M{"status": "verified", "type": "experience"}).Raw()
	if err != nil {
		return err
	}
	vals, err := orgs.Values()
	if err != nil {
		return err
	}
	day := time.Now().UTC().Format("2006-01-02")
	for _, v := range vals {
		org, ok := v.StringValueOK()
		if !ok {
			continue
		}
		key := "knowledge_compaction:daily:" + org + ":" + day
		if n, _ := a.DB.Coll("jobs").CountDocuments(ctx, bson.M{"org_id": org, "dedupe_key": key}); n > 0 {
			continue
		}
		if err := a.txnRetryJob(ctx, func(sc context.Context) error {
			return a.ensureJobTx(sc, org, "knowledge_compaction", key, 3, bson.M{})
		}); err != nil {
			return err
		}
		a.Committed()
	}
	return nil
}
