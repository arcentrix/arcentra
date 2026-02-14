// Copyright 2025 Arcentra Authors.
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

	"github.com/arcentrix/arcentra/internal/pkg/notify/auth"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/bytedance/sonic"
	"github.com/go-resty/resty/v2"
)

// WebhookChannel implements generic webhook notification channel
type WebhookChannel struct {
	webhookURL   string
	method       string
	authProvider auth.IAuthProvider
	client       *resty.Client
}

// NewWebhookChannel creates a new generic webhook notification channel
func NewWebhookChannel(webhookURL, method string) *WebhookChannel {
	if method == "" {
		method = http.MethodPost
	}
	return &WebhookChannel{
		webhookURL: webhookURL,
		method:     method,
		client:     resty.New(),
	}
}

// SetAuth sets authentication provider (supports multiple auth methods)
func (c *WebhookChannel) SetAuth(provider auth.IAuthProvider) error {
	if provider == nil {
		return nil
	}

	c.authProvider = provider
	return provider.Validate()
}

// GetAuth gets the authentication provider
func (c *WebhookChannel) GetAuth() auth.IAuthProvider {
	return c.authProvider
}

// Send sends message to webhook
func (c *WebhookChannel) Send(ctx context.Context, message string) error {
	if err := c.Validate(); err != nil {
		return err
	}

	payload := map[string]interface{}{
		"message": message,
	}

	return c.sendRequest(ctx, payload)
}

// SendWithTemplate sends message using template
func (c *WebhookChannel) SendWithTemplate(ctx context.Context, template string, data map[string]interface{}) error {
	if err := c.Validate(); err != nil {
		return err
	}

	payload := map[string]interface{}{
		"template": template,
		"data":     data,
	}

	return c.sendRequest(ctx, payload)
}

// sendRequest sends HTTP request
func (c *WebhookChannel) sendRequest(ctx context.Context, payload map[string]interface{}) error {
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

	var resp *resty.Response
	var err error

	switch c.method {
	case http.MethodGet:
		resp, err = req.Get(c.webhookURL)
	case http.MethodPost:
		resp, err = req.Post(c.webhookURL)
	case http.MethodPut:
		resp, err = req.Put(c.webhookURL)
	case http.MethodPatch:
		resp, err = req.Patch(c.webhookURL)
	default:
		resp, err = req.Post(c.webhookURL)
	}

	if err != nil {
		log.Errorw("webhook send request failed", "error", err)
		return fmt.Errorf("failed to send request: %w", err)
	}

	if resp.StatusCode() < 200 || resp.StatusCode() >= 300 {
		log.Errorw("webhook request failed", "statusCode", resp.StatusCode(), "response", resp.String())
		return fmt.Errorf("webhook request failed with status %d", resp.StatusCode())
	}

	return nil
}

// Receive receives messages (for processing webhook callbacks)
func (c *WebhookChannel) Receive(ctx context.Context, message string) error {
	// Implement webhook receive logic
	return nil
}

// Validate validates the configuration
func (c *WebhookChannel) Validate() error {
	if c.webhookURL == "" {
		return fmt.Errorf("webhook URL is required")
	}
	if c.authProvider != nil {
		return c.authProvider.Validate()
	}
	return nil
}

// Close closes the connection
func (c *WebhookChannel) Close() error {
	return nil
}

// webhookErrorConfig defines how to parse API error responses
type webhookErrorConfig struct {
	codeKey   string // "code" or "errcode"
	msgKey    string // "msg" or "errmsg"
	logPrefix string // "feishu" or "wecom"
}

func sendWebhookRequest(ctx context.Context, client *resty.Client, url string, authProvider auth.IAuthProvider, payload map[string]interface{}, cfg webhookErrorConfig) error {
	req := client.R().SetContext(ctx)

	if authProvider != nil {
		key, value := authProvider.GetAuthHeader()
		if key != "" && value != "" {
			req.SetHeader(key, value)
		}
	}

	req.SetHeader("Content-Type", "application/json")
	req.SetBody(payload)

	resp, err := req.Post(url)
	if err != nil {
		log.Errorw(cfg.logPrefix+" send request failed", "error", err)
		return fmt.Errorf("failed to send request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		log.Errorw(cfg.logPrefix+" request failed", "statusCode", resp.StatusCode(), "response", resp.String())
		return fmt.Errorf("%s request failed with status %d", cfg.logPrefix, resp.StatusCode())
	}

	var result map[string]interface{}
	if err := sonic.Unmarshal(resp.Body(), &result); err == nil {
		if code, ok := result[cfg.codeKey].(float64); ok && code != 0 {
			msg, _ := result[cfg.msgKey].(string)
			return fmt.Errorf("%s API error: %s=%v, %s=%s", cfg.logPrefix, cfg.codeKey, code, cfg.msgKey, msg)
		}
	}

	return nil
}
