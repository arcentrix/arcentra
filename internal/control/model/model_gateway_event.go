// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package model

// GatewayEvent 保存经过认证的 Agent 原始事件，不触发流水线状态变更或执行载荷
// 同一 Agent 的序号及事件标识分别唯一，允许不同 Agent 使用相同的 outbox 事件标识
type GatewayEvent struct {
	BaseModel
	AgentID     string `gorm:"column:agent_id;type:varchar(64);not null;uniqueIndex:uk_gateway_event_seq,priority:1;uniqueIndex:uk_gateway_event_id,priority:1"`
	Seq         uint64 `gorm:"column:seq;not null;uniqueIndex:uk_gateway_event_seq,priority:2"`
	EventID     string `gorm:"column:event_id;type:varchar(128);not null;uniqueIndex:uk_gateway_event_id,priority:2"`
	EventType   string `gorm:"column:event_type;type:varchar(256);not null"`
	PipelineID  string `gorm:"column:pipeline_id;type:varchar(64)"`
	StepID      string `gorm:"column:step_id;type:varchar(64)"`
	Timestamp   string `gorm:"column:timestamp;type:varchar(40)"`
	Payload     string `gorm:"column:payload;type:longtext;not null"`
	Fingerprint string `gorm:"column:fingerprint;type:varchar(64);not null"`
}

// TableName 返回原始 Agent 事件表名
func (GatewayEvent) TableName() string { return "gateway_events" }

// GatewayEventCursor 保存每个 Agent 已持久化的连续序号，和事件在同一事务中提交
type GatewayEventCursor struct {
	BaseModel
	AgentID         string `gorm:"column:agent_id;type:varchar(64);not null;uniqueIndex:uk_gateway_event_cursor_agent"`
	LastAcceptedSeq uint64 `gorm:"column:last_accepted_seq;not null"`
}

// TableName 返回 Agent 事件接收游标表名
func (GatewayEventCursor) TableName() string { return "gateway_event_cursors" }
