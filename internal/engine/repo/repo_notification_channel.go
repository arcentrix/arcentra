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

package repo

import (
	"context"

	"github.com/arcentrix/arcade/internal/engine/model"
	"github.com/arcentrix/arcade/pkg/database"
)

// INotificationChannelRepository 通知配置仓库接口
type INotificationChannelRepository interface {
	CreateChannel(ctx context.Context, channel *model.NotificationChannel) error
	GetChannelByID(ctx context.Context, channelID string) (*model.NotificationChannel, error)
	GetChannelByName(ctx context.Context, name string) (*model.NotificationChannel, error)
	ListChannels(ctx context.Context) ([]*model.NotificationChannel, error)
	ListActiveChannels(ctx context.Context) ([]*model.NotificationChannel, error)
	UpdateChannel(ctx context.Context, channel *model.NotificationChannel) error
	DeleteChannel(ctx context.Context, channelID string) error
}

type NotificationChannelRepo struct {
	database.IDatabase
}

func NewNotificationChannelRepo(db database.IDatabase) INotificationChannelRepository {
	return &NotificationChannelRepo{
		IDatabase: db,
	}
}

// CreateChannel creates a new notification channel
func (r *NotificationChannelRepo) CreateChannel(ctx context.Context, channel *model.NotificationChannel) error {
	return r.Database().WithContext(ctx).Table(channel.TableName()).Create(channel).Error
}

// GetChannelByID retrieves a channel by channel_id
func (r *NotificationChannelRepo) GetChannelByID(ctx context.Context, channelID string) (*model.NotificationChannel, error) {
	var channel model.NotificationChannel
	err := r.Database().WithContext(ctx).
		Table(channel.TableName()).
		Where("channel_id = ?", channelID).
		First(&channel).Error
	if err != nil {
		return nil, err
	}
	return &channel, nil
}

// GetChannelByName retrieves a channel by name
func (r *NotificationChannelRepo) GetChannelByName(ctx context.Context, name string) (*model.NotificationChannel, error) {
	var channel model.NotificationChannel
	err := r.Database().WithContext(ctx).
		Table(channel.TableName()).
		Where("name = ?", name).
		First(&channel).Error
	if err != nil {
		return nil, err
	}
	return &channel, nil
}

// ListChannels lists all channels
func (r *NotificationChannelRepo) ListChannels(ctx context.Context) ([]*model.NotificationChannel, error) {
	var channels []*model.NotificationChannel
	err := r.Database().WithContext(ctx).
		Table((&model.NotificationChannel{}).TableName()).
		Find(&channels).Error
	return channels, err
}

// ListActiveChannels lists all active channels
func (r *NotificationChannelRepo) ListActiveChannels(ctx context.Context) ([]*model.NotificationChannel, error) {
	var channels []*model.NotificationChannel
	err := r.Database().WithContext(ctx).
		Table((&model.NotificationChannel{}).TableName()).
		Where("is_active = ?", true).
		Find(&channels).Error
	return channels, err
}

// UpdateChannel updates an existing channel
func (r *NotificationChannelRepo) UpdateChannel(ctx context.Context, channel *model.NotificationChannel) error {
	return r.Database().WithContext(ctx).
		Table(channel.TableName()).
		Where("channel_id = ?", channel.ChannelId).
		Omit("id", "channel_id", "created_at").
		Updates(channel).Error
}

// DeleteChannel deletes a channel by channel_id (soft delete by setting is_active = false)
func (r *NotificationChannelRepo) DeleteChannel(ctx context.Context, channelID string) error {
	return r.Database().WithContext(ctx).
		Table((&model.NotificationChannel{}).TableName()).
		Where("channel_id = ?", channelID).
		Update("is_active", false).Error
}
