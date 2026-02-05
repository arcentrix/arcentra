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

package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/arcentrix/arcentra/internal/pkg/notify/auth"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/go-resty/resty/v2"
)

// DiscordChannel implements Discord notification channel
type DiscordChannel struct {
	webhookURL   string
	username     string // optional: custom bot username
	avatarURL    string // optional: custom bot avatar
	authProvider auth.IAuthProvider
	client       *resty.Client
}

// NewDiscordChannel creates a new Discord notification channel
func NewDiscordChannel(webhookURL string) *DiscordChannel {
	return &DiscordChannel{
		webhookURL: webhookURL,
		client:     resty.New(),
	}
}

// NewDiscordChannelWithCustom creates a new Discord channel with custom username and avatar
func NewDiscordChannelWithCustom(webhookURL, username, avatarURL string) *DiscordChannel {
	return &DiscordChannel{
		webhookURL: webhookURL,
		username:   username,
		avatarURL:  avatarURL,
		client:     resty.New(),
	}
}

// SetAuth sets authentication provider
func (c *DiscordChannel) SetAuth(provider auth.IAuthProvider) error {
	if provider == nil {
		return nil
	}

	c.authProvider = provider
	return provider.Validate()
}

// GetAuth gets the authentication provider
func (c *DiscordChannel) GetAuth() auth.IAuthProvider {
	return c.authProvider
}

// Send sends message to Discord
func (c *DiscordChannel) Send(ctx context.Context, message string) error {
	if err := c.Validate(); err != nil {
		return err
	}

	payload := map[string]interface{}{
		"content": message,
	}

	if c.username != "" {
		payload["username"] = c.username
	}
	if c.avatarURL != "" {
		payload["avatar_url"] = c.avatarURL
	}

	return c.sendRequest(ctx, payload)
}

// SendWithTemplate sends message using template with embeds
func (c *DiscordChannel) SendWithTemplate(ctx context.Context, template string, data map[string]interface{}) error {
	if err := c.Validate(); err != nil {
		return err
	}

	payload := map[string]interface{}{}

	if c.username != "" {
		payload["username"] = c.username
	}
	if c.avatarURL != "" {
		payload["avatar_url"] = c.avatarURL
	}

	// Support Discord embeds format
	embed := map[string]interface{}{
		"description": template,
	}

	// Add additional embed fields from data
	if data != nil {
		if title, ok := data["title"].(string); ok {
			embed["title"] = title
		}
		if color, ok := data["color"].(int); ok {
			embed["color"] = color
		}
		if fields, ok := data["fields"].([]interface{}); ok {
			embed["fields"] = fields
		}
		if footer, ok := data["footer"].(map[string]interface{}); ok {
			embed["footer"] = footer
		}
		if thumbnail, ok := data["thumbnail"].(map[string]interface{}); ok {
			embed["thumbnail"] = thumbnail
		}
		if image, ok := data["image"].(map[string]interface{}); ok {
			embed["image"] = image
		}
	}

	payload["embeds"] = []map[string]interface{}{embed}

	return c.sendRequest(ctx, payload)
}

// sendRequest sends HTTP request
func (c *DiscordChannel) sendRequest(ctx context.Context, payload map[string]interface{}) error {
	req := c.client.R().SetContext(ctx)

	// Add authentication header if provided
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
		log.Errorw("discord send request failed", "error", err)
		return fmt.Errorf("failed to send request: %w", err)
	}

	// Discord returns 204 No Content on success
	if resp.StatusCode() != http.StatusNoContent && resp.StatusCode() != http.StatusOK {
		log.Errorw("discord request failed", "statusCode", resp.StatusCode(), "response", resp.String())

		// Try to parse error message
		var errorResp map[string]interface{}
		if err := json.Unmarshal(resp.Body(), &errorResp); err == nil {
			if message, ok := errorResp["message"].(string); ok {
				return fmt.Errorf("discord API error: %s", message)
			}
		}

		return fmt.Errorf("discord request failed with status %d", resp.StatusCode())
	}

	return nil
}

// Receive receives messages (webhook callback)
func (c *DiscordChannel) Receive(ctx context.Context, message string) error {
	return nil
}

// Validate validates the configuration
func (c *DiscordChannel) Validate() error {
	if c.webhookURL == "" {
		return fmt.Errorf("discord webhook URL is required")
	}
	if c.authProvider != nil {
		return c.authProvider.Validate()
	}
	return nil
}

// Close closes the connection
func (c *DiscordChannel) Close() error {
	return nil
}
