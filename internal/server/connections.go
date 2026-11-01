package server

import (
	"context"
	"crypto/x509"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type CreateConnectionRequest struct {
	Name           string    `json:"name"`
	Provider       string    `json:"provider"`
	Deployment     string    `json:"deployment"`
	BaseURL        string    `json:"base_url"`
	AuthMethod     string    `json:"auth_method"`
	AuthUsername   *string   `json:"auth_username"`
	ProxyURL       *string   `json:"proxy_url"`
	CustomCAPEM    *string   `json:"custom_ca_pem"`
	TimeoutMS      *int      `json:"timeout_ms"`
	MaxConcurrency *int      `json:"max_concurrency"`
	AllowedHosts   *[]string `json:"allowed_hosts"`
}
type PatchConnectionRequest struct {
	Name           *string   `json:"name"`
	Deployment     *string   `json:"deployment"`
	BaseURL        *string   `json:"base_url"`
	AuthMethod     *string   `json:"auth_method"`
	AuthUsername   *string   `json:"auth_username"`
	ProxyURL       *string   `json:"proxy_url"`
	CustomCAPEM    *string   `json:"custom_ca_pem"`
	TimeoutMS      *int      `json:"timeout_ms"`
	MaxConcurrency *int      `json:"max_concurrency"`
	AllowedHosts   *[]string `json:"allowed_hosts"`
}
type NormalizedURL struct {
	URL, Host, Path string
	Port            int
}

func normalizeURL(raw string) (NormalizedURL, error) {
	bad := func(msg string) (NormalizedURL, error) { return NormalizedURL{}, validation("base_url", msg) }
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !u.IsAbs() {
		return bad("must be an absolute http(s) URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return bad("scheme must be http or https")
	}
	if u.User != nil {
		return bad("must not contain credentials")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return bad("must not contain a query string or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return bad("host is required")
	}
	if strings.Contains(host, ":") {
		if net.ParseIP(host) == nil {
			return bad("invalid host")
		}
		host = "[" + host + "]"
	}
	port := 80
	if u.Scheme == "https" {
		port = 443
	}
	defaultPort := port
	explicit := ""
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return bad("unknown port")
		}
		if port != defaultPort {
			explicit = ":" + strconv.Itoa(port)
		}
	}
	p := strings.TrimRight(u.EscapedPath(), "/")
	out := NormalizedURL{URL: u.Scheme + "://" + host + explicit + p, Host: host, Port: port, Path: p}
	if out.Path == "" {
		out.Path = "/"
	}
	return out, nil
}

var hostPattern = regexp.MustCompile(`^[a-zA-Z0-9.\-:\[\]]{1,255}$`)

func checkHosts(hosts []string) ([]string, error) {
	out := []string{}
	es := []FieldError{}
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if !hostPattern.MatchString(h) {
			es = append(es, FieldError{"allowed_hosts", "invalid host entry: " + h})
		} else if !contains(out, h) {
			out = append(out, h)
		}
	}
	if len(out) == 0 && len(es) == 0 {
		es = append(es, FieldError{"allowed_hosts", "at least one host is required"})
	}
	return out, validationErrors(es)
}
func checkUsername(raw *string) (*string, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	s := strings.TrimSpace(*raw)
	if len(s) > 254 || strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return nil, validation("auth_username", "must be at most 254 characters without control characters")
	}
	return &s, nil
}
func checkProxy(raw *string) (*string, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	s := strings.TrimSpace(*raw)
	u, err := url.Parse(s)
	if err != nil || !contains([]string{"http", "https", "socks5", "socks5h"}, u.Scheme) || u.Hostname() == "" || u.User != nil {
		return nil, validation("proxy_url", "must be an http, https or socks5 URL without credentials")
	}
	return &s, nil
}
func checkCA(raw *string) (*string, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	s := strings.TrimSpace(*raw)
	if len(s) > 65536 || !x509.NewCertPool().AppendCertsFromPEM([]byte(s)) {
		return nil, validation("custom_ca_pem", "must be a valid PEM certificate")
	}
	return &s, nil
}
func checkConnection(req CreateConnectionRequest) (ConnectionDoc, error) {
	var es []FieldError
	add := func(err error) {
		if err != nil {
			es = append(es, asAppError(err).Errors...)
		}
	}
	name := strings.TrimSpace(req.Name)
	if !validName(name, 80) {
		add(validation("name", "must be 1-80 characters"))
	}
	for _, e := range []struct {
		field, value string
		values       []string
	}{{"provider", req.Provider, []string{"jira", "gitlab", "confluence", "figma"}}, {"deployment", req.Deployment, []string{"cloud", "server", "data_center", "self_managed"}}, {"auth_method", req.AuthMethod, []string{"api_token", "personal_access_token", "basic", "oauth"}}} {
		if !contains(e.values, e.value) {
			add(validation(e.field, "unsupported value"))
		}
	}
	normalized, err := normalizeURL(req.BaseURL)
	add(err)
	username, err := checkUsername(req.AuthUsername)
	add(err)
	proxy, err := checkProxy(req.ProxyURL)
	add(err)
	ca, err := checkCA(req.CustomCAPEM)
	add(err)
	timeout := 15000
	if req.TimeoutMS != nil {
		timeout = *req.TimeoutMS
	}
	if timeout < 1000 || timeout > 120000 {
		add(validation("timeout_ms", "must be between 1000 and 120000"))
	}
	concurrency := 4
	if req.MaxConcurrency != nil {
		concurrency = *req.MaxConcurrency
	}
	if concurrency < 1 || concurrency > 32 {
		add(validation("max_concurrency", "must be between 1 and 32"))
	}
	hosts := []string{normalized.Host}
	if req.AllowedHosts != nil {
		hosts, err = checkHosts(*req.AllowedHosts)
		add(err)
	}
	if err := validationErrors(es); err != nil {
		return ConnectionDoc{}, err
	}
	now := time.Now().UTC()
	return ConnectionDoc{ID: newID("con"), Name: name, NameKey: strings.ToLower(name), Provider: req.Provider, Deployment: req.Deployment, BaseURL: normalized.URL, AuthMethod: req.AuthMethod, AuthUsername: username, CredentialState: "none", ProxyURL: proxy, CustomCaPEM: ca, TimeoutMS: timeout, MaxConcurrency: concurrency, AllowedHosts: hosts, Status: "untested", Active: true, Revision: 1, CreatedAt: now, UpdatedAt: now}, nil
}
func connFilter(org, id string) bson.M { return bson.M{"_id": id, "org_id": org, "active": true} }
func (a *App) connection(ctx context.Context, org, id string) (ConnectionDoc, error) {
	return findOne[ConnectionDoc](ctx, a.DB, "connections", connFilter(org, id))
}
func connectionResponse(w http.ResponseWriter, status int, d ConnectionDoc) {
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, status, d.DTO())
}
func (a *App) listConnections(w http.ResponseWriter, r *http.Request) error {
	limit, after, err := pageQuery(r)
	if err != nil {
		return err
	}
	page, err := listPage[ConnectionDoc](r.Context(), a.DB, "connections", bson.M{"org_id": r.PathValue("orgId"), "active": true}, limit, after)
	if err != nil {
		return err
	}
	writeJSON(w, 200, page)
	return nil
}
func (a *App) createConnection(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, false)
	if err != nil {
		return err
	}
	var req CreateConnectionRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	route := "POST /api/v1/orgs/{orgId}/connections"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org}, req), func(ctx context.Context) (Output, error) {
		d, err := checkConnection(req)
		if err != nil {
			return Output{}, err
		}
		d.OrgID = org
		if _, err = a.DB.Coll("connections").InsertOne(ctx, d); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return Output{}, validation("name", "already used")
			}
			return Output{}, err
		}
		if err = a.AppendEvent(ctx, org, "connection.created", Subject{"connection", d.ID}, bson.M{"name": d.Name, "provider": d.Provider, "base_url": d.BaseURL}, &d.Revision, nil, false); err != nil {
			return Output{}, err
		}
		return Output{201, d.DTO(), map[string]string{"etag": etag(d.Revision), "location": connectionPath(org, d.ID)}}, nil
	})
}
func (a *App) getConnection(w http.ResponseWriter, r *http.Request) error {
	d, err := a.connection(r.Context(), r.PathValue("orgId"), r.PathValue("conId"))
	if err != nil {
		return err
	}
	connectionResponse(w, 200, d)
	return nil
}
func (a *App) patchConnection(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var patch PatchConnectionRequest
	if err = decodeJSON(w, r, &patch, true); err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("conId")
	var doc ConnectionDoc
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		current, e := a.connection(ctx, org, id)
		if e != nil {
			return e
		}
		req := CreateConnectionRequest{Name: current.Name, Provider: current.Provider, Deployment: current.Deployment, BaseURL: current.BaseURL, AuthMethod: current.AuthMethod, AuthUsername: current.AuthUsername, ProxyURL: current.ProxyURL, CustomCAPEM: current.CustomCaPEM, TimeoutMS: &current.TimeoutMS, MaxConcurrency: &current.MaxConcurrency, AllowedHosts: &current.AllowedHosts}
		set := bson.M{}
		if patch.Name != nil {
			req.Name = *patch.Name
			set["name"] = strings.TrimSpace(*patch.Name)
			set["name_key"] = strings.ToLower(strings.TrimSpace(*patch.Name))
		}
		if patch.Deployment != nil {
			req.Deployment = *patch.Deployment
			set["deployment"] = *patch.Deployment
		}
		if patch.AuthMethod != nil {
			req.AuthMethod = *patch.AuthMethod
			set["auth_method"] = *patch.AuthMethod
		}
		if patch.AuthUsername != nil {
			req.AuthUsername = patch.AuthUsername
		}
		if patch.TimeoutMS != nil {
			req.TimeoutMS = patch.TimeoutMS
			set["timeout_ms"] = *patch.TimeoutMS
		}
		if patch.MaxConcurrency != nil {
			req.MaxConcurrency = patch.MaxConcurrency
			set["max_concurrency"] = *patch.MaxConcurrency
		}
		if patch.ProxyURL != nil {
			req.ProxyURL = patch.ProxyURL
		}
		if patch.CustomCAPEM != nil {
			req.CustomCAPEM = patch.CustomCAPEM
		}
		if patch.AllowedHosts != nil {
			req.AllowedHosts = patch.AllowedHosts
			set["allowed_hosts"] = *patch.AllowedHosts
		}
		if patch.BaseURL != nil {
			req.BaseURL = *patch.BaseURL
		}
		updated, e := checkConnection(req)
		if e != nil {
			return e
		}
		if patch.AuthUsername != nil {
			set["auth_username"] = updated.AuthUsername
		}
		if patch.ProxyURL != nil {
			set["proxy_url"] = updated.ProxyURL
		}
		if patch.CustomCAPEM != nil {
			set["custom_ca_pem"] = updated.CustomCaPEM
		}
		if patch.AllowedHosts != nil {
			set["allowed_hosts"] = updated.AllowedHosts
		}
		if updated.BaseURL != current.BaseURL {
			set["base_url"] = updated.BaseURL
			set["status"] = "untested"
			set["last_test"] = nil
			old, _ := normalizeURL(current.BaseURL)
			if patch.AllowedHosts == nil && len(current.AllowedHosts) == 1 && current.AllowedHosts[0] == old.Host {
				n, _ := normalizeURL(updated.BaseURL)
				set["allowed_hosts"] = []string{n.Host}
			}
		}
		if len(set) == 0 {
			return validation("body", "no changes given")
		}
		doc, e = revisionUpdate[ConnectionDoc](ctx, a.DB, "connections", connFilter(org, id), expected, set)
		if e != nil {
			if mongo.IsDuplicateKeyError(e) {
				return validation("name", "already used")
			}
			return e
		}
		return a.AppendEvent(ctx, org, "connection.updated", Subject{"connection", id}, bson.M{"name": doc.Name, "base_url": doc.BaseURL}, &doc.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.Committed()
	connectionResponse(w, 200, doc)
	return nil
}
func (a *App) bestEffortDelete(ctx context.Context, ref *string) {
	if ref != nil {
		if err := a.Creds.Delete(ctx, *ref); err != nil {
			slog.Warn("failed to delete credential from store", "credential_ref", *ref, "error", err)
		}
	}
}
func (a *App) archiveConnection(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("conId")
	var old *string
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		current, e := a.connection(ctx, org, id)
		if e != nil {
			return e
		}
		old = current.CredentialRef
		updated, e := revisionUpdate[ConnectionDoc](ctx, a.DB, "connections", connFilter(org, id), expected, bson.M{"active": false, "archived_at": time.Now().UTC(), "credential_ref": nil, "credential_state": "none"})
		if e != nil {
			return e
		}
		return a.AppendEvent(ctx, org, "connection.archived", Subject{"connection", id}, bson.M{"name": updated.Name}, &updated.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.Committed()
	a.bestEffortDelete(r.Context(), old)
	w.WriteHeader(204)
	return nil
}
func (a *App) putCredential(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req struct {
		Secret *string `json:"secret"`
	}
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if req.Secret == nil || len(*req.Secret) < 1 || len(*req.Secret) > 8192 {
		return validation("secret", "must be 1-8192 bytes")
	}
	return a.replaceCredential(w, r, expected, req.Secret)
}
func (a *App) deleteCredential(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	return a.replaceCredential(w, r, expected, nil)
}
func (a *App) replaceCredential(w http.ResponseWriter, r *http.Request, expected int64, secret *string) error {
	org, id := r.PathValue("orgId"), r.PathValue("conId")
	if _, err := a.connection(r.Context(), org, id); err != nil {
		return err
	}
	var newRef, oldRef *string
	if secret != nil {
		newRef = ptr(newID("cred"))
		if err := a.Creds.Put(r.Context(), *newRef, *secret); err != nil {
			return problem("credential_store_unavailable")
		}
	}
	var doc ConnectionDoc
	err := a.DB.Txn(r.Context(), func(ctx context.Context) error {
		current, e := a.connection(ctx, org, id)
		if e != nil {
			return e
		}
		oldRef = current.CredentialRef
		state := "none"
		kind := "connection.credential_removed"
		payload := bson.M{}
		if newRef != nil {
			state = "set"
			kind = "connection.credential_set"
			payload["credential_ref"] = *newRef
		}
		doc, e = revisionUpdate[ConnectionDoc](ctx, a.DB, "connections", connFilter(org, id), expected, bson.M{"credential_ref": newRef, "credential_state": state, "credential_missing": false, "credential_updated_at": time.Now().UTC()})
		if e != nil {
			return e
		}
		return a.AppendEvent(ctx, org, kind, Subject{"connection", id}, payload, &doc.Revision, nil, false)
	})
	if err != nil {
		a.bestEffortDelete(r.Context(), newRef)
		return err
	}
	a.Committed()
	a.bestEffortDelete(r.Context(), oldRef)
	connectionResponse(w, 200, doc)
	return nil
}
func (a *App) testConnection(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("conId")
	route := "POST /api/v1/orgs/{orgId}/connections/{conId}/test"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "conId": id}, nil), func(ctx context.Context) (Output, error) {
		conn, err := a.connection(ctx, org, id)
		if err != nil {
			return Output{}, err
		}
		return a.startOperation(ctx, org, "connection_test", Subject{"connection", id}, "connection_test:"+id, 3, bson.M{"connection_id": id, "connection_revision": conn.Revision})
	})
}
func (a *App) getOperation(w http.ResponseWriter, r *http.Request) error {
	op, err := findOne[OperationDoc](r.Context(), a.DB, "operations", bson.M{"_id": r.PathValue("opId"), "org_id": r.PathValue("orgId")})
	if err != nil {
		return err
	}
	writeJSON(w, 200, op.DTO())
	return nil
}
func (a *App) transitionOperation(ctx context.Context, job JobDoc, status string, payload bson.M) error {
	if job.OperationID == nil {
		return nil
	}
	set := bson.M{"status": status, "updated_at": time.Now().UTC()}
	kind := "operation.started"
	eventPayload := bson.M{}
	switch status {
	case "succeeded":
		kind = "operation.succeeded"
		set["result"] = payload
	case "failed":
		kind = "operation.failed"
		set["problem"] = payload
		eventPayload["code"] = payload["code"]
	}
	var op OperationDoc
	err := a.DB.Coll("operations").FindOneAndUpdate(ctx, bson.M{"_id": *job.OperationID, "org_id": job.OrgID, "status": bson.M{"$in": []string{"pending", "running"}}}, bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&op)
	if err != nil {
		if status == "failed" && errors.Is(err, mongo.ErrNoDocuments) {
			return nil
		}
		return err
	}
	return a.AppendEvent(ctx, job.OrgID, kind, Subject{"operation", op.ID}, eventPayload, &op.Revision, job.OperationID, true)
}
