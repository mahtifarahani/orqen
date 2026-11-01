package server

import (
	"context"
	"errors"
	"orqen/internal/kindex"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func (e *kenv) verified(key, text string, src string) (string, string) {
	rec := e.create(key, "Title "+key, "", claim(text, src, text))
	id, v := rec["id"].(string), rec["current_version"].(map[string]any)["id"].(string)
	e.f.json("POST", e.base()+"/knowledge-versions/"+v+"/verify", map[string]any{}, e.rev(id), 200)
	return id, v
}

func (e *kenv) search(q string) map[string]any {
	return e.f.json("GET", e.base()+"/knowledge-search?q="+q, nil, nil, 200)
}

func TestWikiDownFallbackThenRecoverAndRebuild(t *testing.T) {
	e := newKEnv(t)
	fake := kindex.NewFake()
	e.f.a.Index = fake
	ctx := context.Background()
	src := e.source(e.org, "Doc", "Zebra crossing rules apply downtown.")
	fake.Down = true
	_, v1 := e.verified("zebra", "Zebra crossing rules apply downtown", src)
	if err := e.f.a.maintainWiki(ctx, e.org); err == nil {
		t.Fatal("maintain must fail while the wiki is down")
	}
	st := e.f.json("GET", e.base()+"/wiki/status", nil, nil, 200)
	if st["mode"] != "fallback" || st["pending"].(float64) < 1 || st["degraded"] == nil {
		t.Fatal(st)
	}
	r := e.search("zebra")
	if r["mode"] != "fallback" || len(r["items"].([]any)) != 1 {
		t.Fatal(r)
	}
	if vs, _ := e.f.a.knowledgeVersion(ctx, e.org, v1); vs.Status != "verified" {
		t.Fatal("verification must survive a wiki outage")
	}
	fake.Down = false
	if err := e.f.a.maintainWiki(ctx, e.org); err != nil {
		t.Fatal(err)
	}
	r = e.search("zebra")
	if r["mode"] != "wiki" || len(r["items"].([]any)) != 1 {
		t.Fatal(r)
	}
	before := r["items"].([]any)[0].(map[string]any)["version_id"]
	e.f.json("POST", e.base()+"/wiki/rebuild", nil, nil, 202)
	if err := e.f.a.maintainWiki(ctx, e.org); err != nil {
		t.Fatal(err)
	}
	st = e.f.json("GET", e.base()+"/wiki/status", nil, nil, 200)
	r = e.search("zebra")
	if st["generation"].(float64) != 1 || r["items"].([]any)[0].(map[string]any)["version_id"] != before {
		t.Fatal(st, r)
	}
}

func TestWikiForeignHitIsDropped(t *testing.T) {
	e := newKEnv(t)
	fake := kindex.NewFake()
	e.f.a.Index = fake
	ctx := context.Background()
	src := e.source(e.org, "Doc", "Quokka facts.")
	e.verified("quokka", "Quokka facts", src)
	if err := e.f.a.maintainWiki(ctx, e.org); err != nil {
		t.Fatal(err)
	}
	fake.SearchOverride = func(string) []kindex.Hit { return []kindex.Hit{{ID: "kvr_foreignorg", Score: 9, Snippet: "x"}} }
	r := e.search("quokka")
	if len(r["items"].([]any)) != 0 {
		t.Fatal(r)
	}
	n, _ := e.f.db.Coll("events").CountDocuments(ctx, bson.M{"org_id": e.org, "type": "knowledge.scope_violation"})
	if n != 1 {
		t.Fatal("scope violation event", n)
	}
}

func TestWikiNotConfiguredIsFallback(t *testing.T) {
	e := newKEnv(t)
	src := e.source(e.org, "Doc", "Narwhal facts.")
	e.verified("narwhal", "Narwhal facts", src)
	r := e.search("narwhal")
	if r["mode"] != "fallback" || r["degraded"] != "wiki_not_configured" {
		t.Fatal(r)
	}
}

func TestWikiVaultLockFenceAndSweep(t *testing.T) {
	e := newKEnv(t)
	fake := kindex.NewFake()
	e.f.a.Index = fake
	ctx := context.Background()
	src := e.source(e.org, "Doc", "Okapi facts are listed here.")
	e.verified("okapi", "Okapi facts are listed here", src)
	// Another writer holds the vault lock: the job reports vault_busy and runs no verb.
	vault := e.f.a.vaultFor(e.org)
	if err := os.MkdirAll(vault.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, _ := os.OpenFile(filepath.Join(vault.Dir, "vault.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	err := e.f.a.maintainWiki(ctx, e.org)
	var jf *JobFailure
	if !errors.As(err, &jf) || jf.Code != "vault_busy" || len(fake.Calls) != 0 {
		t.Fatal(err, fake.Calls)
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	lock.Close()
	// A lost lease stops the run before the next verb: ingest ran, compile and lint did not.
	err = e.f.a.maintainWikiFenced(ctx, e.org, func(phase string) error {
		if phase == "compile" {
			return errors.New("lease lost")
		}
		return nil
	})
	if err == nil || len(fake.Calls) != 1 || fake.Calls[0] != "ingest" {
		t.Fatal(err, fake.Calls)
	}
	// The sweep queues the maintain job for the pending outbox; the real run then applies it once.
	if err = e.f.a.sweepWiki(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.f.db.Coll("jobs").CountDocuments(ctx, bson.M{"org_id": e.org, "kind": "wiki_maintain", "active": true}); n != 1 {
		t.Fatal("jobs", n)
	}
	if err = e.f.a.maintainWiki(ctx, e.org); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.f.db.Coll("wiki_outbox").CountDocuments(ctx, bson.M{"org_id": e.org, "state": "pending"}); n != 0 {
		t.Fatal("pending", n)
	}
}
