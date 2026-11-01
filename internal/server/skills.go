package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"orqen/internal/skills"
)

// SkillDoc is one skill of an organization (collection skills).
type SkillDoc struct {
	ID                string    `bson:"_id"`
	OrgID             string    `bson:"org_id"`
	Slug              string    `bson:"slug"`
	Origin            string    `bson:"origin"` // builtin | imported
	Title             string    `bson:"title"`
	CurrentVersionID  string    `bson:"current_version_id"`
	ApprovedVersionID *string   `bson:"approved_version_id"`
	NextNumber        int       `bson:"next_number"`
	Active            bool      `bson:"active"`
	Revision          int64     `bson:"revision"`
	CreatedAt         time.Time `bson:"created_at"`
	UpdatedAt         time.Time `bson:"updated_at"`
}

// SkillLint is the stored lint result of a version.
type SkillLint struct {
	Flags    []skills.Flag  `bson:"flags"`
	Warnings []skills.Issue `bson:"warnings"`
}

// SkillVersionDoc is one immutable version of a skill (collection skill_versions).
type SkillVersionDoc struct {
	ID            string             `bson:"_id"`
	OrgID         string             `bson:"org_id"`
	SkillID       string             `bson:"skill_id"`
	Number        int                `bson:"number"`
	Semver        string             `bson:"semver"`
	Frontmatter   skills.Frontmatter `bson:"frontmatter"`
	Body          string             `bson:"body"`
	ContentSHA256 string             `bson:"content_sha256"`
	Lint          SkillLint          `bson:"lint"`
	State         string             `bson:"state"` // draft | approved | retired
	AckedFlags    []string           `bson:"acknowledged_flags"`
	ApprovedAt    *time.Time         `bson:"approved_at"`
	CreatedAt     time.Time          `bson:"created_at"`
}

// SkillBindingSetDoc holds the bindings of one scope (collection skill_binding_sets).
type SkillBindingSetDoc struct {
	ID        string           `bson:"_id"`
	OrgID     string           `bson:"org_id"`
	ScopeKey  string           `bson:"scope_key"` // org | project:<prj>
	Bindings  []skills.Binding `bson:"bindings"`
	Revision  int64            `bson:"revision"`
	UpdatedAt time.Time        `bson:"updated_at"`
}

// PinnedSkill is one skill version a run pinned.
type PinnedSkill struct {
	SkillID   string `bson:"skill_id" json:"skill_id"`
	VersionID string `bson:"version_id" json:"version_id"`
	Slug      string `bson:"slug" json:"slug"`
	Semver    string `bson:"semver" json:"semver"`
	SHA256    string `bson:"sha256" json:"sha256"`
}

// PinnedSkillSet is the resolution for one role at run creation: round 0, and every fix round.
type PinnedSkillSet struct {
	Round0      []PinnedSkill     `bson:"round0" json:"round0"`
	Fix         []PinnedSkill     `bson:"fix" json:"fix"`
	Excluded0   []skills.Excluded `bson:"excluded0" json:"excluded0"`
	ExcludedFix []skills.Excluded `bson:"excluded_fix" json:"excluded_fix"`
}

func (a *App) skillRoutes() []Route {
	const p = "/api/v1/orgs/{orgId}"
	return []Route{
		{"GET", p + "/skills", a.listSkills}, {"POST", p + "/skills/import", a.importSkill}, {"GET", p + "/skills/{sklId}", a.getSkill},
		{"GET", p + "/skill-versions/{skvId}", a.getSkillVersion}, {"POST", p + "/skill-versions/{skvId}/approve", a.approveSkillVersion}, {"POST", p + "/skill-versions/{skvId}/retire", a.retireSkillVersion},
		{"GET", p + "/skill-bindings", a.getSkillBindings}, {"PUT", p + "/skill-bindings", a.putSkillBindings},
		{"POST", p + "/skill-bindings/recommended", a.bindRecommendedSkills}, {"GET", p + "/skill-bindings/resolve", a.resolveSkillBindings},
		{"GET", p + "/runs/{runId}/skills", a.getRunSkills},
	}
}

func init() {
	idemRequired["POST /api/v1/orgs/{orgId}/skills/import"] = true
	idemRequired["POST /api/v1/orgs/{orgId}/skill-bindings/recommended"] = true
}

// ---- store

func (a *App) skill(ctx context.Context, org, id string) (SkillDoc, error) {
	return findOne[SkillDoc](ctx, a.DB, "skills", bson.M{"_id": id, "org_id": org, "active": true})
}

func (a *App) skillVersion(ctx context.Context, org, id string) (SkillVersionDoc, error) {
	return findOne[SkillVersionDoc](ctx, a.DB, "skill_versions", bson.M{"_id": id, "org_id": org})
}

func newSkillVersion(org, skillID string, number int, f skills.File, content string, flags []skills.Flag, warnings []skills.Issue, state string) SkillVersionDoc {
	now := time.Now().UTC()
	if flags == nil {
		flags = []skills.Flag{}
	}
	if warnings == nil {
		warnings = []skills.Issue{}
	}
	v := SkillVersionDoc{ID: newID("skv"), OrgID: org, SkillID: skillID, Number: number, Semver: f.Version, Frontmatter: f.Frontmatter, Body: f.Body, ContentSHA256: skills.ContentSHA(content),
		Lint: SkillLint{Flags: flags, Warnings: warnings}, State: state, AckedFlags: []string{}, CreatedAt: now}
	if state == "approved" {
		v.ApprovedAt = &now
	}
	return v
}

// seedBuiltins inserts the embedded skills lazily. A newer embedded text adds a new approved version; bindings keep
// their version, so nothing changes under a pinned run.
func (a *App) seedBuiltins(ctx context.Context, org string) error {
	for _, b := range skills.Builtins() {
		cur, err := findOne[SkillDoc](ctx, a.DB, "skills", bson.M{"org_id": org, "slug": b.Slug})
		if err == nil {
			vs, e := findMany[SkillVersionDoc](ctx, a.DB, "skill_versions", bson.M{"org_id": org, "skill_id": cur.ID}, bson.D{{Key: "number", Value: -1}}, 1)
			if e != nil || len(vs) == 0 || vs[0].ContentSHA256 == b.SHA256 || cur.Origin != "builtin" {
				continue
			}
			err = a.DB.Txn(ctx, func(sc context.Context) error {
				now := time.Now().UTC()
				var s SkillDoc
				if e := a.DB.Coll("skills").FindOneAndUpdate(sc, bson.M{"_id": cur.ID}, bson.M{"$inc": bson.M{"next_number": 1, "revision": int64(1)}, "$set": bson.M{"updated_at": now}}, afterUpdate()).Decode(&s); e != nil {
					return e
				}
				v := newSkillVersion(org, cur.ID, s.NextNumber-1, b.File, b.Content, nil, nil, "approved")
				if _, e := a.DB.Coll("skill_versions").InsertOne(sc, v); e != nil {
					return e
				}
				_, e := a.DB.Coll("skills").UpdateOne(sc, bson.M{"_id": cur.ID}, bson.M{"$set": bson.M{"current_version_id": v.ID, "approved_version_id": v.ID}})
				return e
			})
			if err != nil {
				return err
			}
			continue
		}
		if !errors.Is(err, mongo.ErrNoDocuments) {
			return err
		}
		err = a.DB.Txn(ctx, func(sc context.Context) error {
			now := time.Now().UTC()
			s := SkillDoc{ID: newID("skl"), OrgID: org, Slug: b.Slug, Origin: "builtin", Title: b.Slug, NextNumber: 2, Active: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
			v := newSkillVersion(org, s.ID, 1, b.File, b.Content, nil, nil, "approved")
			s.CurrentVersionID, s.ApprovedVersionID = v.ID, &v.ID
			if _, e := a.DB.Coll("skills").InsertOne(sc, s); e != nil {
				return e
			}
			_, e := a.DB.Coll("skill_versions").InsertOne(sc, v)
			return e
		})
		if err != nil && !mongo.IsDuplicateKeyError(err) {
			return err
		}
	}
	return nil
}

// ---- DTOs

func skillVersionDTO(v SkillVersionDoc) bson.M {
	flags := []bson.M{}
	for _, f := range v.Lint.Flags {
		flags = append(flags, bson.M{"code": f.Code, "line": f.Line, "blocking": f.Blocking})
	}
	warns := []bson.M{}
	for _, w := range v.Lint.Warnings {
		warns = append(warns, bson.M{"code": w.Code, "line": w.Line})
	}
	return bson.M{"id": v.ID, "skill_id": v.SkillID, "number": v.Number, "semver": v.Semver, "name": v.Frontmatter.Name, "description": v.Frontmatter.Description,
		"roles": nonNilStr(v.Frontmatter.Roles), "when": v.Frontmatter.When, "license": v.Frontmatter.License, "source": v.Frontmatter.Source, "body": v.Body, "content_sha256": v.ContentSHA256,
		"lint": bson.M{"flags": flags, "warnings": warns}, "state": v.State, "acknowledged_flags": nonNilStr(v.AckedFlags), "approved_at": optionalTime(v.ApprovedAt), "created_at": timestamp(v.CreatedAt)}
}

func (a *App) skillDTO(ctx context.Context, s SkillDoc) (bson.M, error) {
	out := bson.M{"id": s.ID, "slug": s.Slug, "origin": s.Origin, "title": s.Title, "current_version_id": s.CurrentVersionID, "approved_version_id": optStr(ptrStr(s.ApprovedVersionID)),
		"revision": s.Revision, "updated_at": timestamp(s.UpdatedAt), "description": "", "roles": []string{}, "when": "always", "semver": optStr("")}
	id := s.CurrentVersionID
	if s.ApprovedVersionID != nil {
		id = *s.ApprovedVersionID
	}
	v, err := a.skillVersion(ctx, s.OrgID, id)
	if err != nil {
		return nil, err
	}
	out["description"], out["roles"], out["when"], out["semver"], out["title"] = v.Frontmatter.Description, nonNilStr(v.Frontmatter.Roles), v.Frontmatter.When, optStr(v.Semver), v.Frontmatter.Name
	return out, nil
}

func (a *App) skillDetail(ctx context.Context, s SkillDoc) (bson.M, error) {
	head, err := a.skillDTO(ctx, s)
	if err != nil {
		return nil, err
	}
	vs, err := findMany[SkillVersionDoc](ctx, a.DB, "skill_versions", bson.M{"org_id": s.OrgID, "skill_id": s.ID}, bson.D{{Key: "number", Value: -1}}, 200)
	if err != nil {
		return nil, err
	}
	dtos := []bson.M{}
	for _, v := range vs {
		dtos = append(dtos, skillVersionDTO(v))
	}
	update := false
	if set, e := a.bindingSet(ctx, s.OrgID, "org"); e == nil && s.ApprovedVersionID != nil {
		for _, b := range set.Bindings {
			if b.SkillID == s.ID && b.VersionID != *s.ApprovedVersionID {
				update = true
			}
		}
	}
	return bson.M{"skill": head, "versions": dtos, "update_available": update}, nil
}

func (a *App) respondSkill(w http.ResponseWriter, ctx context.Context, s SkillDoc, status int) error {
	dto, err := a.skillDetail(ctx, s)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(s.Revision))
	writeJSON(w, status, dto)
	return nil
}

// ---- routes

func (a *App) listSkills(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	if err := a.seedBuiltins(r.Context(), org); err != nil {
		return err
	}
	list, err := findMany[SkillDoc](r.Context(), a.DB, "skills", bson.M{"org_id": org, "active": true}, bson.D{{Key: "slug", Value: 1}}, 500)
	if err != nil {
		return err
	}
	items := []bson.M{}
	for _, s := range list {
		d, e := a.skillDTO(r.Context(), s)
		if e != nil {
			return e
		}
		items = append(items, d)
	}
	writeJSON(w, 200, bson.M{"items": items})
	return nil
}

func (a *App) getSkill(w http.ResponseWriter, r *http.Request) error {
	s, err := a.skill(r.Context(), r.PathValue("orgId"), r.PathValue("sklId"))
	if err != nil {
		return err
	}
	return a.respondSkill(w, r.Context(), s, 200)
}

func (a *App) getSkillVersion(w http.ResponseWriter, r *http.Request) error {
	v, err := a.skillVersion(r.Context(), r.PathValue("orgId"), r.PathValue("skvId"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, skillVersionDTO(v))
	return nil
}

func skillInvalid(code string, line int) *AppError {
	e := problem("skill_invalid")
	e.Errors = []FieldError{{"content", code + ":" + strconv.Itoa(line)}}
	return e
}

// importSkill takes a file the person uploaded or pasted. Nothing is fetched; the version is a draft and is never
// rendered anywhere until it is approved.
func (a *App) importSkill(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	var req struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	route := "POST /api/v1/orgs/{orgId}/skills/import"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org}, req), func(ctx context.Context) (Output, error) {
		f, issues := skills.Parse(req.Filename, req.Content)
		var warns []skills.Issue
		for _, is := range issues {
			if is.Warning {
				warns = append(warns, is)
				continue
			}
			if is.Code == skills.IssueTooLarge {
				return Output{}, problem("skill_too_large")
			}
			return Output{}, skillInvalid(is.Code, is.Line)
		}
		for _, b := range skills.Builtins() {
			if b.Slug == f.Name {
				return Output{}, problem("skill_slug_builtin")
			}
		}
		flags := skills.Lint(req.Content)
		var out SkillVersionDoc
		err := a.DB.Txn(ctx, func(sc context.Context) error {
			now := time.Now().UTC()
			s, e := findOne[SkillDoc](sc, a.DB, "skills", bson.M{"org_id": org, "slug": f.Name})
			if errors.Is(e, mongo.ErrNoDocuments) {
				s = SkillDoc{ID: newID("skl"), OrgID: org, Slug: f.Name, Origin: "imported", Title: f.Name, NextNumber: 2, Active: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
				out = newSkillVersion(org, s.ID, 1, f, req.Content, flags, warns, "draft")
				s.CurrentVersionID = out.ID
				if _, e = a.DB.Coll("skills").InsertOne(sc, s); e != nil {
					return e
				}
			} else if e != nil {
				return e
			} else {
				if n, e := a.DB.Coll("skill_versions").CountDocuments(sc, bson.M{"org_id": org, "skill_id": s.ID, "state": "draft"}); e != nil {
					return e
				} else if n > 0 {
					return problem("skill_draft_exists")
				}
				var cur SkillDoc
				if e = a.DB.Coll("skills").FindOneAndUpdate(sc, bson.M{"_id": s.ID}, bson.M{"$inc": bson.M{"next_number": 1, "revision": int64(1)}, "$set": bson.M{"updated_at": now, "active": true}}, afterUpdate()).Decode(&cur); e != nil {
					return e
				}
				out = newSkillVersion(org, s.ID, cur.NextNumber-1, f, req.Content, flags, warns, "draft")
			}
			if _, e = a.DB.Coll("skill_versions").InsertOne(sc, out); e != nil {
				if mongo.IsDuplicateKeyError(e) {
					return problem("skill_draft_exists")
				}
				return e
			}
			return a.AppendEvent(sc, org, "skill.imported", Subject{"skill", out.SkillID}, bson.M{"slug": f.Name, "version_id": out.ID, "flags": len(flags)}, nil, nil, false)
		})
		if err != nil {
			return Output{}, err
		}
		return Output{201, skillVersionDTO(out), nil}, nil
	})
}

// skillMutate moves one version and the skill revision together under If-Match.
func (a *App) skillMutate(w http.ResponseWriter, r *http.Request, event string, f func(sc context.Context, s *SkillDoc, v *SkillVersionDoc) error) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	org := r.PathValue("orgId")
	v0, err := a.skillVersion(r.Context(), org, r.PathValue("skvId"))
	if err != nil {
		return err
	}
	var out SkillDoc
	err = a.DB.Txn(r.Context(), func(sc context.Context) error {
		s, e := a.skill(sc, org, v0.SkillID)
		if e != nil {
			return e
		}
		if s.Revision != expected {
			c := problem("revision_conflict")
			return c
		}
		v, e := a.skillVersion(sc, org, v0.ID)
		if e != nil {
			return e
		}
		if e = f(sc, &s, &v); e != nil {
			return e
		}
		s.UpdatedAt = time.Now().UTC()
		res, e := a.DB.Coll("skills").ReplaceOne(sc, bson.M{"_id": s.ID, "revision": expected}, withRevision(s, expected+1))
		if e != nil {
			return e
		}
		if res.MatchedCount == 0 {
			return problem("revision_conflict")
		}
		if _, e = a.DB.Coll("skill_versions").ReplaceOne(sc, bson.M{"_id": v.ID}, v); e != nil {
			return e
		}
		s.Revision = expected + 1
		out = s
		return a.AppendEvent(sc, org, event, Subject{"skill", s.ID}, bson.M{"slug": s.Slug, "version_id": v.ID}, &s.Revision, nil, false)
	})
	if err != nil {
		var ae *AppError
		if errors.As(err, &ae) && ae.Code == "revision_conflict" {
			if cur, e := a.skill(r.Context(), org, v0.SkillID); e == nil {
				if dto, e2 := a.skillDetail(r.Context(), cur); e2 == nil {
					ae.Current = dto
				}
			}
		}
		return err
	}
	a.Committed()
	return a.respondSkill(w, r.Context(), out, 200)
}

func withRevision(s SkillDoc, rev int64) SkillDoc { s.Revision = rev; return s }

func (a *App) approveSkillVersion(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Acknowledged []string `json:"acknowledged_flags"`
	}
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	return a.skillMutate(w, r, "skill.approved", func(sc context.Context, s *SkillDoc, v *SkillVersionDoc) error {
		if v.State != "draft" {
			return problem("skill_version_not_draft")
		}
		outcome, missing := skills.CheckApproval(v.Lint.Flags, req.Acknowledged)
		switch outcome {
		case skills.ApprovalBlocked:
			return problem("skill_lint_blocked")
		case skills.ApprovalUnacknowledged:
			e := problem("skill_flags_unacknowledged")
			for _, c := range missing {
				e.Errors = append(e.Errors, FieldError{"acknowledged_flags", c})
			}
			return e
		}
		now := time.Now().UTC()
		v.State, v.ApprovedAt, v.AckedFlags = "approved", &now, nonNilStr(req.Acknowledged)
		s.ApprovedVersionID, s.CurrentVersionID = &v.ID, v.ID
		return nil
	})
}

func (a *App) retireSkillVersion(w http.ResponseWriter, r *http.Request) error {
	return a.skillMutate(w, r, "skill.retired", func(sc context.Context, s *SkillDoc, v *SkillVersionDoc) error {
		if v.State == "retired" {
			return problem("skill_version_not_draft")
		}
		v.State = "retired"
		if s.ApprovedVersionID != nil && *s.ApprovedVersionID == v.ID {
			s.ApprovedVersionID = nil
			others, e := findMany[SkillVersionDoc](sc, a.DB, "skill_versions", bson.M{"org_id": s.OrgID, "skill_id": s.ID, "state": "approved", "_id": bson.M{"$ne": v.ID}}, bson.D{{Key: "number", Value: -1}}, 1)
			if e != nil {
				return e
			}
			if len(others) > 0 {
				s.ApprovedVersionID = &others[0].ID
			}
		}
		return nil
	})
}

// ---- bindings

func bindingSetID(org, scope string) string { return "sbs_" + sha256Hex(org + "|" + scope)[:26] }

func (a *App) bindingSet(ctx context.Context, org, scope string) (SkillBindingSetDoc, error) {
	d, err := findOne[SkillBindingSetDoc](ctx, a.DB, "skill_binding_sets", bson.M{"org_id": org, "scope_key": scope})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return SkillBindingSetDoc{OrgID: org, ScopeKey: scope, Bindings: []skills.Binding{}}, nil
	}
	return d, err
}

func bindingSetDTO(d SkillBindingSetDoc) bson.M {
	bs := []bson.M{}
	for _, b := range d.Bindings {
		bs = append(bs, bson.M{"role": b.Role, "skill_id": b.SkillID, "version_id": b.VersionID, "enabled": b.Enabled, "position": b.Position})
	}
	return bson.M{"scope_key": d.ScopeKey, "bindings": bs, "revision": d.Revision}
}

// checkScope validates scope_key: org, or project:<id> of an active project of this org.
func (a *App) checkScope(ctx context.Context, org, scope string) error {
	if scope == "org" {
		return nil
	}
	if id, ok := strings.CutPrefix(scope, "project:"); ok && validID("prj", id) {
		if _, err := a.project(ctx, org, id); err == nil {
			return nil
		}
	}
	return validation("scope_key", "must be org or project:<id> of a project in this organization")
}

func (a *App) getSkillBindings(w http.ResponseWriter, r *http.Request) error {
	scope := r.URL.Query().Get("scope_key")
	if scope == "" {
		scope = "org"
	}
	org := r.PathValue("orgId")
	if err := a.checkScope(r.Context(), org, scope); err != nil {
		return err
	}
	d, err := a.bindingSet(r.Context(), org, scope)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, 200, bindingSetDTO(d))
	return nil
}

type skillBindingInput struct {
	Role      string `json:"role"`
	SkillID   string `json:"skill_id"`
	VersionID string `json:"version_id"`
	Enabled   bool   `json:"enabled"`
	Position  int    `json:"position"`
}

func bindingInvalid(index int, code string) *AppError {
	e := problem("skill_binding_invalid")
	e.Errors = []FieldError{{"bindings[" + strconv.Itoa(index) + "]", code}}
	return e
}

// saveBindings validates and stores a binding set under a revision (0 = not saved yet).
func (a *App) saveBindings(ctx context.Context, org, scope string, in []skills.Binding, expected int64) (SkillBindingSetDoc, error) {
	seen := map[string]bool{}
	for i, b := range in {
		v, err := a.skillVersion(ctx, org, b.VersionID)
		switch {
		case errors.Is(err, mongo.ErrNoDocuments) || err == nil && v.SkillID != b.SkillID:
			return SkillBindingSetDoc{}, bindingInvalid(i, "unknown_version")
		case err != nil:
			return SkillBindingSetDoc{}, err
		case v.State != "approved":
			return SkillBindingSetDoc{}, bindingInvalid(i, "version_not_approved")
		case !contains(v.Frontmatter.Roles, b.Role):
			return SkillBindingSetDoc{}, bindingInvalid(i, "role_not_in_skill")
		case seen[b.Role+"|"+b.SkillID]:
			return SkillBindingSetDoc{}, bindingInvalid(i, "duplicate")
		}
		seen[b.Role+"|"+b.SkillID] = true
	}
	var out SkillBindingSetDoc
	err := a.DB.Txn(ctx, func(sc context.Context) error {
		cur, err := a.bindingSet(sc, org, scope)
		if err != nil {
			return err
		}
		if cur.Revision != expected {
			c := problem("revision_conflict")
			c.Current = bindingSetDTO(cur)
			return c
		}
		next := cur
		next.Bindings, next.UpdatedAt, next.Revision = in, time.Now().UTC(), expected+1
		if next.Bindings == nil {
			next.Bindings = []skills.Binding{}
		}
		if expected == 0 {
			next.ID, next.OrgID, next.ScopeKey = newID("sbs"), org, scope
			if _, err = a.DB.Coll("skill_binding_sets").InsertOne(sc, next); err != nil {
				if mongo.IsDuplicateKeyError(err) {
					return problem("revision_conflict")
				}
				return err
			}
		} else {
			res, err := a.DB.Coll("skill_binding_sets").ReplaceOne(sc, bson.M{"_id": cur.ID, "revision": expected}, next)
			if err != nil {
				return err
			}
			if res.MatchedCount == 0 {
				return problem("revision_conflict")
			}
		}
		out = next
		return a.AppendEvent(sc, org, "skills.bindings_changed", Subject{"skill_bindings", scope}, bson.M{"scope_key": scope, "count": len(in)}, &out.Revision, nil, false)
	})
	if err == nil {
		a.Committed()
	}
	return out, err
}

func (a *App) putSkillBindings(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	var req struct {
		ScopeKey string              `json:"scope_key"`
		Bindings []skillBindingInput `json:"bindings"`
	}
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	if err = a.checkScope(r.Context(), org, req.ScopeKey); err != nil {
		return err
	}
	in := make([]skills.Binding, 0, len(req.Bindings))
	for _, b := range req.Bindings {
		in = append(in, skills.Binding{Role: b.Role, SkillID: b.SkillID, VersionID: b.VersionID, Enabled: b.Enabled, Position: b.Position})
	}
	d, err := a.saveBindings(r.Context(), org, req.ScopeKey, in, expected)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, 200, bindingSetDTO(d))
	return nil
}

func (a *App) bindRecommendedSkills(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org := r.PathValue("orgId")
	if err = a.seedBuiltins(r.Context(), org); err != nil {
		return err
	}
	route := "POST /api/v1/orgs/{orgId}/skill-bindings/recommended"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org}, nil), func(ctx context.Context) (Output, error) {
		cur, err := a.bindingSet(ctx, org, "org")
		if err != nil {
			return Output{}, err
		}
		if len(cur.Bindings) > 0 {
			return Output{}, problem("skill_bindings_not_empty")
		}
		var in []skills.Binding
		for _, b := range skills.RecommendedSet() {
			s, e := findOne[SkillDoc](ctx, a.DB, "skills", bson.M{"org_id": org, "slug": b.SkillID})
			if e != nil || s.ApprovedVersionID == nil {
				return Output{}, e
			}
			b.SkillID, b.VersionID = s.ID, *s.ApprovedVersionID
			in = append(in, b)
		}
		d, err := a.saveBindings(ctx, org, "org", in, cur.Revision)
		if err != nil {
			return Output{}, err
		}
		return Output{200, bindingSetDTO(d), map[string]string{"ETag": etag(d.Revision)}}, nil
	})
}

// ---- resolution

// skillVersionInfos loads the versions a binding list refers to, joined with their slugs.
func (a *App) skillVersionInfos(ctx context.Context, org string, sets ...[]skills.Binding) (map[string]skills.VersionInfo, error) {
	ids := map[string]bool{}
	for _, set := range sets {
		for _, b := range set {
			ids[b.VersionID] = true
		}
	}
	out := map[string]skills.VersionInfo{}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	sort.Strings(list)
	if len(list) == 0 {
		return out, nil
	}
	vs, err := findMany[SkillVersionDoc](ctx, a.DB, "skill_versions", bson.M{"org_id": org, "_id": bson.M{"$in": list}}, bson.D{{Key: "_id", Value: 1}}, int64(len(list)))
	if err != nil {
		return nil, err
	}
	for _, v := range vs {
		out[v.ID] = skills.VersionInfo{SkillID: v.SkillID, VersionID: v.ID, Slug: v.Frontmatter.Name, Semver: v.Semver, SHA256: v.ContentSHA256, Body: v.Body,
			Roles: v.Frontmatter.Roles, When: v.Frontmatter.When, Approved: v.State == "approved"}
	}
	return out, nil
}

func (a *App) resolveSkillBindings(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	q := r.URL.Query()
	role := q.Get("role")
	if !contains(skills.Roles, role) {
		return validation("role", "unknown role")
	}
	round, _ := strconv.Atoi(q.Get("round"))
	project := q.Get("project_id")
	var projSet SkillBindingSetDoc
	if project != "" {
		if err := a.checkScope(r.Context(), org, "project:"+project); err != nil {
			return err
		}
		var err error
		if projSet, err = a.bindingSet(r.Context(), org, "project:"+project); err != nil {
			return err
		}
	}
	orgSet, err := a.bindingSet(r.Context(), org, "org")
	if err != nil {
		return err
	}
	eff, _, _, err := a.effectivePolicy(r.Context(), org, project)
	if err != nil {
		return err
	}
	budget := 6144
	for _, e := range eff.Entries {
		if e.Key == "skills.max_bytes_per_role" {
			budget = policyValues{e.Key: e.Value}.Int(e.Key)
		}
	}
	versions, err := a.skillVersionInfos(r.Context(), org, orgSet.Bindings, projSet.Bindings)
	if err != nil {
		return err
	}
	m := skills.Resolve(skills.Input{Org: orgSet.Bindings, Project: projSet.Bindings, Role: role, Round: round, Versions: versions, Budget: budget})
	writeJSON(w, 200, manifestDTO6(m))
	return nil
}

func manifestDTO6(m skills.Manifest) bson.M {
	inc, exc := []bson.M{}, []bson.M{}
	for _, i := range m.Included {
		inc = append(inc, bson.M{"skill_id": i.SkillID, "version_id": i.VersionID, "slug": i.Slug, "semver": i.Semver, "sha256": i.SHA256, "bytes": i.Bytes})
	}
	for _, x := range m.Excluded {
		exc = append(exc, bson.M{"slug": x.Slug, "reason": x.Reason})
	}
	return bson.M{"included": inc, "excluded": exc, "bytes": m.Bytes, "budget": m.Budget}
}

// ---- run pinning and brief injection

func pinned(m skills.Manifest) []PinnedSkill {
	out := []PinnedSkill{}
	for _, i := range m.Included {
		out = append(out, PinnedSkill{SkillID: i.SkillID, VersionID: i.VersionID, Slug: i.Slug, Semver: i.Semver, SHA256: i.SHA256})
	}
	return out
}

// pinSkills resolves every role for round 0 and for fix rounds at run creation. It returns nil when nothing is bound
// (the run then renders no skills, like a run created before Sprint 6).
func (a *App) pinSkills(sc context.Context, org, project string, budget int) (map[string]PinnedSkillSet, string, error) {
	orgSet, err := a.bindingSet(sc, org, "org")
	if err != nil {
		return nil, "", err
	}
	projSet, err := a.bindingSet(sc, org, "project:"+project)
	if err != nil {
		return nil, "", err
	}
	if len(orgSet.Bindings) == 0 && len(projSet.Bindings) == 0 {
		return nil, "", nil
	}
	versions, err := a.skillVersionInfos(sc, org, orgSet.Bindings, projSet.Bindings)
	if err != nil {
		return nil, "", err
	}
	out := map[string]PinnedSkillSet{}
	fp := ""
	for _, role := range skills.Roles {
		r0 := skills.Resolve(skills.Input{Org: orgSet.Bindings, Project: projSet.Bindings, Role: role, Round: 0, Versions: versions, Budget: budget})
		fx := skills.Resolve(skills.Input{Org: orgSet.Bindings, Project: projSet.Bindings, Role: role, Round: 1, Versions: versions, Budget: budget})
		if len(r0.Included)+len(fx.Included)+len(r0.Excluded)+len(fx.Excluded) == 0 {
			continue
		}
		out[role] = PinnedSkillSet{Round0: pinned(r0), Fix: pinned(fx), Excluded0: r0.Excluded, ExcludedFix: fx.Excluded}
		for _, p := range append(pinned(r0), pinned(fx)...) {
			fp += role + ":" + p.VersionID + ":" + p.SHA256 + ";"
		}
	}
	if len(out) == 0 {
		return nil, "", nil
	}
	return out, sha256Hex(fp), nil
}

// skillsFor renders the pinned skills of one role and round. The bodies are read by version id, so a retired version
// or a later binding edit changes nothing for a pinned run.
func (a *App) skillsFor(ctx context.Context, run RunDoc, role string, round int) (string, bson.M, error) {
	set, ok := run.Pinned.Skills[role]
	if !ok {
		return "", nil, nil
	}
	list, excluded := set.Round0, set.Excluded0
	if round > 0 {
		list, excluded = set.Fix, set.ExcludedFix
	}
	ids := []skills.Binding{}
	for _, p := range list {
		ids = append(ids, skills.Binding{VersionID: p.VersionID})
	}
	infos, err := a.skillVersionInfos(ctx, run.OrgID, ids)
	if err != nil {
		return "", nil, err
	}
	items := []skills.Item{}
	inc, exc := []bson.M{}, []bson.M{}
	for _, p := range list {
		v, ok := infos[p.VersionID]
		if !ok || v.SHA256 != p.SHA256 {
			continue
		}
		items = append(items, skills.Item{Slug: p.Slug, Semver: p.Semver, SHA256: p.SHA256, Body: v.Body})
		inc = append(inc, bson.M{"slug": p.Slug, "semver": p.Semver, "version_id": p.VersionID, "sha256": p.SHA256})
	}
	for _, x := range excluded {
		exc = append(exc, bson.M{"slug": x.Slug, "reason": x.Reason})
	}
	return skills.Render(items), bson.M{"included": inc, "excluded": exc, "bytes": skills.Bytes(items)}, nil
}

func (a *App) getRunSkills(w http.ResponseWriter, r *http.Request) error {
	run, err := a.run(r.Context(), r.PathValue("orgId"), r.PathValue("runId"))
	if err != nil {
		return err
	}
	roles := bson.M{}
	for role, set := range run.Pinned.Skills {
		roles[role] = bson.M{"round0": nonNilPinned(set.Round0), "fix": nonNilPinned(set.Fix), "excluded0": nonNilExcluded(set.Excluded0), "excluded_fix": nonNilExcluded(set.ExcludedFix)}
	}
	writeJSON(w, 200, bson.M{"roles": roles, "fingerprint": optStr(run.Pinned.SkillsFP)})
	return nil
}

func nonNilPinned(p []PinnedSkill) []PinnedSkill {
	if p == nil {
		return []PinnedSkill{}
	}
	return p
}

func nonNilExcluded(p []skills.Excluded) []skills.Excluded {
	if p == nil {
		return []skills.Excluded{}
	}
	return p
}
