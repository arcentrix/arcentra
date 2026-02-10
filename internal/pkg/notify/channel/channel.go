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

// INotifyChannel defines the interface for notification channels
type INotifyChannel interface {
	// SetAuth sets the authentication provider
	SetAuth(provider auth.IAuthProvider) error
	// GetAuth gets the authentication provider
	GetAuth() auth.IAuthProvider
	// Send sends a message
	Send(ctx context.Context, message string) error
	// SendWithTemplate sends a message using template
	SendWithTemplate(ctx context.Context, template string, data map[string]interface{}) error
	// Receive receives a message (for webhook scenarios)
	Receive(ctx context.Context, message string) error
	// Validate validates the channel configuration
	Validate() error
	// Close closes the channel connection
	Close() error
}

// NotifyChannel wraps the notification channel implementation
type NotifyChannel struct {
	channel      INotifyChannel
	authProvider auth.IAuthProvider
}

// NewNotifyChannel creates a new notification channel wrapper
func NewNotifyChannel(channel INotifyChannel) *NotifyChannel {
	return &NotifyChannel{
		channel: channel,
	}
}

// SetAuth sets the authentication provider
func (nc *NotifyChannel) SetAuth(provider auth.IAuthProvider) error {
	nc.authProvider = provider
	return nc.channel.SetAuth(provider)
}

// GetAuth gets the authentication provider
func (nc *NotifyChannel) GetAuth() auth.IAuthProvider {
	return nc.authProvider
}

// Send sends a message through the channel
func (nc *NotifyChannel) Send(ctx context.Context, message string) error {
	return nc.channel.Send(ctx, message)
}

// SendWithTemplate sends a message using template
func (nc *NotifyChannel) SendWithTemplate(ctx context.Context, template string, data map[string]interface{}) error {
	return nc.channel.SendWithTemplate(ctx, template, data)
}

// Validate validates the channel configuration
func (nc *NotifyChannel) Validate() error {
	if nc.authProvider != nil {
		if err := nc.authProvider.Validate(); err != nil {
			return err
		}
	}
	return nc.channel.Validate()
}

// Close closes the channel connection
func (nc *NotifyChannel) Close() error {
	return nc.channel.Close()
}
