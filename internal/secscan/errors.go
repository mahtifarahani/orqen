package secscan

import (
	"errors"
	"fmt"
)

// ErrKind classifies an adapter failure.
type ErrKind string

const (
	KindUnavailable ErrKind = "unavailable"
	KindSandbox     ErrKind = "sandbox_unavailable"
	KindBadRequest  ErrKind = "bad_request" // a path outside the org directory, a URL target, a bad endpoint
	KindFailed      ErrKind = "failed"
)

// Error is a typed adapter failure. Msg never carries tool output or a secret.
type Error struct {
	Kind ErrKind
	Verb string
	Msg  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("security scan %s (%s): %s", e.Kind, e.Verb, e.Msg)
}

// IsKind reports whether err is an *Error of the kind.
func IsKind(err error, k ErrKind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == k
}

func errf(k ErrKind, verb, format string, a ...any) *Error {
	return &Error{Kind: k, Verb: verb, Msg: fmt.Sprintf(format, a...)}
}
