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
	"time"

	domain "github.com/arcentrix/arcentra/internal/domain/notification"
	"github.com/arcentrix/arcentra/pkg/store/database"
)

// Compile-time interface assertions.
var (
	_ domain.INotificationChannelRepo  = (*NotificationChannelRepo)(nil)
	_ domain.INotificationTemplateRepo = (*NotificationTemplateRepo)(nil)
	_ domain.ITemplateRepository       = (*TemplateRepo)(nil)
	_ domain.ChannelRepository         = (*ChannelRepo)(nil)
)

// ---------------------------------------------------------------------------
// NotificationChannelRepo
// ---------------------------------------------------------------------------

// NotificationChannelRepo implements domain.INotificationChannelRepo.
type NotificationChannelRepo struct {
	db database.IDatabase
}

// NewNotificationChannelRepo creates a new NotificationChannelRepo.
func NewNotificationChannelRepo(db database.IDatabase) *NotificationChannelRepo {
	return &NotificationChannelRepo{db: db}
}

// ListActive returns all active notification channels.
func (r *NotificationChannelRepo) ListActive(ctx context.Context) ([]*domain.NotificationChannelModel, error) {
	var pos []NotificationChannelPO
	if err := r.db.Database().WithContext(ctx).
		Table(NotificationChannelPO{}.TableName()).
		Where("is_active = ?", 1).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	channels := make([]*domain.NotificationChannelModel, len(pos))
	for i := range pos {
		channels[i] = pos[i].ToDomainModel()
	}
	return channels, nil
}

// ---------------------------------------------------------------------------
// NotificationTemplateRepo
// ---------------------------------------------------------------------------

// NotificationTemplateRepo implements domain.INotificationTemplateRepo.
type NotificationTemplateRepo struct {
	db database.IDatabase
}

// NewNotificationTemplateRepo creates a new NotificationTemplateRepo.
func NewNotificationTemplateRepo(db database.IDatabase) *NotificationTemplateRepo {
	return &NotificationTemplateRepo{db: db}
}

// Create inserts a new notification template record.
func (r *NotificationTemplateRepo) Create(ctx context.Context, t *domain.NotificationTemplateModel) error {
	po := NotificationTemplatePOFromModel(t)
	return r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error
}

// Get retrieves a notification template by its template ID.
func (r *NotificationTemplateRepo) Get(ctx context.Context, id string) (*domain.NotificationTemplateModel, error) {
	var po NotificationTemplatePO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("template_id = ? AND is_active = ?", id, 1).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomainModel(), nil
}

// GetByNameAndType retrieves a template by name and type combination.
func (r *NotificationTemplateRepo) GetByNameAndType(ctx context.Context, name, templateType string) (*domain.NotificationTemplateModel, error) {
	var po NotificationTemplatePO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("name = ? AND type = ? AND is_active = ?", name, templateType, 1).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomainModel(), nil
}

// List returns notification templates matching the given filter.
func (r *NotificationTemplateRepo) List(ctx context.Context, filter *domain.NotificationTemplateFilter) ([]*domain.NotificationTemplateModel, error) {
	var pos []NotificationTemplatePO

	q := r.db.Database().WithContext(ctx).
		Table(NotificationTemplatePO{}.TableName()).
		Where("is_active = ?", 1)

	if filter.Type != "" {
		q = q.Where("type = ?", filter.Type)
	}
	if filter.Channel != "" {
		q = q.Where("channel = ?", filter.Channel)
	}
	if filter.Name != "" {
		q = q.Where("name LIKE ?", "%"+filter.Name+"%")
	}

	if filter.Limit > 0 {
		q = q.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		q = q.Offset(filter.Offset)
	}

	if err := q.Order("id ASC").Find(&pos).Error; err != nil {
		return nil, err
	}

	templates := make([]*domain.NotificationTemplateModel, len(pos))
	for i := range pos {
		templates[i] = pos[i].ToDomainModel()
	}
	return templates, nil
}

// Update persists changes to a notification template.
func (r *NotificationTemplateRepo) Update(ctx context.Context, t *domain.NotificationTemplateModel) error {
	po := NotificationTemplatePOFromModel(t)
	return r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("template_id = ?", po.TemplateID).
		Updates(po).Error
}

// Delete soft-deletes a notification template by setting is_active = 0.
func (r *NotificationTemplateRepo) Delete(ctx context.Context, id string) error {
	return r.db.Database().WithContext(ctx).
		Table(NotificationTemplatePO{}.TableName()).
		Where("template_id = ?", id).
		Updates(map[string]any{"is_active": 0, "updated_at": time.Now()}).Error
}

// ListByType returns all active templates of a given type.
func (r *NotificationTemplateRepo) ListByType(ctx context.Context, templateType string) ([]*domain.NotificationTemplateModel, error) {
	var pos []NotificationTemplatePO
	if err := r.db.Database().WithContext(ctx).
		Table(NotificationTemplatePO{}.TableName()).
		Where("type = ? AND is_active = ?", templateType, 1).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	templates := make([]*domain.NotificationTemplateModel, len(pos))
	for i := range pos {
		templates[i] = pos[i].ToDomainModel()
	}
	return templates, nil
}

// ListByChannel returns all active templates for a given channel.
func (r *NotificationTemplateRepo) ListByChannel(ctx context.Context, channel string) ([]*domain.NotificationTemplateModel, error) {
	var pos []NotificationTemplatePO
	if err := r.db.Database().WithContext(ctx).
		Table(NotificationTemplatePO{}.TableName()).
		Where("channel = ? AND is_active = ?", channel, 1).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	templates := make([]*domain.NotificationTemplateModel, len(pos))
	for i := range pos {
		templates[i] = pos[i].ToDomainModel()
	}
	return templates, nil
}

// ---------------------------------------------------------------------------
// TemplateRepo (domain-level Template entity)
// ---------------------------------------------------------------------------

// TemplateRepo implements domain.ITemplateRepository.
type TemplateRepo struct {
	db database.IDatabase
}

// NewTemplateRepo creates a new TemplateRepo.
func NewTemplateRepo(db database.IDatabase) *TemplateRepo {
	return &TemplateRepo{db: db}
}

// Create inserts a new domain template record.
func (r *TemplateRepo) Create(ctx context.Context, template *domain.Template) error {
	po := NotificationTemplatePOFromDomain(template)
	return r.db.Database().WithContext(ctx).Table(po.TableName()).Create(po).Error
}

// Get retrieves a domain template by its ID.
func (r *TemplateRepo) Get(ctx context.Context, id string) (*domain.Template, error) {
	var po NotificationTemplatePO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("template_id = ? AND is_active = ?", id, 1).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomainTemplate(), nil
}

// GetByNameAndType retrieves a domain template by name and type.
func (r *TemplateRepo) GetByNameAndType(ctx context.Context, name string, templateType domain.TemplateType) (*domain.Template, error) {
	var po NotificationTemplatePO
	if err := r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("name = ? AND type = ? AND is_active = ?", name, string(templateType), 1).
		First(&po).Error; err != nil {
		return nil, err
	}
	return po.ToDomainTemplate(), nil
}

// List returns domain templates matching the given filter.
func (r *TemplateRepo) List(ctx context.Context, filter *domain.TemplateFilter) ([]*domain.Template, error) {
	var pos []NotificationTemplatePO

	q := r.db.Database().WithContext(ctx).
		Table(NotificationTemplatePO{}.TableName()).
		Where("is_active = ?", 1)

	if filter.Type != "" {
		q = q.Where("type = ?", string(filter.Type))
	}
	if filter.Channel != "" {
		q = q.Where("channel = ?", filter.Channel)
	}
	if filter.Name != "" {
		q = q.Where("name LIKE ?", "%"+filter.Name+"%")
	}
	if filter.Limit > 0 {
		q = q.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		q = q.Offset(filter.Offset)
	}

	if err := q.Order("id ASC").Find(&pos).Error; err != nil {
		return nil, err
	}

	templates := make([]*domain.Template, len(pos))
	for i := range pos {
		templates[i] = pos[i].ToDomainTemplate()
	}
	return templates, nil
}

// Update persists changes to a domain template.
func (r *TemplateRepo) Update(ctx context.Context, template *domain.Template) error {
	po := NotificationTemplatePOFromDomain(template)
	return r.db.Database().WithContext(ctx).
		Table(po.TableName()).
		Where("template_id = ?", po.TemplateID).
		Updates(po).Error
}

// Delete soft-deletes a domain template.
func (r *TemplateRepo) Delete(ctx context.Context, id string) error {
	return r.db.Database().WithContext(ctx).
		Table(NotificationTemplatePO{}.TableName()).
		Where("template_id = ?", id).
		Updates(map[string]any{"is_active": 0, "updated_at": time.Now()}).Error
}

// ListByType returns all active domain templates of a given type.
func (r *TemplateRepo) ListByType(ctx context.Context, templateType domain.TemplateType) ([]*domain.Template, error) {
	var pos []NotificationTemplatePO
	if err := r.db.Database().WithContext(ctx).
		Table(NotificationTemplatePO{}.TableName()).
		Where("type = ? AND is_active = ?", string(templateType), 1).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	templates := make([]*domain.Template, len(pos))
	for i := range pos {
		templates[i] = pos[i].ToDomainTemplate()
	}
	return templates, nil
}

// ListByChannel returns all active domain templates for a given channel.
func (r *TemplateRepo) ListByChannel(ctx context.Context, channel string) ([]*domain.Template, error) {
	var pos []NotificationTemplatePO
	if err := r.db.Database().WithContext(ctx).
		Table(NotificationTemplatePO{}.TableName()).
		Where("channel = ? AND is_active = ?", channel, 1).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	templates := make([]*domain.Template, len(pos))
	for i := range pos {
		templates[i] = pos[i].ToDomainTemplate()
	}
	return templates, nil
}

// ---------------------------------------------------------------------------
// ChannelRepo (domain-level ChannelConfig entity)
// ---------------------------------------------------------------------------

// ChannelRepo implements domain.ChannelRepository.
type ChannelRepo struct {
	db database.IDatabase
}

// NewChannelRepo creates a new ChannelRepo.
func NewChannelRepo(db database.IDatabase) *ChannelRepo {
	return &ChannelRepo{db: db}
}

// ListActiveChannels returns all active channel configurations.
func (r *ChannelRepo) ListActiveChannels(ctx context.Context) ([]*domain.ChannelConfig, error) {
	var pos []NotificationChannelPO
	if err := r.db.Database().WithContext(ctx).
		Table(NotificationChannelPO{}.TableName()).
		Where("is_active = ?", 1).
		Find(&pos).Error; err != nil {
		return nil, err
	}
	configs := make([]*domain.ChannelConfig, len(pos))
	for i := range pos {
		configs[i] = pos[i].ToDomainChannelConfig()
	}
	return configs, nil
}
