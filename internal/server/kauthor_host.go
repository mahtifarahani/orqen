package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"orqen/internal/canon"
	"orqen/internal/catherd"
	"orqen/internal/codexhost"
	"orqen/internal/execution"
	"orqen/internal/kauthor"
	"orqen/internal/knowledge"
	"orqen/internal/rolebriefs"
	"orqen/internal/workspace"
)

// The detached host of a roles author session (Y6). It follows the shape of the run's host session but shares none of
// its state: its own thread, its own catherd run, its own lease, and it never writes the origin run, its host session
// or its role executions.

// kauthorHostBackoff is the wait before a failed host start is tried again (tests shorten it).
var kauthorHostBackoff = 5 * time.Second

// kasRunner executes the steps of one session during one pass of the author job.
type kasRunner struct {
	a      *App
	lease  Lease
	kas    KnowledgeAuthorSessionDoc
	origin RunDoc
	pv     policyValues
	ver    ModelProfileVersionDoc

	env     *catherd.OrgEnv
	cfg     execution.HostConfig
	client  codexhost.Host
	thread  string
	timeout time.Duration
	unpin   func()
}

func (r *kasRunner) close() {
	if r.unpin != nil {
		r.unpin()
		r.unpin = nil
	}
}

func (r *kasRunner) hostUp() bool { return r.client != nil && r.thread != "" }

// set applies a CAS write on the session; ok=false means the session changed under us (cancel, retry) and the pass
// must reload it.
func (r *kasRunner) set(ctx context.Context, from []string, set bson.M) (bool, error) {
	var out KnowledgeAuthorSessionDoc
	err := r.a.fenced(ctx, r.lease, func(sc context.Context) error {
		var e error
		out, e = r.a.kasSet(sc, r.kas.OrgID, r.kas.ID, r.kas.Revision, from, set)
		return e
	})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	r.kas = out
	return true, nil
}

// openHost connects to the organization's app-server and gives the session a thread of its own.
func (r *kasRunner) openHost(ctx context.Context) error {
	a := r.a
	if a.HostSupervisor == nil || !a.hostAvailable(ctx, r.kas.OrgID) {
		return &JobFailure{Code: "host_unavailable", Message: "the orchestration host is not available", Retryable: true}
	}
	cfg, env, err := a.hostConfig(ctx, r.origin)
	if err != nil {
		return err
	}
	client, err := a.HostSupervisor.Client(ctx, r.kas.OrgID, cfg)
	if errors.Is(err, execution.ErrHostBusy) {
		return &JobFailure{Code: "host_busy", Message: "another runner holds the orchestration host", Retryable: true}
	}
	if err != nil {
		return &JobFailure{Code: "host_unavailable", Message: "the orchestration host could not be started", Retryable: true}
	}
	a.HostSupervisor.Pin(r.kas.OrgID)
	r.unpin = func() { a.HostSupervisor.Unpin(r.kas.OrgID) }
	r.cfg, r.env, r.client = cfg, env, client
	r.timeout = time.Duration(r.pv.Int("host.turn_timeout_secs")) * time.Second
	if a.hostTurnTimeout > 0 {
		r.timeout = a.hostTurnTimeout
	}
	if id := r.kas.Host.ThreadID; id != "" {
		if _, e := client.ResumeThread(ctx, id); e == nil {
			r.thread = id
			client.SetEffort(id, cfg.Effort)
			return nil
		}
	}
	th, err := client.StartThread(ctx, codexhost.ThreadParams{Model: cfg.Model, Effort: cfg.Effort, Cwd: cfg.Org.Tmp()})
	if err != nil {
		r.close()
		r.client = nil
		return &JobFailure{Code: "host_unavailable", Message: "a Codex thread could not be started", Retryable: true}
	}
	r.thread = th.ID
	client.SetEffort(th.ID, cfg.Effort)
	ok, err := r.set(ctx, nil, bson.M{"host.thread_id": th.ID})
	if err == nil && !ok {
		err = errors.New("the session changed while its thread was being recorded")
	}
	return err
}

// callWith sends one directive on the session's thread and returns the parsed catherd result. Errors:
// *codexhost.ToolError (catherd refused), *hostFault (the host deviated), errOutcomeUnknown, and *JobFailure.
func (r *kasRunner) callWith(ctx context.Context, d codexhost.Directive) (map[string]any, error) {
	a := r.a
	if _, err := r.set(ctx, nil, bson.M{"host.last_directive": KauthorDirective{ID: d.ID, Tool: d.Tool, ArgsHash: sha256Hex(canon.JSON(d.Args))[:16], State: "sent"}}); err != nil {
		return nil, err
	}
	call, err := r.client.RunDirective(ctx, r.thread, d, r.timeout)
	mark := func(state string) {
		_ = a.fenced(context.WithoutCancel(ctx), r.lease, func(sc context.Context) error {
			set := bson.M{"host.last_directive.state": state, "updated_at": time.Now().UTC()}
			if call.TurnID != "" {
				set["host.last_directive.turn_id"] = call.TurnID
			}
			_, e := a.DB.Coll("knowledge_author_sessions").UpdateOne(sc, bson.M{"_id": r.kas.ID, "org_id": r.kas.OrgID}, bson.M{"$set": set})
			return e
		})
	}
	var te *codexhost.ToolError
	switch {
	case err == nil:
		mark("completed")
		return decodeResult(call.Result, call.Text), nil
	case errors.As(err, &te):
		mark("completed")
		return nil, te
	case errors.Is(err, codexhost.ErrOutcomeUnknown), errors.Is(err, codexhost.ErrClosed), ctx.Err() != nil:
		mark("unknown")
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errOutcomeUnknown
	case errors.Is(err, codexhost.ErrDeviation), errors.Is(err, codexhost.ErrArgsMismatch), errors.Is(err, codexhost.ErrNoToolCall):
		mark("unknown")
		return nil, &hostFault{Tool: d.Tool, Err: err}
	}
	mark("unknown")
	return nil, &JobFailure{Code: "host_unavailable", Message: "the host directive failed", Retryable: true}
}

func (r *kasRunner) call(ctx context.Context, tool string, args map[string]any) (map[string]any, error) {
	return r.callWith(ctx, codexhost.NewDirective(tool, args))
}

// ensureCatherdRun creates the session's scratch repository and starts its catherd run (once).
func (r *kasRunner) ensureCatherdRun(ctx context.Context) error {
	a := r.a
	repo := filepath.Join(a.Config.DataDir, "kauthor", r.kas.OrgID, r.kas.ID, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		mgr, e := a.workspaceManager(r.kas.OrgID, nil, 0)
		if e != nil {
			return e
		}
		if _, e = mgr.Git.Run(ctx, repo, workspace.Creds{}, 0, "init", "-q"); e != nil {
			return e
		}
	}
	if err := a.Catherd.ProfileUseRepo(ctx, r.env, catherd.HostCodex, r.ver.CatherdName, repo); err != nil {
		return catherdJobError(err)
	}
	out, err := r.call(ctx, "run_start", map[string]any{"repo": repo, "title": "knowledge: " + r.origin.IssueKey, "a_lines": []string{"draft knowledge records from the evidence bundle"}})
	if err != nil {
		return err
	}
	id, dir := strOf(out, "run"), strOf(out, "dir")
	if id == "" || dir == "" {
		return &hostFault{Tool: "run_start", Err: errors.New("no run id or folder in the answer")}
	}
	real, err := execution.ValidateRunDir(filepath.Join(r.env.Home(), "data"), dir)
	if err != nil {
		return &runFailureError{Code: "catherd_run_dir_invalid", Message: "catherd reported a run folder outside its data directory"}
	}
	rel, _ := filepath.Rel(a.Config.DataDir, repo)
	ok, err := r.set(ctx, []string{kauthor.Queued, kauthor.Starting}, bson.M{"state": kauthor.Starting, "catherd.run_id": id, "catherd.run_dir": real, "catherd.repo_rel": rel})
	if err == nil && !ok {
		err = errors.New("the session changed while its catherd run was recorded")
	}
	return err
}

func (r *kasRunner) role(writer bool) KauthorRole {
	if writer {
		return r.kas.Writer
	}
	return r.kas.KVerifier
}

func roleKey(writer bool) string {
	if writer {
		return "writer"
	}
	return "kverifier"
}

func (r *kasRunner) bundleText(ctx context.Context) (string, error) {
	b, err := r.a.buildBundle(ctx, r.origin)
	if err != nil {
		return "", err
	}
	return knowledge.RenderBundle(b), nil
}

func (r *kasRunner) readArtifact(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	art, err := findOne[ArtifactDoc](ctx, r.a.DB, "artifacts", bson.M{"_id": id, "org_id": r.kas.OrgID})
	if err != nil {
		return "", err
	}
	data, err := readBlob(filepath.Join(r.a.Config.DataDir, "artifacts"), r.kas.OrgID, art.Sha256)
	return string(data), err
}

// dispatchRole sends the writer's or the verifier's brief. The intent is recorded before the directive and the dispatch
// id after it; a crash in between is settled by reconcile.
func (r *kasRunner) dispatchRole(ctx context.Context, writer bool) (bool, error) {
	a := r.a
	var (
		brief    rolebriefs.Brief
		catRole  = "verifier"
		name     string
		dispatch = kauthor.KVerifyDispatch
		from     = []string{kauthor.WriterDone, kauthor.KVerifyDispatch}
		err      error
	)
	data := rolebriefs.Data{Key: r.origin.IssueKey, Summary: "knowledge from a merged run"}
	if writer {
		catRole, dispatch = "writer", kauthor.WriterDispatching
		from = []string{kauthor.Queued, kauthor.Starting, kauthor.WriterDispatching}
		name = fmt.Sprintf("writer-K%d", r.kas.Attempt)
		if data.Context, err = r.bundleText(ctx); err != nil {
			return false, err
		}
		if r.kas.BundleArtifactID == "" {
			bundle, e := a.buildBundle(ctx, r.origin)
			if e != nil {
				return false, e
			}
			raw, _ := json.Marshal(bundle)
			art, e := (&stageCtx{a: a, lease: r.lease}).storeText(ctx, r.kas.OrgID, raw, "application/json", "knowledge_bundle", Subject{"knowledge_author_session", r.kas.ID})
			if e != nil {
				return false, e
			}
			if ok, e := r.set(ctx, nil, bson.M{"bundle_artifact_id": art.ID}); e != nil || !ok {
				return ok, e
			}
		}
		brief, err = rolebriefs.Render(rolebriefs.Writer, data)
	} else {
		name = fmt.Sprintf("kverify-K%d", r.kas.Attempt)
		text, e := r.readArtifact(ctx, r.kas.Writer.ReplyArtifactID)
		if e != nil {
			return false, e
		}
		bundle, e := a.buildBundle(ctx, r.origin)
		if e != nil {
			return false, e
		}
		recs, e := knowledge.ParseWriterReply(text, knowledge.BundleRefs(bundle))
		if e != nil {
			return false, e
		}
		data.Context = knowledge.RenderDraftForVerify(recs)
		brief, err = rolebriefs.Render(rolebriefs.KVerifier, data)
	}
	if err != nil {
		return false, err
	}
	route, err := r.call(ctx, "route", map[string]any{"run": r.kas.Catherd.RunID, "role": catRole})
	if err != nil {
		return false, err
	}
	rung := strOf(route, "rung")
	if rung == "" {
		return false, &hostFault{Tool: "route", Err: errors.New("no rung in the answer")}
	}
	d := codexhost.NewDirective("dispatch", map[string]any{"run": r.kas.Catherd.RunID, "role": catRole, "name": name, "brief": brief.Text, "rung": rung})
	now := time.Now().UTC()
	key := roleKey(writer)
	tries := r.role(writer).Tries + 1
	ok, err := r.set(ctx, from, bson.M{"state": dispatch, key + ".name": name, key + ".rung": rung, key + ".family": a.familyOf(ctx, r.kas.OrgID, rung), key + ".directive_id": d.ID,
		key + ".dispatching_at": now, key + ".dispatched_at": nil, key + ".dispatch_id": "", key + ".status": "", key + ".tries": tries})
	if err != nil || !ok {
		return ok, err
	}
	out, err := r.callWith(ctx, d)
	if err != nil {
		return false, err
	}
	if err = a.hook("kauthor_" + key + "_dispatched"); err != nil {
		return false, err
	}
	adm, _ := out["dispatched"].(map[string]any)
	id := strOf(adm, "dispatchId")
	if id == "" {
		return false, &hostFault{Tool: "dispatch", Err: errors.New("no dispatch id in the answer")}
	}
	running := kauthor.WriterRunning
	if !writer {
		running = kauthor.KVerifyRunning
	}
	return r.set(ctx, []string{dispatch}, bson.M{"state": running, key + ".dispatch_id": id, key + ".dispatched_at": time.Now().UTC(), key + ".rung": strOf(adm, "rung")})
}

// reconcile settles a dispatch whose outcome was never recorded: adopt it by name, or send it again when the thread
// history shows it never happened, or give up as uncertain.
func (r *kasRunner) reconcile(ctx context.Context, writer bool) (bool, error) {
	role := r.role(writer)
	key := roleKey(writer)
	view, err := r.a.Catherd.RunsShow(ctx, r.env, r.kas.Catherd.RunID)
	if err != nil {
		return false, catherdJobError(err)
	}
	claimed := map[string]bool{}
	for _, x := range []KauthorRole{r.kas.Writer, r.kas.KVerifier} {
		if x.DispatchID != "" {
			claimed[x.DispatchID] = true
		}
	}
	since := time.Now().UTC()
	if role.DispatchingAt != nil {
		since = *role.DispatchingAt
	}
	running := kauthor.WriterRunning
	dispatch := kauthor.WriterDispatching
	if !writer {
		running, dispatch = kauthor.KVerifyRunning, kauthor.KVerifyDispatch
	}
	if id, found := execution.ReconcileDispatch(view, role.Name, since, claimed); found {
		var rung string
		for _, x := range view.Dispatches {
			if x.DispatchID == id {
				rung = x.Rung
			}
		}
		return r.set(ctx, []string{dispatch}, bson.M{"state": running, key + ".dispatch_id": id, key + ".dispatched_at": time.Now().UTC(), key + ".rung": rung})
	}
	var turns []codexhost.Turn
	if th, e := r.client.ResumeThread(ctx, r.thread); e == nil {
		turns = th.Turns
		if turns == nil {
			turns = []codexhost.Turn{}
		}
	}
	if safe, known := execution.DispatchSafeToRetry(turns, role.DirectiveID); known && safe {
		return r.set(ctx, []string{dispatch}, bson.M{key + ".dispatching_at": nil, key + ".tries": role.Tries - 1})
	}
	return r.set(ctx, []string{dispatch}, bson.M{key + ".status": "uncertain"})
}

// collectRole reads the finished role's reply into an artifact and moves the session to the done state.
func (r *kasRunner) collectRole(ctx context.Context, writer bool, rec catherd.RunRecord) (bool, error) {
	if _, err := r.call(ctx, "result", map[string]any{"run": r.kas.Catherd.RunID, "name": r.role(writer).Name}); err != nil {
		return false, err
	}
	var artID string
	if rec.ReplyPath != "" && r.kas.Catherd.RunDir != "" {
		data, e := execution.ReadReply(r.kas.Catherd.RunDir, rec.ReplyPath, maxReplyBytes)
		if e == nil && len(data) > 0 {
			art, e2 := (&stageCtx{a: r.a, lease: r.lease}).storeText(ctx, r.kas.OrgID, data, "text/markdown", "role_reply", Subject{"knowledge_author_session", r.kas.ID})
			if e2 != nil {
				return false, e2
			}
			artID = art.ID
		}
	}
	key := roleKey(writer)
	from, done := []string{kauthor.WriterRunning}, kauthor.WriterDone
	if !writer {
		from, done = []string{kauthor.KVerifyRunning}, kauthor.KVerifyDone
	}
	return r.set(ctx, from, bson.M{"state": done, key + ".reply_artifact_id": artID, key + ".status": rec.Status})
}

// retryWriter sends the writer again after an unparseable reply (the one allowed retry).
func (r *kasRunner) retryWriter(ctx context.Context) (bool, error) {
	return r.set(ctx, []string{kauthor.WriterDone}, bson.M{"state": kauthor.Starting, "writer.dispatch_id": "", "writer.dispatching_at": nil, "writer.dispatched_at": nil, "writer.reply_artifact_id": "", "writer.status": ""})
}

// cancelRoles asks catherd to stop the session's live role (best effort).
func (r *kasRunner) cancelRoles(ctx context.Context) {
	if r.kas.Catherd.RunID == "" {
		return
	}
	env, err := r.a.catherdEnv(r.kas.OrgID)
	if err != nil {
		return
	}
	for _, x := range []KauthorRole{r.kas.Writer, r.kas.KVerifier} {
		if x.DispatchID != "" && x.Status == "" {
			if e := r.a.Catherd.RunsCancel(ctx, env, r.kas.Catherd.RunID, x.Name); e != nil {
				slog.Warn("knowledge author: could not cancel a role", "kas_id", r.kas.ID, "name", x.Name)
			}
		}
	}
}

func (r *kasRunner) parses(ctx context.Context, kverify bool) bool {
	if kverify {
		text, err := r.readArtifact(ctx, r.kas.KVerifier.ReplyArtifactID)
		if err != nil {
			return false
		}
		_, err = knowledge.ParseKVerifyReply(text)
		return err == nil
	}
	text, err := r.readArtifact(ctx, r.kas.Writer.ReplyArtifactID)
	if err != nil {
		return false
	}
	bundle, err := r.a.buildBundle(ctx, r.origin)
	if err != nil {
		return false
	}
	_, err = knowledge.ParseWriterReply(text, knowledge.BundleRefs(bundle))
	return err == nil
}

// facts gathers what kauthor.Next may look at.
func (r *kasRunner) facts(ctx context.Context, paused bool) (kauthor.Facts, error) {
	doc := plain(r.ver.Doc)
	f := kauthor.Facts{State: r.kas.State, HostUp: r.hostUp(), CatherdRunReady: r.kas.Catherd.RunID != "", OrgPaused: paused, HostFailures: r.kas.HostFailures, Tries: r.kas.Writer.Tries,
		WriterEnabled: catherd.RoleEnabled(doc, "writer"), VerifierEnabled: catherd.RoleEnabled(doc, "verifier")}
	writer := true
	switch r.kas.State {
	case kauthor.KVerifyDispatch, kauthor.KVerifyRunning, kauthor.KVerifyDone:
		writer = false
	}
	role := r.role(writer)
	switch {
	case role.Status == "uncertain":
		f.Dispatch = kauthor.DispatchUncertain
	case role.DispatchID != "":
		f.Dispatch = kauthor.DispatchKnown
	case role.DispatchingAt != nil:
		f.Dispatch = kauthor.DispatchIntent
	}
	switch r.kas.State {
	case kauthor.WriterDone:
		f.ParseOK = r.parses(ctx, false)
	case kauthor.KVerifyDone:
		f.ParseOK = r.parses(ctx, true)
	case kauthor.WriterRunning, kauthor.KVerifyRunning:
		f.WallExceeded = role.DispatchedAt != nil && time.Since(*role.DispatchedAt) >= time.Duration(r.pv.Int("roles.max_wall_secs"))*time.Second
		if env, err := r.a.catherdEnv(r.kas.OrgID); err == nil {
			if view, e := r.a.Catherd.RunsShow(ctx, env, r.kas.Catherd.RunID); e == nil {
				if rec, ok := view.RecordFor(role.DispatchID); ok {
					f.RecordPresent = true
					f.RoleFailed = rec.Status != "ok"
				}
			} else {
				slog.Warn("knowledge author: catherd could not be read", "kas_id", r.kas.ID)
			}
		}
	}
	return f, nil
}
