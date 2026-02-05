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

	"github.com/arcentrix/arcentra/internal/pkg/notify/auth"
)

// LarkAppChannel implements Lark (Feishu international) app notification channel
type LarkAppChannel struct {
	*FeishuAppChannel
}

// NewLarkAppChannel creates a new Lark app notification channel
func NewLarkAppChannel(webhookURL string) *LarkAppChannel {
	return &LarkAppChannel{
		FeishuAppChannel: NewFeishuAppChannel(webhookURL),
	}
}

// NewLarkAppChannelWithSecret creates a new Lark app notification channel with signing secret
// secret is optional: pass empty string to disable signature verification
func NewLarkAppChannelWithSecret(webhookURL, secret string) *LarkAppChannel {
	return &LarkAppChannel{
		FeishuAppChannel: NewFeishuAppChannelWithSecret(webhookURL, secret),
	}
}

// SetAuth sets authentication provider
func (c *LarkAppChannel) SetAuth(provider auth.IAuthProvider) error {
	return c.FeishuAppChannel.SetAuth(provider)
}

// GetAuth gets the authentication provider
func (c *LarkAppChannel) GetAuth() auth.IAuthProvider {
	return c.FeishuAppChannel.GetAuth()
}

// Send sends message (Lark uses the same API as Feishu)
func (c *LarkAppChannel) Send(ctx context.Context, message string) error {
	return c.FeishuAppChannel.Send(ctx, message)
}

// SendWithTemplate sends message using template
func (c *LarkAppChannel) SendWithTemplate(ctx context.Context, template string, data map[string]interface{}) error {
	return c.FeishuAppChannel.SendWithTemplate(ctx, template, data)
}

// Receive receives messages
func (c *LarkAppChannel) Receive(ctx context.Context, message string) error {
	return c.FeishuAppChannel.Receive(ctx, message)
}

// Validate validates the configuration
func (c *LarkAppChannel) Validate() error {
	return c.FeishuAppChannel.Validate()
}

// Close closes the connection
func (c *LarkAppChannel) Close() error {
	return c.FeishuAppChannel.Close()
}
