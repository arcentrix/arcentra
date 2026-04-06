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

package notification

import "context"

// INotificationChannelRepo defines the interface for notification channel persistence.
type INotificationChannelRepo interface {
	ListActive(ctx context.Context) ([]*NotificationChannelModel, error)
}

// INotificationTemplateRepo defines the interface for notification template persistence.
type INotificationTemplateRepo interface {
	Create(ctx context.Context, t *NotificationTemplateModel) error
	Get(ctx context.Context, id string) (*NotificationTemplateModel, error)
	GetByNameAndType(ctx context.Context, name, templateType string) (*NotificationTemplateModel, error)
	List(ctx context.Context, filter *NotificationTemplateFilter) ([]*NotificationTemplateModel, error)
	Update(ctx context.Context, t *NotificationTemplateModel) error
	Delete(ctx context.Context, id string) error
	ListByType(ctx context.Context, templateType string) ([]*NotificationTemplateModel, error)
	ListByChannel(ctx context.Context, channel string) ([]*NotificationTemplateModel, error)
}

// ITemplateRepository defines the interface for template storage at the domain level.
type ITemplateRepository interface {
	Create(ctx context.Context, template *Template) error
	Get(ctx context.Context, id string) (*Template, error)
	GetByNameAndType(ctx context.Context, name string, templateType TemplateType) (*Template, error)
	List(ctx context.Context, filter *TemplateFilter) ([]*Template, error)
	Update(ctx context.Context, template *Template) error
	Delete(ctx context.Context, id string) error
	ListByType(ctx context.Context, templateType TemplateType) ([]*Template, error)
	ListByChannel(ctx context.Context, channel string) ([]*Template, error)
}

// ChannelRepository defines a repository that returns channel configs.
type ChannelRepository interface {
	ListActiveChannels(ctx context.Context) ([]*ChannelConfig, error)
}
