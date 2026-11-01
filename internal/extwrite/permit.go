package extwrite

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// NotPermittedError is returned by Execute when the decision made immediately before the write is not execute.
type NotPermittedError struct{ Decision Decision }

func (e *NotPermittedError) Error() string {
	return fmt.Sprintf("extwrite: the write is not permitted (%s: %v)", e.Decision.outcome, e.Decision.reasons)
}

// Gate decides and executes external writes. Load reads the current settings, kill switch, credential presence and
// approval for the operation; Execute calls it again right before the transport is used.
type Gate struct {
	Scope      Scope
	Load       func(ctx context.Context, op OpView) (DecideInput, error)
	Credential func(ctx context.Context, connectionID string) (string, error)
	Dialer     *Dialer
}

// Decide loads the current inputs and decides.
func (g *Gate) Decide(ctx context.Context, op OpView) (Decision, error) {
	in, err := g.Load(ctx, op)
	if err != nil {
		return Decision{}, err
	}
	in.Scope = g.Scope
	in.Op = op
	return Decide(in), nil
}

// Permit is the capability to send one write. It exists only inside Execute; the write credential and the write
// HTTP client cannot be obtained any other way.
type Permit struct {
	g   *Gate
	ctx context.Context
	op  OpView
	d   Decision
}

// Scope is the guard scope of this write.
func (p *Permit) Scope() Scope { return p.d.scope }

// Credential returns the write credential for the operation's connection.
func (p *Permit) Credential() (string, error) {
	if p.g.Credential == nil {
		return "", errors.New("extwrite: no credential source")
	}
	return p.g.Credential(p.ctx, p.op.ConnectionID)
}

// Client is the loopback-guarded HTTP client for writes.
func (p *Permit) Client() *http.Client { return NewClient(p.d.scope, p.g.Dialer, nil) }

// CheckURL applies the loopback rule to a URL the write is about to contact.
func (p *Permit) CheckURL(raw string, git bool) error { return CheckURL(p.d.scope, raw, git) }

// Execute re-runs Decide and calls fn only when the new decision is still execute for the same intent hash.
func (g *Gate) Execute(ctx context.Context, d Decision, op OpView, fn func(*Permit) error) error {
	now, err := g.Decide(ctx, op)
	if err != nil {
		return err
	}
	if now.outcome != OutcomeExecute || d.outcome != OutcomeExecute || now.hash != d.hash || now.action != d.action {
		return &NotPermittedError{Decision: now}
	}
	return fn(&Permit{g: g, ctx: ctx, op: op, d: now})
}
