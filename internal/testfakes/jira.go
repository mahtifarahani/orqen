package testfakes

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const jiraPrefix = "/jira"

// Jira serves 3 projects, boards for project 10000 and a JQL check that rejects queries containing BROKEN.
type Jira struct {
	tracker
	// Cloud selects Basic (User:Token) authentication and the cloud project search; otherwise Bearer or Basic.
	Cloud bool
	User  string
	Token string
	// AgileMissing makes every /rest/agile request answer 404.
	AgileMissing bool
	Delay        time.Duration
	// Issues are keyed by issue key; see SeedIssues. Guarded by imu.
	Issues map[string]*FakeIssue
	// Fail forces a status for matching paths (relative to /jira, after authentication) while Count > 0.
	Fail map[string]*Failure
	// Now returns the fake clock (default time.Now).
	Now  func() time.Time
	imu  sync.Mutex
	jqls []string
}

func NewJira() *Jira {
	return &Jira{User: "bot@example.com", Token: "jira-token", Issues: map[string]*FakeIssue{}, Fail: map[string]*Failure{}}
}

// SetToken changes the accepted token; the previous one is answered with 401 from then on.
func (j *Jira) SetToken(t string) { j.imu.Lock(); j.Token = t; j.imu.Unlock() }

// SetDelay makes every request wait d (0 removes it); SetFail forces a status for a path prefix.
func (j *Jira) SetDelay(d time.Duration) { j.imu.Lock(); j.Delay = d; j.imu.Unlock() }
func (j *Jira) SetFail(prefix string, f *Failure) {
	j.imu.Lock()
	j.Fail[prefix] = f
	j.imu.Unlock()
}
func (j *Jira) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

var jiraProjects = []map[string]any{{"id": "10000", "key": "ORQ", "name": "Orqen"}, {"id": "10001", "key": "WEB", "name": "Website"}, {"id": "10002", "key": "OPS", "name": "Operations"}}

func (j *Jira) authorized(r *http.Request) bool {
	j.imu.Lock()
	token := j.Token
	j.imu.Unlock()
	h := r.Header.Get("Authorization")
	if v, ok := strings.CutPrefix(h, "Bearer "); ok {
		return !j.Cloud && v == token
	}
	if v, ok := strings.CutPrefix(h, "Basic "); ok {
		return v == base64.StdEncoding.EncodeToString([]byte(j.User+":"+token))
	}
	return false
}
func (j *Jira) forcedFailure(w http.ResponseWriter, path string) bool {
	j.imu.Lock()
	for prefix, f := range j.Fail {
		if f.Count > 0 && strings.HasPrefix(path, prefix) {
			f.Count--
			j.imu.Unlock()
			if f.RetryAfter != "" {
				w.Header().Set("Retry-After", f.RetryAfter)
			}
			writeJSON(w, f.Status, map[string]any{"errorMessages": []string{"forced"}})
			return true
		}
	}
	j.imu.Unlock()
	return false
}
func (j *Jira) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		j.enter(r)
		defer j.leave()
		j.imu.Lock()
		delay := j.Delay
		j.imu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		path, ok := strings.CutPrefix(r.URL.EscapedPath(), jiraPrefix)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if !j.authorized(r) {
			writeJSON(w, 401, map[string]any{"errorMessages": []string{"unauthorized"}})
			return
		}
		if j.forcedFailure(w, path) {
			return
		}
		switch {
		case path == "/rest/api/2/myself":
			writeJSON(w, 200, map[string]any{"accountId": "acc-1", "name": "bot", "displayName": "Orqen Bot", "emailAddress": j.User})
		case path == "/rest/api/2/serverInfo":
			dep := "Server"
			if j.Cloud {
				dep = "Cloud"
			}
			writeJSON(w, 200, map[string]any{"version": "9.12.0", "deploymentType": dep})
		case path == "/rest/api/3/project/search":
			j.cloudProjects(w, r)
		case path == "/rest/api/2/project":
			writeJSON(w, 200, jiraProjects)
		case strings.HasPrefix(path, "/rest/api/2/project/"):
			key, _ := url.PathUnescape(strings.TrimPrefix(path, "/rest/api/2/project/"))
			for _, p := range jiraProjects {
				if p["id"] == key || p["key"] == key {
					writeJSON(w, 200, p)
					return
				}
			}
			writeJSON(w, 404, map[string]any{"errorMessages": []string{"No project could be found"}})
		case strings.HasPrefix(path, "/rest/agile/1.0/board"):
			j.boards(w, r, strings.TrimPrefix(path, "/rest/agile/1.0/board"))
		case path == "/rest/api/3/search/jql" || path == "/rest/api/2/search":
			if strings.Contains(r.URL.Query().Get("jql"), "BROKEN") {
				writeJSON(w, 400, map[string]any{"errorMessages": []string{"Error in the JQL Query: unexpected token"}})
				return
			}
			j.search(w, r, path == "/rest/api/3/search/jql")
		case strings.HasPrefix(path, "/rest/api/2/issue/"):
			j.issueRoutes(w, r, strings.TrimPrefix(path, "/rest/api/2/issue/"))
		default:
			writeJSON(w, 404, map[string]any{"errorMessages": []string{"not found"}})
		}
	})
}
func (j *Jira) cloudProjects(w http.ResponseWriter, r *http.Request) {
	start, limit := intParam(r, "startAt", 0), intParam(r, "maxResults", 50)
	query := strings.ToLower(r.URL.Query().Get("query"))
	matches := []map[string]any{}
	for _, p := range jiraProjects {
		if query == "" || strings.Contains(strings.ToLower(p["key"].(string)+" "+p["name"].(string)), query) {
			matches = append(matches, p)
		}
	}
	start = min(start, len(matches))
	end := min(start+limit, len(matches))
	writeJSON(w, 200, map[string]any{"values": matches[start:end], "startAt": start, "maxResults": limit, "isLast": end >= len(matches)})
}
func (j *Jira) boards(w http.ResponseWriter, r *http.Request, rest string) {
	if j.AgileMissing {
		writeJSON(w, 404, map[string]any{"errorMessages": []string{"agile not available"}})
		return
	}
	all := []map[string]any{
		{"id": 1, "name": "ORQ board", "type": "scrum", "location": map[string]any{"projectId": 10000}},
		{"id": 2, "name": "ORQ kanban", "type": "kanban", "location": map[string]any{"projectId": 10000}},
		{"id": 3, "name": "WEB board", "type": "kanban", "location": map[string]any{"projectId": 10001}},
	}
	if id, tail, ok := strings.Cut(strings.Trim(rest, "/"), "/"); ok && tail == "sprint" {
		j.sprints(w, r, id)
		return
	}
	if id := strings.Trim(rest, "/"); id != "" {
		for _, b := range all {
			if strconv.Itoa(b["id"].(int)) == id {
				writeJSON(w, 200, b)
				return
			}
		}
		writeJSON(w, 404, map[string]any{"errorMessages": []string{"board not found"}})
		return
	}
	project := r.URL.Query().Get("projectKeyOrId")
	matches := []map[string]any{}
	for _, b := range all {
		pid := strconv.Itoa(b["location"].(map[string]any)["projectId"].(int))
		if project == "" || project == pid || project == "ORQ" && pid == "10000" || project == "WEB" && pid == "10001" {
			matches = append(matches, b)
		}
	}
	start, limit := intParam(r, "startAt", 0), intParam(r, "maxResults", 50)
	start = min(start, len(matches))
	end := min(start+limit, len(matches))
	writeJSON(w, 200, map[string]any{"values": matches[start:end], "startAt": start, "maxResults": limit, "isLast": end >= len(matches)})
}
