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

package service

import (
	"context"
	"fmt"

	"github.com/arcentrix/arcentra/internal/pkg/notify"
	"github.com/arcentrix/arcentra/internal/pkg/notify/channel"
	"github.com/arcentrix/arcentra/internal/pkg/notify/template"
)

// NotificationService provides high-level notification functionality with template support
type NotificationService struct {
	manager         *notify.NotifyManager
	templateService *template.TemplateService
}

// NewNotificationService creates a new notification service
func NewNotificationService(manager *notify.NotifyManager, templateService *template.TemplateService) *NotificationService {
	return &NotificationService{
		manager:         manager,
		templateService: templateService,
	}
}

// SendWithTemplate sends a notification using a template
func (s *NotificationService) SendWithTemplate(ctx context.Context, channelName, templateID string, data map[string]interface{}) error {
	// Render template
	content, err := s.templateService.RenderTemplate(ctx, templateID, data)
	if err != nil {
		return fmt.Errorf("failed to render template: %w", err)
	}

	// Send to channel
	return s.manager.Send(ctx, channelName, content)
}

// SendWithTemplateByName sends a notification using a template name and type
func (s *NotificationService) SendWithTemplateByName(ctx context.Context, channelName, templateName string, templateType template.TemplateType, data map[string]interface{}) error {
	// Render template
	content, err := s.templateService.RenderTemplateByName(ctx, templateName, templateType, data)
	if err != nil {
		return fmt.Errorf("failed to render template: %w", err)
	}

	// Send to channel
	return s.manager.Send(ctx, channelName, content)
}

// SendToMultipleWithTemplate sends a notification to multiple channels using a template
func (s *NotificationService) SendToMultipleWithTemplate(ctx context.Context, channelNames []string, templateID string, data map[string]interface{}) error {
	// Render template
	content, err := s.templateService.RenderTemplate(ctx, templateID, data)
	if err != nil {
		return fmt.Errorf("failed to render template: %w", err)
	}

	// Send to multiple channels
	return s.manager.SendToMultiple(ctx, channelNames, content)
}

// SendBuildNotification sends a build-related notification
func (s *NotificationService) SendBuildNotification(ctx context.Context, channelName, templateName string, data map[string]interface{}) error {
	return s.SendWithTemplateByName(ctx, channelName, templateName, template.TemplateTypeBuild, data)
}

// SendApprovalNotification sends an approval-related notification
func (s *NotificationService) SendApprovalNotification(ctx context.Context, channelName, templateName string, data map[string]interface{}) error {
	return s.SendWithTemplateByName(ctx, channelName, templateName, template.TemplateTypeApproval, data)
}

// BroadcastBuildNotification broadcasts a build notification to multiple channels
func (s *NotificationService) BroadcastBuildNotification(ctx context.Context, channelNames []string, templateName string, data map[string]interface{}) error {
	// Get template
	tmpl, err := s.templateService.GetTemplateByNameAndType(ctx, templateName, template.TemplateTypeBuild)
	if err != nil {
		return fmt.Errorf("failed to get template: %w", err)
	}

	// Render template
	content, err := s.templateService.RenderTemplate(ctx, tmpl.ID, data)
	if err != nil {
		return fmt.Errorf("failed to render template: %w", err)
	}

	// Send to multiple channels
	return s.manager.SendToMultiple(ctx, channelNames, content)
}

// BroadcastApprovalNotification broadcasts an approval notification to multiple channels
func (s *NotificationService) BroadcastApprovalNotification(ctx context.Context, channelNames []string, templateName string, data map[string]interface{}) error {
	// Get template
	tmpl, err := s.templateService.GetTemplateByNameAndType(ctx, templateName, template.TemplateTypeApproval)
	if err != nil {
		return fmt.Errorf("failed to get template: %w", err)
	}

	// Render template
	content, err := s.templateService.RenderTemplate(ctx, tmpl.ID, data)
	if err != nil {
		return fmt.Errorf("failed to render template: %w", err)
	}

	// Send to multiple channels
	return s.manager.SendToMultiple(ctx, channelNames, content)
}

// RegisterChannel registers a notification channel
func (s *NotificationService) RegisterChannel(name string, ch *channel.NotifyChannel) error {
	return s.manager.RegisterChannel(name, ch)
}
