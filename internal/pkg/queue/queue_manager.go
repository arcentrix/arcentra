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

package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
)

// TaskManager 任务管理器
type TaskManager struct {
	server *Server
}

// NewTaskManager 创建任务管理器
func NewTaskManager(server *Server) *TaskManager {
	return &TaskManager{
		server: server,
	}
}

// EnqueueTask 根据权重入队任务
// priorityWeight: 任务权重，根据配置的队列权重自动选择最合适的队列
func (m *TaskManager) EnqueueTask(ctx context.Context, payload *TaskPayload, priorityWeight int) error {
	if payload == nil {
		return fmt.Errorf("task payload is required")
	}
	if m.server == nil {
		return fmt.Errorf("queue server is required")
	}
	// 设置默认值
	if payload.Timeout == 0 {
		payload.Timeout = 3600 // 默认1小时
	}
	if payload.RetryCount == 0 {
		payload.RetryCount = 3
	}
	payload.Priority = priorityWeight
	return m.server.EnqueueWithPriority(payload, priorityWeight)
}

// EnqueueDelayedTask 入队延迟任务
func (m *TaskManager) EnqueueDelayedTask(ctx context.Context, payload *TaskPayload, delay time.Duration) error {
	if m.server == nil {
		return fmt.Errorf("queue server is required")
	}
	return m.server.EnqueueDelayed(payload, delay, "")
}

// CancelTask 取消任务
func (m *TaskManager) CancelTask(ctx context.Context, taskID string) error {
	if m.server == nil {
		return fmt.Errorf("queue server is required")
	}
	inspector := asynq.NewInspector(m.server.GetRedisConnOpt())
	return inspector.DeleteTask("", taskID)
}

// GetTaskStatus 获取任务状态
func (m *TaskManager) GetTaskStatus(ctx context.Context, taskID string) (string, error) {
	if m.server == nil {
		return "unknown", fmt.Errorf("queue server is required")
	}
	inspector := asynq.NewInspector(m.server.GetRedisConnOpt())
	taskInfo, err := inspector.GetTaskInfo("", taskID)
	if err != nil {
		return "unknown", err
	}
	return taskInfo.State.String(), nil
}

// GetQueueStats 获取队列统计信息
func (m *TaskManager) GetQueueStats(ctx context.Context) (map[string]interface{}, error) {
	if m.server == nil {
		return nil, fmt.Errorf("queue server is required")
	}
	inspector := asynq.NewInspector(m.server.GetRedisConnOpt())
	queues, err := inspector.Queues()
	if err != nil {
		return nil, err
	}

	stats := make(map[string]interface{})
	for _, queue := range queues {
		queueInfo, err := inspector.GetQueueInfo(queue)
		if err != nil {
			continue
		}
		stats[queue] = map[string]interface{}{
			"pending":   queueInfo.Pending,
			"active":    queueInfo.Active,
			"scheduled": queueInfo.Scheduled,
			"retry":     queueInfo.Retry,
			"archived":  queueInfo.Archived,
		}
	}
	return stats, nil
}
