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

import (
	"context"
	"fmt"

	"github.com/arcentrix/arcentra/internal/domain/notification"
	"github.com/bytedance/sonic"
)

// ChannelRepositoryAdapter adapts domain.INotificationChannelRepo to domain.ChannelRepository.
type ChannelRepositoryAdapter struct {
	repo notification.INotificationChannelRepo
}

func NewChannelRepositoryAdapter(r notification.INotificationChannelRepo) *ChannelRepositoryAdapter {
	return &ChannelRepositoryAdapter{repo: r}
}

func (a *ChannelRepositoryAdapter) ListActiveChannels(ctx context.Context) ([]*notification.ChannelConfig, error) {
	models, err := a.repo.ListActive(ctx)
	if err != nil {
		return nil, err
	}

	configs := make([]*notification.ChannelConfig, 0, len(models))
	for _, m := range models {
		cfg, err := modelToChannelConfig(m)
		if err != nil {
			return nil, fmt.Errorf("failed to convert channel %s: %w", m.Name, err)
		}
		configs = append(configs, cfg)
	}

	return configs, nil
}

func modelToChannelConfig(m *notification.NotificationChannelModel) (*notification.ChannelConfig, error) {
	var config map[string]any
	if m.Config != "" {
		if err := sonic.UnmarshalString(m.Config, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal config: %w", err)
		}
	}

	var authConfig map[string]any
	if m.AuthConfig != "" {
		if err := sonic.UnmarshalString(m.AuthConfig, &authConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal auth_config: %w", err)
		}
	}

	return &notification.ChannelConfig{
		ChannelID:  m.ChannelID,
		Name:       m.Name,
		Type:       notification.ChannelType(m.Type),
		Config:     config,
		AuthConfig: authConfig,
	}, nil
}
