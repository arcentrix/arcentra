// Copyright 2025 Arcade Team
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

package channel

import (
	"context"
	"fmt"
	"net/http"

	"github.com/arcentrix/arcade/internal/pkg/notify/auth"
	"github.com/arcentrix/arcade/pkg/log"
	"github.com/go-resty/resty/v2"
)

// SlackChannel implements Slack notification channel
type SlackChannel struct {
	webhookURL   string
	authProvider auth.IAuthProvider
	client       *resty.Client
}

// NewSlackChannel creates a new Slack notification channel
func NewSlackChannel(webhookURL string) *SlackChannel {
	return &SlackChannel{
		webhookURL: webhookURL,
		client:     resty.New(),
	}
}

// SetAuth sets authentication provider (Slack webhook typically doesn't need extra auth)
func (c *SlackChannel) SetAuth(provider auth.IAuthProvider) error {
	if provider == nil {
		return nil
	}

	c.authProvider = provider
	return provider.Validate()
}

// GetAuth gets the authentication provider
func (c *SlackChannel) GetAuth() auth.IAuthProvider {
	return c.authProvider
}

// Send sends message to Slack
func (c *SlackChannel) Send(ctx context.Context, message string) error {
	if err := c.Validate(); err != nil {
		return err
	}

	payload := map[string]interface{}{
		"text": message,
	}

	return c.sendRequest(ctx, payload)
}

// SendWithTemplate sends message using template with blocks/attachments
func (c *SlackChannel) SendWithTemplate(ctx context.Context, template string, data map[string]interface{}) error {
	if err := c.Validate(); err != nil {
		return err
	}

	// Support Slack blocks format
	payload := map[string]interface{}{
		"blocks": []map[string]interface{}{
			{
				"type": "section",
				"text": map[string]interface{}{
					"type": "mrkdwn",
					"text": template,
				},
			},
		},
	}

	// Add additional data if provided
	if data != nil {
		if attachments, ok := data["attachments"].([]interface{}); ok {
			payload["attachments"] = attachments
		}
	}

	return c.sendRequest(ctx, payload)
}

// sendRequest sends HTTP request
func (c *SlackChannel) sendRequest(ctx context.Context, payload map[string]interface{}) error {
	req := c.client.R().SetContext(ctx)

	// Add authentication header
	if c.authProvider != nil {
		key, value := c.authProvider.GetAuthHeader()
		if key != "" && value != "" {
			req.SetHeader(key, value)
		}
	}

	req.SetHeader("Content-Type", "application/json")
	req.SetBody(payload)

	resp, err := req.Post(c.webhookURL)
	if err != nil {
		log.Errorw("slack send request failed", "error", err)
		return fmt.Errorf("failed to send request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		log.Errorw("slack request failed", "statusCode", resp.StatusCode(), "response", resp.String())
		return fmt.Errorf("slack request failed with status %d", resp.StatusCode())
	}

	// Check response body
	respBody := resp.String()
	if respBody != "ok" {
		return fmt.Errorf("slack API error: %s", respBody)
	}

	return nil
}

// Receive receives messages (webhook callback)
func (c *SlackChannel) Receive(ctx context.Context, message string) error {
	return nil
}

// Validate validates the configuration
func (c *SlackChannel) Validate() error {
	if c.webhookURL == "" {
		return fmt.Errorf("slack webhook URL is required")
	}
	if c.authProvider != nil {
		return c.authProvider.Validate()
	}
	return nil
}

// Close closes the connection
func (c *SlackChannel) Close() error {
	return nil
}
