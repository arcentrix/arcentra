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

type INotificationTemplateRepository interface {
	CreateTemplate(ctx context.Context, tmpl *model.NotificationTemplate) error
	GetTemplateByID(ctx context.Context, templateID string) (*model.NotificationTemplate, error)
	GetTemplateByNameAndType(ctx context.Context, name string, templateType string) (*model.NotificationTemplate, error)
	ListTemplates(ctx context.Context, filter *NotificationTemplateFilter) ([]*model.NotificationTemplate, error)
	UpdateTemplate(ctx context.Context, tmpl *model.NotificationTemplate) error
	DeleteTemplate(ctx context.Context, templateID string) error
	ListTemplatesByType(ctx context.Context, templateType string) ([]*model.NotificationTemplate, error)
	ListTemplatesByChannel(ctx context.Context, channel string) ([]*model.NotificationTemplate, error)
}

type NotificationTemplateRepo struct {
	database.IDatabase
}

func NewNotificationTemplateRepo(db database.IDatabase) INotificationTemplateRepository {
	return &NotificationTemplateRepo{
		IDatabase: db,
	}
}

// CreateTemplate creates a new notification template
func (r *NotificationTemplateRepo) CreateTemplate(ctx context.Context, tmpl *model.NotificationTemplate) error {
	return r.Database().WithContext(ctx).Table(tmpl.TableName()).Create(tmpl).Error
}

// GetTemplateByID retrieves a template by template_id
func (r *NotificationTemplateRepo) GetTemplateByID(ctx context.Context, templateID string) (*model.NotificationTemplate, error) {
	var tmpl model.NotificationTemplate
	err := r.Database().WithContext(ctx).
		Table(tmpl.TableName()).
		Where("template_id = ? AND is_active = ?", templateID, true).
		First(&tmpl).Error
	if err != nil {
		return nil, err
	}
	return &tmpl, nil
}

// GetTemplateByNameAndType retrieves a template by name and type
func (r *NotificationTemplateRepo) GetTemplateByNameAndType(ctx context.Context, name string, templateType string) (*model.NotificationTemplate, error) {
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

// ListTemplates lists templates with optional filtering
func (r *NotificationTemplateRepo) ListTemplates(ctx context.Context, filter *NotificationTemplateFilter) ([]*model.NotificationTemplate, error) {
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

// UpdateTemplate updates an existing template
func (r *NotificationTemplateRepo) UpdateTemplate(ctx context.Context, tmpl *model.NotificationTemplate) error {
	return r.Database().WithContext(ctx).
		Table(tmpl.TableName()).
		Where("template_id = ?", tmpl.TemplateID).
		Omit("id", "template_id", "created_at").
		Updates(tmpl).Error
}

// DeleteTemplate deletes a template by template_id (soft delete by setting is_active = false)
func (r *NotificationTemplateRepo) DeleteTemplate(ctx context.Context, templateID string) error {
	return r.Database().WithContext(ctx).
		Table((&model.NotificationTemplate{}).TableName()).
		Where("template_id = ?", templateID).
		Update("is_active", false).Error
}

// ListTemplatesByType lists templates by type
func (r *NotificationTemplateRepo) ListTemplatesByType(ctx context.Context, templateType string) ([]*model.NotificationTemplate, error) {
	return r.ListTemplates(ctx, &NotificationTemplateFilter{Type: templateType})
}

// ListTemplatesByChannel lists templates by channel
func (r *NotificationTemplateRepo) ListTemplatesByChannel(ctx context.Context, channel string) ([]*model.NotificationTemplate, error) {
	return r.ListTemplates(ctx, &NotificationTemplateFilter{Channel: channel})
}
