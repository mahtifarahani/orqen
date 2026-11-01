package providers

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

type gitlab struct {
	base
	api *url.URL
}

// NewGitLab returns a CodeHost over the GitLab v4 REST API. The base URL path is preserved.
func NewGitLab(o Options, auth Auth) CodeHost {
	auth.Provider = "gitlab"
	g := &gitlab{base: newBase(o, auth)}
	if g.initErr == nil {
		a := *g.root
		a.Path = strings.TrimRight(a.Path, "/") + "/api/v4"
		a.RawPath = ""
		g.api = &a
	}
	return g
}
func (g *gitlab) u(path string, q url.Values) string {
	if g.api == nil {
		return ""
	}
	s := g.api.String() + path
	if len(q) > 0 {
		s += "?" + q.Encode()
	}
	return s
}

type glProject struct {
	ID                flex   `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	WebURL            string `json:"web_url"`
	Archived          bool   `json:"archived"`
	Visibility        string `json:"visibility"`
	LastActivityAt    string `json:"last_activity_at"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
}

func (p glProject) project() Project {
	return Project{ID: string(p.ID), Name: p.Name, PathWithNamespace: p.PathWithNamespace, DefaultBranch: p.DefaultBranch, WebURL: p.WebURL, Archived: p.Archived, Visibility: p.Visibility, LastActivityAt: p.LastActivityAt, HTTPURLToRepo: p.HTTPURLToRepo}
}
func (g *gitlab) Identity(ctx context.Context) (Identity, error) {
	var u struct {
		ID       flex   `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if _, err := g.getJSON(ctx, g.u("/user", nil), &u); err != nil {
		if e, ok := AsError(err); ok && e.Kind == KindNotFound {
			return Identity{}, &Error{Kind: KindUnsupported, Code: "api_not_found", Message: "the GitLab API was not found at this URL", Status: e.Status}
		}
		return Identity{}, err
	}
	return Identity{ProviderUserID: string(u.ID), Username: u.Username, DisplayName: u.Name}, nil
}
func (g *gitlab) Capabilities(ctx context.Context) (Capabilities, error) {
	c := Capabilities{Deployment: "gitlab", Features: map[string]string{}}
	var v struct {
		Version string `json:"version"`
	}
	if _, err := g.getJSON(ctx, g.u("/version", nil), &v); err == nil {
		c.ServerVersion = v.Version
	} else if fatal(err) {
		return c, err
	}
	var t struct {
		Scopes []string `json:"scopes"`
	}
	if _, err := g.getJSON(ctx, g.u("/personal_access_tokens/self", nil), &t); err == nil {
		c.TokenScopes = append([]string{}, t.Scopes...)
	} else if fatal(err) {
		return c, err
	}
	hasScope := func(names ...string) bool {
		for _, s := range c.TokenScopes {
			for _, n := range names {
				if s == n {
					return true
				}
			}
		}
		return false
	}
	set := func(feature string, ok bool) {
		switch {
		case c.TokenScopes == nil:
			c.Features[feature] = FeatureUnknown
		case ok:
			c.Features[feature] = FeatureAvailable
		default:
			c.Features[feature] = FeatureDenied
		}
	}
	set("list_projects", hasScope("api", "read_api"))
	set("read_repository_files", hasScope("api", "read_api", "read_repository"))
	set("write_api", hasScope("api"))
	return c, nil
}

const projectsQuery = "membership=true&per_page=100&order_by=id&sort=asc"

func (g *gitlab) ListProjectsPage(ctx context.Context, cursor string) (ProjectsPage, error) {
	var target string
	switch {
	case cursor == "":
		target = g.u("/projects", nil) + "?" + projectsQuery + "&pagination=keyset"
	case strings.HasPrefix(cursor, "page:"):
		n, err := strconv.Atoi(strings.TrimPrefix(cursor, "page:"))
		if err != nil || n < 1 {
			return ProjectsPage{}, newError(KindInvalidOutput, "invalid_cursor", "the page cursor is invalid")
		}
		target = g.u("/projects", nil) + "?" + projectsQuery + "&page=" + strconv.Itoa(n)
	default:
		if err := g.checkNext(cursor); err != nil {
			return ProjectsPage{}, err
		}
		target = cursor
	}
	var raw []glProject
	h, err := g.getJSON(ctx, target, &raw)
	if err != nil {
		return ProjectsPage{}, err
	}
	page := ProjectsPage{Items: make([]Project, 0, len(raw))}
	for _, p := range raw {
		page.Items = append(page.Items, p.project())
	}
	if next := linkNext(h.Get("Link")); next != "" {
		if err = g.checkNext(next); err != nil {
			return ProjectsPage{}, err
		}
		page.Next = next
	} else if np := strings.TrimSpace(h.Get("X-Next-Page")); np != "" {
		page.Next = "page:" + np
	}
	return page, nil
}

// checkNext keeps keyset cursors on the API origin and path prefix, so a hostile Link header cannot redirect the token.
func (g *gitlab) checkNext(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != g.api.Scheme || !strings.EqualFold(u.Host, g.api.Host) || !strings.HasPrefix(u.Path, g.api.Path+"/") || u.User != nil {
		return newError(KindInvalidOutput, "invalid_next_link", "the pagination link points outside the API")
	}
	return nil
}
func linkNext(header string) string {
	for _, part := range strings.Split(header, ",") {
		segs := strings.Split(part, ";")
		if len(segs) < 2 {
			continue
		}
		for _, s := range segs[1:] {
			if strings.ReplaceAll(strings.TrimSpace(s), " ", "") == `rel="next"` {
				return strings.Trim(strings.TrimSpace(segs[0]), "<>")
			}
		}
	}
	return ""
}
func (g *gitlab) GetProject(ctx context.Context, id string) (Project, error) {
	var p glProject
	if _, err := g.getJSON(ctx, g.u("/projects/"+escape(id), nil), &p); err != nil {
		return Project{}, err
	}
	return p.project(), nil
}
func (g *gitlab) ListBranches(ctx context.Context, pid, search string, limit int) ([]Branch, error) {
	q := url.Values{"per_page": {strconv.Itoa(min(max(limit, 1), 100))}}
	if search != "" {
		q.Set("search", search)
	}
	var raw []struct {
		Name      string `json:"name"`
		Default   bool   `json:"default"`
		Protected bool   `json:"protected"`
	}
	if _, err := g.getJSON(ctx, g.u("/projects/"+escape(pid)+"/repository/branches", q), &raw); err != nil {
		return nil, err
	}
	out := make([]Branch, 0, len(raw))
	for _, b := range raw {
		out = append(out, Branch{b.Name, b.Default, b.Protected})
	}
	return out, nil
}
func (g *gitlab) GetBranch(ctx context.Context, pid, name string) (Branch, error) {
	var b struct {
		Name      string `json:"name"`
		Default   bool   `json:"default"`
		Protected bool   `json:"protected"`
	}
	if _, err := g.getJSON(ctx, g.u("/projects/"+escape(pid)+"/repository/branches/"+escape(name), nil), &b); err != nil {
		return Branch{}, err
	}
	return Branch{b.Name, b.Default, b.Protected}, nil
}
func (g *gitlab) ResolveCommit(ctx context.Context, pid, ref string) (string, error) {
	var c struct {
		ID string `json:"id"`
	}
	if _, err := g.getJSON(ctx, g.u("/projects/"+escape(pid)+"/repository/commits/"+escape(ref), nil), &c); err != nil {
		return "", err
	}
	if c.ID == "" {
		return "", newError(KindInvalidOutput, "invalid_output", "the commit has no id")
	}
	return c.ID, nil
}
func (g *gitlab) ListTree(ctx context.Context, pid, ref, path string) ([]TreeEntry, error) {
	q := url.Values{"ref": {ref}, "per_page": {"100"}}
	if path != "" && path != "." {
		q.Set("path", path)
	}
	var raw []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
	}
	if _, err := g.getJSON(ctx, g.u("/projects/"+escape(pid)+"/repository/tree", q), &raw); err != nil {
		return nil, err
	}
	out := make([]TreeEntry, 0, len(raw))
	for _, e := range raw {
		out = append(out, TreeEntry{e.Name, e.Path, e.Type})
	}
	return out, nil
}
func (g *gitlab) ReadFileRaw(ctx context.Context, pid, ref, path string, max int64) ([]byte, bool, error) {
	return g.getRaw(ctx, g.u("/projects/"+escape(pid)+"/repository/files/"+escape(path)+"/raw", url.Values{"ref": {ref}}), max)
}

func (g *gitlab) GetMR(ctx context.Context, pid string, iid int) (MergeRequest, error) {
	var m struct {
		IID          int    `json:"iid"`
		State        string `json:"state"`
		SHA          string `json:"sha"`
		WebURL       string `json:"web_url"`
		Title        string `json:"title"`
		Draft        bool   `json:"draft"`
		HasConflicts bool   `json:"has_conflicts"`
		Blocking     *bool  `json:"blocking_discussions_resolved"`
		MergeCommit  string `json:"merge_commit_sha"`
		Description  string `json:"description"`
		DiffRefs     struct {
			Base  string `json:"base_sha"`
			Start string `json:"start_sha"`
			Head  string `json:"head_sha"`
		} `json:"diff_refs"`
	}
	if _, err := g.getJSON(ctx, g.u("/projects/"+escape(pid)+"/merge_requests/"+strconv.Itoa(iid), nil), &m); err != nil {
		return MergeRequest{}, err
	}
	out := MergeRequest{IID: m.IID, State: m.State, SHA: m.SHA, WebURL: m.WebURL, Title: m.Title, Draft: m.Draft, HasConflicts: m.HasConflicts, BlockingDiscussionsResolved: m.Blocking == nil || *m.Blocking,
		BaseSHA: m.DiffRefs.Base, StartSHA: m.DiffRefs.Start, HeadSHA: m.DiffRefs.Head, MergeCommitSHA: m.MergeCommit, Description: m.Description}
	return out, nil
}
func (g *gitlab) ListMRPipelines(ctx context.Context, pid string, iid int) ([]PipelineRef, error) {
	var raw []struct {
		ID     int    `json:"id"`
		SHA    string `json:"sha"`
		Status string `json:"status"`
		Source string `json:"source"`
	}
	if _, err := g.getJSON(ctx, g.u("/projects/"+escape(pid)+"/merge_requests/"+strconv.Itoa(iid)+"/pipelines", nil), &raw); err != nil {
		return nil, err
	}
	out := make([]PipelineRef, 0, len(raw))
	for _, p := range raw {
		out = append(out, PipelineRef{p.ID, p.SHA, p.Status, p.Source})
	}
	return out, nil
}
func (g *gitlab) GetPipeline(ctx context.Context, pid string, id int) (PipelineRef, error) {
	var p struct {
		ID     int    `json:"id"`
		SHA    string `json:"sha"`
		Status string `json:"status"`
		Source string `json:"source"`
	}
	if _, err := g.getJSON(ctx, g.u("/projects/"+escape(pid)+"/pipelines/"+strconv.Itoa(id), nil), &p); err != nil {
		return PipelineRef{}, err
	}
	return PipelineRef{p.ID, p.SHA, p.Status, p.Source}, nil
}
func (g *gitlab) ListPipelineJobs(ctx context.Context, pid string, id int) ([]PipelineJob, error) {
	var raw []struct {
		Name         string `json:"name"`
		Status       string `json:"status"`
		AllowFailure bool   `json:"allow_failure"`
	}
	if _, err := g.getJSON(ctx, g.u("/projects/"+escape(pid)+"/pipelines/"+strconv.Itoa(id)+"/jobs?per_page=100", nil), &raw); err != nil {
		return nil, err
	}
	out := make([]PipelineJob, 0, len(raw))
	for _, j := range raw {
		out = append(out, PipelineJob{j.Name, j.Status, j.AllowFailure})
	}
	return out, nil
}

// ListMRDiscussionsUnresolved counts resolvable discussions that are still open.
func (g *gitlab) ListMRDiscussionsUnresolved(ctx context.Context, pid string, iid int) (int, error) {
	var raw []struct {
		Notes []struct {
			Resolvable bool `json:"resolvable"`
			Resolved   bool `json:"resolved"`
		} `json:"notes"`
	}
	if _, err := g.getJSON(ctx, g.u("/projects/"+escape(pid)+"/merge_requests/"+strconv.Itoa(iid)+"/discussions?per_page=100", nil), &raw); err != nil {
		return 0, err
	}
	n := 0
	for _, d := range raw {
		for _, note := range d.Notes {
			if note.Resolvable && !note.Resolved {
				n++
				break
			}
		}
	}
	return n, nil
}
