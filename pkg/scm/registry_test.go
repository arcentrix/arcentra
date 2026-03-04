// Copyright 2026 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
		CommitID:     "c",
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

func (p *dummyProvider) CreateChangeRequest(_ context.Context, _ ChangeRequestInput) (string, error) {
	return "https://example.com/pr/1", nil
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
