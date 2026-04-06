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

import (
	"fmt"

	"github.com/arcentrix/arcentra/internal/domain/notification"
	"github.com/arcentrix/arcentra/internal/infra/notification/auth"
)

// ChannelFactory creates notification channels.
type ChannelFactory struct{}

func NewChannelFactory() *ChannelFactory {
	return &ChannelFactory{}
}

type channelCreator func(map[string]any) (notification.INotifyChannel, error)

var channelCreators = map[notification.ChannelType]channelCreator{
	notification.ChannelTypeFeishuApp:  createFeishuAppChannel,
	notification.ChannelTypeFeishuCard: createFeishuCardChannel,
	notification.ChannelTypeLarkApp:    createLarkAppChannel,
	notification.ChannelTypeLarkCard:   createLarkCardChannel,
	notification.ChannelTypeDingTalk:   createDingTalkChannel,
	notification.ChannelTypeWeCom:      createWeComChannel,
	notification.ChannelTypeWebhook:    createWebhookChannel,
	notification.ChannelTypeEmail:      createEmailChannel,
	notification.ChannelTypeSlack:      createSlackChannel,
	notification.ChannelTypeTelegram:   createTelegramChannel,
	notification.ChannelTypeDiscord:    createDiscordChannel,
}

func (cf *ChannelFactory) CreateChannel(channelType notification.ChannelType, config map[string]any) (notification.INotifyChannel, error) {
	if creator, ok := channelCreators[channelType]; ok {
		return creator(config)
	}
	return nil, fmt.Errorf("unsupported channel type: %s", channelType)
}

func createFeishuAppChannel(config map[string]any) (notification.INotifyChannel, error) {
	webhookURL, _ := config["webhook_url"].(string)
	secret, _ := config["secret"].(string)
	if webhookURL == "" {
		return nil, fmt.Errorf("webhook_url is required for feishu_app")
	}
	if secret != "" {
		return NewFeishuAppChannelWithSecret(webhookURL, secret), nil
	}
	return NewFeishuAppChannel(webhookURL), nil
}

func createFeishuCardChannel(config map[string]any) (notification.INotifyChannel, error) {
	appID, _ := config["app_id"].(string)
	appSecret, _ := config["app_secret"].(string)
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("app_id and app_secret are required for feishu_card")
	}
	return NewFeishuCardChannel(appID, appSecret), nil
}

func createLarkAppChannel(config map[string]any) (notification.INotifyChannel, error) {
	webhookURL, _ := config["webhook_url"].(string)
	secret, _ := config["secret"].(string)
	if webhookURL == "" {
		return nil, fmt.Errorf("webhook_url is required for lark_app")
	}
	if secret != "" {
		return NewLarkAppChannelWithSecret(webhookURL, secret), nil
	}
	return NewLarkAppChannel(webhookURL), nil
}

func createLarkCardChannel(config map[string]any) (notification.INotifyChannel, error) {
	appID, _ := config["app_id"].(string)
	appSecret, _ := config["app_secret"].(string)
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("app_id and app_secret are required for lark_card")
	}
	return NewLarkCardChannel(appID, appSecret), nil
}

func createDingTalkChannel(config map[string]any) (notification.INotifyChannel, error) {
	webhookURL, _ := config["webhook_url"].(string)
	secret, _ := config["secret"].(string)
	if webhookURL == "" {
		return nil, fmt.Errorf("webhook_url is required for dingtalk")
	}
	return NewDingTalkChannel(webhookURL, secret), nil
}

func createWeComChannel(config map[string]any) (notification.INotifyChannel, error) {
	webhookURL, _ := config["webhook_url"].(string)
	if webhookURL == "" {
		return nil, fmt.Errorf("webhook_url is required for wecom")
	}
	return NewWeComChannel(webhookURL), nil
}

func createWebhookChannel(config map[string]any) (notification.INotifyChannel, error) {
	webhookURL, _ := config["webhook_url"].(string)
	method, _ := config["method"].(string)
	if webhookURL == "" {
		return nil, fmt.Errorf("webhook_url is required for webhook")
	}
	return NewWebhookChannel(webhookURL, method), nil
}

func createEmailChannel(config map[string]any) (notification.INotifyChannel, error) {
	smtpHost, _ := config["smtp_host"].(string)
	smtpPort, _ := config["smtp_port"].(int)
	fromEmail, _ := config["from_email"].(string)
	toEmailsRaw, _ := config["to_emails"].([]any)
	var toEmails []string
	for _, email := range toEmailsRaw {
		if e, ok := email.(string); ok {
			toEmails = append(toEmails, e)
		}
	}
	if smtpHost == "" || smtpPort == 0 || fromEmail == "" || len(toEmails) == 0 {
		return nil, fmt.Errorf("smtp_host, smtp_port, from_email, and to_emails are required for email")
	}
	return NewEmailChannel(smtpHost, smtpPort, fromEmail, toEmails), nil
}

func createSlackChannel(config map[string]any) (notification.INotifyChannel, error) {
	webhookURL, _ := config["webhook_url"].(string)
	if webhookURL == "" {
		return nil, fmt.Errorf("webhook_url is required for slack")
	}
	return NewSlackChannel(webhookURL), nil
}

func createTelegramChannel(config map[string]any) (notification.INotifyChannel, error) {
	botToken, _ := config["bot_token"].(string)
	chatID, _ := config["chat_id"].(string)
	parseMode, _ := config["parse_mode"].(string)
	if botToken == "" || chatID == "" {
		return nil, fmt.Errorf("bot_token and chat_id are required for telegram")
	}
	if parseMode != "" {
		return NewTelegramChannelWithParseMode(botToken, chatID, parseMode), nil
	}
	return NewTelegramChannel(botToken, chatID), nil
}

func createDiscordChannel(config map[string]any) (notification.INotifyChannel, error) {
	webhookURL, _ := config["webhook_url"].(string)
	username, _ := config["username"].(string)
	avatarURL, _ := config["avatar_url"].(string)
	if webhookURL == "" {
		return nil, fmt.Errorf("webhook_url is required for discord")
	}
	if username != "" || avatarURL != "" {
		return NewDiscordChannelWithCustom(webhookURL, username, avatarURL), nil
	}
	return NewDiscordChannel(webhookURL), nil
}

func (cf *ChannelFactory) CreateAuthProvider(authType notification.AuthType, config map[string]any) (notification.IAuthProvider, error) {
	switch authType {
	case notification.AuthToken:
		token, _ := config["token"].(string)
		if token == "" {
			return nil, fmt.Errorf("token is required")
		}
		return auth.NewTokenAuth(token), nil

	case notification.AuthBearer:
		token, _ := config["token"].(string)
		if token == "" {
			return nil, fmt.Errorf("token is required")
		}
		return auth.NewBearerAuth(token), nil

	case notification.AuthAPIKey:
		apiKey, _ := config["api_key"].(string)
		headerName, _ := config["header_name"].(string)
		if apiKey == "" {
			return nil, fmt.Errorf("api_key is required")
		}
		return auth.NewAPIKeyAuth(apiKey, headerName), nil

	case notification.AuthBasic:
		username, _ := config["username"].(string)
		password, _ := config["password"].(string)
		if username == "" || password == "" {
			return nil, fmt.Errorf("username and password are required")
		}
		return auth.NewBasicAuth(username, password), nil

	case notification.AuthOAuth2:
		clientID, _ := config["client_id"].(string)
		clientSecret, _ := config["client_secret"].(string)
		tokenURL, _ := config["token_url"].(string)
		accessToken, _ := config["access_token"].(string)
		if clientID == "" || clientSecret == "" {
			return nil, fmt.Errorf("client_id and client_secret are required")
		}
		oauth2Auth := auth.NewOAuth2Auth(clientID, clientSecret, tokenURL)
		if accessToken != "" {
			oauth2Auth.SetAccessToken(accessToken)
		}
		return oauth2Auth, nil

	default:
		return nil, fmt.Errorf("unsupported auth type: %s", authType)
	}
}

var _ notification.IChannelFactory = (*ChannelFactory)(nil)
