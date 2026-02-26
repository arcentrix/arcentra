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

package repo

import (
	"context"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/pkg/database"
)

// INotificationChannelRepository defines notification channel persistence with context support.
type INotificationChannelRepository interface {
	Create(ctx context.Context, channel *model.NotificationChannel) error
	Get(ctx context.Context, channelId string) (*model.NotificationChannel, error)
	GetByName(ctx context.Context, name string) (*model.NotificationChannel, error)
	List(ctx context.Context) ([]*model.NotificationChannel, error)
	ListActive(ctx context.Context) ([]*model.NotificationChannel, error)
	Update(ctx context.Context, channel *model.NotificationChannel) error
	Delete(ctx context.Context, channelId string) error
}

type NotificationChannelRepo struct {
	database.IDatabase
}

func NewNotificationChannelRepo(db database.IDatabase) INotificationChannelRepository {
	return &NotificationChannelRepo{
		IDatabase: db,
	}
}

// Create creates a new notification channel.
func (r *NotificationChannelRepo) Create(ctx context.Context, channel *model.NotificationChannel) error {
	return r.Database().WithContext(ctx).Table(channel.TableName()).Create(channel).Error
}

// Get returns channel by channelId.
func (r *NotificationChannelRepo) Get(ctx context.Context, channelId string) (*model.NotificationChannel, error) {
	var channel model.NotificationChannel
	err := r.Database().WithContext(ctx).
		Table(channel.TableName()).
		Where("channel_id = ?", channelId).
		First(&channel).Error
	if err != nil {
		return nil, err
	}
	return &channel, nil
}

// GetByName returns channel by name.
func (r *NotificationChannelRepo) GetByName(ctx context.Context, name string) (*model.NotificationChannel, error) {
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

// List lists all channels.
func (r *NotificationChannelRepo) List(ctx context.Context) ([]*model.NotificationChannel, error) {
	var channels []*model.NotificationChannel
	err := r.Database().WithContext(ctx).
		Table((&model.NotificationChannel{}).TableName()).
		Find(&channels).Error
	return channels, err
}

// ListActive lists all active channels.
func (r *NotificationChannelRepo) ListActive(ctx context.Context) ([]*model.NotificationChannel, error) {
	var channels []*model.NotificationChannel
	err := r.Database().WithContext(ctx).
		Table((&model.NotificationChannel{}).TableName()).
		Where("is_active = ?", true).
		Find(&channels).Error
	return channels, err
}

// Update updates an existing channel.
func (r *NotificationChannelRepo) Update(ctx context.Context, channel *model.NotificationChannel) error {
	return r.Database().WithContext(ctx).
		Table(channel.TableName()).
		Where("channel_id = ?", channel.ChannelId).
		Omit("id", "channel_id", "created_at").
		Updates(channel).Error
}

// Delete soft-deletes channel by channelId (sets is_active = false).
func (r *NotificationChannelRepo) Delete(ctx context.Context, channelId string) error {
	return r.Database().WithContext(ctx).
		Table((&model.NotificationChannel{}).TableName()).
		Where("channel_id = ?", channelId).
		Update("is_active", false).Error
}
