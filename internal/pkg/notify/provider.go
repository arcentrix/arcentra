// Copyright 2025 Arcade Team
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

	"github.com/arcentrix/arcade/internal/engine/repo"
	"github.com/arcentrix/arcade/pkg/log"
	"github.com/google/wire"
)

// ProviderSet provides notify layer related dependencies
var ProviderSet = wire.NewSet(
	ProvideNotifyManager,
)

// ProvideNotifyManager provides notification manager instance
// Loads channels from database on initialization
func ProvideNotifyManager(repos *repo.Repositories) (*NotifyManager, error) {
	manager := NewNotifyManager()

	channelRepoAdapter := NewChannelRepositoryAdapter(repos.NotificationChannel)
	manager.SetChannelRepository(channelRepoAdapter)

	// 从数据库加载所有活跃的通知配置
	ctx := context.Background()
	if err := manager.LoadChannelsFromDatabase(ctx); err != nil {
		log.Warnw("failed to load channels from database", "error", err)
		// 不返回错误，允许使用空的通知管理器
	}

	log.Infow("notify manager initialized", "channel_count", len(manager.ListChannels()))
	return manager, nil
}
