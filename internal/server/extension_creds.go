package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"orqen/internal/extensions"
)

// extCredentialField resolves the {field} of a credential route against the manifest.
func extCredentialField(m *extensions.Manifest, field string) (extensions.CredentialField, error) {
	for _, c := range m.CredentialFields {
		if c.Field == field {
			return c, nil
		}
	}
	return extensions.CredentialField{}, problem("credential_field_unknown")
}

// retryExtUpdate runs an If-Match-free update (credentials have no precondition) and retries on a lost race.
func (a *App) retryExtUpdate(ctx context.Context, org string, m *extensions.Manifest, event string, payload bson.M, f func(*ExtensionStateDoc) error) (ExtensionStateDoc, error) {
	var last error
	for i := 0; i < 4; i++ {
		cur, err := a.extState(ctx, org, m.ID)
		if err != nil {
			return ExtensionStateDoc{}, err
		}
		d, err := a.extUpdate(ctx, org, m, cur.Revision, event, payload, f)
		var ae *AppError
		if errors.As(err, &ae) && ae.Code == "revision_conflict" {
			last = err
			continue
		}
		return d, err
	}
	return ExtensionStateDoc{}, last
}

// setExtensionCredential stores the value in the credential store under a fresh cred_ ref. The value is never written
// to MongoDB, an event, a log or a response; the document keeps only the reference.
func (a *App) setExtensionCredential(w http.ResponseWriter, r *http.Request) error {
	m, err := manifestOf(r)
	if err != nil {
		return err
	}
	field := r.PathValue("field")
	if _, err = extCredentialField(m, field); err != nil {
		return err
	}
	var req struct {
		Value *string `json:"value"`
	}
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if req.Value == nil || len(*req.Value) < 1 || len(*req.Value) > 8192 || strings.ContainsAny(*req.Value, "\r\n\x00") {
		return validation("value", "must be 1-8192 bytes on one line")
	}
	org := r.PathValue("orgId")
	ref := newID("cred")
	if err = a.Creds.Put(r.Context(), ref, *req.Value); err != nil {
		return problem("credential_store_unavailable")
	}
	var old string
	_, err = a.retryExtUpdate(r.Context(), org, m, "extension.credential_set", bson.M{"extension_id": m.ID, "field": field}, func(d *ExtensionStateDoc) error {
		creds := map[string]ExtCred{}
		for k, v := range d.Credentials {
			creds[k] = v
		}
		old = creds[field].Ref
		creds[field] = ExtCred{Ref: ref, SetAt: time.Now().UTC()}
		d.Credentials = creds
		return nil
	})
	if err != nil {
		_ = a.Creds.Delete(r.Context(), ref)
		return err
	}
	if old != "" {
		_ = a.Creds.Delete(r.Context(), old)
	}
	writeJSON(w, 200, bson.M{"field": field, "set": true})
	return nil
}

func (a *App) deleteExtensionCredential(w http.ResponseWriter, r *http.Request) error {
	m, err := manifestOf(r)
	if err != nil {
		return err
	}
	field := r.PathValue("field")
	if _, err = extCredentialField(m, field); err != nil {
		return err
	}
	var old string
	_, err = a.retryExtUpdate(r.Context(), r.PathValue("orgId"), m, "extension.credential_deleted", bson.M{"extension_id": m.ID, "field": field}, func(d *ExtensionStateDoc) error {
		creds := map[string]ExtCred{}
		for k, v := range d.Credentials {
			creds[k] = v
		}
		old = creds[field].Ref
		delete(creds, field)
		d.Credentials = creds
		return nil
	})
	if err != nil {
		return err
	}
	if old != "" {
		_ = a.Creds.Delete(r.Context(), old)
	}
	writeJSON(w, 200, bson.M{"field": field, "set": false})
	return nil
}

// extensionSecretEnv resolves the credential values of an extension into the child environment entries. It is the
// only place a stored extension secret is read; the result goes straight into extensions.BuildEnv.
func (a *App) extensionSecretEnv(ctx context.Context, d ExtensionStateDoc, m *extensions.Manifest) (map[string]string, []string, error) {
	values, allowed := map[string]string{}, []string{}
	for _, c := range m.CredentialFields {
		allowed = append(allowed, c.Env)
		ref := d.Credentials[c.Field].Ref
		if ref == "" {
			continue
		}
		v, ok, err := a.Creds.Get(ctx, ref)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			values[c.Env] = v
		}
	}
	return values, allowed, nil
}
