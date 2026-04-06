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
	"time"

	domain "github.com/arcentrix/arcentra/internal/domain/notification"
	"github.com/bytedance/sonic"
)

// ---------------------------------------------------------------------------
// NotificationChannelPO
// ---------------------------------------------------------------------------

// NotificationChannelPO is the GORM persistence object for the t_notification_channels table.
type NotificationChannelPO struct {
	ID          uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	ChannelID   string    `gorm:"column:channel_id"`
	Name        string    `gorm:"column:name"`
	Type        string    `gorm:"column:type"`
	Config      string    `gorm:"column:config"`
	AuthConfig  string    `gorm:"column:auth_config"`
	Description string    `gorm:"column:description"`
	IsActive    int       `gorm:"column:is_active"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName returns the database table name.
func (NotificationChannelPO) TableName() string { return "t_notification_channels" }

// ToDomainModel converts to the NotificationChannelModel used by the repo interface.
func (po *NotificationChannelPO) ToDomainModel() *domain.NotificationChannelModel {
	return &domain.NotificationChannelModel{
		ChannelID:  po.ChannelID,
		Name:       po.Name,
		Type:       po.Type,
		Config:     po.Config,
		AuthConfig: po.AuthConfig,
		IsEnabled:  po.IsActive == 1,
	}
}

// ToDomainChannelConfig converts to the ChannelConfig domain entity.
func (po *NotificationChannelPO) ToDomainChannelConfig() *domain.ChannelConfig {
	config := make(map[string]interface{})
	authConfig := make(map[string]interface{})

	if po.Config != "" {
		_ = sonic.UnmarshalString(po.Config, &config)
	}
	if po.AuthConfig != "" {
		_ = sonic.UnmarshalString(po.AuthConfig, &authConfig)
	}

	return &domain.ChannelConfig{
		ChannelID:  po.ChannelID,
		Name:       po.Name,
		Type:       domain.ChannelType(po.Type),
		Config:     config,
		AuthConfig: authConfig,
	}
}

// ---------------------------------------------------------------------------
// NotificationTemplatePO
// ---------------------------------------------------------------------------

// NotificationTemplatePO is the GORM persistence object for the t_notification_templates table.
type NotificationTemplatePO struct {
	ID          uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	TemplateID  string    `gorm:"column:template_id"`
	Name        string    `gorm:"column:name"`
	Type        string    `gorm:"column:type"`
	Channel     string    `gorm:"column:channel"`
	Title       string    `gorm:"column:title"`
	Content     string    `gorm:"column:content"`
	Variables   string    `gorm:"column:variables"`
	Format      string    `gorm:"column:format"`
	Metadata    string    `gorm:"column:metadata"`
	Description string    `gorm:"column:description"`
	IsActive    int       `gorm:"column:is_active"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName returns the database table name.
func (NotificationTemplatePO) TableName() string { return "t_notification_templates" }

// ToDomainModel converts to the NotificationTemplateModel used by the repo interface.
func (po *NotificationTemplatePO) ToDomainModel() *domain.NotificationTemplateModel {
	return &domain.NotificationTemplateModel{
		TemplateID:  po.TemplateID,
		Name:        po.Name,
		Type:        po.Type,
		Channel:     po.Channel,
		Title:       po.Title,
		Content:     po.Content,
		Variables:   po.Variables,
		Format:      po.Format,
		Metadata:    po.Metadata,
		Description: po.Description,
		IsActive:    po.IsActive == 1,
	}
}

// NotificationTemplatePOFromModel creates a PO from a NotificationTemplateModel.
func NotificationTemplatePOFromModel(m *domain.NotificationTemplateModel) *NotificationTemplatePO {
	isActive := 0
	if m.IsActive {
		isActive = 1
	}
	return &NotificationTemplatePO{
		TemplateID:  m.TemplateID,
		Name:        m.Name,
		Type:        m.Type,
		Channel:     m.Channel,
		Title:       m.Title,
		Content:     m.Content,
		Variables:   m.Variables,
		Format:      m.Format,
		Metadata:    m.Metadata,
		Description: m.Description,
		IsActive:    isActive,
	}
}

// ToDomainTemplate converts to the Template domain entity.
func (po *NotificationTemplatePO) ToDomainTemplate() *domain.Template {
	var variables []string
	if po.Variables != "" {
		_ = sonic.UnmarshalString(po.Variables, &variables)
	}
	metadata := make(map[string]interface{})
	if po.Metadata != "" {
		_ = sonic.UnmarshalString(po.Metadata, &metadata)
	}

	return &domain.Template{
		ID:          po.TemplateID,
		Name:        po.Name,
		Type:        domain.TemplateType(po.Type),
		Channel:     po.Channel,
		Title:       po.Title,
		Content:     po.Content,
		Variables:   variables,
		Format:      po.Format,
		Metadata:    metadata,
		Description: po.Description,
	}
}

// NotificationTemplatePOFromDomain creates a PO from a domain Template entity.
func NotificationTemplatePOFromDomain(t *domain.Template) *NotificationTemplatePO {
	var variablesJSON, metadataJSON string
	if t.Variables != nil {
		if b, err := sonic.MarshalString(t.Variables); err == nil {
			variablesJSON = b
		}
	}
	if t.Metadata != nil {
		if b, err := sonic.MarshalString(t.Metadata); err == nil {
			metadataJSON = b
		}
	}

	return &NotificationTemplatePO{
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
		IsActive:    1,
	}
}
