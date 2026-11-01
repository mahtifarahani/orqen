package server

import (
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http"
	"strconv"
)

func (a *App) getRunContext(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	run, err := a.run(r.Context(), org, r.PathValue("runId"))
	if err != nil {
		return err
	}
	dto, err := a.contextManifestDTO(r.Context(), run)
	if err != nil {
		return err
	}
	writeJSON(w, 200, dto)
	return nil
}

// listRunTimeline returns the events about a run, oldest first: its own subject events plus Inbox, decision and
// workspace events that carry its id.
func (a *App) listRunTimeline(w http.ResponseWriter, r *http.Request) error {
	org, id := r.PathValue("orgId"), r.PathValue("runId")
	if _, err := a.run(r.Context(), org, id); err != nil {
		return err
	}
	after, err := queryInt(r, "after", 0)
	if err != nil {
		return err
	}
	limit, err := queryInt(r, "limit", 100)
	if err != nil {
		return err
	}
	if limit < 1 || limit > 500 {
		return validation("limit", "must be between 1 and 500")
	}
	filter := bson.M{"org_id": org, "seq": bson.M{"$gt": max(after, 0)}, "$or": []bson.M{{"subject.kind": "run", "subject.id": id}, {"payload.run_id": id}}}
	docs, err := findMany[EventDoc](r.Context(), a.DB, "events", filter, bson.D{{Key: "seq", Value: 1}}, limit+1)
	if err != nil {
		return err
	}
	var next any
	if len(docs) > int(limit) {
		docs = docs[:limit]
		next = strconv.FormatInt(docs[len(docs)-1].Seq, 10)
	}
	items := []bson.M{}
	for _, d := range docs {
		items = append(items, d.DTO())
	}
	writeJSON(w, 200, bson.M{"items": items, "next_cursor": next})
	return nil
}
