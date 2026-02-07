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

package executor

import (
	"context"
	"fmt"
	"sync"

	"github.com/arcentrix/arcentra/pkg/plugin"
)

// ExecutorManager 执行器管理器
// 负责管理和选择合适的执行器
type ExecutorManager struct {
	executors []Executor
	mu        sync.RWMutex
	emitter   *EventEmitter
}

// NewExecutorManager 创建执行器管理器
func NewExecutorManager() *ExecutorManager {
	return &ExecutorManager{
		executors: make([]Executor, 0),
	}
}

// Register 注册执行器
func (m *ExecutorManager) Register(executor Executor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executors = append(m.executors, executor)
}

// SelectExecutor 选择合适的执行器
// 按照注册顺序检查每个执行器是否可以执行
func (m *ExecutorManager) SelectExecutor(req *ExecutionRequest) (Executor, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, executor := range m.executors {
		if executor.CanExecute(req) {
			return executor, nil
		}
	}

	return nil, fmt.Errorf("no executor available for step: %s", req.Step.Name)
}

// Execute 执行 step
// 自动选择合适的执行器并执行
func (m *ExecutorManager) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
	executor, err := m.SelectExecutor(req)
	if err != nil {
		m.emitFailure(ctx, req, nil, err, "")
		return nil, err
	}

	m.emitStarted(ctx, req, executor.Name())

	result, execErr := executor.Execute(ctx, req)
	if execErr != nil {
		m.emitFailure(ctx, req, result, execErr, executor.Name())
		return result, execErr
	}

	m.emitSuccess(ctx, req, result, executor.Name())
	return result, nil
}

// ListExecutors 列出所有注册的执行器
func (m *ExecutorManager) ListExecutors() []Executor {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]Executor, len(m.executors))
	copy(result, m.executors)
	return result
}

// GetExecutor 根据名称获取执行器
func (m *ExecutorManager) GetExecutor(name string) Executor {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, executor := range m.executors {
		if executor.Name() == name {
			return executor
		}
	}

	return nil
}

// SetEventEmitter sets the EventEmitter for the manager.
func (m *ExecutorManager) SetEventEmitter(emitter *EventEmitter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emitter = emitter
}

// SetEventPublisher creates an EventEmitter with the given publisher.
func (m *ExecutorManager) SetEventPublisher(publisher EventPublisher, config EventEmitterConfig) {
	if publisher == nil {
		return
	}
	m.SetEventEmitter(NewEventEmitter(publisher, config))
}

func (m *ExecutorManager) getEmitter() *EventEmitter {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.emitter
}

func (m *ExecutorManager) emitStarted(ctx context.Context, req *ExecutionRequest, executorName string) {
	emitter := m.getEmitter()
	if emitter == nil {
		return
	}
	if req == nil {
		return
	}
	eventCtx := buildEventContext(req)
	data := map[string]any{
		"status":    "running",
		"workspace": req.Workspace,
		"message":   "execution started",
	}
	emitter.Emit(ctx, plugin.EventTypeTaskStarted, emitter.BuildSource(executorName), eventCtx.Subject(), data, eventCtx.Extensions())
}

func (m *ExecutorManager) emitSuccess(ctx context.Context, req *ExecutionRequest, result *ExecutionResult, executorName string) {
	emitter := m.getEmitter()
	if emitter == nil {
		return
	}
	eventCtx := buildEventContext(req)
	durationMs := int64(0)
	if result != nil {
		durationMs = result.Duration.Milliseconds()
	}
	data := map[string]any{
		"status":     "succeeded",
		"durationMs": durationMs,
	}
	emitter.Emit(ctx, plugin.EventTypeTaskSucceeded, emitter.BuildSource(executorName), eventCtx.Subject(), data, eventCtx.Extensions())
	m.emitLogs(ctx, emitter, eventCtx, executorName, result)
	m.emitProgress(ctx, emitter, eventCtx, executorName, result)
	m.emitArtifact(ctx, emitter, eventCtx, executorName, result)
	m.emitFinished(ctx, emitter, eventCtx, executorName, "succeeded", durationMs)
}

func (m *ExecutorManager) emitFailure(ctx context.Context, req *ExecutionRequest, result *ExecutionResult, execErr error, executorName string) {
	emitter := m.getEmitter()
	if emitter == nil {
		return
	}
	eventCtx := buildEventContext(req)
	durationMs := int64(0)
	exitCode := int32(-1)
	if result != nil {
		durationMs = result.Duration.Milliseconds()
		exitCode = result.ExitCode
	}
	data := map[string]any{
		"status":       "failed",
		"errorMessage": execErr.Error(),
		"exitCode":     exitCode,
	}
	emitter.Emit(ctx, plugin.EventTypeTaskFailed, emitter.BuildSource(executorName), eventCtx.Subject(), data, eventCtx.Extensions())
	m.emitLogs(ctx, emitter, eventCtx, executorName, result)
	m.emitProgress(ctx, emitter, eventCtx, executorName, result)
	m.emitArtifact(ctx, emitter, eventCtx, executorName, result)
	m.emitFinished(ctx, emitter, eventCtx, executorName, "failed", durationMs)
}

func (m *ExecutorManager) emitFinished(ctx context.Context, emitter *EventEmitter, eventCtx EventContext, executorName, status string, durationMs int64) {
	data := map[string]any{
		"status":     status,
		"durationMs": durationMs,
	}
	emitter.Emit(ctx, plugin.EventTypeTaskFinished, emitter.BuildSource(executorName), eventCtx.Subject(), data, eventCtx.Extensions())
}

func (m *ExecutorManager) emitLogs(ctx context.Context, emitter *EventEmitter, eventCtx EventContext, executorName string, result *ExecutionResult) {
	if result == nil {
		return
	}
	if result.Output != "" {
		data := map[string]any{
			"stream":  "stdout",
			"content": result.Output,
		}
		emitter.Emit(ctx, plugin.EventTypeTaskLog, emitter.BuildSource(executorName), eventCtx.Subject(), data, eventCtx.Extensions())
	}
	if result.ErrorOutput != "" {
		data := map[string]any{
			"stream":  "stderr",
			"content": result.ErrorOutput,
		}
		emitter.Emit(ctx, plugin.EventTypeTaskLog, emitter.BuildSource(executorName), eventCtx.Subject(), data, eventCtx.Extensions())
	}
}

func (m *ExecutorManager) emitProgress(ctx context.Context, emitter *EventEmitter, eventCtx EventContext, executorName string, result *ExecutionResult) {
	if result == nil || len(result.Metadata) == 0 {
		return
	}
	value, ok := result.Metadata["progress"]
	if !ok {
		return
	}
	parsed, ok := normalizeAnyMap(value)
	if !ok {
		return
	}
	data := normalizeMapKeys(parsed)
	if len(data) == 0 {
		return
	}
	emitter.Emit(ctx, plugin.EventTypeTaskProgress, emitter.BuildSource(executorName), eventCtx.Subject(), data, eventCtx.Extensions())
}

func (m *ExecutorManager) emitArtifact(ctx context.Context, emitter *EventEmitter, eventCtx EventContext, executorName string, result *ExecutionResult) {
	if result == nil || len(result.Metadata) == 0 {
		return
	}
	value, ok := result.Metadata["artifact"]
	if !ok {
		return
	}
	parsed, ok := normalizeAnyMap(value)
	if !ok {
		return
	}
	data := normalizeMapKeys(parsed)
	if len(data) == 0 {
		return
	}
	emitter.Emit(ctx, plugin.EventTypeTaskArtifact, emitter.BuildSource(executorName), eventCtx.Subject(), data, eventCtx.Extensions())
}
