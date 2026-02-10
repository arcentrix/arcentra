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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/arcentrix/arcentra/internal/pkg/notify/auth"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/go-resty/resty/v2"
)

// DingTalkChannel implements DingTalk notification channel
type DingTalkChannel struct {
	webhookURL   string
	secret       string // optional: SEC-prefixed secret for signing, leave empty to disable
	authProvider auth.IAuthProvider
	client       *resty.Client
}

// NewDingTalkChannel creates a new DingTalk notification channel
// secret is optional: pass empty string to disable signature verification
func NewDingTalkChannel(webhookURL, secret string) *DingTalkChannel {
	return &DingTalkChannel{
		webhookURL: webhookURL,
		secret:     secret,
		client:     resty.New(),
	}
}

// SetAuth sets authentication provider (DingTalk typically uses webhook secret signing)
func (c *DingTalkChannel) SetAuth(provider auth.IAuthProvider) error {
	if provider == nil {
		return nil
	}

	c.authProvider = provider
	return provider.Validate()
}

// GetAuth gets the authentication provider
func (c *DingTalkChannel) GetAuth() auth.IAuthProvider {
	return c.authProvider
}

// generateSign generates signature for DingTalk webhook using HmacSHA256
// Sign string format: timestamp + "\n" + secret
// Returns empty string if secret is not configured (signing is optional)
func (c *DingTalkChannel) generateSign(timestamp int64) string {
	if c.secret == "" {
		return ""
	}

	// Create the string to sign: timestamp + "\n" + secret
	stringToSign := fmt.Sprintf("%d\n%s", timestamp, c.secret)

	// Calculate HmacSHA256 signature
	h := hmac.New(sha256.New, []byte(c.secret))
	h.Write([]byte(stringToSign))
	signature := h.Sum(nil)

	// Base64 encode
	signBase64 := base64.StdEncoding.EncodeToString(signature)

	// URL encode (using UTF-8)
	signEncoded := url.QueryEscape(signBase64)

	return signEncoded
}

// Send sends message to DingTalk
func (c *DingTalkChannel) Send(ctx context.Context, message string) error {
	if err := c.Validate(); err != nil {
		return err
	}

	payload := map[string]interface{}{
		"msgtype": "text",
		"text": map[string]string{
			"content": message,
		},
	}

	return c.sendRequest(ctx, payload)
}

// SendWithTemplate sends message using template
func (c *DingTalkChannel) SendWithTemplate(ctx context.Context, template string, data map[string]interface{}) error {
	if err := c.Validate(); err != nil {
		return err
	}

	payload := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]interface{}{
			"title": "Notification",
			"text":  template,
		},
	}

	return c.sendRequest(ctx, payload)
}

// sendRequest sends HTTP request
func (c *DingTalkChannel) sendRequest(ctx context.Context, payload map[string]interface{}) error {
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

	// Build URL with signature if secret is configured (optional)
	// If secret is empty, the webhook will be called without signature
	requestURL := c.webhookURL
	if c.secret != "" {
		timestamp := time.Now().UnixNano() / 1e6 // milliseconds
		sign := c.generateSign(timestamp)
		requestURL = fmt.Sprintf("%s&timestamp=%d&sign=%s", c.webhookURL, timestamp, sign)
	}

	resp, err := req.Post(requestURL)
	if err != nil {
		log.Errorw("dingtalk send request failed", "error", err)
		return fmt.Errorf("failed to send request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		log.Errorw("dingtalk request failed", "statusCode", resp.StatusCode(), "response", resp.String())
		return fmt.Errorf("dingtalk request failed with status %d", resp.StatusCode())
	}

	// Check for errors in response body
	var result map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &result); err == nil {
		if errcode, ok := result["errcode"].(float64); ok && errcode != 0 {
			errmsg := result["errmsg"].(string)
			return fmt.Errorf("dingtalk API error: errcode=%v, errmsg=%s", errcode, errmsg)
		}
	}

	return nil
}

// Receive receives messages (webhook callback)
func (c *DingTalkChannel) Receive(ctx context.Context, message string) error {
	return nil
}

// Validate validates the configuration
func (c *DingTalkChannel) Validate() error {
	if c.webhookURL == "" {
		return fmt.Errorf("dingtalk webhook URL is required")
	}
	if c.authProvider != nil {
		return c.authProvider.Validate()
	}
	return nil
}

// Close closes the connection
func (c *DingTalkChannel) Close() error {
	return nil
}
