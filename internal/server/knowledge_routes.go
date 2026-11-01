package server

import (
	"net/http"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/knowledge"
)

const knowledgeUser = "user"

func (a *App) recordResponse(w http.ResponseWriter, status int, rec KnowledgeRecordDoc) {
	w.Header().Set("ETag", etag(rec.Revision))
	writeJSON(w, status, rec.DTO())
}

func (a *App) listKnowledge(w http.ResponseWriter, r *http.Request) error {
	limit, after, err := pageQuery(r)
	if err != nil {
		return err
	}
	org, q := r.PathValue("orgId"), r.URL.Query()
	f := bson.M{"org_id": org}
	if q.Get("include_archived") != "true" {
		f["active"] = true
	}
	for _, k := range []string{"type", "status", "scope_key"} {
		if v := q.Get(k); v != "" {
			f[k] = v
		}
	}
	if p := q.Get("project_id"); p != "" {
		f["scope.project_id"] = p
	}
	page, err := listPage[KnowledgeRecordDoc](r.Context(), a.DB, "knowledge_records", f, limit, after)
	if err != nil {
		return err
	}
	writeJSON(w, 200, page)
	return nil
}

func (a *App) createKnowledge(w http.ResponseWriter, r *http.Request) error {
	var req CreateKnowledgeRecordRequest
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	rec, v, err := a.createKnowledgeRecord(r.Context(), org, knowledgeUser, req, knowledge.Origin{Kind: "user", Actor: knowledgeUser})
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/api/v1/orgs/"+org+"/knowledge/"+rec.ID)
	body := rec.DTO()
	body["current_version"] = a.versionDTO(r.Context(), org, v, nil)
	w.Header().Set("ETag", etag(rec.Revision))
	writeJSON(w, 201, body)
	return nil
}

func (a *App) getKnowledge(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	rec, err := a.knowledgeRecord(r.Context(), org, r.PathValue("knoId"))
	if err != nil {
		return err
	}
	body := rec.DTO()
	if v, e := a.knowledgeVersion(r.Context(), org, rec.CurrentVersionID); e == nil {
		body["current_version"] = a.versionDTO(r.Context(), org, v, nil)
	}
	if rec.VerifiedVersionID != nil {
		if v, e := a.knowledgeVersion(r.Context(), org, *rec.VerifiedVersionID); e == nil {
			body["verified_version"] = a.versionDTO(r.Context(), org, v, nil)
		}
	}
	w.Header().Set("ETag", etag(rec.Revision))
	writeJSON(w, 200, body)
	return nil
}

func (a *App) listKnowledgeVersions(w http.ResponseWriter, r *http.Request) error {
	org, id := r.PathValue("orgId"), r.PathValue("knoId")
	if _, err := a.knowledgeRecord(r.Context(), org, id); err != nil {
		return err
	}
	docs, err := findMany[KnowledgeVersionDoc](r.Context(), a.DB, "knowledge_versions", bson.M{"org_id": org, "record_id": id}, bson.D{{Key: "number", Value: -1}}, 200)
	if err != nil {
		return err
	}
	items := []bson.M{}
	for _, d := range docs {
		items = append(items, a.versionDTO(r.Context(), org, d, nil))
	}
	writeJSON(w, 200, bson.M{"items": items, "next_cursor": nil})
	return nil
}

func (a *App) createKnowledgeVersionRoute(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req CreateKnowledgeVersionRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	v, err := a.createKnowledgeVersion(r.Context(), org, r.PathValue("knoId"), expected, req, knowledge.Origin{Kind: "user", Actor: knowledgeUser})
	if err != nil {
		return err
	}
	writeJSON(w, 201, a.versionDTO(r.Context(), org, v, nil))
	return nil
}

func (a *App) getKnowledgeVersion(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	v, err := a.knowledgeVersion(r.Context(), org, r.PathValue("kvrId"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, a.versionDTO(r.Context(), org, v, nil))
	return nil
}

type VerifyKnowledgeRequest struct {
	Note         string             `json:"note"`
	Attestations []AttestationInput `json:"attestations"`
}

func (a *App) verifyKnowledgeVersion(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req VerifyKnowledgeRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("kvrId")
	if _, err = a.verifyVersion(r.Context(), org, id, verifyOpts{Actor: knowledge.ActorUser, User: knowledgeUser, IfMatch: expected, Note: req.Note, Attest: attestMap(req.Attestations)}); err != nil {
		return err
	}
	v, err := a.knowledgeVersion(r.Context(), org, id)
	if err != nil {
		return err
	}
	writeJSON(w, 200, a.versionDTO(r.Context(), org, v, nil))
	return nil
}

type RejectKnowledgeRequest struct {
	Reason string `json:"reason"`
}

func (a *App) rejectKnowledgeVersion(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req RejectKnowledgeRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("kvrId")
	if err = a.rejectVersion(r.Context(), org, id, expected, strings.TrimSpace(req.Reason), knowledgeUser); err != nil {
		return err
	}
	a.Committed()
	v, err := a.knowledgeVersion(r.Context(), org, id)
	if err != nil {
		return err
	}
	writeJSON(w, 200, a.versionDTO(r.Context(), org, v, nil))
	return nil
}

type RollbackKnowledgeRequest struct {
	ToVersionID string `json:"to_version_id"`
	Note        string `json:"note"`
}

func (a *App) rollbackKnowledge(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req RollbackKnowledgeRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	out, nv, err := a.rollbackRecord(r.Context(), org, r.PathValue("knoId"), expected, req.ToVersionID, req.Note, knowledgeUser)
	if err != nil {
		return err
	}
	cur, err := a.knowledgeVersion(r.Context(), org, nv.ID)
	if err != nil {
		return err
	}
	status := 200
	if cur.Status != "verified" {
		status = 202
	}
	writeJSON(w, status, bson.M{"outcome": out.Outcome, "version": a.versionDTO(r.Context(), org, cur, nil)})
	return nil
}

type ArchiveKnowledgeRequest struct {
	Reason string `json:"reason"`
}

func (a *App) archiveKnowledge(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req ArchiveKnowledgeRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	rec, err := a.archiveRecord(r.Context(), r.PathValue("orgId"), r.PathValue("knoId"), expected, strings.TrimSpace(req.Reason))
	if err != nil {
		return err
	}
	a.recordResponse(w, 200, rec)
	return nil
}

func (a *App) pinKnowledge(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req PinKnowledgeRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	rec, err := a.pinRecord(r.Context(), r.PathValue("orgId"), r.PathValue("knoId"), expected, req)
	if err != nil {
		return err
	}
	a.recordResponse(w, 200, rec)
	return nil
}

func (a *App) listKnowledgeConflicts(w http.ResponseWriter, r *http.Request) error {
	limit, after, err := pageQuery(r)
	if err != nil {
		return err
	}
	f := bson.M{"org_id": r.PathValue("orgId")}
	if s := r.URL.Query().Get("state"); s != "" {
		f["state"] = s
	}
	docs, err := findMany[KnowledgeConflictDoc](r.Context(), a.DB, "knowledge_conflicts", f, bson.D{{Key: "_id", Value: 1}}, limit)
	_ = after
	if err != nil {
		return err
	}
	items := []bson.M{}
	for _, d := range docs {
		items = append(items, a.conflictDTO(d))
	}
	writeJSON(w, 200, bson.M{"items": items, "next_cursor": nil})
	return nil
}

func (a *App) resolveKnowledgeConflict(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req ResolveKnowledgeConflictRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	c, err := a.resolveConflict(r.Context(), r.PathValue("orgId"), r.PathValue("kcfId"), expected, req, knowledgeUser)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(c.Revision))
	writeJSON(w, 200, a.conflictDTO(c))
	return nil
}

func (a *App) searchKnowledge(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 200 {
		return validation("q", "must be 1 to 200 characters")
	}
	limit, _, err := pageQuery(r)
	if err != nil {
		return err
	}
	hits, err := a.searchVerified(r.Context(), r.PathValue("orgId"), q, r.URL.Query().Get("type"), r.URL.Query().Get("project_id"), int(limit))
	if err != nil {
		return err
	}
	writeJSON(w, 200, hits)
	return nil
}
