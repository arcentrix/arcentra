// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package config

import "fmt"

// ExecutionCapacity 返回当前串行 Kafka 工作器实际支持的作业容量
// 零值表示默认容量一，不支持负值或大于一的并行配置
func (a AgentInfo) ExecutionCapacity() (int32, error) {
	if a.MaxConcurrentJobs < 0 || a.MaxConcurrentJobs > 1 {
		return 0, fmt.Errorf("agent.maxConcurrentJobs=%d is unsupported: the Kafka worker executes one job at a time; use 1 (or 0 for the default)", a.MaxConcurrentJobs)
	}
	return 1, nil
}
