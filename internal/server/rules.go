package server

import (
	"context"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"orqen/internal/contextpkt"
)

const (
	maxRuleTitleRunes = 200
	maxRuleBodyBytes  = 16 << 10
)

// PinnedRuleDoc is a user-written rule that goes into the context packet of every run in its scope. Critical rules
// are never truncated. No code path writes a rule from a run or an answer (Sprint 5 adds promotion).
type PinnedRuleDoc struct {
	ID        string  `bson:"_id"`
	OrgID     string  `bson:"org_id"`
	Scope     string  `bson:"scope"` // org | project
	ProjectID *string `bson:"project_id"`
	Title     string  `bson:"title"`
	Body      string  `bson:"body"`
	Critical  bool    `bson:"critical"`
	// AppliesToRoles limits a critical rule to these roles in role packets (empty = all roles).
	AppliesToRoles []string  `bson:"applies_to_roles,omitempty"`
	Active         bool      `bson:"active"`
	Revision       int64     `bson:"revision"`
	CreatedAt      time.Time `bson:"created_at"`
	UpdatedAt      time.Time `bson:"updated_at"`
}

func (d PinnedRuleDoc) DTO() bson.M {
	return bson.M{"id": d.ID, "scope": d.Scope, "project_id": d.ProjectID, "title": d.Title, "body": d.Body, "critical": d.Critical, "applies_to_roles": nonNilStrings(d.AppliesToRoles), "revision": d.Revision, "created_at": timestamp(d.CreatedAt), "updated_at": timestamp(d.UpdatedAt)}
}

type CreatePinnedRuleRequest struct {
	Scope          string   `json:"scope"`
	ProjectID      *string  `json:"project_id"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	Critical       bool     `json:"critical"`
	AppliesToRoles []string `json:"applies_to_roles"`
}
type PatchPinnedRuleRequest struct {
	Title          *string   `json:"title"`
	Body           *string   `json:"body"`
	Critical       *bool     `json:"critical"`
	AppliesToRoles *[]string `json:"applies_to_roles"`
}

func validRuleRoles(roles []string) []FieldError {
	var es []FieldError
	for _, r := range roles {
		if _, ok := contextpkt.RoleProfiles[r]; !ok {
			es = append(es, FieldError{"applies_to_roles", "unknown role " + r})
		}
	}
	return es
}

func validRuleText(title, body string) []FieldError {
	var es []FieldError
	if t := strings.TrimSpace(title); t == "" || utf8.RuneCountInString(t) > maxRuleTitleRunes {
		es = append(es, FieldError{"title", "must be 1 to 200 characters"})
	}
	if b := strings.TrimSpace(body); b == "" || len(b) > maxRuleBodyBytes || !utf8.ValidString(b) {
		es = append(es, FieldError{"body", "must be 1 byte to 16 KiB of text"})
	}
	return es
}
func (a *App) pinnedRule(ctx context.Context, org, id string) (PinnedRuleDoc, error) {
	return findOne[PinnedRuleDoc](ctx, a.DB, "pinned_rules", bson.M{"_id": id, "org_id": org, "active": true})
}
func (a *App) listPinnedRules(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	filter := bson.M{"org_id": org, "active": true}
	q := r.URL.Query()
	if v := q.Get("scope"); v != "" {
		if v != "org" && v != "project" {
			return validation("scope", "must be org or project")
		}
		filter["scope"] = v
	}
	if v := q.Get("project_id"); v != "" {
		if !validID("prj", v) {
			return validation("project_id", "is not a project id")
		}
		filter["project_id"] = v
	}
	docs, err := findMany[PinnedRuleDoc](r.Context(), a.DB, "pinned_rules", filter, bson.D{{Key: "scope", Value: 1}, {Key: "_id", Value: 1}}, 200)
	if err != nil {
		return err
	}
	out := []bson.M{}
	for _, d := range docs {
		out = append(out, d.DTO())
	}
	writeJSON(w, 200, out)
	return nil
}
func (a *App) createPinnedRule(w http.ResponseWriter, r *http.Request) error {
	var req CreatePinnedRuleRequest
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	es := append(validRuleText(req.Title, req.Body), validRuleRoles(req.AppliesToRoles)...)
	switch req.Scope {
	case "org":
		if req.ProjectID != nil {
			es = append(es, FieldError{"project_id", "an organization rule has no project"})
		}
	case "project":
		if req.ProjectID == nil || !validID("prj", *req.ProjectID) {
			es = append(es, FieldError{"project_id", "is required for a project rule"})
		}
	default:
		es = append(es, FieldError{"scope", "must be org or project"})
	}
	if err := validationErrors(es); err != nil {
		return err
	}
	if req.Scope == "project" {
		if _, err := a.project(r.Context(), org, *req.ProjectID); err != nil {
			return validation("project_id", "unknown project")
		}
	}
	now := time.Now().UTC()
	doc := PinnedRuleDoc{ID: newID("rul"), OrgID: org, Scope: req.Scope, ProjectID: req.ProjectID, Title: strings.TrimSpace(req.Title), Body: strings.TrimSpace(req.Body), Critical: req.Critical, AppliesToRoles: req.AppliesToRoles, Active: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
	err := a.DB.Txn(r.Context(), func(ctx context.Context) error {
		if _, e := a.DB.Coll("pinned_rules").InsertOne(ctx, doc); e != nil {
			return e
		}
		return a.AppendEvent(ctx, org, "pinned_rule.created", Subject{"pinned_rule", doc.ID}, bson.M{"rule_id": doc.ID, "scope": doc.Scope, "critical": doc.Critical}, &doc.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.Committed()
	w.Header().Set("ETag", etag(doc.Revision))
	w.Header().Set("Location", "/api/v1/orgs/"+org+"/rules/"+doc.ID)
	writeJSON(w, 201, doc.DTO())
	return nil
}
func (a *App) patchPinnedRule(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req PatchPinnedRuleRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("rulId")
	var doc PinnedRuleDoc
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		cur, e := a.pinnedRule(ctx, org, id)
		if e != nil {
			return e
		}
		title, body, critical := cur.Title, cur.Body, cur.Critical
		set := bson.M{}
		if req.Title != nil {
			title = strings.TrimSpace(*req.Title)
			set["title"] = title
		}
		if req.Body != nil {
			body = strings.TrimSpace(*req.Body)
			set["body"] = body
		}
		if req.Critical != nil {
			critical = *req.Critical
			set["critical"] = critical
		}
		if req.AppliesToRoles != nil {
			if e = validationErrors(validRuleRoles(*req.AppliesToRoles)); e != nil {
				return e
			}
			set["applies_to_roles"] = *req.AppliesToRoles
		}
		if len(set) == 0 {
			return validation("body", "no changes given")
		}
		if e = validationErrors(validRuleText(title, body)); e != nil {
			return e
		}
		if doc, e = revisionUpdate[PinnedRuleDoc](ctx, a.DB, "pinned_rules", bson.M{"_id": id, "org_id": org, "active": true}, expected, set); e != nil {
			return e
		}
		return a.AppendEvent(ctx, org, "pinned_rule.updated", Subject{"pinned_rule", id}, bson.M{"rule_id": id, "scope": doc.Scope, "critical": doc.Critical}, &doc.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.Committed()
	w.Header().Set("ETag", etag(doc.Revision))
	writeJSON(w, 200, doc.DTO())
	return nil
}
func (a *App) archivePinnedRule(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("rulId")
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		doc, e := revisionUpdate[PinnedRuleDoc](ctx, a.DB, "pinned_rules", bson.M{"_id": id, "org_id": org, "active": true}, expected, bson.M{"active": false})
		if e != nil {
			return e
		}
		return a.AppendEvent(ctx, org, "pinned_rule.archived", Subject{"pinned_rule", id}, bson.M{"rule_id": id, "scope": doc.Scope}, &doc.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.Committed()
	w.WriteHeader(204)
	return nil
}
