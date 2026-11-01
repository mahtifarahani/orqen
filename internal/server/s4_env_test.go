package server

import (
	"context"
	"io"
	"orqen/internal/catherd"
	"orqen/internal/execution"
	"orqen/internal/testfakes/hostfake"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// s4Profile is a model profile whose rungs are all codex rungs of different families for coder and reviewer.
func s4Profile(architect bool) func() map[string]any {
	return func() map[string]any {
		doc := goodProfileDoc()
		roles := doc["roles"].(map[string]any)
		roles["architect"].(map[string]any)["rungs"] = []any{"codex:gpt-6.1-sol#high"}
		roles["architect"].(map[string]any)["enabled"] = architect
		roles["worker"].(map[string]any)["rungs"] = []any{"codex:gpt-6-sol#medium"}
		roles["verifier"].(map[string]any)["rungs"] = []any{"codex:gpt-6-luna#medium"}
		roles["reviewer"].(map[string]any)["rungs"] = []any{"codex:gpt-6.1-sol#high"}
		doc["failover"] = map[string]any{}
		return doc
	}
}

type s4Opts struct {
	intakeOpts
	Scenario *hostfake.Scenario
	// NoHost leaves the host unprobed.
	NoHost      bool
	NoArchitect bool
	TurnTimeout time.Duration
	IdleStop    time.Duration
	CodingSlots int
}

// s4Env is an intake environment with the fake Codex host wired in.
type s4Env struct {
	*intakeEnv
	cat       *hostfake.Catherd
	app       *hostfake.AppServer
	sc        *hostfake.Scenario
	sup       *execution.Supervisor
	spawned   atomic.Int64
	envMu     sync.Mutex
	spawnEnvs [][]string // the environment and arguments of every host process Orqen started
}

type testProc struct {
	done chan struct{}
	once atomic.Bool
}

func (p *testProc) Kill() {
	if p.once.CompareAndSwap(false, true) {
		close(p.done)
	}
}
func (p *testProc) Done() <-chan struct{} { return p.done }

func newS4Env(t *testing.T, o s4Opts) *s4Env {
	t.Helper()
	if o.Scenario == nil {
		o.Scenario = hostfake.DefaultScenario()
	}
	if o.PlanApproval == "" {
		o.PlanApproval = "auto"
	}
	if o.CodingStart == "" {
		o.CodingStart = "auto"
	}
	o.Git = true
	o.intakeOpts.NoHost = o.NoHost
	o.Scheduler = true
	o.ProfileDoc = s4Profile(!o.NoArchitect)
	e := &s4Env{sc: o.Scenario}
	setup := o.Setup
	o.Setup = func(a *App) {
		if setup != nil {
			setup(a)
		}
		if o.CodingSlots > 0 {
			a.Config.CodingSlots = o.CodingSlots
		}
		a.hostTurnTimeout = o.TurnTimeout
	}
	e.intakeEnv = newIntakeEnv(t, o.intakeOpts)
	org := catherd.NewOrgEnv(e.f.a.Config.DataDir, e.org)
	if err := org.Ensure(); err != nil {
		t.Fatal(err)
	}
	e.cat = hostfake.NewCatherd(filepath.Join(org.Home(), "data"), o.Scenario)
	e.app = hostfake.NewAppServer(e.cat)
	e.fake.Runs = e.cat
	e.sup = &execution.Supervisor{IdleStop: o.IdleStop, Spawn: func(ctx context.Context, cfg execution.HostConfig, env, args []string) (io.ReadWriteCloser, execution.Proc, error) {
		e.spawned.Add(1)
		e.envMu.Lock()
		e.spawnEnvs = append(e.spawnEnvs, append(append([]string{}, env...), args...))
		e.envMu.Unlock()
		return e.app.Pipe(), &testProc{done: make(chan struct{})}, nil
	}}
	e.f.a.HostSupervisor = e.sup
	t.Cleanup(e.sup.StopAll)
	if !o.NoHost {
		e.markHostProbed(true)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		runs, _ := findMany[RunDoc](context.Background(), e.f.db, "runs", bson.M{"org_id": e.org}, bson.D{{Key: "_id", Value: 1}}, 10)
		for _, r := range runs {
			debugRun(t, e, r.IssueKey)
		}
		evs, _ := findMany[EventDoc](context.Background(), e.f.db, "events", bson.M{"org_id": e.org}, bson.D{{Key: "seq", Value: -1}}, 25)
		for i := len(evs) - 1; i >= 0; i-- {
			t.Logf("event %s %v", evs[i].Type, evs[i].Payload)
		}
	})
	return e
}

// markHostProbed stores a successful host probe of the fake host.
func (e *s4Env) markHostProbed(ok bool) {
	e.f.t.Helper()
	status := "ok"
	if !ok {
		status = "failed"
	}
	_, err := e.f.db.Coll("host_states").UpdateOne(context.Background(), bson.M{"_id": hostStateID(e.org)}, bson.M{"$set": bson.M{"org_id": e.org, "last_probe": HostProbe{Status: status, CodexVersion: "0.160.0", Login: "ok", Fake: true, CheckedAt: time.Now().UTC()}}, "$setOnInsert": bson.M{"revision": int64(1)}}, options.UpdateOne().SetUpsert(true))
	if err != nil {
		e.f.t.Fatal(err)
	}
}

// jqlFor limits intake to one issue (the fake Jira understands "key = X" only).
func jqlFor(keys ...string) *string {
	q := "key = " + keys[0]
	return &q
}

func (e *s4Env) roles(key string) []RoleExecDoc {
	run, _ := e.runOf(key)
	rexes, err := findMany[RoleExecDoc](context.Background(), e.f.db, "role_executions", bson.M{"org_id": e.org, "run_id": run.ID}, bson.D{{Key: "created_at", Value: 1}}, 100)
	if err != nil {
		e.f.t.Fatal(err)
	}
	return rexes
}

func (e *s4Env) waitState(key, state string) RunDoc {
	e.f.t.Helper()
	waitFor(e.f.t, key+" in state "+state, func() bool { return e.runState(key).State == state })
	return e.runState(key)
}

func t0() context.Context { return context.Background() }
