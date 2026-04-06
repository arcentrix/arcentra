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

package notification

import (
	"context"
	"fmt"
	"sync"

	"github.com/arcentrix/arcentra/internal/domain/notification"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
)

// Manager manages multiple notification channels.
type Manager struct {
	channels    map[string]*notification.NotifyChannel
	mu          sync.RWMutex
	factory     notification.IChannelFactory
	channelRepo notification.ChannelRepository
}

// NewNotifyManager creates a new notification manager.
func NewNotifyManager(factory notification.IChannelFactory) *Manager {
	return &Manager{
		channels: make(map[string]*notification.NotifyChannel),
		factory:  factory,
	}
}

// SetChannelRepository sets the notification config repository.
func (nm *Manager) SetChannelRepository(repo notification.ChannelRepository) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.channelRepo = repo
}

// LoadChannelsFromDatabase loads all active notification configs from the database.
func (nm *Manager) LoadChannelsFromDatabase(ctx context.Context) error {
	if nm.channelRepo == nil {
		return fmt.Errorf("channel repository is not set")
	}

	configs, err := nm.channelRepo.ListActiveChannels(ctx)
	if err != nil {
		return fmt.Errorf("failed to load channels from database: %w", err)
	}

	var errors []error
	for _, cfg := range configs {
		ch, err := nm.factory.CreateChannel(cfg.Type, cfg.Config)
		if err != nil {
			log.Warnw("failed to create channel", "channel", cfg.Name, "error", err)
			errors = append(errors, fmt.Errorf("channel %s: %w", cfg.Name, err))
			continue
		}

		if len(cfg.AuthConfig) > 0 {
			authType, _ := cfg.AuthConfig["type"].(string)
			if authType != "" {
				authProvider, err := nm.factory.CreateAuthProvider(notification.AuthType(authType), cfg.AuthConfig)
				if err == nil {
					_ = ch.SetAuth(authProvider)
				} else {
					log.Warnw("failed to create auth provider", "channel", cfg.Name, "error", err)
				}
			}
		}

		notifyChannel := notification.NewNotifyChannel(ch)
		if err := nm.RegisterChannel(cfg.Name, notifyChannel); err != nil {
			log.Warnw("failed to register channel", "channel", cfg.Name, "error", err)
			errors = append(errors, fmt.Errorf("channel %s: %w", cfg.Name, err))
			continue
		}

		log.Infow("notification channel loaded from database", "channel", cfg.Name, "type", cfg.Type)
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to load %d channel(s): %v", len(errors), errors)
	}

	return nil
}

// RegisterChannel registers a notification channel.
func (nm *Manager) RegisterChannel(name string, ch *notification.NotifyChannel) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if name == "" {
		return fmt.Errorf("channel name cannot be empty")
	}
	if ch == nil {
		return fmt.Errorf("channel cannot be nil")
	}
	if err := ch.Validate(); err != nil {
		return fmt.Errorf("channel validation failed: %w", err)
	}

	nm.channels[name] = ch
	return nil
}

// GetChannel gets a notification channel by name.
func (nm *Manager) GetChannel(name string) (*notification.NotifyChannel, error) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	ch, exists := nm.channels[name]
	if !exists {
		return nil, fmt.Errorf("channel %s not found", name)
	}
	return ch, nil
}

// Send sends a message to a specific channel.
func (nm *Manager) Send(ctx context.Context, channelName, message string) error {
	ch, err := nm.GetChannel(channelName)
	if err != nil {
		return err
	}
	return ch.Send(ctx, message)
}

// SendToMultiple sends a message to multiple channels.
func (nm *Manager) SendToMultiple(ctx context.Context, channelNames []string, message string) error {
	var errs []error
	for _, name := range channelNames {
		if err := nm.Send(ctx, name, message); err != nil {
			errs = append(errs, fmt.Errorf("channel %s: %w", name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("some channels failed: %v", errs)
	}
	return nil
}

// SendWithTemplate sends a message using template.
func (nm *Manager) SendWithTemplate(ctx context.Context, channelName, template string, data map[string]any) error {
	ch, err := nm.GetChannel(channelName)
	if err != nil {
		return err
	}
	return ch.SendWithTemplate(ctx, template, data)
}

// UnregisterChannel unregisters a notification channel.
func (nm *Manager) UnregisterChannel(name string) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	ch, exists := nm.channels[name]
	if !exists {
		return fmt.Errorf("channel %s not found", name)
	}
	if err := ch.Close(); err != nil {
		return fmt.Errorf("failed to close channel: %w", err)
	}
	delete(nm.channels, name)
	return nil
}

// ListChannels lists all registered channels.
func (nm *Manager) ListChannels() []string {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	names := make([]string, 0, len(nm.channels))
	for name := range nm.channels {
		names = append(names, name)
	}
	return names
}
