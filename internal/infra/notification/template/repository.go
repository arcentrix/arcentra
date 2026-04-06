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

package template

import (
	"context"
	"fmt"

	"github.com/arcentrix/arcentra/internal/domain/notification"
	"github.com/bytedance/sonic"
)

type DatabaseTemplateRepository struct {
	repo notification.INotificationTemplateRepo
}

func NewDatabaseTemplateRepository(r notification.INotificationTemplateRepo) *DatabaseTemplateRepository {
	return &DatabaseTemplateRepository{repo: r}
}

func modelToTemplate(m *notification.NotificationTemplateModel) (*notification.Template, error) {
	var variables []string
	if m.Variables != "" {
		if err := sonic.UnmarshalString(m.Variables, &variables); err != nil {
			return nil, fmt.Errorf("failed to unmarshal variables: %w", err)
		}
	}

	var metadata map[string]any
	if m.Metadata != "" {
		if err := sonic.UnmarshalString(m.Metadata, &metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
		}
	}

	return &notification.Template{
		ID:          m.TemplateID,
		Name:        m.Name,
		Type:        notification.TemplateType(m.Type),
		Channel:     m.Channel,
		Title:       m.Title,
		Content:     m.Content,
		Variables:   variables,
		Format:      m.Format,
		Metadata:    metadata,
		Description: m.Description,
	}, nil
}

func templateToModel(t *notification.Template) (*notification.NotificationTemplateModel, error) {
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

	return &notification.NotificationTemplateModel{
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

func (r *DatabaseTemplateRepository) Create(ctx context.Context, template *notification.Template) error {
	m, err := templateToModel(template)
	if err != nil {
		return err
	}
	return r.repo.Create(ctx, m)
}

func (r *DatabaseTemplateRepository) Get(ctx context.Context, id string) (*notification.Template, error) {
	m, err := r.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return modelToTemplate(m)
}

func (r *DatabaseTemplateRepository) GetByNameAndType(
	ctx context.Context,
	name string,
	templateType notification.TemplateType,
) (*notification.Template, error) {
	m, err := r.repo.GetByNameAndType(ctx, name, string(templateType))
	if err != nil {
		return nil, err
	}
	return modelToTemplate(m)
}

func (r *DatabaseTemplateRepository) List(ctx context.Context, filter *notification.TemplateFilter) ([]*notification.Template, error) {
	repoFilter := convertTemplateFilter(filter)
	models, err := r.repo.List(ctx, repoFilter)
	if err != nil {
		return nil, err
	}

	templates := make([]*notification.Template, 0, len(models))
	for _, m := range models {
		t, err := modelToTemplate(m)
		if err != nil {
			return nil, err
		}
		templates = append(templates, t)
	}
	return templates, nil
}

func convertTemplateFilter(filter *notification.TemplateFilter) *notification.NotificationTemplateFilter {
	if filter == nil {
		return nil
	}
	return &notification.NotificationTemplateFilter{
		Type:    string(filter.Type),
		Channel: filter.Channel,
		Name:    filter.Name,
		Limit:   filter.Limit,
		Offset:  filter.Offset,
	}
}

func (r *DatabaseTemplateRepository) Update(ctx context.Context, template *notification.Template) error {
	m, err := templateToModel(template)
	if err != nil {
		return err
	}
	return r.repo.Update(ctx, m)
}

func (r *DatabaseTemplateRepository) Delete(ctx context.Context, id string) error {
	return r.repo.Delete(ctx, id)
}

func (r *DatabaseTemplateRepository) ListByType(
	ctx context.Context,
	templateType notification.TemplateType,
) ([]*notification.Template, error) {
	models, err := r.repo.ListByType(ctx, string(templateType))
	if err != nil {
		return nil, err
	}

	templates := make([]*notification.Template, 0, len(models))
	for _, m := range models {
		t, err := modelToTemplate(m)
		if err != nil {
			return nil, err
		}
		templates = append(templates, t)
	}
	return templates, nil
}

func (r *DatabaseTemplateRepository) ListByChannel(ctx context.Context, channel string) ([]*notification.Template, error) {
	models, err := r.repo.ListByChannel(ctx, channel)
	if err != nil {
		return nil, err
	}

	templates := make([]*notification.Template, 0, len(models))
	for _, m := range models {
		t, err := modelToTemplate(m)
		if err != nil {
			return nil, err
		}
		templates = append(templates, t)
	}
	return templates, nil
}
