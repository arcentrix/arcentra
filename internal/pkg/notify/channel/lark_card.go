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

	"github.com/arcentrix/arcentra/internal/pkg/notify/auth"
)

// LarkCardChannel implements Lark card notification channel
type LarkCardChannel struct {
	*FeishuCardChannel
}

// NewLarkCardChannel creates a new Lark card notification channel
func NewLarkCardChannel(appID, appSecret string) *LarkCardChannel {
	return &LarkCardChannel{
		FeishuCardChannel: NewFeishuCardChannel(appID, appSecret),
	}
}

// SetAuth sets authentication provider
func (c *LarkCardChannel) SetAuth(provider auth.IAuthProvider) error {
	return c.FeishuCardChannel.SetAuth(provider)
}

// GetAuth gets the authentication provider
func (c *LarkCardChannel) GetAuth() auth.IAuthProvider {
	return c.FeishuCardChannel.GetAuth()
}

// Send sends card message
func (c *LarkCardChannel) Send(ctx context.Context, message string) error {
	return c.FeishuCardChannel.Send(ctx, message)
}

// SendWithTemplate sends card message using template
func (c *LarkCardChannel) SendWithTemplate(ctx context.Context, template string, data map[string]interface{}) error {
	return c.FeishuCardChannel.SendWithTemplate(ctx, template, data)
}

// Receive receives messages
func (c *LarkCardChannel) Receive(ctx context.Context, message string) error {
	return c.FeishuCardChannel.Receive(ctx, message)
}

// Validate validates the configuration
func (c *LarkCardChannel) Validate() error {
	return c.FeishuCardChannel.Validate()
}

// Close closes the connection
func (c *LarkCardChannel) Close() error {
	return c.FeishuCardChannel.Close()
}
