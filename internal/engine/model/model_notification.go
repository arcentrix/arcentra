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

package model

import (
	"time"
)

// NotificationTemplate represents a notification template in the database
type NotificationTemplate struct {
	ID          uint      `gorm:"primarykey" json:"id"`
	TemplateID  string    `gorm:"uniqueIndex;type:varchar(100);not null" json:"template_id"` // Unique template identifier
	Name        string    `gorm:"type:varchar(200);not null" json:"name"`                    // Template name
	Type        string    `gorm:"type:varchar(50);not null;index" json:"type"`               // Template type (build/approval)
	Channel     string    `gorm:"type:varchar(50);not null;index" json:"channel"`            // Target channel
	Title       string    `gorm:"type:varchar(200)" json:"title"`                            // Template title
	Content     string    `gorm:"type:text;not null" json:"content"`                         // Template content
	Variables   string    `gorm:"type:text" json:"variables"`                                // Required variables (JSON array)
	Format      string    `gorm:"type:varchar(50);default:markdown" json:"format"`           // Message format
	Metadata    string    `gorm:"type:text" json:"metadata"`                                 // Additional metadata (JSON)
	Description string    `gorm:"type:text" json:"description"`                              // Template description
	IsActive    bool      `gorm:"default:true;index" json:"is_active"`                       // Active status
	CreatedAt   time.Time `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

// TableName specifies the table name for NotificationTemplate
func (NotificationTemplate) TableName() string {
	return "notification_templates"
}

// NotificationLog represents a notification sending record
type NotificationLog struct {
	ID         uint      `gorm:"primarykey" json:"id"`
	TemplateID string    `gorm:"type:varchar(100);index" json:"template_id"`     // Template ID used
	Channel    string    `gorm:"type:varchar(50);not null;index" json:"channel"` // Channel name
	Recipient  string    `gorm:"type:varchar(500)" json:"recipient"`             // Recipient info
	Content    string    `gorm:"type:text" json:"content"`                       // Rendered content
	Status     string    `gorm:"type:varchar(50);not null;index" json:"status"`  // Status: success/failed
	ErrorMsg   string    `gorm:"type:text" json:"error_msg"`                     // Error message if failed
	Metadata   string    `gorm:"type:text" json:"metadata"`                      // Additional metadata (JSON)
	SentAt     time.Time `gorm:"index" json:"sent_at"`                           // Sent timestamp
	CreatedAt  time.Time `gorm:"autoCreateTime" json:"created_at"`
}

// TableName specifies the table name for NotificationLog
func (NotificationLog) TableName() string {
	return "notification_logs"
}

// NotificationChannel represents a notification channel configuration in the database
type NotificationChannel struct {
	BaseModel
	ChannelId   string `gorm:"uniqueIndex;type:varchar(100);not null" json:"channel_id"` // Unique channel identifier
	Name        string `gorm:"type:varchar(200);not null" json:"name"`                   // Channel name
	Type        string `gorm:"type:varchar(50);not null;index" json:"type"`              // Channel type (feishu_app/dingtalk/etc)
	Config      string `gorm:"type:text;not null" json:"config"`                         // Channel configuration (JSON)
	AuthConfig  string `gorm:"type:text" json:"auth_config"`                             // Authentication configuration (JSON, optional)
	Description string `gorm:"type:text" json:"description"`                             // Channel description
	IsActive    bool   `gorm:"default:true;index" json:"is_active"`                      // Active status
}

// TableName specifies the table name for NotificationChannel
func (NotificationChannel) TableName() string {
	return "notification_channels"
}
