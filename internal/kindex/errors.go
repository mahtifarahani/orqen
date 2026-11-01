package kindex

import (
	"errors"
	"fmt"
)

// Kind classifies an adapter failure.
type Kind string

const (
	KindNotConfigured Kind = "not_configured"
	KindUnavailable   Kind = "unavailable" // cannot start, sandbox missing, spawn error
	KindFailed        Kind = "failed"      // non-zero exit
	KindTimeout       Kind = "timeout"
	KindInvalidOutput Kind = "invalid_output" // not JSON, schema violation, oversize
	KindBadVault      Kind = "bad_vault"      // vault path outside the org directory
	KindVaultBusy     Kind = "vault_busy"
)

type Error struct {
	Kind Kind
	Verb string
	Msg  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("knowledge index %s (%s): %s", e.Kind, e.Verb, e.Msg)
}

func IsKind(err error, k Kind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == k
}

func errf(k Kind, verb, format string, a ...any) *Error {
	return &Error{Kind: k, Verb: verb, Msg: fmt.Sprintf(format, a...)}
}
