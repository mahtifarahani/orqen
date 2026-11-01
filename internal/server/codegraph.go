package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"orqen/internal/codegraph"
	"orqen/internal/contextpkt"
	"orqen/internal/extensions"
	"orqen/internal/workspace"
)

// CodeIndexDoc is the status of one code index (collection code_indexes). The index itself stays on disk and is never
// copied to MongoDB.
type CodeIndexDoc struct {
	ID          string     `bson:"_id"`
	OrgID       string     `bson:"org_id"`
	ProjectID   string     `bson:"project_id"`
	RepoKey     string     `bson:"repo_key"`
	CommitSHA   string     `bson:"commit_sha"`
	Trigger     string     `bson:"trigger"` // manual | auto
	State       string     `bson:"state"`   // queued | indexing | ready | failed | license_revoked | removed
	InstallID   string     `bson:"install_id"`
	ToolVersion string     `bson:"tool_version"`
	IndexDir    string     `bson:"index_dir"`
	Bytes       int64      `bson:"bytes"`
	Files       int        `bson:"files"`
	ErrorCode   string     `bson:"error_code"`
	StartedAt   *time.Time `bson:"started_at"`
	FinishedAt  *time.Time `bson:"finished_at"`
	CreatedAt   time.Time  `bson:"created_at"`
	Revision    int64      `bson:"revision"`
}

func (d CodeIndexDoc) DTO() bson.M {
	return bson.M{"id": d.ID, "project_id": d.ProjectID, "repo_key": d.RepoKey, "commit_sha": d.CommitSHA, "trigger": triggerOf(d.Trigger), "state": d.State, "tool_version": optStr(d.ToolVersion), "bytes": d.Bytes, "files": d.Files,
		"error_code": optStr(d.ErrorCode), "started_at": optionalTime(d.StartedAt), "finished_at": optionalTime(d.FinishedAt), "revision": d.Revision}
}

func (a *App) codeGraphRoutes() []Route {
	const p = "/api/v1/orgs/{orgId}"
	return []Route{
		{"GET", p + "/graph/status", a.getGraphStatus}, {"GET", p + "/code-indexes", a.listCodeIndexes}, {"GET", p + "/code-indexes/retention/preview", a.previewCodeIndexRetention},
		{"GET", p + "/code-indexes/{cixId}", a.getCodeIndex}, {"DELETE", p + "/code-indexes/{cixId}", a.deleteCodeIndex}, {"GET", p + "/code-indexes/{cixId}/impact", a.queryCodeImpact},
		{"POST", p + "/projects/{prjId}/code-index", a.startCodeIndex},
	}
}

func init() { idemRequired["POST /api/v1/orgs/{orgId}/projects/{prjId}/code-index"] = true }

var shaPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func (a *App) codeIndex(ctx context.Context, org, id string) (CodeIndexDoc, error) {
	return findOne[CodeIndexDoc](ctx, a.DB, "code_indexes", bson.M{"_id": id, "org_id": org})
}

// codeGraphIndex returns the index port for a permit: the injected fake (tests) or the CLI adapter.
func (a *App) codeGraphIndex(ctx context.Context, org string, p extensions.Permit) codegraph.Index {
	if a.CodeGraph != nil {
		return a.CodeGraph
	}
	c := codegraph.NewCLI(p, codegraph.OrgDir(a.Config.DataDir, org))
	c.Mode = a.extSandboxMode(ctx, org)
	return c
}

func notUsable(reason string) *AppError {
	e := problem("extension_not_usable")
	e.Errors = []FieldError{{"extension", reason}}
	return e
}

func (a *App) gitnexusUsable(ctx context.Context, org, project string) (extensions.Permit, string, error) {
	m, _ := extensions.Get("gitnexus")
	return a.extUsable(ctx, org, m, project)
}

func (a *App) getGraphStatus(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	_, why, err := a.gitnexusUsable(r.Context(), org, "")
	if err != nil {
		return err
	}
	ready, err := findMany[CodeIndexDoc](r.Context(), a.DB, "code_indexes", bson.M{"org_id": org, "state": "ready"}, bson.D{{Key: "finished_at", Value: -1}}, 500)
	if err != nil {
		return err
	}
	var newest any
	if len(ready) > 0 {
		newest = bson.M{"repo_key": ready[0].RepoKey, "commit_sha": ready[0].CommitSHA, "finished_at": optionalTime(ready[0].FinishedAt)}
	}
	writeJSON(w, 200, bson.M{"code_graph": bson.M{"available": why == "", "reason": optStr(why), "ready_indexes": len(ready), "newest": newest}})
	return nil
}

func (a *App) listCodeIndexes(w http.ResponseWriter, r *http.Request) error {
	f := bson.M{"org_id": r.PathValue("orgId")}
	if v := r.URL.Query().Get("project_id"); v != "" {
		f["project_id"] = v
	}
	if v := r.URL.Query().Get("state"); v != "" {
		f["state"] = v
	}
	list, err := findMany[CodeIndexDoc](r.Context(), a.DB, "code_indexes", f, bson.D{{Key: "created_at", Value: -1}}, 200)
	if err != nil {
		return err
	}
	items := []bson.M{}
	for _, d := range list {
		items = append(items, d.DTO())
	}
	writeJSON(w, 200, bson.M{"items": items})
	return nil
}

func (a *App) getCodeIndex(w http.ResponseWriter, r *http.Request) error {
	d, err := a.codeIndex(r.Context(), r.PathValue("orgId"), r.PathValue("cixId"))
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, 200, d.DTO())
	return nil
}

// startCodeIndex queues an index of the primary repository at a commit (default: the default-branch head).
func (a *App) startCodeIndex(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	var req struct {
		CommitSHA string `json:"commit_sha"`
	}
	if r.ContentLength > 0 {
		if err = decodeJSON(w, r, &req, true); err != nil {
			return err
		}
	}
	if req.CommitSHA != "" && !shaPattern.MatchString(req.CommitSHA) {
		return validation("commit_sha", "must be a hex commit id")
	}
	org, prj := r.PathValue("orgId"), r.PathValue("prjId")
	route := "POST /api/v1/orgs/{orgId}/projects/{prjId}/code-index"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "prjId": prj}, req), func(ctx context.Context) (Output, error) {
		proj, err := a.project(ctx, org, prj)
		if err != nil {
			return Output{}, err
		}
		primary := proj.Mapping.primary()
		if primary == nil {
			return Output{}, validation("project", "has no primary repository")
		}
		if _, why, e := a.gitnexusUsable(ctx, org, prj); e != nil {
			return Output{}, e
		} else if why == extensions.ReasonProjectNotBound {
			return Output{}, problem("project_not_bound")
		} else if why != "" {
			return Output{}, notUsable(why)
		}
		sha := req.CommitSHA
		if sha == "" {
			conn, e := a.connection(ctx, org, primary.ConnectionID)
			if e != nil {
				return Output{}, e
			}
			host, e := a.codeHost(ctx, conn)
			if e != nil {
				return Output{}, e
			}
			if sha, e = host.ResolveCommit(ctx, primary.ProviderProjectID, primary.TargetBranch); e != nil {
				return Output{}, providerProblem(e)
			}
		}
		rk := repoKey(org, primary.ConnectionID, primary.ProviderProjectID)
		cix, err := a.upsertCodeIndex(ctx, org, prj, rk, sha)
		if err != nil {
			return Output{}, err
		}
		return a.startOperation(ctx, org, "code_index", Subject{"code_index", cix.ID}, "code_index:"+cix.ID, 2, bson.M{"cix_id": cix.ID})
	})
}

func (a *App) upsertCodeIndex(ctx context.Context, org, prj, rk, sha string) (CodeIndexDoc, error) {
	return a.upsertCodeIndexT(ctx, org, prj, rk, sha, "manual")
}

func (a *App) upsertCodeIndexT(ctx context.Context, org, prj, rk, sha, trigger string) (CodeIndexDoc, error) {
	cur, err := findOne[CodeIndexDoc](ctx, a.DB, "code_indexes", bson.M{"org_id": org, "repo_key": rk, "commit_sha": sha})
	now := time.Now().UTC()
	switch {
	case err == nil:
		if cur.State == "failed" || cur.State == "removed" || cur.State == "license_revoked" {
			_, err = a.DB.Coll("code_indexes").UpdateOne(ctx, bson.M{"_id": cur.ID}, bson.M{"$set": bson.M{"state": "queued", "trigger": trigger, "error_code": "", "finished_at": nil}, "$inc": bson.M{"revision": int64(1)}})
			cur.State = "queued"
		}
		return cur, err
	case !errors.Is(err, mongo.ErrNoDocuments):
		return CodeIndexDoc{}, err
	}
	d := CodeIndexDoc{ID: newID("cix"), OrgID: org, ProjectID: prj, RepoKey: rk, CommitSHA: sha, Trigger: trigger, State: "queued", CreatedAt: now, Revision: 1}
	_, err = a.DB.Coll("code_indexes").InsertOne(ctx, d)
	return d, err
}

// within reports whether p is inside root (after cleaning); the only paths Orqen ever deletes are such paths.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func (a *App) removeIndexDir(org string, d CodeIndexDoc) {
	root := codegraph.OrgDir(a.Config.DataDir, org)
	if d.IndexDir != "" && within(root, d.IndexDir) {
		_ = os.RemoveAll(d.IndexDir)
	}
}

func (a *App) deleteCodeIndex(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	org := r.PathValue("orgId")
	var out CodeIndexDoc
	err = a.DB.Txn(r.Context(), func(sc context.Context) error {
		e := a.DB.Coll("code_indexes").FindOneAndUpdate(sc, bson.M{"_id": r.PathValue("cixId"), "org_id": org, "revision": expected, "state": bson.M{"$in": []string{"ready", "failed", "license_revoked"}}},
			bson.M{"$set": bson.M{"state": "removed"}, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&out)
		if errors.Is(e, mongo.ErrNoDocuments) {
			return problem("revision_conflict")
		}
		if e != nil {
			return e
		}
		return a.AppendEvent(sc, org, "code_index.removed", Subject{"code_index", out.ID}, bson.M{"commit_sha": out.CommitSHA}, &out.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.removeIndexDir(org, out)
	a.Committed()
	w.Header().Set("ETag", etag(out.Revision))
	writeJSON(w, 200, out.DTO())
	return nil
}

func (a *App) previewCodeIndexRetention(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	removable, total, err := a.retentionCandidates(r.Context(), org)
	if err != nil {
		return err
	}
	items := []bson.M{}
	for _, d := range removable {
		items = append(items, bson.M{"id": d.ID, "bytes": d.Bytes})
	}
	writeJSON(w, 200, bson.M{"removable": items, "total_bytes": total})
	return nil
}

// retentionCandidates lists ready indexes beyond the newest N per repository.
func (a *App) retentionCandidates(ctx context.Context, org string) ([]CodeIndexDoc, int64, error) {
	eff, _, _, err := a.effectivePolicy(ctx, org, "")
	if err != nil {
		return nil, 0, err
	}
	keep := 3
	for _, e := range eff.Entries {
		if e.Key == "codegraph.keep_indexes_per_repo" {
			keep = policyValues{e.Key: e.Value}.Int(e.Key)
		}
	}
	ready, err := findMany[CodeIndexDoc](ctx, a.DB, "code_indexes", bson.M{"org_id": org, "state": "ready"}, bson.D{{Key: "finished_at", Value: -1}}, 2000)
	if err != nil {
		return nil, 0, err
	}
	seen := map[string]int{}
	var out []CodeIndexDoc
	var total int64
	for _, d := range ready {
		seen[d.RepoKey]++
		if seen[d.RepoKey] > keep {
			out = append(out, d)
			total += d.Bytes
		}
	}
	return out, total, nil
}

func (a *App) queryCodeImpact(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	d, err := a.codeIndex(r.Context(), org, r.PathValue("cixId"))
	if err != nil {
		return err
	}
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind == "" {
		kind = codegraph.KindFile
	}
	target := q.Get("target")
	if !codegraph.ValidTarget(kind, target) {
		return validation("target", "is not a valid file or symbol")
	}
	depth, limit := 1, 20
	if v, e := strconv.Atoi(q.Get("depth")); e == nil && v >= 1 && v <= 2 {
		depth = v
	}
	if v, e := strconv.Atoi(q.Get("limit")); e == nil && v >= 1 && v <= 50 {
		limit = v
	}
	if d.State != "ready" {
		return problem("index_not_ready")
	}
	p, why, err := a.gitnexusUsable(r.Context(), org, d.ProjectID)
	if err != nil {
		return err
	}
	if why != "" {
		return notUsable(why)
	}
	res, err := a.codeGraphIndex(r.Context(), org, p).Impact(r.Context(), d.IndexDir, target, kind, depth, limit)
	if err != nil {
		return problem("index_not_ready")
	}
	items := []bson.M{}
	for _, it := range res.Items {
		items = append(items, bson.M{"path": it.Path, "symbol": optStr(it.Symbol), "relation": it.Relation, "depth": it.Depth})
	}
	writeJSON(w, 200, bson.M{"commit_sha": d.CommitSHA, "items": items, "truncated": res.Truncated})
	return nil
}

// ---- the index job

// runCodeIndex mirrors the repository, then builds the index from a disposable tree.
func (a *App) runCodeIndex(ctx context.Context, job JobDoc, lease Lease) error {
	org := job.OrgID
	id, _ := job.Payload["cix_id"].(string)
	d, err := a.codeIndex(ctx, org, id)
	if err != nil {
		return err
	}
	if d.State == "ready" {
		return a.finishIndexJob(ctx, job, lease, d, true, "")
	}
	fail := func(code string) error {
		_ = a.setIndexState(ctx, lease, id, bson.M{"state": "failed", "error_code": code, "finished_at": time.Now().UTC()})
		return a.finishIndexJob(ctx, job, lease, d, false, code)
	}
	p, why, err := a.gitnexusUsable(ctx, org, d.ProjectID)
	if err != nil {
		return err
	}
	if why != "" {
		state := "failed"
		if why == extensions.ReasonLicenseRevoked {
			state = "license_revoked"
		}
		_ = a.setIndexState(ctx, lease, id, bson.M{"state": state, "error_code": why, "finished_at": time.Now().UTC()})
		return a.finishIndexJob(ctx, job, lease, d, false, why)
	}
	mirror, git, err := a.indexMirror(ctx, org, d)
	if err != nil {
		return fail("mirror_failed")
	}
	if err = a.buildCodeIndex(ctx, org, d, mirror, git, a.codeGraphIndex(ctx, org, p), p, func(f func(context.Context) error) error { return a.fenced(ctx, lease, f) }); err != nil {
		code := "index_failed"
		var ae *AppError
		if errors.As(err, &ae) {
			code = ae.Code
		}
		if errors.Is(err, errLeaseLost) {
			return err
		}
		return fail(code)
	}
	cur, _ := a.codeIndex(ctx, org, id)
	return a.finishIndexJob(ctx, job, lease, cur, true, "")
}

func (a *App) setIndexState(ctx context.Context, lease Lease, id string, set bson.M) error {
	return a.fenced(ctx, lease, func(sc context.Context) error {
		_, err := a.DB.Coll("code_indexes").UpdateOne(sc, bson.M{"_id": id}, bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}})
		return err
	})
}

func (a *App) finishIndexJob(ctx context.Context, job JobDoc, lease Lease, d CodeIndexDoc, ok bool, code string) error {
	return a.fenced(ctx, lease, func(sc context.Context) error {
		kind := "code_index.ready"
		if !ok {
			kind = "code_index.failed"
		}
		if e := a.AppendEvent(sc, job.OrgID, kind, Subject{"code_index", d.ID}, bson.M{"commit_sha": d.CommitSHA, "code": optStr(code)}, nil, job.OperationID, true); e != nil {
			return e
		}
		if ok {
			if e := a.transitionOperation(sc, job, "succeeded", bson.M{"cix_id": d.ID}); e != nil {
				return e
			}
		} else if e := a.transitionOperation(sc, job, "failed", bson.M{"code": code}); e != nil {
			return e
		}
		return a.completeJob(sc, lease)
	})
}

// indexMirror ensures the managed mirror of the project's primary repository is current.
func (a *App) indexMirror(ctx context.Context, org string, d CodeIndexDoc) (string, *workspace.Git, error) {
	proj, err := a.project(ctx, org, d.ProjectID)
	if err != nil {
		return "", nil, err
	}
	primary := proj.Mapping.primary()
	if primary == nil {
		return "", nil, errors.New("no primary repository")
	}
	conn, err := a.connection(ctx, org, primary.ConnectionID)
	if err != nil {
		return "", nil, err
	}
	eff, _, _, err := a.effectivePolicy(ctx, org, d.ProjectID)
	if err != nil {
		return "", nil, err
	}
	minFree := 512
	for _, e := range eff.Entries {
		if e.Key == "workspace.min_free_mb" {
			minFree = policyValues{e.Key: e.Value}.Int(e.Key)
		}
	}
	m, err := a.workspaceManager(org, &conn, minFree)
	if err != nil {
		return "", nil, err
	}
	if err = m.CheckDisk(); err != nil {
		return "", nil, err
	}
	host, err := a.codeHost(ctx, conn)
	if err != nil {
		return "", nil, err
	}
	gp, err := host.GetProject(ctx, primary.ProviderProjectID)
	if err != nil {
		return "", nil, err
	}
	cloneHost, ok := a.cloneHostAllowed(conn, gp.HTTPURLToRepo)
	if !ok {
		return "", nil, errors.New("clone host not allowed")
	}
	auth, err := a.providerAuth(ctx, conn)
	if err != nil {
		return "", nil, err
	}
	mirror, err := m.EnsureMirror(ctx, conn.ID, primary.ProviderProjectID, gp.HTTPURLToRepo)
	if err != nil {
		return "", nil, err
	}
	if err = m.Fetch(ctx, mirror, primary.TargetBranch, workspace.Creds{Host: cloneHost, Username: "oauth2", Password: auth.Secret}); err != nil {
		return "", nil, err
	}
	return mirror, m.Git, nil
}

// buildCodeIndex archives the commit from the mirror into a disposable tree, analyzes it and records the result. The
// mirror and every worktree stay untouched (git archive only reads). fence wraps every status write: when it fails
// (the job lost its lease) nothing is written after the analyze verb.
func (a *App) buildCodeIndex(ctx context.Context, org string, d CodeIndexDoc, mirror string, git *workspace.Git, ci codegraph.Index, p extensions.Permit, fence func(func(context.Context) error) error) error {
	if !shaPattern.MatchString(d.CommitSHA) {
		return validation("commit_sha", "invalid")
	}
	root := codegraph.OrgDir(a.Config.DataDir, org)
	tree := filepath.Join(root, "trees", d.ID)
	idx := filepath.Join(root, "indexes", d.ID)
	for _, dir := range []string{tree, idx} {
		if !within(root, dir) {
			return errors.New("path outside the codegraph directory")
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	defer os.RemoveAll(tree)
	if err := os.MkdirAll(tree, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(idx), 0o700); err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := fence(func(sc context.Context) error {
		_, e := a.DB.Coll("code_indexes").UpdateOne(sc, bson.M{"_id": d.ID}, bson.M{"$set": bson.M{"state": "indexing", "started_at": now, "tool_version": p.Version(), "index_dir": idx, "error_code": ""}, "$inc": bson.M{"revision": int64(1)}})
		return e
	}); err != nil {
		return err
	}
	res, err := git.Run(ctx, mirror, workspace.Creds{}, 5*time.Minute, "archive", "--format=tar", d.CommitSHA)
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	if err = codegraph.ExtractTar(strings.NewReader(res.Stdout), tree); err != nil {
		return err
	}
	ar, err := ci.Analyze(ctx, tree, idx)
	if err != nil {
		return err
	}
	var size int64
	_ = filepath.Walk(idx, func(_ string, fi os.FileInfo, e error) error {
		if e == nil && fi.Mode().IsRegular() {
			size += fi.Size()
		}
		return nil
	})
	fin := time.Now().UTC()
	if err = fence(func(sc context.Context) error {
		_, e := a.DB.Coll("code_indexes").UpdateOne(sc, bson.M{"_id": d.ID}, bson.M{"$set": bson.M{"state": "ready", "finished_at": fin, "bytes": size, "files": ar.Files}, "$inc": bson.M{"revision": int64(1)}})
		return e
	}); err != nil {
		return err
	}
	// keep the newest N per repository; the older index directories are removed
	if old, _, e := a.retentionCandidates(ctx, org); e == nil {
		for _, o := range old {
			_ = fence(func(sc context.Context) error {
				_, e := a.DB.Coll("code_indexes").UpdateOne(sc, bson.M{"_id": o.ID, "state": "ready"}, bson.M{"$set": bson.M{"state": "removed"}, "$inc": bson.M{"revision": int64(1)}})
				return e
			})
			a.removeIndexDir(org, o)
		}
	}
	return nil
}

// ---- packet item (X19)

// codeImpactItem is the code_impact item for architect, worker and reviewer packets. It is nil unless the project has
// an enabled gitnexus binding. A missing, stale or unavailable index is an excluded item with its reason; a stale
// index (another commit) is never attributed to the run's commit.
func (a *App) codeImpactItem(ctx context.Context, run RunDoc, role, head string, pv policyValues) *contextpkt.Item {
	if role != "architect" && role != "worker" && role != "reviewer" {
		return nil
	}
	m, _ := extensions.Get("gitnexus")
	b, err := findOne[ExtensionBindingDoc](ctx, a.DB, "extension_project_bindings", bson.M{"org_id": run.OrgID, "project_id": run.ProjectID, "extension_id": m.ID, "enabled": true})
	if err != nil {
		return nil
	}
	_ = b
	excl := func(reason string) *contextpkt.Item {
		return &contextpkt.Item{SourceID: codegraph.ItemSourceID, Kind: codegraph.ItemKind, Label: "code impact", Excluded: reason, OrgID: run.OrgID}
	}
	proj, err := a.project(ctx, run.OrgID, run.ProjectID)
	if err != nil || proj.Mapping.primary() == nil {
		return nil
	}
	pr := proj.Mapping.primary()
	permit, why, err := a.extUsable(ctx, run.OrgID, m, run.ProjectID)
	if err != nil {
		return excl("adapter_unavailable")
	}
	if why != "" {
		if why == extensions.ReasonLicenseRevoked {
			return excl("license_revoked")
		}
		return excl("adapter_unavailable")
	}
	rk := repoKey(run.OrgID, pr.ConnectionID, pr.ProviderProjectID)
	cix, err := findOne[CodeIndexDoc](ctx, a.DB, "code_indexes", bson.M{"org_id": run.OrgID, "repo_key": rk, "commit_sha": head, "state": "ready"})
	if err != nil {
		if n, _ := a.DB.Coll("code_indexes").CountDocuments(ctx, bson.M{"org_id": run.OrgID, "repo_key": rk, "state": "ready"}); n > 0 {
			return excl("index_stale")
		}
		return excl("index_missing")
	}
	targets := []string{}
	for _, l := range run.Lanes {
		for _, o := range l.Owns {
			if !strings.ContainsAny(o, "*?[") && codegraph.ValidTarget(codegraph.KindFile, o) && len(targets) < 10 {
				targets = append(targets, o)
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ci := a.codeGraphIndex(ctx, run.OrgID, permit)
	merged := codegraph.ImpactResult{Target: strings.Join(targets, ","), Items: []codegraph.Impacted{}}
	for _, t := range targets {
		r, e := ci.Impact(qctx, cix.IndexDir, t, codegraph.KindFile, 1, 20)
		if e != nil {
			return excl("adapter_unavailable")
		}
		merged.Items = append(merged.Items, r.Items...)
		merged.Truncated = merged.Truncated || r.Truncated
	}
	sort.SliceStable(merged.Items, func(i, j int) bool { return merged.Items[i].Path < merged.Items[j].Path })
	it := codegraph.ImpactItem(merged, cix.CommitSHA, pv.Int("codegraph.impact_max_bytes"), run.OrgID)
	return &it
}

// extractTarString extracts a tar held in memory with the same path validation as the code index.
func extractTarString(tar, dest string) error {
	return codegraph.ExtractTar(strings.NewReader(tar), dest)
}
