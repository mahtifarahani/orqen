package extwrite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// GitLab is the write-side client of one GitLab connection. Every request goes through the loopback-guarded HTTP
// client of a Permit (or, for the credential check, of CheckClient); it never follows redirects.
type GitLab struct {
	// Base is the connection's base URL (for example http://127.0.0.1:4000/gitlab); /api/v4 is appended.
	Base  string
	Token string
	HC    *http.Client
	Scope Scope
}

// StatusError is a non-2xx answer.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string { return "gitlab answered HTTP " + strconv.Itoa(e.Status) }

func (g *GitLab) api(path string) (string, error) {
	u := strings.TrimRight(g.Base, "/") + "/api/v4" + path
	if err := CheckURL(g.Scope, u, false); err != nil {
		return "", err
	}
	return u, nil
}

// do sends one request. body is JSON-encoded when non-nil; the answer is decoded into out when non-nil.
func (g *GitLab) do(ctx context.Context, method, path string, body, out any) error {
	u, err := g.api(path)
	if err != nil {
		return err
	}
	var rdr io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", g.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.HC.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Status: resp.StatusCode, Body: string(raw)}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (g *GitLab) get(ctx context.Context, path string, out any) error {
	return g.do(ctx, http.MethodGet, path, nil, out)
}

// pid encodes a project id for a path.
func pid(id string) string { return url.PathEscape(id) }

// CheckResult is what a GET-only inspection of the write credential found.
type CheckResult struct {
	Status   string // ok | refused | insufficient
	Username string
	UserID   string
	Scopes   []string
	Projects []ProjectAccess
	Detail   string
}

// ProjectAccess is the access level the credential has on a project.
type ProjectAccess struct {
	ProviderProjectID string
	AccessLevel       int
}

// CheckCredential reads /user, /personal_access_tokens/self and each allowlisted project with GET only.
func (g *GitLab) CheckCredential(ctx context.Context, projects []string) (CheckResult, error) {
	res := CheckResult{Status: "ok"}
	var user struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
	}
	if err := g.get(ctx, "/user", &user); err != nil {
		var se *StatusError
		if asStatus(err, &se) && (se.Status == 401 || se.Status == 403) {
			res.Status, res.Detail = "refused", fmt.Sprintf("HTTP %d", se.Status)
			return res, nil
		}
		return res, err
	}
	res.Username, res.UserID = user.Username, strconv.Itoa(user.ID)
	var self struct {
		Scopes []string `json:"scopes"`
	}
	if err := g.get(ctx, "/personal_access_tokens/self", &self); err != nil {
		var se *StatusError
		if !asStatus(err, &se) {
			return res, err
		}
		res.Detail = "scopes unknown"
	} else {
		res.Scopes = self.Scopes
		hasAPI := false
		for _, s := range self.Scopes {
			hasAPI = hasAPI || s == "api"
		}
		if !hasAPI {
			res.Status, res.Detail = "insufficient", "the token lacks the api scope"
		}
	}
	for _, p := range projects {
		var proj struct {
			Permissions struct {
				ProjectAccess *struct {
					AccessLevel int `json:"access_level"`
				} `json:"project_access"`
				GroupAccess *struct {
					AccessLevel int `json:"access_level"`
				} `json:"group_access"`
			} `json:"permissions"`
		}
		if err := g.get(ctx, "/projects/"+pid(p), &proj); err != nil {
			var se *StatusError
			if asStatus(err, &se) && (se.Status == 403 || se.Status == 404) {
				res.Projects = append(res.Projects, ProjectAccess{p, 0})
				res.Status = "insufficient"
				continue
			}
			return res, err
		}
		level := 0
		if a := proj.Permissions.ProjectAccess; a != nil && a.AccessLevel > level {
			level = a.AccessLevel
		}
		if a := proj.Permissions.GroupAccess; a != nil && a.AccessLevel > level {
			level = a.AccessLevel
		}
		res.Projects = append(res.Projects, ProjectAccess{p, level})
		if level < 30 {
			res.Status = "insufficient"
		}
	}
	return res, nil
}

func asStatus(err error, target **StatusError) bool {
	for err != nil {
		if se, ok := err.(*StatusError); ok {
			*target = se
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// CheckClient returns the guarded client for the credential check, which runs outside Execute but is GET only.
func CheckClient(scope Scope) *http.Client { return NewClient(scope, nil, nil) }

// MR is the part of a merge request Orqen reads back.
type MR struct {
	IID          int    `json:"iid"`
	WebURL       string `json:"web_url"`
	SHA          string `json:"sha"`
	State        string `json:"state"`
	Description  string `json:"description"`
	SourceBranch string `json:"source_branch"`
}

// MRParams creates a draft merge request. Orqen never sets remove_source_branch, merges, approves or closes.
type MRParams struct {
	Title        string `json:"title"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	Description  string `json:"description"`
	RemoveSource bool   `json:"remove_source_branch"`
}

// CreateMR opens a merge request.
func (g *GitLab) CreateMR(ctx context.Context, projectID string, p MRParams) (MR, error) {
	var out MR
	err := g.do(ctx, http.MethodPost, "/projects/"+pid(projectID)+"/merge_requests", p, &out)
	return out, err
}

// FindMRByMarker lists the merge requests of a source branch (any state) and returns the one whose description
// carries the marker.
func (g *GitLab) FindMRByMarker(ctx context.Context, projectID, sourceBranch, marker string) (*MR, error) {
	var list []MR
	q := url.Values{"source_branch": {sourceBranch}, "state": {"all"}, "per_page": {"100"}}
	if err := g.get(ctx, "/projects/"+pid(projectID)+"/merge_requests?"+q.Encode(), &list); err != nil {
		return nil, err
	}
	for i := range list {
		if strings.Contains(list[i].Description, marker) {
			return &list[i], nil
		}
	}
	return nil, nil
}

// UpdateMRDescription changes only the description.
func (g *GitLab) UpdateMRDescription(ctx context.Context, projectID string, iid int, desc string) error {
	return g.do(ctx, http.MethodPut, "/projects/"+pid(projectID)+"/merge_requests/"+strconv.Itoa(iid), map[string]string{"description": desc}, nil)
}

// Note is a merge request comment.
type Note struct {
	ID   int    `json:"id"`
	Body string `json:"body"`
}

// ListNotes returns the notes of a merge request (first 100).
func (g *GitLab) ListNotes(ctx context.Context, projectID string, iid int) ([]Note, error) {
	var out []Note
	err := g.get(ctx, "/projects/"+pid(projectID)+"/merge_requests/"+strconv.Itoa(iid)+"/notes?per_page=100&sort=asc", &out)
	return out, err
}

// CreateNote adds a general comment.
func (g *GitLab) CreateNote(ctx context.Context, projectID string, iid int, body string) (Note, error) {
	var out Note
	err := g.do(ctx, http.MethodPost, "/projects/"+pid(projectID)+"/merge_requests/"+strconv.Itoa(iid)+"/notes", map[string]string{"body": body}, &out)
	return out, err
}

// Position anchors a discussion on a line of the diff.
type Position struct {
	PositionType string `json:"position_type"`
	BaseSHA      string `json:"base_sha"`
	StartSHA     string `json:"start_sha"`
	HeadSHA      string `json:"head_sha"`
	NewPath      string `json:"new_path,omitempty"`
	OldPath      string `json:"old_path,omitempty"`
	NewLine      int    `json:"new_line,omitempty"`
	OldLine      int    `json:"old_line,omitempty"`
}

// Discussion is a threaded comment.
type Discussion struct {
	ID    string `json:"id"`
	Notes []Note `json:"notes"`
}

// ListDiscussions returns the discussions of a merge request (first 100).
func (g *GitLab) ListDiscussions(ctx context.Context, projectID string, iid int) ([]Discussion, error) {
	var out []Discussion
	err := g.get(ctx, "/projects/"+pid(projectID)+"/merge_requests/"+strconv.Itoa(iid)+"/discussions?per_page=100", &out)
	return out, err
}

// CreateDiscussion starts a discussion, inline when pos is set.
func (g *GitLab) CreateDiscussion(ctx context.Context, projectID string, iid int, body string, pos *Position) (Discussion, error) {
	var out Discussion
	payload := map[string]any{"body": body}
	if pos != nil {
		payload["position"] = pos
	}
	err := g.do(ctx, http.MethodPost, "/projects/"+pid(projectID)+"/merge_requests/"+strconv.Itoa(iid)+"/discussions", payload, &out)
	return out, err
}
