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

	"github.com/arcentrix/arcentra/internal/engine/config"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/plugin"
)

// PipelineAdapter 将 pipeline 的 step 和 job 适配为执行器格式
type PipelineAdapter struct {
	executorManager *ExecutorManager
	logger          log.Logger
}

// NewPipelineAdapter 创建 pipeline 适配器
func NewPipelineAdapter(executorManager *ExecutorManager, logger log.Logger) *PipelineAdapter {
	return &PipelineAdapter{
		executorManager: executorManager,
		logger:          logger,
	}
}

// ExecuteStep 执行 pipeline step
func (a *PipelineAdapter) ExecuteStep(
	ctx context.Context,
	pipeline *PipelineInfo,
	job *JobInfo,
	step *StepInfo,
	workspace string,
	env map[string]string,
	options *ExecutionOptions,
) (*ExecutionResult, error) {
	req := &ExecutionRequest{
		Step:      step,
		Job:       job,
		Pipeline:  pipeline,
		Workspace: workspace,
		Env:       env,
		Options:   options,
	}

	if options == nil {
		req.Options = &ExecutionOptions{
			Extra: make(map[string]any),
		}
	}

	return a.executorManager.Execute(ctx, req)
}

// ConvertPipelineInfo 转换 pipeline 信息
func ConvertPipelineInfo(namespace string, version string, variables map[string]string) *PipelineInfo {
	return &PipelineInfo{
		Namespace: namespace,
		Version:   version,
		Variables: variables,
	}
}

// ConvertJobInfo 转换 job 信息
func ConvertJobInfo(
	name string,
	description string,
	env map[string]string,
	maxAttempts int,
	retryDelay string,
) *JobInfo {
	job := &JobInfo{
		Name:        name,
		Description: description,
		Env:         env,
	}

	if maxAttempts > 0 {
		job.Retry = &RetryInfo{
			MaxAttempts: maxAttempts,
			Delay:       retryDelay,
		}
	}

	return job
}

// ConvertStepInfo 转换 step 信息
func ConvertStepInfo(
	name string,
	uses string,
	action string,
	args map[string]any,
	env map[string]string,
	timeout string,
	runRemotely bool,
	remoteSelector *RemoteSelector,
) *StepInfo {
	return &StepInfo{
		Name:           name,
		Uses:           uses,
		Action:         action,
		Args:           args,
		Env:            env,
		Timeout:        timeout,
		RunRemotely:    runRemotely,
		RemoteSelector: remoteSelector,
	}
}

// NewExecutorManagerWithDefaults 创建带默认执行器的执行器管理器
func NewExecutorManagerWithDefaults(
	pluginManager *plugin.Manager,
	remoteExecutor RemoteExecutor,
	logger log.Logger,
) *ExecutorManager {
	manager := NewExecutorManager()

	// 注册统一执行器（根据 RunRemotely 自动选择本地或远程执行）
	if pluginManager != nil || remoteExecutor != nil {
		unifiedExec := NewUnifiedExecutor(pluginManager, remoteExecutor, logger)
		manager.Register(unifiedExec)
	}

	// 注册插件执行器（根据 ExecutionType 自动选择执行方式）
	if pluginManager != nil {
		pluginExec := NewPluginExecutor(pluginManager, logger)
		manager.Register(pluginExec)
	}

	return manager
}

// NewExecutorManagerWithDefaultsAndEvents creates a default executor manager with event publishing enabled.
func NewExecutorManagerWithDefaultsAndEvents(
	pluginManager *plugin.Manager,
	remoteExecutor RemoteExecutor,
	logger log.Logger,
	appConf *config.AppConfig,
) *ExecutorManager {
	manager := NewExecutorManagerWithDefaults(pluginManager, remoteExecutor, logger)
	publisher := NewEventPublisherFromConfig(appConf)
	if publisher != nil {
		manager.SetEventPublisher(publisher, BuildEventEmitterConfig(appConf))
	}
	return manager
}
