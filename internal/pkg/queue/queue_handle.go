// Copyright 2025 Arcade Team
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

	"github.com/arcentrix/arcade/pkg/log"
)

// PipelineTaskHandler 流水线任务处理器
type PipelineTaskHandler struct {
	// 可以注入其他依赖，如 task service, agent manager 等
}

func NewPipelineTaskHandler() *PipelineTaskHandler {
	return &PipelineTaskHandler{}
}

func (h *PipelineTaskHandler) HandleTask(ctx context.Context, payload *TaskPayload) error {
	log.Infow("handling pipeline task",
		"task_id", payload.TaskID,
		"pipeline_id", payload.PipelineID,
		"stage", payload.Stage,
	)

	// TODO: 实现流水线任务处理逻辑
	// 1. 更新任务状态为运行中
	// 2. 选择 Agent
	// 3. 执行任务
	// 4. 更新任务状态

	return nil
}

// JobTaskHandler 作业任务处理器
type JobTaskHandler struct {
	// 可以注入其他依赖
}

func NewJobTaskHandler() *JobTaskHandler {
	return &JobTaskHandler{}
}

func (h *JobTaskHandler) HandleTask(ctx context.Context, payload *TaskPayload) error {
	log.Infow("handling job task",
		"task_id", payload.TaskID,
		"pipeline_id", payload.PipelineID,
		"name", payload.Name,
	)

	// TODO: 实现作业任务处理逻辑

	return nil
}

// StepTaskHandler 步骤任务处理器
type StepTaskHandler struct {
	// 可以注入其他依赖
}

func NewStepTaskHandler() *StepTaskHandler {
	return &StepTaskHandler{}
}

func (h *StepTaskHandler) HandleTask(ctx context.Context, payload *TaskPayload) error {
	log.Infow("handling step task",
		"task_id", payload.TaskID,
		"stage_id", payload.StageID,
		"commands", payload.Commands,
	)

	// TODO: 实现步骤任务处理逻辑
	// 1. 执行命令
	// 2. 收集日志
	// 3. 处理结果

	return nil
}

// TaskStatusUpdater 任务状态更新器接口
type TaskStatusUpdater interface {
	UpdateTaskStatus(ctx context.Context, taskID string, status int, result map[string]interface{}) error
}

// TaskExecutor 任务执行器接口
type TaskExecutor interface {
	ExecuteTask(ctx context.Context, payload *TaskPayload) error
}

// DefaultTaskHandler 默认任务处理器（带状态更新和执行器）
type DefaultTaskHandler struct {
	statusUpdater TaskStatusUpdater
	executor      TaskExecutor
}

func NewDefaultTaskHandler(statusUpdater TaskStatusUpdater, executor TaskExecutor) *DefaultTaskHandler {
	return &DefaultTaskHandler{
		statusUpdater: statusUpdater,
		executor:      executor,
	}
}

func (h *DefaultTaskHandler) HandleTask(ctx context.Context, payload *TaskPayload) error {
	startTime := time.Now()
	taskID := payload.TaskID

	log.Infow("starting task execution",
		"task_id", taskID,
		"task_type", payload.TaskType,
	)

	// 更新状态为运行中
	if h.statusUpdater != nil {
		if err := h.statusUpdater.UpdateTaskStatus(ctx, taskID, 3, map[string]interface{}{
			"start_time": startTime,
		}); err != nil {
			log.Errorw("failed to update task status to running", "task_id", taskID, "error", err)
		}
	}

	// 执行任务
	var execErr error
	if h.executor != nil {
		execErr = h.executor.ExecuteTask(ctx, payload)
	} else {
		execErr = fmt.Errorf("no executor configured")
	}

	duration := time.Since(startTime)

	// 更新任务状态
	if h.statusUpdater != nil {
		status := 4 // 成功
		result := map[string]any{
			"end_time": time.Now(),
			"duration": duration.Milliseconds(),
		}

		if execErr != nil {
			status = 5 // 失败
			result["error"] = execErr.Error()
		}

		if err := h.statusUpdater.UpdateTaskStatus(ctx, taskID, status, result); err != nil {
			log.Errorw("failed to update task status", "task_id", taskID, "error", err)
		}
	}

	if execErr != nil {
		log.Errorw("task execution failed",
			"task_id", taskID,
			"duration", duration,
			"error", execErr,
		)
		return execErr
	}

	log.Infow("task execution completed",
		"task_id", taskID,
		"duration", duration,
	)

	return nil
}
