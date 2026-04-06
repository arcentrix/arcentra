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

// ChannelConfig holds notification channel configuration.
type ChannelConfig struct {
	ChannelID  string                 `json:"channelId"`
	Name       string                 `json:"name"`
	Type       ChannelType            `json:"type"`
	Config     map[string]interface{} `json:"config"`
	AuthConfig map[string]interface{} `json:"authConfig"`
}

// NotificationChannelModel represents a notification channel from the database.
type NotificationChannelModel struct {
	ChannelID  string `json:"channelId"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Config     string `json:"config"`
	AuthConfig string `json:"authConfig"`
	IsEnabled  bool   `json:"isEnabled"`
}

// Template represents a notification template.
type Template struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Type        TemplateType           `json:"type"`
	Channel     string                 `json:"channel"`
	Title       string                 `json:"title"`
	Content     string                 `json:"content"`
	Variables   []string               `json:"variables"`
	Format      string                 `json:"format"`
	Metadata    map[string]interface{} `json:"metadata"`
	Description string                 `json:"description"`
}

// NotificationTemplateModel represents a notification template from the database.
type NotificationTemplateModel struct {
	TemplateID  string `json:"templateId"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Channel     string `json:"channel"`
	Title       string `json:"title"`
	Content     string `json:"content"`
	Variables   string `json:"variables"`
	Format      string `json:"format"`
	Metadata    string `json:"metadata"`
	Description string `json:"description"`
	IsActive    bool   `json:"isActive"`
}

// NotificationTemplateFilter represents filter criteria for listing templates.
type NotificationTemplateFilter struct {
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Name    string `json:"name"`
	Limit   int    `json:"limit"`
	Offset  int    `json:"offset"`
}

// TemplateFilter used at the domain/case level.
type TemplateFilter struct {
	Type    TemplateType `json:"type"`
	Channel string       `json:"channel"`
	Name    string       `json:"name"`
	Limit   int          `json:"limit"`
	Offset  int          `json:"offset"`
}
