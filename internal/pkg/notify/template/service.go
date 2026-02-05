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

package template

import (
	"context"
	"fmt"
)

// TemplateService provides template management functionality
type TemplateService struct {
	repository ITemplateRepository
	engine     *TemplateEngine
}

// NewTemplateService creates a new template service
func NewTemplateService(repository ITemplateRepository) *TemplateService {
	return &TemplateService{
		repository: repository,
		engine:     NewTemplateEngine(),
	}
}

// CreateTemplate creates a new template
func (s *TemplateService) CreateTemplate(ctx context.Context, template *Template) error {
	// Validate template content
	if err := s.engine.ValidateTemplate(template.Content); err != nil {
		return fmt.Errorf("invalid template content: %w", err)
	}

	// Extract variables from template
	template.Variables = s.engine.ExtractVariables(template.Content)

	return s.repository.Create(ctx, template)
}

// GetTemplate retrieves a template by ID
func (s *TemplateService) GetTemplate(ctx context.Context, id string) (*Template, error) {
	return s.repository.Get(ctx, id)
}

// GetTemplateByNameAndType retrieves a template by name and type
func (s *TemplateService) GetTemplateByNameAndType(ctx context.Context, name string, templateType TemplateType) (*Template, error) {
	return s.repository.GetByNameAndType(ctx, name, templateType)
}

// RenderTemplate renders a template with the given data
func (s *TemplateService) RenderTemplate(ctx context.Context, templateID string, data map[string]interface{}) (string, error) {
	template, err := s.repository.Get(ctx, templateID)
	if err != nil {
		return "", err
	}

	return s.engine.Render(template.Content, data)
}

// RenderTemplateByName renders a template by name and type
func (s *TemplateService) RenderTemplateByName(ctx context.Context, name string, templateType TemplateType, data map[string]interface{}) (string, error) {
	template, err := s.repository.GetByNameAndType(ctx, name, templateType)
	if err != nil {
		return "", err
	}

	return s.engine.Render(template.Content, data)
}

// RenderTemplateSimple renders a template using simple variable replacement
func (s *TemplateService) RenderTemplateSimple(ctx context.Context, templateID string, data map[string]interface{}) (string, error) {
	template, err := s.repository.Get(ctx, templateID)
	if err != nil {
		return "", err
	}

	return s.engine.RenderSimple(template.Content, data), nil
}

// UpdateTemplate updates an existing template
func (s *TemplateService) UpdateTemplate(ctx context.Context, template *Template) error {
	// Validate template content
	if err := s.engine.ValidateTemplate(template.Content); err != nil {
		return fmt.Errorf("invalid template content: %w", err)
	}

	// Extract variables from template
	template.Variables = s.engine.ExtractVariables(template.Content)

	return s.repository.Update(ctx, template)
}

// DeleteTemplate deletes a template by ID
func (s *TemplateService) DeleteTemplate(ctx context.Context, id string) error {
	return s.repository.Delete(ctx, id)
}

// ListTemplates lists all templates with optional filtering
func (s *TemplateService) ListTemplates(ctx context.Context, filter *TemplateFilter) ([]*Template, error) {
	return s.repository.List(ctx, filter)
}

// ListBuildTemplates lists all build-related templates
func (s *TemplateService) ListBuildTemplates(ctx context.Context) ([]*Template, error) {
	return s.repository.ListByType(ctx, TemplateTypeBuild)
}

// ListApprovalTemplates lists all approval-related templates
func (s *TemplateService) ListApprovalTemplates(ctx context.Context) ([]*Template, error) {
	return s.repository.ListByType(ctx, TemplateTypeApproval)
}

// ListTemplatesByChannel lists templates by channel
func (s *TemplateService) ListTemplatesByChannel(ctx context.Context, channel string) ([]*Template, error) {
	return s.repository.ListByChannel(ctx, channel)
}
