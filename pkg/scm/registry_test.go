package scm

import (
	"context"
	"testing"
	"time"
)

type dummyProvider struct {
	kind ProviderKind
}

func (p *dummyProvider) Kind() ProviderKind { return p.kind }
func (p *dummyProvider) Capabilities() CapSet {
	return CapSet{CapWebhookVerify: true, CapWebhookParse: true, CapPollEvents: true}
}

func (p *dummyProvider) VerifyWebhook(_ context.Context, _ WebhookRequest, secret string) error {
	if secret == "" {
		return Err("missing secret")
	}
	return nil
}

func (p *dummyProvider) ParseWebhook(_ context.Context, _ WebhookRequest) ([]Event, error) {
	return []Event{{
		ProviderKind: p.kind,
		EventType:    EventTypePush,
		Repo:         Repo{Host: "example.com", Owner: "o", Name: "n"},
		ActorName:    "actor",
		CommitId:     "c",
		Ref:          "refs/heads/main",
		OccurredAt:   time.Unix(1, 0).UTC(),
	}}, nil
}

func (p *dummyProvider) PollEvents(_ context.Context, _ Repo, cursor Cursor) ([]Event, Cursor, error) {
	next := cursor
	next.Since = time.Unix(2, 0).UTC()
	return []Event{{
		ProviderKind: p.kind,
		EventType:    EventTypeTag,
		Repo:         Repo{Host: "example.com", Owner: "o", Name: "n"},
		Ref:          "refs/tags/v1.0.0",
		OccurredAt:   time.Unix(2, 0).UTC(),
	}}, next, nil
}

func TestNewProvider_NotRegistered(t *testing.T) {
	_, err := NewProvider(ProviderConfig{Kind: ProviderKind("not-exists")})
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestNewProvider_RegisterAndCreate(t *testing.T) {
	kind := ProviderKind("dummy-test")
	Register(kind, func(cfg ProviderConfig) (Provider, error) {
		return &dummyProvider{kind: cfg.Kind}, nil
	})

	p, err := NewProvider(ProviderConfig{Kind: kind})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Kind() != kind {
		t.Fatalf("unexpected kind: %s", p.Kind())
	}
}

// Err is a tiny test-local error type to avoid extra deps.
type Err string

func (e Err) Error() string { return string(e) }
