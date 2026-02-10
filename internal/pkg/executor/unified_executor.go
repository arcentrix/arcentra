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
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/bytedance/sonic"
)

// RemoteExecutor 定义远程执行器接口
type RemoteExecutor interface {
	ExecuteStepRemotely(ctx context.Context, req *RemoteExecutionRequest) (*RemoteExecutionResult, error)
}

// RemoteExecutionRequest 远程执行请求
type RemoteExecutionRequest struct {
	PipelineID string
	BuildID    string
	JobName    string
	StepName   string
	Step       *RemoteStep
	StepIndex  int
	Workspace  string
	Env        map[string]string
	Selector   *RemoteSelector
}

// RemoteStep 远程 step 信息
type RemoteStep struct {
	Name            string
	Uses            string
	Action          string
	Args            map[string]any
	Env             map[string]string
	Timeout         string
	RunRemotely     bool
	RemoteSelector  *RemoteSelector
	ContinueOnError bool
}

// RemoteExecutionResult 远程执行结果
type RemoteExecutionResult struct {
	Success   bool
	ExitCode  int32
	Error     string
	StartTime time.Time
	EndTime   time.Time
	Metrics   map[string]string
}

// UnifiedExecutor 统一执行器
// 根据 RunRemotely 字段自动选择本地 plugin 执行或远程执行
type UnifiedExecutor struct {
	pluginManager  *plugin.Manager
	remoteExecutor RemoteExecutor
	logger         log.Logger
}

// NewUnifiedExecutor 创建统一执行器
func NewUnifiedExecutor(
	pluginManager *plugin.Manager,
	remoteExecutor RemoteExecutor,
	logger log.Logger,
) *UnifiedExecutor {
	return &UnifiedExecutor{
		pluginManager:  pluginManager,
		remoteExecutor: remoteExecutor,
		logger:         logger,
	}
}

// Name 返回执行器名称
func (e *UnifiedExecutor) Name() string {
	return "unified"
}

// CanExecute 检查是否可以执行
// 统一执行器可以执行所有 step（只要 pluginManager 或 remoteExecutor 可用）
func (e *UnifiedExecutor) CanExecute(req *ExecutionRequest) bool {
	if req == nil || req.Step == nil {
		return false
	}
	// 如果需要远程执行，检查 remoteExecutor 是否可用
	if req.Step.RunRemotely {
		return e.remoteExecutor != nil
	}
	// 如果需要本地执行，检查 pluginManager 是否可用
	return e.pluginManager != nil
}

// Execute 执行 step
// 根据 RunRemotely 字段自动选择执行方式
func (e *UnifiedExecutor) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
	if req == nil || req.Step == nil {
		result := NewExecutionResult(e.Name())
		err := fmt.Errorf("step is nil")
		result.Complete(false, -1, err)
		return result, err
	}

	// 根据 RunRemotely 选择执行方式
	if req.Step.RunRemotely {
		return e.executeRemotely(ctx, req)
	}
	return e.executeLocally(ctx, req)
}

// executeRemotely 在远程节点上执行
func (e *UnifiedExecutor) executeRemotely(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
	result := NewExecutionResult(e.Name())

	if e.remoteExecutor == nil {
		err := fmt.Errorf("remote executor is not available")
		result.Complete(false, -1, err)
		return result, err
	}

	// 转换 step 信息
	remoteStep := &RemoteStep{
		Name:            req.Step.Name,
		Uses:            req.Step.Uses,
		Action:          req.Step.Action,
		Args:            req.Step.Args,
		Env:             req.Step.Env,
		Timeout:         req.Step.Timeout,
		RunRemotely:     req.Step.RunRemotely,
		RemoteSelector:  req.Step.RemoteSelector,
		ContinueOnError: req.Options != nil && req.Options.ContinueOnError,
	}

	// 构建远程执行请求
	remoteReq := &RemoteExecutionRequest{
		PipelineID: req.Pipeline.Namespace,
		BuildID:    "", // TODO: 从 context 获取
		JobName:    req.Job.Name,
		StepName:   req.Step.Name,
		Step:       remoteStep,
		StepIndex:  0, // TODO: 从 context 获取
		Workspace:  req.Workspace,
		Env:        req.Env,
		Selector:   req.Step.RemoteSelector,
	}

	// 应用超时
	if req.Options != nil && req.Options.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Options.Timeout)
		defer cancel()
	}

	// 执行
	remoteResult, err := e.remoteExecutor.ExecuteStepRemotely(ctx, remoteReq)
	if err != nil {
		err = fmt.Errorf("remote execution failed: %w", err)
		result.Complete(false, -1, err)
		return result, err
	}

	// 转换结果
	result.Success = remoteResult.Success
	result.ExitCode = remoteResult.ExitCode
	result.Error = remoteResult.Error
	result.StartTime = remoteResult.StartTime
	result.EndTime = remoteResult.EndTime
	result.Duration = remoteResult.EndTime.Sub(remoteResult.StartTime)

	// 设置元数据
	if remoteResult.Metrics != nil {
		for k, v := range remoteResult.Metrics {
			result.WithMetadata(k, v)
		}
	}

	result.Complete(result.Success, result.ExitCode, nil)

	if e.logger.Log != nil {
		e.logger.Log.Debugw("remote execution completed",
			"step", req.Step.Name,
			"success", result.Success,
			"exit_code", result.ExitCode,
			"duration", result.Duration)
	}

	return result, nil
}

// executeLocally 在本地执行
func (e *UnifiedExecutor) executeLocally(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
	result := NewExecutionResult(e.Name())

	if e.pluginManager == nil {
		err := fmt.Errorf("plugin manager is not available")
		result.Complete(false, -1, err)
		return result, err
	}

	// 获取 plugin
	pluginInstance, err := e.pluginManager.GetPlugin(req.Step.Uses)
	if err != nil {
		err = fmt.Errorf("plugin not found: %s: %w", req.Step.Uses, err)
		result.Complete(false, -1, err)
		return result, err
	}

	// 确定 action（默认为 "Execute"）
	action := req.Step.Action
	if action == "" {
		action = "Execute"
	}

	// 解析参数
	var resolvedParams map[string]any
	if req.Step.Args != nil {
		resolvedParams = req.Step.Args
		// TODO: 支持变量解析
	} else {
		resolvedParams = make(map[string]any)
	}

	// 准备参数 JSON
	paramsJSON, err := sonic.Marshal(resolvedParams)
	if err != nil {
		err = fmt.Errorf("marshal params: %w", err)
		result.Complete(false, -1, err)
		return result, err
	}

	// 准备选项 JSON（workspace 和 env）
	opts := map[string]any{
		"workspace": req.Workspace,
		"env":       req.Env,
	}
	if req.Options != nil && req.Options.Timeout > 0 {
		opts["timeout"] = req.Options.Timeout.Seconds()
	}
	optsJSON, err := sonic.Marshal(opts)
	if err != nil {
		err = fmt.Errorf("marshal opts: %w", err)
		result.Complete(false, -1, err)
		return result, err
	}

	// 调用 plugin 方法
	// 注意：超时已通过 opts 传递给 plugin，plugin 内部会处理超时
	pluginResult, err := pluginInstance.Execute(action, paramsJSON, optsJSON)
	if err != nil {
		err = fmt.Errorf("plugin execution failed: %w", err)
		result.Complete(false, -1, err)
		return result, err
	}

	// 解析 plugin 返回结果
	var resultData map[string]any
	if len(pluginResult) > 0 {
		if err := sonic.Unmarshal(pluginResult, &resultData); err == nil {
			// 提取输出信息
			if stdout, ok := resultData["stdout"].(string); ok {
				result.Output = stdout
			}
			if stderr, ok := resultData["stderr"].(string); ok {
				result.ErrorOutput = stderr
			}
			if exitCode, ok := resultData["exit_code"].(float64); ok {
				result.ExitCode = int32(exitCode)
			}
			if success, ok := resultData["success"].(bool); ok {
				result.Success = success
			}
			if progress, ok := resultData["progress"]; ok {
				result.WithMetadata("progress", progress)
			}
			if artifact, ok := resultData["artifact"]; ok {
				result.WithMetadata("artifact", artifact)
			}
		}
	}

	// 如果没有从结果中提取到成功状态，默认为成功
	// 注意：此时 err 一定是 nil（如果 err != nil，函数已在前面返回）
	if !result.Success {
		result.Success = true
		result.ExitCode = 0
	}

	result.Complete(result.Success, result.ExitCode, nil)

	if e.logger.Log != nil {
		e.logger.Log.Debugw("local execution completed",
			"step", req.Step.Name,
			"success", result.Success,
			"exit_code", result.ExitCode,
			"duration", result.Duration)
	}

	return result, nil
}
