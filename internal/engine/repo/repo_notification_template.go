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

// NotificationTemplateFilter 通知模板查询过滤器
type NotificationTemplateFilter struct {
	Type    string // Template type (build/approval)
	Channel string // Target channel
	Name    string // Template name (支持模糊查询)
	Limit   int    // 分页限制
	Offset  int    // 分页偏移
}

// INotificationTemplateRepository defines notification template persistence with context support.
type INotificationTemplateRepository interface {
	Create(ctx context.Context, tmpl *model.NotificationTemplate) error
	Get(ctx context.Context, templateId string) (*model.NotificationTemplate, error)
	GetByNameAndType(ctx context.Context, name string, templateType string) (*model.NotificationTemplate, error)
	List(ctx context.Context, filter *NotificationTemplateFilter) ([]*model.NotificationTemplate, error)
	Update(ctx context.Context, tmpl *model.NotificationTemplate) error
	Delete(ctx context.Context, templateId string) error
	ListByType(ctx context.Context, templateType string) ([]*model.NotificationTemplate, error)
	ListByChannel(ctx context.Context, channel string) ([]*model.NotificationTemplate, error)
}

type NotificationTemplateRepo struct {
	database.IDatabase
}

func NewNotificationTemplateRepo(db database.IDatabase) INotificationTemplateRepository {
	return &NotificationTemplateRepo{
		IDatabase: db,
	}
}

// Create creates a new notification template.
func (r *NotificationTemplateRepo) Create(ctx context.Context, tmpl *model.NotificationTemplate) error {
	return r.Database().WithContext(ctx).Table(tmpl.TableName()).Create(tmpl).Error
}

// Get returns template by templateId.
func (r *NotificationTemplateRepo) Get(ctx context.Context, templateId string) (*model.NotificationTemplate, error) {
	var tmpl model.NotificationTemplate
	err := r.Database().WithContext(ctx).
		Table(tmpl.TableName()).
		Where("template_id = ? AND is_active = ?", templateId, true).
		First(&tmpl).Error
	if err != nil {
		return nil, err
	}
	return &tmpl, nil
}

// GetByNameAndType returns template by name and type.
func (r *NotificationTemplateRepo) GetByNameAndType(ctx context.Context, name string, templateType string) (*model.NotificationTemplate, error) {
	var tmpl model.NotificationTemplate
	err := r.Database().WithContext(ctx).
		Table(tmpl.TableName()).
		Where("name = ? AND type = ? AND is_active = ?", name, templateType, true).
		First(&tmpl).Error
	if err != nil {
		return nil, err
	}
	return &tmpl, nil
}

// List lists templates with optional filtering.
func (r *NotificationTemplateRepo) List(ctx context.Context, filter *NotificationTemplateFilter) ([]*model.NotificationTemplate, error) {
	var templates []*model.NotificationTemplate
	query := r.Database().WithContext(ctx).Table((&model.NotificationTemplate{}).TableName()).
		Where("is_active = ?", true)

	if filter != nil {
		if filter.Type != "" {
			query = query.Where("type = ?", filter.Type)
		}
		if filter.Channel != "" {
			query = query.Where("channel = ?", filter.Channel)
		}
		if filter.Name != "" {
			query = query.Where("name LIKE ?", "%"+filter.Name+"%")
		}
		if filter.Limit > 0 {
			query = query.Limit(filter.Limit)
		}
		if filter.Offset > 0 {
			query = query.Offset(filter.Offset)
		}
	}

	err := query.Find(&templates).Error
	return templates, err
}

// Update updates an existing template.
func (r *NotificationTemplateRepo) Update(ctx context.Context, tmpl *model.NotificationTemplate) error {
	return r.Database().WithContext(ctx).
		Table(tmpl.TableName()).
		Where("template_id = ?", tmpl.TemplateID).
		Omit("id", "template_id", "created_at").
		Updates(tmpl).Error
}

// Delete soft-deletes template by templateId (sets is_active = false).
func (r *NotificationTemplateRepo) Delete(ctx context.Context, templateId string) error {
	return r.Database().WithContext(ctx).
		Table((&model.NotificationTemplate{}).TableName()).
		Where("template_id = ?", templateId).
		Update("is_active", false).Error
}

// ListByType lists templates by type.
func (r *NotificationTemplateRepo) ListByType(ctx context.Context, templateType string) ([]*model.NotificationTemplate, error) {
	return r.List(ctx, &NotificationTemplateFilter{Type: templateType})
}

// ListByChannel lists templates by channel.
func (r *NotificationTemplateRepo) ListByChannel(ctx context.Context, channel string) ([]*model.NotificationTemplate, error) {
	return r.List(ctx, &NotificationTemplateFilter{Channel: channel})
}
