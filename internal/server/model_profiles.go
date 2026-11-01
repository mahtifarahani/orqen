package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"orqen/internal/catherd"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type ProfileMeta struct {
	BudgetUncappedAck bool `bson:"budget_uncapped_ack" json:"budget_uncapped_ack"`
}
type ProfileDraft struct {
	Doc       bson.M      `bson:"doc" json:"doc"`
	Meta      ProfileMeta `bson:"meta" json:"meta"`
	UpdatedAt time.Time   `bson:"updated_at" json:"updated_at"`
}
type ValidationRecord struct {
	Host           string               `bson:"host" json:"host"`
	DraftHash      string               `bson:"draft_hash" json:"draft_hash"`
	CatherdVersion string               `bson:"catherd_version" json:"catherd_version"`
	Valid          bool                 `bson:"valid" json:"valid"`
	Errors         []catherd.Issue      `bson:"errors" json:"errors"`
	Warnings       []catherd.Issue      `bson:"warnings" json:"warnings"`
	OrqenIssues    []catherd.Issue      `bson:"orqen_issues" json:"orqen_issues"`
	Independence   catherd.Independence `bson:"independence" json:"independence"`
	CheckedAt      time.Time            `bson:"checked_at" json:"checked_at"`
	OperationID    string               `bson:"operation_id" json:"operation_id"`
}
type ProfileSource struct {
	Kind string  `bson:"kind" json:"kind"`
	Ref  *string `bson:"ref" json:"ref"`
}
type ModelProfileDoc struct {
	ID              string            `bson:"_id" json:"_id"`
	OrgID           string            `bson:"org_id" json:"org_id"`
	Slug            string            `bson:"slug" json:"slug"`
	Name            string            `bson:"name" json:"name"`
	Description     *string           `bson:"description" json:"description"`
	Draft           ProfileDraft      `bson:"draft" json:"draft"`
	LastValidation  *ValidationRecord `bson:"last_validation" json:"last_validation"`
	NextNumber      int64             `bson:"next_number" json:"next_number"`
	LatestVersionID *string           `bson:"latest_version_id" json:"latest_version_id"`
	Source          ProfileSource     `bson:"source" json:"source"`
	Active          bool              `bson:"active" json:"active"`
	Revision        int64             `bson:"revision" json:"revision"`
	CreatedAt       time.Time         `bson:"created_at" json:"created_at"`
	UpdatedAt       time.Time         `bson:"updated_at" json:"updated_at"`
	ArchivedAt      *time.Time        `bson:"archived_at" json:"archived_at"`
}
type VersionFailure struct {
	Code    string          `bson:"code" json:"code"`
	Message string          `bson:"message" json:"message"`
	Issues  []catherd.Issue `bson:"issues" json:"issues"`
}
type VersionSource struct {
	Kind          string  `bson:"kind" json:"kind"` // draft or rollback
	FromVersionID *string `bson:"from_version_id" json:"from_version_id"`
}
type ModelProfileVersionDoc struct {
	ID             string            `bson:"_id" json:"_id"`
	OrgID          string            `bson:"org_id" json:"org_id"`
	ProfileID      string            `bson:"profile_id" json:"profile_id"`
	Number         int64             `bson:"number" json:"number"`
	State          string            `bson:"state" json:"state"`
	Doc            bson.M            `bson:"doc" json:"doc"`
	Meta           ProfileMeta       `bson:"meta" json:"meta"`
	DocHash        string            `bson:"doc_hash" json:"doc_hash"`
	CatherdName    string            `bson:"catherd_name" json:"catherd_name"`
	CatherdVersion string            `bson:"catherd_version" json:"catherd_version"`
	Host           string            `bson:"host" json:"host"`
	Resolved       bson.M            `bson:"resolved" json:"resolved"`
	Validation     *ValidationRecord `bson:"validation" json:"validation"`
	Source         VersionSource     `bson:"source" json:"source"`
	OperationID    string            `bson:"operation_id" json:"operation_id"`
	Failure        *VersionFailure   `bson:"failure" json:"failure"`
	CreatedAt      time.Time         `bson:"created_at" json:"created_at"`
	PublishedAt    *time.Time        `bson:"published_at" json:"published_at"`
	// RepublishRequired is set by the post-restore check when catherd no longer has the profile (see X34).
	RepublishRequired bool `bson:"republish_required" json:"republish_required"`
}

func issuesOrEmpty(in []catherd.Issue) []catherd.Issue {
	if in == nil {
		return []catherd.Issue{}
	}
	return in
}
func independenceDTO(i catherd.Independence) bson.M {
	if i.Shared == nil {
		i.Shared = []string{}
	}
	if i.Unmatched == nil {
		i.Unmatched = []string{}
	}
	return bson.M{"status": i.Status, "shared": i.Shared, "unmatched": i.Unmatched}
}

// hostOrDefault reads a record written before hosts existed as codex.
func (v *ValidationRecord) hostOrDefault() string {
	if v.Host == "" {
		return catherd.HostCodex
	}
	return v.Host
}
func (v *ValidationRecord) dto() any {
	if v == nil {
		return nil
	}
	return bson.M{"host": v.hostOrDefault(), "draft_hash": v.DraftHash, "catherd_version": v.CatherdVersion, "valid": v.Valid, "errors": issuesOrEmpty(v.Errors), "warnings": issuesOrEmpty(v.Warnings), "orqen_issues": issuesOrEmpty(v.OrqenIssues), "independence": independenceDTO(v.Independence), "checked_at": timestamp(v.CheckedAt), "operation_id": v.OperationID}
}

// profileHash identifies a draft: the document and the Orqen-only metadata.
func profileHash(doc bson.M, meta ProfileMeta) string {
	return sha256Hex(canonicalJSON(map[string]any{"doc": doc, "meta": meta}))
}
func docHash(doc map[string]any) string { return sha256Hex(canonicalJSON(doc)) }

// plain converts a stored document to the decoded form catherd's package works with (json.Number numbers).
func plain(m bson.M) map[string]any {
	b, err := json.Marshal(m)
	if err != nil {
		return map[string]any{}
	}
	d, err := catherd.Decode(b)
	if err != nil {
		return map[string]any{}
	}
	return d
}
func stored(m map[string]any) bson.M { return bson.M(catherd.Normalize(m).(map[string]any)) }

func (d ModelProfileDoc) DTO() bson.M {
	doc := d.Draft.Doc
	if doc == nil {
		doc = bson.M{}
	}
	return bson.M{"id": d.ID, "slug": d.Slug, "name": d.Name, "description": d.Description, "draft": bson.M{"doc": doc, "meta": d.Draft.Meta, "updated_at": timestamp(d.Draft.UpdatedAt)}, "draft_hash": profileHash(doc, d.Draft.Meta), "last_validation": d.LastValidation.dto(), "latest_version_id": d.LatestVersionID, "source": d.Source, "revision": d.Revision, "created_at": timestamp(d.CreatedAt), "updated_at": timestamp(d.UpdatedAt)}
}
func (d ModelProfileVersionDoc) hostOrDefault() string {
	if d.Host == "" {
		return catherd.HostCodex
	}
	return d.Host
}
func (d ModelProfileVersionDoc) DTO() bson.M {
	var resolved, failure any
	if d.Resolved != nil {
		resolved = d.Resolved
	}
	if f := d.Failure; f != nil {
		failure = bson.M{"code": f.Code, "message": f.Message, "issues": issuesOrEmpty(f.Issues)}
	}
	doc := d.Doc
	if doc == nil {
		doc = bson.M{}
	}
	var op any
	if d.OperationID != "" {
		op = d.OperationID
	}
	return bson.M{"id": d.ID, "profile_id": d.ProfileID, "number": d.Number, "state": d.State, "doc": doc, "meta": d.Meta, "doc_hash": d.DocHash, "catherd_name": d.CatherdName, "catherd_version": d.CatherdVersion, "host": d.hostOrDefault(), "resolved": resolved, "validation": d.Validation.dto(), "source": d.Source, "operation_id": op, "failure": failure, "republish_required": d.RepublishRequired, "created_at": timestamp(d.CreatedAt), "published_at": optionalTime(d.PublishedAt)}
}

var slugPatternProfile = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,15}$`)

func profFilter(org, id string) bson.M { return bson.M{"_id": id, "org_id": org, "active": true} }
func (a *App) profile(ctx context.Context, org, id string) (ModelProfileDoc, error) {
	return findOne[ModelProfileDoc](ctx, a.DB, "model_profiles", profFilter(org, id))
}
func profileResponse(w http.ResponseWriter, status int, d ModelProfileDoc) {
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, status, d.DTO())
}

// currentCatalog returns the latest catalog snapshot, or nil when none was loaded yet.
func (a *App) currentCatalog(ctx context.Context, org string) (*catherd.Catalog, error) {
	st, err := a.catherdState(ctx, org)
	if err != nil || st == nil || st.Catalog == nil {
		return nil, err
	}
	c, err := a.loadCatalog(ctx, org, st.Catalog.ArtifactID)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// catherdGate refuses profile validation and publishing when catherd is missing or outside the 1.5.x window (D61).
func (a *App) catherdGate() error {
	if a.Catherd == nil || a.CatherdRT == nil || !a.CatherdRT.Available() {
		return problem("catherd_unavailable")
	}
	if !a.CatherdRT.Supported() {
		return problem("catherd_version_unsupported")
	}
	return nil
}

type ProfileFrom struct {
	Kind      string  `json:"kind"`
	VersionID *string `json:"version_id"`
}
type CreateProfileRequest struct {
	Slug        string       `json:"slug"`
	Name        string       `json:"name"`
	Description *string      `json:"description"`
	From        *ProfileFrom `json:"from"`
}
type PatchProfileRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}
type PutDraftRequest struct {
	Doc  map[string]any `json:"doc"`
	Meta *ProfileMeta   `json:"meta"`
}

func checkProfileFields(slug, name string, description *string, checkSlug bool) []FieldError {
	var es []FieldError
	if checkSlug && !slugPatternProfile.MatchString(slug) {
		es = append(es, FieldError{"slug", "1-16 characters: lowercase letters, digits and hyphens, starting with a letter or digit"})
	}
	if !validName(name, 80) {
		es = append(es, FieldError{"name", "must be 1-80 characters"})
	}
	if description != nil && utf8.RuneCountInString(*description) > 2000 {
		es = append(es, FieldError{"description", "must be at most 2000 characters"})
	}
	return es
}
func (a *App) listProfiles(w http.ResponseWriter, r *http.Request) error {
	limit, after, err := pageQuery(r)
	if err != nil {
		return err
	}
	page, err := listPage[ModelProfileDoc](r.Context(), a.DB, "model_profiles", bson.M{"org_id": r.PathValue("orgId"), "active": true}, limit, after)
	if err != nil {
		return err
	}
	writeJSON(w, 200, page)
	return nil
}
func (a *App) createProfile(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, false)
	if err != nil {
		return err
	}
	var req CreateProfileRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	route := "POST /api/v1/orgs/{orgId}/model-profiles"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org}, req), func(ctx context.Context) (Output, error) {
		name := strings.TrimSpace(req.Name)
		if err := validationErrors(checkProfileFields(req.Slug, name, req.Description, true)); err != nil {
			return Output{}, err
		}
		doc, meta, src := bson.M(nil), ProfileMeta{}, ProfileSource{Kind: "blank"}
		switch {
		case req.From == nil || req.From.Kind == "blank":
			doc = stored(catherd.BlankDoc())
		case req.From.Kind == "version" && req.From.VersionID != nil:
			v, err := findOne[ModelProfileVersionDoc](ctx, a.DB, "model_profile_versions", bson.M{"_id": *req.From.VersionID, "org_id": org})
			if errors.Is(err, mongo.ErrNoDocuments) || err == nil && v.State != "published" {
				return Output{}, &AppError{Code: "version_not_published", Status: 422, Detail: "the profile version is not published", Errors: []FieldError{{"from.version_id", "must name a published version of this organization"}}}
			}
			if err != nil {
				return Output{}, err
			}
			doc, meta, src = v.Doc, v.Meta, ProfileSource{Kind: "clone", Ref: &v.ID}
		default:
			return Output{}, validation("from", "must be {kind:blank} or {kind:version, version_id}")
		}
		if n, err := a.DB.Coll("model_profile_versions").CountDocuments(ctx, bson.M{"org_id": org, "catherd_name": bson.Regex{Pattern: "^o-" + regexp.QuoteMeta(req.Slug) + "-v[0-9]+$"}}); err != nil {
			return Output{}, err
		} else if n > 0 {
			return Output{}, validation("slug", "already used by an archived profile")
		}
		now := time.Now().UTC()
		d := ModelProfileDoc{ID: newID("mpf"), OrgID: org, Slug: req.Slug, Name: name, Description: trimmedPtr(req.Description), Draft: ProfileDraft{Doc: doc, Meta: meta, UpdatedAt: now}, NextNumber: 1, Source: src, Active: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
		if _, err := a.DB.Coll("model_profiles").InsertOne(ctx, d); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return Output{}, validation("slug", "already used")
			}
			return Output{}, err
		}
		if err := a.AppendEvent(ctx, org, "model_profile.created", Subject{"model_profile", d.ID}, bson.M{"slug": d.Slug, "name": d.Name, "source": src.Kind}, &d.Revision, nil, false); err != nil {
			return Output{}, err
		}
		return Output{201, d.DTO(), map[string]string{"etag": etag(d.Revision), "location": "/api/v1/orgs/" + org + "/model-profiles/" + d.ID}}, nil
	})
}
func trimmedPtr(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}
func (a *App) getProfile(w http.ResponseWriter, r *http.Request) error {
	d, err := a.profile(r.Context(), r.PathValue("orgId"), r.PathValue("mpfId"))
	if err != nil {
		return err
	}
	profileResponse(w, 200, d)
	return nil
}
func (a *App) patchProfile(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req PatchProfileRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("mpfId")
	set := bson.M{}
	name := ""
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		set["name"] = name
	} else {
		name = "x"
	}
	if err = validationErrors(checkProfileFields("", name, req.Description, false)); err != nil {
		return err
	}
	if req.Description != nil {
		if d := trimmedPtr(req.Description); d != nil {
			set["description"] = *d
		} else {
			set["description"] = nil
		}
	}
	if len(set) == 0 {
		return validation("body", "no changes given")
	}
	var doc ModelProfileDoc
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		var e error
		if doc, e = revisionUpdate[ModelProfileDoc](ctx, a.DB, "model_profiles", profFilter(org, id), expected, set); e != nil {
			return e
		}
		return a.AppendEvent(ctx, org, "model_profile.updated", Subject{"model_profile", id}, bson.M{"name": doc.Name}, &doc.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.Committed()
	profileResponse(w, 200, doc)
	return nil
}
func (a *App) archiveProfile(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("mpfId")
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		if _, e := a.profile(ctx, org, id); e != nil {
			return e
		}
		if n, e := a.DB.Coll("projects").CountDocuments(ctx, bson.M{"org_id": org, "active": true, "profile_binding.profile_id": id}); e != nil {
			return e
		} else if n > 0 {
			return problem("profile_in_use")
		}
		doc, e := revisionUpdate[ModelProfileDoc](ctx, a.DB, "model_profiles", profFilter(org, id), expected, bson.M{"active": false, "archived_at": time.Now().UTC()})
		if e != nil {
			return e
		}
		return a.AppendEvent(ctx, org, "model_profile.archived", Subject{"model_profile", id}, bson.M{"name": doc.Name}, &doc.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.Committed()
	w.WriteHeader(204)
	return nil
}
func (a *App) putProfileDraft(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req PutDraftRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if req.Doc == nil {
		return validation("doc", "is required")
	}
	if b, _ := json.Marshal(req.Doc); len(b) > 64<<10 {
		return validation("doc", "must be at most 64 KiB")
	}
	delete(req.Doc, "name")
	meta := ProfileMeta{}
	if req.Meta != nil {
		meta = *req.Meta
	}
	org, id := r.PathValue("orgId"), r.PathValue("mpfId")
	var doc ModelProfileDoc
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		now := time.Now().UTC()
		var e error
		if doc, e = revisionUpdate[ModelProfileDoc](ctx, a.DB, "model_profiles", profFilter(org, id), expected, bson.M{"draft": ProfileDraft{Doc: bson.M(catherd.Normalize(req.Doc).(map[string]any)), Meta: meta, UpdatedAt: now}}); e != nil {
			return e
		}
		return a.AppendEvent(ctx, org, "model_profile.draft_updated", Subject{"model_profile", id}, bson.M{"draft_hash": profileHash(doc.Draft.Doc, doc.Draft.Meta)}, &doc.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.Committed()
	profileResponse(w, 200, doc)
	return nil
}
func (a *App) getDraftIssues(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	d, err := a.profile(r.Context(), org, r.PathValue("mpfId"))
	if err != nil {
		return err
	}
	cat, err := a.currentCatalog(r.Context(), org)
	if err != nil {
		return err
	}
	an := catherd.Analyze(plain(d.Draft.Doc), cat, catherd.Meta{BudgetUncappedAck: d.Draft.Meta.BudgetUncappedAck})
	issues := append(append([]catherd.Issue{}, an.Errors...), an.Warnings...)
	writeJSON(w, 200, bson.M{"issues": issuesOrEmpty(issues), "independence": independenceDTO(an.Independence), "draft_hash": profileHash(d.Draft.Doc, d.Draft.Meta)})
	return nil
}
func (a *App) getProfileDiff(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	d, err := a.profile(r.Context(), org, r.PathValue("mpfId"))
	if err != nil {
		return err
	}
	resolve := func(ref, field string) (map[string]any, error) {
		switch {
		case ref == "draft":
			return plain(d.Draft.Doc), nil
		case ref == "latest":
			if d.LatestVersionID == nil {
				return map[string]any{}, nil
			}
			ref = *d.LatestVersionID
			fallthrough
		case validID("mpv", ref):
			v, err := findOne[ModelProfileVersionDoc](r.Context(), a.DB, "model_profile_versions", bson.M{"_id": ref, "org_id": org, "profile_id": d.ID})
			if err != nil {
				if errors.Is(err, mongo.ErrNoDocuments) {
					return nil, validation(field, "unknown version")
				}
				return nil, err
			}
			return plain(v.Doc), nil
		}
		return nil, validation(field, "must be draft, latest or a version id")
	}
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	if from == "" {
		from = "latest"
	}
	if to == "" {
		to = "draft"
	}
	a1, err := resolve(from, "from")
	if err != nil {
		return err
	}
	b1, err := resolve(to, "to")
	if err != nil {
		return err
	}
	changes := []bson.M{}
	for _, c := range catherd.Diff(a1, b1) {
		changes = append(changes, bson.M{"path": c.Path, "path_display": c.PathDisplay, "kind": c.Kind, "before": jsonValue(c.Before), "after": jsonValue(c.After)})
	}
	writeJSON(w, 200, bson.M{"from": from, "to": to, "changes": changes})
	return nil
}

// jsonValue makes a decoded value safe for bson.M encoding in a response.
func jsonValue(v any) any {
	if v == nil {
		return nil
	}
	return catherd.Normalize(v)
}
func (a *App) listProfileVersions(w http.ResponseWriter, r *http.Request) error {
	org, id := r.PathValue("orgId"), r.PathValue("mpfId")
	if _, err := a.profile(r.Context(), org, id); err != nil {
		return err
	}
	limit, after, err := pageQuery(r)
	if err != nil {
		return err
	}
	filter := bson.M{"org_id": org, "profile_id": id}
	if after != "" {
		n, err := strconv.ParseInt(after, 10, 64)
		if err != nil {
			return validation("cursor", "invalid cursor")
		}
		filter["number"] = bson.M{"$lt": n}
	}
	docs, err := findMany[ModelProfileVersionDoc](r.Context(), a.DB, "model_profile_versions", filter, bson.D{{Key: "number", Value: -1}}, limit+1)
	if err != nil {
		return err
	}
	var next any
	if len(docs) > int(limit) {
		docs = docs[:limit]
		next = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprint(docs[len(docs)-1].Number)))
	}
	items := []bson.M{}
	for _, d := range docs {
		items = append(items, d.DTO())
	}
	writeJSON(w, 200, bson.M{"items": items, "next_cursor": next})
	return nil
}
func (a *App) getProfileVersion(w http.ResponseWriter, r *http.Request) error {
	d, err := findOne[ModelProfileVersionDoc](r.Context(), a.DB, "model_profile_versions", bson.M{"_id": r.PathValue("mpvId"), "org_id": r.PathValue("orgId")})
	if err != nil {
		return err
	}
	writeJSON(w, 200, d.DTO())
	return nil
}
