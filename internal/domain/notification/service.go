// Copyright 2026 Arcentra Authors.
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

package notification

import "context"

// IAuthProvider defines the interface for authentication providers.
type IAuthProvider interface {
	GetAuthType() AuthType
	Authenticate(ctx context.Context) (string, error)
	GetAuthHeader() (string, string)
	Validate() error
}

// INotifyChannel defines the interface for notification channels.
type INotifyChannel interface {
	SetAuth(provider IAuthProvider) error
	GetAuth() IAuthProvider
	Send(ctx context.Context, message string) error
	SendWithTemplate(ctx context.Context, template string, data map[string]interface{}) error
	Receive(ctx context.Context, message string) error
	Validate() error
	Close() error
}

// NotifyChannel wraps a notification channel implementation.
type NotifyChannel struct {
	channel      INotifyChannel
	authProvider IAuthProvider
}

func NewNotifyChannel(channel INotifyChannel) *NotifyChannel {
	return &NotifyChannel{channel: channel}
}

func (nc *NotifyChannel) SetAuth(provider IAuthProvider) error {
	nc.authProvider = provider
	return nc.channel.SetAuth(provider)
}

func (nc *NotifyChannel) GetAuth() IAuthProvider {
	return nc.authProvider
}

func (nc *NotifyChannel) Send(ctx context.Context, message string) error {
	return nc.channel.Send(ctx, message)
}

func (nc *NotifyChannel) SendWithTemplate(ctx context.Context, template string, data map[string]interface{}) error {
	return nc.channel.SendWithTemplate(ctx, template, data)
}

func (nc *NotifyChannel) Validate() error {
	if nc.authProvider != nil {
		if err := nc.authProvider.Validate(); err != nil {
			return err
		}
	}
	return nc.channel.Validate()
}

func (nc *NotifyChannel) Close() error {
	return nc.channel.Close()
}

// IChannelFactory creates notification channels and auth providers.
type IChannelFactory interface {
	CreateChannel(channelType ChannelType, config map[string]any) (INotifyChannel, error)
	CreateAuthProvider(authType AuthType, config map[string]any) (IAuthProvider, error)
}
