// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package taskqueue

import (
	"fmt"
	"strings"
)

// ValidateAgentStepUses 校验 Agent 当前明确支持的步骤类型与动作
// 空 uses 仅保留旧队列 Shell 任务兼容性，流水线静态校验仍要求显式 uses
// 此校验不加载原生插件，不将未知名称按命令或 URL 自动降级执行
func ValidateAgentStepUses(uses, action string) error {
	switch strings.ToLower(strings.TrimSpace(uses)) {
	case "", "shell", "builtin:shell":
		switch action {
		case "", "run", "script", "command":
			return nil
		}
	case "builtin:artifacts":
		if action == "upload" || action == "download" {
			return nil
		}
	case "http":
		if action == "" || action == "request" {
			return nil
		}
	default:
		return fmt.Errorf("agent step uses %q is unsupported: native plugins are not available; use shell, builtin:artifacts, or explicit http", uses)
	}
	return fmt.Errorf("agent step action %q is unsupported for uses %q", action, uses)
}

// AgentRuntimeCapabilities 保存独立于用户标签的 Agent 运行时就绪报告
// 缺失报告或未知版本不能获得离线容器任务
type AgentRuntimeCapabilities struct {
	ExecutionMode             string `json:"executionMode,omitempty"`
	ContainerdOfflineV1       bool   `json:"containerdOfflineV1,omitempty"`
	ContainerdWholeJobRetryV1 bool   `json:"containerdWholeJobRetryV1,omitempty"`
}
