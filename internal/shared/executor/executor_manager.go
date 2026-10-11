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

package executor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/plugin"
)

// ErrBuildLogPersistence 表示构建日志无法持久化，不能将内部取消误判为用户取消
var ErrBuildLogPersistence = errors.New("build log persistence failed")

type executionContextError struct {
	cause error
	err   error
}

func (e *executionContextError) Error() string { return e.err.Error() }
func (e *executionContextError) Unwrap() error { return e.err }

// ExecutionContextCause 返回执行上下文的取消或超时原因，不包含日志发布窗口超时
func ExecutionContextCause(err error) error {
	var executionErr *executionContextError
	if errors.As(err, &executionErr) {
		return executionErr.cause
	}
	return nil
}

// Manager 执行器管理器，负责注册与选择执行器。
type Manager struct {
	executors []Executor
	mu        sync.RWMutex
	emitter   *EventEmitter
	logPub    LogPublisher
}

// NewExecutorManager 创建执行器管理器。
func NewExecutorManager() *Manager {
	return &Manager{
		executors: make([]Executor, 0),
	}
}

// Register 注册执行器。
func (m *Manager) Register(executor Executor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if executor != nil {
		m.executors = append(m.executors, executor)
	}
}

// SelectExecutor 按注册顺序选择第一个可执行该请求的执行器。
func (m *Manager) SelectExecutor(req *ExecutionRequest) (Executor, error) {
	if req == nil || req.Step == nil {
		return nil, fmt.Errorf("nil request or step")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, executor := range m.executors {
		if executor.CanExecute(req) {
			return executor, nil
		}
	}

	return nil, fmt.Errorf("no executor available for step: %s", req.Step.Name)
}

// Execute 选择执行器并执行 step，会触发 started/success/failure 事件。
func (m *Manager) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
	executor, err := m.SelectExecutor(req)
	if err != nil {
		m.emitFailure(ctx, req, nil, err, "")
		return nil, err
	}

	return m.ExecuteWithExecutor(ctx, req, executor)
}

// ExecuteWithExecutor 使用指定的请求级执行器并保留事件与日志处理
func (m *Manager) ExecuteWithExecutor(ctx context.Context, req *ExecutionRequest, executor Executor) (*ExecutionResult, error) {
	if req == nil || req.Step == nil || executor == nil {
		return nil, fmt.Errorf("execution request and executor are required")
	}
	executionCtx := ctx
	if req.Options != nil && req.Options.Timeout > 0 {
		var cancel context.CancelFunc
		executionCtx, cancel = context.WithTimeout(ctx, req.Options.Timeout)
		defer cancel()
	}
	runCtx, cancelRun := context.WithCancel(executionCtx)
	defer cancelRun()
	m.emitStarted(runCtx, req, executor.Name())
	request := *req
	redactor := newSecretRedactor(req.Env)
	var outputMu sync.Mutex
	var logErr error
	var canceledLogDeadline time.Time
	streamed := make(map[string]bool)
	eventCtx := buildEventContext(req)
	emitter := m.getEmitter()
	publisher := m.getLogPublisher()
	var publishOutput func(string, string, bool)
	if emitter != nil || publisher != nil || req.OutputHandler != nil {
		// 调用方持有 outputMu，串行处理输出、脱敏状态和首次持久化错误
		publishOutput = func(stream, content string, notifyHandler bool) {
			if content == "" || logErr != nil {
				return
			}
			// 取消后继续接收管道尾部输出，但所有尾部输出共享有界的发布窗口
			deadline := time.Now().Add(3 * time.Second)
			if runCtx.Err() != nil {
				if canceledLogDeadline.IsZero() {
					canceledLogDeadline = deadline
				}
				deadline = canceledLogDeadline
			}
			logCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
			defer cancel()
			for len(content) > 0 {
				n := min(len(content), 64*1024)
				chunk := content[:n]
				content = content[n:]
				if err := m.publishBuildLog(logCtx, publisher, eventCtx, chunk, stream); err != nil {
					logErr = redactedExecutionError{cause: errors.Join(ErrBuildLogPersistence, err), message: ErrBuildLogPersistence.Error() + ": " + redactor.redact(err.Error())}
					cancelRun()
					return
				}
				if notifyHandler && req.OutputHandler != nil {
					req.OutputHandler(stream, chunk)
				}
				if emitter != nil {
					emitter.Emit(logCtx, plugin.EventTypeTaskLog, emitter.BuildSource(executor.Name()), eventCtx.Subject(), map[string]any{"stream": stream, "content": chunk}, eventCtx.Extensions())
				}
			}
		}
		request.OutputHandler = func(stream, content string) {
			outputMu.Lock()
			defer outputMu.Unlock()
			if logErr != nil || content == "" {
				return
			}
			streamed[stream] = true
			if safe := redactor.chunk(stream, content, false); safe != "" {
				publishOutput(stream, safe, true)
			}
		}
	}
	result, execErr := executor.Execute(runCtx, &request)
	outputMu.Lock()
	if publishOutput != nil {
		for stream := range redactor.pending {
			if safe := redactor.chunk(stream, "", true); safe != "" {
				publishOutput(stream, safe, true)
			}
		}
	}
	if result != nil {
		result.Output = redactor.redact(result.Output)
		result.ErrorOutput = redactor.redact(result.ErrorOutput)
		result.Error = redactor.redact(result.Error)
		// 不支持流式输出的执行器也必须先持久化日志，再报告最终状态
		if publishOutput != nil && result.Metadata["logsStreamed"] != true {
			if !streamed["stdout"] {
				publishOutput("stdout", result.Output, false)
			}
			if !streamed["stderr"] {
				publishOutput("stderr", result.ErrorOutput, false)
			}
		}
	}
	publishErr := logErr
	outputMu.Unlock()
	if execErr != nil {
		execErr = redactedExecutionError{cause: execErr, message: redactor.redact(execErr.Error())}
	}
	if execErr == nil && result == nil {
		execErr = fmt.Errorf("executor returned no result")
	}
	if execErr == nil && !result.Success {
		if result.Error != "" {
			execErr = errors.New(result.Error)
		} else {
			execErr = fmt.Errorf("execution failed with exit code %d", result.ExitCode)
		}
	}
	if publishErr != nil {
		// 保留原始命令错误及取消语义，同时确保日志拒收永远不会变成成功
		execErr = errors.Join(execErr, publishErr)
	}
	if cause := executionCtx.Err(); cause != nil {
		if !errors.Is(execErr, cause) {
			execErr = errors.Join(execErr, cause)
		}
		execErr = &executionContextError{cause: cause, err: execErr}
	}
	if execErr != nil && result != nil {
		result.Success = false
		result.Error = execErr.Error()
		if result.ExitCode == 0 {
			result.ExitCode = 1
		}
	}
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if execErr != nil {
		m.emitFailure(reportCtx, req, result, execErr, executor.Name())
		return result, execErr
	}
	m.emitSuccess(reportCtx, req, result, executor.Name())
	return result, nil
}

// ListExecutors 列出所有已注册执行器。
func (m *Manager) ListExecutors() []Executor {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]Executor, len(m.executors))
	copy(result, m.executors)
	return result
}

// GetExecutor 按名称返回执行器。
func (m *Manager) GetExecutor(name string) Executor {
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
func (m *Manager) SetEventEmitter(emitter *EventEmitter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emitter = emitter
}

// SetEventPublisher creates an EventEmitter with the given publisher.
func (m *Manager) SetEventPublisher(publisher EventPublisher, config EventEmitterConfig) {
	if publisher == nil {
		return
	}
	m.SetEventEmitter(NewEventEmitter(publisher, config))
}

// SetLogPublisher sets the log publisher for build logs.
func (m *Manager) SetLogPublisher(publisher LogPublisher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logPub = publisher
}

func (m *Manager) getEmitter() *EventEmitter {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.emitter
}

func (m *Manager) getLogPublisher() LogPublisher {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.logPub
}

func (m *Manager) emitStarted(ctx context.Context, req *ExecutionRequest, executorName string) {
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

func (m *Manager) emitSuccess(ctx context.Context, req *ExecutionRequest, result *ExecutionResult, executorName string) {
	emitter := m.getEmitter()
	eventCtx := buildEventContext(req)
	if emitter == nil {
		return
	}
	durationMs := int64(0)
	if result != nil {
		durationMs = result.Duration.Milliseconds()
	}
	data := map[string]any{
		"status":     "succeeded",
		"durationMs": durationMs,
	}
	emitter.Emit(ctx, plugin.EventTypeTaskSucceeded, emitter.BuildSource(executorName), eventCtx.Subject(), data, eventCtx.Extensions())
	m.emitProgress(ctx, emitter, eventCtx, executorName, result)
	m.emitArtifact(ctx, emitter, eventCtx, executorName, result)
	m.emitFinished(ctx, emitter, eventCtx, executorName, "succeeded", durationMs)
}

func (m *Manager) emitFailure(ctx context.Context,
	req *ExecutionRequest,
	result *ExecutionResult,
	execErr error,
	executorName string,
) {
	emitter := m.getEmitter()
	eventCtx := buildEventContext(req)
	if emitter == nil {
		return
	}
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
	m.emitProgress(ctx, emitter, eventCtx, executorName, result)
	m.emitArtifact(ctx, emitter, eventCtx, executorName, result)
	m.emitFinished(ctx, emitter, eventCtx, executorName, "failed", durationMs)
}

func (m *Manager) emitFinished(
	ctx context.Context,
	emitter *EventEmitter,
	eventCtx EventContext,
	executorName, status string,
	durationMs int64,
) {
	data := map[string]any{
		"status":     status,
		"durationMs": durationMs,
	}
	emitter.Emit(ctx, plugin.EventTypeTaskFinished, emitter.BuildSource(executorName), eventCtx.Subject(), data, eventCtx.Extensions())
}

func (m *Manager) publishBuildLog(ctx context.Context, publisher LogPublisher, eventCtx EventContext, content, stream string) error {
	if publisher == nil || content == "" {
		return nil
	}
	entry := BuildLogMessageFromEvent(eventCtx, content, stream)
	return publisher.Publish(ctx, entry)
}

func (m *Manager) emitProgress(
	ctx context.Context,
	emitter *EventEmitter,
	eventCtx EventContext,
	executorName string,
	result *ExecutionResult,
) {
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

func (m *Manager) emitArtifact(
	ctx context.Context,
	emitter *EventEmitter,
	eventCtx EventContext,
	executorName string,
	result *ExecutionResult,
) {
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
