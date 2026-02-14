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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	"github.com/arcentrix/arcentra/internal/pkg/notify/auth"
	"github.com/go-resty/resty/v2"
)

// FeishuAppChannel implements Feishu app notification channel
type FeishuAppChannel struct {
	webhookURL   string
	secret       string // optional: signing secret, leave empty to disable
	authProvider auth.IAuthProvider
	client       *resty.Client
}

// NewFeishuAppChannel creates a new Feishu app notification channel
// secret is optional: pass empty string to disable signature verification
func NewFeishuAppChannel(webhookURL string) *FeishuAppChannel {
	return &FeishuAppChannel{
		webhookURL: webhookURL,
		client:     resty.New(),
	}
}

// NewFeishuAppChannelWithSecret creates a new Feishu app notification channel with signing secret
func NewFeishuAppChannelWithSecret(webhookURL, secret string) *FeishuAppChannel {
	return &FeishuAppChannel{
		webhookURL: webhookURL,
		secret:     secret,
		client:     resty.New(),
	}
}

// SetAuth sets authentication provider (Feishu typically uses webhook token or app_id/app_secret)
func (c *FeishuAppChannel) SetAuth(provider auth.IAuthProvider) error {
	if provider == nil {
		return nil
	}

	// Validate authentication type
	if provider.GetAuthType() != auth.AuthTypeToken &&
		provider.GetAuthType() != auth.AuthTypeAPIKey {
		return fmt.Errorf("feishu app channel only supports token or apikey auth")
	}

	c.authProvider = provider
	return provider.Validate()
}

// GetAuth gets the authentication provider
func (c *FeishuAppChannel) GetAuth() auth.IAuthProvider {
	return c.authProvider
}

// generateSign generates signature for Feishu webhook using HmacSHA256
// The signing process:
// 1. Concatenate timestamp and secret with newline: timestamp + "\n" + secret
// 2. Use HmacSHA256 with secret as key to sign the concatenated string
// 3. Base64 encode the signature
// Returns empty map if secret is not configured (signing is optional)
func (c *FeishuAppChannel) generateSign() map[string]interface{} {
	if c.secret == "" {
		return nil
	}

	// Get current timestamp in seconds
	timestamp := time.Now().Unix()

	// Create the string to sign: timestamp + "\n" + secret
	stringToSign := fmt.Sprintf("%d\n%s", timestamp, c.secret)

	// Calculate HmacSHA256 signature using secret as key
	h := hmac.New(sha256.New, []byte(c.secret))
	h.Write([]byte(stringToSign))
	signature := h.Sum(nil)

	// Base64 encode
	signBase64 := base64.StdEncoding.EncodeToString(signature)

	return map[string]interface{}{
		"timestamp": strconv.FormatInt(timestamp, 10),
		"sign":      signBase64,
	}
}

// Send sends message to Feishu
func (c *FeishuAppChannel) Send(ctx context.Context, message string) error {
	if err := c.Validate(); err != nil {
		return err
	}

	payload := map[string]interface{}{
		"msg_type": "text",
		"content": map[string]string{
			"text": message,
		},
	}

	// Add signature if secret is configured (optional)
	if signData := c.generateSign(); signData != nil {
		payload["timestamp"] = signData["timestamp"]
		payload["sign"] = signData["sign"]
	}

	return c.sendRequest(ctx, payload)
}

// SendWithTemplate sends message using template
func (c *FeishuAppChannel) SendWithTemplate(ctx context.Context, template string, data map[string]interface{}) error {
	if err := c.Validate(); err != nil {
		return err
	}

	// Template parsing can be extended here
	payload := map[string]interface{}{
		"msg_type": "interactive",
		"card": map[string]interface{}{
			"config": map[string]interface{}{
				"wide_screen_mode": true,
			},
			"elements": []map[string]interface{}{
				{
					"tag": "div",
					"text": map[string]interface{}{
						"tag":     "lark_md",
						"content": template,
					},
				},
			},
		},
	}

	// Add signature if secret is configured (optional)
	if signData := c.generateSign(); signData != nil {
		payload["timestamp"] = signData["timestamp"]
		payload["sign"] = signData["sign"]
	}

	return c.sendRequest(ctx, payload)
}

func (c *FeishuAppChannel) sendRequest(ctx context.Context, payload map[string]interface{}) error {
	return sendWebhookRequest(ctx, c.client, c.webhookURL, c.authProvider, payload, webhookErrorConfig{
		codeKey: "code", msgKey: "msg", logPrefix: "feishu",
	})
}

// Receive receives messages (webhook callback)
func (c *FeishuAppChannel) Receive(ctx context.Context, message string) error {
	// Implement webhook receive logic
	return nil
}

// Validate validates the configuration
func (c *FeishuAppChannel) Validate() error {
	if c.webhookURL == "" {
		return fmt.Errorf("feishu webhook URL is required")
	}
	if c.authProvider != nil {
		return c.authProvider.Validate()
	}
	return nil
}

// Close closes the connection
func (c *FeishuAppChannel) Close() error {
	return nil
}
