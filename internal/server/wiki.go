package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"orqen/internal/extensions"
	"orqen/internal/kindex"
)

// WikiStateDoc is the per-org projection state. The Wiki vault is derived data; MongoDB stays canonical.
type WikiStateDoc struct {
	ID               string     `bson:"_id"`  // org id
	Mode             string     `bson:"mode"` // wiki | fallback
	Degraded         *WikiIssue `bson:"degraded"`
	AppliedSeq       int64      `bson:"applied_seq"`
	Generation       int        `bson:"generation"`
	RebuildRequested bool       `bson:"rebuild_requested"`
	UpdatedAt        time.Time  `bson:"updated_at"`
}
type WikiIssue struct {
	Code  string    `bson:"code"`
	Phase string    `bson:"phase"`
	At    time.Time `bson:"at"`
}

// indexFor returns the knowledge index of one organization: the injected port (tests), otherwise the OpenClaw Wiki
// CLI when the openclaw-wiki extension is usable for the org. Nil means the Mongo fallback.
func (a *App) indexFor(ctx context.Context, org string) kindex.Index {
	if a.Index != nil {
		return a.Index
	}
	m, ok := extensions.Get("openclaw-wiki")
	if !ok {
		return nil
	}
	p, why, err := a.extUsable(ctx, org, m, "")
	if err != nil || why != "" {
		return nil
	}
	return &kindex.CLI{Bin: p.Path(), DataDir: a.Config.DataDir, ChildPath: "/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin", Sandbox: a.wikiSandboxMode(ctx, org), Timeout: 60 * time.Second}
}

func (a *App) vaultFor(org string) kindex.Vault {
	return kindex.Vault{OrgID: org, Dir: filepath.Join(a.Config.DataDir, "wiki", org, "vault")}
}

func (a *App) wikiState(ctx context.Context, org string) WikiStateDoc {
	st, err := findOne[WikiStateDoc](ctx, a.DB, "wiki_vault_states", bson.M{"_id": org})
	if err != nil {
		return WikiStateDoc{ID: org, Mode: "fallback"}
	}
	return st
}

func (a *App) pendingOutbox(ctx context.Context, org string) (int64, error) {
	return a.DB.Coll("wiki_outbox").CountDocuments(ctx, bson.M{"org_id": org, "state": "pending"})
}

func (a *App) setWiki(ctx context.Context, org string, set bson.M) error {
	set["updated_at"], set["org_id"] = time.Now().UTC(), org // org_id carries the unique index, so a second organization never collides on null
	_, err := a.DB.Coll("wiki_vault_states").UpdateOne(ctx, bson.M{"_id": org}, bson.M{"$set": set}, options.UpdateOne().SetUpsert(true))
	return err
}

func (a *App) degrade(ctx context.Context, org, code, phase string) {
	_ = a.setWiki(ctx, org, bson.M{"mode": "fallback", "degraded": WikiIssue{code, phase, time.Now().UTC()}})
	_ = a.AppendEvent(ctx, org, "wiki.degraded", Subject{"organization", org}, bson.M{"code": code, "phase": phase}, nil, nil, true)
	a.Committed()
}

type outboxRow struct {
	ID        string `bson:"_id"`
	Seq       int64  `bson:"seq"`
	Op        string `bson:"op"`
	VersionID string `bson:"version_id"`
	RecordID  string `bson:"record_id"`
	Hash      string `bson:"content_hash"`
}

func (a *App) projectionDoc(ctx context.Context, org string, v KnowledgeVersionDoc) kindex.DocIn {
	rec, _ := a.knowledgeRecord(ctx, org, v.RecordID)
	claims := []string{}
	for _, c := range v.Claims {
		if c.Verdict == "supported" {
			claims = append(claims, c.Text)
		}
	}
	at := ""
	if v.VerifiedAt != nil {
		at = timestamp(*v.VerifiedAt)
	}
	return kindex.DocIn{OrgID: org, RecordID: v.RecordID, VersionID: v.ID, Type: v.Type, ScopeKey: v.ScopeKey, Title: v.Title, Body: v.Body, Claims: claims, ContentHash: v.ContentHash, VerifiedAt: at, Pinned: rec.Pinned, Critical: rec.Critical}
}

// maintainWiki applies the pending outbox (or rebuilds) to the vault through the index port. It returns a *JobFailure
// (retryable) when the index is unavailable; verified knowledge is never affected by that.
func (a *App) maintainWiki(ctx context.Context, org string) error {
	return a.maintainWikiFenced(ctx, org, nil)
}

// maintainWikiFenced is maintainWiki with a fence called before every index verb: when it fails (the job lost its
// lease) no further verb runs. One writer per vault is enforced with a file lock.
func (a *App) maintainWikiFenced(ctx context.Context, org string, fence func(phase string) error) error {
	idx := a.indexFor(ctx, org)
	if idx == nil {
		return a.setWiki(ctx, org, bson.M{"mode": "fallback", "degraded": WikiIssue{"wiki_not_configured", "", time.Now().UTC()}})
	}
	st := a.wikiState(ctx, org)
	vault := a.vaultFor(org)
	projDir := filepath.Join(vault.Dir, "projection")
	if err := os.MkdirAll(vault.Dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(vault.Dir, "vault.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return &JobFailure{Code: "vault_busy", Message: "another writer holds the vault", Retryable: true, RetryAfter: 5 * time.Second}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	var applied []string
	var maxSeq int64
	if st.RebuildRequested {
		if err := os.RemoveAll(projDir); err != nil {
			return err
		}
		vs, err := findMany[KnowledgeVersionDoc](ctx, a.DB, "knowledge_versions", bson.M{"org_id": org, "status": "verified"}, bson.D{{Key: "_id", Value: 1}}, 100000)
		if err != nil {
			return err
		}
		for _, v := range vs {
			if err = kindex.WriteAtomic(projDir, kindex.DocRelPath(v.ID), kindex.RenderDoc(a.projectionDoc(ctx, org, v), nil)); err != nil {
				return err
			}
		}
	}
	rows, err := findMany[outboxRow](ctx, a.DB, "wiki_outbox", bson.M{"org_id": org, "state": "pending"}, bson.D{{Key: "seq", Value: 1}}, 200)
	if err != nil {
		return err
	}
	es := make([]kindex.Entry, 0, len(rows))
	for _, r := range rows {
		es = append(es, kindex.Entry{Seq: r.Seq, Op: r.Op, VersionID: r.VersionID, RecordID: r.RecordID, ContentHash: r.Hash})
		applied = append(applied, r.ID)
	}
	if !st.RebuildRequested {
		for _, e := range kindex.Coalesce(es) {
			path := filepath.Join(projDir, kindex.DocRelPath(e.VersionID))
			if e.Op == kindex.OpDelete {
				_ = os.Remove(path)
				continue
			}
			v, err := a.knowledgeVersion(ctx, org, e.VersionID)
			if err != nil || v.Status != "verified" {
				_ = os.Remove(path)
				continue
			}
			if err = kindex.WriteAtomic(projDir, kindex.DocRelPath(v.ID), kindex.RenderDoc(a.projectionDoc(ctx, org, v), nil)); err != nil {
				return err
			}
		}
	}
	maxSeq = kindex.MaxSeq(es)
	if err = os.MkdirAll(projDir, 0o700); err != nil {
		return err
	}
	for _, phase := range []string{"ingest", "compile", "lint"} {
		if fence != nil {
			if ferr := fence(phase); ferr != nil {
				return ferr
			}
		}
		var perr error
		switch phase {
		case "ingest":
			_, perr = idx.Ingest(ctx, vault, projDir)
		case "compile":
			_, perr = idx.Compile(ctx, vault)
		case "lint":
			var lr kindex.LintResult
			if lr, perr = idx.Lint(ctx, vault); perr == nil && len(lr.Errors) > 0 {
				perr = &kindex.Error{Kind: kindex.KindFailed, Verb: "lint", Msg: "lint errors"}
			}
		}
		if perr != nil {
			code := "wiki_unavailable"
			var ke *kindex.Error
			if errors.As(perr, &ke) {
				code = "wiki_" + string(ke.Kind)
			}
			a.degrade(ctx, org, code, phase)
			return &JobFailure{Code: code, Message: "the wiki index failed in " + phase, Retryable: true, RetryAfter: 5 * time.Second}
		}
	}
	gen := st.Generation
	if st.RebuildRequested {
		gen++
	}
	err = a.DB.Txn(ctx, func(sc context.Context) error {
		if len(applied) > 0 {
			if _, e := a.DB.Coll("wiki_outbox").UpdateMany(sc, bson.M{"_id": bson.M{"$in": applied}, "state": "pending"}, bson.M{"$set": bson.M{"state": "applied", "purge_at": time.Now().UTC().Add(a.outboxRetention(ctx, org))}}); e != nil {
				return e
			}
		}
		seq := max(st.AppliedSeq, maxSeq)
		if e := a.setWiki(sc, org, bson.M{"mode": "wiki", "degraded": nil, "applied_seq": seq, "generation": gen, "rebuild_requested": false}); e != nil {
			return e
		}
		if st.Degraded != nil {
			return a.AppendEvent(sc, org, "wiki.recovered", Subject{"organization", org}, bson.M{}, nil, nil, true)
		}
		return nil
	})
	if err == nil {
		a.Committed()
	}
	return err
}

func (a *App) runWikiMaintain(ctx context.Context, job JobDoc, lease Lease) error {
	fence := func(string) error { return a.fenced(ctx, lease, func(context.Context) error { return nil }) }
	if err := a.maintainWikiFenced(ctx, job.OrgID, fence); err != nil {
		return err
	}
	return a.fenced(ctx, lease, func(sc context.Context) error { return a.completeJob(sc, lease) })
}

func (a *App) wikiStatusDTO(ctx context.Context, org string) (bson.M, error) {
	st := a.wikiState(ctx, org)
	n, err := a.pendingOutbox(ctx, org)
	if err != nil {
		return nil, err
	}
	var deg any
	if st.Degraded != nil {
		deg = bson.M{"code": st.Degraded.Code, "phase": st.Degraded.Phase, "at": timestamp(st.Degraded.At)}
	}
	mode := st.Mode
	configured := a.indexFor(ctx, org) != nil
	if !configured || st.Degraded != nil || n > 0 {
		mode = "fallback"
	}
	return bson.M{"configured": configured, "mode": mode, "degraded": deg, "pending": n, "applied_seq": st.AppliedSeq, "generation": st.Generation, "rebuild_requested": st.RebuildRequested}, nil
}

func (a *App) getWikiStatus(w http.ResponseWriter, r *http.Request) error {
	m, err := a.wikiStatusDTO(r.Context(), r.PathValue("orgId"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, m)
	return nil
}

func (a *App) startWikiMaintain(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	if err := a.txnRetryJob(r.Context(), func(sc context.Context) error {
		return a.ensureJobTx(sc, org, "wiki_maintain", "wiki:"+org, 1000, bson.M{})
	}); err != nil {
		return err
	}
	a.Committed()
	return a.respondWikiStatus(w, r, 202)
}

func (a *App) startWikiRebuild(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	if err := a.txnRetryJob(r.Context(), func(sc context.Context) error {
		if e := a.setWiki(sc, org, bson.M{"rebuild_requested": true}); e != nil {
			return e
		}
		return a.ensureJobTx(sc, org, "wiki_maintain", "wiki:"+org, 1000, bson.M{})
	}); err != nil {
		return err
	}
	a.Committed()
	return a.respondWikiStatus(w, r, 202)
}

func (a *App) respondWikiStatus(w http.ResponseWriter, r *http.Request, status int) error {
	m, err := a.wikiStatusDTO(r.Context(), r.PathValue("orgId"))
	if err != nil {
		return err
	}
	writeJSON(w, status, m)
	return nil
}

// sweepWiki queues a maintain job for every org with pending outbox rows (the job itself is deduped per org) and
// purges applied outbox rows past their retention date.
func (a *App) sweepWiki(ctx context.Context) error {
	orgs, err := a.DB.Coll("wiki_outbox").Distinct(ctx, "org_id", bson.M{"state": "pending"}).Raw()
	if err != nil {
		return err
	}
	vals, err := orgs.Values()
	if err != nil {
		return err
	}
	for _, v := range vals {
		org, ok := v.StringValueOK()
		if !ok {
			continue
		}
		if a.indexFor(ctx, org) == nil {
			if err = a.purgeFallbackOutbox(ctx, org); err != nil {
				return err
			}
			continue
		}
		if err = a.txnRetryJob(ctx, func(sc context.Context) error {
			return a.ensureJobTx(sc, org, "wiki_maintain", "wiki:"+org, 1000, bson.M{})
		}); err != nil {
			return err
		}
	}
	_, err = a.DB.Coll("wiki_outbox").DeleteMany(ctx, bson.M{"state": "applied", "purge_at": bson.M{"$lt": time.Now().UTC()}})
	return err
}

// purgeFallbackOutbox drops pending projection rows older than the outbox retention in an organization that has no Wiki
// (fallback mode) and asks for a rebuild, so a Wiki configured later projects everything from MongoDB, which stays
// canonical (Y17). Knowledge itself is never touched.
func (a *App) purgeFallbackOutbox(ctx context.Context, org string) error {
	cut := time.Now().UTC().Add(-a.outboxRetention(ctx, org))
	return a.DB.Txn(ctx, func(sc context.Context) error {
		res, err := a.DB.Coll("wiki_outbox").DeleteMany(sc, bson.M{"org_id": org, "state": "pending", "created_at": bson.M{"$lt": cut}})
		if err != nil || res.DeletedCount == 0 {
			return err
		}
		return a.setWiki(sc, org, bson.M{"rebuild_requested": true})
	})
}

// knowledgeIndexFact is "degraded" when the openclaw-wiki extension is usable for the organization and its vault is
// degraded or behind (K41). A Wiki that is simply not configured is the designed default and gives "".
func (a *App) knowledgeIndexFact(ctx context.Context, org string) string {
	st := a.wikiState(ctx, org)
	behind := st.Degraded != nil || st.RebuildRequested
	if !behind {
		if n, err := a.pendingOutbox(ctx, org); err != nil || n == 0 {
			return ""
		}
	}
	if a.Index == nil {
		m, ok := extensions.Get("openclaw-wiki")
		if !ok {
			return ""
		}
		if _, why, err := a.extUsable(ctx, org, m, ""); err != nil || why != "" {
			return ""
		}
	}
	return "degraded"
}
