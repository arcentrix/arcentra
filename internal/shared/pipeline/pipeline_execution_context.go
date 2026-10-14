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

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/arcentrix/arcentra/internal/shared/executor"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/builtin"
	"github.com/arcentrix/arcentra/internal/shared/pipeline/spec"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/nova"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	"github.com/expr-lang/expr"
)

// IPauseChecker allows the execution process to check whether the current
// pipeline run is paused, without importing the process package.
type IPauseChecker interface {
	IsPaused() bool
	WaitIfPaused(ctx context.Context) error
}

// ExecutionContext provides execution context for pipeline
type ExecutionContext struct {
	Pipeline       *spec.Pipeline
	WorkspaceRoot  string
	PluginManager  *plugin.Manager
	BuiltinManager *builtin.Manager
	AgentManager   *AgentManager
	EventEmitter   *executor.EventEmitter
	TaskQueue      nova.TaskQueue
	Logger         log.Logger
	Env            map[string]string

	// JobRunStore is set by the control-plane process to let TaskFramework
	// persist JobRun / StepRun records and poll their status.
	JobRunStore any // process.IJobRunStore (avoid circular import)

	// RunCoordinator exposes pause/resume checking to TaskFramework.
	RunCoordinator IPauseChecker

	// PipelineRunID and PipelineIDRef are set by RunCoordinator so that
	// TaskFramework can associate DB records with the current run.
	PipelineRunID string
	PipelineIDRef string
	// TriggeredBy 保存当前流水线触发者，所有重试继承原始审计身份
	TriggeredBy string
	// RunDeployment 是控制面注入的发布桥接，缺省不可用
	RunDeployment DeploymentStepRunner

	// ArtifactURIs stores upstream job artifact URIs keyed by "jobName/artifactName".
	// Populated after each Job completes; downstream jobs read from here.
	ArtifactURIs       map[string]string
	ArtifactReferences map[string]taskqueue.ArtifactReference
	// RecordLocalArtifacts 在本地上传成功后持久化独立制品及元数据
	RecordLocalArtifacts func(context.Context, string, string, string, []string, string) error
	// CheckLocalArtifacts 在作业间复制前校验登记内容与过期状态
	CheckLocalArtifacts func(context.Context, string, []string) error
	artifactMu          *sync.RWMutex

	// SelectAgent 根据作业选择可用 Agent
	SelectAgent func(context.Context, *spec.Job) (string, error)
	// Runtime 保存已验证的不可变运行时快照，不从步骤参数派生
	Runtime *taskqueue.RuntimeRequest
	// RuntimePolicy 保存操作者策略快照，默认禁用
	RuntimePolicy taskqueue.OfflineRuntimePolicy
	// CancelJob 通知远程 Agent 停止作业
	CancelJob func(context.Context, string, string, string) error
	// StepRunIDs 保存当前作业的步骤执行标识
	StepRunIDs map[string]string
	// StepAttemptIDs 保存控制面预分配的不可变尝试标识
	StepAttemptIDs map[string][]string
	JobRunID       string

	// LogPublisher publishes build log messages to the BUILD_LOGS topic.
	// Set by the control-plane process for local step log visibility.
	LogPublisher executor.LogPublisher
}

// NewExecutionContext creates a new execution context
func NewExecutionContext(
	p *spec.Pipeline,
	pluginMgr *plugin.Manager,
	workspace string,
	logger log.Logger,
) *ExecutionContext {
	env := make(map[string]string)
	if p != nil && p.Runtime != nil {
		maps.Copy(env, p.Runtime.Env)
	}
	if p != nil && p.Variables != nil {
		maps.Copy(env, p.Variables)
	}

	return &ExecutionContext{
		Pipeline:           p,
		PluginManager:      pluginMgr,
		BuiltinManager:     builtin.NewManager(logger),
		WorkspaceRoot:      workspace,
		EventEmitter:       nil,
		Logger:             logger,
		Env:                env,
		ArtifactURIs:       make(map[string]string),
		ArtifactReferences: make(map[string]taskqueue.ArtifactReference),
		artifactMu:         &sync.RWMutex{},
	}
}

// SetEventEmitter sets the event emitter for pipeline events.
func (c *ExecutionContext) SetEventEmitter(emitter *executor.EventEmitter) {
	c.EventEmitter = emitter
}

// SetTaskQueue sets the task queue for pipeline execution.
func (c *ExecutionContext) SetTaskQueue(queue nova.TaskQueue) {
	c.TaskQueue = queue
}

// GetPipeline returns the pipeline (implements builtin.ExecutionContext interface)
func (c *ExecutionContext) GetPipeline() *spec.Pipeline {
	return c.Pipeline
}

// GetWorkspaceRoot returns the workspace root (implements builtin.ExecutionContext interface)
func (c *ExecutionContext) GetWorkspaceRoot() string {
	return c.WorkspaceRoot
}

// JobWorkspace returns workspace path for job
func (c *ExecutionContext) JobWorkspace(jobName string) string {
	p := filepath.Join(c.WorkspaceRoot, c.Pipeline.Namespace, jobName)
	_ = os.MkdirAll(p, 0o755)
	return p
}

// StepWorkspace returns workspace path for step
func (c *ExecutionContext) StepWorkspace(jobName, stepName string) string {
	p := c.JobWorkspace(jobName)
	_ = stepName // 同一个作业内所有步骤共享检出的代码与构建产物
	_ = os.MkdirAll(p, 0o755)
	return p
}

// LogJob logs job message
func (c *ExecutionContext) LogJob(job, msg string) {
	c.Logger.Infof("[job:%s] %s", job, msg)
}

// LogStep logs step message
func (c *ExecutionContext) LogStep(job, step, msg string) {
	c.Logger.Infof("[job:%s][step:%s] %s", job, step, msg)
}

// EvalCondition evaluates when condition expression using expr-lang/expr
// Supports expressions like: branch == "main", env.ENV == "prod", count > 10, etc.
func (c *ExecutionContext) EvalCondition(conditionExpr string) (bool, error) {
	conditionExpr = strings.TrimSpace(conditionExpr)
	if conditionExpr == "" {
		return true, nil
	}

	// Prepare environment for expression evaluation
	env := make(map[string]any)
	for k, v := range c.Env {
		env[k] = v
	}

	// Add env map for accessing environment variables
	env["env"] = c.Env

	// Compile expression
	program, err := expr.Compile(conditionExpr, expr.Env(env))
	if err != nil {
		return false, fmt.Errorf("compile condition expression: %w", err)
	}

	// Run expression
	result, err := expr.Run(program, env)
	if err != nil {
		return false, fmt.Errorf("evaluate condition expression: %w", err)
	}

	// Convert result to bool
	if boolResult, ok := result.(bool); ok {
		return boolResult, nil
	}

	return false, fmt.Errorf("condition expression must return bool, got %T", result)
}

// EvalConditionWithContext evaluates condition with additional context
// This allows expressions to access job/step specific information
func (c *ExecutionContext) EvalConditionWithContext(conditionExpr string, evalContext map[string]any) (bool, error) {
	conditionExpr = strings.TrimSpace(conditionExpr)
	if conditionExpr == "" {
		return true, nil
	}

	// Prepare environment for expression evaluation
	env := make(map[string]any)

	// Add all environment variables directly
	for k, v := range c.Env {
		env[k] = v
	}

	// Add env map for accessing environment variables
	env["env"] = c.Env

	// Add variables map for accessing pipeline variables
	if c.Pipeline != nil && c.Pipeline.Variables != nil {
		env["variables"] = c.Pipeline.Variables
	}

	// Add additional context (e.g., job, step information)
	maps.Copy(env, evalContext)

	// Compile expression
	program, err := expr.Compile(conditionExpr, expr.Env(env))
	if err != nil {
		return false, fmt.Errorf("compile condition expression '%s': %w", conditionExpr, err)
	}

	// Run expression
	result, err := expr.Run(program, env)
	if err != nil {
		return false, fmt.Errorf("evaluate condition expression '%s': %w", conditionExpr, err)
	}

	// Convert result to bool
	if boolResult, ok := result.(bool); ok {
		return boolResult, nil
	}

	return false, fmt.Errorf("condition expression must return bool, got %T: %v", result, result)
}

// ResolveStepEnv resolves environment variables for step
// Priority: step.Env > job.Env > pipeline.Variables
func (c *ExecutionContext) ResolveStepEnv(job *spec.Job, step *spec.Step) map[string]string {
	env := make(map[string]string)

	// Start with pipeline variables
	maps.Copy(env, c.Env)

	// Override with job env
	if job != nil && job.Env != nil {
		for k, v := range job.Env {
			env[k] = c.ResolveVariable(v)
		}
	}

	// Override with step env
	if step != nil && step.Env != nil {
		for k, v := range step.Env {
			env[k] = c.ResolveVariable(v)
		}
	}

	return env
}

// ResolveVariable resolves variable substitution: ${{ variable }}
var varRegex = regexp.MustCompile(`\${{?\s*(\w+(?:\.\w+)?)\s*}}?`)

func (c *ExecutionContext) ResolveVariable(value string) string {
	return varRegex.ReplaceAllStringFunc(value, func(match string) string {
		submatch := varRegex.FindStringSubmatch(match)
		if len(submatch) == 2 {
			key := submatch[1]
			if val, ok := c.Env[key]; ok {
				return val
			}
		}
		return match
	})
}

// ResolveVariables resolves variables in a map recursively
func (c *ExecutionContext) ResolveVariables(params map[string]any) map[string]any {
	resolved := make(map[string]any)
	for k, v := range params {
		resolved[k] = c.resolveValue(v)
	}
	return resolved
}

func (c *ExecutionContext) resolveValue(v any) any {
	switch val := v.(type) {
	case string:
		return c.ResolveVariable(val)
	case map[string]any:
		resolved := make(map[string]any)
		for k, v := range val {
			resolved[k] = c.resolveValue(v)
		}
		return resolved
	case []any:
		resolved := make([]any, len(val))
		for i, v := range val {
			resolved[i] = c.resolveValue(v)
		}
		return resolved
	default:
		return v
	}
}

// MarshalParams marshals params to JSON
func (c *ExecutionContext) MarshalParams(params map[string]any) (json.RawMessage, error) {
	return json.Marshal(params)
}

// SendNotification sends notification using notify plugin
func (c *ExecutionContext) SendNotification(_ context.Context, item *spec.NotifyItem, success bool) error {
	if item == nil || item.Plugin == "" {
		return nil
	}

	pluginClient, err := c.PluginManager.GetPlugin(item.Plugin)
	if err != nil {
		return fmt.Errorf("notify plugin not found: %s: %w", item.Plugin, err)
	}

	// Resolve params with variables
	resolvedParams := c.ResolveVariables(spec.StructAsMap(item.Params))

	// Add success/failure context
	resolvedParams["success"] = success

	paramsJSON, err := json.Marshal(resolvedParams)
	if err != nil {
		return fmt.Errorf("marshal notification params: %w", err)
	}

	// Determine action (default to "Send" if not specified)
	action := item.Action
	if action == "" {
		action = "Send"
	}

	_, err = pluginClient.Execute(action, paramsJSON, nil)
	return err
}

// ForJob 为并行作业创建隔离环境，同时共享受锁保护的产物索引
func (c *ExecutionContext) ForJob() *ExecutionContext {
	child := *c
	child.Env = maps.Clone(c.Env)
	child.RuntimePolicy = c.RuntimePolicy.Clone()
	if c.Runtime != nil {
		runtime := *c.Runtime
		child.Runtime = &runtime
	}
	child.StepRunIDs = make(map[string]string)
	child.StepAttemptIDs = make(map[string][]string)
	return &child
}

// ArtifactSnapshot 返回可安全遍历的产物快照
func (c *ExecutionContext) ArtifactSnapshot() map[string]string {
	c.artifactMu.RLock()
	defer c.artifactMu.RUnlock()
	return maps.Clone(c.ArtifactURIs)
}

// RecordArtifact 记录产物供后续作业使用
func (c *ExecutionContext) RecordArtifact(key, uri string, reference ...taskqueue.ArtifactReference) {
	c.artifactMu.Lock()
	defer c.artifactMu.Unlock()
	if c.ArtifactURIs == nil {
		c.ArtifactURIs = make(map[string]string)
	}
	c.ArtifactURIs[key] = uri
	if len(reference) > 0 {
		if c.ArtifactReferences == nil {
			c.ArtifactReferences = make(map[string]taskqueue.ArtifactReference)
		}
		c.ArtifactReferences[key] = cloneArtifactReference(reference[0])
	}
}

// GetPipelineRunID 返回当前流水线执行标识
func (c *ExecutionContext) GetPipelineRunID() string { return c.PipelineRunID }

// ArtifactReferenceSnapshot 返回制品位置绑定的并发安全快照
func (c *ExecutionContext) ArtifactReferenceSnapshot() map[string]taskqueue.ArtifactReference {
	c.artifactMu.RLock()
	defer c.artifactMu.RUnlock()
	if c.ArtifactReferences == nil {
		return nil
	}
	snapshot := make(map[string]taskqueue.ArtifactReference, len(c.ArtifactReferences))
	for key, ref := range c.ArtifactReferences {
		snapshot[key] = cloneArtifactReference(ref)
	}
	return snapshot
}

// ValidateLocalArtifactRead 检查本地制品下载的登记及有效期
func (c *ExecutionContext) ValidateLocalArtifactRead(ctx context.Context, job string, names []string) error {
	if c.CheckLocalArtifacts == nil {
		return nil
	}
	return c.CheckLocalArtifacts(ctx, job, names)
}

func cloneArtifactReference(ref taskqueue.ArtifactReference) taskqueue.ArtifactReference {
	if ref.Size != nil {
		size := *ref.Size
		ref.Size = &size
	}
	return ref
}

// ArtifactInputSnapshot 在同一锁内返回位置与完整性元数据的独立快照
func (c *ExecutionContext) ArtifactInputSnapshot() (map[string]string, map[string]taskqueue.ArtifactReference) {
	c.artifactMu.RLock()
	defer c.artifactMu.RUnlock()
	uris := maps.Clone(c.ArtifactURIs)
	var refs map[string]taskqueue.ArtifactReference
	if c.ArtifactReferences != nil {
		refs = make(map[string]taskqueue.ArtifactReference, len(c.ArtifactReferences))
		for key, ref := range c.ArtifactReferences {
			refs[key] = cloneArtifactReference(ref)
		}
	}
	return uris, refs
}
