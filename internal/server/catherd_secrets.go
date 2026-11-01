package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"orqen/internal/catherd"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// secretKind describes one key the organization can give catherd. The value lives only in the credential store
// (Keychain); Mongo holds a reference. It reaches catherd as an environment variable of one process (PLAN-S2-GO-ADDENDUM-2 K2).
type secretKind struct {
	Kind, Env, LabelKey string
	// Backend is the harness whose isolation needs this key ("" for catherd's own keys).
	Backend string
	// UsedBy names the calls that receive the key (the injection matrix).
	UsedBy []string
	// Verifier is the cheap authenticated GET; "" means the key cannot be verified without running a model.
	Base, Path, Header, Scheme string
	// Refused403 makes a 403 count as refused (AA only treats 401 as refused).
	Refused403 bool
}

var secretKinds = []secretKind{
	{Kind: "jev", Env: "TYPESAFE_API_KEY", LabelKey: "secrets.kind.jev", UsedBy: []string{"doctor"}, Base: "https://api.typesafe.ai", Path: "/v1/models", Header: "Authorization", Scheme: "Bearer ", Refused403: true},
	{Kind: "artificial_analysis", Env: "ARTIFICIAL_ANALYSIS_API_KEY", LabelKey: "secrets.kind.artificial_analysis", UsedBy: []string{"catalog_sync"}, Base: "https://artificialanalysis.ai", Path: "/api/v2/language/models/free?page=1", Header: "x-api-key"},
	{Kind: "gemini", Env: "GEMINI_API_KEY", LabelKey: "secrets.kind.gemini", Backend: "antigravity", UsedBy: []string{"doctor", "catalog_listings", "profile_validation", "profile_publish"}, Base: "https://generativelanguage.googleapis.com", Path: "/v1beta/models", Header: "x-goog-api-key", Refused403: true},
	{Kind: "cursor", Env: "CURSOR_API_KEY", LabelKey: "secrets.kind.cursor", Backend: "cursor", UsedBy: []string{"doctor", "catalog_listings", "profile_validation", "profile_publish"}},
	{Kind: "xai", Env: "XAI_API_KEY", LabelKey: "secrets.kind.xai", Backend: "grok", UsedBy: []string{"doctor", "catalog_listings", "profile_validation", "profile_publish"}, Base: "https://api.x.ai", Path: "/v1/models", Header: "Authorization", Scheme: "Bearer ", Refused403: true},
}

func secretKindOf(kind string) (secretKind, bool) {
	for _, k := range secretKinds {
		if k.Kind == kind {
			return k, true
		}
	}
	return secretKind{}, false
}
func (k secretKind) verifiable() bool { return k.Base != "" }

// isolationEnvs are the backend keys: they are only meaningful for an isolated harness.
func isolationEnvs() []string {
	var out []string
	for _, k := range secretKinds {
		if k.Backend != "" {
			out = append(out, k.Env)
		}
	}
	return out
}

// isolationEnvsFor lists the keys of the backends whose harness is isolated=true in a profile document.
func isolationEnvsFor(doc map[string]any) []string {
	harness, _ := doc["harness"].(map[string]any)
	var out []string
	for _, k := range secretKinds {
		if h, ok := harness[k.Backend].(map[string]any); ok && k.Backend != "" && h["isolated"] == true {
			out = append(out, k.Env)
		}
	}
	return out
}

// isolatedKeyed lists the keyed backends (grok, cursor, antigravity) a profile document runs on an isolated harness.
func isolatedKeyed(doc map[string]any) []string {
	harness, _ := doc["harness"].(map[string]any)
	used := map[string]bool{}
	for _, b := range usedBackends(doc) {
		used[b] = true
	}
	var out []string
	for _, k := range secretKinds {
		if h, ok := harness[k.Backend].(map[string]any); ok && k.Backend != "" && h["isolated"] == true && used[k.Backend] {
			out = append(out, k.Backend)
		}
	}
	return out
}

// secretDocs loads the organization's key records by kind. Values are never read here.
func (a *App) secretDocs(ctx context.Context, org string) (map[string]CatherdSecretDoc, error) {
	docs, err := findMany[CatherdSecretDoc](ctx, a.DB, "catherd_secrets", bson.M{"org_id": org, "active": true}, bson.D{{Key: "_id", Value: 1}}, 20)
	if err != nil {
		return nil, err
	}
	out := map[string]CatherdSecretDoc{}
	for _, d := range docs {
		out[d.Kind] = d
	}
	return out, nil
}
func (d CatherdSecretDoc) isSet() bool { return d.State == "set" }
func (d CatherdSecretDoc) checkStatus() string {
	if d.LastCheck == nil {
		return ""
	}
	return d.LastCheck.Status
}

type SecretCheck struct {
	Status      string    `bson:"status" json:"status"`
	CheckedAt   time.Time `bson:"checked_at" json:"checked_at"`
	Detail      string    `bson:"detail" json:"detail"`
	OperationID string    `bson:"operation_id" json:"operation_id"`
}
type CatherdSecretDoc struct {
	ID            string       `bson:"_id" json:"_id"`
	OrgID         string       `bson:"org_id" json:"org_id"`
	Kind          string       `bson:"kind" json:"kind"`
	CredentialRef *string      `bson:"credential_ref" json:"credential_ref"`
	State         string       `bson:"state" json:"state"`
	UpdatedAt     *time.Time   `bson:"updated_at" json:"updated_at"`
	Active        bool         `bson:"active" json:"active"`
	Revision      int64        `bson:"revision" json:"revision"`
	CreatedAt     time.Time    `bson:"created_at" json:"created_at"`
	LastCheck     *SecretCheck `bson:"last_check" json:"last_check"`
}

// secretDTO never carries the secret, a fragment, a hash or the credential reference: only state and metadata.
func secretDTO(k secretKind, d *CatherdSecretDoc) bson.M {
	out := bson.M{"kind": k.Kind, "env_name": k.Env, "state": "none", "updated_at": nil, "revision": int64(0), "last_check": nil, "verifiable": k.verifiable(), "used_by": k.UsedBy, "required_reason": nil}
	if k.Backend != "" {
		out["required_reason"] = "isolated_harness"
	}
	if d != nil {
		out["state"], out["revision"] = d.State, d.Revision
		out["updated_at"] = optionalTime(d.UpdatedAt)
		if c := d.LastCheck; c != nil {
			out["last_check"] = bson.M{"status": c.Status, "checked_at": timestamp(c.CheckedAt), "detail": c.Detail, "operation_id": c.OperationID}
		}
	}
	return out
}
func (d CatherdSecretDoc) DTO() bson.M {
	k, _ := secretKindOf(d.Kind)
	if d.Kind == "" {
		k = secretKinds[0]
	}
	return secretDTO(k, &d)
}
func (a *App) secretDoc(ctx context.Context, org, kind string) (*CatherdSecretDoc, error) {
	d, err := findOne[CatherdSecretDoc](ctx, a.DB, "catherd_secrets", bson.M{"org_id": org, "kind": kind, "active": true})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}
func (a *App) secretKindParam(r *http.Request) (secretKind, error) {
	k, ok := secretKindOf(r.PathValue("kind"))
	if !ok {
		return k, problem("not_found")
	}
	return k, nil
}
func (a *App) listCatherdSecrets(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	docs, err := findMany[CatherdSecretDoc](r.Context(), a.DB, "catherd_secrets", bson.M{"org_id": org, "active": true}, bson.D{{Key: "_id", Value: 1}}, 20)
	if err != nil {
		return err
	}
	out := []bson.M{}
	for _, k := range secretKinds {
		var found *CatherdSecretDoc
		for i := range docs {
			if docs[i].Kind == k.Kind {
				found = &docs[i]
			}
		}
		out = append(out, secretDTO(k, found))
	}
	writeJSON(w, 200, out)
	return nil
}

type PutCatherdSecretRequest struct {
	Secret string `json:"secret"`
}

func secretInvalid(message string) error {
	return &AppError{Code: "secret_invalid", Status: 422, Detail: "the key is not valid", Errors: []FieldError{{"secret", message}}}
}

// validateSecretValue never repeats the value in its message.
func validateSecretValue(s string) error {
	n := utf8.RuneCountInString(s)
	switch {
	case n < 1 || n > 2048:
		return secretInvalid("must be 1 to 2048 characters")
	case strings.IndexFunc(s, unicode.IsControl) >= 0:
		return secretInvalid("must be a single line without control characters")
	case strings.TrimSpace(s) != s:
		return secretInvalid("must not start or end with a space")
	}
	return nil
}

var errSecretConflict = errors.New("catherd secret conflict")

func (a *App) secretConflict(ctx context.Context, org string, k secretKind) error {
	cur, err := a.secretDoc(ctx, org, k.Kind)
	if err != nil {
		return err
	}
	conflict := problem("revision_conflict")
	conflict.Current = secretDTO(k, cur)
	return conflict
}
func (a *App) putCatherdSecret(w http.ResponseWriter, r *http.Request) error {
	k, err := a.secretKindParam(r)
	if err != nil {
		return err
	}
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	var req PutCatherdSecretRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if err = validateSecretValue(req.Secret); err != nil {
		return err
	}
	return a.replaceSecret(w, r, k, expected, &req.Secret)
}
func (a *App) deleteCatherdSecret(w http.ResponseWriter, r *http.Request) error {
	k, err := a.secretKindParam(r)
	if err != nil {
		return err
	}
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	return a.replaceSecret(w, r, k, expected, nil)
}

// replaceSecret follows the connection-credential sequence: put the new item, swap the reference in a transaction,
// delete the old item best effort. Removing clears the reference and then deletes the item.
func (a *App) replaceSecret(w http.ResponseWriter, r *http.Request, k secretKind, expected int64, secret *string) error {
	org := r.PathValue("orgId")
	var newRef, oldRef *string
	if secret != nil {
		newRef = ptr(newID("cred"))
		if err := a.Creds.Put(r.Context(), *newRef, *secret); err != nil {
			return problem("credential_store_unavailable")
		}
	}
	var doc CatherdSecretDoc
	err := a.DB.Txn(r.Context(), func(ctx context.Context) error {
		now := time.Now().UTC()
		state, kind := "none", "catherd_secret.removed"
		if newRef != nil {
			state, kind = "set", "catherd_secret.set"
		}
		coll := a.DB.Coll("catherd_secrets")
		switch {
		case expected == 0:
			if secret == nil { // nothing was ever stored
				doc = CatherdSecretDoc{Kind: k.Kind, State: "none"}
				return nil
			}
			doc = CatherdSecretDoc{ID: newID("csec"), OrgID: org, Kind: k.Kind, CredentialRef: newRef, State: state, UpdatedAt: &now, Active: true, Revision: 1, CreatedAt: now}
			if _, e := coll.InsertOne(ctx, doc); e != nil {
				if mongo.IsDuplicateKeyError(e) {
					return errSecretConflict
				}
				return e
			}
		default:
			e := coll.FindOneAndUpdate(ctx, bson.M{"org_id": org, "kind": k.Kind, "active": true, "revision": expected},
				bson.M{"$set": bson.M{"credential_ref": newRef, "state": state, "updated_at": now, "last_check": nil}, "$inc": bson.M{"revision": int64(1)}},
				options.FindOneAndUpdate().SetReturnDocument(options.Before)).Decode(&doc)
			if errors.Is(e, mongo.ErrNoDocuments) {
				return errSecretConflict
			}
			if e != nil {
				return e
			}
			oldRef = doc.CredentialRef
			doc.CredentialRef, doc.State, doc.UpdatedAt, doc.LastCheck, doc.Revision = newRef, state, &now, nil, doc.Revision+1
			if secret == nil && oldRef == nil {
				return nil // already removed
			}
		}
		return a.AppendEvent(ctx, org, kind, Subject{"catherd_secret", k.Kind}, bson.M{"kind": k.Kind}, &doc.Revision, nil, false)
	})
	if errors.Is(err, errSecretConflict) {
		a.bestEffortDelete(r.Context(), newRef)
		return a.secretConflict(r.Context(), org, k)
	}
	if err != nil {
		a.bestEffortDelete(r.Context(), newRef)
		return err
	}
	a.Committed()
	a.bestEffortDelete(r.Context(), oldRef)
	w.Header().Set("ETag", etag(doc.Revision))
	writeJSON(w, 200, secretDTO(k, &doc))
	return nil
}
func (a *App) checkCatherdSecret(w http.ResponseWriter, r *http.Request) error {
	k, err := a.secretKindParam(r)
	if err != nil {
		return err
	}
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org := r.PathValue("orgId")
	route := "POST /api/v1/orgs/{orgId}/catherd/secrets/{kind}/check"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "kind": k.Kind}, nil), func(ctx context.Context) (Output, error) {
		d, err := a.secretDoc(ctx, org, k.Kind)
		if err != nil {
			return Output{}, err
		}
		if d == nil || d.State != "set" {
			return Output{}, validation("kind", "no key is set")
		}
		return a.startOperation(ctx, org, "catherd_secret_check", Subject{"catherd_secret", k.Kind}, "catherd_secret_check:"+k.Kind, 1, bson.M{"kind": k.Kind})
	})
}

// secretEnvFor reads the named environment secrets from the credential store right before an exec. Names without a
// stored secret are simply absent. The result must only be handed to the one catherd call that needs it.
func (a *App) secretEnvFor(ctx context.Context, org string, need []string) (catherd.SecretEnv, error) {
	if len(need) == 0 {
		return nil, nil
	}
	var kinds []string
	for _, k := range secretKinds {
		for _, n := range need {
			if k.Env == n {
				kinds = append(kinds, k.Kind)
			}
		}
	}
	docs, err := findMany[CatherdSecretDoc](ctx, a.DB, "catherd_secrets", bson.M{"org_id": org, "active": true, "state": "set", "kind": bson.M{"$in": kinds}}, bson.D{{Key: "_id", Value: 1}}, 20)
	if err != nil {
		return nil, err
	}
	out := catherd.SecretEnv{}
	for _, d := range docs {
		k, ok := secretKindOf(d.Kind)
		if !ok || d.CredentialRef == nil {
			continue
		}
		v, found, err := a.Creds.Get(ctx, *d.CredentialRef)
		if err != nil {
			return nil, &JobFailure{Code: "credential_store_unavailable", Message: "the credential store is unavailable"}
		}
		if found && v != "" {
			out[k.Env] = v
		}
	}
	return out, nil
}
