package codegraph

import (
	"errors"
	"fmt"
)

// ErrKind classifies an adapter failure.
type ErrKind string

const (
	KindUnavailable   ErrKind = "unavailable" // cannot start
	KindSandbox       ErrKind = "sandbox_unavailable"
	KindFailed        ErrKind = "failed" // non-zero exit
	KindTimeout       ErrKind = "timeout"
	KindInvalidOutput ErrKind = "invalid_output" // not JSON, schema violation, oversize
	KindBadPath       ErrKind = "bad_path"       // a path outside the org directory, or a bad target
)

// Error is a typed adapter failure. Msg never carries output of the tool.
type Error struct {
	Kind ErrKind
	Verb string
	Msg  string
}

func (e *Error) Error() string { return fmt.Sprintf("code graph %s (%s): %s", e.Kind, e.Verb, e.Msg) }

// IsKind reports whether err is an *Error of the kind.
func IsKind(err error, k ErrKind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == k
}

func errf(k ErrKind, verb, format string, a ...any) *Error {
	return &Error{Kind: k, Verb: verb, Msg: fmt.Sprintf(format, a...)}
}
