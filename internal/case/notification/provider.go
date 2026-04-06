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

	"github.com/arcentrix/arcentra/internal/domain/notification"
	"github.com/arcentrix/arcentra/pkg/telemetry/log"
	"github.com/google/wire"
)

var ProviderSet = wire.NewSet(
	ProvideNotifyManager,
)

func ProvideNotifyManager(channelRepo notification.INotificationChannelRepo, factory notification.IChannelFactory) (*Manager, error) {
	manager := NewNotifyManager(factory)

	channelRepoAdapter := &channelRepoAdapterBridge{repo: channelRepo}
	manager.SetChannelRepository(channelRepoAdapter)

	ctx := context.Background()
	if err := manager.LoadChannelsFromDatabase(ctx); err != nil {
		log.Warnw("failed to load channels from database", "error", err)
	}

	log.Infow("notify manager initialized", "channel_count", len(manager.ListChannels()))
	return manager, nil
}

type channelRepoAdapterBridge struct {
	repo notification.INotificationChannelRepo
}

func (b *channelRepoAdapterBridge) ListActiveChannels(ctx context.Context) ([]*notification.ChannelConfig, error) {
	models, err := b.repo.ListActive(ctx)
	if err != nil {
		return nil, err
	}

	configs := make([]*notification.ChannelConfig, 0, len(models))
	for _, m := range models {
		var config map[string]any
		var authConfig map[string]any
		configs = append(configs, &notification.ChannelConfig{
			ChannelID:  m.ChannelID,
			Name:       m.Name,
			Type:       notification.ChannelType(m.Type),
			Config:     config,
			AuthConfig: authConfig,
		})
	}
	return configs, nil
}
