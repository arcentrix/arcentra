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

package notify

import (
	"context"
	"fmt"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/bytedance/sonic"
)

// ChannelRepositoryAdapter 适配器，将 repo.INotificationChannelRepository 适配到 notify.ChannelRepository
// 在 notify 层进行转换，避免 repo 层依赖 notify 包
type ChannelRepositoryAdapter struct {
	repo repo.INotificationChannelRepository
}

// NewChannelRepositoryAdapter 创建通知配置仓库适配器
func NewChannelRepositoryAdapter(repo repo.INotificationChannelRepository) *ChannelRepositoryAdapter {
	return &ChannelRepositoryAdapter{
		repo: repo,
	}
}

// ListActiveChannels 列出所有活跃的通知配置（转换为 ChannelConfig）
func (a *ChannelRepositoryAdapter) ListActiveChannels(ctx context.Context) ([]*ChannelConfig, error) {
	models, err := a.repo.ListActiveChannels(ctx)
	if err != nil {
		return nil, err
	}

	configs := make([]*ChannelConfig, 0, len(models))
	for _, m := range models {
		cfg, err := modelToChannelConfig(m)
		if err != nil {
			return nil, fmt.Errorf("failed to convert channel %s: %w", m.Name, err)
		}
		configs = append(configs, cfg)
	}

	return configs, nil
}

// modelToChannelConfig 将 model.NotificationChannel 转换为 notify.ChannelConfig
func modelToChannelConfig(m *model.NotificationChannel) (*ChannelConfig, error) {
	var config map[string]interface{}
	if m.Config != "" {
		if err := sonic.UnmarshalString(m.Config, &config); err != nil {
			return nil, fmt.Errorf("failed to unmarshal config: %w", err)
		}
	}

	var authConfig map[string]interface{}
	if m.AuthConfig != "" {
		if err := sonic.UnmarshalString(m.AuthConfig, &authConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal auth_config: %w", err)
		}
	}

	return &ChannelConfig{
		ChannelID:  m.ChannelId,
		Name:       m.Name,
		Type:       ChannelType(m.Type),
		Config:     config,
		AuthConfig: authConfig,
	}, nil
}
