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
	"strings"
	"time"
)

// Executor 定义了执行器的统一接口
// 执行器负责执行 pipeline step，支持多种执行方式（本地、远程、容器等）
type Executor interface {
	// Execute 执行一个 step
	// 返回执行结果和错误
	Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResult, error)

	// CanExecute 检查是否可以执行指定的 step
	// 用于执行器选择逻辑
	CanExecute(req *ExecutionRequest) bool

	// Name 返回执行器名称
	Name() string
}

// ExecutionRequest 执行请求
type ExecutionRequest struct {
	// Step 信息
	Step *StepInfo

	// Job 信息
	Job *JobInfo

	// Pipeline 信息
	Pipeline *PipelineInfo

	// 执行环境
	Workspace string
	Env       map[string]string

	// 执行选项
	Options *ExecutionOptions
}

// StepInfo step 信息
type StepInfo struct {
	Name           string
	Uses           string // Plugin name
	Action         string // Plugin action
	Args           map[string]any
	Env            map[string]string
	Timeout        string
	RunRemotely    bool
	RemoteSelector *RemoteSelector
}

// JobInfo job 信息
type JobInfo struct {
	Name        string
	Description string
	Env         map[string]string
	Retry       *RetryInfo
}

// RetryInfo 重试信息
type RetryInfo struct {
	MaxAttempts int
	Delay       string
}

// PipelineInfo pipeline 信息
type PipelineInfo struct {
	Namespace string
	Version   string
	Variables map[string]string
}

// RemoteSelector 远程执行节点选择器
type RemoteSelector struct {
	MatchLabels      map[string]string
	MatchExpressions []LabelExpression
}

// LabelExpression 标签表达式
type LabelExpression struct {
	Key      string
	Operator string // In, NotIn, Exists, NotExists, Gt, Lt
	Values   []string
}

// ExecutionOptions 执行选项
type ExecutionOptions struct {
	// 超时时间
	Timeout time.Duration

	// 是否允许失败后继续
	ContinueOnError bool

	// 重试次数
	RetryCount int

	// 重试延迟
	RetryDelay time.Duration

	// 其他选项
	Extra map[string]any
}

// ExecutionResult 执行结果
type ExecutionResult struct {
	// 执行是否成功
	Success bool

	// 退出码（如果适用）
	ExitCode int32

	// 错误信息
	Error string

	// 执行开始时间
	StartTime time.Time

	// 执行结束时间
	EndTime time.Time

	// 执行时长
	Duration time.Duration

	// 输出（stdout）
	Output string

	// 错误输出（stderr）
	ErrorOutput string

	// 执行器名称
	ExecutorName string

	// 执行器特定的元数据
	Metadata map[string]any
}

// NewExecutionRequest 创建执行请求
func NewExecutionRequest(step *StepInfo, job *JobInfo, pipeline *PipelineInfo) *ExecutionRequest {
	return &ExecutionRequest{
		Step:     step,
		Job:      job,
		Pipeline: pipeline,
		Env:      make(map[string]string),
		Options: &ExecutionOptions{
			Extra: make(map[string]any),
		},
	}
}

// NewExecutionResult 创建执行结果
func NewExecutionResult(executorName string) *ExecutionResult {
	return &ExecutionResult{
		ExecutorName: executorName,
		StartTime:    time.Now(),
		Metadata:     make(map[string]any),
	}
}

// Complete 完成执行结果
func (r *ExecutionResult) Complete(success bool, exitCode int32, err error) {
	r.EndTime = time.Now()
	r.Duration = r.EndTime.Sub(r.StartTime)
	r.Success = success
	r.ExitCode = exitCode
	if err != nil {
		r.Error = err.Error()
	}
}

// WithOutput 设置输出
func (r *ExecutionResult) WithOutput(output, errorOutput string) *ExecutionResult {
	r.Output = output
	r.ErrorOutput = errorOutput
	return r
}

// WithMetadata 设置元数据
func (r *ExecutionResult) WithMetadata(key string, value any) *ExecutionResult {
	if r.Metadata == nil {
		r.Metadata = make(map[string]any)
	}
	r.Metadata[key] = value
	return r
}

// ParseTimeout 解析超时字符串，无效或空时使用 defaultSec 作为默认秒数。
func ParseTimeout(raw string, defaultSec int) time.Duration {
	if strings.TrimSpace(raw) != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			return d
		}
	}
	if defaultSec > 0 {
		return time.Duration(defaultSec) * time.Second
	}
	return 0
}

// BuildCommandFromStepArgs 从 step.Args 中提取 shell 命令（run / script / command / commands）。
// 供 ShellExecutor 及需要从 StepInfo 取命令的调用方使用。
func BuildCommandFromStepArgs(step *StepInfo) string {
	if step == nil || step.Args == nil {
		return ""
	}
	for _, key := range []string{"run", "script", "command"} {
		if value, ok := step.Args[key]; ok {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	if value, ok := step.Args["commands"]; ok {
		if list, ok := value.([]any); ok {
			parts := make([]string, 0, len(list))
			for i := range list {
				if text, ok := list[i].(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	return ""
}
