package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"orqen/internal/providers"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"orqen/internal/knowledge"
)

const maxSourceText = 256 << 10

type sourceIn struct {
	Org, Kind, ConnectionID, ExternalID, URL, Title string
	ProjectID                                       *string
	RemoteVersion                                   int
	RemoteUpdatedAt, Text, ContentHash, RawHash     string
	Truncated, LinkOnly                             bool
	Authority                                       string
	Links                                           []string
}

// ingestSource stores one fetched source version. It is idempotent on the content hash: an unchanged hash returns the
// current latest. A changed hash inserts the next version, flips latest in the same transaction and marks verified
// knowledge that cites the old version as evidence_stale (K16).
func (a *App) ingestSource(ctx context.Context, in sourceIn) (SourceVersionDoc, bool, error) {
	text := in.Text
	if len(text) > maxSourceText {
		text, in.Truncated = text[:maxSourceText], true
	}
	var artID string
	if !in.LinkOnly {
		blob, err := storeBlob(a.artifactRoot(), in.Org, bytes.NewReader([]byte(text)), maxSourceText+1024)
		if err != nil {
			return SourceVersionDoc{}, false, err
		}
		art, err := a.recordArtifact(ctx, in.Org, blob, "text/plain", "source", Subject{"source", in.ExternalID})
		if err != nil {
			return SourceVersionDoc{}, false, err
		}
		artID = art.ID
	}
	hash := in.ContentHash
	if hash == "" {
		hash = knowledge.SHA(knowledge.NormText(text))
	}
	var out SourceVersionDoc
	changed := false
	err := a.DB.Txn(ctx, func(sc context.Context) error {
		changed = false
		filter := bson.M{"org_id": in.Org, "kind": in.Kind, "connection_id": in.ConnectionID, "external_id": in.ExternalID, "latest": true}
		cur, err := findOne[SourceVersionDoc](sc, a.DB, "source_versions", filter)
		if err == nil && cur.ContentHash == hash {
			out = cur
			return nil
		}
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			return err
		}
		prev := ""
		if err == nil {
			prev = cur.ID
			if _, err = a.DB.Coll("source_versions").UpdateOne(sc, bson.M{"_id": cur.ID}, bson.M{"$set": bson.M{"latest": false}}); err != nil {
				return err
			}
		}
		out = SourceVersionDoc{ID: newID("srv"), OrgID: in.Org, ProjectID: in.ProjectID, Kind: in.Kind, ConnectionID: in.ConnectionID, ExternalID: in.ExternalID, URL: in.URL, Title: in.Title,
			RemoteVersion: in.RemoteVersion, RemoteUpdatedAt: in.RemoteUpdatedAt, ContentHash: hash, RawHash: in.RawHash, ArtifactID: artID, Bytes: len(text), Truncated: in.Truncated,
			LinkOnly: in.LinkOnly, Authority: in.Authority, FetchedAt: time.Now().UTC(), PrevID: prev, Latest: true, Links: in.Links}
		if _, err = a.DB.Coll("source_versions").InsertOne(sc, out); err != nil {
			return err
		}
		changed = true
		if prev != "" {
			if _, err = a.DB.Coll("knowledge_versions").UpdateMany(sc, bson.M{"org_id": in.Org, "status": "verified", "claims.evidence.ref_id": prev}, bson.M{"$set": bson.M{"evidence_stale": true}}); err != nil {
				return err
			}
			if err = a.AppendEvent(sc, in.Org, "source.changed", Subject{"source_version", out.ID}, bson.M{"source_version_id": out.ID, "previous": prev, "kind": in.Kind}, nil, nil, true); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		a.Committed()
		if changed && out.PrevID != "" {
			_ = a.proposeSourceChanged(ctx, in.Org, out.PrevID, out.ID)
		}
	}
	return out, changed, err
}

// pageSource turns a Confluence page into a source input. Page text is data; it is never interpreted here.
func pageSource(org, conn string, project *string, p providers.Page) sourceIn {
	return sourceIn{Org: org, Kind: "confluence_page", ConnectionID: conn, ExternalID: p.ID, URL: p.URL, Title: p.Title, ProjectID: project, RemoteVersion: p.Version,
		RemoteUpdatedAt: p.UpdatedAt, Text: p.Text, ContentHash: p.ContentHash, RawHash: p.RawHash, Truncated: p.Truncated, Authority: knowledge.AuthorityCanonicalExternal, Links: p.Links}
}

// SyncRefResult is the outcome of the last sync of one page reference (derived state, collection source_sync_states).
type SyncRefResult struct {
	ConnectionID string    `bson:"connection_id"`
	PageID       string    `bson:"page_id"`
	OK           bool      `bson:"ok"`
	Code         string    `bson:"code"`
	At           time.Time `bson:"at"`
}

// SourceSyncStateDoc is the per-project record of the last source sync (_id = project id). It is derived, rebuilt by the
// next sync, and excluded from backups.
type SourceSyncStateDoc struct {
	ID         string          `bson:"_id"`
	OrgID      string          `bson:"org_id"`
	Refs       []SyncRefResult `bson:"refs"`
	LastSyncAt time.Time       `bson:"last_sync_at"`
}

// syncProjectSources fetches every page reference of a project's mapping (GET only) and ingests the versions.
func (a *App) syncProjectSources(ctx context.Context, org string, proj ProjectDoc, docs func(ConnectionDoc) (providers.DocsSource, error)) (synced, changed int, err error) {
	synced, changed, _, err = a.syncProjectSourcesR(ctx, org, proj, docs)
	return
}

// syncProjectSourcesR is syncProjectSources that also reports the result of every reference. A failed reference is
// skipped exactly as before; the report is what readiness uses (K41).
func (a *App) syncProjectSourcesR(ctx context.Context, org string, proj ProjectDoc, docs func(ConnectionDoc) (providers.DocsSource, error)) (synced, changed int, res []SyncRefResult, err error) {
	for _, ref := range proj.Mapping.Confluence {
		if ref.PageID == nil {
			continue
		}
		r := SyncRefResult{ConnectionID: ref.ConnectionID, PageID: *ref.PageID, At: time.Now().UTC()}
		conn, e := a.connection(ctx, org, ref.ConnectionID)
		if e != nil {
			r.Code = "connection_unavailable"
			res = append(res, r)
			continue
		}
		src, e := docs(conn)
		if e != nil {
			r.Code = "connection_unavailable"
			res = append(res, r)
			continue
		}
		page, e := src.GetPage(ctx, *ref.PageID)
		if e != nil {
			if pe, ok := providers.AsError(e); ok && pe.Retryable() {
				return synced, changed, res, e
			}
			r.Code = "fetch_failed"
			if pe, ok := providers.AsError(e); ok {
				r.Code = string(pe.Kind)
			}
			res = append(res, r)
			continue
		}
		pid := proj.ID
		_, ch, e := a.ingestSource(ctx, pageSource(org, conn.ID, &pid, page))
		if e != nil {
			return synced, changed, res, e
		}
		r.OK = true
		res = append(res, r)
		synced++
		if ch {
			changed++
		}
	}
	return
}

func (a *App) runSourceSync(ctx context.Context, job JobDoc, lease Lease) error {
	id, ok := job.Payload["project_id"].(string)
	if !ok || !validID("prj", id) {
		return &JobFailure{Code: "bad_payload", Message: "missing project_id"}
	}
	if job.OperationID != nil {
		if err := a.fenced(ctx, lease, func(sc context.Context) error { return a.transitionOperation(sc, job, "running", nil) }); err != nil {
			return err
		}
	}
	proj, err := a.project(ctx, job.OrgID, id)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return &JobFailure{Code: "project_archived", Message: "project no longer exists"}
	}
	if err != nil {
		return err
	}
	synced, changed, results, err := a.syncProjectSourcesR(ctx, job.OrgID, proj, func(c ConnectionDoc) (providers.DocsSource, error) { return a.docsSource(ctx, c) })
	if err != nil {
		return err
	}
	return a.fenced(ctx, lease, func(sc context.Context) error {
		if results == nil {
			results = []SyncRefResult{}
		}
		if _, e := a.DB.Coll("source_sync_states").UpdateOne(sc, bson.M{"_id": proj.ID}, bson.M{"$set": bson.M{"org_id": job.OrgID, "refs": results, "last_sync_at": time.Now().UTC()}}, upsertOpt()); e != nil {
			return e
		}
		if err := a.transitionOperation(sc, job, "succeeded", bson.M{"synced": synced, "changed": changed}); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
}

func (a *App) startSourceSync(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("prjId")
	route := "POST /api/v1/orgs/{orgId}/projects/{prjId}/sources/sync"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "prjId": id}, nil), func(ctx context.Context) (Output, error) {
		if _, err := a.project(ctx, org, id); err != nil {
			return Output{}, err
		}
		return a.startOperation(ctx, org, "source_sync", Subject{"project", id}, "source_sync:"+id, 3, bson.M{"project_id": id})
	})
}

func (d SourceVersionDoc) DTO() bson.M {
	return bson.M{"id": d.ID, "kind": d.Kind, "connection_id": optStr(d.ConnectionID), "external_id": d.ExternalID, "url": d.URL, "title": d.Title, "remote_version": d.RemoteVersion,
		"content_hash": d.ContentHash, "artifact_id": optStr(d.ArtifactID), "bytes": d.Bytes, "truncated": d.Truncated, "link_only": d.LinkOnly, "authority": d.Authority,
		"fetched_at": timestamp(d.FetchedAt), "prev_id": optStr(d.PrevID), "latest": d.Latest, "project_id": d.ProjectID}
}

func (a *App) listSources(w http.ResponseWriter, r *http.Request) error {
	limit, after, err := pageQuery(r)
	if err != nil {
		return err
	}
	f := bson.M{"org_id": r.PathValue("orgId")}
	q := r.URL.Query()
	if v := q.Get("kind"); v != "" {
		f["kind"] = v
	}
	if q.Get("latest") == "true" {
		f["latest"] = true
	}
	if v := q.Get("project_id"); v != "" {
		f["project_id"] = v
	}
	page, err := listPage[SourceVersionDoc](r.Context(), a.DB, "source_versions", f, limit, after)
	if err != nil {
		return err
	}
	writeJSON(w, 200, page)
	return nil
}

func (a *App) getSource(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	d, err := findOne[SourceVersionDoc](r.Context(), a.DB, "source_versions", bson.M{"_id": r.PathValue("srvId"), "org_id": org})
	if err != nil {
		return err
	}
	body := d.DTO()
	if d.ArtifactID != "" {
		t, _ := a.artifactText(r.Context(), org, d.ArtifactID, 8192)
		body["text_preview"] = t
	}
	writeJSON(w, 200, body)
	return nil
}
