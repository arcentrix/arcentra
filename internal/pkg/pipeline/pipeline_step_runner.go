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
	"time"

	"github.com/arcentrix/arcade/internal/pkg/pipeline/builtin"
	"github.com/arcentrix/arcade/internal/pkg/pipeline/spec"
	"github.com/bytedance/sonic"
)

// StepRunner runs a single step
type StepRunner struct {
	ctx  *ExecutionContext
	job  *spec.Job
	step *spec.Step
}

// NewStepRunner creates a new step runner
func NewStepRunner(ctx *ExecutionContext, job *spec.Job, step *spec.Step) *StepRunner {
	return &StepRunner{ctx: ctx, job: job, step: step}
}

// Run executes the step
func (r *StepRunner) Run(ctx context.Context) error {
	r.ctx.LogStep(r.job.Name, r.step.Name, "starting step")

	// Evaluate when condition with step context
	stepContext := map[string]any{
		"job": map[string]any{
			"name": r.job.Name,
		},
		"step": map[string]any{
			"name": r.step.Name,
			"uses": r.step.Uses,
		},
	}
	if ok, err := r.ctx.EvalConditionWithContext(r.step.When, stepContext); err != nil {
		return fmt.Errorf("evaluate when condition: %w", err)
	} else if !ok {
		r.ctx.LogStep(r.job.Name, r.step.Name, "skipped (when condition false)")
		return nil
	}

	// Apply step timeout if specified
	if r.step.Timeout != "" {
		timeout, err := time.ParseDuration(r.step.Timeout)
		if err != nil {
			return fmt.Errorf("invalid timeout format: %w", err)
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Determine retry attempts
	retries := 1
	if r.job.Retry != nil && r.job.Retry.MaxAttempts > 0 {
		retries = r.job.Retry.MaxAttempts
	}

	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		if attempt > 1 {
			r.ctx.LogStep(r.job.Name, r.step.Name, fmt.Sprintf("retry attempt %d/%d", attempt, retries))
			if r.job.Retry != nil && r.job.Retry.Delay != "" {
				delay, err := time.ParseDuration(r.job.Retry.Delay)
				if err == nil {
					time.Sleep(delay)
				}
			}
		}

		lastErr = r.execute(ctx)
		if lastErr == nil {
			r.ctx.LogStep(r.job.Name, r.step.Name, "completed successfully")
			return nil
		}

		r.ctx.LogStep(r.job.Name, r.step.Name, fmt.Sprintf("attempt %d failed: %v", attempt, lastErr))
	}

	// Handle continue_on_error
	if lastErr != nil && r.step.ContinueOnError {
		r.ctx.LogStep(r.job.Name, r.step.Name, "failed but continueOnError=true")
		return nil
	}

	return fmt.Errorf("step failed after %d attempts: %w", retries, lastErr)
}

// execute executes the step action
func (r *StepRunner) execute(ctx context.Context) error {
	// Check if step should run on agent
	if r.step.RunOnAgent && r.ctx.AgentManager != nil {
		return r.executeOnAgent(ctx)
	}

	// Execute locally using plugin
	return r.executeLocally(ctx)
}

// executeOnAgent executes step on a remote agent
func (r *StepRunner) executeOnAgent(ctx context.Context) error {
	// Get pipeline and build ID from context
	pipelineID := r.ctx.Pipeline.Namespace // Use namespace as pipeline ID
	buildID := ""                          // TODO: Get build ID from context

	// Find step index in job
	stepIndex := -1
	for i := range r.job.Steps {
		if r.job.Steps[i].Name == r.step.Name {
			stepIndex = i
			break
		}
	}
	if stepIndex == -1 {
		return fmt.Errorf("step %s not found in job %s", r.step.Name, r.job.Name)
	}

	// Resolve environment variables
	env := r.ctx.ResolveStepEnv(r.job, r.step)

	// Get workspace path
	workspace := r.ctx.StepWorkspace(r.job.Name, r.step.Name)

	// Create execution request
	req := &StepExecutionRequest{
		PipelineID: pipelineID,
		BuildID:    buildID,
		JobName:    r.job.Name,
		StepName:   r.step.Name,
		Step:       r.step,
		StepIndex:  stepIndex,
		Workspace:  workspace,
		Env:        env,
		Selector:   r.step.AgentSelector,
		Context:    r.ctx,
	}

	// Execute on agent
	result, err := r.ctx.AgentManager.ExecuteStepOnAgent(ctx, req)
	if err != nil {
		return fmt.Errorf("execute on agent: %w", err)
	}

	if !result.Success {
		return fmt.Errorf("step execution failed on agent: exit code %d, error: %s", result.ExitCode, result.Error)
	}

	return nil
}

// executeLocally executes step locally using plugin or builtin
func (r *StepRunner) executeLocally(ctx context.Context) error {
	// Check if it's a builtin function
	if r.ctx.BuiltinManager != nil {
		builtin, isBuiltin := r.ctx.BuiltinManager.IsBuiltin(r.step.Uses)
		if isBuiltin {
			return r.executeBuiltin(ctx, builtin)
		}
	}

	// Otherwise, treat as plugin
	return r.executePlugin(ctx)
}

// executeBuiltin executes a builtin function
func (r *StepRunner) executeBuiltin(ctx context.Context, builtinName string) error {
	// Determine action (default to first available action if not specified)
	action := r.step.Action
	if action == "" {
		// Try to get default action from builtin info
		info, err := r.ctx.BuiltinManager.GetInfo(builtinName)
		if err == nil && len(info.Actions) > 0 {
			action = info.Actions[0]
		} else {
			action = "Execute"
		}
	}

	// Resolve environment variables
	env := r.ctx.ResolveStepEnv(r.job, r.step)

	// Resolve params with variable substitution
	resolvedParams := r.ctx.ResolveVariables(r.step.Args)

	// Prepare params JSON
	paramsJSON, err := sonic.Marshal(resolvedParams)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	// Prepare builtin options
	opts := &builtin.Options{
		Workspace:        r.ctx.StepWorkspace(r.job.Name, r.step.Name),
		Env:              env,
		Job:              r.job,
		Step:             r.step,
		ExecutionContext: r.ctx,
	}

	// Execute builtin
	_, err = r.ctx.BuiltinManager.Execute(ctx, builtinName, action, paramsJSON, opts)
	if err != nil {
		return fmt.Errorf("builtin execution failed: %w", err)
	}

	return nil
}

// executePlugin executes a plugin
func (r *StepRunner) executePlugin(ctx context.Context) error {
	if r.ctx.PluginManager == nil {
		return fmt.Errorf("plugin manager is not available")
	}

	pluginClient, err := r.ctx.PluginManager.GetPlugin(r.step.Uses)
	if err != nil {
		return fmt.Errorf("plugin not found: %s: %w", r.step.Uses, err)
	}

	// Determine action (default to "Execute" if not specified)
	action := r.step.Action
	if action == "" {
		action = "Execute"
	}

	// Resolve environment variables
	env := r.ctx.ResolveStepEnv(r.job, r.step)

	// Resolve params with variable substitution
	resolvedParams := r.ctx.ResolveVariables(r.step.Args)

	// Prepare params JSON
	paramsJSON, err := json.Marshal(resolvedParams)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	// Prepare opts JSON (workspace and env)
	opts := map[string]any{
		"workspace": r.ctx.StepWorkspace(r.job.Name, r.step.Name),
		"env":       env,
	}
	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return fmt.Errorf("marshal opts: %w", err)
	}

	// Call plugin method
	// Note: ctx is kept for future use (e.g., timeout control)
	_ = ctx
	_, err = pluginClient.Execute(action, paramsJSON, optsJSON)
	if err != nil {
		return fmt.Errorf("plugin execution failed: %w", err)
	}

	return nil
}
