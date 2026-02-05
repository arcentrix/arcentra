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

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/bytedance/sonic"
)

// PluginExecutor 插件执行器
// 支持两种执行方式：
// 1. Shell/Plugin 执行：通过 plugin 接口执行（本地或远程）
// 2. HTTP 执行：通过 HTTP 请求执行（当 step args 包含 url 字段时）
type PluginExecutor struct {
	pluginManager *plugin.Manager
	httpExecutor  *HTTPExecutor
	logger        log.Logger
}

// NewPluginExecutor 创建插件执行器
func NewPluginExecutor(pluginManager *plugin.Manager, logger log.Logger) *PluginExecutor {
	return &PluginExecutor{
		pluginManager: pluginManager,
		httpExecutor:  NewHTTPExecutor(logger),
		logger:        logger,
	}
}

// Name 返回执行器名称
func (e *PluginExecutor) Name() string {
	return "plugin"
}

// CanExecute 检查是否可以执行
// 插件执行器可以执行所有不需要 RunRemotely 的 step
func (e *PluginExecutor) CanExecute(req *ExecutionRequest) bool {
	if req == nil || req.Step == nil {
		return false
	}
	// 如果 step 指定了 RunRemotely，则不应该使用插件执行器
	return !req.Step.RunRemotely
}

// Execute 执行 step
// 根据 step 的 args 判断执行方式：
// - 如果 args 中包含 url 字段，使用 HTTP 执行
// - 否则使用 Shell/Plugin 执行（本地或远程）
func (e *PluginExecutor) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
	if req.Step == nil {
		result := NewExecutionResult(e.Name())
		err := fmt.Errorf("step is nil")
		result.Complete(false, -1, err)
		return result, err
	}

	// 判断执行类型：检查 args 中是否包含 url 字段
	if req.Step.Args != nil {
		if url, ok := req.Step.Args["url"].(string); ok && url != "" {
			// 使用 HTTP 执行
			return e.executeHTTP(ctx, req)
		}
	}

	// 使用 Shell/Plugin 执行（本地或远程）
	// 获取 plugin
	pluginInstance, err := e.pluginManager.GetPlugin(req.Step.Uses)
	if err != nil {
		result := NewExecutionResult(e.Name())
		err = fmt.Errorf("plugin not found: %s: %w", req.Step.Uses, err)
		result.Complete(false, -1, err)
		return result, err
	}

	return e.executePlugin(ctx, req, pluginInstance)
}

// executePlugin 通过 plugin 调用执行（Shell 类型）
// 支持本地执行和远程执行，由 UnifiedExecutor 根据 RunRemotely 字段决定
func (e *PluginExecutor) executePlugin(ctx context.Context, req *ExecutionRequest, pluginInstance plugin.Plugin) (*ExecutionResult, error) {
	result := NewExecutionResult(e.Name())

	// 确定 action（默认为 "Execute"）
	action := req.Step.Action
	if action == "" {
		action = "Execute"
	}

	// 解析参数
	var resolvedParams map[string]any
	if req.Step.Args != nil {
		resolvedParams = req.Step.Args
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
		}
	}

	// 如果没有从结果中提取到成功状态，默认为成功
	if !result.Success {
		result.Success = true
		result.ExitCode = 0
	}

	result.Complete(result.Success, result.ExitCode, nil)

	if e.logger.Log != nil {
		e.logger.Log.Debugw("plugin execution completed",
			"step", req.Step.Name,
			"success", result.Success,
			"exit_code", result.ExitCode,
			"duration", result.Duration)
	}

	return result, nil
}

// executeHTTP 通过 HTTP 执行（HTTP 类型）
// 当 step args 包含 url 字段时，使用 HTTP 执行器执行 HTTP 请求
func (e *PluginExecutor) executeHTTP(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error) {
	if e.logger.Log != nil {
		e.logger.Log.Debugw("executing step via HTTP",
			"step", req.Step.Name,
			"url", req.Step.Args["url"])
	}
	// 使用 HTTP 执行器执行
	return e.httpExecutor.Execute(ctx, req)
}
