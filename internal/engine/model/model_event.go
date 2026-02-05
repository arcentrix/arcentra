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

// SystemEvent 系统事件表
type SystemEvent struct {
	BaseModel
	EventId      string `gorm:"column:event_id" json:"eventId"`
	EventType    int    `gorm:"column:event_type" json:"eventType"`       // 1:任务创建 2:任务开始 3:任务完成 4:任务失败 5:Agent上线 6:流水线开始 7:流水线完成 8:流水线失败
	ResourceType string `gorm:"column:resource_type" json:"resourceType"` // job/pipeline/agent
	ResourceId   string `gorm:"column:resource_id" json:"resourceId"`
	ResourceName string `gorm:"column:resource_name" json:"resourceName"`
	Message      string `gorm:"column:message;type:text" json:"message"`
	Metadata     string `gorm:"column:metadata;type:json" json:"metadata"` // JSON格式
	UserId       string `gorm:"column:user_id" json:"userId"`
}

func (SystemEvent) TableName() string {
	return "t_system_event"
}

// AuditLog 操作审计日志表
type AuditLog struct {
	BaseModel
	UserId         string `gorm:"column:user_id" json:"userId"`
	Username       string `gorm:"column:username" json:"username"`
	Action         string `gorm:"column:action" json:"action"`              // create/update/delete/execute
	ResourceType   string `gorm:"column:resource_type" json:"resourceType"` // pipeline/job/agent/user
	ResourceId     string `gorm:"column:resource_id" json:"resourceId"`
	ResourceName   string `gorm:"column:resource_name" json:"resourceName"`
	IpAddress      string `gorm:"column:ip_address" json:"ipAddress"`
	UserAgent      string `gorm:"column:user_agent" json:"userAgent"`
	RequestParams  string `gorm:"column:request_params;type:json" json:"requestParams"` // JSON格式
	ResponseStatus int    `gorm:"column:response_status" json:"responseStatus"`
	ErrorMessage   string `gorm:"column:error_message;type:text" json:"errorMessage"`
}

func (AuditLog) TableName() string {
	return "t_audit_log"
}
