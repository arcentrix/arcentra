// Copyright 2025 Arcentra Team
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

package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	scmpkg "github.com/arcentrix/arcentra/pkg/scm"
	_ "github.com/arcentrix/arcentra/pkg/scm/builtin"
	"github.com/bytedance/sonic"
)

// ScmParseWebhookArgs contains arguments for webhook.parse
// Keep the shape aligned with pkg/plugins/scm for easier migration.
type ScmParseWebhookArgs struct {
	Provider   scmpkg.ProviderConfig `json:"provider"`
	Secret     string                `json:"secret"`
	Headers    map[string]string     `json:"headers"`
	Body       string                `json:"body,omitempty"`
	BodyBase64 string                `json:"bodyBase64,omitempty"`
}

// ScmPollEventsArgs contains arguments for events.poll
// Keep the shape aligned with pkg/plugins/scm for easier migration.
type ScmPollEventsArgs struct {
	Provider scmpkg.ProviderConfig `json:"provider"`
	Repo     scmpkg.Repo           `json:"repo"`
	Cursor   scmpkg.Cursor         `json:"cursor"`
}

func (m *Manager) handleScmWebhookParse(ctx context.Context, params json.RawMessage, _ *Options) (json.RawMessage, error) {
	var args ScmParseWebhookArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse webhook params: %w", err)
	}
	if args.Provider.Kind == "" {
		return nil, fmt.Errorf("provider.kind is required")
	}

	prov, err := scmpkg.NewProvider(args.Provider)
	if err != nil {
		return nil, err
	}

	body := []byte(args.Body)
	if args.BodyBase64 != "" {
		raw, err := base64.StdEncoding.DecodeString(args.BodyBase64)
		if err != nil {
			return nil, fmt.Errorf("decode bodyBase64: %w", err)
		}
		body = raw
	}

	req := scmpkg.WebhookRequest{
		Headers: args.Headers,
		Body:    body,
	}

	if err := prov.VerifyWebhook(ctx, req, args.Secret); err != nil {
		return nil, err
	}
	events, err := prov.ParseWebhook(ctx, req)
	if err != nil {
		return nil, err
	}

	return sonic.Marshal(map[string]any{
		"events": events,
	})
}

func (m *Manager) handleScmEventsPoll(ctx context.Context, params json.RawMessage, _ *Options) (json.RawMessage, error) {
	var args ScmPollEventsArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse poll params: %w", err)
	}
	if args.Provider.Kind == "" {
		return nil, fmt.Errorf("provider.kind is required")
	}

	prov, err := scmpkg.NewProvider(args.Provider)
	if err != nil {
		return nil, err
	}

	// Keep a sane default timeout; caller may already pass a deadline via ctx.
	pollCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		pollCtx, cancel = context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
	}

	events, next, err := prov.PollEvents(pollCtx, args.Repo, args.Cursor)
	if err != nil {
		return nil, err
	}

	return sonic.Marshal(map[string]any{
		"events":     events,
		"nextCursor": next,
	})
}

