package server

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"orqen/internal/kauthor"
	"orqen/internal/testfakes/hostfake"
)

type kahEnv struct {
	*s4Env
	origin  RunDoc
	finding string
}

// newKahEnv is the fake host with a merged origin run whose evidence is one review finding. The runner is on, so the
// author job runs by itself.
func newKahEnv(t *testing.T, sc *hostfake.Scenario, policy map[string]any, setup func(*App)) *kahEnv {
	t.Helper()
	kauthorHostBackoff = 50 * time.Millisecond
	e := &kahEnv{s4Env: newS4Env(t, s4Opts{Scenario: sc, intakeOpts: intakeOpts{JQL: ptr("key = NONE-1"), Setup: setup}})}
	ctx := context.Background()
	pol := map[string]any{"knowledge.learn_on_merge": true, "knowledge.author": "roles", "roles.poll_interval_secs": 1, "roles.max_wall_secs": 600, "host.turn_timeout_secs": 60,
		"knowledge.promotion.project": "off"}
	for k, v := range policy {
		pol[k] = v
	}
	ver, err := findOne[ModelProfileVersionDoc](ctx, e.f.db, "model_profile_versions", bson.M{"org_id": e.org})
	if err != nil {
		t.Fatal(err)
	}
	e.origin = s7InsertRun(t, e.f.db, RunDoc{OrgID: e.org, ProjectID: e.pid, State: "merged", Pinned: RunPinned{PolicyVersionID: s7Policy(t, e.f.db, e.org, pol), ProfileVersionID: &ver.ID}, Heads: RunHeads{Head: "abc"}})
	if _, err = e.f.db.Coll("runs").UpdateOne(ctx, bson.M{"_id": e.origin.ID}, bson.M{"$set": bson.M{"active": false}}); err != nil {
		t.Fatal(err)
	}
	f := ReviewFindingDoc{ID: newID("fnd"), OrgID: e.org, RunID: e.origin.ID, Round: 1, Severity: "minor", Title: "Missing newline", Body: "Add a trailing newline.", Status: "fixed", CreatedAt: time.Now().UTC(), Revision: 1}
	if _, err = e.f.db.Coll("review_findings").InsertOne(ctx, f); err != nil {
		t.Fatal(err)
	}
	e.finding = f.ID
	return e
}

func (e *kahEnv) newSession() KnowledgeAuthorSessionDoc {
	var kas KnowledgeAuthorSessionDoc
	err := e.f.a.txnRetryJob(context.Background(), func(sc context.Context) error {
		var err error
		kas, err = e.f.a.createAuthorSessionTx(sc, e.origin, "merge", 1, "kas:merge:"+e.origin.ID)
		return err
	})
	if err != nil {
		e.f.t.Fatal(err)
	}
	return kas
}

func (e *kahEnv) session(id string) KnowledgeAuthorSessionDoc {
	kas, err := e.f.a.authorSession(context.Background(), e.org, id)
	if err != nil {
		e.f.t.Fatal(err)
	}
	return kas
}

func (e *kahEnv) waitSession(id, state string) KnowledgeAuthorSessionDoc {
	e.f.t.Helper()
	waitFor(e.f.t, "session in state "+state, func() bool { return e.session(id).State == state })
	return e.session(id)
}

func (e *kahEnv) originDoc() bson.Raw {
	raw, err := e.f.db.Coll("runs").FindOne(context.Background(), bson.M{"_id": e.origin.ID}).Raw()
	if err != nil {
		e.f.t.Fatal(err)
	}
	return raw
}

func (e *kahEnv) proposals() []KnowledgeProposalDoc {
	list, _ := findMany[KnowledgeProposalDoc](context.Background(), e.f.db, "knowledge_proposals", bson.M{"org_id": e.org}, bson.D{{Key: "_id", Value: 1}}, 20)
	return list
}

func TestS7RolesAuthorHappyPathThroughTheDetachedHost(t *testing.T) {
	e := newKahEnv(t, hostfake.DefaultScenario(), nil, nil)
	before := e.originDoc()
	glWrites := len(e.gl.Writes())
	kas := e.newSession()
	done := e.waitSession(kas.ID, kauthor.Applied)
	if n := len(e.cat.CallsOf("dispatch")); n != 2 {
		t.Fatalf("exactly one writer and one verifier dispatch, got %d", n)
	}
	if done.Writer.Name != "writer-K1" || done.KVerifier.Name != "kverify-K1" || done.ProposalID == "" || done.Catherd.RunID == "" || done.BundleArtifactID == "" {
		t.Fatalf("%+v", done)
	}
	ps := e.proposals()
	if len(ps) != 1 || ps[0].Trigger != "merge_roles" || ps[0].ID != done.ProposalID {
		t.Fatalf("%+v", ps)
	}
	if string(e.originDoc()) != string(before) {
		t.Fatal("the origin run must not change")
	}
	if e.count("role_executions", bson.M{"run_id": e.origin.ID}) != 0 || e.count("host_sessions", bson.M{"run_id": e.origin.ID}) != 0 {
		t.Fatal("the session writes no role executions or host sessions for the origin run")
	}
	if len(e.gl.Writes()) != glWrites {
		t.Fatal("no GitLab write")
	}
	waitFor(t, "the author job to complete with the session", func() bool { return e.count("jobs", bson.M{"kind": kauthorJobKind, "active": true}) == 0 })
}

func TestS7RolesAuthorFailuresFallBackToTheDeterministicAuthor(t *testing.T) {
	// the writer never produces a parseable reply: two writer dispatches, no verifier, then the deterministic proposal
	sc := hostfake.DefaultScenario()
	sc.WriterReply = []string{"I could not do that."}
	e := newKahEnv(t, sc, nil, nil)
	kas := e.newSession()
	e.waitSession(kas.ID, kauthor.Failed)
	waitFor(t, "the fallback proposal", func() bool { return e.session(kas.ID).FallbackProposalID != "" })
	got := e.session(kas.ID)
	if got.Code != kauthor.CodeReplyUnparseable || got.Writer.Tries != 2 || len(e.cat.CallsOf("dispatch")) != 2 || got.FallbackProposalID == "" {
		t.Fatalf("%+v dispatches=%d", got, len(e.cat.CallsOf("dispatch")))
	}
	ps := e.proposals()
	if len(ps) != 1 || ps[0].Trigger != "merge" {
		t.Fatalf("%+v", ps)
	}

	// the writer role is disabled in the pinned profile: nothing is dispatched
	d := newKahEnv(t, hostfake.DefaultScenario(), nil, nil)
	if _, err := d.f.db.Coll("model_profile_versions").UpdateMany(context.Background(), bson.M{"org_id": d.org}, bson.M{"$set": bson.M{"doc.roles.writer.enabled": false}}); err != nil {
		t.Fatal(err)
	}
	k2 := d.newSession()
	d.waitSession(k2.ID, kauthor.Failed)
	waitFor(t, "the fallback proposal", func() bool { return d.session(k2.ID).FallbackProposalID != "" })
	g2 := d.session(k2.ID)
	if g2.Code != kauthor.CodeWriterDisabled || len(d.cat.CallsOf("dispatch")) != 0 || len(d.proposals()) != 1 {
		t.Fatalf("%+v", g2)
	}

	// the host is down: five failed starts, then host_unavailable
	h := newKahEnv(t, hostfake.DefaultScenario(), nil, nil)
	h.markHostProbed(false)
	k3 := h.newSession()
	g3 := h.waitSession(k3.ID, kauthor.Failed)
	if g3.Code != kauthor.CodeHostUnavailable || g3.HostFailures < kauthor.MaxHostFailures-1 || len(h.cat.CallsOf("dispatch")) != 0 {
		t.Fatalf("%+v", g3)
	}
}

func TestS7RolesAuthorCrashAdoptionCancelAndPause(t *testing.T) {
	// a crash after the dispatch call: the next pass adopts the dispatch by name
	var crashed atomic.Bool
	e := newKahEnv(t, hostfake.DefaultScenario(), nil, func(a *App) {
		a.runHook = func(stage string) error {
			if stage == "kauthor_writer_dispatched" && crashed.CompareAndSwap(false, true) {
				return errors.New("lease lost after the dispatch call")
			}
			return nil
		}
	})
	kas := e.newSession()
	e.waitSession(kas.ID, kauthor.Applied)
	if !crashed.Load() || len(e.cat.CallsOf("dispatch")) != 2 {
		t.Fatalf("crashed=%v dispatches=%d", crashed.Load(), len(e.cat.CallsOf("dispatch")))
	}

	// cancel while the writer runs: catherd is asked to cancel the role and the session ends cancelled with no drafts
	sc := hostfake.DefaultScenario()
	sc.RecordDelayMs = 600000
	c := newKahEnv(t, sc, nil, nil)
	k2 := c.newSession()
	waitFor(t, "writer running", func() bool { return c.session(k2.ID).State == kauthor.WriterRunning })
	cur := c.session(k2.ID)
	c.f.json("POST", fmt.Sprintf("/api/v1/orgs/%s/knowledge-author-sessions/%s/cancel", c.org, k2.ID), nil, map[string]string{"If-Match": fmt.Sprint(cur.Revision)}, 200)
	got := c.waitSession(k2.ID, kauthor.Cancelled)
	if got.Code != kauthor.CodeCancelled || len(c.proposals()) != 0 || len(c.cat.CallsOf("dispatch")) != 1 {
		t.Fatalf("%+v", got)
	}

	// automation paused: no dispatch until it is resumed
	p := newKahEnv(t, hostfake.DefaultScenario(), nil, nil)
	if _, err := p.f.db.Coll("automation_states").UpdateOne(context.Background(), bson.M{"_id": automationStateID(p.org)}, bson.M{"$set": bson.M{"org_id": p.org, "paused": true}, "$setOnInsert": bson.M{"revision": int64(1)}}, options.UpdateOne().SetUpsert(true)); err != nil {
		t.Fatal(err)
	}
	k3 := p.newSession()
	time.Sleep(1500 * time.Millisecond)
	if len(p.cat.CallsOf("dispatch")) != 0 || p.session(k3.ID).State == kauthor.Applied {
		t.Fatal("a paused organization dispatches nothing")
	}
	if _, err := p.f.db.Coll("automation_states").UpdateOne(context.Background(), bson.M{"_id": automationStateID(p.org)}, bson.M{"$set": bson.M{"paused": false}}); err != nil {
		t.Fatal(err)
	}
	p.waitSession(k3.ID, kauthor.Applied)
}
