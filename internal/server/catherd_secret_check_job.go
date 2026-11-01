package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"io"
	"net/http"
	"strings"
	"time"
)

// verifySecret makes one authenticated GET (no inference, no retry, no redirects, 20 s). The response body is never
// read into a result. Results: ok, refused, unchecked, unverifiable.
func (a *App) verifySecret(ctx context.Context, k secretKind, value string) (status, detail string) {
	if !k.verifiable() {
		return "unverifiable", "this key cannot be checked without running a model"
	}
	base := k.Base
	if o, ok := a.secretBase[k.Kind]; ok {
		base = o
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+k.Path, nil)
	if err != nil {
		return "unchecked", "the check could not be started"
	}
	req.Header.Set(k.Header, k.Scheme+value)
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "unchecked", "the check timed out"
		}
		return "unchecked", "the service could not be reached"
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return "ok", "HTTP " + resp.Status[:3]
	case resp.StatusCode == 401, resp.StatusCode == 403 && k.Refused403:
		return "refused", "HTTP " + resp.Status[:3]
	}
	return "unchecked", "HTTP " + resp.Status[:3]
}
func (a *App) runCatherdSecretCheck(ctx context.Context, job JobDoc, lease Lease) error {
	if job.OperationID == nil {
		return &JobFailure{Code: "no_operation", Message: "job has no operation"}
	}
	kind, _ := job.Payload["kind"].(string)
	k, ok := secretKindOf(kind)
	if !ok {
		return &JobFailure{Code: "bad_payload", Message: "unknown key kind"}
	}
	if err := a.fenced(ctx, lease, func(sc context.Context) error { return a.transitionOperation(sc, job, "running", nil) }); err != nil {
		return err
	}
	d, err := a.secretDoc(ctx, job.OrgID, kind)
	if err != nil {
		return err
	}
	if d == nil || d.State != "set" || d.CredentialRef == nil {
		return &JobFailure{Code: "secret_not_set", Message: "no key is set"}
	}
	value, found, err := a.Creds.Get(ctx, *d.CredentialRef)
	if err != nil {
		return &JobFailure{Code: "credential_store_unavailable", Message: "the credential store is unavailable"}
	}
	if !found {
		return &JobFailure{Code: "secret_not_set", Message: "the key is missing from the credential store"}
	}
	status, detail := a.verifySecret(ctx, k, value)
	detail = strings.ReplaceAll(detail, value, "[REDACTED]")
	if len(detail) > 300 {
		detail = detail[:300]
	}
	return a.fenced(ctx, lease, func(sc context.Context) error {
		now := time.Now().UTC()
		// Only the key that was checked receives the result; a key replaced meanwhile keeps no stale check.
		if _, err := a.DB.Coll("catherd_secrets").UpdateOne(sc, bson.M{"org_id": job.OrgID, "kind": kind, "active": true, "credential_ref": *d.CredentialRef},
			bson.M{"$set": bson.M{"last_check": SecretCheck{Status: status, CheckedAt: now, Detail: detail, OperationID: *job.OperationID}}}); err != nil {
			return err
		}
		if err := a.transitionOperation(sc, job, "succeeded", bson.M{"kind": kind, "status": status}); err != nil {
			return err
		}
		if err := a.AppendEvent(sc, job.OrgID, "catherd_secret.checked", Subject{"catherd_secret", kind}, bson.M{"kind": kind, "status": status}, nil, job.OperationID, true); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
}
