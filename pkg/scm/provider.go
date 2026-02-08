package scm

import (
	"context"
)

type ProviderConfig struct {
	Kind       ProviderKind `json:"kind"`
	BaseUrl    string       `json:"baseUrl,omitempty"`
	ApiBaseUrl string       `json:"apiBaseUrl,omitempty"`
	Token      string       `json:"token,omitempty"`
}

// WebhookRequest is a minimal, transport-agnostic webhook request envelope.
// Implementations should verify signatures based on the raw body bytes.
type WebhookRequest struct {
	Headers map[string]string `json:"headers"`
	Body    []byte            `json:"body"`
}

// Header returns the header value by key.
// It also performs a case-insensitive lookup for convenience.
func (r WebhookRequest) Header(key string) string {
	if r.Headers == nil {
		return ""
	}
	if v, ok := r.Headers[key]; ok {
		return v
	}
	// case-insensitive lookup without allocations for common cases
	for k, v := range r.Headers {
		if equalFold(k, key) {
			return v
		}
	}
	return ""
}

// Provider abstracts SCM vendors for webhook verification/parsing and API polling.
type Provider interface {
	// Kind returns the provider kind
	Kind() ProviderKind
	// Capabilities returns the provider capabilities
	Capabilities() CapSet
	// VerifyWebhook verifies the webhook signature
	VerifyWebhook(ctx context.Context, req WebhookRequest, secret string) error
	// ParseWebhook parses the webhook event
	ParseWebhook(ctx context.Context, req WebhookRequest) ([]Event, error)
	// PollEvents polls the events from the repository
	PollEvents(ctx context.Context, repo Repo, cursor Cursor) (events []Event, nextCursor Cursor, err error)
}

// equalFold compares two strings case-insensitively
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		aa := a[i]
		bb := b[i]
		if aa == bb {
			continue
		}
		// to lower ASCII
		if 'A' <= aa && aa <= 'Z' {
			aa = aa - 'A' + 'a'
		}
		if 'A' <= bb && bb <= 'Z' {
			bb = bb - 'A' + 'a'
		}
		if aa != bb {
			return false
		}
	}
	return true
}
