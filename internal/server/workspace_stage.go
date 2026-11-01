package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net"
	"net/url"
	"orqen/internal/runstate"
	"orqen/internal/workspace"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// branchFor is orqen/<issue-key>/<run suffix>, the plan's branch scheme.
func branchFor(issueKey, runID string) string {
	suffix := strings.ToLower(runID)
	if i := strings.LastIndex(suffix, "_"); i >= 0 {
		suffix = suffix[i+1:]
	}
	if len(suffix) > 10 {
		suffix = suffix[len(suffix)-10:]
	}
	return "orqen/" + strings.ToLower(issueKey) + "/" + suffix
}

func loopbackHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// workspaceManager builds the manager for an organization. The connection supplies the proxy and CA; min free
// space comes from the (pinned) policy.
func (a *App) workspaceManager(org string, conn *ConnectionDoc, minFreeMB int) (*workspace.Manager, error) {
	data := a.Config.DataDir
	root := filepath.Join(data, "workspaces", org)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if _, err := workspace.EnsureAskPass(data); err != nil {
		return nil, err
	}
	g := &workspace.Git{DataDir: data, Ceiling: filepath.Join(data, "workspaces"), AllowFile: a.allowFileRemotes, Hook: a.gitHook}
	if conn != nil {
		if conn.ProxyURL != nil {
			g.Proxy = *conn.ProxyURL
		}
		if conn.CustomCaPEM != nil {
			g.CAPEM = *conn.CustomCaPEM
		}
		if n, err := normalizeURL(conn.BaseURL); err == nil {
			g.AllowHTTP = strings.HasPrefix(conn.BaseURL, "http://") && loopbackHost(n.Host)
		}
	}
	return &workspace.Manager{Root: root, Git: g, MinFreeBytes: int64(minFreeMB) << 20, Statfs: a.statfs}, nil
}

func (a *App) workspaceDoc(ctx context.Context, org, run string) (WorkspaceDoc, bool, error) {
	d, err := findOne[WorkspaceDoc](ctx, a.DB, "workspaces", bson.M{"org_id": org, "run_id": run, "active": true})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return WorkspaceDoc{}, false, nil
	}
	return d, err == nil, err
}

// stageWorkspace prepares the run's managed worktree and moves the run to queued.
func (s *stageCtx) stageWorkspace(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	proj, err := a.project(ctx, run.OrgID, run.ProjectID)
	if err != nil {
		return false, err
	}
	primary := proj.Mapping.primary()
	if primary == nil {
		return false, &stageStop{Spec: stopSpec{Reason: "project_mapping", QuestionCode: "primary_repo_missing", Options: resolveOptions()}}
	}
	conn, err := a.connection(ctx, run.OrgID, primary.ConnectionID)
	if err != nil {
		return false, err
	}
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return false, err
	}
	m, err := a.workspaceManager(run.OrgID, &conn, pv.Int("workspace.min_free_mb"))
	if err != nil {
		return false, err
	}
	if _, err = m.Git.Version(ctx); err != nil {
		return false, err
	}
	if err = m.CheckDisk(); err != nil {
		return false, err
	}
	host, err := a.codeHost(ctx, conn)
	if err != nil {
		return false, err
	}
	gp, err := host.GetProject(ctx, primary.ProviderProjectID)
	if err != nil {
		a.noteRateLimitErr(conn.ID, err)
		return false, err
	}
	// The clone URL must point at the connection's own host (or one the connection allows).
	cloneHost, ok := a.cloneHostAllowed(conn, gp.HTTPURLToRepo)
	if !ok {
		return false, &stageStop{Spec: stopSpec{Reason: "project_mapping", QuestionCode: "clone_host_not_allowed", Params: bson.M{"host": cloneHost}, Options: resolveOptions()}}
	}
	auth, err := a.providerAuth(ctx, conn)
	if err != nil {
		return false, err
	}
	// 1. the workspace document (idempotent per run).
	ws, found, err := a.workspaceDoc(ctx, run.OrgID, run.ID)
	if err != nil {
		return false, err
	}
	if !found {
		ws = WorkspaceDoc{ID: newID("wsp"), OrgID: run.OrgID, RunID: run.ID, ProjectID: run.ProjectID, ConnectionID: conn.ID, ProviderProjectID: primary.ProviderProjectID, Branch: branchFor(run.IssueKey, run.ID), BaseRef: "refs/remotes/origin/" + primary.TargetBranch, State: "preparing", Active: true, Revision: 1, CreatedAt: time.Now().UTC()}
		if err = a.fenced(ctx, s.lease, func(tx context.Context) error {
			_, e := a.DB.Coll("workspaces").InsertOne(tx, ws)
			return e
		}); err != nil {
			return false, err
		}
	}
	// 2. mirror, fetch, worktree: external work, all idempotent.
	creds := workspace.Creds{Host: cloneHost, Username: "oauth2", Password: auth.Secret}
	mirror, err := m.EnsureMirror(ctx, conn.ID, primary.ProviderProjectID, gp.HTTPURLToRepo)
	if err != nil {
		return false, err
	}
	if err = m.Fetch(ctx, mirror, primary.TargetBranch, creds); err != nil {
		return false, err
	}
	wt, err := m.AddWorktree(ctx, mirror, run.ID, ws.Branch, primary.TargetBranch)
	if err != nil {
		return false, err
	}
	st, err := m.Inspect(ctx, wt.Path, wt.BaseSHA)
	if err != nil {
		return false, err
	}
	if err = a.hook(runstate.StageWorkspace); err != nil {
		return false, err
	}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		now := time.Now().UTC()
		var updated WorkspaceDoc
		if e := a.DB.Coll("workspaces").FindOneAndUpdate(tx, bson.M{"_id": ws.ID, "org_id": run.OrgID, "active": true}, bson.M{"$set": bson.M{"state": "ready", "mirror_rel": m.Rel(mirror), "path_rel": m.Rel(wt.Path), "base_ref": wt.BaseRef, "base_sha": wt.BaseSHA, "head_sha": st.HeadSHA, "dirty": !st.Clean}, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&updated); e != nil {
			return e
		}
		if _, e := s.complete(tx, run, runstate.StageWorkspace, runstate.StageQueued, bson.M{"workspace_id": ws.ID, "queued_at": now}, eventSpec{"run.workspace_ready", bson.M{"workspace_id": ws.ID}}); e != nil {
			return e
		}
		return a.AppendEvent(tx, run.OrgID, "workspace.ready", Subject{"run", run.ID}, bson.M{"run_id": run.ID, "workspace_id": ws.ID}, nil, nil, true)
	})
	return true, err
}

// cloneHostAllowed checks the clone URL's host against the connection's base URL host and allowed hosts.
func (a *App) cloneHostAllowed(conn ConnectionDoc, clone string) (string, bool) {
	u, err := url.Parse(clone)
	if err != nil || u.Host == "" {
		return "", false
	}
	n, err := normalizeURL(clone)
	if err != nil {
		return u.Host, false
	}
	if base, e := normalizeURL(conn.BaseURL); e == nil && base.Host == n.Host {
		return u.Host, true
	}
	return u.Host, contains(conn.AllowedHosts, n.Host)
}
