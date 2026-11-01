package server

import (
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http"
)

// listRunRoles is GET /runs/{runId}/roles: the run's role executions and its host session.
func (a *App) listRunRoles(w http.ResponseWriter, r *http.Request) error {
	org, id := r.PathValue("orgId"), r.PathValue("runId")
	if _, err := a.run(r.Context(), org, id); err != nil {
		return err
	}
	rexes, err := findMany[RoleExecDoc](r.Context(), a.DB, "role_executions", bson.M{"org_id": org, "run_id": id}, bson.D{{Key: "created_at", Value: 1}, {Key: "_id", Value: 1}}, 500)
	if err != nil {
		return err
	}
	roles := []bson.M{}
	for _, x := range rexes {
		roles = append(roles, x.DTO())
	}
	var host any
	hs, err := findMany[HostSessionDoc](r.Context(), a.DB, "host_sessions", bson.M{"org_id": org, "run_id": id}, bson.D{{Key: "started_at", Value: -1}}, 1)
	if err != nil {
		return err
	}
	if len(hs) > 0 {
		host = hs[0].DTO()
	}
	writeJSON(w, 200, bson.M{"host_session": host, "roles": roles})
	return nil
}
