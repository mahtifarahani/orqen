// Package providers holds the GitLab and Jira REST clients. It must not import internal/server.
package providers

import (
	"errors"
	"strconv"
	"time"
)

type Kind string

const (
	KindCredentialExpired Kind = "credential_expired"
	KindPermissionDenied  Kind = "permission_denied"
	KindUnsupported       Kind = "unsupported_capability"
	KindRateLimited       Kind = "rate_limited"
	KindNetwork           Kind = "network_unavailable"
	KindInvalidOutput     Kind = "invalid_output"
	KindNotFound          Kind = "not_found"
	KindInvalidRequest    Kind = "invalid_request"
	KindConfig            Kind = "invalid_configuration"
)

// Error never carries a secret, request header or response body beyond a short upstream message.
type Error struct {
	Kind       Kind
	Code       string
	Message    string
	Status     int
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Retryable() bool {
	return e.Kind == KindNetwork || e.Kind == KindRateLimited
}
func newError(kind Kind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message}
}

// NewError builds a provider error for callers that detect a failure before any request (for example a missing credential).
func NewError(kind Kind, code, message string) *Error { return newError(kind, code, message) }

// AsError returns the provider error inside err, if any.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
func statusMessage(status int, what string) string {
	return what + " (HTTP " + strconv.Itoa(status) + ")"
}
