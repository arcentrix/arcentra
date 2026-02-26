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

package channel

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"

	"github.com/arcentrix/arcentra/internal/pkg/notify/auth"
)

// EmailChannel implements email notification channel
type EmailChannel struct {
	smtpHost     string
	smtpPort     int
	fromEmail    string
	toEmails     []string
	authProvider auth.IAuthProvider
}

// NewEmailChannel creates a new email notification channel
func NewEmailChannel(smtpHost string, smtpPort int, fromEmail string, toEmails []string) *EmailChannel {
	return &EmailChannel{
		smtpHost:  smtpHost,
		smtpPort:  smtpPort,
		fromEmail: fromEmail,
		toEmails:  toEmails,
	}
}

// SetAuth sets authentication provider (email uses SMTP auth, typically Basic Auth)
func (c *EmailChannel) SetAuth(provider auth.IAuthProvider) error {
	if provider == nil {
		return nil
	}

	// Email typically uses Basic Auth
	if provider.GetAuthType() != auth.Basic {
		return fmt.Errorf("email channel only supports basic auth")
	}

	c.authProvider = provider
	return provider.Validate()
}

// GetAuth gets the authentication provider
func (c *EmailChannel) GetAuth() auth.IAuthProvider {
	return c.authProvider
}

// Send sends email
func (c *EmailChannel) Send(ctx context.Context, message string) error {
	if err := c.Validate(); err != nil {
		return err
	}

	subject := "Notification"
	body := message

	return c.sendEmail(ctx, subject, body)
}

// SendWithTemplate sends email using template
func (c *EmailChannel) SendWithTemplate(ctx context.Context, template string, data map[string]interface{}) error {
	if err := c.Validate(); err != nil {
		return err
	}

	// Simple template replacement (should use more powerful template engine in production)
	body := template
	for k, v := range data {
		body = strings.ReplaceAll(body, "{{"+k+"}}", fmt.Sprintf("%v", v))
	}

	return c.sendEmail(ctx, "Notification", body)
}

// sendEmail sends email message
func (c *EmailChannel) sendEmail(ctx context.Context, subject, body string) error {
	if c.authProvider == nil {
		return fmt.Errorf("auth provider is required for email")
	}

	// Get authentication information
	basicAuth, ok := c.authProvider.(*auth.BasicAuth)
	if !ok {
		return fmt.Errorf("invalid auth provider type for email")
	}

	// Build email message
	msg := "From: " + c.fromEmail + "\r\n" +
		"To: " + strings.Join(c.toEmails, ",") + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"\r\n" + body

	// SMTP authentication
	smtpAuth := smtp.PlainAuth("", basicAuth.Username, basicAuth.Password, c.smtpHost)

	// Send email
	addr := fmt.Sprintf("%s:%d", c.smtpHost, c.smtpPort)
	err := smtp.SendMail(addr, smtpAuth, c.fromEmail, c.toEmails, []byte(msg))
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	return nil
}

// Receive receives email (POP3/IMAP, not implemented here)
func (c *EmailChannel) Receive(ctx context.Context, message string) error {
	return fmt.Errorf("email receive not implemented")
}

// Validate validates the configuration
func (c *EmailChannel) Validate() error {
	if c.smtpHost == "" {
		return fmt.Errorf("smtp host is required")
	}
	if c.smtpPort <= 0 {
		return fmt.Errorf("smtp port is required")
	}
	if c.fromEmail == "" {
		return fmt.Errorf("from email is required")
	}
	if len(c.toEmails) == 0 {
		return fmt.Errorf("to emails are required")
	}
	if c.authProvider != nil {
		return c.authProvider.Validate()
	}
	return nil
}

// Close closes the connection
func (c *EmailChannel) Close() error {
	return nil
}
