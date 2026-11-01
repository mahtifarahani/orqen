package server

import (
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http"
	"strings"
)

func startAtQuery(r *http.Request) (int, int, error) {
	start, err := queryInt(r, "start_at", 0)
	if err != nil {
		return 0, 0, err
	}
	limit, err := queryInt(r, "limit", 50)
	if err != nil {
		return 0, 0, err
	}
	if start < 0 || start > 100000 {
		return 0, 0, validation("start_at", "must be between 0 and 100000")
	}
	if limit < 1 || limit > 100 {
		return 0, 0, validation("limit", "must be between 1 and 100")
	}
	return int(start), int(limit), nil
}
func nextStartAt(n int) any {
	if n < 0 {
		return nil
	}
	return n
}
func (a *App) lookupJiraProjects(w http.ResponseWriter, r *http.Request) error {
	start, limit, err := startAtQuery(r)
	if err != nil {
		return err
	}
	conn, err := a.connection(r.Context(), r.PathValue("orgId"), r.PathValue("conId"))
	if err != nil {
		return err
	}
	src, err := a.issueSource(r.Context(), conn)
	if err != nil {
		return providerProblem(err)
	}
	page, err := src.ListProjects(r.Context(), strings.TrimSpace(r.URL.Query().Get("query")), start, limit)
	if err != nil {
		return providerProblem(err)
	}
	items := []bson.M{}
	for _, p := range page.Items {
		items = append(items, bson.M{"id": p.ID, "key": p.Key, "name": p.Name})
	}
	writeJSON(w, 200, bson.M{"items": items, "next_start_at": nextStartAt(page.NextStartAt)})
	return nil
}
func (a *App) lookupJiraBoards(w http.ResponseWriter, r *http.Request) error {
	start, limit, err := startAtQuery(r)
	if err != nil {
		return err
	}
	conn, err := a.connection(r.Context(), r.PathValue("orgId"), r.PathValue("conId"))
	if err != nil {
		return err
	}
	src, err := a.issueSource(r.Context(), conn)
	if err != nil {
		return providerProblem(err)
	}
	page, err := src.ListBoards(r.Context(), r.URL.Query().Get("project_id"), start, limit)
	if err != nil {
		return providerProblem(err)
	}
	items := []bson.M{}
	for _, b := range page.Items {
		items = append(items, bson.M{"id": b.ID, "name": b.Name, "type": b.Type})
	}
	writeJSON(w, 200, bson.M{"items": items, "next_start_at": nextStartAt(page.NextStartAt)})
	return nil
}
func (a *App) lookupGitLabBranches(w http.ResponseWriter, r *http.Request) error {
	pid := r.URL.Query().Get("provider_project_id")
	if pid == "" || len(pid) > 255 {
		return validation("provider_project_id", "is required")
	}
	limit, err := queryInt(r, "limit", 50)
	if err != nil {
		return err
	}
	if limit < 1 || limit > 100 {
		return validation("limit", "must be between 1 and 100")
	}
	conn, err := a.connection(r.Context(), r.PathValue("orgId"), r.PathValue("conId"))
	if err != nil {
		return err
	}
	host, err := a.codeHost(r.Context(), conn)
	if err != nil {
		return providerProblem(err)
	}
	branches, err := host.ListBranches(r.Context(), pid, strings.TrimSpace(r.URL.Query().Get("search")), int(limit))
	if err != nil {
		return providerProblem(err)
	}
	items := []bson.M{}
	for _, b := range branches {
		items = append(items, bson.M{"name": b.Name, "default": b.Default, "protected": b.Protected})
	}
	writeJSON(w, 200, bson.M{"items": items})
	return nil
}
