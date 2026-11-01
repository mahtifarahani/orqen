// Command orqen-fakes serves the deterministic GitLab and Jira fakes from internal/testfakes for manual
// verification. It is development-only: it listens on loopback, accepts fixed test tokens and is never built into
// the orqen executable.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"orqen/internal/testfakes"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "loopback listen address")
	cloud := flag.Bool("jira-cloud", true, "authenticate Jira with Basic user:token (Cloud) instead of Bearer")
	reposDir := flag.String("repos-dir", "", "directory for the fake git repositories (default: a temporary directory)")
	seed := flag.Bool("seed-issues", true, "seed the Sprint 3 Jira issues (ORQ-1 ... WEB-1)")
	hostDir := flag.String("fakehost-dir", "", "state directory of orqen-fakehost (PUT /_fakes/host/scenario writes <dir>/scenario.json)")
	seedGraph := flag.Bool("seed-graph", true, "give GitLab projects 1 and 2 manifests with an exact dependency (project 2 depends on project 1) and CODEOWNERS")
	flag.Parse()
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
		fmt.Fprintln(os.Stderr, "orqen-fakes only listens on loopback")
		os.Exit(2)
	}
	gl, jira := testfakes.NewGitLab(), testfakes.NewJira()
	confServer, confCloud := testfakes.NewConfluenceServer("/confluence"), testfakes.NewConfluenceCloud("/cloud")
	jw := &jiraWrites{}
	if *seedGraph {
		gl.SeedGraph()
	}
	jira.Cloud = *cloud
	if *seed {
		jira.SeedIssues()
	}
	dir := *reposDir
	if dir == "" {
		var err error
		if dir, err = os.MkdirTemp("", "orqen-fake-repos-"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	var repos *testfakes.GitRepos
	if testfakes.GitAvailable() {
		if r, err := testfakes.NewGitRepos(dir); err == nil {
			repos = r
		} else {
			fmt.Fprintln(os.Stderr, "git repositories unavailable:", err)
		}
	}
	// Writes: a separate write token and push token (the same one), pipelines started for every pushed commit.
	gl.WriteToken, gl.AutoPipeline = writeToken, true
	if repos != nil {
		repos.PushToken = writeToken
		gl.LinkRepos(repos)
	}
	mux := http.NewServeMux()
	mux.Handle("/_fakes/", adminHandler(jira, gl, repos, *hostDir, confServer, confCloud, jw))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if repos != nil && strings.Contains(r.URL.Path, ".git/") && strings.HasPrefix(r.URL.Path, "/gitlab/") {
			repos.Handler().ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/confluence") {
			confServer.Handler().ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/cloud") {
			confCloud.Handler().ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/jira") {
			jw.record(r)
			jira.Handler().ServeHTTP(w, r)
			return
		}
		gl.Handler().ServeHTTP(w, r)
	}))
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	base := "http://" + ln.Addr().String()
	gl.PublicBase = base
	fmt.Printf("GitLab: %s/gitlab   token: %s\n", base, gl.Token)
	fmt.Printf("Jira:   %s/jira   user: %s   token: %s   (cloud=%v)\n", base, jira.User, jira.Token, *cloud)
	if repos != nil {
		fmt.Printf("Git:    %s/gitlab/group-1/proj-1.git   (GitLab project 1; user oauth2, token %s)\n", base, repos.Token)
	}
	fmt.Printf("Confluence (Server/DC, context path): %s/confluence   token: %s (Bearer)\n", base, confServer.Token)
	fmt.Printf("Confluence (Cloud, the adapter adds /wiki): %s/cloud   user: %s   token: %s (Basic)   pages 1001 architecture, 1002 poisoned, 1003 links a repo\n", base, confCloud.User, confCloud.Token)
	fmt.Printf("Write:  token %s   (the only token that may write; it is also the push token)\n", gl.WriteToken)
	fmt.Printf("Admin:  %s/_fakes/jira/{issues/KEY,fail,token,delay,writes}, POST %s/_fakes/confluence/page   (loopback only)\n", base, base)
	fmt.Printf("        %s/_fakes/gitlab/{pipeline,push-as-other,merge,writes,hang-after-write}, PUT %s/_fakes/gitlab/file, PUT %s/_fakes/host/scenario\n", base, base, base)
	fmt.Println("ready")
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sh)
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// adminHandler serves the loopback-only controls the manual walk uses to change the fake Jira.
func adminHandler(j *testfakes.Jira, gl *testfakes.GitLab, repos *testfakes.GitRepos, hostDir string, confServer, confCloud *testfakes.Confluence, jw *jiraWrites) http.Handler {
	mux := http.NewServeMux()
	decode := func(w http.ResponseWriter, r *http.Request, v any) bool {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return false
		}
		return true
	}
	mux.HandleFunc("PUT /_fakes/jira/issues/{key}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Acceptance  *string   `json:"acceptance"`
			Description *string   `json:"description"`
			Labels      *[]string `json:"labels"`
			Status      *string   `json:"status"`
		}
		if !decode(w, r, &req) {
			return
		}
		key := r.PathValue("key")
		j.SetIssue(key, func(i *testfakes.FakeIssue) {
			if req.Description != nil {
				i.Description = *req.Description
			}
			if req.Acceptance != nil {
				head, _, _ := strings.Cut(i.Description, "h2. Acceptance Criteria")
				i.Description = strings.TrimRight(head, "\n") + "\n\nh2. Acceptance Criteria\n" + *req.Acceptance
			}
			if req.Labels != nil {
				i.Labels = *req.Labels
			}
			if req.Status != nil {
				i.StatusName = *req.Status
				switch strings.ToLower(*req.Status) {
				case "done", "closed", "resolved":
					i.StatusCategory = "done"
				case "in progress", "in review":
					i.StatusCategory = "indeterminate"
				default:
					i.StatusCategory = "new"
				}
			}
		})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /_fakes/jira/fail", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			PathPrefix string `json:"path_prefix"`
			Status     int    `json:"status"`
			RetryAfter string `json:"retry_after"`
			Count      int    `json:"count"`
		}
		if !decode(w, r, &req) {
			return
		}
		j.SetFail(req.PathPrefix, &testfakes.Failure{Status: req.Status, RetryAfter: req.RetryAfter, Count: req.Count})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /_fakes/jira/token", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Token string `json:"token"`
		}
		if !decode(w, r, &req) || req.Token == "" {
			return
		}
		j.SetToken(req.Token)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /_fakes/jira/delay", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MS int `json:"ms"`
		}
		if !decode(w, r, &req) {
			return
		}
		j.SetDelay(time.Duration(req.MS) * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /_fakes/jira/writes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"writes": jw.list()})
	})
	mux.HandleFunc("POST /_fakes/confluence/page", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Flavor string `json:"flavor"` // "server", "cloud" or empty for both
			ID     string `json:"id"`
			Title  string `json:"title"`
			Body   string `json:"body"`
		}
		if !decode(w, r, &req) || req.ID == "" {
			http.Error(w, "need an id and a body", http.StatusBadRequest)
			return
		}
		out := map[string]int{}
		if req.Flavor != "cloud" {
			out["server"] = confServer.SetPage(req.ID, req.Title, req.Body)
		}
		if req.Flavor != "server" {
			out["cloud"] = confCloud.SetPage(req.ID, req.Title, req.Body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	gitlabAdmin(mux, gl, repos, hostDir, decode)
	return mux
}

// jiraWrites records every non-GET request that reaches the fake Jira. The fake serves reads only, so the list must
// stay empty: it is the evidence that Orqen made no Jira write.
type jiraWrites struct {
	mu  sync.Mutex
	log []string
}

func (j *jiraWrites) record(r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return
	}
	j.mu.Lock()
	j.log = append(j.log, r.Method+" "+r.URL.Path)
	j.mu.Unlock()
}
func (j *jiraWrites) list() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string{}, j.log...)
}

// writeToken is the fixed fake write token printed at start. It is a test value for the fake GitLab only.
const writeToken = "glpat-fake-write-token"

func gitlabAdmin(mux *http.ServeMux, gl *testfakes.GitLab, repos *testfakes.GitRepos, hostDir string, decode func(http.ResponseWriter, *http.Request, any) bool) {
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /_fakes/gitlab/pipeline", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Project int                 `json:"project"`
			SHA     string              `json:"sha"`
			Status  string              `json:"status"`
			Jobs    []testfakes.FakeJob `json:"jobs"`
		}
		if !decode(w, r, &req) {
			return
		}
		if req.Project == 0 {
			req.Project = 1
		}
		sha := req.SHA
		if sha == "" { // the head of the newest merge request of the project
			if mrs := gl.MRs(req.Project); len(mrs) > 0 {
				sha = mrs[len(mrs)-1].SHA
			}
		}
		if sha == "" || req.Status == "" {
			http.Error(w, "need a sha (or a merge request in the project) and a status", http.StatusBadRequest)
			return
		}
		reply(w, gl.SetPipeline(sha, req.Status, req.Jobs))
	})
	mux.HandleFunc("POST /_fakes/gitlab/push-as-other", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Project string `json:"project"`
			Branch  string `json:"branch"`
		}
		if !decode(w, r, &req) {
			return
		}
		if repos == nil {
			http.Error(w, "no git repositories", http.StatusServiceUnavailable)
			return
		}
		if req.Project == "" {
			req.Project = "group-1/proj-1"
		}
		sha, err := repos.PushAsOther(req.Project, req.Branch)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reply(w, map[string]string{"sha": sha})
	})
	mux.HandleFunc("POST /_fakes/gitlab/merge", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Project, IID int }
		if !decode(w, r, &req) {
			return
		}
		if req.Project == 0 {
			req.Project = 1
		}
		if !gl.Merge(req.Project, req.IID) {
			http.Error(w, "no such open merge request", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /_fakes/gitlab/file", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Project       int
			Path, Content string
		}
		if !decode(w, r, &req) {
			return
		}
		if req.Project == 0 || req.Path == "" {
			http.Error(w, "project and path are required", http.StatusBadRequest)
			return
		}
		gl.SetProjectFile(req.Project, req.Path, req.Content)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /_fakes/gitlab/writes", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"writes": nonNil(gl.Writes()), "pushes": []testfakes.Push{}, "receive_pack_requests": 0}
		if repos != nil {
			out["pushes"], out["receive_pack_requests"] = nonNilPushes(repos.Pushes()), repos.ReceivePackRequests()
		}
		reply(w, out)
	})
	mux.HandleFunc("POST /_fakes/gitlab/hang-after-write", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			PathPrefix string `json:"path_prefix"`
			Count      int    `json:"count"`
		}
		if !decode(w, r, &req) {
			return
		}
		gl.HangAfterWrite(req.PathPrefix, req.Count)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /_fakes/host/scenario", func(w http.ResponseWriter, r *http.Request) {
		if hostDir == "" {
			http.Error(w, "start orqen-fakes with --fakehost-dir", http.StatusServiceUnavailable)
			return
		}
		var sc json.RawMessage
		if !decode(w, r, &sc) {
			return
		}
		if err := os.MkdirAll(hostDir, 0o700); err == nil {
			err = os.WriteFile(filepath.Join(hostDir, "scenario.json"), sc, 0o600)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func nonNil(w []testfakes.RecordedWrite) []testfakes.RecordedWrite {
	if w == nil {
		return []testfakes.RecordedWrite{}
	}
	return w
}
func nonNilPushes(p []testfakes.Push) []testfakes.Push {
	if p == nil {
		return []testfakes.Push{}
	}
	return p
}
