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

package template

import (
	"context"
	"fmt"

	"github.com/arcentrix/arcentra/internal/engine/model"
	"github.com/arcentrix/arcentra/internal/engine/repo"
	"github.com/bytedance/sonic"
)

// ITemplateRepository defines the interface for template storage
type ITemplateRepository interface {
	// Create creates a new template
	Create(ctx context.Context, template *Template) error

	// Get retrieves a template by ID
	Get(ctx context.Context, id string) (*Template, error)

	// GetByNameAndType retrieves a template by name and type
	GetByNameAndType(ctx context.Context, name string, templateType Type) (*Template, error)

	// List lists all templates with optional filtering
	List(ctx context.Context, filter *Filter) ([]*Template, error)

	// Update updates an existing template
	Update(ctx context.Context, template *Template) error

	// Delete deletes a template by ID
	Delete(ctx context.Context, id string) error

	// ListByType lists templates by type
	ListByType(ctx context.Context, templateType Type) ([]*Template, error)

	// ListByChannel lists templates by channel
	ListByChannel(ctx context.Context, channel string) ([]*Template, error)
}

// Filter represents filter criteria for listing templates
type Filter struct {
	Type    Type
	Channel string
	Name    string
	Limit   int
	Offset  int
}

// DatabaseTemplateRepository implements ITemplateRepository using database storage
type DatabaseTemplateRepository struct {
	repo repo.INotificationTemplateRepository
}

// NewDatabaseTemplateRepository creates a new database template repository
func NewDatabaseTemplateRepository(repo repo.INotificationTemplateRepository) *DatabaseTemplateRepository {
	return &DatabaseTemplateRepository{
		repo: repo,
	}
}

// modelToTemplate converts model.NotificationTemplate to template.Template
func modelToTemplate(m *model.NotificationTemplate) (*Template, error) {
	var variables []string
	if m.Variables != "" {
		if err := sonic.UnmarshalString(m.Variables, &variables); err != nil {
			return nil, fmt.Errorf("failed to unmarshal variables: %w", err)
		}
	}

	var metadata map[string]interface{}
	if m.Metadata != "" {
		if err := sonic.UnmarshalString(m.Metadata, &metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
		}
	}

	return &Template{
		ID:          m.TemplateID,
		Name:        m.Name,
		Type:        Type(m.Type),
		Channel:     m.Channel,
		Title:       m.Title,
		Content:     m.Content,
		Variables:   variables,
		Format:      m.Format,
		Metadata:    metadata,
		Description: m.Description,
	}, nil
}

// templateToModel converts template.Template to model.NotificationTemplate
func templateToModel(t *Template) (*model.NotificationTemplate, error) {
	variablesJSON, err := sonic.MarshalString(t.Variables)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal variables: %w", err)
	}

	metadataJSON := ""
	if len(t.Metadata) > 0 {
		metadataJSON, err = sonic.MarshalString(t.Metadata)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal metadata: %w", err)
		}
	}

	return &model.NotificationTemplate{
		TemplateID:  t.ID,
		Name:        t.Name,
		Type:        string(t.Type),
		Channel:     t.Channel,
		Title:       t.Title,
		Content:     t.Content,
		Variables:   variablesJSON,
		Format:      t.Format,
		Metadata:    metadataJSON,
		Description: t.Description,
		IsActive:    true,
	}, nil
}

// Create creates a new template
func (r *DatabaseTemplateRepository) Create(ctx context.Context, template *Template) error {
	notificationTemplate, err := templateToModel(template)
	if err != nil {
		return err
	}
	return r.repo.Create(ctx, notificationTemplate)
}

// Get retrieves a template by ID
func (r *DatabaseTemplateRepository) Get(ctx context.Context, id string) (*Template, error) {
	templateByID, err := r.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return modelToTemplate(templateByID)
}

// GetByNameAndType retrieves a template by name and type
func (r *DatabaseTemplateRepository) GetByNameAndType(ctx context.Context, name string, templateType Type) (*Template, error) {
	nameAndType, err := r.repo.GetByNameAndType(ctx, name, string(templateType))
	if err != nil {
		return nil, err
	}
	return modelToTemplate(nameAndType)
}

// List lists all templates with optional filtering
func (r *DatabaseTemplateRepository) List(ctx context.Context, filter *Filter) ([]*Template, error) {
	// 将 template.Filter 转换为 repo.NotificationTemplateFilter
	repoFilter := convertTemplateFilter(filter)
	models, err := r.repo.List(ctx, repoFilter)
	if err != nil {
		return nil, err
	}

	templates := make([]*Template, 0, len(models))
	for _, m := range models {
		t, err := modelToTemplate(m)
		if err != nil {
			return nil, err
		}
		templates = append(templates, t)
	}
	return templates, nil
}

// convertTemplateFilter 将 template.Filter 转换为 repo.NotificationTemplateFilter
func convertTemplateFilter(filter *Filter) *repo.NotificationTemplateFilter {
	if filter == nil {
		return nil
	}
	return &repo.NotificationTemplateFilter{
		Type:    string(filter.Type),
		Channel: filter.Channel,
		Name:    filter.Name,
		Limit:   filter.Limit,
		Offset:  filter.Offset,
	}
}

// Update updates an existing template
func (r *DatabaseTemplateRepository) Update(ctx context.Context, template *Template) error {
	notificationTemplate, err := templateToModel(template)
	if err != nil {
		return err
	}
	return r.repo.Update(ctx, notificationTemplate)
}

// Delete deletes a template by ID
func (r *DatabaseTemplateRepository) Delete(ctx context.Context, id string) error {
	return r.repo.Delete(ctx, id)
}

// ListByType lists templates by type
func (r *DatabaseTemplateRepository) ListByType(ctx context.Context, templateType Type) ([]*Template, error) {
	models, err := r.repo.ListByType(ctx, string(templateType))
	if err != nil {
		return nil, err
	}

	templates := make([]*Template, 0, len(models))
	for _, m := range models {
		t, err := modelToTemplate(m)
		if err != nil {
			return nil, err
		}
		templates = append(templates, t)
	}
	return templates, nil
}

// ListByChannel lists templates by channel
func (r *DatabaseTemplateRepository) ListByChannel(ctx context.Context, channel string) ([]*Template, error) {
	models, err := r.repo.ListByChannel(ctx, channel)
	if err != nil {
		return nil, err
	}

	templates := make([]*Template, 0, len(models))
	for _, m := range models {
		t, err := modelToTemplate(m)
		if err != nil {
			return nil, err
		}
		templates = append(templates, t)
	}
	return templates, nil
}
