package server

import (
	"context"
	"errors"
	"orqen/internal/providers"
	"strconv"
	"time"
)

// limiterFor returns the connection's concurrency gate; changing max_concurrency creates a new one.
func (a *App) limiterFor(conn ConnectionDoc) chan struct{} {
	key := conn.ID + ":" + strconv.Itoa(conn.MaxConcurrency)
	a.limMu.Lock()
	defer a.limMu.Unlock()
	if a.limiters == nil {
		a.limiters = map[string]chan struct{}{}
	}
	ch := a.limiters[key]
	if ch == nil {
		ch = make(chan struct{}, max(conn.MaxConcurrency, 1))
		a.limiters[key] = ch
	}
	return ch
}
func (a *App) providerOptions(conn ConnectionDoc) providers.Options {
	o := providers.Options{BaseURL: conn.BaseURL, Timeout: time.Duration(conn.TimeoutMS) * time.Millisecond}
	if conn.ProxyURL != nil {
		o.ProxyURL = *conn.ProxyURL
	}
	if conn.CustomCaPEM != nil {
		o.CustomCAPEM = *conn.CustomCaPEM
	}
	gate := a.limiterFor(conn)
	o.Acquire = func(ctx context.Context) (func(), error) {
		if until, ok := a.cooldown.Load(conn.ID); ok {
			if d := time.Until(until.(time.Time)); d > 0 {
				select {
				case <-time.After(d):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}
		select {
		case gate <- struct{}{}:
			return func() { <-gate }, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return o
}
func (a *App) providerAuth(ctx context.Context, conn ConnectionDoc) (providers.Auth, error) {
	auth := providers.Auth{Provider: conn.Provider, Deployment: conn.Deployment, Method: conn.AuthMethod}
	if conn.AuthUsername != nil {
		auth.Username = *conn.AuthUsername
	}
	if conn.CredentialRef == nil || conn.CredentialState != "set" {
		return auth, providers.NewError(providers.KindCredentialExpired, "credential_missing", "no credential is set")
	}
	secret, ok, err := a.Creds.Get(ctx, *conn.CredentialRef)
	if err != nil {
		var app *AppError
		if errors.As(err, &app) {
			return auth, app
		}
		return auth, problem("credential_store_unavailable")
	}
	if !ok {
		return auth, providers.NewError(providers.KindCredentialExpired, "credential_missing", "the stored credential could not be found")
	}
	auth.Secret = secret
	return auth, nil
}
func (a *App) codeHost(ctx context.Context, conn ConnectionDoc) (providers.CodeHost, error) {
	if conn.Provider != "gitlab" {
		return nil, providerMismatch()
	}
	auth, err := a.providerAuth(ctx, conn)
	if err != nil {
		return nil, err
	}
	return providers.NewGitLab(a.providerOptions(conn), auth), nil
}
func (a *App) issueSource(ctx context.Context, conn ConnectionDoc) (providers.IssueSource, error) {
	if conn.Provider != "jira" {
		return nil, providerMismatch()
	}
	auth, err := a.providerAuth(ctx, conn)
	if err != nil {
		return nil, err
	}
	return providers.NewJira(a.providerOptions(conn), auth, conn.Deployment), nil
}
func providerMismatch() error {
	return &AppError{Code: "provider_mismatch", Status: 422, Detail: "provider mismatch", Errors: []FieldError{{"connection_id", "the connection's provider does not support this operation"}}}
}

// docsSource builds the read-only Confluence client of a connection. The page cap is the adapter default.
func (a *App) docsSource(ctx context.Context, conn ConnectionDoc) (providers.DocsSource, error) {
	if conn.Provider != "confluence" {
		return nil, providerMismatch()
	}
	auth, err := a.providerAuth(ctx, conn)
	if err != nil {
		return nil, err
	}
	return providers.NewConfluence(a.providerOptions(conn), auth, conn.Deployment, 0), nil
}
