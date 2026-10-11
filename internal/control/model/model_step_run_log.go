// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package model

// StepRunLog 保存可重试写入的步骤日志，LogID 同时作为去重键
// 保留现有 step_run_logs 表名以兼容历史日志
type StepRunLog struct {
	BaseModel
	LogID      string `gorm:"column:log_id;type:varchar(64);uniqueIndex:uk_step_run_log_id" json:"-"`
	StepRunID  string `gorm:"column:step_run_id;type:varchar(64);index:idx_step_run_log_line,priority:1" json:"stepRunId"`
	Timestamp  int64  `gorm:"column:timestamp;index:idx_step_run_log_time" json:"timestamp"`
	LineNumber int32  `gorm:"column:line_number;index:idx_step_run_log_line,priority:2" json:"lineNumber"`
	Level      string `gorm:"column:level;type:varchar(32)" json:"level"`
	Content    string `gorm:"column:content;type:longtext" json:"content"`
	Stream     string `gorm:"column:stream;type:varchar(16)" json:"stream"`
	PluginName string `gorm:"column:plugin_name;type:varchar(128)" json:"pluginName"`
	AgentID    string `gorm:"column:agent_id;type:varchar(64)" json:"agentId"`
}

// TableName 返回日志表名
func (StepRunLog) TableName() string { return "step_run_logs" }
