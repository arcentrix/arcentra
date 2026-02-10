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

package notify

import (
	"context"
	"fmt"
	"sync"

	"github.com/arcentrix/arcentra/internal/pkg/notify/auth"
	"github.com/arcentrix/arcentra/internal/pkg/notify/channel"
	"github.com/arcentrix/arcentra/pkg/log"
)

// NotifyManager manages multiple notification channels
type NotifyManager struct {
	channels    map[string]*channel.NotifyChannel
	mu          sync.RWMutex
	factory     *ChannelFactory
	channelRepo ChannelRepository // 通知配置仓库接口
}

// ChannelRepository 定义通知配置仓库接口
// 此接口应在 repo 层实现，但返回 model.NotificationChannel，由 notify 层转换
type ChannelRepository interface {
	ListActiveChannels(ctx context.Context) ([]*ChannelConfig, error)
}

// NewNotifyManager creates a new notification manager
func NewNotifyManager() *NotifyManager {
	return &NotifyManager{
		channels: make(map[string]*channel.NotifyChannel),
		factory:  &ChannelFactory{},
	}
}

// SetChannelRepository 设置通知配置仓库
func (nm *NotifyManager) SetChannelRepository(repo ChannelRepository) {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.channelRepo = repo
}

// LoadChannelsFromDatabase 从数据库加载所有活跃的通知配置
func (nm *NotifyManager) LoadChannelsFromDatabase(ctx context.Context) error {
	if nm.channelRepo == nil {
		return fmt.Errorf("channel repository is not set")
	}

	configs, err := nm.channelRepo.ListActiveChannels(ctx)
	if err != nil {
		return fmt.Errorf("failed to load channels from database: %w", err)
	}

	var errors []error
	for _, cfg := range configs {
		// 创建 channel 实例
		ch, err := nm.factory.CreateChannel(cfg.Type, cfg.Config)
		if err != nil {
			log.Warnw("failed to create channel", "channel", cfg.Name, "error", err)
			errors = append(errors, fmt.Errorf("channel %s: %w", cfg.Name, err))
			continue
		}

		// 设置认证（如果有）
		if len(cfg.AuthConfig) > 0 {
			authType, _ := cfg.AuthConfig["type"].(string)
			if authType != "" {
				authProvider, err := nm.factory.CreateAuthProvider(auth.AuthType(authType), cfg.AuthConfig)
				if err == nil {
					ch.SetAuth(authProvider)
				} else {
					log.Warnw("failed to create auth provider", "channel", cfg.Name, "error", err)
				}
			}
		}

		// 注册 channel
		notifyChannel := channel.NewNotifyChannel(ch)
		if err := nm.RegisterChannel(cfg.Name, notifyChannel); err != nil {
			log.Warnw("failed to register channel", "channel", cfg.Name, "error", err)
			errors = append(errors, fmt.Errorf("channel %s: %w", cfg.Name, err))
			continue
		}

		log.Infow("notification channel loaded from database", "channel", cfg.Name, "type", cfg.Type)
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to load %d channel(s): %v", len(errors), errors)
	}

	return nil
}

// RegisterChannel registers a notification channel
func (nm *NotifyManager) RegisterChannel(name string, ch *channel.NotifyChannel) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if name == "" {
		return fmt.Errorf("channel name cannot be empty")
	}

	if ch == nil {
		return fmt.Errorf("channel cannot be nil")
	}

	if err := ch.Validate(); err != nil {
		return fmt.Errorf("channel validation failed: %w", err)
	}

	nm.channels[name] = ch
	return nil
}

// GetChannel gets a notification channel by name
func (nm *NotifyManager) GetChannel(name string) (*channel.NotifyChannel, error) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	ch, exists := nm.channels[name]
	if !exists {
		return nil, fmt.Errorf("channel %s not found", name)
	}

	return ch, nil
}

// Send sends a message to a specific channel
func (nm *NotifyManager) Send(ctx context.Context, channelName, message string) error {
	ch, err := nm.GetChannel(channelName)
	if err != nil {
		return err
	}

	return ch.Send(ctx, message)
}

// SendToMultiple sends a message to multiple channels
func (nm *NotifyManager) SendToMultiple(ctx context.Context, channelNames []string, message string) error {
	var errs []error
	for _, name := range channelNames {
		if err := nm.Send(ctx, name, message); err != nil {
			errs = append(errs, fmt.Errorf("channel %s: %w", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("some channels failed: %v", errs)
	}

	return nil
}

// SendWithTemplate sends a message using template
func (nm *NotifyManager) SendWithTemplate(ctx context.Context, channelName, template string, data map[string]interface{}) error {
	ch, err := nm.GetChannel(channelName)
	if err != nil {
		return err
	}

	return ch.SendWithTemplate(ctx, template, data)
}

// UnregisterChannel unregisters a notification channel
func (nm *NotifyManager) UnregisterChannel(name string) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	ch, exists := nm.channels[name]
	if !exists {
		return fmt.Errorf("channel %s not found", name)
	}

	// Close the channel
	if err := ch.Close(); err != nil {
		return fmt.Errorf("failed to close channel: %w", err)
	}

	delete(nm.channels, name)
	return nil
}

// ListChannels lists all registered channels
func (nm *NotifyManager) ListChannels() []string {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	names := make([]string, 0, len(nm.channels))
	for name := range nm.channels {
		names = append(names, name)
	}

	return names
}

// ChannelFactory creates notification channels
type ChannelFactory struct{}

// NewChannelFactory creates a new channel factory
func NewChannelFactory() *ChannelFactory {
	return &ChannelFactory{}
}

// CreateChannel creates a notification channel based on type and configuration
func (cf *ChannelFactory) CreateChannel(channelType ChannelType, config map[string]interface{}) (channel.INotifyChannel, error) {
	switch channelType {
	case ChannelTypeFeishuApp:
		webhookURL, _ := config["webhook_url"].(string)
		secret, _ := config["secret"].(string) // optional
		if webhookURL == "" {
			return nil, fmt.Errorf("webhook_url is required for feishu_app")
		}
		if secret != "" {
			return channel.NewFeishuAppChannelWithSecret(webhookURL, secret), nil
		}
		return channel.NewFeishuAppChannel(webhookURL), nil

	case ChannelTypeFeishuCard:
		appID, _ := config["app_id"].(string)
		appSecret, _ := config["app_secret"].(string)
		if appID == "" || appSecret == "" {
			return nil, fmt.Errorf("app_id and app_secret are required for feishu_card")
		}
		return channel.NewFeishuCardChannel(appID, appSecret), nil

	case ChannelTypeLarkApp:
		webhookURL, _ := config["webhook_url"].(string)
		secret, _ := config["secret"].(string) // optional
		if webhookURL == "" {
			return nil, fmt.Errorf("webhook_url is required for lark_app")
		}
		if secret != "" {
			return channel.NewLarkAppChannelWithSecret(webhookURL, secret), nil
		}
		return channel.NewLarkAppChannel(webhookURL), nil

	case ChannelTypeLarkCard:
		appID, _ := config["app_id"].(string)
		appSecret, _ := config["app_secret"].(string)
		if appID == "" || appSecret == "" {
			return nil, fmt.Errorf("app_id and app_secret are required for lark_card")
		}
		return channel.NewLarkCardChannel(appID, appSecret), nil

	case ChannelTypeDingTalk:
		webhookURL, _ := config["webhook_url"].(string)
		secret, _ := config["secret"].(string)
		if webhookURL == "" {
			return nil, fmt.Errorf("webhook_url is required for dingtalk")
		}
		return channel.NewDingTalkChannel(webhookURL, secret), nil

	case ChannelTypeWeCom:
		webhookURL, _ := config["webhook_url"].(string)
		if webhookURL == "" {
			return nil, fmt.Errorf("webhook_url is required for wecom")
		}
		return channel.NewWeComChannel(webhookURL), nil

	case ChannelTypeWebhook:
		webhookURL, _ := config["webhook_url"].(string)
		method, _ := config["method"].(string)
		if webhookURL == "" {
			return nil, fmt.Errorf("webhook_url is required for webhook")
		}
		return channel.NewWebhookChannel(webhookURL, method), nil

	case ChannelTypeEmail:
		smtpHost, _ := config["smtp_host"].(string)
		smtpPort, _ := config["smtp_port"].(int)
		fromEmail, _ := config["from_email"].(string)
		toEmailsRaw, _ := config["to_emails"].([]interface{})

		var toEmails []string
		for _, email := range toEmailsRaw {
			if e, ok := email.(string); ok {
				toEmails = append(toEmails, e)
			}
		}

		if smtpHost == "" || smtpPort == 0 || fromEmail == "" || len(toEmails) == 0 {
			return nil, fmt.Errorf("smtp_host, smtp_port, from_email, and to_emails are required for email")
		}
		return channel.NewEmailChannel(smtpHost, smtpPort, fromEmail, toEmails), nil

	case ChannelTypeSlack:
		webhookURL, _ := config["webhook_url"].(string)
		if webhookURL == "" {
			return nil, fmt.Errorf("webhook_url is required for slack")
		}
		return channel.NewSlackChannel(webhookURL), nil

	case ChannelTypeTelegram:
		botToken, _ := config["bot_token"].(string)
		chatID, _ := config["chat_id"].(string)
		parseMode, _ := config["parse_mode"].(string)
		if botToken == "" || chatID == "" {
			return nil, fmt.Errorf("bot_token and chat_id are required for telegram")
		}
		if parseMode != "" {
			return channel.NewTelegramChannelWithParseMode(botToken, chatID, parseMode), nil
		}
		return channel.NewTelegramChannel(botToken, chatID), nil

	case ChannelTypeDiscord:
		webhookURL, _ := config["webhook_url"].(string)
		username, _ := config["username"].(string)
		avatarURL, _ := config["avatar_url"].(string)
		if webhookURL == "" {
			return nil, fmt.Errorf("webhook_url is required for discord")
		}
		if username != "" || avatarURL != "" {
			return channel.NewDiscordChannelWithCustom(webhookURL, username, avatarURL), nil
		}
		return channel.NewDiscordChannel(webhookURL), nil

	default:
		return nil, fmt.Errorf("unsupported channel type: %s", channelType)
	}
}

// CreateAuthProvider creates an authentication provider based on type and configuration
func (cf *ChannelFactory) CreateAuthProvider(authType auth.AuthType, config map[string]interface{}) (auth.IAuthProvider, error) {
	switch authType {
	case auth.AuthTypeToken:
		token, _ := config["token"].(string)
		if token == "" {
			return nil, fmt.Errorf("token is required")
		}
		return auth.NewTokenAuth(token), nil

	case auth.AuthTypeBearer:
		token, _ := config["token"].(string)
		if token == "" {
			return nil, fmt.Errorf("token is required")
		}
		return auth.NewBearerAuth(token), nil

	case auth.AuthTypeAPIKey:
		apiKey, _ := config["api_key"].(string)
		headerName, _ := config["header_name"].(string)
		if apiKey == "" {
			return nil, fmt.Errorf("api_key is required")
		}
		return auth.NewAPIKeyAuth(apiKey, headerName), nil

	case auth.AuthTypeBasic:
		username, _ := config["username"].(string)
		password, _ := config["password"].(string)
		if username == "" || password == "" {
			return nil, fmt.Errorf("username and password are required")
		}
		return auth.NewBasicAuth(username, password), nil

	case auth.AuthTypeOAuth2:
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
