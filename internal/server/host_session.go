package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"orqen/internal/canon"
	"orqen/internal/catherd"
	"orqen/internal/codexhost"
	"orqen/internal/execution"
	"os"
	"path/filepath"
	"time"
)

// hostFault is a directed turn the host did not carry out as told (deviation, no tool call, wrong arguments).
type hostFault struct {
	Tool string
	Err  error
}

func (e *hostFault) Error() string {
	return "the host did not carry out " + e.Tool + ": " + e.Err.Error()
}
func (e *hostFault) Unwrap() error { return e.Err }

// hostOutcomeUnknown means a directive was sent and nobody knows whether catherd acted on it.
var errOutcomeUnknown = &JobFailure{Code: "outcome_unknown", Message: "the outcome of a host directive is unknown", Retryable: true}

// hostRun is one run's live connection to its Codex thread.
type hostRun struct {
	s       *stageCtx
	run     RunDoc
	env     *catherd.OrgEnv
	cfg     execution.HostConfig
	client  *codexhost.Client
	thread  string
	session HostSessionDoc
	timeout time.Duration
	unpin   func()
}

// close releases the supervisor pin.
func (h *hostRun) close() {
	if h.unpin != nil {
		h.unpin()
		h.unpin = nil
	}
}

func (a *App) profileVersionOf(ctx context.Context, run RunDoc) (ModelProfileVersionDoc, error) {
	if run.Pinned.ProfileVersionID == nil {
		return ModelProfileVersionDoc{}, &runFailureError{Code: "profile_not_pinned", Message: "the run has no pinned model profile"}
	}
	return findOne[ModelProfileVersionDoc](ctx, a.DB, "model_profile_versions", bson.M{"_id": *run.Pinned.ProfileVersionID, "org_id": run.OrgID})
}

// architectEnabled reports whether the run's pinned profile enables the architect role.
func (a *App) architectEnabled(ctx context.Context, run RunDoc) (bool, error) {
	v, err := a.profileVersionOf(ctx, run)
	if err != nil {
		return false, err
	}
	return catherd.RoleEnabled(plain(v.Doc), "architect"), nil
}

func (a *App) worktreePath(ctx context.Context, run RunDoc) (string, error) {
	ws, found, err := a.workspaceDoc(ctx, run.OrgID, run.ID)
	if err != nil {
		return "", err
	}
	if !found || ws.PathRel == "" {
		return "", &runFailureError{Code: "workspace_missing", Message: "the run has no prepared worktree"}
	}
	return filepath.Join(a.Config.DataDir, ws.PathRel), nil
}

func (a *App) hostConfig(ctx context.Context, run RunDoc) (execution.HostConfig, *catherd.OrgEnv, error) {
	env, err := a.catherdEnv(run.OrgID)
	if err != nil {
		return execution.HostConfig{}, nil, err
	}
	if a.CatherdRT == nil || a.CatherdRT.Bin == "" {
		return execution.HostConfig{}, nil, &JobFailure{Code: "catherd_unavailable", Message: "catherd is not available", Retryable: true}
	}
	v, err := a.profileVersionOf(ctx, run)
	if err != nil {
		return execution.HostConfig{}, nil, err
	}
	need := append([]string{"TYPESAFE_API_KEY"}, isolationEnvsFor(plain(v.Doc))...)
	sec, err := a.secretEnvFor(ctx, run.OrgID, need)
	if err != nil {
		return execution.HostConfig{}, nil, err
	}
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return execution.HostConfig{}, nil, err
	}
	return execution.HostConfig{CodexBin: a.Config.CodexBin, CatherdBin: a.CatherdRT.Bin, DataDir: a.Config.DataDir, OrgID: run.OrgID, Org: env, RT: a.CatherdRT, Secrets: sec,
		Model: pv.Str("host.orchestrator_model"), Effort: pv.Str("host.orchestrator_effort")}, env, nil
}

// liveSession returns the run's open host session document.
func (a *App) liveSession(ctx context.Context, org, run string) (HostSessionDoc, bool, error) {
	d, err := findOne[HostSessionDoc](ctx, a.DB, "host_sessions", bson.M{"org_id": org, "run_id": run, "live": true})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return HostSessionDoc{}, false, nil
	}
	return d, err == nil, err
}

// openHost connects to the organization's app-server and makes sure the run has a thread on it.
func (s *stageCtx) openHost(ctx context.Context, run RunDoc) (*hostRun, error) {
	a := s.a
	if a.HostSupervisor == nil || !a.hostAvailable(ctx, run.OrgID) {
		return nil, &JobFailure{Code: "host_unavailable", Message: "the Codex host is not available", Retryable: true}
	}
	cfg, env, err := a.hostConfig(ctx, run)
	if err != nil {
		return nil, err
	}
	client, err := a.HostSupervisor.Client(ctx, run.OrgID, cfg)
	if errors.Is(err, execution.ErrHostBusy) {
		return nil, &JobFailure{Code: "host_busy", Message: "another runner holds the Codex host", Retryable: true}
	}
	if err != nil {
		return nil, &JobFailure{Code: "host_unavailable", Message: "the Codex host could not be started", Retryable: true}
	}
	a.HostSupervisor.Pin(run.OrgID)
	h := &hostRun{s: s, run: run, env: env, cfg: cfg, client: client, unpin: func() { a.HostSupervisor.Unpin(run.OrgID) }}
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		h.close()
		return nil, err
	}
	h.timeout = time.Duration(pv.Int("host.turn_timeout_secs")) * time.Second
	if a.hostTurnTimeout > 0 {
		h.timeout = a.hostTurnTimeout
	}
	hs, found, err := a.liveSession(ctx, run.OrgID, run.ID)
	if err != nil {
		h.close()
		return nil, err
	}
	if !found {
		hs = HostSessionDoc{ID: newID("hss"), OrgID: run.OrgID, RunID: run.ID, State: HostStarting, Live: true, RunnerOwner: s.lease.Owner, StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		if err = a.fenced(ctx, s.lease, func(tx context.Context) error {
			_, e := a.DB.Coll("host_sessions").InsertOne(tx, hs)
			if e != nil {
				return e
			}
			_, e = a.DB.Coll("runs").UpdateOne(tx, bson.M{"_id": run.ID, "org_id": run.OrgID}, bson.M{"$set": bson.M{"host_session_id": hs.ID}})
			return e
		}); err != nil {
			h.close()
			return nil, err
		}
	}
	h.session = hs
	if err = h.ensureThread(ctx); err != nil {
		h.close()
		return nil, err
	}
	return h, nil
}

func (h *hostRun) ensureThread(ctx context.Context) error {
	a, s := h.s.a, h.s
	hs := h.session
	if hs.ThreadID != "" {
		key := fmt.Sprintf("%p/%s", h.client, hs.ThreadID)
		if _, resumed := a.hostResumed.Load(key); resumed {
			h.thread = hs.ThreadID
			return nil
		}
		if _, err := h.client.ResumeThread(ctx, hs.ThreadID); err == nil {
			a.hostResumed.Store(key, true)
			h.client.SetEffort(hs.ThreadID, h.cfg.Effort)
			h.thread = hs.ThreadID
			return nil
		} else {
			var rpc *codexhost.RPCError
			if !errors.As(err, &rpc) {
				return &JobFailure{Code: "host_unavailable", Message: "the Codex thread could not be resumed", Retryable: true}
			}
			// The thread is gone: start another one; the catherd run is unchanged.
			if e := a.fenced(ctx, s.lease, func(tx context.Context) error {
				return a.AppendEvent(tx, h.run.OrgID, "host.session_lost", Subject{"run", h.run.ID}, bson.M{"run_id": h.run.ID}, nil, nil, true)
			}); e != nil {
				return e
			}
		}
	}
	th, err := h.client.StartThread(ctx, codexhost.ThreadParams{Model: h.cfg.Model, Effort: h.cfg.Effort, Cwd: h.cfg.Org.Tmp()})
	if err != nil {
		return &JobFailure{Code: "host_unavailable", Message: "a Codex thread could not be started", Retryable: true}
	}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		now := time.Now().UTC()
		var updated HostSessionDoc
		if e := a.DB.Coll("host_sessions").FindOneAndUpdate(tx, bson.M{"_id": hs.ID, "org_id": h.run.OrgID}, bson.M{"$set": bson.M{"thread_id": th.ID, "codex_version": th.CLIVersion, "model": th.Model, "effort": th.Effort, "state": HostActive, "runner_owner": s.lease.Owner, "updated_at": now}}, afterUpdate()).Decode(&updated); e != nil {
			return e
		}
		h.session = updated
		return a.AppendEvent(tx, h.run.OrgID, "host.session_started", Subject{"run", h.run.ID}, bson.M{"run_id": h.run.ID, "thread_id": th.ID}, nil, nil, true)
	})
	if err != nil {
		return err
	}
	a.hostResumed.Store(fmt.Sprintf("%p/%s", h.client, th.ID), true)
	h.client.SetEffort(th.ID, h.cfg.Effort)
	h.thread = th.ID
	return nil
}

// call sends one directive and returns the parsed JSON object of the catherd result. Errors:
// *codexhost.ToolError (catherd refused), *hostFault (the host deviated) and errOutcomeUnknown.
func (h *hostRun) call(ctx context.Context, tool string, args map[string]any) (map[string]any, error) {
	d := codexhost.NewDirective(tool, args)
	return h.callWith(ctx, d)
}

func (h *hostRun) callWith(ctx context.Context, d codexhost.Directive) (map[string]any, error) {
	a, s := h.s.a, h.s
	if err := a.fenced(ctx, s.lease, func(tx context.Context) error {
		_, e := a.DB.Coll("host_sessions").UpdateOne(tx, bson.M{"_id": h.session.ID, "org_id": h.run.OrgID}, bson.M{"$set": bson.M{"last_directive": LastDirective{ID: d.ID, Tool: d.Tool, ArgsHash: sha256Hex(canon.JSON(d.Args))[:16], State: "sent"}, "updated_at": time.Now().UTC()}})
		return e
	}); err != nil {
		return nil, err
	}
	call, err := h.client.RunDirective(ctx, h.thread, d, h.timeout)
	mark := func(state string) {
		_ = a.fenced(context.WithoutCancel(ctx), s.lease, func(tx context.Context) error {
			set := bson.M{"last_directive.state": state, "updated_at": time.Now().UTC()}
			if call.TurnID != "" {
				set["last_directive.turn_id"] = call.TurnID
			}
			if u, ok := h.client.Usage(h.thread); ok {
				set["host_tokens"] = u.Total
			}
			_, e := a.DB.Coll("host_sessions").UpdateOne(tx, bson.M{"_id": h.session.ID, "org_id": h.run.OrgID}, bson.M{"$set": set})
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
		_ = a.fenced(context.WithoutCancel(ctx), s.lease, func(tx context.Context) error {
			return a.AppendEvent(tx, h.run.OrgID, "host.deviation", Subject{"run", h.run.ID}, bson.M{"run_id": h.run.ID, "directive_id": d.ID, "tool": d.Tool}, nil, nil, true)
		})
		return nil, &hostFault{Tool: d.Tool, Err: err}
	}
	mark("unknown")
	return nil, &JobFailure{Code: "host_unavailable", Message: "the host directive failed", Retryable: true}
}

func decodeResult(raw json.RawMessage, text string) map[string]any {
	out := map[string]any{}
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&out) != nil || out == nil {
			out = map[string]any{}
		}
	}
	if text != "" {
		out["_text"] = text
	}
	return out
}

func strOf(m map[string]any, k string) string { s, _ := m[k].(string); return s }

// validateRunDir resolves the folder catherd reported and requires it inside the organization's catherd data.
func (h *hostRun) validateRunDir(dir string) (string, error) {
	real, err := execution.ValidateRunDir(filepath.Join(h.env.Home(), "data"), dir)
	if err != nil {
		return "", &runFailureError{Code: "catherd_run_dir_invalid", Message: "catherd reported a run folder outside its data directory"}
	}
	return real, nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
