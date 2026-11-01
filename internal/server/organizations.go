package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type CreateOrgRequest struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Locale   string `json:"locale"`
	Timezone string `json:"timezone"`
}
type PatchOrgRequest struct {
	Name     *string `json:"name"`
	Locale   *string `json:"locale"`
	Timezone *string `json:"timezone"`
}

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

func validName(name string, max int) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(name))
	return n >= 1 && n <= max
}
func validateOrg(req CreateOrgRequest, checkSlug bool) error {
	es := []FieldError{}
	if checkSlug && !slugPattern.MatchString(req.Slug) {
		es = append(es, FieldError{"slug", "3-40 characters: lowercase letters, digits and hyphens, not at the ends"})
	}
	if !validName(req.Name, 120) {
		es = append(es, FieldError{"name", "must be 1-120 characters"})
	}
	if !contains([]string{"fa", "en"}, req.Locale) {
		es = append(es, FieldError{"locale", "must be fa or en"})
	}
	if _, err := time.LoadLocation(req.Timezone); err != nil || req.Timezone == "" || req.Timezone == "Local" {
		es = append(es, FieldError{"timezone", "unknown IANA timezone"})
	}
	return validationErrors(es)
}
func pageQuery(r *http.Request) (int64, string, error) {
	limit, err := queryInt(r, "limit", 50)
	if err != nil {
		return 0, "", err
	}
	if limit < 1 || limit > 200 {
		return 0, "", validation("limit", "must be between 1 and 200")
	}
	after := ""
	if values, ok := r.URL.Query()["cursor"]; ok {
		if len(values) != 1 {
			return 0, "", validation("cursor", "invalid cursor")
		}
		b, e := base64.RawURLEncoding.DecodeString(values[0])
		if e != nil || len(b) == 0 || len(b) >= 128 || !utf8.Valid(b) {
			return 0, "", validation("cursor", "invalid cursor")
		}
		after = string(b)
	}
	return limit, after, nil
}
func listPage[T interface{ DTO() bson.M }](ctx context.Context, db *DB, coll string, filter bson.M, limit int64, after string) (bson.M, error) {
	if after != "" {
		filter["_id"] = bson.M{"$gt": after}
	}
	docs, err := findMany[T](ctx, db, coll, filter, bson.D{{Key: "_id", Value: 1}}, limit+1)
	if err != nil {
		return nil, err
	}
	var next any
	if len(docs) > int(limit) {
		docs = docs[:limit]
		next = base64.RawURLEncoding.EncodeToString([]byte(docs[len(docs)-1].DTO()["id"].(string)))
	}
	items := []bson.M{}
	for _, d := range docs {
		items = append(items, d.DTO())
	}
	return bson.M{"items": items, "next_cursor": next}, nil
}

// pageQueryKey is pageQuery for cursors that carry a longer sort key (up to 1 KiB decoded).
func pageQueryKey(r *http.Request) (int64, string, error) {
	limit, err := queryInt(r, "limit", 50)
	if err != nil {
		return 0, "", err
	}
	if limit < 1 || limit > 200 {
		return 0, "", validation("limit", "must be between 1 and 200")
	}
	after := ""
	if values, ok := r.URL.Query()["cursor"]; ok {
		if len(values) != 1 {
			return 0, "", validation("cursor", "invalid cursor")
		}
		b, e := base64.RawURLEncoding.DecodeString(values[0])
		if e != nil || len(b) == 0 || len(b) > 1024 || !utf8.Valid(b) {
			return 0, "", validation("cursor", "invalid cursor")
		}
		after = string(b)
	}
	return limit, after, nil
}

// findPageBy is keyset pagination over sortField; key extracts a document's sort value for the next cursor.
func findPageBy[T any](ctx context.Context, db *DB, coll string, filter bson.M, sortField string, key func(T) string, limit int64, after string) ([]T, any, error) {
	if after != "" {
		filter[sortField] = bson.M{"$gt": after}
	}
	docs, err := findMany[T](ctx, db, coll, filter, bson.D{{Key: sortField, Value: 1}}, limit+1)
	if err != nil {
		return nil, nil, err
	}
	var next any
	if len(docs) > int(limit) {
		docs = docs[:limit]
		next = base64.RawURLEncoding.EncodeToString([]byte(key(docs[len(docs)-1])))
	}
	return docs, next, nil
}
func listPageBy[T interface{ DTO() bson.M }](ctx context.Context, db *DB, coll string, filter bson.M, sortField string, key func(T) string, limit int64, after string) (bson.M, error) {
	docs, next, err := findPageBy(ctx, db, coll, filter, sortField, key, limit, after)
	if err != nil {
		return nil, err
	}
	items := []bson.M{}
	for _, d := range docs {
		items = append(items, d.DTO())
	}
	return bson.M{"items": items, "next_cursor": next}, nil
}
func (a *App) listOrgs(w http.ResponseWriter, r *http.Request) error {
	limit, after, err := pageQuery(r)
	if err != nil {
		return err
	}
	page, err := listPage[OrganizationDoc](r.Context(), a.DB, "organizations", bson.M{"archived_at": nil}, limit, after)
	if err != nil {
		return err
	}
	writeJSON(w, 200, page)
	return nil
}
func (a *App) createOrg(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, false)
	if err != nil {
		return err
	}
	var req CreateOrgRequest
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	route := "POST /api/v1/orgs"
	hash := requestHash("POST", route, nil, req)
	return a.idempotent(w, r, "global", route, key, hash, func(ctx context.Context) (Output, error) {
		if err := validateOrg(req, true); err != nil {
			return Output{}, err
		}
		now := time.Now().UTC()
		doc := OrganizationDoc{ID: newID("org"), Slug: req.Slug, Name: strings.TrimSpace(req.Name), Locale: req.Locale, Timezone: req.Timezone, Revision: 1, CreatedAt: now, UpdatedAt: now}
		if _, err := a.DB.Coll("organizations").InsertOne(ctx, doc); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return Output{}, validation("slug", "already used")
			}
			return Output{}, err
		}
		if err := a.AppendEvent(ctx, doc.ID, "org.created", Subject{"organization", doc.ID}, bson.M{"slug": doc.Slug, "name": doc.Name}, &doc.Revision, nil, false); err != nil {
			return Output{}, err
		}
		return Output{Status: 201, Body: doc.DTO(), Headers: map[string]string{"etag": etag(doc.Revision), "location": "/api/v1/orgs/" + doc.ID}}, nil
	})
}
func (a *App) getOrg(w http.ResponseWriter, r *http.Request) error {
	doc := currentOrg(r)
	w.Header().Set("ETag", etag(doc.Revision))
	writeJSON(w, 200, doc.DTO())
	return nil
}
func ifMatch(r *http.Request) (int64, error) { return ifMatchFrom(r, 1) }

// ifMatchOrZero accepts revision 0, which names a document that does not exist yet (a synthesized default).
func ifMatchOrZero(r *http.Request) (int64, error) { return ifMatchFrom(r, 0) }
func ifMatchFrom(r *http.Request, min int64) (int64, error) {
	raw, ok := r.Header["If-Match"]
	if !ok {
		return 0, problem("precondition_required")
	}
	if len(raw) != 1 {
		return 0, validation("If-Match", "must be a revision number")
	}
	s := strings.TrimSpace(raw[0])
	s = strings.TrimPrefix(s, "W/")
	if strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"") {
		s = s[1 : len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < min {
		return 0, validation("If-Match", "must be a revision number such as \"3\"")
	}
	return n, nil
}
func (a *App) patchOrg(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req PatchOrgRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	doc := currentOrg(r)
	id := doc.ID
	set := bson.M{}
	v := CreateOrgRequest{Name: doc.Name, Locale: doc.Locale, Timezone: doc.Timezone}
	if req.Name != nil {
		v.Name = *req.Name
		set["name"] = strings.TrimSpace(*req.Name)
	}
	if req.Locale != nil {
		v.Locale = *req.Locale
		set["locale"] = *req.Locale
	}
	if req.Timezone != nil {
		v.Timezone = *req.Timezone
		set["timezone"] = *req.Timezone
	}
	if len(set) == 0 {
		return validation("body", "no changes given")
	}
	if err = validateOrg(v, false); err != nil {
		return err
	}
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		var e error
		doc, e = revisionUpdate[OrganizationDoc](ctx, a.DB, "organizations", bson.M{"_id": id, "archived_at": nil}, expected, set)
		if e != nil {
			return e
		}
		return a.AppendEvent(ctx, doc.ID, "org.updated", Subject{"organization", doc.ID}, bson.M{"name": doc.Name, "locale": doc.Locale, "timezone": doc.Timezone}, &doc.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.Committed()
	w.Header().Set("ETag", etag(doc.Revision))
	writeJSON(w, 200, doc.DTO())
	return nil
}
func connectionPath(org, id string) string {
	return fmt.Sprintf("/api/v1/orgs/%s/connections/%s", org, id)
}
