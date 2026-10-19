// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import (
	"errors"
	"net/http"
	"strconv"
	"time"
)

var ErrIncomplete = errors.New("provider pull request snapshot is incomplete")

type Reason string

const (
	ReasonConfiguration  Reason = "invalid_configuration"
	ReasonRequest        Reason = "invalid_request"
	ReasonTransport      Reason = "transport_or_budget"
	ReasonMalformed      Reason = "malformed_provider_evidence"
	ReasonIdentity       Reason = "repository_identity_mismatch"
	ReasonPagination     Reason = "incomplete_or_contradictory_pagination"
	ReasonMovement       Reason = "inventory_changed"
	ReasonLimit          Reason = "inventory_limit"
	ReasonRateLimit      Reason = "rate_limited"
	ReasonAuthentication Reason = "authentication_rejected"
	ReasonForbidden      Reason = "access_forbidden"
	ReasonStatus         Reason = "provider_status"
)

type Error struct {
	Reason             Reason
	StatusCode         int
	RetryAt            time.Time
	RateLimitRemaining *int64
	RateLimitResetAt   time.Time
}

func (e *Error) Error() string    { return ErrIncomplete.Error() + ": " + string(e.Reason) }
func (e *Error) Unwrap() error    { return ErrIncomplete }
func failure(reason Reason) error { return &Error{Reason: reason} }

// Untrusted rate metadata is bounded to one day and never enters observation identity.
func responseFailure(status int, h http.Header, now time.Time) *Error {
	e := &Error{Reason: ReasonStatus, StatusCode: status}
	switch status {
	case 401:
		e.Reason = ReasonAuthentication
	case 403:
		e.Reason = ReasonForbidden
	case 429:
		e.Reason = ReasonRateLimit
	}
	max := now.Add(24 * time.Hour)
	bound := func(t time.Time) time.Time {
		if t.Before(now) {
			return now.UTC()
		}
		if t.After(max) {
			return max.UTC()
		}
		return t.UTC()
	}
	if n, err := strconv.ParseInt(h.Get("Retry-After"), 10, 64); err == nil && n >= 0 {
		if n > 86400 {
			n = 86400
		}
		e.RetryAt = now.Add(time.Duration(n) * time.Second).UTC()
	} else if t, err := http.ParseTime(h.Get("Retry-After")); err == nil {
		e.RetryAt = bound(t)
	}
	remaining := h.Get("X-RateLimit-Remaining")
	if remaining == "" {
		remaining = h.Get("RateLimit-Remaining")
	}
	if n, err := strconv.ParseInt(remaining, 10, 64); err == nil && n >= 0 {
		e.RateLimitRemaining = &n
		if status == 403 && n == 0 {
			e.Reason = ReasonRateLimit
		}
	}
	reset := h.Get("X-RateLimit-Reset")
	if reset == "" {
		reset = h.Get("RateLimit-Reset")
	}
	if n, err := strconv.ParseInt(reset, 10, 64); err == nil && n > 0 {
		e.RateLimitResetAt = bound(time.Unix(n, 0))
		if e.RateLimitResetAt.After(e.RetryAt) {
			e.RetryAt = e.RateLimitResetAt
		}
	}
	if e.Reason == ReasonRateLimit && e.RetryAt.IsZero() {
		e.RetryAt = now.Add(time.Minute).UTC()
	}
	return e
}
