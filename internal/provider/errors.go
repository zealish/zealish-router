package provider

import (
	"errors"
	"fmt"
	"strings"
)

// Retryable upstream failure classes. The routing engine advances its fallback
// chain when an error unwraps to one of these; anything else is terminal.
var (
	// ErrTimeout marks a request that exceeded its deadline.
	ErrTimeout = errors.New("provider: timeout")
	// ErrRateLimited marks an upstream 429 response.
	ErrRateLimited = errors.New("provider: rate limited")
	// ErrUpstream5xx marks an upstream 5xx response.
	ErrUpstream5xx = errors.New("provider: upstream server error")
	// ErrConnection marks a transport-level failure before a response arrived.
	ErrConnection = errors.New("provider: connection error")
)

// ErrUnsupported marks an endpoint the upstream dialect does not implement.
// It is terminal: no other provider in the chain is more likely to succeed
// for a request the operator pointed at the wrong dialect.
var ErrUnsupported = errors.New("provider: endpoint not supported")

// Error carries the upstream context of a failed provider call. Kind, when set,
// is one of the retryable sentinels above.
type Error struct {
	Provider string
	Status   int
	Kind     error
	Message  string
}

func (e *Error) Error() string {
	switch {
	case e.Status > 0 && e.Message != "":
		return fmt.Sprintf("provider %s: upstream status %d: %s", e.Provider, e.Status, e.Message)
	case e.Status > 0:
		return fmt.Sprintf("provider %s: upstream status %d", e.Provider, e.Status)
	case e.Message != "":
		return fmt.Sprintf("provider %s: %s", e.Provider, e.Message)
	default:
		return fmt.Sprintf("provider %s: request failed", e.Provider)
	}
}

// Unwrap exposes the retryable sentinel, if any.
func (e *Error) Unwrap() error { return e.Kind }

// Retryable reports whether err is a transient upstream failure that justifies
// trying the next provider in a fallback chain.
func Retryable(err error) bool {
	return errors.Is(err, ErrTimeout) ||
		errors.Is(err, ErrRateLimited) ||
		errors.Is(err, ErrUpstream5xx) ||
		errors.Is(err, ErrConnection)
}

// redactionPlaceholder replaces a secret in text bound for logs or clients.
const redactionPlaceholder = "[REDACTED]"

// redact removes secret from msg. Upstreams routinely echo the credential back
// in their error payloads ("Incorrect API key provided: sk-…"), which would
// otherwise reach both the log and the caller.
func redact(secret, msg string) string {
	if secret == "" {
		return msg
	}
	return strings.ReplaceAll(msg, secret, redactionPlaceholder)
}
