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
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/arcentrix/arcentra/internal/pkg/notify/auth"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/go-resty/resty/v2"
)

// FeishuCardChannel implements Feishu card notification channel
type FeishuCardChannel struct {
	appID        string
	appSecret    string
	authProvider auth.IAuthProvider
	client       *resty.Client
	accessToken  string
}

// NewFeishuCardChannel creates a new Feishu card notification channel
func NewFeishuCardChannel(appID, appSecret string) *FeishuCardChannel {
	return &FeishuCardChannel{
		appID:     appID,
		appSecret: appSecret,
		client:    resty.New(),
	}
}

// SetAuth sets authentication provider (Feishu card uses app_id/app_secret for OAuth2)
func (c *FeishuCardChannel) SetAuth(provider auth.IAuthProvider) error {
	if provider == nil {
		// If no auth provider is given, create OAuth2 auth with app_id/app_secret
		if c.appID != "" && c.appSecret != "" {
			oauth2Auth := auth.NewOAuth2Auth(c.appID, c.appSecret, "https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal")
			c.authProvider = oauth2Auth
			return oauth2Auth.Validate()
		}
		return nil
	}

	c.authProvider = provider
	return provider.Validate()
}

// GetAuth gets the authentication provider
func (c *FeishuCardChannel) GetAuth() auth.IAuthProvider {
	return c.authProvider
}

// getAccessToken gets the access token
func (c *FeishuCardChannel) getAccessToken(ctx context.Context) (string, error) {
	if c.accessToken != "" {
		return c.accessToken, nil
	}

	if c.authProvider == nil {
		return "", fmt.Errorf("auth provider is required")
	}

	token, err := c.authProvider.Authenticate(ctx)
	if err != nil {
		// If authentication fails, try to fetch token from Feishu API
		return c.fetchAccessTokenFromFeishu(ctx)
	}

	c.accessToken = token
	return token, nil
}

// fetchAccessTokenFromFeishu fetches access token from Feishu API
func (c *FeishuCardChannel) fetchAccessTokenFromFeishu(ctx context.Context) (string, error) {
	if c.appID == "" || c.appSecret == "" {
		return "", fmt.Errorf("app_id and app_secret are required")
	}

	payload := map[string]interface{}{
		"app_id":     c.appID,
		"app_secret": c.appSecret,
	}

	resp, err := c.client.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(payload).
		Post("https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal")

	if err != nil {
		return "", fmt.Errorf("failed to get access token: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return "", fmt.Errorf("failed to get access token: status %d", resp.StatusCode())
	}

	var result struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}

	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if result.Code != 0 {
		return "", fmt.Errorf("feishu API error: code=%d, msg=%s", result.Code, result.Msg)
	}

	c.accessToken = result.TenantAccessToken
	return result.TenantAccessToken, nil
}

// Send sends card message to Feishu
func (c *FeishuCardChannel) Send(ctx context.Context, message string) error {
	if err := c.Validate(); err != nil {
		return err
	}

	token, err := c.getAccessToken(ctx)
	if err != nil {
		return err
	}

	card := map[string]interface{}{
		"config": map[string]interface{}{
			"wide_screen_mode": true,
		},
		"elements": []map[string]interface{}{
			{
				"tag": "div",
				"text": map[string]interface{}{
					"tag":     "lark_md",
					"content": message,
				},
			},
		},
	}

	payload := map[string]interface{}{
		"receive_id": "", // 需要设置接收者 ID
		"msg_type":   "interactive",
		"content":    card,
	}

	req := c.client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+token).
		SetHeader("Content-Type", "application/json").
		SetBody(payload)

	resp, err := req.Post("https://open.feishu.cn/open-apis/im/v1/messages")
	if err != nil {
		log.Errorw("feishu card send request failed", "error", err)
		return fmt.Errorf("failed to send request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		log.Errorw("feishu card request failed", "statusCode", resp.StatusCode(), "response", resp.String())
		return fmt.Errorf("feishu request failed with status %d", resp.StatusCode())
	}

	return nil
}

// SendWithTemplate sends card message using template
func (c *FeishuCardChannel) SendWithTemplate(ctx context.Context, template string, data map[string]interface{}) error {
	// Implement template parsing and sending logic
	return c.Send(ctx, template)
}

// Receive receives messages
func (c *FeishuCardChannel) Receive(ctx context.Context, message string) error {
	return nil
}

// Validate validates the configuration
func (c *FeishuCardChannel) Validate() error {
	if c.appID == "" || c.appSecret == "" {
		return fmt.Errorf("app_id and app_secret are required")
	}
	if c.authProvider != nil {
		return c.authProvider.Validate()
	}
	return nil
}

// Close closes the connection
func (c *FeishuCardChannel) Close() error {
	return nil
}
