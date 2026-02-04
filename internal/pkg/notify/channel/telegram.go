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
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/arcentrix/arcade/internal/pkg/notify/auth"
	"github.com/arcentrix/arcade/pkg/log"
	"github.com/go-resty/resty/v2"
)

// TelegramChannel implements Telegram notification channel
type TelegramChannel struct {
	botToken     string
	chatID       string
	parseMode    string // optional: "Markdown", "MarkdownV2", "HTML"
	authProvider auth.IAuthProvider
	client       *resty.Client
}

// NewTelegramChannel creates a new Telegram notification channel
func NewTelegramChannel(botToken, chatID string) *TelegramChannel {
	return &TelegramChannel{
		botToken:  botToken,
		chatID:    chatID,
		parseMode: "Markdown", // default to Markdown
		client:    resty.New(),
	}
}

// NewTelegramChannelWithParseMode creates a new Telegram channel with custom parse mode
func NewTelegramChannelWithParseMode(botToken, chatID, parseMode string) *TelegramChannel {
	return &TelegramChannel{
		botToken:  botToken,
		chatID:    chatID,
		parseMode: parseMode,
		client:    resty.New(),
	}
}

// SetAuth sets authentication provider
func (c *TelegramChannel) SetAuth(provider auth.IAuthProvider) error {
	if provider == nil {
		return nil
	}

	c.authProvider = provider
	return provider.Validate()
}

// GetAuth gets the authentication provider
func (c *TelegramChannel) GetAuth() auth.IAuthProvider {
	return c.authProvider
}

// Send sends message to Telegram
func (c *TelegramChannel) Send(ctx context.Context, message string) error {
	if err := c.Validate(); err != nil {
		return err
	}

	payload := map[string]interface{}{
		"chat_id": c.chatID,
		"text":    message,
	}

	if c.parseMode != "" {
		payload["parse_mode"] = c.parseMode
	}

	return c.sendRequest(ctx, payload)
}

// SendWithTemplate sends message using template
func (c *TelegramChannel) SendWithTemplate(ctx context.Context, template string, data map[string]interface{}) error {
	if err := c.Validate(); err != nil {
		return err
	}

	// Simple template replacement
	message := template
	for k, v := range data {
		message = strings.ReplaceAll(message, "{{"+k+"}}", fmt.Sprintf("%v", v))
	}

	payload := map[string]interface{}{
		"chat_id": c.chatID,
		"text":    message,
	}

	if c.parseMode != "" {
		payload["parse_mode"] = c.parseMode
	}

	return c.sendRequest(ctx, payload)
}

// sendRequest sends HTTP request
func (c *TelegramChannel) sendRequest(ctx context.Context, payload map[string]interface{}) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", c.botToken)

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

	resp, err := req.Post(apiURL)
	if err != nil {
		log.Errorw("telegram send request failed", "error", err)
		return fmt.Errorf("failed to send request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		log.Errorw("telegram request failed", "statusCode", resp.StatusCode(), "response", resp.String())
		return fmt.Errorf("telegram request failed with status %d", resp.StatusCode())
	}

	// Check response body for errors
	var result map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &result); err == nil {
		if ok, exists := result["ok"].(bool); exists && !ok {
			description := result["description"].(string)
			return fmt.Errorf("telegram API error: %s", description)
		}
	}

	return nil
}

// Receive receives messages (webhook callback)
func (c *TelegramChannel) Receive(ctx context.Context, message string) error {
	return nil
}

// Validate validates the configuration
func (c *TelegramChannel) Validate() error {
	if c.botToken == "" {
		return fmt.Errorf("telegram bot token is required")
	}
	if c.chatID == "" {
		return fmt.Errorf("telegram chat ID is required")
	}
	if c.authProvider != nil {
		return c.authProvider.Validate()
	}
	return nil
}

// Close closes the connection
func (c *TelegramChannel) Close() error {
	return nil
}
