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

// ChannelType represents the notification channel type.
type ChannelType string

const (
	ChannelTypeFeishuApp  ChannelType = "feishu_app"
	ChannelTypeFeishuCard ChannelType = "feishu_card"
	ChannelTypeLarkApp    ChannelType = "lark_app"
	ChannelTypeLarkCard   ChannelType = "lark_card"
	ChannelTypeDingTalk   ChannelType = "dingtalk"
	ChannelTypeWeCom      ChannelType = "wecom"
	ChannelTypeWebhook    ChannelType = "webhook"
	ChannelTypeEmail      ChannelType = "email"
	ChannelTypeSlack      ChannelType = "slack"
	ChannelTypeTelegram   ChannelType = "telegram"
	ChannelTypeDiscord    ChannelType = "discord"
)

// AuthType represents the authentication type.
type AuthType string

const (
	AuthToken  AuthType = "token"
	AuthOAuth2 AuthType = "oauth2"
	AuthAPIKey AuthType = "apikey"
	AuthBasic  AuthType = "basic"
	AuthBearer AuthType = "bearer"
)

// TemplateType represents the type of notification template.
type TemplateType string

const (
	TemplateBuild    TemplateType = "build"
	TemplateApproval TemplateType = "approval"
)
