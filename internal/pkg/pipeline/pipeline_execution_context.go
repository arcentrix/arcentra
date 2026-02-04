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

	"github.com/arcentrix/arcade/internal/pkg/pipeline/builtin"
	"github.com/arcentrix/arcade/internal/pkg/pipeline/spec"
	"github.com/arcentrix/arcade/pkg/log"
	"github.com/arcentrix/arcade/pkg/plugin"
	"github.com/expr-lang/expr"
)

// ExecutionContext provides execution context for pipeline
type ExecutionContext struct {
	Pipeline       *spec.Pipeline
	WorkspaceRoot  string
	PluginManager  *plugin.Manager
	BuiltinManager *builtin.Manager
	AgentManager   *AgentManager
	Logger         log.Logger
	Env            map[string]string
}

// NewExecutionContext creates a new execution context
func NewExecutionContext(
	p *spec.Pipeline,
	pluginMgr *plugin.Manager,
	workspace string,
	logger log.Logger,
) *ExecutionContext {
	env := make(map[string]string)
	if p != nil && p.Variables != nil {
		maps.Copy(env, p.Variables)
	}

	return &ExecutionContext{
		Pipeline:       p,
		PluginManager:  pluginMgr,
		BuiltinManager: builtin.NewManager(logger),
		WorkspaceRoot:  workspace,
		Logger:         logger,
		Env:            env,
	}
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
	_ = os.MkdirAll(p, 0755)
	return p
}

// StepWorkspace returns workspace path for step
func (c *ExecutionContext) StepWorkspace(jobName, stepName string) string {
	p := filepath.Join(c.JobWorkspace(jobName), stepName)
	_ = os.MkdirAll(p, 0755)
	return p
}

// LogJob logs job message
func (c *ExecutionContext) LogJob(job, msg string) {
	if c.Logger.Log != nil {
		c.Logger.Log.Infof("[job:%s] %s", job, msg)
	}
}

// LogStep logs step message
func (c *ExecutionContext) LogStep(job, step, msg string) {
	if c.Logger.Log != nil {
		c.Logger.Log.Infof("[job:%s][step:%s] %s", job, step, msg)
	}
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
func (c *ExecutionContext) EvalConditionWithContext(conditionExpr string, context map[string]any) (bool, error) {
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
	maps.Copy(env, context)

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
func (c *ExecutionContext) SendNotification(ctx context.Context, item *spec.NotifyItem, success bool) error {
	if item == nil || item.Plugin == "" {
		return nil
	}

	pluginClient, err := c.PluginManager.GetPlugin(item.Plugin)
	if err != nil {
		return fmt.Errorf("notify plugin not found: %s: %w", item.Plugin, err)
	}

	// Resolve params with variables
	resolvedParams := c.ResolveVariables(item.Params)

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
