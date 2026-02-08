package scm

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	scmpkg "github.com/arcentrix/arcentra/pkg/scm"
	"github.com/bytedance/sonic"
)

type testProvider struct {
	kind scmpkg.ProviderKind
}

func (p *testProvider) Kind() scmpkg.ProviderKind { return p.kind }
func (p *testProvider) Capabilities() scmpkg.CapSet {
	return scmpkg.CapSet{
		scmpkg.CapWebhookVerify: true,
		scmpkg.CapWebhookParse:  true,
		scmpkg.CapPollEvents:    true,
	}
}
func (p *testProvider) VerifyWebhook(_ context.Context, req scmpkg.WebhookRequest, secret string) error {
	if secret != "s" {
		return errString("bad secret")
	}
	if string(req.Body) != "body" {
		return errString("bad body")
	}
	return nil
}
func (p *testProvider) ParseWebhook(_ context.Context, _ scmpkg.WebhookRequest) ([]scmpkg.Event, error) {
	return []scmpkg.Event{{
		ProviderKind: p.kind,
		EventType:    scmpkg.EventTypePush,
		Repo:         scmpkg.Repo{Host: "example.com", Owner: "o", Name: "n"},
		ActorName:    "a",
		CommitId:     "c",
		Ref:          "refs/heads/main",
		OccurredAt:   time.Unix(1, 0).UTC(),
	}}, nil
}
func (p *testProvider) PollEvents(_ context.Context, _ scmpkg.Repo, cursor scmpkg.Cursor) ([]scmpkg.Event, scmpkg.Cursor, error) {
	next := cursor
	next.Since = time.Unix(2, 0).UTC()
	return []scmpkg.Event{{
		ProviderKind: p.kind,
		EventType:    scmpkg.EventTypeTag,
		Repo:         scmpkg.Repo{Host: "example.com", Owner: "o", Name: "n"},
		Ref:          "refs/tags/v1.0.0",
		OccurredAt:   time.Unix(2, 0).UTC(),
	}}, next, nil
}

type errString string

func (e errString) Error() string { return string(e) }

func TestPlugin_WebhookParse(t *testing.T) {
	kind := scmpkg.ProviderKind("dummy-plugin")
	scmpkg.Register(kind, func(cfg scmpkg.ProviderConfig) (scmpkg.Provider, error) {
		return &testProvider{kind: cfg.Kind}, nil
	})

	p := New()
	if err := p.Init(json.RawMessage{}); err != nil {
		t.Fatalf("init: %v", err)
	}

	params := map[string]any{
		"provider": map[string]any{
			"kind": string(kind),
		},
		"secret":  "s",
		"headers": map[string]string{"x": "y"},
		"body":    "body",
	}
	b, err := sonic.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	out, err := p.Execute("webhook.parse", b, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var resp struct {
		Events []scmpkg.Event `json:"events"`
	}
	if err := sonic.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(resp.Events))
	}
	if resp.Events[0].ProviderKind != kind {
		t.Fatalf("unexpected kind: %s", resp.Events[0].ProviderKind)
	}
}

func TestPlugin_EventsPoll(t *testing.T) {
	kind := scmpkg.ProviderKind("dummy-plugin-poll")
	scmpkg.Register(kind, func(cfg scmpkg.ProviderConfig) (scmpkg.Provider, error) {
		return &testProvider{kind: cfg.Kind}, nil
	})

	p := New()
	if err := p.Init(json.RawMessage{}); err != nil {
		t.Fatalf("init: %v", err)
	}

	params := map[string]any{
		"provider": map[string]any{
			"kind": string(kind),
		},
		"repo": map[string]any{
			"host":  "example.com",
			"owner": "o",
			"name":  "n",
		},
		"cursor": map[string]any{
			"since": "1970-01-01T00:00:00Z",
		},
	}
	b, err := sonic.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	out, err := p.Execute("events.poll", b, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var resp struct {
		Events     []scmpkg.Event `json:"events"`
		NextCursor scmpkg.Cursor  `json:"nextCursor"`
	}
	if err := sonic.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(resp.Events))
	}
	if resp.NextCursor.Since.IsZero() {
		t.Fatalf("expected nextCursor.since to be set")
	}
}
