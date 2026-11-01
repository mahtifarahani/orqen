package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"net/url"
	"orqen/internal/extwrite"
	"time"
)

func hostOf(base string) string {
	if u, err := url.Parse(base); err == nil {
		return u.Host
	}
	return ""
}

// WriteCheck is the last GET-only inspection of a write credential.
type WriteCheck struct {
	Status      string           `bson:"status" json:"status"` // ok | refused | insufficient | unchecked
	Username    string           `bson:"username" json:"username"`
	UserID      string           `bson:"user_id" json:"user_id"`
	Scopes      []string         `bson:"scopes" json:"scopes"`
	Projects    []WriteCheckProj `bson:"projects" json:"projects"`
	Detail      string           `bson:"detail" json:"detail"`
	CheckedAt   time.Time        `bson:"checked_at" json:"checked_at"`
	OperationID string           `bson:"operation_id" json:"operation_id"`
}

// WriteCheckProj is the access level found on one project.
type WriteCheckProj struct {
	ProviderProjectID string `bson:"provider_project_id" json:"provider_project_id"`
	AccessLevel       int    `bson:"access_level" json:"access_level"`
}

// PublisherCredDoc is the separate write credential of one GitLab connection (collection publisher_credentials).
// Only this file and internal/extwrite ever read the secret it points at.
type PublisherCredDoc struct {
	ID            string      `bson:"_id"`
	OrgID         string      `bson:"org_id"`
	ConnectionID  string      `bson:"connection_id"`
	CredentialRef *string     `bson:"credential_ref"`
	State         string      `bson:"state"` // set | none
	UpdatedAt     *time.Time  `bson:"updated_at"`
	LastCheck     *WriteCheck `bson:"last_check"`
	Active        bool        `bson:"active"`
	Revision      int64       `bson:"revision"`
	CreatedAt     time.Time   `bson:"created_at"`
}

// DTO is WriteCredentialDto: the secret is never part of it.
func (d PublisherCredDoc) DTO() bson.M {
	var last any
	if c := d.LastCheck; c != nil {
		projects := []bson.M{}
		for _, p := range c.Projects {
			projects = append(projects, bson.M{"provider_project_id": p.ProviderProjectID, "access_level": p.AccessLevel})
		}
		last = bson.M{"status": c.Status, "username": c.Username, "user_id": c.UserID, "scopes": nonNilStr(c.Scopes), "projects": projects, "detail": c.Detail, "checked_at": timestamp(c.CheckedAt), "operation_id": optStr(c.OperationID)}
	}
	state := d.State
	if state == "" {
		state = "none"
	}
	return bson.M{"state": state, "updated_at": optionalTime(d.UpdatedAt), "last_check": last, "revision": d.Revision}
}

func (a *App) publisherCred(ctx context.Context, org, con string) (PublisherCredDoc, error) {
	d, err := findOne[PublisherCredDoc](ctx, a.DB, "publisher_credentials", bson.M{"org_id": org, "connection_id": con, "active": true})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return PublisherCredDoc{OrgID: org, ConnectionID: con, State: "none"}, nil
	}
	return d, err
}

// hasWriteCredential reports whether the connection has a write credential (never reads the secret).
func (a *App) hasWriteCredential(ctx context.Context, org, con string) bool {
	d, err := a.publisherCred(ctx, org, con)
	return err == nil && d.State == "set" && d.CredentialRef != nil
}

// writeSecret reads the write credential. It is called by the credential check job and by the gate's Execute only.
func (a *App) writeSecret(ctx context.Context, org, con string) (string, error) {
	d, err := a.publisherCred(ctx, org, con)
	if err != nil {
		return "", err
	}
	if d.State != "set" || d.CredentialRef == nil {
		return "", problem("write_credential_missing")
	}
	v, ok, err := a.Creds.Get(ctx, *d.CredentialRef)
	if err != nil || !ok {
		return "", problem("write_credential_missing")
	}
	return v, nil
}

func (a *App) gitlabConnection(ctx context.Context, r *http.Request) (ConnectionDoc, error) {
	conn, err := a.connection(ctx, r.PathValue("orgId"), r.PathValue("conId"))
	if err != nil {
		return conn, err
	}
	if conn.Provider != "gitlab" {
		return conn, problem("connection_kind_unsupported")
	}
	return conn, nil
}

func (a *App) getWriteCredential(w http.ResponseWriter, r *http.Request) error {
	conn, err := a.gitlabConnection(r.Context(), r)
	if err != nil {
		return err
	}
	d, err := a.publisherCred(r.Context(), conn.OrgID, conn.ID)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, 200, d.DTO())
	return nil
}

// PutWriteCredentialRequest is the body of PUT /connections/{conId}/write-credential.
type PutWriteCredentialRequest struct {
	Secret *string `json:"secret"`
}

var errPubConflict = errors.New("write credential revision conflict")

func (a *App) pubConflict(ctx context.Context, org, con string) error {
	cur, err := a.publisherCred(ctx, org, con)
	if err != nil {
		return err
	}
	c := problem("revision_conflict")
	c.Current = cur.DTO()
	return c
}

func (a *App) putWriteCredential(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	var req PutWriteCredentialRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if req.Secret == nil || len(*req.Secret) < 1 || len(*req.Secret) > 8192 {
		return validation("secret", "must be 1-8192 bytes")
	}
	conn, err := a.gitlabConnection(r.Context(), r)
	if err != nil {
		return err
	}
	// The write credential must be a different secret than the read credential.
	if conn.CredentialRef != nil {
		if read, ok, e := a.Creds.Get(r.Context(), *conn.CredentialRef); e == nil && ok {
			a1, b1 := sha256.Sum256([]byte(read)), sha256.Sum256([]byte(*req.Secret))
			if subtle.ConstantTimeCompare(a1[:], b1[:]) == 1 {
				return problem("write_credential_same_as_read")
			}
		}
	}
	return a.replaceWriteCredential(w, r, conn, expected, req.Secret)
}

func (a *App) deleteWriteCredential(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	conn, err := a.gitlabConnection(r.Context(), r)
	if err != nil {
		return err
	}
	return a.replaceWriteCredential(w, r, conn, expected, nil)
}

func (a *App) replaceWriteCredential(w http.ResponseWriter, r *http.Request, conn ConnectionDoc, expected int64, secret *string) error {
	org := conn.OrgID
	var newRef, oldRef *string
	if secret != nil {
		newRef = ptr(newID("cred"))
		if err := a.Creds.Put(r.Context(), *newRef, *secret); err != nil {
			return problem("credential_store_unavailable")
		}
	}
	var doc PublisherCredDoc
	err := a.DB.Txn(r.Context(), func(ctx context.Context) error {
		now := time.Now().UTC()
		state, kind := "none", "write_credential.removed"
		if newRef != nil {
			state, kind = "set", "write_credential.set"
		}
		coll := a.DB.Coll("publisher_credentials")
		if expected == 0 {
			if secret == nil {
				doc = PublisherCredDoc{OrgID: org, ConnectionID: conn.ID, State: "none"}
				return nil
			}
			doc = PublisherCredDoc{ID: newID("pcr"), OrgID: org, ConnectionID: conn.ID, CredentialRef: newRef, State: state, UpdatedAt: &now, Active: true, Revision: 1, CreatedAt: now}
			if _, e := coll.InsertOne(ctx, doc); e != nil {
				if mongo.IsDuplicateKeyError(e) {
					return errPubConflict
				}
				return e
			}
		} else {
			e := coll.FindOneAndUpdate(ctx, bson.M{"org_id": org, "connection_id": conn.ID, "active": true, "revision": expected},
				bson.M{"$set": bson.M{"credential_ref": newRef, "state": state, "updated_at": now, "last_check": nil}, "$inc": bson.M{"revision": int64(1)}},
				options.FindOneAndUpdate().SetReturnDocument(options.Before)).Decode(&doc)
			if errors.Is(e, mongo.ErrNoDocuments) {
				return errPubConflict
			}
			if e != nil {
				return e
			}
			oldRef = doc.CredentialRef
			doc.CredentialRef, doc.State, doc.UpdatedAt, doc.LastCheck, doc.Revision = newRef, state, &now, nil, doc.Revision+1
			if secret == nil && oldRef == nil {
				return nil
			}
		}
		return a.AppendEvent(ctx, org, kind, Subject{"connection", conn.ID}, bson.M{"connection_id": conn.ID}, &doc.Revision, nil, false)
	})
	if errors.Is(err, errPubConflict) {
		a.bestEffortDelete(r.Context(), newRef)
		return a.pubConflict(r.Context(), org, conn.ID)
	}
	if err != nil {
		a.bestEffortDelete(r.Context(), newRef)
		return err
	}
	a.Committed()
	a.bestEffortDelete(r.Context(), oldRef)
	w.Header().Set("ETag", etag(doc.Revision))
	writeJSON(w, 200, doc.DTO())
	return nil
}

func (a *App) checkWriteCredential(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	conn, err := a.gitlabConnection(r.Context(), r)
	if err != nil {
		return err
	}
	org := conn.OrgID
	route := "POST /api/v1/orgs/{orgId}/connections/{conId}/write-credential/check"
	hash := requestHash("POST", route, map[string]string{"orgId": org, "conId": conn.ID}, nil)
	return a.idempotent(w, r, org, route, key, hash, func(ctx context.Context) (Output, error) {
		if !a.hasWriteCredential(ctx, org, conn.ID) {
			return Output{}, problem("write_credential_missing")
		}
		return a.startOperation(ctx, org, "write_credential_check", Subject{"connection", conn.ID}, "write_credential_check:"+conn.ID, 1, bson.M{"connection_id": conn.ID})
	})
}

// runWriteCredentialCheck is the check job: GET only, through the guarded client, against the connection's host.
func (a *App) runWriteCredentialCheck(ctx context.Context, job JobDoc, lease Lease) error {
	conID, _ := job.Payload["connection_id"].(string)
	conn, err := a.connection(ctx, job.OrgID, conID)
	if err != nil {
		return &JobFailure{Code: "connection_missing", Message: "the connection is gone"}
	}
	secret, err := a.writeSecret(ctx, job.OrgID, conID)
	if err != nil {
		return &JobFailure{Code: "write_credential_missing", Message: "no write credential is set"}
	}
	settings, err := a.externalWriteSettings(ctx, job.OrgID)
	if err != nil {
		return err
	}
	var projects []string
	for _, e := range settings.Allowlist {
		if e.ConnectionID == conID {
			projects = append(projects, e.ProviderProjectID)
		}
	}
	scope := a.guard().Scope
	g := &extwrite.GitLab{Base: conn.BaseURL, Token: secret, HC: extwrite.CheckClient(scope), Scope: scope}
	res, err := g.CheckCredential(ctx, projects)
	check := WriteCheck{Status: res.Status, Username: res.Username, UserID: res.UserID, Scopes: res.Scopes, Detail: res.Detail, CheckedAt: time.Now().UTC(), OperationID: ptrStr(job.OperationID)}
	for _, p := range res.Projects {
		check.Projects = append(check.Projects, WriteCheckProj{p.ProviderProjectID, p.AccessLevel})
	}
	if err != nil {
		check.Status, check.Detail = "unchecked", "the check could not reach the host"
		if errors.Is(err, extwrite.ErrNotLoopback) {
			check.Detail = "refused by the environment guard"
		}
	}
	return a.fenced(ctx, lease, func(sc context.Context) error {
		var cur PublisherCredDoc
		if e := a.DB.Coll("publisher_credentials").FindOneAndUpdate(sc, bson.M{"org_id": job.OrgID, "connection_id": conID, "active": true}, bson.M{"$set": bson.M{"last_check": check}, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&cur); e != nil {
			return e
		}
		if e := a.AppendEvent(sc, job.OrgID, "write_credential.checked", Subject{"connection", conID}, bson.M{"connection_id": conID, "status": check.Status}, &cur.Revision, job.OperationID, true); e != nil {
			return e
		}
		if e := a.transitionOperation(sc, job, "succeeded", bson.M{"status": check.Status}); e != nil {
			return e
		}
		return a.completeJob(sc, lease)
	})
}

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
