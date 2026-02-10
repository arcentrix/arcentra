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
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/internal/pkg/pipeline/spec"
)

// JobRunner runs a single job
type JobRunner struct {
	ctx *ExecutionContext
	job *spec.Job
}

// NewJobRunner creates a new job runner
func NewJobRunner(ctx *ExecutionContext, job *spec.Job) *JobRunner {
	return &JobRunner{ctx: ctx, job: job}
}

// Run executes the job
func (r *JobRunner) Run(ctx context.Context) error {
	r.ctx.LogJob(r.job.Name, fmt.Sprintf("starting job: %s", r.job.Description))

	// Evaluate when condition with job context
	jobContext := map[string]any{
		"job": map[string]any{
			"name":        r.job.Name,
			"description": r.job.Description,
		},
	}
	if ok, err := r.ctx.EvalConditionWithContext(r.job.When, jobContext); err != nil {
		return fmt.Errorf("evaluate when condition: %w", err)
	} else if !ok {
		r.ctx.LogJob(r.job.Name, "skipped due to when condition")
		return nil
	}

	// Apply job timeout if specified
	if r.job.Timeout != "" {
		timeout, err := time.ParseDuration(r.job.Timeout)
		if err != nil {
			return fmt.Errorf("invalid timeout format: %w", err)
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Handle source if specified
	if r.job.Source != nil {
		if err := r.handleSource(ctx); err != nil {
			return fmt.Errorf("handle source: %w", err)
		}
	}

	// Handle approval if required
	if r.job.Approval != nil && r.job.Approval.Required {
		if err := r.handleApproval(ctx); err != nil {
			return fmt.Errorf("approval failed: %w", err)
		}
	}

	// Execute steps sequentially
	for i := range r.job.Steps {
		step := &r.job.Steps[i]
		sr := NewStepRunner(r.ctx, r.job, step)
		if err := sr.Run(ctx); err != nil {
			// Handle failure notification
			if r.job.Notify != nil && r.job.Notify.OnFailure != nil {
				_ = r.ctx.SendNotification(ctx, r.job.Notify.OnFailure, false)
			}
			return fmt.Errorf("step %s failed: %w", step.Name, err)
		}
	}

	// Handle target deployment if specified
	if r.job.Target != nil {
		if err := r.handleTarget(ctx); err != nil {
			return fmt.Errorf("handle target: %w", err)
		}
	}

	// Send success notification
	if r.job.Notify != nil && r.job.Notify.OnSuccess != nil {
		if err := r.ctx.SendNotification(ctx, r.job.Notify.OnSuccess, true); err != nil {
			r.ctx.LogJob(r.job.Name, fmt.Sprintf("failed to send success notification: %v", err))
		}
	}

	r.ctx.LogJob(r.job.Name, "completed successfully")
	return nil
}

// handleSource handles source checkout/clone
func (r *JobRunner) handleSource(_ context.Context) error {
	if r.job.Source.Type == "" {
		return fmt.Errorf("source type is required")
	}

	// Use source plugin to check out / clone
	pluginClient, err := r.ctx.PluginManager.GetPlugin(r.job.Source.Type)
	if err != nil {
		return fmt.Errorf("source plugin not found: %s: %w", r.job.Source.Type, err)
	}

	// Prepare source params
	params := map[string]any{
		"repo":   r.job.Source.Repo,
		"branch": r.job.Source.Branch,
	}
	if r.job.Source.Auth != nil {
		params["auth"] = r.job.Source.Auth
	}

	paramsJSON, err := r.ctx.MarshalParams(params)
	if err != nil {
		return fmt.Errorf("marshal source params: %w", err)
	}
	optsJSON, err := r.ctx.MarshalParams(map[string]any{
		"workspace": r.ctx.JobWorkspace(r.job.Name),
	})
	if err != nil {
		return fmt.Errorf("marshal source opts: %w", err)
	}

	// Call clone action
	_, err = pluginClient.Execute("clone", paramsJSON, optsJSON)
	return err
}

// handleApproval handles approval workflow
func (r *JobRunner) handleApproval(_ context.Context) error {
	if r.job.Approval.Plugin == "" {
		return fmt.Errorf("approval plugin is required")
	}

	pluginClient, err := r.ctx.PluginManager.GetPlugin(r.job.Approval.Plugin)
	if err != nil {
		return fmt.Errorf("approval plugin not found: %s: %w", r.job.Approval.Plugin, err)
	}

	r.ctx.LogJob(r.job.Name, "waiting for approval...")

	paramsJSON, err := r.ctx.MarshalParams(r.job.Approval.Params)
	if err != nil {
		return fmt.Errorf("marshal approval params: %w", err)
	}
	_, err = pluginClient.Execute("approval.create", paramsJSON, nil)
	if err != nil {
		return err
	}

	// Poll for approval status
	// TODO: Implement polling logic
	return nil
}

// handleTarget handles deployment target
func (r *JobRunner) handleTarget(_ context.Context) error {
	if r.job.Target.Type == "" {
		return fmt.Errorf("target type is required")
	}

	pluginClient, err := r.ctx.PluginManager.GetPlugin(r.job.Target.Type)
	if err != nil {
		return fmt.Errorf("target plugin not found: %s: %w", r.job.Target.Type, err)
	}

	paramsJSON, err := r.ctx.MarshalParams(r.job.Target.Config)
	if err != nil {
		return fmt.Errorf("marshal target params: %w", err)
	}
	optsJSON, err := r.ctx.MarshalParams(map[string]any{
		"workspace": r.ctx.JobWorkspace(r.job.Name),
	})
	if err != nil {
		return fmt.Errorf("marshal target opts: %w", err)
	}

	_, err = pluginClient.Execute("deploy", paramsJSON, optsJSON)
	return err
}
