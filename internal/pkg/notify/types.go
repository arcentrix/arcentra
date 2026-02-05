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

package notify

// ChannelType represents the notification channel type
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

// ChannelConfig 通知配置结构
type ChannelConfig struct {
	ChannelID  string
	Name       string
	Type       ChannelType
	Config     map[string]interface{}
	AuthConfig map[string]interface{}
}
