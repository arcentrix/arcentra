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

	"github.com/arcentrix/arcentra/internal/domain/notification"
)

// Service provides template management functionality.
type Service struct {
	repository notification.ITemplateRepository
	engine     *Engine
}

func NewTemplateService(repository notification.ITemplateRepository) *Service {
	return &Service{
		repository: repository,
		engine:     NewTemplateEngine(),
	}
}

func (s *Service) CreateTemplate(ctx context.Context, template *notification.Template) error {
	if err := s.engine.ValidateTemplate(template.Content); err != nil {
		return fmt.Errorf("invalid template content: %w", err)
	}
	template.Variables = s.engine.ExtractVariables(template.Content)
	return s.repository.Create(ctx, template)
}

func (s *Service) GetTemplate(ctx context.Context, id string) (*notification.Template, error) {
	return s.repository.Get(ctx, id)
}

func (s *Service) GetTemplateByNameAndType(
	ctx context.Context,
	name string,
	templateType notification.TemplateType,
) (*notification.Template, error) {
	return s.repository.GetByNameAndType(ctx, name, templateType)
}

func (s *Service) RenderTemplate(ctx context.Context, templateID string, data map[string]interface{}) (string, error) {
	tmpl, err := s.repository.Get(ctx, templateID)
	if err != nil {
		return "", err
	}
	return s.engine.Render(tmpl.Content, data)
}

func (s *Service) RenderTemplateByName(
	ctx context.Context,
	name string,
	templateType notification.TemplateType,
	data map[string]interface{},
) (string, error) {
	tmpl, err := s.repository.GetByNameAndType(ctx, name, templateType)
	if err != nil {
		return "", err
	}
	return s.engine.Render(tmpl.Content, data)
}

func (s *Service) RenderTemplateSimple(ctx context.Context, templateID string, data map[string]interface{}) (string, error) {
	tmpl, err := s.repository.Get(ctx, templateID)
	if err != nil {
		return "", err
	}
	return s.engine.RenderSimple(tmpl.Content, data), nil
}

func (s *Service) UpdateTemplate(ctx context.Context, template *notification.Template) error {
	if err := s.engine.ValidateTemplate(template.Content); err != nil {
		return fmt.Errorf("invalid template content: %w", err)
	}
	template.Variables = s.engine.ExtractVariables(template.Content)
	return s.repository.Update(ctx, template)
}

func (s *Service) DeleteTemplate(ctx context.Context, id string) error {
	return s.repository.Delete(ctx, id)
}

func (s *Service) ListTemplates(ctx context.Context, filter *notification.TemplateFilter) ([]*notification.Template, error) {
	return s.repository.List(ctx, filter)
}

func (s *Service) ListBuildTemplates(ctx context.Context) ([]*notification.Template, error) {
	return s.repository.ListByType(ctx, notification.TemplateBuild)
}

func (s *Service) ListApprovalTemplates(ctx context.Context) ([]*notification.Template, error) {
	return s.repository.ListByType(ctx, notification.TemplateApproval)
}

func (s *Service) ListTemplatesByChannel(ctx context.Context, channel string) ([]*notification.Template, error) {
	return s.repository.ListByChannel(ctx, channel)
}
