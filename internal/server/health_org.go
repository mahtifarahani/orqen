package server

import (
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http"
	"time"
)

// getOrgHealth aggregates database, queue, connections, catherd and runner state for one organization.
func (a *App) getOrgHealth(w http.ResponseWriter, r *http.Request) error {
	ctx, org := r.Context(), r.PathValue("orgId")
	name, err := a.DB.ReplicaSet(ctx)
	mongo := bson.M{"ok": err == nil, "replica_set": nil}
	if err == nil {
		mongo["replica_set"] = name
	}
	count := func(filter bson.M) (int64, error) {
		filter["org_id"] = org
		return a.DB.Coll("jobs").CountDocuments(ctx, filter)
	}
	day := time.Now().UTC().Add(-24 * time.Hour)
	queue := bson.M{"oldest_queued_age_secs": nil}
	for key, filter := range map[string]bson.M{"queued": {"state": "queued"}, "leased": {"state": "leased"}, "failed_24h": {"state": "failed", "updated_at": bson.M{"$gte": day}}, "dead_24h": {"state": "dead", "updated_at": bson.M{"$gte": day}}} {
		n, err := count(filter)
		if err != nil {
			return err
		}
		queue[key] = n
	}
	if oldest, err := findMany[JobDoc](ctx, a.DB, "jobs", bson.M{"org_id": org, "state": "queued"}, bson.D{{Key: "next_at", Value: 1}}, 1); err != nil {
		return err
	} else if len(oldest) == 1 {
		queue["oldest_queued_age_secs"] = max(int64(time.Since(oldest[0].NextAt).Seconds()), 0)
	}
	conns, err := findMany[ConnectionDoc](ctx, a.DB, "connections", bson.M{"org_id": org, "active": true}, bson.D{{Key: "_id", Value: 1}}, 200)
	if err != nil {
		return err
	}
	connections := []bson.M{}
	for _, c := range conns {
		reasons := c.readinessReasons()
		connections = append(connections, bson.M{"id": c.ID, "name": c.Name, "provider": c.Provider, "status": c.Status, "readiness": bson.M{"unattended_ready": len(reasons) == 0, "reasons": reasons}})
	}
	status, err := a.catherdStatusDTO(ctx, org)
	if err != nil {
		return err
	}
	alive := time.Since(time.Unix(0, a.runnerBeat.Load())) < 10*time.Second
	writeJSON(w, 200, bson.M{"mongo": mongo, "queue": queue, "connections": connections, "catherd": status, "runner": bson.M{"alive": alive}})
	return nil
}
