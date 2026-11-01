package providers

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const jiraServerProjectCap = 5000

type jira struct {
	base
	deployment string
}

// NewJira returns an IssueSource over the Jira REST API. The base URL path is preserved.
func NewJira(o Options, auth Auth, deployment string) IssueSource {
	auth.Provider, auth.Deployment = "jira", deployment
	return &jira{base: newBase(o, auth), deployment: deployment}
}
func (j *jira) u(path string, q url.Values) string {
	if j.root == nil {
		return ""
	}
	s := strings.TrimRight(j.root.String(), "/") + path
	if len(q) > 0 {
		s += "?" + q.Encode()
	}
	return s
}
func (j *jira) cloud() bool { return j.deployment == "cloud" }
func (j *jira) Identity(ctx context.Context) (Identity, error) {
	var m struct {
		AccountID    string `json:"accountId"`
		Key          string `json:"key"`
		Name         string `json:"name"`
		DisplayName  string `json:"displayName"`
		EmailAddress string `json:"emailAddress"`
	}
	if _, err := j.getJSON(ctx, j.u("/rest/api/2/myself", nil), &m); err != nil {
		if e, ok := AsError(err); ok && e.Kind == KindNotFound {
			return Identity{}, &Error{Kind: KindUnsupported, Code: "api_not_found", Message: "the Jira API was not found at this URL", Status: e.Status}
		}
		return Identity{}, err
	}
	id := first(m.AccountID, m.Key, m.Name)
	return Identity{ProviderUserID: id, Username: first(m.Name, m.EmailAddress, m.DisplayName), DisplayName: m.DisplayName}, nil
}
func first(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
func (j *jira) Capabilities(ctx context.Context) (Capabilities, error) {
	c := Capabilities{Features: map[string]string{}}
	var info struct {
		Version        string `json:"version"`
		DeploymentType string `json:"deploymentType"`
	}
	if _, err := j.getJSON(ctx, j.u("/rest/api/2/serverInfo", nil), &info); err == nil {
		c.ServerVersion, c.Deployment = info.Version, info.DeploymentType
	} else if fatal(err) {
		return c, err
	}
	feature := func(name string, err error) error {
		switch {
		case err == nil:
			c.Features[name] = FeatureAvailable
		case fatal(err):
			return err
		default:
			e, _ := AsError(err)
			switch e.Kind {
			case KindPermissionDenied:
				c.Features[name] = FeatureDenied
			case KindNotFound, KindUnsupported:
				c.Features[name] = FeatureUnsupported
			default:
				c.Features[name] = FeatureUnknown
			}
		}
		return nil
	}
	_, err := j.ListProjects(ctx, "", 0, 1)
	if err = feature("browse_projects", err); err != nil {
		return c, err
	}
	_, err = j.ListBoards(ctx, "", 0, 1)
	if err = feature("boards", err); err != nil {
		return c, err
	}
	if err = feature("jql_search", j.CheckJQL(ctx, "created is not EMPTY")); err != nil {
		return c, err
	}
	return c, nil
}
func (j *jira) ListProjects(ctx context.Context, query string, startAt, limit int) (JiraProjectsPage, error) {
	type project struct {
		ID   flex   `json:"id"`
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	conv := func(ps []project) []JiraProject {
		out := make([]JiraProject, 0, len(ps))
		for _, p := range ps {
			out = append(out, JiraProject{string(p.ID), p.Key, p.Name})
		}
		return out
	}
	if j.cloud() {
		q := url.Values{"startAt": {strconv.Itoa(startAt)}, "maxResults": {strconv.Itoa(limit)}}
		if query != "" {
			q.Set("query", query)
		}
		var r struct {
			Values []project `json:"values"`
			IsLast bool      `json:"isLast"`
		}
		if _, err := j.getJSON(ctx, j.u("/rest/api/3/project/search", q), &r); err != nil {
			return JiraProjectsPage{}, err
		}
		page := JiraProjectsPage{Items: conv(r.Values), NextStartAt: -1}
		if !r.IsLast && len(r.Values) > 0 {
			page.NextStartAt = startAt + len(r.Values)
		}
		return page, nil
	}
	var all []project
	if _, err := j.getJSON(ctx, j.u("/rest/api/2/project", nil), &all); err != nil {
		return JiraProjectsPage{}, err
	}
	page := JiraProjectsPage{NextStartAt: -1}
	if len(all) > jiraServerProjectCap {
		all, page.Truncated = all[:jiraServerProjectCap], true
	}
	needle := strings.ToLower(query)
	filtered := all[:0:0]
	for _, p := range all {
		if needle == "" || strings.Contains(strings.ToLower(p.Key), needle) || strings.Contains(strings.ToLower(p.Name), needle) {
			filtered = append(filtered, p)
		}
	}
	sort.SliceStable(filtered, func(a, b int) bool { return filtered[a].Key < filtered[b].Key })
	if startAt > len(filtered) {
		startAt = len(filtered)
	}
	end := min(startAt+limit, len(filtered))
	page.Items = conv(filtered[startAt:end])
	if end < len(filtered) {
		page.NextStartAt = end
	}
	return page, nil
}
func (j *jira) GetProject(ctx context.Context, id string) (JiraProject, error) {
	var p struct {
		ID   flex   `json:"id"`
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	if _, err := j.getJSON(ctx, j.u("/rest/api/2/project/"+escape(id), nil), &p); err != nil {
		return JiraProject{}, err
	}
	return JiraProject{string(p.ID), p.Key, p.Name}, nil
}

type jiraBoard struct {
	ID       flex   `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Location struct {
		ProjectID flex `json:"projectId"`
	} `json:"location"`
}

func (b jiraBoard) board() Board {
	return Board{ID: string(b.ID), Name: b.Name, Type: b.Type, ProjectID: string(b.Location.ProjectID)}
}

// agile404 turns a missing Agile API into an unsupported-capability error.
func agile404(err error) error {
	if e, ok := AsError(err); ok && e.Kind == KindNotFound {
		return &Error{Kind: KindUnsupported, Code: "agile_unsupported", Message: "the Jira Agile (board) API is not available", Status: e.Status}
	}
	return err
}
func (j *jira) ListBoards(ctx context.Context, projectID string, startAt, limit int) (BoardsPage, error) {
	q := url.Values{"startAt": {strconv.Itoa(startAt)}, "maxResults": {strconv.Itoa(limit)}}
	if projectID != "" {
		q.Set("projectKeyOrId", projectID)
	}
	var r struct {
		Values []jiraBoard `json:"values"`
		IsLast bool        `json:"isLast"`
	}
	if _, err := j.getJSON(ctx, j.u("/rest/agile/1.0/board", q), &r); err != nil {
		return BoardsPage{}, agile404(err)
	}
	page := BoardsPage{Items: make([]Board, 0, len(r.Values)), NextStartAt: -1}
	for _, b := range r.Values {
		page.Items = append(page.Items, b.board())
	}
	if !r.IsLast && len(r.Values) > 0 {
		page.NextStartAt = startAt + len(r.Values)
	}
	return page, nil
}
func (j *jira) GetBoard(ctx context.Context, id string) (Board, error) {
	var b jiraBoard
	if _, err := j.getJSON(ctx, j.u("/rest/agile/1.0/board/"+escape(id), nil), &b); err != nil {
		return Board{}, err
	}
	return b.board(), nil
}
func (j *jira) CheckJQL(ctx context.Context, jql string) error {
	q := url.Values{"jql": {jql}, "maxResults": {"0"}}
	path := "/rest/api/2/search"
	if j.cloud() {
		path = "/rest/api/3/search/jql"
	}
	var ignored map[string]any
	if _, err := j.getJSON(ctx, j.u(path, q), &ignored); err != nil {
		if e, ok := AsError(err); ok && e.Kind == KindInvalidRequest {
			e.Code = "jql_invalid"
		}
		return err
	}
	return nil
}
