package testfakes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RecordedWrite is one write the fake accepted (or refused for its token).
type RecordedWrite struct {
	Method    string `json:"method"`
	Path      string `json:"path"`
	TokenKind string `json:"token_kind"`
	BodySHA   string `json:"body_sha"`
	Status    int    `json:"status"`
}

// FakeJob is a pipeline job.
type FakeJob struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	AllowFailure bool   `json:"allow_failure"`
}

// FakePipeline is a pipeline on a commit.
type FakePipeline struct {
	ID     int       `json:"id"`
	SHA    string    `json:"sha"`
	Status string    `json:"status"`
	Source string    `json:"source"`
	Jobs   []FakeJob `json:"jobs"`
}

// FakeNote is a note or discussion note.
type FakeNote struct {
	ID       int            `json:"id"`
	Body     string         `json:"body"`
	Position map[string]any `json:"position,omitempty"`
}

// FakeDiscussion is a thread.
type FakeDiscussion struct {
	ID    string     `json:"id"`
	Notes []FakeNote `json:"notes"`
}

// FakeMR is a merge request.
type FakeMR struct {
	IID          int              `json:"iid"`
	Project      int              `json:"project_id"`
	Title        string           `json:"title"`
	Description  string           `json:"description"`
	SourceBranch string           `json:"source_branch"`
	TargetBranch string           `json:"target_branch"`
	State        string           `json:"state"`
	SHA          string           `json:"sha"`
	BaseSHA      string           `json:"-"`
	WebURL       string           `json:"web_url"`
	Draft        bool             `json:"draft"`
	MergeCommit  string           `json:"merge_commit_sha,omitempty"`
	Conflicts    bool             `json:"has_conflicts"`
	Notes        []FakeNote       `json:"-"`
	Discussions  []FakeDiscussion `json:"-"`
}

type mrState struct {
	mrs       map[int][]*FakeMR // project -> merge requests
	pipelines []*FakePipeline
	writes    []RecordedWrite
	seq       int
	hangs     map[string]int
}

// Repos links the fake to the smart-HTTP repositories: branch heads, diffs and pushes feed merge requests.
func (g *GitLab) LinkRepos(r *GitRepos) {
	g.mu.Lock()
	g.repos = r
	g.mu.Unlock()
	r.mu.Lock()
	prev := r.OnPush
	r.OnPush = func(project, branch, sha string) {
		if prev != nil {
			prev(project, branch, sha)
		}
		g.onPush(project, branch, sha)
	}
	r.mu.Unlock()
}

func projectIDOf(path string) int {
	// group-<g>/proj-<n>
	if i := strings.LastIndex(path, "proj-"); i >= 0 {
		n, _ := strconv.Atoi(path[i+5:])
		return n
	}
	return 0
}

func (g *GitLab) state() *mrState {
	if g.mr == nil {
		g.mr = &mrState{mrs: map[int][]*FakeMR{}, hangs: map[string]int{}}
	}
	return g.mr
}

func (g *GitLab) onPush(project, branch, sha string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.state()
	id := projectIDOf(project)
	for _, mr := range st.mrs[id] {
		if mr.SourceBranch == branch && mr.State == "opened" {
			mr.SHA = sha
		}
	}
	if g.AutoPipeline {
		st.seq++
		st.pipelines = append(st.pipelines, &FakePipeline{ID: st.seq, SHA: sha, Status: "running", Source: "merge_request_event", Jobs: []FakeJob{{Name: "build", Status: "running"}, {Name: "test", Status: "pending"}}})
	}
}

// SetPipeline records or replaces the pipeline of a commit.
func (g *GitLab) SetPipeline(sha, status string, jobs []FakeJob) FakePipeline {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.state()
	for _, p := range st.pipelines {
		if p.SHA == sha {
			p.Status = status
			if jobs != nil {
				p.Jobs = jobs
			}
			return *p
		}
	}
	st.seq++
	p := &FakePipeline{ID: st.seq, SHA: sha, Status: status, Source: "merge_request_event", Jobs: jobs}
	if p.Jobs == nil {
		p.Jobs = []FakeJob{{Name: "build", Status: status}, {Name: "test", Status: status}}
	}
	st.pipelines = append(st.pipelines, p)
	return *p
}

// Merge marks a merge request merged (as the person would in GitLab).
func (g *GitLab) Merge(project, iid int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, mr := range g.state().mrs[project] {
		if mr.IID == iid {
			mr.State, mr.MergeCommit = "merged", strings.Repeat("c", 40)
			return true
		}
	}
	return false
}

// CloseMR marks a merge request closed.
func (g *GitLab) CloseMR(project, iid int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, mr := range g.state().mrs[project] {
		if mr.IID == iid {
			mr.State = "closed"
			return true
		}
	}
	return false
}

// MRs returns copies of a project's merge requests.
func (g *GitLab) MRs(project int) []FakeMR {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []FakeMR
	for _, mr := range g.state().mrs[project] {
		out = append(out, *mr)
	}
	return out
}

// Writes lists every write the fake served, merge requests, notes and accepted pushes alike.
func (g *GitLab) Writes() []RecordedWrite {
	g.mu.Lock()
	out := append([]RecordedWrite{}, g.state().writes...)
	repos := g.repos
	g.mu.Unlock()
	if repos != nil {
		for _, p := range repos.Pushes() {
			out = append(out, RecordedWrite{Method: "PUSH", Path: p.Project + "#" + p.Branch + "@" + p.SHA, TokenKind: p.TokenKind, Status: 200})
		}
	}
	return out
}

// HangAfterWrite makes the next count writes below the path prefix take effect and then hold the connection until the
// client gives up (a lost response).
func (g *GitLab) HangAfterWrite(prefix string, count int) {
	g.mu.Lock()
	g.state().hangs[prefix] = count
	g.mu.Unlock()
}

func (g *GitLab) record(r *http.Request, kind string, body []byte, status int) {
	sum := sha256.Sum256(body)
	g.mu.Lock()
	g.state().writes = append(g.state().writes, RecordedWrite{Method: r.Method, Path: strings.TrimPrefix(r.URL.EscapedPath(), gitlabPrefix), TokenKind: kind, BodySHA: hex.EncodeToString(sum[:8]), Status: status})
	g.mu.Unlock()
}

func (g *GitLab) shouldHang(segs []string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	path := "/" + strings.Join(segs, "/")
	for prefix, n := range g.state().hangs {
		if n > 0 && strings.HasPrefix(path, prefix) {
			g.state().hangs[prefix] = n - 1
			return true
		}
	}
	return false
}

func (g *GitLab) branchSHA(project int, branch string) string {
	g.mu.Lock()
	repos := g.repos
	g.mu.Unlock()
	if repos == nil || project != 1 {
		return ""
	}
	return repos.Head("group-1/proj-1", branch)
}

func (g *GitLab) diffOf(project int, base, head string) string {
	g.mu.Lock()
	repos := g.repos
	g.mu.Unlock()
	if repos == nil || project != 1 || base == "" || head == "" {
		return ""
	}
	out, err := runGit(repos.bare("group-1/proj-1"), cleanGitEnv(os.TempDir()), "diff", "--no-color", "--no-ext-diff", "-U3", base, head)
	if err != nil {
		return ""
	}
	return out + "\n"
}

func (g *GitLab) mrJSON(mr *FakeMR) map[string]any {
	return map[string]any{"iid": mr.IID, "project_id": mr.Project, "title": mr.Title, "description": mr.Description, "source_branch": mr.SourceBranch, "target_branch": mr.TargetBranch, "state": mr.State, "sha": mr.SHA,
		"web_url": mr.WebURL, "draft": mr.Draft, "has_conflicts": mr.Conflicts, "merge_commit_sha": nilIfEmpty(mr.MergeCommit), "blocking_discussions_resolved": g.discussionsResolved(mr),
		"diff_refs": map[string]any{"base_sha": mr.BaseSHA, "start_sha": mr.BaseSHA, "head_sha": mr.SHA}, "merge_status": "can_be_merged"}
}

func (g *GitLab) discussionsResolved(mr *FakeMR) bool { return true }

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// mrRoutes serves /projects/:id/merge_requests... and pipelines. segs[0] == "projects".
func (g *GitLab) mrRoutes(w http.ResponseWriter, r *http.Request, project int, segs []string, kind string) bool {
	rest := segs[2:]
	switch {
	case len(rest) >= 1 && rest[0] == "merge_requests":
		g.mergeRequests(w, r, project, rest[1:], kind, segs)
		return true
	case len(rest) >= 1 && rest[0] == "pipelines":
		g.pipelineRoutes(w, r, rest[1:])
		return true
	}
	return false
}

func readBody(r *http.Request) []byte {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	return b
}

func (g *GitLab) writeAllowed(w http.ResponseWriter, r *http.Request, kind string, body []byte) bool {
	if kind != "write" {
		g.record(r, kind, body, 403)
		writeJSON(w, 403, map[string]string{"message": "403 Forbidden: this token may not write"})
		return false
	}
	return true
}

func (g *GitLab) mergeRequests(w http.ResponseWriter, r *http.Request, project int, rest []string, kind string, all []string) {
	st := func() *mrState { return g.state() }
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			g.mu.Lock()
			var out []map[string]any
			for _, mr := range st().mrs[project] {
				if q := r.URL.Query().Get("source_branch"); q != "" && mr.SourceBranch != q {
					continue
				}
				if s := r.URL.Query().Get("state"); s != "" && s != "all" && mr.State != s {
					continue
				}
				out = append(out, g.mrJSON(mr))
			}
			g.mu.Unlock()
			if out == nil {
				out = []map[string]any{}
			}
			writeJSON(w, 200, out)
		case http.MethodPost:
			body := readBody(r)
			if !g.writeAllowed(w, r, kind, body) {
				return
			}
			var req struct {
				Title, Description, SourceBranch, TargetBranch string
				RemoveSource                                   bool
			}
			var raw map[string]any
			_ = json.Unmarshal(body, &raw)
			req.Title, _ = raw["title"].(string)
			req.Description, _ = raw["description"].(string)
			req.SourceBranch, _ = raw["source_branch"].(string)
			req.TargetBranch, _ = raw["target_branch"].(string)
			g.mu.Lock()
			for _, mr := range st().mrs[project] {
				if mr.State == "opened" && mr.SourceBranch == req.SourceBranch && mr.TargetBranch == req.TargetBranch {
					g.mu.Unlock()
					g.record(r, kind, body, 409)
					writeJSON(w, 409, map[string]any{"message": []string{"Another open merge request already exists for this source branch"}})
					return
				}
			}
			g.mu.Unlock()
			sha := g.branchSHA(project, req.SourceBranch)
			base := g.branchSHA(project, req.TargetBranch)
			if req.SourceBranch == "" || req.TargetBranch == "" || req.Title == "" || project == 1 && sha == "" {
				g.record(r, kind, body, 400)
				writeJSON(w, 400, map[string]string{"message": "source branch does not exist or a field is missing"})
				return
			}
			g.mu.Lock()
			iid := len(st().mrs[project]) + 1
			mr := &FakeMR{IID: iid, Project: project, Title: req.Title, Description: req.Description, SourceBranch: req.SourceBranch, TargetBranch: req.TargetBranch, State: "opened", SHA: sha, BaseSHA: base,
				WebURL: fmt.Sprintf("http://gitlab.test/group-%d/proj-%d/-/merge_requests/%d", (project-1)/100+1, project, iid), Draft: strings.HasPrefix(strings.ToLower(req.Title), "draft:")}
			st().mrs[project] = append(st().mrs[project], mr)
			out := g.mrJSON(mr)
			g.mu.Unlock()
			g.record(r, kind, body, 201)
			if g.shouldHang(all) {
				holdUntilGone(r)
				return
			}
			writeJSON(w, 201, out)
		default:
			writeJSON(w, 405, map[string]string{"message": "method not allowed"})
		}
		return
	}
	iid, _ := strconv.Atoi(rest[0])
	g.mu.Lock()
	var mr *FakeMR
	for _, m := range st().mrs[project] {
		if m.IID == iid {
			mr = m
		}
	}
	g.mu.Unlock()
	if mr == nil {
		writeJSON(w, 404, map[string]string{"message": "404 Merge Request Not Found"})
		return
	}
	if len(rest) == 1 {
		switch r.Method {
		case http.MethodGet:
			g.mu.Lock()
			out := g.mrJSON(mr)
			g.mu.Unlock()
			writeJSON(w, 200, out)
		case http.MethodPut:
			body := readBody(r)
			if !g.writeAllowed(w, r, kind, body) {
				return
			}
			var raw map[string]any
			_ = json.Unmarshal(body, &raw)
			g.mu.Lock()
			for k := range raw {
				if k != "description" {
					g.mu.Unlock()
					g.record(r, kind, body, 400)
					writeJSON(w, 400, map[string]string{"message": "only the description may be changed"})
					return
				}
			}
			if d, ok := raw["description"].(string); ok {
				mr.Description = d
			}
			out := g.mrJSON(mr)
			g.mu.Unlock()
			g.record(r, kind, body, 200)
			if g.shouldHang(all) {
				holdUntilGone(r)
				return
			}
			writeJSON(w, 200, out)
		default:
			writeJSON(w, 405, map[string]string{"message": "method not allowed"})
		}
		return
	}
	switch rest[1] {
	case "notes":
		g.notes(w, r, mr, kind, all)
	case "discussions":
		g.discussions(w, r, mr, project, kind, all)
	case "pipelines":
		g.mu.Lock()
		var out []map[string]any
		for _, p := range st().pipelines {
			out = append(out, map[string]any{"id": p.ID, "sha": p.SHA, "status": p.Status, "source": p.Source})
		}
		g.mu.Unlock()
		sort.Slice(out, func(i, j int) bool { return out[i]["id"].(int) > out[j]["id"].(int) })
		if out == nil {
			out = []map[string]any{}
		}
		writeJSON(w, 200, out)
	case "changes", "diffs":
		g.mu.Lock()
		base, head := mr.BaseSHA, mr.SHA
		g.mu.Unlock()
		writeJSON(w, 200, map[string]any{"diff": g.diffOf(project, base, head)})
	default:
		writeJSON(w, 404, map[string]string{"message": "404 Not Found"})
	}
}

func (g *GitLab) notes(w http.ResponseWriter, r *http.Request, mr *FakeMR, kind string, all []string) {
	switch r.Method {
	case http.MethodGet:
		g.mu.Lock()
		out := append([]FakeNote{}, mr.Notes...)
		g.mu.Unlock()
		writeJSON(w, 200, out)
	case http.MethodPost:
		body := readBody(r)
		if !g.writeAllowed(w, r, kind, body) {
			return
		}
		var raw map[string]any
		_ = json.Unmarshal(body, &raw)
		text, _ := raw["body"].(string)
		g.mu.Lock()
		n := FakeNote{ID: len(mr.Notes) + len(mr.Discussions) + 1, Body: text}
		mr.Notes = append(mr.Notes, n)
		g.mu.Unlock()
		g.record(r, kind, body, 201)
		if g.shouldHang(all) {
			holdUntilGone(r)
			return
		}
		writeJSON(w, 201, n)
	}
}

func (g *GitLab) discussions(w http.ResponseWriter, r *http.Request, mr *FakeMR, project int, kind string, all []string) {
	switch r.Method {
	case http.MethodGet:
		g.mu.Lock()
		out := append([]FakeDiscussion{}, mr.Discussions...)
		g.mu.Unlock()
		writeJSON(w, 200, out)
	case http.MethodPost:
		body := readBody(r)
		if !g.writeAllowed(w, r, kind, body) {
			return
		}
		var raw map[string]any
		_ = json.Unmarshal(body, &raw)
		text, _ := raw["body"].(string)
		pos, _ := raw["position"].(map[string]any)
		g.mu.Lock()
		base, head := mr.BaseSHA, mr.SHA
		g.mu.Unlock()
		if pos != nil {
			if pos["head_sha"] != head || pos["base_sha"] != base || pos["start_sha"] != base || pos["position_type"] != "text" {
				g.record(r, kind, body, 400)
				writeJSON(w, 400, map[string]string{"message": "position diff refs do not match the merge request"})
				return
			}
			path, _ := pos["new_path"].(string)
			line, _ := pos["new_line"].(float64)
			if !diffHasNewLine(g.diffOf(project, base, head), path, int(line)) && g.diffOf(project, base, head) != "" {
				g.record(r, kind, body, 400)
				writeJSON(w, 400, map[string]string{"message": "line_code can't be blank: the line is not part of the diff"})
				return
			}
		}
		g.mu.Lock()
		d := FakeDiscussion{ID: fmt.Sprintf("d%032x", len(mr.Discussions)+1), Notes: []FakeNote{{ID: len(mr.Notes) + len(mr.Discussions) + 1, Body: text, Position: pos}}}
		mr.Discussions = append(mr.Discussions, d)
		g.mu.Unlock()
		g.record(r, kind, body, 201)
		if g.shouldHang(all) {
			holdUntilGone(r)
			return
		}
		writeJSON(w, 201, d)
	}
}

// diffHasNewLine reports whether the unified diff has the new-side line as an added or context line of the file.
func diffHasNewLine(diff, path string, line int) bool {
	cur := ""
	newN := 0
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+++ b/"):
			cur = strings.TrimPrefix(l, "+++ b/")
		case strings.HasPrefix(l, "@@"):
			if i := strings.Index(l, "+"); i >= 0 {
				rest := l[i+1:]
				if j := strings.IndexAny(rest, ", "); j >= 0 {
					newN, _ = strconv.Atoi(rest[:j])
				}
			}
		case cur == path && strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
			if newN == line {
				return true
			}
			newN++
		case cur == path && strings.HasPrefix(l, " "):
			if newN == line {
				return true
			}
			newN++
		}
	}
	return false
}

func (g *GitLab) pipelineRoutes(w http.ResponseWriter, r *http.Request, rest []string) {
	if len(rest) == 0 {
		g.mu.Lock()
		var out []map[string]any
		for _, p := range g.state().pipelines {
			out = append(out, map[string]any{"id": p.ID, "sha": p.SHA, "status": p.Status, "source": p.Source})
		}
		g.mu.Unlock()
		if out == nil {
			out = []map[string]any{}
		}
		writeJSON(w, 200, out)
		return
	}
	id, _ := strconv.Atoi(rest[0])
	g.mu.Lock()
	var p *FakePipeline
	for _, x := range g.state().pipelines {
		if x.ID == id {
			cp := *x
			p = &cp
		}
	}
	g.mu.Unlock()
	if p == nil {
		writeJSON(w, 404, map[string]string{"message": "404 Pipeline Not Found"})
		return
	}
	if len(rest) == 2 && rest[1] == "jobs" {
		out := []map[string]any{}
		for _, j := range p.Jobs {
			out = append(out, map[string]any{"name": j.Name, "status": j.Status, "allow_failure": j.AllowFailure})
		}
		writeJSON(w, 200, out)
		return
	}
	writeJSON(w, 200, map[string]any{"id": p.ID, "sha": p.SHA, "status": p.Status, "source": p.Source})
}

var _ = filepath.Join
var _ = url.Values{}
var _ sync.Mutex

// holdUntilGone keeps the connection open, without an answer, until the client gives up.
func holdUntilGone(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(60 * time.Second):
	}
}
