package api

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel kinds, matched with errors.Is.
var (
	ErrUnauthorized = errors.New("unauthorized (401)")
	ErrForbidden    = errors.New("forbidden (403)")
	ErrNotFound     = errors.New("not found (404)")
	ErrConflict     = errors.New("conflict (409)")
	ErrCooldown     = errors.New("cooldown active (429)")
	ErrServer       = errors.New("server error")
	ErrHTTP         = errors.New("unexpected HTTP status")
	ErrUnreachable  = errors.New("relay unreachable")
	ErrNoToken      = errors.New("no token set")
	ErrTooLarge     = errors.New("response too large")
	ErrBadID        = errors.New("invalid request id")
)

// Error is a non-2xx answer from the relay.
type Error struct {
	Status      int
	Body        string        // the response body, trimmed and length-capped
	RemainingMs int64         // 429: remaining_ms from the JSON body, else 0
	RetryAfter  time.Duration // 429: the Retry-After header, else 0
}

func (e *Error) Error() string {
	b := strings.TrimSpace(e.Body)
	if b == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, b)
}

// Is makes errors.Is(err, ErrConflict) and friends match on the status code.
func (e *Error) Is(target error) bool { return target == kindFor(e.Status) }

// NotApprover reports a 403 that says the presented token cannot approve,
// meaning it is a valid token but not the approver token.
func (e *Error) NotApprover() bool {
	return e.Status == 403 && strings.Contains(e.Body, "cannot approve")
}

// Remaining is the cooldown left according to a 429: the body's figure when it
// has one, otherwise the Retry-After header.
func (e *Error) Remaining() time.Duration {
	if e.RemainingMs > 0 {
		return time.Duration(e.RemainingMs) * time.Millisecond
	}
	return e.RetryAfter
}

func kindFor(status int) error {
	switch {
	case status == 401:
		return ErrUnauthorized
	case status == 403:
		return ErrForbidden
	case status == 404:
		return ErrNotFound
	case status == 409:
		return ErrConflict
	case status == 429:
		return ErrCooldown
	case status >= 500:
		return ErrServer
	}
	return ErrHTTP
}
